#!/usr/bin/env bash
# LeadStack 控制端一键安装脚本 端到端验收（Lead 所有）
#
# 覆盖 deploy/install-control.sh：端口参数、监听到位、下载+sha256 校验、
# latest 解析、幂等升级、卸载保留数据、systemd 单元渲染。
# 用法：make build && bash scripts/verify_install_control.sh
#
# 设计：不碰真实系统路径（--install-dir/--env-dir/--data-dir 全部指向临时目录），
# 不访问 GitHub（版本与下载地址都指向本地 mock 发布服务）。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SCRIPT="deploy/install-control.sh"
BASE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-install-verify.XXXXXX")"
MOCK_PORT="${MOCK_PORT:-18876}"
MOCK_BASE="http://127.0.0.1:${MOCK_PORT}"
APP_PORT="${APP_PORT:-18877}"
VER="0.1.9"
PASS=0
FAIL=0
MOCK_PID=""
APP_PID=""

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
skip() { printf '  \033[33m-\033[0m %s\n' "$1"; }

cleanup() {
	[ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null
	[ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null
	# 兜底：清理任何指向本次临时目录的 leadstack 进程（kill 子 shell 不会杀掉其子进程）
	pkill -f "$BASE_DIR" 2>/dev/null
	wait 2>/dev/null
	rm -rf "$BASE_DIR"
}
trap cleanup EXIT

INSTALL_DIR="$BASE_DIR/bin"
ENV_DIR="$BASE_DIR/etc"
DATA_DIR="$BASE_DIR/data"

run_install() { # 透传参数调用安装脚本
	sh "$SCRIPT" --install-dir "$INSTALL_DIR" --env-dir "$ENV_DIR" --data-dir "$DATA_DIR" "$@"
}

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
	arm64|aarch64) ARCH=arm64 ;;
	*) ARCH=amd64 ;;
esac

printf '临时目录：%s\n' "$BASE_DIR"

# ---------------------------------------------------------------- 1. 前置
step "1. 前置检查"
[ -x bin/leadstack ] && pass "bin/leadstack 存在" || { fail "缺少 bin/leadstack，请先 make build"; exit 1; }
sh -n "$SCRIPT" && pass "install-control.sh 语法正确" || fail "install-control.sh 语法错误"
sh "$SCRIPT" --help >/dev/null 2>&1 && pass "--help 可正常输出" || fail "--help 退出码非 0"

# ---------------------------------------------------------------- 2. 端口参数
step "2. 端口参数（--port / --bind / --listen）"
DRY="$(run_install --dry-run --port 9001 --from-file bin/leadstack 2>&1)"
printf '%s' "$DRY" | grep -q "LEADSTACK_LISTEN=127.0.0.1:9001" \
	&& pass "--port 9001 → LEADSTACK_LISTEN=127.0.0.1:9001" || fail "--port 未写进环境文件：$(printf '%s' "$DRY" | grep LISTEN | head -2)"

DRY_BIND="$(run_install --dry-run --port 9002 --bind 0.0.0.0 --from-file bin/leadstack 2>&1)"
printf '%s' "$DRY_BIND" | grep -q "LEADSTACK_LISTEN=0.0.0.0:9002" \
	&& pass "--bind 0.0.0.0 --port 9002 → 0.0.0.0:9002" || fail "--bind 未生效"

DRY_LISTEN="$(run_install --dry-run --listen 0.0.0.0:9003 --port 9009 --from-file bin/leadstack 2>&1)"
printf '%s' "$DRY_LISTEN" | grep -q "LEADSTACK_LISTEN=0.0.0.0:9003" \
	&& pass "--listen 优先于 --port（9003 生效）" || fail "--listen 未能覆盖 --port"

BAD="$(run_install --dry-run --port 70000 --from-file bin/leadstack 2>&1)"; BAD_RC=$?
printf '%s' "$BAD" | grep -q "超出范围" && [ "$BAD_RC" -ne 0 ] \
	&& pass "--port 70000 被拒绝（端口范围校验）" || fail "--port 70000 未被拒绝（rc=${BAD_RC}）"

BAD2="$(run_install --dry-run --port abc --from-file bin/leadstack 2>&1)"; BAD2_RC=$?
printf '%s' "$BAD2" | grep -q "必须是数字" && [ "$BAD2_RC" -ne 0 ] \
	&& pass "--port abc 被拒绝（非数字校验）" || fail "--port abc 未被拒绝"

