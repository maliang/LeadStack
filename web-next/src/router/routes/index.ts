import type { RouteRecordRaw } from 'vue-router'
import { serverRoutes } from './servers.ts'
import { toolRoutes } from './tools.ts'
import { deployRoutes } from './deploy.ts'
import { systemRoutes } from './system.ts'
import { miscRoutes } from './misc.ts'

/**
 * 全部路由记录的唯一汇总点。
 *
 * - 顺序 = 注册顺序；域内记录只增不改，name/path/meta 是跨模块契约。
 * - 显式 `.ts` 后缀：让 `node scripts/check-route-count.mjs` 能直接 import 本模块
 *   （Node 原生类型擦除要求 ESM 说明符带扩展名）；Vite 与 vue-tsc 同样接受。
 * - 这里只拼装，不创建 router、不写守卫 —— 守卫在 src/router/index.ts。
 */
export const routes: RouteRecordRaw[] = [
  ...serverRoutes,
  ...toolRoutes,
  ...deployRoutes,
  ...systemRoutes,
  ...miscRoutes,
]
