#!/usr/bin/env bash
# LeadStack 宝塔面板部署通道端到端验收
#
# 覆盖 docs/ARCHITECTURE.md §57：面板凭据 / 连通性测试 / 应用 provider / 服务器绑定 /
# 建站 → 上传 zip → 解压 的完整链路，以及「面板不可用 / 制品不是 zip」两类失败路径。
#
# 用法：make build && bash scripts/verify_baota.sh
#
# 说明：用一个**校验签名**的 mock 面板（python）代替真实宝塔，验证请求签名、参数与调用顺序；
#      真实面板请在「设置 → API 接口」开启 API 并把控制端出口 IP 加白名单后，用控制台「测试连接」验证。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18960}"
PANEL_PORT="${PANEL_PORT:-18961}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-baota-pass"
API_KEY="test-api-key"
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
WORK="$(mktemp -d "$TMP_CANDIDATE/leadstack-baota.XXXXXX")"
DATA_DIR="$WORK/data"
LEAD_PID=""; MOCK_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$WORK"
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

# ---------------------------------------------------------------- mock 宝塔面板
step "0. 启动 mock 宝塔面板"
cat > "$WORK/mock.py" <<'PY'
import hashlib, json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

PORT = int(sys.argv[1]); API_KEY = sys.argv[2]; LOG = sys.argv[3]
SITES = []

def sign(rt):
    token = hashlib.md5(API_KEY.encode()).hexdigest()
    return hashlib.md5((token + rt).encode()).hexdigest()

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):  # 安静
        pass

    def _json(self, obj):
        b = json.dumps(obj).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(b))); self.end_headers(); self.wfile.write(b)

    def do_POST(self):
        n = int(self.headers.get('Content-Length') or 0)
        form = parse_qs(self.rfile.read(n).decode('utf-8', 'replace'))
        g = lambda k: (form.get(k) or [''])[0]
        rt = g('request_time')
        if g('request_token') != sign(rt):
            self._json({"status": False, "msg": "签名校验失败"}); return
        u = urlparse(self.path); act = (parse_qs(u.query).get('action') or [''])[0]
        with open(LOG, 'a') as f:
            f.write(f"{act}\t{g('path')}\t{g('sfile')}\t{g('dfile')}\t{g('filename')}\t{g('port')}\n")
        if act == 'GetSystemTotal':
            self._json({"cpuNum": 8, "memTotal": 16 << 30})
        elif act == 'getData':
            self._json({"data": SITES})
        elif act == 'AddSite':
            try:
                wn = json.loads(g('webname'))
            except Exception:
                wn = {}
            SITES.append({"name": wn.get("domain", ""), "path": g('path'), "status": "1"})
            self._json({"siteStatus": True})
        elif act in ('SaveFileBody', 'Unzip', 'Zip', 'DeleteFile'):
            self._json({"status": True})
        else:
            self._json({"status": False, "msg": "unknown action " + act})

HTTPServer(('127.0.0.1', PORT), H).serve_forever()
PY
: > "$WORK/panel.log"
python3 "$WORK/mock.py" "$PANEL_PORT" "$API_KEY" "$WORK/panel.log" > "$WORK/mock.out" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 30); do
  curl -s -o /dev/null -X POST "http://127.0.0.1:${PANEL_PORT}/system?action=GetSystemTotal" && break
  sleep 0.2
done
pass "mock 面板已启动（端口 ${PANEL_PORT}，签名校验开启）"

# ---------------------------------------------------------------- 控制端
step "1. 启动控制端"
mkdir -p "$DATA_DIR"
env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
    LEADSTACK_DATA_DIR="$DATA_DIR" \
    LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
    ./bin/leadstack > "$WORK/lead.log" 2>&1 &
LEAD_PID=$!
for _ in $(seq 1 80); do [ "$(code GET /healthz)" = "200" ] && break; sleep 0.3; done
[ "$(code GET /healthz)" = "200" ] && pass "控制端已启动（迁移应到 v9）" || { fail "控制端启动失败"; tail -20 "$WORK/lead.log"; exit 1; }
ADMIN="$(login admin "$ADMIN_PASS")"
[ -n "$ADMIN" ] && pass "管理员登录成功" || { fail "登录失败"; exit 1; }

# ---------------------------------------------------------------- 凭据
step "2. 宝塔面板凭据与连通性测试"
PANEL_URL="http://127.0.0.1:${PANEL_PORT}"
CRED="$(api POST /api/v1/credentials "{\"name\":\"bt-panel\",\"kind\":\"baota\",\"panel_url\":\"$PANEL_URL\",\"api_key\":\"$API_KEY\"}" "$ADMIN")"
CRED_ID="$(sget "$CRED" '.data.id')"
[ -n "$CRED_ID" ] && pass "创建 baota 凭据成功" || fail "创建凭据失败：$CRED"
[ "$(sget "$CRED" '.data.kind')" = "baota" ] && pass "凭据类型为 baota" || fail "凭据类型不符"

