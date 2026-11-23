<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import AppIcon from '@/components/AppIcon.vue'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { useI18n } from '@/i18n'

/**
 * 可复用日志面板：详情页「日志」tab。
 *
 * 接口沿用旧界面（web/src/components/server/ServerLogsTab.tsx）：
 *   GET /servers/{id}/logs?path=&lines=&grep= —— 服务端按行数上限读文件末尾并按关键字过滤。
 *
 * 取舍（按旧实现，不自己发明端点）：
 *   - 旧实现只有「路径 / 行数 / grep / 手动刷新 / 5s 自动刷新」，没有下载与清屏；
 *     下载需要 /files/download 语义且日志可能是 root 权限文件，清屏只是前端清空列表、
 *     下一轮轮询又回来，两者都没有真实价值，因此不做。
 *   - 自动刷新默认关闭：详情页首屏不该替用户持续对目标机产生 IO（旧实现默认开，
 *     这里按本次任务要求改为默认关）。
 *   - 自动刷新只在一个定时器里读当前输入值，不在 path/lines/grep 变化时重建定时器；
 *     标签页不可见时跳过本轮，避免后台无意义请求。
 */
const props = defineProps<{
  serverId: string
  /** 当前会话是否有 terminal.use；缺权限时不发任何请求 */
  allowed: boolean
}>()

const { t } = useI18n()

/** 空串 = auto：由控制端在目标机现场探测（常见日志文件 → journalctl）。 */
const path = ref('')
const resolvedPath = ref('')
const lines = ref('200')
const grep = ref('')
const auto = ref(false)
const output = ref<string[]>([])
const truncated = ref(false)
const loading = ref(false)
const error = ref<string | null>(null)

let requests = 0
let timer: number | null = null

const sourceOptions = computed(() => [
  { value: '', labelKey: 'logs.sourceAuto' },
  { value: '/var/log/syslog', labelKey: 'logs.sourceSyslog' },
  { value: '/var/log/messages', labelKey: 'logs.sourceMessages' },
  { value: '/var/log/system.log', labelKey: 'logs.sourceSystemLog' },
  { value: '/var/log/auth.log', labelKey: 'logs.sourceAuth' },
  { value: 'journal', labelKey: 'logs.sourceJournal' },
])

const sourceLabel = computed(() => {
  if (resolvedPath.value === 'journal') return t('logs.resolvedJournal')
  return resolvedPath.value || path.value || t('logs.sourceAuto')
})

function selectSource(): void {
  resolvedPath.value = ''
  void load()
}

function useAuto(): void {
  path.value = ''
  selectSource()
}

async function load(): Promise<void> {
  if (!props.allowed) return
  const target = path.value.trim()
  const current = ++requests
  loading.value = true
  error.value = null
  try {
    const result = await api.serverLogs(props.serverId, {
      path: target || 'auto',
      lines: Number(lines.value) || 200,
      grep: grep.value.trim() || undefined,
    })
    if (current !== requests) return
    output.value = result.lines
    truncated.value = result.truncated
    resolvedPath.value = result.path
  } catch (caught) {
    if (current !== requests) return
    error.value = errorMessage(caught, t('logs.loadFailed'))
  } finally {
    if (current === requests) loading.value = false
  }
}

function stopTimer(): void {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}

function syncTimer(): void {
  stopTimer()
  if (!props.allowed || !auto.value) return
  timer = window.setInterval(() => {
    if (document.visibilityState === 'visible') void load()
  }, 5_000)
}

// serverId 变了要丢弃上一台的输出，否则会把 A 的日志挂在 B 的标题下。
watch(
  () => props.serverId,
  () => {
    requests += 1
    output.value = []
    truncated.value = false
    error.value = null
    resolvedPath.value = ''
    void load()
  },
)

watch([auto, () => props.allowed], syncTimer)

onMounted(() => {
  if (!props.allowed) return
  void load()
})

onBeforeUnmount(() => {
  requests += 1
  stopTimer()
})
</script>

<template>
  <div v-if="!allowed" class="ls-banner ls-banner--warning" role="status">
    <div class="ls-banner__body">
      <p class="ls-banner__title">{{ t('server.detail.needTerminal') }}</p>
      <p class="ls-banner__text">{{ t('server.detail.needTerminalHint') }}</p>
    </div>
  </div>

  <div v-else class="ls-logs">
    <div class="ls-logs__bar">
      <div class="ls-field ls-logs__path">
        <label class="ls-field__label" for="server-logs-path">{{ t('logs.path') }}</label>
        <input
          id="server-logs-path"
          v-model="path"
          class="ls-input ls-logs__mono"
          type="text"
          :placeholder="t('logs.pathPlaceholder')"
          autocomplete="off"
          spellcheck="false"
          @input="resolvedPath = ''"
          @keydown.enter="load"
        />
      </div>
      <div class="ls-field ls-logs__preset">
        <label class="ls-field__label" for="server-logs-preset">{{ t('logs.preset') }}</label>
        <AppSelect id="server-logs-preset" class="ls-input" :value="path" @change="path = ($event.target as HTMLSelectElement).value; selectSource()">
          <option v-for="option in sourceOptions" :key="option.value" :value="option.value">
            {{ t(option.labelKey) }}
          </option>
        </AppSelect>
      </div>
      <div class="ls-field ls-logs__lines">
        <label class="ls-field__label" for="server-logs-lines">{{ t('logs.lines') }}</label>
        <AppSelect id="server-logs-lines" v-model="lines" class="ls-input">
          <option v-for="option in ['100', '200', '500', '1000']" :key="option" :value="option">
            {{ option }}
          </option>
        </AppSelect>
      </div>
      <div class="ls-field ls-logs__grep">
        <label class="ls-field__label" for="server-logs-grep">grep</label>
        <input
          id="server-logs-grep"
          v-model="grep"
          class="ls-input ls-logs__mono"
          type="text"
          :placeholder="t('logs.grepPlaceholder')"
          autocomplete="off"
          spellcheck="false"
          @keydown.enter="load"
        />
      </div>
      <div class="ls-logs__actions">
        <button class="ls-btn ls-btn--small" type="button" :disabled="loading" @click="load">
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
        <label class="ls-logs__auto">
          <input v-model="auto" type="checkbox" />
          {{ t('logs.autoRefresh') }}
        </label>
      </div>
    </div>

    <div v-if="error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('logs.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <p class="ls-notice__text">{{ t('logs.sourceHint') }}</p>
      <button class="ls-notice__action" type="button" @click="load">{{ t('common.retry') }}</button>
      <button v-if="path !== ''" class="ls-notice__action" type="button" @click="useAuto">
        {{ t('logs.useAuto') }}
      </button>
    </div>

    <div v-else class="ls-logs__output">
      <div class="ls-logs__meta">
        <span class="ls-logs__file" :title="sourceLabel">{{ sourceLabel }}</span>
        <span class="ls-logs__stats">
          <span v-if="truncated" class="ls-logs__truncated">{{ t('logs.truncated') }}</span>
          <span class="ls-logs__count">{{ t('logs.lineCount', { count: output.length }) }}</span>
        </span>
      </div>
      <pre class="ls-logs__pre" :aria-busy="loading">{{ output.join('\n') }}</pre>
      <p v-if="output.length === 0" class="ls-logs__empty">
        {{ loading ? t('common.loading') : t('logs.empty') }}
      </p>
    </div>

    <p class="ls-logs__hint">{{ t('logs.footerHint') }}</p>
  </div>
</template>
