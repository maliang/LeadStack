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

# 3) 一键跑七套端到端验收（基础 → 纳管 → 检测 → 告警 → 部署 → backup → ops，日志落在 .e2e-logs/）
make test-e2e   # 等价于 make e2e
```

`make test-e2e` 的行为：

- 按序执行 `scripts/verify_bootstrap.sh` → `verify_servers.sh` → `verify_checks.sh` → `verify_alerts.sh` → `verify_deploy.sh` → `verify_backup.sh` → `verify_ops.sh`（脚本由 Lead 维护）；
- 每个脚本的完整输出（含失败时打印的控制端/agent 日志尾部）tee 到 `.e2e-logs/verify_bootstrap.log` … `verify_deploy.log`、`verify_backup.log`、`verify_ops.log`；
- `TMPDIR` 被指向 `.e2e-logs/`，脚本异常中止时残留的 `leadstack*.log` 也留在那里，便于取证；
- 某个脚本**不存在**时只打印提示并跳过（不算失败），脚本落地后自动纳入，无需改 Makefile；
- 已存在的脚本一旦失败立即停止（不再跑后续脚本）并让 `make` 失败 —— 先用 `[1/7]`…`[7/7]` 定位到哪一档挂了；
- 脚本按 `PORT` 环境变量选端口（`verify_bootstrap` 默认 18777、`verify_servers` 默认 18788，检测/告警/部署/backup/ops 见各自脚本），
  与日常 `make run` 的 8080 不冲突；同时跑多套时用 `PORT=18801 make test-e2e` 之类错开。

### 七个验收脚本分别验什么、大概多久

| 脚本 | 覆盖范围 | 典型耗时（本机 纳管 Max） |
|---|---|---|
| `verify_bootstrap.sh` | 交付命令产物、启动+登录、401/200、接入令牌与安装命令、agent 上线 → offline | **约 80–100 秒**（含 45 秒离线判定等待，是主要耗时） |
| `verify_servers.sh` | SSH 凭证（不泄露）、devssh 连通性、host_info、agentless 采集、指标历史、批量操作、分组过滤、409 保护、agent host_info | **约 60–90 秒**（含 3 秒 SSH 采集间隔下等指标落库） |
| `verify_checks.sh` | 检测项（tcp/http/process）、立即执行与调度结果、两种通道（agent/ssh）执行、大跨度降采样、校验鉴权 | **约 90–150 秒**（含 60 秒调度结果累积） |
| `verify_alerts.sh` | 告警渠道（webhook 真实收到通知、密钥不回显）、check/metric 规则触发、ack/resolved、去重与静默、`/alerts/summary` | **约 100–180 秒**（含 `for_duration` 与周期评估等待，脚本会把评估间隔压到几秒） |
| `verify_deploy.sh` | 应用/模板/制品、真实发布到 devssh 并校验产物落盘与 sha256、日志增量拉取、失败不改 `current`、回滚、并发批量、模板安全、重试、401/409 | **约 120–200 秒**（含发布轮询与回滚各一轮；SSH 通道由 devssh 承担） |
| `verify_backup.sh` | 归档格式/加密（搜不到明文与主密钥）、列表/下载/校验、篡改与错误口令、CLI 离线恢复 + `pre-restore`、API 暂存恢复需重启、定时备份与保留策略、401/路径穿越 | **约 150–240 秒**（含两轮重启 + 定时备份等待若干轮；会自建数据目录，不碰 `./data`） |
| `verify_ops.sh` | 审计/RBAC/API Token/分页/批量导入、Web 终端、文件上传下载、定时任务、日志/进程/端口、Agent 升级、前端 i18n 与主题 | **约 200–300 秒**（终端/文件/升级都走 devssh 真连接；前端用例耗时取决于页面加载） |

`make test-e2e` 全绿时总计约 **12–18 分钟**（脚本缺失会被跳过，相应更快）。
CI 上 基础/纳管/检测/告警 为阻塞项，部署、备份与运维验收在稳定前为 `continue-on-error`；任一项失败都会把 `.e2e-logs/` 作为 artifact 上传。

单独跑某一个脚本：

```sh
PORT=19001 bash scripts/verify_bootstrap.sh
PORT=19002 bash scripts/verify_servers.sh
PORT=19003 bash scripts/verify_checks.sh
PORT=19004 bash scripts/verify_alerts.sh
PORT=19005 bash scripts/verify_deploy.sh
PORT=19006 bash scripts/verify_backup.sh
PORT=19007 bash scripts/verify_ops.sh
```

### 告警 告警相关环境变量

| 变量 | 作用 | 备注 |
|---|---|---|
| `LEADSTACK_ALERT_EVAL_INTERVAL` | 告警规则（`source=metric`）的周期评估间隔 | 默认值见 `internal/config`；`verify_alerts.sh` 会把它压到几秒以便在验收窗口内看到 `firing`。值是 Go duration 形式（如 `5s`、`1m`）。 |
| `LEADSTACK_CHECK_INTERVAL_MIN` | 检测项调度的最小间隔（检测 引入） | 默认值见 `internal/config`；验收脚本同样会调小。 |

生产建议：评估间隔保持默认（分钟级）即可，调太小会显著增加 SQLite 写入与 webhook 调用频率。

## 部署：发布与部署目录约定

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

以上默认值以 `internal/config` 的实现为准（部署 收敛中）。

容器场景下部署根目录的可选挂载示例见 [docker-compose.yml](docker-compose.yml)（`leadstack-deploy` 卷，默认注释掉）与 [Dockerfile](../Dockerfile) 末尾说明：
只有「控制端自己也是部署目标」时才需要挂载；纯控制端 + 远程 agent/SSH 的拓扑不需要。

### 部署 已知限制

- **控制端重启后，正在运行的发布不会自动恢复。** `release_run` 的状态机推进在控制端进程内完成；进程重启后仍处于 `pending`/`running` 的 run 会停在原状态（不会自动续跑，也不会被标记失败）。处理方式：用 `POST /releases/{id}/retry` 人工重试失败的 run，或重新发起发布。
- 同样地，重启期间不保证发布日志完整（日志按 run 分片落库，已写入的部分仍在）。
- 目标机上的 `current` 符号链接切换是原子的，因此**重启期间不会出现半成品目录**；已完成服务器的发布结果不受影响。

## 备份与恢复

> ## ⚠️ 最重要的一条：`backup.pass` 是解密备份的**唯一钥匙**
>
> - 备份归档（`.lsbak`）用 **AES-256-GCM + scrypt** 加密，钥匙来自 `backup.pass`
>   （或 `LEADSTACK_BACKUP_PASSPHRASE`）。**没有它，备份永久无法解密，任何人都救不回来。**
> - `backup.pass` **绝不能**和备份文件放在一起：默认它就在 `<data_dir>/` 里、与 `backups/` 同盘——
>   磁盘整盘丢失/勒索加密时两者会一起没。请把它单独拷到别处保管（另一台机器、密码管理器、
>   离线介质），并且**只给必要的人**。
> - 它**不会**被打进备份归档（这是有意设计：备份被拿走也解不开）、也不会进镜像/仓库。
> - 恢复时口令错了会明确报「口令错误」，不会出现「恢复成功但数据为空」。
> - 丢失 `backup.pass` 的唯一出路：拿一份**已知口令**的旧备份重来，或放弃这些备份。

### 三种日常备份方式

| 方式 | 做法 | 适用 |
|---|---|---|
| **UI 一键** | 控制台「备份与恢复」页 → 立即备份（可选包含制品） | 临改动前手动留档 |
| **定时备份** | `GET/PATCH /api/v1/backup-settings` 打开 `enabled`，设 `interval`（默认 `24h`）与 `retention`（默认 7 份）；也可用环境变量 `LEADSTACK_BACKUP_INTERVAL` / `LEADSTACK_BACKUP_RETENTION` / `LEADSTACK_BACKUP_INCLUDE_ARTIFACTS` | 生产常态 |
| **CLI** | `leadstack backup --data-dir ./data --out /tmp/x.lsbak [--with-artifacts] [--passphrase-file p]`；`leadstack backup verify --file /tmp/x.lsbak` | 脚本化 / 异地备份 |

备份是**在线**的：用 SQLite `VACUUM INTO` 做一致快照，不停止服务，也不需要停机。

### 备份里有什么、放在哪

- 归档默认落在 `<data_dir>/backups/leadstack-<YYYYMMDD-HHMMSS>.lsbak`，旁边有同名 `.json` 边车（列表元数据）；
- 内容：`manifest.json`（首个条目）、`leadstack.db`（一致快照）、`master.key`（凭证主密钥）、
  可选 `known_hosts`、`artifacts/<id>`（仅 `--with-artifacts` / `include_artifacts=true` 时）；
- **绝不进归档**：`backup.pass`、`backups/` 目录、`*.restore-pending/`、`*.db-wal` / `*.db-shm`；
- 归档文件本身以 ASCII `LSBAK1` 开头；除 header 外全是密文，直接 `strings` 搜不到 SQLite 头、
  私钥、口令或 `master.key` 内容；
- `master.key` 在归档里很关键：没有它，恢复后所有凭证（SSH 密码/私钥）都解不开。

### 离线恢复（CLI，**服务必须已停止**）

```sh
# 1) 停服务（必须！否则恢复会与正在写的进程打架）
#    宿主机 systemd：
sudo systemctl stop leadstack
#    Docker：
docker compose -f deploy/docker-compose.yml stop leadstack