BAD3="$(run_install --dry-run --bind 1.2.3.4:80 --from-file bin/leadstack 2>&1)"; BAD3_RC=$?
printf '%s' "$BAD3" | grep -q "不要带端口" && [ "$BAD3_RC" -ne 0 ] \
	&& pass "--bind 带端口被拒绝并提示用 --port" || fail "--bind 带端口未被拒绝"

BAD4="$(run_install --dry-run --port 9004 --version 0.1 --from-file bin/leadstack 2>&1)"; BAD4_RC=$?
printf '%s' "$BAD4" | grep -q "格式非法" && [ "$BAD4_RC" -ne 0 ] \
	&& pass "--version 0.1 被拒绝（版本格式校验）" || fail "--version 0.1 未被拒绝"

# 含空白/特殊字符的值必须加引号，否则 systemd EnvironmentFile 会当非法行忽略
Q1="$(run_install --dry-run --port 9005 --admin-password 'p@ss w0rd!#$' --from-file bin/leadstack 2>&1)"
printf '%s' "$Q1" | grep -q 'LEADSTACK_ADMIN_PASSWORD="p@ss w0rd!#\$"' \
	&& pass "含空白的密码被引号包裹（systemd 才能正确解析）" || fail "特殊字符密码未加引号：$(printf '%s' "$Q1" | grep ADMIN_PASSWORD | head -1)"
Q2="$(run_install --dry-run --port 9006 --admin-password 'simple-pass' --from-file bin/leadstack 2>&1)"
printf '%s' "$Q2" | grep -q ' LEADSTACK_ADMIN_PASSWORD=simple-pass$' \
	&& pass "普通密码保持原样（多余引号也会让用户困惑）" || fail "普通密码被多余加引号"

# ---------------------------------------------------------------- 3. 本地安装
step "3. 本地安装（--no-service + --from-file）"
run_install --no-service --from-file bin/leadstack --port 9100 > "$BASE_DIR/install.log" 2>&1
INSTALL_RC=$?
[ "$INSTALL_RC" -eq 0 ] && pass "安装脚本退出码 0" || { fail "安装失败：$(tail -3 "$BASE_DIR/install.log")"; }
[ -x "$INSTALL_DIR/leadstack" ] && pass "二进制已安装到 ${INSTALL_DIR}/leadstack" || fail "二进制未安装"
# 权限：macOS 的 ls 会带 @ 扩展属性标记，统一用 stat 取八进制
if stat -f '%Lp' / >/dev/null 2>&1; then
	ENV_MODE="$(stat -f '%Lp' "$ENV_DIR/leadstack.env" 2>/dev/null)"
else
	ENV_MODE="$(stat -c '%a' "$ENV_DIR/leadstack.env" 2>/dev/null)"
fi
[ "$ENV_MODE" = "600" ] && pass "环境文件权限 0600" || fail "环境文件权限异常：${ENV_MODE:-缺失}"
grep -q "^LEADSTACK_LISTEN=127.0.0.1:9100$" "$ENV_DIR/leadstack.env" 2>/dev/null \
	&& pass "环境文件写入监听地址 127.0.0.1:9100" || fail "环境文件内容异常"
"$INSTALL_DIR/leadstack" --version >/dev/null 2>&1 && pass "安装出的二进制可执行" || fail "安装出的二进制无法运行"

# ---------------------------------------------------------------- 4. 按端口真实启动
step "4. 按环境文件启动，监听端口生效"
# 直接从环境文件加载（不能放进子 shell：kill 子 shell 不会杀掉 leadstack 子进程，
# 会留下占端口的僵尸，导致后续断言假通过）
set -a
# shellcheck disable=SC1090
. "$ENV_DIR/leadstack.env"
set +a
LEADSTACK_ADMIN_PASSWORD=verify-install-pass "$INSTALL_DIR/leadstack" > "$BASE_DIR/app.log" 2>&1 &
APP_PID=$!
HEALTH=""
for _ in $(seq 1 40); do
	HEALTH="$(curl -sS --max-time 2 "http://127.0.0.1:9100/healthz" 2>/dev/null)"
	[ -n "$HEALTH" ] && break
	sleep 0.3
done
printf '%s' "$HEALTH" | grep -q '"status":"ok"' \
	&& pass "健康检查通过：http://127.0.0.1:9100/healthz" || fail "端口 9100 未按预期监听（app.log 末尾：$(tail -2 "$BASE_DIR/app.log" 2>/dev/null | tr '\n' ' '))"
