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

## 3. systemd 部署的升级（推荐方式）

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

## 9. 发布流程（维护者视角）

```bash
# 1) 本地验证
make test && make fmt-check && make test-e2e

# 2) 打 tag 即发布（CI 会跑检查 → goreleaser 交叉编译 5 平台 → 产出 Release）
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

- 发布物：5 平台 × 2 二进制（共 10 个）+ 归档 + `checksums.txt`。
- 本地干跑（不发布）：`make release-snapshot`（产物在 `dist/`）。
- 镜像：`make docker-check` 可本地构建并做容器内健康检查 + 备份校验。
- 未签名的说明：当前**没有** macOS 公证与 Windows 代码签名证书，macOS 首次运行可能需要在「系统设置 → 隐私与安全性」放行；Windows 可能弹 SmartScreen。证书就绪后在 CI 里加 notarize/sign 段即可。

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
