import { useState } from 'react'
import type { Server } from '../lib/api'
import { Button } from './Button'
import { ConfirmDialog } from './ConfirmDialog'
import { Modal } from './Modal'
import { TagsDialog } from './TagsDialog'
import { FolderIcon, RefreshIcon, TagIcon, TrashIcon, WifiIcon } from './Icons'
import { t } from '../i18n'

interface BatchActionBarProps {
  selected: Server[]
  pending: boolean
  groupOptions: string[]
  onClear: () => void
  onTest: () => void
  onRefreshInfo: () => void
  onAddTags: (tags: string[]) => void
  onSetGroup: (group: string) => void
  onDelete: () => void
}

/** 批量操作栏：选中服务器后出现，逐台结果由页面用 BatchResultDialog 展示。 */
export function BatchActionBar({
  selected,
  pending,
  groupOptions,
  onClear,
  onTest,
  onRefreshInfo,
  onAddTags,
  onSetGroup,
  onDelete,
}: BatchActionBarProps) {
  const [tagsOpen, setTagsOpen] = useState(false)
  const [groupOpen, setGroupOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [group, setGroup] = useState('')
  const count = selected.length

  if (count === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-indigo-100 bg-indigo-50/70 px-4 py-2.5 dark:border-indigo-500/20 dark:bg-indigo-500/10">
      <span className="mr-1 text-sm font-medium text-indigo-800 dark:text-indigo-200">
        {t('s.0208')} {count} {t('s.0280')}
      </span>
      <Button
        size="sm"
        variant="secondary"
        onClick={onTest}
        disabled={pending}
        icon={<WifiIcon className="h-3.5 w-3.5" />}
      >
        {t('s.0281')}
      </Button>
      <Button
        size="sm"
        variant="secondary"
        onClick={onRefreshInfo}
        disabled={pending}
        icon={<RefreshIcon className="h-3.5 w-3.5" />}
      >
        {t('s.0282')}
      </Button>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => setTagsOpen(true)}
        disabled={pending}
        icon={<TagIcon className="h-3.5 w-3.5" />}
      >
        {t('s.0283')}
      </Button>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          setGroup('')
          setGroupOpen(true)
        }}
        disabled={pending}
        icon={<FolderIcon className="h-3.5 w-3.5" />}
      >
        {t('s.0284')}
      </Button>
      <Button
        size="sm"
        variant="danger"
        onClick={() => setDeleteOpen(true)}
        disabled={pending}
        icon={<TrashIcon className="h-3.5 w-3.5" />}
      >
        {t('s.0285')}
      </Button>
      <button
        type="button"
        onClick={onClear}
        disabled={pending}
        className="ml-auto text-xs font-medium text-indigo-700 hover:text-indigo-900 disabled:opacity-50 dark:text-indigo-300 dark:hover:text-indigo-100"
      >
        {t('s.0286')}
      </button>

      <TagsDialog
        open={tagsOpen}
        title={t('s.0287', { '0': count })}
        description={t('s.0288')}
        initialTags={[]}
        loading={pending}
        submitLabel={t('s.0289')}
        onClose={() => setTagsOpen(false)}
        onSubmit={(tags) => {
          setTagsOpen(false)
          onAddTags(tags)
        }}
      />

      <Modal
        open={groupOpen}
        onClose={() => setGroupOpen(false)}
        title={t('s.0290', { '0': count })}
        description={t('s.0291')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setGroupOpen(false)} disabled={pending}>
              {t('s.0024')}
            </Button>
            <Button
              onClick={() => {
                setGroupOpen(false)
                onSetGroup(group.trim())
              }}
              loading={pending}
            >
              {t('s.0292')}
            </Button>
          </>
        }
      >
        <label
          htmlFor="batch-group"
          className="block text-sm font-medium text-slate-700 dark:text-slate-200"
        >
          {t('s.0293')}
        </label>
        <input
          id="batch-group"
          value={group}
          onChange={(event) => setGroup(event.target.value)}
          list="batch-group-options"
          placeholder={t('s.0074')}
          className="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
        />
        <datalist id="batch-group-options">
          {groupOptions.map((option) => (
            <option key={option} value={option} />
          ))}
        </datalist>
      </Modal>

      <ConfirmDialog
        open={deleteOpen}
        title={t('s.0294', { '0': count })}
        confirmLabel={t('s.0295')}
        tone="danger"
        loading={pending}
        onClose={() => setDeleteOpen(false)}
        onConfirm={() => {
          setDeleteOpen(false)
          onDelete()
        }}
      >
        <p>{t('s.0296')} {count} {t('s.0297')}</p>
        <ul className="max-h-40 overflow-y-auto rounded-md border border-slate-200 p-2 text-xs text-slate-600 dark:border-slate-800 dark:text-slate-300">
          {selected.map((server) => (
            <li key={server.id} className="truncate py-0.5">
              {server.name}（{server.host}）
            </li>
          ))}
        </ul>
      </ConfirmDialog>
    </div>
  )
}
