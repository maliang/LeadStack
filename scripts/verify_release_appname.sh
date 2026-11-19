#!/usr/bin/env bash
# 发布应用名「真链路」验收（r3）
#
# 目的：证明 app_name 不是只在单元测试里绿，而是**真控制端 + 真 agent + 真应用/发布 + 真浏览器**
# 全链路可用，并把断言固化在仓库里（不是 /tmp 一次性脚本）。
#
# 覆盖：
#   ① GET /api/v1/releases 每条都带非空 app_name，且等于所建应用名；
#   ② GET /api/v1/releases/{id} 同样带 app_name / app_slug；
#   ③ 应用被删（有发布时 API 返回 409，这里按运维直删模拟孤儿行）后：
#      列表/详情 app_name、app_slug 均为空串，HTTP 200，不再 500；
#   ④ 首页（/#/）与 /#/releases 渲染的应用名逐字相同，且等于所建应用名（真浏览器）；
#   ⑤ 旧后端兼容：反代删掉响应里的 app_name（保留 app_slug），前端仍靠 /apps 映射出应用名
#      —— 若显示 slug 就说明 releaseAppLabel 优先级回退，断言红（真浏览器）。
#   ⑥ 两应用交叉校验（r5）：再建第二个应用并各发一单，列表里每一行的 app_name
#      必须等于**它自己 app_id 对应的应用名**（不是「都等于第一个应用名」）——
#      单元素集合上的 every 会被常量 JOIN 骗过，这里用两行不同期望值把常量打死。
#
# 前置：make build（本脚本会检查 bin 是否落后于源码，避免拿旧二进制跑出假绿）
# 用法：bash scripts/verify_release_appname.sh
#       NEGATIVE_CONTROL=1 bash scripts/verify_release_appname.sh   # 负向对照（见下）
#
# 负向对照（可选，慢）：在临时 git worktree 里把 deploy-domain.ts 的优先级还原成旧序
#   （app_name || app_slug || appNames），构建后跑本脚本；proxy-fallback 变红而其余全绿
#   即证明该断言真的能抓旧序（本模式成功）。不污染主工作树，见
#   scripts/verify_release_appname_negative_control.sh。
# 依赖：curl / jq / sqlite3 / node（playwright 全局或本地；chromium 缺失时用 ms-playwright 缓存）
# 可重复跑：临时 data-dir + 固定端口 + trap 清理（停进程、删目录）；端口被占会直接报错。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 负向对照模式：把控制权交给专门脚本（临时 worktree 构建旧序，断言 proxy-fallback 必红）。
# 放在创建临时 data-dir / 注册 trap 之前，避免负向模式写坏正式报告。
if [ "${NEGATIVE_CONTROL:-0}" = "1" ]; then
  exec bash "$ROOT/scripts/verify_release_appname_negative_control.sh"
fi

PORT="${PORT:-17660}"
PROXY_PORT="${PROXY_PORT:-17661}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m2-pass"
APP_NAME="Playwright"
APP_SLUG="verify-appname-$(date +%s)-$$"
# 第二个应用：名字与 slug 都不同（slug 上限 40，APP_SLUG 约 31 位，追加 -b 仍合法）。
APP2_NAME="second-${APP_SLUG}"
APP2_SLUG="${APP_SLUG}-b"
REPORT="${REPORT:-/tmp/review-appname.md}"

TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
DATA_DIR="$(mktemp -d "${TMP_CANDIDATE%/}/leadstack-appname.XXXXXX")"
DB="$DATA_DIR/data/leadstack.db"
LEAD_PID=""; AGENT_PID=""
PASS=0; FAIL=0
ROWS=()
TOKEN=""; APP_ID=""; REL_ID=""; APP2_ID=""; REL2_ID=""; SRV_ID=""; ETOKEN=""
LOG_TAIL=""
CLEANED="no"
REPORT_WRITTEN=0

