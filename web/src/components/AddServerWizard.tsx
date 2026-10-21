import { useEffect, useState, type FormEvent } from 'react'
import {
  api,
  type ServerGroup,
  resolveControlUrl,
  type Credential,
  type EnrollToken,
  type GroupSummary,
  type Server,
} from '../lib/api'
import { errorMessage } from '../lib/errors'
import { formatDateTime } from '../lib/format'
import { Button } from './Button'
import { CopyButton } from './CopyButton'
import { CredentialDialog } from './CredentialDialog'
import { InlineLoading } from './Loading'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { KeyIcon, ServerIcon } from './Icons'
import { SearchMultiSelect } from './SearchMultiSelect'

import { t } from '../i18n'
type WizardMode = 'agent' | 'ssh'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const TTL_OPTIONS = [
  { value: 3_600, label: t('s.0001') },
  { value: 86_400, label: t('s.0002') },
  { value: 604_800, label: t('s.0003') },
]

const HOST_KEY_POLICIES = [
  { value: 'accept-new', label: t('s.0004'), hint: t('s.0005') },
  { value: 'strict', label: 'strict', hint: t('s.0006') },
  { value: 'insecure', label: 'insecure', hint: t('s.0007') },
]

const AGENT_STEPS = [
  { title: t('s.0008'), description: t('s.0009') },
  { title: t('s.0010'), description: t('s.0011') },
  { title: t('s.0012'), description: t('s.0013') },
]

interface AddServerWizardProps {
  open: boolean
  onClose: () => void
  /** 已产生变更（生成令牌 / 新增服务器）时通知列表刷新。 */
  onFinished: () => void
}

/**
 * 添加服务器向导：先选接入方式（Agent 一键接入 / SSH 免安装），再进入对应流程。
 */
