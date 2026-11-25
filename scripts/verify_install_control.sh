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
	rm -rf "$ROOT/bin/verify-linux"
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
# 必须连 ADMIN_PASSWORD 一起清掉：第 3 节安装时脚本已自动生成密码并写进环境文件，
# set -a 加载后它成了导出的环境变量，会让后面「没指定密码就生成」的用例全部走成
# 「用户已指定密码」分支（表现为汇总不打印密码、升级时又把它写进新环境文件）。
unset LEADSTACK_LISTEN LEADSTACK_DATA_DIR LEADSTACK_ADMIN_PASSWORD

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
	printf '%s' "$UNIT_OUT" | grep -q "ExecStart=/opt/leadstack/leadstack" \
		&& pass "单元 ExecStart 指向安装路径" || fail "单元 ExecStart 异常"
	printf '%s' "$UNIT_OUT" | grep -q "ProtectSystem=strict" \
		&& pass "单元保留加固项（ProtectSystem=strict）" || fail "单元加固项缺失"
else
	skip "本机没有可用的 docker，跳过 Linux 单元渲染断言"
fi

# ---------------------------------------------------------------- 10. 流程可见性
step "10. 流程可见性（下载进度与阶段输出）"
# 用户反馈过「没有流程输出」：十几 MB 下载期间盯着空屏无法判断是否卡住。
DL_STEPS=$(grep -cE '\([1-7]/7\)' "$DL_LOG" 2>/dev/null || true)
[ "${DL_STEPS:-0}" -eq 7 ] && pass "真实安装输出 7 个阶段标题（1/7 … 7/7）" \
	|| fail "阶段标题数量异常：${DL_STEPS:-0}"
grep -q "已下载 .*B" "$DL_LOG" && pass "下载完成打印文件大小" || fail "未打印下载大小"
grep -q "sha256 校验通过" "$DL_LOG" && pass "校验阶段有明确结论" || fail "校验阶段无结论"
grep -qE "已安装 .*/(leadstack|.+/leadstack)$|已安装 " "$DL_LOG" && pass "安装阶段有明确结论" || fail "安装阶段无结论"
grep -q "安装完成（总耗时" "$DL_LOG" && pass "结束打印总耗时" || fail "未打印总耗时"
# 非交互（无 tty）时不应残留 curl 进度条（否则日志会被 \r 刷爆）
if grep -qE '[0-9]+\.[0-9]%' "$DL_LOG"; then
	fail "非 tty 下仍输出了进度条百分比"
else
	pass "非 tty 下不输出进度条（只输出阶段与结论）"
fi
# 每个阶段都要有结论行（✓ 或 跳过），不能只打标题就没了
DL_CONCLUSIONS=$(grep -cE '✓|– 跳过' "$DL_LOG" 2>/dev/null || true)
[ "${DL_CONCLUSIONS:-0}" -ge 7 ] && pass "每个阶段都有 ✓/跳过 结论行" \
	|| fail "结论行偏少：${DL_CONCLUSIONS:-0}"
DRY_OUT="$BASE_DIR/progress-dry.log"
run_install --dry-run --version "$VER" --base-url "$MOCK_BASE" --port 9103 > "$DRY_OUT" 2>&1
grep -q "(1/7) 检查权限" "$DRY_OUT" && grep -q "(7/7) 接入本机" "$DRY_OUT" \
	&& pass "dry-run 同样给出完整阶段序列" || fail "dry-run 阶段序列缺失"
# 干跑不能撒谎：没改系统就不能说「已安装 / 已注册」
if grep -qE "✓ 已安装|✓ 已注册" "$DRY_OUT"; then
	fail "dry-run 出现了「已安装/已注册」的既成结论"
else
	pass "dry-run 的结论措辞与「未改动系统」一致"
fi