grep -q "active_listen=127.0.0.1:9100" "$BASE_DIR/app.log" 2>/dev/null \
	&& pass "启动日志 active_listen=127.0.0.1:9100" || fail "启动日志未显示期望的监听地址"
LOGIN_CODE="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:9100/api/v1/auth/login" \
	-H 'Content-Type: application/json' -d '{"username":"admin","password":"verify-install-pass"}' 2>/dev/null)"
[ "$LOGIN_CODE" = "200" ] && pass "指定密码可正常登录（200）" || fail "登录失败（HTTP ${LOGIN_CODE}）"
kill "$APP_PID" 2>/dev/null; wait "$APP_PID" 2>/dev/null; APP_PID=""
unset LEADSTACK_LISTEN LEADSTACK_DATA_DIR

# ---------------------------------------------------------------- 5. 幂等重装
step "5. 幂等重装（升级路径）"
run_install --no-service --from-file bin/leadstack --port 9101 > "$BASE_DIR/reinstall.log" 2>&1
[ $? -eq 0 ] && pass "重复执行安装成功（幂等）" || fail "重复安装失败：$(tail -3 "$BASE_DIR/reinstall.log")"
grep -q "^LEADSTACK_LISTEN=127.0.0.1:9101$" "$ENV_DIR/leadstack.env" \
	&& pass "重装后端口按新参数更新为 9101" || fail "重装后端口未更新"
grep -q "^LEADSTACK_LISTEN=127.0.0.1:9100$" "$ENV_DIR/leadstack.env" \
	&& fail "重装后仍残留旧端口 9100" || pass "旧端口配置已被覆盖"

# ---------------------------------------------------------------- 6. 下载 + sha256
step "6. 真实下载路径（本地 mock 发布服务 + sha256 校验）"
MOCK_DIR="$BASE_DIR/release"
mkdir -p "$MOCK_DIR/v${VER}"
cp bin/leadstack "$BASE_DIR/leadstack-copy"
tar -czf "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz" -C "$BASE_DIR" leadstack-copy
# 包内文件名必须是 leadstack（脚本按名字查找）
rm -rf "$BASE_DIR/unpack" && mkdir -p "$BASE_DIR/unpack"
tar -xzf "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz" -C "$BASE_DIR/unpack"
mv "$BASE_DIR/unpack/leadstack-copy" "$BASE_DIR/unpack/leadstack"
tar -czf "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz" -C "$BASE_DIR/unpack" leadstack
if command -v sha256sum >/dev/null 2>&1; then
	SUM="$(sha256sum "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz" | awk '{print $1}')"
else
	SUM="$(shasum -a 256 "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz" | awk '{print $1}')"
fi
printf '%s  %s\n' "$SUM" "leadstack_${VER}_${OS}_${ARCH}.tar.gz" > "$MOCK_DIR/v${VER}/checksums.txt"

python3 -m http.server "$MOCK_PORT" --bind 127.0.0.1 --directory "$MOCK_DIR" > "$BASE_DIR/mock.log" 2>&1 &
MOCK_PID=$!
disown "$MOCK_PID" 2>/dev/null # 退出时由 cleanup 显式 kill，避免 shell 打印 "Terminated"
for _ in $(seq 1 40); do
	curl -sS --max-time 1 -o /dev/null "$MOCK_BASE/v${VER}/checksums.txt" 2>/dev/null && break
	sleep 0.2
done

DL_LOG="$BASE_DIR/download.log"
run_install --no-service --version "$VER" --base-url "$MOCK_BASE" --port 9102 > "$DL_LOG" 2>&1
DL_RC=$?
[ "$DL_RC" -eq 0 ] && pass "从发布服务下载并安装成功（v${VER}）" || { fail "下载安装失败：$(tail -3 "$DL_LOG")"; }
grep -q "sha256 校验通过" "$DL_LOG" && pass "校验和比对通过" || fail "未执行 sha256 校验"
grep -q "^LEADSTACK_LISTEN=127.0.0.1:9102$" "$ENV_DIR/leadstack.env" \
	&& pass "下载安装同样应用 --port 9102" || fail "下载安装未应用端口参数"

