import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { CopyButton } from '../components/CopyButton'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { ServerIcon } from '../components/Icons'
import { useToast } from '../components/Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

export default function SetupPage() {
  const { t } = useI18n()
  const { toast } = useToast()
  const navigate = useNavigate()
  const infoQuery = usePolling(api.systemInfo, 30_000)

  const [mode, setMode] = useState<'agent' | 'ssh'>('agent')
  const [note, setNote] = useState('first-server')
  const [token, setToken] = useState<string | null>(null)
  const [command, setCommand] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [sshName, setSshName] = useState('')
  const [sshHost, setSshHost] = useState('')
  const [sshPort, setSshPort] = useState('22')
  const [sshSaving, setSshSaving] = useState(false)

  const finish = () => {
    try {
      window.localStorage.setItem('leadstack.setupDone', '1')
    } catch {
      /* ignore */
    }
    navigate('/', { replace: true })
  }

  const skip = () => finish()

  const handleCreateToken = async () => {
    setLoading(true)
    setError(null)
    try {
      const created = await api.createEnrollToken({ note: note.trim() || 'setup', ttl_s: 86_400 })
      setToken(created.token ?? null)
      if (created.token) {
        const script = await api.installScript({
          token: created.token,
          server_url: window.location.origin,
        })
        setCommand(script.command)
      }
    } catch (caught) {
      setError(errorMessage(caught, t('common.error')))
    } finally {
      setLoading(false)
    }
  }

  const handleCreateSsh = async () => {
    if (!sshName.trim() || !sshHost.trim()) {
      setError(t('setup.errorHost'))
      return
    }
    setSshSaving(true)
    setError(null)
    try {
      await api.createServer({
        name: sshName.trim(),
        host: sshHost.trim(),
        port: Number(sshPort) || 22,
        mode: 'ssh',
      })
      toast(t('setup.serverCreated'), 'success')
      finish()
    } catch (caught) {
      setError(errorMessage(caught, t('common.error')))
    } finally {
      setSshSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-5">
      <div className="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
          {t('setup.title')}
        </h1>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{t('setup.description')}</p>
        {infoQuery.data ? (
          <p className="mt-2 text-xs text-slate-400 dark:text-slate-500">
            {t('setup.version', { version: infoQuery.data.version })} ·{' '}
            {t('setup.serverCount', { count: infoQuery.data.server_count })}
          </p>
        ) : null}

        <div className="mt-4 grid grid-cols-2 gap-2">
          {(['agent', 'ssh'] as const).map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => setMode(option)}
              className={
                'rounded-lg border px-3 py-2 text-left text-sm font-medium transition-colors ' +
                (mode === option
                  ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300')
              }
            >
              {option === 'agent' ? t('setup.agentMode') : t('setup.sshMode')}
              <span className="mt-0.5 block text-xs font-normal text-slate-500 dark:text-slate-400">
                {option === 'agent' ? t('setup.agentModeHint') : t('setup.sshModeHint')}
              </span>
            </button>
          ))}
        </div>

        {mode === 'agent' ? (
          <div className="mt-4 space-y-3">
            <div>
              <label htmlFor="setup-note" className={labelClass}>
                {t('setup.note')}
              </label>
              <input
                id="setup-note"
                value={note}
                onChange={(event) => setNote(event.target.value)}
                className={inputClass + ' mt-1.5'}
              />
            </div>
            {!token ? (
              <Button onClick={() => void handleCreateToken()} loading={loading}>
                {t('setup.generateToken')}
              </Button>
            ) : (
              <div className="space-y-2">
                <p className="text-sm font-medium text-emerald-700 dark:text-emerald-300">
                  {t('setup.tokenCreated')}
                </p>
                {command ? (
                  <div className="flex items-start gap-2">
                    <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded-md border border-slate-200 bg-slate-50 p-3 font-mono text-xs dark:border-slate-800 dark:bg-slate-950">
                      {command}
                    </pre>
                    <CopyButton value={command} />
                  </div>
                ) : null}
                <Button onClick={finish}>{t('setup.done')}</Button>
              </div>
            )}
          </div>
        ) : (
          <div className="mt-4 space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <label htmlFor="setup-ssh-name" className={labelClass}>
                  {t('setup.serverName')}
                </label>
                <input
                  id="setup-ssh-name"
                  value={sshName}
                  onChange={(event) => setSshName(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div>
                <label htmlFor="setup-ssh-host" className={labelClass}>
                  {t('setup.host')}
                </label>
                <input
                  id="setup-ssh-host"
                  value={sshHost}
                  onChange={(event) => setSshHost(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div>
                <label htmlFor="setup-ssh-port" className={labelClass}>
                  {t('setup.port')}
                </label>
                <input
                  id="setup-ssh-port"
                  value={sshPort}
                  onChange={(event) => setSshPort(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
            </div>
            <p className="text-xs text-slate-400 dark:text-slate-500">{t('setup.sshCredentialHint')}</p>
            <Button onClick={() => void handleCreateSsh()} loading={sshSaving}>
              {t('setup.createServer')}
            </Button>
          </div>
        )}

        {error ? (
          <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}

        <div className="mt-5 flex items-center justify-between gap-2 border-t border-slate-100 pt-3 dark:border-slate-800">
          <Button variant="ghost" onClick={skip}>
            {t('setup.skip')}
          </Button>
          <Button variant="secondary" onClick={finish}>
            {t('setup.finishLater')}
          </Button>
        </div>
      </div>

      {infoQuery.data && !infoQuery.data.needs_setup ? (
        <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <EmptyState
            icon={<ServerIcon className="h-6 w-6" />}
            title={t('setup.notNeededTitle')}
            description={t('setup.notNeededDescription')}
            actionLabel={t('setup.goDashboard')}
            onAction={finish}
          />
        </div>
      ) : null}
    </div>
  )
}
