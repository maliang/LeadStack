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
# 前端产物断言：make build 会把 web/dist 拷进 internal/webui/dist，缺特征串即视为契约回归。
dist_has() { # <label> <needle...>
  local label="$1"; shift
  local js missing="" needle
  js="$(ls -t internal/webui/dist/assets/index-*.js 2>/dev/null | head -1)"
  if [ -z "$js" ]; then fail "${label}：未找到前端产物（请先 make build）"; return; fi
  for needle in "$@"; do grep -qF -- "$needle" "$js" || missing="${missing}[${needle}] "; done
  if [ -z "$missing" ]; then
    pass "${label}（${js##*/} 命中 $# 个特征串）"
  else
    fail "${label}：产物缺少 ${missing}"
  fi
}

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

# ---------------------------------------------------------------- 13. Agent 升级入口
step "13. Agent 升级入口（A30）"
# A30 前端：服务器页要能对 mode=agent 的机器发起升级（预填当前平台归档），
# 无 agent.upgrade 权限时按钮禁用并给出只读说明（title 即说明文本）。
dist_has "升级 Agent 入口与无权限说明（A30）" \
  'server.agentUpgrade' 'server.agentUpgradeSubmit' 'server.agentUpgradeNoPerm' \
  'agent_assets' 'refreshUpdateCheck().then('

# ---------------------------------------------------------------- 14. 服务器登录信息
step "14. 服务器登录信息回显（GET /servers/{id}/access）"
# access 是唯一回显 SSH 明文密码的接口：字段必须齐、密码必须与创建时逐字相等、
# 且不得出现在服务器列表里；未绑凭证必须给原因；组范围角色读组外服务器必须 404。
PW_KEY="password"
ACCESS_PW="QaAccess-Pw-2026"
ACCESS_CRED_BODY="$(jq -n --arg name "verify-access" --arg pw "$ACCESS_PW" \
  '{name:$name,kind:"'"$PW_KEY"'",username:"root","'"$PW_KEY"'":$pw}')"
ACCESS_CRED="$(api POST /api/v1/credentials "$ACCESS_CRED_BODY" "$TOKEN")"
ACCESS_CRED_ID="$(jget "$ACCESS_CRED" '.data.id')"
[ -n "$ACCESS_CRED_ID" ] && pass "创建 access 验收用密码凭证" || fail "创建 access 凭证失败：$ACCESS_CRED"

ACCESS_SRV_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg cid "$ACCESS_CRED_ID" \
  '{name:"verify-access-srv",host:$host,port:$port,mode:"ssh",credential_id:$cid}')"
ACCESS_SRV="$(api POST /api/v1/servers "$ACCESS_SRV_BODY" "$TOKEN")"
ACCESS_SRV_ID="$(jget "$ACCESS_SRV" '.data.id')"
[ -n "$ACCESS_SRV_ID" ] && pass "创建 access 验收用服务器" || fail "创建 access 服务器失败：$ACCESS_SRV"

ACCESS_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${ACCESS_SRV_ID}/access")"
ACCESS="$(api GET "/api/v1/servers/${ACCESS_SRV_ID}/access" "" "$TOKEN")"
[ "$ACCESS_CODE" = "200" ] && pass "管理员 GET /servers/{id}/access 返回 200" || fail "GET access 返回 ${ACCESS_CODE}：$ACCESS"

ACCESS_MISSING=""
for f in server_id name host port username auth_type "$PW_KEY"; do
  v="$(printf '%s' "$ACCESS" | jq -r --arg f "$f" '.data[$f] // empty' 2>/dev/null)"
  [ -n "$v" ] || ACCESS_MISSING="${ACCESS_MISSING}${f} "
done
[ -z "$ACCESS_MISSING" ] && pass "登录信息字段齐全（server_id/name/host/port/username/password/auth_type）" \
  || fail "登录信息缺字段：${ACCESS_MISSING}"

ACCESS_GOT_PW="$(printf '%s' "$ACCESS" | jq -jr --arg f "$PW_KEY" '.data[$f] // empty' 2>/dev/null)"
if [ -n "$ACCESS_GOT_PW" ] && [ "$ACCESS_GOT_PW" = "$ACCESS_PW" ]; then
  pass "登录信息里的密码与创建时逐字相等（${#ACCESS_GOT_PW} 字节）"
