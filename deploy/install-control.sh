#!/bin/sh
# =============================================================================
# LeadStack 控制端一键安装 / 卸载脚本
#
# 用法（管道安装，推荐）：
#   curl -fsSL https://raw.githubusercontent.com/maliang/leadstack/master/deploy/install-control.sh \
#     | sh -s -- --port 9000
#
# 本机自测（不改系统）：
#   sh deploy/install-control.sh --dry-run --port 9000
#   sh deploy/install-control.sh --install-dir /tmp/bin --env-dir /tmp/etc \
#     --data-dir /tmp/data --no-service --from-file bin/leadstack --port 9000
#
# 选项：
#   --port N           监听端口（默认 8080）；等价于设置 LEADSTACK_LISTEN 的端口部分
#   --bind ADDR        监听地址（默认 127.0.0.1，仅本机）；对外提供服务用 0.0.0.0
#   --listen ADDR:PORT 直接指定完整监听地址（优先级高于 --bind/--port）
#   --data-dir DIR     数据目录（默认 Linux /var/lib/leadstack/data，macOS /usr/local/var/leadstack/data）
#   --version V        安装版本，默认 latest（自动解析最新 release tag）
#   --base-url URL     发布包前缀，默认 https://github.com/maliang/leadstack/releases/download
#   --from-file PATH   直接安装本地二进制（离线场景 / 自测），跳过下载
#   --admin-password P 指定初始管理员密码（写入 0600 环境文件；留空则随机生成并打印到日志）
#   --public-url URL   目标机回连地址（反代/公网访问时必填）
#   --trusted-proxies  可信反向代理 CIDR 列表（反代后取真实客户端 IP）
#   --install-dir DIR  安装目录，默认 /usr/local/bin
#   --env-dir DIR      环境变量目录，默认 /etc/leadstack（macOS /usr/local/etc/leadstack）
#   --user NAME        运行服务的系统用户，默认 leadstack（仅 Linux systemd）
#   --no-service       只装二进制与环境文件，不注册 / 不启动系统服务
#   --dry-run          只打印将要执行的动作，不改动系统
#   --uninstall        卸载：停服务 + 删单元 + 删二进制 + 删环境文件（保留数据目录）
#   --purge            配合 --uninstall：连数据目录一起删（不可恢复，谨慎）
#   -h, --help         显示帮助
#
# 环境变量：LEADSTACK_VERSION / LEADSTACK_INSTALL_BASE_URL / LEADSTACK_CONTROL_BINARY
#           LEADSTACK_RELEASE_API（解析 latest 的地址，镜像/自测用）
#
# 特性：幂等（重复执行=升级，先停服务再换二进制）、可卸载、端口自定义、
#       下载后强制 sha256 校验、失败信息明确、使用 sudo 前先检测（绝不静默 sudo）。
# =============================================================================
set -eu

BIN_NAME="leadstack"
SERVICE_NAME="leadstack"
REPO="maliang/leadstack"
DEFAULT_BASE_URL="https://github.com/${REPO}/releases/download"
DEFAULT_RELEASE_API="https://api.github.com/repos/${REPO}/releases/latest"
DEFAULT_INSTALL_DIR="/usr/local/bin"
LAUNCHD_LABEL="com.leadstack.control"

log()  { printf '[leadstack-install] %s\n' "$*"; }
warn() { printf '[leadstack-install] 警告：%s\n' "$*" >&2; }
die()  { printf '[leadstack-install] 错误：%s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
LeadStack 控制端一键安装

用法：
  curl -fsSL <install-control.sh 地址> | sh -s -- --port 9000
  sh install-control.sh [选项]

常用：
  --port 9000                 装到 9000 端口（默认 8080）
  --bind 0.0.0.0              允许外部访问（默认 127.0.0.1，建议前面加 TLS 反代）
  --listen 0.0.0.0:9000       直接给完整监听地址
  --admin-password <pwd>      指定初始管理员密码（否则随机生成并打印到日志一次）

其他：
  --data-dir DIR      数据目录
  --version V         版本号，默认 latest
  --base-url URL      发布包下载前缀，默认 GitHub Releases
  --from-file PATH    使用本地二进制安装（离线 / 自测）
  --public-url URL    目标机回连地址（反代/公网访问时必填）
  --trusted-proxies  可信反代 CIDR，逗号分隔，例如 127.0.0.1/32,10.0.0.0/8
  --install-dir DIR   二进制目录，默认 /usr/local/bin
  --env-dir DIR       环境文件目录，默认 /etc/leadstack
  --user NAME         服务运行用户（Linux），默认 leadstack
  --no-service        只安装二进制，不注册系统服务
  --dry-run           只打印动作，不改动系统
  --uninstall         卸载（保留数据目录）
  --purge             卸载时连数据目录一起删
  -h, --help          显示本帮助

环境变量：
  LEADSTACK_CONTROL_BINARY   等价于 --from-file
  LEADSTACK_INSTALL_BASE_URL 等价于 --base-url
  LEADSTACK_VERSION          等价于 --version
  LEADSTACK_RELEASE_API      解析 latest 的地址（镜像 / 自测用）
EOF
}

