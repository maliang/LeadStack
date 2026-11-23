<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'

import AppIcon from '@/components/AppIcon.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type AuditLogEntry } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'

interface RangeOption {
  value: string
  labelKey: string
  ms: number
}

const PAGE_SIZE = 50

/** 与旧界面一致的动作清单；后端没有字典接口，固定枚举按契约列出。 */
const ACTION_OPTIONS = [
  'auth.login',
  'auth.logout',
  'server.create',
  'server.update',
  'server.delete',
  'server.batch',
  'credential.create',
  'credential.delete',
  'check.create',
  'check.update',
  'check.delete',
  'alert_rule.create',
  'alert_rule.update',
  'alert_rule.delete',
  'alert.ack',
  'app.create',
  'template.create',
  'release.create',
  'release.approve',
  'release.reject',
  'release.cancel',
  'backup.create',
  'backup.delete',
  'backup.restore',
  'user.create',
  'user.update',
  'user.delete',
  'token.create',
  'token.delete',
  'terminal.open',
  'terminal.close',
  'agent.upgrade',
  'import.servers',
]

const RANGES: RangeOption[] = [
  { value: '1h', labelKey: 'audit.range.1h', ms: 3_600_000 },
  { value: '24h', labelKey: 'audit.range.24h', ms: 86_400_000 },
  { value: '7d', labelKey: 'audit.range.7d', ms: 604_800_000 },
  { value: '30d', labelKey: 'audit.range.30d', ms: 2_592_000_000 },
  { value: 'all', labelKey: 'audit.range.all', ms: 0 },
]

/** 时间窗 → 毫秒时间戳；「全部时间」返回 undefined，不传 from。 */
function rangeFromSeconds(value: string, now: number): number | undefined {
  const range = RANGES.find((item) => item.value === value)
  if (!range || range.ms === 0) return undefined
  return now - range.ms
}

const { t } = useI18n()
const { can } = useCan()
const canRead = computed(() => can('audit.read'))

const state = ref<'loading' | 'ready' | 'error'>('loading')
const entries = ref<AuditLogEntry[]>([])
const error = ref<string | null>(null)
const loadingMore = ref(false)
const nextCursor = ref<string | null>(null)
const hasMore = ref(false)

const actorFilter = ref('')
const actionFilter = ref('')
const rangeFilter = ref('24h')
const expanded = ref<Set<number>>(new Set())

const loaded = computed(() => state.value !== 'loading')
const filtersActive = computed(
  () => actorFilter.value.trim() !== '' || actionFilter.value !== '' || rangeFilter.value !== '24h',
)

let debounceTimer: ReturnType<typeof setTimeout> | null = null
let mounted = false

function buildFilters(cursor?: string) {
  const filters: {
    actor?: string
    action?: string
    from?: number
    limit?: number
    cursor?: string
  } = { limit: PAGE_SIZE }
  const actor = actorFilter.value.trim()
  if (actor !== '') filters.actor = actor
  if (actionFilter.value !== '') filters.action = actionFilter.value
  const from = rangeFromSeconds(rangeFilter.value, Date.now())
  if (from !== undefined) filters.from = from
  if (cursor) filters.cursor = cursor
  return filters
}

async function load(): Promise<void> {
  state.value = 'loading'
  expanded.value = new Set()
  try {
    const result = await api.auditLogs(buildFilters())
    entries.value = result.items
    nextCursor.value = result.page?.next_cursor ?? null
    hasMore.value = result.page?.has_more ?? false
    state.value = 'ready'
    error.value = null
  } catch (caught) {
    error.value = errorMessage(caught, t('audit.loadFailed'))
    state.value = 'error'
  }
}

