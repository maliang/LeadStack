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
LEAD_PID=""; MOCK_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null
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
      LEADSTACK_TRUSTED_PROXIES="${TRUSTED_PROXIES:-}" \
      LEADSTACK_UPDATE_API_BASE="${UPDATE_API_BASE:-}" \
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

# mock GitHub Releases API（A29）：控制端以 LEADSTACK_UPDATE_API_BASE 指向它。
# 发的是真实 HTTP 请求，断言的是真实 /system/update/check 响应，不走任何桩逻辑。
MOCK_PORT=$((PORT + 3))
UPDATE_API_BASE="http://127.0.0.1:${MOCK_PORT}"
cat > "$BASE_DIR/mock_gh.py" <<'PY'
import http.server, json, sys

PORT = int(sys.argv[1])
BASE = "http://127.0.0.1:%d" % PORT
# 两个 agent 归档用不同的 sha256：若控制端按 os/arch 猜包而不是按文件名精确匹配，
# 取到的 sha 就会对不上，断言会失败。
SHA = {
    "leadstack-agent_v9.9.9_darwin_arm64.tar.gz": "3f1e" + "a" * 60,
    "leadstack-agent_v9.9.9_linux_amd64.tar.gz": "9b7d" + "b" * 60,
}
CTRL = ["leadstack_v9.9.9_darwin_arm64.tar.gz", "leadstack_v9.9.9_linux_amd64.tar.gz"]
ASSETS = [{"name": n, "browser_download_url": BASE + "/pkg/" + n, "size": 1024}
          for n in (list(SHA) + CTRL)]
ASSETS.append({"name": "checksums.txt",
               "browser_download_url": BASE + "/pkg/checksums.txt", "size": 128})
RELEASE = json.dumps({
    "tag_name": "v9.9.9", "name": "v9.9.9", "body": "e2e mock release",
    "html_url": BASE + "/releases/v9.9.9", "published_at": "2026-01-01T00:00:00Z",
    "draft": False, "prerelease": False, "assets": ASSETS,
}).encode()
CHECKSUMS = "\n".join("%s  %s" % (sha, name) for name, sha in SHA.items()).encode() + b"\n"


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.endswith("/releases/latest"):
            body, ctype = RELEASE, "application/json"
        elif self.path == "/pkg/checksums.txt":
            body, ctype = CHECKSUMS, "text/plain"
        elif self.path.startswith("/pkg/"):
            body, ctype = b"e2e-mock-asset", "application/octet-stream"
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


http.server.HTTPServer.allow_reuse_address = True
with http.server.HTTPServer(("127.0.0.1", PORT), H) as httpd:
    httpd.serve_forever()
PY
python3 "$BASE_DIR/mock_gh.py" "$MOCK_PORT" > "$BASE_DIR/mock_gh.out" 2>&1 &
MOCK_PID=$!
MOCK_UP=0
for _ in $(seq 1 40); do
  curl -s -o /dev/null "${UPDATE_API_BASE}/repos/maliang/leadstack/releases/latest" && { MOCK_UP=1; break; }
  sleep 0.2
done
if [ "$MOCK_UP" = "1" ]; then
  pass "mock GitHub Releases API 已监听 ${UPDATE_API_BASE}"
else
  fail "mock 更新 API 未就绪：$(tail -3 "$BASE_DIR/mock_gh.out" 2>/dev/null)"
  exit 1
fi

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

# ---------------------------------------------------------------- 8. 可信代理
step "8. 可信代理：X-Forwarded-For 解析（契约：可信代理白名单）"
stop_lead
IP_LIMIT=3
TRUSTED_PROXIES="127.0.0.1/32"
export IP_LIMIT TRUSTED_PROXIES
start_lead || { fail "开启可信代理后启动失败"; tail -20 "$BASE_DIR/lead.log"; }

xf_login() { # <xff>
  curl -sS -X POST "${BASE}/api/v1/auth/login" -H 'Content-Type: application/json' \
    -H "X-Forwarded-For: $1" -d '{"username":"nobody-xff","password":"WrongPass-000"}' 2>/dev/null
}
A_LAST=""; A_CODE=""
for i in 1 2 3 4 5; do
  A_LAST="$(xf_login 203.0.113.9)"
  A_CODE="$(printf '%s' "$A_LAST" | jq -r '.error.code // empty' 2>/dev/null)"
  [ "$A_CODE" = "rate_limited" ] && break
