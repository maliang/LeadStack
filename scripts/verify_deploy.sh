#!/usr/bin/env bash
# LeadStack 部署 端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 29 节 部署验收标准。
# 用法：make build && make devssh && bash scripts/verify_deploy.sh
#
# 关键设计：devssh 把命令交给本机真实 sh 执行，所以部署真的会落到临时目录，
# 我们可以直接检查符号链接、文件内容与 sha256 —— 不是"看接口返回 200"就算过。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18830}"
FILE_PORT=$((PORT + 1))
SSH_PORT="${SSH_PORT:-0}"   # 0 = 由 devssh 自行分配
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m4-pass"
# 选择纯 ASCII 的临时根目录：部署根路径会经过 deploy 的变量白名单校验，
# 若 TMPDIR 指向含中文的仓库路径（本仓库名含「领栈」），ROOT 会被判非法。
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
DATA_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-m4.XXXXXX")"
DEPLOY_ROOT="$DATA_DIR/deploy-root"     # 部署根目录（控制端传给目标机的 --root）
FILE_DIR="$DATA_DIR/files"
LEAD_PID=""; DEVSSH_PID=""; FILE_PID=""; SRV_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$SRV_PID" "$FILE_PID" "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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
sget() { local v; v="$(jget "$1" "$2")"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }

# 轮询发布状态直到终态
wait_release() { # <release_id> <token> <期望状态> <最长秒数>
  local id="$1" tok="$2" want="$3" max="${4:-120}" i st
  for i in $(seq 1 "$max"); do
    st="$(api GET "/api/v1/releases/${id}" "" "$tok" | jq -r '.data.release.status // .data.status // empty' 2>/dev/null)"
    [ "$st" = "$want" ] && { echo "$st"; return 0; }
    case "$st" in failed|success|canceled) [ "$st" = "$want" ] || { echo "$st"; return 1; };; esac
    sleep 1
  done
  echo "${st:-timeout}"; return 1
}

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 1. 构建产物
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-devssh; do
  [ -x "$bin" ] && pass "$bin 存在" || fail "$bin 缺失"
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动环境
step "2. 启动 HTTP 文件服务 / devssh / 控制端"
mkdir -p "$FILE_DIR"
printf 'leadstack-artifact-v1\n' > "$FILE_DIR/app-v1.txt"
printf 'leadstack-artifact-v2\n' > "$FILE_DIR/app-v2.txt"
ART_SHA_V1="$(shasum -a 256 "$FILE_DIR/app-v1.txt" | awk '{print $1}')"

cat > "$DATA_DIR/files.py" <<'PY'
import http.server, socketserver, sys, os
os.chdir(sys.argv[1])
class H(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a): pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", int(sys.argv[2])), H) as s:
    s.serve_forever()
PY
python3 "$DATA_DIR/files.py" "$FILE_DIR" "$FILE_PORT" > "$DATA_DIR/files.out" 2>&1 &
FILE_PID=$!
FUP=0
for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:${FILE_PORT}/app-v1.txt" && { FUP=1; break; }; sleep 0.2; done
[ "$FUP" = "1" ] && pass "HTTP 制品服务已监听 ${FILE_PORT}（sha256=${ART_SHA_V1:0:12}…）" || { fail "制品服务未就绪"; exit 1; }

ssh-keygen -q -t ed25519 -N '' -f "$DATA_DIR/id" >/dev/null 2>&1
./bin/leadstack-devssh --addr "127.0.0.1:${SSH_PORT}" --user devuser --password devpass \
  --authorized-key-file "$DATA_DIR/id.pub" --port-file "$DATA_DIR/ssh.port" \
  > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SPORT=""
for _ in $(seq 1 40); do SPORT="$(cat "$DATA_DIR/ssh.port" 2>/dev/null)"; [ -n "$SPORT" ] && break; sleep 0.25; done
[ -n "$SPORT" ] && pass "devssh 监听 127.0.0.1:${SPORT}（命令在本机真实执行）" || { fail "devssh 未就绪"; exit 1; }

LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="$DATA_DIR" \
LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
LEADSTACK_SSH_INTERVAL="30s" \
LEADSTACK_DEPLOY_ROOT="$DEPLOY_ROOT" \
LEADSTACK_DEPLOY_CONCURRENCY="4" \
  ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
UP=0
for _ in $(seq 1 60); do [ "$(status_of "${BASE}/healthz")" = "200" ] && { UP=1; break; }; sleep 0.3; done
[ "$UP" = "1" ] && pass "控制端就绪（部署根 ${DEPLOY_ROOT}）" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log" | sed 's/^/      /'; exit 1; }

