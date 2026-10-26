#!/bin/sh
# =============================================================================
# LeadStack agent 安装 / 卸载脚本
#
# 用法（管道安装，推荐）：
#   curl -fsSL https://raw.githubusercontent.com/maliang/leadstack/main/deploy/install.sh \
#     | sh -s -- --server http://<控制端>:8080 --token <接入令牌>
#
# 本机安装 / 自测：
#   sh deploy/install.sh --dry-run --server http://127.0.0.1:8080 --token t
#   sh deploy/install.sh --uninstall
#
# 选项：
#   --server URL       控制端地址（http/https，或直接给 ws/wss），必填
#   --token TOKEN      agent 接入令牌（POST /api/v1/enroll-tokens 获取），必填
#   --version V        安装版本，默认 latest（自动解析最新 release tag）
#   --agent-version V  --version 的别名（显式指定要安装的 agent 版本，便于回滚）
#   --base-url URL     发布包前缀，默认 https://github.com/maliang/leadstack/releases/download
#   --from-file PATH   直接安装本地二进制（离线场景 / 自测），跳过下载
#   --ca PATH          额外信任的 CA 证书（PEM）；控制端用自签证书时使用
#   --insecure         跳过 TLS 证书校验（自签且无法分发 CA 时使用，不推荐）
#   --install-dir DIR  安装目录，默认 /usr/local/bin
#   --env-dir DIR      环境变量目录，默认 /etc/leadstack（Linux）/ /usr/local/etc/leadstack（macOS）
#   --no-service       只装二进制，不安装 / 不启动系统服务
#   --dry-run          只打印将要执行的动作，不改动系统
#   --uninstall        卸载：停服务 + 删单元 + 删二进制 + 删环境变量文件
#   -h, --help         显示帮助
#
# 环境变量：LEADSTACK_AGENT_BINARY / LEADSTACK_INSTALL_BASE_URL / LEADSTACK_VERSION
#
# 特性：幂等（可重复执行）、可卸载、失败信息明确、使用 sudo 前先检测（绝不静默 sudo）。
# =============================================================================
set -eu

BIN_NAME="leadstack-agent"
SERVICE_NAME="leadstack-agent"
REPO="maliang/leadstack"
DEFAULT_BASE_URL="https://github.com/${REPO}/releases/download"
DEFAULT_INSTALL_DIR="/usr/local/bin"
LAUNCHD_LABEL="com.leadstack.agent"

log()  { printf '[leadstack-install] %s\n' "$*"; }
warn() { printf '[leadstack-install] 警告：%s\n' "$*" >&2; }
die()  { printf '[leadstack-install] 错误：%s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 进度输出（下载十几 MB 时不能盯着空屏；非交互环境也照常打阶段行，只是关掉进度条）
# ---------------------------------------------------------------------------
TTY_OUT=0
if [ -t 1 ] || [ -t 2 ]; then TTY_OUT=1; fi
STEP_TOTAL=4
STEP_N=0
STEP_T0=0
STEP_ALL_T0=0

step() { # <标题>
	STEP_N=$((STEP_N + 1))
	STEP_T0=$(date +%s)
	printf '\n[leadstack-install] (%d/%d) %s\n' "$STEP_N" "$STEP_TOTAL" "$*"
}

step_took() {
	_now=$(date +%s)
	_took=$((_now - STEP_T0))
	[ "$_took" -ge 1 ] || _took="<1"
	printf '%s' "$_took"
}

step_ok() { printf '[leadstack-install]    ✓ %s（%ss）\n' "$*" "$(step_took)"; }
step_skip() { printf '[leadstack-install]    – 跳过：%s\n' "$*"; }

human_size() { # <字节数>
	_b=${1:-0}
	case "$_b" in
		''|*[!0-9]*) printf '未知大小'; return 0 ;;
	esac
	if [ "$_b" -ge 1048576 ]; then
		awk -v b="$_b" 'BEGIN { printf "%.1f MB", b / 1048576 }'
	elif [ "$_b" -ge 1024 ]; then
		awk -v b="$_b" 'BEGIN { printf "%.0f KB", b / 1024 }'
	else
		printf '%s B' "$_b"
	fi
}

