#!/bin/bash
# ========================================
# 修订历史 / Revision History
# ========================================
# | 版本 | 日期       | 变更                            | 作者   |
# |------|------------|---------------------------------|--------|
# | 1.0  | 2026-08-18 | 初始版本（本地↔252 索引对齐）  | Infra  |
# ========================================
#
# index-drift-align.sh — 修复 2026-08-18 审计发现的双向索引漂移
#
# 漂移清单（来源：pg_indexes 全量对比，见 runbook §3）：
#   A) 仅 252 有、本地缺（--direction=to-local 补齐）：
#      - request_logs_2026_07/08 的 quality_flags GIN（部分索引）
#      - request_logs_2026_07/08 的 tool_calls GIN（部分索引）
#      ⚠️ 这两个分区在两端均为 columnar；Citus columnar 对 GIN 支持受限，
#         创建失败（unsupported access method）按预期降级为 WARN，不算失败。
#   B) 仅本地有、252 缺（--direction=to-252 补齐）：
#      - handoff_logs_hot 的 (created_at, id) btree
#      - handoff_logs_hot 的 (new_session_id, created_at DESC) 部分索引
#
# 全部语句幂等（CREATE INDEX IF NOT EXISTS）。
#
# 用法：
#   ./scripts/partition/index-drift-align.sh --direction=to-local   # 在 local 执行
#   ./scripts/partition/index-drift-align.sh --direction=to-252     # 在 252 执行
#   ./scripts/partition/index-drift-align.sh --direction=to-local --dry-run

set -euo pipefail

DIRECTION=""
DRY_RUN=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --direction=*) DIRECTION="${1#--direction=}" ;;
    --dry-run)     DRY_RUN=true ;;
    -h|--help)     sed -n '3,25p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

[[ "$DIRECTION" =~ ^(to-local|to-252)$ ]] || { echo "--direction= 必填（to-local|to-252）" >&2; exit 1; }

if [[ "$DIRECTION" == "to-local" ]]; then
  ENV=local
  run_sql_file() { docker exec -i llm-gateway-pg psql -X -U llm_gateway -d llm_gateway "$@"; }
else
  ENV=252
  # shellcheck disable=SC1091
  source "$HOME/workspace/ai-native-tools/envs/loader.sh" --all --project llm-gateway-go --server 115.29.212.252 --mode plain >/dev/null 2>&1 || {
    echo "envs loader 失败（252 凭据）" >&2; exit 1; }
  run_sql_file() { PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -X -h 127.0.0.1 -p 15432 -U "$COMMON_PG_SUPERUSER" -d llm_gateway "$@"; }
fi

TO_LOCAL_SQL=$(cat <<'SQL'
-- 仅 252 有、本地缺（2026-08-18 审计漂移项 A）
CREATE INDEX IF NOT EXISTS request_logs_2026_07_quality_flags_idx
  ON public.request_logs_2026_07 USING gin (quality_flags)
  WHERE (cardinality(quality_flags) > 0);
CREATE INDEX IF NOT EXISTS request_logs_2026_07_tool_calls_idx
  ON public.request_logs_2026_07 USING gin (tool_calls)
  WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));
CREATE INDEX IF NOT EXISTS request_logs_2026_08_quality_flags_idx
  ON public.request_logs_2026_08 USING gin (quality_flags)
  WHERE (cardinality(quality_flags) > 0);
CREATE INDEX IF NOT EXISTS request_logs_2026_08_tool_calls_idx
  ON public.request_logs_2026_08 USING gin (tool_calls)
  WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));
SQL
)

TO_252_SQL=$(cat <<'SQL'
-- 仅本地有、252 缺（2026-08-18 审计漂移项 B；目标为 heap 小表，直接建）
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_created_at
  ON public.handoff_logs_hot USING btree (created_at, id);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_hot_new_session
  ON public.handoff_logs_hot USING btree (new_session_id, created_at DESC)
  WHERE (new_session_id IS NOT NULL);
SQL
)

if [[ "$DRY_RUN" == true ]]; then
  echo "== DRY-RUN direction=${DIRECTION}（未执行）"
  [[ "$DIRECTION" == "to-local" ]] && printf '%s\n' "$TO_LOCAL_SQL" || printf '%s\n' "$TO_252_SQL"
  exit 0
fi

echo "== 执行 direction=$DIRECTION env=$ENV =="
if [[ "$DIRECTION" == "to-local" ]]; then SQL="$TO_LOCAL_SQL"; else SQL="$TO_252_SQL"; fi

OUT=$(printf '%s\n' "$SQL" | run_sql_file 2>&1) || true
echo "$OUT" | grep -vE '^CREATE INDEX$|^$' || true

# 预期内的能力限制：columnar 分区上的 GIN（与 sync 脚本 Q13 同口径）
if echo "$OUT" | grep -q "unsupported access method"; then
  echo "⚠️ columnar 分区 GIN 创建被跳过（Citus 能力限制，预期内）"
fi
if echo "$OUT" | grep -qiE "ERROR" ; then
  if ! echo "$OUT" | grep -q "unsupported access method"; then
    echo "✗ 存在非预期错误，请检查上方输出" >&2; exit 1
  fi
fi

echo "== 校验 =="
if [[ "$DIRECTION" == "to-local" ]]; then
  run_sql_file -Atc "SELECT indexname FROM pg_indexes WHERE schemaname='public'
    AND indexname IN ('request_logs_2026_07_quality_flags_idx','request_logs_2026_07_tool_calls_idx',
                      'request_logs_2026_08_quality_flags_idx','request_logs_2026_08_tool_calls_idx') ORDER BY 1;"
else
  run_sql_file -Atc "SELECT indexname FROM pg_indexes WHERE schemaname='public'
    AND indexname IN ('idx_handoff_logs_hot_created_at','idx_handoff_logs_hot_new_session') ORDER BY 1;"
fi
echo "完成（上面列出的即已存在/新建成功的索引）"
