<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'
import {
  META_FIELD_MAX,
  META_KEY_MAX,
  META_NOTES_MAX,
  META_PRESET_KEYS,
  META_VALUE_MAX,
  metaFilledFieldCount,
  metaRowsFromFields,
  metaValidate,
  newMetaRow,
  type MetaIssue,
  type MetaRow,
} from '@/lib/server-detail'

/**
 * 「服务器档案」标签：备注 + 自定义字段。
 *
 * 草稿留在组件内，提交与「保存后重新读取详情」由页面负责（PATCH 响应不含档案）。
 * 校验走 lib/server-detail 的 metaValidate，错误文案用 server.meta.error.* 统一映射。
 */
const props = defineProps<{
  server: Server
  saving: boolean
  error: string | null
  notice: string | null
  reloadError: string | null
  canWrite: boolean
  /** 保存成功后页面重新读回详情时递增，用来把草稿同步成服务端值。 */
  savedAt: number
}>()

const emit = defineEmits<{
  (event: 'save', payload: { notes: string; rows: MetaRow[] }): void
}>()

const { t } = useI18n()

const notes = ref(props.server.notes)
const rows = ref<MetaRow[]>(metaRowsFromFields(props.server.custom_fields))
const issue = ref<MetaIssue | null>(null)
const dirty = ref(false)

function resetDraft(): void {
  notes.value = props.server.notes
  rows.value = metaRowsFromFields(props.server.custom_fields)
  issue.value = null
  dirty.value = false
}

watch(
  () => [props.server.id, props.savedAt],
  () => resetDraft(),
)

const notesLength = computed(() => [...notes.value].length)
const filledCount = computed(() => metaFilledFieldCount(rows.value))

const issueText = computed(() => {
  if (!issue.value) return ''
  return t(`server.meta.error.${issue.value.code}`)
})

function rowIssue(index: number): string {
  const current = issue.value
  if (!current || current.index !== index) return ''
  return t(`server.meta.error.${current.code}`)
}

function markDirty(): void {
  dirty.value = true
  issue.value = null
}

function addField(_key = '', name = ''): void {
  if (!props.canWrite) return
  rows.value = [...rows.value, newMetaRow(name, '')]
  markDirty()
}

function addPreset(key: string): void {
  const name = t(key)
  if (rows.value.some((row) => row.key.trim() === name)) return
  addField('', name)
}

function removeField(index: number): void {
  if (!props.canWrite) return
  rows.value = rows.value.filter((_, i) => i !== index)
  if (rows.value.length === 0) rows.value = [newMetaRow()]
  markDirty()
}

function submit(): void {
  if (!props.canWrite || props.saving) return
  const found = metaValidate(notes.value, rows.value)
  issue.value = found
  if (found) return
  dirty.value = false
  emit('save', { notes: notes.value, rows: rows.value })
}
</script>

