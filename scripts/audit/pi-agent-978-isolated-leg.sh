#!/usr/bin/env bash
# =====================================================================
# scripts/audit/pi-agent-978-isolated-leg.sh
#
# 迁移 978（pi_canonical_rule_set）在**隔离 PG 真库**上的真跑腿。
#
# 覆盖三种形态（每种都是**真跑**，不是读代码推断）：
#   ① **空表形态**（= 全新安装：01-schema 基线建出空表）⇒ 补种后必须是 25 条；
#   ② **幂等**：同一库跑第二遍 ⇒ 行数不变、内容不变；
#   ③ **存量形态**（= 245：364 的 21 条 + ensureRulesTable 的 2 条 = 23 条）
#      ⇒ 补种后必须是 25 条，且**既有行不被覆盖**（尤其 tool_abuse_exec 的
#      pattern 必须保持 977 订正后的值 —— 证明「978 不会把 977 的订正倒退回去」）。
#
# 用法：
#   bash scripts/audit/pi-agent-978-isolated-leg.sh
#   PG_HOST=127.0.0.1 PG_PORT=15433 PG_USER=postgres bash scripts/audit/pi-agent-978-isolated-leg.sh
#
# 退出码：0 = 全绿；1 = 有断言失败。
# =====================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

PG_HOST="${PG_HOST:-127.0.0.1}"
PG_PORT="${PG_PORT:-15433}"
PG_USER="${PG_USER:-postgres}"
PG_SUPER_DB="${PG_SUPER_DB:-postgres}"
DB_NAME="${DB_NAME:-pi978_leg_$$}"
# ★ MIG_FILE / SEED364_FILE 可被环境覆盖 ⇒ 允许把本脚本**整体**放进
#   PG 容器里跑（宿主机 psql 未必能连到容器的端口映射时）。
MIG="${MIG_FILE:-$REPO_ROOT/sql/migrations/startup/978_pi_canonical_rule_set.sql}"
SEED364="${SEED364_FILE:-$REPO_ROOT/sql/migrations/startup/364_prompt_injection_enhanced.sql}"

