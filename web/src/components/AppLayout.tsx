import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { cn } from '../lib/cn'
import { useAuth } from '../lib/auth'
import { Button } from './Button'
import { LogoutIcon } from './Icons'
import { ThemeToggle } from './ThemeToggle'

const roleLabels: Record<string, string> = {
  admin: '管理员',
  operator: '运维',
  viewer: '只读',
}

function Logo() {
  return (
    <Link to="/" className="flex shrink-0 items-center gap-2.5" aria-label="LeadStack 领栈 总览">
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
        <span className="text-xs font-medium text-slate-400 dark:text-slate-500">领栈</span>
      </span>
    </Link>
  )
}

export function AppLayout() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  const roleLabel = user ? (roleLabels[user.role] ?? user.role) : ''

  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-40 border-b border-slate-200 bg-white/85 backdrop-blur dark:border-slate-800 dark:bg-slate-950/85">
        <div className="mx-auto flex h-14 w-full max-w-7xl items-center gap-2 px-4 sm:gap-3 sm:px-6">
          <Logo />
          <nav className="ml-2 flex items-center gap-1 sm:ml-4" aria-label="主导航">
            <NavLink
              to="/"
              end
              className={({ isActive }) =>
                cn(
                  'rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors sm:px-3',
                  isActive
                    ? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
                )
              }
            >
              总览
            </NavLink>
            <NavLink
              to="/checks"
              className={({ isActive }) =>
                cn(
                  'rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors sm:px-3',
                  isActive
                    ? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
                )
              }
            >
              检测
            </NavLink>
            <NavLink
              to="/credentials"
              className={({ isActive }) =>
                cn(
                  'rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors sm:px-3',
                  isActive
                    ? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-500/10 dark:text-indigo-300'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
                )
              }
            >
              凭证
            </NavLink>
          </nav>

          <div className="ml-auto flex items-center gap-1 sm:gap-2">
            <ThemeToggle />
            {user ? (
              <div className="hidden items-center gap-2 rounded-md px-1.5 py-1 sm:flex">
                <span className="flex h-7 w-7 items-center justify-center rounded-full bg-slate-100 text-xs font-semibold text-slate-600 uppercase dark:bg-slate-800 dark:text-slate-300">
                  {user.username.slice(0, 1)}
                </span>
                <span className="flex flex-col leading-tight">
                  <span className="text-xs font-medium text-slate-700 dark:text-slate-200">
                    {user.username}
                  </span>
                  <span className="text-[11px] text-slate-400 dark:text-slate-500">{roleLabel}</span>
                </span>
              </div>
            ) : null}
            <Button
              variant="ghost"
              size="sm"
              onClick={handleLogout}
              icon={<LogoutIcon className="h-4 w-4" />}
              aria-label="退出登录"
            >
              <span className="hidden sm:inline">退出</span>
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-6 sm:px-6 lg:py-8">
        <Outlet />
      </main>

      <footer className="mx-auto w-full max-w-7xl px-4 pb-6 text-xs text-slate-400 sm:px-6 dark:text-slate-600">
        LeadStack 领栈 · 多服务器部署与检测平台
      </footer>
    </div>
  )
}
