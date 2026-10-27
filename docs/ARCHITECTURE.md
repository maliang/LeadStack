# LeadStack 架构与接口契约 v1（已冻结）

> 本文件是并行开发的唯一事实来源。**任何改动必须先改本文件再改代码。**
> 模块路径：`github.com/maliang/leadstack`

---

## 1. 目录与所有权

```
LeadStack-领栈/
├── cmd/
│   ├── leadstack/            控制端入口            [backend]
│   └── leadstack-agent/      节点 agent 入口        [agent]
├── internal/
│   ├── config/               配置加载               [backend]
│   ├── store/                SQLite + 迁移 + 查询   [backend]
│   ├── api/                  HTTP/WS 路由与处理器   [backend]
│   ├── auth/                 密码哈希/JWT/RBAC     [backend]
│   ├── webui/                前端产物 embed         [backend]
│   └── agent/                agent 运行时           [agent]
├── pkg/protocol/             消息定义（两侧共享）    [lead]
├── web/                      React SPA              [frontend]
├── deploy/                   install.sh / systemd / compose [devops]
├── docs/                     契约与手册             [lead]
├── go.mod / go.sum           依赖清单               [lead]
└── Makefile · .goreleaser.yaml · .github/          [devops]
```

**写文件纪律**：只写自己方括号内的范围。`go.mod`/`go.sum`/`pkg/protocol` 只有 lead 能改；需要新依赖时**报告给 lead**，不要自己 `go get`、不要 `go mod tidy`。

---

## 2. 运行时形态

- 控制端单进程，默认监听 `127.0.0.1:8080`，同时提供 **REST API + Web UI + Agent WebSocket**（同一端口，免额外开洞）。
- Agent 主动外连 `ws://<control>/agent/ws`，无需入站端口。
- 所有持久化在单个 SQLite 文件（默认 `./data/leadstack.db`，WAL 模式，**单写连接**）。

---

## 3. REST API（前缀 `/api/v1`）

统一响应：成功 `{"data": ...}`；失败 `{"error":{"code":"...","message":"..."}}`。
鉴权：除 `/auth/login` 与 `/healthz` 外，均需 `Authorization: Bearer <jwt>`。

| 方法 | 路径 | 说明 | 主要字段 |
|---|---|---|---|
| GET | `/healthz` | 存活探针（无鉴权） | `{status,version,uptime_s}`（**同样带 `data` 信封**） |
| POST | `/api/v1/auth/login` | 登录 | req `{username,password}` → `{token,user}` |
| POST | `/api/v1/auth/logout` | 登出（前端清 token） | — |
| GET | `/api/v1/auth/me` | 当前用户 | `{id,username,role}` |
| GET | `/api/v1/system/info` | 控制端信息 | `{version,uptime_s,server_count,online_count}` |
| GET | `/api/v1/servers` | 服务器列表 | `[{id,name,host,port,mode,status,last_seen_at,agent_version,os,arch,tags}]` |
| POST | `/api/v1/servers` | 新增服务器 | req `{name,host,port,mode}` |
| GET | `/api/v1/servers/{id}` | 详情 | 同上 + `metrics_summary` |
| PATCH | `/api/v1/servers/{id}` | 改名/标签 | `{name?,tags?}` |
| DELETE | `/api/v1/servers/{id}` | 删除 | — |
| GET | `/api/v1/enroll-tokens` | 接入令牌列表 | `[{id,note,expires_at,used_at}]` |
| POST | `/api/v1/enroll-tokens` | 新建令牌 | req `{note,ttl_s}` → `{id,token,expires_at}` |
| POST | `/api/v1/agent/install-script` | 生成一键安装命令 | req `{token,server_url}` → `{command}` |

**状态枚举**：`server.status ∈ {online, offline, unknown}`（由 `last_seen_at` 推导：≤45s online，否则 offline；从未连接为 unknown）。
**错误码**：`unauthorized` `forbidden` `not_found` `conflict` `invalid_argument` `internal`。

### 3.1 `metrics_summary` 字段定义（冻结）

服务器列表/详情返回的 `metrics_summary` 是**以协议指标名为 key 的 `map[string]float64`**，值为该指标最新一次采样：

| key | 单位 | 来源 |
|---|---|---|
| `cpu.pct` | 百分比 0-100 | 心跳 + 指标上报 |
| `mem.used_pct` | 百分比 0-100 | 心跳 + 指标上报 |
| `mem.used_bytes` | 字节 | 指标上报 |
| `disk.used.pct` | 百分比 0-100 | 心跳 |
| `load.1` | 1 分钟平均负载 | 心跳 |
| `uptime_s` | 秒（**可选**，后端可暂不返回） | 心跳 |

- key 一律使用 `pkg/protocol` 中的常量（点分层级），**不使用下划线别名**；未在协议中登记的指标不得落库。
- 前端对缺失 key 显示「—」，不得报错；`uptime_s` 缺失属正常情况。
- 心跳上报的 `cpu_pct`/`mem_used_pct`/`disk_used_pct`/`load1` 由服务端**归一化**为上述 key 后存储。

---

## 4. Agent WebSocket 协议（`/agent/ws`）

握手：`Authorization: Bearer <enroll-token>`（HTTP Header，基础 用；纳管 升级为 mTLS 客户端证书）。
连接建立后服务端回 `hello_ack`，此后双向收发。所有帧为 **JSON 文本**，信封固定：

```json
{ "type": "heartbeat", "id": "uuid", "ts": 1710000000000, "payload": { } }
```

### 4.1 agent → 控制端

| type | payload |
|---|---|
| `hello` | `{hostname, os, arch, kernel, agent_version, boot_time}` |
| `heartbeat` | `{cpu_pct, mem_used_pct, disk_used_pct, load1, uptime_s}` |
| `metrics` | `{samples:[{metric:"cpu.pct", value:12.3, ts:171...}, ...]}` |
| `job.ack` | `{run_id}` |
| `job.log` | `{run_id, seq, stream:"stdout\|stderr", chunk}` |
| `job.done` | `{run_id, exit_code, error?}` |
| `term.data` | `{session_id, b64}` |

指标命名（点分层级，固定集合，基础 只做前 5 个）：`cpu.pct`、`mem.used_pct`、`mem.used_bytes`、`load.1`、`disk.used_pct`、`net.rx_bytes`、`net.tx_bytes`、`proc.count`。

### 4.2 控制端 → agent

| type | payload |
|---|---|
| `hello_ack` | `{server_id, heartbeat_interval_s:20, metrics_interval_s:15}` |
| `config.push` | `{heartbeat_interval_s?, metrics_interval_s?}` |
| `job.exec` | `{run_id, kind:"command", command, timeout_s}` |
| `term.open` / `term.resize` / `term.close` | `{session_id, cols, rows, shell?}` |

### 4.3 行为约定

- 心跳 **20s** 一次；服务端 `>45s` 未收到即 UI 标 offline 并记事件。
- 重连指数退避（1s→2s→4s…上限 60s，带 ±20% 抖动）；重连成功重新 `hello`。
- 指标默认 **15s** 上报一次，批量发送；断线期间本地环形缓冲保留 15 分钟，重连后补传（服务端按 `(server_id,metric,ts)` 去重）。
- 未识别的 `type` 必须忽略，不得断开连接（向前兼容）。

---

## 5. 数据库 schema v1（初始迁移 `0001_init.sql`）

```sql
schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
users(id TEXT PK, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL,
      role TEXT NOT NULL DEFAULT 'admin', created_at INTEGER NOT NULL);
servers(id TEXT PK, name TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 22,
        mode TEXT NOT NULL DEFAULT 'agent', tags TEXT NOT NULL DEFAULT '[]',
        status TEXT NOT NULL DEFAULT 'unknown', last_seen_at INTEGER,
        agent_version TEXT, os TEXT, arch TEXT, kernel TEXT, created_at INTEGER NOT NULL);
enroll_tokens(id TEXT PK, token_hash TEXT UNIQUE NOT NULL, note TEXT,
              expires_at INTEGER NOT NULL, used_at INTEGER, server_id TEXT);
metrics_samples(server_id TEXT NOT NULL, metric TEXT NOT NULL, ts INTEGER NOT NULL,
                value REAL NOT NULL, PRIMARY KEY(server_id, metric, ts)) WITHOUT ROWID;
events(id TEXT PK, server_id TEXT, kind TEXT NOT NULL, message TEXT, ts INTEGER NOT NULL);
settings(key TEXT PK, value TEXT NOT NULL);
```

ID 用 UUID v4 字符串；时间戳统一 **Unix 毫秒整数**；迁移用内嵌 SQL 文件、按 version 顺序执行、单事务。

---

## 6. 基础能力验收标准（必须全部可演示）

1. `make build` 在无网络环境下产出 `bin/leadstack` 与 `bin/leadstack-agent` 两个二进制。
2. `./bin/leadstack` 启动后打印初始管理员密码，浏览器打开 `http://127.0.0.1:8080` 出现登录页，登录成功进入控制台。
3. 控制台「添加服务器」→ 生成接入令牌与一行安装命令。
4. 本机运行 `./bin/leadstack-agent --server http://127.0.0.1:8080 --token <token>`，3 秒内该服务器变为 **online**，页面显示主机名/OS/CPU/内存/负载。
5. 杀掉 agent 进程，45 秒内页面自动变 **offline**（前端 10s 轮询即可，不必 WebSocket 推送）。
6. `curl /healthz` 返回 200；未带 token 访问 `/api/v1/servers` 返回 401。

---

## 7. 协作口径

- 提交前自测：`make build` 通过 + 自己负责的接口能被 `curl` 打通。
- 遇到契约不明处：**先按本文件实现，把疑问报给 lead**，不要私自改契约。
- 完成后向 lead 汇报：改了哪些文件、如何验证、遗留问题。

---

# 服务器纳管契约

> 纳管 在 基础 基础上补齐：**SSH 凭证管理 · 连通性测试 · 主机信息 · Agentless 采集 · 分组标签与批量操作 · 指标历史与降采样**。
> 本节所有签名与字段均已冻结，实现方不得自行改名；确有必要请先报告 lead。

## 8. Go 包 API 契约（`sshmgr` 交付，`backend` 消费）

> 这三组包**只依赖标准库 + `golang.org/x/crypto` + `pkg/protocol`**，不得 import `internal/store` 或 `internal/api`（保证可独立单测）。

### 8.1 `internal/credential` —— 密钥托管与加解密

```go
type Kind string // "password" | "key"

type Secret struct {
    Kind       Kind   `json:"kind"`
    Username   string `json:"username"`
    Password   string `json:"password,omitempty"`
    PrivateKey []byte `json:"private_key,omitempty"` // PEM 原文
    Passphrase string `json:"passphrase,omitempty"`
}

// LoadOrCreateMasterKey 读取 32 字节主密钥；文件不存在则安全随机构造并以 0600 落盘。
func LoadOrCreateMasterKey(path string) ([]byte, error)

type Cipher struct{ /* ... */ }

func NewCipher(masterKey []byte) (*Cipher, error)
// Encrypt 输出 nonce || AES-256-GCM(ciphertext)，可直接存 BLOB。
func (c *Cipher) Encrypt(plain []byte) ([]byte, error)
func (c *Cipher) Decrypt(blob []byte) ([]byte, error)

func Marshal(s Secret) ([]byte, error)
func Unmarshal(b []byte) (Secret, error)
func Validate(s Secret) error
// Fingerprint 返回 "SHA256:<base64>"，仅对私钥有意义；password 类型返回空串。
func Fingerprint(privateKeyPEM []byte) (string, error)
```

### 8.2 `internal/sshx` —— SSH 客户端

```go
// Runner 是可执行远程命令的最小抽象；hostinfo 只依赖它，便于测试替身。
type Runner interface {
    Run(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error)
}

type Auth struct {
    Username   string
    Password   string
    PrivateKey []byte // PEM
    Passphrase string
}

type HostKeyPolicy string // "insecure" | "accept-new" | "strict"

type DialOptions struct {
    Host           string
    Port           int
    Auth           Auth
    Timeout        time.Duration   // 默认 10s
    HostKeyPolicy  HostKeyPolicy   // 默认 "accept-new"
    KnownHostsPath string          // accept-new / strict 时使用
    OnNewHostKey   func(host, fingerprint string) // accept-new 首次见到主机密钥时回调（落库）
}

type Client struct{ /* ... */ }

func Dial(ctx context.Context, o DialOptions) (*Client, error)
func (c *Client) Run(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error)
func (c *Client) ServerVersion() string      // 形如 "SSH-2.0-OpenSSH_9.6"
func (c *Client) HostKeyFingerprint() string // "SHA256:<base64>"
func (c *Client) Close() error
```

### 8.3 `internal/hostinfo` —— 主机信息与 Agentless 指标

```go
type Info struct {
    Hostname       string   `json:"hostname"`
    OS             string   `json:"os"`          // linux | darwin | windows
    OSVersion      string   `json:"os_version"`
    Kernel         string   `json:"kernel"`
    Arch           string   `json:"arch"`
    CPUModel       string   `json:"cpu_model"`
    CPUCores       int      `json:"cpu_cores"`
    MemTotalBytes  uint64   `json:"mem_total_bytes"`
    DiskTotalBytes uint64   `json:"disk_total_bytes"`
    DiskUsedBytes  uint64   `json:"disk_used_bytes"`
    UptimeS        int64    `json:"uptime_s"`
    Timezone       string   `json:"timezone"`
    Docker         bool     `json:"docker"`
    DockerVersion  string   `json:"docker_version,omitempty"`
    IPAddrs        []string `json:"ip_addrs"`
    CollectedAt    int64    `json:"collected_at"` // Unix 毫秒
}

func Collect(ctx context.Context, r sshx.Runner) (Info, error)

// Sampler 持有每台服务器的上一份 CPU 计数，用于算区间占用率（无状态则首帧 CPU 为空）。
type Sampler struct{ /* ... */ }
func NewSampler() *Sampler
// Collect 返回 cpu.pct / mem.used_pct / mem.used_bytes / load.1 / disk.used.pct / uptime_s。
// 单项失败跳过该指标而不是整体报错；serverID 用于隔离各服务器的上一次采样。
func (s *Sampler) Collect(ctx context.Context, serverID string, r sshx.Runner) ([]protocol.Sample, error)
```

### 8.4 `internal/devssh` + `cmd/leadstack-devssh` —— 测试专用 SSH 服务

本机没有 sshd、也没有免密 sudo，SSH 路径必须有一个**可控端点**才能验收。

```go
// package devssh 仅用于开发与测试，不参与发布构建。
type Config struct {
    Addr          string // 如 127.0.0.1:0，传 0 由系统分配
    Username      string // 默认 devuser
    Password      string // 默认 devpass
    AuthorizedKey []byte // 允许的客户端公钥（authorized_keys 行）
    Executor      func(cmd string) (stdout, stderr string, exitCode int) // 默认：本机真实执行 sh -c
}
func Start(cfg Config) (*Server, error)
func (s *Server) Addr() string
func (s *Server) Close() error
```

`cmd/leadstack-devssh` 以固定端口与固定账号启动它，启动横幅必须显式打印 `DEV-ONLY` 警告，供 `scripts/verify_servers.sh` 驱动真实 HTTP API。

## 9. 协议扩展（`pkg/protocol`）

新增一种 agent → 控制端消息，其余不变：

| type | payload |
|---|---|
| `host_info` | 下方 `HostInfo`，agent 上线后立即上报一次，之后每 6 小时一次 |

```go
const TypeHostInfo = "host_info"

type HostInfo struct { // 字段与 hostinfo.Info 一一对应，json tag 相同
    Hostname, OS, OSVersion, Kernel, Arch, CPUModel string
    CPUCores int
    MemTotalBytes, DiskTotalBytes, DiskUsedBytes uint64
    UptimeS int64
    Timezone string
    Docker bool
    DockerVersion string
    IPAddrs []string
    CollectedAt int64
}
```

## 10. REST 新增端点（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/credentials` | 凭证列表，**绝不返回明文或密钥内容**：`{id,name,kind,username,fingerprint,created_at,server_count}` |
| POST | `/credentials` | 创建凭证 `{name,kind,username,password?,private_key?,passphrase?}` → 201 |
| DELETE | `/credentials/{id}` | 被服务器引用时返回 409 `conflict` |
| POST | `/servers/{id}/test-connection` | → `{ok,latency_ms,ssh_banner,host_key_fingerprint,error?}` |
| POST | `/servers/{id}/refresh-host-info` | 通过 SSH 采集并落库 → `host_info` |
| POST | `/servers/batch` | `{ids,action,params?}`，action ∈ `test-connection`/`refresh-host-info`/`add-tags`/`set-group`/`delete` → `{results:[{id,ok,error?}],summary:{ok,failed}}` |
| GET | `/servers/{id}/metrics` | `?from=<ms>&to=<ms>&step=raw\|1m\|1h` → `{series:[{metric,points:[{ts,value}]}],step}` |
| GET | `/groups` | 由 servers 聚合出的分组名与数量（纳管 不建独立表） |

**`POST /servers` 扩展字段**：`credential_id`、`mode`（`agent`/`ssh`）、`group`、`tags`、`host_key_policy`。
**`GET /servers` 扩展**：支持 `?group=&tag=&status=&mode=` 过滤；每项新增 `mode`、`group`、`credential_id`、`last_error`、`host_info`（无则 `null`）。
**agentless 行为**：`mode=ssh` 的服务器由控制端按 `LEADSTACK_SSH_INTERVAL`（默认 30s）采集，连续 3 次失败置 `offline` 并写 `last_error`；成功一次即恢复 `online`。

## 11. 数据库迁移

`0002_credentials.sql`

