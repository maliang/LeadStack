#!/usr/bin/env bash
# LeadStack 告警 端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 22 节 告警验收标准。
# 用法：make build && make devssh && bash scripts/verify_alerts.sh
#
# 写法约束：不要用 `jq '... // empty'` 取布尔字段（会吞掉 false）；
# 不要把带 \" 的 JSON 嵌进两层 "$( )"（bash 3.2 会拆参）。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18798}"
RECV_PORT=$((PORT + 1))
SRV_PORT=$((PORT + 2))
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m3-pass"
DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-m3.XXXXXX")"
RECV_LOG="$DATA_DIR/webhook.log"
LEAD_PID=""; DEVSSH_PID=""; RECV_PID=""; SRV_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$SRV_PID" "$RECV_PID" "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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
jget() { printf '%s' "$1" | jq -r "$2" 2>/dev/null; }
# 取字符串字段（null → 空串）
sget() { local v; v="$(jget "$1" "$2")"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }
recv_count() { [ -f "$RECV_LOG" ] && wc -l < "$RECV_LOG" | tr -d ' ' || echo 0; }
recv_has() { [ -f "$RECV_LOG" ] && grep -q "$1" "$RECV_LOG" && echo yes || echo no; }

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 1. 构建产物
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-devssh; do
  [ -x "$bin" ] && pass "$bin 存在" || fail "$bin 缺失"
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动环境
step "2. 启动 webhook 接收器 / 测试 TCP 服务 / devssh / 控制端"

# 2.1 webhook 接收器：把每次 POST 追加到 RECV_LOG
cat > "$DATA_DIR/recv.py" <<'PY'
import http.server, socketserver, sys, json
out, port = sys.argv[1], int(sys.argv[2])
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0) or 0)
        body = self.rfile.read(n).decode('utf-8', 'replace')
        with open(out, 'a') as f:
            f.write(json.dumps({"path": self.path, "body": body}, ensure_ascii=False) + "\n")
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers()
        self.wfile.write(b'{"ok":true}')
    def log_message(self, *a): pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", port), H) as httpd:
    httpd.serve_forever()
PY
: > "$RECV_LOG"
python3 "$DATA_DIR/recv.py" "$RECV_LOG" "$RECV_PORT" > "$DATA_DIR/recv.out" 2>&1 &
RECV_PID=$!
RECV_UP=0
for _ in $(seq 1 30); do curl -s -o /dev/null -X POST -d '{}' "http://127.0.0.1:${RECV_PORT}/ping" && { RECV_UP=1; break; }; sleep 0.2; done
[ "$RECV_UP" = "1" ] && pass "webhook 接收器已监听 127.0.0.1:${RECV_PORT}" || { fail "接收器未就绪"; exit 1; }
: > "$RECV_LOG"   # 清掉自检请求

# 2.2 一个可控的 TCP 服务（先起着，稍后杀掉制造故障）
cat > "$DATA_DIR/srv.py" <<'PY'
import socket, sys
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', int(sys.argv[1]))); s.listen(64)
while True:
    try:
        c, _ = s.accept(); c.close()
    except OSError:
        break
PY
python3 "$DATA_DIR/srv.py" "$SRV_PORT" > "$DATA_DIR/srv.out" 2>&1 &
SRV_PID=$!
SRV_UP=0
for _ in $(seq 1 30); do nc -z 127.0.0.1 "$SRV_PORT" 2>/dev/null && { SRV_UP=1; break; }; sleep 0.2; done
[ "$SRV_UP" = "1" ] && pass "测试 TCP 服务已监听 127.0.0.1:${SRV_PORT}（用于制造故障与恢复）" || { fail "测试 TCP 服务未就绪"; exit 1; }

# 2.3 devssh（作为检测项的挂载目标机器）
ssh-keygen -q -t ed25519 -N '' -f "$DATA_DIR/id" >/dev/null 2>&1
./bin/leadstack-devssh --addr 127.0.0.1:0 --user devuser --password devpass \
  --authorized-key-file "$DATA_DIR/id.pub" --port-file "$DATA_DIR/ssh.port" \
  > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SSH_PORT=""
for _ in $(seq 1 40); do SSH_PORT="$(cat "$DATA_DIR/ssh.port" 2>/dev/null)"; [ -n "$SSH_PORT" ] && break; sleep 0.25; done
[ -n "$SSH_PORT" ] && pass "devssh 监听 127.0.0.1:${SSH_PORT}" || { fail "devssh 未就绪"; exit 1; }

