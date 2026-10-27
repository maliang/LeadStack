#!/usr/bin/env bash
# LeadStack 部署编排验收：灰度发布 / 回滚到指定版本 / 接管体检
#
# devssh 让命令在本机真实执行：断言检查 current 符号链接、真实 git 仓库与目录权限。
# 用法：make build && make devssh && bash scripts/verify_deploy_rollout.sh
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18985}"
SSH_PORT="${SSH_PORT:-0}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="verify-rollout-pass"

TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
DATA_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-rollout.XXXXXX")"
DEPLOY_ROOT="$DATA_DIR/apps"
LEAD_PID=""; DEVSSH_PID=""
PASS=0; FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  for p in "$DEVSSH_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
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
wait_release() {
  local id="$1" want="$2" max="${3:-90}" i st
  for i in $(seq 1 "$max"); do
    st="$(sget "$(api GET "/api/v1/releases/${id}" "" "$TOKEN")" '.data.release.status')"
    [ "$st" = "$want" ] && { echo "$st"; return 0; }
    case "$st" in failed|success|canceled) echo "$st"; return 1;; esac
    sleep 1
  done
  echo "${st:-timeout}"; return 1
}
make_artifact() { # <name> -> sha (tar.gz)
  local name="$1" dir="$DATA_DIR/pkg-$1"
  mkdir -p "$dir"
  echo "payload-$name" > "$dir/payload.txt"
  tar -czf "$DATA_DIR/$name.tar.gz" -C "$dir" .
  shasum -a 256 "$DATA_DIR/$name.tar.gz" | awk '{print $1}'
}
upload_artifact() { # <file> -> id
  sget "$(curl -sS -X POST "${BASE}/api/v1/artifacts/upload" -H "Authorization: Bearer ${TOKEN}" -F "file=@$1;type=application/gzip")" '.data.id'
}
dump_runs() { # <release_id>：失败时打印每台的状态与日志尾部，便于定位（不改变断言结果）
  local rel="$1" runs
  runs="$(api GET "/api/v1/releases/$rel/runs" "" "$TOKEN")"
  printf '    runs: %s\n' "$(printf '%s' "$runs" | jq -c '[.data.runs[] | {server_id,status,failed_step,error}]' 2>/dev/null)"
  printf '%s' "$runs" | jq -r '.data.runs[0].log // empty' 2>/dev/null | tail -12 | sed 's/^/    | /'
}

create_release() { # <app> <tmpl> <version> <artifact> <strategy> <canary> <hold>
  local body resp
  body="$(jq -n --arg a "$1" --arg t "$2" --arg v "$3" --arg art "$4" --arg st "$5" \
    --arg s1 "$SRV1" --arg s2 "$SRV2" --argjson canary "${6:-0}" --argjson hold "${7:-0}" \
    '{app_id:$a,template_id:$t,version:$v,artifact_id:$art,server_ids:[$s1,$s2],strategy:$st,canary_count:$canary,hold_seconds:$hold}')"
  resp="$(api POST /api/v1/releases "$body" "$TOKEN")"
  local id; id="$(sget "$resp" '.data.release.id')"
  [ -z "$id" ] && echo "RELEASE_ERROR: $resp" >&2
  printf '%s' "$id"
}

echo "临时数据目录：$DATA_DIR"

# ---------------------------------------------------------------- 环境
step "1. 启动 devssh / 控制端 / 两台服务器"
ssh-keygen -q -t ed25519 -N '' -f "$DATA_DIR/id" >/dev/null 2>&1
./bin/leadstack-devssh --addr "127.0.0.1:${SSH_PORT}" --user devuser --password devpass \
  --authorized-key-file "$DATA_DIR/id.pub" --port-file "$DATA_DIR/ssh.port" > "$DATA_DIR/devssh.log" 2>&1 &
DEVSSH_PID=$!
SPORT=""
for _ in $(seq 1 40); do SPORT="$(cat "$DATA_DIR/ssh.port" 2>/dev/null | tr -d '[:space:]')"; [ -n "$SPORT" ] && break; sleep 0.25; done
[ -n "$SPORT" ] && pass "devssh 已就绪" || { fail "devssh 未就绪"; exit 1; }

env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
    LEADSTACK_DATA_DIR="$DATA_DIR/data" \
    LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
    LEADSTACK_SSH_INTERVAL="30s" \
    LEADSTACK_DEPLOY_ROOT="$DEPLOY_ROOT" \
    LEADSTACK_PUBLIC_URL="$BASE" \
    ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
