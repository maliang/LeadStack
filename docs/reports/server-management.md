# 纳管 交付报告：服务器纳管

> 完成时间：2026-10-19　|　验收人：Lead（独立于实现方）
> 构建环境：macOS 26 (arm64) · Go 1.27.2 · Node 26.11.1 / pnpm 10.34.7

---

## 一、结论

**纳管 验收通过（33/33 项），无回归（11/11 项）**。五条并行工作流全部交付：SSH 能力底座、服务端纳管能力、控制台、agent 增强、工程化。

基础 的「只能靠 agent 接入」在 纳管 变成两条路：**Agent 常驻接入**（秒级指标）与 **SSH 免安装接入**（Agentless，控制端主动采集）。同时补上了凭证托管、主机信息、分组标签、批量操作、指标历史与降采样。

## 二、交付物

| 工作流 | 交付 |
|---|---|
| SSH 底座 | `internal/credential`（AES-256-GCM + 主密钥 0600）、`internal/sshx`（密码/私钥认证、三态主机密钥策略、防中间人）、`internal/hostinfo`（Linux+macOS 主机信息与区间 CPU 采样）、`internal/devssh`+`cmd/leadstack-devssh`（DEV-ONLY 测试 SSH 端点） |
| 服务端 | 迁移 0002/0003、凭证/主机信息/降采样 store、`internal/collector`（agentless 采集循环，并发 8、同机不并发、3 次失败判离线）、8 组新 REST 端点、WS 接收 `host_info` |
| 控制台 | 凭证管理页、接入向导（Agent / SSH 双路）、详情页（主机信息卡 + 4 张 uPlot 历史曲线 + 连通性测试/刷新）、四维筛选与批量操作（逐台结果） |
| agent | `host_info` 上报（上线即报 + 每 6h）、`net.rx/tx`（排除虚拟网卡）、`proc.count` |
| 工程化 | `make devssh` / `make test-e2e`、CI 双 e2e 流水线、install.sh 版本校验与卸载残留清理、发布产物排除 devssh |

## 三、验收证据（Lead 在冻结版本上实跑）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | `make build` / `go vet ./...` / `go test ./...`（12 包） | ✅ 全绿 |
| 2 | 凭证列表不泄露私钥与密码明文 | ✅ 反向断言（grep 私钥头与密码） |
| 3 | SSH 连通性测试 | ✅ `ok=true`、banner `SSH-2.0-Go`、指纹 `SHA256:LxZuuXIBSvGcpd1r…` |
| 4 | 主机信息采集（8 字段非空） | ✅ hostname/os/kernel/arch/10 核/32GB/994GB/uptime |
| 5 | Agentless 采集落库 | ✅ 6 个指标（cpu.pct/mem/disk/load/uptime），status=online |
| 6 | 指标历史 `step=raw` / `step=1m` | ✅ 6 条时间序列；1m 汇总接口可用 |
| 7 | 批量操作（2 台逐台结果 + 汇总） | ✅ test-connection 2/2、add-tags 2/2 |
| 8 | 分组聚合与 `?group=` 过滤 | ✅ `/groups` 有聚合，过滤命中 2 台 |
| 9 | 鉴权与引用保护 | ✅ 无 token 401；删除被引用凭证 409 |
| 10 | agent 模式 `host_info` 落库 | ✅ Apple 纳管 Pro/10 核/32GB/时区/IP 列表齐全 |
| 11 | 内嵌 UI 与安装脚本同端口 | ✅ `/` 200、`/assets/index-C_JPe9yh.js` 200、`/install.sh` 200 |
| 12 | 主密钥安全 | ✅ `master.key` 权限 `-rw-------`，密钥 hex 全文未出现在日志 |
| 13 | 交叉编译五平台 | ✅ 10 个产物，且**无 devssh 泄漏** |
| 14 | 基础回归 | ✅ 11/11（agent 接入、指标、离线判定、安装命令） |

复现：`make build && make devssh && bash scripts/verify_bootstrap.sh && bash scripts/verify_servers.sh`