usage() {
	cat <<'EOF'
LeadStack agent 安装脚本

用法：
  curl -fsSL <install.sh 地址> | sh -s -- --server <url> --token <token>
  sh install.sh [选项]

选项：
  --server URL       控制端地址（http/https/ws/wss），必填
  --token TOKEN      agent 接入令牌，必填
  --version V        版本号，默认 latest
  --agent-version V  --version 的别名（显式指定 agent 版本，便于回滚）
  --base-url URL     发布包下载前缀，默认 GitHub Releases
  --from-file PATH   使用本地二进制安装（离线 / 自测）
  --ca PATH          额外信任的 CA 证书（PEM），用于控制端自签 HTTPS
  --insecure         跳过 TLS 证书校验（自签且无法分发 CA 时使用，不推荐）
  --install-dir DIR  安装目录，默认 /usr/local/bin
  --env-dir DIR      环境变量目录，默认 /etc/leadstack 或 /usr/local/etc/leadstack
  --no-service       只安装二进制，不注册系统服务
  --dry-run          只打印动作，不改动系统
  --uninstall        卸载
  -h, --help         显示本帮助

环境变量：
  LEADSTACK_AGENT_BINARY     等价于 --from-file
  LEADSTACK_INSTALL_BASE_URL 等价于 --base-url
  LEADSTACK_VERSION          等价于 --version / --agent-version
EOF
}

# ---------------------------------------------------------------------------
# 参数解析（支持 "--opt value" 与 "--opt=value" 两种写法）
# ---------------------------------------------------------------------------
# 先把 --k=v 拆成 --k v，统一后续解析
if [ $# -gt 0 ]; then
	_count=$#
	while [ "$_count" -gt 0 ]; do
		_arg=$1
		shift
		case "$_arg" in
			--*=*) set -- "$@" "${_arg%%=*}" "${_arg#*=}" ;;
			*)     set -- "$@" "$_arg" ;;
		esac
		_count=$((_count - 1))
	done
	unset _count _arg
fi

SERVER="${LEADSTACK_AGENT_SERVER:-}"
TOKEN="${LEADSTACK_AGENT_TOKEN:-}"
VERSION="${LEADSTACK_VERSION:-${LEADSTACK_AGENT_VERSION:-latest}}"
BASE_URL="${LEADSTACK_INSTALL_BASE_URL:-$DEFAULT_BASE_URL}"
FROM_FILE="${LEADSTACK_AGENT_BINARY:-}"
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
ENV_DIR_OVERRIDE="${LEADSTACK_ENV_DIR:-}"
CA_FILE="${LEADSTACK_AGENT_CA:-}"
INSECURE=0
NO_SERVICE=0
DRY_RUN=0
UNINSTALL=0

# need_value <opt> <count-after-opt>
need_value() {
	[ "$2" -ge 1 ] || die "选项 $1 缺少参数值（用 --help 查看用法）"
}

while [ $# -gt 0 ]; do
	_opt=$1
	case "$_opt" in
		--server)     need_value "$_opt" $(($# - 1)); SERVER=$2; shift 2 ;;
		--token)      need_value "$_opt" $(($# - 1)); TOKEN=$2; shift 2 ;;
		--version)    need_value "$_opt" $(($# - 1)); VERSION=$2; shift 2 ;;
		--agent-version) need_value "$_opt" $(($# - 1)); VERSION=$2; shift 2 ;;
		--base-url)   need_value "$_opt" $(($# - 1)); BASE_URL=$2; shift 2 ;;
		--from-file)  need_value "$_opt" $(($# - 1)); FROM_FILE=$2; shift 2 ;;
		--ca)         need_value "$_opt" $(($# - 1)); CA_FILE=$2; shift 2 ;;
		--insecure)   INSECURE=1; shift ;;
		--install-dir) need_value "$_opt" $(($# - 1)); INSTALL_DIR=$2; shift 2 ;;
		--env-dir)    need_value "$_opt" $(($# - 1)); ENV_DIR_OVERRIDE=$2; shift 2 ;;
		--no-service) NO_SERVICE=1; shift ;;
		--dry-run)    DRY_RUN=1; shift ;;
		--uninstall)  UNINSTALL=1; shift ;;
		-h|--help)    usage; exit 0 ;;
		*)            die "未知选项：${_opt}（用 --help 查看用法）" ;;
	esac
done

# ---------------------------------------------------------------------------
# 平台检测
# ---------------------------------------------------------------------------
_os=$(uname -s 2>/dev/null || echo unknown)
_machine=$(uname -m 2>/dev/null || echo unknown)

case "$_os" in
	Linux)  OS=linux ;;
	Darwin) OS=darwin ;;
	*) die "不支持的操作系统：${_os}（仅支持 Linux 与 macOS；Windows 请手动下载 zip 包）" ;;
esac

case "$_machine" in
	x86_64|amd64)  ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) die "不支持的 CPU 架构：${_machine}（仅支持 amd64 / arm64）" ;;
