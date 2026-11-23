<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { api, type AppItem, type DeployTemplate, type ReleasePreview, type Server } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import {
  canaryMaxFor,
  emptyReleaseForm,
  releasePayloadFromForm,
  validateReleaseForm,
  type ReleaseFormErrors,
  type ReleaseFormValues,
} from '@/lib/deploy-domain'
import { useI18n } from '@/i18n'
import ModalDialog from './ModalDialog.vue'

/**
 * 发起发布：应用 / 版本 / 模板 / 目标服务器 / 灰度策略，先「预览计划」再提交。
 * 预览是只读请求（POST /releases/preview），失败不阻断修改表单，但提交前必过校验。
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    apps: AppItem[]
    templates: DeployTemplate[]
    servers: Server[]
    initialAppId?: string
    busy?: boolean
    error?: string | null
  }>(),
  { initialAppId: '', busy: false, error: null },
)

const emit = defineEmits<{
  (event: 'submit', payload: ReturnType<typeof releasePayloadFromForm>): void
  (event: 'close'): void
}>()

const { t } = useI18n()
const form = reactive<ReleaseFormValues>(emptyReleaseForm())
const errors = ref<ReleaseFormErrors>({})
const attempted = ref(false)

const preview = ref<ReleasePreview | null>(null)
const previewing = ref(false)
const previewError = ref<string | null>(null)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    Object.assign(form, emptyReleaseForm(props.initialAppId))
    errors.value = {}
    attempted.value = false
    preview.value = null
    previewError.value = null
  },
  { immediate: true },
)

const canaryMax = computed(() => canaryMaxFor(form.serverIds.length))
const canaryDisabled = computed(() => form.serverIds.length < 2)

function toggleServer(id: string): void {
  const index = form.serverIds.indexOf(id)
  if (index === -1) form.serverIds.push(id)
  else form.serverIds.splice(index, 1)
  if (form.serverIds.length < 2 && form.strategy === 'canary') form.strategy = 'all'
}

function previewPayload(): { app_id: string; template_id: string; version: string; artifact_id?: string; server_ids: string[] } {
  return {
    app_id: form.appId.trim(),
    template_id: form.templateId.trim(),
    version: form.version.trim(),
    artifact_id: form.artifactId.trim() || undefined,
    server_ids: form.serverIds.slice(),
  }
}

async function runPreview(): Promise<void> {
  previewError.value = null
  const found = validateReleaseForm(form)
  errors.value = found
  attempted.value = true
  if (found.appId || found.version || found.templateId || found.serverIds) return
  previewing.value = true
  try {
    preview.value = await api.previewRelease(previewPayload())
  } catch (caught) {
    preview.value = null
    previewError.value = errorMessage(caught, t('apps.releaseForm.previewFailed'))
  } finally {
    previewing.value = false
  }
}

function submit(): void {
  attempted.value = true
  const found = validateReleaseForm(form)
  errors.value = found
  if (Object.keys(found).length > 0) return
  emit('submit', releasePayloadFromForm(form))
}
</script>

<template>
  <ModalDialog
    :open="open"
    :title="t('apps.releaseForm.title')"
    :description="t('apps.releaseForm.description')"
    test-id="release-form-dialog"
    @close="emit('close')"
  >
    <form class="ls-form" novalidate @submit.prevent="submit">
      <label class="ls-field" for="release-form-app">
        <span class="ls-field__label">{{ t('apps.releaseForm.app') }}</span>
        <select id="release-form-app" v-model="form.appId" class="ls-input">
          <option value="">{{ t('apps.releaseForm.appPlaceholder') }}</option>
          <option v-for="app in apps" :key="app.id" :value="app.id">{{ app.name }}</option>
        </select>
        <span v-if="errors.appId" class="ls-field__error" role="alert">{{ t(errors.appId) }}</span>
      </label>
      <div class="ls-form__row">
        <label class="ls-field" for="release-form-version">
          <span class="ls-field__label">{{ t('apps.releaseForm.version') }}</span>
          <input id="release-form-version" v-model="form.version" class="ls-input" type="text" autocomplete="off" />
          <span v-if="errors.version" class="ls-field__error" role="alert">{{ t(errors.version) }}</span>
        </label>
        <label class="ls-field" for="release-form-template">
          <span class="ls-field__label">{{ t('apps.releaseForm.template') }}</span>
          <select id="release-form-template" v-model="form.templateId" class="ls-input">
            <option value="">{{ t('apps.releaseForm.templatePlaceholder') }}</option>
            <option v-for="template in templates" :key="template.id" :value="template.id">
              {{ template.name }}（{{ template.kind }}）
            </option>
          </select>
          <span v-if="errors.templateId" class="ls-field__error" role="alert">{{ t(errors.templateId) }}</span>
        </label>
      </div>
      <label class="ls-field" for="release-form-artifact">
        <span class="ls-field__label">{{ t('apps.releaseForm.artifact') }}</span>
        <input id="release-form-artifact" v-model="form.artifactId" class="ls-input" type="text" autocomplete="off" />
        <span class="ls-field__hint">{{ t('apps.releaseForm.artifactHint') }}</span>
      </label>

      <fieldset class="ls-form__group">
        <legend class="ls-field__label">{{ t('apps.releaseForm.targets') }}</legend>
        <ul class="ls-modal__choices ls-modal__choices--scroll" data-testid="release-targets">
          <li v-for="server in servers" :key="server.id">
            <label class="ls-modal__choice">
              <input
                type="checkbox"
                :checked="form.serverIds.includes(server.id)"
                :value="server.id"
                @change="toggleServer(server.id)"
              />
              <span class="ls-modal__choice-main">{{ server.name }}</span>
              <span class="ls-modal__choice-meta">{{ server.host }}</span>
            </label>
          </li>
        </ul>
        <span v-if="errors.serverIds" class="ls-field__error" role="alert">{{ t(errors.serverIds) }}</span>
      </fieldset>

      <fieldset class="ls-form__group">
        <legend class="ls-field__label">{{ t('release.strategy') }}</legend>
        <label class="ls-modal__choice">
          <input v-model="form.strategy" type="radio" value="all" name="release-strategy" />
          <span class="ls-modal__choice-main">{{ t('release.strategyAll') }}</span>
        </label>
        <label class="ls-modal__choice">
          <input
            v-model="form.strategy"
            type="radio"
            value="canary"
            name="release-strategy"
            :disabled="canaryDisabled"
          />
          <span class="ls-modal__choice-main">{{ t('release.strategyCanary') }}</span>
          <span class="ls-modal__choice-meta">
            {{ canaryDisabled ? t('release.canaryDisabled') : t('release.canaryCount') + ' ' + canaryMax }}
          </span>
        </label>
        <div v-if="form.strategy === 'canary'" class="ls-form__row">
          <label class="ls-field" for="release-form-canary">
            <span class="ls-field__label">{{ t('release.canaryCount') }}</span>
            <input id="release-form-canary" v-model="form.canaryCount" class="ls-input" type="number" min="1" />
            <span v-if="errors.canaryCount" class="ls-field__error" role="alert">{{ t(errors.canaryCount) }}</span>
          </label>
          <label class="ls-field" for="release-form-hold">
            <span class="ls-field__label">{{ t('release.holdMinutes') }}</span>
            <input id="release-form-hold" v-model="form.holdMinutes" class="ls-input" type="number" min="0" />
            <span v-if="errors.holdMinutes" class="ls-field__error" role="alert">{{ t(errors.holdMinutes) }}</span>
          </label>
        </div>
      </fieldset>

      <div class="ls-form__actions">
        <button id="release-form-preview" type="button" class="ls-btn" :disabled="previewing" @click="runPreview">
          {{ previewing ? t('common.loading') : t('release.preview') }}
        </button>
      </div>
      <div v-if="previewError" class="ls-inline-error" role="alert">{{ previewError }}</div>
      <div v-else-if="preview" class="ls-preview" data-testid="release-preview">
        <p class="ls-preview__title">
          {{ t('release.previewTitle') }} · v{{ preview.version }} · {{ preview.servers.length }}
        </p>
        <ul class="ls-preview__servers">
          <li v-for="plan in preview.servers" :key="plan.server_id" class="ls-preview__server">
            <span class="ls-preview__name">{{ plan.server_name }}</span>
            <span class="ls-preview__steps">{{ t('apps.releaseForm.stepCount', { count: plan.steps.length }) }}</span>
          </li>
        </ul>
      </div>
      <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
      <p v-if="attempted && Object.keys(errors).length > 0" class="ls-inline-error" role="alert">
        {{ t('apps.releaseForm.invalid') }}
      </p>
    </form>
    <template #footer>
      <button type="button" class="ls-btn" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button
        id="release-form-submit"
        type="button"
        class="ls-btn ls-btn--primary"
        :disabled="busy"
        :aria-busy="busy"
        @click="submit"
      >
        {{ busy ? t('common.loading') : t('apps.releaseForm.submit') }}
      </button>
    </template>
  </ModalDialog>
</template>
