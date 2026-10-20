import type { TemplateKind } from '../lib/api'
import { t } from '../i18n'

/** 三种类型的示例模板（契约第 26 节占位符），供模板编辑器一键填充。 */
export const SAMPLE_TEMPLATES: Record<TemplateKind, { name: string; content: string }> = {
  binary: {
    name: t('s.0651'),
    content: t('s.0652'),
  },
  systemd: {
    name: t('s.0653'),
    content: t('s.0654'),
  },
  compose: {
    name: t('s.0655'),
    content: t('s.0656'),
  },
}

export const TEMPLATE_KIND_LABELS: Record<TemplateKind, string> = {
  compose: t('s.0657'),
  systemd: t('s.0658'),
  binary: t('s.0659'),
}
