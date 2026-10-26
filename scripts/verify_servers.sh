#!/usr/bin/env bash
# LeadStack 纳管 端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 12 节 服务器纳管验收标准。
# 用法：make build && make devssh && bash scripts/verify_servers.sh
#
# 本机没有 sshd，SSH 路径使用 bin/leadstack-devssh（DEV-ONLY 测试端点）驱动。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18788}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m1-pass"
DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-m1.XXXXXX")"
KEY_FILE="$DATA_DIR/id_ed25519"
SSH_PORT_FILE="$DATA_DIR/ssh.port"
LEAD_PID=""; DEVSSH_PID=""; AGENT_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
note() { printf '    %s\n' "$1"; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  [ -n "$DEVSSH_PID" ] && kill "$DEVSSH_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT

api() { # api METHOD PATH [JSON_BODY] [TOKEN]
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
status_of() { curl -sS -o /dev/null -w '%{http_code}' "$@" 2>/dev/null; }
# jget <json> <jq-filter>：取不到时返回空串，不报错
jget() { printf '%s' "$1" | jq -r "$2 // empty" 2>/dev/null; }

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 1. 构建产物
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-agent bin/leadstack-devssh; do
  if [ -x "$bin" ]; then pass "$bin 存在"; else fail "$bin 缺失（先 make build && make devssh）"; fi
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动依赖
step "2. 启动测试 SSH 端点与控制端"
ssh-keygen -q -t ed25519 -N '' -f "$KEY_FILE" >/dev/null 2>&1 \
  && pass "生成测试 SSH 密钥对" || fail "ssh-keygen 失败"

./bin/leadstack-devssh --addr 127.0.0.1:0 --user devuser --password devpass \
  --authorized-key-file "$KEY_FILE.pub" --port-file "$SSH_PORT_FILE" \
  > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!

SSH_PORT=""
for _ in $(seq 1 40); do
  SSH_PORT="$(cat "$SSH_PORT_FILE" 2>/dev/null)"
  [ -n "$SSH_PORT" ] && break
  sleep 0.25
done
if [ -n "$SSH_PORT" ]; then
  pass "devssh 已监听 127.0.0.1:${SSH_PORT}"
else
  fail "devssh 未在 10 秒内就绪"; tail -10 "$DATA_DIR/devssh.log" | sed 's/^/      /'; exit 1
fi

LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="$DATA_DIR" \
LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
LEADSTACK_SSH_INTERVAL="3s" \
  ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
UP=0
for _ in $(seq 1 60); do
  [ "$(status_of "${BASE}/healthz")" = "200" ] && { UP=1; break; }
  sleep 0.3
done
[ "$UP" = "1" ] && pass "控制端已就绪（SSH 采集间隔压到 3s 便于验收）" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'; exit 1; }

# ---------------------------------------------------------------- 3. 鉴权
step "3. 鉴权"
LOGIN="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(jget "$LOGIN" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败：$LOGIN"; exit 1; }
[ "$(status_of "${BASE}/api/v1/credentials")" = "401" ] && pass "无 token 访问 /credentials 返回 401" || fail "无 token 访问 /credentials 未返回 401"

# ---------------------------------------------------------------- 4. 凭证
step "4. 凭证管理"
PRIV_PEM="$(cat "$KEY_FILE")"
KEY_BODY="$(jq -n --arg name "verify-key" --arg user "devuser" --arg key "$PRIV_PEM" \
  '{name:$name,kind:"key",username:$user,private_key:$key}')"
CRED_KEY="$(api POST /api/v1/credentials "$KEY_BODY" "$TOKEN")"
CRED_KEY_ID="$(jget "$CRED_KEY" '.data.id')"
[ -n "$CRED_KEY_ID" ] && pass "创建私钥凭证成功" || fail "创建私钥凭证失败：$CRED_KEY"

CRED_PW="$(api POST /api/v1/credentials '{"name":"verify-password","kind":"password","username":"devuser","password":"devpass"}' "$TOKEN")"
CRED_PW_ID="$(jget "$CRED_PW" '.data.id')"
[ -n "$CRED_PW_ID" ] && pass "创建密码凭证成功" || fail "创建密码凭证失败：$CRED_PW"

CREDS_LIST="$(api GET /api/v1/credentials "" "$TOKEN")"
if printf '%s' "$CREDS_LIST" | grep -q "BEGIN OPENSSH PRIVATE KEY\|BEGIN RSA PRIVATE KEY"; then
  fail "凭证列表泄露了私钥明文"
else
  pass "凭证列表不含私钥明文"
fi
if printf '%s' "$CREDS_LIST" | grep -q "devpass"; then
  fail "凭证列表泄露了密码明文"
else
  pass "凭证列表不含密码明文"
fi

# ---------------------------------------------------------------- 5. SSH 服务器接入
step "5. SSH 服务器接入与连通性测试"
SRV_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg cid "$CRED_KEY_ID" \
  '{name:"verify-ssh-key",host:$host,port:$port,mode:"ssh",credential_id:$cid,group:"verify",tags:["e2e","ssh"]}')"
