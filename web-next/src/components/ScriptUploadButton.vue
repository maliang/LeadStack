<script setup lang="ts">
import { ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'

/**
 * 脚本文件读取按钮：把本地文本文件内容交给父组件的 v-model / ref。
 * 只支持文本脚本/配置；二进制文件读取后如果包含 NUL，直接拒绝。
 */
const props = withDefaults(
  defineProps<{
    accept?: string
    maxBytes?: number
    disabled?: boolean
  }>(),
  {
    accept: '.sh,.bash,.zsh,.txt,.service,.yaml,.yml,.conf,.env',
    maxBytes: 256 * 1024,
    disabled: false,
  },
)

const emit = defineEmits<{
  (event: 'load', content: string): void
}>()

const { t } = useI18n()
const input = ref<HTMLInputElement | null>(null)
const error = ref('')
const loadedName = ref('')

function pick(): void {
  error.value = ''
  input.value?.click()
}

async function onChange(event: Event): Promise<void> {
  const target = event.target as HTMLInputElement
  const file = target.files?.[0]
  target.value = ''
  if (!file) return
  if (file.size > props.maxBytes) {
    error.value = t('common.scriptFileTooLarge', { size: Math.round(props.maxBytes / 1024) })
    return
  }
  try {
    const content = await file.text()
    if (content.includes('\u0000')) {
      error.value = t('common.scriptFileBinary')
      return
    }
    loadedName.value = file.name
    emit('load', content)
  } catch {
    error.value = t('common.scriptFileReadFailed')
  }
}
</script>

<template>
  <div class="ls-script-upload">
    <input ref="input" class="ls-script-upload__input" type="file" :accept="accept" :disabled="disabled" @change="onChange" />
    <button class="ls-btn ls-btn--small" type="button" :disabled="disabled" @click="pick">
      <AppIcon name="file-text" size="0.875rem" />
      {{ t('common.uploadScript') }}
    </button>
    <span v-if="loadedName" class="ls-field__hint">{{ loadedName }}</span>
    <span v-if="error" class="ls-field__error" role="alert">{{ error }}</span>
  </div>
</template>