# ---------------------------------------------------------------------------
# 参数解析（支持 "--opt value" 与 "--opt=value" 两种写法）
# ---------------------------------------------------------------------------
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

VERSION="${LEADSTACK_VERSION:-latest}"
BASE_URL="${LEADSTACK_INSTALL_BASE_URL:-$DEFAULT_BASE_URL}"
RELEASE_API="${LEADSTACK_RELEASE_API:-$DEFAULT_RELEASE_API}"
FROM_FILE="${LEADSTACK_CONTROL_BINARY:-}"
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
ENV_DIR_OVERRIDE=""
DATA_DIR=""
PORT="8080"
BIND="127.0.0.1"
LISTEN=""
ADMIN_PASSWORD="${LEADSTACK_ADMIN_PASSWORD:-}"
PUBLIC_URL="${LEADSTACK_PUBLIC_URL:-}"
TRUSTED_PROXIES="${LEADSTACK_TRUSTED_PROXIES:-}"
SERVICE_USER="leadstack"
NO_SERVICE=0
DRY_RUN=0
UNINSTALL=0
PURGE=0

need_value() {
	[ "$2" -ge 1 ] || die "选项 $1 缺少参数值（用 --help 查看用法）"
}

while [ $# -gt 0 ]; do
	_opt=$1
	case "$_opt" in
		--port)            need_value "$_opt" $(($# - 1)); PORT=$2; shift 2 ;;
		--bind)            need_value "$_opt" $(($# - 1)); BIND=$2; shift 2 ;;
		--listen)          need_value "$_opt" $(($# - 1)); LISTEN=$2; shift 2 ;;
		--data-dir)        need_value "$_opt" $(($# - 1)); DATA_DIR=$2; shift 2 ;;
		--version)         need_value "$_opt" $(($# - 1)); VERSION=$2; shift 2 ;;
		--base-url)        need_value "$_opt" $(($# - 1)); BASE_URL=$2; shift 2 ;;
		--from-file)       need_value "$_opt" $(($# - 1)); FROM_FILE=$2; shift 2 ;;
		--admin-password)  need_value "$_opt" $(($# - 1)); ADMIN_PASSWORD=$2; shift 2 ;;
		--public-url)      need_value "$_opt" $(($# - 1)); PUBLIC_URL=$2; shift 2 ;;
		--trusted-proxies) need_value "$_opt" $(($# - 1)); TRUSTED_PROXIES=$2; shift 2 ;;
		--install-dir)     need_value "$_opt" $(($# - 1)); INSTALL_DIR=$2; shift 2 ;;
		--env-dir)         need_value "$_opt" $(($# - 1)); ENV_DIR_OVERRIDE=$2; shift 2 ;;
		--user)            need_value "$_opt" $(($# - 1)); SERVICE_USER=$2; shift 2 ;;
		--no-service)      NO_SERVICE=1; shift ;;
		--dry-run)         DRY_RUN=1; shift ;;
		--uninstall)       UNINSTALL=1; shift ;;
		--purge)           PURGE=1; shift ;;
		-h|--help)         usage; exit 0 ;;
		*)                 die "未知选项：${_opt}（用 --help 查看用法）" ;;
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
	*) die "不支持的操作系统：${_os}（仅支持 Linux 与 macOS；Windows 请手动下载 zip 包后运行 leadstack.exe）" ;;
esac

case "$_machine" in
	x86_64|amd64)  ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) die "不支持的 CPU 架构：${_machine}（仅支持 amd64 / arm64）" ;;
esac

case "$OS" in
	linux)
		UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
		ENV_DIR_DEFAULT="/etc/leadstack"
		DATA_DIR_DEFAULT="/var/lib/leadstack/data"
		;;
	darwin)
		UNIT_PATH="/Library/LaunchDaemons/${LAUNCHD_LABEL}.plist"
		ENV_DIR_DEFAULT="/usr/local/etc/leadstack"
		DATA_DIR_DEFAULT="/usr/local/var/leadstack/data"
		;;
esac
ENV_DIR="$ENV_DIR_DEFAULT"
if [ -n "$ENV_DIR_OVERRIDE" ]; then ENV_DIR="$ENV_DIR_OVERRIDE"; fi
if [ -z "$DATA_DIR" ]; then DATA_DIR="$DATA_DIR_DEFAULT"; fi
ENV_FILE="${ENV_DIR}/leadstack.env"
BIN_PATH="${INSTALL_DIR}/${BIN_NAME}"

# ---------------------------------------------------------------------------
# 监听地址校验：--listen > --bind + --port
# ---------------------------------------------------------------------------
validate_listen() {
	if [ -n "$LISTEN" ]; then
		case "$LISTEN" in
			*:*:*) ;;                      # IPv6 字面量 [::1]:8080 或裸 IPv6
			*:*)   ;;
			*) die "--listen 需要 host:port 形式，例如 127.0.0.1:8080 或 [::]:8080，当前为：${LISTEN}" ;;
		esac
		return 0
	fi
	case "$PORT" in
		''|*[!0-9]*) die "--port 必须是数字（1-65535），当前为：${PORT}" ;;
	esac
	if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
		die "--port 超出范围（1-65535），当前为：${PORT}"
	fi
	case "$BIND" in
		''|*[[:space:]]*) die "--bind 不能为空或包含空白，当前为：${BIND}" ;;
		*:*:*) die "--bind 只接受地址，不要带端口（端口请用 --port）：${BIND}" ;;
		*:[0-9]*) die "--bind 只接受地址，不要带端口（端口请用 --port）：${BIND}" ;;
	esac
	LISTEN="${BIND}:${PORT}"
	return 0
}