else
  fail "密码不一致（期望 ${#ACCESS_PW} 字节，实际 ${#ACCESS_GOT_PW} 字节）"
fi

ACCESS_HOST="$(printf '%s' "$ACCESS" | jq -r '.data.host // empty' 2>/dev/null)"
ACCESS_PORT="$(printf '%s' "$ACCESS" | jq -r '.data.port // empty' 2>/dev/null)"
ACCESS_USER="$(printf '%s' "$ACCESS" | jq -r '.data.username // empty' 2>/dev/null)"
ACCESS_AUTH="$(printf '%s' "$ACCESS" | jq -r '.data.auth_type // empty' 2>/dev/null)"
[ "$ACCESS_HOST" = "127.0.0.1" ] && [ "$ACCESS_PORT" = "$SSH_PORT" ] \
  && pass "地址/端口与创建一致（${ACCESS_HOST}:${ACCESS_PORT}）" \
  || fail "地址/端口不符：${ACCESS_HOST}:${ACCESS_PORT}，期望 127.0.0.1:${SSH_PORT}"
[ "$ACCESS_USER" = "root" ] && [ "$ACCESS_AUTH" = "$PW_KEY" ] \
  && pass "用户名/auth_type 正确（${ACCESS_USER}/${ACCESS_AUTH}）" \
  || fail "用户名或 auth_type 不符：${ACCESS_USER}/${ACCESS_AUTH}，期望 root/${PW_KEY}"

SERVERS_AFTER_ACCESS="$(api GET /api/v1/servers "" "$TOKEN")"
if printf '%s' "$SERVERS_AFTER_ACCESS" | grep -qF -- "$ACCESS_PW"; then
  fail "GET /servers 列表泄露了登录密码明文"
else
  pass "GET /servers 列表不含登录密码明文"
fi
LIST_HAS_PW="$(printf '%s' "$SERVERS_AFTER_ACCESS" | jq -r --arg id "$ACCESS_SRV_ID" --arg k "$PW_KEY" \
  '([.data[] | select(.id==$id) | has($k)] | .[0]) // false' 2>/dev/null)"
[ "$LIST_HAS_PW" = "false" ] && pass "服务器列表响应没有 password 字段" \
  || fail "服务器列表出现了 password 字段（${LIST_HAS_PW}）"

NOCRED_SRV="$(api POST /api/v1/servers "$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" \
  '{name:"verify-no-cred-access",host:$host,port:$port,mode:"ssh"}')" "$TOKEN")"
NOCRED_SRV_ID="$(jget "$NOCRED_SRV" '.data.id')"
NOCRED_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${NOCRED_SRV_ID}/access")"
NOCRED="$(api GET "/api/v1/servers/${NOCRED_SRV_ID}/access" "" "$TOKEN")"
NOCRED_AUTH="$(printf '%s' "$NOCRED" | jq -r '.data.auth_type // empty' 2>/dev/null)"
NOCRED_MSG="$(printf '%s' "$NOCRED" | jq -r '.error.message // empty' 2>/dev/null)"
if [ "$NOCRED_CODE" = "409" ] && [ -z "$NOCRED_AUTH" ]; then
  pass "未绑凭证时无 auth_type（HTTP ${NOCRED_CODE}）"
else
  fail "未绑凭证返回 ${NOCRED_CODE} 且 auth_type='${NOCRED_AUTH}'，期望 409 且为空"
fi
if [ -n "$NOCRED_MSG" ] && printf '%s' "$NOCRED_MSG" | grep -q '凭证'; then
  pass "未绑凭证的 message 给出了原因（${NOCRED_MSG}）"
else
  fail "未绑凭证缺少原因说明：message='${NOCRED_MSG}'"
fi

