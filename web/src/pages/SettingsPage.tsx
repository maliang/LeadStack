import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { useI18n } from '../i18n'
import { useAuth } from '../lib/auth'
import { errorMessage } from '../lib/errors'
import { usePrefs } from '../lib/prefs'
import { Button } from '../components/Button'
import { AppearanceSettings } from '../components/AppearanceSettings'
import { KeyIcon } from '../components/Icons'
import { PageHeader } from '../components/PageHeader'
import { useToast } from '../components/Toast'

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100'
const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

export default function SettingsPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const prefs = usePrefs()
  const { toast } = useToast()
  const infoQuery = usePolling(api.systemInfo, 30_000)

  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleChangePassword = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!oldPassword || newPassword.length < 6) {
      setError(t('users.errorPassword'))
      return
    }
    if (newPassword !== confirmPassword) {
      setError(t('settings.passwordMismatch'))
      return
    }
    setSaving(true)
    setError(null)
    try {
      await api.changeMyPassword({ old_password: oldPassword, new_password: newPassword })
      toast(t('users.ownPasswordChanged'), 'success')
      setOldPassword('')
      setNewPassword('')
      setConfirmPassword('')
    } catch (caught) {
      setError(errorMessage(caught, t('common.error')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader title={t('nav.settings')} description={t('settings.description')} />

      <div className="grid gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
          <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
            {t('appearance.title')}
          </h2>
          <div className="mt-3">
            <AppearanceSettings />
          </div>
        </section>

        <div className="space-y-4">
          <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
              {t('settings.account')}
            </h2>
            <dl className="mt-2 space-y-1.5 text-sm">
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('users.username')}</dt>
                <dd className="text-slate-800 dark:text-slate-100">{user?.username ?? '—'}</dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('users.role')}</dt>
                <dd className="text-slate-800 dark:text-slate-100">
                  {user ? t(`role.${user.role}`) : '—'}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('settings.version')}</dt>
                <dd className="font-mono text-slate-800 dark:text-slate-100">
                  {infoQuery.data?.version ?? '—'}
                </dd>
              </div>
            </dl>
            <Link to="/users" className="mt-3 inline-block">
              <Button size="sm" variant="secondary">
                {t('settings.manageUsers')}
              </Button>
            </Link>
            <Link to="/credentials" className="mt-3 ml-2 inline-block">
              <Button size="sm" variant="secondary" icon={<KeyIcon className="h-3.5 w-3.5" />}>
                {t('nav.credentials')}
              </Button>
            </Link>
          </section>

          <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
              {t('users.changePassword')}
            </h2>
            <form onSubmit={handleChangePassword} className="mt-3 space-y-3">
              <div>
                <label htmlFor="settings-old" className={labelClass}>
                  {t('users.oldPassword')}
                </label>
                <input
                  id="settings-old"
                  type="password"
                  value={oldPassword}
                  onChange={(event) => setOldPassword(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                  autoComplete="current-password"
                />
              </div>
              <div>
                <label htmlFor="settings-new" className={labelClass}>
                  {t('users.newPassword')}
                </label>
                <input
                  id="settings-new"
                  type="password"
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                  autoComplete="new-password"
                />
              </div>
              <div>
                <label htmlFor="settings-confirm" className={labelClass}>
                  {t('settings.confirmPassword')}
                </label>
                <input
                  id="settings-confirm"
                  type="password"
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                  className={inputClass + ' mt-1.5'}
                  autoComplete="new-password"
                />
              </div>
              {error ? (
                <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
                  {error}
                </p>
              ) : null}
              <Button type="submit" loading={saving}>
                {t('common.save')}
              </Button>
            </form>
          </section>

          <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
            <h2 className="text-sm font-semibold text-slate-900 dark:text-slate-100">
              {t('settings.currentPreferences')}
            </h2>
            <dl className="mt-2 space-y-1.5 text-xs">
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('appearance.theme')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">
                  {t(`appearance.theme.${prefs.theme}`)}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('appearance.accent')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">
                  {t(`appearance.accent.${prefs.accent}`)}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('appearance.fontSize')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">
                  {t(`appearance.fontSize.${prefs.fontSize}`)}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('appearance.density')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">
                  {t(`appearance.density.${prefs.density}`)}
                </dd>
              </div>
              <div className="flex justify-between gap-3">
                <dt className="text-slate-500 dark:text-slate-400">{t('appearance.language')}</dt>
                <dd className="text-slate-700 dark:text-slate-200">
                  {prefs.language === 'en-US' ? 'English' : t('s.0255')}
                </dd>
              </div>
            </dl>
          </section>
        </div>
      </div>
    </div>
  )
}
