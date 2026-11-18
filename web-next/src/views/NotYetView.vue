<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

/**
 * 尚未迁移页面的统一占位（web-next 迁移期）。
 *
 * 为什么不做静默 redirect：用户点菜单落回首页会以为「点了没反应」，
 * 这里明确告知页面还没迁完，并把路由名/路径摊出来便于对照旧版 React 界面。
 * 路由名可能是 symbol，统一转成可读字符串。
 */
const route = useRoute()

const routeName = computed(() => {
  const name = route.name
  if (typeof name === 'string') return name
  return name ? String(name) : '（未命名路由）'
})

const pageTitle = computed(() => {
  const title = route.meta.title
  return typeof title === 'string' && title !== '' ? title : routeName.value
})
</script>

<template>
  <section class="ls-notyet" aria-labelledby="ls-notyet-title">
    <p class="ls-notyet__eyebrow">web-next · 迁移中</p>
    <h1 id="ls-notyet-title" class="ls-notyet__title">该页面正在迁移到新版界面</h1>
    <p class="ls-notyet__text">
      「{{ pageTitle }}」还没有在新版界面实现，旧的 React 版界面仍可正常使用。
    </p>
    <dl class="ls-notyet__meta">
      <div class="ls-notyet__row">
        <dt class="ls-notyet__label">路由名</dt>
        <dd class="ls-notyet__value"><code>{{ routeName }}</code></dd>
      </div>
      <div class="ls-notyet__row">
        <dt class="ls-notyet__label">路径</dt>
        <dd class="ls-notyet__value"><code>{{ route.path }}</code></dd>
      </div>
    </dl>
    <RouterLink class="ls-btn ls-btn--primary" to="/">返回首页</RouterLink>
  </section>
</template>

<style scoped>
.ls-notyet {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--ls-space-3);
  max-width: 42rem;
  min-width: 0;
}

.ls-notyet__eyebrow {
  margin: 0;
  font:
    500 var(--ls-text-xs) / 1.4 var(--ls-font-sans);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--ls-text-subtle);
}

.ls-notyet__title {
  margin: 0;
  font:
    600 1.5rem / 1.3 var(--ls-font-sans);
  color: var(--ls-text);
  overflow-wrap: anywhere;
}

.ls-notyet__text {
  margin: 0;
  font:
    400 var(--ls-text-md) / 1.7 var(--ls-font-sans);
  color: var(--ls-text-muted);
  overflow-wrap: anywhere;
}

.ls-notyet__meta {
  display: flex;
  flex-direction: column;
  gap: var(--ls-space-2);
  margin: 0;
  padding: var(--ls-space-3) var(--ls-space-4);
  border: 1px solid var(--ls-border);
  border-radius: var(--ls-radius-md);
  background: var(--ls-surface-muted);
  max-width: 100%;
  min-width: 0;
}

.ls-notyet__row {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--ls-space-2);
  min-width: 0;
}

.ls-notyet__label {
  margin: 0;
  font:
    500 var(--ls-text-xs) / 1.5 var(--ls-font-sans);
  color: var(--ls-text-subtle);
}

.ls-notyet__value {
  margin: 0;
  min-width: 0;
}

.ls-notyet__value code {
  font:
    500 var(--ls-text-sm) / 1.5 var(--ls-font-sans);
  color: var(--ls-text);
  overflow-wrap: anywhere;
  word-break: break-word;
}

@media (max-width: 360px) {
  .ls-notyet__title {
    font-size: 1.25rem;
  }
}
</style>