ACCESS_GRP="$(api POST /api/v1/server-groups '{"name":"verify-access-scope"}' "$TOKEN")"
ACCESS_GRP_ID="$(jget "$ACCESS_GRP" '.data.id')"
ACCESS_ROLE_KEY=""; ACCESS_ROLE=""; VLOGIN=""
if [ -n "$ACCESS_GRP_ID" ]; then
  api PUT "/api/v1/server-groups/${ACCESS_GRP_ID}/servers" \
    "$(jq -n --arg s "$ACCESS_SRV_ID" '{server_ids:[$s]}')" "$TOKEN" >/dev/null
  ACCESS_ROLE="$(api POST /api/v1/roles "$(jq -n --arg g "$ACCESS_GRP_ID" \
    '{name:"verify-access-reader",permissions:["server.read"],scope:{mode:"groups",group_ids:[$g],server_ids:[]}}')" "$TOKEN")"
  ACCESS_ROLE_KEY="$(jget "$ACCESS_ROLE" '.data.key')"
fi
ACCESS_VIEW_PW="QaView-Pw-2026"
VTOKEN=""
if [ -n "$ACCESS_ROLE_KEY" ]; then
  api POST /api/v1/users "$(jq -n --arg r "$ACCESS_ROLE_KEY" --arg pw "$ACCESS_VIEW_PW" \
    '{username:"verify-access-viewer",role:$r,"'"$PW_KEY"'":$pw}')" "$TOKEN" >/dev/null
  VLOGIN="$(api POST /api/v1/auth/login "$(jq -n --arg pw "$ACCESS_VIEW_PW" \
    '{username:"verify-access-viewer","'"$PW_KEY"'":$pw}')")"
  VTOKEN="$(jget "$VLOGIN" '.data.token')"
fi
if [ -n "$VTOKEN" ]; then
  V_IN="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/servers/${ACCESS_SRV_ID}/access")"
  V_OUT="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/servers/${SRV_ID}/access")"
  [ "$V_IN" = "200" ] && pass "组范围角色读组内服务器 200（正向对照）" \
    || fail "组内服务器返回 ${V_IN}，期望 200（否则 404 断言失去意义）"
  [ "$V_OUT" = "404" ] && pass "组范围角色读组外服务器返回 404（不泄露存在性）" \
    || fail "组外服务器返回 ${V_OUT}，期望 404"
else
  fail "创建组范围角色/用户并登录失败：group=${ACCESS_GRP} role=${ACCESS_ROLE} login=${VLOGIN}"
fi

AUDIT_LOGS="$(api GET "/api/v1/audit-logs?action=server.access" "" "$TOKEN")"
AUDIT_ACCESS_N="$(printf '%s' "$AUDIT_LOGS" | jq -r --arg id "$ACCESS_SRV_ID" \
  '[.data[] | select(.action=="server.access" and .resource_id==$id)] | length' 2>/dev/null)"
[ "${AUDIT_ACCESS_N:-0}" -ge 1 ] 2>/dev/null \
  && pass "GET /audit-logs 含有 server.access 记录（${AUDIT_ACCESS_N} 条）" \
  || fail "审计缺少 server.access：$AUDIT_LOGS"
AUDIT_LEAK="$(printf '%s' "$AUDIT_LOGS" | jq -r --arg id "$ACCESS_SRV_ID" --arg pw "$ACCESS_PW" \
  '[.data[] | select(.action=="server.access" and .resource_id==$id) | ((.detail // "") | contains($pw))] | any' 2>/dev/null)"
[ "$AUDIT_LEAK" = "false" ] && pass "server.access 审计 detail 不含密码明文" \
  || fail "server.access 审计 detail 泄露了密码（jq=${AUDIT_LEAK:-解析失败}）"

# ---------------------------------------------------------------- 15. 登录脚本（一键登录）
step "15. 登录脚本：POST /servers/{id}/login 的三种返回形态与失败边界"

# 登录脚本会在目标机上真执行，所以这里用一套 devssh 认可的凭证（devuser/devpass），
# 而不是复用第 14 节那个「只为展示登录信息」的 root 凭证——否则脚本一定认证失败。
LOGIN_CRED="$(api POST /api/v1/credentials "$(jq -n --arg name "verify-login" --arg pw "devpass" --arg u "devuser" \
  '{name:$name,kind:"'"$PW_KEY"'",username:$u,"'"$PW_KEY"'":$pw}')" "$TOKEN")"
