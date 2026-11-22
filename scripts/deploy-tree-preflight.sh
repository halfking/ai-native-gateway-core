#!/usr/bin/env bash
# =============================================================================
# scripts/deploy-tree-preflight.sh — 部署前「树 + 槽位」预检（2026-11-23）
# =============================================================================
# 定位：把两条**已经付出过代价**的纪律变成**闸门**，而不是留在 AGENTS.md 里
#       靠人记。
#
# ── 为什么要有这个脚本（两条纪律各自的代价）────────────────────────────────
#
#  ① 【部署树必须 ⊇ 修复提交】——「祖先闸」
#
#    代价（2026-11-22 实测）：修复发上 211（seq 3198）、推上 origin/main，
#    之后三个班次用**尚未同步这次推送的旧树**连发 211（seq 3199/3200/3201）
#    ⇒ 三棵树都不含修复 ⇒ 大请求体 120 秒超时在生产回来了约 30 分钟。
#
#    ⇒ 根因不是「推送没成功」，而是「**推送只保证修复在 origin，不保证修复在
#      现役**」：每个班次部署的都是自己那棵树的 HEAD，谁落后谁就把别人的
#      修复冲掉。
#    ⇒ 这道闸把「收敛」从口号变成检查：
#      `git merge-base --is-ancestor <fix-commit> HEAD` 不成立 ⇒ **拒绝部署**。
#
#  ② 【生产手工动容器前先看槽位与 upstream】——「槽位闸」
#
#    代价（2026-11-23 实测，见 docs/operations/2026-11-23-211配置优化执行报告.md
#    §五）：手工重建容器时用了已被并行部署占用的名字 llm-gateway-211-b
#    ⇒ docker 报 Conflict ⇒ 旧容器已 stop ⇒ **8781 端口无人监听 ⇒ 服务中断**；
#    随后改用未占用的名字重建，容器起来了却**不在 nginx 路由上**
#    （引擎用 nginx upstream 的端口反推活跃槽，名字必须与槽位匹配）
#    ⇒ 游离实例 + 真实流量在另一槽。
#
#    ⇒ 这道闸把「先 docker ps 确认槽位占用与 upstream 指向」变成**必跑的一步**。
#
# ── 用法 ──────────────────────────────────────────────────────────────────
#   bash scripts/deploy-tree-preflight.sh --require <sha> [--require <sha>...]
#   bash scripts/deploy-tree-preflight.sh --via 211            # 只看目标机现状
#   bash scripts/deploy-tree-preflight.sh --require <sha> --via 211
#
# ── 返回值 ────────────────────────────────────────────────────────────────
#   0 = 全部检查通过（可以部署）
#   1 = 检查失败（**不要部署**）
#   2 = 参数错误
#
# ⚠ 本脚本**只读**：不取锁、不停容器、不切流、不改任何状态。
# =============================================================================
set -euo pipefail

REQUIRED_COMMITS=()
VIA_HOST=""
DRY_RUN=0

usage() {
  cat <<'EOF'
用法：
  bash scripts/deploy-tree-preflight.sh --require <sha> [--require <sha>...] [--via <host>]
  bash scripts/deploy-tree-preflight.sh --via <host>

选项：
  --require <sha>   必须包含在部署树里的提交（可重复）。不通过 ⇒ 退出 1。
  --via <host>       ssh 别名（211 / 211-mesh / …），对目标机做只读现状盘点。
  -h | --help        本帮助。
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --require)
      [[ $# -ge 2 ]] || { echo "参数错误：--require 需要一个 sha" >&2; exit 2; }
      REQUIRED_COMMITS+=("$2"); shift 2 ;;
    --via)
      [[ $# -ge 2 ]] || { echo "参数错误：--via 需要一个主机别名" >&2; exit 2; }
      VIA_HOST="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

RC=0
say()  { printf '%s\n' "$*"; }
ok()   { printf '  ✅ %s\n' "$*"; }
bad()  { printf '  ❌ %s\n' "$*" >&2; }
warn() { printf '  ⚠️  %s\n' "$*" >&2; }

# ── 闸 1：部署树必须 ⊇ 要求的提交（本地）────────────────────────────────
check_tree_ancestry() {
  if [[ ${#REQUIRED_COMMITS[@]} -eq 0 ]]; then
    say "① 祖先闸：未指定 --require ⇒ 跳过（部署前请显式列出本轮修复提交）"
    return 0
  fi
  say "① 祖先闸：部署树必须包含下列提交"
  git fetch origin --quiet 2>/dev/null || warn "git fetch origin 失败（继续用本地 ref 判定）"
  local head_sha
  head_sha="$(git rev-parse --short HEAD)"
  say "   当前树 HEAD = ${head_sha}"
  local c
  for c in "${REQUIRED_COMMITS[@]}"; do
    if ! git cat-file -e "${c}^{commit}" 2>/dev/null; then
      bad "提交 ${c} 在本地仓库里不存在（多半是没 fetch 到）⇒ 不要部署"
      RC=1
      continue
    fi
    if git merge-base --is-ancestor "${c}" HEAD; then
      ok "${c} ∈ HEAD（修复在部署树里）"
    else
      bad "${c} ∉ HEAD —— 这棵部署树**不含**该修复 ⇒ 部署会把它冲掉"
      say "     处置：git fetch origin && git merge origin/main（或 git rebase origin/main）后再部署"
      RC=1
    fi
  done
}

# ── 闸 2：目标机现状盘点（只读）──────────────────────────────────────────
inspect_remote() {
  local host="$1"
  say "② 槽位闸：${host} 现状（只读）"
  ssh -o ConnectTimeout=15 -o BatchMode=yes "$host" 'bash -s' <<'REMOTE'
set -uo pipefail
echo "   -- 容器（llm-gateway-*）--"
docker ps -a --format '   {{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null | grep -E 'llm-gateway' || echo "   （无）"
echo "   -- nginx upstream（活跃槽 = 主 server 行）--"
for f in /etc/nginx/conf.d/ns-gateway-211.conf /etc/nginx/conf.d/*gateway*.conf; do
  [ -f "$f" ] || continue
  echo "   [$f]"
  awk '/upstream .*gateway.*upstream \{/,/\}/' "$f" | sed 's/^/   /' | head -8
  break
done
echo "   -- 部署锁（不抢，只报告）--"
for l in /tmp/kx-llm-gateway-deploy-*.lock /var/lock/kx-llm-gateway-deploy-*.lock; do
  [ -e "$l" ] || continue
  echo "   $l"
  head -5 "$l" 2>/dev/null | sed 's/^/     /'
done
REMOTE
}

say "════ 部署前预检（只读）════"
check_tree_ancestry

if [[ -n "$VIA_HOST" ]]; then
  if ! inspect_remote "$VIA_HOST"; then
    warn "无法连接 ${VIA_HOST} 做现状盘点（不阻断，但请人工确认槽位）"
  fi
fi

say "════ 结果 ════"
if [[ $RC -eq 0 ]]; then
  say "✅ 预检通过 ⇒ 可以部署"
  exit 0
fi
say "❌ 预检未通过 ⇒ **不要部署**（先按上面的处置修好部署树）"
exit 1