# ---------------------------------------------------------------- 11. 同目录本地来源
step "11. 同目录本地来源（脚本与二进制/归档在同一目录时跳过下载）"
# release 包解压后 install-control.sh 与 leadstack 就在同一目录，用户断网也要能装。
# 把「脚本 + 二进制」复制到独立临时目录：脚本按 $0 的 dirname 探测同目录。
# --base-url 指向 127.0.0.1:1（必然连不上）——装成功即证明全程零联网。
LOCAL_BASE="$BASE_DIR/local"
LOCAL_BIN_DIR="$LOCAL_BASE/bin-dir"
LOCAL_ARC_DIR="$LOCAL_BASE/arc-dir"
mkdir -p "$LOCAL_BIN_DIR" "$LOCAL_ARC_DIR"
cp "$SCRIPT" "$LOCAL_BIN_DIR/install-control.sh"
cp "$SCRIPT" "$LOCAL_ARC_DIR/install-control.sh"
cp bin/leadstack "$LOCAL_BIN_DIR/leadstack"
chmod +x "$LOCAL_BIN_DIR/leadstack"
SRC_SUM="$(shasum -a 256 bin/leadstack | awk '{print $1}')"

LI_INSTALL="$BASE_DIR/li/bin"
LI_ENV="$BASE_DIR/li/etc"
LI_DATA="$BASE_DIR/li/data"
run_local() { # run_local <含 install-control.sh 的目录> [参数...]
	local _dir="$1"; shift
	sh "$_dir/install-control.sh" --install-dir "$LI_INSTALL" --env-dir "$LI_ENV" --data-dir "$LI_DATA" "$@"
}
sum_of() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'; }

DIRBIN_LOG="$BASE_DIR/local-dirbin.log"
run_local "$LOCAL_BIN_DIR" --no-service --port 19513 --base-url http://127.0.0.1:1 > "$DIRBIN_LOG" 2>&1
DIRBIN_RC=$?
[ "$DIRBIN_RC" -eq 0 ] && pass "同目录二进制安装成功（--base-url 不可达 → 零联网）" \
	|| fail "同目录安装失败（rc=${DIRBIN_RC}）：$(tail -3 "$DIRBIN_LOG" | tr '\n' ' ')"
grep -q "检测到同目录的 leadstack" "$DIRBIN_LOG" && grep -q "跳过下载" "$DIRBIN_LOG" \
	&& pass "输出声明「检测到同目录…跳过下载」" \
	|| fail "未声明同目录来源：$(grep -m1 '二进制来源' "$DIRBIN_LOG")"
grep -q "(2/7) 获取控制端二进制（本地文件）" "$DIRBIN_LOG" \
	&& pass "阶段 2 标题按来源显示「本地文件」" || fail "阶段 2 标题未按来源区分"
grep -q "使用同目录二进制（跳过下载）" "$DIRBIN_LOG" \
	&& pass "阶段 2 结论为「使用同目录二进制」" || fail "阶段 2 结论措辞不符"
[ "$(sum_of "$LI_INSTALL/leadstack")" = "$SRC_SUM" ] \
	&& pass "装出的二进制与同目录源 sha256 逐字相同" \
	|| fail "装出的二进制与源不一致：$(sum_of "$LI_INSTALL/leadstack") vs ${SRC_SUM}"
"$LI_INSTALL/leadstack" --version >/dev/null 2>&1 && pass "同目录装出的二进制可执行" || fail "同目录装出的二进制无法运行"