# 2) 恢复（会先校验；口令默认从 <data_dir>/backup.pass 读，读不到可用 --passphrase-file）
./bin/leadstack restore --file /tmp/x.lsbak --data-dir /var/lib/leadstack/data

# 3) 启动
sudo systemctl start leadstack
docker compose -f deploy/docker-compose.yml start leadstack
```

恢复的行为（见 `docs/ARCHITECTURE.md` 30–32 节）：

- 先解密 + 校验 manifest/文件哈希/DB 完整性，任何异常直接失败，**不会破坏现有数据**；
- 校验通过后，把现有的受管文件**移动**到 `<data_dir>.pre-restore-<ts>/`（目录级 move，不复制大文件），
  再放置恢复出来的文件 —— **恢复前的旧数据就保存在这个 `pre-restore` 目录里**，确认无误后手工删即可；
- 来自「更新版本」的备份（`schema_version` 高于当前程序）会被拒绝（`ErrSchemaTooNew`）；
- 恢复完成后**必须重新启动控制端**才能看到数据。

### API 暂存恢复（不停机上传，重启生效）

`POST /api/v1/backups/restore`（multipart 上传归档）→ 校验 → 暂存到 `<data_dir>/.restore-pending/`
→ 返回 `202 {"restart_required": true}`。**下次启动**时，控制端会在打开数据库**之前**应用这份暂存恢复
（同样把旧数据移到 `<data_dir>.pre-restore-<ts>/`），并在日志里打印醒目提示。
适合「远程上传、约个时间窗口重启」的场景；真正的数据切换仍需一次重启。

### Docker 场景：怎么备份 `/data` 卷

`/data` 卷里同时有：SQLite 库、上传制品（`/data/artifacts`）、备份归档（`/data/backups`）、解密口令（`/data/backup.pass`）。

```sh
# 1) 用容器内 CLI 生成加密备份（推荐：一致性由服务保证，不需要停容器）
docker compose -f deploy/docker-compose.yml exec leadstack \
  /usr/local/bin/leadstack backup --data-dir /data --out /data/backups/manual.lsbak

