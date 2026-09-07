#!/usr/bin/env bash
# =====================================================================
# scripts/bump-version.sh — 版本号统一管理（SSOT: version.json）
#
# 用法:
#   ./scripts/bump-version.sh                  # 自动 +1 build_seq
#   ./scripts/bump-version.sh --seq 945        # 强制设定值
#   ./scripts/bump-version.sh --dry-run        # 只打印，不写
#   source scripts/bump-version.sh             # 导出环境变量给上游用
#
# 维护的 3 个文件（保持 lockstep）:
#   version.json           (仓库根, 结构化)  **SSOT**
#   VERSION                (兼容旧 binary)   字符串格式
#   web/public/version.json (前端静态展示)
#   web/dist/version.json   (前端 build 产物)
#
# 历史:
#   - 2026-07-10 统一为 version.json 单一来源 (admin/misc.go 重构)
#     * 废弃：.deploy_seq / build_seq / 分散的环境变量
#     * 新增：env 变量 LLM_GATEWAY_VERSION_FILE 作为实例级 version.json 覆盖通道
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
    -h|--help) sed -n '2,34p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
# 跳过 archive/* / sr-* / release/* 等工作分支 tag；只有 semver 风格的
# X.Y.Z 才应当出现在 git_tag 字段。仓库同时存在 v 前缀（v2.4.1）与裸
# semver（2.5.x，2026-09 起）两种 tag，按版本号统一取最新 —— 此前 v 前缀
# 优先，遗留的 v2.5.0 会遮蔽后续裸 2.5.x，版本随每次 bump 回落（2026-09-05
# 部署 stamp 由 2.5.3 回退 2.5.0 即此因）。两种都缺失时仍回落 v0.0.0。
GIT_TAG=$(
  {
    git tag --list 'v[0-9]*.[0-9]*.[0-9]*'
    git tag --list '[0-9]*.[0-9]*.[0-9]*'
  } | awk '{ bare=$0; sub(/^v/, "", bare); print bare "\t" $0 }' \
    | sort -t. -k1,1n -k2,2n -k3,3n \
    | tail -n 1 | cut -f2
)
if [[ -z "$GIT_TAG" ]]; then
  GIT_TAG="v0.0.0"
fi
GIT_TAG_PATCH=$(echo "$GIT_TAG" | sed 's/^v//')   # "2.4.1"

# 新版本号
HEAD_SHA=$(git rev-parse --short=8 HEAD 2>/dev/null || echo "$CURRENT_GIT_SHA")
HEAD_DATE=$(date -u +%Y%m%d)

# build_seq 反映代码变更，不是部署计数。
#
# 修复前：每次 bump 永远 +1 (CURRENT_SEQ+1)。后果是 245 上一次部署停在
# 1957，而之后 91 次仅到 154 的重新部署把 build_seq 顶到 2048——同一份
# commit 在两台机器上 build_seq 漂移 91。deploy-seamless 的 bump-version
# 调用未区分"重跑同一 commit"与"代码真变了"。
#
# 修复后：仅当 git_sha 或 build_date 与 SSOT 不同（说明代码/日期变了）
# 才递增；否则保留原 seq，使同代码多次部署 build_seq 一致。--seq 显式
# 指定仍然尊重（修漂移或强制 +N）。
SAME_CODE=0
if [[ "$HEAD_SHA" == "$CURRENT_GIT_SHA" && "$HEAD_DATE" == "$(python3 -c "import json;print(json.load(open('$VERSION_JSON'))['build_date'])" 2>/dev/null)" ]]; then
  SAME_CODE=1
fi
if (( SAME_CODE == 1 )) && [[ -z "$TARGET_SEQ" ]]; then
  NEW_SEQ="$CURRENT_SEQ"
else
  NEW_SEQ=$((CURRENT_SEQ + 1))
  if [[ -n "$TARGET_SEQ" && "$TARGET_SEQ" -gt "$NEW_SEQ" ]]; then
    NEW_SEQ="$TARGET_SEQ"
  fi
fi
NEW_VERSION="${GIT_TAG_PATCH}-${HEAD_SHA}-${HEAD_DATE}-${NEW_SEQ}"
NOW_DATE=$(date -u +%Y-%m-%d)

echo "📌 bump-version"
echo "   current: seq=$CURRENT_SEQ version=$CURRENT_VERSION"
echo "   target:  seq=$NEW_SEQ version=$NEW_VERSION"
echo "   date:    $HEAD_DATE"
if (( SAME_CODE == 1 )) && [[ -z "$TARGET_SEQ" ]]; then
  echo "   ↳ git_sha/build_date 未变化，seq 保持不变（同代码重跑）"
fi

# ── dry-run 提前退出 ─────────────────────────────────────────────
if [[ "$DRY_RUN" == "true" ]]; then
  echo "   [dry-run] 不写文件"
  return 2>/dev/null || exit 0
fi

# ── 写文件 (SSOT: version.json) ──────────────────────────────────
write_version_files() {
  # 1. VERSION (兼容旧 binary)
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
echo "✅ 已更新 4 个版本文件 (SSOT: version.json)"
echo "   - $VERSION_JSON (后端 SSOT)"
echo "   - $VERSION_FILE (兼容旧 binary)"
echo "   - $WEB_PUBLIC_VERSION_JSON (前端静态)"
echo "   - $WEB_DIST_VERSION_JSON (前端构建产物)"
echo ""
echo "📦 部署时只需上传 version.json 到服务器即可"
echo "   scp version.json root@154:/opt/llm-gateway-go/version.json"

# ── 导出变量 ────────────────────────────────────────────────────
BUILD_VERSION="$GIT_TAG_PATCH"
BUILD_GIT_TAG="$GIT_TAG"
BUILD_GIT_SHA="$HEAD_SHA"
BUILD_DATE="$HEAD_DATE"
BUILD_SEQ="$NEW_SEQ"
VERSION_STRING="$NEW_VERSION"
export BUILD_VERSION BUILD_GIT_TAG BUILD_GIT_SHA BUILD_DATE BUILD_SEQ VERSION_STRING
