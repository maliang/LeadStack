#!/usr/bin/env bash
# LeadStack M5/M6 运维增强与团队协作端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 47 节。
# 用法：make build && make devssh && bash scripts/verify_ops.sh
#
# 关键：终端与文件走真实 devssh（命令在本机真实执行），不是只看接口 200。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18940}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-ops-pass"
# 部署/终端路径会过校验，强制 ASCII 临时根
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
BASE_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-ops.XXXXXX")"
DATA_DIR="$BASE_DIR/data"
UP_DIR="$BASE_DIR/upload"
LEAD_PID=""; DEVSSH_PID=""; CRON_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$CRON_PID" "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  wait 2>/dev/null
  rm -rf "$BASE_DIR"
}
trap cleanup EXIT

api() {
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
status_of() { curl -sS -o /dev/null -w '%{http_code}' "$@" 2>/dev/null; }
jget() { printf '%s' "$1" | jq -r "$2" 2>/dev/null; }
sget() { local v; v="$(jget "$1" "$2")"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }
login() { api POST /api/v1/auth/login "{\"username\":\"$1\",\"password\":\"$2\"}" | jq -r '.data.token // empty' 2>/dev/null; }

start_lead() { # start_lead [extra-env...]
  mkdir -p "$DATA_DIR" "$UP_DIR"
  env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
      LEADSTACK_DATA_DIR="$DATA_DIR" \
      LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
      LEADSTACK_SSH_INTERVAL="30s" \
      "$@" \
      ./bin/leadstack > "$BASE_DIR/lead.log" 2>&1 &
  LEAD_PID=$!
  for _ in $(seq 1 80); do [ "$(status_of "${BASE}/healthz")" = "200" ] && return 0; sleep 0.3; done
  return 1
}
stop_lead() { [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null; wait "$LEAD_PID" 2>/dev/null; LEAD_PID=""; }

echo "临时目录：$BASE_DIR"

# ---------------------------------------------------------------- 1. 构建
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-devssh; do
  [ -x "$bin" ] && pass "$bin 存在" || fail "$bin 缺失"
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动环境
step "2. 启动 devssh 与控制端（终端开启）"
ssh-keygen -q -t ed25519 -N '' -f "$BASE_DIR/id" >/dev/null 2>&1
./bin/leadstack-devssh --addr 127.0.0.1:0 --user devuser --password devpass \
  --authorized-key-file "$BASE_DIR/id.pub" --port-file "$BASE_DIR/ssh.port" \
  > "$BASE_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SSH_PORT=""
for _ in $(seq 1 40); do SSH_PORT="$(cat "$BASE_DIR/ssh.port" 2>/dev/null)"; [ -n "$SSH_PORT" ] && break; sleep 0.25; done
[ -n "$SSH_PORT" ] && pass "devssh 监听 127.0.0.1:${SSH_PORT}" || { fail "devssh 未就绪"; exit 1; }

start_lead LEADSTACK_TERMINAL_ENABLED=true LEADSTACK_CRON_TICK=1s && pass "控制端就绪（终端开启 / cron tick 1s）" \
  || { fail "控制端未就绪"; tail -20 "$BASE_DIR/lead.log" | sed 's/^/      /'; exit 1; }

TOKEN="$(login admin "$ADMIN_PASS")"
[ -n "$TOKEN" ] && pass "admin 登录成功" || { fail "登录失败"; exit 1; }

CRED_ID="$(sget "$(api POST /api/v1/credentials \
  "$(jq -n --arg k "$(cat "$BASE_DIR/id")" '{name:"ops-key",kind:"key",username:"devuser",private_key:$k}')" "$TOKEN")" '.data.id')"
SRV_ID="$(sget "$(api POST /api/v1/servers \
  "$(jq -n --argjson port "$SSH_PORT" --arg cid "$CRED_ID" \
     '{name:"ops-node",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid,group:"ops"}')" "$TOKEN")" '.data.id')"
[ -n "$SRV_ID" ] && pass "目标服务器已创建" || { fail "创建服务器失败"; exit 1; }

# ---------------------------------------------------------------- 3. 审计日志
step "3. 审计日志"
AUDIT="$(api GET '/api/v1/audit-logs?limit=100' "" "$TOKEN")"
if printf '%s' "$AUDIT" | jq -e '(.data.page != null) and ((.data.logs? // .data) | length > 0)' >/dev/null 2>&1 \
   || printf '%s' "$AUDIT" | jq -e '(.page != null) and (.data | length > 0)' >/dev/null 2>&1; then
  pass "审计日志已记录写操作"
else
  # 兼容可能的分页结构
  N="$(printf '%s' "$AUDIT" | jq -r '((.data.logs? // .data) | length) // 0' 2>/dev/null)"
  [ "${N:-0}" -gt 0 ] && pass "审计日志已记录写操作（${N} 条）" || fail "审计日志为空：$(printf '%s' "$AUDIT" | head -c 200)"
fi
if printf '%s' "$AUDIT" | grep -q "BEGIN OPENSSH PRIVATE KEY\|secret-pw\|ops-key.*private"; then
  fail "审计日志泄露了敏感内容"
else
  pass "审计日志不含密钥明文"
fi
HAS_SERVER_CREATE="$(printf '%s' "$AUDIT" | grep -c 'server.create' || true)"
[ "${HAS_SERVER_CREATE:-0}" -ge 1 ] && pass "记录了 server.create 动作" || fail "未记录 server.create（action 命名或中间件缺失）"

# ---------------------------------------------------------------- 4. RBAC
step "4. RBAC 与用户管理"
VIEWER_RESP="$(api POST /api/v1/users \
  "$(jq -n --arg u "ops-viewer" --arg p "viewer-pass-1" '{username:$u,password:$p,role:"viewer"}')" "$TOKEN")"
VIEWER_ID="$(sget "$VIEWER_RESP" '.data.id')"
[ -n "$VIEWER_ID" ] && pass "创建 viewer 用户成功" || fail "创建 viewer 失败：$VIEWER_RESP"
OPER_RESP="$(api POST /api/v1/users \
  "$(jq -n --arg u "ops-operator" --arg p "operator-pass-1" '{username:$u,password:$p,role:"operator"}')" "$TOKEN")"
OPER_ID="$(sget "$OPER_RESP" '.data.id')"
[ -n "$OPER_ID" ] && pass "创建 operator 用户成功" || fail "创建 operator 失败：$OPER_RESP"

VTOKEN="$(login ops-viewer viewer-pass-1)"
[ -n "$VTOKEN" ] && pass "viewer 可登录" || fail "viewer 登录失败"
RD_CODE="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/servers")"
[ "$RD_CODE" = "200" ] && pass "viewer 可读服务器列表" || fail "viewer 读被拒（HTTP ${RD_CODE}）"
WR_CODE="$(status_of -X POST -H "Authorization: Bearer ${VTOKEN}" -H 'Content-Type: application/json' \
  -d '{"name":"x","host":"127.0.0.1","port":22,"mode":"ssh"}' "${BASE}/api/v1/servers")"
[ "$WR_CODE" = "403" ] && pass "viewer 写操作被拒（403）" || fail "viewer 写操作未拒绝（HTTP ${WR_CODE}）"
USR_CODE="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/users")"
[ "$USR_CODE" = "403" ] && pass "viewer 读用户列表被拒（403）" || fail "viewer 读用户列表未拒绝（HTTP ${USR_CODE}）"

