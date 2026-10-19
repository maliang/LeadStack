# web/ · LeadStack 控制台前端

React 19 + TypeScript + Vite + Tailwind 4 的单页应用。构建产物会被 `make build` 复制到
`internal/webui/dist`，由控制端用 `//go:embed` 打进二进制，**与 REST API 同端口同源**提供。

## 命令

```bash
pnpm --dir web install          # 安装依赖
pnpm --dir web dev              # 开发服务器 http://127.0.0.1:5173（/api 代理到 127.0.0.1:8080）
pnpm --dir web build            # tsc -b && vite build → web/dist
pnpm --dir web typecheck        # 仅类型检查
pnpm --dir web preview          # 预览构建产物
```

## 约束与约定

- `vite.config.ts` 中 `base: './'`：产物必须使用相对路径资源，才能被 embed 后在任意子路径下同端口提供。
- 路由使用 `HashRouter`：地址形如 `http://<host>:8080/#/servers/<id>`，避免子路径下的资源相对路径问题。
- 接口契约见 `docs/ARCHITECTURE.md` 第 3 节（M0）与第 10 节（M1 新增端点），统一响应
  `{"data":...}` / `{"error":{code,message}}`，所有时间戳为 Unix 毫秒。字段名以契约为准，前端不做重命名。
- 依赖克制：运行时仅 `react` / `react-dom` / `react-router-dom` / `uplot`（唯一图表库）；
  轮询用自写 `src/lib/hooks.ts`，不引数据请求库。
- 深色模式用 `html.dark` 类切换（Tailwind 4 `@custom-variant dark`），默认浅色，选择存 `localStorage`。
  首屏前由 `index.html` 内联脚本应用，避免闪白；图表跟随主题重绘。

## 页面与能力（M0 + M1）

| 路由 | 说明 |
|---|---|
| `#/login` | 登录，JWT 存 localStorage，401 自动登出 |
| `#/` | 总览：统计卡片、分组/标签/状态/方式筛选、勾选批量操作、服务器表格（状态徽标、接入方式、分组、主机信息摘要、最后错误 tooltip） |
| `#/credentials` | 凭证管理：列表（名称/类型/用户名/指纹/引用数）、新建（密码或私钥，私钥可粘贴或上传文件）、删除（被引用时 409 给出中文指引） |
| `#/servers/:id` | 详情：基础信息、实时指标、主机信息卡（未采集时的引导）、uPlot 指标历史曲线（raw/1m/1h × 1h/24h/7d）、测试连通性、刷新主机信息 |

- 列表与详情每 **10 秒**轮询一次；指标曲线每 30 秒刷新；页面不可见时跳过轮询。
- 接入向导先选 **Agent 一键接入**（生成令牌 + 安装命令）或 **SSH 免安装**（凭证、端口、主机密钥策略、
  分组、标签）。凭证可在向导内直接新建。
- 批量操作（测试连通性 / 刷新主机信息 / 加标签 / 设分组 / 删除）逐台展示成功或失败原因，并给出成功/失败汇总。
- **密钥安全**：凭证接口只返回元数据；私钥输入默认掩码，提交成功后立即清空，界面与产物中都不会回显密钥内容。

## 目录

```
src/
├── App.tsx                 路由装配（/login、/、/credentials、/servers/:id）
├── main.tsx                HashRouter + 挂载
├── lib/
│   ├── api.ts              REST 客户端、契约类型、响应归一化
│   ├── auth.tsx            登录态 Context（token 存 localStorage，401 自动登出）
│   ├── hooks.ts            usePolling（默认 10s，支持 key 触发重取）、useNow
│   ├── format.ts           相对时间、字节、延迟、指纹、指标中文名
│   ├── errors.ts / cn.ts / theme.ts（全局主题订阅，图表随之重绘）
├── components/             布局、表格、筛选、批量栏、凭证弹窗、添加服务器向导、
│                           主机信息、uPlot 曲线、状态/方式徽标、Toast、图标
└── pages/                  LoginPage、DashboardPage、CredentialsPage、ServerDetailPage
```

## 与后端字段的兼容兜底

1. **metrics_summary key**：以契约 3.1 的点分命名为准（`cpu.pct` / `mem.used.pct` / `mem.used_bytes` /
   `disk.used.pct` / `load.1` / `uptime_s`）。M0 期间后端曾返回下划线别名（`disk.used_pct`、
   `mem_used_pct`），前端两种都认，缺失显示「—」不报错。
2. **列表指标**：列表项若缺 `metrics_summary`，对最多 20 台并发补拉 `GET /servers/{id}`；当前后端列表
   已直接返回该字段，兜底不再触发。
3. **状态**：优先使用后端 `status`；缺失或非法时再按 `last_seen_at`（≤45s online，否则 offline，
   从未连接 unknown）推导，保证徽标不空。
4. **host_info 为空**：详情页显示「尚未采集，点击刷新主机信息」引导（SSH 模式可一键刷新；Agent 模式提示
   agent 上线后自动上报），列表主机信息列显示「尚未采集」。
