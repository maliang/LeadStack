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
