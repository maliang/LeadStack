# deploy/ —— 部署与运维

本目录是控制端与 agent 的部署资料 + 一键安装脚本（[devops] 维护）。

| 文件 | 用途 |
|---|---|
| [install.sh](install.sh) | **agent 一键安装/卸载脚本**（唯一事实来源；`make build` 会把它内嵌到 `internal/webui/dist/install.sh`，所以控制端 `GET /install.sh` 能直接下发） |
| [leadstack.service](leadstack.service) | 控制端 systemd 单元模板（含加固项与 StateDirectory） |
| [leadstack-agent.service](leadstack-agent.service) | agent systemd 单元模板（`install.sh` 按它生成同名单元） |
| [leadstack.env.example](leadstack.env.example) | 控制端环境变量示例（`LEADSTACK_LISTEN` / `LEADSTACK_DATA_DIR` / `LEADSTACK_MASTER_KEY` 等） |
| [docker-compose.yml](docker-compose.yml) | 单机容器部署（/data 卷 + healthcheck） |

## 安装 agent

```sh
# 控制台「接入向导」会给出一条带 token 的命令，形如：
curl -fsSL http://<控制端>:8080/install.sh | sh -s -- --server http://<控制端>:8080 --token <接入令牌>
```

常用参数：

| 参数 | 说明 |
|---|---|
| `--server URL` | 控制端地址（http/https/ws/wss），必填 |
| `--token TOKEN` | 接入令牌，必填 |
| `--version V` / `--agent-version V` | 指定版本（`latest` 或 `vX.Y.Z`），用于回滚 |
| `--base-url URL` | 发布包下载前缀（内网镜像/私有发布） |
| `--from-file PATH` | 直接用本地二进制安装（离线场景） |
| `--install-dir DIR` / `--env-dir DIR` | 自定义安装目录 / 环境变量目录 |
| `--no-service` | 只装二进制，不注册服务 |
| `--dry-run` | 只打印将要执行的动作，不改动系统 |
| `--uninstall` | 卸载：停服务 + 清单元（含 systemd wants 符号链接 / launchd plist 残留）+ 删二进制与凭证 |

行为要点：幂等（可重复执行，已存在服务时走 restart/reload）；Linux 用 systemd、macOS 用 launchd；
使用 `sudo` 前先探测（非交互且需要密码时直接给出明确报错，绝不静默提权）；
下载会校验 `checksums.txt` 的 sha256（存在时）。

## 安装控制端

```sh
make build
sudo useradd --system --home /var/lib/leadstack --shell /usr/sbin/nologin leadstack
sudo install -d -o leadstack -g leadstack /var/lib/leadstack /etc/leadstack
sudo install -m 0755 bin/leadstack /usr/local/bin/leadstack
sudo install -m 0640 leadstack.env.example /etc/leadstack/leadstack.env   # 按需修改
sudo install -m 0644 leadstack.service /etc/systemd/system/leadstack.service
sudo systemctl daemon-reload && sudo systemctl enable --now leadstack
journalctl -u leadstack -n 50   # 首次启动会打印随机初始管理员密码（仅一次）
```

容器方式：`docker compose -f deploy/docker-compose.yml up -d --build`。

## 开发自测（本机复现完整验收）

前置：Go 1.27+、Node 26 + pnpm 10、`jq`、`bash`、本地 C 工具链（race 闸门需要）；本机**不需要** sshd 与免密 sudo。

```sh
# 1) 构建交付物 + DEV-ONLY 测试 SSH 端点
make build      # bin/leadstack（内嵌 UI + /install.sh）、bin/leadstack-agent
make devssh     # bin/leadstack-devssh —— 仅供本地验收，绝不进发布产物

# 2) 两道静态闸门（CI 同口径）
make fmt-check  # gofmt -l 必须为空
make test-race  # go test -race 覆盖 internal/api 与 internal/store

# 3) 一键跑五套端到端验收（M0 → M1 → M2 → M3 → M4，日志落在 .e2e-logs/）
make test-e2e   # 等价于 make e2e
```

`make test-e2e` 的行为：

