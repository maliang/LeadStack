# 版本升级与回滚

> 面向自托管用户。控制端升级是**手动**的（不自动联网更新，避免在你没准备好时改数据）；节点 agent 支持控制台一键升级。

---

## 1. 版本号与发布物

- 版本号遵循 **语义化版本** `vX.Y.Z`；`X` 变更 = 不兼容改动（API 或数据库），`Y` = 新功能，`Z` = 修复。
- 每个版本在 GitHub Releases 发布：`leadstack_<version>_<os>_<arch>.tar.gz`、`leadstack-agent_<version>_<os>_<arch>.tar.gz`、`checksums.txt`。
- 查看当前版本：

```bash
leadstack --version
# leadstack v0.2.0 (commit a1b2c3d, built 2026-10-20T08:51:53Z)

curl -s http://127.0.0.1:8080/healthz | jq -r '.data.version'   # 运行中实例的版本
```

> 二进制里的 `version` 由发布流水线注入；源码直接 `make build` 会显示 `<commit>-dirty`，属正常。

---

## 2. 升级前必做（三件事，别跳）

```bash
# 1) 备份（服务运行中也能做，SQLite 一致性快照）
leadstack backup --data-dir /var/lib/leadstack/data --out /root/leadstack-pre-upgrade-$(date +%F).lsbak
leadstack backup verify --file /root/leadstack-pre-upgrade-$(date +%F).lsbak

# 2) 记下当前版本（回滚时要知道回哪去）
leadstack --version | tee /root/leadstack-version-before.txt

# 3) 看一眼 release notes 有没有「不兼容改动 / 需要手工迁移」标记
```

**为什么必须备份**：控制端的数据库迁移是**前向 only**（启动时自动把缺的迁移依次应用，没有 down 迁移）。
升级后如果降到旧版本，**旧二进制不保证能读新库结构**。所以回滚的可靠做法是「旧二进制 + 升级前的备份」，而不是指望旧版本直接打开新库。

> 备份文件里含 `master.key` 与全部凭证的密文，所以归档是加密的。
> **`backup.pass` 必须与备份分开保管**，否则备份等于废纸。

---

## 2.5 控制台一键升级（推荐，含自动回退）

控制端支持「在控制台里点一下完成升级」，不需要命令行：

1. 控制台右上角用户菜单 →「版本与升级」（有更新时会有红点），或「设置 → 网络与端口」旁的升级卡片；
2. 面板会显示 当前版本 → 最新版本、release notes，以及**风险提示**：会自动备份；若含数据库迁移会额外红色警示；
3. 点击「立即升级」→ 二次确认 → 自动执行 **备份 → 下载并校验 sha256 → A/B 替换 → 重启**；
4. 失败/崩溃时，独立 watchdog 会在超时后**自动回退**到旧版本（有迁移时连同数据一起恢复到升级前备份）；
5. 重启期间页面会短暂失联，面板会自动重连并显示新版本。

> 关键设计：watchdog 运行在独立会话中，与 systemd 解耦；回退完成后立刻删除 `upgrade/state.json`，
> 因此 `Restart=always` 下一次拉起的已经是旧版本——不会出现"无限重启坏二进制"。
>
> 关闭它：`LEADSTACK_UPGRADE_ENABLED=false`（或 `--upgrade-enabled=false`）。
> 相关参数：`LEADSTACK_UPGRADE_ROLLBACK_TIMEOUT`（默认 120s，最小 10s）、`LEADSTACK_UPDATE_REPO`、
> `LEADSTACK_UPDATE_API_BASE`、`LEADSTACK_UPDATE_CHECK_INTERVAL`（默认 6h，0=只手动检查）。
> 升级历史在 `<data_dir>/upgrade/upgrade-history.jsonl`，控制台状态接口也会返回最近 10 条。

## 3. systemd 部署的升级（手动方式）