LOGIN_CRED_ID="$(jget "$LOGIN_CRED" '.data.id')"
LOGIN_SRV="$(api POST /api/v1/servers "$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg cid "$LOGIN_CRED_ID" \
  '{name:"verify-login-srv",host:$host,port:$port,mode:"ssh",credential_id:$cid}')" "$TOKEN")"
LOGIN_SRV_ID="$(jget "$LOGIN_SRV" '.data.id')"
[ -n "$LOGIN_SRV_ID" ] && pass "创建登录脚本验收用服务器（devssh 凭证）" \
  || fail "创建登录脚本验收服务器失败：$LOGIN_SRV"
if [ -n "$ACCESS_GRP_ID" ] && [ -n "$LOGIN_SRV_ID" ]; then
  api PUT "/api/v1/server-groups/${ACCESS_GRP_ID}/servers" \
    "$(jq -n --arg a "$ACCESS_SRV_ID" --arg b "$LOGIN_SRV_ID" '{server_ids:[$a,$b]}')" "$TOKEN" >/dev/null
fi

LOGIN_TERM_SCRIPT="printf '{\"terminal\":\"ssh -p 22 root@1.2.3.4\"}'"
LOGIN_PATCH_BODY="$(jq -n --arg s "$LOGIN_TERM_SCRIPT" '{login_script:$s}')"
LOGIN_PATCH="$(api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" "$LOGIN_PATCH_BODY" "$TOKEN")"
LOGIN_SAVED="$(jget "$LOGIN_PATCH" '.data.login_script')"
[ -n "$LOGIN_SAVED" ] && pass "PATCH /servers/{id} 保存 login_script 成功" \
  || fail "保存 login_script 失败：$LOGIN_PATCH"

LOGIN_REREAD="$(jget "$(api GET "/api/v1/servers/${LOGIN_SRV_ID}" "" "$TOKEN")" '.data.login_script')"
[ "$LOGIN_REREAD" = "$LOGIN_SAVED" ] && pass "GET /servers/{id} 回读 login_script 一致" \
  || fail "login_script 回读不一致：写入='${LOGIN_SAVED}' 回读='${LOGIN_REREAD}'"

LOGIN_TERM="$(api POST "/api/v1/servers/${LOGIN_SRV_ID}/login" "" "$TOKEN")"
LOGIN_TERM_CMD="$(jget "$LOGIN_TERM" '.data.terminal')"
[ "$LOGIN_TERM_CMD" = "ssh -p 22 root@1.2.3.4" ] \
  && pass "脚本输出 terminal 被原样解析（${LOGIN_TERM_CMD}）" \
  || fail "terminal 形态解析失败：$LOGIN_TERM"

api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" \
  "$(jq -n --arg s "printf '{\"url\":\"https://panel.example.com/t/abc\"}'" '{login_script:$s}')" "$TOKEN" >/dev/null
LOGIN_URL="$(api POST "/api/v1/servers/${LOGIN_SRV_ID}/login" "" "$TOKEN")"
LOGIN_URL_VAL="$(jget "$LOGIN_URL" '.data.url')"
[ "$LOGIN_URL_VAL" = "https://panel.example.com/t/abc" ] \
  && pass "脚本输出 url 被原样解析（${LOGIN_URL_VAL}）" \
  || fail "url 形态解析失败：$LOGIN_URL"

api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" '{"login_script":"echo hello-login"}' "$TOKEN" >/dev/null
LOGIN_RAW="$(api POST "/api/v1/servers/${LOGIN_SRV_ID}/login" "" "$TOKEN")"
LOGIN_RAW_OUT="$(jget "$LOGIN_RAW" '.data.output')"
LOGIN_RAW_URL="$(jget "$LOGIN_RAW" '.data.url')"
if printf '%s' "$LOGIN_RAW_OUT" | grep -q 'hello-login' && [ -z "$LOGIN_RAW_URL" ]; then
  pass "非 JSON 输出整体落到 output，url/terminal 留空"
else
  fail "非 JSON 输出的降级不正确：$LOGIN_RAW"
fi