export function AddServerWizard({ open, onClose, onFinished }: AddServerWizardProps) {
  const { toast } = useToast()
  const [mode, setMode] = useState<WizardMode | null>(null)
  const [changed, setChanged] = useState(false)

  // Agent 流程
  const [note, setNote] = useState('')
  const [ttl, setTtl] = useState(86_400)
  const [creating, setCreating] = useState(false)
  const [token, setToken] = useState<EnrollToken | null>(null)
  const [command, setCommand] = useState<string | null>(null)
  const [commandLoading, setCommandLoading] = useState(false)
  const [commandError, setCommandError] = useState<string | null>(null)
  const [agentError, setAgentError] = useState<string | null>(null)

  // SSH 流程
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [groups, setGroups] = useState<GroupSummary[]>([])
  const [sshName, setSshName] = useState('')
  const [sshHost, setSshHost] = useState('')
  const [sshPort, setSshPort] = useState('22')
  const [defaultSshPort, setDefaultSshPort] = useState(22)
  const [sshCredentialId, setSshCredentialId] = useState('')
  const [sshPolicy, setSshPolicy] = useState('accept-new')
  const [sshGroup, setSshGroup] = useState('')
  const [sshGroupIds, setSshGroupIds] = useState<string[]>([])
  const [serverGroups, setServerGroups] = useState<ServerGroup[]>([])
  const [sshTags, setSshTags] = useState('')
  const [sshSaving, setSshSaving] = useState(false)
  const [sshError, setSshError] = useState<string | null>(null)
  const [createdServer, setCreatedServer] = useState<Server | null>(null)
  const [credentialDialogOpen, setCredentialDialogOpen] = useState(false)

  useEffect(() => {
    if (!open) return
    setMode(null)
    setChanged(false)
    setNote('')
    setTtl(86_400)
    setCreating(false)
    setToken(null)
    setCommand(null)
    setCommandLoading(false)
    setCommandError(null)
    setAgentError(null)
    setSshName('')
    setSshHost('')
    setSshPort(String(defaultSshPort))
    setSshCredentialId('')
    setSshPolicy('accept-new')
    setSshGroup('')
    setSshGroupIds([])
    setSshTags('')
    setSshSaving(false)
    setSshError(null)
    setCreatedServer(null)
    let cancelled = false
    api
      .network()
      .then((network) => {
        if (cancelled) return
        setDefaultSshPort(network.default_ssh_port)
        setSshPort((current) => (current === '' || current === '22' ? String(network.default_ssh_port) : current))
      })
      .catch(() => {
        /* 接口未就绪时保持 22 */
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  // 进入 SSH 流程时加载凭证与分组候选
  useEffect(() => {
    if (!open || mode !== 'ssh') return
    let cancelled = false
    Promise.allSettled([api.credentials(), api.groups(), api.serverGroups()]).then(
      ([credentialResult, groupResult, serverGroupResult]) => {
        if (cancelled) return
        if (credentialResult.status === 'fulfilled') {
          setCredentials(credentialResult.value)
          setSshCredentialId((current) => current || credentialResult.value[0]?.id || '')
        }
        if (groupResult.status === 'fulfilled') setGroups(groupResult.value)
        if (serverGroupResult.status === 'fulfilled') setServerGroups(serverGroupResult.value)
      },
    )
    return () => {
      cancelled = true
    }
  }, [open, mode])

  const handleClose = () => {
    if (changed) onFinished()
    onClose()
  }

  const loadInstallScript = async (plainToken: string) => {
    setCommandLoading(true)
    setCommandError(null)
    try {
      const script = await api.installScript({ token: plainToken, server_url: resolveControlUrl() })
      setCommand(script.command)
    } catch (caught) {
      setCommand(null)
      setCommandError(errorMessage(caught, t('s.0014')))
    } finally {
      setCommandLoading(false)
    }
  }

  const handleCreateToken = async () => {
    setCreating(true)
    setAgentError(null)
    try {
      const created = await api.createEnrollToken({
        note: note.trim() || t('s.0015'),
        ttl_s: ttl,
      })
      setToken(created)
      setChanged(true)
      if (created.token) {
        toast(t('s.0016'), 'success')
        await loadInstallScript(created.token)
      } else {
        setAgentError(t('s.0017'))
      }
    } catch (caught) {
      setAgentError(errorMessage(caught, t('s.0018')))
    } finally {
      setCreating(false)
    }
  }

  const handleCreateSshServer = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!sshName.trim() || !sshHost.trim()) {
      setSshError(t('s.0019'))
      return
    }
    if (!sshCredentialId) {
      setSshError(t('s.0020'))
      return
    }
    const port = Number(sshPort)
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      setSshError(t('s.0021'))
      return
    }
    setSshSaving(true)
    setSshError(null)
    try {
      const created = await api.createServer({
        name: sshName.trim(),
        host: sshHost.trim(),
        port,
        mode: 'ssh',
        credential_id: sshCredentialId,
        group: sshGroup.trim() || undefined,
        group_ids: sshGroupIds,
        tags: sshTags
          .split(',')
          .map((tag) => tag.trim())
          .filter(Boolean),
        host_key_policy: sshPolicy as 'insecure' | 'accept-new' | 'strict',
      })
      setChanged(true)
      setCreatedServer(created)
      toast(t('s.0022', { '0': created.name }), 'success')
    } catch (caught) {
      setSshError(errorMessage(caught, t('s.0023')))
    } finally {
      setSshSaving(false)
    }
  }

  const footer =
    mode === null ? (
      <Button variant="secondary" onClick={onClose}>
        {t('s.0024')}
      </Button>
    ) : mode === 'agent' ? (
      token ? (
        <>
          <Button
            variant="secondary"
            onClick={() => {
              setToken(null)
              setCommand(null)
              setCommandError(null)
              setAgentError(null)
            }}
          >
            {t('s.0025')}
          </Button>
          <Button onClick={handleClose}>{t('s.0026')}</Button>
        </>
      ) : (
        <>
          <Button variant="secondary" onClick={() => setMode(null)} disabled={creating}>
            {t('s.0027')}
          </Button>
          <Button onClick={handleCreateToken} loading={creating}>
            {t('s.0028')}
          </Button>
        </>
      )
    ) : createdServer ? (
      <Button onClick={handleClose}>{t('s.0026')}</Button>
    ) : (
      <>
        <Button variant="secondary" onClick={() => setMode(null)} disabled={sshSaving}>
          {t('s.0027')}
        </Button>
        <Button type="submit" form="ssh-server-form" loading={sshSaving}>
          {t('s.0029')}
        </Button>
      </>
    )

  return (
    <>
      <Modal
        open={open}
        onClose={handleClose}
        title={t('s.0029')}
        description={
          mode === null
            ? t('s.0030')
            : mode === 'agent'
              ? t('s.0031')
              : t('s.0032')
        }
        size="lg"
        footer={footer}
      >
        {mode === null ? (
          <div className="grid gap-3 sm:grid-cols-2">
            <button
              type="button"
              onClick={() => setMode('agent')}
              className="rounded-xl border border-slate-200 p-4 text-left transition-colors hover:border-indigo-400 hover:bg-indigo-50/40 dark:border-slate-700 dark:hover:border-indigo-500/60 dark:hover:bg-indigo-500/5"
            >
              <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-300">
                <ServerIcon className="h-5 w-5" />
              </span>
              <span className="mt-3 block text-sm font-semibold text-slate-900 dark:text-slate-100">
                {t('s.0033')}
              </span>
              <span className="mt-1 block text-xs leading-relaxed text-slate-500 dark:text-slate-400">
                {t('s.0034')}
              </span>
              <span className="mt-2 inline-block rounded bg-emerald-50 px-1.5 py-0.5 text-[11px] font-medium text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">
                {t('s.0035')}
              </span>
            </button>

            <button
              type="button"
              onClick={() => setMode('ssh')}
              className="rounded-xl border border-slate-200 p-4 text-left transition-colors hover:border-sky-400 hover:bg-sky-50/40 dark:border-slate-700 dark:hover:border-sky-500/60 dark:hover:bg-sky-500/5"
            >
              <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-sky-50 text-sky-600 dark:bg-sky-500/10 dark:text-sky-300">
                <KeyIcon className="h-5 w-5" />
              </span>
              <span className="mt-3 block text-sm font-semibold text-slate-900 dark:text-slate-100">
                {t('s.0036')}
              </span>
              <span className="mt-1 block text-xs leading-relaxed text-slate-500 dark:text-slate-400">
                {t('s.0037')}
              </span>
              <span className="mt-2 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-[11px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                {t('s.0038')}
              </span>
            </button>
          </div>
        ) : mode === 'agent' ? (
          token === null ? (
            <div className="space-y-5">
              <ol className="grid gap-3 sm:grid-cols-3">
                {AGENT_STEPS.map((step, index) => (
                  <li
                    key={step.title}
                    className="rounded-lg border border-slate-200 bg-slate-50/60 p-3 dark:border-slate-800 dark:bg-slate-950/40"
                  >
                    <span className="flex h-5 w-5 items-center justify-center rounded-full bg-indigo-600 text-[11px] font-semibold text-white">
                      {index + 1}
                    </span>
                    <p className="mt-2 text-sm font-medium text-slate-800 dark:text-slate-200">
                      {step.title}
                    </p>
                    <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
                      {step.description}
                    </p>
                  </li>
                ))}
              </ol>

              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <label htmlFor="enroll-note" className={labelClass}>
                    {t('s.0039')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
                  </label>
                  <input
                    id="enroll-note"
                    value={note}
                    onChange={(event) => setNote(event.target.value)}
                    maxLength={64}
                    placeholder={t('s.0041')}
                    className={inputClass + ' mt-1.5'}
                  />
                </div>
                <div>
                  <label htmlFor="enroll-ttl" className={labelClass}>
                    {t('s.0042')}
                  </label>
                  <select
                    id="enroll-ttl"
                    value={ttl}
                    onChange={(event) => setTtl(Number(event.target.value))}
                    className={inputClass + ' mt-1.5'}
                  >
                    {TTL_OPTIONS.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </div>
              </div>

              <p className="text-xs text-slate-400 dark:text-slate-500">
                {t('s.0043')}
              </p>

              {agentError ? (
                <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                  {agentError}
                </p>
              ) : null}
            </div>
          ) : (
            <div className="space-y-4">
              <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-3 dark:border-emerald-500/30 dark:bg-emerald-500/10">
                <p className="text-sm font-medium text-emerald-800 dark:text-emerald-200">
                  {t('s.0044')}
                </p>
                <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
                  {t('s.0045')} {formatDateTime(token.expires_at)} {t('s.0046')}
                </p>
              </div>

              {token.token ? (
                <div>
                  <p className="text-sm font-medium text-slate-700 dark:text-slate-200">{t('s.0047')}</p>
                  <div className="mt-1.5 flex items-start gap-2">
                    <code className="min-w-0 flex-1 break-all rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs text-slate-600 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-300">
                      {token.token}
                    </code>
                    <CopyButton value={token.token} label={t('s.0048')} />
                  </div>
                </div>
              ) : null}

              <div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-sm font-medium text-slate-700 dark:text-slate-200">
                    {t('s.0049')}
                    <span className="ml-1.5 font-normal text-slate-400 dark:text-slate-500">
                      {t('s.0050')}
                    </span>
                  </p>
                  {command ? <CopyButton value={command} label={t('s.0051')} /> : null}
                </div>

                {commandLoading ? (
                  <div className="mt-1.5 rounded-md border border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-950">
                    <InlineLoading label={t('s.0052')} />
                  </div>
                ) : command ? (
                  <pre className="mt-1.5 overflow-x-auto rounded-md border border-slate-200 bg-slate-50 px-3 py-2.5 font-mono text-xs leading-relaxed text-slate-700 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-300">
                    <code className="whitespace-pre-wrap break-all">{command}</code>
                  </pre>
                ) : null}

                {commandError ? (
                  <div className="mt-1.5 flex flex-wrap items-center justify-between gap-2 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                    <span>{commandError}</span>
                    {token.token ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => void loadInstallScript(token.token!)}
                      >
                        {t('s.0053')}
                      </Button>
                    ) : null}
                  </div>
                ) : null}
              </div>

              <div className="rounded-md border border-slate-200 bg-slate-50 px-3.5 py-3 text-xs leading-relaxed text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                <p>
                  {t('s.0054')}{' '}
                  <code className="rounded bg-white px-1 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-900 dark:text-slate-300">
                    {resolveControlUrl()}
                  </code>
                  {t('s.0055')}
                </p>
                <p className="mt-1">{t('s.0056')}</p>
              </div>
            </div>
          )
        ) : createdServer ? (
          <div className="space-y-4">
            <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-3 dark:border-emerald-500/30 dark:bg-emerald-500/10">
              <p className="text-sm font-medium text-emerald-800 dark:text-emerald-200">
                {t('s.0057')}{createdServer.name}{t('s.0058')}
              </p>
              <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
                {t('s.0059')}
              </p>
            </div>
            <dl className="space-y-1.5 rounded-md border border-slate-200 bg-slate-50 px-3.5 py-3 text-xs dark:border-slate-800 dark:bg-slate-950/60">
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('s.0060')}</dt>
                <dd className="font-mono text-slate-700 dark:text-slate-200">
                  {createdServer.host}:{createdServer.port ?? defaultSshPort}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('s.0061')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">{sshPolicy}</dd>
              </div>
              {createdServer.group ? (
                <div className="flex justify-between gap-3">
                  <dt className="text-slate-500 dark:text-slate-400">{t('s.0062')}</dt>
                  <dd className="text-slate-700 dark:text-slate-200">{createdServer.group}</dd>
                </div>
              ) : null}
            </dl>
            <p className="text-xs text-slate-400 dark:text-slate-500">
              {t('s.0063')}
            </p>
          </div>
        ) : (
          <form id="ssh-server-form" onSubmit={handleCreateSshServer} className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-name" className={labelClass}>
                  {t('s.0064')}
                </label>
                <input
                  id="ssh-name"
                  value={sshName}
                  onChange={(event) => setSshName(event.target.value)}
                  maxLength={64}
                  placeholder={t('s.0065')}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div>
                <label htmlFor="ssh-host" className={labelClass}>
                  {t('s.0066')}
                </label>
                <input
                  id="ssh-host"
                  value={sshHost}
                  onChange={(event) => setSshHost(event.target.value)}
                  placeholder={t('s.0067')}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div className="sm:col-span-2">
                <label className={labelClass}>{t('groups.authorizedGroups')}</label>
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('groups.authorizedGroupsHint')}
                </p>
                <div className="mt-1.5">
                  <SearchMultiSelect
                    options={serverGroups.map((group) => ({ id: group.id, label: group.name }))}
                    value={sshGroupIds}
                    onChange={setSshGroupIds}
                    placeholder={t('groups.searchGroups')}
                    emptyText={t('groups.noGroups')}
                  />
                </div>
              </div>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-port" className={labelClass}>
                  {t('s.0068')}
                </label>
                <input
                  id="ssh-port"
                  value={sshPort}
                  onChange={(event) => setSshPort(event.target.value)}
                  inputMode="numeric"
                  className={inputClass + ' mt-1.5'}
                />
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {t('network.defaultPortHint', { port: defaultSshPort })}
                </p>
              </div>
              <div>
                <label htmlFor="ssh-policy" className={labelClass}>
                  {t('s.0061')}
                </label>
                <select
                  id="ssh-policy"
                  value={sshPolicy}
                  onChange={(event) => setSshPolicy(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                >
                  {HOST_KEY_POLICIES.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
                <p className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  {HOST_KEY_POLICIES.find((option) => option.value === sshPolicy)?.hint}
                </p>
              </div>
            </div>

            <div>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <label htmlFor="ssh-credential" className={labelClass}>
                  {t('s.0069')}
                </label>
                <button
                  type="button"
                  onClick={() => setCredentialDialogOpen(true)}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  {t('s.0070')}
                </button>
              </div>
              {credentials.length === 0 ? (
                <p className="mt-1.5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
                  {t('s.0071')}
                </p>
              ) : (
                <select
                  id="ssh-credential"
                  value={sshCredentialId}
                  onChange={(event) => setSshCredentialId(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                >
                  {credentials.map((credential) => (
                    <option key={credential.id} value={credential.id}>
                      {credential.name}（{credential.kind === 'key' ? t('s.0072') : t('s.0073')} ·{' '}
                      {credential.username}）
                    </option>
                  ))}
                </select>
              )}
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-group" className={labelClass}>
                  {t('s.0062')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
                </label>
                <input
                  id="ssh-group"
                  value={sshGroup}
                  onChange={(event) => setSshGroup(event.target.value)}
                  list="ssh-group-options"
                  placeholder={t('s.0074')}
                  className={inputClass + ' mt-1.5'}
                />
                <datalist id="ssh-group-options">
                  {groups.map((group) => (
                    <option key={group.name} value={group.name} />
                  ))}
                </datalist>
              </div>
              <div>
                <label htmlFor="ssh-tags" className={labelClass}>
                  {t('s.0075')} <span className="font-normal text-slate-400">{t('s.0076')}</span>
                </label>
                <input
                  id="ssh-tags"
                  value={sshTags}
                  onChange={(event) => setSshTags(event.target.value)}
                  placeholder={t('s.0077')}
                  className={inputClass + ' mt-1.5'}
                />
              </div>
            </div>

            {sshError ? (
              <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                {sshError}
              </p>
            ) : null}
          </form>
        )}
      </Modal>

      <CredentialDialog
        open={credentialDialogOpen}
        onClose={() => setCredentialDialogOpen(false)}
        onCreated={(credential) => {
          setCredentials((current) => [...current, credential])
          setSshCredentialId(credential.id)
        }}
      />
    </>
  )
}
