# LeadStack 领栈 · 多服务器部署与检测平台 — 计划书 v0.1

> 状态：草案（待确认 5 项关键决策）
> 日期：2026-10-19

---

## 1. 产品定位

**一句话**：把 N 台服务器的「上线（部署）+ 值守（检测告警）」收进一个界面，开箱即用。

| 项 | 内容 |
|---|---|
| 目标用户 | 个人开发者、中小团队运维，管 5～200 台 VPS/云主机/自建机 |
| 核心场景 | ① 批量把应用部署/更新到多台机器 ② 7×24 检测服务器与业务存活，异常推送到人 |
| 部署形态 | 单二进制（含 Web UI）/ Docker 一条命令，启动后浏览器打开即用 |
| 简单性承诺 | 一条命令启动控制端；一条命令接入一台服务器；5 分钟内看到第一张监控图 |
| v1 非目标 | 不做 K8s 编排、不做 APM/链路追踪、不做镜像仓库、不做 CMDB/工单、不做 SaaS 计费 |

---

## 2. 总体架构

```
       浏览器 (React SPA)
            │  HTTP / WebSocket
            ▼
   ┌─────────────────────────┐        首次接入：SSH 引导
   │   LeadStack 控制端       │◄──────────────────────────────┐
   │  · API / 任务调度        │                               │
   │  · 检测与告警引擎        │   mTLS WebSocket (agent 主动外连)
   │  · SQLite 单文件存储     │◄──────────────┐               │
   └─────────────────────────┘               │               │
                                             ▼               ▼
                                   [leadstack-agent]   [无 Agent 节点]
                                   常驻 · 秒级指标      纯 SSH 拉取（降级）
```

**节点接入双模式**
1. **Agent 模式（推荐）**：控制端用 SSH 一次性引导安装 agent，此后 agent 主动外连，秒级指标 + 远程执行 + 实时日志。
2. **Agentless 模式（降级）**：不装任何东西，纯 SSH 周期采集/执行。适合托管环境、临时机器、禁止安装 agent 的客户。采样粒度 30s～5min。

**关键设计点**
- Agent **主动外连** `wss://control/agent`（默认 8443，公网环境建议走 443），天然穿透 NAT/防火墙，无需入站端口。
- 安全：自签内部 CA，agent 用一次性 Token 换取客户端证书（mTLS）；控制端默认只监听 `127.0.0.1`，暴露公网需显式开启 TLS。
- 心跳 20s，连续 3 次丢失（约 60s）判离线；离线/恢复各触发一次事件。
- 部署 = 控制端下发 **Job** → agent 拉取执行 → 日志分块流式回传；支持广播多台、并发度限制、失败即停、幂等重跑、回滚到上一版本。
- 单语言双产物：控制端与 agent 共用一套 Go 代码库与协议定义（同一 repo，两个 main）。

---

## 3. 技术栈（推荐方案 A：Go 单栈）

| 层 | 选型 | 理由 |
|---|---|---|
| 控制端 + Agent | **Go 1.22+** | 单静态二进制、交叉编译零依赖、内存占用低、并发模型适合长连接 |
| 存储 | **SQLite**（`modernc.org/sqlite`，纯 Go / CGO=0）+ WAL + 单写连接 | 单文件、免运维、交叉编译无需 C 工具链；`mattn/go-sqlite3` 需 CGO，不选 |
| 指标采集 | `gopsutil` 为主，按平台补采 | Linux：`/proc`+cgroup、Docker Engine API；macOS：`sysctl`/`libproc`（避开慢速 `lsof`）；Windows：PDH / `GetExtendedTcpTable`、Docker named pipe |
| 远程执行 | `golang.org/x/crypto/ssh` + `os/exec` | 引导安装、免 Agent 模式、Web 终端复用；Bastion 用 ProxyJump |
| 通信协议 | WebSocket + JSON（控制帧）/ 二进制（日志与终端流）over TLS | 443 出站穿 NAT、易调试、重连简单；gRPC 受代理影响难排查，轮询实时性差 |
| 前端 | React + TypeScript + Vite + Tailwind + shadcn/ui | 开发速度快、UI 一致性好 |
| 图表 / 终端 | **uPlot** / xterm.js + FitAddon | uPlot 轻量、适合高频时序（服务端降采样）；终端 resize 回传 cols/rows |
| 服务化 | Linux systemd · Windows `kardianos/service` · macOS launchd | 三平台一致的守护与开机自启 |
| 打包分发 | GoReleaser（linux/amd64·arm64、darwin/universal、windows/amd64）+ 多架构 Docker | 一次打全平台 |
| 签名 | macOS 公证（notarize）· Windows 代码签名 | 早建 CI 签名流程，并提供手动安装兜底 |
| 安装体验 | `curl -fsSL https://get.leadstack.dev \| sh`、Homebrew tap、Scoop、PowerShell 脚本注册 Windows 服务 | 极简接入 |

