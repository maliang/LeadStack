import { useMemo, useState, type FormEvent } from 'react'
import {
  api,
  type ScheduledTask,
  type ScheduledTaskRun,
  type Server,
} from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { errorMessage } from '../lib/errors'
import { describeCron, parseCron } from '../lib/cron'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { cn } from '../lib/cn'
import { Button, IconButton } from '../components/Button'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { EmptyState } from '../components/EmptyState'
import { EnableSwitch } from '../components/EnableSwitch'
import {
  ChevronRightIcon,
  ClockIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshIcon,
  TrashIcon,
} from '../components/Icons'
import { InlineLoading } from '../components/Loading'
import { Modal } from '../components/Modal'
import { PageHeader } from '../components/PageHeader'
import { useToast } from '../components/Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

interface TaskForm {
  name: string
  cron: string
  command: string
  timeout_s: string
  concurrency: string
  enabled: boolean
  serverIds: Set<string>
}

const EMPTY_FORM: TaskForm = {
  name: '',
  cron: '0 3 * * *',
  command: '',
  timeout_s: '60',
  concurrency: '4',
  enabled: true,
  serverIds: new Set(),
}

export default function ScheduledTasksPage() {
  const { t } = useI18n()
  const { toast } = useToast()
  const tasksQuery = usePolling(api.scheduledTasks, 10_000)
  const serversQuery = usePolling(api.servers, 30_000)
  const tasks = tasksQuery.data ?? []
  const servers = serversQuery.data ?? []

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<ScheduledTask | null>(null)
  const [form, setForm] = useState<TaskForm>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const [runsTask, setRunsTask] = useState<ScheduledTask | null>(null)
  const [runs, setRuns] = useState<ScheduledTaskRun[]>([])
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState<string | null>(null)

  const [deleting, setDeleting] = useState<ScheduledTask | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)
  const [togglingId, setTogglingId] = useState<string | null>(null)

  const cronPreview = useMemo(() => (parseCron(form.cron) ? describeCron(form.cron) : null), [form.cron])
  const serverNameOf = useMemo(() => {
    const names = new Map(servers.map((server) => [server.id, server.name]))
    return (id: string) => names.get(id) ?? id
  }, [servers])

  const openCreate = () => {
    setEditing(null)
    setForm(EMPTY_FORM)
    setFormError(null)
    setDialogOpen(true)
  }

  const openEdit = (task: ScheduledTask) => {
    setEditing(task)
    setForm({
      name: task.name,
      cron: task.cron,
      command: task.command,
      timeout_s: String(task.timeout_s),
      concurrency: String(task.concurrency),
      enabled: task.enabled,
      serverIds: new Set(task.server_ids),
    })
    setFormError(null)
    setDialogOpen(true)
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!form.name.trim()) return setFormError(t('schedules.errorName'))
    if (!parseCron(form.cron)) return setFormError(t('schedules.errorCron'))
    if (!form.command.trim()) return setFormError(t('schedules.errorCommand'))
    if (form.serverIds.size === 0) return setFormError(t('schedules.errorTargets'))
    setSaving(true)
    setFormError(null)
    const payload = {
      name: form.name.trim(),
      cron: form.cron.trim(),
      command: form.command,
      timeout_s: Number(form.timeout_s) || 60,
      enabled: form.enabled,
      concurrency: Number(form.concurrency) || 4,
      server_ids: [...form.serverIds],
    }
    try {
      if (editing) {
        await api.updateScheduledTask(editing.id, payload)
        toast(t('schedules.updated', { name: payload.name }), 'success')
      } else {
        await api.createScheduledTask(payload)
        toast(t('schedules.created', { name: payload.name }), 'success')
      }
      setDialogOpen(false)
      void tasksQuery.refresh()
    } catch (caught) {
      setFormError(errorMessage(caught, t('common.error')))
    } finally {
      setSaving(false)
    }
  }

  const openRuns = async (task: ScheduledTask) => {
    setRunsTask(task)
    setRuns([])
    setRunsError(null)
    setRunsLoading(true)
    try {
      const result = await api.scheduledTaskRuns(task.id, 50)
      setRuns(result)
    } catch (caught) {
      setRunsError(errorMessage(caught, t('common.error')))
    } finally {
      setRunsLoading(false)
    }
  }

  const handleRun = async (task: ScheduledTask) => {
    try {
      await api.runScheduledTask(task.id)
      toast(t('schedules.runTriggered', { name: task.name }), 'success')
      void tasksQuery.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    }
  }

  const handleToggle = async (task: ScheduledTask) => {
    setTogglingId(task.id)
    try {
      await api.updateScheduledTask(task.id, { enabled: !task.enabled })
      void tasksQuery.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    } finally {
      setTogglingId(null)
    }
  }

  const handleDelete = async () => {
    if (!deleting) return
    setDeleteLoading(true)
    try {
      await api.deleteScheduledTask(deleting.id)
      toast(t('schedules.deleted', { name: deleting.name }), 'success')
      setDeleting(null)
      void tasksQuery.refresh()
    } catch (caught) {
      toast(errorMessage(caught, t('common.error')), 'error')
    } finally {
      setDeleteLoading(false)
    }
  }

  const toggleServer = (id: string) => {
    setForm((current) => {
      const next = new Set(current.serverIds)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return { ...current, serverIds: next }
    })
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('schedules.title')}
        description={t('schedules.description')}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => void tasksQuery.refresh()}
              loading={tasksQuery.loading}
              icon={<RefreshIcon className="h-4 w-4" />}
            >
              {t('common.refresh')}
            </Button>
            <Button onClick={openCreate} icon={<PlusIcon className="h-4 w-4" />}>
              {t('schedules.create')}
            </Button>
          </>
        }
      />

      {tasksQuery.error ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
          <span>{t('schedules.loadFailed')}：{tasksQuery.error}</span>
          <Button size="sm" variant="secondary" onClick={() => void tasksQuery.refresh()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <section className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100">
            <ClockIcon className="h-4 w-4 text-indigo-500" />
            {t('schedules.title')}
          </h2>
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            {tasks.length}
          </span>
        </div>

        {tasksQuery.loading && tasksQuery.data === null ? (
          <InlineLoading label={t('common.loading')} />
        ) : tasks.length === 0 ? (
          <EmptyState
            icon={<ClockIcon className="h-6 w-6" />}
            title={t('schedules.emptyTitle')}
            description={t('schedules.emptyDescription')}
            actionLabel={t('schedules.create')}
            onAction={openCreate}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1040px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-slate-200 text-left text-xs font-medium whitespace-nowrap text-slate-500 dark:border-slate-800 dark:text-slate-400">
                  <th className="px-4 py-2.5 font-medium">{t('common.name')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('schedules.cron')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('schedules.targets')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('schedules.lastRun')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('schedules.nextRun')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('common.enabled')}</th>
                  <th className="px-4 py-2.5 text-right font-medium">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70">
                {tasks.map((task) => (
                  <tr key={task.id} className="hover:bg-slate-50/80 dark:hover:bg-slate-900/60">
                    <td className="px-4 py-3 align-top">
                      <span className="font-medium text-slate-800 dark:text-slate-100">{task.name}</span>
                      <span className="mt-0.5 block max-w-[260px] truncate font-mono text-[11px] text-slate-400 dark:text-slate-500">
                        {task.command}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top">
                      <code className="font-mono text-xs text-slate-700 dark:text-slate-200">{task.cron}</code>
                      <span className="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500">
                        {describeCron(task.cron) ?? '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-600 dark:text-slate-300">
                      {task.server_ids.length > 0
                        ? t('s.1073', { '0': task.server_ids.length, '1': task.server_ids
                            .slice(0, 2)
                            .map(serverNameOf)
                            .join('、') })
                        : t('schedules.noTargets')}
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {task.last_run_at ? formatRelativeTime(task.last_run_at) : '—'}
                    </td>
                    <td className="px-4 py-3 align-top text-xs text-slate-500 dark:text-slate-400">
                      {task.next_run_at ? formatDateTime(task.next_run_at) : '—'}
                    </td>
                    <td className="px-4 py-3 align-top">
                      <EnableSwitch
                        checked={task.enabled}
                        loading={togglingId === task.id}
                        onChange={() => void handleToggle(task)}
                      />
                    </td>
                    <td className="px-4 py-3 align-top">
                      <div className="flex items-center justify-end gap-0.5">
                        <IconButton label={t('schedules.runNow')} onClick={() => void handleRun(task)}>
                          <PlayIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton label={t('schedules.history')} onClick={() => void openRuns(task)}>
                          <ChevronRightIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton label={t('common.edit')} onClick={() => openEdit(task)}>
                          <PencilIcon className="h-4 w-4" />
                        </IconButton>
                        <IconButton
                          label={t('common.delete')}
                          tone="danger"
                          onClick={() => setDeleting(task)}
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

      <Modal
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={editing ? t('schedules.edit') : t('schedules.create')}
        description={t('schedules.dialogHint')}
        size="lg"
        footer={
          <>
            <Button variant="secondary" onClick={() => setDialogOpen(false)} disabled={saving}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="schedule-form" loading={saving}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <form id="schedule-form" onSubmit={handleSubmit} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor="task-name" className={labelClass}>
                {t('common.name')}
              </label>
              <input
                id="task-name"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                className={inputClass + ' mt-1.5'}
              />
            </div>
            <div>
              <label htmlFor="task-cron" className={labelClass}>
                {t('schedules.cron')}
              </label>
              <input
                id="task-cron"
                value={form.cron}
                onChange={(event) => setForm({ ...form, cron: event.target.value })}
                className={inputClass + ' mt-1.5 font-mono text-xs'}
                placeholder="0 3 * * *"
              />
              <p
                className={cn(
                  'mt-1 text-xs',
                  cronPreview ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400',
                )}
              >
                {cronPreview ?? t('schedules.cronInvalid')}
              </p>
            </div>
          </div>

          <div>
            <label htmlFor="task-command" className={labelClass}>
              {t('schedules.command')}
            </label>
            <textarea
              id="task-command"
              value={form.command}
              onChange={(event) => setForm({ ...form, command: event.target.value })}
              rows={4}
              spellCheck={false}
              className={inputClass + ' mt-1.5 resize-y font-mono text-xs'}
              placeholder="df -h && free -m"
            />
            <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
              {t('schedules.commandWarning')}
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div>
              <label htmlFor="task-timeout" className={labelClass}>
                {t('schedules.timeout')}
              </label>
              <input
                id="task-timeout"
                value={form.timeout_s}
                onChange={(event) => setForm({ ...form, timeout_s: event.target.value })}
                className={inputClass + ' mt-1.5'}
                inputMode="numeric"
              />
            </div>
            <div>
              <label htmlFor="task-concurrency" className={labelClass}>
                {t('schedules.concurrency')}
              </label>
              <input
                id="task-concurrency"
                value={form.concurrency}
                onChange={(event) => setForm({ ...form, concurrency: event.target.value })}
                className={inputClass + ' mt-1.5'}
                inputMode="numeric"
              />
            </div>
            <label className="mt-6 flex items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
                className="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-800"
              />
              {t('schedules.enableTask')}
            </label>
          </div>

          <div>
            <label className={labelClass}>
              {t('schedules.targets')}（{form.serverIds.size}/{servers.length}）
            </label>
            <div className="mt-1.5 max-h-40 overflow-y-auto rounded-md border border-slate-200 p-1.5 dark:border-slate-800">
              {servers.length === 0 ? (
                <p className="px-2 py-3 text-xs text-slate-400">{t('schedules.noServers')}</p>
              ) : (
                servers.map((server: Server) => (
                  <label
                    key={server.id}
                    className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-slate-50 dark:hover:bg-slate-800/60"
                  >
                    <input
                      type="checkbox"
                      checked={form.serverIds.has(server.id)}
                      onChange={() => toggleServer(server.id)}
                      className="h-3.5 w-3.5 rounded border-slate-300 text-indigo-600 dark:border-slate-600 dark:bg-slate-800"
                    />
                    <span className="min-w-0 flex-1 truncate text-slate-700 dark:text-slate-200">
                      {server.name}
                    </span>
                    <span className="font-mono text-[11px] text-slate-400">{server.host}</span>
                  </label>
                ))
              )}
            </div>
          </div>

          {formError ? (
            <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
              {formError}
            </p>
          ) : null}
        </form>
      </Modal>

      <Modal
        open={runsTask !== null}
        onClose={() => setRunsTask(null)}
        title={t('schedules.history')}
        description={runsTask?.name}
        size="lg"
        footer={
          <Button variant="secondary" onClick={() => setRunsTask(null)}>
            {t('common.close')}
          </Button>
        }
      >
        {runsLoading ? (
          <InlineLoading label={t('common.loading')} />
        ) : runsError ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {runsError}
          </p>
        ) : runs.length === 0 ? (
          <p className="py-6 text-center text-sm text-slate-400 dark:text-slate-500">
            {t('schedules.noRuns')}
          </p>
        ) : (
          <ul className="max-h-96 space-y-1.5 overflow-y-auto">
            {runs.map((run) => (
              <li
                key={run.id}
                className="rounded-md border border-slate-200 px-3 py-2 text-xs dark:border-slate-800"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span
                    className={cn(
                      'font-medium',
                      run.status === 'success'
                        ? 'text-emerald-600 dark:text-emerald-400'
                        : run.status === 'running'
                          ? 'text-blue-600 dark:text-blue-400'
                          : 'text-rose-600 dark:text-rose-400',
                    )}
                  >
                    {run.status}
                  </span>
                  <span className="text-slate-500 dark:text-slate-400">
                    {serverNameOf(run.server_id)}
                  </span>
                  <span className="text-slate-400">{formatDateTime(run.ts)}</span>
                  {run.exit_code !== null ? (
                    <span className="font-mono text-slate-500">exit {run.exit_code}</span>
                  ) : null}
                  {run.duration_ms !== null ? (
                    <span className="text-slate-400">{run.duration_ms} ms</span>
                  ) : null}
                </div>
                {run.stdout ? (
                  <pre className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap rounded bg-slate-950 p-2 font-mono text-[11px] text-slate-100">
                    {run.stdout}
                  </pre>
                ) : null}
                {run.stderr ? (
                  <pre className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap rounded bg-rose-950/80 p-2 font-mono text-[11px] text-rose-100">
                    {run.stderr}
                  </pre>
                ) : null}
                {run.message ? (
                  <p className="mt-1 text-rose-600 dark:text-rose-400">{run.message}</p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Modal>

      <ConfirmDialog
        open={deleting !== null}
        title={t('schedules.delete')}
        description={deleting?.name}
        confirmLabel={t('common.delete')}
        tone="danger"
        loading={deleteLoading}
        onClose={() => setDeleting(null)}
        onConfirm={() => void handleDelete()}
      >
        <p>{t('schedules.deleteImpact')}</p>
      </ConfirmDialog>
    </div>
  )
}
