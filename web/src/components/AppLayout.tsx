import { useEffect, useRef, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { cn } from '../lib/cn'
import { useAuth } from '../lib/auth'
import { api } from '../lib/api'
import { usePolling } from '../lib/hooks'
import { usePrefs } from '../lib/prefs'
import { useI18n } from '../i18n'
import { NAV_GROUPS, breadcrumbsFor } from '../lib/nav'
import { IconButton } from './Button'
import {
  ArrowUpCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  GlobeIcon,
  LogoutIcon,
  MenuIcon,
  PaletteIcon,
  SettingsIcon,
  ShieldCheckIcon,
  CloseIcon,
} from './Icons'
import { AppearanceDialog } from './AppearanceSettings'
import { UpdateDialog, useUpdateNotice } from './SystemUpdate'

import { t } from '../i18n'
const roleLabelKeys: Record<string, string> = {
  admin: 'role.admin',
  operator: 'role.operator',
  viewer: 'role.viewer',
}

function Logo() {
  return (
    <Link to="/" className="flex shrink-0 items-center gap-2.5" aria-label="LeadStack">
      <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-indigo-600 text-white shadow-sm">
        <svg viewBox="0 0 24 24" className="h-5 w-5" fill="currentColor" aria-hidden="true">
          <rect x="4" y="4.5" width="16" height="3.6" rx="1" />
          <rect x="4" y="10.2" width="16" height="3.6" rx="1" opacity="0.8" />
          <rect x="4" y="15.9" width="16" height="3.6" rx="1" opacity="0.6" />
        </svg>
      </span>
      <span className="flex items-baseline gap-1.5">
        <span className="text-[15px] font-semibold tracking-tight text-slate-900 dark:text-slate-100">
          LeadStack
        </span>
        <span className="text-xs font-medium text-slate-400 dark:text-slate-500">{t('s.0253')}</span>
      </span>
    </Link>
  )
}

function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useI18n()
  const { can } = useAuth()
  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <div className="flex h-14 shrink-0 items-center border-b border-slate-200 px-4 dark:border-slate-800">
        <Logo />
      </div>
      <nav className="flex-1 space-y-4 px-2 py-3" aria-label={t('nav.main')}>
        {NAV_GROUPS.map((group) => {
          const items = group.items.filter((item) => !item.permission || can(item.permission))
          if (items.length === 0) return null
          return (
          <div key={group.key}>
            <p className="px-3 pb-1 text-[11px] font-semibold tracking-wider text-slate-400 uppercase dark:text-slate-500">
              {t(group.labelKey)}
            </p>
            <ul className="space-y-0.5">
              {items.map((item) => {
                const Icon = item.icon
                return (
                  <li key={item.to}>
                    <NavLink
                      to={item.to}
                      end={item.end}
                      onClick={onNavigate}
                      className={({ isActive }) =>
                        cn(
                          'flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                          isActive
                            ? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300'
                            : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
                        )
                      }
                    >
                      <Icon className="h-4 w-4 shrink-0" />
                      <span className="truncate">{t(item.labelKey)}</span>
                    </NavLink>
                  </li>
                )
              })}
            </ul>
          </div>
          )
        })}
      </nav>
    </div>
  )
}

