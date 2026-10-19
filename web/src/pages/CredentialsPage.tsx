import { useState } from 'react'
import { ApiError, api, type Credential } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { errorMessage } from '../lib/errors'
import { formatDateTime, truncateMiddle } from '../lib/format'
import { Button } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CredentialDialog } from '../components/CredentialDialog'
import { EmptyState } from '../components/EmptyState'
import { KeyIcon, PlusIcon, RefreshIcon, TrashIcon } from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { useToast } from '../components/Toast'
import { IconButton } from '../components/Button'

function KindBadge({ kind }: { kind: string }) {
  const isKey = kind === 'key'
  return (
    <span
      className={
        'inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset ' +
        (isKey
          ? 'bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/30'
          : 'bg-slate-100 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-300 dark:ring-slate-400/30')
      }
    >
      {isKey ? '私钥' : '密码'}
    </span>
  )
}

export default function CredentialsPage() {
  const { toast } = useToast()
  const query = usePolling(api.credentials, 10_000)
  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<Credential | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  const credentials = query.data ?? []

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    setDeleteError(null)
    try {
      await api.deleteCredential(deleting.id)
      toast(`已删除凭证「${deleting.name}」`, 'success')
      setDeleting(null)
      void query.refresh()
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setDeleteError(
          `该凭证正被服务器引用，无法删除。\n请先在「总览」中把这台服务器更换凭证或改为 Agent 接入（解绑）后再删除。`,
        )
      } else {
        setDeleteError(errorMessage(caught, '删除凭证失败'))
      }
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            凭证管理
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            SSH 免安装接入使用的登录凭证；密钥加密存储，界面与接口都不会回显内容
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={() => void query.refresh()}
            loading={query.loading}
            icon={<RefreshIcon className="h-4 w-4" />}
          >
            刷新
          </Button>
          <Button onClick={() => setCreateOpen(true)} icon={<PlusIcon className="h-4 w-4" />}>
            新建凭证
          </Button>
        </div>
      </div>

      {query.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>加载凭证失败：{query.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void query.refresh()}>
            重试
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">凭证</h2>
            <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {credentials.length}
            </span>
          </div>
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {query.updatedAt ? `更新于 ${formatDateTime(query.updatedAt)}` : '加载中…'}
          </p>
        </div>

        {query.loading && query.data === null ? (
          <InlineLoading label="正在加载凭证…" />
        ) : credentials.length === 0 ? (
          <EmptyState
            icon={<KeyIcon className="h-6 w-6" />}
            title="还没有凭证"
            description="接入 SSH 免安装服务器前，需要先创建一份登录凭证"
            actionLabel="新建第一个凭证"
            onAction={() => setCreateOpen(true)}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[760px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th scope="col" className="px-4 py-2.5 font-medium">名称</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">类型</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">用户名</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">私钥指纹</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">引用服务器</th>
                  <th scope="col" className="px-4 py-2.5 font-medium">创建时间</th>
                  <th scope="col" className="px-4 py-2.5 text-right font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {credentials.map((credential) => (
                  <tr
                    key={credential.id}
                    className="transition-colors hover:bg-slate-50/80 dark:hover:bg-slate-900/60"
                  >
                    <td className="px-4 py-3 font-medium text-slate-900 dark:text-slate-100">
                      {credential.name}
                    </td>
                    <td className="px-4 py-3">
                      <KindBadge kind={credential.kind} />
                    </td>
                    <td className="px-4 py-3 text-slate-600 dark:text-slate-300">
                      {credential.username || '—'}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-500 dark:text-slate-400">
                      <span title={credential.fingerprint ?? undefined}>
                        {credential.fingerprint
                          ? truncateMiddle(credential.fingerprint, 20, 10)
                          : '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 tabular-nums text-slate-600 dark:text-slate-300">
                      {credential.server_count > 0 ? `${credential.server_count} 台` : '未使用'}
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(credential.created_at)}
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex justify-end">
                        <IconButton
                          label="删除凭证"
                          tone="danger"
                          onClick={() => {
                            setDeleteError(null)
                            setDeleting(credential)
                          }}
                        >
                          <TrashIcon className="h-4 w-4" />
                        </IconButton>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <p className="text-xs text-slate-400 dark:text-slate-500">
        安全提示：凭证接口只返回名称、类型、用户名与指纹；密码与私钥明文仅存在于创建请求中，创建后无法查看。
      </p>

      <CredentialDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => void query.refresh()}
      />

      <ConfirmDialog
        open={deleting !== null}
        title="删除凭证"
        description={deleting ? `凭证：${deleting.name}` : undefined}
        confirmLabel="确认删除"
        tone="danger"
        loading={deleteLoading}
        error={deleteError}
        onClose={() => {
          setDeleting(null)
          setDeleteError(null)
        }}
        onConfirm={() => void handleDelete()}
      >
        <p>
          确定要删除凭证
          <span className="font-semibold text-slate-900 dark:text-slate-100">
            「{deleting?.name}」
          </span>
          吗？该操作不可撤销。
        </p>
      </ConfirmDialog>
    </div>
  )
}
