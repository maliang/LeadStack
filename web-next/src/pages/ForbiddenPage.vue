<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { useLoginCopy } from '@/composables/useLoginCopy'

/**
 * 路由级权限门（对应 web/src/components/PermissionGate.tsx）：
 * 有路由但缺权限点时渲染这里，而不是白屏或静默跳首页。
 */
const route = useRoute()
const copy = useLoginCopy()

const from = typeof route.query.from === 'string' ? route.query.from : ''
</script>

<template>
  <section class="ls-forbidden">
    <span class="ls-forbidden__icon" aria-hidden="true">
      <AppIcon name="shield-check" size="1.5rem" />
    </span>
    <h1 class="ls-forbidden__title">{{ copy.forbiddenTitle }}</h1>
    <p class="ls-forbidden__text">{{ copy.forbiddenHint }}</p>
    <p v-if="from" class="ls-forbidden__path">{{ from }}</p>
    <div class="ls-forbidden__actions">
      <RouterLink class="ls-btn ls-btn--primary" to="/">{{ copy.backHome }}</RouterLink>
      <RouterLink class="ls-btn" to="/login">{{ copy.backToLogin }}</RouterLink>
    </div>
  </section>
</template>
