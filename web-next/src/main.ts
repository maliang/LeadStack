import { createApp } from 'vue'
import '@vean/ui/styles.css'
import './styles/main.css'
import App from './App.vue'
import router from './router'
import { ensureLanguage, initI18n, readStoredLanguage } from './i18n'

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