```bash
# 1) 下载新版本并校验（以 linux/amd64 为例）
cd /tmp
V=v0.2.0
curl -fLO "https://github.com/maliang/leadstack/releases/download/${V}/leadstack_${V#v}_linux_amd64.tar.gz"
curl -fLO "https://github.com/maliang/leadstack/releases/download/${V}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt          # macOS 用 shasum -a 256 -c

# 2) 解压（拿到 leadstack 与 leadstack-agent 两个二进制）
tar xzf "leadstack_${V#v}_linux_amd64.tar.gz"

# 3) 备份 + 停服务
leadstack backup --data-dir /var/lib/leadstack/data --out /root/pre-upgrade.lsbak
sudo systemctl stop leadstack

# 4) 先放新文件再原子替换（保留旧二进制用于回滚）
sudo install -m 0755 leadstack /usr/local/bin/leadstack.new
sudo mv /usr/local/bin/leadstack /usr/local/bin/leadstack.prev    # 旧版本留档
sudo mv /usr/local/bin/leadstack.new /usr/local/bin/leadstack

# 5) 启动并验证（迁移会在启动时自动执行，看日志确认）
sudo systemctl start leadstack
journalctl -u leadstack -n 60 --no-pager | grep -E "数据库迁移|HTTP 监听|初始管理员"

# 6) 健康检查
curl -s http://127.0.0.1:8080/healthz | jq .
```

**校验要点**：`/healthz` 返回的 `version` 是新版本；日志里能看到新增的迁移记录；登录后抽查一台服务器、一个检测项、一次发布。

### 只升级 agent（不需要动控制端）

agent 是独立二进制，可在控制台里一键升级（见第 5 节），也可手动：

```bash
sudo sh deploy/install.sh --version v0.2.0 --server http://<控制端>:8080 --token <令牌>
```

`install.sh` 支持 `--version`（默认 `latest`）、`--base-url`（自建镜像源）、`--uninstall`，幂等可重跑。

---

## 4. Docker 部署的升级

```yaml
# deploy/docker-compose.yml：**固定版本号**，不要用 latest（升级时机要自己掌控）
services:
  leadstack:
    image: ghcr.io/maliang/leadstack:v0.2.0   # 或自建镜像
```

```bash
# 升级：改 tag → 拉取 → 重建（数据在 /data 卷里，不受影响）
docker compose -f deploy/docker-compose.yml pull
docker compose -f deploy/docker-compose.yml up -d
docker logs --tail 60 leadstack | grep -E "数据库迁移|HTTP 监听"
```

升级前同样先备份（容器内执行，或直接备份 `/data` 卷）：

```bash
docker compose exec leadstack leadstack backup --out /data/backups/pre-upgrade.lsbak
docker cp leadstack:/data/backups/pre-upgrade.lsbak ./          # 拷出来异地保存
docker cp leadstack:/data/backup.pass ./backup.pass             # 口令单独保管！
```

---

## 5. 节点 agent 的升级（控制台一键，安全）

控制端只下发「下载 + 校验 + 替换」的指令，agent 侧用 **A/B 替换**：

1. 下载新版本到**可执行文件同目录**的临时文件；
2. 校验发布包 sha256；
3. 把当前可执行文件 `rename` 成 `.old`，把新文件 `rename` 到位（同一文件系统内原子）；
4. 任一步失败 → **保留旧版本、不断连**，回报失败原因；
5. 成功后按需重启（systemd/launchd 负责拉起；也可由 agent 自行 exec）。

操作位置：控制台「服务器 → 批量操作 → Agent 升级」（可按当前版本筛选），或单台服务器详情。
升级记录会写入审计日志（`agent.upgrade`）。

> 为什么不是「原地覆盖」：Windows 上运行中的 exe 不能删除/覆盖，Unix 上覆盖正在运行的文件也可能导致崩溃。A/B 重命名是唯一跨平台安全的做法。

---

## 6. 升级后的验证清单

| 检查 | 命令 / 位置 | 期望 |
|---|---|---|
| 版本 | `curl /healthz` | `version` 为新版本 |
| 数据库迁移 | `journalctl -u leadstack \| grep 迁移` | 新增迁移均显示「已应用」 |
| 登录 | 浏览器 | 正常，原有账号可登录 |
| 服务器 | 列表页 | 在线机器仍 `online`，凭证仍可解密 |
| 检测/告警 | 检测页 / 告警页 | 规则仍在、状态正常 |
| 发布 | 应用页 | 历史发布记录与 `current` 不受影响 |
| 备份 | 备份页 | 旧备份仍可校验（schema 更旧是允许的） |

---

## 7. 回滚

**情况 A：新版本有问题，但数据库结构没变（补丁版本）**

