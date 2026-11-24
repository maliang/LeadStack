<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useAttrs, watch } from 'vue'
import { SSelect } from '@vean/ui'

/**
 * 统一下拉组件：视觉用 Vean SSelect，底层保留一个视觉隐藏的原生 <select>。
 *
 * 为什么不直接写原生 select：全站筛选/表单需要统一的高度、圆角、焦点态与弹层，
 * 原生下拉在 macOS/Windows 上各长一套，且无法与设计 token 对齐。
 *
 * 保留原生 select 的原因：
 * - 表单语义（name/required/label for）与浏览器自动填充仍走原生控件；
 * - 自动化测试与老代码用 selectOption / change 事件操作时行为不变；
 * - 选项仍由调用方的 <option> 子节点声明，迁移成本低（只换标签名）。
 */
defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    modelValue?: string | number | null
    disabled?: boolean
    id?: string
    ariaLabel?: string
  }>(),
  { modelValue: '', disabled: false, id: undefined, ariaLabel: undefined },
)

const emit = defineEmits<{
  (event: 'update:modelValue', value: string): void
  (event: 'change', payload: Event): void
}>()

const attrs = useAttrs()
const nativeEl = ref<HTMLSelectElement | null>(null)
const items = ref<{ value: string; label: string; disabled?: boolean }[]>([])

let observer: MutationObserver | null = null

function readOptions(): void {
  const el = nativeEl.value
  if (!el) return
  const next = Array.from(el.options).map((option) => ({
    value: option.value,
    label: (option.textContent ?? '').trim(),
    disabled: option.disabled,
  }))
  const serialized = JSON.stringify(next)
  if (serialized !== JSON.stringify(items.value)) items.value = next
  // 空值且没有「全部/请选择」空选项时，默认落到第一项，避免下拉显示空白。
  // 筛选类下拉的第一项通常是「全部」，表单类下拉的第一项是合理默认值。
  if (
    model.value === '' &&
    next.length > 0 &&
    !next.some((item) => item.value === '') &&
    !props.disabled
  ) {
    void nextTick(() => emit('update:modelValue', next[0].value))
  }
}

function onNativeChange(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  emit('update:modelValue', value)
  emit('change', event)
  // 受控语义：异步更新失败/回滚时，把原生 select 的显示值拉回模型。
  void nextTick(() => {
    const el = nativeEl.value
    if (el && el.value !== model.value) el.value = model.value
  })
}

function onPick(value: string | number): void {
  const el = nativeEl.value
  const text = String(value)
  if (!el) {
    emit('update:modelValue', text)
    return
  }
  el.value = text
  // 让 @change="handler($event)" 的老写法继续拿到真实 Event / target.value。
  el.dispatchEvent(new Event('change'))
  void nextTick(() => {
    readOptions()
    if (nativeEl.value && nativeEl.value.value !== model.value) nativeEl.value.value = model.value
  })
}

const model = computed(() => (props.modelValue === null ? '' : String(props.modelValue)))
const itemsSignature = computed(() => items.value.map((item) => `${item.value}:${item.label}`).join('\u0001'))

// 受控组件：外部模型没跟上（例如异步切换失败回滚）时，把原生 select 的值同步回去。
watch(model, (value) => {
  const el = nativeEl.value
  if (el && el.value !== value) el.value = value
})
const triggerClass = computed(() => (attrs.class as string | undefined) ?? undefined)
const triggerAttrs = computed(() => {
  const { class: _class, id: _id, ...rest } = attrs
  return rest
})

onMounted(() => {
  readOptions()
  if (nativeEl.value && typeof MutationObserver !== 'undefined') {
    observer = new MutationObserver(readOptions)
    observer.observe(nativeEl.value, { childList: true, subtree: true, attributes: true, characterData: true })
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})
</script>

<template>
  <div class="ls-select">
    <SSelect
      :key="itemsSignature"
      v-bind="triggerAttrs"
      :model-value="model"
      :items="items"
      :disabled="disabled"
      :show-arrow="false"
      :aria-label="ariaLabel"
      :class="triggerClass"
      @update:model-value="onPick"
    />
    <select
      ref="nativeEl"
      :id="props.id"
      class="ls-select__native"
      :value="model"
      :disabled="disabled"
      tabindex="-1"
      aria-hidden="true"
      @change="onNativeChange"
    >
      <slot />
    </select>
  </div>
</template>
