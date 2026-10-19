# M3 交付报告：告警

> 完成时间：2026-10-19　|　验收人：Lead（独立于实现方）
> 构建环境：macOS 26 (arm64) · Go 1.27.2 · Node 26.11.1 / pnpm 10.34.7

---

## 一、结论

**M3 验收通过（29/29 项），M0/M1/M2 全部无回归（11/11、33/33、25/25）——累计 98 项断言全绿。**
产品形成闭环：**接入 → 采集 → 检测 → 告警 → 推送**。故障能在 60 秒内从"检测失败"变成"人收到通知"，恢复后自动再推一次。

## 二、交付物

| 工作流 | 交付 |
|---|---|
| 告警引擎 | `internal/alerting`：for_duration 持续时长、dedup_key 去重、去重窗口、静默、恢复、多规则优先级；**50 个测试、覆盖率 88.5%** |
| 通知渠道 | `internal/notify`：webhook / 钉钉 / 企微 / 飞书 / Slack / Telegram / 邮件；**87 个用例、覆盖率 83%** |
| 服务端 | 迁移 0005、11 个 REST 端点、渠道配置 AES 加密 + 脱敏、三类事件接线、多渠道发送隔离、90 天保留 |
| 控制台 | 告警页三标签（列表/规则/渠道）、确认、静默、测试发送、事件轨迹四要素、总览告警概览与 firing 横幅 |
| 工程化 | 新增 **gofmt** 与 **race** 两道 CI 闸门；`make test-e2e` 扩到四脚本 |

## 三、验收证据（Lead 在冻结版本上实跑）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | `make build` / `gofmt` / `go vet` / `go test ./...`（16 包） | ✅ 全绿 |
| 2 | webhook 渠道「测试发送」真实送达 | ✅ 接收器真实收到 POST |
| 3 | 渠道列表不泄露 secret 明文 | ✅ 反向断言（含钉钉 access_token、Slack 路径 token） |
| 4 | 检测项故障 → 60 秒内 `firing` | ✅ 且接收器收到通知（体含规则名） |
| 5 | 去重：同一故障不新增告警 | ✅ 仍只有 1 条 firing |
| 6 | 人工确认 | ✅ `status=acked`，`events` 有记录 |
| 7 | 恢复 → `resolved` + 恢复通知 | ✅ 接收器收到第二封（`[已恢复]`） |
| 8 | 指标阈值规则周期评估 | ✅ `cpu.pct > 0` 产生 firing |
| 9 | 静默：仍建告警但不发 firing 通知 | ✅ `notify_count=0`，接收器无 firing 通知 |
| 10 | 告警概览 | ✅ `{firing:2, acked:0, resolved_24h:1, by_severity:{critical:0,info:1,warning:1}}` |
| 11 | 未带 token 访问 `/alert-rules` | ✅ 401 |
| 12 | M0/M1/M2 回归 | ✅ 11/11、33/33、25/25 |
| 13 | race 闸门（api + store） | ✅ 188s 无数据竞争 |
| 14 | 交叉编译五平台 | ✅ 10 产物、无 devssh 泄漏 |

复现：`make build && make devssh && make test-e2e`

## 四、并行协作方式

M3 是并行度最高的一轮（7 个成员）。契约依旧先行：把 `alerting` 与 `notify` 两个包的 **Go API 与全部语义**（for_duration、去重窗口、静默、恢复、各渠道 Config 字段）写进契约，两个新成员先交可编译骨架解除 backend 阻塞，再由 backend 接线。关键设计是让 `alerting` **不依赖 store/notify**（接口注入），于是能独立单测到 88.5% 覆盖；`notify` 不依赖任何 internal 包。

## 五、这一轮抓到的关键缺陷（都由队友独立发现，值得记录）

1. **for_duration 在真实链路上永远不会触发**（alertdev 自查发现）
   `checker.Deps.OnStateChange` 只在**状态翻转**时回调一次，异常持续期间没有第二次事件。若只靠 `HandleEvent` 推进，`for_duration_s > 0` 的规则永远等不到第二次观测 → **整个 M3 会静默失效**。修复：pending 保存完整观测，由周期 tick 的 `promoteDuePending` 提升。这类"单测全绿但产品不工作"的缺陷，是本轮最有价值的发现。

2. **钉钉 sign 二次编码**（notifydev 被测试抓出）
   sign 先 urlencode 再交给 `url.Values.Encode()` 会变成 `%252B`，服务端解码后签名必然不匹配 —— 典型"看起来发了、对方必然拒收"。已改为手工拼接保证只编码一次，并用 Python 独立算出的已知答案断言。

3. **`len(servers)==0` 的 early-return 会让 for_duration 停摆**（alertdev 发现、backend 修复）—— 延后而非丢失，但会造成告警迟到。

4. **`value=0` 被当成"未提供"** —— 真实 0 值告警会被跳过。

5. **webhook URL 里的 token 未脱敏**（frontend 联调发现）—— 钉钉/Slack 的凭证就在 URL 路径或查询参数里，明文预填进输入框等于接口响应泄漏密钥。

6. **gofmt 闸门当场抓到真实漂移**（devops 建闸门时即捕获 `internal/alerting/alerting.go` 未格式化）。

## 六、Lead 的产品裁决（记录）

| 事项 | 裁决 | 理由 |
|---|---|---|
| pending 计时是否持久化 | **不持久化**（内存态） | 控制端重启后从头计时是合理行为；为此改 schema 不划算 |
| 多规则命中同一 dedup_key | **严重级别最高者优先**（critical > warning > info），同级按 ID | 若按 ID 排序，用户配了 warning + critical 时 critical 可能永远发不出去 |
| 静默期是否抑制恢复通知 | **恢复通知必须穿透静默** | 静默是"别再告诉我新故障"，不是"别告诉我已经好了"；恢复是降噪信息 |
| 静默能否取消 | 支持 `duration_s=0` 取消（backend 已实现） | 否则只能等静默自然到期 |

## 七、已知限制

1. **pending 为内存态**：控制端重启后 `for_duration` 从头计时。
2. **服务器上下线事件走周期巡检**（≤1 个评估周期的延迟），换来不动 collector/checker；首轮只建基线，不补发历史事件。
3. **指标规则只喂最新值**，不做滑窗平均（契约未要求）。
4. **钉钉/飞书/Slack/Telegram 未对真实端点联网验证**（离线环境无凭据）：签名正确性用独立已知答案 + 真实 httptest 请求双重保证，但"服务商确实接受"未实测。
5. 邮件渠道未做连接池（每渠道每次一条连接）。
6. 告警列表固定 `limit=200`，无分页。
7. Docker 镜像、GoReleaser 仍只有结构校验；macOS 公证/Windows 签名仍是 TODO。

## 八、下一步（M4：批量部署）

这是产品的**差异化重头**：部署模板（compose / systemd / 二进制 + 脚本）→ 制品来源与校验 → 批量下发与并发控制 → 实时日志流 → 失败回滚与版本回退。
已具备的基础：`internal/jobs` 的任务下发链路（M2 打通）、agent 侧 `job.exec` 执行与日志分块回传、SSH 执行通道（带连接池）。
