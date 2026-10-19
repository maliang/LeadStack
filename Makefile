# LeadStack —— 构建入口（契约见 docs/ARCHITECTURE.md 第 1 / 6 / 12 节）
#
#   make build      ← 交付给用户的命令：构建前端产物 + 内嵌 UI + 两个 Go 二进制
#   make build-go   只构建两个 Go 二进制（跳过前端，前端未就绪时也能用）
#   make cross      交叉编译 5 个目标到 dist/
#   make devssh     DEV-ONLY 测试 SSH 服务（仅本地验收用，**绝不进发布产物**）
#   make test-e2e   依次跑 scripts/verify_m0.sh 与 scripts/verify_m1.sh
#   make help       查看全部目标
#
# 本机 Go 可能不在 PATH 中（例如 /opt/homebrew/bin/go），下面用 command -v 兜底；
# 需要时也可以显式覆盖：make build GO=/path/to/go PNPM=/path/to/pnpm

SHELL := /bin/sh
.DEFAULT_GOAL := help

GO   ?= $(shell command -v go 2>/dev/null || echo /opt/homebrew/bin/go)
PNPM ?= $(shell command -v pnpm 2>/dev/null || echo pnpm)

MODULE   := github.com/maliang/leadstack
BIN_DIR  := bin
DIST_DIR := dist
WEB_DIR  := web
WEBUI_DIR    := internal/webui
WEBUI_DIST   := $(WEBUI_DIR)/dist
CMD_CTL      := ./cmd/leadstack
CMD_AGENT    := ./cmd/leadstack-agent
CMD_DEVSSH   := ./cmd/leadstack-devssh

# 版本信息：优先取 git（无仓库时退化为 dev / none，不报错）
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# 版本注入的变量名；若入口包改名，只需覆盖这个变量
PKG_VERSION_VAR ?= main.version
PKG_COMMIT_VAR  ?= main.commit
PKG_DATE_VAR    ?= main.date

LDFLAGS  := -s -w -X $(PKG_VERSION_VAR)=$(VERSION) -X $(PKG_COMMIT_VAR)=$(COMMIT) -X $(PKG_DATE_VAR)=$(DATE)
GOFLAGS_BUILD := -trimpath

CROSS_TARGETS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64
# 发布用二进制白名单：只有这两个会进 bin/（release）与 dist/（cross）。
# cmd/leadstack-devssh 是 DEV-ONLY 测试 SSH 端点，**不要**加进来；cross 末尾有泄漏断言兜底。
BINARIES := leadstack leadstack-agent
# e2e 验收脚本的日志目录（verify_m0.sh / verify_m1.sh 的 stdout+诊断都落在这里）
E2E_LOG_DIR ?= .e2e-logs

.PHONY: help all build build-go web web-install web-embed webui-placeholder \
        webui-install-script cross run run-agent dev web-dev devssh test-e2e e2e \
        test vet fmt tidy clean docker

help: ## 显示本帮助
	@printf 'LeadStack 构建入口（module: %s）\n\n' '$(MODULE)'
	@printf '用法：make <目标> [VERSION=x.y.z] [GO=/path/to/go]\n\n'
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@printf '\n当前：GO=%s\n      PNPM=%s\n      VERSION=%s COMMIT=%s\n' '$(GO)' '$(PNPM)' '$(VERSION)' '$(COMMIT)'

all: build ## 等价于 build

# ---------------------------------------------------------------------------
# 交付命令：前端 → 内嵌 UI → 两个二进制
# ---------------------------------------------------------------------------
build: web-embed build-go ## 一条命令产出内嵌 UI 的 bin/leadstack 与 bin/leadstack-agent

# 已知限制：build-go 跳过前端，因此不会内嵌 install.sh，此时控制端 GET /install.sh 会 404
# （内嵌 UI 也只是占位页）。需要完整可用的交付物请用 `make build`；M1 会把安装脚本改为正式 embed。
build-go: webui-placeholder ## 只构建 Go 二进制（跳过前端）
	@mkdir -p $(BIN_DIR)
	@printf '>> go build CGO_ENABLED=0 VERSION=%s COMMIT=%s\n' '$(VERSION)' '$(COMMIT)'
	CGO_ENABLED=0 $(GO) build $(GOFLAGS_BUILD) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leadstack $(CMD_CTL)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS_BUILD) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leadstack-agent $(CMD_AGENT)
	@printf '>> 产物：\n'
	@ls -lh $(BIN_DIR)/leadstack $(BIN_DIR)/leadstack-agent

