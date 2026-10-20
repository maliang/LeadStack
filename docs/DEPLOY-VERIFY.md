# 发布工程验证记录（GoReleaser / Docker）

> 对应 `docs/ARCHITECTURE.md` 第 47 节验收第 13 条：
> `goreleaser release --snapshot --clean` 成功产出 **5 平台 10 个二进制**；docker 可用时
> `docker build` 成功且容器 `/healthz` 返回 200。
>
> 本文记录的是**本机真实执行**的过程与输出（macOS 27.0.1 / arm64 / Apple M1 Pro），
> 不是纸面推演。所有命令都可以照抄复现。

---

## 0. 环境与工具链

| 项 | 值 |
|---|---|
| 主机 | macOS 27.0.1 / arm64（Apple M1 Pro，32 GB） |
| Go | 1.27.2（`/opt/homebrew/bin/go`） |
| Node / pnpm | 26.11.1 / 10.34.7 |
| goreleaser | **2.18.3**（`brew install goreleaser` 安装） |
| docker CLI | **29.9.1**（`brew install docker`） |
| colima | **0.10.3** + lima 2.2.1（`brew install colima`） |
| 虚拟化 | `sysctl kern.hv_support = 1`（可用） |
| git | 仓库有 commit、**无 tag**、工作区 dirty（快照模式下 goreleaser 允许） |

安装命令（本机原先三者都没有）：

```sh
brew install goreleaser colima docker
colima start --cpu 4 --memory 6 --disk 30
```

---

## 1. GoReleaser

### 1.1 配置校验

```console
$ goreleaser check
  • checking                                  path=.goreleaser.yaml
  • 1 configuration file(s) validated
  • thanks for using GoReleaser!
```

### 1.2 快照发布（官方命令，含 before hook）

```console
$ goreleaser release --snapshot --clean
  • snapshotting
    • building snapshot...                           version=0.0.1-snapshot
  • running before hooks
    • running                                        hook=make web-embed
  ...（10 个 target 依次构建）...
  • archives       （10 个归档：linux/darwin 用 tar.gz，windows 用 zip）
  • calculating checksums
  • writing artifacts metadata
  • release succeeded after 8s
  • thanks for using GoReleaser!
```

`version=0.0.1-snapshot` 的来源：仓库当前**没有任何 git tag**，goreleaser 取 `v0.0.0` 再用
`snapshot.version_template: "{{ incpatch .Version }}-snapshot"` 递增补丁位。
打 tag 后（`git tag v1.2.3 && git push --tags`）则用真实版本号，归档名同步变成
`leadstack_1.2.3_<os>_<arch>.tar.gz`，与 `deploy/install.sh` 的拼装规则一致。

### 1.3 产物清单（10 个二进制，5 平台 × 2 程序）

```console
$ find dist -type f \( -name 'leadstack' -o -name 'leadstack-agent' \
    -o -name 'leadstack.exe' -o -name 'leadstack-agent.exe' \) | sort
dist/leadstack-agent_darwin_amd64_v1/leadstack-agent
dist/leadstack-agent_darwin_arm64_v8.0/leadstack-agent
dist/leadstack-agent_linux_amd64_v1/leadstack-agent
dist/leadstack-agent_linux_arm64_v8.0/leadstack-agent
dist/leadstack-agent_windows_amd64_v1/leadstack-agent.exe
dist/leadstack_darwin_amd64_v1/leadstack
dist/leadstack_darwin_arm64_v8.0/leadstack
dist/leadstack_linux_amd64_v1/leadstack
dist/leadstack_linux_arm64_v8.0/leadstack
dist/leadstack_windows_amd64_v1/leadstack.exe

$ # 计数
10
```

平台正确性（`file` 抽样）：

```console
dist/leadstack_windows_amd64_v1/leadstack.exe: PE32+ executable (console) x86-64, for MS Windows
dist/leadstack_linux_arm64_v8.0/leadstack:     ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped
dist/leadstack_darwin_amd64_v1/leadstack:      Mach-O 64-bit executable x86_64
```

### 1.4 归档与校验和

```console
$ ls dist/*.tar.gz dist/*.zip | wc -l
10

$ wc -l < dist/checksums.txt
10

$ head -3 dist/checksums.txt
69aaa597706e4e3af1c2ed957127513958ad5649788a1bb8c541cd34d865e89e  leadstack-agent_0.0.1-snapshot_darwin_amd64.tar.gz
2d3e7a5a84fd80bae7eaedf931a1e2d1e8a9bd63d57819e1288e29b371a2e2e7  leadstack-agent_0.0.1-snapshot_darwin_arm64.tar.gz
5343fcfe2e57eccba833e61520fabf76b9e774cbd09037ab64d0a0ae53d6b799  leadstack-agent_0.0.1-snapshot_linux_amd64.tar.gz
```

