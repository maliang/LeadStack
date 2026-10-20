#!/usr/bin/env bash
# LeadStack 检测 端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 17 节 监控检测验收标准。
# 用法：make build && make devssh && bash scripts/verify_checks.sh
#
# 写法约束（踩过的坑，勿改）：**不要**把带 \" 转义的 JSON 放进两层嵌套的 "$( )" 里，
# bash 3.2 会把参数拆坏。所有请求都先落到变量，再用 jget 取值（两步式）。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18793}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m2-pass"
DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-m2.XXXXXX")"
KEY_FILE="$DATA_DIR/id_ed25519"
SSH_PORT_FILE="$DATA_DIR/ssh.port"
LEAD_PID=""; DEVSSH_PID=""; AGENT_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  [ -n "$DEVSSH_PID" ] && kill "$DEVSSH_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$DATA_DIR"
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
jget() { printf '%s' "$1" | jq -r "$2 // empty" 2>/dev/null; }
now_ms() { echo "$(($(date +%s) * 1000))"; }

check_status() { # <check_id> <token>
  api GET /api/v1/checks "" "$2" \
    | jq -r ".data[] | select(.id==\"$1\") | .status // empty" 2>/dev/null
}
run_field() { # <check_id> <token> <field>
  # 注意：不能用 `// empty` —— jq 的 `//` 会把布尔 false 当 falsey 一并丢弃，
  # 导致 ok=false 被读成空串（检测 集成时踩过）。这里只把 null 归一成空串。
  local resp val
  resp="$(api POST "/api/v1/checks/$1/run" "" "$2")"
  val="$(printf '%s' "$resp" | jq -r ".data.results[0].$3" 2>/dev/null)"
  [ "$val" = "null" ] && val=""
  printf '%s' "$val"
}

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 1. 构建产物
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-agent bin/leadstack-devssh; do
  [ -x "$bin" ] && pass "$bin 存在" || fail "$bin 缺失"
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动
step "2. 启动测试 SSH 端点与控制端"
ssh-keygen -q -t ed25519 -N '' -f "$KEY_FILE" >/dev/null 2>&1 && pass "生成测试密钥对" || fail "ssh-keygen 失败"

./bin/leadstack-devssh --addr 127.0.0.1:0 --user devuser --password devpass \
  --authorized-key-file "$KEY_FILE.pub" --port-file "$SSH_PORT_FILE" \
  > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SSH_PORT=""
for _ in $(seq 1 40); do SSH_PORT="$(cat "$SSH_PORT_FILE" 2>/dev/null)"; [ -n "$SSH_PORT" ] && break; sleep 0.25; done
[ -n "$SSH_PORT" ] && pass "devssh 监听 127.0.0.1:${SSH_PORT}" || { fail "devssh 未就绪"; exit 1; }

LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="$DATA_DIR" \
LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
LEADSTACK_SSH_INTERVAL="3s" \
LEADSTACK_CHECK_INTERVAL_MIN="10s" \
  ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
UP=0
for _ in $(seq 1 60); do [ "$(status_of "${BASE}/healthz")" = "200" ] && { UP=1; break; }; sleep 0.3; done
[ "$UP" = "1" ] && pass "控制端就绪" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'; exit 1; }

# ---------------------------------------------------------------- 3. 鉴权与目标机器
step "3. 鉴权与准备两台目标机器"
LOGIN_BODY="{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}"
LOGIN_RESP="$(api POST /api/v1/auth/login "$LOGIN_BODY")"
TOKEN="$(jget "$LOGIN_RESP" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败：$LOGIN_RESP"; exit 1; }
[ "$(status_of "${BASE}/api/v1/checks")" = "401" ] && pass "无 token 访问 /checks 返回 401" || fail "无 token 访问 /checks 未返回 401"

CRED_BODY="$(jq -n --arg k "$(cat "$KEY_FILE")" '{name:"m2-key",kind:"key",username:"devuser",private_key:$k}')"
CRED_RESP="$(api POST /api/v1/credentials "$CRED_BODY" "$TOKEN")"
CRED_ID="$(jget "$CRED_RESP" '.data.id')"
[ -n "$CRED_ID" ] && pass "私钥凭证已创建" || { fail "创建凭证失败：$CRED_RESP"; exit 1; }

SRV_BODY="$(jq -n --argjson port "$SSH_PORT" --arg cid "$CRED_ID" \
  '{name:"m2-ssh",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid,group:"m2"}')"
SRV_RESP="$(api POST /api/v1/servers "$SRV_BODY" "$TOKEN")"
SRV_SSH="$(jget "$SRV_RESP" '.data.id')"
[ -n "$SRV_SSH" ] && pass "SSH 模式服务器已创建" || { fail "创建 SSH 服务器失败：$SRV_RESP"; exit 1; }

