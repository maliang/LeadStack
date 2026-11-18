<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'

/**
 * 兜底路由（/:pathMatch(.*)*）。
 *
 * 之前兜底静默 redirect 到 /：地址写错时用户以为自己被「踢回首页」，
 * 也掩盖了菜单链接缺失的问题。这里显式给出 404 与两个出口。
 * 权限不足走的是 /forbidden（见路由守卫），不会落到这里。
 */
const route = useRoute()
</script>

<template>
  <section class="ls-notfound" aria-labelledby="ls-notfound-title">
    <p class="ls-notfound__code">404</p>
    <h1 id="ls-notfound-title" class="ls-notfound__title">页面不存在</h1>
    <p class="ls-notfound__text">
      没有找到 <code class="ls-notfound__path">{{ route.fullPath }}</code> 对应的页面，可能是链接过期或路径拼写有误。
    </p>
    <p class="ls-notfound__hint">如果是权限问题，页面会显示「无权限」而不是 404。</p>
    <div class="ls-notfound__actions">
      <RouterLink class="ls-btn ls-btn--primary" to="/">返回首页</RouterLink>
      <RouterLink class="ls-btn" to="/servers">去服务器列表</RouterLink>
    </div>
  </section>
</template>

<style scoped>
.ls-notfound {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--ls-space-3);
  max-width: 42rem;
  min-width: 0;
}

.ls-notfound__code {
  margin: 0;
  font:
    600 var(--ls-text-xs) / 1.4 var(--ls-font-sans);
  letter-spacing: 0.16em;
  color: var(--ls-text-subtle);
}

.ls-notfound__title {
  margin: 0;
  font:
    600 1.5rem / 1.3 var(--ls-font-sans);
  color: var(--ls-text);
  overflow-wrap: anywhere;
}

.ls-notfound__text,
.ls-notfound__hint {
  margin: 0;
  font:
    400 var(--ls-text-md) / 1.7 var(--ls-font-sans);
  color: var(--ls-text-muted);
  overflow-wrap: anywhere;
}

.ls-notfound__hint {
  color: var(--ls-text-subtle);
}

.ls-notfound__path {
  font:
    500 var(--ls-text-sm) / 1.5 var(--ls-font-sans);
  color: var(--ls-text);
  overflow-wrap: anywhere;
  word-break: break-word;
}

.ls-notfound__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--ls-space-2);
}

@media (max-width: 360px) {
  .ls-notfound__title {
    font-size: 1.25rem;
  }
}
</style>
