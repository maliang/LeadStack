#!/usr/bin/env bash
# LeadStack 控制端自升级验收（Lead 所有）
#
# 覆盖 docs/ARCHITECTURE.md 第 51.5 节。
# 用法：make build && bash scripts/verify_upgrade.sh
#
# 用本地 mock GitHub API 提供「新版本」，真实验证：
#   1) 检查更新
#   2) 备份失败 → 拒绝升级且二进制不变
#   3) 正常升级 → 版本变化、产生备份、保留 .prev
#   4) 新版本一启动就崩溃 → watchdog 自动回退到旧版本且服务可用
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export PATH="/opt/homebrew/bin:$PATH"

PORT="${PORT:-19200}"
MOCK_PORT=$((PORT + 1))
BASE="http://127.0.0.1:${PORT}"
TMP_CANDIDATE="${TMPDIR:-/tmp}"
if printf '%s' "$TMP_CANDIDATE" | LC_ALL=C grep -q '[^ -~]'; then TMP_CANDIDATE=/tmp; fi
WORK="$(mktemp -d "$TMP_CANDIDATE/leadstack-upgrade.XXXXXX")"
PKG_DIR="$WORK/pkgs"
mkdir -p "$PKG_DIR"
PASS=0; FAIL=0
LEAD_PID=""; MOCK_PID=""

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
jget() { printf '%s' "$1" | jq -r "$2" 2>/dev/null; }