done
[ "$A_CODE" = "rate_limited" ] && pass "XFF 来源 A（203.0.113.9）触发限流：按转发 IP 分桶" \
  || fail "可信代理下 XFF 来源未被单独计数（最后响应：$A_LAST）"

B_LAST="$(xf_login 203.0.113.10)"
B_CODE="$(printf '%s' "$B_LAST" | jq -r '.error.code // empty' 2>/dev/null)"
[ "$B_CODE" != "rate_limited" ] && pass "XFF 来源 B（203.0.113.10）不受 A 影响：未全站共享一个桶" \
  || fail "不同 XFF 来源仍共用限流桶：$B_LAST"

AUDIT_XFF="$(api GET "/api/v1/audit-logs?limit=200" "" "$ADMIN")"
# 登录失败在审计里记作 auth.login + status_code=401（路由级审计按路由命名，不区分成败）
LOGIN_FAIL_SEL='select(.path=="/api/v1/auth/login") | select(.status_code==401)'
XFF_N="$(printf '%s' "$AUDIT_XFF" | jq "[.data.logs[] | $LOGIN_FAIL_SEL | select(.ip==\"203.0.113.9\")] | length" 2>/dev/null)"
[ "${XFF_N:-0}" -ge 1 ] 2>/dev/null && pass "审计记录真实客户端 IP（XFF 203.0.113.9）" \
  || fail "审计未记录转发客户端 IP（命中 ${XFF_N:-0} 条）"

# 对照：去掉白名单后 XFF 必须被忽略，来源 IP 一律记为对端（127.0.0.1）
stop_lead
TRUSTED_PROXIES=""
export TRUSTED_PROXIES
start_lead || { fail "关闭可信代理后启动失败"; tail -20 "$BASE_DIR/lead.log"; }
xf_login 203.0.113.11 >/dev/null
AUDIT_PLAIN="$(api GET "/api/v1/audit-logs?limit=50" "" "$ADMIN")"
PLAIN_N="$(printf '%s' "$AUDIT_PLAIN" | jq "[.data.logs[] | $LOGIN_FAIL_SEL | select(.ip==\"203.0.113.11\")] | length" 2>/dev/null)"
PLAIN_LOOP="$(printf '%s' "$AUDIT_PLAIN" | jq "[.data.logs[] | $LOGIN_FAIL_SEL | select(.ip==\"127.0.0.1\")] | length" 2>/dev/null)"
if [ "${PLAIN_N:-0}" -eq 0 ] && [ "${PLAIN_LOOP:-0}" -ge 1 ] 2>/dev/null; then
  pass "未配置白名单时 XFF 被忽略（记为对端 127.0.0.1）"
else
  fail "未配置白名单时仍采信 XFF（伪造命中 ${PLAIN_N:-0}，对端命中 ${PLAIN_LOOP:-0}）"
fi

# ---------------------------------------------------------------- 9. 应用敏感字段收敛与审计脱敏
step "9. 应用敏感字段收敛与审计脱敏（A9/A16）"

SECRET1="s3cr3t-value-aaa"
SECRET2="s3cr3t-value-bbb"
APP_BODY="$(jq -n --arg s1 "$SECRET1" '{name:"敏感字段",slug:"sensitive",provider:"builtin",deploy_mode:"release",
  env_json:({API_SECRET:$s1,DB_URL:"mysql://u:p@h/db"}|tojson),init_script:"echo init",restart_cmd:"systemctl restart sensitive"}')"
APP_JSON="$(api POST /api/v1/apps "$APP_BODY" "$ADMIN")"
APP_ID="$(sget "$APP_JSON" '.data.id')"
[ -n "$APP_ID" ] && pass "创建带 env_json/init_script 的应用" || fail "创建应用失败：$APP_JSON"

# 管理员（scope=all + deploy.write）可见原值
APP_DETAIL="$(api GET "/api/v1/apps/$APP_ID" "" "$ADMIN")"
printf '%s' "$APP_DETAIL" | jq -e '.data.env_json | type == "string"' >/dev/null 2>&1 \
  && pass "管理员可见 env_json 原值" || fail "管理员应看到 env_json：$APP_DETAIL"
printf '%s' "$APP_DETAIL" | jq -e '.data.env_keys | index("API_SECRET") != null' >/dev/null 2>&1 \
  && pass "响应带 env_keys 键名" || fail "缺少 env_keys"

# 发布员（deploy.read+write、scope=all）同样可见
DEP_APP="$(api GET "/api/v1/apps/$APP_ID" "" "$DEPLOYER")"
printf '%s' "$DEP_APP" | jq -e '.data.env_json | type == "string"' >/dev/null 2>&1 \
  && pass "发布员（有 deploy.write）可见 env_json" || fail "发布员应可见 env_json：$DEP_APP"

# 只读角色（仅 deploy.read）：三个敏感字段一律 null，但 env_keys 仍在（键名可留）
READONLY_ROLE="$(api POST /api/v1/roles '{"name":"应用只读","permissions":["deploy.read"],"scope":{"mode":"all","group_ids":[],"server_ids":[]}}' "$ADMIN" | jq -r '.data.key // empty')"
api POST /api/v1/users "{\"username\":\"appviewer\",\"password\":\"AppViewer-123\",\"role\":\"$READONLY_ROLE\"}" "$ADMIN" > /dev/null
APPVIEWER="$(login appviewer "AppViewer-123")"
[ -n "$APPVIEWER" ] && pass "应用只读用户登录成功" || fail "应用只读用户登录失败"
RO_DETAIL="$(api GET "/api/v1/apps/$APP_ID" "" "$APPVIEWER")"
printf '%s' "$RO_DETAIL" | jq -e '.data.env_json == null and .data.init_script == null and .data.restart_cmd == null' >/dev/null 2>&1 \
  && pass "无 deploy.write 时敏感字段全部为 null" || fail "敏感字段未收敛：$RO_DETAIL"
printf '%s' "$RO_DETAIL" | jq -e '.data.redacted == true' >/dev/null 2>&1 \
  && pass "响应标记 redacted=true" || fail "缺少 redacted 标记：$RO_DETAIL"
printf '%s' "$RO_DETAIL" | jq -e '.data.env_keys | length == 2' >/dev/null 2>&1 \
  && pass "键名仍可见（2 个）" || fail "env_keys 数量不符：$RO_DETAIL"
RO_LIST="$(api GET /api/v1/apps "" "$APPVIEWER")"
printf '%s' "$RO_LIST" | jq -e '[.data[] | .env_json] | all(. == null)' >/dev/null 2>&1 \
  && pass "列表接口同样不泄露 env_json" || fail "列表泄露 env_json：$RO_LIST"

# 审计脱敏：改动 env_json 后，审计里不得出现明文值
api PATCH "/api/v1/apps/$APP_ID" "$(jq -n --arg s2 "$SECRET2" '{env_json:({API_SECRET:$s2}|tojson)}')" "$ADMIN" > /dev/null
AUDIT_APP="$(api GET "/api/v1/audit-logs?limit=200" "" "$ADMIN")"
if printf '%s' "$AUDIT_APP" | grep -q "$SECRET1" || printf '%s' "$AUDIT_APP" | grep -q "$SECRET2"; then
  fail "审计泄露 env_json 明文值"
else
  pass "审计未出现 env_json 明文值"
fi
printf '%s' "$AUDIT_APP" | jq -e '[.data.logs[] | select(.action=="app.update" or .action=="app.create")] | length >= 1' >/dev/null 2>&1 \
  && pass "审计记录了 app.update/app.create" || fail "审计缺少 app 变更记录"

# 409 透出精确原因（不是通用文案）
DUP_BODY="$(jq -n '{name:"生产组"}')"
DUP_CODE="$(code POST /api/v1/server-groups "$DUP_BODY" "$ADMIN")"
DUP_MSG="$(api POST /api/v1/server-groups "$DUP_BODY" "$ADMIN" | jq -r '.error.message // empty')"
[ "$DUP_CODE" = "409" ] && pass "重名分组返回 409" || fail "重名分组应 409，实际 $DUP_CODE"
if [ "$DUP_MSG" != "资源已存在或冲突" ] && [ -n "$DUP_MSG" ]; then
  pass "409 透出精确原因：${DUP_MSG}"
else
  fail "409 仍是通用文案：${DUP_MSG}"
fi

# ---------------------------------------------------------------- 10. 检测汇总口径
step "10. 检测汇总按范围收敛（A12/A14）"
# 管理员在「范围外」服务器（S2）再建两个检测项，让全局与受限两种口径的差异可观测。
api POST /api/v1/checks "$(jq -cn --arg s "$S2" '{name:"范围外-1",kind:"tcp",target:"127.0.0.1:81",interval_s:60,server_ids:[$s]}')" "$ADMIN" >/dev/null
api POST /api/v1/checks "$(jq -cn --arg s "$S2" '{name:"范围外-2",kind:"tcp",target:"127.0.0.1:82",interval_s:60,server_ids:[$s]}')" "$ADMIN" >/dev/null

ADMIN_LIST_N="$(api GET /api/v1/checks "" "$ADMIN" | jq '.data | length')"
ADMIN_SUM="$(api GET /api/v1/checks/summary "" "$ADMIN")"
ADMIN_TOTAL="$(printf '%s' "$ADMIN_SUM" | jq '.data.total // -1')"
ADMIN_ROWS="$(printf '%s' "$ADMIN_SUM" | jq '.data.checks | length')"
if [ "${ADMIN_TOTAL}" = "${ADMIN_LIST_N}" ] && [ "${ADMIN_ROWS}" = "${ADMIN_LIST_N}" ] && [ "${ADMIN_LIST_N:-0}" -ge 3 ]; then
  pass "管理员 summary 与列表同口径（total=${ADMIN_TOTAL}，列表 ${ADMIN_LIST_N} 项）"
else
  fail "管理员 summary 与列表不一致：total=${ADMIN_TOTAL}、checks=${ADMIN_ROWS}、列表=${ADMIN_LIST_N}（应 ≥3 且三者相等）"
fi

SC_LIST_N="$(api GET /api/v1/checks "" "$SCOPED" | jq '.data | length')"
SC_SUM="$(api GET /api/v1/checks/summary "" "$SCOPED")"
SC_TOTAL="$(printf '%s' "$SC_SUM" | jq '.data.total // -1')"
SC_ROWS="$(printf '%s' "$SC_SUM" | jq '.data.checks | length')"
if [ "${SC_TOTAL}" = "1" ] && [ "${SC_ROWS}" = "1" ] && [ "${SC_LIST_N}" = "1" ]; then
  pass "受限用户 summary 只统计范围内 1 项（total=1、checks=1、列表 1 项）"
else
  fail "受限用户 summary 未按范围收敛：total=${SC_TOTAL}、checks=${SC_ROWS}、列表=${SC_LIST_N}（应都为 1）"
fi
SC_SCOPE_OK="$(printf '%s' "$SC_SUM" | jq -r --arg s "$S1" \
  '(.data.checks | length) >= 1 and ([.data.checks[].server_ids[]] | all(. == $s))')"
[ "$SC_SCOPE_OK" = "true" ] \
  && pass "summary.checks 只含范围内服务器（${S1}）" \
  || fail "summary.checks 混入范围外服务器：$(printf '%s' "$SC_SUM" | jq -c '[.data.checks[].server_ids]')"

# A12 假绿的根因：旧夹具里每个检测项只有一个目标，字段裁不裁都看不出来。
# 这里建一个「组内 + 组外」双目标的检测项，并用真实探测造出两种可区分的结果：
#   ① 受限用户执行（只跑范围内目标）→ S1 一条失败（target 是关闭端口 127.0.0.1:1）；
#   ② 管理员把 target 改到控制端端口后执行 → S1/S2 各一条成功；
#   ③ 只留组外目标再执行一次 → 全局最新结果落在 S2（旧实现会把这条当受限用户的 last_result 返回）。
# 检测项建为 enabled=false：定时调度器不插手，结果条数完全由下面三次 /run 决定。
# 期望口径：受限用户 2 条（1 成功 1 失败）= 50%，管理员 4 条（3 成功 1 失败）= 75%。
DUAL_ID="$(api POST /api/v1/checks "$(jq -cn --arg a "$S1" --arg b "$S2" \
  '{name:"双目标-裁剪",kind:"tcp",target:"127.0.0.1:1",interval_s:3600,enabled:false,server_ids:[$a,$b]}')" \
  "$ADMIN" | jq -r '.data.id // empty')"
[ -n "$DUAL_ID" ] && pass "创建双目标检测项（组内 ${S1} + 组外 ${S2}）" || fail "创建双目标检测项失败"

api POST "/api/v1/checks/${DUAL_ID}/run" "" "$SCOPED" > /dev/null
sleep 0.1
api PATCH "/api/v1/checks/${DUAL_ID}" "{\"target\":\"127.0.0.1:${PORT}\"}" "$ADMIN" > /dev/null
api POST "/api/v1/checks/${DUAL_ID}/run" "" "$ADMIN" > /dev/null
sleep 0.1
api POST "/api/v1/checks/${DUAL_ID}/targets" "{\"server_ids\":[\"$S2\"]}" "$ADMIN" > /dev/null
api POST "/api/v1/checks/${DUAL_ID}/run" "" "$ADMIN" > /dev/null
api POST "/api/v1/checks/${DUAL_ID}/targets" "{\"server_ids\":[\"$S1\",\"$S2\"]}" "$ADMIN" > /dev/null
sleep 0.1

SC_DUAL_LIST="$(api GET /api/v1/checks "" "$SCOPED")"
SC_DUAL="$(printf '%s' "$SC_DUAL_LIST" | jq -c --arg id "$DUAL_ID" '[.data[] | select(.id == $id)][0] // empty')"
AD_DUAL="$(api GET "/api/v1/checks/${DUAL_ID}" "" "$ADMIN")"
SC_DUAL_DETAIL="$(api GET "/api/v1/checks/${DUAL_ID}" "" "$SCOPED")"

[ -n "$SC_DUAL" ] && pass "双目标检测项对受限用户可见（组内目标仍在范围）" \
  || fail "双目标检测项未出现在受限用户列表：$(printf '%s' "$SC_DUAL_LIST" | jq -c '[.data[].name]')"
[ -z "$SC_DUAL" ] || {
  SC_DUAL_IDS="$(printf '%s' "$SC_DUAL" | jq -r '.server_ids | join(",")')"
  [ "$SC_DUAL_IDS" = "$S1" ] \
    && pass "受限用户列表 server_ids 只含组内服务器（${S1}）" \
    || fail "受限用户列表 server_ids 混入范围外服务器：${SC_DUAL_IDS}（应为 ${S1}）"
  SC_DUAL_LAST="$(printf '%s' "$SC_DUAL" | jq -r '.last_result.server_id // empty')"
  [ "$SC_DUAL_LAST" = "$S1" ] \
    && pass "受限用户列表 last_result 是组内那台的结果（不泄露组外探测内容）" \
    || fail "受限用户列表 last_result 来自 ${SC_DUAL_LAST:-空}（应为 ${S1}，旧实现会给出组外服务器）"
  SC_DUAL_AVAIL="$(printf '%s' "$SC_DUAL" | jq -r '.availability_24h')"
  SC_DUAL_RESULTS="$(printf '%s' "$SC_DUAL" | jq -r '.results_24h')"
  [ "$SC_DUAL_AVAIL" = "50" ] && [ "$SC_DUAL_RESULTS" = "2" ] \
    && pass "受限用户统计只算范围内结果（availability_24h=50、results_24h=2）" \
    || fail "受限用户统计混入组外结果：availability_24h=${SC_DUAL_AVAIL}、results_24h=${SC_DUAL_RESULTS}（应为 50/2）"
  printf '%s' "$SC_DUAL_LIST" | grep -q "$S2" \
    && fail "受限用户列表响应里出现组外服务器 UUID ${S2}（越权泄露）" \
    || pass "受限用户列表响应里没有组外服务器 UUID"
}

AD_DUAL_AVAIL="$(printf '%s' "$AD_DUAL" | jq -r '.data.availability_24h')"
AD_DUAL_RESULTS="$(printf '%s' "$AD_DUAL" | jq -r '.data.results_24h')"
AD_DUAL_LAST="$(printf '%s' "$AD_DUAL" | jq -r '.data.last_result.server_id // empty')"
# server_ids 必须按集合比：接口按 ORDER BY check_id ASC, server_id ASC 排序返回 UUID，
# 与「创建顺序 S1,S2」无关（按创建顺序比大约 50% 概率随机红）。
AD_DUAL_SIDS="$(printf '%s' "$AD_DUAL" | jq -r '.data.server_ids | sort | join(",")')"
AD_DUAL_SIDS_EXPECT="$(printf '%s\n%s\n' "$S1" "$S2" | LC_ALL=C sort | paste -sd, -)"
[ "$AD_DUAL_AVAIL" = "75" ] && [ "$AD_DUAL_RESULTS" = "4" ] \
  && pass "管理员统计仍是全局口径（availability_24h=75、results_24h=4）" \
  || fail "管理员统计被范围影响：availability_24h=${AD_DUAL_AVAIL}、results_24h=${AD_DUAL_RESULTS}（应为 75/4）"
# last_result 不钉死 S2：两个目标在同一次 /run 里并发探测，谁最后写入由调度决定（S1/S2 都可能）。
# 「越权点可观测」的正向证据改由确定成立的统计承担——管理员 results_24h=4 远大于受限用户的 2。
case "$AD_DUAL_LAST" in
  "$S1"|"$S2") AD_DUAL_LAST_OK=1 ;;
  *) AD_DUAL_LAST_OK=0 ;;
