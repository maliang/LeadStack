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

# 3) 一键跑四套端到端验收（M0 → M1 → M2 → M3，日志落在 .e2e-logs/）
make test-e2e   # 等价于 make e2e
```

`make test-e2e` 的行为：

- 按序执行 `scripts/verify_m0.sh` → `verify_m1.sh` → `verify_m2.sh` → `verify_m3.sh`（脚本由 Lead 维护）；
- 每个脚本的完整输出（含失败时打印的控制端/agent 日志尾部）tee 到 `.e2e-logs/verify_m0.log` … `verify_m3.log`；
- `TMPDIR` 被指向 `.e2e-logs/`，脚本异常中止时残留的 `leadstack*.log` 也留在那里，便于取证；
- 某个脚本**不存在**时只打印提示并跳过（不算失败），脚本落地后自动纳入，无需改 Makefile；
- 已存在的脚本一旦失败立即停止（不再跑后续脚本）并让 `make` 失败 —— 先用 `[1/4]`…`[4/4]` 定位到哪一档挂了；
- 脚本按 `PORT` 环境变量选端口（`verify_m0` 默认 18777、`verify_m1` 默认 18788，M2/M3 见各自脚本），
  与日常 `make run` 的 8080 不冲突；同时跑多套时用 `PORT=18801 make test-e2e` 之类错开。

### 四个验收脚本分别验什么、大概多久

| 脚本 | 覆盖范围 | 典型耗时（本机 M1 Max） |
|---|---|---|
| `verify_m0.sh` | 交付命令产物、启动+登录、401/200、接入令牌与安装命令、agent 上线 → offline | **约 80–100 秒**（含 45 秒离线判定等待，是主要耗时） |
| `verify_m1.sh` | SSH 凭证（不泄露）、devssh 连通性、host_info、agentless 采集、指标历史、批量操作、分组过滤、409 保护、agent host_info | **约 60–90 秒**（含 3 秒 SSH 采集间隔下等指标落库） |
| `verify_m2.sh` | 检测项（tcp/http/process）、立即执行与调度结果、两种通道（agent/ssh）执行、大跨度降采样、校验鉴权 | **约 90–150 秒**（含 60 秒调度结果累积） |
| `verify_m3.sh` | 告警渠道（webhook 真实收到通知、密钥不回显）、check/metric 规则触发、ack/resolved、去重与静默、`/alerts/summary` | **约 100–180 秒**（含 `for_duration` 与周期评估等待，脚本会把评估间隔压到几秒） |

`make test-e2e` 全绿时总计约 **6–9 分钟**（脚本缺失会被跳过，相应更快）。
CI 上 M0/M1/M2 为阻塞项，M3 在稳定前为 `continue-on-error`；任一项失败都会把 `.e2e-logs/` 作为 artifact 上传。

单独跑某一个脚本：

```sh
PORT=19001 bash scripts/verify_m0.sh
PORT=19002 bash scripts/verify_m1.sh
PORT=19003 bash scripts/verify_m2.sh
PORT=19004 bash scripts/verify_m3.sh
```

### M3 告警相关环境变量

| 变量 | 作用 | 备注 |
|---|---|---|
| `LEADSTACK_ALERT_EVAL_INTERVAL` | 告警规则（`source=metric`）的周期评估间隔 | 默认值见 `internal/config`；`verify_m3.sh` 会把它压到几秒以便在验收窗口内看到 `firing`。值是 Go duration 形式（如 `5s`、`1m`）。 |
| `LEADSTACK_CHECK_INTERVAL_MIN` | 检测项调度的最小间隔（M2 引入） | 默认值见 `internal/config`；验收脚本同样会调小。 |

生产建议：评估间隔保持默认（分钟级）即可，调太小会显著增加 SQLite 写入与 webhook 调用频率。

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