```sql
CREATE TABLE credentials(
  id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, kind TEXT NOT NULL,
  username TEXT NOT NULL, secret_enc BLOB NOT NULL,
  fingerprint TEXT, created_at INTEGER NOT NULL);
ALTER TABLE servers ADD COLUMN credential_id TEXT;
ALTER TABLE servers ADD COLUMN "group" TEXT;
ALTER TABLE servers ADD COLUMN host_key_policy TEXT NOT NULL DEFAULT 'accept-new';
ALTER TABLE servers ADD COLUMN last_error TEXT;
CREATE TABLE server_host_info(
  server_id TEXT PRIMARY KEY, info_json TEXT NOT NULL, collected_at INTEGER NOT NULL);
```

`0003_metrics_rollup.sql`

```sql
CREATE TABLE metrics_rollup(
  server_id TEXT NOT NULL, metric TEXT NOT NULL, bucket TEXT NOT NULL, ts INTEGER NOT NULL,
  avg REAL NOT NULL, min REAL NOT NULL, max REAL NOT NULL, cnt INTEGER NOT NULL,
  PRIMARY KEY(server_id, metric, bucket, ts)) WITHOUT ROWID;
```

保留策略：原始指标 7 天；`1m` 汇总 30 天；`1h` 汇总 1 年。降采样任务随清扫循环运行，每次最多处理 5000 行。

## 12. 服务器纳管验收标准（`scripts/verify_servers.sh` 一键复现）

1. `make build` 成功；`go vet ./...` 与 `go test ./...` 全绿。
2. 创建 SSH 私钥凭证 → 列表接口响应中**不出现**任何密钥明文（以原始私钥片段做反向断言）。
3. 启动 `leadstack-devssh` → 添加 `mode=ssh` 服务器 → `test-connection` 返回 `ok=true`、SSH banner、主机密钥指纹。
4. `refresh-host-info` 返回 hostname/os/kernel/arch/cpu_cores/mem_total_bytes/disk_total_bytes/uptime_s 且全部非空。
5. 60 秒内出现 agentless 采集的 `cpu.pct`/`mem.used_pct`/`load.1`，状态为 `online`。
6. `GET /servers/{id}/metrics?step=raw` 返回时间序列；构造历史后 `step=1m` 可返回汇总。
7. 批量 `add-tags` + `test-connection` 对 2 台机器返回逐台结果与汇总。
8. `?group=` 过滤生效；agent 模式服务器上报 `host_info` 后详情可见主机信息。
9. 未带 token 访问 `/credentials` 返回 401；被引用的凭证删除返回 409。
10. `scripts/verify_bootstrap.sh` 仍全绿（**纳管 不得回归 基础**）。

---

# 监控与检测契约

> 检测 补齐「**主动检测**」：检测项（端口/HTTP/Ping/进程/脚本）· 调度与结果时间线 · 检测视图 · 大范围指标查询降级。
> 并按计划为 告警 告警准备数据（连续失败计数与状态起止时间）。

## 13. 检测模型与执行位置

| 类型 | Target 形态 | 在哪执行 | 说明 |
|---|---|---|---|
| `tcp` | `host:port` | 控制端直连 | 端口存活；`params.timeout_ms` 可选 |
| `http` | 完整 URL | 控制端直连 | `params.expect_status`（默认 2xx）、`params.keyword`、`params.method`（默认 GET）、`params.insecure`（跳过证书校验） |
| `ping` | `host` | 控制端直连 | ICMP 非特权模式（`icmp.ListenPacket("udp4")`）；**无权限时必须返回 ok=false + message「当前环境不支持非特权 ICMP」**，不得静默成功 |
| `process` | 进程名 | 目标机 | 由控制端生成命令，经 Executor 执行后按进程数判定；`params.min_count`（默认 1）、`params.user` 可选 |
| `script` | 脚本内容 | 目标机 | `params.exit_zero_ok`（默认 true）、`params.expect_stdout` 可选；**脚本内容会进审计日志** |

**Executor 决定目标机执行通道**：agent 模式 → 控制端下发 job（`internal/jobs`）；ssh 模式 → SSH `Run`。两种模式对检测层完全透明。

**检测 已知限制（记录在案）**：`process`/`script` 的命令由**控制端**生成，`process` 分支按控制端 `GOOS` 选择实现（POSIX `ps+awk` / Windows `tasklist`）。因此**跨 OS 纳管**（例如 Linux 控制端纳管 Windows 目标）时 process 检测会走错分支。`Executor` 接口当前不带目标机 OS；告警/部署 若要支持跨 OS 检测，需要在此接口上扩展 OS 字段（属于破坏性变更，届时同步更新本节）。

## 14. Go 包 API 契约（`checkdev` 交付，`backend`/`agentdev` 消费）

### 14.1 `internal/checker` —— 探测与调度（`checkdev` 负责）

```go
type Kind string // "tcp" | "http" | "ping" | "process" | "script"

type Spec struct {
    ID        string         `json:"id"`
    Name      string         `json:"name"`
    Kind      Kind           `json:"kind"`
    Target    string         `json:"target"`
    IntervalS int            `json:"interval_s"` // 最小 10
    TimeoutS  int            `json:"timeout_s"`  // 默认 10
    Params    map[string]any `json:"params,omitempty"`
}

type Result struct {
    OK         bool   `json:"ok"`
    LatencyMS  int64  `json:"latency_ms"`
    StatusCode int    `json:"status_code,omitempty"` // http 专用
    Message    string `json:"message,omitempty"`     // 失败原因 / 摘要
    TS         int64  `json:"ts"`                    // Unix 毫秒
}

// Executor 是「在目标机上执行一条命令」的抽象：agent 模式由 jobs.Dispatcher 实现，
// ssh 模式由 backend 用 sshx.Client 包装。checker 不感知底层通道。
type Executor interface {
    Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error)
}

// Probe 执行一次探测。exec 仅在 Kind 为 process/script 时被使用，其余可为 nil。
// 任何失败都要转成 Result{OK:false, Message:...}，不要返回 error（除 ctx 取消）。
func Probe(ctx context.Context, spec Spec, exec Executor, serverID string) Result

// Store 是调度器对持久化的最小依赖，由 backend 实现（避免 checker import store）。
type Store interface {
    ListEnabledTargets(ctx context.Context) ([]Target, error)
    SaveResult(ctx context.Context, checkID, serverID string, r Result) error
    UpdateState(ctx context.Context, checkID, serverID string, ok bool, ts int64) error
}

type Target struct {
    CheckID  string
    ServerID string
    Mode     string // "agent" | "ssh"
    Spec     Spec
}

type Deps struct {
    Store            Store
    Exec             Executor
    Concurrency      int           // 默认 8
    OnStateChange    func(checkID, serverID string, ok bool, since int64) // 可选，告警 告警挂这里
}

type Scheduler struct{ /* ... */ }
func NewScheduler(d Deps) *Scheduler
func (s *Scheduler) Start(ctx context.Context)   // 阻塞直到 ctx 取消
func (s *Scheduler) Wake()                       // 检测项变更后立即重读目标列表
func (s *Scheduler) RunOnce(ctx context.Context, checkID, serverID string) Result // 手动「立即执行」
```

### 14.2 `internal/jobs` —— 控制端任务下发（`agentdev` 负责）

基础 只做了 agent 侧的 job.exec 接收，**控制端还没有下发能力**，检测 补上。

```go
// Sender 由 backend 的 WS Hub 实现：把一条 job.exec 发给指定服务器。
type Sender interface {
    SendJob(serverID string, envelope []byte) error // envelope 为 protocol.Envelope 的 JSON
    IsOnline(serverID string) bool
}

type Result struct {
    ExitCode int
    Stdout   string
    Stderr   string
}

type Dispatcher struct{ /* ... */ }
func NewDispatcher(s Sender, opts Options) *Dispatcher
// Dispatch 下发 job 并等待 job.done；ctx 超时或服务器离线返回 error。
// 实现 Executor 接口，因此可直接注入 checker.Deps.Exec。
func (d *Dispatcher) Exec(ctx context.Context, serverID, command string, timeoutS int) (stdout, stderr string, exitCode int, err error)
// Handle* 由 backend 在收到 agent 帧时调用，把结果投递给等待中的 Dispatch。
func (d *Dispatcher) HandleAck(serverID, runID string)
func (d *Dispatcher) HandleLog(serverID, runID string, seq int, stream, chunk string)
func (d *Dispatcher) HandleDone(serverID, runID string, exitCode int, errMsg string)
func (d *Dispatcher) FailServer(serverID string, cause error) // 连接断开时唤醒所有等待者
```

## 15. REST 新增端点（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/checks` | 检测项列表，含目标服务器数、最近结果、当前状态（`up`/`down`/`unknown`）、可用率（24h） |
| POST | `/checks` | 创建检测项 → 201；body 见下 |
| GET | `/checks/{id}` | 详情 |
| PATCH | `/checks/{id}` | 改名称/间隔/超时/参数/启用状态（改 interval 后需重排调度） |
| DELETE | `/checks/{id}` | 删除（连带结果与状态） |
| POST | `/checks/{id}/targets` | `{server_ids:[...]}` 覆盖式设置适用服务器 |
| POST | `/checks/{id}/run` | 立即对全部目标执行一次 → `{results:[{server_id,ok,latency_ms,message}]}` |
| GET | `/checks/{id}/results` | `?server_id=&from=&to=&limit=` → 结果时间线（默认最近 100 条） |
| GET | `/checks/summary` | `{total,up,down,unknown,checks:[...]}` 供总览页 |
| GET | `/servers/{id}/checks` | 该服务器适用的检测项 + 最近结果 + 状态 |

**创建 body**：`{name,kind,target,interval_s,timeout_s,params?,server_ids?}`。
**状态口径**：`down` = 最近一次失败且连续失败次数 ≥ `params.fail_threshold`（默认 3）；否则 `up`；无结果为 `unknown`。
**指标查询降级（检测 附带）**：`GET /servers/{id}/metrics` 当 `step=raw` 且时间跨度 > 6 小时时**自动降级**为 `1m` 并在响应 `step` 字段返回实际值（不再返回超大原始序列）。

### 15.1 响应字段（检测 冻结，前端按此实现）

`GET /checks` 每项：
```json
{ "id":"…", "name":"…", "kind":"tcp", "target":"127.0.0.1:22",
  "interval_s":10, "timeout_s":5, "params":{}, "enabled":true,
  "state":"up", "status":"up",            // 同值，state 为准，status 保留兼容
  "target_count":2, "server_count":2, "server_ids":["…","…"],
  "availability_24h":99.5,                // 0-100 百分比
  "results_24h":240,
  "consecutive_fail":0,
  "last_result":{"ts":1710000000000,"ok":true,"latency_ms":3,"message":""},
  "created_at":1710000000000 }
```
`GET /checks/summary` → `{total,up,down,unknown,checks:[…]}`。
`POST /checks/{id}/run` → `{results:[{server_id,ok,latency_ms,status_code,message}],summary:{ok,failed}}`，**results 顺序与 GET /checks 的 `server_ids` 一致**。
`GET /checks/{id}/results` 与 `GET /servers/{id}/checks` 均返回**数组**（后者元素同上，但 state/availability_24h/last_result 是**该服务器视角**）。
单元格语义：`status_code` 仅 http 有意义；`message` 失败时必有值、成功时可为空。

## 16. 数据库迁移 `0004_checks.sql`

```sql
CREATE TABLE checks(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL, target TEXT NOT NULL,
  interval_s INTEGER NOT NULL, timeout_s INTEGER NOT NULL DEFAULT 10,
  params_json TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL);
CREATE TABLE check_targets(
  check_id TEXT NOT NULL, server_id TEXT NOT NULL,
  PRIMARY KEY(check_id, server_id)) WITHOUT ROWID;
CREATE TABLE check_results(
  id INTEGER PRIMARY KEY AUTOINCREMENT, check_id TEXT NOT NULL, server_id TEXT NOT NULL,
  ts INTEGER NOT NULL, ok INTEGER NOT NULL, latency_ms INTEGER NOT NULL DEFAULT 0,
  status_code INTEGER, message TEXT);
CREATE INDEX idx_check_results_lookup ON check_results(check_id, server_id, ts);
CREATE TABLE check_state(
  check_id TEXT NOT NULL, server_id TEXT NOT NULL, ok INTEGER NOT NULL,
  consecutive_fail INTEGER NOT NULL DEFAULT 0, since INTEGER NOT NULL,
  last_ts INTEGER NOT NULL, last_message TEXT,
  PRIMARY KEY(check_id, server_id)) WITHOUT ROWID;
```

保留：`check_results` 保留 30 天（随清扫循环清理）。

## 17. 监控检测验收标准（`scripts/verify_checks.sh` 一键复现）

1. `make build` 成功；`go vet ./...` 与 `go test ./...` 全绿；基础/纳管 两个脚本仍全绿。
2. 创建 `tcp` 检测项指向 devssh 端口、`http` 检测项指向控制端自身 `/healthz`，各应用 1 台服务器 → `POST /checks/{id}/run` 返回 `ok=true` 且 `latency_ms` 有值。
3. 创建指向不可达端口的 `tcp` 检测项 → 立即执行 `ok=false` 且有 message；间隔 10s 连续执行后状态变 `down`。
4. `http` 检测项对 `/healthz` 校验 `expect_status=200` 通过；指向不存在路径且 `expect_status=200` → `ok=false`。
5. `process` 检测项（匹配本机真实进程名，如 `leadstack`）经 **agent 模式**服务器（job.exec 下发）执行成功；同一检测项对 **ssh 模式**服务器执行也成功（两种通道都通）。
6. 调度器按间隔产生结果：60 秒内单目标 ≥4 条结果记录。
7. `GET /checks/{id}/results` 返回时间线；`GET /servers/{id}/checks` 返回该机器检测项与状态。
8. 指标大范围查询：`step=raw` 且跨度 > 6h 时响应 `step` 变为 `1m`。
9. 未带 token 访问 `/checks` 返回 401。
10. `scripts/verify_bootstrap.sh` 与 `scripts/verify_servers.sh` 仍全绿（**检测 不得回归**）。

---

# 告警契约

> 告警 补齐「**异常推送到人**」：规则引擎 · 持续时长与去重 · 静默与确认 · 7 类通知渠道 · 告警历史。
> 触发源有三类：检测项状态翻转、服务器上下线、指标阈值。检测 的 `check_state` 与 `Deps.OnStateChange` 就是天然挂载点。

## 18. 告警模型

| 概念 | 约定 |
|---|---|
| `dedup_key` | 同类故障的唯一标识：`check:<checkID>:<serverID>` / `server:<serverID>` / `metric:<ruleID>:<serverID>`。**同一 key 同时只允许一条未解决告警** |
| `for_duration_s` | 从「首次观测到异常」起计时，持续满足该时长才真正 `firing`；期间恢复则不告警 |
| 去重窗口 | 告警已 `firing` 时再次触发：**不新建告警**，只累加 `notify_count` 并更新 `updated_at`；超过 `dedup_window_s` 未通知过才重新推送一次 |
| `silence_until` | 静默期内**仍然建告警与记录事件**，但不发送通知 |
| `acked` | 人工确认，只改变展示与后续通知策略，**不代表恢复** |
| 恢复 | 异常解除 → 状态置 `resolved` + 发送一次恢复通知（与故障通知走同一渠道） |

### 18.1 语义细则（告警 实现后冻结，均为 Lead 裁决）

- **静默只抑制 firing 通知，恢复通知必须穿透静默**。理由：静默的语义是"别再告诉我新故障"，不是"别告诉我已经好了"；恢复是降噪信息而非噪声。
- **多规则命中同一 `dedup_key` 时，严重级别最高者为主规则**（`critical` > `warning` > `info`，同级按 `Rule.ID` 升序）。渠道与去重窗口都跟主规则走。理由：若按 ID 排序，用户同时配了 warning 与 critical 时 critical 可能永远发不出去。
- **pending 计时为纯内存态**：控制端重启后 `for_duration` 从头计时（有意为之，不落库）。
- **`for_duration` 的推进依赖周期 tick**（`LEADSTACK_ALERT_EVAL_INTERVAL`）。因为检测层的 `OnStateChange` 只在**状态翻转**时回调一次，异常持续期间没有第二次事件；引擎在 `EvaluateMetrics` 周期里做 `promoteDuePending`。**该周期调用必须无条件执行**（不得因"无服务器/无样本"提前返回）。
- **`value=0` 是合法值**，不得当作"未提供"跳过。
- **告警事件 `kind` 取值**：`firing` / `resolved` / `notify` / `acked`（引擎与 store 必须一致）。
- **渠道配置脱敏**：密钥类字段（`secret`/`password`/`bot_token`/`token`/`api_key` 等）与 **`url`/`webhook`/`api_base` 中的路径末段与查询参数**（钉钉 `access_token`、Slack/飞书路径 token）都必须脱敏；`PATCH` 渠道为**合并式更新**，密钥字段传空串或含 `***` 的值一律视为"不修改"。

## 19. Go 包 API 契约

### 19.1 `internal/alerting` —— 规则评估与告警状态机（`alertdev` 负责）

**不得 import `internal/store` / `internal/api` / `internal/notify`**（用接口注入，保证可独立单测）。