SRV="$(api POST /api/v1/servers "$SRV_BODY" "$TOKEN")"
SRV_ID="$(jget "$SRV" '.data.id')"
[ -n "$SRV_ID" ] && pass "创建 mode=ssh 服务器成功" || { fail "创建服务器失败：$SRV"; exit 1; }

TC="$(api POST "/api/v1/servers/${SRV_ID}/test-connection" "" "$TOKEN")"
TC_OK="$(jget "$TC" '.data.ok')"
TC_BANNER="$(jget "$TC" '.data.ssh_banner')"
TC_FP="$(jget "$TC" '.data.host_key_fingerprint')"
if [ "$TC_OK" = "true" ]; then
  pass "连通性测试通过（${TC_BANNER:-无 banner}）"
  [ -n "$TC_FP" ] && pass "返回主机密钥指纹：${TC_FP}" || fail "未返回主机密钥指纹"
else
  fail "连通性测试失败：$TC"
fi

# ---------------------------------------------------------------- 6. 主机信息
step "6. 主机信息采集"
HI="$(api POST "/api/v1/servers/${SRV_ID}/refresh-host-info" "" "$TOKEN")"
HI_OBJ="$(printf '%s' "$HI" | jq -c '.data.host_info // .data' 2>/dev/null)"
check_field() {
  local name="$1" val
  val="$(printf '%s' "$HI_OBJ" | jq -r ".${name} // empty" 2>/dev/null)"
  if [ -n "$val" ] && [ "$val" != "0" ] && [ "$val" != "null" ]; then
    pass "host_info.${name} = ${val}"
  else
    fail "host_info.${name} 为空"
  fi
}
for f in hostname os kernel arch cpu_cores mem_total_bytes disk_total_bytes uptime_s; do check_field "$f"; done

# ---------------------------------------------------------------- 7. Agentless 采集
step "7. Agentless 指标采集（间隔已压到 3s）"
METRICS_OK=0
for _ in $(seq 1 60); do
  S="$(api GET /api/v1/servers "" "$TOKEN")"
  N="$(printf '%s' "$S" | jq -r "[.data[] | select(.id==\"${SRV_ID}\") | .metrics_summary // {} | length] | .[0] // 0" 2>/dev/null)"
  if [ "${N:-0}" -ge 3 ] 2>/dev/null; then METRICS_OK=1; break; fi
  sleep 1
done
if [ "$METRICS_OK" = "1" ]; then
  pass "agentless 指标已落库（≥3 个指标）"
  printf '%s' "$S" | jq -c ".data[] | select(.id==\"${SRV_ID}\") | {status,last_error,metrics_summary}" | sed 's/^/    /'
else
  fail "60 秒内未采到足够指标"
  printf '%s' "$S" | jq -c ".data[] | select(.id==\"${SRV_ID}\")" | sed 's/^/    /'
  tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'
fi

# ---------------------------------------------------------------- 8. 指标历史
step "8. 指标历史接口"
NOW="$(($(date +%s) * 1000))"
FROM=$((NOW - 3600000))
MS="$(api GET "/api/v1/servers/${SRV_ID}/metrics?from=${FROM}&to=${NOW}&step=raw" "" "$TOKEN")"
SERIES_LEN="$(printf '%s' "$MS" | jq -r '.data.series | length' 2>/dev/null)"
if [ "${SERIES_LEN:-0}" -gt 0 ] 2>/dev/null; then
  pass "step=raw 返回 ${SERIES_LEN} 条时间序列"
  printf '%s' "$MS" | jq -c '.data.series[0] | {metric,points:(.points|length)}' | sed 's/^/    /'
else
  fail "step=raw 未返回时间序列：$MS"
fi
MS1M="$(api GET "/api/v1/servers/${SRV_ID}/metrics?from=${FROM}&to=${NOW}&step=1m" "" "$TOKEN")"
CODE_STEP="$(printf '%s' "$MS1M" | jq -r '.data.step // empty' 2>/dev/null)"
[ "$CODE_STEP" = "1m" ] && pass "step=1m 接口可用（降采样后允许为空）" || fail "step=1m 接口异常：$MS1M"

# ---------------------------------------------------------------- 9. 批量操作
step "9. 批量操作"
SRV2_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg cid "$CRED_PW_ID" \
  '{name:"verify-ssh-password",host:$host,port:$port,mode:"ssh",credential_id:$cid,group:"verify"}')"
