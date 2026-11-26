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
const step = ref<1 | 2>(1)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    Object.assign(form, props.app ? appFormFromItem(props.app) : emptyAppForm())
    errors.value = {}
    attempted.value = false
    step.value = props.app ? 2 : 1
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

const scenarioOptions = [
  { value: 'command', key: 'apps.scenario.command', hintKey: 'apps.scenarioCommandHint' },
  { value: 'inplace', key: 'apps.scenario.inplace', hintKey: 'apps.scenarioInplaceHint' },
  { value: 'release', key: 'apps.scenario.release', hintKey: 'apps.scenarioReleaseHint' },
  { value: 'compose', key: 'apps.scenario.compose', hintKey: 'apps.scenarioComposeHint' },
  { value: 'systemd', key: 'apps.scenario.systemd', hintKey: 'apps.scenarioSystemdHint' },
  { value: 'baota', key: 'apps.scenario.baota', hintKey: 'apps.scenarioBaotaHint' },
] as const

const scenarioHint = computed(() => {
  const option = scenarioOptions.find((item) => item.value === form.scenario) ?? scenarioOptions[0]
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
      <template v-if="step === 1">
      <div class="ls-field">
        <span id="app-form-scenario-label" class="ls-field__label">{{ t('apps.form.scenario') }}</span>
        <div id="app-form-scenario" class="ls-mode-picker ls-mode-picker--wrap" role="radiogroup" aria-labelledby="app-form-scenario-label">
          <button
            v-for="option in scenarioOptions"
            :id="`app-form-scenario-${option.value}`"
            :key="option.value"
            class="ls-mode-picker__item"
            :class="{ 'is-active': form.scenario === option.value }"
            type="button"
            role="radio"
            :aria-checked="form.scenario === option.value"
            @click="form.scenario = option.value"
          >
            {{ t(option.key) }}
          </button>
        </div>
        <span class="ls-field__hint">{{ scenarioHint }}</span>
      </div>

      </template>

      <template v-if="step === 2">
      <label v-if="form.scenario !== 'baota'" class="ls-field" for="app-form-root">
        <span class="ls-field__label">
          {{ form.scenario === 'command' ? t('apps.form.workDir') : form.scenario === 'inplace' ? t('apps.form.targetDir') : t('apps.form.root') }}
        </span>
        <input id="app-form-root" v-model="form.root" class="ls-input" type="text" autocomplete="off" :placeholder="form.scenario === 'command' ? '/www/wwwroot/app' : form.scenario === 'inplace' ? '/opt/app' : '/srv/app'" />
        <span class="ls-field__hint">
          {{ form.scenario === 'command' ? t('apps.form.workDirHint') : form.scenario === 'inplace' ? t('apps.form.targetDirHint') : t('apps.form.rootHint') }}
        </span>
        <span v-if="errors.root" class="ls-field__error" role="alert">{{ t(errors.root) }}</span>
      </label>

      <template v-if="form.scenario === 'command'">
        <label class="ls-field" for="app-form-deploy-script">
          <span class="ls-field__label">{{ t('apps.form.deployScript') }}</span>
          <textarea id="app-form-deploy-script" v-model="form.deployScript" class="ls-input ls-textarea" rows="7" spellcheck="false" :placeholder="t('apps.form.deployScriptPlaceholder')" />
          <span v-if="errors.deployScript" class="ls-field__error" role="alert">{{ t(errors.deployScript) }}</span>
          <span class="ls-field__hint">{{ t('apps.form.deployScriptHint') }}</span>
        </label>
        <label class="ls-field" for="app-form-rollback-script">
          <span class="ls-field__label">{{ t('apps.form.rollbackScript') }}</span>
          <textarea id="app-form-rollback-script" v-model="form.rollbackScript" class="ls-input ls-textarea" rows="4" spellcheck="false" :placeholder="t('apps.form.scriptPlaceholder')" />
          <span class="ls-field__hint">{{ t('apps.form.rollbackScriptHint') }}</span>
        </label>
      </template>

      <template v-else-if="form.scenario === 'inplace'">
        <label class="ls-field" for="app-form-keep-paths">
          <span class="ls-field__label">{{ t('apps.form.keepPaths') }}</span>
          <input id="app-form-keep-paths" v-model="form.keepPaths" class="ls-input" type="text" autocomplete="off" placeholder="storage/,uploads/,.env" />
          <span class="ls-field__hint">{{ t('apps.form.keepPathsHint') }}</span>
        </label>
        <label class="ls-field" for="app-form-restart">
          <span class="ls-field__label">{{ t('apps.form.restartCmd') }}</span>
          <input id="app-form-restart" v-model="form.restartCmd" class="ls-input" type="text" :placeholder="t('apps.form.restartCmdPlaceholder')" />
          <span class="ls-field__hint">{{ t('apps.form.restartCmdHint') }}</span>
        </label>
        <label class="ls-field" for="app-form-init-script">
          <span class="ls-field__label">{{ t('apps.form.initScript') }}</span>
          <textarea id="app-form-init-script" v-model="form.initScript" class="ls-input ls-textarea" rows="3" spellcheck="false" :placeholder="t('apps.form.scriptPlaceholder')" />
          <span class="ls-field__hint">{{ t('apps.form.initScriptHint') }}</span>
        </label>
      </template>

      <template v-else-if="form.scenario === 'release'">
        <label class="ls-field" for="app-form-template-content">
          <span class="ls-field__label">{{ t('apps.form.startScript') }}</span>
          <textarea id="app-form-template-content" v-model="form.deployTemplateContent" class="ls-input ls-textarea" rows="7" spellcheck="false" :placeholder="t('apps.form.startScriptPlaceholder')" />
          <span class="ls-field__hint">{{ t('apps.form.startScriptHint') }}</span>
        </label>
      </template>

      <template v-else-if="form.scenario === 'compose' || form.scenario === 'systemd'">
        <label class="ls-field" for="app-form-template-content">
          <span class="ls-field__label">{{ form.scenario === 'compose' ? t('apps.form.composeContent') : t('apps.form.systemdContent') }}</span>
          <textarea id="app-form-template-content" v-model="form.deployTemplateContent" class="ls-input ls-textarea" rows="10" spellcheck="false" :placeholder="form.scenario === 'compose' ? t('apps.form.composePlaceholder') : t('apps.form.systemdPlaceholder')" />
          <span v-if="errors.deployTemplateContent" class="ls-field__error" role="alert">{{ t(errors.deployTemplateContent) }}</span>
          <span class="ls-field__hint">{{ t('apps.form.templateContentHint') }}</span>
        </label>
      </template>

      <template v-else>
        <label class="ls-field" for="app-form-site-name">
          <span class="ls-field__label">{{ t('apps.form.siteName') }}</span>
          <input id="app-form-site-name" v-model="form.siteName" class="ls-input" type="text" :placeholder="t('apps.form.siteNamePlaceholder')" />
        </label>
        <div class="ls-field__row">
          <label class="ls-field" for="app-form-site-port">
            <span class="ls-field__label">{{ t('apps.form.sitePort') }}</span>
            <input id="app-form-site-port" v-model="form.sitePort" class="ls-input" type="number" min="1" max="65535" />
          </label>
          <label class="ls-field" for="app-form-site-type">
            <span class="ls-field__label">{{ t('apps.form.siteType') }}</span>
            <AppSelect id="app-form-site-type" v-model="form.siteType" class="ls-input">
              <option value="static">{{ t('apps.form.siteTypeStatic') }}</option>
              <option value="php">{{ t('apps.form.siteTypePhp') }}</option>
            </AppSelect>
          </label>
        </div>
      </template>

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
      <label v-if="form.scenario !== 'baota'" class="ls-field" for="app-form-env">
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
      </template>
      <p v-if="error" class="ls-inline-error" role="alert">{{ error }}</p>
      <p v-if="attempted && Object.keys(errors).length > 0" class="ls-inline-error" role="alert">
        {{ t('apps.form.invalid') }}
      </p>
    </form>
    <template #footer>
      <button type="button" class="ls-btn" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button v-if="step === 2" type="button" class="ls-btn" :disabled="busy" @click="step = 1">
        {{ t('apps.form.back') }}
      </button>
      <button
        v-if="step === 1"
        id="app-form-next"
        type="button"
        class="ls-btn ls-btn--primary"
        :disabled="busy"
        @click="step = 2"
      >
        {{ t('apps.form.next') }}
      </button>
      <button
        v-else
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
