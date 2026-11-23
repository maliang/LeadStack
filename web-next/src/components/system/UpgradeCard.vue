<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import AppIcon from '@/components/AppIcon.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type UpdateInfo, type UpdateStatus } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { renderReleaseNotes } from '@/lib/release-notes'
import {
  formatBytes,
  formatDateTime,
  isTerminalPhase,
  phaseLabelKey,
  phaseProgress,
  phaseStepIndex,
  phaseTone,
  isHandoffPhase,
  UPGRADE_STEPS,
  resolveLostJobOutcome,
  validateUpdateFile,
} from '@/lib/upgrade'

/**
 * 版本与升级面板（设置页）。
 *
 * 数据归属：本组件独占 UpdateInfo / UpdateStatus，网络请求也只在这里发起；
 * 父页面只负责按 `system.manage` 决定是否挂载（未挂载 = 零请求）。
 */

const { t } = useI18n()
const { can } = useCan()

/** 兜底权限判断：即使父级误挂载，也绝不发请求。 */
const allowed = computed(() => can('system.manage'))

type LoadState = 'idle' | 'loading' | 'ready' | 'error'

const loadState = ref<LoadState>('idle')
const info = ref<UpdateInfo | null>(null)
const requestError = ref<string | null>(null)
const checking = ref(false)

const confirming = ref(false)
const applying = ref(false)
const applyError = ref<string | null>(null)

const job = ref<UpdateStatus | null>(null)
const jobStarted = ref(false)
const pollError = ref<string | null>(null)

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFile = ref<File | null>(null)
const fileError = ref<string | null>(null)
const uploading = ref(false)
const uploadPct = ref<number | null>(null)
const uploadError = ref<string | null>(null)
const uploadNotice = ref<string | null>(null)

let pollTimer: number | null = null
let pollInFlight = false
let pollAbort: AbortController | null = null
/**
 * 本次提交的目标版本。自升级重启后进程内存里的 job 会丢失，
 * GET /status 只回 {current_version, running:false, history:[…]}，
 * 没有这份「本地记住的目标」就无法把 idle 判定成这次升级的终态。
 */
let pendingTarget: string | null = null
let uploadAbort: AbortController | null = null
let uploadSeq = 0

const latestVersion = computed(() => info.value?.latest ?? null)
const hasUpdate = computed(() => Boolean(info.value?.has_update && latestVersion.value))
const upgradable = computed(() => info.value?.upgradable !== false)
const serverUpgradeEnabled = computed(() => info.value?.upgrade_enabled !== false)
const publishedText = computed(() => formatDateTime(info.value?.published_at) || t('upgrade.noRelease'))
const notesText = computed(() => info.value?.notes?.trim() || null)
const notesHtml = computed(() => (notesText.value ? renderReleaseNotes(notesText.value) : ''))
/** 服务端 200 里的 info.error 优先（要求原样展示）；HTTP 抛错时用 errorMessage 的结果。 */
const checkFailure = computed(() => info.value?.error?.trim() || requestError.value)
const jobTerminal = computed(() => isTerminalPhase(job.value?.phase))
const jobBusy = computed(() => jobStarted.value && !jobTerminal.value)
const uploadBlocked = computed(() => uploading.value || jobBusy.value)
const phaseText = computed(() => t(phaseLabelKey(job.value?.phase)))
const phaseModifier = computed(() => `ls-upgrade__phase--${phaseTone(job.value?.phase)}`)
const progressPct = computed(() => phaseProgress(job.value?.phase, job.value?.phase_elapsed_s ?? 0))
const stepIndex = computed(() => phaseStepIndex(job.value?.phase))
const stepNumber = computed(() => (stepIndex.value >= 0 ? `${stepIndex.value + 1}/${UPGRADE_STEPS.length}` : ''))
const recentHistory = computed(() => (job.value?.history ?? []).slice(0, 3))
const progressTone = computed(() => (job.value?.phase === 'failed' || job.value?.phase === 'stalled' ? 'danger' : job.value?.phase === 'rolled_back' ? 'warning' : 'normal'))
const showStallWarning = computed(() => Boolean(job.value?.stalled) && !jobTerminal.value)
const handoff = computed(() => isHandoffPhase(job.value?.phase) && !jobTerminal.value)
const handoffLong = computed(() => handoff.value && (job.value?.phase_elapsed_s ?? 0) >= 8)
const canApply = computed(() => hasUpdate.value && upgradable.value && serverUpgradeEnabled.value)
const applyDisabled = computed(() => !canApply.value || jobBusy.value || applying.value)
const elapsedText = computed(() => {
  const seconds = job.value?.phase_elapsed_s
  return typeof seconds === 'number' && Number.isFinite(seconds) && seconds >= 0
    ? t('upgrade.elapsed', { seconds })
    : null
})
const uploadPctText = computed(() => t('upgrade.uploading', { pct: uploadPct.value ?? 0 }))

