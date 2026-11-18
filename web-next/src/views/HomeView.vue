<script setup lang="ts">
import { SButton } from '@vean/ui'
</script>

<template>
  <section class="home">
    <p class="eyebrow">Vue 3 · Vite · Vean UI</p>
    <h1>LeadStack 领栈新版控制台</h1>
    <p class="lede">
      这是 web-next 脚手架的首屏。路由使用 hash 模式，构建产物可由 Go 二进制在子路径下直接托管。
    </p>
    <div class="actions">
      <SButton color="primary" variant="solid">开始使用</SButton>
      <RouterLink class="about-link" to="/about">了解构建约定</RouterLink>
    </div>
  </section>
</template>

<style scoped>
.home {
  max-width: 42rem;
}

.eyebrow {
  margin: 0 0 0.5rem;
  font-size: 0.8125rem;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: #64748b;
}

h1 {
  margin: 0 0 0.75rem;
  font-size: 2rem;
  line-height: 1.2;
}

.lede {
  margin: 0 0 1.5rem;
  line-height: 1.7;
  color: #475569;
}

.actions {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.about-link {
  color: #059669;
}
</style>