# 2.4 控制端（把告警评估与采集间隔都压短，便于验收）
LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="$DATA_DIR" \
LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
LEADSTACK_SSH_INTERVAL="5s" \
LEADSTACK_CHECK_INTERVAL_MIN="5s" \
LEADSTACK_ALERT_EVAL_INTERVAL="3s" \
  ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
UP=0
for _ in $(seq 1 60); do [ "$(status_of "${BASE}/healthz")" = "200" ] && { UP=1; break; }; sleep 0.3; done
[ "$UP" = "1" ] && pass "控制端就绪（告警评估 3s / 采集 5s）" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'; exit 1; }

# ---------------------------------------------------------------- 3. 鉴权与目标机器
step "3. 鉴权与准备目标机器"
LOGIN_RESP="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败：$LOGIN_RESP"; exit 1; }
[ "$(status_of "${BASE}/api/v1/alert-rules")" = "401" ] && pass "无 token 访问 /alert-rules 返回 401" || fail "无 token 访问 /alert-rules 未返回 401"

CRED_RESP="$(api POST /api/v1/credentials \
  "$(jq -n --arg k "$(cat "$DATA_DIR/id")" '{name:"m3-key",kind:"key",username:"devuser",private_key:$k}')" "$TOKEN")"
CRED_ID="$(sget "$CRED_RESP" '.data.id')"
SRV_RESP="$(api POST /api/v1/servers \
  "$(jq -n --argjson port "$SSH_PORT" --arg cid "$CRED_ID" \
     '{name:"m3-node",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid,group:"m3"}')" "$TOKEN")"
SRV_ID="$(sget "$SRV_RESP" '.data.id')"
[ -n "$SRV_ID" ] && pass "SSH 目标机器已创建" || { fail "创建服务器失败：$SRV_RESP"; exit 1; }

# ---------------------------------------------------------------- 4. 渠道
step "4. 通知渠道与测试发送"
CH_RESP="$(api POST /api/v1/alert-channels \
  "$(jq -n --arg url "http://127.0.0.1:${RECV_PORT}/hook" \
     '{name:"m3-webhook",kind:"webhook",enabled:true,config:{url:$url,template:"json"}}')" "$TOKEN")"
CH_ID="$(sget "$CH_RESP" '.data.id')"
[ -n "$CH_ID" ] && pass "webhook 渠道已创建" || { fail "创建渠道失败：$CH_RESP"; exit 1; }

TEST_RESP="$(api POST "/api/v1/alert-channels/${CH_ID}/test" "" "$TOKEN")"
TEST_OK="$(jget "$TEST_RESP" '.data.ok')"
if [ "$TEST_OK" = "true" ] && [ "$(recv_count)" -ge 1 ]; then
  pass "测试发送成功，接收器真实收到 $(recv_count) 条请求"
else
  fail "测试发送失败（ok=${TEST_OK:-空}，接收器收到 $(recv_count) 条）：$TEST_RESP"
fi

SECRET="m3secret-abc123-zzz"
api POST /api/v1/alert-channels \
  "$(jq -n --arg url "http://127.0.0.1:${RECV_PORT}/ding" --arg s "$SECRET" \
     '{name:"m3-dingtalk",kind:"dingtalk",enabled:true,config:{webhook:$url,secret:$s}}')" "$TOKEN" >/dev/null
CH_LIST="$(api GET /api/v1/alert-channels "" "$TOKEN")"
if printf '%s' "$CH_LIST" | grep -q "$SECRET"; then
  fail "渠道列表泄露了 secret 明文"
else
  pass "渠道列表不含 secret 明文"
fi

# ---------------------------------------------------------------- 5. 检测项与规则
step "5. 创建检测项与告警规则"
CHK_RESP="$(api POST /api/v1/checks \
  "$(jq -n --argjson port "$SRV_PORT" --arg sid "$SRV_ID" \
     '{name:"m3-flap",kind:"tcp",target:("127.0.0.1:"+($port|tostring)),interval_s:5,timeout_s:3,params:{fail_threshold:1},server_ids:[$sid]}')" "$TOKEN")"
CHK_ID="$(sget "$CHK_RESP" '.data.id')"
[ -n "$CHK_ID" ] && pass "tcp 检测项已创建（指向可控服务 ${SRV_PORT}）" || { fail "创建检测项失败：$CHK_RESP"; exit 1; }

