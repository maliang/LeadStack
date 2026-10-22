#!/usr/bin/env bash
# LeadStack 部署 v2 端到端验收（健康检查 / 接管既有目录 / 首版初始化 / 环境变量 / 保留路径 / 回滚）
#
# 用两个真实项目的**部署形态**做样本（见 docs/reports/deploy-flexibility.md）：
#   A. nengtan（慧碳云枢）：spring boot jar + 两个前端 + systemd 服务 + 健康检查 + 首版初始化
#   B. tuoluojiang（拓罗匠）：php 站点 + docker compose 数据卷（数据目录必须保留）
#
# devssh 让命令在**本机真实执行**，所以断言检查的是真实文件/目录/权限，而不是接口返回。
# 用法：make build && make devssh && bash scripts/verify_deploy_v2.sh
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18870}"
FILE_PORT=$((PORT + 1))
HEALTH_PORT=$((PORT + 2))
SSH_PORT="${SSH_PORT:-0}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-v2-pass"

TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
DATA_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-v2.XXXXXX")"
FILE_DIR="$DATA_DIR/files"
SRV_DIR="$DATA_DIR/sites"
LEAD_PID=""; DEVSSH_PID=""; FILE_PID=""; HEALTH_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$HEALTH_PID" "$FILE_PID" "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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

wait_release() { # <release_id> <token> <期望状态> <最长秒数>
  local id="$1" tok="$2" want="$3" max="${4:-90}" i st
  for i in $(seq 1 "$max"); do
    st="$(sget "$(api GET "/api/v1/releases/${id}" "" "$tok")" '.data.release.status')"
    [ "$st" = "$want" ] && { echo "$st"; return 0; }
    case "$st" in failed|success|canceled) echo "$st"; return 1;; esac
    sleep 1
  done
  echo "${st:-timeout}"; return 1
}

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 环境
step "1. 启动制品服务 / 健康检查服务 / devssh / 控制端"
mkdir -p "$FILE_DIR" "$SRV_DIR"

cat > "$DATA_DIR/serve.py" <<'PY'
import http.server, socketserver, sys, os
os.chdir(sys.argv[1])
class H(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a): pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", int(sys.argv[2])), H) as s:
    s.serve_forever()
PY
python3 "$DATA_DIR/serve.py" "$FILE_DIR" "$FILE_PORT" > "$DATA_DIR/files.out" 2>&1 &
FILE_PID=$!

cat > "$DATA_DIR/health.py" <<'PY'
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        self.send_response(200); self.send_header('Content-Type','application/json')
        self.end_headers(); self.wfile.write(b'{"status":"up"}')
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
python3 "$DATA_DIR/health.py" "$HEALTH_PORT" > "$DATA_DIR/health.out" 2>&1 &
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
[ -n "$SPORT" ] && pass "devssh 已就绪（命令在本机真实执行）" || { fail "devssh 未就绪"; exit 1; }

env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
    LEADSTACK_DATA_DIR="$DATA_DIR/data" \
    LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
    LEADSTACK_SSH_INTERVAL="30s" \
    LEADSTACK_DEPLOY_ROOT="$DATA_DIR/release-root" \
    LEADSTACK_PUBLIC_URL="$BASE" \
    ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
for _ in $(seq 1 80); do [ "$(code GET /healthz)" = "200" ] && break; sleep 0.3; done
[ "$(code GET /healthz)" = "200" ] && pass "控制端就绪" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log"; exit 1; }

LOGIN_BODY="$(jq -n --arg u admin --arg p "$ADMIN_PASS" '{username:$u,password:$p}')"
LOGIN_RESP="$(api POST /api/v1/auth/login "$LOGIN_BODY")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败"; exit 1; }

CRED_BODY="$(jq -n --arg k "$(cat "$DATA_DIR/id")" '{name:"v2-key",kind:"key",username:"devuser",private_key:$k}')"
CRED_RESP="$(api POST /api/v1/credentials "$CRED_BODY" "$TOKEN")"
CRED_ID="$(sget "$CRED_RESP" '.data.id')"
[ -n "$CRED_ID" ] || { fail "创建凭证失败：$CRED_RESP"; exit 1; }
SRV_BODY="$(jq -n --argjson port "$SPORT" --arg cid "$CRED_ID" \
  '{name:"v2-node",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid}')"
SRV_RESP="$(api POST /api/v1/servers "$SRV_BODY" "$TOKEN")"
SRV_ID="$(sget "$SRV_RESP" '.data.id')"
[ -n "$SRV_ID" ] && pass "目标服务器已创建" || { fail "创建服务器失败：$SRV_RESP（cred=$CRED_ID port=$SPORT）"; exit 1; }

