<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  api,
  type Credential,
  type LocalServerResult,
  type Server,
  type UpdateServerPayload,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { validateServerHost } from '@/lib/network'
import { MAX_BATCH_TAGS, MAX_BATCH_TAG_LENGTH, parseTagsInput } from '@/lib/server-batch'
import { writeClipboard } from '@/lib/clipboard'

/**
 * 服务器纳管 / 编辑弹窗（ServersPage 与 ServerDetailPage 共用）。
 *
 * - 新建：POST /servers，支持 agent / SSH 两种接入方式；SSH 需要引用已有凭证。
 * - 编辑：PATCH /servers/{id}，只提交名称 / 地址 / 端口 / 分组 / 标签。
 * - 「一键接入本机」（POST /servers/local）需要 enroll.manage；成功后展示
 *   安装命令并复制，列表由父级刷新、agent 上线状态由轮询接管。
 *
 * 校验错误就近展示：字段级错误 + 一个提交错误位；提交失败不关闭弹窗，
 * 用户不用重填（此前定时任务页的教训：关窗即丢草稿）。
 */
const props = withDefaults(
  defineProps<{
    /** 传入表示编辑，不传表示新建 */
    server?: Server | null
    /** 已有分组名（datalist 建议值） */
    groupOptions?: string[]
  }>(),
  { server: null, groupOptions: () => [] },
)

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', server: Server): void
  /** 一键接入本机成功：服务器已创建，但弹窗要留着展示安装命令 */
  (event: 'local-created'): void
}>()

const { t } = useI18n()
const { can } = useCan()

const editing = computed(() => props.server !== null)
const canLocal = computed(() => !editing.value && can('enroll.manage'))

const form = reactive({
  name: '',
  host: '',
  port: '22',
  mode: 'agent' as 'agent' | 'ssh',
  credentialId: '',
  group: '',
  tags: '',
  hostKeyPolicy: 'accept-new' as 'accept-new' | 'strict' | 'insecure',
})

const credentials = ref<Credential[]>([])
const credentialsState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const credentialsError = ref<string | null>(null)

const busy = ref(false)
const error = ref<string | null>(null)
const fieldErrors = ref<Record<string, string>>({})

const localBusy = ref(false)
const localError = ref<string | null>(null)
const localResult = ref<LocalServerResult | null>(null)
const localCopied = ref(false)

const sshCredentials = computed(() => credentials.value.filter((item) => item.kind !== 'baota'))

const addressChanged = computed(() => {
  const server = props.server
  if (!server) return false
  return form.host.trim() !== server.host || Number(form.port) !== server.port
})

function hydrate(server: Server): void {
  form.name = server.name
  form.host = server.host
  form.port = server.port === null ? '22' : String(server.port)
  form.mode = server.mode === 'ssh' ? 'ssh' : 'agent'
  form.credentialId = server.credential_id ?? ''
  form.group = server.group ?? server.group_names[0] ?? ''
  form.tags = server.tags.join(', ')
  form.hostKeyPolicy = 'accept-new'
}

async function loadCredentials(): Promise<void> {
  if (credentialsState.value === 'loading' || credentialsState.value === 'ready') return
  credentialsState.value = 'loading'
  credentialsError.value = null
  try {
    credentials.value = await api.credentials()
    credentialsState.value = 'ready'
  } catch (caught) {
    credentialsState.value = 'error'
    credentialsError.value = errorMessage(caught, t('common.error'))
  }
}

function selectMode(mode: 'agent' | 'ssh'): void {
  if (editing.value) return
  form.mode = mode
  if (mode === 'ssh') void loadCredentials()
}

function validate(): boolean {
  const next: Record<string, string> = {}
  if (form.name.trim() === '') next.name = t('server.form.errorName')
  const hostError = validateServerHost(form.host)
  if (hostError) next.host = t(hostError)
  const port = Number(form.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) next.port = t('server.form.errorPort')
  if (!editing.value && form.mode === 'ssh' && form.credentialId === '') {
    next.credential = t('server.form.errorCredential')
  }
  fieldErrors.value = next
  return Object.keys(next).length === 0
}

async function submit(): Promise<void> {
  if (busy.value) return
  error.value = null
  if (!validate()) return
  busy.value = true
  const tags = parseTagsInput(form.tags).slice(0, MAX_BATCH_TAGS)
  const group = form.group.trim()
  try {
    if (props.server) {
      const payload: UpdateServerPayload = {
        name: form.name.trim(),
        host: form.host.trim(),
        port: Number(form.port),
        tags,
        group: group === '' ? null : group,
      }
      const saved = await api.updateServer(props.server.id, payload)
      emit('saved', saved)
      return
    }
    const created = await api.createServer({
      name: form.name.trim(),
      host: form.host.trim(),
      port: Number(form.port),
      mode: form.mode,
      credential_id: form.mode === 'ssh' ? form.credentialId : undefined,
      group: group === '' ? undefined : group,
      tags,
      host_key_policy: form.mode === 'ssh' ? form.hostKeyPolicy : undefined,
    })
    emit('saved', created)
  } catch (caught) {
    error.value = errorMessage(caught, t('server.form.saveFailed'))
  } finally {
    busy.value = false
  }
}