归档内容抽样（`leadstack-agent` linux/amd64）：

```console
$ tar tzf dist/leadstack-agent_0.0.1-snapshot_linux_amd64.tar.gz
deploy/install.sh
deploy/leadstack-agent.service
docs/ARCHITECTURE.md
docs/PLAN.md
...（其余 docs/*.md）
leadstack-agent
```

**DEV-ONLY 排除校验**：遍历全部 10 个归档，`devssh` 命中数为 **0** ——
`cmd/leadstack-devssh` 没有进入任何发布产物（`.goreleaser.yaml` 的 builds 是显式白名单，
`make cross` 末尾还有一道 dist/ 断言）。

### 1.5 版本注入结果

`leadstack-agent` 有 `--version`：

```console
$ ./dist/leadstack-agent_darwin_arm64_v8.0/leadstack-agent --version
leadstack-agent 0.0.1-snapshot (commit none, built 2026-10-20T03:19:10Z)
```

`leadstack`（控制端）目前**没有 `--version` 子命令/参数**（它只解析
`-listen/-data-dir/-admin-password` 等 flag），版本从启动日志与 `/healthz` 读取：

```console
$ LEADSTACK_LISTEN=127.0.0.1:<port> LEADSTACK_DATA_DIR=<tmp> \
    ./dist/leadstack_darwin_arm64_v8.0/leadstack
# 启动日志
version=0.0.1-snapshot commit=none

$ curl -s http://127.0.0.1:<port>/healthz
{"data":{"status":"ok","version":"0.0.1-snapshot","uptime_s":0}}
```

两条路径都证明 `-X main.version/commit/date` 注入生效（与 Makefile 的 LDFLAGS 同源）。
> 备注：控制端加 `--version` 属于 Go 代码改动，不在工程化写范围内；已在本记录中如实标注。

### 1.6 过程中遇到的两个真实阻塞（非 goreleaser 问题）

第一次执行官方命令时失败，原因是**并行开发中的代码/前端尚未收敛**，与发布配置无关：

1. `before.hooks` 的 `make web-embed` 失败：
   `src/i18n/index.ts(2,18): error TS2307: Cannot find module './zh-CN'`（前端 i18n 正在落地）。
2. Go 编译失败：`internal/api/handlers_remote.go:54:20: undefined: remoteRunnerAdapter`、
   `internal/api/handlers_terminal.go:165:48: undefined: mustJSON`（后端 M6 代码未提交完整）。

处理方式：先用 `goreleaser release --snapshot --clean --skip=before`（复用已存在的
`internal/webui/dist`）验证**发布配置本身**可用（10 个二进制照常产出），
等两位同学各自收敛后再跑官方命令 —— 最终结果见 1.2，**`release succeeded`**。

---

## 2. Docker

### 2.1 colima 启动

```console
$ colima start --cpu 4 --memory 6 --disk 30
...
INFO[0011] READY. Run `limactl shell colima` to open the shell.
INFO[0012] Successfully created context "colima"
INFO[0012] Current context is now "colima"
INFO[0012] done

$ docker info --format '{{.ServerVersion}} / {{.Architecture}} / {{.OperatingSystem}}'
29.5.2 / aarch64 / Ubuntu 24.04.4 LTS
```

### 2.2 首次构建失败：VM 内 DNS 不可用（真实报错）

```console
$ docker build -t leadstack:dev .
...
Step 5/35 : FROM ${NODE_IMAGE} AS web
failed to resolve reference "docker.io/library/node:26-alpine":
  failed to do request: Head "https://registry-1.docker.io/v2/library/node/manifests/26-alpine":
  dial tcp: lookup registry-1.docker.io on [::1]:53: read udp [::1]:54031->[::1]:53:
  read: connection refused
```

根因：colima/Lima 虚拟机里 `/etc/resolv.conf` 是指向
`/run/systemd/resolve/stub-resolv.conf` 的软链，但该文件（systemd-resolved stub）在镜像里
**不存在**，导致 dockerd 把 DNS 查询发到 `[::1]:53` 且无人监听。
`colima start --dns 8.8.8.8 --dns 1.1.1.1` 不足以覆盖（dockerd 读的是 VM 内的 resolv.conf）。

#### 修复：一条可复制的命令

> **⚠️ 只对当前运行的 VM 有效。`colima stop` / `colima start` / 重启电脑后必须重跑一次**
> （症状会重现为 `docker build` 报 `lookup ... [::1]:53: connection refused`）。
> 它只改虚拟机内的 `/etc/resolv.conf`，不改仓库任何文件。

