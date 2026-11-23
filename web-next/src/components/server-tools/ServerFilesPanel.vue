<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import AppModal from '@/components/AppModal.vue'
import { api, type RemoteFileEntry } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatDateTime } from '@/lib/format'
import { useI18n } from '@/i18n'

/**
 * 可复用文件面板：面包屑 / 搜索 / 排序 / 上传 / 下载 / 删除 / 元信息。
 *
 * 两处使用：服务器列表行内展开与详情页「文件」tab；组件只认 serverId / allowed。
 * 危险操作（删除）必须二次确认；上传和下载都走 terminal.use 权限（后端同权限）。
 */
const props = defineProps<{
  serverId: string
  allowed: boolean
}>()

interface Crumb {
  label: string
  path: string
}

type SortKey = 'name' | 'size' | 'mod_time' | 'mode'
type SortDir = 'asc' | 'desc'

const { t } = useI18n()

const path = ref('/')
const entries = ref<RemoteFileEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const selected = ref<RemoteFileEntry | null>(null)
const keyword = ref('')
const sortKey = ref<SortKey>('name')
const sortDir = ref<SortDir>('asc')
const actionError = ref<string | null>(null)
const notice = ref<string | null>(null)
const uploading = ref(false)
const downloadingPath = ref<string | null>(null)
const deleteTarget = ref<RemoteFileEntry | null>(null)
const deleting = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

const crumbs = computed<Crumb[]>(() => {
  const parts = path.value.split('/').filter(Boolean)
  const list: Crumb[] = [{ label: '/', path: '/' }]
  let current = ''
  for (const part of parts) {
    current += `/${part}`
    list.push({ label: part, path: current })
  }
  return list
})

const parentPath = computed(() => {
  const parts = path.value.split('/').filter(Boolean)
  if (parts.length === 0) return '/'
  parts.pop()
  return parts.length === 0 ? '/' : `/${parts.join('/')}`
})

const visibleEntries = computed(() => {
  const term = keyword.value.trim().toLowerCase()
  const list = term === ''
    ? entries.value
    : entries.value.filter((entry) => entry.name.toLowerCase().includes(term))
  return [...list].sort((left, right) => {
    if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1
    let result = 0
    if (sortKey.value === 'name') result = left.name.localeCompare(right.name)
    else if (sortKey.value === 'size') result = left.size - right.size
    else if (sortKey.value === 'mod_time') result = (left.mod_time ?? 0) - (right.mod_time ?? 0)
    else result = (left.mode ?? '').localeCompare(right.mode ?? '')
    return sortDir.value === 'asc' ? result : -result
  })
})

function sortBy(key: SortKey): void {
  if (sortKey.value === key) sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  else {
    sortKey.value = key
    sortDir.value = key === 'name' ? 'asc' : 'desc'
  }
}

function sortLabel(key: SortKey): string {
  const direction = sortKey.value === key ? (sortDir.value === 'asc' ? '↑' : '↓') : ''
  return `${t(`files.${key}`)} ${direction}`.trim()
}

function joinPath(base: string, name: string): string {
  return base === '/' ? `/${name}` : `${base}/${name}`
}

let requests = 0

async function load(target: string = path.value): Promise<void> {
  const current = ++requests
  loading.value = true
  error.value = null
  actionError.value = null
  try {
    const list = await api.listFiles(props.serverId, target)
    if (current !== requests) return
    path.value = target
    entries.value = list
    selected.value = null
  } catch (caught) {
    if (current !== requests) return
    error.value = errorMessage(caught, t('files.loadFailed'))
  } finally {
    if (current === requests) loading.value = false
  }
}

function openEntry(entry: RemoteFileEntry): void {
  selected.value = entry
  if (entry.is_dir) void load(joinPath(path.value, entry.name))
}

function triggerUpload(): void {
  fileInput.value?.click()
}

async function onUpload(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || uploading.value) return
  uploading.value = true
  actionError.value = null
  notice.value = null
  try {
    const target = joinPath(path.value, file.name)
    const result = await api.uploadServerFile(props.serverId, target, file)
    notice.value = t('files.uploaded', { name: file.name, size: formatBytes(result.bytes) })
    await load(path.value)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('files.uploadFailed'))
  } finally {
    uploading.value = false
  }
}

