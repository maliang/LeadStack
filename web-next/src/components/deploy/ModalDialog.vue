<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'

/**
 * 模态基座：挂到 document.body 上（Teleport），Esc 关闭、Tab 焦点在面板内循环，
 * 打开时焦点进入面板、关闭后还给触发元素。
 *
 * 为什么不用 inline 面板：发布/删除/回滚都是「先看清对象再决定」的确认场景，
 * 盖在页面上的模态才能保证按钮背后的列表不会被误点。
 */
const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    /** danger 用红色标题条，表达破坏性后果 */
    tone?: 'default' | 'danger'
    testId?: string
  }>(),
  { description: '', tone: 'default', testId: 'dialog' },
)

const emit = defineEmits<{
  (event: 'close'): void
}>()

const { t } = useI18n()
const panel = ref<HTMLElement | null>(null)
const titleId = computed(() => `${props.testId}-title`)

let trigger: HTMLElement | null = null
let previousOverflow = ''

function focusables(): HTMLElement[] {
  const root = panel.value
  if (!root) return []
  return Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  )
}

async function enter(): Promise<void> {
  trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  await nextTick()
  const first = focusables()[0]
  first?.focus()
}

function leave(): void {
  document.body.style.overflow = previousOverflow
  const target = trigger
  trigger = null
  if (target && document.contains(target)) target.focus()
}

watch(
  () => props.open,
  (open) => {
    if (open) void enter()
    else leave()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  document.body.style.overflow = previousOverflow
})

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    emit('close')
    return
  }
  if (event.key !== 'Tab') return
  const items = focusables()
  if (items.length === 0) return
  const first = items[0]
  const last = items[items.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="ls-dialog" :data-testid="testId" @keydown="onKeydown">
      <div class="ls-dialog__backdrop" aria-hidden="true" @click="emit('close')" />
      <div
        ref="panel"
        class="ls-dialog__panel"
        :class="{ 'ls-dialog__panel--danger': tone === 'danger' }"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
      >
        <header class="ls-dialog__head">
          <h2 :id="titleId" class="ls-dialog__title">{{ title }}</h2>
          <button type="button" class="ls-icon-btn" :aria-label="t('common.close')" @click="emit('close')">
            <AppIcon name="close" />
          </button>
        </header>
        <p v-if="description" class="ls-dialog__text">{{ description }}</p>
        <div class="ls-dialog__body">
          <slot />
        </div>
        <footer class="ls-dialog__foot">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>