OTOKEN="$(login ops-operator operator-pass-1)"
WR2="$(status_of -X POST -H "Authorization: Bearer ${OTOKEN}" -H 'Content-Type: application/json' \
  -d '{"name":"ops-2","host":"127.0.0.2","port":22,"mode":"ssh"}' "${BASE}/api/v1/servers")"
[ "$WR2" = "201" ] && pass "operator 可执行写操作（201）" || fail "operator 写操作失败（HTTP ${WR2}）"
USR2_CODE="$(status_of -H "Authorization: Bearer ${OTOKEN}" "${BASE}/api/v1/users")"
[ "$USR2_CODE" = "403" ] && pass "operator 管理用户被拒（403）" || fail "operator 管理用户未拒绝（HTTP ${USR2_CODE}）"

SELF_DEL="$(status_of -X DELETE -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/users/$(api GET /api/v1/auth/me "" "$TOKEN" | jq -r '.data.id // empty')")"
[ "$SELF_DEL" = "400" ] || [ "$SELF_DEL" = "403" ] && pass "禁止删除自己（HTTP ${SELF_DEL}）" || fail "允许删除自己（HTTP ${SELF_DEL}）"

# ---------------------------------------------------------------- 5. API Token
step "5. API Token"
TK_RESP="$(api POST /api/v1/api-tokens '{"name":"ci-token","role":"viewer"}' "$TOKEN")"
TK_PLAIN="$(sget "$TK_RESP" '.data.token')"
TK_ID="$(sget "$TK_RESP" '.data.id')"
if printf '%s' "$TK_PLAIN" | grep -q '^lsk_'; then
  pass "创建 token 并返回一次明文（lsk_ 前缀）"
