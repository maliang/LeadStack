#!/usr/bin/env bash
# LeadStack 基础 端到端验收脚本（Lead 所有，独立于各工作流）
#
# 覆盖 docs/ARCHITECTURE.md 第 6 节验收标准 1-6。
# 用法：bash scripts/verify_bootstrap.sh
# 前置：已执行 make build（产出 bin/leadstack 与 bin/leadstack-agent）
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-18777}"
BASE="http://127.0.0.1:${PORT}"
ADMIN_USER="admin"
ADMIN_PASS="verify-m0-pass"
DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/leadstack-e2e.XXXXXX")"
LEAD_PID=""
AGENT_PID=""
PASS=0
FAIL=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null
  [ -n "$LEAD_PID" ] && kill "$LEAD_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT

api() { # api METHOD PATH [JSON_BODY] [TOKEN]
  local method="$1" path="$2" body="${3:-}" token="${4:-}"
  local args=(-sS -X "$method" "${BASE}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null
}

status_of() { curl -sS -o /dev/null -w '%{http_code}' "$@" 2>/dev/null; }

# ---------------------------------------------------------------- 1. 二进制
step "1. 构建产物"
for bin in bin/leadstack bin/leadstack-agent; do
  if [ -x "$bin" ]; then pass "$bin 存在且可执行"; else fail "$bin 缺失，请先 make build"; fi
done
if [ "$FAIL" -gt 0 ]; then echo; echo "构建产物缺失，终止。"; exit 1; fi

# ---------------------------------------------------------------- 2. 启动
step "2. 启动控制端"
LEADSTACK_LISTEN="127.0.0.1:${PORT}" \
LEADSTACK_DATA_DIR="$DATA_DIR" \
LEADSTACK_ADMIN_PASSWORD="$ADMIN_PASS" \
  ./bin/leadstack > "$DATA_DIR/lead.log" 2>&1 &
LEAD_PID=$!

UP=0
for _ in $(seq 1 40); do
  if [ "$(status_of "${BASE}/healthz")" = "200" ]; then UP=1; break; fi
  sleep 0.5
done
if [ "$UP" = "1" ]; then
  pass "控制端 3 秒内就绪（127.0.0.1:${PORT}）"
  HEALTH="$(api GET /healthz)"
  echo "    healthz → $HEALTH"
else
  fail "控制端 20 秒内未就绪，日志尾部："
  tail -20 "$DATA_DIR/lead.log" | sed 's/^/    /'
  exit 1
fi

# ---------------------------------------------------------------- 3. 鉴权
step "3. 鉴权"
CODE="$(status_of "${BASE}/api/v1/servers")"
[ "$CODE" = "401" ] && pass "无 token 访问 /api/v1/servers 返回 401" || fail "无 token 访问返回 ${CODE}，期望 401"

LOGIN="$(api POST /api/v1/auth/login "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")"
TOKEN="$(printf '%s' "$LOGIN" | jq -r '.data.token // empty' 2>/dev/null)"
if [ -n "$TOKEN" ]; then
  pass "用初始管理员密码登录成功"
else
  fail "登录失败：$LOGIN"
  tail -20 "$DATA_DIR/lead.log" | sed 's/^/    /'
  exit 1
fi

CODE="$(status_of -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/servers")"
[ "$CODE" = "200" ] && pass "带 token 访问 /api/v1/servers 返回 200" || fail "带 token 访问返回 ${CODE}，期望 200"

# ---------------------------------------------------------------- 4. 接入令牌
step "4. 生成接入令牌"
ETOKEN_JSON="$(api POST /api/v1/enroll-tokens '{"note":"e2e-verify","ttl_s":900}' "$TOKEN")"
ETOKEN="$(printf '%s' "$ETOKEN_JSON" | jq -r '.data.token // empty' 2>/dev/null)"
if [ -n "$ETOKEN" ]; then
  pass "接入令牌创建成功"
  INSTALL_JSON="$(api POST /api/v1/agent/install-script "{\"token\":\"${ETOKEN}\",\"server_url\":\"${BASE}\"}" "$TOKEN")"
  CMD="$(printf '%s' "$INSTALL_JSON" | jq -r '.data.command // empty' 2>/dev/null)"
  [ -n "$CMD" ] && pass "安装命令生成成功：${CMD:0:80}..." || fail "安装命令生成失败：$INSTALL_JSON"