**方案 B（备选）**：TypeScript 全栈（Fastify + better-sqlite3 + ssh2 + systeminformation + React），用 `bun build --compile` 出单文件。
- 优点：本机 Node 26 + pnpm 已就绪，前端后端同语言。
- 缺点：单文件打包不如 Go 干净，常驻内存偏高，agent 分发体积大。

> 现状：本机 Node 26.11 / pnpm 10.34 / sqlite3 已具备，**Go 未安装**（需 `brew install go`，约 1 分钟）。出网正常（proxy.golang.org / registry.npmjs.org 均可达），依赖拉取无障碍。

---

## 3.5 Agent 协议与任务执行要点

**连接与鉴权**：agent 启动 → 携带一次性配对 Token 连接 `wss://control:8443/agent/enroll` → 控制端签发客户端证书 → 后续用 mTLS 连 `/agent`。Token 一次有效，防重放。

**消息类型（初版）**
| 方向 | 类型 | 说明 |
|---|---|---|
| agent→控制端 | `hello` | OS/架构/agent 版本/主机名/启动时间 |
| agent→控制端 | `heartbeat` | 20s 一次，带负载、内存、磁盘摘要 |
| agent→控制端 | `metrics` | 批量指标（默认 10～30s 聚合一次） |
| agent→控制端 | `job.ack` / `job.log` / `job.done` | 任务确认 / 日志分块 / 结果与退出码 |
| 控制端→agent | `config.push` | 采样间隔、白名单采集项、日志级别 |
| 控制端→agent | `job.exec` | 命令 / 脚本 / 部署模板（带幂等 key） |
| 控制端→agent | `term.open/data/resize/close` | Web 终端（与 SSH 复用同一抽象） |
| 控制端→agent | `agent.upgrade` | 下载新版本 → 校验 → 原子替换 → 重启 |

**可靠性设计**
- 任务先落库（`job_runs` 状态机：pending→dispatched→running→success/failed/canceled），agent 断线重连后按 run_id 续传日志、避免重复执行（幂等 key）。
- 日志流按 `seq` 分块回传并落库，前端从任意位置续读，刷新页面不丢进度。
- 控制端不可达时，agent 本地环形缓冲最近 N 分钟指标，重连后补传（带时间戳，服务端按 ts 去重入库）。
- 离线判定：心跳 20s，连续 3 次未达（约 60s）判离线并触发告警；`>25s` 未更新在 UI 上标黄，恢复时发恢复通知。
- 重连：指数退避 + 抖动；重连成功先补传本地环形缓冲的指标（服务端按 ts 去重入库）。
- 升级安全：agent 自我更新采用 A/B 双文件 + 校验和 + 失败回滚，绝不原地覆盖运行中的二进制。
- 终端安全：一次性票据换取终端会话、CSP 防注入、连接限流、全量审计。

**采集口径注意**（gopsutil 已够用，但需按平台补采）：进程 CPU 口径各平台不一致（Linux 用 `/proc/<pid>/stat` 的 jiffies 差值，不要直接读百分比）；macOS 无进程 IO 数据，UI 需隐藏而非显示 0；连接枚举在 macOS/Windows 需要更高权限，缺失时降级为「不展示」；Windows WMI 首次调用慢，需预热缓存。

---

## 4. 数据模型（初版）