# --from-file 优先级最高：同目录明明有 leadstack，也必须装 --from-file 指定的那一份
FROM_SRC="$BASE_DIR/from-file-leadstack"
if [ -f bin/leadstack-agent ]; then cp bin/leadstack-agent "$FROM_SRC"; else cp bin/leadstack "$FROM_SRC"; fi
# 兜底：万一两份字节相同就追加标记，保证能分辨「装的是哪一份」
if [ "$(sum_of "$FROM_SRC")" = "$SRC_SUM" ]; then printf 'leadstack-verify-from-file-marker' >> "$FROM_SRC"; fi
FROM_SUM="$(sum_of "$FROM_SRC")"
FROM_LOG="$BASE_DIR/local-fromfile.log"
run_local "$LOCAL_BIN_DIR" --no-service --port 19514 --base-url http://127.0.0.1:1 --from-file "$FROM_SRC" > "$FROM_LOG" 2>&1
FROM_RC=$?
if [ "$FROM_RC" -eq 0 ] && [ "$(sum_of "$LI_INSTALL/leadstack")" = "$FROM_SUM" ] && [ "$FROM_SUM" != "$SRC_SUM" ]; then
	pass "--from-file 压过同目录探测（装的是指定那份）"
else
	fail "--from-file 未压过同目录探测（rc=${FROM_RC}，装出 $(sum_of "$LI_INSTALL/leadstack")）"
fi
grep -qF "使用本地文件 ${FROM_SRC}" "$FROM_LOG" && pass "阶段 2 结论点名 --from-file 的路径" || fail "阶段 2 未点名 from-file 路径"
if grep -q "二进制来源：同目录" "$FROM_LOG"; then
	fail "--from-file 时仍声明「二进制来源：同目录」"
else
	pass "--from-file 时不再报告同目录来源（优先级生效）"
fi

# 同目录归档：只有 leadstack_<ver>_<os>_<arch>.tar.gz 时同样能装
ARC_NAME="leadstack_0.1.99_${OS}_${ARCH}.tar.gz"
ARC_PKG="$BASE_DIR/arc-pkg"; mkdir -p "$ARC_PKG"
cp bin/leadstack "$ARC_PKG/leadstack"
tar -czf "$LOCAL_ARC_DIR/$ARC_NAME" -C "$ARC_PKG" leadstack
ARC_SUM="$(sum_of "$LOCAL_ARC_DIR/$ARC_NAME")"
ARC_LOG="$BASE_DIR/local-archive.log"
run_local "$LOCAL_ARC_DIR" --no-service --port 19515 --base-url http://127.0.0.1:1 > "$ARC_LOG" 2>&1
ARC_RC=$?
if [ "$ARC_RC" -eq 0 ] && grep -qF "使用同目录归档 ${ARC_NAME}" "$ARC_LOG"; then
	pass "同目录归档安装成功（${ARC_NAME}）"
else
	fail "同目录归档安装失败（rc=${ARC_RC}）：$(tail -3 "$ARC_LOG" | tr '\n' ' ')"
fi
grep -q "未找到 checksums.txt，跳过校验" "$ARC_LOG" \
	&& pass "无 checksums.txt 时只告警跳过校验、不中止" || fail "缺 checksums.txt 的处理不符合预期"
[ "$(sum_of "$LI_INSTALL/leadstack")" = "$SRC_SUM" ] \
	&& pass "归档内的二进制被正确装出（sha256 与源相同）" \
	|| fail "归档装出的二进制不一致：$(sum_of "$LI_INSTALL/leadstack")"

# 有 checksums.txt 且匹配 → 校验通过
printf '%s  %s\n' "$ARC_SUM" "$ARC_NAME" > "$LOCAL_ARC_DIR/checksums.txt"
ARC_OK_LOG="$BASE_DIR/local-archive-ok.log"
run_local "$LOCAL_ARC_DIR" --no-service --port 19515 --base-url http://127.0.0.1:1 > "$ARC_OK_LOG" 2>&1
grep -q "sha256 校验通过" "$ARC_OK_LOG" \
	&& pass "同目录 checksums.txt 匹配时校验通过" || fail "同目录 checksums.txt 未参与校验"

