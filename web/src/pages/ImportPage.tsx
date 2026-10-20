import { useMemo, useState, type FormEvent } from 'react'
import { api, type Credential, type ImportServersResult } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { cn } from '../lib/cn'
import { Button } from '../components/Button'
import { EmptyState } from '../components/EmptyState'
import { PlusIcon } from '../components/Icons'
import { PageHeader } from '../components/PageHeader'
import { useToast } from '../components/Toast'
import { Link } from 'react-router-dom'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

interface ParsedLine {
  host: string
  port: number
  note?: string
  duplicated?: boolean
}

function parseText(text: string, defaultPort: number): ParsedLine[] {
  const seen = new Set<string>()
  const lines: ParsedLine[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '' || line.startsWith('#')) continue
    const match = /^([^\s:]+)(?::(\d{1,5}))?$/.exec(line)
    if (!match) {
      lines.push({ host: line, port: defaultPort, note: 'invalid' })
      continue
    }
    const port = match[2] ? Number(match[2]) : defaultPort
    const key = `${match[1]}:${port}`
    const duplicated = seen.has(key)
    seen.add(key)
    lines.push({ host: match[1], port, duplicated, note: duplicated ? 'duplicate' : undefined })
  }
  return lines
}

export default function ImportPage() {
  const { t } = useI18n()
  const { toast } = useToast()
  const credentialsQuery = usePolling(api.credentials, 30_000)
  const credentials = credentialsQuery.data ?? []

  const [text, setText] = useState('')
  const [defaultPort, setDefaultPort] = useState('22')
  const [credentialId, setCredentialId] = useState('')
  const [group, setGroup] = useState('')
  const [tags, setTags] = useState('')
  const [mode, setMode] = useState<'agent' | 'ssh'>('ssh')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<ImportServersResult | null>(null)

  const parsed = useMemo(() => parseText(text, Number(defaultPort) || 22), [text, defaultPort])
  const validCount = parsed.filter((line) => !line.duplicated && line.note !== 'invalid').length

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (validCount === 0) {
      setError(t('import.errorEmpty'))
      return
    }
    if (!credentialId) {
      setError(t('import.errorCredential'))
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const response = await api.importServers({
        text,
        default_port: Number(defaultPort) || 22,
        credential_id: credentialId,
        group: group.trim() || undefined,
        tags: tags
          .split(',')
          .map((tag) => tag.trim())
          .filter(Boolean),
        mode,
      })
      setResult(response)
      toast(t('import.done', { created: response.created.length, failed: response.failed.length }), 'success')
    } catch (caught) {
      setError(errorMessage(caught, t('common.error')))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader title={t('import.title')} description={t('import.description')} />

      <form onSubmit={handleSubmit} className="grid gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <label htmlFor="import-text" className={labelClass}>
            {t('import.pasteLabel')}
          </label>
          <textarea
            id="import-text"
            value={text}
            onChange={(event) => {
              setText(event.target.value)
              setResult(null)
            }}
            rows={10}
            spellCheck={false}
            placeholder={t('s.1035')}
            className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
          />
          <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">{t('import.pasteHint')}</p>

          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <div>
              <label htmlFor="import-port" className={labelClass}>
                {t('import.defaultPort')}
              </label>
              <input
                id="import-port"
                value={defaultPort}
                onChange={(event) => setDefaultPort(event.target.value)}
                className={inputClass + ' mt-1.5'}
                inputMode="numeric"
              />
            </div>
            <div>
              <label htmlFor="import-mode" className={labelClass}>
                {t('import.mode')}
              </label>
              <select
                id="import-mode"
                value={mode}
                onChange={(event) => setMode(event.target.value as 'agent' | 'ssh')}
                className={inputClass + ' mt-1.5'}
              >
                <option value="ssh">SSH</option>
                <option value="agent">Agent</option>
              </select>
            </div>
            <div>
              <label htmlFor="import-credential" className={labelClass}>
                {t('import.credential')}
              </label>
              <select
                id="import-credential"
                value={credentialId}
                onChange={(event) => setCredentialId(event.target.value)}
                className={inputClass + ' mt-1.5'}
              >
                <option value="">{t('import.selectCredential')}</option>
                {credentials.map((credential: Credential) => (
                  <option key={credential.id} value={credential.id}>
                    {credential.name}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="import-group" className={labelClass}>
                {t('import.group')}
              </label>
              <input
                id="import-group"
                value={group}
                onChange={(event) => setGroup(event.target.value)}
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <div className="sm:col-span-2">
              <label htmlFor="import-tags" className={labelClass}>
                {t('import.tags')}
              </label>
              <input
                id="import-tags"
                value={tags}
                onChange={(event) => setTags(event.target.value)}
                placeholder="prod, hk"
                className={inputClass + ' mt-1.5'}
              />
            </div>
          </div>

          {error ? (
            <p className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {error}
            </p>
          ) : null}

          <div className="mt-3 flex items-center justify-between gap-2">
            <span className="text-xs text-slate-500 dark:text-slate-400">
              {t('import.parsed', { valid: validCount, total: parsed.length })}
            </span>
            <Button type="submit" loading={submitting} icon={<PlusIcon className="h-4 w-4" />}>
              {t('import.submit')}
            </Button>
          </div>
        </section>

        <section className="space-y-4">
          <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
              {t('import.preview')}
            </h2>
            {parsed.length === 0 ? (
              <EmptyState
                icon={<PlusIcon className="h-6 w-6" />}
                title={t('import.previewEmptyTitle')}
                description={t('import.previewEmptyDescription')}
              />
            ) : (
              <div className="mt-2 max-h-64 overflow-auto rounded-md border border-slate-100 dark:border-slate-800">
                <table className="w-full min-w-[360px] border-collapse text-xs">
                  <thead>
                    <tr className="border-b border-slate-200 bg-slate-50 text-left text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                      <th className="px-2.5 py-1.5 font-medium">{t('import.host')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('import.port')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('import.status')}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                    {parsed.map((line, index) => (
                      <tr key={`${line.host}:${line.port}:${index}`}>
                        <td className="px-2.5 py-1.5 font-mono text-slate-700 dark:text-slate-200">
                          {line.host}
                        </td>
                        <td className="px-2.5 py-1.5 tabular-nums text-slate-500 dark:text-slate-400">
                          {line.port}
                        </td>
                        <td
                          className={cn(
                            'px-2.5 py-1.5',
                            line.note
                              ? 'text-amber-600 dark:text-amber-400'
                              : 'text-emerald-600 dark:text-emerald-400',
                          )}
                        >
                          {line.note === 'duplicate'
                            ? t('import.duplicate')
                            : line.note === 'invalid'
                              ? t('import.invalid')
                              : t('import.ok')}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          {result ? (
            <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
              <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
                {t('import.result')}
              </h2>
              <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
                {t('import.resultSummary', {
                  total: result.total,
                  created: result.created.length,
                  failed: result.failed.length,
                })}
              </p>
              <ul className="mt-2 space-y-1 text-xs">
                {result.created.map((item) => (
                  <li key={item.host} className="flex items-center gap-2 text-emerald-600 dark:text-emerald-400">
                    <span>✓</span>
                    <span className="font-mono">{item.host}</span>
                    <Link
                      to={`/servers/${item.server_id}`}
                      className="text-indigo-600 hover:underline dark:text-indigo-400"
                    >
                      {t('import.openServer')}
                    </Link>
                  </li>
                ))}
                {result.failed.map((item) => (
                  <li key={item.host} className="text-rose-600 dark:text-rose-400">
                    ✗ <span className="font-mono">{item.host}</span> — {item.error}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </section>
      </form>
    </div>
  )
}
