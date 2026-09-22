#!/usr/bin/env bash
# tests/48h-audit/scripts/aggregate-reports.sh
# 把各域 reports/latest.md 聚合到 reports/INDEX.md，便于一眼看全局状态。
# Usage:
#   bash tests/48h-audit/scripts/aggregate-reports.sh > tests/48h-audit/reports/INDEX.md

set -uo pipefail
SELF_PATH="${BASH_SOURCE[0]:-$0}"
if [[ "$SELF_PATH" != /* ]]; then
  SELF_PATH="$(cd "$(dirname "$SELF_PATH")" && pwd)/$(basename "$SELF_PATH")"
fi
SELF_DIR="$(cd "$(dirname "$SELF_PATH")" && pwd)"
DIR="$SELF_DIR/.."

c_blu=$'\033[0;34m'; c_off=$'\033[0m'

echo "# 48h 审计 · 跨域报告聚合"
echo
echo "_聚合时间: $(date '+%Y-%m-%d %H:%M:%S %Z')_"
echo
echo "| 域 | 名称 | 状态 | 改动 commits | P0 | P1 | P2 | P3 | 遗留 | 链接 |"
echo "|---|---|---|---:|---:|---:|---:|---:|---:|---|"

for d in $(ls "$DIR" | grep -E '^D[0-9]+-' | sort); do
  d_short="$(echo $d | cut -d- -f1)"
  latest="$DIR/$d/reports/latest.md"
  if [[ ! -f "$latest" ]]; then
    echo "|$d_short|$(echo $d | sed -E 's/^D[0-9]+-//')|未留档|—|—|—|—|—|—|[plan.md](../$d/plan.md)|"
    continue
  fi
  p0=$(grep -cE '^\| *P0 *\|' "$latest" 2>/dev/null | tr -d '\n' || echo 0)
  p1=$(grep -cE '^\| *P1 *\|' "$latest" 2>/dev/null | tr -d '\n' || echo 0)
  p2=$(grep -cE '^\| *P2 *\|' "$latest" 2>/dev/null | tr -d '\n' || echo 0)
  p3=$(grep -cE '^\| *P3 *\|' "$latest" 2>/dev/null | tr -d '\n' || echo 0)
  left=$(grep -cE '^- \[ \]' "$latest" 2>/dev/null | tr -d '\n' || echo 0)
  status=$(grep -E '状态[：:]' "$latest" 2>/dev/null | head -1 | sed 's/.*状态[：:]//; s/[[:space:]]*$//' | head -1)
  [[ -z "$status" ]] && status="草稿"
  echo "|$d_short|$(echo $d | sed -E 's/^D[0-9]+-//')|$status|$p0|$p1|$p2|$p3|$left|[latest]($d/reports/latest.md)|" # column 2 = kebab name, column 1 = DXX
done

echo
echo "_由 tests/48h-audit/scripts/aggregate-reports.sh 自动生成_"