# 归档被篡改（checksums.txt 仍是旧哈希）→ 中止且不覆盖已装二进制
BEFORE_ARC_SUM="$(sum_of "$LI_INSTALL/leadstack")"
printf 'corrupted' >> "$LOCAL_ARC_DIR/$ARC_NAME"
BAD_ARC_LOG="$BASE_DIR/local-archive-badsum.log"
run_local "$LOCAL_ARC_DIR" --no-service --port 19515 --base-url http://127.0.0.1:1 > "$BAD_ARC_LOG" 2>&1
BAD_ARC_RC=$?
if [ "$BAD_ARC_RC" -ne 0 ] && grep -q "校验和不匹配" "$BAD_ARC_LOG"; then
	pass "同目录归档被篡改 → 校验和不匹配并中止（rc=${BAD_ARC_RC}）"
else
	fail "篡改的同目录归档未被拦截（rc=${BAD_ARC_RC}）：$(tail -3 "$BAD_ARC_LOG" | tr '\n' ' ')"
fi
[ "$(sum_of "$LI_INSTALL/leadstack")" = "$BEFORE_ARC_SUM" ] \
	&& pass "校验失败时未覆盖已安装二进制" || fail "校验失败仍覆盖了已装二进制"

# ---------------------------------------------------------------- 12. 自动接入本机
step "12. 自动接入本机（分支与初始密码）"
# 用户的原话：「接入本机还要在客户端自己做，应该可以自动完成或者初始安装的时候自动安装」。
# 所以安装脚本要自己把 agent 装上；这里先验证各条分支的判定与文案，真上线放到 13。
AGENT_DRY="$BASE_DIR/agent-dry.log"
run_install --dry-run --version "$VER" --base-url "$MOCK_BASE" --port 19516 > "$AGENT_DRY" 2>&1
grep -q '(7/7) 接入本机' "$AGENT_DRY" \
	&& pass "安装流程多出「7/7 接入本机」阶段" || fail "缺少 7/7 接入本机阶段"
grep -q 'dry-run：只打印自动接入动作' "$AGENT_DRY" \
	&& pass "dry-run 明确标注自动接入未真正执行" || fail "dry-run 未标注自动接入被跳过"

AGENT_OPT="$BASE_DIR/agent-optout.log"
# 带 --from-file：这两条只验第 7 阶段的判定，不该因为外网下载抖一下就整条失败
run_install --no-service --no-local-agent --from-file bin/leadstack --port 19516 > "$AGENT_OPT" 2>&1
grep -q '指定了 --no-local-agent' "$AGENT_OPT" \
	&& pass "--no-local-agent 跳过自动接入并说明原因" || fail "--no-local-agent 未被识别"

AGENT_NOSVC="$BASE_DIR/agent-noservice.log"
run_install --no-service --from-file bin/leadstack --port 19516 > "$AGENT_NOSVC" 2>&1
grep -q 'no-service 未注册服务，跳过自动接入' "$AGENT_NOSVC" \
	&& pass "--no-service 时跳过自动接入（agent 需要服务托管）" || fail "--no-service 未跳过自动接入"

# 初始密码：没指定就生成并写进环境文件——顺带治「装完密码就丢了」的老毛病
GEN_DIR="$BASE_DIR/gen"
GEN_BIN="$GEN_DIR/bin"; GEN_ENV="$GEN_DIR/etc"; GEN_DATA="$GEN_DIR/data"
GEN_LOG="$GEN_DIR/gen.log"
mkdir -p "$GEN_DIR"
sh "$SCRIPT" --install-dir "$GEN_BIN" --env-dir "$GEN_ENV" --data-dir "$GEN_DATA" \
	--no-service --port 19516 > "$GEN_LOG" 2>&1
# 以环境文件为权威来源（比从日志里抠字符串稳），再反过来核对汇总打印的同一个密码
GEN_PWD="$(sed -n 's/^LEADSTACK_ADMIN_PASSWORD=//p' "$GEN_ENV/leadstack.env" 2>/dev/null | head -n1)"
[ -n "$GEN_PWD" ] \
	&& pass "未指定 --admin-password 时生成了初始密码并写入环境文件" \
	|| fail "没有生成初始密码（环境文件缺 LEADSTACK_ADMIN_PASSWORD）"