for _ in $(seq 1 80); do [ "$(code GET /healthz)" = "200" ] && break; sleep 0.3; done
[ "$(code GET /healthz)" = "200" ] && pass "控制端就绪" || { fail "控制端未就绪"; tail -20 "$DATA_DIR/lead.log"; exit 1; }

TOKEN="$(sget "$(api POST /api/v1/auth/login "$(jq -n --arg u admin --arg p "$ADMIN_PASS" '{username:$u,password:$p}')")" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败"; exit 1; }

CRED_ID="$(sget "$(api POST /api/v1/credentials "$(jq -n --arg k "$(cat "$DATA_DIR/id")" '{name:"rollout-key",kind:"key",username:"devuser",private_key:$k}')" "$TOKEN")" '.data.id')"
SRV1="$(sget "$(api POST /api/v1/servers "$(jq -n --argjson port "$SPORT" --arg cid "$CRED_ID" '{name:"srv-a",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid}')" "$TOKEN")" '.data.id')"
SRV2="$(sget "$(api POST /api/v1/servers "$(jq -n --argjson port "$SPORT" --arg cid "$CRED_ID" '{name:"srv-b",host:"127.0.0.1",port:$port,mode:"ssh",credential_id:$cid}')" "$TOKEN")" '.data.id')"
[ -n "$SRV1" ] && [ -n "$SRV2" ] && pass "两台服务器已创建" || { fail "创建服务器失败"; exit 1; }

APP_ID="$(sget "$(api POST /api/v1/apps '{"name":"灰度应用","slug":"rollout","deploy_mode":"release"}' "$TOKEN")" '.data.id')"
TPL_ID="$(sget "$(api POST /api/v1/templates '{"name":"空模板","kind":"binary","content":""}' "$TOKEN")" '.data.id')"
[ -n "$APP_ID" ] && [ -n "$TPL_ID" ] && pass "应用与模板已创建" || { fail "创建应用/模板失败"; exit 1; }
SHA1="$(make_artifact r1)"; ART1="$(upload_artifact "$DATA_DIR/r1.tar.gz")"
[ -n "$ART1" ] && pass "制品 r1 已上传" || fail "上传制品失败"

# ---------------------------------------------------------------- 灰度
step "2. 灰度发布"
BAD_SINGLE="$(code POST /api/v1/releases "$(jq -n --arg a "$APP_ID" --arg t "$TPL_ID" --arg art "$ART1" --arg s "$SRV1" '{app_id:$a,template_id:$t,version:"0.0.1",artifact_id:$art,server_ids:[$s],strategy:"canary",canary_count:1,hold_seconds:1}')" "$TOKEN")"
[ "$BAD_SINGLE" = "400" ] && pass "只选 1 台服务器不能灰度（400）" || fail "单机灰度应 400，实际 $BAD_SINGLE"
BAD_COUNT="$(code POST /api/v1/releases "$(jq -n --arg a "$APP_ID" --arg t "$TPL_ID" --arg art "$ART1" --arg s1 "$SRV1" --arg s2 "$SRV2" '{app_id:$a,template_id:$t,version:"0.0.2",artifact_id:$art,server_ids:[$s1,$s2],strategy:"canary",canary_count:2,hold_seconds:1}')" "$TOKEN")"
[ "$BAD_COUNT" = "400" ] && pass "canary_count 必须小于服务器数（400）" || fail "非法 canary_count 应 400，实际 $BAD_COUNT"

CANARY="$(create_release "$APP_ID" "$TPL_ID" "1.0.0" "$ART1" canary 1 2)"
[ -n "$CANARY" ] && pass "灰度发布已创建" || fail "创建灰度发布失败"
[ "$(wait_release "$CANARY" success 120)" = "success" ] && pass "灰度发布成功（两批都完成）" || fail "灰度发布失败"
CANARY_RUNS="$(api GET "/api/v1/releases/$CANARY/runs" "" "$TOKEN")"
SUCC_COUNT="$(printf '%s' "$CANARY_RUNS" | jq -r '[.data.runs[] | select(.status=="success")] | length')"
[ "$SUCC_COUNT" = "2" ] && pass "两台都成功" || fail "成功台数不符：$SUCC_COUNT"
printf '%s' "$CANARY_RUNS" | jq -e '.data.runs[0] | has("status")' >/dev/null && pass "runs 结构正常" || fail "runs 结构异常"
CANARY_META="$(sget "$(api GET "/api/v1/releases/$CANARY" "" "$TOKEN")" '.data.release.strategy')"
[ "$CANARY_META" = "canary" ] && pass "发布记录保留 strategy=canary" || fail "strategy 未落库：$CANARY_META"