cleanup() {
  [ -n "$LEAD_PID" ] && kill -9 "$LEAD_PID" 2>/dev/null
  [ -n "$MOCK_PID" ] && kill -9 "$MOCK_PID" 2>/dev/null
  pkill -f "leadstack-upgrade" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

login() { curl -s -X POST "${BASE}/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"'$1'"}' | jq -r '.data.token // empty' 2>/dev/null; }

echo "工作目录：$WORK"

# ---------------------------------------------------------------- 0. 构建两个版本 + 崩溃版
step "0. 构建「当前版本」「新版本」与「崩溃版本」"
mkdir -p "$WORK/old" "$WORK/new"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.0.1" -o "$WORK/old/leadstack" ./cmd/leadstack 2>&1 | head -3
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.0.2" -o "$WORK/new/leadstack" ./cmd/leadstack 2>&1 | head -3
[ -x "$WORK/old/leadstack" ] && [ -x "$WORK/new/leadstack" ] && pass "已构建 v0.0.1 与 v0.0.2" || { fail "构建失败"; exit 1; }

# 崩溃版：一个立刻退出的可执行脚本（模拟"新版本启动即失败"）
printf '#!/bin/sh\nexit 1\n' > "$WORK/new-crash-leadstack"
chmod +x "$WORK/new-crash-leadstack"

# 打包（tar.gz 内是 leadstack 二进制）
tar -czf "$PKG_DIR/leadstack_0.0.2_darwin_arm64.tar.gz" -C "$WORK/new" leadstack
tar -czf "$PKG_DIR/leadstack_0.0.3_darwin_arm64.tar.gz" -C "$WORK/new" leadstack   # 占位，稍后替换为崩溃版
rm -f "$PKG_DIR/leadstack_0.0.3_darwin_arm64.tar.gz"
TMPC="$WORK/crashdir"; mkdir -p "$TMPC"; cp "$WORK/new-crash-leadstack" "$TMPC/leadstack"
tar -czf "$PKG_DIR/leadstack_0.0.3_darwin_arm64.tar.gz" -C "$TMPC" leadstack
# 真实发行版同时包含 leadstack-agent_*，且 GitHub 按名字排序返回（agent 在前）。
# 这里如实模拟：agent 归档里没有 leadstack 可执行文件——若升级逻辑误选它，必然失败。
TMPA="$WORK/agentdir"; mkdir -p "$TMPA"; printf 'not-a-control-plane-binary\n' > "$TMPA/leadstack-agent"
tar -czf "$PKG_DIR/leadstack-agent_0.0.2_darwin_arm64.tar.gz" -C "$TMPA" leadstack-agent
SHA2A="$(shasum -a 256 "$PKG_DIR/leadstack-agent_0.0.2_darwin_arm64.tar.gz" | awk '{print $1}')"
SHA2="$(shasum -a 256 "$PKG_DIR/leadstack_0.0.2_darwin_arm64.tar.gz" | awk '{print $1}')"
SHA3="$(shasum -a 256 "$PKG_DIR/leadstack_0.0.3_darwin_arm64.tar.gz" | awk '{print $1}')"
cat > "$PKG_DIR/checksums.txt" <<EOF
${SHA2A}  leadstack-agent_0.0.2_darwin_arm64.tar.gz
${SHA2}  leadstack_0.0.2_darwin_arm64.tar.gz
${SHA3}  leadstack_0.0.3_darwin_arm64.tar.gz
EOF
pass "已生成发布包与 checksums"

# ---------------------------------------------------------------- 1. mock GitHub API
step "1. 启动 mock GitHub API"
cat > "$WORK/mock.py" <<'PY'
import http.server, json, os, sys
root, port, state_path = sys.argv[1], int(sys.argv[2]), sys.argv[3]
class H(http.server.BaseHTTPRequestHandler):
    def _json(self, obj):
        b = json.dumps(obj).encode()
        self.send_response(200); self.send_header('Content-Type','application/json')
        self.send_header('Content-Length', str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        st = json.load(open(state_path))
        if self.path.endswith('/releases/latest'):
            ver = st['version']
            base = f"http://127.0.0.1:{port}"
            self._json({"tag_name": ver, "published_at": "2026-10-21T00:00:00Z",
                        "html_url": "https://example.invalid/release",
                        "body": "测试版本\n<!-- schema_changed: " + ("true" if st.get("schema") else "false") + " -->",
                        "assets": [
                          {"name": f"leadstack-agent_{ver[1:]}_darwin_arm64.tar.gz",
                           "browser_download_url": f"{base}/pkg/leadstack-agent_{ver[1:]}_darwin_arm64.tar.gz",
                           "size": 100},
                          {"name": f"leadstack_{ver[1:]}_darwin_arm64.tar.gz",
                           "browser_download_url": f"{base}/pkg/leadstack_{ver[1:]}_darwin_arm64.tar.gz",
                           "size": 100},
                          {"name": "checksums.txt", "browser_download_url": f"{base}/pkg/checksums.txt", "size": 100}]})
        elif self.path.startswith('/pkg/'):
            name = os.path.basename(self.path)
            p = os.path.join(root, name)
            if not os.path.exists(p):
                self.send_response(404); self.end_headers(); return
            data = open(p,'rb').read()
            self.send_response(200); self.send_header('Content-Length', str(len(data))); self.end_headers()
            self.wfile.write(data)
        else:
            self.send_response(404); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
echo '{"version":"v0.0.2","schema":false}' > "$WORK/mockstate.json"
python3 "$WORK/mock.py" "$PKG_DIR" "$MOCK_PORT" "$WORK/mockstate.json" > "$WORK/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:${MOCK_PORT}/repos/acme/leadstack/releases/latest" && break; sleep 0.2; done
curl -s "http://127.0.0.1:${MOCK_PORT}/repos/acme/leadstack/releases/latest" | jq -e '.tag_name' >/dev/null 2>&1 \
  && pass "mock API 就绪（latest=v0.0.2）" || fail "mock API 未就绪"

# ---------------------------------------------------------------- 2. 启动当前版本
start_lead() { # start_lead <data_dir> <exe> [extra env...]
  local d="$1" exe="$2"; shift 2
  env LEADSTACK_LISTEN="127.0.0.1:${PORT}" LEADSTACK_DATA_DIR="$d" \
      LEADSTACK_ADMIN_PASSWORD=upgrade-pass \
      LEADSTACK_UPDATE_API_BASE="http://127.0.0.1:${MOCK_PORT}" \
      LEADSTACK_UPDATE_REPO=acme/leadstack \
      LEADSTACK_UPDATE_CHECK_INTERVAL=0 \
      LEADSTACK_UPGRADE_ROLLBACK_TIMEOUT=15s \
      "$@" "$exe" > "$WORK/lead.log" 2>&1 &
  LEAD_PID=$!
  for _ in $(seq 1 100); do [ "$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/healthz")" = "200" ] && return 0; sleep 0.2; done
  return 1
}
stop_lead() { [ -n "$LEAD_PID" ] && kill -9 "$LEAD_PID" 2>/dev/null; wait "$LEAD_PID" 2>/dev/null; LEAD_PID=""; }

step "2. 检查更新（当前 v0.0.1 → 最新 v0.0.2）"
DATA1="$WORK/data1"; mkdir -p "$DATA1"
EXE="$WORK/bin/leadstack"; mkdir -p "$WORK/bin"; cp "$WORK/old/leadstack" "$EXE"; chmod +x "$EXE"
start_lead "$DATA1" "$EXE" && pass "当前版本已启动（v0.0.1）" || { fail "启动失败"; tail -20 "$WORK/lead.log"; exit 1; }
TOKEN="$(login upgrade-pass)"
[ -n "$TOKEN" ] && pass "管理员登录成功" || { fail "登录失败"; exit 1; }

UPD="$(curl -s -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/system/update")"
LATEST="$(jget "$UPD" '.data.latest')"; HAS="$(jget "$UPD" '.data.has_update')"
if [ "$LATEST" = "v0.0.2" ] && [ "$HAS" = "true" ]; then
  pass "检查更新正确：latest=${LATEST} has_update=${HAS}"
else
  fail "检查更新异常：$(printf '%s' "$UPD" | head -c 200)"
fi

# ---------------------------------------------------------------- 3. 备份失败必须中止
step "3. 备份失败 → 拒绝升级且二进制不变"
stop_lead
DATA2="$WORK/data2"; mkdir -p "$DATA2"
# 备份目录指向不可写位置（/dev/null 下无法建目录）
start_lead "$DATA2" "$EXE" LEADSTACK_BACKUP_DIR=/dev/null/nope && pass "以不可写备份目录启动" || fail "启动失败"
TOKEN="$(login upgrade-pass)"
APPLY="$(curl -s -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"version":"v0.0.2"}' "${BASE}/api/v1/system/update/apply")"
JOB_ID="$(jget "$APPLY" '.data.job_id')"
[ -n "$JOB_ID" ] && pass "升级任务已受理（202，job=${JOB_ID:0:8}…）" || fail "apply 未返回 job：$(printf '%s' "$APPLY" | head -c 160)"

ABORTED=0
for _ in $(seq 1 60); do
  ST="$(curl -s -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/system/update/status")"
  ERRMSG="$(printf '%s' "$ST" | jq -r '.data.job.error // empty' 2>/dev/null)"
  PHASE="$(printf '%s' "$ST" | jq -r '.data.job.phase // empty' 2>/dev/null)"
  if [ -n "$ERRMSG" ]; then ABORTED=1; break; fi
  sleep 0.5
done
[ "$ABORTED" = "1" ] && pass "备份失败已中止升级（phase=${PHASE}）：${ERRMSG:0:70}" || fail "备份失败未中止升级"
VER_NOW="$(curl -s "${BASE}/healthz" | jq -r '.data.version' 2>/dev/null)"
[ "$VER_NOW" = "v0.0.1" ] && pass "二进制未被替换（仍为 v0.0.1）" || fail "二进制已被替换（当前 ${VER_NOW}）"

# ---------------------------------------------------------------- 4. 正常升级
step "4. 正常升级 v0.0.1 → v0.0.2"
stop_lead
DATA3="$WORK/data3"; mkdir -p "$DATA3"
cp "$WORK/old/leadstack" "$EXE"; chmod +x "$EXE"
echo '{"version":"v0.0.2","schema":false}' > "$WORK/mockstate.json"
start_lead "$DATA3" "$EXE" && pass "以 v0.0.1 启动" || fail "启动失败"
TOKEN="$(login upgrade-pass)"
curl -s -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"version":"v0.0.2"}' "${BASE}/api/v1/system/update/apply" >/dev/null