else
  fail "token 创建异常：$TK_RESP"
fi
TK_CODE="$(status_of -H "Authorization: Bearer ${TK_PLAIN}" "${BASE}/api/v1/servers")"
[ "$TK_CODE" = "200" ] && pass "用 token 可访问 API" || fail "token 认证失败（HTTP ${TK_CODE}）"
TK_LIST="$(api GET /api/v1/api-tokens "" "$TOKEN")"
if [ -n "$TK_PLAIN" ] && printf '%s' "$TK_LIST" | grep -q "$TK_PLAIN"; then
  fail "token 列表泄露明文"
else
  pass "token 列表不含明文"
fi
api DELETE "/api/v1/api-tokens/${TK_ID}" "" "$TOKEN" >/dev/null
TK_AFTER="$(status_of -H "Authorization: Bearer ${TK_PLAIN}" "${BASE}/api/v1/servers")"
[ "$TK_AFTER" = "401" ] && pass "撤销后 token 失效（401）" || fail "撤销后仍可用（HTTP ${TK_AFTER}）"

# ---------------------------------------------------------------- 6. 分页
step "6. 分页"
P1="$(api GET '/api/v1/audit-logs?limit=2' "" "$TOKEN")"
P1_N="$(printf '%s' "$P1" | jq -r '((.data.logs? // .data) | length) // 0' 2>/dev/null)"
P1_MORE="$(printf '%s' "$P1" | jq -r '(.data.page.has_more // .page.has_more // empty)' 2>/dev/null)"
P1_CUR="$(printf '%s' "$P1" | jq -r '(.data.page.next_cursor // .page.next_cursor // empty)' 2>/dev/null)"
[ "${P1_N:-0}" -le 2 ] && [ -n "$P1_MORE" ] && pass "limit=2 生效（${P1_N} 条，has_more=${P1_MORE}）" || fail "分页参数未生效"
if [ -n "$P1_CUR" ]; then
  P2="$(api GET "/api/v1/audit-logs?limit=2&cursor=${P1_CUR}" "" "$TOKEN")"
  P2_FIRST="$(printf '%s' "$P2" | jq -r '(.data.logs? // .data)[0].id // empty' 2>/dev/null)"
  P1_LAST="$(printf '%s' "$P1" | jq -r '(.data.logs? // .data)[-1].id // empty' 2>/dev/null)"
  [ "$P2_FIRST" != "$P1_LAST" ] && pass "cursor 翻页不重复" || fail "cursor 翻页重复（首条与上页末条相同）"
else
  fail "未返回 next_cursor"
fi
NO_LIMIT="$(api GET /api/v1/servers "" "$TOKEN")"
printf '%s' "$NO_LIMIT" | jq -e '.data | type == "array"' >/dev/null 2>&1 \
  && pass "不传 limit 时仍返回数组（未破坏既有接口）" || fail "默认响应形状被改变"

