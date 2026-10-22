# 升级链路验证报告（真实 GitHub × 多平台）

> 对象：`https://github.com/maliang/LeadStack`，发行版 `v0.1.0` / `v0.1.1`
> 结论：**升级地址正确**；**macOS 与 Linux（amd64/arm64）实测可平滑升级且落盘二进制与官方产物逐字节一致**；
> 但验证中发现一个**会阻断一键升级的资产选择缺陷**（已修复），见第 4 节。

## 1. 地址核对

| 用途 | 代码中的值 | 实测 |
|---|---|---|
| 检查更新 API | `https://api.github.com/repos/maliang/leadstack/releases/latest` | **200**（GitHub 对 owner/repo 大小写不敏感；仓库实际为 `LeadStack`） |
| 发行版资产下载 | `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>` | **200**（`checksums.txt` 实测可下载） |
| 安装脚本（release notes 内） | `https://raw.githubusercontent.com/maliang/LeadStack/v0.1.1/deploy/install.sh` | **200** |
| 校验清单 | 与所选资产同目录的 `checksums.txt`（必填，缺失即中止） | 存在且与资产匹配 |

要点：
- 控制端的仓库与 API 基址来自配置（`--update-repo` / `--update-api-base`，或 `LEADSTACK_UPDATE_REPO` / `LEADSTACK_UPDATE_API_BASE`），默认值即上表；
- `deploy/install.sh` 的 `REPO` 默认也是 `maliang/leadstack`（大小写不敏感，实测可下载）；
- **sha256 是硬性要求**：下载后必须能在 `checksums.txt` 找到对应条目，否则中止升级。

## 2. 平台矩阵（实测）

| 平台 | 方式 | 结果 | 证据 |
|---|---|---|---|
| **macOS arm64** | 真实 `api.github.com` + 真实资产 | ✅ 成功 | 0.1.0 → v0.1.1；升级后二进制 sha256 `e38e18fc…` 与官方 `leadstack_0.1.1_darwin_arm64` 内二进制**完全一致**；备份 `leadstack-20261022-020233.lsbak`（12 KB）；升级历史 `outcome=success`；升级前创建的服务器仍在 |
| **Linux amd64** | 容器内跑 fixed 构建，资产用真实 v0.1.1 归档（本地镜像，见 §5） | ✅ 成功 | 0.1.0 → v0.1.1；二进制 sha256 `f688f50d…` 与官方 `linux_amd64` 一致；`.prev` 保留；数据保留 |
| **Linux arm64** | 同上（arm64 容器） | ✅ 成功 | 二进制 sha256 `1ffdd125…` 与官方 `linux_arm64` 一致 |
| **Windows amd64** | 静态验证 + 交叉编译 | ⚠️ 按设计不支持自升级 | `platformUpgradeUnsupported()` 在 apply 入口直接返回 403：`Windows 不支持控制台自升级：请手动下载新版本替换可执行文件后重启`；发行版有 `leadstack_0.1.1_windows_amd64.zip` 可手动替换 |
| **agent 自升级** | 代码审阅 | ⚠️ Windows 上不可靠 | agent 用 `os.Rename` 替换运行中的可执行文件；Windows 会拒绝重命名运行中的 exe（Linux/macOS 正常） |

负路径（Linux 实测）：
- 已是最新再升级 → `400 目标版本 v0.1.1 不高于当前版本 0.1.1`；
- 目标版本与最新版本不一致 → `400 目标版本 v1.0.0 与最新版本 v0.1.1 不一致`（需先刷新）；
- 备份目录不可写 → 在 `phase=backup` 中止，二进制不变、服务不受影响；
- 新版本一启动即崩溃 → watchdog 自动回退，`upgrade-history` 记 `outcome=rollback`。

## 3. 升级流程（实测行为）

1. 检查更新：返回 `current/latest/has_update/upgradable/schema_changed/assets/published_at/notes`；
2. 发起升级（`POST /system/update/apply`）：`validating → backup → download → verify_sha256 → extract → swap → restart → self-check`；
3. `schema_changed=true` 的版本（如 v0.1.1 含迁移 0008）会强制先生成备份；
4. 替换使用同目录两次原子 `rename`，旧文件保留为 `<exe>.prev`；
5. 独立 watchdog（`Setsid`）在新进程自检通过前待命，超时未通过则换回 `.prev` 并记录回退。