export function AppLayout() {
  const { t } = useI18n()
  const { user, logout, can } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const prefs = usePrefs()

  const [mobileOpen, setMobileOpen] = useState(false)
  const [userMenuOpen, setUserMenuOpen] = useState(false)
  const [appearanceOpen, setAppearanceOpen] = useState(false)
  const [updateOpen, setUpdateOpen] = useState(false)
  const update = useUpdateNotice()
  const systemInfo = usePolling(api.systemInfo, 300_000)
  const currentVersion = update.info?.current || systemInfo.data?.version || '—'
  const hasUpdate = Boolean(update.info?.has_update)
  const menuRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    setMobileOpen(false)
    setUserMenuOpen(false)
  }, [location.pathname])

  useEffect(() => {
    if (!userMenuOpen) return
    const handler = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setUserMenuOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [userMenuOpen])

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  // 首次安装向导：needs_setup 为真且用户未跳过时进入 /setup
  useEffect(() => {
    if (location.pathname === '/setup') return
    let cancelled = false
    try {
      if (window.localStorage.getItem('leadstack.setupDone') === '1') return
    } catch {
      return
    }
    api
      .systemInfo()
      .then((info) => {
        if (cancelled) return
        if (info.needs_setup) navigate('/setup', { replace: true })
      })
      .catch(() => {
        /* 接口未就绪时不阻塞 */
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname])

  const crumbs = breadcrumbsFor(location.pathname)
  const isBuiltinRole = user ? ['admin', 'operator', 'viewer'].includes(user.role) : false
  const roleLabel = user
    ? isBuiltinRole
      ? t(roleLabelKeys[user.role] ?? 'role.viewer')
      : user.role_name ?? user.role
    : ''
  const limitedScope = user ? user.scope.mode !== 'all' : false

  return (
    <div className="flex min-h-full">
      {/* 桌面侧边栏 */}
      <aside className="hidden w-60 shrink-0 border-r border-slate-200 bg-white lg:block dark:border-slate-800 dark:bg-slate-900">
        <div className="sticky top-0 h-screen">
          <SidebarContent />
        </div>
      </aside>

      {/* 移动端抽屉 */}
      {mobileOpen ? (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div
            className="absolute inset-0 bg-slate-900/40 backdrop-blur-sm dark:bg-black/60"
            onClick={() => setMobileOpen(false)}
            aria-hidden="true"
          />
          <aside className="relative h-full w-64 border-r border-slate-200 bg-white shadow-xl dark:border-slate-800 dark:bg-slate-900">
            <div className="absolute top-2.5 right-2 z-10">
              <IconButton label={t('common.close')} onClick={() => setMobileOpen(false)}>
                <CloseIcon className="h-4 w-4" />
              </IconButton>
            </div>
            <SidebarContent onNavigate={() => setMobileOpen(false)} />
          </aside>
        </div>
      ) : null}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-40 border-b border-slate-200 bg-white/85 backdrop-blur dark:border-slate-800 dark:bg-slate-950/85">
          <div className="flex h-14 items-center gap-2 px-4 sm:px-6">
            <IconButton
              label={t('nav.openMenu')}
              className="lg:hidden"
              onClick={() => setMobileOpen(true)}
            >
              <MenuIcon className="h-5 w-5" />
            </IconButton>

            <nav className="flex min-w-0 items-center gap-1 text-sm" aria-label={t('nav.breadcrumb')}>
              <Link
                to="/"
                className="truncate text-slate-400 transition-colors hover:text-slate-600 dark:text-slate-500 dark:hover:text-slate-300"
              >
                LeadStack
              </Link>
              {crumbs.map((crumb, index) => (
                <span key={`${crumb.labelKey}-${index}`} className="flex min-w-0 items-center gap-1">
                  <ChevronRightIcon className="h-3.5 w-3.5 shrink-0 text-slate-300 dark:text-slate-600" />
                  {crumb.to ? (
                    <Link
                      to={crumb.to}
                      className="truncate text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
                    >
                      {t(crumb.labelKey)}
                    </Link>
                  ) : (
                    <span className="truncate font-medium text-slate-800 dark:text-slate-100">
                      {t(crumb.labelKey)}
                    </span>
                  )}
                </span>
              ))}
            </nav>

            <div className="ml-auto flex items-center gap-1">
              {limitedScope ? (
                <span
                  className="hidden items-center gap-1 rounded-md border border-amber-200 bg-amber-50 px-2 py-1 text-[11px] font-medium text-amber-700 sm:flex dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300"
                  title={t('authz.scopeLimitedHint', {
                    groups: user?.scope.group_ids.length ?? 0,
                    servers: user?.scope.server_ids.length ?? 0,
                  })}
                >
                  <ShieldCheckIcon className="h-3 w-3" />
                  {t('authz.scopeLimited')}
                </span>
              ) : null}
              <span className="hidden items-center gap-1 rounded-md border border-slate-200 px-2 py-1 text-[11px] text-slate-500 sm:flex dark:border-slate-700 dark:text-slate-400">
                <GlobeIcon className="h-3 w-3" />
                {prefs.language === 'en-US' ? 'EN' : t('s.0254')}
              </span>
              <IconButton label={t('appearance.title')} onClick={() => setAppearanceOpen(true)}>
                <PaletteIcon className="h-4 w-4" />
              </IconButton>

              <div className="relative" ref={menuRef}>
                <button
                  type="button"
                  onClick={() => setUserMenuOpen((open) => !open)}
                  className="flex items-center gap-2 rounded-md px-1.5 py-1 transition-colors hover:bg-slate-100 dark:hover:bg-slate-800"
                  aria-haspopup="menu"
                  aria-expanded={userMenuOpen}
                >
                  <span className="relative flex h-7 w-7 items-center justify-center rounded-full bg-slate-100 text-xs font-semibold text-slate-600 uppercase dark:bg-slate-800 dark:text-slate-300">
                    {user?.username.slice(0, 1) ?? '?'}
                    {hasUpdate ? (
                      <span
                        className="absolute -top-0.5 -right-0.5 h-2.5 w-2.5 rounded-full bg-indigo-500 ring-2 ring-white dark:ring-slate-950"
                        aria-label={t('update.available')}
                      />
                    ) : null}
                  </span>
                  <span className="hidden flex-col leading-tight text-left sm:flex">
                    <span className="text-xs font-medium text-slate-700 dark:text-slate-200">
                      {user?.username ?? ''}
                    </span>
                    <span className="text-[11px] text-slate-400 dark:text-slate-500">{roleLabel}</span>
                  </span>
                  <ChevronDownIcon className="h-3.5 w-3.5 text-slate-400" />
                </button>

                {userMenuOpen ? (
                  <div
                    role="menu"
                    className="absolute right-0 z-50 mt-1 w-48 overflow-hidden rounded-lg border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900"
                  >
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        setUserMenuOpen(false)
                        setAppearanceOpen(true)
                      }}
                      className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-50 dark:text-slate-200 dark:hover:bg-slate-800"
                    >
                      <PaletteIcon className="h-4 w-4" />
                      {t('appearance.title')}
                    </button>
                    {can('system.manage') ? (
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        setUserMenuOpen(false)
                        setUpdateOpen(true)
                      }}
                      className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-50 dark:text-slate-200 dark:hover:bg-slate-800"
                    >
                      <ArrowUpCircleIcon className="h-4 w-4" />
                      <span className="flex-1">{t('update.menuEntry')}</span>
                      <span className="font-mono text-[11px] text-slate-400 dark:text-slate-500">
                        {currentVersion}
                      </span>
                      {hasUpdate ? <span className="h-2 w-2 rounded-full bg-indigo-500" /> : null}
                    </button>
                    ) : null}
                    <Link
                      to="/settings"
                      role="menuitem"
                      className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-slate-700 hover:bg-slate-50 dark:text-slate-200 dark:hover:bg-slate-800"
                    >
                      <SettingsIcon className="h-4 w-4" />
                      {t('nav.settings')}
                    </Link>
                    <div className="my-1 border-t border-slate-100 dark:border-slate-800" />
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => void handleLogout()}
                      className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-500/10"
                    >
                      <LogoutIcon className="h-4 w-4" />
                      {t('nav.logout')}
                    </button>
                  </div>
                ) : null}
              </div>
            </div>
          </div>
        </header>

        <main key={prefs.language} className="mx-auto w-full max-w-7xl flex-1 px-4 py-5 sm:px-6 lg:py-6">
          <Outlet />
        </main>

        <footer className="mx-auto w-full max-w-7xl px-4 pb-5 text-xs text-slate-400 sm:px-6 dark:text-slate-600">
          {t('app.footer')}
        </footer>
      </div>

      <AppearanceDialog open={appearanceOpen} onClose={() => setAppearanceOpen(false)} />
      <UpdateDialog open={updateOpen} onClose={() => setUpdateOpen(false)} />
    </div>
  )
}