# ---------------------------------------------------------------------------
# 前端
# ---------------------------------------------------------------------------
web-install: ## 安装前端依赖（有 lockfile 时优先 frozen；离线时回落到本地缓存）
	@if [ -f $(WEB_DIR)/package.json ]; then \
		printf '>> pnpm install (%s)\n' '$(WEB_DIR)'; \
		$(PNPM) --dir $(WEB_DIR) install --frozen-lockfile \
			|| $(PNPM) --dir $(WEB_DIR) install --prefer-offline \
			|| $(PNPM) --dir $(WEB_DIR) install; \
	else \
		printf '>> 跳过前端依赖安装：%s/package.json 不存在（frontend 尚未落地）\n' '$(WEB_DIR)'; \
	fi

web: web-install ## 构建前端产物到 web/dist
	@if [ -f $(WEB_DIR)/package.json ]; then \
		printf '>> pnpm build (%s)\n' '$(WEB_DIR)'; \
		$(PNPM) --dir $(WEB_DIR) run build; \
	else \
		printf '>> 跳过前端构建：%s/package.json 不存在\n' '$(WEB_DIR)'; \
	fi

web-embed: web ## 把 web/dist 复制进 internal/webui/dist 供 go:embed（并内嵌安装脚本）
	@if [ -f $(WEB_DIR)/dist/index.html ]; then \
		rm -rf $(WEBUI_DIST); \
		mkdir -p $(WEBUI_DIST); \
		cp -R $(WEB_DIR)/dist/. $(WEBUI_DIST)/; \
		printf '>> 已内嵌 UI：%s/dist -> %s\n' '$(WEB_DIR)' '$(WEBUI_DIST)'; \
	else \
		printf '>> %s/dist 不存在，改为写入占位页\n' '$(WEB_DIR)'; \
		$(MAKE) webui-placeholder; \
	fi
	@$(MAKE) --no-print-directory webui-install-script

# 让控制端 GET /install.sh 直接可用：把 deploy/install.sh 放进内嵌静态目录。
# 唯一事实来源仍是 deploy/install.sh —— 这里只是构建期复制，不产生第二份需要人工同步的脚本。
# 幂等：可重复执行；dist 目录不存在时会先创建。
webui-install-script: ## 内嵌 deploy/install.sh 到 internal/webui/dist/install.sh
	@if [ -d $(WEBUI_DIR) ]; then \
		mkdir -p $(WEBUI_DIST); \
		install -m 0755 deploy/install.sh $(WEBUI_DIST)/install.sh; \
		printf '>> 已内嵌安装脚本：deploy/install.sh -> %s/install.sh\n' '$(WEBUI_DIST)'; \
	else \
		printf '>> %s 尚未创建，跳过安装脚本内嵌\n' '$(WEBUI_DIR)'; \
	fi

# go:embed 需要目录里至少有一个文件；前端未就绪时放一个占位页保证 go build 能过。
# 只在 internal/webui 已存在且 dist 缺失时写入，绝不覆盖真实前端产物。
webui-placeholder: ## 前端未就绪时写入最小占位页，保证 go:embed 可编译
	@if [ -d $(WEBUI_DIR) ]; then \
		if [ ! -f $(WEBUI_DIST)/index.html ]; then \
			mkdir -p $(WEBUI_DIST); \
			printf '%s\n' '<!doctype html>' '<html lang="zh-CN"><head><meta charset="utf-8">' \
				'<title>LeadStack</title></head><body>' \
				'<p>前端产物尚未构建：请运行 <code>make build</code>。</p>' \
				'</body></html>' > $(WEBUI_DIST)/index.html; \
			printf '>> 写入占位页 %s/index.html（运行 make build 后会被真实产物覆盖）\n' '$(WEBUI_DIST)'; \
		fi; \
	else \
		printf '>> %s 尚未创建，跳过占位页（等待 backend 落地）\n' '$(WEBUI_DIR)'; \
	fi

web-dev: ## 启动前端 dev server（Vite，/api 代理到 127.0.0.1:8080）
	$(PNPM) --dir $(WEB_DIR) run dev

# ---------------------------------------------------------------------------
# 运行 / 调试
# ---------------------------------------------------------------------------
run: build-go ## 构建并启动控制端
	LEADSTACK_DATA_DIR=$${LEADSTACK_DATA_DIR:-./data} ./$(BIN_DIR)/leadstack

run-agent: build-go ## 构建并启动 agent（AGENT_SERVER=... AGENT_TOKEN=...）
	./$(BIN_DIR)/leadstack-agent --server '$(AGENT_SERVER)' --token '$(AGENT_TOKEN)' --verbose

AGENT_SERVER ?= http://127.0.0.1:8080
AGENT_TOKEN  ?=

dev: webui-placeholder ## 开发模式跑控制端（前端热更新请另开 make web-dev）
	LEADSTACK_DATA_DIR=$${LEADSTACK_DATA_DIR:-./data} $(GO) run $(CMD_CTL)

# ---------------------------------------------------------------------------
# 质量
# ---------------------------------------------------------------------------
test: ## 运行全部 Go 测试
	$(GO) test ./...

vet: ## go vet 全部包
	$(GO) vet ./...