```sh
colima ssh -- sudo sh -c 'rm -f /etc/resolv.conf; printf "nameserver 223.5.5.5\nnameserver 114.114.114.114\nnameserver 1.1.1.1\n" > /etc/resolv.conf'
```

验证（应看到 `Status: Downloaded newer image`）：

```sh
docker pull alpine:3.22
```

希望少重跑可以在启动时就指定 DNS（部分 colima 版本有效，仍建议启动后跑上面那条兜底）：

```sh
colima start --dns 223.5.5.5 --dns 1.1.1.1
```

### 2.3 `make docker-check` 实际结果

```console
$ make docker-check
>> docker build -t leadstack:dev .
Step 1/35  : ARG NODE_IMAGE=node:26-alpine
...
Step 9/35  : RUN pnpm --dir web build          ← 前端阶段（node:26-alpine）
...
Step 17/35 : RUN go mod download               ← Go 阶段（golang:1.27-alpine）
Step 20/35 : COPY . .
Step 21/35 : COPY --from=web /src/web/dist ./internal/webui/dist
Step 22/35 : COPY deploy/install.sh ./internal/webui/dist/install.sh
Step 23/35 : RUN go build ... ./cmd/leadstack && go build ... ./cmd/leadstack-agent
...
Step 35/35 : ENTRYPOINT ["/usr/local/bin/leadstack"]
Successfully built d731a8aa1ded
Successfully tagged leadstack:dev

>> docker run -d -p 18080:8080 -v leadstack-smoke-data:/data
8a07e18107a5...
>> 容器 /healthz → 200
>> docker-check 完成（容器与卷已清理）
```

镜像大小与启动：

```console
$ docker images leadstack --format '{{.Repository}}:{{.Tag}}  {{.Size}}'
leadstack:dev  53.7MB

$ docker run -d --name leadstack-smoke -p 18080:8080 -v leadstack-smoke-data:/data leadstack:dev
$ curl -s http://127.0.0.1:18080/healthz
{"data":{"status":"ok","version":"dev","uptime_s":0}}
```

Dockerfile 的每个 `COPY` / `RUN` 路径都在真实构建中被执行过（35 步全部通过），
包括 `COPY deploy/install.sh`（依赖 `.dockerignore` 里 `deploy/*` + `!deploy/install.sh` 的放行规则）。

### 2.4 容器内 `leadstack backup` 可用（真实生成 + 校验）

```console
$ docker exec leadstack-smoke leadstack backup --help
Usage of backup:
  -data-dir string
    	数据目录 (default "/data")
  -out string
    	...

$ docker exec leadstack-smoke sh -c 'mkdir -p /data/backups; \
    leadstack backup --data-dir /data --out /data/backups/smoke.lsbak'
# 归档 magic 与体积
LSBAK1
10814            # bytes

$ docker exec leadstack-smoke leadstack backup verify --file /data/backups/smoke.lsbak
校验通过
  文件: /data/backups/smoke.lsbak
  应用版本: dev
  数据库版本: 7
  含制品: false
  创建时间: 2026-10-20T03:22:25Z
  文件清单:
    leadstack.db                 339968 字节  sha256=7a02b176...
    master.key                       32 字节  sha256=c7578ed7...
```

容器内 `/data` 布局（`backup.pass` 权限正确为 0600）：

```console
$ docker exec leadstack-smoke ls -l /data /data/backups
/data:
-rw-------    1 leadstack  backup.pass
drwxr-sr-x    2 leadstack  backups
-rw-r--r--    1 leadstack  leadstack.db
-rw-------    1 leadstack  master.key
drwxr-s---    2 leadstack  artifacts

/data/backups:
-rw-------    1 leadstack  smoke.lsbak
-rw-r--r--    1 leadstack  smoke.lsbak.json
```

清理（容器与命名卷都删除，无残留）：

```console
$ docker rm -f leadstack-smoke && docker volume rm leadstack-smoke-data
$ docker ps -a --filter name=leadstack-smoke --format '{{.Names}}'
（空）
```

### 2.5 Docker 阶段的一个已知注意点

镜像构建的第 1 阶段直接在镜像里跑 `pnpm --dir web build`，所以**前端代码必须可编译**。
本记录期间前端在多次重构（i18n / 主题 / MonitorTab 拆分），曾出现：
`src/i18n/index.ts: Cannot find module './zh-CN'`、`src/lib/prefs.tsx: Cannot find name 'Theme'`、
`src/components/server/MonitorTab.tsx: Cannot find module '../lib/api'`。
这些是并行开发中的瞬时状态，**不是 Dockerfile 缺陷**；前端收敛后同一 Dockerfile 一次构建成功。

