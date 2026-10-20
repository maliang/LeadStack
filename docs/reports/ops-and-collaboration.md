# 交付报告：运维 运维增强 · 协作 团队协作与体验

> 完成时间：2026-10-20　|　验收人：Lead（独立于实现方）

---

## 一、结论

**此前所有「未完成功能」与「功能缺口」已全部补齐，七套验收全绿：11 + 33 + 25 + 29 + 33 + 35 + 46 = 212 项。**
产品从「能用」推进到「可交付」：多用户协作、操作可追溯、浏览器内排障、发布工程真实验证，以及一次完整的界面与体验重构。

## 二、补齐的功能清单

| 类别 | 能力 | 状态 |
|---|---|---|
| **审计日志** | 全部写操作自动记录（action/actor/IP/状态码），敏感字段脱敏；登录、终端、升级手动记录；可按 actor/action/时间筛选 | ✅ |
| **RBAC** | admin / operator / viewer 三级；越权 403；禁止自删与删除最后一个 admin；被禁用用户的 JWT 立即失效 | ✅ |
| **用户管理** | 用户 CRUD、改角色、禁用、重置密码、本人改密码 | ✅ |
| **API Token** | `lsk_` 前缀、明文仅创建时返回一次、库存 sha256、可撤销/过期、防角色提权 | ✅ |
| **Web SSH 终端** | 一次性 ticket + WebSocket；ssh 通道走 PTY、agent 通道走真 PTY；xterm.js + resize 同步；默认关闭（`LEADSTACK_TERMINAL_ENABLED`） | ✅ |
| **文件传输** | 浏览/上传/下载/删除；ssh 走 SFTP、agent 走 base64；8 类路径穿越防护 | ✅ |
| **定时任务** | 5 字段 cron（含 `*/n`、`a-b`）、人话预览、按分钟命中判定、运行历史 | ✅ |
| **日志查看** | 远端 `tail` + 服务端 grep 过滤，输出 ≤256KB 带截断标记 | ✅ |
| **进程/端口视图** | 跨平台采集（Linux `ps`/`ss`、macOS `ps -ax`/`lsof`），含 `ss→netstat→lsof` 回退 | ✅ |
| **批量导入主机** | 粘贴文本、`host[:port]`、IPv6、注释、去重、500 行上限、逐行结果 | ✅ |
| **分页** | `limit`+`cursor`；**不传 limit 时仍返回数组**，既有 212 项零回归 | ✅ |
| **Agent 自升级** | `agent.upgrade` 下发；A/B 替换 + sha256；失败保留旧版本 | ✅ |
| **跨 OS 进程检测** | 按 `params.os` 选命令，未改 `Executor` 接口 | ✅ |
| **中英双语** | zh-CN / en-US 各 **1488 词条**，键完全对齐（缺 0 多 0） | ✅ |
| **首次安装向导** | `needs_setup` 时引导：欢迎 → 添加第一台机器 → 完成 | ✅ |
| **主题 / 字号** | 主题三态（light/dark/system）+ 5 主题色；字号三档（14/16/18px）；密度两档；均持久化 | ✅ |
| **UI 布局与动线** | 导航分组（监控/运维/发布/系统）、面包屑、统一详情页结构、移动端抽屉、危险操作二次确认、空状态引导 | ✅ |
| **发布工程验证** | GoReleaser **实跑成功**（10 二进制 + checksums）；Docker **实跑成功**（容器内 `/healthz` 200、备份 CLI 可用） | ✅ |

## 三、验收证据（Lead 独立复核）

```
make build / gofmt / go vet / go test ./...   →  22 个包全绿
verify_m0  11  · verify_m1 33 · verify_m2 25 · verify_m3 29
verify_m4  33  · verify_backup 35 · verify_ops 46        →  合计 212/212
race 闸门 357s 无数据竞争
交叉编译 10 产物（无 devssh 泄漏）；goreleaser 快照 10 二进制
i18n：zh 1488 / en 1488，缺 0 多 0
```
`verify_ops.sh` 的关键断言是**真实执行**而非看接口返回：
- 终端：用最小 WebSocket 客户端连上去执行 `echo ls-terminal-ok` 并收到回显；
- 文件：上传 → 检查目标机磁盘上真的出现该文件且内容一致 → 下载比对 → 删除后确认消失；
- 定时任务：`tick=1s` 下真实产生 `exit_code=0` 的运行记录；
- 审计：确认写操作被记录且**响应中不含密钥明文**；viewer 越权 403。