mkdir -p "$DATA_DIR"

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
record() { # <id> <title> <PASS|FAIL> <evidence>
  local id="$1" title="$2" status="$3" evidence="$4"
  if [ "$status" = "PASS" ]; then pass "${id} ${title}"; else fail "${id} ${title} — ${evidence}"; fi
  ROWS+=("| ${id} | ${title} | ${status} | ${evidence//|/\\|} |")
}

api() { # <method> <path> [body] [token]
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
code() { # <method> <path> [body] [token]
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -o /dev/null -w '%{http_code}' -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}
jqget() { printf '%s' "$1" | jq -r "$2 // empty" 2>/dev/null; }

port_busy() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

write_report() {
  [ "$REPORT_WRITTEN" -eq 1 ] && return 0
  REPORT_WRITTEN=1
  local verdict="FAIL（通过 ${PASS}，失败 ${FAIL}）"
  [ "$FAIL" -eq 0 ] && [ "$PASS" -gt 0 ] && verdict="PASS（${PASS} 项断言全部通过）"
  {
    printf '# 发布应用名真链路验收报告\n\n'
    printf -- '- 执行时间：%s\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')"
    printf -- '- 控制端：`%s`（端口 %s；代理端口 %s）\n' "$BASE" "$PORT" "$PROXY_PORT"
    printf -- '- 真 agent：server_id=`%s`\n' "${SRV_ID:-<未上线>}"
    printf -- '- 应用：`%s`（slug=`%s`，id=`%s`）；`%s`（slug=`%s`，id=`%s`）\n' \
      "$APP_NAME" "$APP_SLUG" "${APP_ID:-<未创建>}" "$APP2_NAME" "$APP2_SLUG" "${APP2_ID:-<未创建>}"
    printf -- '- 发布单：id=`%s`（应用一）、id=`%s`（应用二）\n' "${REL_ID:-<未创建>}" "${REL2_ID:-<未创建>}"
    printf -- '- 临时数据目录：`%s`（退出时删除=%s）\n' "$DATA_DIR" "$CLEANED"
    printf -- '- 脚本：`scripts/verify_release_appname.sh` + `scripts/verify_release_appname_browser.mjs`\n\n'
    printf '## 结论：%s\n\n' "$verdict"
    printf '| # | 断言 | 结果 | 证据 |\n|---|------|------|------|\n'
    if [ "${#ROWS[@]}" -gt 0 ]; then printf '%s\n' "${ROWS[@]}"; fi
    printf '\n## 复跑\n\n```bash\nmake build && bash scripts/verify_release_appname.sh\n```\n'
    printf '\n## 清理说明\n\n'
    printf -- '- `trap EXIT` 停掉控制端与 agent（TERM→KILL），等待退出后 `rm -rf` 临时 data-dir；\n'
    printf -- '- 端口 %s/%s 用固定值但数据目录每次 mktemp，脚本可重复跑；端口被占会提前失败而不是连到旧进程。\n' "$PORT" "$PROXY_PORT"
    if [ -n "$LOG_TAIL" ]; then
      printf '\n## 失败日志尾部\n\n```text\n%s\n```\n' "$LOG_TAIL"
    fi
  } > "$REPORT"
}

cleanup() {
  trap - EXIT
  if [ -n "$DATA_DIR" ] && [ -d "$DATA_DIR" ]; then
    LOG_TAIL="$(tail -15 "$DATA_DIR/lead.log" 2>/dev/null)"
  fi
  for p in "$AGENT_PID" "$LEAD_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  local i alive
  for i in $(seq 1 30); do
    alive=0
    for p in "$AGENT_PID" "$LEAD_PID"; do
      [ -n "$p" ] && kill -0 "$p" 2>/dev/null && alive=1
    done
    [ "$alive" -eq 0 ] && break
    sleep 0.1
  done
  for p in "$AGENT_PID" "$LEAD_PID"; do [ -n "$p" ] && kill -9 "$p" 2>/dev/null; done
  wait 2>/dev/null
  rm -rf "$DATA_DIR"
  [ ! -e "$DATA_DIR" ] && CLEANED="yes"
  write_report
}
trap cleanup EXIT

