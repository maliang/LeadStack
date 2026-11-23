<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import {
  api,
  type Credential,
  type LocalServerResult,
  type Server,
  type ServerGroup,
  type UpdateServerPayload,
} from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { validateServerHost } from '@/lib/network'
import { MAX_BATCH_TAGS, MAX_BATCH_TAG_LENGTH } from '@/lib/server-batch'
import { writeClipboard } from '@/lib/clipboard'

/**
 * 服务器纳管 / 编辑弹窗（ServersPage 与 ServerDetailPage 共用）。
 *
 * - 新建：POST /servers，agent / SSH 两种接入方式；SSH 要引用已有凭证。
 * - 编辑：PATCH /servers/{id}。agent 模式的地址由 agent 上报、端口不参与连接，
 *   所以编辑时只读；只有 SSH 模式才允许改地址/端口。
 * - 宝塔面板：绑定 kind=baota 的 API 密钥凭证；绑定后列表出现「宝塔登录」。
 * - 分组：下拉选已管理分组（写 group_ids），也可切换「自定义」直接输入文本。
 * - 标签：输入逗号 / 空格 / 换行即转成可删除的标签块。
 */
const props = withDefaults(
  defineProps<{
    /** 传入表示编辑，不传表示新建 */
    server?: Server | null
  }>(),
  { server: null },
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
const isAgentEdit = computed(() => editing.value && props.server?.mode !== 'ssh')
const showPort = computed(() => (editing.value ? props.server?.mode === 'ssh' : form.mode === 'ssh'))

const CUSTOM_GROUP = '__custom__'

const form = reactive({
  name: '',
  host: '',
  port: '22',
  mode: 'agent' as 'agent' | 'ssh',
  credentialId: '',
  panelCredentialId: '',
  groupChoice: '' as string,
  customGroup: '',
  hostKeyPolicy: 'accept-new' as 'accept-new' | 'strict' | 'insecure',
})

const tags = ref<string[]>([])
const tagDraft = ref('')

const credentials = ref<Credential[]>([])
const credentialsState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const credentialsError = ref<string | null>(null)

const serverGroups = ref<ServerGroup[]>([])
const groupsState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const groupsError = ref<string | null>(null)

const busy = ref(false)
const error = ref<string | null>(null)
const fieldErrors = ref<Record<string, string>>({})

const localBusy = ref(false)
const localError = ref<string | null>(null)
const localResult = ref<LocalServerResult | null>(null)
const localCopied = ref(false)

const sshCredentials = computed(() => credentials.value.filter((item) => item.kind !== 'baota'))
const baotaCredentials = computed(() => credentials.value.filter((item) => item.kind === 'baota'))
const selectedGroupName = computed(() => {
  if (form.groupChoice === '' || form.groupChoice === CUSTOM_GROUP) return ''
  return serverGroups.value.find((group) => group.id === form.groupChoice)?.name ?? ''
})

const addressChanged = computed(() => {
  const server = props.server
  if (!server || server.mode !== 'ssh') return false
  return form.host.trim() !== server.host || Number(form.port) !== server.port
})

function hydrate(server: Server): void {
  form.name = server.name
  form.host = server.host
  form.port = server.port === null ? '22' : String(server.port)
  form.mode = server.mode === 'ssh' ? 'ssh' : 'agent'
  form.credentialId = server.credential_id ?? ''
  form.panelCredentialId = server.panel_credential_id ?? ''
  form.hostKeyPolicy = 'accept-new'
  tags.value = [...server.tags]
  tagDraft.value = ''
  // 先按 group_ids 匹配；历史数据只有文本 group 时落到「自定义」。
  form.groupChoice = server.group_ids[0] ?? (server.group ? CUSTOM_GROUP : '')
  form.customGroup = server.group ?? ''
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

async function loadGroups(): Promise<void> {
  if (groupsState.value === 'loading' || groupsState.value === 'ready') return
  groupsState.value = 'loading'
  groupsError.value = null
  try {
    serverGroups.value = await api.serverGroups()
    groupsState.value = 'ready'
    // 历史数据只有 group 文本：若与已管理分组同名，自动切到该分组，避免重复文本。
    if (form.groupChoice === CUSTOM_GROUP && form.customGroup.trim() !== '') {
      const matched = serverGroups.value.find((group) => group.name === form.customGroup.trim())
      if (matched) form.groupChoice = matched.id
    }
  } catch (caught) {
    groupsState.value = 'error'
    groupsError.value = errorMessage(caught, t('common.error'))
  }
}

function selectMode(mode: 'agent' | 'ssh'): void {
  if (editing.value) return
  form.mode = mode
}

function addTags(raw: string): void {
  const next = [...tags.value]
  for (const part of raw.split(/[,，\s]+/)) {
    const value = part.trim()
    if (value === '' || [...value].length > MAX_BATCH_TAG_LENGTH) continue
    if (next.includes(value)) continue
    if (next.length >= MAX_BATCH_TAGS) break
    next.push(value)
  }
  tags.value = next
}

function onTagInput(event: Event): void {
  const input = event.target as HTMLInputElement
  const raw = input.value
  if (/[,，\s]/.test(raw)) {
    const parts = raw.split(/[,，\s]+/)
    const pending = parts.pop() ?? ''
    addTags(parts.join(' '))
    tagDraft.value = pending.trim()
    input.value = tagDraft.value
    return
  }
  tagDraft.value = raw
}

function commitTagDraft(): void {
  if (tagDraft.value.trim() === '') return
  addTags(tagDraft.value)
  tagDraft.value = ''
}

function onTagPaste(event: ClipboardEvent): void {
  const text = event.clipboardData?.getData('text') ?? ''
  if (!/[\r\n]/.test(text)) return
  event.preventDefault()
  addTags(text)
  tagDraft.value = ''
}

function onTagKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' || event.key === ',' || event.key === '，') {
    event.preventDefault()
    commitTagDraft()
    return
  }
  if (event.key === 'Backspace' && tagDraft.value === '' && tags.value.length > 0) {
    removeTag(tags.value[tags.value.length - 1])
  }
}

