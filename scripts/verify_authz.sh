#!/usr/bin/env bash
# LeadStack 权限体系端到端验收：自定义角色 / 服务器分组 / 资源级授权 / 登录限流与锁定
#
# 覆盖 docs/ARCHITECTURE.md 第 52–56 节。
# 用法：make build && bash scripts/verify_authz.sh
#
# 关键：不止看状态码——受限用户的列表必须真的只剩范围内服务器，
# 锁定必须在第 N 次失败后生效且锁定期内正确密码也被拒，解锁后必须能登录。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18955}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-authz-pass"
MAX_ATTEMPTS=3
LOCK_DURATION="60s"
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
BASE_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-authz.XXXXXX")"
DATA_DIR="$BASE_DIR/data"
LEAD_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
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
code() {
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -o /dev/null -w '%{http_code}' -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
jget() { printf '%s' "$1" | jq -r "$2" 2>/dev/null; }
sget() { local v; v="$(jget "$1" "$2")"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }
login() { api POST /api/v1/auth/login "{\"username\":\"$1\",\"password\":\"$2\"}" | jq -r '.data.token // empty' 2>/dev/null; }

start_lead() {
  mkdir -p "$DATA_DIR"
  env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
      LEADSTACK_DATA_DIR="$DATA_DIR" \
      LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
      LEADSTACK_LOGIN_MAX_ATTEMPTS="$MAX_ATTEMPTS" \
      LEADSTACK_LOGIN_LOCK_DURATION="$LOCK_DURATION" \
      LEADSTACK_LOGIN_IP_LIMIT="${IP_LIMIT:-0}" \
      LEADSTACK_LOGIN_IP_WINDOW="60s" \
      ./bin/leadstack > "$BASE_DIR/lead.log" 2>&1 &
  LEAD_PID=$!
  for _ in $(seq 1 80); do [ "$(code GET /healthz)" = "200" ] && return 0; sleep 0.3; done
  return 1
}
stop_lead() {
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  wait "$LEAD_PID" 2>/dev/null
  LEAD_PID=""
}

echo "临时目录：$BASE_DIR"

# ---------------------------------------------------------------- 1. 构建
step "1. 构建与启动"
[ -x bin/leadstack ] || { echo "缺少 bin/leadstack，请先 make build"; exit 1; }
pass "bin/leadstack 存在"
start_lead || { echo "控制端启动失败，日志："; tail -20 "$BASE_DIR/lead.log"; exit 1; }
pass "控制端已启动（迁移应到 v8）"

ADMIN="$(login admin "$ADMIN_PASS")"
[ -n "$ADMIN" ] && pass "管理员登录成功" || { fail "管理员登录失败"; exit 1; }

# ---------------------------------------------------------------- 2. 权限目录与内置角色
step "2. 权限目录与内置角色（契约 §52）"
PERMS="$(api GET /api/v1/permissions "" "$ADMIN")"
PERM_COUNT="$(printf '%s' "$PERMS" | jq '.data | length')"
[ "$PERM_COUNT" = "24" ] && pass "权限点 24 个" || fail "权限点应为 24，实际 $PERM_COUNT"
ADMIN_ONLY="$(printf '%s' "$PERMS" | jq '[.data[] | select(.admin_only)] | length')"
[ "$ADMIN_ONLY" = "6" ] && pass "仅管理员可持有 6 个" || fail "admin_only 应为 6，实际 $ADMIN_ONLY"

ROLES="$(api GET /api/v1/roles "" "$ADMIN")"
V=$(printf '%s' "$ROLES" | jq -r '[.data[] | select(.key=="viewer")][0].permissions | length')
O=$(printf '%s' "$ROLES" | jq -r '[.data[] | select(.key=="operator")][0].permissions | length')
A=$(printf '%s' "$ROLES" | jq -r '[.data[] | select(.key=="admin")][0].permissions | length')
[ "$V" = "6" ] && pass "viewer 权限 6 个" || fail "viewer 权限应为 6，实际 $V"
[ "$O" = "14" ] && pass "operator 权限 14 个" || fail "operator 权限应为 14，实际 $O"
[ "$A" = "24" ] && pass "admin 拥有全部 24 个" || fail "admin 权限应为 24，实际 $A"

# ---------------------------------------------------------------- 3. 自定义角色
step "3. 自定义角色与防提权"
DEPLOY_ROLE_JSON='{"name":"发布员","description":"只能发布","permissions":["deploy.read","deploy.write"],"scope":{"mode":"all","group_ids":[],"server_ids":[]}}'
DEPLOY_ROLE="$(api POST /api/v1/roles "$DEPLOY_ROLE_JSON" "$ADMIN")"
DEPLOY_KEY="$(sget "$DEPLOY_ROLE" '.data.key')"
[ -n "$DEPLOY_KEY" ] && pass "创建自定义角色 ${DEPLOY_KEY}" || fail "创建自定义角色失败：$DEPLOY_ROLE"

ESCALATE_CODE="$(code POST /api/v1/roles '{"name":"提权","permissions":["user.manage"],"scope":{"mode":"all"}}' "$ADMIN")"
[ "$ESCALATE_CODE" = "400" ] && pass "带 user.manage 的自定义角色被拒（400）" || fail "防提权应为 400，实际 $ESCALATE_CODE"

api POST /api/v1/users "{\"username\":\"deployer\",\"password\":\"DeployPass-123\",\"role\":\"$DEPLOY_KEY\"}" "$ADMIN" > /dev/null
DEPLOYER="$(login deployer "DeployPass-123")"
[ -n "$DEPLOYER" ] && pass "自定义角色用户登录成功" || fail "自定义角色用户登录失败"
[ "$(code GET /api/v1/releases "" "$DEPLOYER")" = "200" ] && pass "发布员可读发布（200）" || fail "发布员读发布应 200"
[ "$(code POST /api/v1/servers '{"name":"x","host":"10.9.9.9"}' "$DEPLOYER")" = "403" ] && pass "发布员无 server.write（403）" || fail "发布员建服务器应 403"

# ---------------------------------------------------------------- 4. 分组与资源级授权
step "4. 服务器分组与资源级授权（契约 §53）"
S1="$(api POST /api/v1/servers '{"name":"prod-a","host":"10.10.0.1","mode":"ssh"}' "$ADMIN" | jq -r '.data.id')"
S2="$(api POST /api/v1/servers '{"name":"prod-b","host":"10.10.0.2","mode":"ssh"}' "$ADMIN" | jq -r '.data.id')"
[ -n "$S1" ] && [ -n "$S2" ] && pass "创建两台服务器" || fail "创建服务器失败"

GROUP="$(api POST /api/v1/server-groups '{"name":"生产组","description":"验收用"}' "$ADMIN" | jq -r '.data.id')"
[ -n "$GROUP" ] && pass "创建分组 ${GROUP}" || fail "创建分组失败"
api PUT "/api/v1/server-groups/$GROUP/servers" "{\"server_ids\":[\"$S1\"]}" "$ADMIN" > /dev/null
MEMBERS="$(api GET "/api/v1/server-groups/${GROUP}/servers" "" "$ADMIN" | jq '.data | length')"
[ "$MEMBERS" = "1" ] && pass "分组成员 1 台" || fail "分组成员应为 1，实际 $MEMBERS"

SCOPED_JSON="{\"name\":\"生产运维\",\"permissions\":[\"server.read\",\"check.read\",\"check.write\",\"group.write\"],\"scope\":{\"mode\":\"groups\",\"group_ids\":[\"$GROUP\"],\"server_ids\":[]}}"
SCOPED_ROLE="$(api POST /api/v1/roles "$SCOPED_JSON" "$ADMIN")"
SCOPED_KEY="$(sget "$SCOPED_ROLE" '.data.key')"
api POST /api/v1/users "{\"username\":\"scoped\",\"password\":\"ScopedPass-123\",\"role\":\"$SCOPED_KEY\"}" "$ADMIN" > /dev/null
SCOPED="$(login scoped "ScopedPass-123")"
[ -n "$SCOPED" ] && pass "受限用户登录成功" || fail "受限用户登录失败"

SERVERS_JSON="$(api GET /api/v1/servers "" "$SCOPED")"
SC_COUNT="$(printf '%s' "$SERVERS_JSON" | jq '.data | length')"
SC_ID="$(printf '%s' "$SERVERS_JSON" | jq -r '.data[0].id // empty' 2>/dev/null)"
[ "$SC_COUNT" = "1" ] && pass "受限用户只看到 1 台服务器" || fail "受限用户应只看到 1 台，实际 $SC_COUNT"
[ "${SC_ID}" = "${S1}" ] && pass "看到的是分组内那台（${S1}）" || fail "可见服务器应为 ${S1}，实际 ${SC_ID}"
[ "$(code GET "/api/v1/servers/$S2" "" "$SCOPED")" = "404" ] && pass "越界读服务器返回 404" || fail "越界读应为 404"

BODY_OUT_OF_SCOPE="$(jq -cn --arg s "$S2" '{name:"越界",kind:"tcp",target:"127.0.0.1:80",interval_s:60,server_ids:[$s]}')"
CODE_OUT_OF_SCOPE="$(code POST /api/v1/checks "$BODY_OUT_OF_SCOPE" "$SCOPED")"
[ "$CODE_OUT_OF_SCOPE" = "403" ] && pass "越界绑定检测目标被拒（403）" || fail "越界写检测目标应 403，实际 $CODE_OUT_OF_SCOPE"

BODY_IN_SCOPE="$(jq -cn --arg s "$S1" '{name:"范围内",kind:"tcp",target:"127.0.0.1:80",interval_s:60,server_ids:[$s]}')"
CODE_IN_SCOPE="$(code POST /api/v1/checks "$BODY_IN_SCOPE" "$SCOPED")"
[ "$CODE_IN_SCOPE" = "201" ] && pass "范围内创建检测成功（201）" || fail "范围内创建检测应 201，实际 $CODE_IN_SCOPE"

GROUP_BODY="$(jq -cn '{name:"越权组"}')"
CODE_GROUP="$(code POST /api/v1/server-groups "$GROUP_BODY" "$SCOPED")"
[ "$CODE_GROUP" = "403" ] && pass "受限范围写分组被拒（403，group.write 需全局范围）" || fail "受限范围写分组应 403，实际 $CODE_GROUP"

CHECKS_VISIBLE="$(api GET /api/v1/checks "" "$SCOPED" | jq '.data | length')"
[ "$CHECKS_VISIBLE" = "1" ] && pass "检测列表按范围收敛（1 项）" || fail "检测列表应为 1 项，实际 $CHECKS_VISIBLE"

# 单台服务器授权（scope=servers）：只应看到指定那一台
SRV_ONLY_JSON="{\"name\":\"单机授权\",\"permissions\":[\"server.read\"],\"scope\":{\"mode\":\"servers\",\"group_ids\":[],\"server_ids\":[\"$S2\"]}}"
SRV_ONLY_ROLE="$(api POST /api/v1/roles "$SRV_ONLY_JSON" "$ADMIN" | jq -r '.data.key // empty')"
api POST /api/v1/users "{\"username\":\"single\",\"password\":\"SinglePass-123\",\"role\":\"$SRV_ONLY_ROLE\"}" "$ADMIN" > /dev/null
SINGLE="$(login single "SinglePass-123")"
SINGLE_JSON="$(api GET /api/v1/servers "" "$SINGLE")"
SINGLE_COUNT="$(printf '%s' "$SINGLE_JSON" | jq '.data | length')"
SINGLE_ID="$(printf '%s' "$SINGLE_JSON" | jq -r '.data[0].id // empty')"
[ "$SINGLE_COUNT" = "1" ] && [ "${SINGLE_ID}" = "${S2}" ] && pass "单机授权只看到指定那一台（${S2}）" \
  || fail "单机授权应只看到 ${S2}，实际 ${SINGLE_COUNT} 台 / ${SINGLE_ID}"

# ---------------------------------------------------------------- 5. 内置角色不可改删 / 被引用角色不可删
step "5. 角色保护规则"
[ "$(code PATCH /api/v1/roles/admin '{"name":"x"}' "$ADMIN")" = "403" ] && pass "内置角色不可改（403）" || fail "内置角色改应 403"
[ "$(code DELETE /api/v1/roles/operator "" "$ADMIN")" = "403" ] && pass "内置角色不可删（403）" || fail "内置角色删应 403"
[ "$(code DELETE "/api/v1/roles/$DEPLOY_KEY" "" "$ADMIN")" = "409" ] && pass "被引用角色不可删（409）" || fail "被引用角色删除应 409"

# ---------------------------------------------------------------- 6. 登录失败锁定与解锁
step "6. 登录失败锁定 → 解锁（契约 §54）"
LOCKCODE=""
for i in $(seq 1 "$MAX_ATTEMPTS"); do
  LAST="$(code POST /api/v1/auth/login '{"username":"deployer","password":"WrongPass-000"}')"
done
LOCKCODE="$LAST"
[ "$LOCKCODE" = "429" ] && pass "第 ${MAX_ATTEMPTS} 次失败即触发锁定（429）" || fail "锁定应 429，实际 $LOCKCODE"

LOCKBODY="$(api POST /api/v1/auth/login '{"username":"deployer","password":"WrongPass-000"}')"
[ "$(sget "$LOCKBODY" '.error.code')" = "login_locked" ] && pass "锁定响应 code=login_locked" || fail "锁定 code 不符：$LOCKBODY"
[ -n "$(sget "$LOCKBODY" '.error.details.retry_after_s')" ] && pass "锁定响应带 retry_after_s" || fail "锁定响应缺 retry_after_s"
[ "$(code POST /api/v1/auth/login '{"username":"deployer","password":"DeployPass-123"}')" = "429" ] \
  && pass "锁定期内正确密码也被拒（429）" || fail "锁定期内正确密码应 429"

LOCKED_ROW="$(api GET /api/v1/users "" "$ADMIN" | jq -r '[.data[] | select(.username=="deployer")][0]')"
BOOL_LOCKED="$(printf '%s' "$LOCKED_ROW" | jq -r '.locked')"
[ "$BOOL_LOCKED" = "true" ] && pass "用户列表显示 locked=true" || fail "用户列表 locked 应为 true，实际 $BOOL_LOCKED"
DEPLOY_UID="$(printf '%s' "$LOCKED_ROW" | jq -r '.id')"

[ "$(code POST "/api/v1/users/$DEPLOY_UID/unlock" "" "$ADMIN")" = "200" ] && pass "管理员解锁成功" || fail "解锁应 200"
UNLOCKED="$(login deployer "DeployPass-123")"
[ -n "$UNLOCKED" ] && pass "解锁后可正常登录" || fail "解锁后仍无法登录"

AUDIT="$(api GET "/api/v1/audit-logs?limit=200" "" "$ADMIN")"
[ "$(printf '%s' "$AUDIT" | jq '[.data.logs[] | select(.action=="auth.login_locked")] | length')" -ge 1 ] \
  && pass "审计含 auth.login_locked" || fail "审计缺少 auth.login_locked"
[ "$(printf '%s' "$AUDIT" | jq '[.data.logs[] | select(.action=="user.unlock")] | length')" -ge 1 ] \
  && pass "审计含 user.unlock" || fail "审计缺少 user.unlock"

# ---------------------------------------------------------------- 7. IP 限流（重启后开启）
step "7. IP 限流（契约 §54）"
stop_lead
IP_LIMIT=3
export IP_LIMIT
start_lead || { fail "开启 IP 限流后启动失败"; tail -20 "$BASE_DIR/lead.log"; }
RL_LAST=""; RL_CODE=""
for i in 1 2 3 4 5; do
  BODY_BIN="$(api POST /api/v1/auth/login '{"username":"nobody-here","password":"WrongPass-000"}')"
  RL_LAST="$BODY_BIN"
  RL_CODE="$(printf '%s' "$BODY_BIN" | jq -r '.error.code // empty' 2>/dev/null)"
  [ "$RL_CODE" = "rate_limited" ] && break
done
[ "$RL_CODE" = "rate_limited" ] && pass "超出 IP 窗口返回 rate_limited（429 语义）" || fail "IP 限流未生效（最后响应：$RL_LAST）"
RETRY_HEADER="$(curl -sS -o /dev/null -D - -X POST "${BASE}/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"nobody-here","password":"WrongPass-000"}' 2>/dev/null | tr -d '\r' | awk 'tolower($1)=="retry-after:"{print $2; exit}')"
[ -n "$RETRY_HEADER" ] && pass "429 响应带 Retry-After 头（${RETRY_HEADER}s）" || fail "缺少 Retry-After 头"

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