else
  fail "接入令牌创建失败：$ETOKEN_JSON"
  exit 1
fi

# ---------------------------------------------------------------- 5. agent 上线
step "5. Agent 接入 → 期望 3 秒内 online"
./bin/leadstack-agent --server "$BASE" --token "$ETOKEN" > "$DATA_DIR/agent.log" 2>&1 &
AGENT_PID=$!

ONLINE=0
for _ in $(seq 1 20); do
  S="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '.data[0].status // empty' 2>/dev/null)"
  if [ "$S" = "online" ]; then ONLINE=1; break; fi
  sleep 0.5
done
if [ "$ONLINE" = "1" ]; then
  pass "agent 接入后服务器状态为 online"
else
  fail "服务器未在 10 秒内变为 online"
  echo "    agent 日志："; tail -15 "$DATA_DIR/agent.log" | sed 's/^/      /'
  echo "    控制端日志："; tail -15 "$DATA_DIR/lead.log" | sed 's/^/      /'
fi

# 指标必须真正落库（契约 3.1：metrics_summary 以协议指标名为 key）
HAS_METRICS=0
for _ in $(seq 1 40); do
  M="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '.data[0].metrics_summary["cpu.pct"] // empty' 2>/dev/null)"
  if [ -n "$M" ]; then HAS_METRICS=1; break; fi
  sleep 0.5
done
if [ "$HAS_METRICS" = "1" ]; then
  pass "metrics_summary 已落库（cpu.pct 等）"
  api GET /api/v1/servers "" "$TOKEN" \
    | jq -c '.data[0] | {name,host,status,os,arch,agent_version,metrics_summary}' | sed 's/^/    /'
else
  fail "20 秒内 metrics_summary 仍为空"
  api GET /api/v1/servers "" "$TOKEN" | jq -c '.data[0]' | sed 's/^/    /'
fi

# ---------------------------------------------------------------- 6. 离线判定
step "6. 杀掉 agent → 期望 45 秒内 offline（最多等 75 秒）"
kill "$AGENT_PID" 2>/dev/null; wait "$AGENT_PID" 2>/dev/null; AGENT_PID=""

OFFLINE=0
for _ in $(seq 1 150); do
  S="$(api GET /api/v1/servers "" "$TOKEN" | jq -r '.data[0].status // empty' 2>/dev/null)"
  if [ "$S" = "offline" ]; then OFFLINE=1; break; fi
  sleep 0.5
done
[ "$OFFLINE" = "1" ] && pass "agent 停止后服务器状态变为 offline" || fail "75 秒内未判定 offline（当前：${S:-未知}）"

# ---------------------------------------------------------------- 汇总
step "7. agent 安装脚本流程可见性（deploy/install.sh --dry-run）"
AGENT_DRY="$DATA_DIR/agent-dry.log"
sh deploy/install.sh --dry-run --server http://127.0.0.1:1 --token verify-token \
	--install-dir "$DATA_DIR/agentbin" > "$AGENT_DRY" 2>&1
AGENT_RC=$?
[ "$AGENT_RC" -eq 0 ] && pass "dry-run 退出码 0" || fail "dry-run 失败：$(tail -3 "$AGENT_DRY")"
[ "$(grep -cE '\([1-4]/4\)' "$AGENT_DRY" 2>/dev/null || echo 0)" -eq 4 ] \
	&& pass "输出 4 个阶段标题（1/4 … 4/4）" || fail "阶段标题缺失"
[ "$(grep -cE '✓|– 跳过' "$AGENT_DRY" 2>/dev/null || echo 0)" -ge 4 ] \
	&& pass "每个阶段都有 ✓/跳过 结论" || fail "结论行缺失"
grep -q "未指定 --version" "$AGENT_DRY" && pass "latest 版本解析步骤可见" || fail "未提示版本解析"

step "验收结果"
printf '  通过 %d 项，失败 %d 项\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] && { printf '\033[32m引导与接入 验收通过\033[0m\n'; exit 0; } || { printf '\033[31m引导与接入 验收未通过\033[0m\n'; exit 1; }
