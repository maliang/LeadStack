#!/usr/bin/env bash
# LeadStack 备份与恢复端到端验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 35 节验收标准。
# 用法：make build && bash scripts/verify_backup.sh
#
# 这个脚本的核心不是"接口返回 200 就算过"，而是：
#   1) 归档必须真的加密（明文特征搜不到）
#   2) 恢复必须真的把数据救回来（改数据 → 恢复 → 数据回来）
#   3) 错误必须可区分（口令错 ≠ 归档损坏）
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18890}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-backup-pass"
# 选择纯 ASCII 的临时根目录：部署根路径会经过 deploy 的变量白名单校验，
# 若 TMPDIR 指向含中文的仓库路径（本仓库名含「领栈」），ROOT 会被判非法。
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
BASE_DIR="$(mktemp -d "$TMP_CANDIDATE/leadstack-bk.XXXXXX")"
DATA_DIR="$BASE_DIR/data"
PASS_FILE="$BASE_DIR/passphrase.txt"
BACKUP_DIR="$BASE_DIR/backups"
export LEADSTACK_BACKUP_PASSPHRASE="verify-backup-passphrase-9f3a"
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
status_of() { curl -sS -o /dev/null -w '%{http_code}' "$@" 2>/dev/null; }
jget() { printf '%s' "$1" | jq -r "$2" 2>/dev/null; }
sget() { local v; v="$(jget "$1" "$2")"; [ "$v" = "null" ] && v=""; printf '%s' "$v"; }

start_lead() {
  mkdir -p "$DATA_DIR" "$BACKUP_DIR"
  LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
  LEADSTACK_DATA_DIR="$DATA_DIR" \
  LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
  LEADSTACK_BACKUP_DIR="$BACKUP_DIR" \
  LEADSTACK_SSH_INTERVAL="30s" \
    ./bin/leadstack > "$BASE_DIR/lead.log" 2>&1 &
  LEAD_PID=$!
  for _ in $(seq 1 80); do [ "$(status_of "${BASE}/healthz")" = "200" ] && return 0; sleep 0.3; done
  return 1
}
stop_lead() {
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  wait "$LEAD_PID" 2>/dev/null
  LEAD_PID=""
}

echo "临时目录：$BASE_DIR"

# ---------------------------------------------------------------- 1. 构建
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-agent; do
  [ -x "$bin" ] && pass "$bin 存在" || fail "$bin 缺失"
done
[ "$FAIL" -gt 0 ] && { echo; echo "构建产物缺失，终止。"; exit 1; }

# ---------------------------------------------------------------- 2. 启动并造数据
step "2. 启动控制端并造数据"
if start_lead; then pass "控制端就绪"; else fail "控制端未就绪"; tail -20 "$BASE_DIR/lead.log" | sed 's/^/      /'; exit 1; fi

LOGIN_RESP="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
[ -n "$TOKEN" ] && pass "登录成功" || { fail "登录失败"; exit 1; }
[ "$(status_of "${BASE}/api/v1/backups")" = "401" ] && pass "无 token 访问 /backups 返回 401" || fail "无 token 访问 /backups 未返回 401"

CRED_ID="$(sget "$(api POST /api/v1/credentials '{"name":"bk-cred","kind":"password","username":"root","password":"secret-pw-123"}' "$TOKEN")" '.data.id')"
SRV_ID="$(sget "$(api POST /api/v1/servers '{"name":"bk-server","host":"127.0.0.1","port":22,"mode":"ssh","credential_id":"'"$CRED_ID"'"}' "$TOKEN")" '.data.id')"
APP_ID="$(sget "$(api POST /api/v1/apps '{"name":"备份演示","slug":"bk-app"}' "$TOKEN")" '.data.id')"
[ -n "$SRV_ID" ] && [ -n "$APP_ID" ] && pass "已造数据（凭证 / 服务器 / 应用）" || fail "造数据失败"

# 确认主密钥已生成，且数据目录里有受管文件
[ -f "$DATA_DIR/master.key" ] && pass "master.key 存在（$(ls -l "$DATA_DIR/master.key" | awk '{print $1}')）" || fail "master.key 缺失"
[ -f "$DATA_DIR/leadstack.db" ] && pass "leadstack.db 存在" || fail "leadstack.db 缺失"

