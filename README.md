# LeadStack 领栈

> 多服务器 **部署** 与 **检测** 平台 —— 把 N 台服务器的「上线 + 值守」收进一个界面。

Go 单二进制 + 内嵌 Web 控制台 + 轻量节点 agent，跨平台、开箱即用。

---

## 现在能做什么（M0）

- 一条命令启动控制端，浏览器登录控制台
- 生成接入令牌，一行命令把服务器接进来
- 节点 agent 主动外连上报心跳与指标，控制台实时显示在线状态、OS/架构、CPU/内存/磁盘/负载
- agent 掉线后自动判定离线

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
| `LEADSTACK_DATA_DIR` | `./data` | 数据目录（SQLite 单文件） |
| `LEADSTACK_ADMIN_PASSWORD` | 随机生成 | 初始管理员密码，仅首次初始化生效 |

## 设计原则

1. **简单优先**：一条命令启动，三步接入一台机器。
2. **批量优先**：为几十到几百台设计，而不是单机面板。
3. **部署与监控同源**：一次接入，同时具备下发应用与采集健康的能力。
4. **安全默认**：默认只监听本地、凭证加密存储、关键操作全审计。

## 状态

**M0（骨架）已完成并通过端到端验收** —— 详见 [M0 交付报告](docs/M0-REPORT.md)。M1–M6 计划见 [docs/PLAN.md](docs/PLAN.md)。

端到端验收可一键复现：

```bash
make build && bash scripts/verify_m0.sh
```
