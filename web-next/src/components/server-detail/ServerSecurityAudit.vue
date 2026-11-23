<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useI18n } from '@/i18n'
import { api, type SecurityAudit, type SecurityFinding } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

/**
 * 服务器安全基线体检：由 agent/SSH 通道执行只读采集脚本。
 * 不自动运行（避免切 tab 就触发远程命令），结果只保存在当前页面。
 */
const props = defineProps<{
  serverId: string
  canRun: boolean
  canHarden: boolean
}>()

const { t } = useI18n()
const audit = ref<SecurityAudit | null>(null)
const busy = ref(false)
const error = ref<string | null>(null)
const hardenOpen = ref(false)
const hardening = ref(false)
const hardenNotice = ref<string | null>(null)

const scoreTone = computed(() => {
  const score = audit.value?.score ?? 0
  if (score >= 85) return 'ok'
  if (score >= 60) return 'warn'
  return 'danger'
})

const riskCount = computed(
  () => audit.value?.findings.filter((finding) => finding.level !== 'ok').length ?? 0,
)

const sortedFindings = computed<SecurityFinding[]>(() => {
  const rank: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, ok: 4 }
  return [...(audit.value?.findings ?? [])].sort(
    (left, right) => (rank[left.level] ?? 9) - (rank[right.level] ?? 9),
  )
})

async function harden(): Promise<void> {
  if (!props.canHarden || hardening.value) return
  hardening.value = true
  error.value = null
  hardenNotice.value = null
  try {
    const result = await api.securityHarden(props.serverId)
    hardenNotice.value =
      result.changed.length === 0 ? t('security.hardenNoop') : t('security.hardenDone', { count: result.changed.length })
    hardenOpen.value = false
    await run()
  } catch (caught) {
    error.value = errorMessage(caught, t('security.hardenFailed'))
  } finally {
    hardening.value = false
  }
}

async function run(): Promise<void> {
  if (!props.canRun || busy.value) return
  busy.value = true
  error.value = null
  try {
    audit.value = await api.securityAudit(props.serverId)
  } catch (caught) {
    error.value = errorMessage(caught, t('security.runFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section id="server-security-audit" class="ls-card" :aria-label="t('security.title')">
    <header class="ls-card__head">
      <div class="ls-card__heading">
        <h2 class="ls-card__title">
          <AppIcon name="shield-check" size="1rem" />
          {{ t('security.title') }}
        </h2>
        <p class="ls-card__subtitle">{{ t('security.hint') }}</p>
      </div>
      <div class="ls-card__actions">
        <span v-if="audit" class="ls-chip" :class="`ls-chip--${scoreTone}`">
          {{ t('security.score', { score: audit.score }) }}
        </span>
        <button
          v-if="canRun && canHarden"
          id="security-harden"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="hardening"
          @click="hardenOpen = true"
        >
          <AppIcon name="shield-check" size="0.875rem" />
          {{ t('security.harden') }}
        </button>
        <button
          v-if="canRun"
          id="security-audit-run"
          class="ls-btn ls-btn--small ls-btn--primary"
          type="button"
          :disabled="busy"
          :aria-busy="busy"
          @click="run"
        >
          <span v-if="busy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="shield-check" size="0.875rem" />
          {{ busy ? t('security.running') : t('security.run') }}
        </button>
      </div>
    </header>

    <p v-if="!canRun" class="ls-field__hint">{{ t('security.noPermission') }}</p>
    <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
    <p v-if="hardenNotice" class="ls-ok-hint" role="status">{{ hardenNotice }}</p>

    <div v-if="!audit && !busy" class="ls-empty" role="status">
      <AppIcon name="shield-check" size="1.25rem" />
      <p class="ls-empty__title">{{ t('security.emptyTitle') }}</p>
      <p class="ls-empty__text">{{ t('security.emptyHint') }}</p>
    </div>

    <template v-else-if="audit">
      <div class="ls-security-summary">
        <div class="ls-security-score" :class="`ls-security-score--${scoreTone}`">
          <span class="ls-security-score__value">{{ audit.score }}</span>
          <span class="ls-security-score__label">{{ t('security.scoreLabel') }}</span>
        </div>
        <div class="ls-security-summary__body">
          <p class="ls-security-summary__title">
            {{ riskCount === 0 ? t('security.allGood') : t('security.riskCount', { count: riskCount }) }}
          </p>
          <p class="ls-security-summary__time">
            {{ t('security.checkedAt', { time: formatDateTime(audit.checked_at) }) }}
          </p>
        </div>
      </div>

      <ul class="ls-security-list">
        <li v-for="finding in sortedFindings" :key="finding.id" class="ls-security-item" :class="`ls-security-item--${finding.level}`">
          <span class="ls-security-item__dot" aria-hidden="true" />
          <div class="ls-security-item__body">
            <p class="ls-security-item__title">
              {{ finding.title }}
              <span class="ls-chip ls-chip--muted">{{ t(`security.level.${finding.level}`) }}</span>
            </p>
            <p class="ls-security-item__detail">{{ finding.detail }}</p>
            <p v-if="finding.recommendation" class="ls-security-item__fix">
              {{ t('security.recommendation') }}：{{ finding.recommendation }}
            </p>
          </div>
        </li>
      </ul>
    </template>

    <AppModal
      v-if="hardenOpen"
      :title="t('security.hardenConfirmTitle')"
      :description="t('security.hardenConfirmText')"
      size="sm"
      @close="hardenOpen = false"
    >
      <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="hardening" @click="hardenOpen = false">{{ t('common.cancel') }}</button>
        <button class="ls-btn ls-btn--primary" type="button" :disabled="hardening" :aria-busy="hardening" @click="harden">
          {{ hardening ? t('security.hardening') : t('security.hardenConfirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
