#!/usr/bin/env bash
# LeadStack Web UI 传输层验收：gzip 预压缩 / Vary / 缓存策略 / HEAD / 条件请求
#
# 覆盖内嵌前端产物（internal/webui）的传输行为。真起 bin/leadstack：
#   随机空闲端口 + 独立临时 data-dir + LEADSTACK_LISTEN，全部用 curl 实测响应头与字节。
#
# 用法：make build && bash scripts/verify_webui_transfer.sh
#
# 写法约束（与其它 scripts/verify_*.sh 一致）：
#   - macOS bash 3.2：不用 ${var,,}、不用关联数组；
#   - 断言失败不中断（set -uo pipefail，无 -e），结尾统一汇总；
#   - 每条断言打印实际值（状态码 / 头 / 字节数），失败能直接定位是哪一层。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BIN="./bin/leadstack"
BASE=""
PORT=""
DATA_DIR=""
LEAD_PID=""
PASS=0
FAIL=0
LAST_CC=""

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# ---------------------------------------------------------------- 进程与清理
stop_pid() { # <pid>：先 TERM，最多等 3s，再 KILL
  local pid="${1:-}" i
  [ -z "$pid" ] && return 0
  kill "$pid" 2>/dev/null || true
  for i in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
  return 0
}

cleanup() {
  local p
  # 1) 按监听端口收口（防止 PID 文件/变量丢失时留下孤儿）
  if [ -n "${PORT:-}" ]; then
    for p in $(lsof -nP -tiTCP:"${PORT}" -sTCP:LISTEN 2>/dev/null); do
      [ "$p" != "$$" ] && stop_pid "$p"
    done
  fi
  # 2) 按 PID 收口
  stop_pid "${LEAD_PID:-}"
  # 3) 兜底：清掉命令行里带本次临时 data-dir 的残留进程（正常路径上由 PID 收口）
  [ -n "${DATA_DIR:-}" ] && pkill -f "${DATA_DIR}" 2>/dev/null
  wait 2>/dev/null
  # 4) 删临时 data-dir
  [ -n "${DATA_DIR:-}" ] && rm -rf "${DATA_DIR}"
  return 0
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- HTTP 工具
pick_port() { # 随机挑一个 lsof 预检未被监听的高位端口
  local p i
  for i in $(seq 1 50); do
    p=$(( 20000 + RANDOM % 30000 ))
    if ! lsof -nP -iTCP:"${p}" -sTCP:LISTEN >/dev/null 2>&1; then
      printf '%s' "$p"
      return 0
    fi
  done
  return 1
}

req() { # <method> <path> <body-out> <hdr-out> [curl 参数...] → 打印 HTTP 状态码（HEAD 请用 req_head）
  local method="$1" path="$2" bodyf="$3" hdrf="$4" code
  shift 4
  code="$(curl -q -sS --noproxy '*' --max-time 15 -X "$method" \
    -o "$bodyf" -D "$hdrf" -w '%{http_code}' "$@" "${BASE}${path}" 2>/dev/null)"
  printf '%s' "${code:-000}"
}

# HEAD 专用：curl -I 会把响应头写进 -o 指定的文件，所以 body 是否存在
# 只能看 %{size_download}（HEAD 响应正确时为 0），不能拿 -o 文件大小当 body 长度。
req_head() { # <path> <hdr-out> [curl 参数...] → 打印 "<状态码> <body 字节数>"
  local path="$1" hdrf="$2" out
  shift 2
  out="$(curl -q -sS --noproxy '*' --max-time 15 --head \
    -o /dev/null -D "$hdrf" -w '%{http_code} %{size_download}' \
    "$@" "${BASE}${path}" 2>/dev/null)"
  printf '%s' "${out:-000 0}"
}

hdr() { # <hdr-file> <header-name> → 首个同名头的值（去 CR）
  tr -d '\r' < "$1" 2>/dev/null \
    | grep -i "^$2:[[:space:]]*" | head -1 | sed -e 's/^[^:]*:[[:space:]]*//'
}

hdr_has() { # <hdr-file> <header-name> <token>：大小写不敏感包含
  hdr "$1" "$2" | grep -qi -- "$3"
}

fsize() { if [ -f "$1" ]; then wc -c < "$1" | tr -d ' '; else echo 0; fi; }
sha256() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'; }

# 对「可压缩资源」跑完整 gzip 断言，并把 Cache-Control 留在 LAST_CC 供调用方断言
check_gzip_resource() { # <label> <url-path>
  local label="$1" path="$2"
  local gz_body="$DATA_DIR/res.gz" gz_hdr="$DATA_DIR/res.gz.hdr"
  local pl_body="$DATA_DIR/res.plain" pl_hdr="$DATA_DIR/res.plain.hdr"
  local code enc cl act plain gz drop

  code="$(req GET "$path" "$gz_body" "$gz_hdr" -H 'Accept-Encoding: gzip')"
  enc="$(hdr "$gz_hdr" Content-Encoding)"
  if [ "$code" = "200" ] && [ "$enc" = "gzip" ]; then
    pass "$label 带 Accept-Encoding: gzip → Content-Encoding: gzip（状态 ${code}）"
  else
    fail "$label 未返回 gzip（状态 ${code}，Content-Encoding=${enc:-空}）"
  fi

  if hdr_has "$gz_hdr" Vary "Accept-Encoding"; then
    pass "$label gzip 响应 Vary 含 Accept-Encoding"
  else
    fail "$label gzip 响应缺 Vary（实际：$(hdr "$gz_hdr" Vary)）"
  fi

  cl="$(hdr "$gz_hdr" Content-Length)"
  act="$(fsize "$gz_body")"
  if [ -n "$cl" ] && [ "$cl" = "$act" ]; then
    pass "$label gzip Content-Length=${cl} 与 body 字节数一致"
  else
    fail "$label gzip Content-Length 与 body 不一致（头=${cl:-空} body=${act}）"
  fi

  # 不带 Accept-Encoding：必须是原样字节，且不带 Content-Encoding
  code="$(req GET "$path" "$pl_body" "$pl_hdr")"
  enc="$(hdr "$pl_hdr" Content-Encoding)"
  if [ "$code" = "200" ] && [ -z "$enc" ]; then
    pass "$label 不带 gzip 请求 → 无 Content-Encoding（状态 ${code}）"
  else
    fail "$label 不带 gzip 请求异常（状态 ${code}，Content-Encoding=${enc:-空}）"
  fi

  if hdr_has "$pl_hdr" Vary "Accept-Encoding"; then
    pass "$label 原始响应同样带 Vary: Accept-Encoding"
  else
    fail "$label 原始响应缺 Vary（同一 URL 两种体会互相污染缓存）"
  fi

  # gzip -dc 后的 sha256 必须与不带 gzip 时拿到的 body sha256 完全相同
  local sha_plain sha_dec
  sha_plain="$(sha256 "$pl_body")"
  if gzip -dc "$gz_body" > "$DATA_DIR/res.dec" 2>/dev/null; then
    sha_dec="$(sha256 "$DATA_DIR/res.dec")"
  else
    sha_dec=""
  fi
  if [ -n "$sha_dec" ] && [ "$sha_dec" = "$sha_plain" ]; then
    pass "$label gzip 解压后 sha256 与原始 body 相同（${sha_plain:0:16}…）"
  else
    fail "$label sha256 不一致（原始 ${sha_plain:0:16}… / 解压 ${sha_dec:0:16}…）"
  fi

  plain="$(fsize "$pl_body")"
  gz="$(fsize "$gz_body")"
  if [ "$plain" -gt 0 ] 2>/dev/null; then
    drop=$(( (plain - gz) * 100 / plain ))
    if [ $(( (plain - gz) * 100 )) -ge $(( plain * 40 )) ]; then
      pass "$label 压缩后 ${gz} B / 原始 ${plain} B（下降 ${drop}%，≥40%）"
    else
      fail "$label 压缩收益不足：${gz} B / ${plain} B（下降 ${drop}%）"
    fi
  else
    fail "$label 原始 body 为空，无法计算压缩收益"
  fi

  LAST_CC="$(hdr "$pl_hdr" Cache-Control)"
}

check_long_cache() { # <label> <Cache-Control>
  local label="$1" cc="$2"
  case "$cc" in
    *immutable*) pass "$label Cache-Control 含 immutable（${cc}）" ;;
    *) fail "$label Cache-Control 缺 immutable（实际：${cc:-空}）" ;;
  esac
  case "$cc" in
    *max-age=31536000*) pass "$label max-age=31536000（内容哈希资源长缓存）" ;;
    *) fail "$label max-age 不是 31536000（实际：${cc:-空}）" ;;
  esac
}