- 按序执行 `scripts/verify_m0.sh` → `verify_m1.sh` → `verify_m2.sh` → `verify_m3.sh` → `verify_m4.sh`（脚本由 Lead 维护）；
- 每个脚本的完整输出（含失败时打印的控制端/agent 日志尾部）tee 到 `.e2e-logs/verify_m0.log` … `verify_m4.log`；
- `TMPDIR` 被指向 `.e2e-logs/`，脚本异常中止时残留的 `leadstack*.log` 也留在那里，便于取证；
- 某个脚本**不存在**时只打印提示并跳过（不算失败），脚本落地后自动纳入，无需改 Makefile；
- 已存在的脚本一旦失败立即停止（不再跑后续脚本）并让 `make` 失败 —— 先用 `[1/5]`…`[5/5]` 定位到哪一档挂了；
- 脚本按 `PORT` 环境变量选端口（`verify_m0` 默认 18777、`verify_m1` 默认 18788，M2/M3/M4 见各自脚本），
  与日常 `make run` 的 8080 不冲突；同时跑多套时用 `PORT=18801 make test-e2e` 之类错开。

### 五个验收脚本分别验什么、大概多久

| 脚本 | 覆盖范围 | 典型耗时（本机 M1 Max） |
|---|---|---|
| `verify_m0.sh` | 交付命令产物、启动+登录、401/200、接入令牌与安装命令、agent 上线 → offline | **约 80–100 秒**（含 45 秒离线判定等待，是主要耗时） |
| `verify_m1.sh` | SSH 凭证（不泄露）、devssh 连通性、host_info、agentless 采集、指标历史、批量操作、分组过滤、409 保护、agent host_info | **约 60–90 秒**（含 3 秒 SSH 采集间隔下等指标落库） |
| `verify_m2.sh` | 检测项（tcp/http/process）、立即执行与调度结果、两种通道（agent/ssh）执行、大跨度降采样、校验鉴权 | **约 90–150 秒**（含 60 秒调度结果累积） |
| `verify_m3.sh` | 告警渠道（webhook 真实收到通知、密钥不回显）、check/metric 规则触发、ack/resolved、去重与静默、`/alerts/summary` | **约 100–180 秒**（含 `for_duration` 与周期评估等待，脚本会把评估间隔压到几秒） |
| `verify_m4.sh` | 应用/模板/制品、真实发布到 devssh 并校验产物落盘与 sha256、日志增量拉取、失败不改 `current`、回滚、并发批量、模板安全、重试、401/409 | **约 120–200 秒**（含发布轮询与回滚各一轮；SSH 通道由 devssh 承担） |

`make test-e2e` 全绿时总计约 **8–12 分钟**（脚本缺失会被跳过，相应更快）。
CI 上 M0/M1/M2/M3 为阻塞项，M4 在稳定前为 `continue-on-error`；任一项失败都会把 `.e2e-logs/` 作为 artifact 上传。

单独跑某一个脚本：

```sh
PORT=19001 bash scripts/verify_m0.sh
PORT=19002 bash scripts/verify_m1.sh
PORT=19003 bash scripts/verify_m2.sh
PORT=19004 bash scripts/verify_m3.sh
PORT=19005 bash scripts/verify_m4.sh
```

### M3 告警相关环境变量

| 变量 | 作用 | 备注 |
|---|---|---|
| `LEADSTACK_ALERT_EVAL_INTERVAL` | 告警规则（`source=metric`）的周期评估间隔 | 默认值见 `internal/config`；`verify_m3.sh` 会把它压到几秒以便在验收窗口内看到 `firing`。值是 Go duration 形式（如 `5s`、`1m`）。 |
| `LEADSTACK_CHECK_INTERVAL_MIN` | 检测项调度的最小间隔（M2 引入） | 默认值见 `internal/config`；验收脚本同样会调小。 |

生产建议：评估间隔保持默认（分钟级）即可，调太小会显著增加 SQLite 写入与 webhook 调用频率。

## M4：发布与部署目录约定

### 目标机上的部署根目录（回滚的基础）

每个应用在**目标机**上固定一个部署根目录（默认 `/opt/leadstack/apps/<app_slug>`，可在创建应用时用 `root` 覆盖）：

```
<root>/
├── current -> releases/20261019-171500-a1b2c3     # 符号链接，原子切换
├── releases/
│   ├── 20261019-171500-a1b2c3/                    # 每次发布一个不可变目录
│   └── 20261019-160000-d4e5f6/
└── .leadstack/                                    # 元数据：release id、模板快照、校验和
```