```
users(id, name, pass_hash, role, totp_secret, created_at)
servers(id, name, host, port, tags, group_id, mode[agent|ssh], os_info,
        status, last_seen_at, agent_version, credential_id)
credentials(id, name, type[password|key], secret_enc, passphrase_enc)
metrics_samples(server_id, ts, metric, value)        -- 窄表
metrics_rollup(server_id, ts, metric, avg, max, min, bucket)
checks(id, name, type[http|tcp|ping|process|script|disk], target, interval,
       timeout, expect, enabled, server_ids)
check_results(check_id, server_id, ts, ok, latency_ms, message)
alert_rules(id, name, expr, for_duration, severity, silence_until, channel_ids)
alerts(id, rule_id, server_id, status[firing|resolved|acked], started_at, resolved_at)
alert_channels(id, type[webhook|email|dingtalk|wecom|feishu|telegram], config_enc)
jobs(id, name, kind[deploy|command|script], template_id, params, created_by)
job_runs(id, job_id, server_id, status, started_at, finished_at, exit_code)
job_logs(run_id, seq, ts, stream, chunk)
templates(id, name, type[compose|systemd|binary], content, version)
releases(id, app_id, version, artifact_url, checksum, template_id)
audit_logs(id, user_id, action, target, detail, ts)
settings(key, value)
```

---

## 4.5 功能基线（对标竞品后的优先级）

**市场缺口**：Portainer / Coolify / Dokploy / 1Panel / 宝塔 偏部署，监控只到容器与资源曲线；Netdata / Uptime Kuma / Beszel / Zabbix / Prometheus 偏监控，纳管与部署要另配工具。**「跨 SSH 批量纳管几十上百台 VPS + 部署 + 统一告警」这块大家都做得很浅** —— 这就是我们的切入点。

**P0（MVP 必须）**
1. SSH 密钥纳管 + 批量导入主机　2. 分组/标签 + 批量执行　3. 应用模板批量下发
4. CPU/内存/磁盘/负载/网络采集　5. 存活 + 端口/HTTP 拨测　6. 阈值规则 + 通知
7. 告警去重、静默、恢复通知　8. 一键安装与干净卸载

**P1**：Docker/Compose 部署与日志、历史曲线与保留策略、Web 终端、多渠道 Webhook、权限与审计、agent 自动升级

**P2**：Git/CI 集成与回滚、SSL 证书管理、数据备份、开放 API、移动端

**「简单易用」的具体落地**
- 一条命令安装（Docker 或 `curl | sh`），5 分钟内出第一台机器的数据。
- 首启向导：加机器 → 自动探测 → 一键装 agent → 立刻出图并触发首条告警。
- 优先支持 Agentless 纯 SSH，单二进制、低占用、可离线安装。
- 首页即「全部机器红/黄/绿总览」，点击下钻；所有操作默认支持批量。
- 通知模板内置「测试发送」，降低配置失败率。

**差异化三点**：① 部署与监控同源，一次接入同时下发应用与采集健康，不维护两套 agent；② 批量优先，为几十到几百台设计（批量接入/下发/策略），而非单机面板；③ 中文开箱，国内通知渠道 + 离线安装 + 国内镜像，开源自托管、不按节点收费。

---

## 5. 里程碑（单人估算 ≈ 6～8 周，2～3 人并行 ≈ 3～4 周）

| 里程碑 | 内容 | 交付验收 | 估时 |
|---|---|---|---|
| **M0 骨架** | 仓库结构、CI、配置、登录鉴权、SQLite 迁移、页面框架、健康检查 | 本地一条命令起来，能登录 | 3～5 天 |
| **M1 纳管** | 服务器 CRUD/分组标签、凭证加密存储、SSH 连通性测试、agent 一键安装、心跳与在线状态、主机信息采集、总览页 | 5 分钟接入一台机器并看到主机信息 | 1 周 |
| **M2 监控检测** | 指标采集与实时图表、历史存储与降采样、检测项（HTTP/TCP/Ping/进程/端口/磁盘阈值/自定义脚本）、服务器详情页、检测时间线 | 断网/端口关闭能在 1 分钟内看到红 | 1～1.5 周 |
| **M3 告警** | 规则引擎（阈值+持续时长+恢复通知）、防抖与静默、通知渠道（Webhook/邮件/钉钉/企微/飞书/Telegram）、告警历史与确认 | 阈值触发后 30 秒内收到通知 | 3～5 天 |
| **M4 部署** | 部署模板（compose/systemd/二进制+脚本）、制品来源（URL/上传）、批量执行与并发控制、实时日志、失败回滚、发布记录与版本回退、批量命令 | 一次操作把同一应用发到 10 台并成功回滚 | 1.5 周 |
| **M5 运维增强** | Web SSH 终端、文件上传下载、定时任务(cron)、日志查看、进程/端口实时视图 | 浏览器里能直接上机排障 | 1 周 |
| **M6 打磨** | RBAC（管理员/运维/只读）、审计日志、中英双语、备份恢复、安装向导、文档 | 交给非作者也能独立部署使用 | 1 周 |

