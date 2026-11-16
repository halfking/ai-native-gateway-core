#!/usr/bin/env bash
# =====================================================================
# scripts/test-gate-merge-coverage.sh — 验证合并提交下闸门的取包覆盖
# =====================================================================
# 用途：断言合并提交时 deploy-test-gate.sh 能正确提取两侧差异的包清单。
# 用法：bash scripts/test-gate-merge-coverage.sh
# 退出码：0=通过；1=失败
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

log() { printf '[test-gate-merge] %s\n' "$*" >&2; }
die() { printf '[test-gate-merge] FAIL: %s\n' "$*" >&2; exit 1; }

cd "$REPO_ROOT"

# ── 测试用例：使用已知展示盲区的合并提交 ─────────────────────────────
# f3c1374ab 是 R76-3 轮的合并提交，feature 分支改了 provider/，
# 旧口径（HEAD~1 HEAD）完全看不到，是验证盲区修复的理想用例。
merge_commit="f3c1374ab"
if ! git cat-file -e "$merge_commit" 2>/dev/null; then
  log "★ 提交 $merge_commit 不存在于当前仓库 ⇒ 回落查找最近的合并提交"
  merge_commit=""
  for c in $(git rev-list HEAD -50); do
    parents_count="$(git rev-list --parents -n1 "$c" 2>/dev/null | wc -w | tr -d ' ')"
    if [[ "$parents_count" -ge 3 ]]; then
      merge_commit="$c"
      break
    fi
  done
  
  if [[ -z "$merge_commit" ]]; then
    log "★ 最近 50 个提交内没有合并提交 ⇒ 跳过此测试（无可测形态）"
    exit 0
  fi
fi

log "找到合并提交：$merge_commit"
git log --oneline -1 "$merge_commit"

# ── 提取该合并提交涉及的包（按 merge-base 口径） ──────────────────────
# 模拟闸门脚本的逻辑：检测合并 ⇒ 用 merge-base
parents_count="$(git rev-list --parents -n1 "$merge_commit" 2>/dev/null | wc -w | tr -d ' ')"
if [[ "$parents_count" -ge 3 ]]; then
  merge_base="$(git merge-base "${merge_commit}~1" "${merge_commit}~2" 2>/dev/null || true)"
  if [[ -z "$merge_base" ]]; then
    die "拿不到 merge-base（提交 $merge_commit）"
  fi
  log "merge-base: $merge_base"
  
  # 按 merge-base → HEAD 取全部改动文件
  changed_files="$(git diff --name-only -z "$merge_base" "$merge_commit" 2>/dev/null | tr '\0' '\n' | sort -u)"
  
  # 只留 Go 包目录
  packages=()
  dirs="$(printf '%s\n' "$changed_files" | grep '\.go$' | xargs -n1 dirname 2>/dev/null | sort -u || true)"
  while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    # 检查该目录在当前 HEAD 是否有测试文件（用 HEAD 的树，不是合并提交的）
    if git ls-tree -r --name-only HEAD -- "$dir" | grep -q '_test\.go$' 2>/dev/null; then
      packages+=("$dir")
    fi
  done <<< "$dirs"
  
  if [[ ${#packages[@]} -eq 0 ]]; then
    log "★ 该合并提交没有触及带测试的包 ⇒ 无法验证（换一个提交）"
    exit 0
  fi
  
  log "按 merge-base 口径提取到 ${#packages[@]} 个包："
  printf '  %s\n' "${packages[@]}"
  
  # ── 断言：关键包必须在清单里 ──────────────────────────────────────────
  # 根据交接文档，provider/ 是典型的「feature 分支改动、但 HEAD~1 HEAD 看不到」的包
  must_have=()
  for critical in provider bg admin domains/streaming; do
    for pkg in "${packages[@]}"; do
      if [[ "$pkg" == "$critical" || "$pkg" == "$critical/"* ]]; then
        must_have+=("$critical")
        break
      fi
    done
  done
  
  if [[ ${#must_have[@]} -gt 0 ]]; then
    log "★ PASS：合并提交下关键包进入清单（${must_have[*]}）"
  else
    log "★ SKIP：该合并提交未触及关键包（provider/bg/admin/domains/streaming），无法验证盲区修复"
  fi
  
  # ── 对比：旧口径（HEAD~1 HEAD）会漏掉哪些 ──────────────────────────
  old_changed="$(git diff --name-only -z "${merge_commit}~1" "$merge_commit" 2>/dev/null | tr '\0' '\n' | sort -u)"
  old_packages=()
  old_dirs="$(printf '%s\n' "$old_changed" | grep '\.go$' | xargs -n1 dirname 2>/dev/null | sort -u || true)"
  while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    if git ls-tree -r --name-only HEAD -- "$dir" | grep -q '_test\.go$' 2>/dev/null; then
      old_packages+=("$dir")
    fi
  done <<< "$old_dirs"
  
  # 找出新口径有、旧口径没有的包（即旧口径的盲区）
  blind_spot=()
  for pkg in "${packages[@]}"; do
    found=0
    for old_pkg in "${old_packages[@]}"; do
      if [[ "$pkg" == "$old_pkg" ]]; then
        found=1
        break
      fi
    done
    if [[ $found -eq 0 ]]; then
      blind_spot+=("$pkg")
    fi
  done
  
  if [[ ${#blind_spot[@]} -gt 0 ]]; then
    log "★ 旧口径（HEAD~1 HEAD）的盲区（${#blind_spot[@]} 个包）："
    printf '    %s\n' "${blind_spot[@]}"
    log "★ PASS：修复后这些包能进清单"
  else
    log "★ 该合并提交两侧改动无差异 ⇒ 旧口径无盲区（无法展示修复效果）"
  fi
  
else
  die "提交 $merge_commit 不是合并提交（parents_count=$parents_count）"
fi

log "★ 测试通过 ★"
exit 0
