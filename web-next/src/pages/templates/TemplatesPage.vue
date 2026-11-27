<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppSelect from '@/components/AppSelect.vue'

import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import ScriptUploadButton from '@/components/ScriptUploadButton.vue'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, TEMPLATE_PLACEHOLDERS, type CreateTemplatePayload, type DeployTemplate } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import {
  emptyTemplateForm,
  filterTemplates,
  TEMPLATE_KINDS,
  TEMPLATE_SAMPLES,
  templateFormFromItem,
  templateKindLabelKey,
  templatePatchFromForm,
  templatePayloadFromForm,
  validateTemplateForm,
  type TemplateFormErrors,
  type TemplateFormValues,
} from '@/lib/template-form'

/**
 * 应用模板页（挂载在 AppShell 默认 slot → .ls-main__inner，路由 /templates）。
 *
 * 数据 owner 就是本页：列表 + 三态 + 写操作（新建 / 编辑 / 删除）。
 * 权限：无 deploy.read 不发列表请求；无 deploy.write 时「新建」「编辑」「删除」
 * 三个写入口整排不渲染，页面不会发出任何写请求。写权限的「全局范围」要求由后端判定，
 * 后端 403 文案原样展示，前端不自行拦截。
 */
const { t } = useI18n()
const { can } = useCan()

const canRead = computed(() => can('deploy.read'))
const canWrite = computed(() => can('deploy.write'))

const state = ref<'loading' | 'ready' | 'error'>('loading')
const loaded = ref(false)
const refreshing = ref(false)
const templates = ref<DeployTemplate[]>([])
const error = ref<string | null>(null)
const keyword = ref('')
const notice = ref('')

const writeBusy = ref(false)
const writeError = ref<string | null>(null)

const dialog = ref<'create' | 'edit' | null>(null)
const editing = ref<DeployTemplate | null>(null)
const form = ref<TemplateFormValues>(emptyTemplateForm())
const formErrors = ref<TemplateFormErrors>({})
const formAttempted = ref(false)

const deleting = ref<DeployTemplate | null>(null)
const contentEl = ref<HTMLTextAreaElement | null>(null)

const loading = computed(() => state.value === 'loading')
const visibleTemplates = computed(() =>
  filterTemplates(templates.value, keyword.value, (item) => t(templateKindLabelKey(item.kind))),
)
const formTitle = computed(() =>
  editing.value ? t('templates.form.editTitle') : t('templates.form.createTitle'),
)

const deleteDetails = computed(() => {
  const target = deleting.value
  if (!target) return []
  return [
    t('templates.delete.detailName', { name: target.name }),
    t('templates.delete.detailKind', { kind: t(templateKindLabelKey(target.kind)) }),
    t('templates.delete.detailImpact'),
  ]
})

async function load(options: { silent?: boolean } = {}): Promise<void> {
  if (!canRead.value) return
  if (options.silent) refreshing.value = true
  else state.value = 'loading'
  try {
    templates.value = await api.templates()
    state.value = 'ready'
    error.value = null
    loaded.value = true
    // 手动刷新成功后清掉上一次的成功提示，避免旧「已创建」提示挂在新列表上。
    if (options.silent) notice.value = ''
  } catch (caught) {
    error.value = errorMessage(caught, t('templates.loadFailed'))
    if (options.silent) notice.value = ''
    state.value = 'error'
  } finally {
    refreshing.value = false
  }
}

// 权限在会话校验完成后才可用：允许时补一次首屏加载，避免直接刷新 /templates 时空白。
watch(
  canRead,
  (allowed) => {
    if (allowed && !loaded.value) void load()
  },
  { immediate: true },
)

function clearWriteError(): void {
  writeError.value = null
}

/** 在光标处插入 {{NAME}}；没有焦点时追加到末尾。 */
function insertPlaceholder(name: string): void {
  const token = `{{${name}}}`
  const el = contentEl.value
  if (!el) {
    form.value.content += token
    return
  }
  const start = el.selectionStart ?? form.value.content.length
  const end = el.selectionEnd ?? start
  const before = form.value.content.slice(0, start)
  const after = form.value.content.slice(end)
  form.value.content = `${before}${token}${after}`
  void nextTick(() => {
    el.focus()
    el.selectionStart = start + token.length
    el.selectionEnd = start + token.length
  })
}