# 2) 把归档拷出到宿主机
docker compose -f deploy/docker-compose.yml cp leadstack:/data/backups/manual.lsbak ./manual.lsbak

# 3) ⚠ 单独拷出解密口令，与上面的 .lsbak 分开保管（不要放同一个目录/同一块盘）
docker compose -f deploy/docker-compose.yml cp leadstack:/data/backup.pass ./backup.pass

# 4) 可选：整个 /data 卷打包（停机窗口内做，或用①的在线备份代替）
docker compose -f deploy/docker-compose.yml stop leadstack
docker run --rm -v leadstack_leadstack-data:/data -v "$PWD":/out alpine \
  tar czf /out/leadstack-data-$(date +%Y%m%d).tgz -C /data .
docker compose -f deploy/docker-compose.yml start leadstack
```

> 卷级 tar 会把 `backup.pass` 一起打进去——如果你要「备份与钥匙分离」，请把 `backup.pass`
> 从 tar 里去掉或单独保管，否则打包件被拿走等于备份被解出。

Docker 下的离线恢复：

```sh
docker compose -f deploy/docker-compose.yml stop leadstack
docker run --rm -v leadstack_leadstack-data:/data -v "$PWD":/in \
  --entrypoint /usr/local/bin/leadstack leadstack:local \
  restore --file /in/manual.lsbak --data-dir /data