esac

case "$OS" in
	linux)
		UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
		ENV_DIR="/etc/leadstack"
		;;
	darwin)
		UNIT_PATH="/Library/LaunchDaemons/${LAUNCHD_LABEL}.plist"
		ENV_DIR="/usr/local/etc/leadstack"
		;;
esac
if [ -n "$ENV_DIR_OVERRIDE" ]; then ENV_DIR="$ENV_DIR_OVERRIDE"; fi
ENV_FILE="${ENV_DIR}/agent.env"
BIN_PATH="${INSTALL_DIR}/${BIN_NAME}"

# ---------------------------------------------------------------------------
# 执行封装：dry-run 只打印；sudo 先检测，绝不静默提权
# ---------------------------------------------------------------------------
SUDO=""
SUDO_CHECKED=0

# 判断是否需要 root：root 直接写；否则若目标目录可写且不装服务，则不需要 sudo
# 附加的 TLS 参数：systemd 行内拼接，launchd 用独立 <string> 数组项。
AGENT_EXTRA=""
LAUNCHD_EXTRA=""
if [ -n "$CA_FILE" ]; then
	AGENT_EXTRA="${AGENT_EXTRA} --ca ${CA_FILE}"
	LAUNCHD_EXTRA="${LAUNCHD_EXTRA}
		<string>--ca</string>
		<string>${CA_FILE}</string>"
fi
if [ "$INSECURE" -eq 1 ]; then
	AGENT_EXTRA="${AGENT_EXTRA} --insecure"
	LAUNCHD_EXTRA="${LAUNCHD_EXTRA}
		<string>--insecure</string>"
fi

# 目录可能尚不存在（--install-dir /tmp/new-dir）：向上找最近一个存在的祖先来判断可写性，
# 否则 `-w` 对不存在的路径恒为 false，会被误判成「必须 sudo」，非交互安装直接失败。
writable_dir() {
	_wd_p=$1
	while [ ! -e "$_wd_p" ]; do
		_wd_prev=$_wd_p
		_wd_p=$(dirname "$_wd_p")
		[ "$_wd_p" = "$_wd_prev" ] && break
	done
	[ -w "$_wd_p" ]
}

detect_privilege() {
	if [ "$(id -u)" -eq 0 ]; then
		SUDO=""
		return 0
	fi
	if [ "$NO_SERVICE" -eq 1 ] && writable_dir "$INSTALL_DIR" && writable_dir "$ENV_DIR"; then
		SUDO=""
		return 0
	fi
	command -v sudo >/dev/null 2>&1 || die "需要 root 权限写入 ${INSTALL_DIR} / ${UNIT_PATH}，但系统没有 sudo。请以 root 运行：su -c 'sh install.sh ...'"
	SUDO="sudo"
}

# 首次真正需要提权时检测 sudo 可用性，避免中途卡在密码提示上
check_sudo() {
	if [ "$SUDO" != "sudo" ]; then return 0; fi
	if [ "$SUDO_CHECKED" -eq 1 ]; then return 0; fi
	if [ "$DRY_RUN" -eq 1 ]; then SUDO_CHECKED=1; return 0; fi
	if sudo -n true 2>/dev/null; then
		SUDO_CHECKED=1
		return 0
	fi
	if [ -t 0 ] && [ -t 1 ]; then
		log "需要输入 sudo 密码（仅用于安装到 ${INSTALL_DIR} 与注册服务）"
		sudo -v || die "sudo 认证失败"
		SUDO_CHECKED=1
		return 0
	fi
	die "sudo 需要密码，但当前是非交互会话（例如 curl | sh）。请先在终端执行 'sudo -v'，或改用：curl -fsSL <url> -o install.sh && sudo sh install.sh ..."
}

run() {
	if [ "$DRY_RUN" -eq 1 ]; then
		printf '  + %s\n' "$*"
		return 0
	fi
	"$@"
}

run_priv() {
	if [ "$SUDO" = "sudo" ]; then
		check_sudo
		if [ "$DRY_RUN" -eq 1 ]; then
			printf '  + sudo %s\n' "$*"
			return 0
		fi
		log "以 root 执行：sudo $*"
		sudo "$@"
		return 0
	fi
	run "$@"
}

