# 部署发布方式的灵活性评估 + 宝塔面板对接

> 结论先行（2026-10 更新）：**首次部署**完整可靠；**「把已经跑着的项目接进来」**原本只是「hack 得通」，
> 现在补齐了：接管已有目录（inplace）、首版初始化、环境变量、部署后健康检查、变更探测向导、上传制品签名下载。
> 仍缺的是：自动生成接管模板、按环境变量注入 systemd unit、宝塔 node/SSL/反代、大规模灰度编排。

## 1. 现状能力矩阵（builtin 通道）

| 能力 | 现状 | 说明 |
|---|---|---|
| 首次部署 | ✅ | 通过 SSH 直连或 agent 任务在目标机执行；`releases/<id>` 独立目录 + `current` 原子切换 |
| 制品校验 | ✅ | url 型强制 sha256；upload 型服务端计算并校验；校验失败不切 `current` |
| 失败不破坏线上 | ✅ | 先落盘、后切换；失败自动执行逆序回滚步骤 |
| 秒级回滚 | ✅ | `current` 指回上一版本，不重新下载 |
| 多机并发 / 分批 | ✅ | 发布级 concurrency + 全局上限；逐台状态与流式日志 |
| 环境变量 / 密钥注入 | ✅ | `env_json` → `<target>/.leadstack.env`（0600、日志脱敏、base64 落盘） |
| 部署后健康检查 | ✅ | `healthcheck_path` + `healthcheck_port`，控制端真实 HTTP 探测；失败自动回滚并标 `health-check` |
| 已有 systemd 服务接管 | ✅ | `inplace` + `restart_cmd = systemctl restart <unit>`：只换文件，**不碰原 unit 与 EnvironmentFile** |
| 已有目录/静态站接管 | ✅ | `inplace`：备份到 `.leadstack-backups/` → rsync 覆盖（保留 `keep_paths`）→ 重启 |
| 导入已有应用（自动发现） | ✅ | `POST /servers/{id}/discover`：systemd / docker / 宝塔站点目录 → 候选 + 建议参数，前端一键回填 |
| 脏数据迁移（首版带数据） | ✅ | `init_script` 只在「该服务器第一次成功部署」执行一次 |
| 上传制品供目标机下载 | ✅ | 短时效签名地址（此前只有 JWT 路由，目标机实际拉不到——本轮修掉） |

`builtin` 的模板就是 shell/systemd/compose 文本 + 占位符（`{{ROOT}}`、`{{RELEASE_DIR}}`、`{{ARTIFACT_URL}}`…），
所以**能力上限等于用户的脚本能力**：灵活，但没有护栏（备份、健康检查、exactly-once 初始化都靠自觉）。

## 2. 本轮新增：宝塔面板通道（provider=baota）

面板托管的机器通常没有给控制端开 SSH，或者应用是「面板站点」而不是 systemd 服务。
新通道把初始化部署变成纯 API 流程：

```
Ping → EnsureSite(已存在则只复用) → 读取 zip 制品(校验 sha256) →
备份站点目录 → SaveFileBody 上传 → Unzip 覆盖 → 可选 HTTP 健康检查
```

- 已有站点**绝不改写**目录/端口/SSL 配置（接管的安全底线）；部署目录以面板记录为准；
- 解压/健康检查失败 → 用备份 zip 覆盖回去（best-effort），并在发布日志里明确写「回滚失败/成功」；
- 面板返回 `{"status":false}` 一律当失败，避免「HTTP 200 但没做成」；
- 制品上限 16MB（base64 上传），必须 zip；node/php 进程重启、SSL、反代不在 v1 范围。

验收：`bash scripts/verify_baota.sh` **28 项全绿**（含签名校验、错误密钥、非 zip、绑定类型校验、建站复用）。

## 3. 本轮之后仍缺的能力（按 ROI 排序）

1. **自动生成接管模板** —— 目前发现结果里给出了 `systemctl restart` 与目录，但 systemd 接管仍要求用户
   在面板/服务器侧确认「unit 指向的文件确实在 target_dir 里」；后续可以让向导直接读 `ExecStart` 解析出路径并校验。
2. **按环境/分组的变量注入** —— 现在 `env_json` 是应用级单份；多环境（dev/staging/prod 不同 DB）还需要一层环境表。
3. **systemd unit 自动生成** —— 新项目常见需求：给一个二进制就生成 unit（含 EnvironmentFile 指向 `.leadstack.env`）。
4. **宝塔通道增强** —— SSL 申请、反向代理、node 项目（PM2/Node 项目管理接口）、大制品分片上传。
5. **灰度/分批编排** —— 目前并发上限 + 逐台状态有了，但「先 1 台观察 N 分钟再全量」还没有。

## 4. 样例验证（nengtan / tuoluojiang）

`scripts/verify_deploy_v2.sh`（38 项全绿）用两个真实项目的部署形态做端到端验证：

| 样本 | 首次部署 | 后续发布 | 关键断言 |
|---|---|---|---|
| nengtan（jar + 双前端 + systemd） | inplace 接管 `/…/release`、备份、rsync、init 一次、环境文件 0600、健康检查通过 | 第二版只换文件、init 不重复；坏健康检查 → 失败 + 回滚到上一版 | `.leadstack-backups/backup-<id>.tar.gz` 存在；`conf/`、`web/client` 保留 |
| tuoluojiang（php + compose 数据卷） | 站点目录接管、数据目录列 keep_paths | 第二次发布后 mysql/redis data 完好、旧代码文件被清掉 | 数据文件字节不变；`restart.log` 记录 compose 重启 |

## 5. 运维提示

- 宝塔通道会在站点旁累积 `<站点>.backup-<ts>.zip`，记得定期清理；
- 面板 API 需要控制端出口 IP 白名单，换机器/换出口 IP 后「测试连接」会立刻暴露问题；
- 面板版本差异集中在 `internal/panel/baota/client.go`，字段名对不上时只改那一处。