[ "${#GEN_PWD}" -eq 20 ] \
	&& pass "生成的初始密码为 20 位" || fail "生成的密码长度异常（${#GEN_PWD} 位）"
grep -qF "初始管理员：admin / $GEN_PWD" "$GEN_LOG" \
	&& pass "安装汇总打印了与之一致的管理员密码" \
	|| fail "汇总没有打印生成的管理员密码"
grep -qF "本次由脚本随机生成" "$GEN_LOG" \
	&& pass "汇总说明了密码是本次生成的（并提示从环境文件删除）" \
	|| fail "汇总未说明密码来源"
GEN_PERM="$(stat -f '%Lp' "$GEN_ENV/leadstack.env" 2>/dev/null || stat -c '%a' "$GEN_ENV/leadstack.env" 2>/dev/null)"
[ "$GEN_PERM" = "600" ] && pass "环境文件权限 0600（含明文密码）" || fail "环境文件权限是 ${GEN_PERM}，应为 0600"

# 已有数据库（重复执行=升级）时不许凭空造密码：老密码不会因为环境变量变了而改变
GEN_ENV2="$GEN_DIR/etc2"
# --no-service 不会创建数据目录，这里显式建好再放一个库文件（幂等升级判据只看文件是否存在）
mkdir -p "$GEN_DATA"
: > "$GEN_DATA/leadstack.db"
UP_LOG="$GEN_DIR/upgrade.log"
sh "$SCRIPT" --install-dir "$GEN_BIN" --env-dir "$GEN_ENV2" --data-dir "$GEN_DATA" \
	--no-service --port 19516 > "$UP_LOG" 2>&1
if grep -q '本次由脚本随机生成' "$UP_LOG"; then
	fail "已有安装（库已存在）时仍生成了新密码，会误导用户"
else
	pass "已有安装时不凭空生成密码"
fi
if [ -f "$GEN_ENV2/leadstack.env" ] && grep -q 'LEADSTACK_ADMIN_PASSWORD=' "$GEN_ENV2/leadstack.env"; then
	fail "升级路径把生成的密码写进了环境文件"
else
	pass "升级路径不写入 ADMIN_PASSWORD（不覆盖既有账号）"
fi