## 4. 发现并修复的问题

### 4.1 资产选择会误选 agent 包（阻断升级，已修复）

**现象**（用官方 v0.1.0 二进制 + 真实发行版资产复现）：

```
检查更新 assets: [leadstack-agent_0.1.1_darwin_amd64.tar.gz, …, leadstack_0.1.1_linux_amd64.tar.gz, …]
发起升级 → 下载 3384047 字节（= leadstack-agent_0.1.1_linux_amd64.tar.gz）
        → WARN 升级中止 phase=extract err="升级包里没有找到 leadstack 可执行文件（已中止）"
```

**原因**：GitHub 按文件名字母序返回资产，`leadstack-agent_*` 排在 `leadstack_*` 之前；
旧逻辑只按「os/arch 命中第一个」挑选，于是选中了**节点 agent 包**。发行版里两类归档同名结构（都不含 `leadstack`），
解包时找不到控制端二进制 → 升级必然失败（好在是**安全失败**：升级前已备份，二进制未被替换，服务继续跑旧版本）。

**修复**：
- 新增 `classifyAsset()`：只承认 `leadstack_<ver>_<os>_<arch>.{tar.gz,zip}` 这类**控制端**归档，跳过 `leadstack-agent_*` 与 `checksums.txt`；
- `pickAsset()` 增加兜底校验（即便上游缓存混入 agent 包也不会选）；
- 回归测试 `internal/api/update_asset_test.go` 使用**真实 v0.1.1 的 11 个资产名与顺序**断言：只会挑到控制端包；
- `scripts/verify_upgrade.sh` 的 mock 也按真实顺序下发 agent 归档（内含假 `leadstack-agent`），端到端守住该回归。

### 4.2 Windows 的「可升级」语义（已改进）

旧版本在 Windows 上仍返回 `upgradable=true`，点击升级才拿到 403。现改为：
`upgradable=false` + 新增 `upgrade_note`（申请自升级时同样返回 403 与同一句原因），
控制台在按钮旁直接显示「Windows 不支持控制台自升级：请手动下载新版本替换可执行文件后重启」。

## 5. 复现方式

```bash
# 1) macOS：直接跑官方或本地构建的 v0.1.0，然后
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"..."}' | jq -r .data.token)
curl -s -X POST http://127.0.0.1:8080/api/v1/system/update/check -H "Authorization: Bearer $TOKEN" | jq .data
curl -s -X POST http://127.0.0.1:8080/api/v1/system/update/apply \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"version":"v0.1.1"}' | jq .
curl -s http://127.0.0.1:8080/api/v1/system/update/status -H "Authorization: Bearer $TOKEN" | jq .data.history

# 2) Linux（本机无法直连 GitHub 时）：把真实资产镜像到本地，用 --update-api-base 指过去
#    资产名与顺序必须与真实发行版一致，否则测不出 4.1 那类问题
LEADSTACK_UPDATE_API_BASE=http://<mirror>:18096 ./leadstack

# 3) 全链路自动化（mock）
make build && bash scripts/verify_upgrade.sh     # 20 项断言
```

## 6. 平台限制与建议

1. **已发布的 v0.1.0 / v0.1.1 含 4.1 的缺陷**：这两个版本上的「一键升级」会在解包阶段中止（安全失败，服务不受影响）。
   - 老用户：**手动下载一次**新版本（`leadstack_<ver>_<os>_<arch>.tar.gz`），替换二进制并重启；
   - 之后发布（含修复）版本起，一键升级恢复正常。
2. **Windows**：控制端与 agent 的自升级都不支持（运行中的 exe 不可替换），请按安装包手动替换；
   控制台已明确提示，不再给「点了才发现不行」的按钮。
3. **agent 升级**：Linux/macOS 正常（下载 → 校验 → 原子 rename → 重启）；Windows 依赖人工。
4. 升级需要能访问 `api.github.com`、`github.com`、`objects.githubusercontent.com`；内网环境请用 `--update-api-base` 指向镜像。
