<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from '@/i18n'

/**
 * 兜底路由（/:pathMatch(.*)*）。
 *
 * 之前兜底静默 redirect 到 /：地址写错时用户以为自己被「踢回首页」，
 * 也掩盖了菜单链接缺失的问题。这里显式给出 404 与两个出口。
 * 权限不足走的是 /forbidden（见路由守卫），不会落到这里。
 */
const route = useRoute()
const { t } = useI18n()
</script>

<template>
  <section class="ls-notfound" aria-labelledby="ls-notfound-title">
    <p class="ls-notfound__code">404</p>
    <h1 id="ls-notfound-title" class="ls-notfound__title">{{ t('notFound.title') }}</h1>
    <p class="ls-notfound__text">
      {{ t('notFound.text', { path: route.fullPath }) }}
    </p>
    <p class="ls-notfound__hint">{{ t('notFound.hint') }}</p>
    <div class="ls-notfound__actions">
      <RouterLink class="ls-btn ls-btn--primary" to="/">{{ t('notFound.home') }}</RouterLink>
      <RouterLink class="ls-btn" to="/servers">{{ t('notFound.servers') }}</RouterLink>
    </div>
  </section>
</template>