async function write(action: () => Promise<unknown>): Promise<boolean> {
  writeBusy.value = true
  try {
    await action()
    await load({ silent: true })
    return true
  } catch (caught) {
    writeError.value = errorMessage(caught, t('common.error'))
    return false
  } finally {
    writeBusy.value = false
  }
}

function openCreate(): void {
  clearWriteError()
  editing.value = null
  form.value = emptyTemplateForm()
  formErrors.value = {}
  formAttempted.value = false
  dialog.value = 'create'
}

function openEdit(item: DeployTemplate): void {
  clearWriteError()
  editing.value = item
  form.value = templateFormFromItem(item)
  formErrors.value = {}
  formAttempted.value = false
  dialog.value = 'edit'
}

function closeForm(): void {
  if (writeBusy.value) return
  dialog.value = null
  editing.value = null
}

async function submitForm(): Promise<void> {
  if (writeBusy.value) return
  formAttempted.value = true
  const found = validateTemplateForm(form.value)
  formErrors.value = found
  if (Object.keys(found).length > 0) return

  const payload: CreateTemplatePayload = templatePayloadFromForm(form.value)
  const target = editing.value
  if (target) {
    // 编辑只提交变更字段（后端 PATCH 三字段皆可选）；无变更就不发请求，直接收起弹窗。
    const patch = templatePatchFromForm(target, form.value)
    if (Object.keys(patch).length === 0) {
      dialog.value = null
      editing.value = null
      return
    }
    const saved = await write(() => api.updateTemplate(target.id, patch))
    if (!saved) return
    notice.value = t('templates.saved', { name: payload.name })
  } else {
    const created = await write(() => api.createTemplate(payload))
    if (!created) return
    notice.value = t('templates.created', { name: payload.name })
  }
  dialog.value = null
  editing.value = null
}

function openDelete(item: DeployTemplate): void {
  clearWriteError()
  deleting.value = item
}

function closeDelete(): void {
  if (writeBusy.value) return
  deleting.value = null
}

async function confirmDelete(): Promise<void> {
  const target = deleting.value
  if (!target || writeBusy.value) return
  const ok = await write(() => api.deleteTemplate(target.id))
  if (!ok) return
  notice.value = t('templates.deleted', { name: target.name })
  deleting.value = null
}

function applySample(sample: (typeof TEMPLATE_SAMPLES)[number]): void {
  form.value = {
    name: t(sample.nameKey),
    kind: sample.kind,
    content: sample.content,
  }
  formErrors.value = {}
  formAttempted.value = false
}

function firstLine(content: string): string {
  const line = content.split('\n').find((entry) => entry.trim() !== '')
  return line ? line.trim() : ''
}
</script>