# ---------------------------------------------------------------- 1. 启动
step "1. 构建产物与启动环境"
if [ -x "$BIN" ]; then
  pass "bin/leadstack 存在且可执行"
else
  fail "bin/leadstack 缺失或不可执行（先 make build）"
fi

DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-webui.XXXXXX" 2>/dev/null)"
if [ -n "$DATA_DIR" ] && [ -d "$DATA_DIR" ]; then
  pass "临时 data-dir 已创建（${DATA_DIR}）"
else
  fail "mktemp 创建临时 data-dir 失败"
fi

PORT="$(pick_port)"
if [ -n "$PORT" ]; then
  pass "随机空闲端口 ${PORT}（lsof 预检未被监听）"
else
  fail "找不到空闲端口"
fi

if [ "$FAIL" -gt 0 ]; then
  step "验收结果"
  printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
  printf '前置条件不满足，终止。\n'
  exit 1
fi

BASE="http://127.0.0.1:${PORT}"
LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="${DATA_DIR}" \
LEADSTACK_ADMIN_PASSWORD="webui-verify-pass" \
  "$BIN" >"${DATA_DIR}/server.log" 2>&1 &
LEAD_PID=$!

UP=0
for _ in $(seq 1 60); do
  CODE="$(curl -q -sS --noproxy '*' --max-time 3 -o /dev/null -w '%{http_code}' "${BASE}/healthz" 2>/dev/null)"
  if [ "$CODE" = "200" ]; then UP=1; break; fi
  sleep 0.3