SRV2="$(api POST /api/v1/servers "$SRV2_BODY" "$TOKEN")"
SRV2_ID="$(jget "$SRV2" '.data.id')"
[ -n "$SRV2_ID" ] && pass "创建第二台 mode=ssh 服务器（密码认证）" || fail "创建第二台服务器失败：$SRV2"

BATCH="$(api POST /api/v1/servers/batch \
  "$(jq -n --arg a "$SRV_ID" --arg b "$SRV2_ID" '{ids:[$a,$b],action:"test-connection"}')" "$TOKEN")"
B_RESULTS="$(printf '%s' "$BATCH" | jq -r '.data.results | length' 2>/dev/null)"
B_OK="$(printf '%s' "$BATCH" | jq -r '.data.summary.ok // 0' 2>/dev/null)"
if [ "${B_RESULTS:-0}" -ge 2 ] 2>/dev/null && [ "${B_OK:-0}" -ge 2 ] 2>/dev/null; then
  pass "批量 test-connection：${B_RESULTS} 台逐台返回，成功 ${B_OK} 台"
else
  fail "批量 test-connection 结果异常：$BATCH"
fi

TAGGED="$(api POST /api/v1/servers/batch \
  "$(jq -n --arg a "$SRV_ID" --arg b "$SRV2_ID" '{ids:[$a,$b],action:"add-tags",params:{tags:["batch"]}}')" "$TOKEN")"
if printf '%s' "$TAGGED" | jq -r '.data.summary.ok // 0' 2>/dev/null | grep -q '^2$'; then
  pass "批量 add-tags 成功 2 台"
else
  fail "批量 add-tags 异常：$TAGGED"
fi

# ---------------------------------------------------------------- 10. 分组过滤
step "10. 分组过滤"
# 注意：不能用 GROUPS —— bash 里它是只读的「当前用户组 ID」特殊数组变量（bash 3.2 下赋值直接失败）
GROUPS_JSON="$(api GET /api/v1/groups "" "$TOKEN")"
printf '%s' "$GROUPS_JSON" | jq -e '.data | length > 0' >/dev/null 2>&1 \
  && pass "GET /groups 返回分组聚合" || fail "GET /groups 异常：$GROUPS_JSON"
FILTERED="$(api GET "/api/v1/servers?group=verify" "" "$TOKEN")"
F_COUNT="$(printf '%s' "$FILTERED" | jq -r '.data | length' 2>/dev/null)"
[ "${F_COUNT:-0}" -ge 2 ] 2>/dev/null && pass "?group=verify 过滤命中 ${F_COUNT} 台" || fail "分组过滤异常：$FILTERED"

# ---------------------------------------------------------------- 11. 凭证引用保护
step "11. 凭证引用保护"
CODE_DEL="$(status_of -X DELETE -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/credentials/${CRED_KEY_ID}")"
[ "$CODE_DEL" = "409" ] && pass "删除被引用的凭证返回 409" || fail "删除被引用凭证返回 ${CODE_DEL}，期望 409"

# ---------------------------------------------------------------- 12. agent 模式主机信息
step "12. agent 模式上报 host_info"
ETOKEN_JSON="$(api POST /api/v1/enroll-tokens '{"note":"m1-e2e","ttl_s":900}' "$TOKEN")"
ETOKEN="$(jget "$ETOKEN_JSON" '.data.token')"
if [ -n "$ETOKEN" ]; then
  ./bin/leadstack-agent --server "$BASE" --token "$ETOKEN" > "$DATA_DIR/agent.log" 2>&1 &
  AGENT_PID=$!
  AGENT_HI=0
  for _ in $(seq 1 40); do
    S="$(api GET /api/v1/servers "" "$TOKEN")"
    if printf '%s' "$S" | jq -e '[.data[] | select(.mode=="agent" and (.host_info != null))] | length > 0' >/dev/null 2>&1; then
      AGENT_HI=1; break
    fi
    sleep 0.5
  done
  if [ "$AGENT_HI" = "1" ]; then
    pass "agent 模式服务器已上报 host_info"
    printf '%s' "$S" | jq -c '[.data[] | select(.mode=="agent")][0] | {name,status,host_info}' | sed 's/^/    /'
  else
    fail "agent 未在 20 秒内上报 host_info"
    printf '%s' "$S" | jq -c '[.data[] | select(.mode=="agent")][0]' | sed 's/^/    /'
    tail -15 "$DATA_DIR/agent.log" | sed 's/^/      /'
  fi
else
  fail "创建接入令牌失败：$ETOKEN_JSON"
fi

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m服务器纳管 验收通过\033[0m\n'; exit 0; fi
printf '\033[31m服务器纳管 验收未通过\033[0m\n'
exit 1
