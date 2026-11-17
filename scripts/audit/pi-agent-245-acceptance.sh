#!/usr/bin/env bash
# =====================================================================
# scripts/audit/pi-agent-245-acceptance.sh
#
# PI 智能体在 **245 目标机活库**上的部署后验收（对象 / 语义 / 双账本 sha）。
#
# ★ 设计要点（本轮踩过的坑都固化在这里）：
#   ① **远端 SQL 输出 `KEY=VALUE` 机器可判行**，本地只做 `grep '^KEY=1'` 断言
#      ⇒ 不解析 psql 的表格输出（表格宽度 / 对齐 / schema 前缀都会让
#      「grep 某个名字」变成假阴性）。
#   ② **双账本指的是哪两本，必须写清**（本轮一开始也搞错过）：
#      · `public.schema_migrations`               —— 启动迁移的**版本台账**
#        （迁移自己 INSERT，含字母后缀版本）；
#      · `public.llm_gateway_migration_checksums` —— 启动迁移的**字节指纹台账**
#        （deploy 通道按文件内容 sha256 记账 ⇒ 「改字节即重放」就靠它）。
#      ⇒ `public.gateway_db_revision_sequences` 是**另一条通道**
#        （apply-db-revision-sequence.sh）的台账，键是 `<scope>:<version>__<file>`；
#        `public.repository_schema_migrations` 是 URSM/repository scope 的台账
#        ⇒ **三者不是同一本账的三份拷贝**，断言前必须先说清查的是哪本。
#   ③ **缺席型判据必须剥注释**：`prosrc` 与 `pg_get_functiondef()` 都保留 plpgsql 体的注释
#      ⇒ 判「函数体里没有 RETURNING」时，先 `regexp_replace(prosrc,'--[^\n]*','','g')`。
#
# 用法：
#   bash scripts/audit/pi-agent-245-acceptance.sh
#   SSH_HOST=245 REMOTE_ENV=/opt/llm-gateway-go/.env bash scripts/audit/pi-agent-245-acceptance.sh
#
# 退出码：0 = 全部断言通过；1 = 有断言失败。
# =====================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

SSH_HOST="${SSH_HOST:-245}"
REMOTE_ENV="${REMOTE_ENV:-/opt/llm-gateway-go/.env}"
MIGRATIONS=("971_pi_error_analysis_system.sql" "977_pi_error_analysis_semantics.sql")

# ── 本地算仓库文件 sha（字节一致口径）────────────────────────────────
declare -a LOCAL_SHA=()
for f in "${MIGRATIONS[@]}"; do
  path="$REPO_ROOT/sql/migrations/startup/$f"
  [[ -f "$path" ]] || { echo "FAIL [本地] 迁移文件缺失: sql/migrations/startup/$f" >&2; exit 1; }
  LOCAL_SHA+=("$(shasum -a 256 "$path" | awk '{print $1}')")
done

