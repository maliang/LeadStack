#!/bin/sh
# LeadStack 自动发版版本推导：取最新 tag → patch + 1
# 契约：docs/ARCHITECTURE.md §51.4（主分支自动发版）
#
# 用法：
#   sh scripts/next-version.sh          # 在仓库内执行，打印下一个版本号
#
# 输出：单行版本号，带 v 前缀，例如 `v0.1.1`
#
# 规则：
#   - 没有任何 v* tag（全新仓库）           → v0.1.0（首版）
#   - 最新 tag 是 v1.2.3                   → v1.2.4
#   - 最新 tag 带预发布/构建后缀 v1.2.3-rc1 → v1.2.4（后缀忽略，只递增 patch）
#   - tag 不是 vX.Y.Z 形式                 → 报错退出（不猜）
#
# 可覆盖的环境变量：
#   TAG_MATCH  匹配哪些 tag（默认 'v[0-9]*'）；例如只认 v1/v2 主线可设为 'v*'
set -eu

# 取"版本号最大"的 tag，而不是 git describe 的"拓扑最近"：
#   - 发布号是单调语义版本，最高版本才是最新已发布版本；
#   - 分支分叉/旧提交补 tag 时，describe 可能给出更小的版本，导致重复发同一版本号。
# 注意：需要完整 tag（CI 里用 actions/checkout fetch-depth: 0 保证）。
match="${TAG_MATCH:-v[0-9]*}"
latest="$(git tag --list "$match" --sort=-v:refname 2>/dev/null | head -n 1 || true)"

# 没有任何 tag：首版从 v0.1.0 起（而不是 v0.0.0，避免与"占位版本"混淆）
if [ -z "$latest" ]; then
	printf 'v0.1.0\n'
	exit 0
fi

core="${latest#v}"
core="${core%%-*}"   # 去掉 -rc1 / -beta 之类的预发布后缀
core="${core%%+*}"   # 去掉 +build 构建元数据

old_ifs=$IFS
IFS=.
# shellcheck disable=SC2086  # 这里的字段拆分是刻意的
set -- $core
IFS=$old_ifs

if [ "$#" -ne 3 ]; then
	printf 'error: 最新 tag %s 不是 vX.Y.Z 三段形式，无法自动递增\n' "$latest" >&2
	exit 1
fi

major="$1"
minor="$2"
patch="$3"

for n in "$major" "$minor" "$patch"; do
	case "$n" in
		''|*[!0-9]*)
			printf 'error: 最新 tag %s 不是 vX.Y.Z 形式，无法自动递增\n' "$latest" >&2
			exit 1
			;;
	esac
done

printf 'v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))"
