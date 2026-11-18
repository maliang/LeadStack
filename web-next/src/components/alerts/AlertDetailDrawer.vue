<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import { api, type AlertItem } from '@/lib/api'
import {
  alertTime,
  eventKindText,
  ruleText,
  serverText,
  severityLabelKey,
  severityTone,
  statusLabelKey,
  statusTone,
} from '@/lib/alerts'
import { errorMessage } from '@/lib/errors'

/**
 * 单条告警详情抽屉：规则信息 + 最近事件轨迹。
 *
 * 异步读取的 owner 就是这个组件（一开一关即一次读取，不跟列表轮询耦合）：
 * 加载 / 失败可重试 / 无事件 / 有数据四态齐全，失败文案走 errorMessage()（带 requestId）。
 *
 * 挂在 document.body 上的模态：Esc 关闭、打开时焦点进入面板、关闭后由页面把焦点
 * 还给触发按钮（页面持有 trigger，见 AlertsPage.closeDrawer）。
 */
const props = defineProps<{
  /** 列表里的那条告警：先用它渲染已知字段，详情回来后整体替换 */
  alert: AlertItem
}>()

const emit = defineEmits<{
  (event: 'close'): void
}>()

const { t } = useI18n()

const detail = ref<AlertItem | null>(null)
const loading = ref(false)
const loadError = ref('')
const panel = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
const now = ref(Date.now())

let clock: ReturnType<typeof setInterval> | null = null

const titleId = computed(() => `alert-drawer-title-${props.alert.id}`)
const current = computed<AlertItem>(() => detail.value ?? props.alert)
const severityText = computed(() => t(severityLabelKey(current.value.severity)))
const severityClass = computed(() => `ls-tone--${severityTone(current.value.severity)}`)
const statusText = computed(() => t(statusLabelKey(current.value.status)))
const statusClass = computed(() => `ls-tone--${statusTone(current.value.status)}`)
const started = computed(() => alertTime(current.value.started_at, now.value))
const updated = computed(() => alertTime(current.value.updated_at, now.value))
const resolved = computed(() => alertTime(current.value.resolved_at, now.value))
const acked = computed(() => alertTime(current.value.acked_at, now.value))
const events = computed(() => current.value.events ?? [])

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    detail.value = await api.alert(props.alert.id)
  } catch (caught) {
    loadError.value = errorMessage(caught, t('alerts.detailFailed'))
  } finally {
    loading.value = false
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    emit('close')
    return
  }
  // 轻量焦点陷阱：Tab 在面板内循环，避免焦点跑到背后的页面上
  if (event.key !== 'Tab' || !panel.value) return
  const focusable = panel.value.querySelectorAll<HTMLElement>(
    'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )
  if (focusable.length === 0) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  } else if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  }
}

onMounted(() => {
  void load()
  closeButton.value?.focus()
  document.addEventListener('keydown', onKeydown)
  clock = setInterval(() => {
    now.value = Date.now()
  }, 1_000)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  if (clock !== null) {
    clearInterval(clock)
    clock = null
  }
})

watch(
  () => props.alert.id,
  () => {
    detail.value = null
    void load()
  },
)
</script>

