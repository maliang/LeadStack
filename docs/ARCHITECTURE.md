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