TEST_OK="$(api POST "/api/v1/credentials/$CRED_ID/test" "" "$ADMIN")"
[ "$(sget "$TEST_OK" '.data.ok')" = "true" ] && pass "连通性测试通过（面板可访问、签名正确）" || fail "连通性测试失败：$TEST_OK"

BAD="$(api POST /api/v1/credentials "{\"name\":\"bt-bad\",\"kind\":\"baota\",\"panel_url\":\"$PANEL_URL\",\"api_key\":\"wrong-key\"}" "$ADMIN")"
BAD_ID="$(sget "$BAD" '.data.id')"
BAD_TEST="$(api POST "/api/v1/credentials/$BAD_ID/test" "" "$ADMIN")"
[ "$(sget "$BAD_TEST" '.data.ok')" = "false" ] && pass "错误密钥的测试返回 ok=false（不误报成功）" || fail "错误密钥应失败：$BAD_TEST"

MISSING_FIELD="$(code POST /api/v1/credentials '{"name":"bt-no-key","kind":"baota","panel_url":"https://x:8888"}' "$ADMIN")"
[ "$MISSING_FIELD" = "400" ] && pass "缺 api_key 被拒（400）" || fail "缺 api_key 应 400，实际 $MISSING_FIELD"

# ---------------------------------------------------------------- 服务器与应用
step "3. 服务器绑定面板凭据 / 应用选择 baota"
SRV="$(api POST /api/v1/servers "{\"name\":\"bt-host\",\"host\":\"10.9.9.9\",\"mode\":\"ssh\",\"panel_credential_id\":\"$CRED_ID\"}" "$ADMIN")"
SRV_ID="$(sget "$SRV" '.data.id')"
[ -n "$SRV_ID" ] && pass "创建服务器并绑定面板凭据" || fail "创建服务器失败：$SRV"
[ "$(sget "$SRV" '.data.panel_credential_id')" = "$CRED_ID" ] && pass "服务器响应回显 panel_credential_id" || fail "服务器未回显面板凭据"

SSH_CRED="$(api POST /api/v1/credentials '{"name":"ssh-one","kind":"password","username":"root","password":"x"}' "$ADMIN")"
SSH_ID="$(sget "$SSH_CRED" '.data.id')"
MISBIND="$(code PATCH "/api/v1/servers/$SRV_ID" "{\"panel_credential_id\":\"$SSH_ID\"}" "$ADMIN")"
[ "$MISBIND" = "400" ] && pass "把 SSH 凭据绑成面板凭据被拒（400）" || fail "类型校验失效，实际 $MISBIND"

APP="$(api POST /api/v1/apps '{"name":"商城","slug":"shop","provider":"baota","site_name":"shop.example.com","site_port":8080,"site_type":"static"}' "$ADMIN")"
APP_ID="$(sget "$APP" '.data.id')"
[ "$(sget "$APP" '.data.provider')" = "baota" ] && pass "应用创建为 baota 部署方式" || fail "应用 provider 不符：$APP"
[ "$(sget "$APP" '.data.site_port')" = "8080" ] && pass "站点端口已保存" || fail "站点端口未保存：$APP"
BAD_PROVIDER="$(code POST /api/v1/apps '{"name":"x","slug":"x","provider":"pm2"}' "$ADMIN")"
[ "$BAD_PROVIDER" = "400" ] && pass "非法 provider 被拒（400）" || fail "非法 provider 应 400，实际 $BAD_PROVIDER"

# ---------------------------------------------------------------- 制品与发布
step "4. 发布：建站 → 上传 → 解压"
python3 - "$WORK/app.zip" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], 'w') as z:
    z.writestr('index.html', '<html>ok</html>')
PY
ART="$(curl -sS -X POST "${BASE}/api/v1/artifacts/upload" -H "Authorization: Bearer ${ADMIN}" -F "file=@$WORK/app.zip;type=application/zip")"
ART_ID="$(sget "$ART" '.data.id')"
[ -n "$ART_ID" ] && pass "上传 zip 制品成功" || fail "上传制品失败：$ART"

TMPL="$(api POST /api/v1/templates '{"name":"占位","kind":"binary","content":""}' "$ADMIN")"
TMPL_ID="$(sget "$TMPL" '.data.id')"
[ -n "$TMPL_ID" ] && pass "创建模板成功" || fail "创建模板失败：$TMPL"