# ---------------------------------------------------------------- 7. 批量导入
step "7. 批量导入主机"
IMP_RESP="$(api POST /api/v1/servers/import \
  "$(jq -n '{text:"10.9.0.1\n10.9.0.2:2222\n# 注释\n\n10.9.0.1",default_port:22,mode:"ssh"}')" "$TOKEN")"
IMP_OK="$(printf '%s' "$IMP_RESP" | jq -r '.data.created | length' 2>/dev/null)"
IMP_BAD="$(printf '%s' "$IMP_RESP" | jq -r '.data.failed | length' 2>/dev/null)"
if [ "${IMP_OK:-0}" = "2" ] && [ "${IMP_BAD:-0}" -ge 1 ]; then
  pass "导入逐条结果正确（成功 ${IMP_OK} / 失败 ${IMP_BAD}）"
else
  fail "导入结果异常：$(printf '%s' "$IMP_RESP" | head -c 250)"
fi

# ---------------------------------------------------------------- 8. 终端
step "8. Web SSH 终端"
TICKET_RESP="$(api POST "/api/v1/servers/${SRV_ID}/terminal-ticket" "" "$TOKEN")"
TICKET="$(sget "$TICKET_RESP" '.data.ticket')"
[ -n "$TICKET" ] && pass "申请终端 ticket 成功" || fail "申请 ticket 失败：$TICKET_RESP"
TERM_OUT="$(python3 - "$PORT" "$SRV_ID" "$TICKET" <<'PY' 2>&1
import sys, json, socket, base64, time
port, sid, ticket = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    import websocket  # 可能不存在
except ImportError:
    websocket = None
if websocket is None:
    # 无第三方库时用最简 WS 客户端（握手 + 帧）
    import os, struct, hashlib
    key = base64.b64encode(os.urandom(16)).decode()
    s = socket.create_connection(("127.0.0.1", int(port)), timeout=10)
    path = f"/api/v1/servers/{sid}/terminal?ticket={ticket}"
    req = (f"GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nUpgrade: websocket\r\n"
           f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n")
    s.sendall(req.encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        buf += s.recv(4096)
    if b"101" not in buf.split(b"\r\n")[0]:
        print("HANDSHAKE_FAIL:" + buf.split(b"\r\n")[0].decode("latin1")); sys.exit(0)
    def send_text(payload):
        data = payload.encode()
        hdr = bytearray([0x81])
        n = len(data)
        if n < 126: hdr.append(0x80 | n)
        else:
            hdr.append(0x80 | 126); hdr += struct.pack(">H", n)
        mask = os.urandom(4)
        hdr += mask
        s.sendall(bytes(hdr) + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))
    def recv_frame():
        h = s.recv(2)
        if len(h) < 2: return None
        op = h[0] & 0x0F
        n = h[1] & 0x7F
        if n == 126: n = struct.unpack(">H", s.recv(2))[0]
        elif n == 127: n = struct.unpack(">Q", s.recv(8))[0]
        data = b""
        while len(data) < n:
            chunk = s.recv(n - len(data))
            if not chunk: break
            data += chunk
        return op, data
    send_text(json.dumps({"type":"open","cols":100,"rows":30}))
    time.sleep(0.5)
    send_text(json.dumps({"type":"input","data":"echo ls-terminal-ok\n"}))
    out, deadline = b"", time.time() + 12
    while time.time() < deadline:
        try:
            fr = recv_frame()
        except Exception:
            break
        if not fr: break
        op, data = fr
        if op == 0x8: break
        out += data
        if b"ls-terminal-ok" in out and out.count(b"ls-terminal-ok") >= 2:
            break
    print("GOT:" + out.decode("utf-8", "replace"))
else:
    print("SKIP")
PY
)"
if printf '%s' "$TERM_OUT" | grep -q "ls-terminal-ok"; then
  pass "终端执行命令并收到回显（echo ls-terminal-ok）"
else
  fail "终端未收到预期输出：$(printf '%s' "$TERM_OUT" | tail -c 200)"
