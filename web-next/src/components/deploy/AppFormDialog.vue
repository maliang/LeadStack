<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import type { AppItem, CreateAppPayload } from '@/lib/api'
import {
  appFormFromItem,
  appPayloadFromForm,
  emptyAppForm,
  validateAppForm,
  type AppFormErrors,
  type AppFormValues,
} from '@/lib/deploy-domain'
import { useI18n } from '@/i18n'
import ModalDialog from './ModalDialog.vue'

/**
 * 新建 / 编辑应用。校验规则来自 lib/deploy-domain 的纯函数，错误文案走 i18n 键，
 * 提交前必过校验——「名称/标识为空」不发请求。
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    app?: AppItem | null
    busy?: boolean
    error?: string | null
  }>(),
  { app: null, busy: false, error: null },
)

const emit = defineEmits<{
  (event: 'submit', payload: CreateAppPayload): void
  (event: 'close'): void
}>()

const { t } = useI18n()
const form = reactive<AppFormValues>(emptyAppForm())
const errors = ref<AppFormErrors>({})
const attempted = ref(false)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    Object.assign(form, props.app ? appFormFromItem(props.app) : emptyAppForm())
    errors.value = {}
    attempted.value = false
  },
  { immediate: true },
)

function submit(): void {
  attempted.value = true
  const found = validateAppForm(form)
  errors.value = found
  if (Object.keys(found).length > 0) return
  emit('submit', appPayloadFromForm(form))
}

const modeOptions = [
  { value: 'release', key: 'apps.mode.release', hintKey: 'apps.modeReleaseHint' },
  { value: 'inplace', key: 'apps.mode.inplace', hintKey: 'apps.modeInplaceHint' },
  { value: 'script', key: 'apps.mode.script', hintKey: 'apps.modeScriptHint' },
]

const modeHint = computed(() => {
  const option = modeOptions.find((item) => item.value === form.deployMode) ?? modeOptions[0]
  return t(option.hintKey)
})
</script>

<template>
  <ModalDialog
    :open="open"
    :title="app ? t('apps.form.editTitle') : t('apps.form.createTitle')"
    :description="t('apps.form.description')"
    test-id="app-form-dialog"
    @close="emit('close')"
  >
    <form class="ls-form" novalidate @submit.prevent="submit">
      <label class="ls-field" for="app-form-name">
        <span class="ls-field__label">{{ t('apps.form.name') }}</span>
        <input
          id="app-form-name"
          v-model="form.name"
          class="ls-input"
          type="text"
          autocomplete="off"
          :placeholder="t('apps.form.namePlaceholder')"
          :aria-invalid="Boolean(errors.name)"
        />
        <span v-if="errors.name" class="ls-field__error" role="alert">{{ t(errors.name) }}</span>
      </label>
      <label class="ls-field" for="app-form-slug">
        <span class="ls-field__label">{{ t('apps.form.slug') }}</span>
        <input
          id="app-form-slug"
          v-model="form.slug"
          class="ls-input"
          type="text"
          autocomplete="off"
          :placeholder="t('apps.form.slugPlaceholder')"
          :aria-invalid="Boolean(errors.slug)"
        />
        <span v-if="errors.slug" class="ls-field__error" role="alert">{{ t(errors.slug) }}</span>
        <span v-else class="ls-field__hint">{{ t('apps.form.slugHint') }}</span>
      </label>
      <label class="ls-field" for="app-form-root">
        <span class="ls-field__label">{{ t('apps.form.root') }}</span>
        <input id="app-form-root" v-model="form.root" class="ls-input" type="text" autocomplete="off" :placeholder="t('apps.form.rootPlaceholder')" />
        <span class="ls-field__hint">{{ t('apps.form.rootHint') }}</span>
      </label>
      <label class="ls-field" for="app-form-mode">
        <span class="ls-field__label">{{ t('apps.form.deployMode') }}</span>
        <AppSelect id="app-form-mode" v-model="form.deployMode" class="ls-input">
          <option v-for="option in modeOptions" :key="option.value" :value="option.value">
            {{ t(option.key) }}
          </option>
        </AppSelect>
        <span class="ls-field__hint">{{ modeHint }}</span>
      </label>
      <label class="ls-field" for="app-form-restart">
        <span class="ls-field__label">{{ t('apps.form.restartCmd') }}</span>
        <input id="app-form-restart" v-model="form.restartCmd" class="ls-input" type="text" autocomplete="off" :placeholder="t('apps.form.restartCmdPlaceholder')" />
        <span class="ls-field__hint">{{ t('apps.form.restartCmdHint') }}</span>
      </label>
      <div class="ls-field__row">
        <label class="ls-field" for="app-healthcheck">
          <span class="ls-field__label">{{ t('apps.form.healthcheckPath') }}</span>
          <input id="app-healthcheck" v-model="form.healthcheckPath" class="ls-input" type="text" placeholder="/healthz" autocomplete="off" />
          <span class="ls-field__hint">{{ t('apps.healthcheckPathHint') }}</span>
        </label>
        <label class="ls-field" for="app-healthcheck-port">
          <span class="ls-field__label">{{ t('apps.form.healthcheckPort') }}</span>
          <input id="app-healthcheck-port" v-model="form.healthcheckPort" class="ls-input" type="number" min="0" max="65535" inputmode="numeric" autocomplete="off" />
          <span class="ls-field__hint">{{ t('apps.form.healthcheckPortHint') }}</span>
        </label>
      </div>
      <label class="ls-field" for="app-form-env">
        <span class="ls-field__label">{{ t('apps.form.envJson') }}</span>
        <textarea id="app-form-env" v-model="form.envJson" class="ls-input ls-textarea" rows="4" spellcheck="false" :placeholder="t('apps.form.envPlaceholder')" />
        <span class="ls-field__hint">{{ t('apps.envKeysOnly') }}</span>
      </label>
      <p v-if="app && app.redacted" class="ls-field__hint" data-app-env-keys>
        {{ t('apps.envKeysOnly') }}
        <template v-if="app.env_keys.length > 0">
          {{ t('apps.envKeysLead') }}{{ app.env_keys.join(t('apps.envKeysSeparator')) }}
        </template>
        <template v-if="app.env_keys_truncated">{{ t('apps.envKeysTruncated') }}</template>
      </p>
      <label class="ls-field" for="app-form-description">
        <span class="ls-field__label">{{ t('apps.form.note') }}</span>
        <textarea id="app-form-description" v-model="form.description" class="ls-input ls-textarea" rows="2" :placeholder="t('apps.form.notePlaceholder')" />
        <span class="ls-field__hint">{{ t('apps.form.noteHint') }}</span>
      </label>
      <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
      <p v-if="attempted && Object.keys(errors).length > 0" class="ls-inline-error" role="alert">
        {{ t('apps.form.invalid') }}
      </p>
    </form>
    <template #footer>
      <button type="button" class="ls-btn" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button
        id="app-form-submit"
        type="button"
        class="ls-btn ls-btn--primary"
        :disabled="busy"
        :aria-busy="busy"
        @click="submit"
      >
        {{ busy ? t('common.loading') : app ? t('common.save') : t('common.create') }}
      </button>
    </template>
  </ModalDialog>
</template>