api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" '{"login_script":"echo boom >&2; exit 3"}' "$TOKEN" >/dev/null
LOGIN_FAIL_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${LOGIN_SRV_ID}/login")"
LOGIN_FAIL_BODY="$(api POST "/api/v1/servers/${LOGIN_SRV_ID}/login" "" "$TOKEN")"
LOGIN_FAIL_ERR="$(jget "$LOGIN_FAIL_BODY" '.error.code')"
if [ "$LOGIN_FAIL_CODE" = "502" ] && [ "$LOGIN_FAIL_ERR" = "remote_failure" ]; then
  pass "脚本执行失败返回 502 remote_failure（不谎称成功）"
else
  fail "脚本失败应 502 remote_failure，实际 ${LOGIN_FAIL_CODE}/${LOGIN_FAIL_ERR}：$LOGIN_FAIL_BODY"
fi

LOGIN_EMPTY_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${NOCRED_SRV_ID}/login")"
LOGIN_EMPTY_BODY="$(api POST "/api/v1/servers/${NOCRED_SRV_ID}/login" "" "$TOKEN")"
LOGIN_EMPTY_MSG="$(jget "$LOGIN_EMPTY_BODY" '.error.message')"
if [ "$LOGIN_EMPTY_CODE" = "409" ] && printf '%s' "$LOGIN_EMPTY_MSG" | grep -q '未配置登录脚本'; then
  pass "未配置脚本返回 409 并说明原因（${LOGIN_EMPTY_MSG}）"
else
  fail "未配置脚本应 409 且给出原因，实际 ${LOGIN_EMPTY_CODE}：$LOGIN_EMPTY_BODY"
fi

LOGIN_LONG="$(api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" \
  "$(jq -n --arg s "$(printf 'x%.0s' $(seq 1 9000))" '{login_script:$s}')" "$TOKEN")"
LOGIN_LONG_CODE="$(printf '%s' "$LOGIN_LONG" | jq -r '.error.code // empty' 2>/dev/null)"
[ "$LOGIN_LONG_CODE" = "invalid_argument" ] && pass "超长 login_script 返回 400 invalid_argument" \
  || fail "超长 login_script 未被拦截：$LOGIN_LONG"

LOGIN_NUL_CODE="$(curl -sS -o /dev/null -w '%{http_code}' -X PATCH "${BASE}/api/v1/servers/${LOGIN_SRV_ID}" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer ${TOKEN}" \
  --data-binary '{"login_script":"a\u0000b"}' 2>/dev/null)"
[ "$LOGIN_NUL_CODE" = "400" ] && pass "含 NUL 的 login_script 返回 400" \
  || fail "含 NUL 的 login_script 返回 ${LOGIN_NUL_CODE}，期望 400"

api PATCH "/api/v1/servers/${LOGIN_SRV_ID}" \
  "$(jq -n --arg s "$LOGIN_TERM_SCRIPT" '{login_script:$s}')" "$TOKEN" >/dev/null
LOGIN_SCOPE_KEY=""; LOGIN_SCOPE_LOGIN=""
if [ -n "$ACCESS_GRP_ID" ]; then
  LOGIN_SCOPE_KEY="$(jget "$(api POST /api/v1/roles "$(jq -n --arg g "$ACCESS_GRP_ID" \
    '{name:"verify-login-writer",permissions:["server.read","server.write"],scope:{mode:"groups",group_ids:[$g],server_ids:[]}}')" "$TOKEN")" '.data.key')"
