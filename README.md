# LeadStack 领栈

> 多服务器 **部署** 与 **检测** 平台 —— 把 N 台服务器的「上线 + 值守」收进一个界面。

Go 单二进制 + 内嵌 Web 控制台 + 轻量节点 agent，跨平台、开箱即用。

---

## 现在能做什么（M4）

- 一条命令启动控制端，浏览器登录控制台
- **两种接入方式**：Agent 常驻（秒级指标）/ SSH 免安装（控制端主动采集，无需在目标机装任何东西）
- **凭证托管**：密码与私钥均以 AES-256-GCM 加密落库，界面与接口都不回显密钥；主密钥 0600 存放
- **主机信息**：主机名 / OS / 内核 / 架构 / CPU 型号与核数 / 内存 / 磁盘 / 时区 / Docker / IP 列表
- **主动检测（5 类）**：TCP 端口存活 / HTTP 拨测（期望状态码 + 关键字）/ Ping / 进程存在 / 自定义脚本
  - 按间隔自动调度；一键「立即执行」并对每台机器给出结果与延迟
  - 连续失败达阈值自动置为 `down`，恢复自动回到 `up`
  - 结果时间线保留 30 天，服务器详情页可直接看某台机器的检测历史
- **告警（异常推送到人）**：
  - 规则来源三类：检测项状态翻转 / 服务器上下线 / 指标阈值（`cpu.pct > 90` 这类）
  - **持续时长**（异常要连续存在 N 秒才告警，避免抖动误报）、**去重窗口**（未恢复期间不反复打扰）、**静默**（维护期不再收新告警）
  - **恢复通知**（服务好了会告诉你，且穿透静默）、人工确认、告警历史与事件轨迹
  - **7 类通知渠道**：Webhook / 钉钉 / 企业微信 / 飞书 / Slack / Telegram / 邮件；每个渠道可「测试发送」
  - 多渠道隔离：一个渠道挂了不影响其它渠道，失败原因会记录在渠道上
- **批量部署与回滚**：
  - 三类部署模板：`binary`（下载解压落位）/ `compose`（容器编排）/ `systemd`（服务单元）；模板是版本化文本，发布时冻结快照
  - 制品来源：URL 下载（**必须提供 sha256**）/ 控制端上传（边写边算校验和）/ 容器镜像
  - **原子发布**：每次发布写独立 `releases/<id>` 目录，成功后才原子切换 `current` 符号链接 —— 失败绝不破坏线上版本
  - **秒级回滚**：`current` 指回上一成功版本，不重新下载
  - **批量推送**：一次发到 N 台机器，并发上限可配；一台失败不影响其它机器
  - **增量日志**：发布详情页按 `after_seq` 增量拉取并追加，实时看进度
  - **重试只跑失败项**、支持取消；有发布记录的应用不允许直接删除（409）
- **分组标签与批量操作**：按分组/标签/状态/接入方式筛选；批量测连通性、刷新主机信息、加标签、设分组、删除，逐台给出结果
- **指标与历史**：CPU / 内存 / 磁盘 / 负载 / 网络 / 进程数实时展示 + 历史曲线（raw / 1m / 1h 三档，自动降采样；大范围查询自动降级）
- agent 掉线 45 秒内自动判定离线；SSH 模式连续 3 次采集失败判离线并记录原因，恢复后自动清除错误

> 告警语义：静默只抑制**新故障**通知，恢复通知仍会送达；同一故障未恢复期间只保留一条告警记录，超过去重窗口才再次提醒。

> 检测的执行位置：`TCP/HTTP/Ping` 由控制端直连探测（外部视角）；`进程/脚本` 在目标机执行——Agent 模式经控制端下发任务，SSH 模式走 SSH 通道，对使用者透明。

## 快速开始

```bash
# 1. 构建（需要 Go 1.22+ 与 Node 20+/pnpm）
make build                # 产出 bin/leadstack 与 bin/leadstack-agent

# 2. 启动控制端（首次启动会打印初始管理员密码）
./bin/leadstack

# 3. 打开控制台
open http://127.0.0.1:8080
```

在控制台「接入服务器」生成令牌后，在目标机器上执行：

```bash
./bin/leadstack-agent --server http://<控制端地址>:8080 --token <接入令牌>
```

或使用一键安装脚本（Linux systemd / macOS launchd）：

