#!/usr/bin/env bash
# =====================================================================
# scripts/bump-version.sh — 版本号统一管理（5 文件锁步更新）
#
# 用法:
#   ./scripts/bump-version.sh                  # 自动 +1 build_seq
#   ./scripts/bump-version.sh --seq 945        # 强制设定值
#   ./scripts/bump-version.sh --dry-run        # 只打印，不写
#   source scripts/bump-version.sh             # 导出环境变量给上游用
#
# 维护的 4 个文件（保持 lockstep）:
#   VERSION                (仓库根, 简版字符串)
#   version.json           (仓库根, 结构化)
#   web/public/version.json (前端静态)
#   web/dist/version.json   (前端 build 产物 — 通常由 web build 重写,
#                           但这里兜底以确保发布时一致)
#
# 计算规则:
#   git tag → <tag>
#   short git sha → <8 chars>
#   date → YYYYMMDD
#   seq → 取 version.json 的 build_seq (single source of truth) + 1
#     若 --seq 指定，使用指定值（但要求 ≥ 当前 seq）
#
# 导出变量:
#   BUILD_VERSION / BUILD_GIT_TAG / BUILD_GIT_SHA /
#   BUILD_DATE / BUILD_SEQ / VERSION_STRING
# =====================================================================

set -euo pipefail

# 支持 bash 直接调用和 source 两种模式
if [[ -n "${BASH_SOURCE[0]:-}" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
fi
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

VERSION_FILE="$PROJECT_ROOT/VERSION"
VERSION_JSON="$PROJECT_ROOT/version.json"
WEB_PUBLIC_VERSION_JSON="$PROJECT_ROOT/web/public/version.json"
WEB_DIST_VERSION_JSON="$PROJECT_ROOT/web/dist/version.json"

# ── 参数 ────────────────────────────────────────────────────────
TARGET_SEQ=""
DRY_RUN=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --seq)     TARGET_SEQ="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "bump-version: 未知参数 $1" >&2; exit 1 ;;
  esac
done

# ── 读取当前版本 ────────────────────────────────────────────────
[[ -f "$VERSION_JSON" ]] || { echo "bump-version: $VERSION_JSON 不存在" >&2; exit 1; }

# 用 python 解析 JSON (避免 grep 复杂正则多语言问题)
CURRENT_SEQ=$(python3 -c "import json; print(json.load(open('$VERSION_JSON'))['build_seq'])" 2>/dev/null \
  || echo 0)
CURRENT_GIT_SHA=$(python3 -c "import json; print(json.load(open('$VERSION_JSON')).get('git_sha',''))" 2>/dev/null \
  || echo "")
CURRENT_VERSION=$(python3 -c "import json; print(json.load(open('$VERSION_JSON'))['version'])" 2>/dev/null \
  || echo "v0.0.0")

# git tag (e.g. "v2.4.1") — 这是 NEW_VERSION 的 tag 部分
GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
GIT_TAG_PATCH=$(echo "$GIT_TAG" | sed 's/^v//')   # "2.4.1"

# 新版本号
HEAD_SHA=$(git rev-parse --short=8 HEAD 2>/dev/null || echo "$CURRENT_GIT_SHA")
HEAD_DATE=$(date -u +%Y%m%d)
NEW_SEQ=$((CURRENT_SEQ + 1))
if [[ -n "$TARGET_SEQ" && "$TARGET_SEQ" -gt "$NEW_SEQ" ]]; then
  NEW_SEQ="$TARGET_SEQ"
fi
NEW_VERSION="${GIT_TAG_PATCH}-${HEAD_SHA}-${HEAD_DATE}-${NEW_SEQ}"
NOW_DATE=$(date -u +%Y-%m-%d)

echo "📌 bump-version"
echo "   current: seq=$CURRENT_SEQ version=$CURRENT_VERSION"
echo "   target:  seq=$NEW_SEQ version=$NEW_VERSION"
echo "   date:    $HEAD_DATE"

# ── dry-run 提前退出 ─────────────────────────────────────────────
if [[ "$DRY_RUN" == "true" ]]; then
  echo "   [dry-run] 不写文件"
  return 2>/dev/null || exit 0
fi

# ── 写 4 个文件 ──────────────────────────────────────────────────
write_version_files() {
  # 1. VERSION (简版)
  printf '%s\n' "$NEW_VERSION" > "$VERSION_FILE"

  # 2-4. JSON 三个 (结构化)
  python3 - "$VERSION_JSON" "$WEB_PUBLIC_VERSION_JSON" "$WEB_DIST_VERSION_JSON" \
    "$NEW_VERSION" "$NEW_SEQ" "$HEAD_SHA" "$HEAD_DATE" <<'PY'
import json, sys, os
vjson, wp_vjson, wd_vjson, version, seq, sha, date = sys.argv[1:]
data = {
  "version": version,
  "git_tag": version.split("-")[0],
  "git_sha": sha,
  "build_seq": int(seq),
  "build_date": date,
  "module": "llm-gateway-go",
}
out = json.dumps(data, indent=2) + "\n"
for p in [vjson, wp_vjson, wd_vjson]:
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f: f.write(out)
PY
}

write_version_files
echo "✅ 已更新 4 个版本文件"
echo "   - $VERSION_FILE"
echo "   - $VERSION_JSON"
echo "   - $WEB_PUBLIC_VERSION_JSON"
echo "   - $WEB_DIST_VERSION_JSON"

# ── 导出变量 ────────────────────────────────────────────────────
BUILD_VERSION="$GIT_TAG_PATCH"
BUILD_GIT_TAG="$GIT_TAG"
BUILD_GIT_SHA="$HEAD_SHA"
BUILD_DATE="$HEAD_DATE"
BUILD_SEQ="$NEW_SEQ"
VERSION_STRING="$NEW_VERSION"
export BUILD_VERSION BUILD_GIT_TAG BUILD_GIT_SHA BUILD_DATE BUILD_SEQ VERSION_STRING