done
if [ "$UP" = "1" ]; then
  pass "控制端在 ${BASE} 就绪（/healthz 200）"
  if lsof -nP -tiTCP:"${PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
    pass "进程确实监听在临时端口 ${PORT}（未误用默认 8080）"
  else
    fail "没有任何进程监听 ${PORT}"
  fi
else
  fail "控制端 20 秒内未就绪"
  tail -15 "${DATA_DIR}/server.log" 2>/dev/null | sed 's/^/      /'
  step "验收结果"
  printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
  printf '服务未就绪，终止。\n'
  exit 1
fi

# ---------------------------------------------------------------- 2. 未鉴权入口
step "2. 未鉴权入口页"
ROOT_BODY="$DATA_DIR/root.html"
ROOT_HDR="$DATA_DIR/root.hdr"
CODE="$(req GET / "$ROOT_BODY" "$ROOT_HDR")"
if [ "$CODE" = "200" ]; then
  pass "未带 Authorization 的 GET / 返回 200（实际 ${CODE}）"
else
  fail "未带 Authorization 的 GET / 应返回 200，实际 ${CODE}"
fi
if grep -q 'id="app"' "$ROOT_BODY" 2>/dev/null; then
  pass 'GET / 的 HTML 含 id="app"（前端挂载点存在）'
else
  fail 'GET / 的 HTML 不含 id="app"'
fi
CT="$(hdr "$ROOT_HDR" Content-Type)"
case "$CT" in
  text/html*) pass "GET / Content-Type 为 HTML（${CT}）" ;;
  *) fail "GET / Content-Type 异常：${CT:-空}" ;;