REL="$(api POST /api/v1/releases "{\"app_id\":\"$APP_ID\",\"template_id\":\"$TMPL_ID\",\"version\":\"1.0.0\",\"artifact_id\":\"$ART_ID\",\"server_ids\":[\"$SRV_ID\"]}" "$ADMIN")"
REL_ID="$(sget "$REL" '.data.release.id')"
[ -n "$REL_ID" ] && pass "创建发布成功" || fail "创建发布失败：$REL"

STATUS=""
for _ in $(seq 1 60); do
  STATUS="$(sget "$(api GET "/api/v1/releases/$REL_ID" "" "$ADMIN")" '.data.release.status')"
  case "$STATUS" in success|failed) break;; esac
  sleep 0.3
done
[ "$STATUS" = "success" ] && pass "发布成功（面板通道）" || fail "发布状态应为 success，实际 $STATUS"

RUNS="$(api GET "/api/v1/releases/$REL_ID/runs" "" "$ADMIN")"
RUN_MSG="$(sget "$RUNS" '.data.runs[0].message')"
[ -n "$RUN_MSG" ] && pass "发布任务报文：$RUN_MSG" || fail "发布任务缺少 Message"

LOG="$WORK/panel.log"
grep -q $'GetSystemTotal' "$LOG" && pass "面板收到 GetSystemTotal（连接校验）" || fail "面板未收到 GetSystemTotal"
grep -q $'AddSite' "$LOG" && pass "面板收到 AddSite（建站）" || fail "面板未收到 AddSite"
grep -q 'shop.example.com' "$LOG" && pass "建站使用应用配置的站点名" || fail "建站站点名不符"
grep -q $'SaveFileBody' "$LOG" && pass "面板收到 SaveFileBody（上传 zip）" || fail "面板未收到上传"
grep -q $'Unzip' "$LOG" && pass "面板收到 Unzip（解压到站点目录）" || fail "面板未收到解压"

# ---------------------------------------------------------------- 失败路径
step "5. 失败路径"
NOT_ZIP="$(curl -sS -X POST "${BASE}/api/v1/artifacts/upload" -H "Authorization: Bearer ${ADMIN}" -F "file=@$WORK/mock.py;type=text/plain")"
NOT_ZIP_ID="$(sget "$NOT_ZIP" '.data.id')"
REL2="$(api POST /api/v1/releases "{\"app_id\":\"$APP_ID\",\"template_id\":\"$TMPL_ID\",\"version\":\"1.0.1\",\"artifact_id\":\"$NOT_ZIP_ID\",\"server_ids\":[\"$SRV_ID\"]}" "$ADMIN")"
REL2_ID="$(sget "$REL2" '.data.release.id')"
STATUS2=""
for _ in $(seq 1 60); do
  STATUS2="$(sget "$(api GET "/api/v1/releases/$REL2_ID" "" "$ADMIN")" '.data.release.status')"
  case "$STATUS2" in success|failed) break;; esac
  sleep 0.3
done
[ "$STATUS2" = "failed" ] && pass "非 zip 制品被拒（发布失败）" || fail "非 zip 应失败，实际 $STATUS2"
RUN2_MSG="$(sget "$(api GET "/api/v1/releases/$REL2_ID/runs" "" "$ADMIN")" '.data.runs[0].message')"
printf '%s' "$RUN2_MSG" | grep -q "zip" && pass "失败信息明确指出需要 zip：$RUN2_MSG" || fail "失败信息不明确：$RUN2_MSG"

# 面板坏凭据：把服务器改绑到错误密钥的凭据
api PATCH "/api/v1/servers/$SRV_ID" "{\"panel_credential_id\":\"$BAD_ID\"}" "$ADMIN" > /dev/null
REL3="$(api POST /api/v1/releases "{\"app_id\":\"$APP_ID\",\"template_id\":\"$TMPL_ID\",\"version\":\"1.0.2\",\"artifact_id\":\"$ART_ID\",\"server_ids\":[\"$SRV_ID\"]}" "$ADMIN")"
REL3_ID="$(sget "$REL3" '.data.release.id')"
STATUS3=""
for _ in $(seq 1 60); do
  STATUS3="$(sget "$(api GET "/api/v1/releases/$REL3_ID" "" "$ADMIN")" '.data.release.status')"
  case "$STATUS3" in success|failed) break;; esac
  sleep 0.3
done
[ "$STATUS3" = "failed" ] && pass "错误面板密钥导致发布失败（不静默成功）" || fail "错误密钥应失败，实际 $STATUS3"
RUN3_MSG="$(sget "$(api GET "/api/v1/releases/$REL3_ID/runs" "" "$ADMIN")" '.data.runs[0].message')"
printf '%s' "$RUN3_MSG" | grep -q "面板" && pass "失败信息指明面板连接失败：$RUN3_MSG" || fail "失败信息不明确：$RUN3_MSG"

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
