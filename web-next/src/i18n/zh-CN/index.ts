import common from './common'
import servers from './servers'
import serversBatch from './serversBatch'
import deploy from './deploy'
import system from './system'
import tools from './tools'
import alerts from './alerts'
import checks from './checks'
import dashboard from './dashboard'
import legacy from './legacy'
import ops from './ops'
import upgrade from './upgrade'
import templates from './templates'

/**
 * zh-CN 词典入口：领域文件展开合并为与改造前完全一致的扁平键表，
 * 对外仍由 src/i18n/index.ts 的 t() / useI18n() 访问。
 */
const zhCN: Record<string, string> = {
  ...common,
  ...servers,
  ...serversBatch,
  ...deploy,
  ...system,
  ...tools,
  ...alerts,
  ...checks,
  ...dashboard,
  ...legacy,
  ...ops,
  ...upgrade,
  ...templates,
}

export default zhCN
