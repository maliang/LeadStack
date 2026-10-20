# LeadStack 控制端多阶段构建（[devops]）
#   阶段 1：node 构建前端 SPA          -> web/dist
#   阶段 2：golang 构建两个 Go 二进制   -> /out/{leadstack,leadstack-agent}
#   阶段 3：alpine 运行（非 root，暴露 8080，/data 卷）
#
# 构建：
#   docker build -t leadstack:local .
# 运行：
#   docker run -d --name leadstack -p 127.0.0.1:8080:8080 -v leadstack-data:/data leadstack:local
#   docker logs leadstack   # 首次启动打印随机管理员密码
#
# 基础镜像可用 --build-arg 覆盖（内网镜像时很方便）：
#   docker build --build-arg GO_IMAGE=golang:1.27-alpine .
#
# Go 版本注意：go.mod 的 go 指令是 1.27.2，构建镜像的 Go 不能比它旧，
# 否则会直接报 "go.mod requires go >= 1.27.2"。默认的 golang:1.27-alpine 是滚动 tag，
# 始终指向 1.27 系列最新补丁（≥1.27.2），因此安全；若内网镜像固定在更旧的补丁，
# 请改用 golang:1.27.2-alpine（或更新），或依赖 GOTOOLCHAIN=auto 自动切换（需网络）。

ARG NODE_IMAGE=node:26-alpine
ARG PNPM_VERSION=10
ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.22

# ---------------------------------------------------------------------------
# 阶段 1：前端
# ---------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS web
ARG PNPM_VERSION
# web/package.json 未锁定 packageManager，这里显式装 pnpm，避免 corepack 交互式下载
RUN npm install -g pnpm@${PNPM_VERSION} && pnpm --version
WORKDIR /src
COPY web/ ./web/
RUN pnpm --dir web install --frozen-lockfile || pnpm --dir web install
RUN pnpm --dir web build

# ---------------------------------------------------------------------------
# 阶段 2：Go（CGO_ENABLED=0，modernc sqlite 为纯 Go 实现，静态可跑）
# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS build
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用前端阶段产物覆盖 internal/webui/dist，保证 UI 真正内嵌进二进制
COPY --from=web /src/web/dist ./internal/webui/dist
# 让容器内 GET /install.sh 直接可用（与 make build 行为一致；安装脚本唯一来源仍是 deploy/install.sh）
COPY deploy/install.sh ./internal/webui/dist/install.sh
# 注意：只构建发布用二进制。cmd/leadstack-devssh 是 DEV-ONLY 测试 SSH 端点，
# 不在此处构建、也不进镜像（见 docs/ARCHITECTURE.md 8.4）。
RUN go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o /out/leadstack ./cmd/leadstack \
 && go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o /out/leadstack-agent ./cmd/leadstack-agent

# ---------------------------------------------------------------------------
# 阶段 3：运行
# ---------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS runtime
LABEL org.opencontainers.image.title="LeadStack" \
      org.opencontainers.image.description="LeadStack 控制端（API + Web UI + Agent WebSocket）" \
      org.opencontainers.image.source="https://github.com/maliang/leadstack"

RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S leadstack \
 && adduser -S -G leadstack -h /data leadstack

COPY --from=build /out/leadstack /usr/local/bin/leadstack
COPY --from=build /out/leadstack-agent /usr/local/bin/leadstack-agent

RUN mkdir -p /data && chown leadstack:leadstack /data
USER leadstack

ENV LEADSTACK_LISTEN=0.0.0.0:8080 \
    LEADSTACK_DATA_DIR=/data

# 持久化：
#   /data —— SQLite 数据库 + 上传型制品（<data_dir>/artifacts）+ 备份归档（<data_dir>/backups）
#            + 解密口令（<data_dir>/backup.pass）。三类数据同卷，否则容器重建后
#            制品下载 404、历史备份与口令一起消失。
#   ⚠ 备份目录在 /data 卷内，备份数据随卷持久化；但 backup.pass 是解密备份的唯一钥匙，
#     **必须单独备份**（拷出卷外、与 .lsbak 分开保管）——丢了它，备份就永久解不开。
#     备份归档与口令文件都不会进镜像（.dockerignore 已排除 backups/、*.lsbak、backup.pass）。
VOLUME ["/data"]
EXPOSE 8080

# M4 部署根目录（可选，仅当控制端自己也作为部署目标时才需要）：
#   目标机上的 <root>/current -> releases/<ts>-<hash> 快照、.leadstack/ 元数据都属于
#   运行时状态，**不在镜像内**（.dockerignore 已排除 artifacts/ 与各种 deploy root 目录）。
#   容器场景请用 volume 把 LEADSTACK_DEPLOY_ROOT 挂进来，例如：
#     docker run -v leadstack-deploy:/opt/leadstack/apps -e LEADSTACK_DEPLOY_ROOT=/opt/leadstack/apps ...
#   或参考 deploy/docker-compose.yml 里被注释掉的 deploy-root 示例。

# 备份相关环境变量（均可选，对应 REST /backup-settings）：
#   LEADSTACK_BACKUP_INTERVAL         定时备份间隔（Go duration，默认 24h）
#   LEADSTACK_BACKUP_RETENTION        保留份数（默认 7，超出按时间删最旧）
#   LEADSTACK_BACKUP_INCLUDE_ARTIFACTS 备份是否包含上传制品（默认 false，制品体积大）
#   LEADSTACK_BACKUP_PASSPHRASE       口令（优先于 <data_dir>/backup.pass；生产建议用文件或挂载 secret，
#                                     不要把口令写进镜像 ENV）

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/leadstack"]