UPGRADED=0
for _ in $(seq 1 150); do
  V="$(curl -s --max-time 2 "${BASE}/healthz" | jq -r '.data.version' 2>/dev/null)"
  [ "$V" = "v0.0.2" ] && { UPGRADED=1; break; }
  sleep 0.5
done
[ "$UPGRADED" = "1" ] && pass "/healthz 版本已变为 v0.0.2（exec 自升级生效）" || {
  fail "升级后版本未变化（当前 ${V:-无响应}）"; tail -15 "$WORK/lead.log" | sed 's/^/      /'; }

BKN="$(ls "$DATA3"/backups/*.lsbak 2>/dev/null | wc -l | tr -d ' ')"
[ "${BKN:-0}" -ge 1 ] && pass "升级前备份已生成（${BKN} 份）" || fail "未生成升级前备份"
[ -f "$EXE.prev" ] && pass "旧二进制已保留为 .prev" || fail "缺少 .prev 备份二进制"

# ---------------------------------------------------------------- 5. 崩溃版本自动回退
step "5. 新版本启动即崩溃 → watchdog 自动回退"
stop_lead
TOKEN="$(login upgrade-pass 2>/dev/null)"
echo '{"version":"v0.0.3","schema":false}' > "$WORK/mockstate.json"
# 重新启动（升级后的 v0.0.2 实例可能已被 kill，重新拉起同一 exe）
start_lead "$DATA3" "$EXE" && pass "以 v0.0.2 启动，准备升级到崩溃版 v0.0.3" || fail "启动失败"
TOKEN="$(login upgrade-pass)"
curl -s -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d '{"version":"v0.0.3"}' "${BASE}/api/v1/system/update/apply" >/dev/null