async function loadMore(): Promise<void> {
  if (!nextCursor.value || loadingMore.value) return
  loadingMore.value = true
  try {
    const result = await api.auditLogs(buildFilters(nextCursor.value))
    entries.value = [...entries.value, ...result.items]
    nextCursor.value = result.page?.next_cursor ?? null
    hasMore.value = result.page?.has_more ?? false
  } catch (caught) {
    error.value = errorMessage(caught, t('audit.loadFailed'))
  } finally {
    loadingMore.value = false
  }
}

function scheduleReload(): void {
  if (!mounted) return
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    debounceTimer = null
    void load()
  }, 200)
}

function clearFilters(): void {
  actorFilter.value = ''
  actionFilter.value = ''
  rangeFilter.value = '24h'
}

function toggleDetail(entry: AuditLogEntry): void {
  const next = new Set(expanded.value)
  if (next.has(entry.id)) next.delete(entry.id)
  else next.add(entry.id)
  expanded.value = next
}

function detailText(entry: AuditLogEntry): string {
  if (!entry.detail) return t('audit.noDetail')
  try {
    return JSON.stringify(JSON.parse(entry.detail), null, 2)
  } catch {
    return entry.detail
  }
}

function statusTone(entry: AuditLogEntry): string {
  const code = entry.status_code ?? 0
  if (code >= 500) return 'ls-adminpage__status--locked'
  if (code >= 400) return 'ls-adminpage__status--disabled'
  return 'ls-adminpage__status--active'
}

watch([actorFilter, actionFilter, rangeFilter], scheduleReload)

onMounted(() => {
  mounted = true
  if (canRead.value) void load()
})

onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
})
</script>