make_artifact() { # <name> <dir-to-pack>
  local name="$1" dir="$2"
  tar -czf "$FILE_DIR/$name.tar.gz" -C "$dir" .
  shasum -a 256 "$FILE_DIR/$name.tar.gz" | awk '{print $1}'
}

ART_ID=""  # 由 upload 得到
upload_artifact() { # <file> -> id
  local f="$1"
  local resp
  resp="$(curl -sS -X POST "${BASE}/api/v1/artifacts/upload" -H "Authorization: Bearer ${TOKEN}" -F "file=@${f};type=application/gzip")"
  sget "$resp" '.data.id'
}
create_release() { # <app> <tmpl> <version> <artifact> -> release id
  local body resp
  body="$(jq -n --arg a "$1" --arg t "$2" --arg v "$3" --arg art "$4" --arg srv "$SRV_ID" \
    '{app_id:$a,template_id:$t,version:$v,artifact_id:$art,server_ids:[$srv]}')"
  resp="$(api POST /api/v1/releases "$body" "$TOKEN")"
  local id
  id="$(sget "$resp" '.data.release.id')"
  if [ -z "$id" ]; then echo "RELEASE_ERROR: $resp" >&2; fi
  printf '%s' "$id"
}

# ---------------------------------------------------------------- A. nengtan 形态
step "2. 样本 A：nengtan（jar + 前端 + systemd + 首版初始化 + 健康检查）"
TARGET_A="$SRV_DIR/nengtan/release"
mkdir -p "$TARGET_A/conf" "$TARGET_A/web/client" "$TARGET_A/logs"
echo 'old-jar-v0' > "$TARGET_A/gdhh-carbon-hub.jar"
echo 'old client' > "$TARGET_A/web/client/index.html"
echo 'DB_HOST=127.0.0.1' > "$TARGET_A/conf/nengtan.env"
# 模拟 systemd 重启：写文件即可断言「重启命令真的被执行」
RESTART_A="sh -c 'echo restarted >> $TARGET_A/restart.log'"

PKG1="$DATA_DIR/pkg-a1"; mkdir -p "$PKG1/web/admin"
echo 'jar-v1' > "$PKG1/gdhh-carbon-hub.jar"
echo 'admin v1' > "$PKG1/web/admin/index.html"
SHA1="$(make_artifact a1 "$PKG1")"
ART1="$(upload_artifact "$FILE_DIR/a1.tar.gz")"
[ -n "$ART1" ] && pass "制品 a1 已上传（sha256=${SHA1:0:12}…）" || fail "上传制品失败"

TPL_ID="$(sget "$(api POST /api/v1/templates '{"name":"v2-empty","kind":"binary","content":""}' "$TOKEN")" '.data.id')"
[ -n "$TPL_ID" ] && pass "空 binary 模板已创建（接管模式不需要模板内容）" || fail "创建模板失败"

APP_A_BODY="$(jq -n \
  --arg target "$TARGET_A" --arg restart "$RESTART_A" --argjson hport "$HEALTH_PORT" \
  --arg init "echo init-ran >> $TARGET_A/logs/.init-count" \
  --arg env '{"DB_HOST":"127.0.0.1","SERVER_PORT":"8081"}' \
  '{name:"慧碳云枢",slug:"nengtan",provider:"builtin",deploy_mode:"inplace",target_dir:$target,root:$target,
    restart_cmd:$restart,init_script:$init,env_json:$env,
    keep_paths:"conf,logs,web/client",healthcheck_path:"/healthz",healthcheck_port:$hport}')"
APP_A_RESP="$(api POST /api/v1/apps "$APP_A_BODY" "$TOKEN")"
APP_A="$(sget "$APP_A_RESP" '.data.id')"
[ -n "$APP_A" ] && pass "应用已创建（inplace 接管 + 健康检查 + 首版初始化）" || { fail "创建应用失败：$APP_A_RESP"; exit 1; }

REL_A1="$(create_release "$APP_A" "$TPL_ID" "1.0.0" "$ART1")"
[ -n "$REL_A1" ] && pass "首次发布已创建" || fail "创建首次发布失败"
A1_STATE="$(wait_release "$REL_A1" "$TOKEN" success 90)"
[ "$A1_STATE" = "success" ] && pass "首次发布成功（初始化 + 健康检查都过）" || {
  A1_MSG="$(sget "$(api GET "/api/v1/releases/$REL_A1/runs" "" "$TOKEN")" '.data.runs[0].message')"
  fail "首次发布未成功（${A1_STATE}）：${A1_MSG}"
}

[ -f "$TARGET_A/gdhh-carbon-hub.jar" ] && grep -q 'jar-v1' "$TARGET_A/gdhh-carbon-hub.jar" \
  && pass "新版本 jar 已落位" || fail "jar 未更新"