# ── 远端 SQL（输出 KEY=VALUE）─────────────────────────────────────────
SQL=$(cat <<'SQL'
\set ON_ERROR_STOP off
\pset pager off
\pset tuples_only on
\pset format unaligned
SELECT 'LEDGER_VERSION_971=' || (SELECT count(*) FROM public.schema_migrations WHERE version = '971');
SELECT 'LEDGER_VERSION_977=' || (SELECT count(*) FROM public.schema_migrations WHERE version = '977');
SELECT 'SHA_971=' || COALESCE((SELECT checksum FROM public.llm_gateway_migration_checksums WHERE version = '971'), 'ABSENT');
SELECT 'SHA_977=' || COALESCE((SELECT checksum FROM public.llm_gateway_migration_checksums WHERE version = '977'), 'ABSENT');
SELECT 'OBJ_TABLE_QUEUE=' || (to_regclass('public.error_analysis_queue') IS NOT NULL)::int;
SELECT 'OBJ_TABLE_RESULTS=' || (to_regclass('public.error_analysis_results') IS NOT NULL)::int;
SELECT 'OBJ_VIEW_STATS=' || (to_regclass('public.v_error_analysis_stats') IS NOT NULL)::int;
SELECT 'OBJ_FUNC_CLEANUP=' || (to_regprocedure('public.cleanup_old_error_analysis(integer)') IS NOT NULL)::int;
SELECT 'OBJ_FUNC_UPDATED_AT=' || (to_regprocedure('public.update_error_analysis_updated_at()') IS NOT NULL)::int;
SELECT 'OBJ_IDX_UNIQ=' || (SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND tablename='error_analysis_results' AND indexname='idx_error_analysis_results_queue_uniq' AND indexdef LIKE 'CREATE UNIQUE INDEX%');
SELECT 'OBJ_TRIGGER=' || (SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid WHERE c.relname='error_analysis_results' AND t.tgname='trigger_error_analysis_results_updated_at' AND NOT t.tgisinternal);
-- ★ 977 语义 F1：剥注释后判「函数体里没有 RETURNING」＋ 两条正形判据
SELECT 'SEM_F1_NO_RETURNING=' || (SELECT (position('RETURNING' in regexp_replace(p.prosrc, '--[^\n]*', '', 'g')) = 0)::int FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='cleanup_old_error_analysis');
SELECT 'SEM_F1_ROWCOUNT_RESULTS=' || (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='cleanup_old_error_analysis' AND pg_get_functiondef(p.oid) LIKE '%GET DIAGNOSTICS v_deleted_results = ROW_COUNT%');
SELECT 'SEM_F1_ROWCOUNT_QUEUE=' || (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='cleanup_old_error_analysis' AND pg_get_functiondef(p.oid) LIKE '%GET DIAGNOSTICS v_deleted_queue = ROW_COUNT%');
-- ★ 977 语义 F2：统计视图走 per_queue（**不是**旧的 JOIN 膨胀形态）
SELECT 'SEM_F2_PER_QUEUE=' || (SELECT (pg_get_viewdef('public.v_error_analysis_stats'::regclass, true) LIKE '%per_queue%')::int);
SELECT 'SEM_F2_DISTINCT_QUEUE=' || (SELECT (pg_get_viewdef('public.v_error_analysis_stats'::regclass, true) ILIKE '%count(DISTINCT %queue_id)%')::int);
-- ★ 977 语义 F3：queue_id 唯一索引
SELECT 'SEM_F3_UNIQUE_INDEX=' || (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='idx_error_analysis_results_queue_uniq' AND i.indisunique);
-- ★ 977 语义 F4：三条 L2 规则的 pattern 形态
SELECT 'SEM_F4_TOOL_ABUSE=' || (SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name='tool_abuse_exec' AND pattern LIKE '%subprocess%child_process%');
SELECT 'SEM_F4_RULE_ROWS=' || (SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name IN ('privilege_escalation','command_injection','tool_abuse_exec'));
-- ★ 978（本轮新增）：canonical 规则集在场（25 条）
SELECT 'RULE_SET_COUNT=' || (SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name IN ('instruction_override_ignore','instruction_override_new_rules','instruction_override_system_prompt','data_exfiltration_http','data_exfiltration_tool','resource_exhaustion_repeat','resource_exhaustion_long','social_engineering_urgency','social_engineering_authority','unicode_homoglyph','unicode_rtl_override','unicode_zero_width','tool_abuse_exec','tool_abuse_import','tool_abuse_file','multi_turn_setup','multi_turn_reference','payload_smuggling_base64','payload_smuggling_markdown','context_manipulation_role','context_manipulation_persona','role_hijack_ignore_previous','role_hijack_you_are_now','privilege_escalation','command_injection'));
SELECT 'RULE_SET_PRIV=' || (SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name='privilege_escalation' AND pattern LIKE '%\\badmin\\b%');
SELECT 'RULE_SET_CMD=' || (SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name='command_injection' AND pattern LIKE '%exec|eval%');
SQL
)
B64="$(printf '%s' "$SQL" | base64 | tr -d '\n')"

OUT="$(ssh -o BatchMode=yes -o ConnectTimeout=15 "$SSH_HOST" "set -euo pipefail
ENV='$REMOTE_ENV'
DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' \"\$ENV\" | cut -d= -f2-)
test -n \"\$DB\"
_psql() {
  if command -v psql >/dev/null 2>&1; then psql \"\$DB\" \"\$@\"
  else docker run --rm --network host postgres:17-alpine psql \"\$DB\" \"\$@\"; fi
}
echo '$B64' | base64 -d | _psql -X -q -t -v ON_ERROR_STOP=0
" 2>&1)"