function reloadPage(): void {
  window.location.reload()
}

async function loadInfo(force = false): Promise<void> {
  if (!allowed.value) return
  if (force) checking.value = true
  else loadState.value = 'loading'
  try {
    const data = force ? await api.refreshUpdateCheck() : await api.checkForUpdate()
    info.value = data
    requestError.value = null
    loadState.value = 'ready'
    if (force) uploadNotice.value = null
  } catch (caught) {
    requestError.value = errorMessage(caught, t('upgrade.checkFailedTitle'))
    // 保留上次数据：检查失败时旧版本信息仍然有意义。
    loadState.value = info.value ? 'ready' : 'error'
  } finally {
    checking.value = false
  }
}

function stopPolling(): void {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
  pollAbort?.abort()
  pollAbort = null
}

function settleTerminal(status: UpdateStatus): void {
  uploadPct.value = null
  if (status.phase !== 'success') return
  const next = status.current_version ?? status.target_version
  if (info.value && next) {
    info.value = { ...info.value, current: next, has_update: info.value.latest !== next }
  }
}

async function pollStatus(): Promise<void> {
  if (pollInFlight) return
  pollInFlight = true
  const controller = new AbortController()
  pollAbort = controller
  const timeout = window.setTimeout(() => controller.abort(), 6_000)
  try {
    // 先记下这次提交的目标：下一轮 status 可能已经没有 job，只剩 history。
    const target = job.value?.target_version ?? pendingTarget
    const status = await api.updateStatus({ signal: controller.signal })
    // 重启后 job 丢失：后端不再返回 job，phase 会归一成 idle。若本次目标已知，
    // 用持久化的 history 判定终态，否则轮询永不停止、版本不刷新、上传永久禁用。
    if (status.phase === 'idle' && target) {
      const outcome = resolveLostJobOutcome(status, target)
      if (outcome === 'success') {
        const settled: UpdateStatus = { ...status, phase: 'success', target_version: target }
        job.value = settled
        pollError.value = null
        pendingTarget = null
        stopPolling()
        settleTerminal(settled)
        return
      }
      if (outcome === 'rolled_back') {
        job.value = { ...status, phase: 'rolled_back', rolled_back: true, target_version: target }
        pollError.value = null
        pendingTarget = null
        stopPolling()
        return
      }
    }
    job.value = status
    pollError.value = null
    // 终态必须停表：否则事件循环里永远有一个 2s 定时器在转。
    if (isTerminalPhase(status.phase)) {
      pendingTarget = null
      stopPolling()
      settleTerminal(status)
    }
  } catch (caught) {
    // 重启期间 /system/update/status 可能短暂不可达：显示错误但继续轮询，不静默。
    // 超时必须主动中断，否则浏览器连接可能挂住，pollInFlight 会永久为 true。
    pollError.value =
      caught instanceof DOMException && caught.name === 'AbortError'
        ? t('upgrade.statusTimeout')
        : errorMessage(caught, t('upgrade.pollRetry'))
  } finally {
    window.clearTimeout(timeout)
    pollAbort = null
    pollInFlight = false
  }
}

