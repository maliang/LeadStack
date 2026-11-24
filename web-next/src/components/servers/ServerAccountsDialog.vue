<script setup lang="ts">
import AppModal from '@/components/AppModal.vue'
import ServerAccountsTable from '@/components/server-detail/ServerAccountsTable.vue'
import { useI18n } from '@/i18n'
import type { Server } from '@/lib/api'

/** 服务器列表的「访问账号」弹窗：复用详情页同一份账号列表与登录脚本能力。 */
defineProps<{
  server: Server
  canWrite: boolean
}>()

const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
</script>

<template>
  <AppModal
    :title="t('servers.accountsTitle', { name: server.name })"
    :description="t('servers.accountsHint')"
    size="lg"
    @close="emit('close')"
  >
    <ServerAccountsTable :server="server" :can-write="canWrite" />
  </AppModal>
</template>
