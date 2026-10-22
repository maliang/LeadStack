#!/usr/bin/env bash
# LeadStack 脚本模式（deploy_mode=script）端到端验收
#
# 场景来自 tuoluojiang 的真实更新方式：宝塔直装（非 Docker），更新 = git pull 后 `start.sh restart`。
# 脚本模式不需要制品：发布时在目标机执行 binary 模板的内容；失败/健康检查不通过时执行回滚脚本。
#
# devssh 让命令在本机真实执行，断言检查的是真实 git 仓库状态与文件。
# 用法：make build && make devssh && bash scripts/verify_deploy_script.sh
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18975}"
HEALTH_PORT=$((PORT + 1))
SSH_PORT="${SSH_PORT:-0}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-script-pass"

TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
DATA_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-script.XXXXXX")"
SRC_REPO="$DATA_DIR/src-repo"
SITE_DIR="$DATA_DIR/site"
TARGET="$SITE_DIR/tuoluojiang"
LEAD_PID=""; DEVSSH_PID=""; HEALTH_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$HEALTH_PID" "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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
code() {
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -o /dev/null -w '%{http_code}' -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
sget() { local v; v="$(printf '%s' "$1" | jq -r "$2" 2>/dev/null)"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }

wait_release() {
  local id="$1" tok="$2" want="$3" max="${4:-90}" i st
  for i in $(seq 1 "$max"); do
    st="$(sget "$(api GET "/api/v1/releases/${id}" "" "$tok")" '.data.release.status')"
    [ "$st" = "$want" ] && { echo "$st"; return 0; }
    case "$st" in failed|success|canceled) echo "$st"; return 1;; esac
    sleep 1
  done
  echo "${st:-timeout}"; return 1
}

create_release() { # <app> <tmpl> <version> ; 脚本模式不带 artifact
  local body resp id
  body="$(jq -n --arg a "$1" --arg t "$2" --arg v "$3" --arg srv "$SRV_ID" \
    '{app_id:$a,template_id:$t,version:$v,server_ids:[$srv]}')"
  resp="$(api POST /api/v1/releases "$body" "$TOKEN")"
  id="$(sget "$resp" '.data.release.id')"
  [ -z "$id" ] && echo "RELEASE_ERROR: $resp" >&2
  printf '%s' "$id"
}

run_msg() { sget "$(api GET "/api/v1/releases/$1/runs" "" "$TOKEN")" '.data.runs[0].message'; }
run_step() { sget "$(api GET "/api/v1/releases/$1/runs" "" "$TOKEN")" '.data.runs[0].failed_step'; }

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 环境
step "1. 准备 git 仓库 / 健康检查服务 / devssh / 控制端"
mkdir -p "$SITE_DIR"
git init -q "$SRC_REPO"
git -C "$SRC_REPO" config user.email "verify@example.com"
git -C "$SRC_REPO" config user.name "verify"
mkdir -p "$SRC_REPO/public"
echo '<?php echo "v1";' > "$SRC_REPO/public/index.php"
printf '#!/bin/bash\necho restart >> "$(dirname "$0")/restart.log"\n' > "$SRC_REPO/start.sh"
chmod +x "$SRC_REPO/start.sh"
git -C "$SRC_REPO" add -A && git -C "$SRC_REPO" commit -qm "v1"
C1="$(git -C "$SRC_REPO" rev-parse HEAD)"
git clone -q "$SRC_REPO" "$TARGET"
pass "已准备源仓库与站点 checkout（初始 commit ${C1:0:7}）"

cat > "$DATA_DIR/health.py" <<'PY'
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        self.send_response(200); self.send_header('Content-Type','application/json')
        self.end_headers(); self.wfile.write(b'{"status":"up"}')
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
python3 "$DATA_DIR/health.py" "$HEALTH_PORT" > /dev/null 2>&1 &
HEALTH_PID=$!
for _ in $(seq 1 40); do curl -s -o /dev/null "http://127.0.0.1:${HEALTH_PORT}/healthz" && break; sleep 0.2; done
curl -s -o /dev/null "http://127.0.0.1:${HEALTH_PORT}/healthz" && pass "健康检查服务已监听 ${HEALTH_PORT}" || { fail "健康检查服务未就绪"; exit 1; }

ssh-keygen -q -t ed25519 -N '' -f "$DATA_DIR/id" >/dev/null 2>&1
./bin/leadstack-devssh --addr "127.0.0.1:${SSH_PORT}" --user devuser --password devpass \
  --authorized-key-file "$DATA_DIR/id.pub" --port-file "$DATA_DIR/ssh.port" \
  > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SPORT=""