```go
type Severity string // "critical" | "warning" | "info"
type Source string   // "check" | "server" | "metric"

type Rule struct {
    ID           string
    Name         string
    Enabled      bool
    Source       Source
    CheckIDs     []string // 适用范围；全部为空 = 匹配所有
    ServerIDs    []string
    Groups       []string
    Tags         []string
    Metric       string   // source=metric 时使用，如 "cpu.pct"
    Comparator   string   // ">" ">=" "<" "<="
    Threshold    float64
    ForDurationS int
    Severity     Severity
    DedupWindowS int
    ChannelIDs   []string
    SilenceUntil int64
}

// Event 是一次观测到的状态变化（由 backend 从检测回调/服务器上下线转成）。
type Event struct {
    Source      Source
    ServerID    string
    ServerName  string
    Group       string
    Tags        []string
    CheckID     string
    CheckName   string
    Active      bool    // true=异常发生, false=恢复
    Value       float64
    Message     string
    TS          int64
}

// MetricSample 是周期评估的输入（backend 从最新指标快照构造）。
type MetricSample struct {
    ServerID   string
    ServerName string
    Group      string
    Tags       []string
    Metric     string
    Value      float64
    TS         int64
}

type Alert struct {
    ID, RuleID, DedupKey string
    ServerID, CheckID    string
    Status               string // "firing" | "resolved" | "acked"
    Severity             Severity
    Title, Message       string
    Value                float64
    StartedAt, UpdatedAt int64
    ResolvedAt, AckedAt  int64
    NotifyCount          int
    LastNotifyAt         int64
    SilenceUntil         int64
}

// Store 由 backend 实现。
type Store interface {
    ListRules(ctx context.Context) ([]Rule, error)
    FindActiveAlert(ctx context.Context, dedupKey string) (*Alert, error)
    CreateAlert(ctx context.Context, a Alert) error
    UpdateAlert(ctx context.Context, a Alert) error
    ResolveAlert(ctx context.Context, id string, ts int64) error
    AppendAlertEvent(ctx context.Context, alertID, kind string, ts int64, detail string) error
}

type Deps struct {
    Store Store
    // Notify 由 backend 注入：按渠道发送（内部接 notify.Client）。
    // channels 为规则配置的渠道 ID；recovered 为 true 时表示恢复通知。
    Notify func(ctx context.Context, a Alert, channels []string, recovered bool)
    Now    func() int64 // 可注入便于测试，默认 time.Now().UnixMilli()
}

type Engine struct{ /* ... */ }
func New(d Deps) *Engine
// HandleEvent 处理一次状态翻转：负责 for_duration 判定、去重、建/销告警、触发通知。
func (e *Engine) HandleEvent(ctx context.Context, ev Event) error
// EvaluateMetrics 周期评估指标阈值规则（同一去重键同样走 for_duration 与去重）。
func (e *Engine) EvaluateMetrics(ctx context.Context, samples []MetricSample) error
// Reload 规则增删改后刷新缓存（由 backend 在 API 写操作后调用）。
func (e *Engine) Reload(ctx context.Context) error
```

### 19.2 `internal/notify` —— 通知渠道（`notifydev` 负责）

**不得 import `internal/store` / `internal/api` / `internal/alerting`**。

```go
type Kind string // webhook | email | dingtalk | wecom | feishu | telegram | slack

type Channel struct {
    ID      string
    Name    string
    Kind    Kind
    Enabled bool
    Config  map[string]any
}

type Severity string // critical | warning | info

type Message struct {
    Title      string
    Body       string
    Severity   Severity
    ServerName string
    CheckName  string
    Value      float64
    TS         int64
    Recovered  bool
    AlertID    string
    ConsoleURL string
}

type Client struct{ /* http.Client + SMTP 配置 */ }

func NewClient(opts Options) *Client
func (c *Client) Validate(ch Channel) error
func (c *Client) Send(ctx context.Context, ch Channel, msg Message) error
```

**各 Kind 的 `Config` 字段（冻结）**

| Kind | 必填 | 可选 |
|---|---|---|
| `webhook` | `url` | `method`(默认 POST)、`headers`(map)、`template`(`json`/`text`，默认 json)、`timeout_s` |
| `dingtalk` | `webhook` | `secret`（加签，用 HMAC-SHA256 + base64 + urlencode） |
| `wecom` | `webhook` | — |
| `feishu` | `webhook` | `secret`（签名校验） |
| `slack` | `webhook` | `channel` |
| `telegram` | `bot_token`、`chat_id` | `parse_mode`、`api_base`（**测试/代理用**，默认 `https://api.telegram.org`） |
| `email` | `smtp_host`、`from`、`to`(数组) | `smtp_port`(默认 587)、`username`、`password`、`tls`(`starttls`/`ssl`/`none`，默认 starttls) |

**协作 补充（冻结）**
- `Client.Probe(ctx, ch) (ProbeResult, error)`：与 `Send` 使用**完全相同**的请求构造，服务商任意应答（含 4xx/5xx/业务码）都算「可达」；只有 DNS/连接/TLS/超时/取消返回分类错误。用于「测试连通性」。
- `ErrorKind`：`config|dns|connect|tls|timeout|canceled|auth|http|server|provider|internal`，带 `Label()`（中文，可直接写入 `alert_channels.last_error`）、`Category()`（`config|network|provider`）、`Retryable()`。`Send` 返回 `*SendError`，用 `AsSendError`/`KindOf` 取值。
- **重试**：仅 `timeout` 与 `server`(5xx) 重试，默认 2 次指数退避（总 <10s）；4xx/鉴权/业务错误不重试；ctx 取消立即停止。
- **SMTP 语义**：SMTP 4xx 属于协议定义的临时失败，**映射为可重试**（与 HTTP 「4xx 不重试」字面不同，按协议语义处理）；SMTP 5xx/535 不重试。
- `email` 配置新增可选字段：`reply_to`、`html`（true 时发 multipart/alternative）。

发送要求：单渠道超时 10s；HTTP 非 2xx 视为失败并**截断响应体**（保留前 512 字节）进错误信息；**渠道配置里的密钥类字段在日志与错误信息中必须脱敏**（`xxx` 或只留前 4 位）。

## 20. REST 新增端点（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/alert-rules` | 规则列表 / 创建（201） |
| GET/PATCH/DELETE | `/alert-rules/{id}` | 详情 / 修改（改后需 Reload）/ 删除 |
| POST | `/alert-rules/{id}/silence` | `{duration_s}` 临时静默；**`duration_s=0` 表示取消静默**（清空 `silence_until`），负数 400 |
| GET/POST | `/alert-channels` | 渠道列表（**配置中的密钥类字段必须脱敏返回**）/ 创建 |
| PATCH/DELETE | `/alert-channels/{id}` | 修改 / 删除 |
| POST | `/alert-channels/{id}/test` | 发送一条测试消息 → `{ok,error?}` |
| GET | `/alerts` | `?status=&severity=&rule_id=&server_id=&from=&to=&limit=` 告警列表 |
| GET | `/alerts/{id}` | 详情 + `events`（状态变更轨迹与通知记录） |
| POST | `/alerts/{id}/ack` | 人工确认 → `status=acked`，记 `acked_at` |
| GET | `/alerts/summary` | `{firing,acked,resolved_24h,by_severity:{critical,warning,info}}` |

**规则 body**：`{name,enabled,source,check_ids?,server_ids?,groups?,tags?,metric?,comparator?,threshold?,for_duration_s,severity,dedup_window_s,channel_ids}`。
**渠道 body**：`{name,kind,enabled,config:{...}}`；`config` 落库前用 `credential.Cipher` 加密（复用 纳管 主密钥）。

## 21. 数据库迁移 `0005_alerts.sql`

```sql
CREATE TABLE alert_rules(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
  source TEXT NOT NULL, check_ids TEXT NOT NULL DEFAULT '[]', server_ids TEXT NOT NULL DEFAULT '[]',
  groups TEXT NOT NULL DEFAULT '[]', tags TEXT NOT NULL DEFAULT '[]',
  metric TEXT, comparator TEXT, threshold REAL,
  for_duration_s INTEGER NOT NULL DEFAULT 0, severity TEXT NOT NULL DEFAULT 'warning',
  dedup_window_s INTEGER NOT NULL DEFAULT 300, channel_ids TEXT NOT NULL DEFAULT '[]',
  silence_until INTEGER, created_at INTEGER NOT NULL);
CREATE TABLE alert_channels(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL,
  config_enc BLOB NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
  last_error TEXT, last_ok_at INTEGER, created_at INTEGER NOT NULL);
CREATE TABLE alerts(
  id TEXT PRIMARY KEY, rule_id TEXT NOT NULL, dedup_key TEXT NOT NULL,
  server_id TEXT, check_id TEXT, status TEXT NOT NULL, severity TEXT NOT NULL,
  title TEXT NOT NULL, message TEXT, value REAL,
  started_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  resolved_at INTEGER, acked_at INTEGER, acked_by TEXT,
  notify_count INTEGER NOT NULL DEFAULT 0, last_notify_at INTEGER, silence_until INTEGER);
CREATE INDEX idx_alerts_dedup ON alerts(dedup_key, status);
CREATE INDEX idx_alerts_started ON alerts(started_at);
CREATE TABLE alert_events(
  id INTEGER PRIMARY KEY AUTOINCREMENT, alert_id TEXT NOT NULL,
  kind TEXT NOT NULL, ts INTEGER NOT NULL, detail TEXT);
```

## 22. 告警验收标准（`scripts/verify_alerts.sh` 一键复现）

1. `make build` 成功；`go vet ./...` 与 `go test ./...` 全绿；基础/纳管/检测 脚本仍全绿。
2. 创建 `webhook` 渠道指向**本机 HTTP 接收器**，`POST /alert-channels/{id}/test` → `ok=true` 且接收器**真实收到**一次 POST。
3. 渠道列表接口**不返回**配置中的密钥明文（以原始 token/secret 片段反向断言）。
4. 创建 `source=check` 规则（`for_duration_s=0`、severity=critical、绑定上述渠道与目标检测项）→ 制造失败检测项 → 60 秒内 `/alerts` 出现 `firing`，且接收器收到告警通知（标题含规则名/服务器名）。
5. `POST /alerts/{id}/ack` → `status=acked`；`GET /alerts/{id}` 的 `events` 含 ack 记录。
6. **恢复通知**：把检测项目标改为可达 → 告警转 `resolved`，接收器收到第二封（恢复）通知。
7. **去重**：同一故障重复触发**不新增告警记录**（总数不变），`notify_count` 增长受 `dedup_window_s` 约束。
8. **指标规则**：`source=metric`（如 `cpu.pct > 0`）在周期评估后产生 `firing` 告警。
9. **静默**：`POST /alert-rules/{id}/silence` 后新触发仍建告警但**接收器不再收到新通知**。
10. `GET /alerts/summary` 返回 `firing`/`by_severity` 计数；未带 token 访问 `/alert-rules` 返回 401。
11. `scripts/verify_bootstrap.sh`、`verify_servers.sh`、`verify_checks.sh` 仍全绿（**告警 不得回归**）。

---

# 批量部署契约

> 部署 是产品的差异化重头：**把一次发布安全地推到 N 台机器，并能在失败时干净回退**。
> 底座已就绪：检测 的 `internal/jobs` 下发链路、agent 侧 `job.exec` 与日志分块、纳管 的 SSH 执行通道。
> 部署 **不改动**这两条通道，只在其上做编排。

## 23. 核心概念

| 概念 | 约定 |
|---|---|
| **应用（app）** | 被部署的业务单元，有名字与唯一 slug。一个应用可部署到多台服务器 |
| **发布模板（template）** | 如何部署：`compose` / `systemd` / `binary`。模板是**版本化文本**，发布时冻结快照 |
| **制品（artifact）** | 要部署的文件或镜像：`url`（HTTP 下载）/ `upload`（控制端存储）/ `image`（容器镜像，不下载） |
| **发布（release）** | 一次「把某个应用版本部署到某组服务器」的记录，是**一等公民**。包含版本号、模板快照、制品引用、目标服务器、状态 |
| **发布任务（release_run）** | 发布在**单台服务器**上的执行单元。一台机器一个 run，独立状态、独立日志、独立回滚 |
| **回滚（rollback）** | 把某台服务器恢复到**上一个成功发布**的版本（目录快照切换 / 镜像 tag 回指） |

**状态机**（`release.status` / `release_run.status`）
```
pending → running → success
                 ↘ failed → (可 retry / rollback)
pending → canceled
```
发布整体状态由各 run 汇总：全部 success → `success`；任一 failed 且不再重试 → `failed`；仍在跑 → `running`。

## 24. 目录与快照约定（回滚的基础）

每个应用的部署路径 = **`<app.root base>/<app_slug>`**，其中 `app.root` 是**基础目录**（不是应用根目录）：
- 创建应用时 `root` 留空 → 基础目录取 `LEADSTACK_DEPLOY_ROOT`（默认 `/opt/leadstack/apps`），实际路径为 `/opt/leadstack/apps/<slug>`；
- 显式填 `root=/srv/apps` → 实际路径为 `/srv/apps/<slug>`。

> ⚠️ **不要**把 `<slug>` 也写进 `root`，否则会得到 `/srv/apps/<slug>/<slug>`。UI 在创建应用时应展示「实际部署路径」提示。

```
<base>/<slug>/
├── current -> releases/20261019-171500-a1b2c3     # 符号链接，原子切换
├── releases/
│   ├── 20261019-171500-a1b2c3/                    # 每次发布一个不可变目录
│   └── 20261019-161500-d4e5f6/
└── .leadstack/                                    # 元数据：release id、模板快照、校验和
```

**部署 = 在临时目录准备好新版本 → 原子切换 `current` 符号链接 → 重启服务。**
**回滚 = 把 `current` 指回上一个 release 目录 → 重启服务。** 因此回滚是秒级的，不依赖重新下载。

## 25. Go 包 API 契约（`deploydev` 负责 `internal/deploy`）

**不得 import `internal/store` / `internal/api`**（接口注入，保证可独立单测）。

```go
type Kind string // "compose" | "systemd" | "binary"

type Template struct {
    ID      string
    Kind    Kind
    Content string // 渲染后的模板文本（含占位符，见 §26）
}

type Artifact struct {
    Kind      string // "url" | "upload" | "image"
    Location  string // url / 控制端存储 key / 镜像引用
    SHA256    string // url/upload 必填；image 可为空
    SizeBytes int64
}

// Step 是编排出的一个执行步骤，便于记录与排障。
type Step struct {
    Name    string
    Command string
    TimeoutS int
    // IgnoreFailure 为 true 时该步失败不中断（用于 best-effort 清理）。
    IgnoreFailure bool
}

// Plan 是一次部署在某台服务器上的完整执行计划。
type Plan struct {
    AppSlug    string
    ReleaseID  string
    Version    string
    Root       string
    Steps      []Step
    // RollbackSteps 在失败时按逆序执行（best-effort）。
    RollbackSteps []Step
}

// Build 根据模板 + 制品 + 目标信息编排执行计划（纯函数，不触网、不执行）。
func Build(tmpl Template, art Artifact, p BuildParams) (Plan, error)

type BuildParams struct {
    AppSlug      string
    ReleaseID    string
    Version      string
    Root         string
    PrevReleaseID string // 上一个成功发布，用于回滚
    Now          int64
}

// Render 渲染模板占位符（见 §26），未知占位符必须报错而不是留空。
func Render(content string, vars map[string]string) (string, error)

// Rollback 生成回滚计划（纯函数）。
func Rollback(p Plan) (Plan, error)
```

### 25.1 执行器（`internal/deploy` 也负责）

```go
// Runner 在目标机上执行一条命令，由 backend 注入（agent → jobs.Dispatcher；ssh → sshx 包装）。
// 与 checker.Executor 同形，但**额外提供流式日志回调**。
type Runner interface {
    Run(ctx context.Context, serverID, command string, timeoutS int,
        onLog func(stream, chunk string)) (stdout, stderr string, exitCode int, err error)
}

type Executor struct{ /* ... */ }
func NewExecutor(r Runner, opts ExecutorOptions) *Executor

type ExecutorOptions struct {
    Concurrency int // 全局并发上限，默认 4（避免同时压满所有机器）
    Logger      *slog.Logger
}

// RunPlan 在单台服务器上执行计划，逐步回调进度与日志。
func (e *Executor) RunPlan(ctx context.Context, serverID string, p Plan,
    hooks Hooks) (RunOutcome, error)

type Hooks struct {
    OnStep   func(stepIndex int, name string)
    OnLog    func(stream, chunk string)
    OnStatus func(status string, message string)
}

type RunOutcome struct {
    Status   string // success | failed
    ExitCode int
    Message  string
    FailedStep string
}
```

**执行语义（硬要求）**
1. 步骤**严格串行**，任一步非 0 退出即失败（`IgnoreFailure` 除外）。
2. 失败时**必须**按逆序执行 `RollbackSteps`（best-effort，失败只记日志）。
3. 每个步骤的命令都必须是**幂等可重跑**的（由模板保证，`Build` 里不插入非幂等操作）。
4. 日志通过 `Hooks.OnLog` 实时流出，**不缓冲到结束**（用户要能看见进度）。
5. `ctx` 取消时终止当前步骤并走回滚。

## 26. 模板占位符（冻结）

占位符写作 `{{APP_SLUG}}`（**双花括号 + 大写下划线名**）。`Render` 同时接受 `{{.APP_SLUG}}` 这种带点前缀的写法（视作同一变量，兼容 Go template 习惯）；两种写法之外的未知占位符一律报错。

| 占位符 | 含义 |
|---|---|
| `{{APP_SLUG}}` | 应用 slug（已校验 `[a-z0-9-]{1,40}`） |
| `{{VERSION}}` | 版本号（已校验 `[A-Za-z0-9._-]{1,64}`） |
| `{{RELEASE_ID}}` | 本次发布 ID |
| `{{ROOT}}` | 部署根目录 |
| `{{RELEASE_DIR}}` | `<ROOT>/releases/<RELEASE_ID>` |
| `{{ARTIFACT_URL}}` | 制品下载地址 |
| `{{ARTIFACT_SHA256}}` | 制品校验和 |
| `{{PREV_RELEASE_ID}}` | 上一个成功发布 ID |

**安全红线**：所有变量值在代入前必须校验字符集（slug/version 白名单，其余禁止 `;|&$`\` 与换行），**防止命令注入**。未知占位符 → 报错（不能静默留空，否则会部署出半成品）。

**校验口径**：路径类变量（`ROOT`、`RELEASE_DIR`）允许 **UTF-8 可打印字符**（中文目录名如 `/srv/应用` 必须可用，上限 1024 字节），但仍拒绝空白、控制字符、换行与 shell 元字符 `; | & $ \` " ' ( ) { } < > * ? ! ~ #`，并拒绝 `..` 路径段；`APP_SLUG`/`VERSION`/`RELEASE_ID`/`ARTIFACT_URL`/`ARTIFACT_SHA256` 的白名单保持不变。