# write_file <目标路径> <权限>，内容从 stdin 读入
write_file() {
	_wf_path=$1
	_wf_mode=$2
	if [ "$DRY_RUN" -eq 1 ]; then
		printf '  + 写入 %s（权限 %s），内容：\n' "$_wf_path" "$_wf_mode"
		sed 's/^/      | /'
		return 0
	fi
	_wf_tmp=$(mktemp "${TMPDIR:-/tmp}/leadstack-install.XXXXXX")
	cat > "$_wf_tmp"
	run_priv mkdir -p "$(dirname "$_wf_path")"
	run_priv install -m "$_wf_mode" "$_wf_tmp" "$_wf_path"
	rm -f "$_wf_tmp"
}

# ---------------------------------------------------------------------------
# 下载与校验
# ---------------------------------------------------------------------------
DOWNLOAD_TOOL=""
PROGRESS_ARG=""
detect_download_tool() {
	if command -v curl >/dev/null 2>&1; then
		DOWNLOAD_TOOL="curl"
	elif command -v wget >/dev/null 2>&1; then
		DOWNLOAD_TOOL="wget"
	else
		die "需要 curl 或 wget 之一来下载二进制；离线安装请用 --from-file <本地二进制路径>"
	fi
	detect_progress
}

# 只有交互终端才开进度条：进度写 stderr，所以「curl … | sh」也能看到。
detect_progress() {
	PROGRESS_ARG=""
	[ "$TTY_OUT" -eq 1 ] || return 0
	case "$DOWNLOAD_TOOL" in
		curl) PROGRESS_ARG="--progress-bar" ;;
		wget)
			if wget --help 2>&1 | grep -q -- '--show-progress'; then
				PROGRESS_ARG="--show-progress"
			fi
			;;
	esac
}

download() { # download <url> <目标文件>
	_dl_url=$1
	_dl_dest=$2
	case "$DOWNLOAD_TOOL" in
		curl)
			if [ -n "$PROGRESS_ARG" ]; then
				curl -fL "$PROGRESS_ARG" --retry 3 --connect-timeout 15 -o "$_dl_dest" "$_dl_url"
			else
				curl -fsSL --retry 3 --connect-timeout 15 -o "$_dl_dest" "$_dl_url"
			fi
			;;
		wget)
			if [ -n "$PROGRESS_ARG" ]; then
				wget -q "$PROGRESS_ARG" -T 15 -O "$_dl_dest" "$_dl_url"
			else
				wget -q -T 15 -O "$_dl_dest" "$_dl_url"
			fi
			;;
	esac
}

download_optional() { # 失败返回非 0，不中断脚本
	_dlo_url=$1
	_dlo_dest=$2
	case "$DOWNLOAD_TOOL" in
		curl) curl -fsSL --retry 1 --connect-timeout 10 -o "$_dlo_dest" "$_dlo_url" 2>/dev/null ;;
		wget) wget -q -T 10 -O "$_dlo_dest" "$_dlo_url" 2>/dev/null ;;
	esac
}

sha256_of() { # sha256_of <文件>
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		return 1
	fi
}

# 校验并规范化 --version / --agent-version。
# 版本号会拼进下载 URL，必须拒绝空白、路径分隔符与 ..；同时接受 vX.Y.Z 并去掉前导 v。
validate_agent_version() {
	if [ -z "$VERSION" ]; then
		die "--version/--agent-version 不能为空"
	fi
	if [ "$VERSION" = "latest" ]; then
		return 0
	fi
	case "$VERSION" in
		v[0-9]*) VERSION="${VERSION#v}" ;;
	esac
	case "$VERSION" in
		*[!A-Za-z0-9._+-]*) die "--version/--agent-version 含非法字符：${VERSION}（只允许字母、数字与 . _ + -）" ;;
		*..*)               die "--version/--agent-version 非法：${VERSION}（不允许 ..）" ;;
	esac
	case "$VERSION" in
		[0-9]*.[0-9]*.[0-9]*) : ;;
		*) die "--version/--agent-version 格式非法：${VERSION}（期望 latest 或 vX.Y.Z，例如 --agent-version v0.2.0）" ;;
	esac
	return 0
}

resolve_latest_version() {
	_api="https://api.github.com/repos/${REPO}/releases/latest"
	log "解析最新版本号：$_api"
	_body=$(download_tool_stdout "$_api") || die "无法访问 $_api 解析最新版本；请显式指定 --version <x.y.z>"
	_tag=$(printf '%s' "$_body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$_tag" ] || die "无法从 $_api 解析 tag_name；请显式指定 --version <x.y.z>"
	VERSION=${_tag#v}
	log "最新版本：${VERSION}"
}

download_tool_stdout() {
	case "$DOWNLOAD_TOOL" in
		curl) curl -fsSL --connect-timeout 15 "$1" ;;
		wget) wget -q -O - "$1" ;;
	esac
}

