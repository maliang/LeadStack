# LeadStack 架构与接口契约 v1（M0 冻结）

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

握手：`Authorization: Bearer <enroll-token>`（HTTP Header，M0 用；M1 升级为 mTLS 客户端证书）。
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

指标命名（点分层级，固定集合，M0 只做前 5 个）：`cpu.pct`、`mem.used_pct`、`mem.used_bytes`、`load.1`、`disk.used_pct`、`net.rx_bytes`、`net.tx_bytes`、`proc.count`。

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

## 5. 数据库 schema v1（M0 迁移 `0001_init.sql`）

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

## 6. M0 验收标准（必须全部可演示）

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

# M1 契约（服务器纳管）

> M1 在 M0 基础上补齐：**SSH 凭证管理 · 连通性测试 · 主机信息 · Agentless 采集 · 分组标签与批量操作 · 指标历史与降采样**。
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

`cmd/leadstack-devssh` 以固定端口与固定账号启动它，启动横幅必须显式打印 `DEV-ONLY` 警告，供 `scripts/verify_m1.sh` 驱动真实 HTTP API。

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
| GET | `/groups` | 由 servers 聚合出的分组名与数量（M1 不建独立表） |

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

## 12. M1 验收标准（`scripts/verify_m1.sh` 一键复现）

1. `make build` 成功；`go vet ./...` 与 `go test ./...` 全绿。
2. 创建 SSH 私钥凭证 → 列表接口响应中**不出现**任何密钥明文（以原始私钥片段做反向断言）。
3. 启动 `leadstack-devssh` → 添加 `mode=ssh` 服务器 → `test-connection` 返回 `ok=true`、SSH banner、主机密钥指纹。
4. `refresh-host-info` 返回 hostname/os/kernel/arch/cpu_cores/mem_total_bytes/disk_total_bytes/uptime_s 且全部非空。
5. 60 秒内出现 agentless 采集的 `cpu.pct`/`mem.used_pct`/`load.1`，状态为 `online`。
6. `GET /servers/{id}/metrics?step=raw` 返回时间序列；构造历史后 `step=1m` 可返回汇总。
7. 批量 `add-tags` + `test-connection` 对 2 台机器返回逐台结果与汇总。
8. `?group=` 过滤生效；agent 模式服务器上报 `host_info` 后详情可见主机信息。
9. 未带 token 访问 `/credentials` 返回 401；被引用的凭证删除返回 409。
10. `scripts/verify_m0.sh` 仍全绿（**M1 不得回归 M0**）。

---

# M2 契约（监控与检测）

> M2 补齐「**主动检测**」：检测项（端口/HTTP/Ping/进程/脚本）· 调度与结果时间线 · 检测视图 · 大范围指标查询降级。
> 并按计划为 M3 告警准备数据（连续失败计数与状态起止时间）。

## 13. 检测模型与执行位置

| 类型 | Target 形态 | 在哪执行 | 说明 |
|---|---|---|---|
| `tcp` | `host:port` | 控制端直连 | 端口存活；`params.timeout_ms` 可选 |
| `http` | 完整 URL | 控制端直连 | `params.expect_status`（默认 2xx）、`params.keyword`、`params.method`（默认 GET）、`params.insecure`（跳过证书校验） |
| `ping` | `host` | 控制端直连 | ICMP 非特权模式（`icmp.ListenPacket("udp4")`）；**无权限时必须返回 ok=false + message「当前环境不支持非特权 ICMP」**，不得静默成功 |
| `process` | 进程名 | 目标机 | 由控制端生成命令，经 Executor 执行后按进程数判定；`params.min_count`（默认 1）、`params.user` 可选 |
| `script` | 脚本内容 | 目标机 | `params.exit_zero_ok`（默认 true）、`params.expect_stdout` 可选；**脚本内容会进审计日志** |

**Executor 决定目标机执行通道**：agent 模式 → 控制端下发 job（`internal/jobs`）；ssh 模式 → SSH `Run`。两种模式对检测层完全透明。

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
    OnStateChange    func(checkID, serverID string, ok bool, since int64) // 可选，M3 告警挂这里
}

type Scheduler struct{ /* ... */ }
func NewScheduler(d Deps) *Scheduler
func (s *Scheduler) Start(ctx context.Context)   // 阻塞直到 ctx 取消
func (s *Scheduler) Wake()                       // 检测项变更后立即重读目标列表
func (s *Scheduler) RunOnce(ctx context.Context, checkID, serverID string) Result // 手动「立即执行」
```

### 14.2 `internal/jobs` —— 控制端任务下发（`agentdev` 负责）

M0 只做了 agent 侧的 job.exec 接收，**控制端还没有下发能力**，M2 补上。

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
**指标查询降级（M2 附带）**：`GET /servers/{id}/metrics` 当 `step=raw` 且时间跨度 > 6 小时时**自动降级**为 `1m` 并在响应 `step` 字段返回实际值（不再返回超大原始序列）。

### 15.1 响应字段（M2 冻结，前端按此实现）

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

## 17. M2 验收标准（`scripts/verify_m2.sh` 一键复现）

1. `make build` 成功；`go vet ./...` 与 `go test ./...` 全绿；M0/M1 两个脚本仍全绿。
2. 创建 `tcp` 检测项指向 devssh 端口、`http` 检测项指向控制端自身 `/healthz`，各应用 1 台服务器 → `POST /checks/{id}/run` 返回 `ok=true` 且 `latency_ms` 有值。
3. 创建指向不可达端口的 `tcp` 检测项 → 立即执行 `ok=false` 且有 message；间隔 10s 连续执行后状态变 `down`。
4. `http` 检测项对 `/healthz` 校验 `expect_status=200` 通过；指向不存在路径且 `expect_status=200` → `ok=false`。
5. `process` 检测项（匹配本机真实进程名，如 `leadstack`）经 **agent 模式**服务器（job.exec 下发）执行成功；同一检测项对 **ssh 模式**服务器执行也成功（两种通道都通）。
6. 调度器按间隔产生结果：60 秒内单目标 ≥4 条结果记录。
7. `GET /checks/{id}/results` 返回时间线；`GET /servers/{id}/checks` 返回该机器检测项与状态。
8. 指标大范围查询：`step=raw` 且跨度 > 6h 时响应 `step` 变为 `1m`。
9. 未带 token 访问 `/checks` 返回 401。
10. `scripts/verify_m0.sh` 与 `scripts/verify_m1.sh` 仍全绿（**M2 不得回归**）。
