<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, ApiError } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { useI18n } from '@/i18n'

/**
 * 可复用终端面板：一次性 ticket 换 WebSocket，xterm + fit addon。
 *
 * 两处使用：服务器列表行内展开（ServerRow）与详情页「终端」tab。组件只认
 * serverId / allowed，不依赖列表上下文；切走即卸载、断开 ws。
 *
 * 握手/控制帧与旧界面 web/src/components/server/TerminalTab.tsx 保持一致：
 *   - 先 POST /servers/{id}/terminal-ticket 拿 60s 单次 ticket；
 *   - 连 /api/v1/servers/{id}/terminal?ticket=...（后端只挂了这个路径，见 router.go）；
 *   - 打开后发 {type:'resize',cols,rows}，输入发 {type:'input',data}，尺寸变化重发 resize；
 *   - 下行 JSON type=output|data 写屏、error 红字、exit 结束；二进制帧是 PTY 原始
 *     字节，原样交给 xterm 自己流式解码（跨帧 UTF-8 不会碎成 U+FFFD），只在首帧
 *     为识别旧控制端的纯文本拒绝而临时解码一次（见 handleBinaryFrame）。
 * 连接失败/断开都给出可读文案与「重新连接」入口，不留白屏。
 *
 * 「已连接」的判据是**服务端首帧**而不是 ws.onopen：后端 handlers_terminal.go 没有
 * 独立的 ready 帧，接受会话时直接转发 PTY 输出（成功路径第一帧是 output/data 或二进制）。
 * 拒绝路径有三条：新后端先发 {type:'error',message}（agent 拒绝）或 {type:'error',data}
 * （控制端自己拒绝，见 handlers_terminal.go），旧后端把「错误：…」「已有终端会话…」
 * 当纯文本发过来，且**可能落在 BinaryMessage 里**（旧控制端一路 BinaryMessage 写浏览器）。
 * 三条都必须落「已断开」+ 可读文案 + 重连入口，绝不能被当成首帧就置「已连接」。
 * 但拒绝判定只对**首帧（state 仍为 connecting）**生效：已经「已连接」后程序正常输出
 * 以「错误：」开头的文本不是拒绝，必须继续写屏（v2 的全帧前缀判定是 P0 回归）。
 * onopen 只代表 TCP/WS 握手完成，收到真正的数据/就绪帧才置「已连接」。
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

/**
 * 纯文本拒绝的兜底前缀（兼容 u1 之前的旧后端）。
 *
 * 旧链路里 agent 的拒绝只走 term.data 纯文本，「已有终端会话在运行」这类原因
 * 会被当成普通 PTY 输出写屏，界面于是停在「连接中」/误判「已连接」。新链路
 * 先发结构化的 {type:'error',message}，纯文本仍作为兼容兜底保留，所以两条都要认。
 * 旧控制端还可能把它塞进 BinaryMessage，二进制分支解码后同样过这里。
 */
const TEXT_FAILURE_PREFIXES = ['错误：', '错误:', '已有终端会话', 'Error:', 'error:']

/**
 * 二进制帧的解码策略：正常 PTY 数据**不在前端解码**，直接把 Uint8Array 交给 xterm，
 * 由它自带的流式解码器跨帧拼 UTF-8（agent 按 4096 字节切、浏览器帧更小，逐帧非流式
 * 解码会把中文劈成 U+FFFD）。只有「首帧且仍在连接中」才临时解码一次做拒绝判定，
 * 该解码器一次性创建、用完即丢，不跨帧复用（避免残留的半个字符）。
 */
function decodeProbe(bytes: Uint8Array): string {
  return new TextDecoder().decode(bytes)
}

/** 判断一段「非 JSON」文本是否是拒绝原因；是则返回可展示文案，否则 null。 */
function textFailureMessage(text: string): string | null {
  const trimmed = text.trim()
  if (!trimmed) return null
  return TEXT_FAILURE_PREFIXES.some((prefix) => trimmed.startsWith(prefix)) ? trimmed : null
}

/**
 * 从一个 error 帧里取可展示文案。
 * 控制端自己拒绝（agent 离线等）用 data（见 handlers_terminal.go），agent 原样
 * 转发的结构化拒绝用 message（见 pkg/protocol ErrorFrame），两者都认。
 */