function startPolling(): void {
  stopPolling()
  void pollStatus()
  pollTimer = window.setInterval(() => {
    void pollStatus()
  }, 2_000)
}

function openConfirm(): void {
  if (!canApply.value || jobBusy.value || applying.value) return
  confirming.value = true
  applyError.value = null
}

function cancelConfirm(): void {
  confirming.value = false
}

async function submitApply(): Promise<void> {
  const version = latestVersion.value
  if (!version || applying.value || jobBusy.value) return
  applying.value = true
  applyError.value = null
  try {
    await api.applyUpdate({ version })
    confirming.value = false
    uploadNotice.value = null
    pollError.value = null
    job.value = null
    jobStarted.value = true
    // 202 只回 job_id；目标版本由本地记住，供重启后无 job 时判定终态。
    pendingTarget = version
    startPolling()
  } catch (caught) {
    applyError.value = errorMessage(caught, t('upgrade.applyFailedTitle'))
  } finally {
    applying.value = false
  }
}

function onFileChange(event: Event): void {
  const target = event.target as HTMLInputElement
  const file = target.files?.[0] ?? null
  fileError.value = null
  uploadError.value = null
  uploadNotice.value = null
  uploadPct.value = null
  if (!file) {
    selectedFile.value = null
    return
  }
  const check = validateUpdateFile(file)
  if (!check.ok) {
    selectedFile.value = null
    fileError.value =
      check.reason === 'empty'
        ? t('upgrade.fileEmpty')
        : t('upgrade.fileTooLarge', { size: formatBytes(check.size) })
    // 清空值，允许用户重新选择同一个文件。
    target.value = ''
    return
  }
  selectedFile.value = file
}

async function submitUpload(): Promise<void> {
  const file = selectedFile.value
  if (!file || uploadBlocked.value) return
  const seq = ++uploadSeq
  const controller = new AbortController()
  uploadAbort = controller
  uploading.value = true
  uploadPct.value = 0
  uploadError.value = null
  fileError.value = null
  try {
    const result = await api.uploadUpdate(file, {
      allowMajor: false,
      onProgress: (pct) => {
        if (seq === uploadSeq) uploadPct.value = Math.max(0, Math.min(100, Math.round(pct)))
      },
      signal: controller.signal,
    })
    if (seq !== uploadSeq) return
    selectedFile.value = null
    if (fileInput.value) fileInput.value.value = ''
    uploadNotice.value = t('upgrade.jobAccepted')
    job.value = result.status.phase !== 'idle' ? result.status : null
    pendingTarget = result.status.target_version ?? pendingTarget
    pollError.value = null
    jobStarted.value = true
    // 202 后与 applyUpdate 同一条轮询链路。
    startPolling()
  } catch (caught) {
    if (seq !== uploadSeq) return
    if (caught instanceof DOMException && caught.name === 'AbortError') {
      uploadNotice.value = t('upgrade.uploadAborted')
    } else {
      uploadError.value = errorMessage(caught, t('upgrade.uploadFailedTitle'))
    }
  } finally {
    if (seq === uploadSeq) {
      uploading.value = false
      uploadPct.value = null
      uploadAbort = null
    }
  }
}

function cancelUpload(): void {
  uploadAbort?.abort()
}

onMounted(() => {
  if (!allowed.value) return
  void loadInfo()
})

onBeforeUnmount(() => {
  stopPolling()
  uploadSeq += 1
  uploadAbort?.abort()
})
</script>

