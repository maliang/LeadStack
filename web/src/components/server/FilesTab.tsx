import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type RemoteFileEntry } from '../../lib/api'
import { useI18n } from '../../i18n'
import { errorMessage } from '../../lib/errors'
import { formatBytes, formatDateTime } from '../../lib/format'
import { cn } from '../../lib/cn'
import { Button, IconButton } from '../Button'
import { ConfirmDialog } from '../ConfirmDialog'
import { EmptyState } from '../EmptyState'
import {
  ChevronRightIcon,
  DownloadIcon,
  FolderIcon,
  RefreshIcon,
  TrashIcon,
  UploadIcon,
} from '../Icons'
import { InlineLoading } from '../Loading'
import { useToast } from '../Toast'

const inputClass =
  'h-9 rounded-md border border-slate-300 bg-white px-3 text-sm text-slate-700 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200'

function joinPath(base: string, name: string): string {
  if (base === '/' || base === '') return `/${name}`
  return `${base.replace(/\/+$/, '')}/${name}`
}

function parentPath(path: string): string {
  const trimmed = path.replace(/\/+$/, '')
  if (trimmed === '' || trimmed === '/') return '/'
  const index = trimmed.lastIndexOf('/')
  return index <= 0 ? '/' : trimmed.slice(0, index)
}