esac
[ "$AD_DUAL_LAST_OK" = "1" ] && [ "$AD_DUAL_SIDS" = "$AD_DUAL_SIDS_EXPECT" ] \
  && [ "$AD_DUAL_RESULTS" -gt "${SC_DUAL_RESULTS:-0}" ] \
  && pass "管理员看到两个目标且统计是全局口径（server_ids=${AD_DUAL_SIDS}、results_24h=${AD_DUAL_RESULTS}）" \
  || fail "管理员详情口径不对：server_ids=${AD_DUAL_SIDS}（应为 ${AD_DUAL_SIDS_EXPECT}）、last_result=${AD_DUAL_LAST}、results_24h=${AD_DUAL_RESULTS}"

SC_DET_IDS="$(printf '%s' "$SC_DUAL_DETAIL" | jq -r '.data.server_ids | join(",")')"
SC_DET_LAST="$(printf '%s' "$SC_DUAL_DETAIL" | jq -r '.data.last_result.server_id // empty')"
SC_DET_AVAIL="$(printf '%s' "$SC_DUAL_DETAIL" | jq -r '.data.availability_24h')"
[ "$SC_DET_IDS" = "$S1" ] && [ "$SC_DET_LAST" = "$S1" ] && [ "$SC_DET_AVAIL" = "50" ] \
  && pass "详情接口与列表同口径裁剪（server_ids/last_result/availability_24h 一致）" \
  || fail "详情与列表口径不一致：server_ids=${SC_DET_IDS}、last_result=${SC_DET_LAST}、availability_24h=${SC_DET_AVAIL}"