# ---------------------------------------------------------------- 0. 前置检查
step "0. 前置检查"

if [ ! -x "$ROOT/bin/leadstack" ] || [ ! -x "$ROOT/bin/leadstack-agent" ]; then
  record "preflight-bin" "bin/leadstack 与 bin/leadstack-agent 存在" FAIL "缺少二进制，请先 make build"
  exit 1
fi
STALE=""
for src in internal/store/deploy.go internal/store/models.go internal/api/handlers_deploy.go \
           web-next/src/lib/deploy-domain.ts web-next/src/lib/api.ts; do
  [ "$ROOT/$src" -nt "$ROOT/bin/leadstack" ] && STALE="${STALE} ${src}"
done
if [ -n "$STALE" ]; then
  record "preflight-fresh" "bin/leadstack 不落后于契约源码" FAIL "以下源码比二进制新：${STALE}；请先 make build"
  exit 1
fi
record "preflight-fresh" "bin/leadstack 不落后于契约源码" PASS "契约源码均早于 bin/leadstack（$(date -r "$ROOT/bin/leadstack" '+%H:%M:%S')）"

for bin in curl jq sqlite3 node; do
  command -v "$bin" >/dev/null 2>&1 || { record "preflight-dep" "依赖 ${bin}" FAIL "PATH 中找不到 ${bin}"; exit 1; }
done
record "preflight-dep" "curl/jq/sqlite3/node 可用" PASS "依赖齐全"

if port_busy "$PORT" || port_busy "$PROXY_PORT"; then
  record "preflight-port" "端口 ${PORT}/${PROXY_PORT} 空闲" FAIL "端口已被占用（可能是上一次未清理的进程）"
  exit 1
fi
record "preflight-port" "端口 ${PORT}/${PROXY_PORT} 空闲" PASS "避开 17681-17697/8080/18111/18471/19191/19870/19990/20191"

# ---------------------------------------------------------------- 1. 控制端 + agent
step "1. 启动控制端与真 agent"
env LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
    LEADSTACK_DATA_DIR="$DATA_DIR/data" \
    LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
    LEADSTACK_DEPLOY_ROOT="$DATA_DIR/release-root" \
    LEADSTACK_PUBLIC_URL="$BASE" \
    ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!
READY=0
for _ in $(seq 1 120); do [ "$(code GET /healthz)" = "200" ] && { READY=1; break; }; sleep 0.25; done
if [ "$READY" -ne 1 ]; then
  record "ctl-ready" "控制端在 ${PORT} 就绪" FAIL "healthz 未就绪"; exit 1
fi
record "ctl-ready" "控制端在 ${PORT} 就绪" PASS "GET /healthz=200"

LOGIN_BODY="$(jq -n --arg u "$ADMIN_USER" --arg p "$ADMIN_PASS" '{username:$u,password:$p}')"
LOGIN_RESP="$(api POST /api/v1/auth/login "$LOGIN_BODY")"
TOKEN="$(jqget "$LOGIN_RESP" '.data.token')"
if [ -z "$TOKEN" ]; then
  record "login" "管理员登录" FAIL "登录响应无 token"; exit 1
fi
record "login" "管理员登录" PASS "拿到 Bearer token"

ETOKEN_RESP="$(api POST /api/v1/enroll-tokens '{"note":"r3-appname","ttl_s":900}' "$TOKEN")"
ETOKEN="$(jqget "$ETOKEN_RESP" '.data.token')"
if [ -z "$ETOKEN" ]; then
  record "agent-token" "创建接入令牌" FAIL "响应无 token"; exit 1
fi
./bin/leadstack-agent --server "$BASE" --token "$ETOKEN" > "$DATA_DIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 80); do
  SRV_ID="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '[.data[] | select(.mode=="agent")][0].id // empty' 2>/dev/null)"
  [ -n "$SRV_ID" ] && break
  sleep 0.25