# 监听地址里的端口（用于结尾提示与健康检查）
listen_port() {
	case "$1" in
		\[*\]:*) _lp=${1##*]:} ;;
		*:*)     _lp=${1##*:} ;;
		*)       _lp="" ;;
	esac
	printf '%s' "$_lp"
}

validate_version() {
	[ -n "$VERSION" ] || die "--version 不能为空"
	if [ "$VERSION" = "latest" ]; then return 0; fi
	case "$VERSION" in
		v[0-9]*) VERSION="${VERSION#v}" ;;
	esac
	case "$VERSION" in
		*[!A-Za-z0-9._+-]*) die "--version 含非法字符：${VERSION}（只允许字母、数字与 . _ + -）" ;;
		*..*)               die "--version 非法：${VERSION}（不允许 ..）" ;;
	esac
	case "$VERSION" in
		[0-9]*.[0-9]*.[0-9]*) : ;;
		*) die "--version 格式非法：${VERSION}（期望 latest 或 vX.Y.Z，例如 --version v0.1.5）" ;;
	esac
	return 0
}

validate_listen
validate_version

# ---------------------------------------------------------------------------
# 执行封装：dry-run 只打印；sudo 先检测，绝不静默提权
# ---------------------------------------------------------------------------
SUDO=""
SUDO_CHECKED=0

# writable_dir <目录>：目录可写，或目录尚不存在时向上找到最近的已存在目录并判断其可写
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
	# --no-service（只装二进制+环境文件）时不强制 root：两个目录都可写就当前用户装
	if [ "$NO_SERVICE" -eq 1 ] && writable_dir "$INSTALL_DIR" && writable_dir "$ENV_DIR"; then
		SUDO=""
		return 0
	fi
	# 没有服务单元要处理（未装服务 / 已手工删除）且目录可写：也不需要 root
	if [ ! -e "$UNIT_PATH" ] && writable_dir "$INSTALL_DIR" && writable_dir "$ENV_DIR"; then
		SUDO=""
		return 0
	fi
	command -v sudo >/dev/null 2>&1 || die "需要 root 权限写入 ${INSTALL_DIR} / ${UNIT_PATH}，但系统没有 sudo。请以 root 运行：su -c 'sh install-control.sh ...'"
	SUDO="sudo"
}

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
	die "sudo 需要密码，但当前是非交互会话（例如 curl | sh）。请先在终端执行 'sudo -v'，或改用：curl -fsSL <url> -o install-control.sh && sudo sh install-control.sh ..."
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

