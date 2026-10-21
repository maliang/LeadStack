# 权限体系交付报告：自定义角色 / 服务器分组 / 资源级授权 / 登录保护

> 契约：`docs/ARCHITECTURE.md` 第 52–56 节（冻结）。
> 验收：`scripts/verify_authz.sh`（38 项断言，真二进制端到端）+ `internal/api/authz_test.go`（7 个 Go 用例）。

## 1. 交付内容

### 1.1 权限模型（§52）

- **24 个权限点**，按「域.动作」命名：`server.read/write/delete`、`terminal.use`、`check.read/write`、`alert.read/write`、`deploy.read/write`、`backup.read/write/delete/restore`、`task.read/write`、`group.write`、`agent.upgrade`，以及 6 个**仅管理员**权限：`user.manage`、`role.manage`、`token.manage`、`enroll.manage`、`system.manage`、`audit.read`。
- **内置角色**（代码常量，不可改不可删）：`viewer` 6 项、`operator` 14 项、`admin` 24 项；范围恒为 `all`。
- **自定义角色**：`r_` + 8 位十六进制 key，权限必须是非空的「可授予」子集——含管理权限直接 400（**防提权**）；被用户引用时删除返回 409。
- **逐路由权限**：路由表由「角色分组」改成「每条路由声明权限」（`r.With(a.RequirePerm(...))`），同一路径的 GET 与写方法可以有不同权限。

### 1.2 服务器分组与资源级授权（§53）

- 新增一等公民分组：`server_groups` + `server_group_members`（多对多），REST 见 §55.3；旧的自由文本 `servers."group"` 保留为「筛选标签」，迁移时自动把历史值转成正式分组与成员关系。
- 角色范围：`all` / `groups` / `servers`（可混用，求并集），落 `role_scopes` 表。
- 判定规则（实测）：
  - 对象级越界 → **404**（不泄露存在性）；写越界 → **403**；
  - 列表级（servers / checks / alerts / releases / scheduled-tasks）只返回范围内对象，**先过滤后分页**；
  - 全局资源（备份、审计、系统设置、用户/角色/Token/注册令牌、应用与模板、告警规则与渠道）要求 `scope=all`；
  - `group.write` 额外要求 `scope=all`，避免「把自己机器塞进自己的组」式自我扩权。

### 1.3 登录限流与失败锁定（§54）

- 账号维度**持久化**：连续失败达到阈值 → `locked_until` 落库；锁定期内即使密码正确也拒绝；成功登录清零；管理员重置密码同时清零。
- IP 维度**进程内滑动窗口**：同 IP 在窗口内请求超限 → 429 `rate_limited` + `Retry-After`。
- 响应带 `error.details`：`remaining_attempts` / `retry_after_s` / `locked_until`。
- 审计新增 `auth.login_locked`、`auth.login_rate_limited`、`user.unlock`。
- 配置：`--login-max-attempts`、`--login-lock-duration`、`--login-ip-limit`、`--login-ip-window`（对应 `LEADSTACK_LOGIN_*` 环境变量；0 表示关闭）。

### 1.4 API 与前端

- 新增 `GET /permissions`、`/roles` CRUD、`/server-groups` CRUD + 成员、`POST /users/{id}/unlock`；`/auth/login`、`/auth/me` 增加 `permissions`、`role_name`、`scope`；服务器载荷增加 `group_ids`/`group_names`。
- 前端：新增「角色与权限」「服务器分组」两个页面，用户页显示锁定状态并支持解锁，登录页处理 429/剩余次数，导航与按钮按权限门控。

## 2. 设计取舍

| 决定 | 理由 |
|---|---|
| 权限按能力判定，保留 `roleRank` 仅用于兼容校验 | 自定义角色没有等级，硬编码等级会立刻失效 |
| JWT 只带角色 key，权限每次请求解析（带缓存） | 改权限即时生效；缓存以版本号失效，避免每次请求多查库 |
| 自定义角色不能持有管理权限 | 否则「改角色 → 给自己 admin」是必然的提权路径 |
| 分组与自由标签并存 | 不破坏既有筛选习惯；分组只承担授权语义 |
| 受限用户的列表「先过滤后分页」 | 安全性优先；代价是单页条数可能少于 `limit`（已在契约中注明） |
| 未知用户名也做进程内计数 | 避免通过「是否被锁定」枚举用户名；不落库，重启即清 |

## 3. 验证结果（实测）

`bash scripts/verify_authz.sh` → **38/38 通过**，其中关键几项：

- 权限目录 24 项、6 项仅管理员；`viewer` 6 / `operator` 14 / `admin` 24 与契约一致；
- 自定义「发布员」能读发布、不能建服务器（403）；带 `user.manage` 的角色创建被拒（400）；
- 建分组 → 只放 1 台 → 受限用户 `/servers` 只剩 1 台、越界详情 404、越界绑定检测目标 403、范围内建检测 201；
- 受限范围用户写分组 403（`group.write` 需全局范围）；
- 内置角色 PATCH/DELETE 403，被引用角色 DELETE 409；
- 3 次错误密码 → 第 3 次即 429 `login_locked`，锁定期内正确密码仍 429，用户列表 `locked=true`，解锁后登录成功，审计含 `auth.login_locked` 与 `user.unlock`；
- IP 限流触发 429 `rate_limited` 且带 `Retry-After` 头。

Go 侧：`go test ./...` 全绿；新增 `internal/api/authz_test.go` 覆盖权限矩阵、角色 CRUD 边界、范围过滤与锁定/解锁流程；`internal/auth/perm` 目录与内置角色集合有断言守护。

## 4. 已知限制

1. **告警汇总**（`/alerts/summary`）与**检测汇总**（`/checks/summary`）仍是全局口径：受限用户看到的是全站计数（明细是收敛的）。需要时再做范围聚合。
2. **审计日志**只对 `scope=all` 且持有 `audit.read` 的内置管理员开放，自定义角色不能获得审计读取权。
3. **PATCH 角色范围**为整包替换语义（`group_ids`/`server_ids` 传空数组即清空），前端需要一次提交完整集合。
4. 受限用户看到的**无服务器 `server_id` 的规则级告警**不可见（仅全局范围可见），符合契约但对老数据需要适应。
5. 备份、应用/模板、告警规则仍是全局资源：本版本不做按分组的多租户化。
6. `GET /credentials` 与旧版 `GET /groups`（标签聚合）仍返回全局元数据：受限用户能看到凭证名称与标签名，但**用不了**（越界服务器 404）。
7. 进程内 IP 限流与未知用户名计数随进程重启清零；账号锁定是落库的，重启后仍生效。
