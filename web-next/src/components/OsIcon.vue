<script setup lang="ts">
import { computed } from 'vue'

/**
 * 发行版图标：按 os_id 选择图形，旧 agent 没上报 os_id 时由 serverOsId 兜底。
 * 图标只做视觉识别，旁边的系统名称已经提供语义，所以对读屏隐藏。
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
  if (id.includes('ubuntu') || id.includes('linuxmint')) return id.includes('mint') ? 'linuxmint' : 'ubuntu'
  if (id.includes('debian')) return 'debian'
  if (id.includes('raspbian')) return 'raspbian'
  if (id.includes('centos')) return 'centos'
  if (id.includes('rocky')) return 'rocky'
  if (id.includes('alma')) return 'almalinux'
  if (id.includes('fedora')) return 'fedora'
  if (id.includes('rhel') || id.includes('redhat') || id.includes('red-hat')) return 'rhel'
  if (id.includes('opensuse') || id.includes('suse') || id.includes('sles')) return 'opensuse'
  if (id.includes('alpine')) return 'alpine'
  if (id.includes('arch')) return 'arch'
  if (id.includes('manjaro')) return 'manjaro'
  if (id.includes('gentoo')) return 'gentoo'
  if (id.includes('nixos')) return 'nixos'
  if (id.includes('kali')) return 'kali'
  if (id.includes('darwin') || id.includes('macos')) return 'macos'
  if (id.includes('windows')) return 'windows'
  return 'linux'
})
</script>

<template>
  <svg
    class="ls-os-icon"
    viewBox="0 0 24 24"
    :style="{ width: size, height: size }"
    aria-hidden="true"
    focusable="false"
  >
    <g v-if="kind === 'ubuntu'">
      <circle cx="12" cy="12" r="10" fill="#E95420" />
      <circle cx="12" cy="12" r="5.2" fill="none" stroke="#fff" stroke-width="1.6" />
      <circle cx="12" cy="5.2" r="1.55" fill="#fff" />
      <circle cx="6.1" cy="15.4" r="1.55" fill="#fff" />
      <circle cx="17.9" cy="15.4" r="1.55" fill="#fff" />
    </g>
    <g v-else-if="kind === 'debian'">
      <circle cx="12" cy="12" r="10" fill="#D70A53" />
      <path d="M12 4.5c4.2 0 7.5 3.2 7.5 7.2 0 3.2-2.2 5.8-5.3 6.5-2.7.6-5.1-.5-6.4-2.7" fill="none" stroke="#fff" stroke-width="1.5" stroke-linecap="round" />
      <path d="M7.2 15.5c-1.7-2.2-1.3-5.5.9-7.3 1.6-1.3 3.8-1.6 5.7-.7" fill="none" stroke="#fff" stroke-width="1.5" stroke-linecap="round" />
      <circle cx="12" cy="12" r="1.35" fill="#fff" />
    </g>
    <g v-else-if="kind === 'centos'">
      <circle cx="12" cy="12" r="10" fill="#932279" />
      <path d="M6.6 8.2h7.2l-2 3.1H6.6z" fill="#9EE64B" />
      <path d="M17.4 15.8H10.2l2-3.1h5.2z" fill="#EF8E3B" />
      <path d="M8.2 10.2l2.2-3.4v3.4z" fill="#fff" />
      <path d="M15.8 13.8l-2.2 3.4v-3.4z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'rhel'">
      <circle cx="12" cy="12" r="10" fill="#EE0000" />
      <path d="M7.5 9.2c2.2-2.3 6.4-2.5 8.5-.4l1.3 1.3-2.2 1.2-1.2-1.1c-1.3-1.2-3.6-1-4.8.2z" fill="#fff" />
      <path d="M6.4 10.6l5.7 5.8c1.3 1.3 3.5 1.3 4.8 0l1.7-1.8-1.3-1.3-1.7 1.7c-.6.6-1.6.6-2.2 0l-5.7-5.7z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'fedora'">
      <circle cx="12" cy="12" r="10" fill="#294172" />
      <path d="M7 12c0-2.2 1.8-4 4-4 1.3 0 2.4.6 3.2 1.6.4-.3.9-.5 1.5-.5 1.5 0 2.7 1.2 2.7 2.7 0 1.4-1 2.5-2.4 2.7-.5 1.6-2 2.8-3.8 2.8-2.2 0-4-1.8-4-4 0-.5.1-.9.3-1.3z" fill="none" stroke="#fff" stroke-width="1.5" />
      <circle cx="14.1" cy="12" r="1.2" fill="#fff" />
    </g>
    <g v-else-if="kind === 'rocky'">
      <circle cx="12" cy="12" r="10" fill="#10B981" />
      <path d="M4.2 16.8L9.6 8l2.6 4.1L14.8 8l5 8.8H4.2z" fill="#fff" />
      <path d="M9.6 16.8l2.4-3.8 2 3.8z" fill="#0F766E" />
    </g>
    <g v-else-if="kind === 'almalinux'">
      <circle cx="12" cy="12" r="10" fill="#0F4266" />
      <path d="M6.2 17.8L12 5.4l5.8 12.4h-3.3L12 12.5l-2.5 5.3z" fill="#fff" />
      <path d="M9.5 17.8L12 12.5l1.1 2.3-1.5 3z" fill="#9EE64B" />
    </g>
    <g v-else-if="kind === 'alpine'">
      <circle cx="12" cy="12" r="10" fill="#0D597F" />
      <path d="M4 17.5L8.5 9l3.2 5.4L14 11l6 6.5H4z" fill="#fff" />
      <path d="M8.5 9l1.9 3.2-1.9 2.2-1.9-2.2z" fill="#9BD7F5" />
    </g>
    <g v-else-if="kind === 'opensuse'">
      <circle cx="12" cy="12" r="10" fill="#73BA25" />
      <path d="M7 14.2c0-3.1 2.4-5.6 5.5-5.6 2.7 0 4.9 1.9 5.4 4.4.7.2 1.2.8 1.2 1.5 0 .9-.8 1.6-1.7 1.6H9.5c-2.1 0-3.8-1-3.8-2.5 0-.8.5-1.5 1.3-1.9z" fill="#fff" />
      <circle cx="17.5" cy="14.5" r="1" fill="#73BA25" />
    </g>
    <g v-else-if="kind === 'arch'">
      <circle cx="12" cy="12" r="10" fill="#1793D1" />
      <path d="M12 4.5l6.5 14.8h-3.3L12 11.2l-3.2 8.1H5.5z" fill="#fff" />
      <path d="M8.6 19.3l2.1-4.2 1.3 2.2-1 2z" fill="#0B5E88" />
    </g>
    <g v-else-if="kind === 'manjaro'">
      <circle cx="12" cy="12" r="10" fill="#35BF5C" />
      <path d="M5.5 18L12 5l6.5 13h-3.1L12 11.4 8.6 18z" fill="#fff" />
      <path d="M9.7 18l2.3-4.5 2.3 4.5z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'gentoo'">
      <circle cx="12" cy="12" r="10" fill="#54487A" />
      <path d="M14.8 5.8c-2.6-.7-5.4.8-6.3 3.4-.7 2 .1 4.1 1.8 5.2-.5.6-1 1.4-.9 2.3.1 1.7 1.6 2.9 3.3 2.7 1.9-.2 3.2-1.8 3-3.7-.1-1.4-1-2.6-2.3-3.1.2-1.2-.2-2.4-1.1-3.2 1.1.2 2.3 0 3.3-.6z" fill="#fff" />
      <circle cx="12.5" cy="15.5" r="1.1" fill="#54487A" />
    </g>
    <g v-else-if="kind === 'nixos'">
      <circle cx="12" cy="12" r="10" fill="#5277C3" />
      <path d="M7.2 16.8L12 8l4.8 8.8h-2.6L12 12.5l-2.2 4.3z" fill="#fff" />
      <path d="M9 7.2h6l-1.5 2.7-1.5-2.7z" fill="#9BD7F5" />
    </g>
    <g v-else-if="kind === 'kali'">
      <circle cx="12" cy="12" r="10" fill="#268BEE" />
      <path d="M7 6.8h3.2v4.8l3.8-4.8h3.6l-4.6 5.5 4.8 5.8h-3.7l-3.9-4.9v4.9H7z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'linuxmint'">
      <circle cx="12" cy="12" r="10" fill="#86BE43" />
      <path d="M7 7.5h3v6.8h4.8v3H7z" fill="#fff" />
      <path d="M14.5 7.5H17v6.8h-2.5z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'raspbian'">
      <circle cx="12" cy="12" r="10" fill="#C51A4A" />
      <path d="M8 9.5c0-1.4 1.1-2.5 2.5-2.5S13 8.1 13 9.5c0 .6-.2 1.1-.6 1.5.9.5 1.6 1.5 1.6 2.7 0 1.8-1.4 3.2-3.2 3.2-1.2 0-2.2-.7-2.8-1.7-.4.2-.8.3-1.3.3-1.4 0-2.5-1.1-2.5-2.5S5.3 9.5 6.7 9.5c.5 0 .9.1 1.3.4z" fill="#fff" />
      <circle cx="15.8" cy="13.7" r="1.4" fill="#fff" />
    </g>
    <g v-else-if="kind === 'macos'">
      <circle cx="12" cy="12" r="10" fill="#3B3B3B" />
      <path d="M16.8 13.2c0 2.1-1.6 3.9-3.6 3.9-1 0-1.8-.5-2.7-.5s-1.7.5-2.7.5c-1.9 0-3.5-1.8-3.5-3.9 0-2.7 2.2-4.8 4.7-4.8 1 0 1.9.5 2.5.5.6 0 1.7-.6 2.8-.5.9.1 1.8.6 2.4 1.4-1.9 1.2-1.7 3.3-.1 4.4-.2.5-.4 1-.7 1.4z" fill="#fff" />
      <path d="M12.6 6.3c.5-.7.9-1.6.8-2.5-.8 0-1.7.5-2.2 1.2-.4.6-.8 1.5-.7 2.4.9.1 1.7-.4 2.1-1.1z" fill="#fff" />
    </g>
    <g v-else-if="kind === 'windows'">
      <rect x="3.5" y="3.5" width="8" height="8" rx="1.2" fill="#00A4EF" />
      <rect x="12.5" y="3.5" width="8" height="8" rx="1.2" fill="#00A4EF" />
      <rect x="3.5" y="12.5" width="8" height="8" rx="1.2" fill="#00A4EF" />
      <rect x="12.5" y="12.5" width="8" height="8" rx="1.2" fill="#00A4EF" />
    </g>
    <g v-else>
      <circle cx="12" cy="12" r="10" fill="#374151" />
      <path d="M12 4.6c-2.7 0-4.9 2.2-4.9 4.9 0 .9.2 1.7.7 2.4-.7.5-1.2 1.4-1.2 2.4 0 1.7 1.3 3.1 3 3.1.6 0 1.2-.2 1.7-.5.4.2.9.3 1.4.3 1.9 0 3.5-1.6 3.5-3.5 0-.9-.3-1.7-.9-2.3.2-.5.3-1.1.3-1.7 0-2.8-1.9-5.1-3.6-5.1z" fill="#fff" />
      <circle cx="10.1" cy="9.2" r="0.9" fill="#374151" />
      <circle cx="13.9" cy="9.2" r="0.9" fill="#374151" />
      <path d="M10.2 11.2c.5.7 1.1 1 1.8 1s1.3-.3 1.8-1" fill="none" stroke="#374151" stroke-width="1" stroke-linecap="round" />
    </g>
  </svg>
</template>