sha256_of() {
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

# ---------------------------------------------------------------------------
# 下载与校验
# ---------------------------------------------------------------------------
DOWNLOAD_TOOL=""
detect_download_tool() {
	if command -v curl >/dev/null 2>&1; then
		DOWNLOAD_TOOL="curl"
	elif command -v wget >/dev/null 2>&1; then
		DOWNLOAD_TOOL="wget"
	else
		die "需要 curl 或 wget 之一来下载二进制；离线安装请用 --from-file <本地 leadstack 路径>"
	fi
}

download() {
	_dl_url=$1
	_dl_dest=$2
	case "$DOWNLOAD_TOOL" in
		curl) curl -fsSL --retry 3 --connect-timeout 15 -o "$_dl_dest" "$_dl_url" ;;
		wget) wget -q -T 15 -O "$_dl_dest" "$_dl_url" ;;
	esac
}

download_optional() {
	_dlo_url=$1
	_dlo_dest=$2
	case "$DOWNLOAD_TOOL" in
		curl) curl -fsSL --retry 1 --connect-timeout 10 -o "$_dlo_dest" "$_dlo_url" 2>/dev/null ;;
		wget) wget -q -T 10 -O "$_dlo_dest" "$_dlo_url" 2>/dev/null ;;
	esac
}

download_stdout() {
	case "$DOWNLOAD_TOOL" in
		curl) curl -fsSL --connect-timeout 15 "$1" ;;
		wget) wget -q -O - "$1" ;;
	esac
}

resolve_latest_version() {
	log "解析最新版本号：${RELEASE_API}"
	_body=$(download_stdout "$RELEASE_API") || die "无法访问 ${RELEASE_API} 解析最新版本；请显式指定 --version <x.y.z>"
	_tag=$(printf '%s' "$_body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$_tag" ] || die "无法从 ${RELEASE_API} 解析 tag_name；请显式指定 --version <x.y.z>"
	VERSION=${_tag#v}
	log "最新版本：${VERSION}"
}

# ---------------------------------------------------------------------------
# 系统用户与目录（Linux systemd 单元以该用户运行）
# ---------------------------------------------------------------------------
ensure_user_and_dirs() {
	[ "$OS" = "linux" ] || return 0
	[ "$NO_SERVICE" -eq 1 ] && return 0
	_state_dir=$(dirname "$DATA_DIR")
	if [ "$DRY_RUN" -eq 1 ]; then
		log "[dry-run] 确保系统用户 ${SERVICE_USER} 存在（不存在则 useradd --system）"
		log "[dry-run] 建目录 ${_state_dir} 与 ${DATA_DIR}（属主 ${SERVICE_USER}:${SERVICE_USER}，权限 0750）"
		return 0
	fi
	if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
		log "创建系统用户 ${SERVICE_USER}"
		run_priv useradd --system --home "$_state_dir" --shell /usr/sbin/nologin "$SERVICE_USER" \
			|| die "创建系统用户 ${SERVICE_USER} 失败（可手工 useradd 后重试，或用 --user 指定已有用户）"
	fi
	run_priv mkdir -p "$_state_dir" "$DATA_DIR"
	run_priv chown -R "${SERVICE_USER}:${SERVICE_USER}" "$_state_dir"
	run_priv chmod 0750 "$_state_dir" "$DATA_DIR" || true
}

# ---------------------------------------------------------------------------
# 安装二进制（升级时先停服务，避免 ETXTBSY）
# ---------------------------------------------------------------------------
service_exists() {
	case "$OS" in
		linux) [ -f "$UNIT_PATH" ] ;;
		darwin) [ -f "$UNIT_PATH" ] ;;
	esac
}

