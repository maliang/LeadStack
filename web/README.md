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
- 接口契约见 `docs/ARCHITECTURE.md` 第 3 节，统一响应 `{"data":...}` / `{"error":{code,message}}`，
  所有时间戳为 Unix 毫秒。字段名以契约为准，前端不做重命名。
- 依赖克制：仅 `react` / `react-dom` / `react-router-dom`；轮询用自写 `src/lib/hooks.ts`，不引数据请求库。
- 深色模式用 `html.dark` 类切换（Tailwind 4 `@custom-variant dark`），默认浅色，选择存 `localStorage`。
  首屏前由 `index.html` 内联脚本应用，避免闪白。

## 目录

```
src/
├── App.tsx                 路由装配（/login、/、/servers/:id）
├── main.tsx                HashRouter + 挂载
├── lib/
│   ├── api.ts              REST 客户端、类型、响应归一化
│   ├── auth.tsx            登录态 Context（token 存 localStorage，401 自动登出）
│   ├── hooks.ts            usePolling（默认 10s）、useNow
│   ├── format.ts           相对时间、百分比、运行时长等格式化
│   ├── errors.ts / cn.ts / theme.ts
├── components/             布局、表格、徽标、弹窗、接入向导、Toast、图标
└── pages/                  LoginPage、DashboardPage、ServerDetailPage
```

## 与后端字段的两点兜底（M0）

1. **列表指标**：契约第 3 节 `GET /api/v1/servers` 的「主要字段」未含指标，但总览表格需要展示
   CPU/内存/负载。前端先读列表项上的 `metrics_summary` / `metrics` / `latest_metrics` / 平铺字段；
   若都缺失，则对最多 20 台服务器并发调用 `GET /api/v1/servers/{id}`（契约中该接口返回
   `metrics_summary`）补齐。后端若愿意在列表里直接返回 `metrics_summary`，前端自动不再补拉。
2. **状态**：优先使用后端返回的 `status`；若缺失或非法，再按 `last_seen_at`（≤45s online，
   否则 offline，从未连接 unknown）在前端推导，保证徽标不空。
