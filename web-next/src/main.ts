import { createApp } from 'vue'
import '@vean/ui/styles.css'
import './styles/main.css'
import App from './App.vue'
import router from './router'
import { ensureLanguage, initI18n, readStoredLanguage } from './i18n'

/**
 * 发版后旧 index.html 可能还会请求已被替换的懒加载 chunk，浏览器会报
 * "Failed to fetch dynamically imported module"。这里只自动刷新一次：
 * 刷新后拿到新 index.html 与新 chunk，用户不会停在坏页面上。
 */
const CHUNK_RELOAD_KEY = 'leadstack.chunk-reload-at'
window.addEventListener('vite:preloadError', () => {
  // 不能 preventDefault：Vite 的 __vitePreload 在 preventDefault 后会把失败的
  // import 当成 undefined 成功返回，i18n 等调用方会误以为模块已加载。
  // 词典加载失败要留给设置页展示可读提示，不能直接刷新把提示冲掉。
  if ((window as Window & { __leadstackI18nImporting?: boolean }).__leadstackI18nImporting) return
  const last = Number(window.sessionStorage.getItem(CHUNK_RELOAD_KEY) ?? '0')
  if (Date.now() - last < 10_000) return
  window.sessionStorage.setItem(CHUNK_RELOAD_KEY, String(Date.now()))
  window.location.reload()
})

/**
 * 启动顺序：先补齐当前语言的词典再挂载。
 * zh-CN 是静态打包的默认语言，这里立即 resolve；
 * 存储语言为 en-US 时才会去拉独立的 en chunk，避免首帧闪回中文。
 * 词典加载失败不阻塞启动：ensureLanguage 内部吞掉异常，界面回退 zh-CN。
 */
async function bootstrap(): Promise<void> {
  await ensureLanguage(readStoredLanguage())
  initI18n()
  createApp(App).use(router).mount('#app')
}

void bootstrap()