fi
AUDIT_TERM="$(api GET '/api/v1/audit-logs?limit=200' "" "$TOKEN")"
printf '%s' "$AUDIT_TERM" | grep -q 'terminal.open' && pass "审计记录了 terminal.open" || fail "审计缺少 terminal.open"

# ---------------------------------------------------------------- 9. 文件传输
step "9. 文件上传 / 下载 / 删除"
printf 'leadstack-file-upload-evidence\n' > "$UP_DIR/probe.txt"
TARGET_PATH="$BASE_DIR/remote-probe.txt"
UP_CODE="$(curl -sS -o "$BASE_DIR/up.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${TOKEN}" -F "path=${TARGET_PATH}" -F "file=@${UP_DIR}/probe.txt" \
  "${BASE}/api/v1/servers/${SRV_ID}/files/upload")"
if [ "$UP_CODE" = "200" ] && [ -f "$TARGET_PATH" ]; then
  pass "上传成功且文件真实落盘"
  grep -q "leadstack-file-upload-evidence" "$TARGET_PATH" && pass "落盘内容一致" || fail "落盘内容不一致"
else
  fail "上传失败（HTTP ${UP_CODE}）：$(head -c 150 "$BASE_DIR/up.json" 2>/dev/null)"
fi
DL_CONTENT="$(curl -sS -H "Authorization: Bearer ${TOKEN}" \
  "${BASE}/api/v1/servers/${SRV_ID}/files/download?path=${TARGET_PATH}")"
[ "$DL_CONTENT" = "leadstack-file-upload-evidence" ] && pass "下载内容一致" || fail "下载内容不一致：${DL_CONTENT:0:60}"
LIST_RESP="$(api GET "/api/v1/servers/${SRV_ID}/files?path=${BASE_DIR}" "" "$TOKEN")"
printf '%s' "$LIST_RESP" | grep -q "remote-probe.txt" && pass "目录列表可见该文件" || fail "目录列表不含该文件"
DEL_CODE="$(status_of -X DELETE -H "Authorization: Bearer ${TOKEN}" \
  "${BASE}/api/v1/servers/${SRV_ID}/files?path=${TARGET_PATH}")"
if [ "$DEL_CODE" = "200" ] || [ "$DEL_CODE" = "204" ]; then
  [ ! -f "$TARGET_PATH" ] && pass "删除后文件不存在" || fail "删除后文件仍存在"
else
  fail "删除失败（HTTP ${DEL_CODE}）"
fi
TRAV="$(status_of -H "Authorization: Bearer ${TOKEN}" \
  "${BASE}/api/v1/servers/${SRV_ID}/files/download?path=../../../../etc/passwd")"
[ "$TRAV" = "400" ] || [ "$TRAV" = "403" ] && pass "路径穿越被拒绝（HTTP ${TRAV}）" || fail "路径穿越未被拒绝（HTTP ${TRAV}）"

# ---------------------------------------------------------------- 10. 定时任务
step "10. 定时任务"
CRON_RESP="$(api POST /api/v1/scheduled-tasks \
  "$(jq -n --arg sid "$SRV_ID" '{name:"ops-cron",cron:"* * * * *",command:"echo cron-ok",timeout_s:10,server_ids:[$sid]}')" "$TOKEN")"
CRON_ID="$(sget "$CRON_RESP" '.data.id')"
[ -n "$CRON_ID" ] && pass "创建定时任务成功" || fail "创建定时任务失败：$CRON_RESP"
BAD_CRON="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"name":"bad","cron":"61 * * * *","command":"echo x","server_ids":[]}' "${BASE}/api/v1/scheduled-tasks")"
[ "$BAD_CRON" = "400" ] && pass "非法 cron 被拒绝（400）" || fail "非法 cron 未拒绝（HTTP ${BAD_CRON}）"
RUN_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/scheduled-tasks/${CRON_ID}/run")"
[ "$RUN_CODE" = "200" ] || [ "$RUN_CODE" = "202" ] && pass "立即运行已接受（HTTP ${RUN_CODE}）" || fail "立即运行失败（HTTP ${RUN_CODE}）"
RUNS_OK=0
for _ in $(seq 1 40); do
  RUNS="$(api GET "/api/v1/scheduled-tasks/${CRON_ID}/runs?limit=10" "" "$TOKEN")"
  if printf '%s' "$RUNS" | jq -e '[(.data.runs? // .data)[] | select(.exit_code == 0)] | length > 0' >/dev/null 2>&1; then
    RUNS_OK=1; break
  fi
  sleep 1