## 27. REST 端点（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/apps` | 应用列表 / 创建 `{name,slug,root?,description?}` |
| GET/PATCH/DELETE | `/apps/{id}` | 详情 / 修改 / 删除（有 release 时 409） |
| GET/POST | `/templates` | 模板列表 / 创建 `{name,kind,content}` |
| GET/PATCH/DELETE | `/templates/{id}` | 详情 / 修改（已发布快照不受影响）/ 删除 |
| POST | `/templates/{id}/render` | `{vars}` → 渲染预览 `{content}`（**不执行**，用于 UI 预览） |
| POST | `/artifacts` | 上传制品（multipart `file` 字段）→ `{id,sha256,size_bytes}`（**sha256 由服务端计算**）；或登记 URL 制品 `{kind:"url",url,sha256,size_bytes?}` —— **`sha256` 必填**（64 位 hex，大写自动归一化），缺失或非法 → 400。理由：校验和是「失败不破坏 current、回滚有意义」的前提，必须在登记时 fail-fast |
| GET/POST | `/releases` | 发布列表 / 创建发布（见下） |
| GET | `/releases/{id}` | 详情 + 各 run 状态与日志摘要 |
| GET | `/releases/{id}/runs` | 各服务器 run 列表 |
| GET | `/releases/{id}/runs/{serverID}/logs` | `?after_seq=` 增量拉取日志（**支持断点续读**） |
| POST | `/releases/{id}/retry` | 对失败的 run 重试（只重跑 failed 的服务器） |
| POST | `/releases/{id}/rollback` | 回滚（`{server_ids?}` 缺省全部成功的 run） |
| POST | `/releases/{id}/cancel` | 取消仍在 pending/running 的 run |

**创建发布 body**：`{app_id,template_id,version,artifact_id,server_ids,concurrency?}`。
**创建响应**：`{release:{...}, runs:[{server_id,status}]}` —— 立即返回，执行在后台异步进行（前端轮询进度）。

**补充约定（部署 实现后冻结）**
- **上传型制品需要目标机能访问控制端**：`upload` 制品发布时，控制端把 `<LEADSTACK_PUBLIC_URL>/api/v1/artifacts/{id}/download` 作为下载地址交给目标机。生产部署必须配置 `LEADSTACK_PUBLIC_URL`（如 `http://10.0.0.5:8080`）；不配时创建发布直接 400 并说明原因，而不是让目标机静默失败。
- `GET /releases`（列表）**不返回 runs**，只返回 release 主体；需要进度时用 `GET /releases/{id}` 或 `/releases/{id}/runs`。
- `retry` 会重置失败 run 的日志（seq 从 1 重新开始），成功 run 不受影响。
- `cancel` 会把 pending/running 置 `canceled`；已完成的 run 保持原状态。

## 28. 数据库迁移 `0006_deploy.sql`

```sql
CREATE TABLE apps(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, slug TEXT UNIQUE NOT NULL,
  root TEXT, description TEXT, created_at INTEGER NOT NULL);
CREATE TABLE deploy_templates(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL,
  content TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE artifacts(
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, location TEXT NOT NULL,
  sha256 TEXT, size_bytes INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
CREATE TABLE releases(
  id TEXT PRIMARY KEY, app_id TEXT NOT NULL, template_id TEXT NOT NULL,
  version TEXT NOT NULL, artifact_id TEXT, template_snapshot TEXT NOT NULL,
  status TEXT NOT NULL, created_by TEXT, created_at INTEGER NOT NULL,
  started_at INTEGER, finished_at INTEGER, concurrency INTEGER NOT NULL DEFAULT 4);
CREATE TABLE release_runs(
  id TEXT PRIMARY KEY, release_id TEXT NOT NULL, server_id TEXT NOT NULL,
  status TEXT NOT NULL, exit_code INTEGER, message TEXT, failed_step TEXT,
  prev_release_id TEXT, started_at INTEGER, finished_at INTEGER,
  UNIQUE(release_id, server_id));
CREATE INDEX idx_release_runs_lookup ON release_runs(release_id, status);
CREATE TABLE release_logs(
  release_run_id TEXT NOT NULL, seq INTEGER NOT NULL, ts INTEGER NOT NULL,
  stream TEXT NOT NULL, chunk TEXT NOT NULL,
  PRIMARY KEY(release_run_id, seq)) WITHOUT ROWID;
```

## 29. 部署验收标准（`scripts/verify_deploy.sh` 一键复现）

1. `make build` 成功；`gofmt` 干净；`go vet ./...` 与 `go test ./...` 全绿；既有能力 脚本仍全绿。
2. 创建应用 + `binary` 模板 + `url` 制品（指向本机 HTTP 文件服务）→ 创建发布到 1 台 SSH 服务器 → 轮询至 `success`。
3. **真实产物落盘**：目标机（devssh 执行）上 `<root>/current` 符号链接指向的目录里存在下载的文件，且**校验和与制品一致**。
4. **发布日志可增量拉取**：`GET /releases/{id}/runs/{serverID}/logs?after_seq=0` 返回有序日志，再以 `after_seq=<最后一条>` 拉取不重复。
5. **失败与回滚**：让第二次发布的制品校验和错误 → 该 run 变 `failed` 且 `current` 仍指向**上一次成功的版本**（未被破坏）。
6. 第二次发布修正后成功 → 再 `POST /releases/{id}/rollback` → `current` 指回上一版本，且 `release_runs` 记录回滚结果。
7. **批量并发**：一次发布到 2 台服务器（1 台真实 devssh + 1 台不可达）→ 一台 `success`、一台 `failed`，整体状态 `failed`，成功的那台**不受影响**。
8. **模板安全**：`Render` 对未知占位符报错；含 `; rm -rf /` 的 version 被拒绝（400）；渲染预览端点不执行任何命令。
9. **重试**：`POST /releases/{id}/retry` 只重跑 failed 的服务器，成功的 run 不重复执行。
10. 未带 token 访问 `/apps` 返回 401；删除有 release 的应用返回 409。
11. `scripts/verify_bootstrap.sh` ~ `verify_alerts.sh` 仍全绿（**部署 不得回归**）。

---

# 备份与恢复契约

> 数据目录里有四类东西：`leadstack.db`（业务数据）、`master.key`（凭证加密主密钥）、`known_hosts`（SSH 主机密钥）、`artifacts/`（上传制品）。
> **丢了主密钥 = 所有凭证作废**，所以备份必须包含它；而包含它的归档如果明文存放，等于把全部服务器钥匙交出去 —— 因此备份**必须加密**。

## 30. 归档格式 `LSBAK1`

```
+--------------------------------------------------------------+
| magic  "LSBAK1\n"  (7 字节)                                   |
| headerLen  uint32 BE                                          |
| header     JSON，长度 headerLen                               |
| ciphertext AES-256-GCM(tar.gz)                                 |
+--------------------------------------------------------------+
```

**header JSON（冻结）**
```json
{ "format_version": 1,
  "app_version": "1.2.3",
  "schema_version": 6,
  "created_at": 1710000000000,
  "includes_artifacts": false,
  "kdf": {"name":"scrypt","salt":"<b64>","n":32768,"r":8,"p":1,"key_len":32},
  "cipher": {"name":"aes-256-gcm","nonce":"<b64>"} }
```

**tar.gz 内文件（manifest.json 必须为第一个条目）**
| 文件 | 必选 | 说明 |
|---|---|---|
| `manifest.json` | ✅ | `{format_version,app_version,schema_version,created_at,includes_artifacts,files:[{name,size,sha256}]}` |
| `leadstack.db` | ✅ | 用 `VACUUM INTO` 导出的**一致快照**（不能直接拷 WAL 模式的库文件） |
| `master.key` | ✅ | 凭证主密钥；缺失则恢复后所有凭证不可用 |
| `known_hosts` | 可选 | SSH 主机密钥记录 |
| `artifacts/<id>` | 可选 | 上传制品；默认不含（体积大），由开关控制 |

**绝不进归档**：`backup.pass`、`backups/` 目录、`*.restore-pending/`、`*.db-wal` / `*.db-shm`。
> `backup.pass` 被排除是有意设计：它是解密备份的钥匙，单独存放，这样**备份文件即使被拿走也解不开**。

## 31. 口令与密钥

| 项 | 约定 |
|---|---|
| 口令来源 | `LEADSTACK_BACKUP_PASSPHRASE` 环境变量优先；否则读 `<data_dir>/backup.pass`；都不存在则**自动生成** 32 字节随机口令并以 0600 落盘（日志提示「请单独保管」） |
| 派生 | `scrypt(passphrase, salt, N=32768, r=8, p=1)` → 32 字节 AES-256 密钥 |
| 加密 | AES-256-GCM，随机 12 字节 nonce；认证失败（口令错/被篡改）必须**明确区分**为 `ErrBadPassphrase` 与 `ErrCorrupt` |
| 恢复 | 必须提供口令；错误口令 → 明确报错，不得静默失败 |

## 32. Go 包 API 契约（`internal/backup`）

**不得 import `internal/store` / `internal/api`**（只做文件与归档，SQLite 用只读连接做快照）。

```go
type Options struct {
    DataDir    string        // 要备份的数据目录
    BackupDir  string        // 归档输出目录（默认 <data_dir>/backups）
    Passphrase string        // 空则从 EnvPassphrase / backup.pass 解析
    Now        func() int64
}

type Info struct {
    Name              string `json:"name"`         // 文件名，如 leadstack-20261020-091500.lsbak
    SizeBytes         int64  `json:"size_bytes"`
    CreatedAt         int64  `json:"created_at"`
    IncludesArtifacts bool   `json:"includes_artifacts"`
    SchemaVersion     int    `json:"schema_version"`
    SHA256            string `json:"sha256"`       // 归档文件本身的校验和
}

type Manifest struct {
    FormatVersion     int            `json:"format_version"`
    AppVersion        string         `json:"app_version"`
    SchemaVersion     int            `json:"schema_version"`
    CreatedAt         int64          `json:"created_at"`
    IncludesArtifacts bool           `json:"includes_artifacts"`
    Files             []ManifestFile `json:"files"`
}
type ManifestFile struct {
    Name      string `json:"name"`
    SizeBytes int64  `json:"size_bytes"`
    SHA256    string `json:"sha256"`
}

type Manager struct{ /* ... */ }
func NewManager(opts Options) (*Manager, error)

// Create 生成一份备份。**在线执行**：不停止服务，不阻塞写入过久（VACUUM INTO 快照）。
func (m *Manager) Create(ctx context.Context, includeArtifacts bool) (*Info, error)
// List 从备份目录读取（按创建时间倒序）；元数据存同名 .json 边车文件。
func (m *Manager) List(ctx context.Context) ([]Info, error)
// Verify 解密 + 校验 header/manifest/文件哈希/DB 完整性，任何异常都报错。
func (m *Manager) Verify(ctx context.Context, path string) (*Manifest, error)
func (m *Manager) Delete(ctx context.Context, name string) error

// 哨兵错误
var ErrBadPassphrase = errors.New("backup: 口令错误")
var ErrCorrupt       = errors.New("backup: 归档损坏或格式不符")
var ErrSchemaTooNew  = errors.New("backup: 备份来自更新的版本")

// Restore 离线恢复：解压到临时目录 → 校验 → 把现有受管文件移到 <dataDir>.pre-restore-<ts>/ → 放置新文件。
// **要求服务已停止**（调用方负责）；恢复前自动做一次安全快照（目录级 move，不复制大文件）。
func Restore(opts RestoreOptions) (*RestoreResult, error)

type RestoreOptions struct {
    File       string
    DataDir    string
    Passphrase string
    // KeepOld 为 true 时保留 <dataDir>.pre-restore-<ts>/，默认 true。
    KeepOld bool
    MaxSchemaVersion int // 当前程序支持的最高迁移版本，用于拒绝「来自更新版本」的备份
}
type RestoreResult struct {
    Manifest      *Manifest
    PreRestoreDir string
    RestoredFiles []string
}
```

## 33. REST 端点（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backups` | 备份列表（含 size/created_at/includes_artifacts/schema_version/sha256） |
| POST | `/backups` | 立即备份 `{include_artifacts?}` → 201 + `Info` |
| GET | `/backups/{name}/download` | 下载归档（`Content-Disposition: attachment`） |
| POST | `/backups/{name}/verify` | 校验 → `{ok, manifest}`；失败返回 400 + `{error:{code,message}}`。**同时校验 schema 版本**：备份来自更新版本时 `code=schema_too_new`（与恢复阶段同一判定，避免用户到恢复时才发现） |
| DELETE | `/backups/{name}` | 删除（同时删边车 json） |
| POST | `/backups/restore` | multipart 上传归档（字段名 `file`）→ 校验 → 暂存到 `<data_dir>/.restore-pending/` → `202 {restart_required:true, staged:{source,created_at,verified_at,archive_sha256,schema_version}}`；**下次启动时应用**。校验失败 400 且不留半成品 |
| GET/PATCH | `/backup-settings` | `{enabled, interval, retention, include_artifacts}`；`interval` 为 Go duration 字符串 |

**已知限制（备份 记录）**：「版本过新」在真实环境需由**更新版本的程序**生成的归档才能触发；验收脚本不构造该场景，改由单测覆盖 `ErrSchemaTooNew` 与 `code=schema_too_new`。

**启动时应用暂存恢复**：`cmd/leadstack` 在打开数据库**之前**检查 `.restore-pending/meta.json`，存在则执行离线恢复（旧数据保留为 `<data_dir>.pre-restore-<ts>/`）再继续启动，并打印醒目日志。
**定时备份**：`enabled=true` 时按 `interval`（默认 `24h`）执行，保留 `retention`（默认 7）份，超出按创建时间删最旧。

## 34. 命令行

```bash
leadstack backup  --data-dir ./data --out /tmp/x.lsbak [--with-artifacts] [--passphrase-file p]
leadstack backup verify --file /tmp/x.lsbak [--passphrase-file p]
leadstack restore --file /tmp/x.lsbak --data-dir ./data [--passphrase-file p]   # 服务必须已停止
```

## 35. 验收标准（`scripts/verify_backup.sh` 一键复现）

1. `make build` 成功；`gofmt` 干净；`go vet ./...` 与 `go test ./...` 全绿；既有能力 脚本仍全绿。
2. 造数据（服务器/凭证/应用/模板/制品/检测项/告警规则）→ `POST /backups` 成功，`size_bytes>0`，文件以 `LSBAK1` 开头。
3. **归档必须加密**：原始归档字节中搜不到 `SQLite format 3`、`BEGIN OPENSSH PRIVATE KEY`、明文密码、`master.key` 内容；`backup.pass` 存在于数据目录但**不在**归档内。
4. `GET /backups` 列表可见；下载内容与磁盘文件一致；`POST /verify` 返回 `ok=true` 且 manifest 含 db/master.key 条目。
5. **篡改检测**：翻转归档中一个字节 → `verify` 失败且错误可读。
6. **口令错误**：用错误口令 `verify`/`restore` → 明确报 `口令错误`，不得"成功但数据为空"。
7. **恢复：离线 CLI** —— 先改数据（删除一台服务器）→ 停服务 → `leadstack restore` → 启动 → 被删的服务器**恢复存在**，且 `pre-restore` 目录保留了恢复前状态。
8. **恢复：API 暂存** —— 上传归档到 `/backups/restore` → 202 `restart_required` → 重启控制端 → 数据恢复且暂存目录被清理。
9. **定时备份 + 保留策略**：`interval` 压到几秒、`retention=2`，等若干轮后备份数量不超过 2 且最新的更新。
10. **主密钥有效性**：恢复后原凭证仍可解密（用恢复后的实例 `POST /servers/{id}/test-connection` 或读取凭证指纹一致来证明）。
11. 未带 token 访问 `/backups` 返回 401；备份文件名含路径穿越（`../`）时删除/下载被拒（400）。
12. `scripts/verify_bootstrap.sh` ~ `verify_deploy.sh` 仍全绿（**不得回归**）。

---

# 运维与协作 契约（运维增强 · 团队协作 · 体验）

> 本批补齐全部剩余功能。**新功能不得破坏既有 166 项验收**：所有列表接口在**不传 `limit` 时仍返回 `{data:[...]}` 数组**（分页参数只在显式传入时生效）。

## 36. 审计日志

**记录范围**：所有写操作（`POST`/`PATCH`/`PUT`/`DELETE`）自动记录；登录成功/失败、终端会话开启、备份恢复、Agent 升级手动记录。
**不记录**：`GET` 读操作（避免噪声）。

```sql
CREATE TABLE audit_logs(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  actor TEXT NOT NULL,          -- 用户名；API Token 为 "token:<name>"；系统为 "system"
  actor_role TEXT,
  action TEXT NOT NULL,         -- "<resource>.<verb>"，如 server.create / release.rollback
  resource_type TEXT, resource_id TEXT,
  method TEXT, path TEXT,
  status_code INTEGER,
  ip TEXT, user_agent TEXT,
  detail TEXT                   -- JSON；**必须脱敏**（密钥/密码/口令一律 ***）
);
CREATE INDEX idx_audit_ts ON audit_logs(ts);
CREATE INDEX idx_audit_actor ON audit_logs(actor, ts);
```

- 保留 `LEADSTACK_AUDIT_RETENTION_DAYS`（默认 180），随清扫循环清理。
- **action 命名表**（实现时按此，便于筛选）：`auth.login` / `auth.logout` / `server.create|update|delete|batch` / `credential.create|delete` / `check.create|update|delete|run` / `alert_rule.create|update|delete|silence` / `alert_channel.create|update|delete|test` / `alert.ack` / `app.create|update|delete` / `template.create|update|delete` / `artifact.create` / `release.create|retry|rollback|cancel` / `backup.create|delete|restore` / `user.create|update|delete` / `token.create|delete` / `terminal.open|close` / `agent.upgrade` / `import.servers`。
- `GET /audit-logs?actor=&action=&resource_type=&from=&to=&limit=&cursor=`

