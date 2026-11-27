<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import ScriptUploadButton from '@/components/ScriptUploadButton.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type ScheduledTask, type ScheduledTaskRun, type Server } from '@/lib/api'
import { parseCron, describeCron } from '@/lib/cron'
import { errorMessage } from '@/lib/errors'
import { formatDateTime, formatLatency, formatRelativeTime } from '@/lib/format'

/**
 * 定时任务页（挂载面：AppShell 默认 slot → .ls-main__inner）。
 *
 * 数据 owner 是本页：任务列表为主请求，服务器列表为辅助请求（失败只影响目标选择，
 * 不阻断列表）。读权限由路由守卫保证；task.write 缺失时所有写入口隐藏且不发写请求。
 * 删除、编辑、新建、启用切换、立即运行、运行历史各有一条错误回显路径，不静默失败。
 */
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('task.read'))
const canWrite = computed(() => can('task.write'))

const tasks = ref<ScheduledTask[]>([])
const servers = ref<Server[]>([])
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const loaded = ref(false)
const error = ref<string | null>(null)
const refreshing = ref(false)
const actionError = ref<string | null>(null)
const notice = ref<string | null>(null)

const createOpen = ref(false)
const editing = ref<ScheduledTask | null>(null)
const saving = ref(false)
const formErrorKey = ref<string | null>(null)
/** 服务端返回的保存失败原因：贴在弹窗内部，避免关窗后只剩页面顶部的 actionError。 */
const formSubmitError = ref<string | null>(null)

const form = reactive({
  name: '',
  cron: '0 3 * * *',
  command: '',
  timeoutS: '60',
  concurrency: '4',
  enabled: true,
  serverIds: [] as string[],
})

const togglingId = ref<string | null>(null)
const runningId = ref<string | null>(null)

const deleteTarget = ref<ScheduledTask | null>(null)
const deleteBusy = ref(false)

const historyTask = ref<ScheduledTask | null>(null)
const runs = ref<ScheduledTaskRun[]>([])
const runsState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const runsError = ref<string | null>(null)

const serverNameMap = computed(() => {
  const map = new Map<string, string>()
  for (const server of servers.value) map.set(server.id, server.name || server.host || server.id)
  return map
})

const cronPreview = computed(() => describeCron(form.cron))
const formError = computed(() => (formErrorKey.value ? t(formErrorKey.value) : null))

function serverName(id: string): string {
  return serverNameMap.value.get(id) ?? id
}

function resetForm(): void {
  form.name = ''
  form.cron = '0 3 * * *'
  form.command = ''
  form.timeoutS = '60'
  form.concurrency = '4'
  form.enabled = true
  form.serverIds = []
  formErrorKey.value = null
  formSubmitError.value = null
}

function openCreate(): void {
  if (!canWrite.value) return
  editing.value = null
  resetForm()
  createOpen.value = true
}

function openEdit(task: ScheduledTask): void {
  if (!canWrite.value) return
  editing.value = task
  form.name = task.name
  form.cron = task.cron
  form.command = task.command
  form.timeoutS = String(task.timeout_s)
  form.concurrency = String(task.concurrency)
  form.enabled = task.enabled
  form.serverIds = [...task.server_ids]
  formErrorKey.value = null
  formSubmitError.value = null
  createOpen.value = true
}

function closeForm(): void {
  if (saving.value) return
  createOpen.value = false
  editing.value = null
  formErrorKey.value = null
  formSubmitError.value = null
}

function toggleServer(id: string): void {
  form.serverIds = form.serverIds.includes(id)
    ? form.serverIds.filter((item) => item !== id)
    : [...form.serverIds, id]
}

function validate(): string | null {
  if (!form.name.trim()) return 'schedules.errorName'
  if (!parseCron(form.cron)) return 'schedules.errorCron'
  if (!form.command.trim()) return 'schedules.errorCommand'
  if (form.serverIds.length === 0) return 'schedules.errorTargets'
  return null
}

