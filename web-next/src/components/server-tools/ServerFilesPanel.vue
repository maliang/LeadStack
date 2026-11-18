<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { api, type RemoteFileEntry } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatDateTime } from '@/lib/format'
import { useI18n } from '@/i18n'

/**
 * 可复用文件面板：api.listFiles(serverId, path) + 面包屑逐级进入。
 *
 * 两处使用：服务器列表行内展开（ServerRow）与详情页「文件」tab；组件只认
 * serverId / allowed，不依赖列表上下文。
 *
 * 只读：目录点击进入，文件点击只展开元信息，不触发下载。旧界面 FilesTab 的
 * 上传/下载/删除不在本次复用范围内（与现有列表面板保持一致），需要时按
 * api.uploadServerFile / downloadServerFile / deleteServerFile 单独补入口。
 * 加载 / 失败（可重试）/ 空目录三态都有可见文案；无 terminal.use 时显示统一占位。
 */
const props = defineProps<{
  serverId: string
  allowed: boolean
}>()

interface Crumb {
  label: string
  path: string
}

const { t } = useI18n()

const path = ref('/')
const entries = ref<RemoteFileEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const selected = ref<RemoteFileEntry | null>(null)

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

function sortEntries(list: RemoteFileEntry[]): RemoteFileEntry[] {
  return [...list].sort((left, right) => {
    if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1
    return left.name.localeCompare(right.name)
  })
}

function joinPath(base: string, name: string): string {
  return base === '/' ? `/${name}` : `${base}/${name}`
}

let requests = 0

async function load(target: string = path.value): Promise<void> {
  const current = ++requests
  loading.value = true
  error.value = null
  try {
    const list = await api.listFiles(props.serverId, target)
    if (current !== requests) return
    path.value = target
    entries.value = sortEntries(list)
    selected.value = null
  } catch (caught) {
    if (current !== requests) return
    error.value = errorMessage(caught, t('files.loadFailed'))
  } finally {
    if (current === requests) loading.value = false
  }
}

function openEntry(entry: RemoteFileEntry): void {
  if (entry.is_dir) {
    void load(joinPath(path.value, entry.name))
    return
  }
  selected.value = entry
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

    <div v-if="loading" class="ls-srv-files__loading" aria-busy="true" :aria-label="t('common.loading')">
      <span v-for="index in 4" :key="index" class="ls-skeleton ls-srv-files__skeleton" />
    </div>

    <div v-else-if="error" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('files.loadFailed') }}</p>
      <p class="ls-notice__text">{{ error }}</p>
      <button class="ls-notice__action" type="button" @click="load(path)">
        {{ t('common.retry') }}
      </button>
    </div>

    <p v-else-if="entries.length === 0" class="ls-srv-files__empty">{{ t('servers.filesEmpty') }}</p>

    <ul v-else class="ls-srv-files__list">
      <li v-for="entry in entries" :key="`${entry.name}-${entry.is_dir}`" class="ls-srv-files__item">
        <button
          class="ls-srv-files__entry"
          :class="{ 'ls-srv-files__entry--selected': selected?.name === entry.name && !entry.is_dir }"
          type="button"
          :title="entry.name"
          @click="openEntry(entry)"
        >
          <AppIcon :name="entry.is_dir ? 'folder' : 'file-text'" size="0.875rem" />
          <span class="ls-srv-files__name">{{ entry.name }}</span>
          <span v-if="!entry.is_dir" class="ls-srv-files__size">{{ formatBytes(entry.size) }}</span>
        </button>
      </li>
    </ul>

    <dl v-if="selected" class="ls-srv-files__meta">
      <div class="ls-srv-files__meta-row">
        <dt>{{ t('files.name') }}</dt>
        <dd :title="selected.name">{{ selected.name }}</dd>
      </div>
      <div class="ls-srv-files__meta-row">
        <dt>{{ t('files.size') }}</dt>
        <dd>{{ formatBytes(selected.size) }}</dd>
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
  </div>
</template>