for _ in $(seq 1 40); do SPORT="$(tr -d '[:space:]' < "$DATA_DIR/ssh.port" 2>/dev/null)"; [ -n "$SPORT" ] && break; sleep 0.25; done
[ -n "$SPORT" ] && pass "devssh 已就绪" || { fail "devssh 未就绪"; exit 1; }

env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
    LEADSTACK_DATA_DIR="$DATA_DIR/data" \
    LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
    LEADSTACK_SSH_INTERVAL="30s" \
    ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
for _ in $(seq 1 80); do [ "$(code GET /healthz)" = "200" ] && break; sleep 0.3; done
[ "$(code GET /healthz)" = "200" ] && pass "控制端就绪" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log"; exit 1; }

LOGIN_BODY="$(jq -n --arg u admin --arg p "$ADMIN_PASS" '{username:$u,password:$p}')"
TOKEN="$(sget "$(api POST /api/v1/auth/login "$LOGIN_BODY")" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败"; exit 1; }

CRED_BODY="$(jq -n --arg k "$(cat "$DATA_DIR/id")" '{name:"script-key",kind:"key",username:"devuser",private_key:$k}')"
CRED_ID="$(sget "$(api POST /api/v1/credentials "$CRED_BODY" "$TOKEN")" '.data.id')"
SRV_BODY="$(jq -n --argjson port "$SPORT" --arg cid "$CRED_ID" '{name:"script-node",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid}')"
SRV_ID="$(sget "$(api POST /api/v1/servers "$SRV_BODY" "$TOKEN")" '.data.id')"
[ -n "$SRV_ID" ] && pass "目标服务器已创建" || { fail "创建服务器失败"; exit 1; }

# ---------------------------------------------------------------- 应用
step "2. 脚本模式应用（无制品）"
UPDATE_SCRIPT="$(cat <<TPL
#!/bin/bash
set -eu
cd "$TARGET"
git pull --ff-only
bash start.sh restart
TPL
)"
ROLLBACK_SCRIPT="$(cat <<TPL
#!/bin/bash
set -eu
cd "$TARGET"
git reset --hard HEAD@{1}
echo rollback >> rollback.log
bash start.sh restart
TPL
)"
TPL_BODY="$(jq -n --arg c "$UPDATE_SCRIPT" '{name:"tuoluojiang-update",kind:"binary",content:$c}')"
TPL_ID="$(sget "$(api POST /api/v1/templates "$TPL_BODY" "$TOKEN")" '.data.id')"
[ -n "$TPL_ID" ] && pass "更新脚本模板已创建" || fail "创建模板失败"

APP_BODY="$(jq -n --arg target "$TARGET" --arg rb "$ROLLBACK_SCRIPT" --argjson hport "$HEALTH_PORT" \
  '{name:"陀螺匠",slug:"tuoluojiang",provider:"builtin",deploy_mode:"script",target_dir:$target,root:$target,
    rollback_script:$rb,healthcheck_path:"/healthz",healthcheck_port:$hport}')"
APP_RESP="$(api POST /api/v1/apps "$APP_BODY" "$TOKEN")"
APP_ID="$(sget "$APP_RESP" '.data.id')"
[ -n "$APP_ID" ] && pass "应用已创建（script 模式，无制品）" || { fail "创建应用失败：$APP_RESP"; exit 1; }
[ "$(sget "$APP_RESP" '.data.deploy_mode')" = "script" ] && pass "deploy_mode=script 已保存" || fail "deploy_mode 未保存"

NONBIN="$(code POST /api/v1/releases "$(jq -n --arg a "$APP_ID" --arg srv "$SRV_ID" '{app_id:$a,template_id:"nonexistent",version:"0.0.1",server_ids:[$srv]}')" "$TOKEN")"
[ "$NONBIN" = "404" ] && pass "引用不存在的模板返回 404" || fail "非法模板应 404，实际 $NONBIN"

REL1="$(create_release "$APP_ID" "$TPL_ID" "1.0.0")"
[ -n "$REL1" ] && pass "首次发布已创建（不带 artifact）" || fail "创建发布失败"
S1="$(wait_release "$REL1" "$TOKEN" success 90)"
[ "$S1" = "success" ] && pass "首次脚本发布成功" || fail "首次发布失败（${S1}）：$(run_msg "$REL1")"
[ "$(git -C "$TARGET" rev-parse HEAD)" = "$C1" ] && pass "站点仓库在目标 commit 上" || fail "仓库 commit 不符"
[ "$(wc -l < "$TARGET/restart.log" 2>/dev/null | tr -d ' ')" = "1" ] && pass "start.sh restart 已执行 1 次" || fail "重启脚本未执行"

