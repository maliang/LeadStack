import { computed } from 'vue'
import { t, useI18n } from '@/i18n'

/**
 * 外壳（侧边栏 / 顶栏 / 空态 / 失败态）的全部可见文案。
 *
 * 集中在一处：状态映射与文案都在纯函数里，组件模板只做取值，
 * 便于一眼看全并做断言（也避免同一个词在两处各写一遍）。
 */
export interface ShellCopy {
  appName: string
  appTagline: string
  openNav: string
  closeNav: string
  mainNav: string
  breadcrumbNav: string
  skipToContent: string
  logout: string
  loading: string
  retry: string
  currentUser: string
  roleFallback: string
  /** 会话校验失败（网络 / 5xx）的标题与说明 */
  sessionErrorTitle: string
  sessionErrorHint: string
  /** 未登录时侧边栏的占位 */
  anonymousTitle: string
  anonymousHint: string
  /** 已登录但无任何可见导航项 */
  navEmptyTitle: string
  navEmptyHint: string
}

/** 纯函数：按当前语言（t 内部读取）返回整屏文案。 */
export function shellCopy(): ShellCopy {
  return {
    // 品牌名：中英文界面都显示「LeadStack 领栈」，属于商标/品牌，不翻译。
    appName: 'LeadStack 领栈',
    appTagline: t('shell.appTagline'),
    openNav: t('nav.openMenu'),
    closeNav: t('common.close'),
    mainNav: t('nav.main'),
    breadcrumbNav: t('nav.breadcrumb'),
    skipToContent: t('shell.skipToContent'),
    logout: t('nav.logout'),
    loading: t('common.loading'),
    retry: t('common.retry'),
    currentUser: t('shell.currentUser'),
    roleFallback: t('shell.roleFallback'),
    sessionErrorTitle: t('shell.sessionErrorTitle'),
    sessionErrorHint: t('shell.sessionErrorHint'),
    anonymousTitle: t('shell.anonymousTitle'),
    anonymousHint: t('shell.anonymousHint'),
    navEmptyTitle: t('shell.navEmptyTitle'),
    navEmptyHint: t('shell.navEmptyHint'),
  }
}

/** 组合式版本：语言切换时自动刷新文案。 */
export function useShellCopy() {
  const { language } = useI18n()
  return computed<ShellCopy>(() => {
    // 建立对 language 的响应式依赖：切换语言时重新取字典
    void language.value
    return shellCopy()
  })
}
