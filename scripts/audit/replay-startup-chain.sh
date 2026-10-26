#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Purpose: 把注册启动链**再跑一遍**，逐条报告结果。
#
# WHY (2026-10-21, R67 轮 / C13):
#   迁移 532 的脚本里有 `DROP VIEW IF EXISTS request_logs_with_current_month`。
#   而三个**后建**视图（routing_analytics_source / v_model_health_dashboard /
#   v_probe_system_health）依赖它 ⇒ 链跑第二遍时 532 会撞 2BP01 而**中止**。
#   ⇒ 「链跑两遍」这件事的完整影响面必须**实测**，不能推演。
#
# 用法: bash scripts/audit/replay-startup-chain.sh <目标库名>
#   目标库会被就地重放启动链（请传一次性库）。
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
DB="${1:-}"
if [[ -z "$DB" ]]; then
  echo "用法: bash $0 <目标库名>" >&2
  exit 2
fi
SF_DIR="$REPO_ROOT/installer/cmd/llm-gw-installer/embeddata/startup"

psql_() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" -q -A -t "$@"; }

echo "== 重放注册启动链到 ${DB} =="

ok=0; fail=0; missing=0
: > /tmp/r67/replay-fail.txt
while read -r f; do
  [[ -n "$f" ]] || continue
  p="$SF_DIR/$f"
  if [[ ! -f "$p" ]]; then
    missing=$((missing+1))
    echo "$f :: ★文件不在 embeddata 里" >> /tmp/r67/replay-fail.txt
    continue
  fi
  args=(-q -v ON_ERROR_STOP=1)
  grep -qE '^[[:space:]]*--.*dbinit:no-transaction' "$p" || args+=(--single-transaction)
  if docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" "${args[@]}" < "$p" >/dev/null 2>/tmp/r67/replay.err; then
    ok=$((ok+1))
  else
    fail=$((fail+1))
    echo "$f :: $(grep -oiE '(ERROR|FATAL): .*' /tmp/r67/replay.err | head -1 | cut -c1-160)" >> /tmp/r67/replay-fail.txt
  fi
done < <(sed -n '/StartupFiles: \[\]string{/,/^\t}/p' \
           "$REPO_ROOT/installer/internal/dbinit/runner.go" \
         | grep -oE '"[0-9a-zA-Z_]+\.sql"' | tr -d '"')

echo "  第二遍：applied=$ok failed=$fail missing=$missing"
if [[ $fail -gt 0 ]]; then
  echo "  ── 第二遍失败的迁移:"
  cat /tmp/r67/replay-fail.txt | sed 's/^/      /'
fi