## 37. RBAC 与用户管理

| 角色 | 权限 |
|---|---|
| `admin` | 全部 |
| `operator` | 服务器/凭证（读+用，不可删凭证）/检测/告警规则与渠道/应用/模板/发布/备份/定时任务/终端/文件/日志/进程；**不可**管理用户、不可读审计日志、不可恢复备份、不可删除服务器 |
| `viewer` | 只读全部业务数据；不可执行任何写操作与终端 |

**实现**：路由组挂 `RequireRole(min)`；写操作用 `RequireRole(operator)`；用户管理/审计/备份恢复/凭证删除用 `RequireRole(admin)`。越权返回 **403** `code=forbidden`。

```
GET    /users                  # admin
POST   /users                  # admin  {username,password,role}
PATCH  /users/{id}             # admin  {role?,disabled?,password?}
DELETE /users/{id}             # admin；禁止删除最后一个 admin、禁止自删
POST   /users/me/password      # 所有角色 {old_password,new_password}
GET    /auth/me                # 返回 {id,username,role}
```
用户表新增 `disabled INTEGER NOT NULL DEFAULT 0`、`updated_at INTEGER`。

## 38. API Token

```
api_tokens(id TEXT PK, name TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL, role TEXT NOT NULL,
           created_by TEXT, created_at INTEGER NOT NULL, last_used_at INTEGER,
           expires_at INTEGER, revoked_at INTEGER)
```
- 明文格式 `lsk_<32字节base64url>`，**只在创建响应返回一次**；库中只存 sha256。
- 认证中间件：`Authorization: Bearer lsk_...` → 按 token 角色鉴权，并更新 `last_used_at`；过期/撤销 → 401。
- `GET /api-tokens`（不返回明文）、`POST /api-tokens {name,role,expires_in?}`、`DELETE /api-tokens/{id}`（admin）。

## 39. 分页约定

- 显式传 `limit`（1–500，默认 100 上限）时：响应 `{data:[...], page:{next_cursor,has_more}}`；`cursor` 为不透明字符串。
- **不传 `limit` 时保持原样**（`{data:[...]}` 数组），保证既有前端与验收脚本不受影响。
- 首批接入：`/audit-logs`、`/alerts`、`/releases`、`/checks`、`/servers`、`/scheduled-tasks/{id}/runs`。

## 40. Web SSH 终端

**开关**：`LEADSTACK_TERMINAL_ENABLED`（默认 `false`）；关闭时端点返回 403 并提示。

```
POST /servers/{id}/terminal-ticket   -> {data:{ticket, expires_in:60}}
GET  /servers/{id}/terminal?ticket=  -> WebSocket（升级）
```
- 一次性 ticket（60s 有效、单次使用），避免把 JWT 放进 URL/日志。
- **ssh 模式**：`sshx.PTY(ctx, opts)` 打开带 PTY 的会话；**agent 模式**：控制端经 WS 向 agent 发 `term.open`，agent 用 `creack/pty` 起 shell 并双向转发。
- 帧：文本帧传控制 JSON（`{"type":"input","data":"..."}` / `{"type":"resize","cols":..,"rows":..}`），或直接二进制帧传终端数据；实现选二进制帧 + 首帧 JSON 元信息。
- 断开/超时（30 分钟无操作）关闭会话；`audit_logs` 写 `terminal.open` / `terminal.close`（含 server_id、操作者、时长）。

## 41. 文件上传/下载

```
GET  /servers/{id}/files?path=/var/log      -> {entries:[{name,is_dir,size,mod_time,mode}]}
GET  /servers/{id}/files/download?path=/x   -> 流式下载（Content-Disposition）
POST /servers/{id}/files/upload             -> multipart（path=..., file=...）→ {ok,bytes}
DELETE /servers/{id}/files?path=/x          -> 删除（admin 或 operator）
```
- **ssh 模式用 SFTP**；**agent 模式用 `job.exec` + base64 分块**（单文件上限 **8 MiB**，超出报错）。两种通道由同一组端点暴露。
- 路径安全：拒绝 `..` 段穿越出用户指定根；下载/上传记录审计。

## 42. 定时任务（cron）

```sql
CREATE TABLE scheduled_tasks(
  id TEXT PK, name TEXT NOT NULL, cron TEXT NOT NULL, command TEXT NOT NULL,
  timeout_s INTEGER NOT NULL DEFAULT 60, enabled INTEGER NOT NULL DEFAULT 1,
  concurrency INTEGER NOT NULL DEFAULT 4, created_at INTEGER NOT NULL,
  last_run_at INTEGER, next_run_at INTEGER);
CREATE TABLE scheduled_task_targets(task_id TEXT, server_id TEXT, PRIMARY KEY(task_id,server_id)) WITHOUT ROWID;
CREATE TABLE scheduled_task_runs(
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, server_id TEXT NOT NULL,
  ts INTEGER NOT NULL, status TEXT NOT NULL, exit_code INTEGER, duration_ms INTEGER,
  stdout TEXT, stderr TEXT, message TEXT);
CREATE INDEX idx_str_task ON scheduled_task_runs(task_id, ts);
```
- cron：**5 字段**（分 时 日 月 周），支持 `*` `,` `-` `*/n`；解析失败 400 并给出人话错误。
- 引擎每分钟检查一次（`LEADSTACK_CRON_TICK` 可覆盖，测试用 `1s`），按 server_ids 并发执行；保留 30 天。
- `GET/POST /scheduled-tasks`、`GET/PATCH/DELETE /scheduled-tasks/{id}`、`POST /scheduled-tasks/{id}/run`、`GET /scheduled-tasks/{id}/runs?limit=`。
- **禁止把 command 直接拼进 shell**：执行方式与检测项一致（经 job.exec / ssh Run），由使用者承担命令内容责任，但界面需提示"命令将在目标机以当前用户权限执行"。

## 43. 日志查看与进程/端口视图

```
GET /servers/{id}/logs?path=/var/log/app.log&lines=200&grep=error  -> {lines:[...], truncated}
GET /servers/{id}/processes?sort=cpu|mem&limit=50                  -> {processes:[{pid,user,cpu_pct,mem_pct,rss_kb,command}]}
GET /servers/{id}/ports                                            -> {ports:[{proto,local,pid,process}]}
```
- 实现：ssh 模式执行 `tail -n` / `ps` / `ss -tulpn`（macOS 回退 `lsof -i`）；agent 模式经 `job.exec`。
- 输出大小上限 256 KB；`grep` 用服务端过滤（不要拉全量）。

## 44. Agent 自升级

- 协议新增（`pkg/protocol`）：`agent.upgrade` → `{version,url,sha256,restart}`。
- agent 收到后：下载到临时文件 → 校验 sha256 → **A/B 双文件替换**（当前运行的可执行文件不能原地覆盖）→ 重启（systemd/launchd 负责重启，或自 exec）。
- `POST /servers/{id}/agent-upgrade {version,url,sha256}`、`POST /agents/upgrade`（批量，按当前版本筛选）。
- 审计 `agent.upgrade`；失败保留旧版本并回报原因。

## 45. 批量导入主机

```
POST /servers/import
{ "text": "10.0.0.1\n10.0.0.2:2222\n# 注释行\nhost3.example.com",
  "default_port": 22, "credential_id": "...", "group": "...", "tags": ["..."],
  "mode": "ssh" }
-> {data:{total, created:[{host,server_id}], failed:[{host,error}]}}
```
- 解析：每行 `host[:port]`，`#` 开头与空行忽略；去重（同 host:port 已存在则跳过并计失败原因）；单次上限 500 行。
- 复用 纳管 的服务器创建逻辑；返回逐条结果。

## 46. 中英双语 · 主题 · 字号 · 布局（前端规范）

**i18n**： zh-CN（默认）/ en-US：`web/src/i18n/{zh-CN,en-US}.ts` + `useI18n()`；语言存 `localStorage.leadstack.lang`；所有界面文案走 `t('key')`，**不允许硬编码中文残留**（验收会 grep 抽查）。

**主题**：`light` / `dark` / `system` 三态 + 主题色预设（至少 蓝/靛/青/绿/紫 5 种）；写入 `document.documentElement.dataset.theme` 与 `--accent` CSS 变量；图表（uPlot）需随主题重绘。

**字号**：`small` / `normal` / `large` 三档，映射到根 `font-size`（14/16/18px），Tailwind 用 rem 基准；持久化 `localStorage.leadstack.fontSize`。

**密度**：`comfortable` / `compact` 两档（表格行高与内边距）。

**布局与动线（必须遵守）**
1. 左侧导航**分组**：监控（总览/服务器/检测/告警）· 运维（终端、文件、定时任务、日志）· 发布（应用/发布）· 系统（凭证/用户/审计/备份/设置）。
2. 顶部：面包屑 + 当前页操作区（主操作靠右）；移动端折叠为抽屉导航。
3. 详情页统一结构：标题区（名称、状态徽标、主操作）→ 概览卡 → 标签页（概览/检测/监控/终端/文件/日志）。
4. 所有危险操作（删除/回滚/恢复/重启）使用**确认弹窗 + 影响面描述**。
5. 空状态必须给出「下一步该做什么」的引导按钮。
6. 表单错误就近展示，不用全局 toast 代替字段级错误。

## 47. 验收标准（新增 `scripts/verify_ops.sh`）

1. `make build` / `gofmt` / `go vet` / `go test ./...` 全绿；既有 6 个脚本全绿（**不得回归**）。
2. **审计**：执行创建服务器/发布回滚/删除凭据等写操作后，`GET /audit-logs` 能查到对应 action、actor、IP、状态码；**响应中不含明文密码/密钥**；viewer 角色访问返回 403。
3. **RBAC**：viewer 执行写操作 → 403；operator 管理用户 → 403；admin 正常；禁止删除最后一个 admin。
4. **API Token**：创建后返回一次明文；用该 token 调 API 成功且 `last_used_at` 更新；撤销后 401；明文不再出现于任何接口。
5. **分页**：`?limit=2` 返回 `page.has_more` 与 `next_cursor`，用 cursor 取下一页不重复；不传 limit 仍返回数组。
6. **批量导入**：导入 3 行（含 1 行重复、1 行注释）→ `created=2 failed=1`，服务器确实存在。
7. **终端**：`LEADSTACK_TERMINAL_ENABLED=true` 时申请 ticket → WS 连接 → 执行 `echo ls-terminal-ok` → 收到该输出；关闭开关时 403；审计有 `terminal.open`。
8. **文件**：上传一个小文件到目标机 → 列目录可见 → 下载内容一致 → 删除后不可见（ssh 通道）。
9. **定时任务**：创建 `* * * * *` 任务（tick 压到 1s）→ 等待后产生 run 记录且 exit_code=0；非法 cron → 400。
10. **日志/进程/端口**：`/logs` 返回目标机文件末几行；`/processes` 与 `/ports` 返回非空结构。
11. **Agent 升级**：`POST /servers/{id}/agent-upgrade` 指向一个 sha256 错误的包 → 失败且旧版本仍在运行（不断连）。
12. **前端**：语言切换 zh↔en 生效且刷新后保持；主题三态与 5 种主题色、三档字号、密度切换均生效并持久化；导航分组与面包屑存在；390px 无横向溢出。
13. **Docker / GoReleaser**：`goreleaser release --snapshot --clean` 成功产出 5 平台 10 个二进制；docker 可用时 `docker build` 成功且容器 `/healthz` 返回 200（不可用则记录为环境受限）。

## 48. 测试夹具补充：devssh 支持 PTY 与 SFTP

`internal/devssh` 除原有 exec 能力外，扩展支持：
- `pty-req` + `shell`（服务端用 `creack/pty` 起本机 `sh`，支持 `window-change`）；
- `sftp` 子系统（`pkg/sftp` 服务端实现）。

**它仍然只是 DEV-ONLY 测试夹具**，不参与发布产物（`make cross` 有断言拦截），生产部署不会用到。扩展目的是让「终端 / 文件传输」在 ssh 通道上也有真实端到端验收端点。

---

## 49. 端口与网络自定义

三处端口需要可由用户自定义，**全部在控制台内配置，不再依赖改环境变量**：

| 项 | settings key | 作用 | 生效方式 |
|---|---|---|---|
| **控制台监听地址** | `network.listen` | Web/API/Agent WS 的监听地址（`host:port`，如 `0.0.0.0:8080`） | **重启控制端后生效** |
| **控制台对外地址** | `network.public_url` | 目标机回连用的地址（生成 install.sh 命令、上传制品下载地址） | 立即生效 |
| **目标机默认 SSH 端口** | `servers.default_ssh_port` | 新增/批量导入服务器时未填端口时的默认值（默认 22） | 立即生效 |
| 单台目标机端口 | `servers.port` | 每台机器独立（建服时可改，**且允许事后修改**） | 立即生效 |

**优先级**：settings（控制台设置）> 环境变量（`LEADSTACK_LISTEN` / `LEADSTACK_PUBLIC_URL`）> 内置默认。
启动日志必须打印实际生效值与来源（`settings` / `env` / `default`），便于排障。

### 49.1 REST

```
GET  /system/network -> {data:{
  listen,               // 配置值（未配置时为空）
  listen_source,        // settings | env | default
  active_listen,        // 当前进程实际监听地址
  public_url, public_url_source,
  default_ssh_port, default_ssh_port_source,
  restart_required      // true 表示配置值 ≠ 当前生效值
}}
PUT  /system/network -> {listen?, public_url?, default_ssh_port?}   // 校验并持久化
POST /system/network/reset -> 清空以上配置，回落 env/默认
```

**校验规则（失败 400 且给中文原因）**
- `listen`：形如 `host:port`；port 1–65535；host 为空或 `0.0.0.0`/`::`/具体 IP/主机名均可；**保存前尝试 bind 一次**，端口被占用 → 400 并提示换端口（避免重启后起不来）。**例外**：提交的值与当前 `active_listen` 相同时跳过探测（幂等保存，不能自己撞自己）。
- `public_url`：必须以 `http://` 或 `https://` 开头；不能是 `0.0.0.0`/`::`（目标机连不上）；**不允许空串**（清空请用 `POST /system/network/reset`），错误文案须提示这一点。
- `default_ssh_port`：1–65535。

### 49.2 目标机端口可改

`PATCH /servers/{id}` 扩展支持 `host`、`port`：允许修改（改完需重新测试连通性）；修改时若 `host:port` 变化，**清除该机的主机密钥记录**并写审计 `server.update`（detail 记录旧值→新值）。

### 49.3 验收（追加到 `scripts/verify_ops.sh`）

1. `GET /system/network` 返回 `listen_source`/`active_listen`/`default_ssh_port`。
2. `PUT` 一个**被占用**的端口 → 400；`PUT` 非法 `public_url`（`ftp://x`、`0.0.0.0:8080`）→ 400；非法 `default_ssh_port` → 400。
3. `PUT` 合法值后 `GET` 返回新值且 `restart_required=true`。
4. **重启控制端后** `active_listen` 变为新值，`restart_required=false`（验收时用一个空闲端口重启一次验证）。
5. `default_ssh_port=2222` 后，`POST /servers` **不传 port** → 该服务器 port 为 2222。
6. `PATCH /servers/{id}` 改 port 成功，且 `GET` 返回新端口。
7. 审计中存在 `system.network_update`（或等价 action）。

---

## 50. 监听端口不可自锁（逃生通道）

**问题**：如果把"控制台监听端口"只放在控制台里配置，就存在死锁 —— 默认端口被占用/被封禁时，你打不开控制台去改它；而 `POST /system/network/reset` 同样要求服务已经起来。**这是必须消除的可用性缺陷。**

### 50.1 永不锁死：启动绑定回退链

启动时按优先级依次尝试，**任一成功即启动**，绝不因为"配置的端口绑不上"而退出：

| 顺位 | 来源 | 说明 |
|---|---|---|
| 1 | **显式命令行 `--listen`** | 最高优先级，用于紧急覆盖（break-glass），即使 settings 里存了坏值也能起来 |
| 2 | 控制台设置 `network.listen` | 正常路径 |
| 3 | 环境变量 `LEADSTACK_LISTEN` | 部署脚本路径 |
| 4 | 默认 `127.0.0.1:8080` | 内置 |
| 5 | **`127.0.0.1:0` 临时端口** | 全部失败时的最后兜底（系统分配空闲端口） |

- 每次回退都必须 **WARN 日志** 打印：失败的候选、失败原因、最终实际地址。
- 临时端口启动时，日志**必须显著打印** `active_listen` 与「请用 leadstack network --listen 修正」的提示。
- 只有连临时端口都无法绑定（系统网络不可用）才退出。
- `GET /system/network` 增加 `fallback_reason`（空串表示未回退），UI 据此显示警示横幅。

### 50.2 离线逃生：CLI 子命令 `leadstack network`

**不依赖服务运行**（直接打开数据库读写 settings），用于控制台打不开时修正：

```bash
leadstack network --show                       # 显示 settings / env / default / 实际生效建议
leadstack network --listen 0.0.0.0:9090        # 写入设置（下次启动生效）
leadstack network --public-url http://1.2.3.4:9090
leadstack network --default-ssh-port 2222
leadstack network --reset                      # 清除全部网络设置，回落 env/默认
```
- 输出必须给出「当前 settings / 环境变量 / 默认值」三者的实际取值，并提示「需要重启控制端生效」。
- 若数据目录被占用（服务在跑），允许**直接写库**（下次启动生效），但要提示「服务正在运行，需重启生效」。

### 50.3 控制台提示

设置页「网络与端口」必须写明：
1. 改监听端口后**需重启**才生效；
2. 若新端口无法绑定，服务会**自动回退**到环境变量/默认/临时端口并在日志打印实际地址；
3. 万一打不开控制台，用 `leadstack network --listen <新地址>` 修正，或用 `leadstack --listen <地址>` 临时启动。

### 50.4 验收（追加到 `scripts/verify_ops.sh`）