docker compose -f deploy/docker-compose.yml start leadstack
```

### 校验与排错

```sh
# 只校验不恢复：解密 + header/manifest/文件哈希/DB 完整性
./bin/leadstack backup verify --file /tmp/x.lsbak
```

| 现象 | 含义 | 处理 |
|---|---|---|
| `backup: 口令错误` | 口令不对（或 `backup.pass` 不是当初那把） | 换正确的口令文件；**不要**反复试写，先确认钥匙来源 |
| `backup: 归档损坏或格式不符` | 文件被截断/篡改/不是 LSBAK1 | 换一份备份；确认传输完整（比对 sha256） |
| `backup: 备份来自更新的版本` | 备份的 `schema_version` 高于当前程序 | 升级控制端到同版本或更新再恢复 |
| 恢复后凭证全失效 | 归档里缺 `master.key` | 说明备份不完整，换一份 |

### 备份相关环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `LEADSTACK_BACKUP_INTERVAL` | `24h` | 定时备份间隔（Go duration）；`0s` 表示只手动备份 |
| `LEADSTACK_BACKUP_RETENTION` | `7` | 保留份数，超出按创建时间删最旧 |
| `LEADSTACK_BACKUP_INCLUDE_ARTIFACTS` | `false` | 备份是否含上传制品（体积大；不含时恢复后需重新上传制品） |
| `LEADSTACK_BACKUP_PASSPHRASE` | 空 | 口令（优先于 `<data_dir>/backup.pass`）。容器部署建议用 secret/env_file 注入，别写进 compose 明文 |

默认值以 `internal/config` 实现为准。

## 运维能力部署注意事项（运维与协作）

这一批能力（Web 终端、文件管理、定时任务、日志/进程/端口、Agent 升级、审计、用户与 RBAC）都会在**目标机**上执行命令，
部署前请确认目标机的前置条件。

### 新增环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `LEADSTACK_TERMINAL_ENABLED` | `false` | Web SSH 终端总开关。**默认关闭**；关闭时终端端点直接 403。生产建议按需临时打开，用完再关。 |
| `LEADSTACK_CRON_TICK` | `1m` | 定时任务引擎的检查间隔（Go duration）。默认每分钟检查一次；验收脚本会压到 `1s`。调太小会频繁扫描任务表。 |
| `LEADSTACK_AUDIT_RETENTION_DAYS` | `180` | 审计日志保留天数，随后台清扫循环清理。审计量约等于「写操作次数」，可按机房规模估算磁盘。 |

### Web 终端

- 目标机必须有**可用的 shell**：ssh 模式要求 sshd 允许 PTY 且该用户有登录 shell（`/bin/sh`、`/bin/bash`、`/bin/zsh` 均可）；
  agent 模式由 agent 用 PTY 起 shell，要求 agent 进程有权限 fork（agent 默认以 root 运行）。
- 精简容器/最小镜像常见问题：没有 `/bin/sh` → 终端直接失败；没有 `sftp-server` → 文件管理失败（见下）。
- 会话用**一次性 ticket**（60 秒有效）换 WS，避免把 JWT 放进 URL/日志；30 分钟无操作自动断开。
- 审计记录 `terminal.open` / `terminal.close`（含 server_id、操作者、时长）。

### 文件上传/下载

- ssh 模式走 **SFTP**：目标机需要 `sftp-server`（OpenSSH 默认自带；Alpine 等精简镜像可能缺失，缺失时改用 agent 模式）。
- agent 模式走 `job.exec` + base64 分块，**单文件上限 8 MiB**，超出直接报错。
- 路径校验拒绝 `..` 穿越出用户指定根；上传/下载/删除都会写审计。

### 定时任务（cron）

- 5 字段 cron（分 时 日 月 周），支持 `*` `,` `-` `*/n`；解析失败返回 400 并给出人话错误。
- 命令在目标机上以**当前用户权限**执行：agent 模式默认是 root，SSH 模式是凭证里的用户。
  也就是说「能建定时任务 = 能在目标机执行任意命令」，请按 RBAC 收紧（operator 及以上）。
- `scheduled_task_runs` 保留 30 天；单任务并发默认 4。

### 日志 / 进程 / 端口视图

- 依赖目标机有 `tail`、`ps`、`ss`（macOS 无 `ss`，回退 `lsof -i`）。精简容器缺这些命令时该视图会返回空/报错。
- 单次输出上限 256 KB；`grep` 在服务端过滤，不会把全量日志拉回控制端。

### Agent 自升级

- 目标程序需要能**写自身所在目录**（A/B 双文件替换，避免覆盖正在运行的可执行文件）；失败保留旧版本并回报原因。
- 下载 URL 必须目标机可达且 sha256 匹配；批量升级按「当前版本筛选」，请先小范围灰度。
- 升级后由 systemd / launchd 负责重启（模板里的 `Restart=always` / `KeepAlive` 已配好）。

### 审计、用户与 RBAC

- 角色三档：`admin` / `operator` / `viewer`。viewer 执行写操作返回 403；operator 不能管理用户；**禁止删除最后一个 admin**。
- 审计记录写操作的 `action` / `actor` / `IP` / 状态码，响应中**不含**明文密码或密钥；保留期见 `LEADSTACK_AUDIT_RETENTION_DAYS`。
- API Token（`lsk_...`）：明文**只在创建响应返回一次**，库里只存 sha256；撤销后立即 401，`last_used_at` 会更新。
- 批量导入：每行 `host[:port]`，`#` 开头与空行忽略，按 `host:port` 去重，单次上限 500 行。
- 分页：显式传 `limit`（1–500）时返回 `page.{next_cursor,has_more}`；不传 `limit` 仍是数组（兼容旧前端与验收脚本）。

### `bin/leadstack-devssh` 是什么

本机通常没有 sshd，而 纳管 的 SSH 免安装接入（agentless）必须有一个可控的 SSH 端点才能验收，
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