<template>
  <section v-if="allowed" id="upgrade-card" class="ls-card ls-settingspage__wide" :aria-busy="loadState === 'loading' || checking || jobBusy">
    <header class="ls-card__head">
      <h2 class="ls-card__title">{{ t('upgrade.title') }}</h2>
      <p class="ls-card__note">{{ t('upgrade.description') }}</p>
    </header>

    <div v-if="loadState === 'loading' && !info" id="upgrade-loading" class="ls-upgrade__loading" aria-busy="true">
      <span class="ls-spinner" aria-hidden="true" />
      {{ t('common.loading') }}
    </div>

    <div v-else-if="loadState === 'error' && !info" id="upgrade-load-error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('upgrade.checkFailedTitle') }}</p>
      <p class="ls-notice__text">{{ checkFailure }}</p>
      <button id="upgrade-reload" class="ls-notice__action" type="button" @click="loadInfo(false)">
        {{ t('common.retry') }}
      </button>
    </div>

    <template v-else>
      <dl v-if="info" class="ls-upgrade__facts">
        <dt>{{ t('upgrade.current') }}</dt>
        <dd><span id="upgrade-current" class="ls-upgrade__version">{{ info.current || t('common.na') }}</span></dd>
        <dt>{{ t('upgrade.latest') }}</dt>
        <dd>
          <span id="upgrade-latest" class="ls-upgrade__version">{{ latestVersion || t('upgrade.noRelease') }}</span>
        </dd>
        <dt>{{ t('upgrade.publishedAt') }}</dt>
        <dd id="upgrade-published">{{ publishedText }}</dd>
      </dl>

      <div v-if="checkFailure" id="upgrade-check-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('upgrade.checkFailedTitle') }}</p>
        <p id="upgrade-check-error-text" class="ls-notice__text">{{ checkFailure }}</p>
        <p class="ls-notice__text">{{ t('upgrade.uploadStillAvailable') }}</p>
      </div>

      <p v-if="info && !hasUpdate && !checkFailure" id="upgrade-up-to-date" class="ls-upgrade__hint" role="status">
        {{ t('upgrade.upToDate') }}
      </p>

      <div v-if="info && !upgradable" id="upgrade-note" class="ls-banner ls-banner--warning" role="status">
        <AppIcon name="alert-triangle" size="1rem" />
        <div class="ls-banner__body">
          <p class="ls-banner__title">{{ t('upgrade.notUpgradableTitle') }}</p>
          <p class="ls-banner__text">{{ info.upgrade_note?.trim() || t('upgrade.notUpgradableFallback') }}</p>
          <p class="ls-banner__text">{{ t('upgrade.notUpgradableHint') }}</p>
        </div>
      </div>

      <section v-if="info" class="ls-upgrade__notes-block">
        <h3 class="ls-upgrade__section-title">
          {{ t('upgrade.notes') }}
          <span v-if="info.schema_changed" id="upgrade-schema-badge" class="ls-upgrade__badge">
            {{ t('upgrade.schemaChanged') }}
          </span>
        </h3>
        <div v-if="notesHtml" id="upgrade-notes" class="ls-upgrade__notes ls-markdown" v-html="notesHtml" />
        <p v-else id="upgrade-notes" class="ls-upgrade__hint">{{ t('upgrade.noNotes') }}</p>
      </section>

      <div class="ls-upgrade__actions">
        <button
          id="upgrade-check"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="checking || jobBusy"
          @click="loadInfo(true)"
        >
          <span v-if="checking" class="ls-spinner" aria-hidden="true" />
          {{ checking ? t('upgrade.checking') : t('upgrade.check') }}
        </button>
        <button
          v-if="hasUpdate"
          id="upgrade-apply"
          class="ls-btn ls-btn--small ls-btn--primary"
          type="button"
          :disabled="applyDisabled"
          @click="openConfirm"
        >
          {{ applying ? t('upgrade.submitting') : t('upgrade.applyTo', { version: latestVersion ?? '' }) }}
        </button>
      </div>

      <p v-if="info && !serverUpgradeEnabled" id="upgrade-server-disabled" class="ls-upgrade__hint" role="status">
        {{ t('upgrade.serverDisabled') }}
      </p>

      <div v-if="confirming" id="upgrade-confirm-panel" class="ls-upgrade__confirm" role="group" :aria-label="t('upgrade.confirmTitle', { version: latestVersion ?? '' })">
        <p class="ls-upgrade__confirm-title">
          {{ t('upgrade.confirmTitle', { version: latestVersion ?? '' }) }}
        </p>
        <p class="ls-upgrade__confirm-text">
          {{ t('upgrade.confirmText', { from: info?.current ?? t('common.na'), to: latestVersion ?? '' }) }}
        </p>
        <div class="ls-upgrade__actions">
          <button
            id="upgrade-confirm"
            class="ls-btn ls-btn--small ls-btn--primary"
            type="button"
            :disabled="applying"
            @click="submitApply"
          >
            <span v-if="applying" class="ls-spinner" aria-hidden="true" />
            {{ applying ? t('upgrade.submitting') : t('upgrade.confirmApply') }}
          </button>
          <button id="upgrade-cancel" class="ls-btn ls-btn--small" type="button" :disabled="applying" @click="cancelConfirm">
            {{ t('upgrade.cancel') }}
          </button>
        </div>
      </div>

      <div v-if="applyError" id="upgrade-apply-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('upgrade.applyFailedTitle') }}</p>
        <p class="ls-notice__text">{{ applyError }}</p>
      </div>

      <div v-if="jobStarted" id="upgrade-status" class="ls-upgrade__progress" aria-live="polite">
        <p class="ls-upgrade__phase" :class="phaseModifier">
          <span id="upgrade-phase">{{ phaseText }}</span>
          <span v-if="elapsedText" id="upgrade-elapsed" class="ls-upgrade__elapsed">{{ elapsedText }}</span>
        </p>
        <div
          id="upgrade-progress"
          class="ls-upgrade__bar"
          :class="{ 'ls-upgrade__bar--handoff': handoff }"
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="progressPct"
          :aria-valuetext="`${progressPct}%`"
        >
          <div class="ls-upgrade__bar-fill" :class="`ls-upgrade__bar-fill--${progressTone}`" :style="{ width: `${progressPct}%` }" />
        </div>
        <ol class="ls-upgrade__steps" :aria-label="t('upgrade.steps')">
          <li
            v-for="(step, index) in UPGRADE_STEPS"
            :key="step.phase"
            class="ls-upgrade__step"
            :class="{
              'ls-upgrade__step--done': index < stepIndex || job?.phase === 'success',
              'ls-upgrade__step--current': index === stepIndex && job?.phase !== 'success',
              'ls-upgrade__step--failed': index === stepIndex && (job?.phase === 'failed' || job?.phase === 'stalled'),
            }"
          >
            <span class="ls-upgrade__step-dot" aria-hidden="true" />
            {{ t(step.labelKey) }}
          </li>
        </ol>
        <p v-if="job?.target_version" class="ls-upgrade__hint">
          {{ t('upgrade.target', { version: job.target_version }) }}
          <span v-if="stepNumber" class="ls-upgrade__step-number">· {{ t('upgrade.stepNumber', { step: stepNumber }) }}</span>
        </p>
        <p v-if="handoff" id="upgrade-handoff-hint" class="ls-upgrade__hint" role="status">
          {{ handoffLong ? t('upgrade.handoffLongHint') : t('upgrade.handoffHint') }}
          <button v-if="handoffLong" class="ls-btn ls-btn--small ls-upgrade__inline-action" type="button" @click="reloadPage">
            {{ t('upgrade.refreshPage') }}
          </button>
        </p>
        <p v-if="jobBusy && job?.phase === 'restarting'" class="ls-upgrade__hint">{{ t('upgrade.restartHint') }}</p>
        <p v-if="showStallWarning" id="upgrade-stalled-warning" class="ls-upgrade__warning" role="status">
          {{ t('upgrade.stalledWarning') }}
        </p>
        <p v-if="pollError" id="upgrade-poll-error" class="ls-upgrade__hint" role="status">
          {{ t('upgrade.pollRetry', { message: pollError }) }}
        </p>
        <p v-if="job?.phase === 'success'" class="ls-upgrade__hint">{{ t('upgrade.successHint') }}</p>
        <p v-if="job?.phase === 'failed'" class="ls-upgrade__hint">{{ t('upgrade.failedHint') }}</p>
        <div v-if="recentHistory.length > 0" class="ls-upgrade__history">
          <p class="ls-upgrade__history-title">{{ t('upgrade.history') }}</p>
          <ul>
            <li v-for="entry in recentHistory" :key="`${entry.ts}-${entry.target_version}`" class="ls-upgrade__history-row">
              <span class="ls-upgrade__history-outcome" :class="`ls-upgrade__history-outcome--${entry.outcome}`">
                {{ t(`upgrade.outcome.${entry.outcome}`) }}
              </span>
              <span class="ls-upgrade__history-version">{{ entry.from_version }} → {{ entry.target_version }}</span>
              <span class="ls-upgrade__history-time">{{ formatDateTime(entry.ts) }}</span>
            </li>
          </ul>
        </div>
        <div v-if="job?.phase === 'stalled'" class="ls-upgrade__actions">
          <button id="upgrade-retry" class="ls-btn ls-btn--small ls-btn--primary" type="button" @click="openConfirm">
            {{ t('upgrade.retry') }}
          </button>
        </div>
      </div>
    </template>

    <section class="ls-upgrade__section">
      <h3 class="ls-upgrade__section-title">{{ t('upgrade.uploadTitle') }}</h3>
      <p class="ls-upgrade__hint">{{ t('upgrade.uploadHint') }}</p>
      <p class="ls-upgrade__hint">{{ t('upgrade.uploadLimit') }}</p>
      <div class="ls-upgrade__upload">
        <label class="ls-upgrade__picker" :class="{ 'ls-upgrade__picker--disabled': uploadBlocked }">
          {{ t('upgrade.chooseFile') }}
          <input
            id="upgrade-file"
            ref="fileInput"
            class="ls-upgrade__file"
            type="file"
            accept=".bin,application/octet-stream,*/*"
            :disabled="uploadBlocked"
            @change="onFileChange"
          />
        </label>
        <span v-if="selectedFile" id="upgrade-selected-file" class="ls-upgrade__file-meta">
          {{ t('upgrade.fileChosen', { name: selectedFile.name, size: formatBytes(selectedFile.size) }) }}
        </span>
        <span v-else class="ls-upgrade__file-meta">{{ t('upgrade.noFile') }}</span>
      </div>
      <p id="upgrade-accept-hint" class="ls-upgrade__hint">{{ t('upgrade.uploadAcceptHint') }}</p>
      <p v-if="fileError" id="upgrade-file-error" class="ls-upgrade__file-error" role="alert">{{ fileError }}</p>

      <div v-if="uploading" id="upgrade-upload-progress" class="ls-upgrade__upload-progress">
        <div
          class="ls-upgrade__bar"
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="uploadPct ?? 0"
          :aria-valuetext="uploadPctText"
        >
          <div class="ls-upgrade__bar-fill" :style="{ width: `${uploadPct ?? 0}%` }" />
        </div>
        <span class="ls-upgrade__file-meta">{{ uploadPctText }}</span>
      </div>

      <div class="ls-upgrade__actions">
        <button
          id="upgrade-upload-submit"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="!selectedFile || uploadBlocked"
          @click="submitUpload"
        >
          {{ t('upgrade.startUpload') }}
        </button>
        <button v-if="uploading" id="upgrade-upload-cancel" class="ls-btn ls-btn--small" type="button" @click="cancelUpload">
          {{ t('upgrade.cancelUpload') }}
        </button>
      </div>

      <div v-if="uploadError" id="upgrade-upload-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('upgrade.uploadFailedTitle') }}</p>
        <p class="ls-notice__text">{{ uploadError }}</p>
      </div>
      <p v-if="uploadNotice" id="upgrade-upload-notice" class="ls-upgrade__hint" role="status">{{ uploadNotice }}</p>
    </section>
  </section>
</template>