done
if [ -z "$SRV_ID" ]; then
  record "agent-online" "真 agent 已注册为 server" FAIL "等待 20s 仍未上线"; exit 1
fi
record "agent-online" "真 agent 已注册为 server" PASS "server_id=${SRV_ID:0:8}…（mode=agent）"

# ---------------------------------------------------------------- 2. 真建应用 / 模板 / 发布
step "2. 真建应用与发布"
APP_BODY="$(jq -n --arg n "$APP_NAME" --arg s "$APP_SLUG" --arg td "$DATA_DIR/app-target" \
  '{name:$n,slug:$s,deploy_mode:"script",target_dir:$td,restart_cmd:"true"}')"
APP_RESP="$(api POST /api/v1/apps "$APP_BODY" "$TOKEN")"
APP_ID="$(jqget "$APP_RESP" '.data.id')"
if [ -z "$APP_ID" ]; then
  record "app-create" "创建应用" FAIL "响应无 id：${APP_RESP:0:200}"; exit 1
fi
record "app-create" "创建应用「${APP_NAME}」" PASS "app_id=${APP_ID:0:8}…"

TPL_BODY="$(jq -n --arg n "r3-appname-${APP_SLUG}" '{name:$n,kind:"binary",content:"echo appname-verify-ok"}')"
TPL_RESP="$(api POST /api/v1/templates "$TPL_BODY" "$TOKEN")"
TPL_ID="$(jqget "$TPL_RESP" '.data.id')"
if [ -z "$TPL_ID" ]; then
  record "tpl-create" "创建 binary 模板" FAIL "响应无 id：${TPL_RESP:0:200}"; exit 1
fi

REL_BODY="$(jq -n --arg a "$APP_ID" --arg t "$TPL_ID" --arg v "0.0.1" --arg s "$SRV_ID" \
  '{app_id:$a,template_id:$t,version:$v,server_ids:[$s]}')"
REL_RESP="$(api POST /api/v1/releases "$REL_BODY" "$TOKEN")"
REL_ID="$(jqget "$REL_RESP" '.data.release.id')"
if [ -z "$REL_ID" ]; then
  record "release-create" "创建发布单" FAIL "响应无 release.id：${REL_RESP:0:300}"; exit 1
fi
record "release-create" "创建发布单 v0.0.1" PASS "release_id=${REL_ID:0:8}…（目标 server=${SRV_ID:0:8}…，真 agent 执行）"

# 创建响应本身也应带 app_name（前端 201 后直接渲染）。
CREATE_NAME="$(jqget "$REL_RESP" '.data.release.app_name')"
record "release-create-appname" "创建响应带 app_name" "$([ "$CREATE_NAME" = "$APP_NAME" ] && echo PASS || echo FAIL)" \
  "app_name=${CREATE_NAME:-<空>}，期望 ${APP_NAME}"

# 第二个应用（名字/slug 都不同）+ 第二单：让列表出现两行不同期望值，
# 常量 JOIN（不管 app_id 都填同一个应用名）会被 1-list-two-apps 打死。
APP2_BODY="$(jq -n --arg n "$APP2_NAME" --arg s "$APP2_SLUG" --arg td "$DATA_DIR/app-target-2" \
  '{name:$n,slug:$s,deploy_mode:"script",target_dir:$td,restart_cmd:"true"}')"
APP2_RESP="$(api POST /api/v1/apps "$APP2_BODY" "$TOKEN")"
APP2_ID="$(jqget "$APP2_RESP" '.data.id')"
if [ -z "$APP2_ID" ]; then
  record "app2-create" "创建第二个应用" FAIL "响应无 id：${APP2_RESP:0:200}"; exit 1
fi
record "app2-create" "创建第二个应用「${APP2_NAME}」" PASS "app_id=${APP2_ID:0:8}…，slug=${APP2_SLUG}"

REL2_BODY="$(jq -n --arg a "$APP2_ID" --arg t "$TPL_ID" --arg v "0.0.2" --arg s "$SRV_ID" \
  '{app_id:$a,template_id:$t,version:$v,server_ids:[$s]}')"