fi
if [ -n "$LOGIN_SCOPE_KEY" ]; then
  api POST /api/v1/users "$(jq -n --arg r "$LOGIN_SCOPE_KEY" --arg pw 'verify-login-pw' \
    '{username:"verify-login-writer",role:$r,"'"$PW_KEY"'":$pw}')" "$TOKEN" >/dev/null
  LOGIN_SCOPE_TOKEN="$(jget "$(api POST /api/v1/auth/login "$(jq -n --arg pw 'verify-login-pw' \
    '{username:"verify-login-writer","'"$PW_KEY"'":$pw}')")" '.data.token')"
  if [ -n "$LOGIN_SCOPE_TOKEN" ]; then
    LS_IN="$(status_of -X POST -H "Authorization: Bearer ${LOGIN_SCOPE_TOKEN}" "${BASE}/api/v1/servers/${LOGIN_SRV_ID}/login")"
    LS_OUT="$(status_of -X POST -H "Authorization: Bearer ${LOGIN_SCOPE_TOKEN}" "${BASE}/api/v1/servers/${SRV_ID}/login")"
    [ "$LS_IN" = "200" ] && pass "组范围写权限角色对组内服务器可执行登录脚本（200）" \
      || fail "组内登录脚本返回 ${LS_IN}，期望 200"
    [ "$LS_OUT" = "404" ] && pass "组范围角色对组外服务器执行登录脚本返回 404" \
      || fail "组外登录脚本返回 ${LS_OUT}，期望 404"
  else
    fail "组范围写权限用户登录失败"
  fi
else
  fail "创建组范围写权限角色失败：${ACCESS_GRP_ID}"
fi

LOGIN_AUDIT="$(api GET "/api/v1/audit-logs?action=server.login" "" "$TOKEN")"
LOGIN_AUDIT_N="$(printf '%s' "$LOGIN_AUDIT" | jq -r --arg id "$LOGIN_SRV_ID" \
  '[.data[] | select(.action=="server.login" and .resource_id==$id)] | length' 2>/dev/null)"
[ "${LOGIN_AUDIT_N:-0}" -ge 1 ] 2>/dev/null \
  && pass "GET /audit-logs 含有 server.login 记录（${LOGIN_AUDIT_N} 条）" \
  || fail "审计缺少 server.login：$LOGIN_AUDIT"
LOGIN_AUDIT_LEAK="$(printf '%s' "$LOGIN_AUDIT" | jq -r --arg id "$LOGIN_SRV_ID" \
  '[.data[] | select(.action=="server.login" and .resource_id==$id) | ((.detail // "") | contains("panel.example.com"))] | any' 2>/dev/null)"
[ "$LOGIN_AUDIT_LEAK" = "false" ] && pass "server.login 审计 detail 不含脚本产出的 URL 明文" \
  || fail "server.login 审计 detail 泄露了 URL（jq=${LOGIN_AUDIT_LEAK:-解析失败}）"

dist_has "登录信息卡片含一键登录入口" 'server.login.action' 'server.login.scriptTitle'

# ---------------------------------------------------------------- 16. 服务器面板入口
step "16. 服务器面板入口（GET /servers/{id}/panel）"
# 面板入口只回显面板地址与认证类型：api_key 任何响应/审计里都不许出现；
# 未绑定面板凭证给 409 并说明原因；组范围角色读组外服务器 404。
PANEL_URL="https://panel.example.test:8888/abcd1234"
PANEL_API_KEY="{{SECRET_avgnlwz5}}"
PANEL_CRED_BODY="$(jq -n --arg name "verify-panel" --arg u "$PANEL_URL" --arg k "$PANEL_API_KEY" \
  '{name:$name,kind:"baota",panel_url:$u,api_key:$k}')"
PANEL_CRED="$(api POST /api/v1/credentials "$PANEL_CRED_BODY" "$TOKEN")"
PANEL_CRED_ID="$(jget "$PANEL_CRED" '.data.id')"
[ -n "$PANEL_CRED_ID" ] && pass "创建宝塔面板凭证（kind=baota）" || fail "创建宝塔面板凭证失败：$PANEL_CRED"

PANEL_SRV_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg pcid "$PANEL_CRED_ID" \
  '{name:"verify-panel-srv",host:$host,port:$port,mode:"ssh",panel_credential_id:$pcid}')"
PANEL_SRV="$(api POST /api/v1/servers "$PANEL_SRV_BODY" "$TOKEN")"
PANEL_SRV_ID="$(jget "$PANEL_SRV" '.data.id')"
[ -n "$PANEL_SRV_ID" ] && pass "创建服务器并绑定面板凭证" || fail "创建面板验收服务器失败：$PANEL_SRV"

