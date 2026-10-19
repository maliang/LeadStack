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

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/leadstack"]
