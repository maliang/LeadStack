import { usePrefs, type Accent, type Density, type FontSizePref, type ThemeMode } from '../lib/prefs'
import { useI18n } from '../i18n'
import { cn } from '../lib/cn'
import { ACCENTS } from '../lib/prefs'
import { CheckIcon, GlobeIcon, PaletteIcon, TypeIcon } from './Icons'
import { Modal } from './Modal'
import { Button } from './Button'

interface AppearanceSettingsProps {
  /** 用于嵌入设置页时隐藏标题 */
  compact?: boolean
}

const THEMES: ThemeMode[] = ['light', 'dark', 'system']
const FONT_SIZES: FontSizePref[] = ['small', 'normal', 'large']
const DENSITIES: Density[] = ['comfortable', 'compact']

/** 外观设置：主题三态 + 5 主题色 + 字号三档 + 密度两档 + 语言。 */
export function AppearanceSettings({ compact = false }: AppearanceSettingsProps) {
  const prefs = usePrefs()
  const { t, language, setLanguage } = useI18n()

  return (
    <div className="space-y-5">
      <section>
        <h3 className="flex items-center gap-1.5 text-sm font-semibold text-slate-800 dark:text-slate-100">
          <PaletteIcon className="h-4 w-4 text-indigo-500" />
          {t('appearance.theme')}
        </h3>
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {t('appearance.themeHint')}
        </p>
        <div className="mt-2 grid grid-cols-3 gap-2">
          {THEMES.map((mode) => (
            <button
              key={mode}
              type="button"
              onClick={() => prefs.setTheme(mode)}
              className={cn(
                'rounded-lg border px-3 py-2 text-sm font-medium transition-colors',
                prefs.theme === mode
                  ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
              )}
            >
              {t(`appearance.theme.${mode}`)}
            </button>
          ))}
        </div>
      </section>

      <section>
        <h3 className="text-sm font-semibold text-slate-800 dark:text-slate-100">
          {t('appearance.accent')}
        </h3>
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {t('appearance.accentHint')}
        </p>
        <div className="mt-2 flex flex-wrap gap-2">
          {ACCENTS.map((accent) => (
            <button
              key={accent.value}
              type="button"
              title={t(`appearance.accent.${accent.value}`)}
              aria-label={t(`appearance.accent.${accent.value}`)}
              onClick={() => prefs.setAccent(accent.value as Accent)}
              className={cn(
                'flex h-9 w-9 items-center justify-center rounded-full ring-2 ring-offset-2 transition-transform hover:scale-105 dark:ring-offset-slate-900',
                prefs.accent === accent.value
                  ? 'ring-slate-400 dark:ring-slate-300'
                  : 'ring-transparent',
              )}
              style={{ backgroundColor: accent.color }}
            >
              {prefs.accent === accent.value ? (
                <CheckIcon className="h-4 w-4 text-white" />
              ) : null}
            </button>
          ))}
          <span className="self-center text-xs text-slate-400 dark:text-slate-500">
            {t(`appearance.accent.${prefs.accent}`)}
          </span>
        </div>
      </section>

      <section>
        <h3 className="flex items-center gap-1.5 text-sm font-semibold text-slate-800 dark:text-slate-100">
          <TypeIcon className="h-4 w-4 text-indigo-500" />
          {t('appearance.fontSize')}
        </h3>
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {t('appearance.fontSizeHint')}
        </p>
        <div className="mt-2 grid grid-cols-3 gap-2">
          {FONT_SIZES.map((size) => (
            <button
              key={size}
              type="button"
              onClick={() => prefs.setFontSize(size)}
              className={cn(
                'rounded-lg border px-3 py-2 text-sm font-medium transition-colors',
                prefs.fontSize === size
                  ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
              )}
            >
              {t(`appearance.fontSize.${size}`)}
            </button>
          ))}
        </div>
      </section>

      <section>
        <h3 className="text-sm font-semibold text-slate-800 dark:text-slate-100">
          {t('appearance.density')}
        </h3>
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {t('appearance.densityHint')}
        </p>
        <div className="mt-2 grid grid-cols-2 gap-2">
          {DENSITIES.map((density) => (
            <button
              key={density}
              type="button"
              onClick={() => prefs.setDensity(density)}
              className={cn(
                'rounded-lg border px-3 py-2 text-sm font-medium transition-colors',
                prefs.density === density
                  ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
              )}
            >
              {t(`appearance.density.${density}`)}
            </button>
          ))}
        </div>
      </section>

      <section>
        <h3 className="flex items-center gap-1.5 text-sm font-semibold text-slate-800 dark:text-slate-100">
          <GlobeIcon className="h-4 w-4 text-indigo-500" />
          {t('appearance.language')}
        </h3>
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {t('appearance.languageHint')}
        </p>
        <div className="mt-2 grid grid-cols-2 gap-2">
          {(['zh-CN', 'en-US'] as const).map((lang) => (
            <button
              key={lang}
              type="button"
              onClick={() => setLanguage(lang)}
              className={cn(
                'rounded-lg border px-3 py-2 text-sm font-medium transition-colors',
                language === lang
                  ? 'border-indigo-500 bg-indigo-50 text-indigo-700 dark:border-indigo-400 dark:bg-indigo-500/10 dark:text-indigo-300'
                  : 'border-slate-200 text-slate-600 hover:border-slate-300 dark:border-slate-700 dark:text-slate-300',
              )}
            >
              {lang === 'zh-CN' ? t('s.0255') : 'English'}
            </button>
          ))}
        </div>
      </section>

      {!compact ? (
        <p className="text-xs text-slate-400 dark:text-slate-500">{t('appearance.persistHint')}</p>
      ) : null}
    </div>
  )
}

export function AppearanceDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('appearance.title')}
      description={t('appearance.description')}
      size="lg"
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <AppearanceSettings />
    </Modal>
  )
}