async function download(entry: RemoteFileEntry): Promise<void> {
  if (entry.is_dir || downloadingPath.value !== null) return
  downloadingPath.value = entry.name
  actionError.value = null
  notice.value = null
  try {
    const { blob, filename } = await api.downloadServerFile(props.serverId, joinPath(path.value, entry.name))
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
    notice.value = t('files.downloadStarted', { name: filename })
  } catch (caught) {
    actionError.value = errorMessage(caught, t('files.downloadFailed'))
  } finally {
    downloadingPath.value = null
  }
}

function askDelete(entry: RemoteFileEntry): void {
  deleteTarget.value = entry
}

function closeDelete(): void {
  if (deleting.value) return
  deleteTarget.value = null
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  actionError.value = null
  try {
    await api.deleteServerFile(props.serverId, joinPath(path.value, target.name))
    deleteTarget.value = null
    notice.value = t('files.deleted', { name: target.name })
    await load(path.value)
  } catch (caught) {
    actionError.value = errorMessage(caught, t('files.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  if (props.allowed) void load('/')
})
</script>

<template>
  <div v-if="!allowed" class="ls-srv-files ls-srv-files--blocked">
    <p class="ls-notice__title">{{ t('servers.noTerminalPermission') }}</p>
    <p class="ls-notice__text">{{ t('terminal.disabledHint') }}</p>
  </div>

  <div v-else class="ls-srv-files">
    <div class="ls-srv-files__toolbar">
      <nav class="ls-srv-files__crumbs" :aria-label="t('files.path')">
        <template v-for="(crumb, index) in crumbs" :key="crumb.path">
          <span v-if="index > 0" class="ls-srv-files__crumb-sep" aria-hidden="true">/</span>
          <button
            class="ls-srv-files__crumb"
            type="button"
            :aria-current="crumb.path === path ? 'location' : undefined"
            :title="crumb.path"
            @click="load(crumb.path)"
          >
            {{ crumb.label }}
          </button>
        </template>
      </nav>
      <div class="ls-srv-files__actions">
        <input
          v-model="keyword"
          class="ls-input ls-srv-files__search"
          type="search"
          :placeholder="t('files.search')"
          :aria-label="t('files.search')"
        />
        <button class="ls-btn ls-btn--small" type="button" :disabled="loading" @click="load(path)">
          <AppIcon name="refresh" size="0.875rem" />
          {{ t('common.refresh') }}
        </button>
        <button
          id="server-files-upload"
          class="ls-btn ls-btn--small ls-btn--primary"
          type="button"
          :disabled="uploading"
          :aria-busy="uploading"
          @click="triggerUpload"
        >
          <span v-if="uploading" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="plus" size="0.875rem" />
          {{ uploading ? t('files.uploadingFile') : t('files.upload') }}
        </button>
        <input ref="fileInput" class="ls-srv-files__file" type="file" @change="onUpload" />
      </div>
    </div>

    <p v-if="actionError" class="ls-inline-error" role="alert">{{ actionError }}</p>
    <p v-if="notice" class="ls-ok-hint" role="status">{{ notice }}</p>

    <div v-if="loading" class="ls-srv-files__loading" aria-busy="true" :aria-label="t('common.loading')">
      <span v-for="index in 5" :key="index" class="ls-skeleton ls-srv-files__skeleton" />
    </div>

    <div v-else-if="error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('files.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button class="ls-notice__action" type="button" @click="load(path)">
        {{ t('common.retry') }}
      </button>
    </div>

    <template v-else>
      <div class="ls-srv-files__table">
        <div class="ls-srv-files__head" role="row">
          <button class="ls-srv-files__sort" type="button" :aria-sort="sortKey === 'name' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'" @click="sortBy('name')">
            {{ sortLabel('name') }}
          </button>
          <button class="ls-srv-files__sort" type="button" :aria-sort="sortKey === 'size' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'" @click="sortBy('size')">
            {{ sortLabel('size') }}
          </button>
          <button class="ls-srv-files__sort" type="button" :aria-sort="sortKey === 'mod_time' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'" @click="sortBy('mod_time')">
            {{ sortLabel('mod_time') }}
          </button>
          <button class="ls-srv-files__sort" type="button" :aria-sort="sortKey === 'mode' ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'" @click="sortBy('mode')">
            {{ sortLabel('mode') }}
          </button>
          <span class="ls-srv-files__head-actions">{{ t('files.actions') }}</span>
        </div>

        <p v-if="visibleEntries.length === 0" class="ls-srv-files__empty">
          {{ keyword.trim() === '' ? t('servers.filesEmpty') : t('files.searchEmpty') }}
        </p>

        <ul v-else class="ls-srv-files__list">
          <li v-if="path !== '/'" class="ls-srv-files__item ls-srv-files__item--parent">
            <button class="ls-srv-files__up" type="button" :title="t('files.up')" @click="load(parentPath)">
              <AppIcon name="chevron-right" size="0.875rem" class="ls-srv-files__up-icon" />
              ..
            </button>
          </li>
          <li v-for="entry in visibleEntries" :key="`${entry.name}-${entry.is_dir}`" class="ls-srv-files__item">
            <button
              class="ls-srv-files__entry"
              :class="{ 'ls-srv-files__entry--selected': selected?.name === entry.name }"
              type="button"
              :title="entry.name"
              @click="openEntry(entry)"
            >
              <AppIcon :name="entry.is_dir ? 'folder' : 'file-text'" size="0.875rem" />
              <span class="ls-srv-files__name">{{ entry.name }}</span>
            </button>
            <span class="ls-srv-files__size">{{ entry.is_dir ? '—' : formatBytes(entry.size) }}</span>
            <span class="ls-srv-files__modified">{{ entry.mod_time ? formatDateTime(entry.mod_time) : t('common.na') }}</span>
            <span class="ls-srv-files__mode">{{ entry.mode ?? t('common.na') }}</span>
            <span class="ls-srv-files__row-actions">
              <button
                v-if="!entry.is_dir"
                class="ls-icon-btn ls-srv-files__action"
                type="button"
                :title="t('files.download')"
                :aria-label="`${t('files.download')} ${entry.name}`"
                :disabled="downloadingPath === entry.name"
                @click.stop="download(entry)"
              >
                <AppIcon name="download" size="0.875rem" />
              </button>
              <button
                class="ls-icon-btn ls-srv-files__action ls-srv-files__action--danger"
                type="button"
                :title="t('common.delete')"
                :aria-label="`${t('common.delete')} ${entry.name}`"
                @click.stop="askDelete(entry)"
              >
                <AppIcon name="trash" size="0.875rem" />
              </button>
            </span>
          </li>
        </ul>
      </div>

      <dl v-if="selected" class="ls-srv-files__meta">
        <div class="ls-srv-files__meta-row">
          <dt>{{ t('files.name') }}</dt>
          <dd :title="selected.name">{{ selected.name }}</dd>
        </div>
        <div class="ls-srv-files__meta-row">
          <dt>{{ t('files.size') }}</dt>
          <dd>{{ selected.is_dir ? '—' : formatBytes(selected.size) }}</dd>
        </div>
        <div class="ls-srv-files__meta-row">
          <dt>{{ t('files.modified') }}</dt>
          <dd>{{ selected.mod_time ? formatDateTime(selected.mod_time) : t('common.na') }}</dd>
        </div>
        <div class="ls-srv-files__meta-row">
          <dt>{{ t('files.mode') }}</dt>
          <dd>{{ selected.mode ?? t('common.na') }}</dd>
        </div>
      </dl>
    </template>

    <AppModal
      v-if="deleteTarget"
      :title="t('files.deleteConfirmTitle', { name: deleteTarget.name })"
      :description="t('files.deleteConfirmText')"
      size="sm"
      tone="danger"
      @close="closeDelete"
    >
      <p v-if="actionError" class="ls-inline-error" role="alert">{{ actionError }}</p>
      <template #footer>
        <button class="ls-btn" type="button" :disabled="deleting" @click="closeDelete">{{ t('common.cancel') }}</button>
        <button class="ls-btn ls-btn--danger" type="button" :disabled="deleting" :aria-busy="deleting" @click="confirmDelete">
          {{ deleting ? t('common.deleting') : t('common.delete') }}
        </button>
      </template>
    </AppModal>
  </div>
</template>
