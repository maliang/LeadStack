<script setup lang="ts">
import { computed, nextTick, reactive, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { useI18n } from '@/i18n'
import { api, type ServerAccount } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { LOGIN_SCRIPT_SAMPLES } from '@/lib/login-samples'
import {
  LOGIN_SCRIPT_MAX_RUNES,
  insertLoginScriptSample,
  loginScriptIsOverLimit,
  loginScriptRuneCount,
} from '@/lib/server-login'
import {
  metaFieldsFromRows,
  metaRowsFromFields,
  metaValidate,
  newMetaRow,
  type MetaRow,
} from '@/lib/server-detail'

/**
 * 访问账号新建 / 编辑弹窗。
 *
 * 密码与自定义字段都复用服务器档案的约束：密码留空表示不修改（编辑），
 * 自定义字段最多 32 个；脚本可留空（不执行一键登录）。
 */
const props = defineProps<{
  serverId: string
  /** 传入表示编辑；不传表示新建 */
  account?: ServerAccount | null
  /** 新建时的默认地址（通常是服务器 host:port） */
  defaultAddress?: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', account: ServerAccount): void
}>()

const { t } = useI18n()

const editing = computed(() => props.account !== null)
const form = reactive({
  name: '',
  address: '',
  username: '',
  password: '',
  note: '',
  loginScript: '',
  clearPassword: false,
})
const rows = ref<MetaRow[]>(metaRowsFromFields(props.account?.custom_fields ?? {}))
const scriptEl = ref<HTMLTextAreaElement | null>(null)
const scriptIssue = ref<'tooLong' | 'nul' | null>(null)

const busy = ref(false)
const error = ref<string | null>(null)
const fieldErrors = ref<Record<string, string>>({})

const scriptCount = computed(() => loginScriptRuneCount(form.loginScript))
const scriptOver = computed(() => loginScriptIsOverLimit(form.loginScript))

if (props.account) {
  form.name = props.account.name
  form.address = props.account.address
  form.username = props.account.username
  form.note = props.account.note
  form.loginScript = props.account.login_script
} else {
  form.address = props.defaultAddress ?? ''
}

function addField(): void {
  if (rows.value.filter((row) => row.key.trim() !== '').length >= 32) return
  rows.value = [...rows.value, newMetaRow()]
}

function removeField(index: number): void {
  rows.value = rows.value.filter((_, i) => i !== index)
  if (rows.value.length === 0) rows.value = [newMetaRow()]
}

function insertSample(script: string): void {
  const el = scriptEl.value
  const next = insertLoginScriptSample(
    form.loginScript,
    script,
    el?.selectionStart ?? null,
    el?.selectionEnd ?? null,
  )
  form.loginScript = next.text
  void nextTick(() => {
    el?.focus()
    el?.setSelectionRange(next.caret, next.caret)
  })
}

function validate(): boolean {
  const next: Record<string, string> = {}
  if (form.name.trim() === '') next.name = t('server.account.form.errorName')
  if (form.loginScript.includes('\u0000')) scriptIssue.value = 'nul'
  else if (loginScriptIsOverLimit(form.loginScript)) scriptIssue.value = 'tooLong'
  else scriptIssue.value = null
  if (scriptIssue.value) next.script = t('server.account.form.errorScript')

  const custom = metaValidate(form.note, rows.value)
  if (custom) next.custom = t(`server.meta.error.${custom.code}`)

  fieldErrors.value = next
  return Object.keys(next).length === 0
}

async function submit(): Promise<void> {
  if (busy.value) return
  error.value = null
  if (!validate()) return
  busy.value = true
  const customFields = metaFieldsFromRows(rows.value)
  const payload = {
    name: form.name.trim(),
    address: form.address.trim(),
    username: form.username.trim(),
    note: form.note.trim(),
    custom_fields: customFields,
    login_script: form.loginScript.trim(),
  }
  try {
    let saved: ServerAccount
    if (props.account) {
      saved = await api.updateServerAccount(props.serverId, props.account.id, {
        ...payload,
        // 留空 = 不修改；勾选清空 = 提交空串清空。
        password: form.clearPassword ? '' : form.password === '' ? undefined : form.password,
      })
    } else {
      saved = await api.createServerAccount(props.serverId, {
        ...payload,
        password: form.password === '' ? undefined : form.password,
      })
    }
    emit('saved', saved)
  } catch (caught) {
    error.value = errorMessage(caught, t('server.accounts.saveFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppModal
    :title="editing ? t('server.account.form.editTitle') : t('server.account.form.createTitle')"
    :description="
      editing
        ? t('server.account.form.editHint', { name: props.account?.name ?? '' })
        : t('server.account.form.createHint')
    "
    size="lg"
    @close="emit('close')"
  >
    <form id="server-account-form" class="ls-account-form" novalidate @submit.prevent="submit">
      <label class="ls-field" for="account-form-name">
        <span class="ls-field__label">{{ t('server.account.form.name') }}</span>
        <input
          id="account-form-name"
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
        <label class="ls-field" for="account-form-address">
          <span class="ls-field__label">{{ t('server.account.form.address') }}</span>
          <input
            id="account-form-address"
            v-model="form.address"
            class="ls-input"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :placeholder="props.defaultAddress"
          />
          <span class="ls-field__hint">{{ t('server.account.form.addressHint') }}</span>
        </label>
        <label class="ls-field" for="account-form-username">
          <span class="ls-field__label">{{ t('server.account.form.username') }}</span>
          <input
            id="account-form-username"
            v-model="form.username"
            class="ls-input"
            type="text"
            autocomplete="off"
            spellcheck="false"
          />
        </label>
      </div>

      <label class="ls-field" for="account-form-password">
        <span class="ls-field__label">{{ t('server.account.form.password') }}</span>
        <input
          id="account-form-password"
          v-model="form.password"
          class="ls-input"
          type="password"
          autocomplete="new-password"
          :disabled="form.clearPassword"
        />
        <span class="ls-field__hint">
          {{ editing ? t('server.account.form.passwordEditHint') : t('common.optional') }}
        </span>
      </label>

      <label v-if="editing && props.account?.has_password" class="ls-check">
        <input v-model="form.clearPassword" type="checkbox" />
        <span>{{ t('server.account.form.passwordClear') }}</span>
      </label>

      <label class="ls-field" for="account-form-note">
        <span class="ls-field__label">{{ t('server.account.form.note') }}</span>
        <textarea id="account-form-note" v-model="form.note" class="ls-input ls-textarea" rows="2" />
        <span class="ls-field__hint">{{ t('server.meta.notesHint') }}</span>
      </label>

      <fieldset class="ls-adminpage__fieldset">
        <legend class="ls-field__label">{{ t('server.account.form.customFields') }}</legend>
        <p class="ls-field__hint">{{ t('server.account.form.customFieldsHint') }}</p>
        <ul class="ls-account-form__fields">
          <li v-for="(row, index) in rows" :key="row.id" class="ls-account-form__field">
            <input
              v-model="row.key"
              class="ls-input"
              type="text"
              :placeholder="t('server.account.form.keyPlaceholder')"
              :aria-label="t('server.account.form.keyPlaceholder') + ' ' + (index + 1)"
              maxlength="40"
            />
            <input
              v-model="row.value"
              class="ls-input"
              type="text"
              :placeholder="t('server.account.form.valuePlaceholder')"
              :aria-label="t('server.account.form.valuePlaceholder') + ' ' + (index + 1)"
              maxlength="200"
            />
            <button
              class="ls-icon-btn"
              type="button"
              :aria-label="t('server.account.form.removeField')"
              :title="t('server.account.form.removeField')"
              @click="removeField(index)"
            >
              <AppIcon name="close" size="0.875rem" />
            </button>
          </li>
        </ul>
        <button class="ls-btn ls-btn--small" type="button" @click="addField">
          <AppIcon name="plus" size="0.875rem" />
          {{ t('server.account.form.addField') }}
        </button>
        <p v-if="fieldErrors.custom" class="ls-field__error" role="alert">{{ fieldErrors.custom }}</p>
      </fieldset>

      <label class="ls-field" for="account-form-script">
        <span class="ls-field__label">{{ t('server.account.form.loginScript') }}</span>
        <textarea
          id="account-form-script"
          ref="scriptEl"
          v-model="form.loginScript"
          class="ls-input ls-textarea ls-script-editor"
          spellcheck="false"
          :placeholder="t('server.account.form.scriptPlaceholder')"
          :aria-invalid="Boolean(fieldErrors.script)"
        />
        <span class="ls-field__hint">
          {{ t('server.account.form.loginScriptHint') }}
        </span>
        <span class="ls-field__counter" :class="{ 'ls-field__counter--over': scriptOver }">
          {{ scriptCount }} / {{ LOGIN_SCRIPT_MAX_RUNES }}
        </span>
        <span v-if="fieldErrors.script" class="ls-field__error" role="alert">{{ fieldErrors.script }}</span>
      </label>

      <div class="ls-login-script__row">
        <span class="ls-field__label">{{ t('server.account.form.scriptSamples') }}</span>
        <div class="ls-script-samples">
          <button
            v-for="sample in LOGIN_SCRIPT_SAMPLES"
            :key="sample.key"
            class="ls-btn ls-btn--small"
            type="button"
            @click="insertSample(sample.script)"
          >
            <AppIcon name="file-text" size="0.875rem" />
            {{ t(sample.labelKey) }}
          </button>
        </div>
      </div>

      <p v-if="error" id="server-account-form-error" class="ls-adminpage__form-error" role="alert">
        {{ error }}
      </p>
    </form>

    <template #footer>
      <button
        id="server-account-cancel"
        class="ls-btn"
        type="button"
        :disabled="busy"
        @click="emit('close')"
      >
        {{ t('server.account.form.cancel') }}
      </button>
      <button
        id="server-account-submit"
        class="ls-btn ls-btn--primary"
        type="submit"
        form="server-account-form"
        :disabled="busy"
        :aria-busy="busy"
      >
        <span v-if="busy" class="ls-spinner" aria-hidden="true" />
        {{
          busy
            ? t('common.loading')
            : editing
              ? t('server.account.form.save')
              : t('server.account.form.create')
        }}
      </button>
    </template>
  </AppModal>
</template>