# ---------------------------------------------------------------- 13. 容器内真跑
step "13. 自动接入本机（容器内真跑：安装 → agent 在线）"
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	# 必须放在仓库目录里：colima/Docker Desktop 只把用户目录（及工作目录）挂进容器，
	# 放在 $TMPDIR（/var/folders/...）时容器看到的是空目录，表现为「--from-file 指定的文件不存在」。
	LIN_DIR="$ROOT/bin/verify-linux"
	mkdir -p "$LIN_DIR"
	if GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$LIN_DIR/leadstack" ./cmd/leadstack 2>"$BASE_DIR/gobuild.log" \
		&& GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$LIN_DIR/leadstack-agent" ./cmd/leadstack-agent 2>>"$BASE_DIR/gobuild.log"; then
		pass "交叉编译出 linux/${ARCH} 控制端与 agent（容器里跑真二进制）"
		AGENT_E2E_LOG="$BASE_DIR/agent-e2e.log"
		docker run --rm --dns 8.8.8.8 \
			-v "$ROOT":/src:ro \
			-v "$LIN_DIR":/lin:ro \
			debian:stable-slim \
			sh -c '
				# 网络抖动很常见：装不上就重试三次，别让一次 timeout 把整段跳过
				for _try in 1 2 3; do
					apt-get update -qq >/dev/null 2>&1
					apt-get install -y -qq --no-install-recommends curl ca-certificates openssl passwd procps >/dev/null 2>&1 && break
					sleep 2
				done
				if ! command -v curl >/dev/null 2>&1; then echo NO_CURL; exit 9; fi
				export PATH=/src/scripts/testdata/fakebin:$PATH
				: >/tmp/fake-systemctl.log
				sh /src/deploy/install-control.sh \
					--port 19555 \
					--data-dir /tmp/ls/data --env-dir /tmp/ls/env --install-dir /tmp/ls/bin \
					--from-file /lin/leadstack --agent-from-file /lin/leadstack-agent \
					--admin-password E2ePassw0rdFixed || exit 1
				echo "=== 服务注册 ==="
				grep "leadstack-agent" /tmp/fake-systemctl.log || true
				echo "=== agent 环境文件 ==="
				stat -c "%a %n" /tmp/ls/env/agent.env 2>/dev/null || echo "缺 agent.env"
				echo "=== API 核对 ==="
				TOK=$(curl -fsS -X POST http://127.0.0.1:19555/api/v1/auth/login \
					-H "Content-Type: application/json" \
					-d "{\"username\":\"admin\",\"password\":\"E2ePassw0rdFixed\"}" \
					| sed -n "s/.*\"token\":\"\([^\"]*\)\".*/\1/p")
				[ -n "$TOK" ] && echo "登录成功" || { echo "登录失败"; exit 1; }
				curl -fsS -H "Authorization: Bearer $TOK" http://127.0.0.1:19555/api/v1/servers
				echo
				systemctl stop leadstack.service >/dev/null 2>&1 || true
				systemctl stop leadstack-agent.service >/dev/null 2>&1 || true
			' > "$AGENT_E2E_LOG" 2>&1
		AGENT_E2E_RC=$?
		if [ "$AGENT_E2E_RC" -eq 9 ] || grep -q NO_CURL "$AGENT_E2E_LOG"; then
			skip "容器内装不上 curl（网络受限），跳过「安装 → agent 上线」断言"
		elif [ "$AGENT_E2E_RC" -ne 0 ]; then
			fail "容器内一键安装失败（rc=${AGENT_E2E_RC}）：$(tail -3 "$AGENT_E2E_LOG" | tr '\n' ' ')"
		else
			pass "容器内一键安装退出码 0（含自动接入本机）"
			grep -q '本机 agent 已安装并注册 systemd 服务' "$AGENT_E2E_LOG" \
				&& pass "安装结论：agent 已注册系统服务" || fail "安装结论里没有 agent 注册"
			grep -q '本机接入：已自动完成' "$AGENT_E2E_LOG" \
				&& pass "汇总写明本机接入已自动完成" || fail "汇总缺「本机接入已完成」"
			grep -q 'systemctl enable --now leadstack-agent.service' "$AGENT_E2E_LOG" \
				&& pass "确实为 leadstack-agent 注册了服务" || fail "没有对 leadstack-agent 注册服务"
			grep -q '600 /tmp/ls/env/agent.env' "$AGENT_E2E_LOG" \
				&& pass "agent 环境文件（含令牌）权限 0600" || fail "agent 环境文件权限不是 0600"
			grep -q '登录成功' "$AGENT_E2E_LOG" \
				&& pass "安装时生成/指定的管理员密码可用于登录" || fail "管理员密码无法登录"
			grep -q '"mode":"agent"' "$AGENT_E2E_LOG" \
				&& pass "本机服务器以 agent 模式纳管" || fail "本机服务器没有建出来"
			grep -q '"status":"online"' "$AGENT_E2E_LOG" \
				&& pass "本机 agent 状态 online（安装完就在线）" || fail "本机 agent 未上线"
		fi
	else
		fail "交叉编译 linux 二进制失败：$(tail -3 "$BASE_DIR/gobuild.log" | tr '\n' ' ')"
	fi
else
	skip "本机没有可用的 docker，跳过「安装 → agent 上线」真机断言"
fi

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m控制端一键安装 验收通过\033[0m\n'; exit 0; fi
printf '\033[31m控制端一键安装 验收未通过\033[0m\n'
exit 1