```bash
sudo systemctl stop leadstack
sudo mv /usr/local/bin/leadstack.prev /usr/local/bin/leadstack   # 换回旧二进制
sudo systemctl start leadstack
```

**情况 B：经历了带迁移的大版本升级（推荐做法）**

```bash
sudo systemctl stop leadstack
sudo mv /usr/local/bin/leadstack.prev /usr/local/bin/leadstack

# 用升级前的备份恢复数据（会先把当前数据移到 .pre-restore-<时间>/ 保留）
leadstack restore --file /root/pre-upgrade.lsbak --data-dir /var/lib/leadstack/data

sudo systemctl start leadstack
```

> ⚠️ 只换回旧二进制而不恢复数据，属于**不受支持**的降级：旧程序读到比它新的库结构，可能报错或行为异常。

---

## 8. 兼容性承诺

| 维度 | 约定 |
|---|---|
| HTTP API | 统一 `/api/v1` 前缀；`v1` 内只做加法，不破坏字段 |
| 数据库 | 迁移只增不减（前向 only）；每个版本启动时自动补齐缺失迁移 |
| 备份格式 | `LSBAK1`；能恢复**同版本或更旧 schema** 的备份，拒绝更新 schema 的备份（`schema_too_new`） |
| agent ↔ 控制端 | 消息契约向后兼容：未知消息类型被忽略而不是断连，因此**可以先升控制端再升 agent** |
| 配置 | 环境变量新增不影响旧值；端口类设置支持 `设置 > 环境变量 > 默认` 并可见来源 |

---

## 9. 发布流程（维护者视角）：主分支自动发版

**正常情况下你什么都不用做**：往 `main`（当前仓库是 `master`，两者都支持）推代码，CI 自动发一个新版本。

```
push 到主分支
   │
   ├─ 改动只有文档（docs/**、*.md、LICENSE…）→ 跳过发版
   ├─ 提交信息含 [skip release]              → 跳过发版
   │
   ▼
prepare：推导版本（最新 tag patch+1）+ 判定 schema 是否变化
   │
   ▼
release：gofmt/vet/test 门禁 → 建 tag → goreleaser「草稿」上传 → 校验资产 → 撤销草稿正式发布
                                          └─ 任一步失败 → 删草稿 Release + 删 tag
```

### 9.1 版本号规则

| 场景 | 结果 |
|---|---|
| 仓库还没有任何 tag | `v0.1.0`（首版） |
| 最新 tag `v1.2.3` | `v1.2.4`（patch + 1） |
| 最新 tag `v1.2.3-rc1` | `v1.2.4`（预发布后缀忽略） |
| 算出来的 tag 已存在 | **跳过**并告警，不覆盖已发布版本 |

本地预览（与 CI 同一脚本、同一规则）：

```bash
make next-version        # 等价于 sh scripts/next-version.sh
```

### 9.2 跳过发版

| 方式 | 做法 |
|---|---|
| 纯文档改动 | 不用管：`paths-ignore` 里的路径（`docs/**`、`**.md`、`LICENSE`…）不会触发 |
| 改了代码但不想发 | 提交信息里写 `[skip release]`，整个 workflow 跳过 |
| 手工指定版本 | Actions → **Release (auto)** → Run workflow → 填 `version`（如 `v1.3.0`），留空则自动推导 |

### 9.3 `schema_changed` 标记（升级前是否必须恢复数据）

每次发布都会在 Release body 末尾写一行机器可读标记：

```html
<!-- schema_changed: true -->
```

判定方式：对比**上一个版本 tag** 与本次提交之间，`internal/store/migrations/` 下是否有**新增**文件。

| 情况 | 标记 |
|---|---|
| 新增了迁移文件（数据库结构变化） | `true` —— 控制端会提示「本次升级涉及数据结构变更，升级前请备份」 |
| 没有新增迁移文件 | `false` —— 常规版本，直接升级即可 |
| 仓库首个版本（还没有上一个 tag） | `true` —— 保守标记，避免漏报 |

> 别手动改这个标记；如果发现标错了，说明迁移文件的提交方式有问题（例如把迁移塞进了别的目录）。

### 9.4 失败不留半成品（为什么可以先建 tag）

流程是「先建 tag → 用 `goreleaser release --clean --draft` 上传到**草稿** Release → 校验资产数 ≥ 11（10 个归档 + checksums.txt）→ 最后 `gh release edit --draft=false` 正式发布」。