ETOKEN_RESP="$(api POST /api/v1/enroll-tokens '{"note":"m2","ttl_s":900}' "$TOKEN")"
ETOKEN="$(jget "$ETOKEN_RESP" '.data.token')"
./bin/leadstack-agent --server "$BASE" --token "$ETOKEN" > "$DATA_DIR/agent.log" 2>&1 &
AGENT_PID=$!
SRV_AGENT=""
for _ in $(seq 1 40); do
  SERVERS="$(api GET /api/v1/servers "" "$TOKEN")"
  SRV_AGENT="$(printf '%s' "$SERVERS" | jq -r '[.data[] | select(.mode=="agent")][0].id // empty' 2>/dev/null)"
  [ -n "$SRV_AGENT" ] && break
  sleep 0.5
done
[ -n "$SRV_AGENT" ] && pass "agent 模式服务器已上线（${SRV_AGENT:0:8}…）" || fail "agent 未上线"

# ---------------------------------------------------------------- 4. 检测项：成功路径
step "4. 检测项创建与立即执行（成功路径）"
TCP_TARGET="127.0.0.1:${SSH_PORT}"
TCP_BODY="$(jq -n --arg t "$TCP_TARGET" --arg sid "$SRV_SSH" \
  '{name:"m2-tcp-alive",kind:"tcp",target:$t,interval_s:10,timeout_s:5,server_ids:[$sid]}')"
TCP_RESP="$(api POST /api/v1/checks "$TCP_BODY" "$TOKEN")"
TCP_ID="$(jget "$TCP_RESP" '.data.id')"
[ -n "$TCP_ID" ] && pass "tcp 检测项已创建" || { fail "创建 tcp 检测项失败：$TCP_RESP"; exit 1; }

TCP_OK="$(run_field "$TCP_ID" "$TOKEN" ok)"
TCP_LAT="$(run_field "$TCP_ID" "$TOKEN" latency_ms)"
[ "$TCP_OK" = "true" ] && pass "tcp 立即执行成功（延迟 ${TCP_LAT}ms）" || fail "tcp 立即执行失败（ok=${TCP_OK:-空}）"

HTTP_URL="${BASE}/healthz"
HTTP_BODY="$(jq -n --arg u "$HTTP_URL" --arg sid "$SRV_SSH" \
  '{name:"m2-http-healthz",kind:"http",target:$u,interval_s:10,timeout_s:5,params:{expect_status:200},server_ids:[$sid]}')"
HTTP_RESP="$(api POST /api/v1/checks "$HTTP_BODY" "$TOKEN")"
HTTP_ID="$(jget "$HTTP_RESP" '.data.id')"
[ "$(run_field "$HTTP_ID" "$TOKEN" ok)" = "true" ] && pass "http 检测项对 /healthz 执行成功" || fail "http 检测项执行失败"

BAD_URL="${BASE}/api/v1/definitely-not-here"
HTTP_BAD_BODY="$(jq -n --arg u "$BAD_URL" --arg sid "$SRV_SSH" \
  '{name:"m2-http-404",kind:"http",target:$u,interval_s:10,timeout_s:5,params:{expect_status:200},server_ids:[$sid]}')"
HTTP_BAD_RESP="$(api POST /api/v1/checks "$HTTP_BAD_BODY" "$TOKEN")"
HTTP_BAD_ID="$(jget "$HTTP_BAD_RESP" '.data.id')"
if [ "$(run_field "$HTTP_BAD_ID" "$TOKEN" ok)" = "false" ]; then
  pass "http 期望 200 但实际 404 → ok=false（消息：$(run_field "$HTTP_BAD_ID" "$TOKEN" message)）"
else
  fail "http 状态码断言失效（404 竟判成功）"
fi

# ---------------------------------------------------------------- 5. 失败路径与 down 状态
step "5. 不可达目标 → ok=false 且连续失败后变 down"
DOWN_BODY="$(jq -n --arg sid "$SRV_SSH" \
  '{name:"m2-tcp-dead",kind:"tcp",target:"127.0.0.1:1",interval_s:10,timeout_s:3,params:{fail_threshold:3},server_ids:[$sid]}')"
DOWN_RESP="$(api POST /api/v1/checks "$DOWN_BODY" "$TOKEN")"
DOWN_ID="$(jget "$DOWN_RESP" '.data.id')"
DEAD_OK="$(run_field "$DOWN_ID" "$TOKEN" ok)"
DEAD_MSG="$(run_field "$DOWN_ID" "$TOKEN" message)"
if [ "$DEAD_OK" = "false" ] && [ -n "$DEAD_MSG" ]; then
  pass "不可达目标 ok=false，原因：${DEAD_MSG}"
else
  fail "不可达目标判定异常（ok=${DEAD_OK:-空} message=${DEAD_MSG:-空}）"
fi

DOWN_SEEN=0
for _ in $(seq 1 90); do
  [ "$(check_status "$DOWN_ID" "$TOKEN")" = "down" ] && { DOWN_SEEN=1; break; }
  sleep 1
done
[ "$DOWN_SEEN" = "1" ] && pass "连续失败后状态变为 down（含调度器自动执行）" || fail "90 秒内状态未变为 down（当前：$(check_status "$DOWN_ID" "$TOKEN")）"

