# M0 交付报告

> 完成时间：2026-10-19　|　验收人：Lead（独立于实现方）
> 构建环境：macOS 26 (arm64) · Go 1.27.2 · Node 26.11.1 / pnpm 10.34.7

---

## 一、结论

**M0 验收通过（11/11 项）**。四条并行工作流全部交付：控制端后端、节点 agent、React 控制台、工程化与安装包。

## 二、交付物

| 工作流 | 交付 |
|---|---|
| 控制端后端 | `cmd/leadstack` + `internal/{config,store,auth,api,webui}`；SQLite(WAL/单写) + 内嵌迁移；bcrypt+JWT；全部 REST 端点；`/agent/ws` 长连接与 45s 离线清扫 |
| 节点 agent | `cmd/leadstack-agent` + `internal/agent`；hello/心跳/指标上报、指数退避无限重连、15 分钟环形缓冲补传、job.exec 执行 |
| 控制台前端 | `web/` React 19 + Vite + Tailwind 4；登录、总览、服务器表格（状态徽标/指标/相对心跳）、接入向导、10s 轮询 |
| 工程化 | `Makefile`（build/build-go/cross/test/docker）、`deploy/install.sh`（systemd+launchd、幂等、可卸载）、`Dockerfile`+compose、`.goreleaser.yaml`、GitHub Actions CI |

## 三、验收证据（全部为 Lead 在冻结版本上实跑）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | `make build` 产出 `bin/leadstack` (11M) 与 `bin/leadstack-agent` (6.3M) | ✅ |
| 2 | 启动后 `/healthz` 200，3 秒内就绪 | ✅ `{"data":{"status":"ok","version":"dev","uptime_s":0}}` |
| 3 | 无 token 访问 `/api/v1/servers` → 401；带 token → 200 | ✅ |
| 4 | 初始管理员密码可登录 | ✅ |
| 5 | 接入令牌 + 一行安装命令生成 | ✅ |
| 6 | agent 接入 ≤3s 变 online，带 os/arch/kernel/agent_version | ✅ `darwin/arm64` |
| 7 | `metrics_summary` 真实落库 | ✅ `cpu.pct 30.1 / mem.used_pct 63.1 / mem.used_bytes 21.7GB / disk.used_pct 10.9 / load.1 13.06` |
| 8 | 杀掉 agent 后 ≤45s 判 offline | ✅ |
| 9 | `go vet ./...` + `go test ./...` 全绿 | ✅ 5 个包 ok |
| 10 | 内嵌 UI 与安装脚本同端口可用 | ✅ `/` 200 · `/assets/*.js` 200 · `/install.sh` 200 且与 `deploy/install.sh` **逐字节一致** |
| 11 | 交叉编译五平台 | ✅ 10 个产物（linux/darwin/windows × amd64/arm64） |

复现命令：`make build && bash scripts/verify_m0.sh`

## 四、并行协作方式

冻结 `docs/ARCHITECTURE.md` 作为唯一契约（REST 字段、WS 消息、DB schema），四人按互不重叠目录并行：
`cmd/leadstack`+`internal/*`（后端）／`cmd/leadstack-agent`+`internal/agent`（agent）／`web/`（前端）／`Makefile`+`deploy/`+CI（工程化）。
`go.mod`/`go.sum`/`pkg/protocol` 由 Lead 独占，避免并发写冲突。

集成期由 Lead 修复的三处跨工作流问题：
1. gopsutil 跨平台传递依赖缺失 → Lead 补齐 5 个 indirect 依赖，四平台交叉编译转绿；
2. `/install.sh` 404（安装命令不可用）→ 裁定由 `make web-embed` 把 `deploy/install.sh` 复制进 `internal/webui/dist/`，唯一事实来源仍是 `deploy/install.sh`；
3. 验收脚本自身缺陷（`api()` 参数顺序导致 401）→ 修复并新增「指标必须落库」断言。

## 五、已知限制（不阻塞 M0，已排入 M1）

1. `make build-go`（跳过前端）路径下 `/install.sh` 会 404，需用 `make build`；M1 改为正式 embed。
2. 指标保留 7 天、无降采样表；M1 加 1m/1h 汇总。
3. agent 服务托管依赖 `install.sh`（systemd/launchd），`kardianos/service` 自安装留待 M1（依赖已从 go.mod 移除，需要时再加回）。
4. M0 不实现 job.exec 的**控制端侧**下发与终端（agent 侧已实现并单测）；收到相关消息一律忽略不断连。
5. Docker 镜像与 GoReleaser 仅做结构校验（本机无 docker/goreleaser），由 CI 兜底。
6. macOS 公证 / Windows 代码签名未接入（CI 中留 TODO）。
7. 仓库尚未纳入 git 版本管理。

## 六、下一步（M1：服务器纳管）

凭证加密存储 → 纯 SSH 连通性测试与 Agentless 降级采集 → 服务器分组/标签与批量操作 → 主机信息卡与详情页 → 指标历史曲线。