- 草稿 Release 不会出现在 `/releases/latest`，也不会出现在 Releases 页面（未登录/普通访客）——**用户不会看到「有新版本但下载 404」**；
- 任一步失败都会触发 `失败清理` 步骤：`gh release delete --yes --cleanup-tag` + `git push origin :refs/tags/<tag>`，tag 和草稿一起消失，下个提交会重新用同一个版本号。

### 9.5 双重发布是怎么避免的（别把发布写回 ci.yml）

- `ci.yml` 现在**只有质量门禁与发布配置验证**（`release-snapshot` 用 `goreleaser build --snapshot`，不发布、不建 tag）；
- **真正发布只有 `release-auto.yml` 一个出口**：它负责版本推导、tag、Release notes（含 `schema_changed` 标记）与 goreleaser 发布；
- 所以即使自动发版 workflow 建了 tag，也没有第二个 workflow 会因此再发一次。
- 反过来说：**手工 `git push` 一个 tag 不会再触发发布**（这是有意的，避免误推 tag 就发版）。要手工发版请用 `workflow_dispatch`。
- 历史注记：早先 `ci.yml` 有一个 tag 触发的 release job。虽然 GitHub 的 `GITHUB_TOKEN` 建 tag 默认不会触发其它 workflow，但一旦有人换成 PAT / GitHub App token，这个隐式保护就失效 —— 所以直接删掉，不依赖它。

### 9.6 回滚一个坏 Release

发出去的版本有问题时，按「影响面」选一种：

```bash
# A) 版本还没人升级、只是想撤掉这个 Release（会同时删 tag）
gh release delete v1.2.4 --yes --cleanup-tag
git push origin :refs/tags/v1.2.4     # 兜底（--cleanup-tag 已删时忽略报错）

# B) 已经有人装了坏版本（推荐）：发一个新 patch 版本修掉它，不要删旧版本
#    修好代码 → 合并 → 自动发 v1.2.5；控制端的升级提示会自然指向 v1.2.5

# C) 用户机器已经升级坏了：用「升级回滚」而不是删 Release
#    控制端会自动保留 <exe>.prev，升级失败/watchdog 超时会自动换回；
#    数据侧用升级前那份备份恢复（见 §7）
```

> 删 Release 不会影响已经下载走的二进制；对已升级的用户，正确动作是发修复版本或引导他们用 §7 回滚。

### 9.7 本地干跑（不发布）

```bash
make next-version      # 预览下一个版本号
make release-snapshot  # goreleaser 全量构建（5 平台 × 2 二进制），产物在 dist/，不发 Release
make docker-check      # 构建镜像 + 容器内 /healthz + 容器内备份生成/校验
make test-e2e          # 七套端到端验收
```

发布物：5 平台 × 2 二进制（共 10 个）+ 归档 + `checksums.txt`。

未签名说明：当前**没有** macOS 公证与 Windows 代码签名证书，macOS 首次运行可能需要在「系统设置 → 隐私与安全性」放行；Windows 可能弹 SmartScreen。证书就绪后在 `.goreleaser.yaml` 里打开 notarize/sign 段即可。

---

## 10. 常见问题

**Q：能不能自动升级？**
控制端不做自动升级——自动改数据库结构的风险高于收益。建议做法：`systemd` 之外用你的配置管理工具（Ansible 等）在**备份之后**按版本号执行升级，并保留上一版本二进制。
agent 支持一键升级，可以放心批量做（A/B 替换 + 校验 + 失败保留旧版）。

**Q：升级后发现新版本有问题，但已经跑了几天、数据变了怎么办？**
不要用旧备份恢复（会丢掉这几天的数据）。优先**等修复版本**，或联系维护者拿热修补丁。只有确认数据可丢弃时才走「情况 B」。

**Q：跨多个大版本能一次跳过去吗？**
可以，启动时会按顺序应用所有缺失迁移。但跨大版本升级前**必须**备份，且建议先在测试实例上演练一遍。

**Q：agent 版本和控制端版本必须一致吗？**
不必须。消息契约向后兼容（未知类型忽略），建议顺序是「先升控制端，再升 agent」。控制台会显示各机器的 agent 版本，便于排查。