esac

IDX_BODY="$DATA_DIR/index.html.body"
IDX_HDR="$DATA_DIR/index.html.hdr"
CODE="$(req GET /index.html "$IDX_BODY" "$IDX_HDR")"
IDX_CC="$(hdr "$IDX_HDR" Cache-Control)"
if [ "$CODE" = "200" ] && [ "$IDX_CC" = "no-cache" ]; then
  pass "index.html 返回 200 且 Cache-Control: no-cache（实际：${IDX_CC}）"
else
  fail "index.html 缓存策略异常（状态 ${CODE}，Cache-Control=${IDX_CC:-空}）"
fi
case "$IDX_CC" in
  *immutable*) fail "index.html 不得带 immutable（否则发版后用户拿旧 HTML 引旧 bundle）" ;;
  *) pass "index.html 不含 immutable" ;;
esac

# 入口资源从 index.html 解析，避免把 Vite 内容哈希写死在脚本里
ENTRY_CSS="$(grep -oE 'assets/[^"]+\.css' "$ROOT_BODY" 2>/dev/null | head -1)"
ENTRY_JS="$(grep -oE 'assets/[^"]+\.js' "$ROOT_BODY" 2>/dev/null | head -1)"
if [ -n "$ENTRY_CSS" ]; then
  pass "从 index.html 解析到 CSS 入口 ${ENTRY_CSS}"
else
  fail "index.html 里找不到 assets/*.css 入口"
fi
if [ -n "$ENTRY_JS" ]; then
  pass "从 index.html 解析到 JS 入口 ${ENTRY_JS}"
else
  fail "index.html 里找不到 assets/*.js 入口"
fi
if [ -z "$ENTRY_CSS" ] || [ -z "$ENTRY_JS" ]; then
  step "验收结果"
  printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
  printf '入口资源缺失，终止。\n'
  exit 1
fi

# ---------------------------------------------------------------- 3. CSS 传输
step "3. 入口 CSS：gzip / Vary / 长度 / 解压一致 / 降幅"
check_gzip_resource "CSS" "/${ENTRY_CSS}"
check_long_cache "CSS" "$LAST_CC"

# ---------------------------------------------------------------- 4. JS 传输
step "4. 入口 JS：gzip / Vary / 长度 / 解压一致 / 降幅"
check_gzip_resource "JS" "/${ENTRY_JS}"
check_long_cache "JS" "$LAST_CC"

# ---------------------------------------------------------------- 5. 非压缩资源
step "5. 非压缩资源：png / install.sh / 小文件"
# 说明：当前 dist 里没有 png/woff2（唯一非文本大文件是 install.sh），所以这里
# ① 用 /assets/*.png 的未知路径证明带扩展名的二进制请求不会被当成文本预压缩；
# ② 紧接着用真实存在的 /install.sh 证明非白名单扩展名确实不进压缩表。
PNG_BODY="$DATA_DIR/png.body"
PNG_HDR="$DATA_DIR/png.hdr"
CODE="$(req GET "/assets/webui-transfer-probe.png" "$PNG_BODY" "$PNG_HDR" -H 'Accept-Encoding: gzip')"
if [ "$CODE" = "404" ]; then
  pass "未知 /assets/*.png 返回 404（带扩展名不回退 SPA，状态 ${CODE}）"
else
  fail "/assets/*.png 应返回 404，实际 ${CODE}"
fi
ENC="$(hdr "$PNG_HDR" Content-Encoding)"
if [ -z "$ENC" ]; then
  pass "png 路径无 Content-Encoding: gzip（二进制不得预压缩）"
else
  fail "png 路径被设置了 Content-Encoding=${ENC}"
fi

