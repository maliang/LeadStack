<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type CheckItem, type CheckKind, type CheckParams, type Server } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { checkKindLabel } from '@/lib/checks'

/**
 * 检测项新建 / 编辑弹窗。
 *
 * 参数按类型分组：tcp/http/ping 共享探测通道，http 追加状态码与关键字，
 * process 追加最小进程数，script 追加期望输出。未展示的 params 键原样保留
 * （编辑时以现有 params 为底），不会把后端存的其它字段抹掉。
 *
 * 目标服务器来自 GET /servers；没有 server.read 权限时只提示「无法选择目标」，
 * 检测项仍可保存（后端允许 server_ids 为空，稍后由有权限的角色补目标）。
 */
const props = withDefaults(defineProps<{ check?: CheckItem | null }>(), { check: null })

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', check: CheckItem): void
}>()

const { t } = useI18n()
const { can } = useCan()

const editing = computed(() => props.check !== null)
const canPickTargets = computed(() => can('server.read'))
const supportsProbe = computed(() => ['tcp', 'http', 'ping'].includes(form.kind))

const KINDS: CheckKind[] = ['tcp', 'http', 'ping', 'process', 'script']

const form = reactive({
  name: '',
  kind: 'http' as CheckKind,
  target: '',
  intervalS: '60',
  timeoutS: '5',
  probeVia: 'control' as 'control' | 'agent',
  expectStatus: '',
  keyword: '',
  minCount: '',
  expectStdout: '',
  enabled: true,
  serverIds: [] as string[],
})

const servers = ref<Server[]>([])
const serversState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const serversError = ref<string | null>(null)

const busy = ref(false)
const error = ref<string | null>(null)
const fieldErrors = ref<Record<string, string>>({})

const targetPlaceholder = computed(() => {
  if (form.kind === 'http') return 'https://example.com/healthz'
  if (form.kind === 'tcp') return '10.0.0.11:22'
  if (form.kind === 'ping') return '10.0.0.11'
  if (form.kind === 'process') return 'nginx'
  return 'systemctl is-active nginx'
})

function hydrate(check: CheckItem): void {
  form.name = check.name
  form.kind = (KINDS.includes(check.kind as CheckKind) ? check.kind : 'http') as CheckKind
  form.target = check.target
  form.intervalS = String(check.interval_s)
  form.timeoutS = String(check.timeout_s)
  form.probeVia = check.params.probe_via === 'agent' ? 'agent' : 'control'
  form.expectStatus = typeof check.params.expect_status === 'string' ? check.params.expect_status : ''
  form.keyword = typeof check.params.keyword === 'string' ? check.params.keyword : ''
  form.minCount = check.params.min_count === undefined ? '' : String(check.params.min_count)
  form.expectStdout = typeof check.params.expect_stdout === 'string' ? check.params.expect_stdout : ''
  form.enabled = check.enabled
  form.serverIds = [...check.server_ids]
}

async function loadServers(): Promise<void> {
  if (!canPickTargets.value || serversState.value === 'loading' || serversState.value === 'ready') return
  serversState.value = 'loading'
  serversError.value = null
  try {
    servers.value = await api.servers()
    serversState.value = 'ready'
  } catch (caught) {
    serversState.value = 'error'
    serversError.value = errorMessage(caught, t('common.error'))
  }
}

function toggleServer(id: string): void {
  form.serverIds = form.serverIds.includes(id)
    ? form.serverIds.filter((item) => item !== id)
    : [...form.serverIds, id]
}

function buildParams(): CheckParams {
  const params: CheckParams = { ...(props.check?.params ?? {}) }
  if (supportsProbe.value) params.probe_via = form.probeVia
  else delete params.probe_via
  if (form.kind === 'http') {
    if (form.expectStatus.trim()) params.expect_status = form.expectStatus.trim()
    else delete params.expect_status
    if (form.keyword.trim()) params.keyword = form.keyword.trim()
    else delete params.keyword
  }
  if (form.kind === 'process') {
    const value = Number(form.minCount)
    if (Number.isInteger(value) && value > 0) params.min_count = value
    else delete params.min_count
  }
  if (form.kind === 'script') {
    if (form.expectStdout.trim()) params.expect_stdout = form.expectStdout.trim()
    else delete params.expect_stdout
  }
  return params
}