# ---------------------------------------------------------------- 11. 审计脱敏占位符
step "11. 审计 detail 对 env_json 显式落 __masked（A15）"
APP_UPDATE_DETAIL="$(printf '%s' "$AUDIT_APP" | jq -r '[.data.logs[] | select(.action=="app.update")][0].detail // empty')"
ENV_IN_DETAIL="$(printf '%s' "$APP_UPDATE_DETAIL" | jq -r '.env_json // "（缺失）"' 2>/dev/null)"
[ "$ENV_IN_DETAIL" = "__masked" ] \
  && pass "审计 detail 中 env_json 已替换为 __masked（占位符，非仅删除）" \
  || fail "审计 detail 的 env_json 未显式脱敏：${ENV_IN_DETAIL}（detail=${APP_UPDATE_DETAIL}）"

# ---------------------------------------------------------------- 12. 模板正文可追溯
step "12. 模板正文改动可追溯到审计（A16）"
TPL_MARK="e2e-a16-marker-$$"
TPL_JSON="$(api POST /api/v1/templates \
  "$(jq -n --arg c "services: {}
# ${TPL_MARK}" '{name:"审计模板",kind:"compose",content:$c}')" "$ADMIN")"
TPL_ID="$(sget "$TPL_JSON" '.data.id')"
[ -n "$TPL_ID" ] && pass "创建模板 ${TPL_ID}" || fail "创建模板失败：$TPL_JSON"
api PATCH "/api/v1/templates/$TPL_ID" \
  "$(jq -n --arg c "services: {}
# ${TPL_MARK}" '{content:$c}')" "$ADMIN" >/dev/null
AUDIT_TPL="$(api GET "/api/v1/audit-logs?limit=200" "" "$ADMIN")"
TPL_REC="$(printf '%s' "$AUDIT_TPL" | jq '[.data.logs[] | select(.action=="template.update")] | length')"
[ "${TPL_REC:-0}" -ge 1 ] && pass "审计记录了 template.update（${TPL_REC} 条）" \
  || fail "审计缺少 template.update 记录：$(printf '%s' "$AUDIT_TPL" | jq -c '[.data.logs[].action]')"
TPL_DETAIL="$(printf '%s' "$AUDIT_TPL" | jq -r '[.data.logs[] | select(.action=="template.update")][0].detail // empty')"
printf '%s' "$TPL_DETAIL" | grep -qF -- "$TPL_MARK" \
  && pass "审计 detail 保留了模板正文（改动内容可追溯）" \
  || fail "审计 detail 未保留正文标记 ${TPL_MARK}：${TPL_DETAIL}"
if printf '%s' "$TPL_DETAIL" | grep -q -- "__masked"; then
  fail "审计把模板正文当敏感字段脱敏了（正文不应被掩码）：${TPL_DETAIL}"
else
  pass "模板正文未被误判为敏感字段"
fi

# ---------------------------------------------------------------- 13. 凭证引用冲突
step "13. 凭证仍被引用时删除给出精确冲突（A17）"
INUSE_CRED="$(api POST /api/v1/credentials \
  '{"name":"被引用凭证","kind":"password","username":"inuse","password":"InUse-Pass-1"}' "$ADMIN" \
  | jq -r '.data.id // empty')"
[ -n "$INUSE_CRED" ] && pass "创建被引用凭证 ${INUSE_CRED}" || fail "创建被引用凭证失败"
INUSE_N=0
for i in 1 2 3; do
  api POST /api/v1/servers "$(jq -n --arg c "$INUSE_CRED" --arg n "引用-${i}" \
    '{name:$n,host:"10.20.0.9",mode:"ssh",credential_id:$c}')" "$ADMIN" >/dev/null
  INUSE_N=$((INUSE_N + 1))
done
DEL_CRED_CODE="$(code DELETE "/api/v1/credentials/$INUSE_CRED" "" "$ADMIN")"
DEL_CRED_MSG="$(api DELETE "/api/v1/credentials/$INUSE_CRED" "" "$ADMIN" | jq -r '.error.message // empty')"
[ "$DEL_CRED_CODE" = "409" ] && pass "被引用的凭证删除返回 409" \
  || fail "被引用凭证删除应 409，实际 ${DEL_CRED_CODE}：${DEL_CRED_MSG}"
if printf '%s' "$DEL_CRED_MSG" | grep -q "3" && printf '%s' "$DEL_CRED_MSG" | grep -q "服务器"; then
  pass "409 文案含引用台数与资源名：${DEL_CRED_MSG}"
else
  fail "409 文案未说明「被 ${INUSE_N} 台服务器引用」：${DEL_CRED_MSG}"
fi

# ---------------------------------------------------------------- 14. Agent 升级授权
step "14. Agent 升级：范围内可提交、范围外 404、无权限 403（A30）"
UPG_SHA="$(printf 'c%.0s' $(seq 1 64))"
UPG_BODY="$(jq -n --arg s "$UPG_SHA" \
  '{version:"v9.9.9",url:"http://127.0.0.1:1/leadstack-agent_v9.9.9_linux_amd64.tar.gz",sha256:$s}')"
UPG_ROLE="$(api POST /api/v1/roles \
  "{\"name\":\"升级员\",\"permissions\":[\"server.read\",\"agent.upgrade\"],\"scope\":{\"mode\":\"groups\",\"group_ids\":[\"$GROUP\"],\"server_ids\":[]}}" \
  "$ADMIN" | jq -r '.data.key // empty')"
api POST /api/v1/users "{\"username\":\"upgrader\",\"password\":\"Upgrader-123\",\"role\":\"$UPG_ROLE\"}" "$ADMIN" >/dev/null
UPGRADER="$(login upgrader "Upgrader-123")"
[ -n "$UPGRADER" ] && pass "持 agent.upgrade 的受限用户登录成功" || fail "升级员登录失败"

OUT_UPG_CODE="$(code POST "/api/v1/servers/$S2/agent-upgrade" "$UPG_BODY" "$UPGRADER")"
OUT_UPG_ERR="$(api POST "/api/v1/servers/$S2/agent-upgrade" "$UPG_BODY" "$UPGRADER" | jq -r '.error.code // empty')"
[ "$OUT_UPG_CODE" = "404" ] && [ "$OUT_UPG_ERR" = "not_found" ] \
  && pass "范围外服务器升级返回 404/not_found（不泄露存在性）" \
  || fail "范围外升级应 404/not_found，实际 ${OUT_UPG_CODE}/${OUT_UPG_ERR}"

IN_UPG_CODE="$(code POST "/api/v1/servers/$S1/agent-upgrade" "$UPG_BODY" "$UPGRADER")"
[ "$IN_UPG_CODE" = "409" ] \
  && pass "范围内服务器通过范围校验（agent 未在线 → 409）" \
  || fail "范围内升级应进入 handler（未在线 409），实际 ${IN_UPG_CODE}"

NO_UPG_CODE="$(code POST "/api/v1/servers/$S1/agent-upgrade" "$UPG_BODY" "$SCOPED")"
[ "$NO_UPG_CODE" = "403" ] && pass "无 agent.upgrade 权限返回 403" \
  || fail "无 agent.upgrade 应 403，实际 ${NO_UPG_CODE}"

# ---------------------------------------------------------------- 15. 更新检查资产分类
step "15. 更新检查：agent 归档分类与 sha256（A29）"
UPD="$(api POST /api/v1/system/update/check "" "$ADMIN")"
UPD_LATEST="$(sget "$UPD" '.data.latest')"
[ "$UPD_LATEST" = "v9.9.9" ] && pass "已从 mock 拿到最新版本（${UPD_LATEST}）" \
  || fail "检查更新未取到 mock 版本：${UPD}"
AGENT_N="$(printf '%s' "$UPD" | jq '(.data.agent_assets // []) | length')"
[ "$AGENT_N" = "2" ] && pass "agent_assets 收录 2 个 agent 归档" \
  || fail "agent_assets 应为 2，实际 ${AGENT_N}：$(printf '%s' "$UPD" | jq -c '.data.agent_assets')"
AGENT_SHAPE_OK="$(printf '%s' "$UPD" | jq -r --arg base "$UPDATE_API_BASE" '
  (.data.agent_assets // []) as $a
  | ($a | length) == 2
    and ([$a[] | (.name | startswith("leadstack-agent_"))
               and (.url | startswith($base))
               and (.sha256 | length == 64)
               and ((.os | length) > 0) and ((.arch | length) > 0)] | all)')"
[ "$AGENT_SHAPE_OK" = "true" ] \
  && pass "每个 agent 归档都带 name/os/arch/url 与 64 位 sha256" \
  || fail "agent_assets 字段不完整：$(printf '%s' "$UPD" | jq -c '.data.agent_assets')"
EXP_SHA_DARWIN="3f1e$(printf 'a%.0s' $(seq 1 60))"
EXP_SHA_LINUX="9b7d$(printf 'b%.0s' $(seq 1 60))"
SHA_DARWIN="$(printf '%s' "$UPD" | jq -r '[.data.agent_assets[] | select(.os=="darwin" and .arch=="arm64")][0].sha256 // empty')"
SHA_LINUX="$(printf '%s' "$UPD" | jq -r '[.data.agent_assets[] | select(.os=="linux" and .arch=="amd64")][0].sha256 // empty')"
if [ "$SHA_DARWIN" = "$EXP_SHA_DARWIN" ] && [ "$SHA_LINUX" = "$EXP_SHA_LINUX" ]; then
  pass "sha256 按文件名精确取自 checksums.txt（darwin/arm64 与 linux/amd64 各自对得上）"
else
  fail "sha256 与 checksums.txt 不匹配：darwin/arm64=${SHA_DARWIN:-空}、linux/amd64=${SHA_LINUX:-空}"
fi
CTRL_OK="$(printf '%s' "$UPD" | jq -r '
  (.data.assets // []) as $c
  | ($c | length) == 2 and ([$c[] | (.name | startswith("leadstack-agent_") | not)] | all)')"
[ "$CTRL_OK" = "true" ] && pass "assets 只含控制端归档（不含 agent 包）" \
  || fail "assets 混入 agent 包：$(printf '%s' "$UPD" | jq -c '.data.assets')"
HAS_UPD="$(sget "$UPD" '.data.has_update')"
[ "$HAS_UPD" = "true" ] && pass "has_update=true（v9.9.9 > 当前版本）" \
  || fail "has_update 应为 true，实际 ${HAS_UPD}"

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
