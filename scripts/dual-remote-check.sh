#!/usr/bin/env bash
# 双远端推送一致性校验（dual-remote-push-rule 固化）
#
# 背景（用户明令，2026-11-04 确立）：llm-gateway-go 每次推送必须双推
# （origin=codeup official-deploy + github 镜像）并核对两远端 0/0 一致。
# 历史上并行会话长期单推 codeup，造成过三方真分叉（github 落后 8、
# 本地独有 3、codeup 独有 4），收敛靠人工甄别，成本高。
#
# 本脚本把「推送前/后核对」固化为可执行检查：
#   1. fetch 两个远端（失败即红——推送场景下不能拿陈旧引用当证据）
#   2. 报告三方差异：HEAD / origin/main / github/main 各自的 ahead/behind
#   3. 退出码：0 = 三方一致；1 = 存在分叉（列出来待甄别，不自动合并）
#
# 用法：
#   scripts/dual-remote-check.sh            # 检查 main（默认）
#   scripts/dual-remote-check.sh <branch>   # 检查指定分支
#   scripts/dual-remote-check.sh --quiet    # CI 用：只输出结论行
#
# 设计边界：只读操作（fetch + rev-list），绝不自动 push/merge——
# 分叉甄别按 dual-remote-push-rule 是人工静默流程，脚本只负责发现。

set -euo pipefail

BRANCH="${1:-main}"
QUIET=0
[ "${1:-}" = "--quiet" ] && { QUIET=1; BRANCH="main"; }

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'

# 兼容 macOS bash 3.2 的 echo（不解释 \033）：用 printf 渲染颜色。
say() { if [ "$QUIET" -eq 0 ]; then printf '%b\n' "$*"; fi }

# 远端名探测：origin=codeup、github=镜像 是本仓约定；但仓库可能只有其中一个
# 或名字不同（如父仓 origin=github）。按「存在 origin 与 github 两个远端」的
# 仓才启用双远端校验；只有一个远端的仓直接跳过（exit 0，不算失败）。
has_origin=$(git remote | grep -cx '^origin$' || true)
has_github=$(git remote | grep -cx '^github$' || true)
if [ "$has_origin" -eq 0 ] || [ "$has_github" -eq 0 ]; then
  say "dual-remote-check: skipped (this repo does not have both 'origin' and 'github' remotes)"
  exit 0
fi

say "dual-remote-check: branch=$BRANCH"

# 1. fetch（--quiet 避免刷屏；失败即退出——陈旧引用不能当证据）
git fetch origin "$BRANCH" --quiet 2>/dev/null || {
  echo "dual-remote-check: FAIL — fetch origin/$BRANCH failed" >&2
  exit 1
}
git fetch github "$BRANCH" --quiet 2>/dev/null || {
  echo "dual-remote-check: FAIL — fetch github/$BRANCH failed" >&2
  exit 1
}

LOCAL_SHA=$(git rev-parse --short HEAD)
ORIGIN_SHA=$(git rev-parse --short "origin/$BRANCH" 2>/dev/null || echo "MISSING")
GITHUB_SHA=$(git rev-parse --short "github/$BRANCH" 2>/dev/null || echo "MISSING")

say "  local(main)  = $LOCAL_SHA"
say "  origin/main  = $ORIGIN_SHA"
say "  github/main  = $GITHUB_SHA"

FAIL=0

# 2. 本地 vs origin
if [ "$LOCAL_SHA" != "$ORIGIN_SHA" ]; then
  AB=$(git rev-list --left-right --count "HEAD...origin/$BRANCH")
  say "  ${YELLOW}local vs origin: behind/ahead = $AB${NC}"
  FAIL=1
fi

# 3. origin vs github（双推一致性的核心检查）
if [ "$ORIGIN_SHA" != "$GITHUB_SHA" ]; then
  AB=$(git rev-list --left-right --count "origin/$BRANCH...github/$BRANCH")
  say "  ${YELLOW}origin vs github: origin-only/github-only = $AB${NC}"
  git log --oneline "origin/$BRANCH..github/$BRANCH" 2>/dev/null | head -5 | sed 's/^/    github-only: /'
  git log --oneline "github/$BRANCH..origin/$BRANCH" 2>/dev/null | head -5 | sed 's/^/    origin-only: /'
  FAIL=1
fi

# 4. 结论
if [ "$FAIL" -eq 0 ]; then
  say "  ${GREEN}OK: three-way consistent (0/0/0)${NC}"
  [ "$QUIET" -eq 1 ] && echo "dual-remote-check: PASS ($BRANCH 0/0/0 @ $ORIGIN_SHA)"
  exit 0
else
  say "  ${RED}FAIL: divergence detected — resolve per dual-remote-push-rule (identify silently, then converge; never auto-merge)${NC}"
  [ "$QUIET" -eq 1 ] && echo "dual-remote-check: FAIL ($BRANCH diverged)"
  exit 1
fi