REL2_RESP="$(api POST /api/v1/releases "$REL2_BODY" "$TOKEN")"
REL2_ID="$(jqget "$REL2_RESP" '.data.release.id')"
if [ -z "$REL2_ID" ]; then
  record "release2-create" "创建第二个发布单" FAIL "响应无 release.id：${REL2_RESP:0:300}"; exit 1
fi
record "release2-create" "创建第二个发布单 v0.0.2" PASS "release_id=${REL2_ID:0:8}…（应用二号）"

# ---------------------------------------------------------------- 3. ① 列表
step "3. ① GET /api/v1/releases 每条的 app_name"
# 新库现在有 2 个应用：本断言按 app_id 收窄到「本应用」（否则「每条都等于 APP_NAME」
# 会因为第二行而假红）。跨应用逐行匹配由下面的 1-list-two-apps 负责。
APP_LIST_URL="/api/v1/releases?limit=50&app_id=${APP_ID}"
LIST_STATUS="$(code GET "$APP_LIST_URL" "" "$TOKEN")"
LIST_BODY="$(api GET "$APP_LIST_URL" "" "$TOKEN")"
LIST_COUNT="$(printf '%s' "$LIST_BODY" | jq -r '.data | length' 2>/dev/null)"
LIST_OK="$(printf '%s' "$LIST_BODY" | jq -r --arg n "$APP_NAME" \
  '(.data | length) > 0 and ([.data[] | (has("app_name") and (.app_name | type == "string") and .app_name != "" and .app_name == $n)] | all)' 2>/dev/null)"
ROW_NAME="$(printf '%s' "$LIST_BODY" | jq -r --arg id "$REL_ID" '.data[] | select(.id == $id) | .app_name' 2>/dev/null)"
record "1-list-status" "列表 HTTP 200" "$([ "$LIST_STATUS" = "200" ] && echo PASS || echo FAIL)" "status=${LIST_STATUS}（app_id scope）"
record "1-list-all-appname" "列表 ${LIST_COUNT} 条均带非空 app_name 且等于「${APP_NAME}」" \
  "$([ "$LIST_OK" = "true" ] && echo PASS || echo FAIL)" "逐条校验结果=${LIST_OK:-<jq 失败>}；本次发布 app_name=${ROW_NAME:-<空>}（app_id scope）"

# ⑥ 两应用交叉校验：每行 app_name/app_slug 必须等于「它自己 app_id 对应的应用」。
# 断言要点：两行都存在、没有未知 app_id 的行、两行名字互不相同、逐行匹配自己的应用
# —— 把 JOIN 写成常量或「统一取第一个应用名」都会红。
TWO_BODY="$(api GET "/api/v1/releases?limit=50" "" "$TOKEN")"
TWO_OK="$(printf '%s' "$TWO_BODY" | jq -r --arg a1 "$APP_ID" --arg n1 "$APP_NAME" --arg s1 "$APP_SLUG" \
  --arg a2 "$APP2_ID" --arg n2 "$APP2_NAME" --arg s2 "$APP2_SLUG" '
  def exp_name($id): if $id == $a1 then $n1 elif $id == $a2 then $n2 else "" end;
  def exp_slug($id): if $id == $a1 then $s1 elif $id == $a2 then $s2 else "" end;
  ([.data[] | select(.app_id == $a1)] | length) as $c1 |
  ([.data[] | select(.app_id == $a2)] | length) as $c2 |
  ($c1 == 1) and ($c2 == 1) and
  ([.data[] | select(.app_id != $a1 and .app_id != $a2)] | length) == 0 and
  ([.data[] | (has("app_name") and (.app_name | type == "string") and .app_name == exp_name(.app_id)
              and has("app_slug") and .app_slug == exp_slug(.app_id))] | all) and
  ([.data[] | .app_name] | unique | length) >= 2' 2>/dev/null)"