# ---------------------------------------------------------------- 回滚到指定版本
step "3. 回滚到指定版本"
SHA2="$(make_artifact r2)"; ART2="$(upload_artifact "$DATA_DIR/r2.tar.gz")"
# 两台「服务器」在验收环境里其实是同一台机器上的同一个部署根，并行发布会在 `ln -sfn current` 上互相抢；
# 用灰度串行（canary 1）避免这个环境假象，真实多机部署不存在共享路径。
REL2="$(create_release "$APP_ID" "$TPL_ID" "1.0.1" "$ART2" canary 1 0)"
[ "$(wait_release "$REL2" success 120)" = "success" ] && pass "第二个版本发布成功" || { fail "第二个版本发布失败"; dump_runs "$REL2"; }

TARGETS="$(api GET "/api/v1/apps/$APP_ID/rollback-targets" "" "$TOKEN")"
TARGET_COUNT="$(printf '%s' "$TARGETS" | jq -r '.data | length' 2>/dev/null)"
[ "${TARGET_COUNT:-0}" -ge 2 ] && pass "回滚目标列表返回 ${TARGET_COUNT} 个历史版本" || fail "回滚目标列表异常：$TARGET_COUNT"
LISTED_CANARY="$(printf '%s' "$TARGETS" | jq -r --arg id "$CANARY" '.data[] | select(.release_id==$id) | .version')"
[ "$LISTED_CANARY" = "1.0.0" ] && pass "列表包含 1.0.0 版本" || fail "列表缺少 1.0.0：$LISTED_CANARY"

RB="$(api POST "/api/v1/releases/$REL2/rollback" "$(jq -n --arg t "$CANARY" --arg s1 "$SRV1" --arg s2 "$SRV2" '{target_release_id:$t,server_ids:[$s1,$s2]}')" "$TOKEN")"
ROLLED="$(sget "$RB" '.data.rolled_back')"
[ "$ROLLED" = "2" ] && pass "回滚到 1.0.0 成功（2 台）" || fail "回滚结果异常：$RB"
CURRENT_LINK="$(readlink "$DEPLOY_ROOT/rollout/current" 2>/dev/null || true)"
printf '%s' "$CURRENT_LINK" | grep -q "releases/$CANARY$" && pass "current 已指向 1.0.0 的 release 目录" || fail "current 指向不符：$CURRENT_LINK"