RULE_NAME="m3-rule-flap"
RULE_RESP="$(api POST /api/v1/alert-rules \
  "$(jq -n --arg n "$RULE_NAME" --arg c "$CHK_ID" --arg ch "$CH_ID" \
     '{name:$n,enabled:true,source:"check",check_ids:[$c],for_duration_s:0,severity:"critical",dedup_window_s:3600,channel_ids:[$ch]}')" "$TOKEN")"
RULE_ID="$(sget "$RULE_RESP" '.data.id')"
[ -n "$RULE_ID" ] && pass "告警规则已创建（source=check，绑定 webhook 渠道）" || { fail "创建规则失败：$RULE_RESP"; exit 1; }

# ---------------------------------------------------------------- 6. 故障 → firing
step "6. 制造故障 → 期望 firing + 真实通知"
# 先等检测项建立基线（up），再杀服务制造翻转
BASELINE=0
for _ in $(seq 1 40); do
  [ "$(api GET /api/v1/checks "" "$TOKEN" | jq -r ".data[] | select(.id==\"$CHK_ID\") | .state" 2>/dev/null)" = "up" ] && { BASELINE=1; break; }
  sleep 1
done
[ "$BASELINE" = "1" ] && pass "检测项基线状态为 up" || fail "检测项未建立 up 基线（状态翻转告警依赖基线）"

kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null; SRV_PID=""
pass "已关闭测试 TCP 服务，制造故障"

FIRING=0
for _ in $(seq 1 60); do
  if api GET "/api/v1/alerts?status=firing" "" "$TOKEN" | jq -e ".data[] | select(.rule_id==\"$RULE_ID\")" >/dev/null 2>&1; then FIRING=1; break; fi
  sleep 1
done
if [ "$FIRING" = "1" ]; then
  pass "告警已触发（status=firing）"
else
  fail "60 秒内未出现 firing 告警"
  api GET /api/v1/alerts "" "$TOKEN" | jq -c '.data' | sed 's/^/    /'
  tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'
fi

NOTIFIED=0
for _ in $(seq 1 30); do [ "$(recv_has "$RULE_NAME")" = "yes" ] && { NOTIFIED=1; break; }; sleep 1; done
[ "$NOTIFIED" = "1" ] && pass "接收器真实收到告警通知（通知体含规则名）" || fail "接收器未收到告警通知（当前 $(recv_count) 条）"

# ---------------------------------------------------------------- 7. 去重
step "7. 去重：同一故障不重复建告警"
sleep 12   # 让调度器再跑两轮
DUP_COUNT="$(api GET "/api/v1/alerts?rule_id=${RULE_ID}" "" "$TOKEN" | jq -r '[.data[] | select(.status=="firing")] | length' 2>/dev/null)"
[ "${DUP_COUNT:-0}" = "1" ] && pass "同一故障仍只有 1 条 firing 告警（去重生效）" || fail "firing 告警条数为 ${DUP_COUNT:-空}，期望 1"

# ---------------------------------------------------------------- 8. 确认
step "8. 人工确认"
ALERT_ID="$(api GET "/api/v1/alerts?rule_id=${RULE_ID}&status=firing" "" "$TOKEN" | jq -r '.data[0].id' 2>/dev/null)"
ACK_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/alerts/${ALERT_ID}/ack")"
ACK_STATUS="$(api GET "/api/v1/alerts/${ALERT_ID}" "" "$TOKEN" | jq -r '.data.status' 2>/dev/null)"
if [ "$ACK_CODE" = "200" ] && [ "$ACK_STATUS" = "acked" ]; then
  pass "确认成功（status=acked）"
else
  fail "确认失败（HTTP ${ACK_CODE}，status=${ACK_STATUS:-空}）"
fi
if api GET "/api/v1/alerts/${ALERT_ID}" "" "$TOKEN" | jq -e '.data.events | length > 0' >/dev/null 2>&1; then
  pass "告警详情含事件轨迹（events）"
else
  fail "告警详情缺少 events"
fi

# ---------------------------------------------------------------- 9. 恢复通知
step "9. 恢复 → resolved + 恢复通知"
BEFORE_RECV="$(recv_count)"
python3 "$DATA_DIR/srv.py" "$SRV_PORT" > "$DATA_DIR/srv2.out" 2>&1 &
SRV_PID=$!
sleep 1
RESOLVED=0
for _ in $(seq 1 60); do
  ST="$(api GET "/api/v1/alerts/${ALERT_ID}" "" "$TOKEN" | jq -r '.data.status' 2>/dev/null)"
  [ "$ST" = "resolved" ] && { RESOLVED=1; break; }
  sleep 1
done
if [ "$RESOLVED" = "1" ]; then
  pass "服务恢复后告警转为 resolved"
else
  fail "60 秒内告警未转为 resolved（当前：${ST:-空}）"
fi
AFTER_RECV="$(recv_count)"
[ "${AFTER_RECV:-0}" -gt "${BEFORE_RECV:-0}" ] 2>/dev/null && pass "接收器收到恢复通知（${BEFORE_RECV} → ${AFTER_RECV}）" || fail "未收到恢复通知（仍为 ${AFTER_RECV:-0} 条）"

# ---------------------------------------------------------------- 10. 指标规则
step "10. 指标阈值规则"
MRULE_RESP="$(api POST /api/v1/alert-rules \
  "$(jq -n --arg ch "$CH_ID" \
     '{name:"m3-rule-metric",enabled:true,source:"metric",metric:"cpu.pct",comparator:">",threshold:0,for_duration_s:0,severity:"warning",dedup_window_s:3600,channel_ids:[$ch]}')" "$TOKEN")"
MRULE_ID="$(sget "$MRULE_RESP" '.data.id')"
if [ -n "$MRULE_ID" ]; then
  pass "指标规则已创建（cpu.pct > 0）"
else
  fail "创建指标规则失败：$MRULE_RESP"
fi
MFIRING=0
for _ in $(seq 1 40); do
  if api GET "/api/v1/alerts?rule_id=${MRULE_ID}" "" "$TOKEN" | jq -e '.data[] | select(.status=="firing")' >/dev/null 2>&1; then MFIRING=1; break; fi
  sleep 1
done
[ "$MFIRING" = "1" ] && pass "指标规则周期评估后产生 firing 告警" || fail "40 秒内指标规则未触发"

# ---------------------------------------------------------------- 11. 静默
step "11. 静默：仍记录但不通知"
SRULE_RESP="$(api POST /api/v1/alert-rules \
  "$(jq -n --arg ch "$CH_ID" \
     '{name:"m3-rule-silent",enabled:true,source:"metric",metric:"mem.used_pct",comparator:">",threshold:0,for_duration_s:0,severity:"info",dedup_window_s:3600,channel_ids:[$ch]}')" "$TOKEN")"
SRULE_ID="$(sget "$SRULE_RESP" '.data.id')"
SIL_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"duration_s":300}' "${BASE}/api/v1/alert-rules/${SRULE_ID}/silence")"
[ "$SIL_CODE" = "200" ] && pass "静默设置成功" || fail "静默设置返回 HTTP ${SIL_CODE}"
SIL_ALERTED=0
for _ in $(seq 1 30); do
  if api GET "/api/v1/alerts?rule_id=${SRULE_ID}" "" "$TOKEN" | jq -e '.data[]' >/dev/null 2>&1; then SIL_ALERTED=1; break; fi
  sleep 1
