<script setup lang="ts">
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { CheckItem } from '@/lib/api'
import {
  availabilityText,
  availabilityTone,
  checkKindLabel,
  checkStateLabelKey,
  checkStateTone,
  enabledLabelKey,
  isProbeAgent,
  lastResultLatencyText,
  lastResultStateKey,
} from '@/lib/checks'
import { formatRelativeTime } from '@/lib/format'

/**
 * 检测列表的一行（`<tr>`，挂在 ChecksPage 的 `<tbody>` 里）。
 *
 * 纯展示 + 事件上抛：数据来自页面（唯一 owner = useChecks 组合式），
 * 这里不发请求、不存副本；「立即执行 / 启用开关 / 详情」只 emit 意图。
 */
const props = defineProps<{
  check: CheckItem
  /** 有 check.write 才允许立即执行与启用开关 */
  canWrite: boolean
  /** 正在执行（行内按钮转 spinner 防重复点击） */
  running: boolean
  /** 正在切换启用态 */
  toggling: boolean
  /**
   * 启用开关的当前值。prop 名首字母大写是与页面绑定的既定契约（`:Enabled="..."`），
   * 值直接来自后端 CheckItem.enabled，组件内不存本地副本。
   */
  Enabled: boolean
}>()

const emit = defineEmits<{
  (event: 'run'): void
  (event: 'toggle', enabled: boolean): void
  (event: 'detail'): void
}>()

const { t } = useI18n()

const stateLabel = () => t(checkStateLabelKey(props.check.state))
const stateToneClass = () => `ls-checkspage__tone ls-tone--${checkStateTone(props.check.state)}`
const availabilityValue = () => availabilityText(props.check.availability_24h)
const availabilityClass = () => `ls-tone--${availabilityTone(props.check.availability_24h)}`
const lastStateLabel = () => {
  const key = lastResultStateKey(props.check)
  return key ? t(key) : ''
}
const lastLatency = () => lastResultLatencyText(props.check)
const lastTime = () => formatRelativeTime(props.check.last_result?.ts ?? null)
</script>

<template>
  <tr :id="`check-row-${check.id}`" class="ls-checkspage__row">
    <td class="ls-checkspage__cell ls-checkspage__cell--name" :data-label="t('checks.colName')">
      <span class="ls-checkspage__name">{{ check.name }}</span>
      <span
        v-if="isProbeAgent(check)"
        class="ls-chip ls-chip--muted ls-checkspage__probe"
        :title="t('checks.probeAgentHint')"
      >
        {{ t('checks.probeAgentBadge') }}
      </span>
      <span class="ls-visually-hidden">{{ check.id }}</span>
    </td>

    <td class="ls-checkspage__cell" :data-label="t('checks.colKind')">
      <span class="ls-checkspage__kind">{{ checkKindLabel(check.kind) }}</span>
    </td>

    <td class="ls-checkspage__cell" :data-label="t('checks.colTarget')">
      <code class="ls-checkspage__target">{{ check.target }}</code>
      <span class="ls-checkspage__meta">{{ t('checks.targetCount', { count: check.server_count }) }}</span>
    </td>

    <td class="ls-checkspage__cell" :data-label="t('checks.colInterval')">
      <span class="ls-checkspage__interval">{{ check.interval_s }}s</span>
    </td>

    <td class="ls-checkspage__cell" :data-label="t('checks.colEnabled')">
      <button
        :id="`check-enabled-${check.id}`"
        class="ls-checkspage__switch"
        type="button"
        role="switch"
        :aria-checked="Enabled ? 'true' : 'false'"
        :aria-label="t('checks.toggleEnabled', { name: check.name })"
        :aria-busy="toggling ? 'true' : 'false'"
        :disabled="!canWrite || toggling"
        @click="emit('toggle', !Enabled)"
      >
        <span class="ls-checkspage__switch-track" aria-hidden="true">
          <span class="ls-checkspage__switch-dot" />
        </span>
        <span class="ls-checkspage__switch-text">{{ t(enabledLabelKey(Enabled)) }}</span>
      </button>
    </td>

    <td class="ls-checkspage__cell ls-checkspage__cell--state" :data-label="t('checks.colState')">
      <span class="ls-checkspage__state" :class="stateToneClass()" :title="t('s.0081', { 0: stateLabel() })">
        <span class="ls-checkspage__dot" aria-hidden="true" />
        {{ stateLabel() }}
      </span>
      <span v-if="check.last_result" class="ls-checkspage__last">
        <span class="ls-checkspage__last-state" :class="check.last_result.ok ? 'ls-tone--success' : 'ls-tone--danger'">
          {{ lastStateLabel() }}
        </span>
        <span class="ls-checkspage__latency">{{ lastLatency() }}</span>
        <span class="ls-checkspage__last-time">{{ lastTime() }}</span>
      </span>
      <span v-else class="ls-checkspage__last ls-tone--muted">{{ t('s.0965') }}</span>
      <span class="ls-checkspage__availability" :class="availabilityClass()">
        <span class="ls-checkspage__availability-label">{{ t('checks.availability24h') }}</span>
        <span class="ls-checkspage__availability-value">{{ availabilityValue() }}</span>
      </span>
    </td>

    <td class="ls-checkspage__cell ls-checkspage__cell--actions" :data-label="t('checks.colActions')">
      <button
        :id="`check-run-${check.id}`"
        class="ls-btn ls-btn--small ls-checkspage__run"
        type="button"
        :disabled="!canWrite || running"
        :aria-busy="running ? 'true' : 'false'"
        :aria-label="t('checks.runAria', { name: check.name })"
        @click="emit('run')"
      >
        <span v-if="running" class="ls-spinner" aria-hidden="true" />
        <AppIcon v-else name="play" size="0.875rem" />
        {{ t('checks.run') }}
      </button>
      <button
        :id="`check-detail-${check.id}`"
        class="ls-btn ls-btn--small ls-checkspage__detail"
        type="button"
        aria-haspopup="dialog"
        :aria-label="t('checks.openDetail', { name: check.name })"
        @click="emit('detail')"
      >
        {{ t('checks.detail') }}
        <AppIcon name="chevron-right" size="0.75rem" />
      </button>
    </td>
  </tr>
</template>