/** 服务器详情「文件」标签：浏览 / 上传 / 下载 / 删除（ssh 走 SFTP，agent 走 job.exec）。 */
export function FilesTab({ serverId }: { serverId: string }) {
  const { t } = useI18n()
  const { toast } = useToast()
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [path, setPath] = useState('/')
  const [pathInput, setPathInput] = useState('/')
  const [entries, setEntries] = useState<RemoteFileEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [uploading, setUploading] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<RemoteFileEntry | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  const load = useCallback(
    async (target: string) => {
      setLoading(true)
      setError(null)
      try {
        const list = await api.listFiles(serverId, target)
        setEntries(list)
      } catch (caught) {
        setError(errorMessage(caught, t('files.loadFailed')))
      } finally {
        setLoading(false)
      }
    },
    [serverId, t],
  )

  useEffect(() => {
    void load(path)
  }, [path, load])

  const navigate = (target: string) => {
    setPath(target)
    setPathInput(target)
  }

  const handleUpload = async (file: File | null) => {
    if (!file) return
    if (file.size > 8 * 1024 * 1024) {
      toast(t('files.tooLarge'), 'error')
      return
    }
    setUploading(file.name)
    try {
      await api.uploadServerFile(serverId, joinPath(path, file.name), file)
      toast(t('files.uploaded', { name: file.name }), 'success')
      await load(path)
    } catch (caught) {
      toast(errorMessage(caught, t('files.uploadFailed')), 'error')
    } finally {
      setUploading(null)
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  const handleDownload = async (entry: RemoteFileEntry) => {
    try {
      const result = await api.downloadServerFile(serverId, joinPath(path, entry.name))
      const url = URL.createObjectURL(result.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename
      document.body.appendChild(anchor)
      anchor.click()
      document.body.removeChild(anchor)
      window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
    } catch (caught) {
      toast(errorMessage(caught, t('files.downloadFailed')), 'error')
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    try {
      await api.deleteServerFile(serverId, joinPath(path, deleting.name))
      toast(t('files.deleted', { name: deleting.name }), 'success')
      setDeleting(null)
      await load(path)
    } catch (caught) {
      toast(errorMessage(caught, t('files.deleteFailed')), 'error')
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-slate-200 bg-white px-3 py-2 shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <button
          type="button"
          onClick={() => navigate(parentPath(path))}
          className="rounded border border-slate-200 px-2 py-1 text-xs text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        >
          {t('files.up')}
        </button>
        <input
          value={pathInput}
          onChange={(event) => setPathInput(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') navigate(pathInput.trim() || '/')
          }}
          className={cn(inputClass, 'min-w-0 flex-1 font-mono text-xs')}
          aria-label={t('files.path')}
        />
        <Button size="sm" variant="secondary" onClick={() => navigate(pathInput.trim() || '/')}>
          {t('files.go')}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => void load(path)}
          loading={loading}
          icon={<RefreshIcon className="h-3.5 w-3.5" />}
        >
          {t('common.refresh')}
        </Button>
        <Button
          size="sm"
          onClick={() => fileInputRef.current?.click()}
          loading={Boolean(uploading)}
          icon={<UploadIcon className="h-3.5 w-3.5" />}
        >
          {t('files.upload')}
        </Button>
        <input
          ref={fileInputRef}
          type="file"
          className="hidden"
          onChange={(event) => void handleUpload(event.target.files?.[0] ?? null)}
        />
      </div>

      {uploading ? (
        <div className="rounded-md border border-indigo-200 bg-indigo-50 px-3 py-2 text-xs text-indigo-800 dark:border-indigo-500/30 dark:bg-indigo-500/10 dark:text-indigo-200">
          {t('files.uploading', { name: uploading })}
          <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-indigo-100 dark:bg-indigo-500/20">
            <div className="h-1 w-1/2 animate-pulse rounded-full bg-indigo-500" />
          </div>
        </div>
      ) : null}

      {error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{error}</span>
          <Button size="sm" variant="secondary" onClick={() => void load(path)}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        {loading && entries.length === 0 ? (
          <InlineLoading label={t('common.loading')} />
        ) : entries.length === 0 ? (
          <EmptyState
            icon={<FolderIcon className="h-6 w-6" />}
            title={t('files.emptyTitle')}
            description={t('files.emptyDescription')}
            actionLabel={t('files.upload')}
            onAction={() => fileInputRef.current?.click()}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('files.name')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('files.size')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('files.modified')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('files.mode')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {entries.map((entry) => (
                  <tr key={entry.name} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                    <td className="px-4 py-2.5">
                      {entry.is_dir ? (
                        <button
                          type="button"
                          onClick={() => navigate(joinPath(path, entry.name))}
                          className="inline-flex items-center gap-2 font-medium text-indigo-600 hover:underline dark:text-indigo-400"
                        >
                          <FolderIcon className="h-4 w-4" />
                          {entry.name}
                        </button>
                      ) : (
                        <span className="font-mono text-xs text-slate-700 dark:text-slate-200">
                          {entry.name}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 text-xs tabular-nums text-slate-500 dark:text-slate-400">
                      {entry.is_dir ? '—' : formatBytes(entry.size)}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-500 dark:text-slate-400">
                      {formatDateTime(entry.mod_time)}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-[11px] text-slate-500 dark:text-slate-400">
                      {entry.mode ?? '—'}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex items-center justify-end gap-0.5">
                        {!entry.is_dir ? (
                          <IconButton
                            label={t('files.download')}
                            onClick={() => void handleDownload(entry)}
                          >
                            <DownloadIcon className="h-4 w-4" />
                          </IconButton>
                        ) : (
                          <IconButton
                            label={t('files.open')}
                            onClick={() => navigate(joinPath(path, entry.name))}
                          >
                            <ChevronRightIcon className="h-4 w-4" />
                          </IconButton>
                        )}
                        <IconButton
                          label={t('common.delete')}
                          tone="danger"
                          onClick={() => setDeleting(entry)}
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

      <p className="text-xs text-slate-400 dark:text-slate-500">{t('files.footerHint')}</p>

      <ConfirmDialog
        open={deleting !== null}
        title={t('files.deleteTitle')}
        description={deleting ? joinPath(path, deleting.name) : undefined}
        confirmLabel={t('common.delete')}
        tone="danger"
        loading={deleteLoading}
        onClose={() => setDeleting(null)}
        onConfirm={() => void handleDelete()}
      >
        <p>{t('files.deleteImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}