function validate(): boolean {
  const next: Record<string, string> = {}
  if (form.name.trim() === '') next.name = t('checks.form.errorName')
  if (form.target.trim() === '') next.target = t('checks.form.errorTarget')
  const interval = Number(form.intervalS)
  if (!Number.isInteger(interval) || interval < 5) next.interval = t('checks.form.errorInterval')
  const timeout = Number(form.timeoutS)
  if (!Number.isInteger(timeout) || timeout < 1) next.timeout = t('checks.form.errorTimeout')
  if (timeout >= interval) next.timeout = t('checks.form.errorTimeoutOrder')
  fieldErrors.value = next
  return Object.keys(next).length === 0
}

async function submit(): Promise<void> {
  if (busy.value) return
  error.value = null
  if (!validate()) return
  busy.value = true
  const params = buildParams()
  try {
    if (props.check) {
      const saved = await api.updateCheck(props.check.id, {
        name: form.name.trim(),
        target: form.target.trim(),
        interval_s: Number(form.intervalS),
        timeout_s: Number(form.timeoutS),
        params,
        enabled: form.enabled,
      })
      const currentIds = [...props.check.server_ids].sort().join(',')
      const nextIds = [...form.serverIds].sort().join(',')
      if (currentIds !== nextIds) await api.setCheckTargets(props.check.id, form.serverIds)
      emit('saved', saved)
      return
    }
    const created = await api.createCheck({
      name: form.name.trim(),
      kind: form.kind,
      target: form.target.trim(),
      interval_s: Number(form.intervalS),
      timeout_s: Number(form.timeoutS),
      params,
      server_ids: form.serverIds,
    })
    emit('saved', created)
  } catch (caught) {
    error.value = errorMessage(caught, t('checks.form.saveFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  if (props.check) hydrate(props.check)
  void loadServers()
})
</script>

<template>
  <AppModal
    :title="editing ? t('checks.form.editTitle') : t('checks.form.createTitle')"
    :description="editing ? t('checks.form.editHint', { name: props.check?.name ?? '' }) : t('checks.form.createHint')"
    size="lg"
    @close="emit('close')"
  >
    <form id="check-form" class="ls-check-form" novalidate @submit.prevent="submit">
      <div class="ls-form__row">
        <label class="ls-field" for="check-form-name">
          <span class="ls-field__label">{{ t('checks.form.name') }}</span>
          <input
            id="check-form-name"
            v-model="form.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            data-autofocus
            :aria-invalid="Boolean(fieldErrors.name)"
          />
          <span v-if="fieldErrors.name" class="ls-field__error" role="alert">{{ fieldErrors.name }}</span>
        </label>

        <label class="ls-field" for="check-form-kind">
          <span class="ls-field__label">{{ t('checks.form.kind') }}</span>
          <AppSelect id="check-form-kind" v-model="form.kind" class="ls-input" :disabled="editing">
            <option v-for="kind in KINDS" :key="kind" :value="kind">{{ checkKindLabel(kind) }}</option>
          </AppSelect>
          <span v-if="editing" class="ls-field__hint">{{ t('checks.form.kindLocked') }}</span>
        </label>
      </div>

      <label class="ls-field" for="check-form-target">
        <span class="ls-field__label">{{ t('checks.form.target') }}</span>
        <input
          id="check-form-target"
          v-model="form.target"
          class="ls-input"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :placeholder="targetPlaceholder"
          :aria-invalid="Boolean(fieldErrors.target)"
        />
        <span class="ls-field__hint">{{ t(`checks.form.targetHint.${form.kind}`) }}</span>
        <span v-if="fieldErrors.target" class="ls-field__error" role="alert">{{ fieldErrors.target }}</span>
      </label>

      <div class="ls-form__row">
        <label class="ls-field" for="check-form-interval">
          <span class="ls-field__label">{{ t('checks.form.interval') }}</span>
          <input
            id="check-form-interval"
            v-model="form.intervalS"
            class="ls-input"
            type="number"
            min="5"
            inputmode="numeric"
            :aria-invalid="Boolean(fieldErrors.interval)"
          />
          <span v-if="fieldErrors.interval" class="ls-field__error" role="alert">{{ fieldErrors.interval }}</span>
        </label>
        <label class="ls-field" for="check-form-timeout">
          <span class="ls-field__label">{{ t('checks.form.timeout') }}</span>
          <input
            id="check-form-timeout"
            v-model="form.timeoutS"
            class="ls-input"
            type="number"
            min="1"
            inputmode="numeric"
            :aria-invalid="Boolean(fieldErrors.timeout)"
          />
          <span v-if="fieldErrors.timeout" class="ls-field__error" role="alert">{{ fieldErrors.timeout }}</span>
        </label>
      </div>

      <label v-if="supportsProbe" class="ls-field" for="check-form-probe">
        <span class="ls-field__label">{{ t('checks.probeChannel') }}</span>
        <AppSelect id="check-form-probe" v-model="form.probeVia" class="ls-input">
          <option value="control">{{ t('checks.probeControl') }}</option>
          <option value="agent">{{ t('checks.probeAgent') }}</option>
        </AppSelect>
        <span class="ls-field__hint">
          {{ form.probeVia === 'agent' ? t('checks.probeAgentNote') : t('checks.probeControlHint') }}
        </span>
      </label>

      <div v-if="form.kind === 'http'" class="ls-form__row">
        <label class="ls-field" for="check-form-status">
          <span class="ls-field__label">{{ t('checks.form.expectStatus') }}</span>
          <input
            id="check-form-status"
            v-model="form.expectStatus"
            class="ls-input"
            type="text"
            inputmode="numeric"
            placeholder="200"
            autocomplete="off"
          />
        </label>
        <label class="ls-field" for="check-form-keyword">
          <span class="ls-field__label">{{ t('checks.form.keyword') }}</span>
          <input
            id="check-form-keyword"
            v-model="form.keyword"
            class="ls-input"
            type="text"
            autocomplete="off"
          />
        </label>
      </div>

      <label v-else-if="form.kind === 'process'" class="ls-field" for="check-form-min-count">
        <span class="ls-field__label">{{ t('checks.form.minCount') }}</span>
        <input
          id="check-form-min-count"
          v-model="form.minCount"
          class="ls-input"
          type="number"
          min="1"
          inputmode="numeric"
          placeholder="1"
        />
      </label>

      <label v-else-if="form.kind === 'script'" class="ls-field" for="check-form-expect-stdout">
        <span class="ls-field__label">{{ t('checks.form.expectStdout') }}</span>
        <input
          id="check-form-expect-stdout"
          v-model="form.expectStdout"
          class="ls-input"
          type="text"
          autocomplete="off"
        />
        <span class="ls-field__hint">{{ t('checks.form.expectStdoutHint') }}</span>
      </label>

      <label v-if="editing" class="ls-check-form__toggle">
        <input v-model="form.enabled" type="checkbox" />
        <span>{{ t('checks.form.enabled') }}</span>
      </label>

      <fieldset class="ls-adminpage__fieldset">
        <legend class="ls-field__label">{{ t('checks.form.targets') }}</legend>
        <p v-if="!canPickTargets" class="ls-field__hint">{{ t('checks.form.targetsNoPermission') }}</p>
        <p v-else-if="serversState === 'loading'" class="ls-field__hint">{{ t('common.loading') }}</p>
        <p v-else-if="serversState === 'error'" class="ls-inline-error" role="alert">
          {{ serversError }}
          <button class="ls-btn ls-btn--small" type="button" @click="loadServers">
            {{ t('common.retry') }}
          </button>
        </p>
        <p v-else-if="servers.length === 0" class="ls-field__hint">{{ t('checks.form.noServers') }}</p>
        <div v-else class="ls-check-form__servers">
          <label v-for="server in servers" :key="server.id" class="ls-check-form__server">
            <input
              type="checkbox"
              :checked="form.serverIds.includes(server.id)"
              @change="toggleServer(server.id)"
            />
            <span>{{ server.name }}</span>
            <span class="ls-adminpage__meta">{{ server.host }}</span>
          </label>
        </div>
      </fieldset>

      <p v-if="error" id="check-form-error" class="ls-adminpage__form-error" role="alert">{{ error }}</p>
    </form>

    <template #footer>
      <button id="check-form-cancel" class="ls-btn" type="button" :disabled="busy" @click="emit('close')">
        {{ t('common.cancel') }}
      </button>
      <button
        id="check-form-submit"
        class="ls-btn ls-btn--primary"
        type="submit"
        form="check-form"
        :disabled="busy"
        :aria-busy="busy"
      >
        <span v-if="busy" class="ls-spinner" aria-hidden="true" />
        {{ busy ? t('common.loading') : editing ? t('common.save') : t('checks.form.createAction') }}
      </button>
    </template>
  </AppModal>
</template>