# ---------------------------------------------------------------------------
# 安装二进制
# ---------------------------------------------------------------------------
install_binary() {
	if [ -n "$FROM_FILE" ]; then
		[ -f "$FROM_FILE" ] || die "--from-file 指定的文件不存在：$FROM_FILE"
		log "使用本地二进制：$FROM_FILE"
		run_priv install -m 0755 "$FROM_FILE" "$BIN_PATH"
		return 0
	fi

	_version_label="$VERSION"
	if [ "$VERSION" = "latest" ]; then
		if [ "$DRY_RUN" -eq 1 ]; then
			log "[dry-run] 未指定 --version：将调用 GitHub API 解析最新 release tag 后再下载"
			_version_label="<最新 tag>"
		else
			resolve_latest_version
			_version_label="$VERSION"
		fi
	fi

	_archive="${BIN_NAME}_${_version_label}_${OS}_${ARCH}.tar.gz"
	_url="${BASE_URL}/v${_version_label}/${_archive}"

	if [ "$DRY_RUN" -eq 1 ]; then
		log "[dry-run] 下载 ${_url}"
		log "[dry-run] 校验 ${BASE_URL}/v${_version_label}/checksums.txt（存在时校验 sha256）"
		log "[dry-run] 解压 ${_archive} 并取其中的 ${BIN_NAME}"
		printf '  + install -m 0755 <解压出的 %s> %s\n' "$BIN_NAME" "$BIN_PATH"
		return 0
	fi

	_tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/leadstack-install.XXXXXX")
	# shellcheck disable=SC2064
	trap 'rm -rf "${_tmpdir:-}"' EXIT INT TERM

	log "下载：$_url"
	download "$_url" "${_tmpdir}/${_archive}" || die "下载 agent 二进制失败
  请求地址：$_url
  目标平台：${OS}/${ARCH}    版本：${VERSION}
  请依次确认：
    1) 控制端 / 发布地址是否可达（代理、镜像可用 --base-url <前缀> 指定）
    2) 该版本是否发布了 ${OS}/${ARCH} 产物（到 release 页面核对文件名与 checksums.txt）
    3) 版本号是否写错（--version / --agent-version 需为 latest 或 vX.Y.Z）
  离线安装：--from-file <本地 leadstack-agent 路径>（仓库根 make build 后即 bin/leadstack-agent）"

	_dl_bytes=$(wc -c < "${_tmpdir}/${_archive}" 2>/dev/null | tr -d '[:space:]')
	log "已下载 $(human_size "${_dl_bytes:-0}")"

	if download_optional "${BASE_URL}/v${VERSION}/checksums.txt" "${_tmpdir}/checksums.txt"; then
		_want=$(awk -v f="$_archive" '$2 == f || $2 == "*"f {print $1}' "${_tmpdir}/checksums.txt" | head -n 1)
		if [ -n "$_want" ]; then
			_got=$(sha256_of "${_tmpdir}/${_archive}") || _got=""
			if [ -z "$_got" ]; then
				warn "系统缺少 sha256sum/shasum/openssl，跳过校验"
			elif [ "$_want" != "$_got" ]; then
				die "校验和不匹配：期望 ${_want}，实际 ${_got}（文件可能损坏，请重试或联系维护者）"
			else
				SHA_VERIFIED=1
				log "sha256 校验通过：${_got}"
			fi
		else
			warn "checksums.txt 中没有 ${_archive} 的记录，跳过校验"
		fi
	else
		warn "未找到 checksums.txt，跳过 sha256 校验"
	fi

	tar -xzf "${_tmpdir}/${_archive}" -C "$_tmpdir" || die "解压失败：${_archive}（文件可能不完整）"
	_src="${_tmpdir}/${BIN_NAME}"
	if [ ! -f "$_src" ]; then
		_src=$(find "$_tmpdir" -type f -name "${BIN_NAME}*" ! -name "*.tar.gz" | head -n 1)
	fi
	[ -n "$_src" ] && [ -f "$_src" ] || die "压缩包内未找到 ${BIN_NAME} 可执行文件"

	run_priv install -m 0755 "$_src" "$BIN_PATH"
}