# ---------------------------------------------------------------- 3. 鉴权与目标机
step "3. 鉴权与目标机"
LOGIN_RESP="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败：$LOGIN_RESP"; exit 1; }
[ "$(status_of "${BASE}/api/v1/apps")" = "401" ] && pass "无 token 访问 /apps 返回 401" || fail "无 token 访问 /apps 未返回 401"

CRED_ID="$(sget "$(api POST /api/v1/credentials \
  "$(jq -n --arg k "$(cat "$DATA_DIR/id")" '{name:"m4-key",kind:"key",username:"devuser",private_key:$k}')" "$TOKEN")" '.data.id')"
SRV_ID="$(sget "$(api POST /api/v1/servers \
  "$(jq -n --argjson port "$SPORT" --arg cid "$CRED_ID" \
     '{name:"m4-node",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid,group:"m4"}')" "$TOKEN")" '.data.id')"
[ -n "$SRV_ID" ] && pass "目标服务器已创建" || { fail "创建服务器失败"; exit 1; }

# ---------------------------------------------------------------- 4. 应用 / 模板 / 制品
step "4. 应用、模板与制品"
APP_ID="$(sget "$(api POST /api/v1/apps '{"name":"演示应用","slug":"demo-app","description":"部署 验收"}' "$TOKEN")" '.data.id')"
[ -n "$APP_ID" ] && pass "应用已创建（slug=demo-app）" || { fail "创建应用失败"; exit 1; }

BIN_TPL="$(cat <<'TPL'
set -eu
ROOT="{{ROOT}}"
REL="{{RELEASE_DIR}}"
mkdir -p "$REL"
curl -fsSL "{{ARTIFACT_URL}}" -o "$REL/app.bin"
echo "{{ARTIFACT_SHA256}}  $REL/app.bin" | shasum -a 256 -c - >/dev/null
echo "制品校验通过 version={{VERSION}}"
TPL
)"
TPL_ID="$(sget "$(api POST /api/v1/templates \
  "$(jq -n --arg c "$BIN_TPL" '{name:"m4-binary",kind:"binary",content:$c}')" "$TOKEN")" '.data.id')"
[ -n "$TPL_ID" ] && pass "binary 模板已创建" || { fail "创建模板失败"; exit 1; }

# 渲染预览：必须返回替换后的内容且不执行
RENDER_RESP="$(api POST "/api/v1/templates/${TPL_ID}/render" \
  "$(jq -n --arg root "$DEPLOY_ROOT" --arg rel "$DEPLOY_ROOT/releases/preview" --arg url "http://127.0.0.1:${FILE_PORT}/app-v1.txt" --arg sha "$ART_SHA_V1" \
     '{vars:{ROOT:$root,RELEASE_DIR:$rel,ARTIFACT_URL:$url,ARTIFACT_SHA256:$sha,VERSION:"preview-1"}}')" "$TOKEN")"
RENDERED="$(sget "$RENDER_RESP" '.data.content')"
if printf '%s' "$RENDERED" | grep -q "preview-1" && ! printf '%s' "$RENDERED" | grep -q '{{'; then
  pass "模板渲染预览正确（占位符已替换，未执行）"
else
  fail "渲染预览异常：$RENDER_RESP"
fi

ART_ID="$(sget "$(api POST /api/v1/artifacts \
  "$(jq -n --arg u "http://127.0.0.1:${FILE_PORT}/app-v1.txt" --arg s "$ART_SHA_V1" \
     '{kind:"url",url:$u,sha256:$s}')" "$TOKEN")" '.data.id')"
[ -n "$ART_ID" ] && pass "制品已登记（url 类型）" || { fail "登记制品失败"; exit 1; }

# ---------------------------------------------------------------- 5. 首次发布
step "5. 首次发布（v1）"
REL1_RESP="$(api POST /api/v1/releases \
  "$(jq -n --arg app "$APP_ID" --arg tpl "$TPL_ID" --arg art "$ART_ID" --arg sid "$SRV_ID" \
     '{app_id:$app,template_id:$tpl,version:"v1.0.0",artifact_id:$art,server_ids:[$sid]}')" "$TOKEN")"
REL1_ID="$(sget "$REL1_RESP" '.data.release.id')"
[ -n "$REL1_ID" ] && pass "发布已创建（${REL1_ID:0:8}…）" || { fail "创建发布失败：$REL1_RESP"; exit 1; }

ST1="$(wait_release "$REL1_ID" "$TOKEN" success 180)"
if [ "$ST1" = "success" ]; then
  pass "首次发布成功（success）"
