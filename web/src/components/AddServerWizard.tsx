import { useEffect, useState, type FormEvent } from 'react'
import {
  api,
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

type WizardMode = 'agent' | 'ssh'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

const TTL_OPTIONS = [
  { value: 3_600, label: '1 小时' },
  { value: 86_400, label: '24 小时' },
  { value: 604_800, label: '7 天' },
]

const HOST_KEY_POLICIES = [
  { value: 'accept-new', label: 'accept-new（推荐）', hint: '首次连接记录主机密钥，之后不一致则拒绝' },
  { value: 'strict', label: 'strict', hint: '只允许已记录过的主机密钥' },
  { value: 'insecure', label: 'insecure', hint: '不校验主机密钥，仅用于临时排障' },
]

const AGENT_STEPS = [
  { title: '生成令牌', description: '一次性使用，过期自动失效' },
  { title: '执行命令', description: '在目标服务器以 root 身份运行' },
  { title: '等待上线', description: '10 秒内自动出现在服务器列表' },
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
  const [sshCredentialId, setSshCredentialId] = useState('')
  const [sshPolicy, setSshPolicy] = useState('accept-new')
  const [sshGroup, setSshGroup] = useState('')
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
    setSshPort('22')
    setSshCredentialId('')
    setSshPolicy('accept-new')
    setSshGroup('')
    setSshTags('')
    setSshSaving(false)
    setSshError(null)
    setCreatedServer(null)
  }, [open])

  // 进入 SSH 流程时加载凭证与分组候选
  useEffect(() => {
    if (!open || mode !== 'ssh') return
    let cancelled = false
    Promise.allSettled([api.credentials(), api.groups()]).then(([credentialResult, groupResult]) => {
      if (cancelled) return
      if (credentialResult.status === 'fulfilled') {
        setCredentials(credentialResult.value)
        setSshCredentialId((current) => current || credentialResult.value[0]?.id || '')
      }
      if (groupResult.status === 'fulfilled') setGroups(groupResult.value)
    })
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
      setCommandError(errorMessage(caught, '安装命令生成失败，请重试'))
    } finally {
      setCommandLoading(false)
    }
  }

  const handleCreateToken = async () => {
    setCreating(true)
    setAgentError(null)
    try {
      const created = await api.createEnrollToken({
        note: note.trim() || '控制台接入',
        ttl_s: ttl,
      })
      setToken(created)
      setChanged(true)
      if (created.token) {
        toast('接入令牌已生成', 'success')
        await loadInstallScript(created.token)
      } else {
        setAgentError('控制端未返回令牌明文，无法生成安装命令，请重新生成')
      }
    } catch (caught) {
      setAgentError(errorMessage(caught, '接入令牌生成失败'))
    } finally {
      setCreating(false)
    }
  }

  const handleCreateSshServer = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!sshName.trim() || !sshHost.trim()) {
      setSshError('请填写服务器名称与主机地址')
      return
    }
    if (!sshCredentialId) {
      setSshError('请选择一份 SSH 凭证；没有凭证时请先新建')
      return
    }
    const port = Number(sshPort)
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      setSshError('端口需为 1-65535 之间的整数')
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
        tags: sshTags
          .split(',')
          .map((tag) => tag.trim())
          .filter(Boolean),
        host_key_policy: sshPolicy as 'insecure' | 'accept-new' | 'strict',
      })
      setChanged(true)
      setCreatedServer(created)
      toast(`服务器「${created.name}」已添加`, 'success')
    } catch (caught) {
      setSshError(errorMessage(caught, '添加服务器失败'))
    } finally {
      setSshSaving(false)
    }
  }

  const footer =
    mode === null ? (
      <Button variant="secondary" onClick={onClose}>
        取消
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
            重新生成
          </Button>
          <Button onClick={handleClose}>完成</Button>
        </>
      ) : (
        <>
          <Button variant="secondary" onClick={() => setMode(null)} disabled={creating}>
            上一步
          </Button>
          <Button onClick={handleCreateToken} loading={creating}>
            生成接入令牌
          </Button>
        </>
      )
    ) : createdServer ? (
      <Button onClick={handleClose}>完成</Button>
    ) : (
      <>
        <Button variant="secondary" onClick={() => setMode(null)} disabled={sshSaving}>
          上一步
        </Button>
        <Button type="submit" form="ssh-server-form" loading={sshSaving}>
          添加服务器
        </Button>
      </>
    )

  return (
    <>
      <Modal
        open={open}
        onClose={handleClose}
        title="添加服务器"
        description={
          mode === null
            ? '选择接入方式：Agent 常驻上报，或 SSH 免安装周期采集。'
            : mode === 'agent'
              ? '生成一次性接入令牌，在目标机器执行安装命令即可完成接入。'
              : '填写 SSH 连接信息，控制端将按周期免安装采集主机状态。'
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
                Agent 一键接入
              </span>
              <span className="mt-1 block text-xs leading-relaxed text-slate-500 dark:text-slate-400">
                在目标机器执行一行安装命令。心跳 20 秒、指标 15 秒，支持远程执行与实时日志（后续版本）。
              </span>
              <span className="mt-2 inline-block rounded bg-emerald-50 px-1.5 py-0.5 text-[11px] font-medium text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">
                推荐
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
                SSH 免安装
              </span>
              <span className="mt-1 block text-xs leading-relaxed text-slate-500 dark:text-slate-400">
                不装任何东西，控制端用凭证通过 SSH 周期采集主机信息与指标，适合托管环境。
              </span>
              <span className="mt-2 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-[11px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                免安装
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
                    备注 <span className="font-normal text-slate-400">（可选）</span>
                  </label>
                  <input
                    id="enroll-note"
                    value={note}
                    onChange={(event) => setNote(event.target.value)}
                    maxLength={64}
                    placeholder="例如：生产环境 / 东京节点"
                    className={inputClass + ' mt-1.5'}
                  />
                </div>
                <div>
                  <label htmlFor="enroll-ttl" className={labelClass}>
                    令牌有效期
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
                令牌仅用于首次接入，生成后请立即在目标服务器执行安装命令；令牌明文只展示一次。
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
                  接入令牌已生成（仅显示一次）
                </p>
                <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
                  有效期至 {formatDateTime(token.expires_at)} · 使用后自动失效
                </p>
              </div>

              {token.token ? (
                <div>
                  <p className="text-sm font-medium text-slate-700 dark:text-slate-200">接入令牌</p>
                  <div className="mt-1.5 flex items-start gap-2">
                    <code className="min-w-0 flex-1 break-all rounded-md border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs text-slate-600 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-300">
                      {token.token}
                    </code>
                    <CopyButton value={token.token} label="复制令牌" />
                  </div>
                </div>
              ) : null}

              <div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-sm font-medium text-slate-700 dark:text-slate-200">
                    安装命令
                    <span className="ml-1.5 font-normal text-slate-400 dark:text-slate-500">
                      （在目标服务器执行）
                    </span>
                  </p>
                  {command ? <CopyButton value={command} label="复制命令" /> : null}
                </div>

                {commandLoading ? (
                  <div className="mt-1.5 rounded-md border border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-950">
                    <InlineLoading label="正在生成安装命令…" />
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
                        重试
                      </Button>
                    ) : null}
                  </div>
                ) : null}
              </div>

              <div className="rounded-md border border-slate-200 bg-slate-50 px-3.5 py-3 text-xs leading-relaxed text-slate-500 dark:border-slate-800 dark:bg-slate-950/60 dark:text-slate-400">
                <p>
                  命令中的控制端地址为{' '}
                  <code className="rounded bg-white px-1 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-900 dark:text-slate-300">
                    {resolveControlUrl()}
                  </code>
                  ，请确保目标机器可以访问该地址。
                </p>
                <p className="mt-1">执行后无需刷新页面，服务器列表每 10 秒自动更新。</p>
              </div>
            </div>
          )
        ) : createdServer ? (
          <div className="space-y-4">
            <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-3.5 py-3 dark:border-emerald-500/30 dark:bg-emerald-500/10">
              <p className="text-sm font-medium text-emerald-800 dark:text-emerald-200">
                服务器「{createdServer.name}」已添加
              </p>
              <p className="mt-0.5 text-xs text-emerald-700/80 dark:text-emerald-300/80">
                控制端将在 30 秒内完成首次 SSH 采集，状态会从「未知」变为「在线」。
              </p>
            </div>
            <dl className="space-y-1.5 rounded-md border border-slate-200 bg-slate-50 px-3.5 py-3 text-xs dark:border-slate-800 dark:bg-slate-950/60">
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">主机</dt>
                <dd className="font-mono text-slate-700 dark:text-slate-200">
                  {createdServer.host}:{createdServer.port ?? 22}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">主机密钥策略</dt>
                <dd className="text-slate-700 dark:text-slate-200">{sshPolicy}</dd>
              </div>
              {createdServer.group ? (
                <div className="flex justify-between gap-3">
                  <dt className="text-slate-500 dark:text-slate-400">分组</dt>
                  <dd className="text-slate-700 dark:text-slate-200">{createdServer.group}</dd>
                </div>
              ) : null}
            </dl>
            <p className="text-xs text-slate-400 dark:text-slate-500">
              可到服务器详情页点击「测试连通性」立即验证 SSH 是否可达。
            </p>
          </div>
        ) : (
          <form id="ssh-server-form" onSubmit={handleCreateSshServer} className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-name" className={labelClass}>
                  服务器名称
                </label>
                <input
                  id="ssh-name"
                  value={sshName}
                  onChange={(event) => setSshName(event.target.value)}
                  maxLength={64}
                  placeholder="例如：托管机 · 香港 01"
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div>
                <label htmlFor="ssh-host" className={labelClass}>
                  主机地址
                </label>
                <input
                  id="ssh-host"
                  value={sshHost}
                  onChange={(event) => setSshHost(event.target.value)}
                  placeholder="例如：10.0.0.12 或 example.com"
                  className={inputClass + ' mt-1.5'}
                />
              </div>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-port" className={labelClass}>
                  SSH 端口
                </label>
                <input
                  id="ssh-port"
                  value={sshPort}
                  onChange={(event) => setSshPort(event.target.value)}
                  inputMode="numeric"
                  className={inputClass + ' mt-1.5'}
                />
              </div>
              <div>
                <label htmlFor="ssh-policy" className={labelClass}>
                  主机密钥策略
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
                  SSH 凭证
                </label>
                <button
                  type="button"
                  onClick={() => setCredentialDialogOpen(true)}
                  className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  新建凭证
                </button>
              </div>
              {credentials.length === 0 ? (
                <p className="mt-1.5 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
                  还没有可用凭证，请先点击「新建凭证」创建密码或私钥凭证。
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
                      {credential.name}（{credential.kind === 'key' ? '私钥' : '密码'} ·{' '}
                      {credential.username}）
                    </option>
                  ))}
                </select>
              )}
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="ssh-group" className={labelClass}>
                  分组 <span className="font-normal text-slate-400">（可选）</span>
                </label>
                <input
                  id="ssh-group"
                  value={sshGroup}
                  onChange={(event) => setSshGroup(event.target.value)}
                  list="ssh-group-options"
                  placeholder="例如：生产环境"
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
                  标签 <span className="font-normal text-slate-400">（逗号分隔）</span>
                </label>
                <input
                  id="ssh-tags"
                  value={sshTags}
                  onChange={(event) => setSshTags(event.target.value)}
                  placeholder="例如：香港, 托管"
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
