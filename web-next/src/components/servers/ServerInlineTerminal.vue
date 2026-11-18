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
      state.value = 'connected'
      fit()
      send({ type: 'resize', cols: active.cols, rows: active.rows })
      active.focus()
    }

    ws.onmessage = (event) => {
      if (current !== session) return
      if (typeof event.data === 'string') {
        try {
          const message = JSON.parse(event.data) as { type?: string; data?: string }
          if (message.type === 'output' || message.type === 'data') write(message.data ?? '')
          else if (message.type === 'error') {
            write(`\r\n\x1b[31m${message.data ?? ''}\x1b[0m\r\n`)
          } else if (message.type === 'exit') {
            state.value = 'closed'
            write(`\r\n\x1b[90m${t('terminal.sessionClosed')}\x1b[0m\r\n`)
          } else write(event.data)
        } catch {
          write(event.data)
        }
      } else if (event.data instanceof ArrayBuffer) {
        write(new TextDecoder().decode(event.data))
      }
    }

    ws.onerror = () => {
      if (current !== session) return
      state.value = 'error'
      error.value = t('terminal.connectFailed')
      write(`\r\n\x1b[31m${t('terminal.connectFailed')}\x1b[0m\r\n`)
    }

    ws.onclose = () => {
      if (current !== session) return
      if (state.value !== 'error') state.value = 'closed'
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
