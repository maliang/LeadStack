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

# ---------------------------------------------------------------- 发布预览与预设
step "5. 发布预览与内置预设"
PREVIEW="$(api POST /api/v1/releases/preview "$(jq -n --arg a "$APP_A" --arg t "$TPL_ID" --arg art "$ART2" --arg srv "$SRV_ID" \
  '{app_id:$a,template_id:$t,version:"1.0.9",artifact_id:$art,server_ids:[$srv]}')" "$TOKEN")"
PREVIEW_ROOT="$(sget "$PREVIEW" '.data.servers[0].plan_root')"
[ "$PREVIEW_ROOT" = "$TARGET_A" ] && pass "预览返回计划根目录（接管目录）" || fail "预览根目录不符：$PREVIEW_ROOT"
SECRET_CMD="$(sget "$PREVIEW" '.data.servers[0].steps[] | select(.name=="write-env") | .command')"
SECRET_FLAG="$(sget "$PREVIEW" '.data.servers[0].steps[] | select(.name=="write-env") | .secret')"
[ "$SECRET_FLAG" = "true" ] && [ -z "$SECRET_CMD" ] && pass "预览对 secret 步骤脱敏（命令为空）" || fail "secret 步骤未脱敏"
printf '%s' "$PREVIEW" | jq -e '.data.servers[0].rollback_steps | length > 0' >/dev/null 2>&1   && pass "预览包含回滚步骤" || fail "预览缺少回滚步骤"
printf '%s' "$PREVIEW" | jq -e '.data.servers[0].notes | type == "array"' >/dev/null 2>&1   && pass "预览带 notes 字段（风险提示通道可用）" || fail "预览缺少 notes 字段"
BEFORE_COUNT="$(api GET "/api/v1/releases?limit=100" "" "$TOKEN" | jq -r '.data | length')"
NONE="$(api POST /api/v1/releases/preview "$(jq -n --arg a "$APP_A" --arg t "$TPL_ID" --arg srv "$SRV_ID" '{app_id:$a,template_id:$t,version:"1.0.9",server_ids:[$srv]}')" "$TOKEN")"
AFTER_COUNT="$(api GET "/api/v1/releases?limit=100" "" "$TOKEN" | jq -r '.data | length')"
[ "$BEFORE_COUNT" = "$AFTER_COUNT" ] && pass "预览不会创建发布记录（只读）" || fail "预览产生了发布记录（$BEFORE_COUNT → $AFTER_COUNT）"

PRESETS="$(api GET /api/v1/deploy/presets "" "$TOKEN")"
PRESET_COUNT="$(printf '%s' "$PRESETS" | jq -r '.data | length' 2>/dev/null)"
[ "${PRESET_COUNT:-0}" -ge 5 ] && pass "内置预设返回 ${PRESET_COUNT} 个" || fail "预设数量异常：${PRESET_COUNT}"
printf '%s' "$PRESETS" | jq -e '.data[] | select(.key=="laravel-swoole") | .template.content | test("start.sh restart")' >/dev/null 2>&1 \
  && pass "LaravelS 预设包含 start.sh restart" || fail "LaravelS 预设内容不符"
printf '%s' "$NONE" | jq -e '.data.servers[0].notes | join(" ") | test("制品")' >/dev/null 2>&1 \
  && pass "inplace 缺制品时预览给出明确提示（不静默）" || fail "缺制品提示不明确"

# ---------------------------------------------------------------- 本机接入
step "5. 本机接入（agent 一键命令）"
LOCAL="$(api POST /api/v1/servers/local '{"name":"本机-e2e","ttl_s":600}' "$TOKEN")"
LOCAL_ID="$(sget "$LOCAL" '.data.server.id')"
[ -n "$LOCAL_ID" ] && pass "本机服务器已创建" || fail "本机接入失败：$LOCAL"
[ "$(sget "$LOCAL" '.data.server.mode')" = "agent" ] && pass "本机服务器为 agent 模式" || fail "本机服务器模式不符"
LOCAL_CMD="$(sget "$LOCAL" '.data.command')"
printf '%s' "$LOCAL_CMD" | grep -q "/install.sh" && printf '%s' "$LOCAL_CMD" | grep -q -- "--token '" \
  && pass "返回可执行的安装命令" || fail "安装命令格式不符：$LOCAL_CMD"
printf '%s' "$LOCAL_CMD" | grep -q "127.0.0.1" && pass "安装命令使用回环地址（本机场景）" || fail "安装命令地址不符"
LOCAL_TOKEN="$(sget "$LOCAL" '.data.token')"
printf '%s' "$LOCAL_CMD" | grep -q "$LOCAL_TOKEN" && pass "命令内嵌一次性令牌" || fail "命令未包含令牌"
LOCAL_LIST="$(api GET /api/v1/servers "" "$TOKEN")"
printf '%s' "$LOCAL_LIST" | jq -e --arg id "$LOCAL_ID" '.data[] | select(.id==$id) | .mode=="agent"' >/dev/null 2>&1 \
  && pass "本机服务器出现在服务器列表中" || fail "列表未包含本机服务器"