TWO_ROWS="$(printf '%s' "$TWO_BODY" | jq -r '[.data[] | "\(.app_id[0:8])…=\(.app_name)"] | join("，")' 2>/dev/null)"
record "1-list-two-apps" "两应用各一行且 app_name 逐行等于自己 app_id 对应的应用名" \
  "$([ "$TWO_OK" = "true" ] && echo PASS || echo FAIL)" "逐行校验=${TWO_OK:-<jq 失败>}；两行=${TWO_ROWS:0:280}"

# ---------------------------------------------------------------- 4. ② 详情
step "4. ② GET /api/v1/releases/{id}"
DETAIL_STATUS="$(code GET "/api/v1/releases/${REL_ID}" "" "$TOKEN")"
DETAIL_BODY="$(api GET "/api/v1/releases/${REL_ID}" "" "$TOKEN")"
DETAIL_OK="$(printf '%s' "$DETAIL_BODY" | jq -r --arg n "$APP_NAME" --arg s "$APP_SLUG" \
  '(.data.release | has("app_name")) and .data.release.app_name == $n and .data.release.app_slug == $s' 2>/dev/null)"
DETAIL_NAME="$(jqget "$DETAIL_BODY" '.data.release.app_name')"
record "2-detail-status" "详情 HTTP 200" "$([ "$DETAIL_STATUS" = "200" ] && echo PASS || echo FAIL)" "status=${DETAIL_STATUS}"
record "2-detail-appname" "详情带 app_name/app_slug" "$([ "$DETAIL_OK" = "true" ] && echo PASS || echo FAIL)" \
  "app_name=${DETAIL_NAME:-<空>}，app_slug=$(jqget "$DETAIL_BODY" '.data.release.app_slug')"

# ---------------------------------------------------------------- 5. ④⑤ 真浏览器
step "5. ④⑤ 真浏览器断言（首页 / releases / 反代删字段）"
BROWSER_OUT="$(VERIFY_BASE="$BASE" \
  VERIFY_APP_ID="$APP_ID" \
  VERIFY_RELEASE_ID="$REL_ID" \
  VERIFY_APP_NAME="$APP_NAME" \
  VERIFY_APP_SLUG="$APP_SLUG" \
  VERIFY_PROXY_PORT="$PROXY_PORT" \
  VERIFY_ADMIN_PASS="$ADMIN_PASS" \
  node scripts/verify_release_appname_browser.mjs 2>&1)"
BROWSER_RC=$?
printf '%s\n' "$BROWSER_OUT" | grep -E '^(RESULT|INFO)' | sed 's/^/  /'
if [ "$BROWSER_RC" -eq 2 ]; then
  record "browser-env" "真浏览器环境可用" FAIL "playwright/chromium 缺失（环境问题，不是断言失败）"
elif [ "$BROWSER_RC" -ne 0 ]; then
  record "browser-run" "真浏览器断言执行" FAIL "退出码 ${BROWSER_RC}（详见上方 RESULT/异常输出）"
fi
for id in direct-dashboard direct-releases direct-identical proxy-strip proxy-fallback; do
  line="$(printf '%s\n' "$BROWSER_OUT" | grep -E "^RESULT ${id} (PASS|FAIL)" | head -1)"
  if [ -z "$line" ]; then
    record "$id" "浏览器断言 ${id}" FAIL "未产出该断言（脚本异常或环境缺失）"
    continue
  fi
  status="$(printf '%s' "$line" | awk '{print $3}')"
  detail="$(printf '%s' "$line" | cut -d' ' -f4-)"
  record "$id" "浏览器断言 ${id}" "$status" "${detail:0:300}"
done

# ---------------------------------------------------------------- 6. ③ 删应用后孤儿发布
step "6. ③ 删除应用后列表/详情不 500 且字段为空串"
DEL_CODE="$(code DELETE "/api/v1/apps/${APP_ID}" "" "$TOKEN")"
if [ "$DEL_CODE" = "200" ]; then
  DEL_HOW="DELETE /apps 返回 200"