1. **回退链路**：占用端口 O 并写入 settings；以 `LEADSTACK_LISTEN=空闲端口 F` 启动 → 服务**仍能启动**并监听 F，日志含 WARN 与 `source=env`，`/system/network` 的 `fallback_reason` 非空。
2. **命令行优先**：settings 存坏值（被占用端口）时，用 `--listen 空闲端口 G` 启动 → 监听 G（命令行压过 settings）。
3. **离线 CLI**：服务停止时 `leadstack network --listen <新值>` 能写入并回显；`--show` 输出三者取值；`--reset` 后设置清空。
4. 以上任一场景下，控制台 `/healthz` 必须可达（即"人能进去改回来"）。

---

## 51. 控制端自升级（控制台提示 → 一键升级 → 自动备份 → 失败自动回退）

**总原则**：升级是**用户主动点击**触发的（不后台偷跑）；**没有备份就不升级**；**升级失败由独立 watchdog 自动回退**（含数据恢复）。

### 51.1 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `LEADSTACK_UPDATE_REPO` | `maliang/leadstack` | 检查更新的 GitHub 仓库 |
| `LEADSTACK_UPDATE_API_BASE` | `https://api.github.com` | API 基址（测试/镜像用） |
| `LEADSTACK_UPDATE_CHECK_INTERVAL` | `6h` | 自动检查间隔（0 = 只在手动刷新时检查） |
| `LEADSTACK_UPGRADE_ENABLED` | `true` | 是否允许控制台自升级（false 时 apply 返回 403） |
| `LEADSTACK_UPGRADE_ROLLBACK_TIMEOUT` | `120s` | watchdog 等待新版本确认成功的上限 |

### 51.2 检查更新

```
GET  /system/update            -> 缓存结果（超过 interval 才真正请求 GitHub）
POST /system/update/check      -> 强制刷新（忽略缓存）
```
响应：
```json
{ "data": {
  "current": "v0.2.0", "latest": "v0.3.0", "has_update": true,
  "published_at": 1710000000000, "release_url": "https://github.com/…",
  "notes": "…release body 前 4KB…",
  "schema_changed": true,          // 该版本包含数据库迁移（需提示用户）
  "assets": [{"os":"linux","arch":"amd64","url":"…","size_bytes":123}],
  "upgradable": true,              // 当前平台/架构在 assets 中存在
  "checked_at": 1710000000000, "error": ""   // 检查失败时 error 非空且保留旧数据
}}
```
**实现要求**：GitHub API 失败（离线/限流）**不得影响其它功能**，返回 `error` 并保留上次结果；响应体只取 release notes 前 4KB。

### 51.3 执行升级

```
POST /system/update/apply  {"version":"v0.3.0"}   -> 202 {"job_id":"…"}
GET  /system/update/status                        -> 进度与结果
```
**流程（严格按序，任一步失败即中止）**

1. 校验：目标版本 > 当前版本、当前平台有对应 asset、`LEADSTACK_UPGRADE_ENABLED`；
2. **强制备份**（调用备份服务，落到 `<backup_dir>`）：失败 → 中止并返回错误，**绝不继续**；
3. 下载 asset 到 `<data_dir>/upgrade/<version>.tar.gz`（临时文件 + 原子 rename），校验 `checksums.txt` 中的 sha256；
4. 解压取 `leadstack`，校验其为可执行文件；
5. 写入升级标记 `<data_dir>/upgrade/state.json`：
   `{target_version, prev_exe, new_exe, backup_file, started_at, deadline, watchdog_pid}`；
6. A/B 替换：`<exe>` → `<exe>.prev`，新文件 → `<exe>`（同一文件系统 rename，原子）；
7. **spawn 独立 watchdog 进程**（detached，不随父进程退出）：
   `leadstack upgrade --watchdog --state <state.json>`；
8. 旧进程退出（unix `syscall.Exec` 新二进制；Windows 不支持自升级 → 提前 403）。

**watchdog 语义（自动回退的关键）**

- 轮询 `state.json` 是否被新进程删除（新进程自检成功后删除它）；
- 成功：清理 `.prev`（保留 N 天由配置），写 `upgrade-history`（成功记录）；
- 超时（`rollback_timeout`）或发现新进程未监听：**回退** ——
  1. `<exe>` → `<exe>.failed-<ts>`，`<exe>.prev` → `<exe>`；
  2. 若本次升级执行了迁移（`schema_changed`），**把数据恢复为升级前备份**（`backup restore`，旧数据保留在 `.pre-restore-*`）；
  3. 重启服务（`systemctl restart` / launchd kickstart；无服务管理器时直接 exec 旧二进制）；
  4. 写 `upgrade-history`（失败记录：原因、回退时间、恢复的备份文件）。

**新进程自检**（启动时，在监听成功后 ≤10s 内）：`PRAGMA integrity_check` 通过、schema 版本等于本二进制支持版本、数据目录可写 → 删除 `state.json`。任一失败则不删标记，进程退出让 watchdog 回退。

**安全约束**
- 不允许跨大版本跳跃（目标 `major` 必须相同，或用户在请求里显式 `allow_major=true`）；
- 升级期间拒绝新的升级请求（返回 409）；
- 全程写审计 `system.upgrade_start` / `system.upgrade_success` / `system.upgrade_rollback`；
- 升级端点仅 `admin`。

### 51.4 GitHub 主分支自动发版（CI）

`.github/workflows/release-auto.yml`：
- 触发：`push` 到 `main`（排除 `docs/**`、`**.md` 等纯文档提交；`[skip release]` 可跳过）；
- 步骤：取最新 tag → 语义化 **patch 自增**得新版本 → 创建 tag → `goreleaser release --clean`（用 `GITHUB_TOKEN`）；
- 产物即 5 平台 × 2 二进制 + archives + `checksums.txt`；
- 并发保护：同一分支同时只跑一个 release（`concurrency`），避免重复 tag；
- 保留手动触发（`workflow_dispatch`，可指定 version）。
- `schema_changed` 的判定来源：发布前对比迁移目录 `internal/store/migrations/` 在上一 tag 与本 tag 之间是否有新增文件，写入 release body 的机器可读标记 `<!-- schema_changed: true -->`。

### 51.5 验收（追加到 `scripts/verify_ops.sh`）

1. `GET /system/update` 在 mock GitHub API 下返回 `current/latest/has_update/schema_changed`；断网（指向不可达地址）时返回 `error` 且**不影响** `/healthz` 与其它接口。
2. `POST /system/update/apply` 在**备份失败**时返回错误且不替换二进制（用只读备份目录模拟）。
3. 正常升级（本地 mock 提供 v+1 的 tarball 与 checksums）：升级后 `/healthz.version` 变为新版本，`<backup_dir>` 产生**升级前**的备份，`<exe>.prev` 存在。
4. **失败自动回退**：mock 提供的"新版本"是一个**一启动就崩溃**的假二进制 → watchdog 超时后把 `.prev` 换回，服务恢复到旧版本且 `/healthz` 可用；`upgrade-history` 有失败与回退记录。
5. `LEADSTACK_UPGRADE_ENABLED=false` → apply 返回 403；非 admin → 403。
6. CI 配置：`release-auto.yml` 在 `push main` 下能算出 patch+1 的版本并创建 tag（本地用 `act` 不可用则做静态断言：触发条件、版本计算脚本可独立执行、`concurrency` 存在）。

## 52. 权限模型（权限点 / 内置角色 / 自定义角色）

> 本节冻结「谁能做什么」。**权限按能力判定，不再只看角色等级**；角色等级（admin > operator > viewer）只保留给兼容校验与「最后一个管理员」这类保护。

### 52.1 权限点（冻结，共 24 个）

| key | 含义 | 可授予自定义角色 |
|---|---|---|
| `server.read` | 查看服务器/分组/指标 | ✅ |
| `server.write` | 纳管、改配置、测试连通、刷新主机信息、调整分组归属 | ✅ |
| `server.delete` | 删除服务器 | ✅ |
| `terminal.use` | 终端 / 文件 / 日志 / 进程 / 端口 | ✅ |
| `check.read` | 查看检测项与结果 | ✅ |
| `check.write` | 增删改检测项、设目标、立即运行 | ✅ |
| `alert.read` | 查看告警 | ✅ |
| `alert.write` | 规则 / 渠道 / 静默 / 确认 | ✅ |
| `deploy.read` | 查看应用、模板、制品、发布 | ✅ |
| `deploy.write` | 应用/模板/制品/发布，含重试、回滚、取消 | ✅ |
| `backup.read` | 查看备份与备份设置 | ✅ |
| `backup.write` | 创建备份、改备份设置 | ✅ |
| `backup.delete` | 删除备份 | ✅ |
| `backup.restore` | 校验与恢复（含上传恢复） | ✅ |
| `task.read` | 查看定时任务与执行历史 | ✅ |
| `task.write` | 增删改定时任务、立即执行 | ✅ |
| `group.write` | 分组 CRUD 与成员调整（**额外要求 scope=all**） | ✅ |
| `agent.upgrade` | 单机/批量升级 agent | ✅ |
| `user.manage` | 用户增删改、解锁 | ❌ 仅内置 admin |
| `role.manage` | 角色与范围管理 | ❌ 仅内置 admin |
| `token.manage` | API Token 管理 | ❌ 仅内置 admin |
| `enroll.manage` | 注册令牌管理 | ❌ 仅内置 admin |
| `system.manage` | 系统设置、网络、自升级 | ❌ 仅内置 admin |
| `audit.read` | 审计日志 | ❌ 仅内置 admin |

### 52.2 内置角色权限集合（冻结）

| 角色 | 权限 |
|---|---|
| `viewer` | `server.read` `check.read` `alert.read` `deploy.read` `backup.read` `task.read` |
| `operator` | viewer 全部 + `server.write` `terminal.use` `check.write` `alert.write` `deploy.write` `backup.write` `task.write` `group.write` |
| `admin` | 全部 24 个（含 6 个管理权限） |

内置角色**不可改名、不可改权限、不可删除**（403）。它们的范围恒为 `all`。

### 52.3 自定义角色

- `key`：`r_` + 8 位小写十六进制（服务端生成）；`name`：1–32 字符且唯一；`description` ≤ 200；
- `permissions`：必须是非空的**可授予**子集（含管理权限 → 400，防止自我提权），重复项去重后落库；
- `scope`：`all` / `groups` / `servers`（见 §53）；
- 删除前必须无用户引用（409）；改名不影响 `users.role`（引用的是 key）；
- **即时生效**：JWT 只携带角色 key，权限每次请求按 key 解析，不做 JWT 内嵌。

### 52.4 中间件

- `RequirePerm(perm)`：解析调用者角色的权限集合，缺失 → 403 `codeForbidden`（消息含所需权限 key），并写审计与日志；
- 路由**逐条**声明所需权限（同一路径的 GET 与写方法可以不同权限）；
- `RequireRole(minRole)` 保留，仅用于「必须是内置等级」的兼容场景（如最后一个管理员保护）。

## 53. 服务器分组与资源级授权

### 53.1 分组（新增一等公民）

- 平铺分组（无嵌套）；服务器与分组**多对多**；分组名 1–32 字符唯一；
- `servers.tags`（自由标签，用于筛选）保留，与分组并存、互不影响：**标签=筛选口径，分组=授权单元**；
- 删除分组：一并清理成员关系与 `role_scopes` 中对该分组的引用，不删服务器；
- 分组写操作（创建/改名/删除/调整成员）需要 `group.write` **且** 调用者范围必须是 `all`（否则可自我扩权）。

### 53.2 角色范围

`roles.scope_mode` + `role_scopes(role_key, kind, target_id)`，`kind ∈ {group, server}`：

| mode | 可见/可操作服务器 |
|---|---|
| `all` | 全部 |
| `groups` | 授权分组内服务器 ∪ 显式授权的单台服务器 |
| `servers` | 仅显式授权的单台服务器 |

允许 `groups`/`servers` 混用（两者求并集）；`mode=all` 时忽略 `role_scopes` 行（落库时清空）。

### 53.3 判定规则（冻结）

1. **对象级**（`/servers/{id}/…` 全部子资源，含终端、文件、日志、进程、端口、指标、agent 升级、发布运行）：目标不在范围内 → **404**（不泄露存在性），不返回 403。
2. **列表级**（servers / checks / alerts / releases / scheduled-tasks）：只返回范围内对象；范围为空 → 空数组（不是 403）。
   - checks：只要有**任一**目标服务器在范围内即可见；写操作（`check.write`）要求**所有**目标都在范围内；
   - alerts：按 `server_id` 判定；规则级（无 `server_id`）告警仅 `all` 范围可见；
   - releases / scheduled-tasks：按目标服务器判定，创建/重试/运行要求目标全部在范围内。
3. **全局资源**（要求 `scope=all`，且具备对应权限）：备份与备份设置、审计日志、系统设置/网络/自升级、用户/角色/Token/注册令牌、应用与模板定义、告警规则与渠道（规则是全局策略，不允许局部持有）。
4. **写越界** → 403；**读越界** → 404。删除/批量操作对越界目标整体失败并列出服务器名。

### 53.4 实现与代价

- 每个请求解析一次范围（1–3 条 SQLite 查询），列表过滤在内存完成；
- 分页语义：**先过滤后分页**，受限用户单页可能少于 `limit`（`docs` 明示该限制）。

## 54. 登录限流与失败锁定

- **账号维度（持久化）**：连续失败达到 `login_max_attempts`（默认 **5**）→ 锁定 `login_lock_duration`（默认 **15m**）；锁定期内即使密码正确也拒绝；成功登录清零；管理员改密码同时清零；
- **IP 维度（进程内滑动窗口）**：`login_ip_window`（默认 60s）内最多 `login_ip_limit`（默认 20）次登录请求，超出 → 429；
- **响应**：
  - 凭据错误 → 401，响应体 `error.details.remaining_attempts` 仅在剩余次数 ≥1 时给出；
  - 账号锁定 → 429，`error.code = "login_locked"`，`error.details.retry_after_s`，并带 `Retry-After` 头；
  - IP 限流 → 429，`error.code = "rate_limited"`，同上。
- **解锁**：`POST /api/v1/users/{userID}/unlock`（`user.manage`）清零失败计数与锁定；响应 `{ok:true}`；
- **审计**：`login_locked`、`login_rate_limited`、`user_unlock` 三类事件入审计日志（`audit_logs`），失败登录沿用既有 `login` 事件；
- **未知用户名**也计入进程内按用户名计数（不落库），避免用户名枚举与内存放大（LRU 上限 4096 条）；
- **CLI**：`--login-max-attempts`、`--login-lock-duration`、`--login-ip-limit`、`--login-ip-window`（0 表示关闭对应保护）。

## 55. REST 新增与变更（本节即契约）

### 55.1 权限目录

`GET /api/v1/permissions`（`role.manage`）→
```json
[{"key":"server.read","name":"查看服务器","description":"…","admin_only":false}]
```

### 55.2 角色

`GET /api/v1/roles`（`role.manage`）→
```json
[{"key":"operator","name":"运维","description":"…","builtin":true,
  "permissions":["server.read","…"],"scope":{"mode":"all","group_ids":[],"server_ids":[]},
  "user_count":2,"created_at":1750000000000,"updated_at":1750000000000}]
```
- `POST /api/v1/roles`：`{"name":"发布员","description":"…","permissions":["deploy.read","deploy.write"],"scope":{"mode":"groups","group_ids":["g1"],"server_ids":[]}}` → 201（返回同上结构）
- `PATCH /api/v1/roles/{key}`：字段均可选；`scope` 整包替换（`group_ids`/`server_ids` 传空数组表示清空）
- `DELETE /api/v1/roles/{key}`：内置 403、被引用 409、`scope` 中引用了不存在分组 → 400

### 55.3 分组

- `GET /api/v1/server-groups`（`server.read`）→ `[{"id","name","description","member_count","created_at","updated_at"}]`
- `POST /api/v1/server-groups`（`group.write` + 全局范围）：`{"name","description"}` → 201
- `PATCH /api/v1/server-groups/{groupID}`：`{"name?","description?"}`
- `DELETE /api/v1/server-groups/{groupID}` → `{"ok":true}`
- `PUT /api/v1/server-groups/{groupID}/servers`：`{"server_ids":["s1","s2"]}`（整包替换）
- `GET /api/v1/server-groups/{groupID}/servers`（`server.read`）→ 服务器精简列表

### 55.4 服务器载荷

`GET /servers`、`GET /servers/{id}`、`PATCH /servers/{id}` 增加：
- 请求：`group_ids: []string`（整包替换；缺省表示不改）
- 响应：`group_ids: []string`、`group_names: []string`

### 55.5 用户

- `GET /users` 每条增加：`role_name`、`locked`(bool)、`locked_until`(ms)、`failed_attempts`(int)
- `POST /users` / `PATCH /users/{id}` 的 `role` 必须是已存在角色 key（内置或自定义），否则 400
- `POST /api/v1/users/{userID}/unlock`（`user.manage`）→ `{"ok":true}`

### 55.6 登录与当前用户

- `POST /api/v1/auth/login` 成功响应增加 `permissions: []string`、`role_name`、`scope:{"mode":"all|groups|servers","group_ids":[],"server_ids":[]}`
- `GET /api/v1/auth/me` 同步增加上述字段
- 失败响应见 §54（401 / 429 两类）

## 56. 迁移 0008 与验收

### 56.1 迁移 `0008_permissions.sql`（`currentSchemaVersion = 8`）

```sql
CREATE TABLE IF NOT EXISTS roles (
    key         TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    permissions TEXT NOT NULL DEFAULT '[]',
    builtin     INTEGER NOT NULL DEFAULT 0,
    scope_mode  TEXT NOT NULL DEFAULT 'all',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS role_scopes (
    role_key  TEXT NOT NULL,
    kind      TEXT NOT NULL,      -- group | server
    target_id TEXT NOT NULL,
    PRIMARY KEY (role_key, kind, target_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS server_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS server_group_members (
    group_id  TEXT NOT NULL,
    server_id TEXT NOT NULL,
    PRIMARY KEY (group_id, server_id)
) WITHOUT ROWID;

ALTER TABLE users ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN last_failed_at  INTEGER NOT NULL DEFAULT 0;
```

