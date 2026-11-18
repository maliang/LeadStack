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

/** 纯函数：给定语言字典返回整屏文案。 */
export function shellCopy(): ShellCopy {
  return {
    appName: 'LeadStack 领栈',
    appTagline: '运维控制台',
    openNav: t('nav.openMenu'),
    closeNav: t('common.close'),
    mainNav: t('nav.main'),
    breadcrumbNav: t('nav.breadcrumb'),
    skipToContent: '跳到主要内容',
    logout: t('nav.logout'),
    loading: t('common.loading'),
    retry: t('common.retry'),
    currentUser: '当前账号',
    roleFallback: '成员',
    sessionErrorTitle: '会话校验失败',
    sessionErrorHint: '后端不可达或返回异常。可重试；若持续失败请重新登录。',
    anonymousTitle: '未登录',
    anonymousHint: '登录后显示可用导航；当前仅能访问公开页面。',
    navEmptyTitle: '没有可访问的导航',
    navEmptyHint: '当前账号缺少模块权限，请联系管理员分配角色。',
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
