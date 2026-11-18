<template>
  <section class="about">
    <h1>关于 web-next</h1>
    <ul>
      <li>Vue 3 + TypeScript，组件的全局配置由 Vean UI 的 <code>SConfigProvider</code> 提供。</li>
      <li>路由使用 <code>createWebHashHistory</code>，构建时 <code>base: './'</code>，便于嵌入 Go 二进制。</li>
      <li>类型检查与构建统一走 <code>vue-tsc -b</code>，产物落在 <code>web-next/dist</code>。</li>
    </ul>
    <RouterLink to="/">返回概览</RouterLink>
  </section>
</template>

<style scoped>
.about {
  max-width: 42rem;
}

h1 {
  margin: 0 0 1rem;
  font-size: 1.75rem;
}

ul {
  margin: 0 0 1.5rem;
  padding-left: 1.25rem;
  line-height: 1.9;
  color: #475569;
}

code {
  padding: 0.1rem 0.35rem;
  border-radius: 0.25rem;
  background: #f1f5f9;
  font-size: 0.875em;
}

a {
  color: #059669;
}
</style>
