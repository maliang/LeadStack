#!/usr/bin/env bash
# 发布应用名「真链路」验收的负向对照（r5，可选、慢）
#
# 目的：证明 proxy-fallback 断言真的挂在 `releaseAppLabel` 的优先级顺序上，而不是碰巧绿。
# 做法：
#   ① 在临时 git worktree 里重建**当前工作区**（HEAD + git diff + 未跟踪文件），不碰主工作树；
#   ② 把 web-next/src/lib/deploy-domain.ts 的优先级还原成旧序
#      （app_name || app_slug || appNames），构建前端与二进制；
#   ③ 在旧序产物上跑 scripts/verify_release_appname.sh（换端口，避免与主跑冲突）；
#   ④ 期望：proxy-fallback 变红、其余断言全绿、脚本整体退出非 0。
#      （browser-run 是浏览器脚本的聚合退出码，随 proxy-fallback 一起红，属派生项不算噪声。）
#      若旧序下仍然全绿，说明该断言抓不住优先级回退 —— 本对照失败。
#
# 用法：NEGATIVE_CONTROL=1 bash scripts/verify_release_appname.sh
#      （或直接 bash scripts/verify_release_appname_negative_control.sh）
# 端口：NEG_PORT（默认 17670）/ NEG_PROXY_PORT（默认 17671）。
# 可重复跑：mktemp 目录 + trap 清理 worktree 与临时文件；端口被占会由内层脚本提前失败。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${NEG_PORT:-17670}"
PROXY_PORT="${NEG_PROXY_PORT:-17671}"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP="$(mktemp -d "$TMP_BASE/leadstack-negctl.XXXXXX")" || { echo "✗ 无法创建临时目录"; exit 1; }
WT="$TMP/wt"
REPORT_OUT="$TMP/report.md"
RUN_LOG="$TMP/run.log"
BUILD_LOG="$TMP/build.log"

say() { printf '%s\n' "$*"; }
die() { say "✗ 负向对照失败：$*"; exit 1; }

cleanup() {
  trap - EXIT
  git worktree remove --force "$WT" >/dev/null 2>&1 || true
  git worktree prune >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

say "== 负向对照：旧优先级（app_name || app_slug || appNames）下 proxy-fallback 必须变红"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "当前目录不是 git 仓库，无法建临时 worktree"
git worktree add --detach "$WT" HEAD >/dev/null 2>&1 || die "git worktree add 失败（HEAD 不可用？）"

# worktree 是 HEAD 的干净副本：先把眼下的未提交改动搬进去，否则测的不是当前实现。
git diff --binary > "$TMP/work.patch" 2>/dev/null || die "git diff 失败"
if [ -s "$TMP/work.patch" ]; then
  git -C "$WT" apply --binary "$TMP/work.patch" >/dev/null 2>&1 || die "把主工作树 diff apply 到 worktree 失败"
fi
# 未跟踪但参与构建的文件（新增脚本/资源等）；构建产物与团队临时目录不复制。
while IFS= read -r f; do
  [ -n "$f" ] || continue
  case "$f" in
    bin/*|dist/*|internal/webui/dist/*|web-next/dist/*|web-next/node_modules/*|.agent-teams/*|.auto-team/*|.e2e-logs/*) continue ;;
  esac
  [ -f "$f" ] || continue
  mkdir -p "$WT/$(dirname "$f")"
  cp "$f" "$WT/$f"
done < <(git ls-files --others --exclude-standard)

SRC="$WT/web-next/src/lib/deploy-domain.ts"
[ -f "$SRC" ] || die "worktree 里找不到 $SRC"

# 旧序：app_name || app_slug || appNames || app_id。改不动就拒绝给结论，绝不静默跳过。
node - "$SRC" <<'NODE' || die "改写 deploy-domain.ts 失败（当前优先级与预期不一致，源码可能已变）"
const fs = require('fs')
const file = process.argv[2]
const src = fs.readFileSync(file, 'utf8')
const NEW_LINE = '  return release.app_name || appNames?.get(release.app_id) || release.app_slug || release.app_id'
const OLD_LINE = '  return release.app_name || release.app_slug || appNames?.get(release.app_id) || release.app_id'
if (!src.includes(NEW_LINE)) {
  console.error('✗ 未找到「新优先级」那一行，拒绝在未知状态下给结论')
  process.exit(1)
}
fs.writeFileSync(file, src.replace(NEW_LINE, OLD_LINE))
NODE
grep -q 'return release.app_name || release.app_slug || appNames' "$SRC" || die "旧序未生效（grep 不到 app_name || app_slug || appNames）"

say ">> worktree 构建（make build，日志 ${BUILD_LOG}）…"
( cd "$WT" && make build ) > "$BUILD_LOG" 2>&1 || {
  tail -25 "$BUILD_LOG" | sed 's/^/   /'
  die "worktree 构建失败"
}
[ -x "$WT/bin/leadstack" ] && [ -x "$WT/bin/leadstack-agent" ] || die "构建后仍缺 bin/leadstack(-agent)"

say ">> 旧序产物上跑真链路验收（端口 ${PORT}/${PROXY_PORT}，日志 ${RUN_LOG}）…"
( cd "$WT" && NEGATIVE_CONTROL=0 PORT="$PORT" PROXY_PORT="$PROXY_PORT" REPORT="$REPORT_OUT" \
    bash scripts/verify_release_appname.sh ) > "$RUN_LOG" 2>&1
RC=$?
[ -f "$REPORT_OUT" ] || { tail -20 "$RUN_LOG" | sed 's/^/   /'; die "验收未产出报告（退出码 ${RC}）"; }

FALLBACK_FAIL="$(grep -c '^| proxy-fallback |.*| FAIL |' "$REPORT_OUT" 2>/dev/null || true)"
OTHER_FAIL="$(grep '| FAIL |' "$REPORT_OUT" 2>/dev/null | grep -v '^| proxy-fallback |' | grep -v '^| browser-run |' | wc -l | tr -d ' ')"
FALLBACK_FAIL="${FALLBACK_FAIL:-0}"

say "   旧序下验收退出码 ${RC}；失败断言："
grep '| FAIL |' "$REPORT_OUT" 2>/dev/null | sed 's/^/   /' | head -5

[ "$FALLBACK_FAIL" -ge 1 ] || die "旧序下 proxy-fallback 没有变红 —— 该断言抓不住优先级回退"
[ "$OTHER_FAIL" -eq 0 ] || die "旧序下除 proxy-fallback 还有 ${OTHER_FAIL} 条断言失败 —— 噪声过多，结论不可信"
[ "$RC" -ne 0 ] || die "旧序下脚本整体退出 0 —— proxy-fallback 失败却没有让脚本失败"

say "✓ 负向对照通过：旧序下仅 proxy-fallback 变红、其余断言全绿 —— 该断言确实能抓住优先级回退"
exit 0