function removeTag(tag: string): void {
  tags.value = tags.value.filter((item) => item !== tag)
}

function validate(): boolean {
  const next: Record<string, string> = {}
  if (form.name.trim() === '') next.name = t('server.form.errorName')
  if (!isAgentEdit.value) {
    const hostError = validateServerHost(form.host)
    if (hostError) next.host = t(hostError)
  }
  if (showPort.value) {
    const port = Number(form.port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) next.port = t('server.form.errorPort')
  }
  if (!editing.value && form.mode === 'ssh' && form.credentialId === '') {
    next.credential = t('server.form.errorCredential')
  }
  fieldErrors.value = next
  return Object.keys(next).length === 0
}

function buildGroup(): { group: string; groupIds: string[] } {
  // PATCH 语义：空串表示清除；null/缺省表示不改。所以这里统一返回空串而不是 null。
  if (form.groupChoice === '') return { group: '', groupIds: [] }
  if (form.groupChoice === CUSTOM_GROUP) {
    return { group: form.customGroup.trim(), groupIds: [] }
  }
  return { group: selectedGroupName.value || props.server?.group || '', groupIds: [form.groupChoice] }
}

async function submit(): Promise<void> {
  if (busy.value) return
  commitTagDraft()
  error.value = null
  if (!validate()) return
  busy.value = true
  const group = buildGroup()
  const tagsValue = tags.value.slice(0, MAX_BATCH_TAGS)
  try {
    if (props.server) {
      const payload: UpdateServerPayload = {
        name: form.name.trim(),
        tags: tagsValue,
        group: group.group,
        group_ids: group.groupIds,
        panel_credential_id: form.panelCredentialId,
      }
      // agent 模式的 host/port 由 agent 上报，不参与连接；编辑时不提交，避免无意义变更。
      if (props.server.mode === 'ssh') {
        payload.host = form.host.trim()
        payload.port = Number(form.port)
      }
      const saved = await api.updateServer(props.server.id, payload)
      emit('saved', saved)
      return
    }
    const created = await api.createServer({
      name: form.name.trim(),
      host: form.host.trim(),
      port: showPort.value ? Number(form.port) : 22,
      mode: form.mode,
      credential_id: form.mode === 'ssh' ? form.credentialId : undefined,
      panel_credential_id: form.panelCredentialId === '' ? undefined : form.panelCredentialId,
      group: group.group === '' ? undefined : group.group,
      group_ids: group.groupIds,
      tags: tagsValue,
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
  if (props.server) hydrate(props.server)
  void loadCredentials()
  void loadGroups()
})

// 编辑弹窗可能在分组列表返回后才打开（例如从详情页打开），这里补一次匹配。
watch(
  () => props.server?.id,
  () => {
    if (props.server) hydrate(props.server)
  },
)
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
            :readonly="isAgentEdit"
            :aria-invalid="Boolean(fieldErrors.host)"
          />
          <span v-if="isAgentEdit" class="ls-field__hint">{{ t('server.form.hostAgentHint') }}</span>
          <span v-if="fieldErrors.host" class="ls-field__error" role="alert">{{ fieldErrors.host }}</span>
        </label>
        <label v-if="showPort" class="ls-field" for="server-form-port">
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
          <span class="ls-field__hint">{{ t('server.form.portSshHint') }}</span>
          <span v-if="fieldErrors.port" class="ls-field__error" role="alert">{{ fieldErrors.port }}</span>
        </label>
      </div>

      <p v-if="isAgentEdit" class="ls-field__hint">{{ t('server.form.portAgentHint') }}</p>

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

        <label v-if="form.mode === 'ssh'" class="ls-field" for="server-form-credential">
          <span class="ls-field__label">{{ t('server.form.credential') }}</span>
          <AppSelect
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
          </AppSelect>
          <span class="ls-field__hint">{{ t('server.form.credentialHint') }}</span>
          <span v-if="fieldErrors.credential" class="ls-field__error" role="alert">
            {{ fieldErrors.credential }}
          </span>
        </label>

        <p v-if="form.mode === 'ssh' && credentialsState === 'error'" class="ls-inline-error" role="alert">
          {{ credentialsError }}
          <button class="ls-btn ls-btn--small" type="button" @click="loadCredentials">
            {{ t('common.retry') }}
          </button>
        </p>
        <p v-else-if="form.mode === 'ssh' && credentialsState === 'ready' && sshCredentials.length === 0" class="ls-field__hint">
          {{ t('server.form.noCredentials') }}
        </p>

        <label v-if="form.mode === 'ssh'" class="ls-field" for="server-form-policy">
          <span class="ls-field__label">{{ t('server.form.hostKeyPolicy') }}</span>
          <AppSelect id="server-form-policy" v-model="form.hostKeyPolicy" class="ls-input">
            <option value="accept-new">{{ t('server.form.policyAcceptNew') }}</option>
            <option value="strict">{{ t('server.form.policyStrict') }}</option>
            <option value="insecure">{{ t('server.form.policyInsecure') }}</option>
          </AppSelect>
          <span class="ls-field__hint">{{ t('server.form.policyHint') }}</span>
        </label>
      </fieldset>

      <div class="ls-form__row">
        <label class="ls-field" for="server-form-group">
          <span class="ls-field__label">{{ t('server.form.group') }}</span>
          <AppSelect id="server-form-group" v-model="form.groupChoice" class="ls-input">
            <option value="">{{ t('server.form.groupNone') }}</option>
            <option v-for="group in serverGroups" :key="group.id" :value="group.id">
              {{ group.name }}（{{ group.member_count }}）
            </option>
            <option :value="CUSTOM_GROUP">{{ t('server.form.groupCustom') }}</option>
          </AppSelect>
          <input
            v-if="form.groupChoice === CUSTOM_GROUP"
            v-model="form.customGroup"
            class="ls-input"
            type="text"
            autocomplete="off"
            :placeholder="t('server.form.groupPlaceholder')"
            :aria-label="t('server.form.groupCustom')"
          />
          <span class="ls-field__hint">{{ t('server.form.groupHint') }}</span>
          <span v-if="groupsState === 'error'" class="ls-field__error" role="alert">
            {{ groupsError }}
          </span>
        </label>

        <div class="ls-field">
          <label class="ls-field__label" for="server-form-tags">{{ t('server.form.tags') }}</label>
          <div class="ls-tags">
            <span v-for="tag in tags" :key="tag" class="ls-tag">
              {{ tag }}
              <button type="button" class="ls-tag__remove" :aria-label="`${t('common.delete')} ${tag}`" @click="removeTag(tag)">
                <AppIcon name="close" size="0.75rem" />
              </button>
            </span>
            <input
              id="server-form-tags"
              class="ls-tags__input"
              type="text"
              autocomplete="off"
              :value="tagDraft"
              :placeholder="tags.length === 0 ? t('server.form.tagsPlaceholder') : ''"
              @input="onTagInput"
              @paste="onTagPaste"
              @blur="commitTagDraft"
              @keydown="onTagKeydown"
            />
          </div>
          <span class="ls-field__hint">
            {{ t('server.form.tagsHint', { max: MAX_BATCH_TAGS, len: MAX_BATCH_TAG_LENGTH }) }}
          </span>
        </div>
      </div>

      <label class="ls-field" for="server-form-panel">
        <span class="ls-field__label">{{ t('server.form.panelCredential') }}</span>
        <AppSelect
          id="server-form-panel"
          v-model="form.panelCredentialId"
          class="ls-input"
          :disabled="credentialsState === 'loading'"
        >
          <option value="">{{ t('server.form.panelCredentialNone') }}</option>
          <option v-for="credential in baotaCredentials" :key="credential.id" :value="credential.id">
            {{ credential.name }}
          </option>
          <option
            v-if="form.panelCredentialId && !baotaCredentials.some((item) => item.id === form.panelCredentialId)"
            :value="form.panelCredentialId"
          >
            {{ form.panelCredentialId }}
          </option>
        </AppSelect>
        <span class="ls-field__hint">{{ t('server.form.panelCredentialHint') }}</span>
        <span v-if="credentialsState === 'ready' && baotaCredentials.length === 0" class="ls-field__hint">
          {{ t('server.form.noPanelCredentials') }}
          <RouterLink to="/credentials">{{ t('server.form.openCredentials') }}</RouterLink>
        </span>
      </label>

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