内置角色不落 `roles` 表（代码常量），`GET /roles` 会把它们与自定义角色合并返回（`builtin:true`）。

### 56.2 验收（`scripts/verify_authz.sh`）

1. 权限目录 24 项；内置角色权限集合与 §52.2 一致；
2. 自定义角色：`deploy.write` 之外不含 `server.write` 时，创建发布成功、添加服务器 403；
3. 防提权：自定义角色带 `user.manage` → 400；
4. 分组：创建分组、加服务器、按分组授权角色后，受限用户只看到该分组服务器；访问分组外服务器 → 404；越界写 → 403；
5. 列表过滤：受限用户的 `/servers`、`/checks`、`/alerts`、`/releases`、`/scheduled-tasks` 只返回范围内对象；
6. `group.write` 但 `scope=groups` 的用户 → 写分组 403；
7. 锁定：连续 5 次错误密码 → 第 6 次即使密码正确也 429 `login_locked`；管理员 `unlock` 后可登录；锁定与解锁均出现在审计日志；
8. IP 限流：窗口内超限 → 429 `rate_limited` + `Retry-After`；
9. 内置角色不可改不可删（403/409 语义）；
10. 被用户引用的自定义角色删除 → 409。

---

## 57. 宝塔面板部署通道（`provider=baota`）

### 57.1 定位

部署有两条**并列通道**，由应用的 `provider` 选择：

| provider | 执行位置 | 适用场景 |
|---|---|---|
| `builtin`（默认） | 目标机（SSH 直连或 agent 任务） | 通用：systemd/compose/二进制，模板即脚本 |
| `baota` | 宝塔面板 API（控制端发起） | 面板托管的机器：建站 → 上传 zip → 解压即生效 |

`baota` 通道**不需要目标机 SSH**，也不需要 `LEADSTACK_PUBLIC_URL`（制品由控制端读取后直传面板）。
代价是制品必须是 **zip**、单包 ≤ **16MB**（`SaveFileBody` 走 base64），且 v1 只覆盖 `static`/`php` 站点
（node 进程管理、SSL、反代不在 v1 范围，仍可在面板里手工配）。

### 57.2 数据模型（迁移 0009）

```sql
ALTER TABLE apps ADD COLUMN provider       TEXT NOT NULL DEFAULT 'builtin'; -- builtin | baota
ALTER TABLE apps ADD COLUMN site_name      TEXT;   -- 面板站点名，默认取 slug
ALTER TABLE apps ADD COLUMN site_port      INTEGER;-- 站点端口，默认 80
ALTER TABLE apps ADD COLUMN site_type      TEXT;   -- static | php
ALTER TABLE apps ADD COLUMN healthcheck_path TEXT; -- 部署后健康检查路径，空=跳过
ALTER TABLE servers ADD COLUMN panel_credential_id TEXT; -- kind=baota 的凭据
```

约定：
- 站点根目录固定 `/www/wwwroot/<site_name>`；
- `panel_credential_id` 与 SSH 的 `credential_id` **相互独立**（同一台机器可以两者都绑）；
- 面板凭据 `kind=baota`，密文里存 `panel_url` + `api_key` + `insecure_skip_verify`；
  **任何接口都不回显 `api_key`**，`GET /credentials` 只回 `panel_url` 与 `insecure_skip_verify`。

### 57.3 鉴权与签名

面板侧开关：`设置 → API 接口 → 开启`，并把**控制端出口 IP** 加入白名单。
每次请求带 `request_time`（秒）与 `request_token = md5(md5(api_key) + request_time)`，
同时在该请求的 Cookie 里带同一个 `request_token`（兼容校验 Cookie 的面板版本）。
响应 `{"status":false,"msg":"…"}` 统一转成错误，避免「HTTP 200 但没做成」的假成功。

### 57.4 部署流程（每台服务器）

```
1 Ping(GetSystemTotal)          连接/密钥/白名单校验
2 EnsureSite                   站点存在则复用（绝不改写目录/端口），不存在才 AddSite
3 读取制品                      upload 走控制端存储 / url 走下载；校验 sha256；必须 PK zip
4 ZipDir                       备份站点目录到 <站点>.backup-<ts>.zip（best-effort）
5 SaveFileBody + Unzip         上传 /www/wwwroot/<site>-release-<id>.zip 并解压覆盖站点根
6 健康检查                      可选，GET http://<host>:<port><healthcheck_path>，2xx/3xx 通过
```

失败处理：
- 第 5/6 步失败 → 用第 4 步的备份 zip 覆盖回去（best-effort），`failed_step` 记为 `unzip` / `healthcheck`；
- 第 4 步备份失败 → 只告警，不阻断（此时也失去自动回滚能力，日志里明确写出）；
- 面板连接失败 → 直接失败，`message` 透传面板错误（如「签名校验失败」）。

### 57.5 REST

- `POST /api/v1/credentials`：`{"name":"bt","kind":"baota","panel_url":"https://1.2.3.4:8888","api_key":"…","insecure_skip_verify":true}` → 201
- `POST /api/v1/credentials/{id}/test` → `{"ok":true,"message":"面板连接正常"}`；失败也是 200 + `ok:false`（把面板原因原样带出）
- `POST/PATCH /api/v1/apps`：新增 `provider`、`site_name`、`site_port`、`site_type`、`healthcheck_path`
- `POST/PATCH /api/v1/servers`：新增 `panel_credential_id`（空串=解绑；非 baota 凭据 → 400）
- `binary` 模板允许 `content` 为空（表示本次发布不重启服务；baota 通道直接用空模板）

### 57.6 验收（`scripts/verify_baota.sh`，28 项）

用一个**校验签名的 mock 面板**跑完整链路：
1. 创建 baota 凭据 → 连通性 `ok:true`；
2. 错误密钥 → `ok:false`（不误报）；缺 `api_key` → 400；
3. 服务器绑定面板凭据并回显；把 SSH 凭据绑成面板凭据 → 400；
4. 应用 `provider=baota` 落库；非法 provider → 400；
5. 上传 zip → 建发布 → **发布成功**，面板依次收到 `GetSystemTotal/AddSite/SaveFileBody/Unzip`，建站用应用配置的站点名；
6. 非 zip 制品 → 发布失败且信息指明「要求 zip」；
7. 面板密钥错误 → 发布失败且信息含面板原因。

### 57.7 已知限制（诚实清单）

- 未在**真实宝塔面板**上验证过（开发环境没有面板实例）。字段名按 7.x/8.x 社区 SDK 约定实现，
  集中在 `internal/panel/baota/client.go` 的 `endpoints` 常量与各方法参数里，面板版本差异只需改这一处；
  上线前请用控制台「测试连接」+ 一次小 zip 对真机验证。
- 单制品 ≤16MB；更大请用 `builtin`（SSH/agent）通道。
- 只支持 `static`/`php` 站点；`node` 项目、SSL、反向代理不自动配置。
- 回滚粒度是「上一版站点目录的 zip 备份」；多次发布会在站点旁累积 `.backup-<ts>.zip`，
  需要定期清理（面板文件管理里删）。

---

## 58. 部署 v2：接管既有目录、健康检查、首版初始化与变更探测

### 58.1 两种部署模式

| `deploy_mode` | 目录结构 | 适用 |
|---|---|---|
| `release`（默认） | `<app>/releases/<id>` + `current` 符号链接，原子切换 | 新项目、可接受多版本目录 |
| `inplace` | 直接用 `target_dir`（已有目录），每次发布**备份→rsync 覆盖→重启** | 已有站点/服务，目录里有别人的配置与数据 |

`inplace` 的步骤（`internal/deploy/inplace.go`）：

```
prepare → download → verify-sha256 → unpack(临时目录)
→ backup-existing（tar 到 <target>/.leadstack-backups/backup-<release>.tar.gz）
→ sync-inplace（rsync -a --delete，保留 keep_paths 与 .leadstack*）
→ write-env（可选，<target>/.leadstack.env，0600）
→ write-init-script + run-init-script（仅首版）
→ restart（restart_cmd，可选）
→ write-metadata
```

回滚 = 解回备份 tar + 再执行 `restart_cmd`；**首次发布失败也能回滚**（备份在同步之前做）。

### 58.2 应用字段（迁移 0010）

```sql
ALTER TABLE apps ADD COLUMN deploy_mode      TEXT NOT NULL DEFAULT 'release'; -- release | inplace
ALTER TABLE apps ADD COLUMN target_dir       TEXT;   -- inplace 目标目录（空则用 root）
ALTER TABLE apps ADD COLUMN restart_cmd      TEXT;   -- 发布后重启命令（systemctl / docker compose …）
ALTER TABLE apps ADD COLUMN init_script      TEXT;   -- 首版初始化脚本（每台服务器只执行一次）
ALTER TABLE apps ADD COLUMN env_json         TEXT;   -- {"KEY":"VALUE"} → <target>/.leadstack.env
ALTER TABLE apps ADD COLUMN keep_paths       TEXT;   -- 同步时保留（逗号分隔，支持 *）
ALTER TABLE apps ADD COLUMN healthcheck_port INTEGER;-- 健康检查端口
```

- `init_script` 的「首版」判定：该服务器上此应用**没有成功发布记录**（`previousSuccessRelease` 为空）；
- `env_json` 写成 `.env` 时在命令里是 base64，并标记 `Step.Secret`，日志层只显示步骤名；
- `keep_paths` 同时用于备份排除与 rsync 保留（数据目录、上传目录、配置目录）。

### 58.3 健康检查（两条通道共用）

- 配置：`healthcheck_path` + `healthcheck_port`（baota 通道缺省用 `site_port`）；
- 执行：控制端在部署计划**全部成功之后**发 `GET http://<server.host>:<port><path>`，
  2xx/3xx 通过，最多轮询 30 秒；
- 失败：执行计划里的 `RollbackSteps`（release 模式切回上一版本；inplace 模式解回备份），
  再把 run 标记为 `failed`、`failed_step=health-check`，message 里带探测错误。

### 58.4 接管向导（发现 + 一键接管）

`POST /api/v1/servers/{serverID}/discover`（`server.read`）在目标机跑一段**只读**脚本：

```
systemctl list-units（运行中的服务）+ 每个服务的 ExecStart
ss -lntp / netstat -lntp（监听端口）
docker ps（容器名/镜像/端口）
ls -1d /www/wwwroot/*（宝塔站点目录）
```

返回 `{server_id, candidates:[{kind, name, detail, suggested:{deploy_mode,target_dir,restart_cmd,healthcheck_port,root}}], raw:{...}}`。
前端选中候选后预填「新建应用」表单（inplace 模式 + 目标目录 + 重启命令 + 健康检查端口）——
**发现是只读的，接管动作仍然是显式创建应用 + 首次发布会执行备份**。

### 58.5 上传制品的签名下载地址

上传型制品由控制端通过 `/api/v1/artifacts/{id}/download` 提供给目标机，而目标机没有 JWT。因此：

- 发布计划里的下载地址带 `?exp=<unix>&sig=<hmac-sha256(artifactID|exp)>`，默认 30 分钟有效；
- 签名密钥首次启动随机生成并存 `settings.artifact.sign_key`，重启不变；
- 下载路由注册在 `requireAuth` 组**之外**，中间件逻辑：有效签名 → 放行；否则回落 JWT + `deploy.read`；
- 上传制品的下载地址没有扩展名，解包改为**按文件头判断**（zip `PK\x03\x04` / gzip / bzip2 / xz），都不是才当裸二进制安装。

### 58.6 验收（`scripts/verify_deploy_v2.sh`，38 项）

用两个真实项目的部署形态做样本（devssh 让命令在本机真实执行，断言检查真实文件）：

| 样本 | 形态 | 覆盖 |
|---|---|---|
| nengtan（慧碳云枢） | jar + 两个前端 + systemd 服务 + 健康检查 + 首版初始化 | inplace 接管、keep_paths、init 只跑一次、.leadstack.env 0600、restart、健康检查失败回滚 |
| tuoluojiang（拓罗匠） | php 站点 + compose 数据卷 | 数据目录（mysql/redis data）在两次发布后完好、旧文件被 rsync --delete 清掉、compose 重启命令执行 |

另含：接管向导 discover 返回 candidates/raw；二次发布不重复执行 init。

---

## 59. 部署模式 script：脚本驱动（含 git pull 更新）

### 59.1 定位

第三种部署模式（`deploy_mode=script`）：**不需要制品**，一次发布＝在目标机执行用户给的更新脚本。
适合「服务器上已有项目、更新方式是 `git pull` + 重启脚本」这类流程（例如宝塔直装的 LaravelS 项目：

```bash
#!/bin/bash
set -eu
cd /www/wwwroot/tuoluojiang
git pull --ff-only
composer install --no-dev --no-interaction
bash start.sh restart
```

平台负责的是：逐台执行、实时日志、失败标记、健康检查、可选的自动回滚脚本。

### 59.2 字段与语义

```sql
ALTER TABLE apps ADD COLUMN rollback_script TEXT; -- 可选回滚脚本（迁移 0011）
```

| 字段 | 说明 |
|---|---|
| `deploy_mode` | `script`；模板必须是 **binary**，模板内容即更新脚本（发布时冻结快照，改模板不影响已发布记录） |
| `target_dir` | 脚本工作目录（绝对路径；为空回落到 `root`）；脚本会被 `cd` 到这里执行 |
| `rollback_script` | 发布脚本失败或健康检查不通过时执行（≤64KB） |
| `healthcheck_path` + `healthcheck_port` | 脚本跑完后控制端发 HTTP 探测，失败 → 执行回滚脚本并标 `health-check` |
| 制品 | **可省略**；填了也不会被使用（脚本自己负责取代码） |
| `keep_paths` / `restart_cmd` | 不适用（脚本里自己做），UI 会隐藏 |

发布步骤（`internal/deploy/script.go`）：

```
prepare（mkdir <work>/.leadstack/scripts）
→ write-deploy-script（写 <work>/.leadstack/scripts/<release>.sh，0755）
→ run-deploy-script（cd <work> && bash <script>）
→ write-metadata
```

回滚步骤：`write-rollback-script` → `run-rollback-script`（失败只记日志，不掩盖原始错误）。

### 59.3 回滚顺序约定（重要）

`Plan.RollbackSteps` 由执行器按**逆序**执行（见 §25.1）。因此构造器里要按「期望执行顺序的倒序」书写：

| 模式 | 期望执行顺序 | RollbackSteps 书写顺序 |
|---|---|---|
| script | 写回滚脚本 → 跑回滚脚本 | `[run-rollback, write-rollback]` |
| inplace | 解回备份 → 重启 | `[restart, restore]` |
| release(binary/systemd/compose) | 切回上一版本 → 重启旧版 | `[restart, switch-current]` |

健康检查失败时由编排器执行同一份 `RollbackSteps`，同样按逆序展开，两条路径语义一致。

### 59.4 验收（`scripts/verify_deploy_script.sh`，24 项）

用真实 git 仓库模拟 tuoluojiang 的更新方式（devssh 真实执行）：

1. 源仓库 v1 → 克隆到站点目录 → 脚本发布成功，仓库停在 v1；
2. 源仓库 v2 → 再发布 → `git pull` 生效，站点文件更新；
3. 坏脚本（pull 后 exit 1）→ 发布 failed，`failed_step=run-deploy-script`，回滚脚本执行，仓库回到发布前 commit；
4. 健康检查端口改坏 → 发布 failed，`failed_step=health-check`，回滚脚本执行；
5. 脚本模式创建发布**不需要 artifact**；引用不存在的模板 → 404。

---

## 60. 部署打磨：发布预览、内置预设、接管向导识别 git

### 60.1 发布预览（dry-run）

`POST /api/v1/releases/preview`（`deploy.read`）——请求体与创建发布一致，**只读**：不落库、不执行。

```json
{"data":{"app_id":"…","app_slug":"…","version":"1.0.9","provider":"builtin","deploy_mode":"inplace",
  "servers":[{"server_id":"…","server_name":"…","plan_root":"/www/wwwroot/shop",
    "steps":[{"name":"prepare","command":"mkdir -p …","timeout_s":60},
             {"name":"write-env","command":"","timeout_s":30,"secret":true}],
    "rollback_steps":[…],"notes":["inplace 模式未配置 keep_paths：…"]}]}}
```

- 复用真实的计划构造器（`buildPlanFor`），保证预览与执行**同源**；
- `Step.Secret`（写环境变量）不回显命令，只给 `secret:true`；
- `provider=baota` 时返回动作序列（`panel.ping / panel.ensure-site / artifact.read / panel.backup / panel.upload / panel.unzip / healthcheck`）而不是 shell；
- `notes` 是风险提示：keep_paths 为空、未配健康检查、缺制品、没有回滚步骤等；构建失败也写进 notes（不返回 500）；
- 版本用 `"preview"` 作为临时 releaseID 参与渲染，不污染数据库。

### 60.2 内置部署预设

`GET /api/v1/deploy/presets`（`deploy.read`）返回 6 个预设，前端「从预设创建」＝建模板 + 打开新建应用并回填 defaults：

| key | 形态 | 关键提示 |
|---|---|---|
| `git-pull` | 脚本模式 + `git pull` + 重启 | 工作目录=仓库根；回滚脚本 `git reset --hard HEAD@{1}` |
| `static-takeover` | inplace 接管静态站 | keep_paths 必填，否则同步会删文件 |
| `baota-site` | 面板 API（zip） | 需先建面板凭据并测试连接 |
| `systemd-binary` | release + systemd unit | `EnvironmentFile=-{{ROOT}}/current/.leadstack.env` |
| `compose-stack` | compose（image 制品） | 数据目录用宿主机绝对路径 |
| `laravel-swoole` | 脚本模式 LaravelS | keep_paths：storage/、public/uploads/、.env、vendor/ |