[ -f "$TARGET_A/web/admin/index.html" ] && pass "新版本前端文件已落位" || fail "前端文件缺失"
[ -f "$TARGET_A/conf/nengtan.env" ] && grep -q 'DB_HOST' "$TARGET_A/conf/nengtan.env" \
  && pass "保留路径 conf/ 未被覆盖删除" || fail "keep_paths 失效（conf 被删）"
[ -f "$TARGET_A/web/client/index.html" ] && grep -q 'old client' "$TARGET_A/web/client/index.html" \
  && pass "keep_paths web/client 保留旧内容（多端分开发布场景）" || fail "web/client 被覆盖"
INIT_LINES="$(wc -l < "$TARGET_A/logs/.init-count" 2>/dev/null | tr -d ' ')"
[ "$INIT_LINES" = "1" ] && pass "首版初始化执行了 1 次" || fail "首版初始化次数异常：${INIT_LINES}"
[ -f "$TARGET_A/.leadstack.env" ] && [ "$(stat -f '%Lp' "$TARGET_A/.leadstack.env" 2>/dev/null || stat -c '%a' "$TARGET_A/.leadstack.env")" = "600" ] \
  && pass "环境文件已写入且权限 0600" || fail "环境文件缺失或权限不对"
grep -q 'SERVER_PORT=8081' "$TARGET_A/.leadstack.env" 2>/dev/null && pass "环境变量内容正确" || fail "环境文件内容不符"
[ -f "$TARGET_A/restart.log" ] && pass "重启命令已执行" || fail "重启命令未执行"
ls "$TARGET_A/.leadstack-backups/"backup-*.tar.gz >/dev/null 2>&1 && pass "存在发布前备份" || fail "未生成备份"

# 第二次发布：初始化不能重复执行
PKG2="$DATA_DIR/pkg-a2"; mkdir -p "$PKG2/web/admin"
echo 'jar-v2' > "$PKG2/gdhh-carbon-hub.jar"
echo 'admin v2' > "$PKG2/web/admin/index.html"
make_artifact a2 "$PKG2" >/dev/null
ART2="$(upload_artifact "$FILE_DIR/a2.tar.gz")"
REL_A2="$(create_release "$APP_A" "$TPL_ID" "1.0.1" "$ART2")"
[ "$(wait_release "$REL_A2" "$TOKEN" success 90)" = "success" ] && pass "第二次发布成功" || fail "第二次发布失败"
grep -q 'jar-v2' "$TARGET_A/gdhh-carbon-hub.jar" && pass "第二次发布内容已生效" || fail "第二次发布内容未生效"
INIT_LINES="$(wc -l < "$TARGET_A/logs/.init-count" 2>/dev/null | tr -d ' ')"
[ "$INIT_LINES" = "1" ] && pass "首版初始化没有重复执行" || fail "初始化被执行了 ${INIT_LINES} 次（应只 1 次）"

# 健康检查失败 → 自动回滚
PATCH_RESP="$(api PATCH "/api/v1/apps/$APP_A" "{\"healthcheck_port\":$((HEALTH_PORT + 7))}" "$TOKEN")"
printf 'PATCH healthcheck_port → %s\n' "$(sget "$PATCH_RESP" '.data.healthcheck_port')"
PKG3="$DATA_DIR/pkg-a3"; mkdir -p "$PKG3"
echo 'jar-broken' > "$PKG3/gdhh-carbon-hub.jar"
make_artifact a3 "$PKG3" >/dev/null
ART3="$(upload_artifact "$FILE_DIR/a3.tar.gz")"
REL_A3="$(create_release "$APP_A" "$TPL_ID" "1.0.2" "$ART3")"
A3_STATE="$(wait_release "$REL_A3" "$TOKEN" failed 120)"
if [ "$A3_STATE" != "failed" ]; then
  A3_MSG="$(sget "$(api GET "/api/v1/releases/$REL_A3/runs" "" "$TOKEN")" '.data.runs[0].message')"
  fail "健康检查失败未让发布失败（${A3_STATE}）：${A3_MSG}"
else
  pass "健康检查失败时发布标记为失败"
fi
RUN_A3="$(api GET "/api/v1/releases/$REL_A3/runs" "" "$TOKEN")"
STEP_A3="$(sget "$RUN_A3" '.data.runs[0].failed_step')"
[ "$STEP_A3" = "health-check" ] && pass "失败步骤记录为 health-check" || fail "失败步骤不符：${STEP_A3}"
grep -q 'jar-v2' "$TARGET_A/gdhh-carbon-hub.jar" && pass "健康检查失败后已回滚到上一版本（jar-v2）" || fail "回滚未生效"

