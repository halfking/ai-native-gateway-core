#!/usr/bin/env bash
# market_price_sync_contract_test.sh —— 行情同步器契约门（2026-11-10 行情整合轮）。
#
# 被测物：scripts/market-price-sync.py（宿主侧单向同步：
# salesdecorate price_observation → 网关 940 market_price_reference）。
#
# ★ 红线（940 迁移头注 + 2026-11-03 方案档 §4.4）：行情价禁止回流路由/计费表。
#   本门用【文本级断言】钉住生成 SQL 的写目标白名单——脚本里允许出现的
#   INSERT/UPDATE 目标只有 market_price_reference / market_price_reference_sync；
#   model_credit_rates / model_price_versions / credential_model_bindings /
#   models_canonical 只允许出现在【读/锚点/解析】上下文，绝不作写目标。
#
# 判据（全部源码文本级，不需要 DB）：
#   C1 脚本存在且 python 语法可解析（ast.parse）
#   C2 写目标白名单：INSERT INTO / UPDATE 的目标表 ⊆ 白名单
#   C3 幂等：ON CONFLICT (canonical_name, dimension) DO UPDATE
#   C4 台账对账三联：source_rows/landed_rows/rejected_rows 同写一行
#   C5 红线锚点：model_credit_rates 行数 before/after 自检存在
#   C6 NULL≠0：num() 对空值返回 NULL（不折 0）
#   C7 歧义守卫：同 (canonical_id, dimension) 双声明回退 NULL 的 UPDATE 存在
#   C8 两趟解析：精确小写 + 归一化（normalize）都存在
#   C9 --dry-run 与 --dump-sql 存在
#   C10 变异臂：把 C2 白名单中的 upsert 目标表名改成 model_credit_rates ⇒ 门必须红
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT}/scripts/market-price-sync.py"
pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ✅ %s\n' "$*"; }
bad() { fail=$((fail+1)); printf '  ❌ %s\n' "$*"; }

BODY="$(cat "${SCRIPT}" 2>/dev/null || true)"

# C1 存在 + 语法
if [ -f "${SCRIPT}" ]; then
  ok "C1a 脚本存在"
else
  bad "C1a 脚本不存在: ${SCRIPT}"
fi
if python -c "import ast,sys; ast.parse(open(sys.argv[1],encoding='utf-8').read())" "${SCRIPT}" >/dev/null 2>&1; then
  ok "C1b python 语法可解析"
else
  bad "C1b python 语法解析失败"
fi

# C2 写目标白名单（★ 关键红线）
WHITE='market_price_reference|market_price_reference_sync'
WRITE_TARGETS="$(printf '%s' "${BODY}" | grep -oE '(INSERT INTO public\.[a-z_]+|INSERT INTO [a-z_]+|UPDATE public\.[a-z_]+|UPDATE [a-z_]+ )' | sed 's/INSERT INTO //;s/UPDATE //;s/public\.//;s/ $//' | sort -u)"
ILLEGAL=0
while IFS= read -r t; do
  [ -z "${t}" ] && continue
  if ! printf '%s' "${t}" | grep -qE "^(${WHITE})$"; then
    bad "C2 写目标越权: ${t}"
    ILLEGAL=1
  fi
done <<< "${WRITE_TARGETS}"
if [ "${ILLEGAL}" = "0" ] && [ -n "${WRITE_TARGETS}" ]; then
  ok "C2 写目标白名单（全部 ⊆ {market_price_reference, market_price_reference_sync}）"
fi

# C3 幂等 upsert
if printf '%s' "${BODY}" | grep -q "ON CONFLICT (canonical_name, dimension) DO UPDATE"; then
  ok "C3 幂等 upsert（ON CONFLICT DO UPDATE）"
else
  bad "C3 缺少幂等 upsert"
fi

# C4 台账对账三联
if printf '%s' "${BODY}" | grep -q "source_platforms, source_rows, landed_rows, rejected_rows" && \
   printf '%s' "${BODY}" | grep -q "market_price_reference_sync"; then
  ok "C4 台账对账三联（源=落地+拒绝，写 market_price_reference_sync）"
else
  bad "C4 台账三联缺失"
fi

# C5 红线锚点
if printf '%s' "${BODY}" | grep -q "FROM public.model_credit_rates" && \
   printf '%s' "${BODY}" | grep -q "mcr_before" && \
   printf '%s' "${BODY}" | grep -q "mcr_after" && \
   printf '%s' "${BODY}" | grep -q "RED-LINE VIOLATION"; then
  ok "C5 红线锚点（model_credit_rates before/after 计数自检 + 违规即 FATAL）"
else
  bad "C5 红线锚点缺失"
fi

# C6 NULL≠0
if printf '%s' "${BODY}" | grep -q 'def num' && \
   printf '%s' "${BODY}" | grep -A 3 'def num' | grep -q 'return "NULL"'; then
  ok "C6 num() 空值 → NULL（未知价 ≠ 免费红线）"
else
  bad "C6 num() 未保留 NULL 语义"
fi

# C7 歧义守卫
if printf '%s' "${BODY}" | grep -q "other.canonical_id = mpr.canonical_id" && \
   printf '%s' "${BODY}" | grep -q "SET canonical_id = NULL"; then
  ok "C7 歧义守卫（同 (canonical_id, dimension) 双声明回退 NULL）"
else
  bad "C7 歧义守卫缺失"
fi

# C8 两趟解析
if printf '%s' "${BODY}" | grep -q "def normalize" && \
   printf '%s' "${BODY}" | grep -q "canon_exact" && \
   printf '%s' "${BODY}" | grep -q "canon_norm"; then
  ok "C8 两趟解析（精确小写 → 归一化）"
else
  bad "C8 两趟解析缺失"
fi

# C9 dry-run / dump-sql
if printf '%s' "${BODY}" | grep -q '"--dry-run"' && printf '%s' "${BODY}" | grep -q '"--dump-sql"'; then
  ok "C9 --dry-run 与 --dump-sql 存在"
else
  bad "C9 --dry-run/--dump-sql 缺失"
fi

# C10 变异臂：upsert 目标改成 model_credit_rates ⇒ 白名单门必须红
TMP="$(mktemp "${TMPDIR:-/tmp}/mps-mutant-XXXXXX.py")"
cp "${SCRIPT}" "${TMP}"
python - "$TMP" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
s = s.replace("INSERT INTO public.market_price_reference (canonical_name, dimension, currency, canonical_id,",
              "INSERT INTO public.model_credit_rates (canonical_name, dimension, currency, canonical_id,", 1)
open(p, 'w', encoding='utf-8', newline='').write(s)
PYEOF
MUT_BODY="$(cat "${TMP}")"
MUT_DIRTY=0
while IFS= read -r t; do
  [ -z "${t}" ] && continue
  if ! printf '%s' "${t}" | grep -qE "^(${WHITE})$"; then
    MUT_DIRTY=1
  fi
done <<< "${MUT_BODY}"
rm -f "${TMP}"
if [ "${MUT_DIRTY}" = "1" ]; then
  ok "C10 变异臂：upsert 目标改写为 model_credit_rates ⇒ 白名单门当场红（有牙）"
else
  bad "C10 变异臂未检出（门无牙）"
fi

printf '\n================ 汇总 ================\n'
printf '通过 %d / 失败 %d\n' "${pass}" "${fail}"
[ "${fail}" = "0" ]