# ---------------------------------------------------------------- 6. 调度器产率
step "6. 调度器按间隔产生结果"
RESULTS="$(api GET "/api/v1/checks/${DOWN_ID}/results?limit=100" "" "$TOKEN")"
RCOUNT="$(printf '%s' "$RESULTS" | jq -r '.data | length' 2>/dev/null)"
if [ "${RCOUNT:-0}" -ge 3 ] 2>/dev/null; then
  pass "单目标已产生 ${RCOUNT} 条结果（间隔 10s）"
else
  fail "结果条数不足：${RCOUNT:-0}"
fi

# ---------------------------------------------------------------- 7. process 检测（两条通道）
step "7. process 检测：SSH 通道与 agent(job.exec) 通道"
PROC_SSH_BODY="$(jq -n --arg sid "$SRV_SSH" \
  '{name:"m2-proc-ssh",kind:"process",target:"leadstack",interval_s:30,timeout_s:10,params:{min_count:1},server_ids:[$sid]}')"
PROC_SSH_RESP="$(api POST /api/v1/checks "$PROC_SSH_BODY" "$TOKEN")"
PROC_SSH_ID="$(jget "$PROC_SSH_RESP" '.data.id')"
if [ "$(run_field "$PROC_SSH_ID" "$TOKEN" ok)" = "true" ]; then
  pass "process 检测经 SSH 通道执行成功"
else
  fail "process 检测经 SSH 通道失败：$(run_field "$PROC_SSH_ID" "$TOKEN" message)"
fi

if [ -n "$SRV_AGENT" ]; then
  PROC_AG_BODY="$(jq -n --arg sid "$SRV_AGENT" \
    '{name:"m2-proc-agent",kind:"process",target:"leadstack",interval_s:30,timeout_s:15,params:{min_count:1},server_ids:[$sid]}')"
  PROC_AG_RESP="$(api POST /api/v1/checks "$PROC_AG_BODY" "$TOKEN")"
  PROC_AG_ID="$(jget "$PROC_AG_RESP" '.data.id')"
  if [ "$(run_field "$PROC_AG_ID" "$TOKEN" ok)" = "true" ]; then
    pass "process 检测经 agent(job.exec) 通道执行成功"
  else
    fail "process 检测经 agent 通道失败：$(run_field "$PROC_AG_ID" "$TOKEN" message)"
  fi
fi

# ---------------------------------------------------------------- 8. 查询接口
step "8. 结果时间线与服务器维度视图"
TL="$(api GET "/api/v1/checks/${DOWN_ID}/results?limit=10" "" "$TOKEN")"
TL_N="$(printf '%s' "$TL" | jq -r '.data | length' 2>/dev/null)"
TS_FIELD="$(printf '%s' "$TL" | jq -r '.data[0].ts // empty' 2>/dev/null)"
if [ "${TL_N:-0}" -gt 0 ] 2>/dev/null && [ -n "$TS_FIELD" ]; then
  pass "结果时间线返回 ${TL_N} 条（含 ts 字段）"
else
  fail "结果时间线异常：$TL"
fi

SC="$(api GET "/api/v1/servers/${SRV_SSH}/checks" "" "$TOKEN")"
SC_N="$(printf '%s' "$SC" | jq -r '.data | length' 2>/dev/null)"
[ "${SC_N:-0}" -ge 3 ] 2>/dev/null && pass "服务器维度检测视图返回 ${SC_N} 个检测项" || fail "服务器维度检测视图异常：$SC"

SUMMARY="$(api GET /api/v1/checks/summary "" "$TOKEN")"
if printf '%s' "$SUMMARY" | jq -e '.data.total >= 1 and (.data.down >= 1)' >/dev/null 2>&1; then
  pass "检测概览：$(printf '%s' "$SUMMARY" | jq -c '.data | {total,up,down,unknown}')"
else
  fail "检测概览异常：$SUMMARY"
fi

# ---------------------------------------------------------------- 9. 指标大范围降级
step "9. 指标查询降级（step=raw 且跨度 > 6h → 1m）"
TO="$(now_ms)"; FROM=$((TO - 86400000))
BIG="$(api GET "/api/v1/servers/${SRV_SSH}/metrics?from=${FROM}&to=${TO}&step=raw" "" "$TOKEN")"
STEP_BACK="$(printf '%s' "$BIG" | jq -r '.data.step // empty' 2>/dev/null)"
[ "$STEP_BACK" = "1m" ] && pass "24 小时跨度请求 raw 被降级为 1m" || fail "降级未生效（step=${STEP_BACK:-空}）"
TO2="$(now_ms)"; FROM2=$((TO2 - 3600000))
SMALL="$(api GET "/api/v1/servers/${SRV_SSH}/metrics?from=${FROM2}&to=${TO2}&step=raw" "" "$TOKEN")"
STEP_KEEP="$(printf '%s' "$SMALL" | jq -r '.data.step // empty' 2>/dev/null)"
[ "$STEP_KEEP" = "raw" ] && pass "1 小时跨度仍返回 raw（未误伤）" || fail "小跨度被错误降级（step=${STEP_KEEP:-空}）"

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32mM2 验收通过\033[0m\n'; exit 0; fi
printf '\033[31mM2 验收未通过\033[0m\n'
exit 1