SCRIPT_DIR="$DEPLOY_ROOT/scriptless"
mkdir -p "$SCRIPT_DIR"
echo '{"v":1}' > "$SCRIPT_DIR/state.json"
SCRIPT_TPL="$(sget "$(api POST /api/v1/templates "$(jq -n --arg c "#!/bin/sh\nset -eu\necho deploy-{{VERSION}} >> state.json" '{name:"script-update",kind:"binary",content:$c}')" "$TOKEN")" '.data.id')"
SCRIPT_APP="$(sget "$(api POST /api/v1/apps "$(jq -n --arg t "$SCRIPT_DIR" '{name:"脚本模式",slug:"scriptless",deploy_mode:"script",target_dir:$t}')" "$TOKEN")" '.data.id')"
SCRIPT_BODY="$(jq -n --arg a "$SCRIPT_APP" --arg t "$SCRIPT_TPL" --arg s "$SRV1" '{app_id:$a,template_id:$t,version:"2.0.0",server_ids:[$s]}')"
SCRIPT_REL="$(sget "$(api POST /api/v1/releases "$SCRIPT_BODY" "$TOKEN")" '.data.release.id')"
[ -n "$SCRIPT_REL" ] && pass "脚本模式发布已创建（无制品）" || fail "脚本模式发布创建失败"
[ "$(wait_release "$SCRIPT_REL" success 60)" = "success" ] && pass "脚本模式首次发布成功" || fail "脚本模式发布失败"
REJECT="$(code POST "/api/v1/releases/$SCRIPT_REL/rollback" "$(jq -n --arg t "$SCRIPT_REL" '{target_release_id:$t}')" "$TOKEN")"
[ "$REJECT" = "400" ] && pass "脚本模式回滚到指定版本被拒（400）" || fail "脚本模式应拒绝，实际 $REJECT"
REJECT_MSG="$(api POST "/api/v1/releases/$SCRIPT_REL/rollback" "$(jq -n --arg t "$SCRIPT_REL" '{target_release_id:$t}')" "$TOKEN" | jq -r '.error.message // empty')"
printf '%s' "$REJECT_MSG" | grep -q "不支持回滚到指定版本" && pass "拒绝原因说明清楚：$REJECT_MSG" || fail "拒绝原因不明确：$REJECT_MSG"

# ---------------------------------------------------------------- 接管体检
step "4. 接管体检"
GIT_DIR="$DATA_DIR/git-site"
git init -q "$GIT_DIR"
git -C "$GIT_DIR" config user.email v@example.com
git -C "$GIT_DIR" config user.name verify
echo "hello" > "$GIT_DIR/index.php"
git -C "$GIT_DIR" add -A && git -C "$GIT_DIR" commit -qm init
DIAG="$(api POST "/api/v1/servers/$SRV1/diagnose" "$(jq -n --arg d "$GIT_DIR" '{kind:"site",name:"git-site",target_dir:$d}')" "$TOKEN")"
DIAG_MODE="$(sget "$DIAG" '.data.recommendation.deploy_mode')"
[ "$DIAG_MODE" = "script" ] && pass "体检识别 git 仓库并建议脚本模式" || fail "体检建议不符：$DIAG_MODE"
printf '%s' "$DIAG" | jq -e '.data.recommendation.script | test("git pull")' >/dev/null 2>&1 \
  && pass "体检给出 git pull 脚本建议" || fail "缺少脚本建议"
DIR_CHECK="$(printf '%s' "$DIAG" | jq -r '.data.checks[] | select(.id=="dir") | .status')"
[ "$DIR_CHECK" = "pass" ] && pass "体检通过项包含目录检查" || fail "目录检查状态：$DIR_CHECK"
BAD_DIAG="$(code POST "/api/v1/servers/$SRV1/diagnose" '{"kind":"nope","name":"x"}' "$TOKEN")"
[ "$BAD_DIAG" = "400" ] && pass "非法 kind 被拒（400）" || fail "非法 kind 应 400，实际 $BAD_DIAG"

# ---------------------------------------------------------------- 中止发布
step "5. 中止进行中的灰度发布（A26）"
SHA3="$(make_artifact r3)"; ART3="$(upload_artifact "$DATA_DIR/r3.tar.gz")"
ABORT_REL="$(create_release "$APP_ID" "$TPL_ID" "1.0.2" "$ART3" canary 1 30)"
[ -n "$ABORT_REL" ] && pass "可中止的灰度发布已创建（hold 30s）" || fail "创建待中止发布失败"
# 等到「第一批已成功、第二批仍 pending」这个时间窗，此时中止才有意义。
ABORT_READY=0
for _ in $(seq 1 90); do
  ABORT_RUNS="$(api GET "/api/v1/releases/$ABORT_REL/runs" "" "$TOKEN")"
  S_N="$(printf '%s' "$ABORT_RUNS" | jq -r '[.data.runs[] | select(.status=="success")] | length' 2>/dev/null)"
  P_N="$(printf '%s' "$ABORT_RUNS" | jq -r '[.data.runs[] | select(.status=="pending")] | length' 2>/dev/null)"
  if [ "${S_N:-0}" -ge 1 ] && [ "${P_N:-0}" -ge 1 ]; then ABORT_READY=1; break; fi
  sleep 1
done
[ "$ABORT_READY" = "1" ] && pass "进入可中止窗口（成功 ${S_N} 台 / 待执行 ${P_N} 台）" \
  || fail "未能进入可中止窗口（成功 ${S_N:-?} 台 / 待执行 ${P_N:-?} 台）：$(printf '%s' "$ABORT_RUNS" | jq -c '[.data.runs[] | {status}]')"

ABORT_RESP="$(api POST "/api/v1/releases/$ABORT_REL/cancel" "" "$TOKEN")"
ABORT_CANCELED="$(sget "$ABORT_RESP" '.data.canceled')"
[ "$ABORT_CANCELED" = "1" ] && pass "中止只取消未开始的 1 台（data.canceled=${ABORT_CANCELED}）" \
  || fail "中止台数不符：data.canceled=${ABORT_CANCELED:-空}（应为 1）resp=$(printf '%s' "$ABORT_RESP" | jq -c '.data' 2>/dev/null)"

ABORT_ST=""
for _ in $(seq 1 40); do
  ABORT_ST="$(sget "$(api GET "/api/v1/releases/$ABORT_REL" "" "$TOKEN")" '.data.release.status')"
  case "$ABORT_ST" in success|failed|canceled) break;; esac
  sleep 1
