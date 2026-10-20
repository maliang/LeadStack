import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import App from './App'
import { initI18n } from './i18n'
import './index.css'
import { t } from './i18n'

initI18n()

const container = document.getElementById('root')
if (!container) {
  throw new Error(t('s.0660'))
}

createRoot(container).render(
  <StrictMode>
    <HashRouter>
      <App />
    </HashRouter>
  </StrictMode>,
)