# ---------------------------------------------------------------------------
# 服务单元
# ---------------------------------------------------------------------------
emit_systemd_unit() {
	cat <<EOF
[Unit]
Description=LeadStack Agent（节点采集与上报）
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# 凭证放在 EnvironmentFile（权限 0600），由 systemd 在启动时展开到 ExecStart
EnvironmentFile=-${ENV_FILE}
ExecStart=${BIN_PATH} --server \${LEADSTACK_AGENT_SERVER} --token \${LEADSTACK_AGENT_TOKEN} --verbose${AGENT_EXTRA}
Restart=always
RestartSec=5
# agent 需要执行运维任务，保持 root；如需降权请自行调整 User=
KillMode=process
TimeoutStopSec=20
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
}

emit_launchd_plist() {
	cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>${LAUNCHD_LABEL}</string>
	<key>ProgramArguments</key>
	<array>
		<string>${BIN_PATH}</string>
		<string>--server</string>
		<string>${SERVER}</string>
		<string>--token</string>
		<string>${TOKEN}</string>
		<string>--verbose</string>${LAUNCHD_EXTRA}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>/var/log/${SERVICE_NAME}.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/${SERVICE_NAME}.err.log</string>
</dict>
</plist>
EOF
}

# 启动时读取的环境变量文件（systemd EnvironmentFile；macOS 侧作为备份凭证）
emit_env_file() {
	cat <<EOF
# LeadStack agent 环境变量（由 install.sh 生成，权限 0600）
LEADSTACK_AGENT_SERVER=${SERVER}
LEADSTACK_AGENT_TOKEN=${TOKEN}
EOF
}

install_service() {
	# 环境变量文件即使不注册服务也写（--no-service 场景下手动启动可直接 source）
	_env_writable=0
	if [ "$(id -u)" -eq 0 ] || [ "$SUDO" = "sudo" ] || writable_dir "$ENV_DIR"; then _env_writable=1; fi
	if [ "$_env_writable" -eq 1 ]; then
		emit_env_file | write_file "$ENV_FILE" 0600
	else
		warn "环境变量目录 ${ENV_DIR} 不可写，跳过凭证文件（本次以 --no-service 运行时可忽略）"
	fi

	SERVICE_STATE="registered"
	if [ "$NO_SERVICE" -eq 1 ]; then
		log "已指定 --no-service，跳过服务注册"
		SERVICE_STATE="env-only"
		return 0
	fi
	# dry-run 下即使本机没有 systemd 也把单元内容打印出来，便于人工核对
	if [ "$OS" = "linux" ] && [ "$DRY_RUN" -eq 0 ] && ! command -v systemctl >/dev/null 2>&1; then
		warn "未检测到 systemctl，跳过服务注册（二进制已安装，可手动运行 ${BIN_PATH}）"
		SERVICE_STATE="skipped"
		return 0
	fi

	# 已存在单元 → 重装时用 restart/reload，避免重复 enable（幂等）
	_service_existed=0
	if [ -f "$UNIT_PATH" ]; then _service_existed=1; fi

	case "$OS" in
		linux)
			emit_systemd_unit | write_file "$UNIT_PATH" 0644
			log "重载 systemd 配置"
			run_priv systemctl daemon-reload
			if [ "$_service_existed" -eq 1 ]; then
				log "服务已存在，重启以加载新版本"
				run_priv systemctl restart "${SERVICE_NAME}.service" || warn "systemctl restart 失败，请查看 systemctl status ${SERVICE_NAME}"
			else
				run_priv systemctl enable --now "${SERVICE_NAME}.service" || warn "systemctl enable --now 失败，请查看 systemctl status ${SERVICE_NAME}"
			fi
			;;
		darwin)
			# plist 内含 token，用 0600 避免同机其他用户读取（launchd 以 root 读取，无需 world-readable）
			emit_launchd_plist | write_file "$UNIT_PATH" 0600
			run_priv chown root:wheel "$UNIT_PATH"
			if [ "$_service_existed" -eq 1 ]; then
				log "服务已存在，重新加载"
				run_priv launchctl bootout "system/${LAUNCHD_LABEL}" 2>/dev/null || true
			fi
			if [ "$DRY_RUN" -eq 1 ]; then
				log "[dry-run] 若 bootstrap 失败则回退：launchctl load -w ${UNIT_PATH}"
			fi
			if ! run_priv launchctl bootstrap system "$UNIT_PATH" 2>/dev/null; then
				run_priv launchctl load -w "$UNIT_PATH" || warn "launchctl 加载失败，请检查 ${UNIT_PATH}"
			fi
			;;
	esac
}

