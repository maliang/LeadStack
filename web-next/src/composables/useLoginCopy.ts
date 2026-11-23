import { computed } from 'vue'
import { ApiError } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { formatDuration } from '@/lib/format'
import { t, useI18n } from '@/i18n'

/**
 * 登录页 / 无权限页的全部可见文案与状态映射（纯函数）。
 *
 * 为什么要集中：登录失败的四种分支（锁定 / 限流 / 剩余次数 / 通用）在旧版散落在组件里，
 * 文案与状态各写一套，测试只能靠点。这里全部收敛成 loginCopy() + loginErrorMessage()，
 * 组件模板只取值与派发事件。
 */
export interface LoginCopy {
  brandTagline: string
  title: string
  subtitle: string
  formTitle: string
  formHint: string
  usernameLabel: string
  passwordLabel: string
  submit: string
  submitting: string
  showPassword: string
  hidePassword: string
  checkingSession: string
  requiredError: string
  genericError: string
  retry: string
  footHint: string
  forbiddenTitle: string
  forbiddenHint: string
  backHome: string
  backToLogin: string
  signedInAs: string
}

/** 纯函数：按当前语言返回整页文案（t 内部读取当前词典）。 */
export function loginCopy(): LoginCopy {
  return {
    brandTagline: t('s.0253'),
    title: t('s.1039'),
    subtitle: t('s.1038'),
    formTitle: t('s.1039'),
    formHint: t('s.1040'),
    usernameLabel: t('s.0112'),
    passwordLabel: t('s.0073'),
    submit: t('s.1042'),
    submitting: t('login.submitting'),
    showPassword: t('server.access.showPassword'),
    hidePassword: t('server.access.hidePassword'),
    checkingSession: t('s.0487'),
    requiredError: t('s.1036'),
    genericError: t('s.1037'),
    retry: t('common.retry'),
    footHint: t('s.1043'),
    forbiddenTitle: t('authz.noPermission'),
    forbiddenHint: t('login.forbiddenHint'),
    backHome: t('login.backHome'),
    backToLogin: t('login.backToLogin'),
    signedInAs: t('login.signedInAs'),
  }
}

/** 组合式版本：语言切换时自动刷新文案。 */
export function useLoginCopy() {
  const { language } = useI18n()
  return computed<LoginCopy>(() => {
    // 建立对 language 的响应式依赖：切换语言后重新取值
    void language.value
    return loginCopy()
  })
}

/**
 * 纯函数：把登录失败映射成一句可读文案。
 *
 * 后端约定（POST /auth/login）：
 * - code=login_locked + details.retry_after_s   → 锁定倒计时
 * - code=rate_limited + details.retry_after_s   → 限流倒计时
 * - status=401 + details.remaining_attempts ≥ 1 → 提示剩余次数
 * - 其余 → errorMessage() 的文案（网络 / 5xx / 未知）
 */
export function loginErrorMessage(error: unknown): string {
  const fallback = t('login.failed')
  if (!(error instanceof ApiError)) return errorMessage(error, fallback)

  const details = error.details ?? {}
  const retryAfter = Number(details.retry_after_s)
  const remaining = Number(details.remaining_attempts)
  const duration = formatDuration(Number.isFinite(retryAfter) ? retryAfter : null)

  if (error.code === 'login_locked') return t('login.locked', { time: duration })
  if (error.code === 'rate_limited') return t('login.rateLimited', { time: duration })
  if (error.status === 401 && Number.isFinite(remaining) && remaining >= 1) {
    return t('login.remainingAttempts', { count: remaining })
  }
  return errorMessage(error, fallback)
}