**建议排期策略**：M1→M2→M3 是「可见价值最短路径」，先让软件立刻有用；M4 部署是差异化重头，紧随其后。

---

## 6. 质量与验收基线

- **易用**：控制端一条命令启动；接入一台机器 ≤ 3 步；首次进入有引导与演示数据。
- **性能**：100 节点 / 10s 采样，控制端 1C1G 稳定运行；agent 常驻内存 < 30MB；指标写入不阻塞 UI。
- **可靠**：agent 断线 60s 内自动重连；控制端重启不丢任务（任务落库、幂等重试）。
- **跨平台**：控制端 macOS / Linux / Windows 一致；agent 覆盖主流 Linux（amd64/arm64），macOS 与 Windows 标 Beta。
- **安全**：凭证 AES-GCM 加密落库（主密钥来自环境变量或密钥文件）；默认仅本地监听；终端操作与敏感动作进审计日志。

---

## 7. 风险与对策

| 风险 | 对策 |
|---|---|
| 范围膨胀（做成"宝塔"） | 冻结 v1 非目标，任何新功能进 v2 清单 |
| **Agent 本身就是特权 RCE 入口**（最大安全风险） | mTLS 双向认证 + RBAC 授权 + 命令白名单 + 全量审计；控制端默认仅本地监听 |
| 凭证与 Web 终端泄露 | 凭证 AES-GCM 加密落库（主密钥来自环境变量/密钥文件）；终端一次性票据 + CSP + 限流 |
| Agent 跨平台差异（Windows 服务、macOS 权限） | Linux 优先做扎实，Windows/macOS 标 Beta 并单独排期；Windows 用 `kardianos/service` |
| macOS 公证 / Windows 代码签名 | 尽早把签名与公证接入 CI，同时提供「手动下载安装」兜底路径，避免被证书问题卡住发布 |
| 大规模指标写入瓶颈 | SQLite WAL + **单写连接** + 批量写入 + 分级保留（原始/1m/1h）；>200 节点再评估 PostgreSQL/TimescaleDB |
| 部署幂等与回滚难 | 模板化 + 版本化 + 保留上一版本（目录/镜像 tag）实现快照式回滚 |
| 无 Go 环境 | `brew install go` 一次性解决；或切方案 B |

---

## 8. 已确认决策（2026-10-19）

| # | 决策 | 结论 |
|---|---|---|
| 1 | 技术栈 | **方案 A：Go 单栈**（Go 1.27 + modernc SQLite + React 内嵌） |
| 2 | 重心 | **均衡**：先落地 M1（纳管）→ M2（监控检测）→ M3（告警） |
| 3 | 接入模式 | **支持 Agentless 纯 SSH**，作为降级能力（仅首次引导与临时排障，不做高频采集） |
| 4 | 通知渠道 | **一版做全**：Webhook / 邮件 / 钉钉 / 企微 / 飞书 / Telegram / Slack |
| 5 | 控制端形态 | **本地单人使用优先**，RBAC 留到 M6 |

> 执行中：M0 已开工，接口契约见 [ARCHITECTURE.md](ARCHITECTURE.md)。

---

## 9. 仓库结构（预案）

```
LeadStack-领栈/
├── cmd/            leadstack(控制端) / leadstack-agent(节点)
├── internal/       api · store · metrics · checks · alerts · deploy · ssh · agent
├── pkg/protocol/   agent ↔ 控制端 消息定义
├── web/            React SPA（构建产物 embed 进二进制）
├── deploy/         docker-compose · systemd · install.sh · install.ps1
├── docs/           计划书 · 架构 · 接口 · 使用手册
└── Makefile · .goreleaser.yaml · .github/workflows/
```