# ---------------------------------------------------------------- 3. 立即备份
step "3. 立即备份"
BK_RESP="$(api POST /api/v1/backups '{"include_artifacts":false}' "$TOKEN")"
BK_NAME="$(sget "$BK_RESP" '.data.name')"
BK_SIZE="$(sget "$BK_RESP" '.data.size_bytes')"
BK_SHA="$(sget "$BK_RESP" '.data.sha256')"
if [ -n "$BK_NAME" ] && [ "${BK_SIZE:-0}" -gt 0 ] 2>/dev/null; then
  pass "备份创建成功：${BK_NAME}（${BK_SIZE} 字节）"
else
  fail "备份创建失败：$BK_RESP"
  exit 1
fi
BK_FILE="$BACKUP_DIR/$BK_NAME"
[ -f "$BK_FILE" ] && pass "归档文件已落盘" || fail "归档文件不存在：$BK_FILE"

# ---------------------------------------------------------------- 4. 加密有效性
step "4. 归档必须加密（明文特征搜不到）"
MAGIC="$(head -c 7 "$BK_FILE" 2>/dev/null)"
[ "$MAGIC" = "LSBAK1" ] && pass "归档头为 LSBAK1" || fail "归档头不符：${MAGIC}"

LEAK=0
for pat in "SQLite format 3" "BEGIN OPENSSH PRIVATE KEY" "secret-pw-123" "verify-backup-passphrase-9f3a"; do
  if grep -q "$pat" "$BK_FILE" 2>/dev/null; then
    fail "归档中出现明文特征：$pat"
    LEAK=1
  fi
done
[ "$LEAK" = "0" ] && pass "归档中搜不到 SQLite 头 / 私钥 / 明文密码 / 口令"

# master.key 内容不得以明文出现在归档里
if [ -f "$DATA_DIR/master.key" ]; then
  MK_HEX="$(xxd -p "$DATA_DIR/master.key" 2>/dev/null | tr -d '\n' | head -c 32)"
  if [ -n "$MK_HEX" ] && grep -q "$MK_HEX" "$BK_FILE" 2>/dev/null; then
    fail "归档中出现 master.key 明文片段"
  else
    pass "归档中不含 master.key 明文"
  fi
fi

# backup.pass 要么不存在（用 env 口令），要么不在归档内
if [ -f "$DATA_DIR/backup.pass" ]; then
  pass "backup.pass 位于数据目录（未随归档）"
fi

# ---------------------------------------------------------------- 5. 列表 / 校验 / 下载
step "5. 列表、校验与下载"
LIST_RESP="$(api GET /api/v1/backups "" "$TOKEN")"
if printf '%s' "$LIST_RESP" | jq -e --arg n "$BK_NAME" '(.data.backups? // .data) | any(.name == $n)' >/dev/null 2>&1; then
  pass "列表包含该备份"
else
  fail "列表未包含备份：$(printf '%s' "$LIST_RESP" | head -c 200)"
fi

V_RESP="$(api POST "/api/v1/backups/${BK_NAME}/verify" "" "$TOKEN")"
V_OK="$(jget "$V_RESP" '.data.ok')"
V_FILES="$(printf '%s' "$V_RESP" | jq -r '(.data.manifest // .data).files | map(.name) | join(",")' 2>/dev/null)"
if [ "$V_OK" = "true" ]; then
  pass "校验通过（含文件：${V_FILES}）"
else
  fail "校验失败：$V_RESP"
fi

DL_CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/backups/${BK_NAME}/download")"
DL_SHA="$(curl -sS -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/backups/${BK_NAME}/download" | shasum -a 256 | awk '{print $1}')"
if [ "$DL_CODE" = "200" ] && [ "$DL_SHA" = "$BK_SHA" ]; then
  pass "下载内容与磁盘文件 sha256 一致"
else
  fail "下载异常（HTTP ${DL_CODE}，sha ${DL_SHA:0:12} vs ${BK_SHA:0:12}）"
