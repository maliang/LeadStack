/**
 * LeadStack 品牌标记（复用组件）。
 *
 * 造型：白色 L 形框架 =「领」（前导 / LeadStack 的首字母），
 * 贯穿的脉冲折线 =「栈」上跑着的检测与健康状态（emerald，与「在线/健康」语义一致）。
 * 单独作为 favicon / 文档图标时使用 web/public/favicon.svg（带圆角底）。
 */
export function BrandMark({ className = 'h-5 w-5' }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" className={className} fill="none" aria-hidden="true">
      <path
        d="M17 11 V45 H52"
        stroke="currentColor"
        strokeWidth={7.2}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <path
        d="M26 33 L32 33 L36 21 L42 38 L46 27 L52 27"
        stroke="#34d399"
        strokeWidth={4.8}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}