# 第二次：上游有新提交 → git pull 生效
echo '<?php echo "v2";' > "$SRC_REPO/public/index.php"
git -C "$SRC_REPO" add -A && git -C "$SRC_REPO" commit -qm "v2"
C2="$(git -C "$SRC_REPO" rev-parse HEAD)"
REL2="$(create_release "$APP_ID" "$TPL_ID" "1.0.1")"
S2="$(wait_release "$REL2" "$TOKEN" success 90)"
[ "$S2" = "success" ] && pass "第二次发布成功（git pull）" || fail "第二次发布失败（${S2}）：$(run_msg "$REL2")"
[ "$(git -C "$TARGET" rev-parse HEAD)" = "$C2" ] && pass "git pull 后已到新 commit ${C2:0:7}" || fail "git pull 未生效"
grep -q 'v2' "$TARGET/public/index.php" && pass "站点代码已更新到 v2" || fail "站点代码未更新"

# ---------------------------------------------------------------- 失败与回滚
step "3. 失败注入：脚本失败 → 执行回滚脚本"
BAD_SCRIPT="$(cat <<TPL
#!/bin/bash
set -eu
cd "$TARGET"
git pull --ff-only
exit 1
TPL
)"
BAD_TPL="$(jq -n --arg c "$BAD_SCRIPT" '{name:"tuoluojiang-update-bad",kind:"binary",content:$c}')"
BAD_TPL_ID="$(sget "$(api POST /api/v1/templates "$BAD_TPL" "$TOKEN")" '.data.id')"
# 先让上游前进一个提交，这样失败脚本里的 git pull 会真的把站点带到新版本，
# 回滚脚本的 `git reset --hard HEAD@{1}` 才有东西可回。
echo '<?php echo "v3-bad";' > "$SRC_REPO/public/index.php"
git -C "$SRC_REPO" add -A && git -C "$SRC_REPO" commit -qm "v3-bad"
C3="$(git -C "$SRC_REPO" rev-parse HEAD)"
REL3="$(create_release "$APP_ID" "$BAD_TPL_ID" "1.0.2")"
S3="$(wait_release "$REL3" "$TOKEN" failed 90)"
[ "$S3" = "failed" ] && pass "脚本失败导致发布失败" || fail "脚本失败未让发布失败（${S3}）"
run_msg "$REL3" | grep -q "run-deploy-script" && pass "失败步骤记录为 run-deploy-script" || fail "失败步骤不符：$(run_step "$REL3")"
if [ -f "$TARGET/rollback.log" ] && grep -q rollback "$TARGET/rollback.log"; then
  pass "回滚脚本已执行"
else
  echo "DEBUG head=$(git -C "$TARGET" rev-parse --short HEAD) files=$(ls -A "$TARGET" | tr '\n' ' ')"
  echo "DEBUG msg=$(run_msg "$REL3")"
  echo "DEBUG rollback-logs=$(api GET "/api/v1/releases/$REL3/runs/$SRV_ID/logs" "" "$TOKEN" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']['logs']
print(''.join(x['chunk'] for x in d)[-1200:])")"
  fail "回滚脚本未执行"
fi
[ "$(git -C "$TARGET" rev-parse HEAD)" = "$C2" ] && pass "回滚后 commit 回到发布前的版本（${C2:0:7}）" || {
  fail "回滚后 commit 不正确（当前 $(git -C "$TARGET" rev-parse --short HEAD)，期望 ${C2:0:7}，坏版本 ${C3:0:7}）"
}

step "4. 健康检查不通过 → 执行回滚脚本"
api PATCH "/api/v1/apps/$APP_ID" "{\"healthcheck_port\":$((HEALTH_PORT + 9))}" "$TOKEN" >/dev/null
echo '<?php echo "v3";' > "$SRC_REPO/public/index.php"
git -C "$SRC_REPO" add -A && git -C "$SRC_REPO" commit -qm "v3"
REL4="$(create_release "$APP_ID" "$TPL_ID" "1.0.3")"
S4="$(wait_release "$REL4" "$TOKEN" failed 120)"
[ "$S4" = "failed" ] && pass "健康检查失败导致发布失败" || fail "健康检查未生效（${S4}）：$(run_msg "$REL4")"
[ "$(run_step "$REL4")" = "health-check" ] && pass "失败步骤记录为 health-check" || fail "失败步骤不符：$(run_step "$REL4")"
grep -q rollback "$TARGET/rollback.log" && pass "健康检查失败也执行了回滚脚本" || fail "回滚脚本未执行"

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
