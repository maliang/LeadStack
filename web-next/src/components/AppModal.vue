<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useI18n } from '@/i18n'

/**
 * 通用模态：Teleport 到 body，Esc 关闭、焦点回归、Tab 焦点环、背景滚动锁。
 *
 * 页面用 v-if 控制挂载；打开期间组件常驻，因此关闭只 emit('close')，
 * 由 owner 决定何时卸载（避免 busy 时误关）。
 */
const props = withDefaults(
  defineProps<{
    title: string
    description?: string
    size?: 'sm' | 'md' | 'lg'
    /** danger 用红色标题，表达破坏性后果（删除 / 回滚 / 恢复） */
    tone?: 'default' | 'danger'
    /** 供端到端脚本定位的 data-testid */
    testId?: string
  }>(),
  { size: 'md', description: undefined, tone: 'default', testId: undefined },
)

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const panel = ref<HTMLElement | null>(null)

/**
 * 标题 / 描述的 id 用 Vue 的 useId()：它在实例间唯一。
 * 之前用 `<script setup>` 内的自增变量，每个实例都从 0 开始（永远得到 -1），
 * 两个模态同时存在时 aria-labelledby 会指向同一个 id。
 */
const uid = useId()
const titleId = `ls-modal-title-${uid}`
const descriptionId = `ls-modal-desc-${uid}`

let trigger: HTMLElement | null = null
let previousOverflow: string | null = null

function focusableElements(): HTMLElement[] {
  const root = panel.value
  if (!root) return []
  return Array.from(
    root.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) => element.getClientRects().length > 0)
}

function requestClose(): void {
  emit('close')
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    requestClose()
    return
  }
  if (event.key !== 'Tab') return
  const items = focusableElements()
  if (items.length === 0) {
    event.preventDefault()
    panel.value?.focus()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement
  if (!panel.value?.contains(active)) {
    event.preventDefault()
    first.focus()
    return
  }
  if (event.shiftKey && active === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  document.body.classList.add('ls-scroll-locked')
  document.addEventListener('keydown', onKeydown, true)
  void nextTick(() => {
    const target =
      panel.value?.querySelector<HTMLElement>('[data-autofocus]') ??
      focusableElements().find((element) => !element.classList.contains('ls-modal__close')) ??
      panel.value
    target?.focus()
  })
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  document.body.classList.remove('ls-scroll-locked')
  if (previousOverflow === null) document.body.style.removeProperty('overflow')
  else document.body.style.overflow = previousOverflow
  const previous = trigger
  trigger = null
  void nextTick(() => {
    if (previous && previous.isConnected) previous.focus()
  })
})
</script>

<template>
  <Teleport to="body">
    <div class="ls-modal" :data-testid="props.testId" @click.self="requestClose">
      <div
        ref="panel"
        class="ls-modal__panel"
        :class="[`ls-modal__panel--${props.size}`, { 'ls-modal__panel--danger': props.tone === 'danger' }]"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        :aria-describedby="props.description ? descriptionId : undefined"
        tabindex="-1"
      >
        <header class="ls-modal__head">
          <div class="ls-modal__heading">
            <h2 :id="titleId" class="ls-modal__title">{{ props.title }}</h2>
            <p v-if="props.description" :id="descriptionId" class="ls-modal__desc">
              {{ props.description }}
            </p>
          </div>
          <button
            class="ls-icon-btn ls-modal__close"
            type="button"
            :aria-label="t('common.close')"
            @click="requestClose"
          >
            <AppIcon name="close" size="1rem" />
          </button>
        </header>

        <div class="ls-modal__body">
          <slot />
        </div>

        <footer v-if="$slots.footer" class="ls-modal__foot">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>