# dist 里目前唯一的真实非文本资源是 install.sh（.sh 不在可压缩扩展名白名单）
INST_BODY="$DATA_DIR/install.body"
INST_HDR="$DATA_DIR/install.hdr"
CODE="$(req GET /install.sh "$INST_BODY" "$INST_HDR" -H 'Accept-Encoding: gzip')"
ENC="$(hdr "$INST_HDR" Content-Encoding)"
if [ "$CODE" = "200" ] && [ -z "$ENC" ]; then
  pass "/install.sh 返回 200 且无 Content-Encoding（.sh 不预压缩）"
else
  fail "/install.sh 传输异常（状态 ${CODE}，Content-Encoding=${ENC:-空}）"
fi
INST_CC="$(hdr "$INST_HDR" Cache-Control)"
case "$INST_CC" in
  *max-age=3600*) pass "/install.sh 沿用短缓存 public, max-age=3600（实际：${INST_CC}）" ;;
  *) fail "/install.sh 缓存策略异常：${INST_CC:-空}" ;;
esac

# 小文件（< gzipMinSize=1024 B）不进预压缩表：favicon.svg 当前约 185 B
FAV_BODY="$DATA_DIR/fav.body"
FAV_HDR="$DATA_DIR/fav.hdr"
CODE="$(req GET /favicon.svg "$FAV_BODY" "$FAV_HDR" -H 'Accept-Encoding: gzip')"
ENC="$(hdr "$FAV_HDR" Content-Encoding)"
FAV_SIZE="$(fsize "$FAV_BODY")"
if [ "$CODE" = "200" ] && [ -z "$ENC" ]; then
  pass "小于 1024 B 的 favicon.svg（${FAV_SIZE} B）不压缩，无 Content-Encoding"
else
  fail "小文件被压缩或请求失败（状态 ${CODE}，${FAV_SIZE} B，Content-Encoding=${ENC:-空}）"
fi

# ---------------------------------------------------------------- 6. HEAD
step "6. HEAD：Content-Length 正确且 body 为空"
HEAD_HDR="$DATA_DIR/head.hdr"
HEAD_RES="$(req_head / "$HEAD_HDR")"
CODE="${HEAD_RES%% *}"
HEAD_BODY_BYTES="${HEAD_RES##* }"
if [ "$CODE" = "200" ]; then
  pass "HEAD / 返回 200"
else
  fail "HEAD / 应返回 200，实际 ${CODE}"
fi
HEAD_CL="$(hdr "$HEAD_HDR" Content-Length)"
ROOT_SIZE="$(fsize "$ROOT_BODY")"
if [ -n "$HEAD_CL" ] && [ "$HEAD_CL" = "$ROOT_SIZE" ]; then
  pass "HEAD / 的 Content-Length=${HEAD_CL} 与 GET / 的 body 字节数一致"
else
  fail "HEAD / Content-Length 不正确（头=${HEAD_CL:-空} GET body=${ROOT_SIZE}）"
fi
if [ "${HEAD_BODY_BYTES:-1}" = "0" ]; then
  pass "HEAD / 的 body 为空（下载字节 0）"
else
  fail "HEAD / 返回了 body（${HEAD_BODY_BYTES:-?} 字节）"
fi

# 带 gzip 的 HEAD：Content-Length 应是压缩后的长度，body 仍为空
HEADG_HDR="$DATA_DIR/head-gz.hdr"
ROOTG_BODY="$DATA_DIR/root-gz.body"
ROOTG_HDR="$DATA_DIR/root-gz.hdr"
CODE="$(req GET / "$ROOTG_BODY" "$ROOTG_HDR" -H 'Accept-Encoding: gzip')"
ROOTG_ENC="$(hdr "$ROOTG_HDR" Content-Encoding)"
HEADG_RES="$(req_head / "$HEADG_HDR" -H 'Accept-Encoding: gzip')"
HEADG_CODE="${HEADG_RES%% *}"
HEADG_BODY_BYTES="${HEADG_RES##* }"
HEADG_CL="$(hdr "$HEADG_HDR" Content-Length)"
ROOTG_SIZE="$(fsize "$ROOTG_BODY")"
if [ "$HEADG_CODE" = "200" ] && [ "$ROOTG_ENC" = "gzip" ] && [ -n "$HEADG_CL" ] \
   && [ "$HEADG_CL" = "$ROOTG_SIZE" ] && [ "${HEADG_BODY_BYTES:-1}" = "0" ]; then
  pass "HEAD /（Accept-Encoding: gzip）Content-Length=${HEADG_CL} 与压缩 body 一致且 body 为空"