done
case "$ABORT_ST" in
  canceled) pass "中止后发布收敛到 canceled（1 台成功 + 1 台取消 ≠ 失败，A26 语义）";;
  success|failed) fail "中止后发布状态应为 canceled，实际 ${ABORT_ST}（有机器成功不代表发布成功，有机器取消也不代表失败）";;
  *) fail "中止后发布仍停在 ${ABORT_ST:-空}（应进入 canceled）";;
esac
ABORT_UNFINISHED="$(api GET "/api/v1/releases/$ABORT_REL/runs" "" "$TOKEN" \
  | jq -r '[.data.runs[] | select(.status=="pending" or .status=="running")] | length' 2>/dev/null)"
[ "${ABORT_UNFINISHED:-1}" = "0" ] && pass "中止后没有残留 pending/running 的 run" \
  || fail "中止后仍有 ${ABORT_UNFINISHED:-?} 个 run 未收尾"

ABORT_RUNS2="$(api GET "/api/v1/releases/$ABORT_REL/runs" "" "$TOKEN")"
KEPT="$(printf '%s' "$ABORT_RUNS2" | jq -r '[.data.runs[] | select(.status=="success")] | length' 2>/dev/null)"
GONE="$(printf '%s' "$ABORT_RUNS2" | jq -r '[.data.runs[] | select(.status=="canceled")] | length' 2>/dev/null)"
[ "${KEPT:-0}" = "1" ] && [ "${GONE:-0}" = "1" ] \
  && pass "已完成的 ${KEPT} 台保持 success、未开始的 ${GONE} 台标记 canceled（已完成不回滚）" \
  || fail "中止后 run 状态不符：success=${KEPT:-?}、canceled=${GONE:-?}（应为 1/1）"

# 纯中止（还没有任何一台成功）：应直接落 canceled，而不是 failed——这是「中止」与「失败」的分界。
FULL_REL=""; FULL_CANCELED=0
for _ in 1 2 3; do
  FULL_REL="$(create_release "$APP_ID" "$TPL_ID" "1.0.3" "$ART3" all 0 0)"
  [ -z "$FULL_REL" ] && break
  FULL_CANCELED="$(sget "$(api POST "/api/v1/releases/$FULL_REL/cancel" "" "$TOKEN")" '.data.canceled')"
  [ "${FULL_CANCELED:-0}" = "2" ] && break
  wait_release "$FULL_REL" success 90 >/dev/null 2>&1 || true
  FULL_CANCELED=0
done
[ "${FULL_CANCELED:-0}" = "2" ] && pass "纯中止：两台都在开始前被取消（data.canceled=${FULL_CANCELED}）" \
  || fail "纯中止未能取消两台：data.canceled=${FULL_CANCELED:-空}（应 2）"
FULL_ST=""
for _ in $(seq 1 40); do
  FULL_ST="$(sget "$(api GET "/api/v1/releases/$FULL_REL" "" "$TOKEN")" '.data.release.status')"
  [ "$FULL_ST" = "canceled" ] && break
  sleep 1
done
[ "$FULL_ST" = "canceled" ] && pass "无任何 success 的发布状态为 canceled（实际 ${FULL_ST}）" \
  || fail "纯中止状态应为 canceled，实际 ${FULL_ST}"

# 前端「中止发布」入口契约（A26）：确认框写明「已完成的 N 台不会回滚」、按钮为 danger、台数取 data.canceled。
dist_has "前端中止发布入口（A26）" \
  'release.abort' 'release.abortConfirmTitle' 'release.abortConfirmBody' 'release.abortDone' \
  '不会回滚' 'cancelRelease('

# ---------------------------------------------------------------- 汇总
echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m通过 %d 项，失败 0 项\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31m通过 %d 项，失败 %d 项\033[0m\n' "$PASS" "$FAIL"
exit 1
