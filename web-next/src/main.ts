import { createApp } from 'vue'
import '@vean/ui/styles.css'
import './styles/main.css'
import App from './App.vue'
import router from './router'

createApp(App).use(router).mount('#app')