<template>
  <Teleport to="body">
    <div class="ls-alertspage-drawer">
      <div class="ls-alertspage-drawer__backdrop" @click="emit('close')" />
      <div
        ref="panel"
        class="ls-alertspage-drawer__panel"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        data-testid="alert-drawer"
      >
        <header class="ls-alertspage-drawer__head">
          <div class="ls-alertspage-drawer__heading">
            <h2 :id="titleId" class="ls-alertspage-drawer__title">{{ current.title }}</h2>
            <p class="ls-alertspage-drawer__subtitle">{{ ruleText(current) }}</p>
          </div>
          <button
            id="alert-drawer-close"
            ref="closeButton"
            class="ls-icon-btn"
            type="button"
            :aria-label="t('common.close')"
            @click="emit('close')"
          >
            <AppIcon name="close" size="1rem" />
          </button>
        </header>

        <dl class="ls-alertspage-drawer__meta">
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.colSeverity') }}</dt>
            <dd>
              <span class="ls-alertspage__badge" :class="severityClass">{{ severityText }}</span>
            </dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.colStatus') }}</dt>
            <dd>
              <span class="ls-alertspage__badge" :class="statusClass">{{ statusText }}</span>
            </dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.colRule') }}</dt>
            <dd class="ls-alertspage-drawer__value">{{ ruleText(current) }}</dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.colServer') }}</dt>
            <dd class="ls-alertspage-drawer__value">{{ serverText(current) }}</dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailCheck') }}</dt>
            <dd class="ls-alertspage-drawer__value">{{ current.check_name || current.check_id || '—' }}</dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailValue') }}</dt>
            <dd class="ls-alertspage-drawer__value">
              {{ current.value === null || current.value === undefined ? '—' : current.value }}
            </dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.colStarted') }}</dt>
            <dd class="ls-alertspage-drawer__value">
              <span :title="started.absolute">{{ started.absolute }}</span>
              <span class="ls-alertspage__relative">{{ started.relative }}</span>
            </dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailUpdated') }}</dt>
            <dd class="ls-alertspage-drawer__value">
              <span :title="updated.absolute">{{ updated.absolute }}</span>
              <span class="ls-alertspage__relative">{{ updated.relative }}</span>
            </dd>
          </div>
          <div v-if="current.resolved_at" class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailResolved') }}</dt>
            <dd class="ls-alertspage-drawer__value">
              <span :title="resolved.absolute">{{ resolved.absolute }}</span>
              <span class="ls-alertspage__relative">{{ resolved.relative }}</span>
            </dd>
          </div>
          <div v-if="current.acked_at" class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailAcked') }}</dt>
            <dd class="ls-alertspage-drawer__value">
              <span :title="acked.absolute">{{ acked.absolute }}</span>
              <span class="ls-alertspage__relative">{{ acked.relative }}</span>
              <span v-if="current.acked_by" class="ls-alertspage__relative">
                {{ t('alerts.detailAckedBy', { by: current.acked_by }) }}
              </span>
            </dd>
          </div>
          <div class="ls-alertspage-drawer__meta-item">
            <dt>{{ t('alerts.detailNotifyCount') }}</dt>
            <dd class="ls-alertspage-drawer__value">{{ current.notify_count }}</dd>
          </div>
        </dl>

        <p v-if="current.message" class="ls-alertspage-drawer__message">{{ current.message }}</p>

        <section class="ls-alertspage-drawer__section" :aria-busy="loading ? 'true' : 'false'">
          <h3 class="ls-alertspage-drawer__section-title">{{ t('alerts.detailEvents') }}</h3>

          <div v-if="loading" class="ls-alertspage-drawer__skeleton" aria-hidden="true">
            <span v-for="index in 3" :key="index" class="ls-skeleton ls-alertspage-drawer__skeleton-item" />
          </div>

          <div v-else-if="loadError" id="alert-drawer-error" class="ls-notice ls-notice--error" role="alert">
            <p class="ls-notice__title">{{ t('common.error') }}</p>
            <p class="ls-notice__text">{{ loadError }}</p>
            <button id="alert-drawer-retry" class="ls-notice__action" type="button" @click="load">
              {{ t('common.retry') }}
            </button>
          </div>

          <div v-else-if="events.length === 0" id="alert-drawer-events-empty" class="ls-notice">
            <p class="ls-notice__title">{{ t('alerts.detailEventsEmptyTitle') }}</p>
            <p class="ls-notice__text">{{ t('alerts.detailEventsEmptyHint') }}</p>
          </div>

          <ol v-else class="ls-alertspage-drawer__events">
            <li v-for="(event, index) in events" :key="`${event.id ?? index}`" class="ls-alertspage-drawer__event">
              <span class="ls-alertspage-drawer__event-kind">{{ eventKindText(event.kind) }}</span>
              <span class="ls-alertspage-drawer__event-time" :title="alertTime(event.ts, now).absolute">
                {{ alertTime(event.ts, now).absolute }}
                <span class="ls-alertspage__relative">{{ alertTime(event.ts, now).relative }}</span>
              </span>
              <span v-if="event.detail" class="ls-alertspage-drawer__event-detail">{{ event.detail }}</span>
            </li>
          </ol>
        </section>
      </div>
    </div>
  </Teleport>
</template>
