import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '../lib/auth'
import { errorMessage } from '../lib/errors'
import { Button } from '../components/Button'
import { PageLoading } from '../components/Loading'
import { ThemeToggle } from '../components/ThemeToggle'
import { t } from '../i18n'

export default function LoginPage() {
  const { status, login } = useAuth()
  const navigate = useNavigate()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (status === 'loading') return <PageLoading label={t('s.0487')} />
  if (status === 'authenticated') return <Navigate to="/" replace />

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!username.trim() || !password) {
      setError(t('s.1036'))
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await login(username.trim(), password)
      navigate('/', { replace: true })
    } catch (caught) {
      setError(errorMessage(caught, t('s.1037')))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="relative flex min-h-screen flex-col items-center justify-center overflow-hidden px-4 py-12">
      <div className="pointer-events-none absolute inset-0" aria-hidden="true">
        <div className="absolute -top-40 left-1/2 h-80 w-[40rem] -translate-x-1/2 rounded-full bg-indigo-200/50 blur-3xl dark:bg-indigo-500/10" />
        <div className="absolute -bottom-24 -right-16 h-72 w-72 rounded-full bg-sky-200/40 blur-3xl dark:bg-sky-500/10" />
      </div>

      <div className="absolute right-4 top-4">
        <ThemeToggle />
      </div>

      <div className="relative w-full max-w-sm">
        <div className="flex flex-col items-center text-center">
          <span className="flex h-11 w-11 items-center justify-center rounded-xl bg-indigo-600 text-white shadow-lg shadow-indigo-600/25">
            <svg viewBox="0 0 24 24" className="h-6 w-6" fill="currentColor" aria-hidden="true">
              <rect x="4" y="4.5" width="16" height="3.6" rx="1" />
              <rect x="4" y="10.2" width="16" height="3.6" rx="1" opacity="0.8" />
              <rect x="4" y="15.9" width="16" height="3.6" rx="1" opacity="0.6" />
            </svg>
          </span>
          <h1 className="mt-4 flex items-baseline gap-1.5 text-xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">
            LeadStack
            <span className="text-sm font-medium text-slate-400 dark:text-slate-500">{t('s.0253')}</span>
          </h1>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{t('s.1038')}</p>
        </div>

        <div className="mt-6 rounded-2xl border border-slate-200 bg-white/90 p-6 shadow-xl shadow-slate-900/5 backdrop-blur dark:border-slate-800 dark:bg-slate-900/90 dark:shadow-black/30">
          <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">{t('s.1039')}</h2>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            {t('s.1040')}
          </p>

          <form onSubmit={handleSubmit} className="mt-5 space-y-4">
            <div>
              <label
                htmlFor="username"
                className="block text-sm font-medium text-slate-700 dark:text-slate-200"
              >
                {t('s.0112')}
              </label>
              <input
                id="username"
                name="username"
                autoComplete="username"
                autoFocus
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                placeholder="admin"
                className="mt-1.5 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
              />
            </div>

            <div>
              <label
                htmlFor="password"
                className="block text-sm font-medium text-slate-700 dark:text-slate-200"
              >
                {t('s.0073')}
              </label>
              <div className="relative mt-1.5">
                <input
                  id="password"
                  name="password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  placeholder={t('s.1041')}
                  className="w-full rounded-md border border-slate-300 bg-white py-2 pl-3 pr-16 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400"
                />
                <button
                  type="button"
                  onClick={() => setShowPassword((shown) => !shown)}
                  className="absolute inset-y-0 right-0 rounded-r-md px-3 text-xs font-medium text-slate-400 hover:text-slate-600 dark:hover:text-slate-300"
                  tabIndex={-1}
                >
                  {showPassword ? t('s.0137') : t('s.0138')}
                </button>
              </div>
            </div>

            {error ? (
              <p
                role="alert"
                className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
              >
                {error}
              </p>
            ) : null}

            <Button type="submit" loading={submitting} className="w-full">
              {t('s.1042')}
            </Button>
          </form>
        </div>

        <p className="mt-4 text-center text-xs leading-relaxed text-slate-400 dark:text-slate-500">
          {t('s.1043')}
        </p>
      </div>
    </div>
  )
}