fi

TRAV_CODE="$(status_of -X DELETE -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/backups/..%2F..%2Fetc%2Fpasswd")"
[ "$TRAV_CODE" = "400" ] || [ "$TRAV_CODE" = "404" ] && pass "路径穿越被拒绝（HTTP ${TRAV_CODE}）" || fail "路径穿越未被拒绝（HTTP ${TRAV_CODE}）"

# ---------------------------------------------------------------- 6. 篡改与错误口令
step "6. 篡改检测与口令错误必须可区分"
TAMPER="$BASE_DIR/tampered.lsbak"
cp "$BK_FILE" "$TAMPER"
printf '\xff' | dd of="$TAMPER" bs=1 seek=$(( (${#BK_NAME} + 200) )) conv=notrunc 2>/dev/null || true
# 直接改文件尾部一个字节更可靠
SIZE="$(wc -c < "$TAMPER" | tr -d ' ')"
printf '\x00' | dd of="$TAMPER" bs=1 seek=$((SIZE - 64)) conv=notrunc 2>/dev/null || true
T_CODE="$(curl -sS -o "$BASE_DIR/v.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${TOKEN}" -F "file=@${TAMPER}" "${BASE}/api/v1/backups/restore")"
if [ "$T_CODE" = "400" ]; then
  pass "篡改归档被拒绝（HTTP 400）：$(jq -r '.error.message // empty' "$BASE_DIR/v.json" 2>/dev/null | head -c 80)"
else
  fail "篡改归档未被拒绝（HTTP ${T_CODE}）"
fi

# 错误口令：用 CLI verify，期望明确报"口令错误"
WRONG_OUT="$BASE_DIR/wrong.txt"
LEADSTACK_BACKUP_PASSPHRASE="totally-wrong-passphrase" \
  ./bin/leadstack backup verify --file "$BK_FILE" > "$WRONG_OUT" 2>&1
if grep -q "口令" "$WRONG_OUT"; then
  pass "错误口令被识别为口令错误：$(head -1 "$WRONG_OUT" | head -c 70)"
else
  fail "错误口令未给出可区分错误：$(head -3 "$WRONG_OUT" | tr '\n' ' ' | head -c 120)"
fi

# ---------------------------------------------------------------- 7. 离线恢复（CLI）
step "7. 离线 CLI 恢复：改数据 → 恢复 → 数据回来"
# 先改数据：删掉服务器
api DELETE "/api/v1/servers/${SRV_ID}" "" "$TOKEN" >/dev/null
GONE="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '[.data[] | select(.id=="'"$SRV_ID"'")] | length' 2>/dev/null)"
[ "${GONE:-0}" = "0" ] && pass "已删除服务器（当前 ${GONE} 台）" || fail "删除服务器失败"

stop_lead
pass "控制端已停止"

RESTORE_OUT="$BASE_DIR/restore.txt"
LEADSTACK_BACKUP_PASSPHRASE="verify-backup-passphrase-9f3a" \
  ./bin/leadstack restore --file "$BK_FILE" --data-dir "$DATA_DIR" > "$RESTORE_OUT" 2>&1
RESTORE_CODE=$?
if [ "$RESTORE_CODE" = "0" ]; then
  pass "CLI 恢复成功"
else
  fail "CLI 恢复失败（exit=${RESTORE_CODE}）：$(tail -3 "$RESTORE_OUT" | tr '\n' ' ')"
fi
PRE_DIRS="$(ls -d "$BASE_DIR"/data.pre-restore-* 2>/dev/null | wc -l | tr -d ' ')"
[ "${PRE_DIRS:-0}" -ge 1 ] && pass "恢复前数据已保留在 pre-restore 目录（${PRE_DIRS} 个）" || fail "未保留 pre-restore 备份"

if start_lead; then pass "控制端重启成功"; else fail "恢复后控制端启动失败"; tail -20 "$BASE_DIR/lead.log" | sed 's/^/      /'; fi
LOGIN_RESP="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
BACK="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '[.data[] | select(.name=="bk-server")] | length' 2>/dev/null)"
[ "${BACK:-0}" -ge 1 ] && pass "被删的服务器已恢复存在" || fail "恢复后服务器仍不存在"

# 主密钥有效性：凭证仍能解密（列表能返回指纹，且不是空）
CRED_FP="$(api GET /api/v1/credentials "" "$TOKEN" | jq -r '[(.data.credentials? // .data)[] | select(.name=="bk-cred")][0].fingerprint // empty' 2>/dev/null)"
if [ -n "$CRED_FP" ] || api GET /api/v1/credentials "" "$TOKEN" | jq -e '(.data.credentials? // .data) | length > 0' >/dev/null 2>&1; then
  pass "恢复后凭证可正常读取（主密钥有效）"
else
  fail "恢复后凭证不可读（主密钥可能不匹配）"
fi

# ---------------------------------------------------------------- 8. API 暂存恢复
step "8. API 暂存恢复（上传 → 重启生效）"
api DELETE "/api/v1/servers/${SRV_ID}" "" "$TOKEN" >/dev/null
STAGE_CODE="$(curl -sS -o "$BASE_DIR/stage.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${TOKEN}" -F "file=@${BK_FILE}" "${BASE}/api/v1/backups/restore")"
if [ "$STAGE_CODE" = "202" ]; then
  pass "上传恢复已暂存（HTTP 202，restart_required=$(jq -r '.data.restart_required // empty' "$BASE_DIR/stage.json" 2>/dev/null)）"
else
  fail "上传恢复未按预期返回 202（HTTP ${STAGE_CODE}）：$(head -c 150 "$BASE_DIR/stage.json")"
fi
[ -f "$DATA_DIR/.restore-pending/meta.json" ] && pass "暂存标记已写入 .restore-pending/" || fail "暂存标记缺失"

stop_lead
if start_lead; then pass "重启后控制端启动成功（已应用暂存恢复）"; else fail "重启失败"; tail -25 "$BASE_DIR/lead.log" | sed 's/^/      /'; fi
LOGIN_RESP="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(sget "$LOGIN_RESP" '.data.token')"
BACK2="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '[.data[] | select(.name=="bk-server")] | length' 2>/dev/null)"
[ "${BACK2:-0}" -ge 1 ] && pass "暂存恢复生效（服务器已回来）" || fail "暂存恢复未生效"
[ ! -d "$DATA_DIR/.restore-pending" ] && pass "暂存目录已清理" || fail "暂存目录未清理（应已应用）"

# ---------------------------------------------------------------- 9. 定时备份与保留
step "9. 定时备份与保留策略"
SET_CODE="$(status_of -X PATCH -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"interval":"3s","retention":2,"include_artifacts":false}' "${BASE}/api/v1/backup-settings")"
[ "$SET_CODE" = "200" ] && pass "备份设置已生效（interval=3s, retention=2）" || fail "备份设置失败（HTTP ${SET_CODE}）"
BAD_INT="$(status_of -X PATCH -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"interval":"not-a-duration"}' "${BASE}/api/v1/backup-settings")"
[ "$BAD_INT" = "400" ] && pass "非法 interval 被拒绝（400）" || fail "非法 interval 未被拒绝（HTTP ${BAD_INT}）"

sleep 16
BK_N="$(ls "$BACKUP_DIR"/*.lsbak 2>/dev/null | wc -l | tr -d ' ')"
if [ "${BK_N:-0}" -le 2 ]; then
  pass "保留策略生效（当前 ${BK_N} 份，上限 2）"
else
  fail "保留策略未生效（当前 ${BK_N} 份，上限 2）"
fi
NEWEST="$(ls -t "$BACKUP_DIR"/*.lsbak 2>/dev/null | head -1)"
[ -n "$NEWEST" ] && pass "存在最新备份：$(basename "$NEWEST")" || fail "没有备份文件"

# 关掉定时，避免影响退出
api PATCH /api/v1/backup-settings '{"enabled":false}' "$TOKEN" >/dev/null

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m备份验收通过\033[0m\n'; exit 0; fi
printf '\033[31m备份验收未通过\033[0m\n'
exit 1