PANEL_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${PANEL_SRV_ID}/panel")"
PANEL_BODY="$(api GET "/api/v1/servers/${PANEL_SRV_ID}/panel" "" "$TOKEN")"
PANEL_GOT_URL="$(jget "$PANEL_BODY" '.data.panel_url')"
if [ "$PANEL_CODE" = "200" ] && [ "$PANEL_GOT_URL" = "$PANEL_URL" ]; then
  pass "GET /servers/{id}/panel 返回 200 且 panel_url 与创建值逐字相等"
else
  fail "GET panel 返回 ${PANEL_CODE}，panel_url='${PANEL_GOT_URL}'，期望 ${PANEL_URL}"
fi
PANEL_GOT_SID="$(jget "$PANEL_BODY" '.data.server_id')"
PANEL_GOT_AUTH="$(jget "$PANEL_BODY" '.data.auth_type')"
[ "$PANEL_GOT_SID" = "$PANEL_SRV_ID" ] && [ "$PANEL_GOT_AUTH" = "baota" ] \
  && pass "面板入口字段齐全（server_id/auth_type=baota）" \
  || fail "面板入口字段不符：server_id='${PANEL_GOT_SID}' auth_type='${PANEL_GOT_AUTH}'"
if printf '%s' "$PANEL_BODY" | grep -qF -- "$PANEL_API_KEY"; then
  fail "GET /servers/{id}/panel 响应泄露了 api_key 明文"
else
  pass "GET /servers/{id}/panel 响应不含 api_key 明文"
fi

PANEL_NOCRED_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${NOCRED_SRV_ID}/panel")"
PANEL_NOCRED_BODY="$(api GET "/api/v1/servers/${NOCRED_SRV_ID}/panel" "" "$TOKEN")"
PANEL_NOCRED_MSG="$(jget "$PANEL_NOCRED_BODY" '.error.message')"
if [ "$PANEL_NOCRED_CODE" = "409" ] && printf '%s' "$PANEL_NOCRED_MSG" | grep -q '面板凭证'; then
  pass "未绑定面板凭证返回 409 并说明原因（${PANEL_NOCRED_MSG}）"
else
  fail "未绑定面板凭证应 409 且 message 含「面板凭证」，实际 ${PANEL_NOCRED_CODE}：$PANEL_NOCRED_BODY"
fi

# 独立用例：先绑定面板凭证、再显式解绑（PATCH panel_credential_id=""），GET 仍须 409。
PANEL_UNBIND_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg pcid "$PANEL_CRED_ID" \
  '{name:"verify-panel-unbind",host:$host,port:$port,mode:"ssh",panel_credential_id:$pcid}')"
PANEL_UNBIND_SRV="$(api POST /api/v1/servers "$PANEL_UNBIND_BODY" "$TOKEN")"
PANEL_UNBIND_ID="$(jget "$PANEL_UNBIND_SRV" '.data.id')"
api PATCH "/api/v1/servers/${PANEL_UNBIND_ID}" '{"panel_credential_id":""}' "$TOKEN" >/dev/null
PANEL_UNBIND_LEFT="$(jget "$(api GET "/api/v1/servers/${PANEL_UNBIND_ID}" "" "$TOKEN")" '.data.panel_credential_id')"
PANEL_UNBIND_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${PANEL_UNBIND_ID}/panel")"
PANEL_UNBIND_GET="$(api GET "/api/v1/servers/${PANEL_UNBIND_ID}/panel" "" "$TOKEN")"
PANEL_UNBIND_MSG="$(jget "$PANEL_UNBIND_GET" '.error.message')"
if [ -n "$PANEL_UNBIND_ID" ] && [ -z "$PANEL_UNBIND_LEFT" ] && [ "$PANEL_UNBIND_CODE" = "409" ] \
  && printf '%s' "$PANEL_UNBIND_MSG" | grep -q '面板凭证'; then
  pass "解绑面板凭证后 GET /servers/{id}/panel 返回 409 并说明原因"
else
  fail "解绑面板凭证后应 409 且 message 含「面板凭证」，实际 ${PANEL_UNBIND_CODE}：panel_credential_id='${PANEL_UNBIND_LEFT}' ${PANEL_UNBIND_GET}"
fi