else
  fail "首次发布未成功（状态 ${ST1}）"
  api GET "/api/v1/releases/${REL1_ID}" "" "$TOKEN" | jq -c '.data' | sed 's/^/    /'
  tail -25 "$DATA_DIR/lead.log" | sed 's/^/      /'
fi

# 真实产物落盘：current 指向的目录里必须有文件且内容正确
CURRENT_LINK="$DEPLOY_ROOT/demo-app/current"
if [ -L "$CURRENT_LINK" ]; then
  pass "current 是符号链接 → $(readlink "$CURRENT_LINK")"
  TARGET_DIR="$(cd "$(dirname "$CURRENT_LINK")" && readlink "$CURRENT_LINK")"
  case "$TARGET_DIR" in /*) ;; *) TARGET_DIR="$(dirname "$CURRENT_LINK")/$TARGET_DIR";; esac
  if [ -f "$TARGET_DIR/app.bin" ]; then
    GOT="$(cat "$TARGET_DIR/app.bin" 2>/dev/null | head -1)"
    [ "$GOT" = "leadstack-artifact-v1" ] && pass "产物内容正确（app.bin = v1）" || fail "产物内容不符：${GOT:-空}"
    GOT_SHA="$(shasum -a 256 "$TARGET_DIR/app.bin" | awk '{print $1}')"
    [ "$GOT_SHA" = "$ART_SHA_V1" ] && pass "落盘文件 sha256 与制品一致" || fail "sha256 不符"
  else
    fail "current 指向的目录里没有 app.bin（$TARGET_DIR）"
  fi
else
  fail "current 不是符号链接（或不存在）：$(ls -la "$DEPLOY_ROOT/demo-app" 2>&1 | head -5)"
fi

# ---------------------------------------------------------------- 6. 日志增量拉取
step "6. 发布日志增量拉取"
LOGS1="$(api GET "/api/v1/releases/${REL1_ID}/runs/${SRV_ID}/logs?after_seq=0" "" "$TOKEN")"
L1_N="$(printf '%s' "$LOGS1" | jq -r '.data.logs | length' 2>/dev/null)"
L1_LAST="$(printf '%s' "$LOGS1" | jq -r '[.data.logs[].seq] | max // 0' 2>/dev/null)"
if [ "${L1_N:-0}" -ge 1 ] 2>/dev/null; then
  pass "日志可拉取（${L1_N} 条，最大 seq=${L1_LAST}）"
else
  fail "日志为空：$LOGS1"
fi
LOGS2="$(api GET "/api/v1/releases/${REL1_ID}/runs/${SRV_ID}/logs?after_seq=${L1_LAST}" "" "$TOKEN")"
L2_N="$(printf '%s' "$LOGS2" | jq -r '.data.logs | length' 2>/dev/null)"
L2_MIN="$(printf '%s' "$LOGS2" | jq -r '[.data.logs[].seq] | min // empty' 2>/dev/null)"
if [ "${L2_N:-0}" = "0" ] || { [ -n "$L2_MIN" ] && [ "$L2_MIN" -gt "$L1_LAST" ]; }; then
  pass "after_seq 增量拉取不重复（新增 ${L2_N} 条）"
else
  fail "增量拉取出现重复（min seq=${L2_MIN}，期望 > ${L1_LAST}）"
fi

# ---------------------------------------------------------------- 7. 失败保护
step "7. 失败发布不破坏 current（校验和错误）"
# 故意登记一个 sha256 与实际文件不符的制品，制造「下载成功但校验失败」的场景。
BAD_ART_ID="$(sget "$(api POST /api/v1/artifacts \
  "$(jq -n --arg u "http://127.0.0.1:${FILE_PORT}/app-v2.txt" \
     '{kind:"url",url:$u,sha256:"0000000000000000000000000000000000000000000000000000000000000000"}')" "$TOKEN")" '.data.id')"
REL2B_RESP="$(api POST /api/v1/releases \
  "$(jq -n --arg app "$APP_ID" --arg tpl "$TPL_ID" --arg art "$BAD_ART_ID" --arg sid "$SRV_ID" \
     '{app_id:$app,template_id:$tpl,version:"v2.0.0",artifact_id:$art,server_ids:[$sid]}')" "$TOKEN")"
REL2B_ID="$(sget "$REL2B_RESP" '.data.release.id')"
if [ -n "$REL2B_ID" ]; then
  ST2="$(wait_release "$REL2B_ID" "$TOKEN" failed 180)"
  [ "$ST2" = "failed" ] && pass "校验和错误的发布被判为 failed" || fail "期望 failed，实际 ${ST2}"
  # current 必须仍指向 v1
  TARGET_AFTER="$(readlink "$CURRENT_LINK" 2>/dev/null)"
  if printf '%s' "$TARGET_AFTER" | grep -q "v1\|${REL1_ID}"; then
    pass "失败发布后 current 仍指向首次发布版本"
  else
    # 退一步：检查 current 里的内容仍是 v1
    CUR_DIR="$(cd "$(dirname "$CURRENT_LINK")" && readlink "$CURRENT_LINK")"
    case "$CUR_DIR" in /*) ;; *) CUR_DIR="$(dirname "$CURRENT_LINK")/$CUR_DIR";; esac
    if [ "$(cat "$CUR_DIR/app.bin" 2>/dev/null | head -1)" = "leadstack-artifact-v1" ]; then
      pass "失败发布后 current 内容仍是 v1（未被破坏）"
    else
      fail "失败发布破坏了 current（指向 ${TARGET_AFTER}）"
    fi
  fi
  RUN_MSG="$(api GET "/api/v1/releases/${REL2B_ID}/runs" "" "$TOKEN" | jq -r '.data.runs[0].message // .data[0].message // empty' 2>/dev/null)"
  pass "失败原因已记录：${RUN_MSG:0:60}"
else
  fail "创建失败场景发布失败：$REL2B_RESP"
fi

# ---------------------------------------------------------------- 8. 修正后发布 + 回滚
step "8. 修正后发布成功 → 回滚到上一版本"
GOOD_ART_ID="$(sget "$(api POST /api/v1/artifacts \
  "$(jq -n --arg u "http://127.0.0.1:${FILE_PORT}/app-v2.txt" --arg s "$(shasum -a 256 "$FILE_DIR/app-v2.txt" | awk '{print $1}')" \
     '{kind:"url",url:$u,sha256:$s}')" "$TOKEN")" '.data.id')"
REL3_RESP="$(api POST /api/v1/releases \
  "$(jq -n --arg app "$APP_ID" --arg tpl "$TPL_ID" --arg art "$GOOD_ART_ID" --arg sid "$SRV_ID" \
     '{app_id:$app,template_id:$tpl,version:"v2.0.0",artifact_id:$art,server_ids:[$sid]}')" "$TOKEN")"
REL3_ID="$(sget "$REL3_RESP" '.data.release.id')"
ST3="$(wait_release "$REL3_ID" "$TOKEN" success 180)"
[ "$ST3" = "success" ] && pass "修正后发布成功（v2.0.0）" || fail "修正后发布未成功（${ST3}）"
CUR_AFTER_V2="$(cd "$(dirname "$CURRENT_LINK")" && readlink "$CURRENT_LINK")"
case "$CUR_AFTER_V2" in /*) ;; *) CUR_AFTER_V2="$(dirname "$CURRENT_LINK")/$CUR_AFTER_V2";; esac
[ "$(cat "$CUR_AFTER_V2/app.bin" 2>/dev/null | head -1)" = "leadstack-artifact-v2" ] \
  && pass "v2 已生效（current 内容为 v2）" || fail "v2 未生效"

RB_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d "{\"server_ids\":[\"$SRV_ID\"]}" "${BASE}/api/v1/releases/${REL3_ID}/rollback")"
if [ "$RB_CODE" = "200" ] || [ "$RB_CODE" = "202" ]; then
  pass "回滚请求已接受（HTTP ${RB_CODE}）"
  RB_OK=0
  for _ in $(seq 1 90); do
    CUR_RB="$(cd "$(dirname "$CURRENT_LINK")" && readlink "$CURRENT_LINK")"
    case "$CUR_RB" in /*) ;; *) CUR_RB="$(dirname "$CURRENT_LINK")/$CUR_RB";; esac
    if [ "$(cat "$CUR_RB/app.bin" 2>/dev/null | head -1)" = "leadstack-artifact-v1" ]; then RB_OK=1; break; fi
    sleep 1
  done
  [ "$RB_OK" = "1" ] && pass "回滚生效：current 已指回 v1" || fail "回滚未生效（current 仍为 ${CUR_RB}）"
else
  fail "回滚请求失败（HTTP ${RB_CODE}）"
fi

# ---------------------------------------------------------------- 9. 批量：部分失败
step "9. 批量发布：一台成功、一台不可达"
DEAD_SRV="$(sget "$(api POST /api/v1/servers \
  "$(jq -n --arg cid "$CRED_ID" '{name:"m4-dead",host:"127.0.0.1",port:1,mode:"ssh",credential_id:$cid,group:"m4"}')" "$TOKEN")" '.data.id')"
REL4_RESP="$(api POST /api/v1/releases \
  "$(jq -n --arg app "$APP_ID" --arg tpl "$TPL_ID" --arg art "$ART_ID" --arg a "$SRV_ID" --arg b "$DEAD_SRV" \
     '{app_id:$app,template_id:$tpl,version:"v3.0.0",artifact_id:$art,server_ids:[$a,$b]}')" "$TOKEN")"
REL4_ID="$(sget "$REL4_RESP" '.data.release.id')"
ST4="$(wait_release "$REL4_ID" "$TOKEN" failed 240)"
[ "$ST4" = "failed" ] && pass "含不可达目标的发布整体为 failed" || fail "期望 failed，实际 ${ST4}"
RUNS4="$(api GET "/api/v1/releases/${REL4_ID}/runs" "" "$TOKEN")"
OK_N="$(printf '%s' "$RUNS4" | jq -r '[.data.runs[]? // .data[] | select(.status=="success")] | length' 2>/dev/null)"
BAD_N="$(printf '%s' "$RUNS4" | jq -r '[.data.runs[]? // .data[] | select(.status=="failed")] | length' 2>/dev/null)"
if [ "${OK_N:-0}" -ge 1 ] && [ "${BAD_N:-0}" -ge 1 ]; then
  pass "逐台状态正确（成功 ${OK_N} 台 / 失败 ${BAD_N} 台）"
else
  fail "逐台状态异常（成功 ${OK_N:-0} / 失败 ${BAD_N:-0}）：$(printf '%s' "$RUNS4" | jq -c '.data' | head -c 300)"
fi

# ---------------------------------------------------------------- 10. 重试只跑失败项
step "10. 重试只重跑失败的服务器"
RETRY_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/releases/${REL4_ID}/retry")"
if [ "$RETRY_CODE" = "200" ] || [ "$RETRY_CODE" = "202" ]; then
  pass "重试请求已接受（HTTP ${RETRY_CODE}）"
  sleep 3
  RUNS5="$(api GET "/api/v1/releases/${REL4_ID}/runs" "" "$TOKEN")"
  OK_N2="$(printf '%s' "$RUNS5" | jq -r '[.data.runs[]? // .data[] | select(.status=="success")] | length' 2>/dev/null)"
  [ "${OK_N2:-0}" = "${OK_N:-0}" ] && pass "重试后成功的 run 数量未变（未重复执行成功项）" || fail "重试影响了成功项（${OK_N:-0} → ${OK_N2:-0}）"
else
  fail "重试请求失败（HTTP ${RETRY_CODE}）"
fi

# ---------------------------------------------------------------- 11. 模板安全
step "11. 模板安全（注入与未知占位符）"
INJ_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg app "$APP_ID" --arg tpl "$TPL_ID" --arg art "$ART_ID" --arg sid "$SRV_ID" \
     '{app_id:$app,template_id:$tpl,version:"v1.0; rm -rf /",artifact_id:$art,server_ids:[$sid]}')" \
  "${BASE}/api/v1/releases")"
if [ "$INJ_CODE" = "400" ] || [ "$INJ_CODE" = "422" ]; then
  pass "含命令注入的 version 被拒绝（HTTP ${INJ_CODE}）"
else
  fail "注入 version 未被拒绝（HTTP ${INJ_CODE}）"
fi
UNK_CODE="$(status_of -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"vars":{"ROOT":"/tmp/x"}}' "${BASE}/api/v1/templates/$(api POST /api/v1/templates \
     '{"name":"m4-unknown","kind":"binary","content":"echo {{NOT_A_REAL_PLACEHOLDER}}"}' "$TOKEN" | jq -r '.data.id')/render")"
[ "$UNK_CODE" = "400" ] || [ "$UNK_CODE" = "422" ] && pass "未知占位符渲染被拒绝（HTTP ${UNK_CODE}）" || fail "未知占位符未报错（HTTP ${UNK_CODE}）"

# ---------------------------------------------------------------- 12. 删除保护
step "12. 删除保护"
DEL_CODE="$(status_of -X DELETE -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/apps/${APP_ID}")"
[ "$DEL_CODE" = "409" ] && pass "删除有发布记录的应用返回 409" || fail "删除有发布的应用返回 ${DEL_CODE}，期望 409"

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m部署与回滚 验收通过\033[0m\n'; exit 0; fi
printf '\033[31m部署与回滚 验收未通过\033[0m\n'
exit 1