```bash
curl -fsSL https://<你的地址>/install.sh | sh -s -- --server http://<控制端>:8080 --token <令牌>
```

## 目录结构

```
cmd/leadstack/          控制端入口
cmd/leadstack-agent/    节点 agent 入口
internal/               控制端实现（config/store/api/auth/webui/agent）
pkg/protocol/           agent ↔ 控制端 消息契约
web/                    React 控制台（构建产物内嵌进二进制）
deploy/                 安装脚本、systemd 单元、Docker Compose
docs/                   计划书与接口契约
```

## 文档

- [产品与技术计划](docs/PLAN.md)
- [架构与接口契约](docs/ARCHITECTURE.md) —— 改代码前必读

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `LEADSTACK_LISTEN` | `127.0.0.1:8080` | 监听地址（公网暴露请自行加 TLS 反代） |
| `LEADSTACK_DATA_DIR` | `./data` | 数据目录（SQLite 单文件、`master.key`、`known_hosts`） |
| `LEADSTACK_ADMIN_PASSWORD` | 随机生成 | 初始管理员密码，仅首次初始化生效 |
| `LEADSTACK_MASTER_KEY` | 无（用 `master.key`） | 64 位 hex 的凭证加密主密钥；给了就不读文件 |
| `LEADSTACK_SSH_INTERVAL` | `30s` | SSH 模式采集间隔（Go duration，1s–24h） |
| `LEADSTACK_CHECK_CONCURRENCY` | `8` | 检测调度并发上限 |
| `LEADSTACK_CHECK_INTERVAL_MIN` | `10s` | 检测项最小间隔（防呆下限） |
| `LEADSTACK_ALERT_EVAL_INTERVAL` | `30s` | 告警规则周期评估间隔（指标阈值与持续时长推进） |
| `LEADSTACK_ALERT_RETENTION_DAYS` | `90` | 已恢复告警保留天数 |
| `LEADSTACK_DEPLOY_ROOT` | `/opt/leadstack/apps` | 部署基础目录（实际路径 `<root>/<app_slug>`） |
| `LEADSTACK_DEPLOY_CONCURRENCY` | `4` | 发布并发上限 |
| `LEADSTACK_ARTIFACT_MAX_MB` | `512` | 上传制品大小上限 |
| `LEADSTACK_PUBLIC_URL` | 无 | 目标机回连控制端的地址；使用「上传型制品」时必填 |

> ⚠️ `master.key` 丢失将导致已存凭证无法解密，请与数据目录一并备份。

## 设计原则

1. **简单优先**：一条命令启动，三步接入一台机器。
2. **批量优先**：为几十到几百台设计，而不是单机面板。
3. **部署与监控同源**：一次接入，同时具备下发应用与采集健康的能力。
4. **安全默认**：默认只监听本地、凭证加密存储、主机密钥变更即拒绝、关键操作全审计。

## 状态

- **M0（骨架）已完成** —— [交付报告](docs/M0-REPORT.md)（11/11 项验收）
- **M1（服务器纳管）已完成** —— [交付报告](docs/M1-REPORT.md)（33/33 项验收，M0 无回归）
- **M2（监控与检测）已完成** —— [交付报告](docs/M2-REPORT.md)（25/25 项验收，M0/M1 无回归）
- **M3（告警）已完成** —— [交付报告](docs/M3-REPORT.md)（29/29 项验收，M0–M2 无回归）
- **M4（批量部署）已完成** —— [交付报告](docs/M4-REPORT.md)（33/33 项验收，M0–M3 无回归）
- M5–M6 计划见 [计划书](docs/PLAN.md)

端到端验收可一键复现（共 131 项断言）：

```bash
make build && make devssh
bash scripts/verify_m0.sh    # 11 项：接入、指标、离线判定
bash scripts/verify_m1.sh    # 33 项：凭证、SSH 纳管、主机信息、批量、历史
bash scripts/verify_m2.sh    # 25 项：五类检测、两条执行通道、调度、降级
bash scripts/verify_m3.sh    # 29 项：渠道、firing、去重、确认、恢复、指标规则、静默
bash scripts/verify_m4.sh    # 33 项：发布、产物落盘校验、失败保护、回滚、批量、重试、注入防护
# 或：make test-e2e
```

其它质量闸门：`make fmt-check`（gofmt）、`make test-race`（api + store 竞态检测）。