REVERTED=0
for _ in $(seq 1 200); do
  V="$(curl -s --max-time 2 "${BASE}/healthz" | jq -r '.data.version' 2>/dev/null)"
  if [ "$V" = "v0.0.2" ]; then REVERTED=1; break; fi
  sleep 0.5
done
if [ "$REVERTED" = "1" ]; then
  pass "watchdog 已回退到 v0.0.2 且服务可用"
else
  fail "未回退（当前版本 ${V:-无响应}）"
  echo "    watchdog/服务日志："; tail -20 "$WORK/lead.log" | sed 's/^/      /'
fi
# watchdog 在换回文件、重启服务之后才写 history 并释放 running，给它收尾时间再断言。
SETTLED=0
for _ in $(seq 1 60); do
  ST="$(curl -s -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/system/update/status")"
  RUNNING="$(printf '%s' "$ST" | jq -r '.data.running' 2>/dev/null)"
  [ "$RUNNING" = "false" ] && { SETTLED=1; break; }
  sleep 0.5
done
[ "$SETTLED" = "1" ] && pass "回退后升级任务已释放（running=false）" || fail "回退后任务仍显示运行中"
HIST="$(printf '%s' "$ST" | jq -c '.data.history // []' 2>/dev/null)"
printf '%s' "$HIST" | grep -qE '"outcome":"rollback"' && pass "升级历史记录了回退（outcome=rollback）" || fail "升级历史缺少回退记录：${HIST:0:160}"

# ---------------------------------------------------------------- 6. 权限与开关
step "6. 开关与权限"
# 保证没有升级在跑，否则 409（并发保护）会盖住"版本不存在"的 400
for _ in $(seq 1 40); do
  R="$(curl -s -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/system/update/status" | jq -r '.data.running' 2>/dev/null)"
  [ "$R" = "false" ] && break
  sleep 0.5
done
APPLY_DISABLED="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer ${TOKEN}" \
  -H 'Content-Type: application/json' -d '{"version":"v0.0.4"}' "${BASE}/api/v1/system/update/apply")"
if [ "$APPLY_DISABLED" = "400" ] || [ "$APPLY_DISABLED" = "403" ]; then
  pass "不存在的版本被拒绝（HTTP ${APPLY_DISABLED}）"
else
  fail "不存在的版本未被拒绝（HTTP ${APPLY_DISABLED}）"
fi
NOAUTH="$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/api/v1/system/update")"
[ "$NOAUTH" = "401" ] && pass "未带 token 访问更新接口返回 401" || fail "未鉴权未被拒绝（HTTP ${NOAUTH}）"

# ---------------------------------------------------------------- 汇总
step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -eq 0 ]; then printf '\033[32m自升级验收通过\033[0m\n'; exit 0; fi
printf '\033[31m自升级验收未通过\033[0m\n'
exit 1