stop_service_before_upgrade() {
	[ "$NO_SERVICE" -eq 1 ] && return 0
	[ "$DRY_RUN" -eq 1 ] && return 0
	[ "$OS" = "linux" ] || return 0
	command -v systemctl >/dev/null 2>&1 || return 0
	if service_exists; then
		log "已安装，先停服务再换二进制（升级）"
		run_priv systemctl stop "${SERVICE_NAME}.service" || warn "systemctl stop 失败，继续尝试替换二进制"
	fi
}

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
			log "[dry-run] 未指定 --version：将调用 ${RELEASE_API} 解析最新 release tag 后再下载"
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
	download "$_url" "${_tmpdir}/${_archive}" || die "下载控制端二进制失败
  请求地址：$_url
  目标平台：${OS}/${ARCH}    版本：${VERSION}
  请依次确认：
    1) 发布地址是否可达（代理、镜像可用 --base-url <前缀> 指定）
    2) 该版本是否发布了 ${OS}/${ARCH} 产物（到 release 页面核对文件名与 checksums.txt）
    3) 版本号是否写错（--version 需为 latest 或 vX.Y.Z）
  离线安装：--from-file <本地 leadstack 路径>（仓库根 make build 后即 bin/leadstack）"

	if download_optional "${BASE_URL}/v${VERSION}/checksums.txt" "${_tmpdir}/checksums.txt"; then
		_want=$(awk -v f="$_archive" '$2 == f || $2 == "*"f {print $1}' "${_tmpdir}/checksums.txt" | head -n 1)
		if [ -n "$_want" ]; then
			_got=$(sha256_of "${_tmpdir}/${_archive}") || _got=""
			if [ -z "$_got" ]; then
				warn "系统缺少 sha256sum/shasum/openssl，跳过校验"
			elif [ "$_want" != "$_got" ]; then
				die "校验和不匹配：期望 ${_want}，实际 ${_got}（文件可能损坏，请重试或联系维护者）"
			else
				log "sha256 校验通过"
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
		_src=$(find "$_tmpdir" -type f -name "${BIN_NAME}" ! -name "*.tar.gz" | head -n 1)
	fi
	[ -n "$_src" ] && [ -f "$_src" ] || die "压缩包内未找到 ${BIN_NAME} 可执行文件"

	run_priv install -m 0755 "$_src" "$BIN_PATH"
}

# ---------------------------------------------------------------------------
# 环境变量文件与服务单元
# ---------------------------------------------------------------------------
# env_quote：按 systemd EnvironmentFile 的语义给值加引号。
# 含空白/引号/反斜杠的值不加引号会被 systemd 当成非法行而静默忽略（服务起不来又看不出原因）。
env_quote() {
	case "$1" in
		*[![:print:]]*|*[[:space:]\"\'\\]*)
			_esc=$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
			printf '"%s"' "$_esc"
			;;
		*) printf '%s' "$1" ;;
	esac
}

emit_env_file() {
	cat <<EOF
# LeadStack 控制端环境变量（由 install-control.sh 生成，权限 0600）
# 端口改这里后需重启服务：systemctl restart ${SERVICE_NAME}（也可用 leadstack network --listen）
LEADSTACK_LISTEN=${LISTEN}
LEADSTACK_DATA_DIR=${DATA_DIR}
EOF
	if [ -n "$PUBLIC_URL" ]; then
		printf 'LEADSTACK_PUBLIC_URL=%s\n' "$(env_quote "$PUBLIC_URL")"
	fi
	if [ -n "$TRUSTED_PROXIES" ]; then
		printf 'LEADSTACK_TRUSTED_PROXIES=%s\n' "$(env_quote "$TRUSTED_PROXIES")"
	fi
	if [ -n "$ADMIN_PASSWORD" ]; then
		printf '# 初始管理员密码：仅在首次初始化（用户表为空）时生效，登录后请从本文件删除\n'
		printf 'LEADSTACK_ADMIN_PASSWORD=%s\n' "$(env_quote "$ADMIN_PASSWORD")"
	fi
}