<template>
  <div class="ls-detail__stack">
    <section class="ls-card" :aria-label="t('server.meta.title')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="file-text" size="1rem" />
            {{ t('server.meta.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.meta.hint') }}</p>
        </div>
        <div class="ls-card__actions">
          <button
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="saving || !dirty || !canWrite"
            @click="resetDraft"
          >
            {{ t('server.meta.reset') }}
          </button>
          <button
            class="ls-btn ls-btn--primary ls-btn--small"
            type="button"
            :disabled="saving || !canWrite"
            @click="submit"
          >
            <span v-if="saving" class="ls-spinner" aria-hidden="true" />
            {{ saving ? t('server.meta.saving') : t('server.meta.save') }}
          </button>
        </div>
      </header>

      <p v-if="!canWrite" class="ls-card__note">{{ t('server.meta.readOnly') }}</p>

      <div v-if="error" class="ls-notice ls-notice--error" role="alert">
        <div class="ls-notice__body">
          <p class="ls-notice__title">{{ t('server.meta.saveFailed') }}</p>
          <p class="ls-notice__text">{{ error }}</p>
        </div>
      </div>
      <p v-if="reloadError" class="ls-banner ls-banner--warning" role="alert">
        {{ t('server.meta.reloadFailed') }}{{ reloadError }}
      </p>
      <p v-if="notice" class="ls-ok-hint" role="status">
        <AppIcon name="shield-check" size="0.875rem" />
        {{ notice }}
      </p>

      <div class="ls-field">
        <div class="ls-field__row">
          <label class="ls-field__label" for="server-meta-notes">{{ t('server.meta.notesLabel') }}</label>
          <span class="ls-field__counter" :class="{ 'ls-field__counter--over': notesLength > META_NOTES_MAX }">
            {{ notesLength }} / {{ META_NOTES_MAX }}
          </span>
        </div>
        <textarea
          id="server-meta-notes"
          v-model="notes"
          class="ls-input ls-textarea"
          rows="4"
          :maxlength="META_NOTES_MAX"
          :placeholder="t('server.meta.notesPlaceholder')"
          :disabled="!canWrite || saving"
          @input="markDirty"
        />
        <p class="ls-field__hint">{{ t('server.meta.notesHint') }}</p>
      </div>

      <div class="ls-field">
        <div class="ls-field__row">
          <span class="ls-field__label">{{ t('server.meta.fieldsLabel') }}</span>
          <span class="ls-field__counter">{{ filledCount }} / {{ META_FIELD_MAX }}</span>
        </div>

        <p v-if="rows.length === 0" class="ls-card__note">{{ t('server.meta.fieldsEmpty') }}</p>

        <ul class="ls-meta-rows">
          <li v-for="(row, index) in rows" :key="row.id" class="ls-meta-row">
            <input
              v-model="row.key"
              class="ls-input ls-meta-row__key"
              type="text"
              :maxlength="META_KEY_MAX"
              :placeholder="t('server.meta.keyPlaceholder')"
              :aria-label="`${t('server.meta.fieldKey')} ${index + 1}`"
              :disabled="!canWrite || saving"
              @input="markDirty"
            />
            <input
              v-model="row.value"
              class="ls-input ls-meta-row__value"
              type="text"
              :maxlength="META_VALUE_MAX"
              :placeholder="t('server.meta.valuePlaceholder')"
              :aria-label="`${t('server.meta.fieldValue')} ${index + 1}`"
              :disabled="!canWrite || saving"
              @input="markDirty"
            />
            <button
              class="ls-icon-btn ls-meta-row__remove"
              type="button"
              :aria-label="t('server.meta.removeField')"
              :title="t('server.meta.removeField')"
              :disabled="!canWrite || saving"
              @click="removeField(index)"
            >
              <AppIcon name="close" size="0.875rem" />
            </button>
            <p v-if="rowIssue(index)" class="ls-inline-error">{{ rowIssue(index) }}</p>
          </li>
        </ul>

        <div class="ls-meta-actions">
          <button
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="!canWrite || saving || filledCount >= META_FIELD_MAX"
            @click="addField()"
          >
            <AppIcon name="plus" size="0.875rem" />
            {{ t('server.meta.addField') }}
          </button>
          <span class="ls-meta-presets">
            <span class="ls-field__hint">{{ t('server.meta.presetsLabel') }}</span>
            <button
              v-for="key in META_PRESET_KEYS"
              :key="key"
              class="ls-chip ls-chip--button"
              type="button"
              :disabled="!canWrite || saving || filledCount >= META_FIELD_MAX"
              @click="addPreset(key)"
            >
              {{ t(key) }}
            </button>
          </span>
        </div>
      </div>

      <p v-if="issue && issue.index === -1" class="ls-inline-error" role="alert">{{ issueText }}</p>
      <p class="ls-field__hint">{{ t('server.meta.saveHint') }}</p>
    </section>
  </div>
</template>