<template>
  <section class="ls-templates" aria-labelledby="templates-title">
    <header class="ls-templates__head">
      <div class="ls-templates__heading">
        <h1 id="templates-title" class="ls-templates__title">{{ t('templates.title') }}</h1>
        <p class="ls-templates__subtitle">{{ t('templates.description') }}</p>
      </div>
      <div class="ls-templates__actions">
        <button
          type="button"
          class="ls-btn"
          :disabled="loading || refreshing"
          :aria-busy="refreshing"
          @click="load({ silent: true })"
        >
          <AppIcon name="refresh" size="1rem" />
          {{ refreshing ? t('common.loading') : t('common.refresh') }}
        </button>
        <button
          v-if="canWrite"
          id="templates-new"
          type="button"
          class="ls-btn ls-btn--primary"
          @click="openCreate"
        >
          <AppIcon name="plus" size="1rem" />
          {{ t('templates.new') }}
        </button>
      </div>
    </header>

    <div v-if="!canRead" class="ls-notice" role="status">
      <p class="ls-notice__title">{{ t('templates.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('templates.noPermissionText') }}</p>
    </div>

    <div v-if="error && loaded" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('templates.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button type="button" class="ls-notice__action" @click="load()">{{ t('common.retry') }}</button>
    </div>

    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>

    <div v-if="canRead" class="ls-templates__bar">
      <label class="ls-field ls-templates__search" for="templates-search">
        <span class="ls-visually-hidden">{{ t('common.search') }}</span>
        <input
          id="templates-search"
          v-model="keyword"
          class="ls-input"
          type="search"
          :placeholder="t('templates.searchPlaceholder')"
          :aria-label="t('common.search')"
        />
      </label>
      <p class="ls-templates__count" aria-live="polite">
        {{ t('templates.count', { count: templates.length }) }}
      </p>
    </div>

    <template v-if="canRead">
      <div v-if="loading && !loaded" class="ls-templates__skeleton" aria-busy="true">
        <div v-for="n in 3" :key="n" class="ls-skeleton ls-templates__skeleton-row" />
      </div>

      <div v-else-if="error && !loaded" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('templates.loadFailed') }}</p>
        <p class="ls-notice__text">{{ error }}</p>
        <button type="button" class="ls-notice__action" @click="load()">{{ t('common.retry') }}</button>
      </div>

      <div v-else-if="templates.length === 0" class="ls-empty">
        <p class="ls-empty__title">{{ t('templates.emptyTitle') }}</p>
        <p class="ls-empty__text">{{ t('templates.emptyText') }}</p>
        <button v-if="canWrite" type="button" class="ls-btn ls-btn--primary" @click="openCreate">
          {{ t('templates.new') }}
        </button>
      </div>

      <div v-else-if="visibleTemplates.length === 0" class="ls-empty">
        <p class="ls-empty__title">{{ t('templates.noMatchTitle') }}</p>
        <p class="ls-empty__text">{{ t('templates.noMatchText', { keyword }) }}</p>
        <button type="button" class="ls-btn" @click="keyword = ''">{{ t('templates.clearSearch') }}</button>
      </div>

      <div v-else class="ls-templates__scroll">
        <table class="ls-templates__table">
          <caption class="ls-visually-hidden">{{ t('templates.tableCaption') }}</caption>
          <thead>
            <tr>
              <th scope="col">{{ t('templates.col.name') }}</th>
              <th scope="col">{{ t('templates.col.kind') }}</th>
              <th scope="col">{{ t('templates.col.updatedAt') }}</th>
              <th scope="col" class="ls-templates__actions-col">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="item in visibleTemplates"
              :key="item.id"
              class="ls-templates__row"
              :data-template-id="item.id"
            >
              <td>
                <span class="ls-templates__name" :title="item.name">{{ item.name }}</span>
                <span
                  v-if="firstLine(item.content)"
                  class="ls-templates__preview"
                  :title="firstLine(item.content)"
                >
                  {{ firstLine(item.content) }}
                </span>
              </td>
              <td>
                <span class="ls-templates__kind">{{ t(templateKindLabelKey(item.kind)) }}</span>
              </td>
              <td class="ls-templates__time">{{ formatDateTime(item.updated_at ?? item.created_at) }}</td>
              <td class="ls-templates__actions-col">
                <div v-if="canWrite" class="ls-templates__row-actions">
                  <button
                    type="button"
                    class="ls-btn ls-btn--small"
                    :aria-label="t('templates.editNamed', { name: item.name })"
                    @click="openEdit(item)"
                  >
                    {{ t('common.edit') }}
                  </button>
                  <button
                    type="button"
                    class="ls-btn ls-btn--small ls-btn--danger"
                    :aria-label="t('templates.deleteNamed', { name: item.name })"
                    @click="openDelete(item)"
                  >
                    {{ t('common.delete') }}
                  </button>
                </div>
                <span v-else class="ls-templates__readonly">{{ t('templates.readOnly') }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppModal
      v-if="dialog !== null"
      :title="formTitle"
      :description="t('templates.form.description')"
      size="md"
      @close="closeForm"
    >
      <form class="ls-form ls-templates__form" novalidate @submit.prevent="submitForm">
        <label class="ls-field" for="template-form-name">
          <span class="ls-field__label">{{ t('templates.form.name') }}</span>
          <input
            id="template-form-name"
            v-model="form.name"
            class="ls-input"
            type="text"
            autocomplete="off"
            data-autofocus
            :placeholder="t('templates.form.namePlaceholder')"
            :aria-invalid="Boolean(formErrors.name)"
            @keydown.enter.prevent="submitForm"
          />
          <span v-if="formErrors.name" class="ls-field__error" role="alert">{{ t(formErrors.name) }}</span>
        </label>

        <label class="ls-field" for="template-form-kind">
          <span class="ls-field__label">{{ t('templates.form.kind') }}</span>
          <AppSelect id="template-form-kind" v-model="form.kind" class="ls-input">
            <option v-for="kind in TEMPLATE_KINDS" :key="kind" :value="kind">
              {{ t(templateKindLabelKey(kind)) }}
            </option>
          </AppSelect>
          <span class="ls-field__hint">{{ t(`templates.kind.${form.kind}Hint`) }}</span>
        </label>

        <div class="ls-templates__samples">
          <span class="ls-field__label">{{ t('templates.form.samples') }}</span>
          <div class="ls-chip-group">
            <button
              v-for="sample in TEMPLATE_SAMPLES"
              :key="sample.key"
              class="ls-chip ls-chip--button"
              type="button"
              @click="applySample(sample)"
            >
              {{ t(sample.nameKey) }}
            </button>
          </div>
        </div>

        <label class="ls-field" for="template-form-content">
          <span class="ls-field__label">{{ t('templates.form.content') }}</span>
          <textarea
            id="template-form-content"
            ref="contentEl"
            v-model="form.content"
            class="ls-input ls-textarea ls-templates__code"
            spellcheck="false"
            :aria-invalid="Boolean(formErrors.content)"
            :aria-describedby="formErrors.content ? undefined : 'template-form-content-hint'"
          />
          <ScriptUploadButton :accept="form.kind === 'compose' || form.kind === 'systemd' ? '.yml,.yaml,.service,.txt,.conf' : '.sh,.bash,.zsh,.txt'" @load="form.content = $event" />
          <span v-if="formErrors.content" class="ls-field__error" role="alert">
            {{ t(formErrors.content) }}
          </span>
          <span v-else id="template-form-content-hint" class="ls-field__hint">
            {{ t('templates.form.contentHint') }}
          </span>
        </label>

        <div class="ls-templates__placeholders">
          <span class="ls-field__label">{{ t('templates.form.placeholders') }}</span>
          <div class="ls-chip-group">
            <button
              v-for="placeholder in TEMPLATE_PLACEHOLDERS"
              :key="placeholder.name"
              class="ls-chip ls-chip--button"
              type="button"
              :title="placeholder.description"
              @click="insertPlaceholder(placeholder.name)"
            >
              {{ placeholder.name }}
            </button>
          </div>
        </div>

        <p v-if="writeError" class="ls-inline-error" role="alert">{{ writeError }}</p>
        <p v-if="formAttempted && Object.keys(formErrors).length > 0" class="ls-inline-error" role="alert">
          {{ t('templates.form.invalid') }}
        </p>
      </form>

      <template #footer>
        <button type="button" class="ls-btn" :disabled="writeBusy" @click="closeForm">
          {{ t('common.cancel') }}
        </button>
        <button
          id="template-form-submit"
          type="button"
          class="ls-btn ls-btn--primary"
          :disabled="writeBusy"
          :aria-busy="writeBusy"
          @click="submitForm"
        >
          {{ writeBusy ? t('common.loading') : editing ? t('common.save') : t('common.create') }}
        </button>
      </template>
    </AppModal>

    <AppModal
      v-if="deleting !== null"
      :title="t('templates.delete.title')"
      :description="t('templates.delete.description')"
      size="sm"
      @close="closeDelete"
    >
      <div class="ls-templates__delete">
        <ul class="ls-templates__delete-list">
          <li v-for="detail in deleteDetails" :key="detail">{{ detail }}</li>
        </ul>
        <p v-if="writeError" class="ls-inline-error" role="alert">{{ writeError }}</p>
      </div>

      <template #footer>
        <button type="button" class="ls-btn" :disabled="writeBusy" @click="closeDelete">
          {{ t('common.cancel') }}
        </button>
        <button
          id="template-delete-confirm"
          type="button"
          class="ls-btn ls-btn--danger"
          :disabled="writeBusy"
          :aria-busy="writeBusy"
          @click="confirmDelete"
        >
          {{ writeBusy ? t('common.loading') : t('templates.delete.confirm') }}
        </button>
      </template>
    </AppModal>
  </section>
</template>