## 四、并行协作方式

契约先行是 纳管 能并行的关键：我先把 `docs/ARCHITECTURE.md` 的第 8 节写成**可编译的 Go API 契约**（类型、签名、语义），sshmgr 先交签名骨架解除 backend 的阻塞，再填实现；`pkg/protocol` 的 `host_info` 也由我先落地，agent 与后端才各自动手。五条工作流写文件范围互不重叠，`go.mod`/`go.sum`/`pkg/protocol`/`scripts`/`docs` 由我独占。

集成期修掉的跨工作流问题：
1. **SSH 无验收端点**：本机无 sshd、无免密 sudo → 设计 `cmd/leadstack-devssh` 作为 DEV-ONLY 测试端点，并把 CLI 参数写成契约，避免脚本与实现各写一套；
2. **`verify_servers.sh` 的 `GROUPS` 假失败**（我自己的 bug）：`GROUPS` 是 bash 只读特殊变量（组 ID 数组），赋值恒失效，断言拿到的是本机 gid `20`；改名 `GROUPS_JSON` 后 33 项全绿。这是**由队友独立定位**的；
3. **`step=raw` 偶发为空**（backend 定位并修复）：脚本 `to` 取整到秒，比刚落库的采样早 21ms，半开区间把它排除 → 改闭区间 + 5s 时钟偏差容忍；
4. **容器内 `/install.sh` 会返回 HTML**（devops 定位并修复）：webui 静态未命中会回退 index.html，Dockerfile 之前没把 install.sh 放进 dist；
5. **`gliderlabs/ssh` 停在 indirect 块**：由我执行 `go mod tidy` 归位为直接依赖。

## 五、需要记录的实现裁决（与 基础契约的差异）

| 事项 | 裁决 |
|---|---|
| `test-connection` 失败语义 | **200 + `ok=false` + `error`**（便于前端展示原因），而非 4xx；服务器不存在仍 404 |
| `refresh-host-info` 采集失败 | 502 + `code=internal`（未新增错误码） |
| 离线判定适用范围 | 45s 心跳清扫**只作用于 `mode=agent`**；SSH 模式的状态权威是采集器（否则 30s 采一次的机器会被误判离线） |
| `LEADSTACK_SSH_INTERVAL` | 按 Go duration 解析（`3s`），纯数字按秒兜底，范围 1s–24h |
| `net.rx/tx` 口径 | 排除回环/点对点 + 虚拟与桥接前缀（docker/veth/br-/virbr/vmnet/utun/awdl/llw/…）；全部被排除时**跳过指标而不是报 0** |
| 前端私钥掩码 | 用 `-webkit-text-security`（Firefox 下粘贴内容可见；接口与存储层不回显） |

## 六、已知限制（不阻塞 纳管，已排入 检测/部署）

1. Agentless 采集是轮询式的：本轮 SSH 慢会推迟下一轮（不叠加并发）。检测 若要严格节拍需改为 per-server 独立 ticker。
2. macOS 的区间 CPU 用 `ps` 求和近似（Linux 用 `/proc/stat` 精确差分）；macOS 首帧即有值，Linux 首帧为空（契约允许）。
3. `anpi*/gif0/stf0/nan0` 等 macOS 接口不在黑名单内（实测计数为 0），后续如异常再补前缀。
4. job.exec 的控制端下发与 Web 终端仍未实现（agent 侧已具备）。
5. Docker 镜像、GoReleaser 仍只有结构校验（本机无 docker/goreleaser），由 CI 兜底；macOS 公证/Windows 签名仍是 TODO。
6. 前端包体因 uPlot 增至 gzip 132.79 kB，未做图表懒加载。

## 七、下一步（检测：监控与检测）

检测项模型（HTTP/TCP/Ping/进程/端口/脚本）→ 检测调度与结果时间线 → 服务器详情页的检测视图 → 指标降采样查询优化（大范围 raw 截断）→ 为 告警 告警规则引擎准备数据模型。