async function onboardLocal(): Promise<void> {
  if (localBusy.value) return
  localBusy.value = true
  localError.value = null
  try {
    localResult.value = await api.createLocalServer()
    emit('local-created')
  } catch (caught) {
    localError.value = errorMessage(caught, t('server.form.localFailed'))
  } finally {
    localBusy.value = false
  }
}

async function copyCommand(): Promise<void> {
  const command = localResult.value?.command ?? ''
  if (!command) return
  localCopied.value = await writeClipboard(command)
  if (localCopied.value) setTimeout(() => (localCopied.value = false), 1800)
}

onMounted(() => {
  if (props.server) {
    hydrate(props.server)
    return
  }
  // 新建默认 agent：不需要凭证，等用户切到 SSH 再按需拉取凭证。
})
</script>

<template>
  <AppModal
    :title="editing ? t('server.edit.title') : t('server.form.createTitle')"
    :description="
      editing
        ? t('server.form.editDescription', { name: props.server?.name ?? '' })
        : t('server.form.createDescription')
    "
    size="lg"
    @close="emit('close')"
  >
    <div v-if="localResult" class="ls-server-form__local" role="status">
      <p class="ls-notice__title">{{ t('server.form.localTitle') }}</p>
      <p class="ls-notice__text">{{ t('server.form.localCreated') }}</p>
      <p v-if="localResult.created === false" class="ls-field__hint">
        {{ t('server.form.localReused') }}
      </p>
      <pre id="server-local-command" class="ls-server-form__command">{{ localResult.command }}</pre>
      <p class="ls-field__hint">{{ t('server.form.localHint') }}</p>
    </div>

    <form v-else id="server-form" class="ls-adminpage__form" novalidate @submit.prevent="submit">
      <label class="ls-field" for="server-form-name">
        <span class="ls-field__label">{{ t('server.edit.name') }}</span>
        <input
          id="server-form-name"
          v-model="form.name"
          class="ls-input"
          type="text"
          autocomplete="off"
          data-autofocus
          :aria-invalid="Boolean(fieldErrors.name)"
        />
        <span v-if="fieldErrors.name" class="ls-field__error" role="alert">{{ fieldErrors.name }}</span>
      </label>

      <div class="ls-form__row">
        <label class="ls-field" for="server-form-host">
          <span class="ls-field__label">{{ t('server.edit.host') }}</span>
          <input
            id="server-form-host"
            v-model="form.host"
            class="ls-input"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :aria-invalid="Boolean(fieldErrors.host)"
          />
          <span v-if="fieldErrors.host" class="ls-field__error" role="alert">{{ fieldErrors.host }}</span>
        </label>
        <label class="ls-field" for="server-form-port">
          <span class="ls-field__label">{{ t('server.edit.port') }}</span>
          <input
            id="server-form-port"
            v-model="form.port"
            class="ls-input"
            type="number"
            min="1"
            max="65535"
            inputmode="numeric"
            :aria-invalid="Boolean(fieldErrors.port)"
          />
          <span class="ls-field__hint">{{ t('server.form.portHint') }}</span>
          <span v-if="fieldErrors.port" class="ls-field__error" role="alert">{{ fieldErrors.port }}</span>
        </label>
      </div>

      <p v-if="addressChanged" class="ls-field__hint" role="status">
        {{ t('server.edit.addressWarning') }} — {{ t('server.edit.addressWarningHint') }}
      </p>

      <fieldset v-if="!editing" class="ls-adminpage__fieldset">
        <legend class="ls-field__label">{{ t('server.form.mode') }}</legend>
        <div class="ls-adminpage__modes ls-adminpage__modes--2" role="radiogroup" :aria-label="t('server.form.mode')">
          <button
            id="server-form-mode-agent"
            class="ls-adminpage__mode"
            :class="{ 'is-active': form.mode === 'agent' }"
            type="button"
            role="radio"
            :aria-checked="form.mode === 'agent'"
            @click="selectMode('agent')"
          >
            <span class="ls-adminpage__mode-label">{{ t('server.form.modeAgent') }}</span>
            <span class="ls-adminpage__mode-hint">{{ t('server.form.modeAgentHint') }}</span>
          </button>
          <button
            id="server-form-mode-ssh"
            class="ls-adminpage__mode"
            :class="{ 'is-active': form.mode === 'ssh' }"
            type="button"
            role="radio"
            :aria-checked="form.mode === 'ssh'"
            @click="selectMode('ssh')"
          >
            <span class="ls-adminpage__mode-label">{{ t('server.form.modeSsh') }}</span>
            <span class="ls-adminpage__mode-hint">{{ t('server.form.modeSshHint') }}</span>
          </button>
        </div>

        <template v-if="form.mode === 'ssh'">
          <label class="ls-field" for="server-form-credential">
            <span class="ls-field__label">{{ t('server.form.credential') }}</span>
            <select
              id="server-form-credential"
              v-model="form.credentialId"
              class="ls-input"
              :disabled="credentialsState === 'loading'"
              :aria-invalid="Boolean(fieldErrors.credential)"
            >
              <option value="">{{ t('server.form.credentialNone') }}</option>
              <option v-for="credential in sshCredentials" :key="credential.id" :value="credential.id">
                {{ credential.name }}（{{ credential.username || credential.kind }}）
              </option>
            </select>
            <span class="ls-field__hint">{{ t('server.form.credentialHint') }}</span>
            <span v-if="fieldErrors.credential" class="ls-field__error" role="alert">
              {{ fieldErrors.credential }}
            </span>
          </label>

          <p v-if="credentialsState === 'error'" class="ls-inline-error" role="alert">
            {{ credentialsError }}
            <button class="ls-btn ls-btn--small" type="button" @click="loadCredentials">
              {{ t('common.retry') }}
            </button>
          </p>
          <p v-else-if="credentialsState === 'ready' && sshCredentials.length === 0" class="ls-field__hint">
            {{ t('server.form.noCredentials') }}
          </p>

          <label class="ls-field" for="server-form-policy">
            <span class="ls-field__label">{{ t('server.form.hostKeyPolicy') }}</span>
            <select id="server-form-policy" v-model="form.hostKeyPolicy" class="ls-input">
              <option value="accept-new">{{ t('server.form.policyAcceptNew') }}</option>
              <option value="strict">{{ t('server.form.policyStrict') }}</option>
              <option value="insecure">{{ t('server.form.policyInsecure') }}</option>
            </select>
            <span class="ls-field__hint">{{ t('server.form.policyHint') }}</span>
          </label>
        </template>
      </fieldset>

      <div class="ls-form__row">
        <label class="ls-field" for="server-form-group">
          <span class="ls-field__label">{{ t('server.form.group') }}</span>
          <input
            id="server-form-group"
            v-model="form.group"
            class="ls-input"
            type="text"
            list="server-form-group-options"
            autocomplete="off"
            :placeholder="t('server.form.groupPlaceholder')"
          />
          <datalist id="server-form-group-options">
            <option v-for="group in props.groupOptions" :key="group" :value="group" />
          </datalist>
          <span class="ls-field__hint">{{ t('server.form.groupHint') }}</span>
        </label>
        <label class="ls-field" for="server-form-tags">
          <span class="ls-field__label">{{ t('server.form.tags') }}</span>
          <input
            id="server-form-tags"
            v-model="form.tags"
            class="ls-input"
            type="text"
            autocomplete="off"
            :placeholder="t('server.form.tagsPlaceholder')"
          />
          <span class="ls-field__hint">
            {{ t('server.form.tagsHint', { max: MAX_BATCH_TAGS, len: MAX_BATCH_TAG_LENGTH }) }}
          </span>
        </label>
      </div>

      <p v-if="error" id="server-form-error" class="ls-adminpage__form-error" role="alert">{{ error }}</p>

      <p v-if="localError" class="ls-inline-error" role="alert">{{ localError }}</p>
    </form>

    <template #footer>
      <template v-if="localResult">
        <button
          id="server-form-copy"
          class="ls-btn"
          type="button"
          :disabled="!localResult.command"
          @click="copyCommand"
        >
          <AppIcon name="file-text" size="0.875rem" />
          {{ localCopied ? t('server.form.localCopied') : t('server.form.localCopy') }}
        </button>
        <button id="server-form-close" class="ls-btn ls-btn--primary" type="button" @click="emit('close')">
          {{ t('server.form.close') }}
        </button>
      </template>
      <template v-else>
        <button
          v-if="canLocal"
          id="server-form-local"
          class="ls-btn"
          type="button"
          :disabled="busy || localBusy"
          @click="onboardLocal"
        >
          <span v-if="localBusy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="plus" size="0.875rem" />
          {{ t('server.form.local') }}
        </button>
        <button
          id="server-form-cancel"
          class="ls-btn"
          type="button"
          :disabled="busy || localBusy"
          @click="emit('close')"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          id="server-form-submit"
          class="ls-btn ls-btn--primary"
          type="submit"
          form="server-form"
          :disabled="busy || localBusy"
        >
          <span v-if="busy" class="ls-spinner" aria-hidden="true" />
          {{
            busy
              ? editing
                ? t('server.form.saving')
                : t('server.form.creating')
              : editing
                ? t('server.form.saveAction')
                : t('server.form.createAction')
          }}
        </button>
      </template>
    </template>
  </AppModal>
</template>