# 组外对照服务器：同样绑定面板凭证（管理员能 200），这样组范围角色的 404 只能来自范围判定。
PANEL_OUT_BODY="$(jq -n --arg host "127.0.0.1" --argjson port "$SSH_PORT" --arg pcid "$PANEL_CRED_ID" \
  '{name:"verify-panel-out",host:$host,port:$port,mode:"ssh",panel_credential_id:$pcid}')"
PANEL_OUT_SRV="$(api POST /api/v1/servers "$PANEL_OUT_BODY" "$TOKEN")"
PANEL_OUT_SRV_ID="$(jget "$PANEL_OUT_SRV" '.data.id')"
[ -n "$PANEL_OUT_SRV_ID" ] && pass "创建组外面板验收服务器" || fail "创建组外面板服务器失败：$PANEL_OUT_SRV"
PANEL_OUT_ADMIN="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers/${PANEL_OUT_SRV_ID}/panel")"
[ "$PANEL_OUT_ADMIN" = "200" ] && pass "组外服务器管理员读面板 200（证明后续 404 来自范围判定）" \
  || fail "组外服务器管理员读面板返回 ${PANEL_OUT_ADMIN}，期望 200"

if [ -n "$ACCESS_GRP_ID" ] && [ -n "$PANEL_SRV_ID" ]; then
  api PUT "/api/v1/server-groups/${ACCESS_GRP_ID}/servers" \
    "$(jq -n --arg a "$ACCESS_SRV_ID" --arg p "$PANEL_SRV_ID" '{server_ids:[$a,$p]}')" "$TOKEN" >/dev/null
fi
if [ -n "$VTOKEN" ] && [ -n "$PANEL_SRV_ID" ] && [ -n "$PANEL_OUT_SRV_ID" ]; then
  PANEL_SCOPE_IN="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/servers/${PANEL_SRV_ID}/panel")"
  PANEL_SCOPE_OUT="$(status_of -H "Authorization: Bearer ${VTOKEN}" "${BASE}/api/v1/servers/${PANEL_OUT_SRV_ID}/panel")"
  [ "$PANEL_SCOPE_IN" = "200" ] && pass "组范围角色读组内服务器面板 200（正向对照）" \
    || fail "组内面板入口返回 ${PANEL_SCOPE_IN}，期望 200（否则 404 断言失去意义）"
  [ "$PANEL_SCOPE_OUT" = "404" ] && pass "组范围角色读组外服务器面板返回 404（不泄露存在性）" \
    || fail "组外面板入口返回 ${PANEL_SCOPE_OUT}，期望 404"
else
  fail "缺少组范围角色 token，无法验证面板入口的组外 404"
fi

PANEL_AUDIT="$(api GET "/api/v1/audit-logs?action=server.panel" "" "$TOKEN")"
PANEL_AUDIT_N="$(printf '%s' "$PANEL_AUDIT" | jq -r --arg id "$PANEL_SRV_ID" \
  '[.data[] | select(.action=="server.panel" and .resource_id==$id)] | length' 2>/dev/null)"
[ "${PANEL_AUDIT_N:-0}" -ge 1 ] 2>/dev/null \
  && pass "GET /audit-logs 含有 server.panel 记录（${PANEL_AUDIT_N} 条）" \
  || fail "审计缺少 server.panel：$PANEL_AUDIT"
PANEL_AUDIT_LEAK="$(printf '%s' "$PANEL_AUDIT" | jq -r --arg id "$PANEL_SRV_ID" --arg u "$PANEL_URL" --arg k "$PANEL_API_KEY" \
  '[.data[] | select(.action=="server.panel" and .resource_id==$id) | ((.detail // "") | (contains($u) or contains($k)))] | any' 2>/dev/null)"
[ "$PANEL_AUDIT_LEAK" = "false" ] && pass "server.panel 审计 detail 不含 panel_url / api_key 明文" \
  || fail "server.panel 审计 detail 泄露了面板地址或密钥（jq=${PANEL_AUDIT_LEAK:-解析失败}）"

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m服务器纳管 验收通过\033[0m\n'; exit 0; fi
printf '\033[31m服务器纳管 验收未通过\033[0m\n'
exit 1