function errorFrameMessage(frame: { data?: unknown; message?: unknown; error?: unknown }, fallback: string): string {
  for (const value of [frame.data, frame.message, frame.error]) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return fallback
}

/**
 * 一帧服务端下行的统一判定（文本帧与「首帧解码出的二进制帧」共用，保证两条链路行为一致）。
 *
 * 顺序即优先级，判定只认「JSON 结构」与「帧首前缀」，不做内容包含匹配：
 *   1. JSON 且 type=error          → 拒绝：回「已断开」、关 socket，绝不置「已连接」；
 *   2. JSON 且 hello/hello_ack/ready → 就绪兜底（后端目前不发），置「已连接」但不写屏；
 *   3. 非 JSON 且**首帧**命中拒绝前缀 → 旧后端纯文本拒绝（含二进制承载），同上转断开；
 *   4. 其余                          → 正常 PTY 数据（中文/控制字符都可能），置「已连接」并写屏。
 *
 * 第 3 条必须带 first：已连接后程序正常输出「错误：这是正常输出」只是 PTY 数据，
 * 不能当拒绝（v2 的全帧前缀判定是 P0 回归，轨迹 连接中→已连接→已断开）。
 * bytes 只在二进制帧传入：非结构化数据直接写原始字节，交给 xterm 流式解码。
 */
function handleServerFrame(text: string, bytes?: Uint8Array): void {
  const first = state.value === 'connecting'
  let frame: { type?: string; data?: unknown; message?: unknown; error?: unknown } | null = null
  try {
    const parsed: unknown = JSON.parse(text)
    if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
      frame = parsed as { type?: string; data?: unknown; message?: unknown; error?: unknown }
    }
  } catch {
    // 非 JSON：可能是正常 PTY 文本，也可能是旧后端的纯文本拒绝，下面统一按前缀判定。
  }

  if (frame?.type === 'error') {
    // 拒绝会话：绝不置「已连接」，回「已断开」并保留重连入口。
    markDisconnected(errorFrameMessage(frame, t('terminal.connectFailed')))
    socket?.close()
    return
  }
  // 后端目前不发显式就绪帧，这里只做兜底：若将来补发 hello/ready 类帧，
  // 视为会话已接受且不把原始 JSON 写进屏幕（常量见 pkg/protocol/protocol.go）。
  if (frame?.type === 'hello' || frame?.type === 'hello_ack' || frame?.type === 'ready') {
    if (first) markConnected()
    return
  }
  if (!frame && first) {
    // 拒绝前缀只认首帧：已连接后同样的文本是程序正常输出，照常写屏（P0 回归）。
    const failure = textFailureMessage(text)
    if (failure) {
      markDisconnected(failure)
      socket?.close()
      return
    }
  }
  if (first) markConnected()
  if (frame?.type === 'output' || frame?.type === 'data') {
    write(typeof frame.data === 'string' ? frame.data : '')
  } else if (frame?.type === 'exit') {
    state.value = 'closed'
    write(`\r\n\x1b[90m${t('terminal.sessionClosed')}\x1b[0m\r\n`)
  } else if (bytes) {
    // 正常 PTY 数据：原始字节交给 xterm 的流式 UTF-8 解码，跨帧中文不会变 U+FFFD。
    term?.write(bytes)
  } else {
    write(text)
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
      // 握手成功 ≠ 服务端接受了终端会话：保持「连接中」，等第一帧就绪信号。
      fit()
      send({ type: 'resize', cols: active.cols, rows: active.rows })
    }

    ws.onmessage = (event) => {
      if (current !== session) return
      if (typeof event.data === 'string') {
        handleServerFrame(event.data)
      } else if (event.data instanceof ArrayBuffer) {
        const bytes = new Uint8Array(event.data)
        // 二进制帧的两种身份：
        //   - 已连接：必定是 PTY 原始字节，直接给 xterm 流式解码（不做任何判定），
        //     前端逐帧 decode 会把跨帧中文劈成 U+FFFD（P0 回归 2）；
        //   - 仍在连接中：可能是正常数据，也可能是旧控制端降级下发的纯文本拒绝，
        //     此时才临时解码一次（解码器用完即丢）走首帧判定。
        if (state.value === 'connecting') handleServerFrame(decodeProbe(bytes), bytes)
        else term?.write(bytes)
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