# 篡改包 → 必须中止且不覆盖已装二进制
printf 'corrupted' >> "$MOCK_DIR/v${VER}/leadstack_${VER}_${OS}_${ARCH}.tar.gz"
BEFORE_SUM="$(shasum -a 256 "$INSTALL_DIR/leadstack" 2>/dev/null | awk '{print $1}')"
BAD_LOG="$BASE_DIR/badsum.log"
run_install --no-service --version "$VER" --base-url "$MOCK_BASE" --port 9102 > "$BAD_LOG" 2>&1
BAD_RC=$?
AFTER_SUM="$(shasum -a 256 "$INSTALL_DIR/leadstack" 2>/dev/null | awk '{print $1}')"
grep -q "校验和不匹配" "$BAD_LOG" && [ "$BAD_RC" -ne 0 ] \
	&& pass "包被篡改 → 校验和不匹配并中止" || fail "篡改包未被拦截（rc=${BAD_RC}）"
[ "$BEFORE_SUM" = "$AFTER_SUM" ] && pass "校验失败时未覆盖已安装二进制" || fail "校验失败仍覆盖了二进制"

# ---------------------------------------------------------------- 7. latest 解析
step "7. latest 版本解析（LEADSTACK_RELEASE_API 指向 mock）"
mkdir -p "$MOCK_DIR/api"
cat > "$MOCK_DIR/api/latest.json" <<JSON
{"tag_name":"v${VER}","name":"v${VER}"}
JSON
LATEST_LOG="$BASE_DIR/latest.log"
LEADSTACK_RELEASE_API="$MOCK_BASE/api/latest.json" \
	run_install --no-service --base-url "$MOCK_BASE" --port 9103 > "$LATEST_LOG" 2>&1
grep -q "最新版本：${VER}" "$LATEST_LOG" && pass "从 release API 解析到 v${VER}" \
	|| fail "latest 解析失败：$(tail -3 "$LATEST_LOG")"

# ---------------------------------------------------------------- 8. 卸载
step "8. 卸载（默认保留数据，--purge 才删）"
mkdir -p "$DATA_DIR"
printf 'keep-me' > "$DATA_DIR/marker.txt"
run_install --uninstall > "$BASE_DIR/uninstall.log" 2>&1
UN_RC=$?
[ "$UN_RC" -eq 0 ] && pass "卸载退出码 0" || fail "卸载失败：$(tail -3 "$BASE_DIR/uninstall.log")"
[ ! -e "$INSTALL_DIR/leadstack" ] && pass "二进制已删除" || fail "二进制仍存在"
[ ! -e "$ENV_DIR/leadstack.env" ] && pass "环境文件已删除" || fail "环境文件仍存在"
[ -f "$DATA_DIR/marker.txt" ] && pass "数据目录被保留（含 marker.txt）" || fail "数据目录被误删"

run_install --uninstall --purge > "$BASE_DIR/purge.log" 2>&1
[ ! -e "$DATA_DIR" ] && pass "--purge 会一并删除数据目录" || fail "--purge 未删除数据目录"

# ---------------------------------------------------------------- 9. systemd 单元渲染
step "9. Linux systemd 单元渲染（有 docker 时验证）"
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	UNIT_OUT="$(docker run --rm -v "$ROOT":/src:ro -w /src debian:stable-slim \
		sh -c "sh deploy/install-control.sh --dry-run --port 9200 --from-file /src/bin/leadstack" 2>&1)"
	printf '%s' "$UNIT_OUT" | grep -q "LEADSTACK_LISTEN=127.0.0.1:9200" \
		&& pass "容器内（Linux）环境文件端口正确" || fail "容器内端口渲染异常"
	printf '%s' "$UNIT_OUT" | grep -q "User=leadstack" \
		&& pass "systemd 单元含 User=leadstack（非 root 运行）" || fail "单元缺少 User="
	printf '%s' "$UNIT_OUT" | grep -q "EnvironmentFile=-/etc/leadstack/leadstack.env" \
		&& pass "单元引用环境文件（端口来自这里）" || fail "单元缺少 EnvironmentFile"
	printf '%s' "$UNIT_OUT" | grep -q "ExecStart=/usr/local/bin/leadstack" \
		&& pass "单元 ExecStart 指向安装路径" || fail "单元 ExecStart 异常"
	printf '%s' "$UNIT_OUT" | grep -q "ProtectSystem=strict" \
		&& pass "单元保留加固项（ProtectSystem=strict）" || fail "单元加固项缺失"
else
	skip "本机没有可用的 docker，跳过 Linux 单元渲染断言"
fi

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m控制端一键安装 验收通过\033[0m\n'; exit 0; fi
printf '\033[31m控制端一键安装 验收未通过\033[0m\n'
exit 1