# ---------------------------------------------------------------- B. tuoluojiang 形态
step "3. 样本 B：tuoluojiang（php 站点 + compose 数据目录必须保留）"
TARGET_B="$SRV_DIR/tuoluojiang"
mkdir -p "$TARGET_B/code/public" "$TARGET_B/help/docker-compose/mysql/data" "$TARGET_B/help/docker-compose/redis/data"
echo '<?php echo "old";' > "$TARGET_B/code/public/index.php"
echo 'mysql-data-bytes' > "$TARGET_B/help/docker-compose/mysql/data/ibdata1"
echo 'redis-dump-bytes' > "$TARGET_B/help/docker-compose/redis/data/dump.rdb"
RESTART_B="sh -c 'echo compose-up >> $TARGET_B/restart.log'"

PKB1="$DATA_DIR/pkg-b1"; mkdir -p "$PKB1/code/public" "$PKB1/code/app"
echo '<?php echo "v1";' > "$PKB1/code/public/index.php"
echo 'app v1' > "$PKB1/code/app/controller.php"
make_artifact b1 "$PKB1" >/dev/null
ARTB1="$(upload_artifact "$FILE_DIR/b1.tar.gz")"

APP_B_BODY="$(jq -n \
  --arg target "$TARGET_B" --arg restart "$RESTART_B" \
  '{name:"拓罗匠",slug:"tuoluojiang",provider:"builtin",deploy_mode:"inplace",target_dir:$target,root:$target,
    restart_cmd:$restart,
    keep_paths:"help/docker-compose/mysql/data,help/docker-compose/redis/data,code/public/uploads"}')"
APP_B_RESP="$(api POST /api/v1/apps "$APP_B_BODY" "$TOKEN")"
APP_B="$(sget "$APP_B_RESP" '.data.id')"
[ -n "$APP_B" ] && pass "应用已创建（数据目录列入 keep_paths）" || { fail "创建应用失败：$APP_B_RESP"; exit 1; }

RELB1="$(create_release "$APP_B" "$TPL_ID" "2.0.0" "$ARTB1")"
[ "$(wait_release "$RELB1" "$TOKEN" success 90)" = "success" ] && pass "站点首次发布成功" || fail "站点首次发布失败"
grep -q 'v1' "$TARGET_B/code/public/index.php" && pass "站点代码已更新" || fail "站点代码未更新"
grep -q 'mysql-data-bytes' "$TARGET_B/help/docker-compose/mysql/data/ibdata1" && pass "MySQL 数据目录未被覆盖" || fail "MySQL 数据被破坏"
grep -q 'redis-dump-bytes' "$TARGET_B/help/docker-compose/redis/data/dump.rdb" && pass "Redis 数据未被删除" || fail "Redis 数据被删除"
[ -f "$TARGET_B/restart.log" ] && pass "compose 重启命令已执行" || fail "重启命令未执行"

PKB2="$DATA_DIR/pkg-b2"; mkdir -p "$PKB2/code/public"
echo '<?php echo "v2";' > "$PKB2/code/public/index.php"
make_artifact b2 "$PKB2" >/dev/null
ARTB2="$(upload_artifact "$FILE_DIR/b2.tar.gz")"
RELB2="$(create_release "$APP_B" "$TPL_ID" "2.0.1" "$ARTB2")"
[ "$(wait_release "$RELB2" "$TOKEN" success 90)" = "success" ] && pass "站点第二次发布成功" || fail "站点第二次发布失败"
grep -q 'v2' "$TARGET_B/code/public/index.php" && pass "第二次发布内容生效" || fail "第二次发布内容未生效"
grep -q 'mysql-data-bytes' "$TARGET_B/help/docker-compose/mysql/data/ibdata1" && pass "第二次发布后数据目录仍然完好" || fail "第二次发布损坏了数据"
[ ! -f "$TARGET_B/code/app/controller.php" ] && pass "旧版本残留文件被清理（rsync --delete 生效）" || fail "旧文件残留"

# ---------------------------------------------------------------- C. 接管向导
step "4. 接管向导（只读探测）"
DISC="$(api POST "/api/v1/servers/$SRV_ID/discover" "" "$TOKEN")"
D_OK="$(sget "$DISC" '.data.server_id')"
[ "$D_OK" = "$SRV_ID" ] && pass "探测接口返回报告" || fail "探测接口异常：$DISC"
printf '%s' "$DISC" | jq -e '.data.candidates | type == "array"' >/dev/null 2>&1 && pass "候选列表是数组" || fail "候选列表格式不对"
printf '%s' "$DISC" | jq -e '.data.raw.SYSTEMD != null' >/dev/null 2>&1 && pass "原始分段（SYSTEMD/PORTS/…）可回传" || fail "缺少原始分段"

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