---

## 3. 本地/CI 复现方式

```sh
# 本机（需先安装 goreleaser / docker + colima）
make release-snapshot   # = goreleaser release --snapshot --clean（含前端内嵌），产物在 dist/
make docker-check       # build → run → /healthz 200 → 容器内 backup 生成+校验 → 清理
make build              # 交付命令，仍然成功

# 前端正在重构（tsc 暂时报错）时，可以复用已构建好的 internal/webui/dist：
make release-snapshot BUILD_SKIP_WEB=1     # = goreleaser release --snapshot --clean --skip=before
make docker-check DOCKER_CHECK_SKIP_BUILD=1  # 复用已有 leadstack:dev 镜像，只跑 run/健康检查/backup 校验
```

> 这两个开关**只跳过构建环节**，不改变任何产物内容；验证发布配置或容器运行时行为时很有用。
> 前端收敛后请用不带开关的完整命令再跑一次。

清理：`make clean` 会一并删除 `bin/ dist/ web/dist/ internal/webui/dist/`
（本次实测删掉了 170 MB 的 goreleaser 产物）；清理后 `make build` 仍能从零重建成功（exit=0）。

已在本机实际跑通的两种形态：

| 命令 | 结果 |
|---|---|
| `make release-snapshot BUILD_SKIP_WEB=1` | `release succeeded after 5s`，产物清单 10 个二进制 |
| `make docker-check DOCKER_CHECK_SKIP_BUILD=1` | `[ok] backup 子命令可用` / `[ok] 归档 magic=LSBAK1` / `[ok] backup verify 通过` / 清理完成 |

CI（`.github/workflows/ci.yml`）：

| 触发 | 任务 | 说明 |
|---|---|---|
| push / PR | `build-test` | vet + **gofmt 闸门** + test + **go test -race** + `make build` + M0–M3 阻塞验收 + M4/备份/运维允许失败 |
| push / PR | `docker` | `docker build -t leadstack:ci .`（前端就绪时） |
| push / PR | **`release-snapshot`** | `goreleaser check` + `goreleaser build --snapshot --clean`，并断言 **10 个二进制**且不含 devssh |
| tag `v*` | `release` | `goreleaser release --clean` 发布 Release |

---

## 4. 结论

| 验收项 | 结果 |
|---|---|
| `goreleaser check` | ✅ 1 configuration file validated |
| `goreleaser release --snapshot --clean` | ✅ `release succeeded after 8s` |
| 5 平台 × 2 二进制 | ✅ 10 个（linux/darwin × amd64/arm64 + windows/amd64） |
| 归档 + checksums | ✅ 10 个归档、10 行 sha256 |
| 版本注入 | ✅ agent `--version` / 控制端日志与 `/healthz` 均为 `0.0.1-snapshot` |
| DEV-ONLY devssh 排除 | ✅ 10 个归档中命中 0 |
| `docker build` | ✅ 35 步全部通过，镜像 53.7 MB |
| 容器 `/healthz` | ✅ HTTP 200 |
| 容器内 `leadstack backup` | ✅ 生成 10814 字节 `LSBAK1` 归档、`backup verify` 通过 |
| 环境备注 | colima VM 内 DNS 曾不可用（已修，见 2.2），非仓库问题 |

### 最终完整跑（前端收敛后，全部不带开关）

```console
$ make build
>> 已内嵌 UI：web/dist -> internal/webui/dist
>> 已内嵌安装脚本：deploy/install.sh -> internal/webui/dist/install.sh
>> go build CGO_ENABLED=0 VERSION=44a1f0f-dirty COMMIT=44a1f0f
exit=0

$ make release-snapshot
>> goreleaser release --snapshot --clean （VERSION=44a1f0f-dirty）
  • release succeeded after 6s
>> 产物清单：10 个二进制（见 1.3）
>> checksums：10 行

$ make docker-check
>> docker build -t leadstack:dev .
Successfully built 1b32efbad974
Successfully tagged leadstack:dev
>> docker run -d -p 18080:8080 -v leadstack-smoke-data:/data
>> 容器 /healthz → 200
>> 容器内 backup CLI 验证（帮助 + 真实生成归档）：
   [ok] backup 子命令可用
   [ok] 归档 magic=LSBAK1
   [ok] backup verify 通过
>> docker-check 完成（容器与卷已清理）
```

> 扩展名说明：快照构建目录名不带版本（`dist/leadstack_linux_amd64_v1/leadstack`），
> 版本只体现在归档名与 `checksums.txt` 里 —— 这是 goreleaser v2 的默认布局。