echo "$OUT" | grep -E '^[A-Z0-9_]+=' || true
echo

FAIL=0
pass() { echo "PASS [$1] $2"; }
fail() { echo "FAIL [$1] $2"; FAIL=1; }

kv() { printf '%s\n' "$OUT" | grep -E "^$1=" | head -1 | cut -d= -f2-; }

expect() { # expect <KEY> <期望值> <说明>
  local got; got="$(kv "$1")"
  if [[ "$got" == "$2" ]]; then pass "$1" "$3（实际 ${got}）"; else fail "$1" "$3（期望 $2，实际 ${got:-<缺失>}）"; fi
}

# ── 双账本：版本台账 + 字节指纹台账 ───────────────────────────────────
expect LEDGER_VERSION_971 1 "971 在 schema_migrations 台账"
expect LEDGER_VERSION_977 1 "977 在 schema_migrations 台账"
if [[ "$(kv SHA_971)" == "${LOCAL_SHA[0]}" ]]; then
  pass SHA_971 "971 的 sha 与仓库文件字节一致"
else
  fail SHA_971 "971 的 sha 与仓库字节不一致（期望 ${LOCAL_SHA[0]}，实际 $(kv SHA_971)）"
fi
if [[ "$(kv SHA_977)" == "${LOCAL_SHA[1]}" ]]; then
  pass SHA_977 "977 的 sha 与仓库文件字节一致"
else
  fail SHA_977 "977 的 sha 与仓库字节不一致（期望 ${LOCAL_SHA[1]}，实际 $(kv SHA_977)）"
fi

# ── 对象在场 ─────────────────────────────────────────────────────────
expect OBJ_TABLE_QUEUE 1 "error_analysis_queue 表在场"
expect OBJ_TABLE_RESULTS 1 "error_analysis_results 表在场"
expect OBJ_VIEW_STATS 1 "v_error_analysis_stats 视图在场"
expect OBJ_FUNC_CLEANUP 1 "cleanup_old_error_analysis(integer) 在场"
expect OBJ_FUNC_UPDATED_AT 1 "update_error_analysis_updated_at() 在场"
expect OBJ_IDX_UNIQ 1 "idx_error_analysis_results_queue_uniq 在场且为 UNIQUE"
expect OBJ_TRIGGER 1 "trigger_error_analysis_results_updated_at 在场"

# ── 977 语义 ─────────────────────────────────────────────────────────
expect SEM_F1_NO_RETURNING 1 "977/F1：函数体（剥注释后）无 RETURNING"
expect SEM_F1_ROWCOUNT_RESULTS 1 "977/F1：结果侧用 GET DIAGNOSTICS … ROW_COUNT"
expect SEM_F1_ROWCOUNT_QUEUE 1 "977/F1：队列侧用 GET DIAGNOSTICS … ROW_COUNT"
expect SEM_F2_PER_QUEUE 1 "977/F2：统计视图走 per_queue 聚合"
expect SEM_F2_DISTINCT_QUEUE 1 "977/F2：统计视图用 count(DISTINCT … queue_id)"
expect SEM_F3_UNIQUE_INDEX 1 "977/F3：queue_id 唯一索引 indisunique"
expect SEM_F4_TOOL_ABUSE 1 "977/F4：tool_abuse_exec 是订正后的 pattern"

# ── 978（本轮新增）：canonical 规则集 ────────────────────────────────
expect RULE_SET_COUNT 25 "978：canonical 规则集 25 条在场"
expect RULE_SET_PRIV 1 "978：privilege_escalation 是订正后的 pattern"
expect RULE_SET_CMD 1 "978：command_injection 是订正后的 pattern"

echo
if (( FAIL == 0 )); then
  echo "=== 245 活库验收：全部断言通过 ==="
else
  echo "=== 245 活库验收：存在失败项 ===" >&2
fi
exit "$FAIL"
