import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../../lib/api'
import { useI18n } from '../../i18n'
import { errorMessage } from '../../lib/errors'
import { cn } from '../../lib/cn'
import { getTheme } from '../../lib/theme'
import { Button } from '../Button'
import { TerminalIcon } from '../Icons'

type ConnState = 'idle' | 'connecting' | 'connected' | 'closed' | 'error' | 'disabled'

/** 服务器详情「终端」标签：一次性 ticket 换 WS，xterm.js + fit addon，resize 同步。 */
export function TerminalTab({ serverId }: { serverId: string }) {
  const { t } = useI18n()
  const containerRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const [state, setState] = useState<ConnState>('idle')
  const [error, setError] = useState<string | null>(null)
  const [sessionKey, setSessionKey] = useState(0)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    let disposed = false

    const term = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontFamily: 'SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 13,
      theme:
        getTheme() === 'dark'
          ? { background: '#020617', foreground: '#e2e8f0', cursor: '#a5b4fc' }
          : { background: '#0f172a', foreground: '#e2e8f0', cursor: '#c7d2fe' },
      scrollback: 5000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(container)
    fit.fit()
    termRef.current = term
    fitRef.current = fit

    const write = (text: string) => {
      if (!disposed) term.write(text)
    }

    const connect = async () => {
      setState('connecting')
      setError(null)
      write(`\x1b[90m${t('terminal.connecting')}\x1b[0m\r\n`)
      try {
        const ticket = await api.createTerminalTicket(serverId)
        if (disposed) return
        const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
        const url = `${protocol}://${window.location.host}/api/v1/servers/${encodeURIComponent(
          serverId,
        )}/terminal?ticket=${encodeURIComponent(ticket.ticket)}`
        const socket = new WebSocket(url)
        socket.binaryType = 'arraybuffer'
        wsRef.current = socket

        socket.onopen = () => {
          if (disposed) return
          setState('connected')
          fit.fit()
          socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
          term.focus()
        }
        socket.onmessage = (event) => {
          if (typeof event.data === 'string') {
            try {
              const message = JSON.parse(event.data) as { type?: string; data?: string }
              if (message.type === 'output' || message.type === 'data') write(message.data ?? '')
              else if (message.type === 'error') write(`\r\n\x1b[31m${message.data ?? ''}\x1b[0m\r\n`)
              else if (message.type === 'exit') {
                setState('closed')
                write(`\r\n\x1b[90m${t('terminal.sessionClosed')}\x1b[0m\r\n`)
              } else write(event.data)
            } catch {
              write(event.data)
            }
          } else if (event.data instanceof ArrayBuffer) {
            write(new TextDecoder().decode(event.data))
          }
        }
        socket.onerror = () => {
          if (disposed) return
          setState('error')
          setError(t('terminal.connectFailed'))
          write(`\r\n\x1b[31m${t('terminal.connectFailed')}\x1b[0m\r\n`)
        }
        socket.onclose = () => {
          if (disposed) return
          setState((current) => (current === 'error' ? current : 'closed'))
        }
      } catch (caught) {
        if (disposed) return
        const message = errorMessage(caught, t('terminal.connectFailed'))
        // 403：控制端未开启终端
        setError(message)
        setState(message.includes('403') || /disabled/i.test(message) ? 'disabled' : 'error')
        write(`\r\n\x1b[31m${message}\x1b[0m\r\n`)
      }
    }

    const dataListener = term.onData((data) => {
      const socket = wsRef.current
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'input', data }))
      }
    })

    const resizeListener = term.onResize(({ cols, rows }) => {
      const socket = wsRef.current
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    })

    const observer = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        /* ignore */
      }
    })
    observer.observe(container)

    void connect()

    return () => {
      disposed = true
      observer.disconnect()
      dataListener.dispose()
      resizeListener.dispose()
      wsRef.current?.close()
      wsRef.current = null
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverId, sessionKey])

  const statusLabel: Record<ConnState, string> = {
    idle: t('terminal.status.idle'),
    connecting: t('terminal.status.connecting'),
    connected: t('terminal.status.connected'),
    closed: t('terminal.status.closed'),
    error: t('terminal.status.error'),
    disabled: t('terminal.status.disabled'),
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center gap-2">
          <TerminalIcon className="h-4 w-4 text-indigo-500" />
          <span className="font-medium text-slate-700 dark:text-slate-200">{t('terminal.title')}</span>
          <span
            className={cn(
              'rounded-full px-2 py-0.5 font-medium',
              state === 'connected'
                ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
                : state === 'disabled' || state === 'error'
                  ? 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400',
            )}
          >
            {statusLabel[state]}
          </span>
        </div>
        <div className="flex items-center gap-2 text-slate-400 dark:text-slate-500">
          <span>{t('terminal.ticketHint')}</span>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => setSessionKey((key) => key + 1)}
            disabled={state === 'connecting'}
          >
            {t('terminal.reconnect')}
          </Button>
        </div>
      </div>

      {state === 'disabled' ? (
        <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          <p className="font-medium">{t('terminal.disabledTitle')}</p>
          <p className="mt-1 text-xs leading-relaxed">{t('terminal.disabledHint')}</p>
          {error ? <p className="mt-1 font-mono text-[11px] opacity-80">{error}</p> : null}
        </div>
      ) : null}

      {state === 'error' && error ? (
        <div className="rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          {error}
        </div>
      ) : null}

      <div
        ref={containerRef}
        className="h-[420px] overflow-hidden rounded-lg border border-slate-800 bg-slate-950 p-2"
      />
      <p className="text-xs text-slate-400 dark:text-slate-500">{t('terminal.footerHint')}</p>
    </div>
  )
}