<template>
  <section class="ls-adminpage ls-auditpage">
    <div v-if="!canRead" id="audit-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('authz.noPermission') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('audit.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('audit.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <span class="ls-chip ls-chip--muted">{{ t('ops.audit.readOnly') }}</span>
          <button id="audit-refresh" class="ls-btn" type="button" :disabled="state === 'loading'" @click="load()">
            <AppIcon name="refresh" size="0.875rem" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </header>

      <div class="ls-adminpage__toolbar">
        <input
          id="audit-actor"
          class="ls-input ls-adminpage__search"
          type="search"
          autocomplete="off"
          :placeholder="t('audit.filterActor')"
          :aria-label="t('audit.filterActor')"
          :value="actorFilter"
          @input="actorFilter = ($event.target as HTMLInputElement).value"
        />
        <AppSelect
          id="audit-action"
          class="ls-input"
          :aria-label="t('audit.filterAction')"
          :value="actionFilter"
          @change="actionFilter = ($event.target as HTMLSelectElement).value"
        >
          <option value="">{{ t('audit.allActions') }}</option>
          <option v-for="action in ACTION_OPTIONS" :key="action" :value="action">{{ action }}</option>
        </AppSelect>
        <AppSelect
          id="audit-range"
          class="ls-input"
          :aria-label="t('audit.filterTime')"
          :value="rangeFilter"
          @change="rangeFilter = ($event.target as HTMLSelectElement).value"
        >
          <option v-for="range in RANGES" :key="range.value" :value="range.value">
            {{ t(range.labelKey) }}
          </option>
        </AppSelect>
        <span id="audit-count" class="ls-adminpage__count" aria-live="polite">
          {{ t('audit.count', { count: entries.length }) }}
        </span>
        <button
          v-if="filtersActive"
          id="audit-clear-filters"
          class="ls-btn ls-btn--small"
          type="button"
          @click="clearFilters"
        >
          {{ t('ops.audit.clearFilters') }}
        </button>
      </div>

      <div
        v-if="state === 'loading' && !loaded"
        id="audit-skeleton"
        class="ls-adminpage__skeleton"
        aria-busy="true"
        :aria-label="t('common.loading')"
      >
        <span v-for="index in 6" :key="index" class="ls-skeleton ls-adminpage__skeleton-item" />
      </div>

      <div v-else-if="state === 'error' && entries.length === 0" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('audit.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button id="audit-retry" class="ls-notice__action" type="button" @click="load()">
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="entries.length === 0" id="audit-empty" class="ls-notice">
        <p class="ls-notice__title">{{ t('audit.emptyTitle') }}</p>
        <p class="ls-notice__text">{{ t('audit.emptyDescription') }}</p>
      </div>

      <template v-else>
        <div v-if="error && state === 'ready'" class="ls-notice ls-notice--error" role="alert">
          <p class="ls-notice__title">{{ t('common.error') }}</p>
          <p class="ls-notice__text">{{ error }}</p>
          <button class="ls-notice__action" type="button" @click="loadMore()">{{ t('common.retry') }}</button>
        </div>

        <div class="ls-adminpage__scroll">
          <table class="ls-adminpage__table">
            <caption class="ls-visually-hidden">{{ t('audit.title') }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t('audit.time') }}</th>
                <th scope="col">{{ t('audit.actor') }}</th>
                <th scope="col">{{ t('audit.action') }}</th>
                <th scope="col">{{ t('audit.resource') }}</th>
                <th scope="col">{{ t('audit.result') }}</th>
                <th scope="col">{{ t('audit.ip') }}</th>
                <th scope="col">{{ t('common.details') }}</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="entry in entries" :key="entry.id">
                <tr class="ls-adminpage__row ls-auditpage__row" :data-audit-id="entry.id">
                  <td :data-label="t('audit.time')" class="ls-adminpage__date">{{ formatDateTime(entry.ts) }}</td>
                  <td :data-label="t('audit.actor')">
                    <div class="ls-adminpage__primary">{{ entry.actor }}</div>
                    <div v-if="entry.actor_role" class="ls-adminpage__meta">{{ entry.actor_role }}</div>
                  </td>
                  <td :data-label="t('audit.action')"><code class="ls-auditpage__action">{{ entry.action }}</code></td>
                  <td :data-label="t('audit.resource')">
                    <div>{{ entry.resource_type || t('common.na') }}</div>
                    <div class="ls-adminpage__meta">{{ entry.resource_id || '' }}</div>
                  </td>
                  <td :data-label="t('audit.result')">
                    <span class="ls-chip ls-adminpage__status" :class="statusTone(entry)">
                      {{ entry.status_code ?? t('common.na') }}
                    </span>
                  </td>
                  <td :data-label="t('audit.ip')" class="ls-adminpage__date">{{ entry.ip || t('common.na') }}</td>
                  <td :data-label="t('common.details')" class="ls-adminpage__cell-actions">
                    <button
                      class="ls-btn ls-btn--small ls-auditpage__expand"
                      type="button"
                      :aria-expanded="expanded.has(entry.id)"
                      :aria-label="expanded.has(entry.id) ? t('ops.audit.collapse') : t('ops.audit.expand')"
                      @click="toggleDetail(entry)"
                    >
                      {{ expanded.has(entry.id) ? t('ops.audit.collapse') : t('common.details') }}
                    </button>
                  </td>
                </tr>
                <tr v-if="expanded.has(entry.id)" class="ls-auditpage__detail">
                  <td colspan="7">
                    <dl class="ls-auditpage__dl">
                      <dt>{{ t('audit.methodPath') }}</dt>
                      <dd><code>{{ entry.method || '—' }} {{ entry.path || '' }}</code></dd>
                      <dt>{{ t('audit.userAgent') }}</dt>
                      <dd>{{ entry.user_agent || t('common.na') }}</dd>
                      <dt>{{ t('common.details') }}</dt>
                      <dd><pre class="ls-auditpage__json">{{ detailText(entry) }}</pre></dd>
                    </dl>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <div v-if="hasMore" class="ls-adminpage__pager">
          <button
            id="audit-load-more"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="loadingMore"
            @click="loadMore()"
          >
            <span v-if="loadingMore" class="ls-spinner" aria-hidden="true" />
            {{ t('audit.loadMore') }}
          </button>
        </div>
      </template>
    </template>
  </section>
</template>
