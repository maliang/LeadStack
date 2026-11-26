<script setup lang="ts">
import { computed } from 'vue'

/**
 * 系统图标：直接使用 BaoTa 的 svgtofont 系统字形（Ubuntu/Debian/CentOS/Fedora/Linux/Windows）。
 * macOS 没有对应字形，保留一个简洁的 Apple 轮廓；其它 Linux 发行版使用通用 Linux 字形。
 */
const props = withDefaults(
  defineProps<{
    osId: string
    size?: string
  }>(),
  { size: '1rem' },
)

const kind = computed(() => {
  const id = props.osId.trim().toLowerCase()
  if (id.includes('darwin') || id.includes('macos')) return 'macos'
  if (id.includes('windows')) return 'windows'
  if (id.includes('ubuntu')) return 'ubuntu'
  if (id.includes('debian') || id.includes('raspbian') || id.includes('mint')) return 'debian'
  if (id.includes('centos')) return 'centos'
  if (id.includes('fedora')) return 'fedora'
  return 'linux'
})
</script>

<template>
  <span
    class="ls-os-icon ls-os-glyph"
    :class="`ls-os-glyph--${kind}`"
    :style="{ fontSize: size }"
    aria-hidden="true"
  >
    <svg v-if="kind === 'macos'" viewBox="0 0 24 24" focusable="false">
      <path
        d="M16.8 13.2c0 2.1-1.6 3.9-3.6 3.9-1 0-1.8-.5-2.7-.5s-1.7.5-2.7.5c-1.9 0-3.5-1.8-3.5-3.9 0-2.7 2.2-4.8 4.7-4.8 1 0 1.9.5 2.5.5.6 0 1.7-.6 2.8-.5.9.1 1.8.6 2.4 1.4-1.9 1.2-1.7 3.3-.1 4.4-.2.5-.4 1-.7 1.4z M12.6 6.3c.5-.7.9-1.6.8-2.5-.8 0-1.7.5-2.2 1.2-.4.6-.8 1.5-.7 2.4.9.1 1.7-.4 2.1-1.1z"
        fill="currentColor"
      />
    </svg>
  </span>
</template>
