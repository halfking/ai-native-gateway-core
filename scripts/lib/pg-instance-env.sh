#!/usr/bin/env bash
# Shared env-loader discovery for instance-sync scripts.
set -euo pipefail
PG_INSTANCE_ROOT="${PG_INSTANCE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

pg_instance_detect_envs_loader() {
  local cursor="$PG_INSTANCE_ROOT"
  # ★ 2026-11-05：工作区重组后 envs 仓落到 ai-native-tools/deploy/envs ⇒
  #   候选从「<ws>/envs/loader.sh」扩成两条（**新布局优先**、旧布局兜底），
  #   否则本函数在重组后的布局上永远返回 1（找不到）。
  local rel
  while [[ "$cursor" != "/" ]]; do
    if [[ "$(basename "$cursor")" == "ai-native-tools" ]]; then
      for rel in "deploy/envs/loader.sh" "envs/loader.sh"; do
        if [[ -r "$cursor/$rel" ]]; then
          printf '%s\n' "$cursor/$rel"
          return 0
        fi
      done
    fi
    cursor="$(dirname "$cursor")"
  done
  return 1
}