done
[ "$RUNS_OK" = "1" ] && pass "任务运行记录存在且 exit_code=0" || fail "未产生成功运行记录：$(printf '%s' "${RUNS:-}" | head -c 200)"

# ---------------------------------------------------------------- 11. 日志 / 进程 / 端口
step "11. 日志查看、进程与端口"
printf 'line-1\nline-2\nline-3-error\n' > "$BASE_DIR/app.log"
LOG_RESP="$(api GET "/api/v1/servers/${SRV_ID}/logs?path=${BASE_DIR}/app.log&lines=10" "" "$TOKEN")"
if printf '%s' "$LOG_RESP" | jq -e '(.data.lines? // .data) | length > 0' >/dev/null 2>&1 \
   || printf '%s' "$LOG_RESP" | grep -q "line-3-error"; then
  pass "日志查看返回文件内容"
else
  fail "日志查看异常：$(printf '%s' "$LOG_RESP" | head -c 200)"
fi
LOG_GREP="$(api GET "/api/v1/servers/${SRV_ID}/logs?path=${BASE_DIR}/app.log&lines=50&grep=error" "" "$TOKEN")"
if printf '%s' "$LOG_GREP" | grep -q "line-3-error" && ! printf '%s' "$LOG_GREP" | grep -q "line-1"; then
  pass "grep 过滤生效（只返回匹配行）"
else
  fail "grep 过滤未生效：$(printf '%s' "$LOG_GREP" | head -c 150)"
fi
PROC_RESP="$(api GET "/api/v1/servers/${SRV_ID}/processes?limit=10" "" "$TOKEN")"
PROC_N="$(printf '%s' "$PROC_RESP" | jq -r '(.data.processes? // .data) | length' 2>/dev/null)"
[ "${PROC_N:-0}" -ge 1 ] && pass "进程列表非空（${PROC_N} 条）" || fail "进程列表为空：$(printf '%s' "$PROC_RESP" | head -c 150)"
PORT_RESP="$(api GET "/api/v1/servers/${SRV_ID}/ports" "" "$TOKEN")"
PORT_N="$(printf '%s' "$PORT_RESP" | jq -r '(.data.ports? // .data) | length' 2>/dev/null)"
[ "${PORT_N:-0}" -ge 1 ] && pass "端口列表非空（${PORT_N} 条）" || fail "端口列表为空：$(printf '%s' "$PORT_RESP" | head -c 150)"

# ---------------------------------------------------------------- 12. onboarding
step "12. 首次安装向导状态"
ONB="$(api GET /api/v1/system/onboarding "" "$TOKEN")"
if printf '%s' "$ONB" | jq -e '.data | has("needs_setup")' >/dev/null 2>&1; then
  pass "onboarding 状态可用：$(printf '%s' "$ONB" | jq -c '.data | {needs_setup,has_servers,has_credentials}')"
else
  fail "onboarding 接口异常：$ONB"
fi

# ---------------------------------------------------------------- 13. 终端关闭行为
step "13. 终端开关（关闭时应 403）"
stop_lead
start_lead LEADSTACK_TERMINAL_ENABLED=false && pass "以终端关闭模式重启" || fail "重启失败"
TOKEN="$(login admin "$ADMIN_PASS")"
TICKET_OFF="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${SRV_ID}/terminal-ticket")"
[ "$TICKET_OFF" = "403" ] && pass "终端关闭时 ticket 返回 403" || fail "终端关闭时未拒绝（HTTP ${TICKET_OFF}）"

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32mM5/M6 验收通过\033[0m\n'; exit 0; fi
printf '\033[31mM5/M6 验收未通过\033[0m\n'
exit 1