uninstall_service() {
	_removed=0
	case "$OS" in
		linux)
			# 单元文件可能已被手工删除，但服务仍处于 enabled/loaded 状态——
			# 所以 disable/reset-failed 不看文件是否存在，一律 best-effort 执行。
			if command -v systemctl >/dev/null 2>&1; then
				run_priv systemctl disable --now "${SERVICE_NAME}.service" || warn "systemctl disable 未成功（服务可能本来就没运行），继续清理残留"
				run_priv systemctl reset-failed "${SERVICE_NAME}.service" 2>/dev/null || true
			fi
			# 单元残留 1：单元文件本体
			if [ -f "$UNIT_PATH" ]; then
				run_priv rm -f "$UNIT_PATH"
				_removed=1
			fi
			# 单元残留 2：enable 生成的 wants 符号链接（单元被手工删除时最容易遗留）
			for _want in /etc/systemd/system/*.wants/"${SERVICE_NAME}.service"; do
				if [ -L "$_want" ]; then
					run_priv rm -f "$_want"
					_removed=1
				fi
			done
			if command -v systemctl >/dev/null 2>&1 && [ "$_removed" -eq 1 ]; then
				run_priv systemctl daemon-reload || true
			fi
			;;
		darwin)
			# plist 可能已被手工删除但服务仍加载——bootout 按 label 执行，不依赖文件
			run_priv launchctl bootout "system/${LAUNCHD_LABEL}" 2>/dev/null || true
			if [ -f "$UNIT_PATH" ]; then
				run_priv launchctl unload -w "$UNIT_PATH" 2>/dev/null || true
				run_priv rm -f "$UNIT_PATH"
				_removed=1
			fi
			;;
	esac

	if [ -f "$BIN_PATH" ] || [ -L "$BIN_PATH" ]; then
		run_priv rm -f "$BIN_PATH"
		_removed=1
	fi
	if [ -f "$ENV_FILE" ]; then
		run_priv rm -f "$ENV_FILE"
		_removed=1
	fi
	# 目录为空时顺手删掉，非空则保留（幂等）
	run_priv rmdir "$ENV_DIR" 2>/dev/null || true

	if [ "$_removed" -eq 0 ]; then
		log "未检测到已安装的 LeadStack agent，无需卸载"
	else
		log "卸载完成：已停止并移除服务，删除二进制 ${BIN_PATH}、单元 ${UNIT_PATH}、凭证 ${ENV_FILE}（不存在的项自动跳过）"
	fi
}

do_uninstall() {
	log "开始卸载 LeadStack agent（os=${OS} arch=${ARCH}）"
	uninstall_service
}

do_install() {
	STEP_N=0
	STEP_TOTAL=4
	STEP_ALL_T0=$(date +%s)

	# ---- 1/4 参数与权限 ----
	step "检查参数、平台与权限"
	[ -n "$SERVER" ] || die "缺少 --server（控制端地址），例如：--server http://127.0.0.1:8080"
	[ -n "$TOKEN" ]  || die "缺少 --token（接入令牌，先在控制台生成接入令牌）"
	case "$SERVER" in
		http://*|https://*|ws://*|wss://*) : ;;
		*) die "--server 必须是 http(s):// 或 ws(s):// 开头的地址，当前为：$SERVER" ;;
	esac
	case "$TOKEN" in
		*[!A-Za-z0-9._~-]*) warn "--token 含非常规字符，请确认复制时没有带入空格或换行" ;;
	esac
	# TLS 选项：--ca 必须指向本机可读的 PEM；--insecure 只对外走 https/wss 时才有意义。
	if [ -n "$CA_FILE" ]; then
		[ -r "$CA_FILE" ] || die "--ca 指向的证书不可读：${CA_FILE}"
		case "$CA_FILE" in
			/*) : ;;
			*) die "--ca 必须是绝对路径（服务以 root 启动，相对路径没有意义）：${CA_FILE}" ;;
		esac
	fi
	if [ "$INSECURE" -eq 1 ]; then
		case "$SERVER" in
			https://*|wss://*) warn "已启用 --insecure：将跳过 TLS 证书校验，仅应在受控链路中使用" ;;
			*) warn "--insecure 只对 https/wss 生效，当前控制端地址是 ${SERVER}" ;;
		esac
	fi
	validate_agent_version

	log "安装 LeadStack agent：os=${OS} arch=${ARCH} version=${VERSION}"
	log "  安装目录：${INSTALL_DIR}"
	log "  控制端：${SERVER}"
	if [ "$NO_SERVICE" -eq 1 ]; then log "  服务注册：跳过（--no-service）"; fi
	if [ "$DRY_RUN" -eq 1 ]; then log "  模式：dry-run（只打印动作，不改动系统）"; fi

	detect_privilege
	if [ -z "$FROM_FILE" ]; then
		detect_download_tool
	fi
	if [ "$(id -u)" -eq 0 ]; then
		_priv="当前已是 root"
	elif [ "$SUDO" = "sudo" ]; then
		_priv="需要时用 sudo"
	else
		_priv="安装目录可写，无需 sudo"
	fi
	step_ok "环境就绪（${_priv}${PROGRESS_ARG:+，下载显示进度}）"

	# ---- 2/4 下载、校验并安装 ----
	if [ -n "$FROM_FILE" ]; then
		step "安装 agent 二进制（本地文件）"
		run_priv mkdir -p "$INSTALL_DIR"
		install_binary
		step_ok "已从 ${FROM_FILE} 安装到 ${BIN_PATH}"
	else
		step "下载、校验并安装 agent 二进制"
		run_priv mkdir -p "$INSTALL_DIR"
		install_binary
		if [ "$DRY_RUN" -eq 1 ]; then
			step_ok "dry-run：只打印下载与安装动作"
		elif [ "${SHA_VERIFIED:-0}" -eq 1 ]; then
			step_ok "已下载 $(human_size "${_dl_bytes:-0}")，sha256 校验通过，已安装 ${BIN_PATH}"
		else
			step_ok "已下载 $(human_size "${_dl_bytes:-0}") 并安装 ${BIN_PATH}（未做 sha256 校验）"
		fi
	fi

	# ---- 3/4 可执行性自检 ----
	step "确认二进制可执行"
	verify_binary
	if [ "$DRY_RUN" -eq 1 ]; then
		step_skip "dry-run 未落盘"
	else
		step_ok "${BIN_PATH} 可执行"
	fi

	# ---- 4/4 服务注册 ----
	step "写入凭证并注册服务"
	SERVICE_STATE=""
	install_service
	case "${SERVICE_STATE:-}" in
		registered)
			if [ "$DRY_RUN" -eq 1 ]; then
				step_ok "dry-run：只打印凭证文件与服务注册内容"
			elif [ "$OS" = "darwin" ]; then
				step_ok "已注册 launchd 任务 ${LAUNCHD_LABEL} 并启动"
			else
				step_ok "已注册 systemd 服务 ${SERVICE_NAME} 并启动"
			fi
			;;
		env-only) step_skip "指定了 --no-service：只写凭证文件，不注册服务" ;;
		skipped) step_skip "系统没有 systemctl，未注册服务（可手动运行 ${BIN_PATH}）" ;;
		*) step_ok "凭证文件已写入" ;;
	esac

	# ---- 汇总 ----
	_total=$(( $(date +%s) - STEP_ALL_T0 ))
	log "安装完成（总耗时 ${_total}s）。"
	log "  二进制：${BIN_PATH}"
	if [ "$NO_SERVICE" -eq 0 ]; then log "  服务单元：${UNIT_PATH}"; fi
	if [ "$NO_SERVICE" -eq 1 ]; then
		log "  手动启动：${BIN_PATH} --server ${SERVER} --token <token>${AGENT_EXTRA}"
	elif [ "$OS" = "linux" ]; then
		log "  查看状态：systemctl status ${SERVICE_NAME}"
		log "  查看日志：journalctl -u ${SERVICE_NAME} -f"
	else
		log "  查看状态：launchctl print system/${LAUNCHD_LABEL}"
		log "  查看日志：tail -f /var/log/${SERVICE_NAME}.log"
	fi
}

# 安装后确认二进制可执行（dry-run 跳过；不依赖二进制支持 --help，且加超时防挂住）
verify_binary() {
	if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
	[ -x "$BIN_PATH" ] || die "安装后 ${BIN_PATH} 不可执行，请检查安装目录权限"
	if command -v timeout >/dev/null 2>&1; then
		timeout 5 "$BIN_PATH" --help >/dev/null 2>&1 || true
	fi
	return 0
}

# ---------------------------------------------------------------------------
main() {
	if [ "$UNINSTALL" -eq 1 ]; then
		detect_privilege
		do_uninstall
		exit 0
	fi
	do_install
}

main