else
  if ! command -v sqlite3 >/dev/null 2>&1; then
    record "3-delete-app" "删除应用（模拟运维直删）" FAIL "API 返回 ${DEL_CODE} 且无 sqlite3"
    exit 1
  fi
  sqlite3 -cmd ".timeout 5000" "$DB" "DELETE FROM apps WHERE id = '${APP_ID}';" >/dev/null 2>&1
  DEL_HOW="DELETE /apps 返回 ${DEL_CODE}（有发布记录时的契约行为），改用 sqlite3 直删模拟孤儿行"
fi
APP_GONE_STATUS="$(code GET "/api/v1/apps/${APP_ID}" "" "$TOKEN")"
APP_GONE="$(api GET "/api/v1/apps/${APP_ID}" "" "$TOKEN" | jq -r '.error.code // .code // "missing"' 2>/dev/null)"
record "3-delete-app" "应用已删除（孤儿发布保留）" "$([ "$APP_GONE_STATUS" = "404" ] && [ "$APP_GONE" = "not_found" ] && echo PASS || echo FAIL)" \
  "${DEL_HOW}；GET /apps/${APP_ID} → HTTP ${APP_GONE_STATUS} code=${APP_GONE}"

ORPHAN_LIST_URL="/api/v1/releases?limit=50&app_id=${APP_ID}"
ORPHAN_LIST_STATUS="$(code GET "$ORPHAN_LIST_URL" "" "$TOKEN")"
ORPHAN_LIST="$(api GET "$ORPHAN_LIST_URL" "" "$TOKEN")"
ORPHAN_OK="$(printf '%s' "$ORPHAN_LIST" | jq -r --arg id "$REL_ID" \
  '([.data[] | select(.id == $id)] | length) == 1 and
   ([.data[] | (has("app_name") and .app_name == "" and has("app_slug") and .app_slug == "")] | all)' 2>/dev/null)"
ORPHAN_NAME="$(printf '%s' "$ORPHAN_LIST" | jq -r --arg id "$REL_ID" '.data[] | select(.id == $id) | .app_name' 2>/dev/null)"
record "3-list-no-500" "删除应用后列表 HTTP 200" "$([ "$ORPHAN_LIST_STATUS" = "200" ] && echo PASS || echo FAIL)" "status=${ORPHAN_LIST_STATUS}"
record "3-list-empty-appname" "孤儿发布 app_name/app_slug 为空串" "$([ "$ORPHAN_OK" = "true" ] && echo PASS || echo FAIL)" \
  "app_name=${ORPHAN_NAME-<null>}，jq 校验=${ORPHAN_OK:-<失败>}"

ORPHAN_DETAIL_STATUS="$(code GET "/api/v1/releases/${REL_ID}" "" "$TOKEN")"
ORPHAN_DETAIL="$(api GET "/api/v1/releases/${REL_ID}" "" "$TOKEN")"
ORPHAN_DETAIL_OK="$(printf '%s' "$ORPHAN_DETAIL" | jq -r \
  '(.data.release | has("app_name")) and .data.release.app_name == "" and .data.release.app_slug == ""' 2>/dev/null)"
ORPHAN_DETAIL_NAME="$(jqget "$ORPHAN_DETAIL" '.data.release.app_name')"
record "3-detail-no-500" "删除应用后详情 HTTP 200" "$([ "$ORPHAN_DETAIL_STATUS" = "200" ] && echo PASS || echo FAIL)" "status=${ORPHAN_DETAIL_STATUS}"
record "3-detail-empty-appname" "详情 app_name 为空串（不是 null/缺字段）" "$([ "$ORPHAN_DETAIL_OK" = "true" ] && echo PASS || echo FAIL)" \
  "app_name=${ORPHAN_DETAIL_NAME-<null>}"

# ---------------------------------------------------------------- 汇总
step "汇总"
printf '  PASS=%s FAIL=%s\n' "$PASS" "$FAIL"
printf '  报告：%s\n' "$REPORT"
if [ "$FAIL" -gt 0 ]; then exit 1; fi
exit 0