done
[ "$SIL_ALERTED" = "1" ] && pass "静默期内仍然建立了告警记录" || fail "静默期内未建立告警记录（静默不应阻止记录）"
# 静默只抑制 firing 通知；恢复通知会穿透静默（产品裁决）。
# 因此这里必须精确判定「没有 firing 通知」，而不是「没有提到该规则名的任何请求」。
# 本用例用 mem.used_pct > 0（持续成立、不会恢复），正常情况下不应有恢复通知；
# 万一有，也按 recovered 字段排除，避免把恢复通知误判成静默失效。
SIL_FIRING_NOTIFY=0
if [ -f "$RECV_LOG" ]; then
  SIL_FIRING_NOTIFY="$(grep 'm3-rule-silent' "$RECV_LOG" 2>/dev/null | grep -c '"recovered": *false' || true)"
fi
[ "${SIL_FIRING_NOTIFY:-0}" = "0" ] && pass "静默期内没有发送 firing 通知" || fail "静默期内仍发送了 ${SIL_FIRING_NOTIFY} 条 firing 通知"

# ---------------------------------------------------------------- 12. 概览
step "12. 告警概览"
SUMMARY="$(api GET /api/v1/alerts/summary "" "$TOKEN")"
if printf '%s' "$SUMMARY" | jq -e '.data.firing >= 1 and (.data.by_severity != null)' >/dev/null 2>&1; then
  pass "概览：$(printf '%s' "$SUMMARY" | jq -c '.data | {firing,acked,resolved_24h,by_severity}')"
else
  fail "概览异常：$SUMMARY"
fi

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32mM3 验收通过\033[0m\n'; exit 0; fi
printf '\033[31mM3 验收未通过\033[0m\n'
exit 1