else
  fail "HEAD gzip 不一致（GET 压缩体 ${ROOTG_SIZE} B / HEAD 头=${HEADG_CL:-空} / HEAD body=${HEADG_BODY_BYTES:-?} B / enc=${ROOTG_ENC:-空}）"
fi

# ---------------------------------------------------------------- 7. 条件请求
step "7. 条件请求：不得 500（304 或 200 都算通过，贴实际状态码）"
LM="$(hdr "$HEAD_HDR" Last-Modified)"
C_BODY="$DATA_DIR/cond.body"
C_HDR="$DATA_DIR/cond.hdr"
if [ -n "$LM" ]; then
  CODE="$(req GET /index.html "$C_BODY" "$C_HDR" -H "If-Modified-Since: ${LM}")"
else
  # 内嵌 FS 的 ModTime 为零值时服务端不发 Last-Modified；用一个合法旧日期仍应正常响应
  CODE="$(req GET /index.html "$C_BODY" "$C_HDR" -H "If-Modified-Since: Thu, 01 Jan 1970 00:00:00 GMT")"
fi
if [ "$CODE" = "200" ] || [ "$CODE" = "304" ]; then
  pass "If-Modified-Since 未 500（实际状态码 ${CODE}）"
else
  fail "If-Modified-Since 返回异常状态码 ${CODE}"
fi

CODE="$(req GET /index.html "$C_BODY" "$C_HDR" -H 'If-None-Match: "webui-transfer-verify"')"
if [ "$CODE" = "200" ] || [ "$CODE" = "304" ]; then
  pass "If-None-Match 未 500（实际状态码 ${CODE}）"
else
  fail "If-None-Match 返回异常状态码 ${CODE}"
fi

# ---------------------------------------------------------------- 8. Range 与 404
step "8. Range 不得走压缩 / 未知扩展名不得回退 SPA"
R_BODY="$DATA_DIR/range.body"
R_HDR="$DATA_DIR/range.hdr"
CODE="$(req GET "/${ENTRY_CSS}" "$R_BODY" "$R_HDR" \
  -H 'Accept-Encoding: gzip' -H 'Range: bytes=0-99')"
R_ENC="$(hdr "$R_HDR" Content-Encoding)"
if [ "$CODE" = "206" ] && [ -z "$R_ENC" ]; then
  pass "Range 请求返回 206 且未压缩（压缩字节偏移与原文不再对应）"
else
  fail "Range 请求异常（状态 ${CODE}，Content-Encoding=${R_ENC:-空}）"
fi

N_BODY="$DATA_DIR/notfound.body"
N_HDR="$DATA_DIR/notfound.hdr"
CODE="$(req GET /assets/webui-transfer-missing.js "$N_BODY" "$N_HDR")"
N_CT="$(hdr "$N_HDR" Content-Type)"
case "$CODE" in
  404) pass "未知 /assets/*.js 返回 404（状态 ${CODE}）" ;;
  *) fail "未知 /assets/*.js 应返回 404，实际 ${CODE}" ;;
esac
case "$N_CT" in
  text/html*) fail "未知 .js 返回了 HTML（SPA 回退），Content-Type=${N_CT}" ;;
  *) pass "未知 .js 未回退成 HTML（Content-Type=${N_CT:-空}）" ;;
esac

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m传输层 gzip 与缓存策略 验收通过\033[0m\n'
  exit 0
fi
printf '\033[31m传输层 gzip 与缓存策略 验收未通过\033[0m\n'
exit 1