- **发布** = 临时目录准备新版本 → 原子切换 `current` → 重启服务；
- **回滚** = 把 `current` 指回上一个 release 目录 → 重启服务（秒级，不重新下载）；
- 因此 `<root>` 必须落在**目标机的持久化路径**上，且执行发布的账号（agent 默认 root / SSH 用户）对其有写权限。

### 制品存储位置

| 制品类型 | 存放位置 | 说明 |
|---|---|---|
| `upload` | **控制端**数据目录内（`<LEADSTACK_DATA_DIR>/artifacts/`，具体路径记录在 `artifacts.location`） | 容器部署时必须让 `/data` 用 volume 持久化，否则容器重建后制品文件丢失、历史 release 无法重新发布 |
| `url` | 不落盘 | 只登记 URL 与 sha256；发布时由目标机直接下载并校验 |
| `image` | 不进控制端 | 只记录镜像引用，不下载 |

上传大小上限由 `LEADSTACK_ARTIFACT_MAX_MB` 控制；制品与数据库同卷，备份 `/data` 即可同时保住两者。

### 三个环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `LEADSTACK_DEPLOY_ROOT` | `/opt/leadstack/apps` | 目标机部署根目录的**基路径**（应用未指定 `root` 时使用 `<LEADSTACK_DEPLOY_ROOT>/<app_slug>`）。仅在控制端自己也充当部署目标（本机 devssh / binary 模板）时才对控制端自身生效；容器场景需要把它挂成 volume（见下）。 |
| `LEADSTACK_DEPLOY_CONCURRENCY` | `4` | 一次发布同时推进的目标机数量上限（多台批量发布时）。调大对控制端和目标机都更吃资源，建议 ≤ CPU 核数。 |
| `LEADSTACK_ARTIFACT_MAX_MB` | `512` | 上传型制品单文件大小上限（MB）。超过直接拒绝，避免把磁盘写满。 |

以上默认值以 `internal/config` 的实现为准（M4 收敛中）。

容器场景下部署根目录的可选挂载示例见 [docker-compose.yml](docker-compose.yml)（`leadstack-deploy` 卷，默认注释掉）与 [Dockerfile](../Dockerfile) 末尾说明：
只有「控制端自己也是部署目标」时才需要挂载；纯控制端 + 远程 agent/SSH 的拓扑不需要。

### M4 已知限制

- **控制端重启后，正在运行的发布不会自动恢复。** `release_run` 的状态机推进在控制端进程内完成；进程重启后仍处于 `pending`/`running` 的 run 会停在原状态（不会自动续跑，也不会被标记失败）。处理方式：用 `POST /releases/{id}/retry` 人工重试失败的 run，或重新发起发布。
- 同样地，重启期间不保证发布日志完整（日志按 run 分片落库，已写入的部分仍在）。
- 目标机上的 `current` 符号链接切换是原子的，因此**重启期间不会出现半成品目录**；已完成服务器的发布结果不受影响。

### `bin/leadstack-devssh` 是什么

本机通常没有 sshd，而 M1 的 SSH 免安装接入（agentless）必须有一个可控的 SSH 端点才能验收，
所以有了这个 **DEV-ONLY** 测试服务（见 `docs/ARCHITECTURE.md` 8.4）：固定账号密码 + 可选公钥认证，
把命令交给本机真实 `sh -c` 执行，启动横幅会打印 `DEV-ONLY` 警告。

它只由 `make devssh` 构建到 `bin/`：**不进 `dist/`、不进 goreleaser 归档、不进 Docker 镜像**。
`make cross` 末尾会断言 `dist/` 中没有 `*devssh*` 产物；`.goreleaser.yaml` 的 builds 是显式白名单，
不会包含 `cmd/leadstack-devssh`。

## 卸载

```sh
# agent（默认路径）
curl -fsSL http://<控制端>:8080/install.sh | sh -s -- --uninstall
# 或本地脚本
sh deploy/install.sh --uninstall

# 控制端
sudo systemctl disable --now leadstack
sudo rm -f /etc/systemd/system/leadstack.service /usr/local/bin/leadstack
sudo systemctl daemon-reload
```