emit_systemd_unit() {
	cat <<EOF
[Unit]
Description=LeadStack 控制端（API + Web UI + Agent WebSocket）
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${DATA_DIR%/*}
EnvironmentFile=-${ENV_FILE}
ExecStart=${BIN_PATH}

Restart=always
RestartSec=3
# 单进程 + SQLite 单写，停止时给足刷盘时间
TimeoutStopSec=20
KillMode=mixed
LimitNOFILE=65536
StateDirectory=leadstack
StateDirectoryMode=0750

# 加固：控制端只需要写数据目录
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
ReadWritePaths=/var/lib/leadstack
CapabilityBoundingSet=
AmbientCapabilities=

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
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>LEADSTACK_LISTEN</key>
		<string>${LISTEN}</string>
		<key>LEADSTACK_DATA_DIR</key>
		<string>${DATA_DIR}</string>$(_plist_extra)
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/var/log/${SERVICE_NAME}.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/${SERVICE_NAME}.err.log</string>
</dict>
</plist>
EOF
}

# launchd plist 的额外环境变量项
_plist_extra() {
	if [ -n "$PUBLIC_URL" ]; then
		printf '\n\t\t<key>LEADSTACK_PUBLIC_URL</key>\n\t\t<string>%s</string>' "$PUBLIC_URL"
	fi
	if [ -n "$TRUSTED_PROXIES" ]; then
		printf '\n\t\t<key>LEADSTACK_TRUSTED_PROXIES</key>\n\t\t<string>%s</string>' "$TRUSTED_PROXIES"
	fi
	if [ -n "$ADMIN_PASSWORD" ]; then
		printf '\n\t\t<key>LEADSTACK_ADMIN_PASSWORD</key>\n\t\t<string>%s</string>' "$ADMIN_PASSWORD"
	fi
}

install_service() {
	# 环境文件即使 --no-service 也写（手动启动可直接 source）
	_env_writable=0
	if [ "$(id -u)" -eq 0 ] || [ "$SUDO" = "sudo" ] || writable_dir "$ENV_DIR"; then _env_writable=1; fi
	if [ "$_env_writable" -eq 1 ]; then
		emit_env_file | write_file "$ENV_FILE" 0600
		if [ "$DRY_RUN" -eq 0 ] && [ "$OS" = "linux" ] && [ "$NO_SERVICE" -eq 0 ]; then
			run_priv chown root:"$SERVICE_USER" "$ENV_FILE" 2>/dev/null || true
			run_priv chmod 0640 "$ENV_FILE" || true
		fi
	else
		warn "环境变量目录 ${ENV_DIR} 不可写，跳过环境文件"
	fi

	if [ "$NO_SERVICE" -eq 1 ]; then
		log "已指定 --no-service，跳过服务注册"
		return 0
	fi
	if [ "$OS" = "linux" ] && [ "$DRY_RUN" -eq 0 ] && ! command -v systemctl >/dev/null 2>&1; then
		warn "未检测到 systemctl，跳过服务注册（二进制已安装，可手动运行 ${BIN_PATH}）"
		return 0
	fi

	_service_existed=0
	if service_exists; then _service_existed=1; fi

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
			emit_launchd_plist | write_file "$UNIT_PATH" 0600
			run_priv chown root:wheel "$UNIT_PATH"
			if [ "$_service_existed" -eq 1 ]; then
				log "服务已存在，重新加载"
				run_priv launchctl bootout "system/${LAUNCHD_LABEL}" 2>/dev/null || true
			fi
			if ! run_priv launchctl bootstrap system "$UNIT_PATH" 2>/dev/null; then
				run_priv launchctl load -w "$UNIT_PATH" || warn "launchctl 加载失败，请检查 ${UNIT_PATH}"
			fi
			;;
	esac
}

# 装完探活：只对本机可达的监听地址做（0.0.0.0 也按 127.0.0.1 探）
health_check() {
	[ "$NO_SERVICE" -eq 1 ] && return 0
	[ "$DRY_RUN" -eq 1 ] && return 0
	command -v curl >/dev/null 2>&1 || return 0
	_p=$(listen_port "$LISTEN")
	[ -n "$_p" ] || return 0
	# 只探回环：0.0.0.0 / :: 也一定在 127.0.0.1 上可达
	_host="127.0.0.1"
	_i=0
	while [ "$_i" -lt 20 ]; do
		if curl -fsS --max-time 2 "http://${_host}:${_p}/healthz" >/dev/null 2>&1; then
			log "健康检查通过：http://${_host}:${_p}/healthz"
			return 0
		fi
		_i=$((_i + 1))
		sleep 0.5
	done
	warn "健康检查未通过（http://${_host}:${_p}/healthz）；请查看服务日志：journalctl -u ${SERVICE_NAME} -n 50（macOS：tail -50 /var/log/${SERVICE_NAME}.log）"
}

# ---------------------------------------------------------------------------
# 卸载
# ---------------------------------------------------------------------------
uninstall_all() {
	log "开始卸载 LeadStack 控制端（os=${OS} arch=${ARCH}）"
	_removed=0
	case "$OS" in
		linux)
			if command -v systemctl >/dev/null 2>&1; then
				run_priv systemctl disable --now "${SERVICE_NAME}.service" || warn "systemctl disable 未成功（服务可能本来就没运行），继续清理残留"
				run_priv systemctl reset-failed "${SERVICE_NAME}.service" 2>/dev/null || true
			fi
			if [ -f "$UNIT_PATH" ]; then
				run_priv rm -f "$UNIT_PATH"
				_removed=1
			fi
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
	run_priv rmdir "$ENV_DIR" 2>/dev/null || true

	if [ "$PURGE" -eq 1 ]; then
		if [ -d "$DATA_DIR" ]; then
			run_priv rm -rf "$DATA_DIR"
			_removed=1
			log "已删除数据目录 ${DATA_DIR}（--purge）"
		fi
		if [ "$OS" = "linux" ]; then
			run_priv rmdir "$(dirname "$DATA_DIR")" 2>/dev/null || true
		fi
	elif [ -d "$DATA_DIR" ]; then
		log "数据目录已保留：${DATA_DIR}（含数据库与凭证主密钥；确认不再需要时手动删除或加 --purge）"
	fi

	if [ "$_removed" -eq 0 ]; then
		log "未检测到已安装的 LeadStack 控制端，无需卸载"
	else
		log "卸载完成：已停止并移除服务，删除二进制 ${BIN_PATH}、单元 ${UNIT_PATH}、环境文件 ${ENV_FILE}"
	fi
}

# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------
do_uninstall() {
	detect_privilege
	uninstall_all
}

do_install() {
	detect_privilege
	log "安装 LeadStack 控制端：os=${OS} arch=${ARCH} version=${VERSION}"
	log "  监听地址：${LISTEN}"
	log "  数据目录：${DATA_DIR}"
	log "  安装目录：${INSTALL_DIR}"
	if [ -n "$PUBLIC_URL" ]; then log "  对外地址：${PUBLIC_URL}"; fi
	if [ "$NO_SERVICE" -eq 1 ]; then log "  服务注册：跳过（--no-service）"; fi
	if [ "$DRY_RUN" -eq 1 ]; then log "  模式：dry-run（只打印动作，不改动系统）"; fi

	if [ -z "$FROM_FILE" ]; then
		detect_download_tool
	fi

	stop_service_before_upgrade
	ensure_user_and_dirs

	run_priv mkdir -p "$INSTALL_DIR"
	install_binary
	install_service
	health_check

	_p=$(listen_port "$LISTEN")
	log "安装完成。"
	log "  二进制：${BIN_PATH}"
	log "  环境文件：${ENV_FILE}"
	if [ "$NO_SERVICE" -eq 0 ]; then log "  服务单元：${UNIT_PATH}"; fi
	log "  访问地址：http://127.0.0.1:${_p}（监听 ${LISTEN}）"
	case "$LISTEN" in
		127.0.0.1:*|localhost:*|'[::1]':*)
			log "  提示：当前只监听本机。要让其他机器访问，用 --bind 0.0.0.0（并建议前置 TLS 反代）"
			;;
	esac
	if [ -n "$ADMIN_PASSWORD" ]; then
		log "  初始管理员：admin / 指定密码（已写入环境文件；登录后请改密并从文件删除）"
	else
		if [ "$OS" = "linux" ] && [ "$NO_SERVICE" -eq 0 ]; then
			log "  初始管理员密码：journalctl -u ${SERVICE_NAME} -n 80 | grep -A3 '初始密码'（仅首次启动打印一次）"
		elif [ "$OS" = "darwin" ] && [ "$NO_SERVICE" -eq 0 ]; then
			log "  初始管理员密码：grep -A3 '初始密码' /var/log/${SERVICE_NAME}.log（仅首次启动打印一次）"
		else
			log "  初始管理员密码：手动运行 ${BIN_PATH} 时会在首次启动打印一次"
		fi
	fi
	if [ "$NO_SERVICE" -eq 1 ]; then
		log "  手动启动：env LEADSTACK_LISTEN=${LISTEN} LEADSTACK_DATA_DIR=${DATA_DIR} ${BIN_PATH}"
	fi
	if command -v "${BIN_NAME}" >/dev/null 2>&1 || [ -x "$BIN_PATH" ]; then
		log "  下一步：登录后到「设置 → 网络与端口」核对端口与对外地址，再用「接入本机 / 添加服务器」纳管目标机"
	fi
}

if [ "$UNINSTALL" -eq 1 ]; then
	do_uninstall
else
	do_install
fi