PSQL=(psql -X -q -v ON_ERROR_STOP=1 -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER")

FAIL=0
pass() { echo "PASS [$1] $2"; }
fail() { echo "FAIL [$1] $2"; FAIL=1; }

cleanup() {
  "${PSQL[@]}" -d "$PG_SUPER_DB" -c "DROP DATABASE IF EXISTS \"$DB_NAME\";" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== 0. 准备隔离库 $DB_NAME =="
"${PSQL[@]}" -d "$PG_SUPER_DB" -c "DROP DATABASE IF EXISTS \"$DB_NAME\";" >/dev/null 2>&1 || true
"${PSQL[@]}" -d "$PG_SUPER_DB" -c "CREATE DATABASE \"$DB_NAME\";" >/dev/null || {
  echo "FAIL [setup] 建库失败" >&2; exit 1;
}
pass setup "隔离库 $DB_NAME 已建"

# ① 空表形态：复刻 01-schema 基线的 prompt_injection_rules（同样的列 + 同样的唯一键）
#
# ★ category_new 在基线里是**枚举列**（public.injection_category）⇒ 夹具必须
#   把枚举本身建出来（否则 364 的种子行插不进枚举值 ⇒ 夹具自己先炸）。
#   ★ 枚举值取自基线的**全部 15 个**（sql/schema/01-schema.sql 第 66 行起）。
"${PSQL[@]}" -d "$DB_NAME" >/dev/null <<'SQL'
CREATE TYPE public.injection_category AS ENUM (
    'role_hijack','instruction_override','instruction_leak','jailbreak',
    'encoding_bypass','injection_marker','multi_turn_attack','resource_exhaustion',
    'data_exfiltration','social_engineering','prompt_leaking','payload_smuggling',
    'unicode_obfuscation','context_manipulation','tool_abuse'
);
CREATE TABLE public.prompt_injection_rules (
    id integer NOT NULL,
    rule_name character varying(100) NOT NULL,
    rule_type character varying(50) NOT NULL,
    category character varying(50) NOT NULL,
    pattern text NOT NULL,
    description text,
    severity integer NOT NULL,
    enabled boolean DEFAULT true,
    case_sensitive boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    category_new public.injection_category,
    action_override text,
    is_system boolean DEFAULT true,
    tags text[],
    examples text[],
    false_positive_rate double precision DEFAULT 0,
    CONSTRAINT prompt_injection_rules_severity_check CHECK (((severity >= 1) AND (severity <= 10)))
);
CREATE SEQUENCE public.prompt_injection_rules_id_seq;
ALTER SEQUENCE public.prompt_injection_rules_id_seq OWNED BY public.prompt_injection_rules.id;
ALTER TABLE ONLY public.prompt_injection_rules ALTER COLUMN id SET DEFAULT nextval('public.prompt_injection_rules_id_seq'::regclass);
ALTER TABLE ONLY public.prompt_injection_rules ADD CONSTRAINT prompt_injection_rules_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.prompt_injection_rules ADD CONSTRAINT prompt_injection_rules_rule_name_key UNIQUE (rule_name);
CREATE TABLE public.schema_migrations (version text PRIMARY KEY, description text);
SQL
pass setup "基线形态的规则表已建（空表）"

apply_mig() {
  "${PSQL[@]}" -d "$DB_NAME" -f "$MIG" >/dev/null 2>&1
}

# ── ① 空表形态 ───────────────────────────────────────────────────────
if apply_mig; then
  pass "978/空表" "首次执行 RC=0"
else
  fail "978/空表" "首次执行失败"
fi
n=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules")
[[ "$n" == "25" ]] && pass "978/空表" "补种后 25 条（实际 $n）" || fail "978/空表" "补种后应为 25 条，实际 $n"
for r in privilege_escalation command_injection role_hijack_ignore_previous tool_abuse_exec; do
  c=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules WHERE rule_name='$r'")
  [[ "$c" == "1" ]] && pass "978/空表" "规则 $r 在场" || fail "978/空表" "规则 $r 缺失"
done
led=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.schema_migrations WHERE version='978'")
[[ "$led" == "1" ]] && pass "978/台账" "schema_migrations 有 978 行" || fail "978/台账" "978 台账行缺失"

# ── ② 幂等 ───────────────────────────────────────────────────────────
before=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT md5(string_agg(rule_name||'|'||pattern, ',' ORDER BY rule_name)) FROM public.prompt_injection_rules")
if apply_mig; then pass "978/幂等" "第二遍 RC=0"; else fail "978/幂等" "第二遍失败"; fi
after=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT md5(string_agg(rule_name||'|'||pattern, ',' ORDER BY rule_name)) FROM public.prompt_injection_rules")
[[ "$before" == "$after" ]] && pass "978/幂等" "第二遍内容不变" || fail "978/幂等" "第二遍内容变了"
n2=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules")
[[ "$n2" == "25" ]] && pass "978/幂等" "第二遍仍是 25 条" || fail "978/幂等" "第二遍行数 $n2"

# ── ③ 存量形态（245：364 的 21 条 + ensureRulesTable 的 2 条 = 23）─────
"${PSQL[@]}" -d "$DB_NAME" -c "DELETE FROM public.prompt_injection_rules;" >/dev/null
# 364 的 21 条：直接从种子文件里取 VALUES 段（**零转录误差**）
# ★ 注意：364 的最后一条规则行**结尾没有逗号**（它是 364 自己 VALUES 列表的
#   末项）⇒ 拼装时**不能**给每行都补 `;`（会得到 `(...),;` ⇒ 语法错）。
#   ⇒ 这里拼成**一条** INSERT 语句（21 行共用一个 VALUES），末行去掉尾逗号。
{
  echo "INSERT INTO public.prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, is_system, tags) VALUES"
  sed -n '/^INSERT INTO prompt_injection_rules/,/^ON CONFLICT (rule_name) DO NOTHING;/p' "$SEED364" \
    | grep -E "^\('" \
    | sed 's/,$//' \
    | awk '{ if (NR>1) printf ",\n"; printf "%s", $0 } END { printf ";\n" }'
} > /tmp/pi978_seed364.sql
if ! "${PSQL[@]}" -d "$DB_NAME" -f /tmp/pi978_seed364.sql >/dev/null 2>&1; then
  echo "FAIL [978/存量] 364 种子行灌入失败（拼装文件：/tmp/pi978_seed364.sql）" >&2
  head -3 /tmp/pi978_seed364.sql >&2
fi
"${PSQL[@]}" -d "$DB_NAME" -c "INSERT INTO public.prompt_injection_rules (rule_name, rule_type, category, pattern, description, severity) VALUES
('role_hijack_ignore_previous', 'basic', 'role_hijack', '(?i)(ignore|forget|disregard).*(previous|above|prior).*(instruction|prompt|rule)', '尝试让模型忽略之前的指令', 9),
('role_hijack_you_are_now', 'basic', 'role_hijack', '(?i)you are now (a|an) .*(admin|root|system|god mode|developer)', '尝试切换模型角色为特权用户', 10);" >/dev/null
n23=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules")
[[ "$n23" == "23" ]] && pass "978/存量" "存量形态 23 条就位" || fail "978/存量" "存量形态应为 23 条，实际 $n23"

# 模拟 977 的效果：tool_abuse_exec 的 pattern 已是订正后的值
"${PSQL[@]}" -d "$DB_NAME" -c "UPDATE public.prompt_injection_rules SET pattern = '(?i)\b(exec|eval)\s*\(|\b(system|shell)\(|os\s*\.\s*system\s*\(|\bshell_exec\s*\(|\b(subprocess|child_process)\b\s*[.(]|\b(call|invoke|execute|run|spawn)\s+(the\s+)?(subprocess|child_process)\b' WHERE rule_name='tool_abuse_exec';" >/dev/null
before23=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT md5(string_agg(rule_name||'|'||pattern, ',' ORDER BY rule_name)) FROM public.prompt_injection_rules")

if apply_mig; then pass "978/存量" "在 23 条形态上执行 RC=0"; else fail "978/存量" "在 23 条形态上执行失败"; fi
n25=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules")
[[ "$n25" == "25" ]] && pass "978/存量" "补种后 25 条（实际 $n25）" || fail "978/存量" "补种后应为 25 条，实际 $n25"

# ★ 关键：既有 23 条**不被覆盖**（否则 978 会把 977 的订正倒退回去）
kept=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT count(*) FROM public.prompt_injection_rules r WHERE r.rule_name IN ('instruction_override_ignore','data_exfiltration_http','unicode_zero_width','tool_abuse_exec','role_hijack_ignore_previous','role_hijack_you_are_now') AND r.pattern = (SELECT s.pattern FROM public.prompt_injection_rules s WHERE s.rule_name = r.rule_name)")
after23=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT md5(string_agg(rule_name||'|'||pattern, ',' ORDER BY rule_name)) FROM public.prompt_injection_rules")
# 校验：978 只应新增 2 条 ⇒ 既有行的 pattern 指纹必须保持（新增会改变整体 md5，
#        所以这里逐条比 tool_abuse_exec 的 pattern 值）
toolpat=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT pattern FROM public.prompt_injection_rules WHERE rule_name='tool_abuse_exec'")
case "$toolpat" in
  *subprocess*child_process*) pass "978/存量" "tool_abuse_exec 保持 977 订正后的 pattern（未被 978 覆盖）" ;;
  *) fail "978/存量" "tool_abuse_exec 的 pattern 被改动：$toolpat" ;;
esac
privpat=$("${PSQL[@]}" -d "$DB_NAME" -tAc "SELECT pattern FROM public.prompt_injection_rules WHERE rule_name='privilege_escalation'")
case "$privpat" in
  *'\\badmin\\b'*|*'\badmin\b'*) pass "978/存量" "privilege_escalation 用的是订正后的 pattern（词界形态）" ;;
  *) fail "978/存量" "privilege_escalation 的 pattern 不是订正后的形态：$privpat" ;;
esac

echo
if (( FAIL == 0 )); then
  echo "=== 978 隔离 PG 真跑腿：全部断言通过 ==="
else
  echo "=== 978 隔离 PG 真跑腿：存在失败项 ===" >&2
fi
exit "$FAIL"
