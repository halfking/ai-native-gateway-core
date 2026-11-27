#!/usr/bin/env bash
# 安全删除已合并分支脚本
# 生成时间: 2026-11-27
# 用途: 删除内容已100%合并到main的远端分支
#
# 使用方法:
#   bash scripts/delete-merged-branches.sh --dry-run   # 预览模式（推荐）
#   bash scripts/delete-merged-branches.sh --execute   # 实际执行

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

MODE="${1:-}"

if [[ "$MODE" != "--dry-run" && "$MODE" != "--execute" ]]; then
  echo "错误: 必须指定模式"
  echo "用法: $0 --dry-run   # 预览"
  echo "      $0 --execute   # 执行"
  exit 1
fi

echo "================================================"
echo "远端分支删除工具"
echo "================================================"
echo "模式: $MODE"
echo "仓库: $REPO_ROOT"
echo ""

# 待删除分支列表（已验证零丢失）
BRANCHES_TO_DELETE=(
  "fix/rm-turns-leg-savepoint"
  "codex/211-statistics-v2-release"
  "feature/users-page-role-verify"
)

echo "准备删除的分支（共 ${#BRANCHES_TO_DELETE[@]} 个）："
printf '  - %s\n' "${BRANCHES_TO_DELETE[@]}"
echo ""

# 验证每个分支的合并状态
echo "================================================"
echo "步骤 1: 验证分支合并状态"
echo "================================================"

VERIFICATION_FAILED=0

for branch in "${BRANCHES_TO_DELETE[@]}"; do
  echo ""
  echo "检查: $branch"
  
  # 检查分支是否存在
  if ! git rev-parse "origin/$branch" >/dev/null 2>&1; then
    echo "  ⚠️  分支不存在于远端，跳过"
    continue
  fi
  
  # 获取分支tip
  tip=$(git rev-parse "origin/$branch")
  echo "  Tip: $tip"
  
  # 检查是否是main的祖先
  if git merge-base --is-ancestor "$tip" origin/main 2>/dev/null; then
    echo "  ✅ 已是 origin/main 的祖先（直接合并）"
  else
    # 检查patch-id等价性
    echo "  ℹ️  不是直接祖先，检查patch等价性..."
    
    # 获取该分支独有的提交
    unique_commits=$(git log origin/main.."origin/$branch" --format=%H 2>/dev/null)
    
    if [[ -z "$unique_commits" ]]; then
      echo "  ✅ 无独有提交（已合并）"
    else
      # 检查每个提交是否有等价patch在main
      all_found=true
      for commit in $unique_commits; do
        # 计算patch-id
        patch_id=$(git show "$commit" | git patch-id --stable | cut -d' ' -f1)
        
        # 在main中查找相同patch-id
        # SIGPIPE 判据（跨平台口径）：判据量 = **中间段的输出体量**（git log
        # 全历史 diff 流 → git patch-id 中段；Linux PIPE_BUF 4,096 B 起 / macOS
        # 16–32 KiB——大仓库可触及 64 KiB 管道容量，grep -q 提前退出有 SIGPIPE 面）。
        # ⚠ 本脚本为运维便利脚本，误杀仅丢一行等价判定（fail-safe 侧 skip）。
        if git log origin/main --format=%H --patch-with-stat | git patch-id --stable | grep -q "^$patch_id"; then
          echo "  ✅ $commit 有等价patch在main"
        else
          echo "  ❌ $commit 未在main找到等价patch"
          all_found=false
          VERIFICATION_FAILED=1
        fi
      done
      
      if $all_found; then
        echo "  ✅ 所有提交均有等价patch在main"
      fi
    fi
  fi
done

echo ""
echo "================================================"
echo "步骤 2: 执行删除"
echo "================================================"

if [[ $VERIFICATION_FAILED -eq 1 ]]; then
  echo ""
  echo "❌ 验证失败：部分分支内容未在main"
  echo "   请手动审查后再删除"
  exit 1
fi

if [[ "$MODE" == "--dry-run" ]]; then
  echo ""
  echo "🔍 预览模式 - 将执行以下命令："
  echo ""
  for branch in "${BRANCHES_TO_DELETE[@]}"; do
    if git rev-parse "origin/$branch" >/dev/null 2>&1; then
      echo "  git push origin --delete $branch"
    fi
  done
  echo ""
  echo "✓ 预览完成"
  echo "  要实际执行删除，请运行: $0 --execute"
  
elif [[ "$MODE" == "--execute" ]]; then
  echo ""
  echo "⚠️  即将删除远端分支..."
  echo "   按Ctrl+C可在5秒内中止"
  sleep 5
  
  DELETED_COUNT=0
  SKIPPED_COUNT=0
  
  for branch in "${BRANCHES_TO_DELETE[@]}"; do
    if ! git rev-parse "origin/$branch" >/dev/null 2>&1; then
      echo "  ⊘ $branch (不存在，跳过)"
      ((SKIPPED_COUNT++))
      continue
    fi
    
    echo "  → 删除 $branch..."
    if git push origin --delete "$branch" 2>&1; then
      echo "  ✓ $branch 已删除"
      ((DELETED_COUNT++))
    else
      echo "  ✗ $branch 删除失败"
    fi
  done
  
  echo ""
  echo "================================================"
  echo "删除完成"
  echo "================================================"
  echo "  已删除: $DELETED_COUNT"
  echo "  已跳过: $SKIPPED_COUNT"
  echo ""
  echo "建议操作:"
  echo "  1. 更新本地仓库: git fetch --prune"
  echo "  2. 删除本地跟踪分支: git branch -d <branch-name>"
fi
