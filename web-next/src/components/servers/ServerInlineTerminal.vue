<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, ApiError } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { useI18n } from '@/i18n'

/**
 * 列表行内嵌终端：一次性 ticket 换 WebSocket，xterm + fit addon。
 *
 * 握手/控制帧与旧界面 web/src/components/server/TerminalTab.tsx 保持一致：
 *   - 先 POST /servers/{id}/terminal-ticket 拿 60s 单次 ticket；
 *   - 连 /api/v1/servers/{id}/terminal?ticket=...（后端只挂了这个路径，见 router.go）；
 *   - 打开后发 {type:'resize',cols,rows}，输入发 {type:'input',data}，尺寸变化重发 resize；
 *   - 下行 JSON type=output|data 写屏、error 红字、exit 结束，ArrayBuffer 直接解码。
 * 连接失败/断开都给出可读文案与「重新连接」入口，不留白屏。
 *
 * 「已连接」的判据是**服务端首帧**而不是 ws.onopen：后端 handlers_terminal.go 没有
 * 独立的 ready 帧，接受会话时直接转发 PTY 输出（成功路径第一帧是 output/data 或二进制），
 * 拒绝开 session 时先发 {type:'error',data}（见 openTerminalSession 失败分支）。因此
 * onopen 只代表 TCP/WS 握手完成，此时仍停在「连接中」，收到第一帧（非 error）才置「已连接」，
 * error 帧/onerror/onclose 一律回「已断开」并保留重试入口。
 */
const props = defineProps<{
  serverId: string
  /** 当前会话是否有 terminal.use；缺权限时两个面板都只显示占位 */
  allowed: boolean
}>()

type ConnState = 'idle' | 'connecting' | 'connected' | 'closed' | 'error' | 'disabled'

const { t } = useI18n()

const container = ref<HTMLDivElement | null>(null)
const state = ref<ConnState>('idle')
const error = ref<string | null>(null)

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let socket: WebSocket | null = null
let observer: ResizeObserver | null = null
let dataListener: { dispose: () => void } | null = null
let resizeListener: { dispose: () => void } | null = null
let session = 0

function statusLabelKey(value: ConnState): string {
  return `terminal.status.${value}`
}

function cssToken(name: string, fallback: string): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value || fallback
}

function cssPx(name: string, fallback: number): number {
  const value = Number.parseFloat(cssToken(name, ''))
  return Number.isFinite(value) && value > 0 ? value : fallback
}

function send(frame: Record<string, unknown>): void {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(frame))
}

function write(text: string): void {
  term?.write(text)
}

function fit(): void {
  try {
    fitAddon?.fit()
  } catch {
    /* 容器暂时隐藏时 fit 会抛错，下一次 ResizeObserver 会补 */
  }
}

/** 服务端首帧到达：会话确实被接受，这时才算「已连接」。 */
function markConnected(): void {
  state.value = 'connected'
  fit()
  term?.focus()
}

/** 拒绝帧 / onerror / onclose 的统一收口：回「已断开」，给出可读文案与重试入口。 */
function markDisconnected(message: string): void {
  state.value = 'closed'
  error.value = message
  write(`\r\n\x1b[31m${message}\x1b[0m\r\n`)
}

function markDisabled(caught: unknown): void {
  const message = errorMessage(caught, t('terminal.connectFailed'))
  error.value = message
  const forbidden = caught instanceof ApiError && caught.status === 403
  state.value = forbidden || /disabled|not enabled|未开启/i.test(message) ? 'disabled' : 'error'
  write(`\r\n\x1b[31m${message}\x1b[0m\r\n`)
}