## 四、这一轮修掉的真实缺陷

1. **部署模板拒绝中文路径**（devops 发现）：`/srv/应用/` 直接被拒。本仓库名含「领栈」导致本地 部署 全线失败。已改为按 rune 校验（允许 UTF-8，仍拒空白/shell 元字符/`..`）。
2. **i18n 迁移脚本吃掉了 150 处 JSX 闭合标签**（Lead 发现）：`</p>` 变成 `{t('s.0805')}p>`，全站构建失败。已修复并加验证。
3. **macOS 无 `ss` 导致端口采集失败**（backend 发现）：补 `ss→netstat→lsof` 回退链。
4. **colima VM 的 DNS 失效**导致 `docker build` 报 `[::1]:53 connection refused`（devops 定位并修复，文档记录"VM 重启后会复现"）。
5. **`retry` 与收尾的丢唤醒竞态**（上一轮）：本轮 race 闸门持续验证无回归。
6. **我自己的验收脚本 jq bug**（`//` 不兜底 type error）：已在备份验收中修正。
7. **`.gitignore` 行尾注释导致 `/dist/` 规则失效**（devops 自查发现并修复，避免了 170MB 产物入库）。

## 五、并行协作方式

本轮 7 条工作流并行（后端总装 / 前端总装 / SSH 底座 / agent / 纯逻辑包 / 通知健壮性 / 发布验证），靠**先冻结契约**（新增 §36–§48）与**签名骨架先行**解阻塞：
- sshmgr 先交 `PTY/SFTP/RunStream + term.Manager` 签名 → backend 立刻接线；
- checkdev 先交 `cron/remote/hostimport` 签名 → backend 立刻消费；
- agentdev 先交 `term/upgrade` 骨架 → backend 立刻转发。

集成期我处理的冲突：
- notifydev 与 backend 同时实现了「测试发送带 probe」，notifydev 主动删掉自己的实现、改为给 backend 的实现补测试（避免了重复声明冲突）；
- backend 报告 `channel_probe.go` 被删又重建，已核对最终语义一致。

## 六、环境与遗留

**已验证**：GoReleaser 2.18.3 快照发布、Docker（colima + docker CLI）镜像构建与容器内运行。

**明确的环境限制**：
1. **macOS 公证 / Windows 代码签名**：没有 Apple Developer ID 与 Windows 代码签名证书，无法完成。这是**唯一无法在本机完成**的项，需要你提供证书后接 CI。
2. 钉钉/飞书/Slack/Telegram **只用假凭据验证到"服务商接受请求"**（返回 401/errcode），**没有真实凭据因此无法验证"真能投递到群"**。配置好真实 webhook 后点一次「测试发送」即可确认。
3. 邮件同理：只验证到 SMTP 服务商应答（503/553），真实投递需你的 SMTP 账号。
4. Windows 终端按契约返回"不支持"（pty 仅 unix），未在真机 Windows 验证。
5. 浏览器 UI 的视觉核对由 frontend 用 headless Chrome 完成，**没有在你的真实浏览器里跑过**——建议你打开一次过一遍动线。
6. 终端吞吐走 base64 JSON 文本帧（非二进制帧），大量输出时效率略低。

## 七、下一步建议

1. **真机验收**：在你真实的 2～3 台服务器上走一遍「接入 → 检测 → 告警 → 发布 → 回滚 → 备份恢复」，这是最后一道验证；
2. **配置真实通知渠道**并触发一次告警，确认能收到；
3. **补充签名证书**接 CI（唯一的硬缺口）；
4. 规模压测（50～100 台）后再上生产。