async function load(silent = false): Promise<void> {
  if (!canRead.value) return
  if (!silent && !loaded.value) state.value = 'loading'
  if (silent) refreshing.value = true
  error.value = null
  try {
    tasks.value = await api.scheduledTasks()
    loaded.value = true
    state.value = 'ready'
  } catch (err) {
    error.value = errorMessage(err, t('schedules.loadFailed'))
    if (!loaded.value) state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

async function loadServers(): Promise<void> {
  try {
    servers.value = await api.servers()
  } catch {
    // 服务器列表只用于目标展示与选择：失败不阻断任务列表，保留已有数据。
    servers.value = []
  }
}

function refresh(): void {
  void load(true)
  void loadServers()
}

async function submitForm(): Promise<void> {
  if (!canWrite.value || saving.value) return
  const invalid = validate()
  if (invalid) {
    formErrorKey.value = invalid
    return
  }
  formErrorKey.value = null
  formSubmitError.value = null
  saving.value = true
  actionError.value = null
  const payload = {
    name: form.name.trim(),
    cron: form.cron.trim(),
    command: form.command,
    timeout_s: Number(form.timeoutS) || 60,
    concurrency: Number(form.concurrency) || 4,
    enabled: form.enabled,
    server_ids: [...form.serverIds],
  }
  try {
    if (editing.value) {
      await api.updateScheduledTask(editing.value.id, payload)
      notice.value = t('schedules.updated', { name: payload.name })
    } else {
      await api.createScheduledTask(payload)
      notice.value = t('schedules.created', { name: payload.name })
    }
    createOpen.value = false
    editing.value = null
    await load(true)
  } catch (err) {
    // 保存失败不关弹窗：用户刚填的 cron / 命令不能因为一次 409 就丢光。
    formSubmitError.value = errorMessage(err, t('common.error'))
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(task: ScheduledTask): Promise<void> {
  if (!canWrite.value || togglingId.value) return
  togglingId.value = task.id
  actionError.value = null
  try {
    await api.updateScheduledTask(task.id, { enabled: !task.enabled })
    notice.value = t('schedules.updated', { name: task.name })
    await load(true)
  } catch (err) {
    actionError.value = errorMessage(err, t('common.error'))
  } finally {
    togglingId.value = null
  }
}

async function runNow(task: ScheduledTask): Promise<void> {
  if (!canWrite.value || runningId.value) return
  runningId.value = task.id
  actionError.value = null
  try {
    await api.runScheduledTask(task.id)
    notice.value = t('schedules.runTriggered', { name: task.name })
    await load(true)
  } catch (err) {
    actionError.value = errorMessage(err, t('common.error'))
  } finally {
    runningId.value = null
  }
}

function askDelete(task: ScheduledTask): void {
  if (!canWrite.value) return
  deleteTarget.value = task
}

function cancelDelete(): void {
  if (deleteBusy.value) return
  deleteTarget.value = null
}

async function confirmDelete(): Promise<void> {
  const task = deleteTarget.value
  if (!task || deleteBusy.value) return
  deleteBusy.value = true
  actionError.value = null
  try {
    await api.deleteScheduledTask(task.id)
    deleteTarget.value = null
    notice.value = t('schedules.deleted', { name: task.name })
    await load(true)
  } catch (err) {
    deleteTarget.value = null
    actionError.value = errorMessage(err, t('common.error'))
  } finally {
    deleteBusy.value = false
  }
}

async function openHistory(task: ScheduledTask): Promise<void> {
  historyTask.value = task
  runs.value = []
  runsError.value = null
  await loadRuns(task)
}

async function loadRuns(task: ScheduledTask): Promise<void> {
  runsState.value = 'loading'
  runsError.value = null
  try {
    runs.value = await api.scheduledTaskRuns(task.id, 50)
    runsState.value = 'ready'
  } catch (err) {
    runsError.value = errorMessage(err, t('common.error'))
    runsState.value = 'error'
  }
}

function closeHistory(): void {
  historyTask.value = null
  runs.value = []
  runsState.value = 'idle'
  runsError.value = null
}

function runStatusClass(status: string): string {
  if (status === 'success') return 'ls-scheduledpage__status--success'
  if (status === 'failed') return 'ls-scheduledpage__status--error'
  if (status === 'running') return 'ls-scheduledpage__status--running'
  return 'ls-scheduledpage__status--idle'
}

/** 运行状态文案：与发布详情页一样走映射，绝不把后端 raw status 直接贴到界面。 */
function runStatusLabel(status: string): string {
  if (status === 'success' || status === 'failed' || status === 'running') {
    return t(`schedules.runStatus.${status}`)
  }
  return t('schedules.runStatus.other')
}

onMounted(() => {
  if (!canRead.value) return
  void load()
  void loadServers()
})
</script>

<template>
  <section class="ls-adminpage ls-scheduledpage">
    <div v-if="!canRead" id="scheduled-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('schedules.title') }}</p>
      <p class="ls-notice__text">{{ t('schedules.readOnlyHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('schedules.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('schedules.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button
            id="scheduled-refresh"
            class="ls-btn"
            type="button"
            :disabled="refreshing"
            @click="refresh"
          >
            <span v-if="refreshing" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
          <button
            v-if="canWrite"
            id="scheduled-create-open"
            class="ls-btn ls-btn--primary"
            type="button"
            @click="openCreate"
          >
            <AppIcon name="plus" size="0.875rem" />
            {{ t('schedules.create') }}
          </button>
        </div>
      </header>

      <p v-if="!canWrite" id="scheduled-readonly" class="ls-notice" role="status">
        <span class="ls-notice__text">{{ t('schedules.readOnlyHint') }}</span>
      </p>

      <div v-if="actionError" id="scheduled-action-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('common.error') }}</p>
        <p class="ls-notice__text">{{ actionError }}</p>
        <button class="ls-notice__action" type="button" @click="actionError = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="notice" id="scheduled-action-notice" class="ls-notice ls-notice--success" role="status">
        <p class="ls-notice__title">{{ notice }}</p>
        <button class="ls-notice__action" type="button" @click="notice = null">
          {{ t('common.close') }}
        </button>
      </div>

      <div v-if="error && loaded" id="scheduled-banner-error" class="ls-banner" role="alert">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('common.error') }}</p>
          <p class="ls-banner__text">{{ error }}</p>
        </div>
        <button class="ls-notice__action" type="button" @click="refresh">{{ t('common.retry') }}</button>
      </div>

      <div v-if="state === 'loading'" id="scheduled-loading" class="ls-scheduledpage__loading" aria-busy="true">
        <span class="ls-visually-hidden">{{ t('common.loading') }}</span>
        <div v-for="index in 4" :key="index" class="ls-skeleton ls-scheduledpage__skeleton-row"></div>
      </div>

      <div v-else-if="state === 'error'" id="scheduled-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('schedules.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button class="ls-btn ls-btn--small" type="button" @click="load()">{{ t('common.retry') }}</button>
      </div>

      <div v-else-if="tasks.length === 0" id="scheduled-empty" class="ls-empty">
        <h2 class="ls-empty__title">{{ t('schedules.emptyTitle') }}</h2>
        <p class="ls-empty__text">{{ t('schedules.emptyDescription') }}</p>
        <button v-if="canWrite" class="ls-btn ls-btn--primary" type="button" @click="openCreate">
          {{ t('schedules.create') }}
        </button>
      </div>

      <div v-else class="ls-adminpage__scroll">
        <table class="ls-adminpage__table ls-scheduledpage__table">
          <caption class="ls-visually-hidden">{{ t('schedules.title') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('common.name') }}</th>
              <th scope="col">{{ t('schedules.cron') }}</th>
              <th scope="col">{{ t('schedules.targets') }}</th>
              <th scope="col">{{ t('schedules.lastRun') }}</th>
              <th scope="col">{{ t('schedules.nextRun') }}</th>
              <th scope="col">{{ t('common.enabled') }}</th>
              <th scope="col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="task in tasks"
              :key="task.id"
              class="ls-adminpage__row ls-scheduledpage__row"
              :data-task-id="task.id"
            >
              <td data-label="">
                <div class="ls-adminpage__primary">{{ task.name }}</div>
                <div class="ls-adminpage__meta ls-scheduledpage__command" :title="task.command">
                  {{ task.command }}
                </div>
              </td>
              <td :data-label="t('schedules.cron')">
                <code class="ls-scheduledpage__code">{{ task.cron }}</code>
                <div class="ls-adminpage__meta">{{ describeCron(task.cron) ?? t('schedules.cronInvalid') }}</div>
              </td>
              <td :data-label="t('schedules.targets')" class="ls-scheduledpage__targets">
                <template v-if="task.server_ids.length === 0">{{ t('schedules.noTargets') }}</template>
                <template v-else>
                  {{ task.server_ids.map(serverName).join(', ') }}
                </template>
              </td>
              <td :data-label="t('schedules.lastRun')" class="ls-adminpage__date">
                {{ task.last_run_at ? formatRelativeTime(task.last_run_at) : t('common.na') }}
              </td>
              <td :data-label="t('schedules.nextRun')" class="ls-adminpage__date">
                {{ task.next_run_at ? formatDateTime(task.next_run_at) : t('common.na') }}
              </td>
              <td :data-label="t('common.enabled')">
                <label v-if="canWrite" class="ls-scheduledpage__toggle">
                  <input
                    type="checkbox"
                    :checked="task.enabled"
                    :disabled="togglingId !== null"
                    @change="toggleEnabled(task)"
                  />
                  <span>{{ task.enabled ? t('common.enabled') : t('common.disabled') }}</span>
                </label>
                <span v-else class="ls-chip">{{ task.enabled ? t('common.enabled') : t('common.disabled') }}</span>
              </td>
              <td :data-label="t('common.actions')" class="ls-adminpage__cell-actions">
                <button
                  v-if="canWrite"
                  class="ls-btn ls-btn--small ls-scheduledpage__run"
                  type="button"
                  :disabled="runningId === task.id"
                  @click="runNow(task)"
                >
                  {{ t('schedules.runNow') }}
                </button>
                <button
                  class="ls-btn ls-btn--small ls-scheduledpage__history"
                  type="button"
                  @click="openHistory(task)"
                >
                  {{ t('schedules.history') }}
                </button>
                <template v-if="canWrite">
                  <button
                    class="ls-btn ls-btn--small ls-scheduledpage__edit"
                    type="button"
                    @click="openEdit(task)"
                  >
                    {{ t('common.edit') }}
                  </button>
                  <button
                    class="ls-btn ls-btn--small ls-btn--danger ls-scheduledpage__delete"
                    type="button"
                    @click="askDelete(task)"
                  >
                    {{ t('common.delete') }}
                  </button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal
      v-if="createOpen"
      :title="editing ? t('schedules.edit') : t('schedules.create')"
      :description="t('schedules.dialogHint')"
      size="lg"
      @close="closeForm"
    >
      <form id="scheduled-form" class="ls-adminpage__form" novalidate @submit.prevent="submitForm">
        <label class="ls-field">
          <span class="ls-field__label">{{ t('common.name') }}</span>
          <input id="scheduled-name" v-model="form.name" class="ls-input" type="text" autocomplete="off" />
        </label>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('schedules.cron') }}</span>
          <input id="scheduled-cron" v-model="form.cron" class="ls-input" type="text" autocomplete="off" />
          <span class="ls-field__hint">
            {{ cronPreview ?? t('schedules.cronInvalid') }}
          </span>
        </label>

        <label class="ls-field">
          <span class="ls-field__label">{{ t('schedules.command') }}</span>
          <textarea id="scheduled-command" v-model="form.command" class="ls-input" rows="4"></textarea>
          <ScriptUploadButton :accept="'.sh,.bash,.zsh,.txt'" @load="form.command = $event" />
          <span class="ls-field__hint">{{ t('schedules.commandWarning') }}</span>
        </label>

        <div class="ls-scheduledpage__form-grid">
          <label class="ls-field">
            <span class="ls-field__label">{{ t('schedules.timeout') }}</span>
            <input id="scheduled-timeout" v-model="form.timeoutS" class="ls-input" type="number" min="1" />
          </label>
          <label class="ls-field">
            <span class="ls-field__label">{{ t('schedules.concurrency') }}</span>
            <input id="scheduled-concurrency" v-model="form.concurrency" class="ls-input" type="number" min="1" />
          </label>
        </div>

        <label class="ls-scheduledpage__toggle">
          <input id="scheduled-enabled" v-model="form.enabled" type="checkbox" />
          <span>{{ t('schedules.enableTask') }}</span>
        </label>

        <fieldset class="ls-adminpage__fieldset">
          <legend class="ls-field__label">{{ t('schedules.targets') }}</legend>
          <p v-if="servers.length === 0" class="ls-field__hint">{{ t('schedules.noServers') }}</p>
          <div v-else class="ls-scheduledpage__servers">
            <label
              v-for="server in servers"
              :key="server.id"
              class="ls-adminpage__option ls-scheduledpage__server"
            >
              <input
                type="checkbox"
                :value="server.id"
                :checked="form.serverIds.includes(server.id)"
                @change="toggleServer(server.id)"
              />
              <span>{{ server.name }}</span>
              <span class="ls-adminpage__meta">{{ server.host }}</span>
            </label>
          </div>
        </fieldset>

        <p v-if="formError" id="scheduled-form-error" class="ls-adminpage__form-error" role="alert">
          {{ formError }}
        </p>
        <p v-if="formSubmitError" id="scheduled-form-submit-error" class="ls-adminpage__form-error" role="alert">
          {{ formSubmitError }}
        </p>
      </form>

      <template #footer>
        <button id="scheduled-cancel" class="ls-btn" type="button" :disabled="saving" @click="closeForm">
          {{ t('common.cancel') }}
        </button>
        <button
          id="scheduled-save"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="scheduled-form"
          :disabled="saving"
        >
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleteTarget"
      :title="t('schedules.delete')"
      :description="deleteTarget.name"
      size="sm"
      @close="cancelDelete"
    >
      <div id="scheduled-delete">
        <p class="ls-notice__text">{{ t('schedules.deleteImpact') }}</p>
      </div>
      <template #footer>
        <button id="scheduled-delete-cancel" class="ls-btn" type="button" :disabled="deleteBusy" @click="cancelDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="scheduled-delete-confirm"
          class="ls-btn ls-btn--danger"
          type="button"
          :disabled="deleteBusy"
          @click="confirmDelete"
        >
          {{ deleteBusy ? t('common.loading') : t('common.delete') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="historyTask"
      :title="t('schedules.history')"
      :description="historyTask.name"
      size="lg"
      @close="closeHistory"
    >
      <section id="scheduled-runs" class="ls-scheduledpage__runs" :aria-busy="runsState === 'loading'">
        <p v-if="runsState === 'loading'" class="ls-adminpage__meta">{{ t('common.loading') }}</p>
        <div v-else-if="runsState === 'error'" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__text">{{ runsError }}</p>
          <button class="ls-btn ls-btn--small" type="button" @click="historyTask && loadRuns(historyTask)">
            {{ t('common.retry') }}
          </button>
        </div>
        <p v-else-if="runs.length === 0" class="ls-adminpage__meta">{{ t('schedules.noRuns') }}</p>
        <ul v-else class="ls-scheduledpage__run-list">
          <li v-for="run in runs" :key="run.id" class="ls-scheduledpage__run-item">
            <div class="ls-scheduledpage__run-head">
              <span class="ls-chip" :class="runStatusClass(run.status)">{{ runStatusLabel(run.status) }}</span>
              <span class="ls-adminpage__meta">{{ serverName(run.server_id) }}</span>
              <span class="ls-adminpage__meta">{{ run.ts ? formatDateTime(run.ts) : t('common.na') }}</span>
              <span class="ls-adminpage__meta">
                {{ t('schedules.exitCode', { code: run.exit_code ?? t('common.na') }) }}
              </span>
              <span class="ls-adminpage__meta">
                {{ t('schedules.duration') }} {{ formatLatency(run.duration_ms) }}
              </span>
            </div>
            <p v-if="run.message" class="ls-scheduledpage__run-message">{{ run.message }}</p>
            <pre v-if="run.stdout" class="ls-scheduledpage__run-output">{{ run.stdout }}</pre>
            <pre v-if="run.stderr" class="ls-scheduledpage__run-output ls-scheduledpage__run-output--error">{{ run.stderr }}</pre>
          </li>
        </ul>
      </section>
    </AppModal>
  </section>
</template>