# ---------------------------------------------------------------- C. 接管向导
step "6. 接管向导（只读探测）"
DISC="$(api POST "/api/v1/servers/$SRV_ID/discover" "" "$TOKEN")"
D_OK="$(sget "$DISC" '.data.server_id')"
[ "$D_OK" = "$SRV_ID" ] && pass "探测接口返回报告" || fail "探测接口异常：$DISC"
printf '%s' "$DISC" | jq -e '.data.candidates | type == "array"' >/dev/null 2>&1 && pass "候选列表是数组" || fail "候选列表格式不对"
printf '%s' "$DISC" | jq -e '.data.raw.SYSTEMD != null' >/dev/null 2>&1 && pass "原始分段（SYSTEMD/PORTS/…）可回传" || fail "缺少原始分段"

# ---------------------------------------------------------------- D. 应用参数校验
step "7. 应用参数校验与健康检查路径持久化（A9）"
# healthcheck_path：必须以 / 开头、不含空白/?/#、长度 <= 200
chk_pair() { # <期望码> <body> <说明>
  local want="$1" body="$2" desc="$3" c r
  r="$(api PATCH "/api/v1/apps/$APP_A" "$body" "$TOKEN")"
  c="$(printf '%s' "$r" | jq -r '.error.code // empty' 2>/dev/null)"
  if [ "$want" = "200" ]; then
    if printf '%s' "$r" | jq -e '.data.id' >/dev/null 2>&1; then pass "$desc"; else fail "$desc（响应：$r）"; fi
  else
    if printf '%s' "$r" | jq -e --arg c "$want" '.error.code == $c' >/dev/null 2>&1; then pass "$desc"
    else fail "$desc（期望 $want，实际 $r）"; fi
  fi
}
chk_pair invalid_argument '{"healthcheck_path":"healthz"}' "healthcheck_path 无前导 / → 400"
chk_pair invalid_argument '{"healthcheck_path":"/he althz"}' "healthcheck_path 含空白 → 400"
chk_pair invalid_argument '{"healthcheck_path":"/a?x=1"}' "healthcheck_path 含 ? → 400"
chk_pair invalid_argument '{"healthcheck_path":"/a#frag"}' "healthcheck_path 含 # → 400"
LONG_PATH="/$(printf 'a%.0s' $(seq 1 200))"
chk_pair invalid_argument "{\"healthcheck_path\":\"$LONG_PATH\"}" "healthcheck_path 超过 200 字符 → 400"
chk_pair invalid_argument '{"keep_paths":"/etc/passwd"}' "keep_paths 含绝对路径 → 400"
chk_pair invalid_argument '{"keep_paths":"../outside"}' "keep_paths 含 .. → 400"
chk_pair invalid_argument '{"canary_count":99999}' "canary_count 超过服务器数 → 400"
chk_pair 200 '{"healthcheck_path":"/healthz"}' "合法 healthcheck_path 保存成功"
SAVED_PATH="$(sget "$(api GET "/api/v1/apps/$APP_A" "" "$TOKEN")" '.data.healthcheck_path')"
[ "$SAVED_PATH" = "/healthz" ] && pass "编辑保存后 healthcheck_path 未被清空（A1）" \
  || fail "healthcheck_path 被清空或丢失：$SAVED_PATH"
ENV_KEYS="$(api GET "/api/v1/apps/$APP_A" "" "$TOKEN" | jq -c '.data.env_keys // []' 2>/dev/null)"
printf '%s' "$ENV_KEYS" | jq -e 'length > 0' >/dev/null 2>&1 && pass "应用响应带 env_keys 键名数组（不泄露值）" \
  || fail "缺少 env_keys：$ENV_KEYS"
printf '%s' "$ENV_KEYS" | jq -e 'all(.[]; . == "DB_HOST" or . == "SERVER_PORT" or . == "KEY")' >/dev/null 2>&1 \
  && pass "env_keys 只含键名" || fail "env_keys 内容异常：$ENV_KEYS"

# ---------------------------------------------------------------- 8. 前端表单契
step "8. 前端表单契约（A1/A2/A25）"
# A1/A2：健康检查路径与端口必须是可定位的独立输入（e2e 钩子 id），提示文案说明「留空不检查」；
#        env_keys 模式要显式告知「仅键名、保存不覆盖」。
dist_has "健康检查路径/端口输入与提示（A1/A2）" \
  'app-healthcheck' 'app-healthcheck-port' 'apps.healthcheckPathHint' 'apps.envKeysOnly' \
  '留空 = 不检查' '仅显示键名' 'healthcheck_path' 'env_keys'
# A25：按授权分组选机，计数必须是「已选 N 台 / 该分组共 M 台」，且分组来自授权分组而非 legacy group。
dist_has "按授权分组选机与计数（A25）" \
  'picker.selectedCount' 'picker.groupTotal' 'picker.authorizedGroup' \
  '已选 {n} 台' '该分组共 {m} 台' '授权分组'

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m全部通过：%d 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m失败 %d 项，通过 %d 项\033[0m\n' "$FAIL" "$PASS"
exit 1