预设只是**数据**（模板内容 + defaults + hints），不引入新的执行路径。

### 60.3 接管向导识别 git 仓库

发现的站点目录如果是 git 仓库（`<dir>/.git`），`suggested.deploy_mode` 直接返回 `script`，
并在 `detail` 里带上分支与远端（`路径 · git(master ssh://…)`）——这类项目的更新方式就是 `git pull`，
前端据此推荐「脚本模式 + git-pull 预设」。

---

## 61. 灰度发布、回滚到指定版本、接管体检

### 61.1 灰度发布（canary）

创建发布时可带：

```json
{"strategy":"canary","canary_count":1,"hold_seconds":120}
```

- `strategy=all`（默认）全量并发；`canary` 先发 `canary_count` 台（按 server_id 排序取前 N）；
- 第一批完成后等待 `hold_seconds`（0 表示不等待）；**观察期内可取消**；
- 观察结束再确认第一批仍成功，才发剩余批次；
- 任一停止条件命中 → 剩余 run 标记 `canceled`（message 说明原因），发布整体按已执行结果汇总；
- 约束：服务器 ≥2、`1 ≤ canary_count ≤ 服务器数-1`、`hold_seconds ∈ [0,3600]`；
- 数据库：`releases.strategy/canary_count/hold_seconds`（迁移 0012）。

### 61.2 回滚到指定历史版本

- `GET /apps/{appID}/rollback-targets?server_id=`：列出该应用发布成功过的版本（可选按服务器过滤）；
- `POST /releases/{id}/rollback {target_release_id}`：
  - **release 模式**：生成「切 current 到 `releases/<target>` + 跑该版本启动脚本」的计划；
  - **inplace 模式**：备份名是 `backup-<releaseID>`，存的是「该次发布之前」的状态，
    所以回滚到版本 X 要恢复**「X 之后替换它的那次发布」**的备份（自动找后继成功版本）；目标已是当前版本时跳过；
  - **script 模式**：返回 400（git/外部状态不由平台管理），提示用回滚脚本；
- 被回滚的 run 的 message 写「已回滚到版本 X」。

### 61.3 接管体检

`POST /servers/{serverID}/diagnose {kind,name,target_dir}`（只读）：

```
目录（存在/可写/磁盘）→ git（分支/未提交数/remote）→ systemd（ExecStart/WorkingDirectory/User/单元文件/状态）→ 监听端口
```

返回 `checks[]`（pass/warn/fail + detail）与 `recommendation`：
git 仓库 → 建议 `script` 模式并给出 `git pull --ff-only && bash start.sh restart` 脚本；
否则建议 `inplace`；systemd 候选给出 `systemctl restart <unit>`、ExecStart 是否落在目标目录内作为 warn 提示。

---

## 62. 本机接入（agent 一键安装）

### 62.1 为什么走 agent 而不是 SSH

同一台机器上用 SSH 也能部署，但两个问题：要给控制端开 sshd；且 SSH 通道是**轮询**（默认 30s 采集、连续 3 次失败才判离线，最差 ~90s）。
agent 常驻后是**推送**：心跳 20s、指标 15s、45s 无心跳判离线，部署任务走长连下发，状态更接近实时。

### 62.2 接口

`POST /api/v1/servers/local`（`enroll.manage`，admin-only）：

```json
{"name":"本机（mac-mini）","ttl_s":3600,"server_url":"http://127.0.0.1:8080"}
```

→ 201：
```json
{"data":{"server":{…mode=agent, host=127.0.0.1…},"token":"<一次性明文>","expires_at":…,
  "command":"curl -fsSL 'http://127.0.0.1:8080/install.sh' | sh -s -- --server 'http://127.0.0.1:8080' --token '…'"}}
```

- 一次性完成：建服务器（mode=agent）+ 建接入令牌（预绑定该服务器）+ 生成安装命令；
- 地址推导：显式 `server_url` > 请求 Host 的端口 + 回环地址 > `127.0.0.1:8080`（本机 agent 只走回环）；
- 令牌明文只返回一次；`ttl_s` 默认 1 小时，范围 60s–30d；
- 执行命令后 agent 起来 → 服务器状态转 online（agent 推送），随后即可用三种部署模式。

### 62.3 权限与边界

- 端点要求 `enroll.manage`（内置 admin 独占；自定义角色含该权限会被 400 拒绝，见 §52）；
- 执行安装命令需要目标机器 root（安装 systemd/launchd 服务），控制端不代跑安装；
- 本机 agent 与控制端同机时，注意控制端端口变更后要重新生成命令（地址是生成时快照）。

---

## 63. 受限网络：agent 代检、可信代理、自签证书

目标机常常「只允许出向、不给开端口、只有 IP、没有公网证书」。本节定义为此新增的三处能力。

### 63.1 为什么 agent 模式不需要目标机开端口

agent 是**出向客户端**：它主动拨控制端的 `/agent/ws`（`internal/api/router.go`），携带 Bearer 令牌完成握手，之后心跳/指标/任务全部复用这条长连接；部署任务、网页终端也走这条连接下发（`internal/api/deploy_runner.go` 的 `deployRoutingRunner`、`internal/api/handlers_terminal.go` 的 `term.open`）；制品由目标机自己按 `ARTIFACT_URL` 拉取（`internal/deploy/build.go` 的 `VarArtifactURL`）。因此目标机侧**没有任何监听端口**——`cmd/leadstack-agent` 的参数只有 `-server/-token/-once/-verbose/-version/-ca/-insecure`。

真正需要可达性的是反方向：**目标机要能连到控制端**。控制端地址可以用 IP、可以走 nginx 反代、可以走 `ssh -L` 隧道。

硬边界（模式无法绕过）：

- 检测项 `tcp/http/ping` 默认由**控制端直连**目标，目标在 NAT 后探不到 → 用 63.2 的 agent 代检；
- `process/script` 本来就在目标机执行，不受影响；
- 部署健康检查由控制端发 HTTP（`healthcheck_port` 留空则跳过）；
- 目标机完全不能出网：agent 与 SSH 都不可用。

### 63.2 agent 代检（`probe_via=agent`）

检测项 `params.probe_via` 取 `control`（默认）或 `agent`：

```json
{"name":"内网端口","kind":"tcp","target":"10.0.0.9:3306",
 "params":{"probe_via":"agent"},"server_ids":["…"]}
```

- 只适用于 `tcp`/`http`/`ping`；`process`/`script` 传该参数返回 400（`params.probe_via 只适用于 tcp/http/ping（process/script 本身就在目标机执行）`）；
- 非法值返回 400（`params.probe_via 只能是 control 或 agent`）；空串视为未设置并删除键；
- 参数存在 `checks.params_json`，**无需迁移**（`internal/store/checks.go` 的 `checkColumns` 已含该列）。

执行链路：`checker.Probe` 发现 `probe_via=agent` 且 kind 属于 tcp/http/ping 时，走可选接口 `checker.AgentProber.ProbeOnTarget`（`internal/api/checks_wiring.go` 的 `RoutingExecutor` 实现）→ `jobs.Dispatcher.Probe` 打包成 `job.exec{kind:"probe", command:"<ProbeRequest JSON>"}` → agent 的 `jobManager.runProbe` 用 `checker.ProbeLocally` 本地执行 → 单帧 `job.log{stream:"stdout"}` 回传 `ProbeResult` JSON → 控制端解析后写结果。

契约要点：

- **不静默降级**：SSH 接入的服务器、agent 未启用、执行通道不支持时都返回 `ok=false` 加明确中文原因（例如「该服务器以 SSH 方式接入，无法由 agent 代检…」），绝不退回控制端直连；
- 结果必须经 `job.log` 回传，而不是 `job.done`（控制端 pending 是按 `job.log` 聚合 stdout 的，`job.done` 只带 exit_code/error）；
- `ProbeLocally` 会复制 params 并删掉 `probe_via`，避免 agent 侧再次路由（自我递归）。

### 63.3 可信代理与 `X-Forwarded-For`

默认**不信任任何代理头**（`remoteHost` 只取 TCP 对端，防伪造）。配置白名单后才会解析：

```bash
LEADSTACK_TRUSTED_PROXIES="127.0.0.1/32,10.0.0.0/8"   # 或 --trusted-proxies
```

- 取值：逗号分隔的 CIDR 或单个 IP（单个 IP 归一为 `/32` 或 `/128`）；非法值在启动时报 `配置错误: trusted-proxies 中的 "…" 不是合法 CIDR`；
- 解析规则（`clientIPWith`）：对端不在白名单 → 直接用对端；在白名单 → 合并所有 `X-Forwarded-For`（多个头按序拼接）后按逗号拆分、去空白、去端口/方括号、忽略 `unknown`，**从右往左取第一个不在白名单里的地址**（左侧都可被客户端伪造）；无 `X-Forwarded-For` 时退化为 `X-Real-IP`；都不可用时回落对端；
- 影响范围：登录 IP 限流的分桶、审计日志的 `ip` 字段、WebSocket 连接日志。

它解决的是「反代之后所有请求看起来都来自 nginx」：审计失真 + 登录限流退化成全站共用一个桶。

### 63.4 agent 侧 TLS（`--ca` / `--insecure`）

- `--ca <pem>`：`x509.SystemCertPool()` 之后追加该文件的证书（**追加而非替换**），仍校验证书链与主机名；文件读不到或没有可用 PEM 时**启动即失败**（`读取 CA 文件 … 失败` / `CA 文件 … 中没有可用的 PEM 证书`），不会退化成反复重连；
- `--insecure`：`InsecureSkipVerify=true`，启动时打 Warn；仅用于无法安装 CA 的受控链路；
- 两者可同时出现（先跳过校验，CA 无关）；
- 自签证书必须带目标机访问时使用的 IP/域名 SAN，否则报 `x509: cannot validate certificate for … because it doesn't contain any IP SANs`；
- `deploy/install.sh` 同步支持 `--ca PATH`（要求绝对路径且可读）与 `--insecure`，并写进 systemd `ExecStart` / launchd `ProgramArguments`。

### 63.5 验收

- 单元：`internal/checker/probe_via_test.go`、`internal/jobs/probe_test.go`、`internal/agent/probe_job_test.go`（含探测任务与 TLS 配置/自定义 CA 用例）、`internal/api/probe_via_test.go`、`internal/api/client_ip_test.go`、`internal/config/trusted_proxies_test.go`；
- 端到端：`scripts/verify_checks.sh` 第 8 节（agent 代检成功、SSH 服务器明确拒绝且不回落、`process` 带 `probe_via` → 400、`params` 原样回显）；`scripts/verify_authz.sh` 第 8 节（按转发 IP 分桶、审计记录真实客户端 IP、未配白名单时忽略 XFF）。

## 64. 控制端一键安装脚本（`deploy/install-control.sh`）

目标：让「装一个控制端」从「下载包 → 放二进制 → 写 env → 配 systemd」四步变成一条命令，并且**端口可指定**。

### 64.1 为什么是脚本而不是安装包

- 控制端是单文件二进制（前端、agent 安装脚本都 `//go:embed` 在里面），没有运行时依赖，脚本只需要做四件事：下载 / 校验 / 落盘 / 注册服务；
- `.deb` / `.rpm` / Homebrew 都要各自维护打包与仓库，而脚本对所有 Linux 发行版与 macOS 是同一份；
- 与 `deploy/install.sh`（agent）对称：同一个仓库、同一种「管道执行」的心智模型。

### 64.2 参数与落点

| 参数 | 落点 / 语义 |
|---|---|
| `--port N` | 合成监听地址 `127.0.0.1:N`，写进环境文件的 `LEADSTACK_LISTEN`（校验 1–65535） |
| `--bind ADDR` | 只换地址部分；地址里带端口直接报错（提示改用 `--port`），避免出现「两个端口谁生效」的歧义 |
| `--listen ADDR:PORT` | 直接给完整值，优先级最高（`--port`/`--bind` 被忽略） |
| `--data-dir` / `--install-dir` / `--env-dir` / `--user` | 除 `--data-dir` 外都有平台默认值；`--no-service` 时只落二进制与环境文件 |
| `--version` / `--base-url` / `--from-file` | 版本默认 `latest`（解析 `LEADSTACK_RELEASE_API` 的 `tag_name`）；`--base-url` 指向镜像；`--from-file` 完全离线 |
| `--uninstall` / `--purge` | 停服务 → 删单元 → 删二进制 → 删环境文件；**默认保留数据目录**，`--purge` 才删 |

### 64.3 安全与幂等

- **必校验 sha256**：先取 `checksums.txt`，比对通过才解压替换；不匹配 `die` 且**不动已安装的二进制**（先装到临时目录再 `install`）；
- **绝不静默 sudo**：所有需要特权的动作走 `run_priv`，非 root 且无 `sudo` 时报错退出；`--no-service` + 目录可写时完全不需要提权（这条也修过 bug：对尚不存在的目录做 `-w` 判断恒为假，改成向上找最近存在目录的 `writable_dir()`）；
- **写入前先落 0600**：环境文件用 `umask 077` + 临时文件 + `mv`，中途失败不会留下「半截明文密码」；
- **幂等即升级**：重复执行 = 停服务 → 换二进制 → 起服务；端口/env 全部按本次参数重写，不残留旧值。

### 64.4 验收

`scripts/verify_install_control.sh`（47 项，`make verify-install`）：端口参数与非法值、环境文件引号编码、按 env 真实启动并 `curl /healthz` + 登录、幂等重装换端口、本地 mock 发布服务的真实下载与篡改包中止、`LEADSTACK_RELEASE_API` 解析、卸载保留数据 / `--purge` 删除、docker 内 systemd 单元渲染（`User=leadstack`、`EnvironmentFile`、`ProtectSystem=strict`）。

踩过的坑（写测试时才会暴露）：

- 用 `( … ) &` 启动被测服务时，`kill $!` 只杀子 shell，**里的二进制会变成占端口的孤儿**，后续断言会「对着旧进程」假通过——必须直接后台启动二进制，并在 cleanup 里按临时目录 `pkill -f` 兜底；
- macOS 的 `ls -l` 会带扩展属性标记（`-rw-------@`），判断权限要用 `stat -f '%Lp'` / `stat -c '%a'`；
- 容器内（colima）**单文件 bind mount 不可见**，只共享工作目录，所以 Linux 侧的验证要把交叉编译产物放进仓库目录再挂载。

### 64.5 进度输出（不能让人盯着空屏）

下载十几 MB、校验、解包、注册服务全部静默执行时，用户唯一的感受是「卡住了」——实操里这条反馈出现过一次，所以两个安装脚本（`install-control.sh` 与 agent 的 `install.sh`）都统一成：

- 每阶段一行 `(n/总数) 标题`，结束时一行 `   ✓ 说明（Ns）`，跳过的一行 `   – 跳过：原因`（`--no-service` / dry-run / 无 systemctl 都是显式跳过而不是默默不做事）；
- 下载在交互终端下带进度条：`curl --progress-bar` / `wget --show-progress`。进度写 stderr，所以 `curl … | sudo sh` 一样能看到；
- 下载完成打印实际大小（`已下载 6.7 MB`），`sha256 校验通过：<hash>` 打出全量摘要，结束打印 `安装完成（总耗时 Ns）`；
- **非 tty 关掉进度条**（否则 CI 日志会被 `\r` 刷爆），但阶段行与结论行照常输出——日志里也要能看出走到哪一步。

两个连带修掉的权限判断缺陷（都在 agent 的 `install.sh`，非交互安装会直接失败）：

- `--install-dir /tmp/new-dir` 这种**目录尚不存在**的情况：`-w "$dir"` 恒为 false，被误判成「必须 sudo」，于是非交互会话里直接报「sudo 需要密码」。改为 `writable_dir()`：向上找最近一个存在的祖先再判断可写性；
- `install_service` 写环境文件前同样用 `-w "$ENV_DIR"` 判断，同样会误判「目录不可写，跳过凭证文件」，一并改成 `writable_dir()`。

验收对应 `verify_install_control.sh` 第 10 节（8 项：6 个阶段标题、大小、校验结论、安装结论、总耗时、非 tty 无进度条残留、每阶段有结论行、dry-run 阶段序列）与 `verify_bootstrap.sh` 第 7 节（4 项：agent 脚本 dry-run 的 4 阶段与结论行）。
- 干跑不撒谎：`--dry-run` 的第 4/5 阶段结论是「dry-run：只打印…动作」，不再沿用「✓ 已安装 / ✓ 已注册」。干跑的意义就是「看看它要做什么」，说要改系统却什么都没改，比没有输出更误导。`verify_install_control.sh` 有一条断言专门锁住这点（dry-run 输出不得出现「✓ 已安装/✓ 已注册」）。

### 64.6 CI 的 `go test -race` 闸门为什么曾经随机红

现象：`vit / build / test` job 的 `go test -race（闸门：internal/api 与 internal/store）` 步骤在不同提交上偶发失败，失败提交与上个成功提交之间**没有任何 Go 代码差异**。

根因：`internal/auth` 用 bcrypt cost=12 做密码哈希，而 api 包的测试里每个用例都要 `newTestEnv` + `login()`，整套下来有上百次 bcrypt 校验。竞态检测器会把 `x/crypto/blowfish` 的纯计算放大约 50–100 倍，实测单次登录从 ~200ms 涨到 3–4 秒；于是这个包在 2 核 runner 上会撞 `go test` 的 10 分钟包超时，表现为整步失败——和代码回归无关。

修法（`internal/auth/password.go`）：成本改为可覆盖的 `LEADSTACK_BCRYPT_COST`（校验 4–31，非法值回落 12，默认仍是 **12**），api 包的 `TestMain` 把它设为 4。生产默认值不变；测试里 `make test-race` 从 ~9 分钟（且经常超时）降到 **77 秒**。`auth` 包有两条测试锁定语义（合法值生效、越界/非数字回落默认；哈希中嵌入的成本与配置一致）。