async function connect(): Promise<void> {
  if (!term) return
  const current = ++session
  socket?.close()
  socket = null
  state.value = 'connecting'
  error.value = null
  term.reset()
  write(`\x1b[90m${t('terminal.connecting')}\x1b[0m\r\n`)

  try {
    const ticket = await api.createTerminalTicket(props.serverId)
    if (current !== session || !term) return
    const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${protocol}://${window.location.host}/api/v1/servers/${encodeURIComponent(
      props.serverId,
    )}/terminal?ticket=${encodeURIComponent(ticket.ticket)}`

    const active = term
    const ws = new WebSocket(url)
    socket = ws
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      if (current !== session) return
      // 握手成功 ≠ 服务端接受了终端会话：保持「连接中」，等第一帧就绪信号。
      fit()
      send({ type: 'resize', cols: active.cols, rows: active.rows })
    }

    ws.onmessage = (event) => {
      if (current !== session) return
      const first = state.value === 'connecting'
      if (typeof event.data === 'string') {
        try {
          const message = JSON.parse(event.data) as { type?: string; data?: string }
          if (message.type === 'error') {
            markDisconnected(message.data || t('terminal.connectFailed'))
            return
          }
          // 后端目前不发显式就绪帧，这里只做兜底：若将来补发 hello/ready 类帧，
          // 视为会话已接受且不把原始 JSON 写进屏幕（常量见 pkg/protocol/protocol.go）。
          if (message.type === 'hello' || message.type === 'hello_ack' || message.type === 'ready') {
            if (first) markConnected()
            return
          }
          if (first) markConnected()
          if (message.type === 'output' || message.type === 'data') write(message.data ?? '')
          else if (message.type === 'exit') {
            state.value = 'closed'
            write(`\r\n\x1b[90m${t('terminal.sessionClosed')}\x1b[0m\r\n`)
          } else write(event.data)
        } catch {
          if (first) markConnected()
          write(event.data)
        }
      } else if (event.data instanceof ArrayBuffer) {
        if (first) markConnected()
        write(new TextDecoder().decode(event.data))
      }
    }

    ws.onerror = () => {
      if (current !== session) return
      markDisconnected(t('terminal.connectFailed'))
    }

    ws.onclose = () => {
      if (current !== session) return
      // 连接未就绪就被关掉（服务端拒绝或网络断了）：回「已断开」，可重试。
      if (state.value === 'connected') markDisconnected(t('terminal.disconnected'))
      else if (state.value === 'connecting') markDisconnected(t('terminal.connectFailed'))
    }
  } catch (caught) {
    if (current !== session) return
    markDisabled(caught)
  }
}

onMounted(() => {
  if (!props.allowed || !container.value) return

  term = new Terminal({
    cursorBlink: true,
    convertEol: true,
    fontFamily: cssToken('--ls-font-mono', 'SFMono-Regular, Menlo, Consolas, monospace'),
    fontSize: cssPx('--ls-terminal-font-size', 13),
    theme: {
      background: cssToken('--ls-text', '#0f172a'),
      foreground: cssToken('--ls-bg', '#f8fafc'),
      cursor: cssToken('--ls-brand-500', '#22c55e'),
    },
    scrollback: 5000,
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(container.value)
  fit()

  dataListener = term.onData((data) => send({ type: 'input', data }))
  resizeListener = term.onResize(({ cols, rows }) => send({ type: 'resize', cols, rows }))

  observer = new ResizeObserver(() => fit())
  observer.observe(container.value)

  void connect()
})

onBeforeUnmount(() => {
  session += 1
  observer?.disconnect()
  observer = null
  dataListener?.dispose()
  dataListener = null
  resizeListener?.dispose()
  resizeListener = null
  socket?.close()
  socket = null
  term?.dispose()
  term = null
  fitAddon = null
})
</script>

<template>
  <div v-if="!allowed" class="ls-srv-term ls-srv-term--blocked">
    <p class="ls-notice__title">{{ t('servers.noTerminalPermission') }}</p>
    <p class="ls-notice__text">{{ t('terminal.disabledHint') }}</p>
  </div>

  <div v-else class="ls-srv-term">
    <div class="ls-srv-term__bar">
      <span class="ls-srv-term__state" :class="`ls-srv-term__state--${state}`">
        {{ t(statusLabelKey(state)) }}
      </span>
      <button
        v-if="state === 'error' || state === 'closed'"
        class="ls-btn ls-btn--small"
        type="button"
        @click="connect"
      >
        {{ t('terminal.reconnect') }}
      </button>
    </div>

    <div v-if="error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('terminal.connectFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button class="ls-notice__action" type="button" @click="connect">
        {{ t('common.retry') }}
      </button>
    </div>
    <p v-else-if="state === 'disabled'" class="ls-notice__text">{{ t('terminal.disabledHint') }}</p>

    <div
      ref="container"
      class="ls-srv-term__screen"
      role="application"
      :aria-label="`${t('terminal.title')} · ${serverId}`"
    />
  </div>
</template>