fmt: ## gofmt 格式化
	$(GO) fmt ./...

tidy: ## 同步 go.mod/go.sum（注意：go.mod/go.sum 归 lead 所有，改完请告知 lead）
	@printf '>> 注意：go.mod / go.sum 由 lead 维护，本目标仅供 lead 使用\n'
	$(GO) mod tidy

# ---------------------------------------------------------------------------
# 开发用测试端点 / 端到端验收
# ---------------------------------------------------------------------------
# DEV-ONLY：本机没有 sshd、也没有免密 sudo，SSH 路径靠这个测试端点验收
# （见 docs/ARCHITECTURE.md 8.4）。它只落 bin/ 供本地使用，
# **不进 dist/、不进 goreleaser 归档、不进 Docker 镜像**。
devssh: ## 构建 DEV-ONLY 测试 SSH 服务到 bin/leadstack-devssh（不参与发布）
	@mkdir -p $(BIN_DIR)
	@printf '>> go build %s -> %s/leadstack-devssh（DEV-ONLY，不会进发布产物）\n' '$(CMD_DEVSSH)' '$(BIN_DIR)'
	CGO_ENABLED=0 $(GO) build $(GOFLAGS_BUILD) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leadstack-devssh $(CMD_DEVSSH)

# 依次跑两个验收脚本；输出（含脚本内部打印的控制端/agent 日志尾部）tee 到 $(E2E_LOG_DIR)/，
# CI 失败时把该目录作为 artifact 上传。脚本缺失时打印提示并继续，不算失败。
test-e2e: ## 端到端验收：先 verify_m0.sh 再 verify_m1.sh（前置：make build && make devssh）
	@mkdir -p $(E2E_LOG_DIR)
	@printf '>> 前置产物：bin/leadstack、bin/leadstack-agent（make build）+ bin/leadstack-devssh（make devssh）\n'
	@if [ -f scripts/verify_m0.sh ]; then \
		printf '>> [1/2] bash scripts/verify_m0.sh（日志：%s/verify_m0.log）\n' '$(E2E_LOG_DIR)'; \
		bash -o pipefail -c 'TMPDIR="$(CURDIR)/$(E2E_LOG_DIR)" bash scripts/verify_m0.sh 2>&1 | tee "$(E2E_LOG_DIR)/verify_m0.log"'; \
	else \
		printf '>> scripts/verify_m0.sh 不存在，跳过 M0 验收\n'; \
	fi
	@if [ -f scripts/verify_m1.sh ]; then \
		printf '>> [2/2] bash scripts/verify_m1.sh（日志：%s/verify_m1.log）\n' '$(E2E_LOG_DIR)'; \
		bash -o pipefail -c 'TMPDIR="$(CURDIR)/$(E2E_LOG_DIR)" bash scripts/verify_m1.sh 2>&1 | tee "$(E2E_LOG_DIR)/verify_m1.log"'; \
	else \
		printf '>> scripts/verify_m1.sh 尚未就绪（由 lead 编写）：跳过 M1 验收；脚本落地后本目标自动纳入，无需改 Makefile。\n'; \
	fi

e2e: test-e2e ## test-e2e 的别名

# ---------------------------------------------------------------------------
# 交叉编译 / 清理
# ---------------------------------------------------------------------------
cross: webui-placeholder ## 交叉编译 5 个目标到 dist/（只出发布白名单，不含 DEV-ONLY devssh）
	@mkdir -p $(DIST_DIR)
	@set -eu; for target in $(CROSS_TARGETS); do \
		os=$${target%%/*}; arch=$${target##*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		for name in $(BINARIES); do \
			out="$(DIST_DIR)/$${name}_$(VERSION)_$${os}_$${arch}$${ext}"; \
			printf '>> %s\n' "$$out"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS_BUILD) \
				-ldflags "$(LDFLAGS)" -o "$$out" ./cmd/$$name; \
		done; \
	done
	@printf '>> dist/ 产物：\n'
	@ls -lh $(DIST_DIR)
	@if ls $(DIST_DIR)/*devssh* >/dev/null 2>&1; then \
		printf '>> 错误：dist/ 出现 DEV-ONLY 的 devssh 产物，发布产物被污染（检查 BINARIES/CROSS_TARGETS）\n' >&2; \
		exit 1; \
	fi
	@printf '>> 断言通过：dist/ 中没有 DEV-ONLY devssh 产物\n'

docker: ## 构建控制端容器镜像（需要本地 docker）
	docker build -t leadstack:$(VERSION) .

clean: ## 清理构建产物（bin/ dist/ web/dist/ internal/webui/dist/）
	rm -rf $(BIN_DIR) $(DIST_DIR) $(WEB_DIR)/dist $(WEBUI_DIST)
	@printf '>> 已清理 bin/ dist/ %s/dist %s\n' '$(WEB_DIR)' '$(WEBUI_DIST)'
