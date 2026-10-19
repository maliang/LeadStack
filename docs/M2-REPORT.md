# M2 交付报告：监控与检测

> 完成时间：2026-10-19　|　验收人：Lead（独立于实现方）
> 构建环境：macOS 26 (arm64) · Go 1.27.2 · Node 26.11.1 / pnpm 10.34.7

---

## 一、结论

**M2 验收通过（25/25 项），M0 无回归（11/11），M1 无回归（33/33）——累计 69 项断言全绿。**
产品从「能看到服务器状态」变成「能主动发现服务不可用」：五类检测项 + 调度器 + 结果时间线，并补上了 M0 遗留的**控制端任务下发链路**。

## 二、交付物

| 工作流 | 交付 |
|---|---|
| 检测引擎 | `internal/checker`：`tcp`/`http`/`ping`/`process`/`script` 五类探测 + 每目标独立 ticker 调度器；55 个测试、覆盖率 **88%** |
| 任务下发 | `internal/jobs`：控制端 `job.exec` 下发与结果收集（runID 隔离、超时清理、1 MiB 截断、断连唤醒）；19 个测试 |
| 服务端 | 迁移 0004（checks/check_targets/check_results/check_state）、10 个 REST 端点、状态机与 24h 可用率、30 天保留、SSH 连接池复用、job 帧路由、指标查询降级 |
| 控制台 | 检测页（列表/概览/筛选/启用开关/立即执行逐台结果）、检测项表单（按类型切换）、服务器详情「检测」标签 + 结果时间线、总览检测概览与 down 横幅 |
| 工程化 | `make test-e2e` 三脚本按序执行、CI 中 M1 升级为阻塞 + 新增 M2、CI 改用 `go-version-file` 消除 Go 版本漂移 |

## 三、验收证据（Lead 在冻结版本上实跑）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | `make build` / `go vet ./...` / `go test ./...`（14 包） | ✅ 全绿 |
| 2 | tcp 检测项指向 devssh 端口，立即执行 | ✅ `ok=true` + 延迟 |
| 3 | http 检测项对 `/healthz` 期望 200 | ✅ `ok=true` |
| 4 | http 期望 200 但实际 404 | ✅ `ok=false`（"HTTP 状态码 404 不在期望范围 200"） |
| 5 | 不可达端口 + 连续失败达阈值 | ✅ `ok=false` → 状态变 `down` |
| 6 | 调度器按 10s 间隔产生结果 | ✅ 单目标 5 条结果 |
| 7 | **process 检测经 SSH 通道** | ✅ 成功 |
| 8 | **process 检测经 agent(job.exec) 通道** | ✅ 成功（这条证明 M0 遗留的下发链路真正打通） |
| 9 | 结果时间线 / 服务器维度视图 / 概览 | ✅ 5 条结果、5 个检测项、`{total:6,up:4,down:2}` |
| 10 | 指标大范围降级 | ✅ 24h→`1m`，1h 仍为 `raw`（未误伤） |
| 11 | 未带 token 访问 `/checks` | ✅ 401 |
| 12 | M0 / M1 回归 | ✅ 11/11、33/33 |
| 13 | 内嵌 UI 与安装脚本 | ✅ `assets/index-Dr7ZYMW0.js`、`/install.sh` 200 |
| 14 | 交叉编译五平台 | ✅ 10 产物、无 devssh 泄漏 |

复现：`make build && make devssh && bash scripts/verify_m0.sh && bash scripts/verify_m1.sh && bash scripts/verify_m2.sh`

## 四、并行协作方式

同样契约先行：这次我把**两个新包的 Go API** 写进契约（`checker` 与 `jobs`），checkdev 先交签名骨架解除 backend 阻塞，再由 backend 接线。职责切分的关键是让 `checker` **不依赖 store/api**——用 `Store`/`Executor` 两个接口注入，于是检测引擎可以独立单测（88% 覆盖率），而 backend 用 `RoutingExecutor` 按 `mode` 把执行分派给 agent(Dispatcher) 或 SSH 通道。

集成期发现并修复的问题（**其中两个是我自己脚本的 bug，都由队友独立定位**）：
1. **`GROUPS` 是 bash 只读特殊变量**（M1 阶段）——断言恒拿到本机 gid；
2. **`verify_m2.sh` 里带 `\"` 的 JSON 嵌在两层 `"$( )"` 中**——bash 3.2 把参数拆坏，导致登录 token 变成错误响应体，后续全部 400。已把全脚本改成「先落变量再取值」的两步式；
3. **`jq '... // empty'` 会吞掉布尔 `false`**——`ok=false` 被读成空串，误报"404 竟判成功"。已改成只把 `null` 归一成空串；
4. `servers.last_error` 在恢复成功后未清空——三条通道（SSH 采集/agent 心跳/检测状态）全部修正并补测；
5. ping 无权限时的文案是底层错误原文——已归类为可解释提示；
6. 契约缺口 `/checks` 响应字段名——已冻结为 §15.1。

## 五、实现裁决（需要记录）

| 事项 | 裁决 |
|---|---|
| process 检测的实现 | **不用 `pgrep -f`**：Linux 上它会把执行命令的 `sh -c` 自身计入（假 up）。改为 POSIX 八进制转义 + `ps -ww -eo pid=,ppid=,command=` + awk 排除自身进程树，target 原文不进 argv；18 条注入用例验证 canary 文件未被创建 |
| ping | 非特权 ICMP 不可用时 `ok=false` + 可解释文案，**绝不静默成功**；本机（macOS 非 root）实际拿不到权限，Linux 视 `net.ipv4.ping_group_range` |
| 状态机位置 | `fail_threshold` 判定放在 checker 内（阈值属于 `params`）；`SaveResult` 存原始结果，`UpdateState` 传判定后的状态；首次结果只建基线不回调 |
| SSH 执行通道 | 按 serverID 缓存一条已认证连接，同机串行执行（避免触发对端 MaxStartups），空闲 90s 回收，失败重试一次；N 台机器最多 N 条长连接 |
| `state` 与 `status` | 同值双写，前端优先 `state`、回落 `status`（§15.1 已冻结） |

## 六、已知限制（不阻塞 M2）

1. **process 检测按控制端 GOOS 生成命令**：跨 OS 纳管（如 Linux 控制端管 Windows 目标）会走错分支。`Executor` 接口不带目标机 OS，M3/M4 若要支持跨 OS 需扩接口。
2. ping 在本机拿不到非特权 ICMP，无法演示绿色结果；Linux 需 `ping_group_range` 放开。
3. 本机存在**透明 TCP/DNS 拦截**（不存在的域名也能连上），验收脚本因此用「已关闭端口」而非「不可达 IP」构造失败用例——真实环境排障时需注意这一点。
4. 检测结果保留 30 天、无降采样；结果量大时时间线接口依赖 `limit`。
5. `internal/checker` 无 Logger 注入（契约未含），落库失败静默重试。
6. Docker 镜像、GoReleaser 仍只有结构校验（本机无 docker/goreleaser），macOS 公证/Windows 签名仍是 TODO。

## 七、下一步（M3：告警）

规则引擎（阈值 + 持续时长 + 恢复通知）→ 告警去重与静默 → 通知渠道（Webhook / 邮件 / 钉钉 / 企微 / 飞书 / Telegram / Slack）→ 告警历史与确认。
数据侧已经就绪：`check_state` 提供了连续失败计数与状态起始时间，`Deps.OnStateChange` 就是告警的挂载点。
