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
#   C11 registry 裁定（A-17）：读 market_platform_registry + 拒绝族
#       platform_unmapped_region + 958/959 前置 fail-closed
#   C12 A-17 双地板写列（vendor 原生币 + overseas/domestic 地板 + a17_* 台账）
#   C13 币种列语义钉死（currency='CNY' 综合地板基准 + 币种冲突计数）
#   C14 源容器默认 acc-pg17（wa-pg17 退役根修）
#   C15 变异臂：改写 registry 读取 ⇒ C11 判据集合当场不再齐
#
# ★★ 2026-11-10 审计 P2-C 轮修（本门此前有**两个真缺陷**）：
#   ① 本门用 `python` 调解释器 ⇒ **本机/多数环境只有 python3** ⇒ C1b 恒红
#      （本机实测：C1b ❌「python 语法解析失败」⇒ 假红）。
#   ② C10 变异臂**假绿**：变异用的也是 `python` ⇒ 变异脚本**根本没执行**
#      （本机实测 rc=127）⇒ 变异后的文件与原文逐字相同 ⇒
#      门读到的是**未变异的**内容 ⇒ 「白名单检出」是自欺。
#      ⇒ 现在两处都用 python3，且 C10 额外断言「变异确实改动了文件」
#      （改前改后必须不同 ⇒ 否则变异臂自己报红，不许自己绿）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT}/scripts/market-price-sync.py"
pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ✅ %s\n' "$*"; }
bad()  { fail=$((fail+1)); printf '  ❌ %s\n' "$*"; }
skip() { printf '  ⚠️ %s\n' "$*"; }

BODY="$(cat "${SCRIPT}" 2>/dev/null || true)"

# C1 存在 + 语法
if [ -f "${SCRIPT}" ]; then
  ok "C1a 脚本存在"
else
  bad "C1a 脚本不存在: ${SCRIPT}"
fi
# ★ 缺 python3 时**跳过并显式说明**，不让整门因缺解释器而红
#   （仓内惯例：tests/deploy_211_contract_test.sh 同形态）。
if ! command -v python3 >/dev/null 2>&1; then
  skip "C1b/C10 跳过：本机无 python3（★ 缺解释器不算代码缺陷）"
elif python3 -c "import ast,sys; ast.parse(open(sys.argv[1],encoding='utf-8').read())" "${SCRIPT}" >/dev/null 2>&1; then
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
# ★ mktemp 模板必须以 X 结尾（macOS 与 GNU 都如此）⇒ "XXXXXX.py" 会失败
#   （实测：mkstemp failed ... File exists）⇒ 这里用 mktemp -d + 目录内文件。
MUT_DIR="$(mktemp -d)"
TMP="${MUT_DIR}/mps-mutant.py"
cp "${SCRIPT}" "${TMP}"
if ! command -v python3 >/dev/null 2>&1; then
  skip "C10a/C10b 跳过：本机无 python3（★ 缺解释器不算代码缺陷）"
  rm -f "${TMP}"; rm -rf "${MUT_DIR}"
else
python3 - "$TMP" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = "INSERT INTO public.market_price_reference (canonical_name, dimension, currency, canonical_id,"
new = "INSERT INTO public.model_credit_rates (canonical_name, dimension, currency, canonical_id,"
if old not in s:
    # 锚点不在 => 变异没施加 => 变异臂不得自绿（下面会查文件是否真的变了）
    sys.exit(3)
s = s.replace(old, new, 1)
open(p, 'w', encoding='utf-8', newline='').write(s)
PYEOF
MUT_RC=$?
# ★★ 前置断言：变异**必须真的改了文件**。
#   否则「检出」是自欺（读到的还是原文）—— 本门旧实现就栽在这里：
#   变异用 `python`（本机不存在）=> rc=127 => 文件逐字未变 => 门却报绿。
if [ "${MUT_RC}" != "0" ]; then
  bad "C10a 变异臂自身失效：变异脚本 rc=${MUT_RC}（解释器缺失或锚点失配）=> 变异未施加"
elif cmp -s "${SCRIPT}" "${TMP}"; then
  bad "C10a 变异臂自身失效：变异后文件与原文逐字相同 => 变异未生效"
else
  ok "C10a 变异确实施加（变异后文件与原文不同）"
  MUT_BODY="$(cat "${TMP}")"
  MUT_DIRTY=0
  while IFS= read -r t; do
    [ -z "$t" ] && continue
    if ! printf '%s' "$t" | grep -qE "^(${WHITE})$"; then
      MUT_DIRTY=1
    fi
  done <<< "${MUT_BODY}"
  if [ "${MUT_DIRTY}" = "1" ]; then
    ok "C10b 变异臂：upsert 目标改写为 model_credit_rates ⇒ 白名单门当场红（有牙）"
  else
    bad "C10b 变异臂未检出（门无牙）"
  fi
fi
fi
rm -f "${TMP}"
rm -rf "${MUT_DIR}"

# ── A-17 双币制契约（2026-11-12 收口轮；总体设计定稿 §3.6）────────────────
# C11 registry 裁定：读 market_platform_registry + 拒绝族 platform_unmapped_region
#     + 958/959 前置 fail-closed（缺迁移即 FATAL，不静默降级）
# C12 双地板列：vendor_list_currency / overseas_min_in_per_1m / domestic_min_in_per_1m
#     必须出现在 upsert 写列清单；台账带 a17_unmapped_rows
# C13 币种列语义钉死：currency 槽 = 字面量 'CNY'（综合地板折算基准；959 COMMENT
#     钉死的有意偏离 —— 厂商原生币在 vendor_list_currency 列）+ 币种冲突计数
# C14 源容器默认 acc-pg17（wa-pg17 已退役 ⇒ 11-11 夜跑 FATAL 的根修）
# C15 变异臂：把 registry 读取行改写 ⇒ C11 门必须当场红
if printf '%s' "${BODY}" | grep -q "FROM public.market_platform_registry" && \
   printf '%s' "${BODY}" | grep -q "platform_unmapped_region" && \
   printf '%s' "${BODY}" | grep -q "to_regclass('public.market_platform_registry')"; then
  ok "C11 registry 裁定（958/959 前置 fail-closed + 拒绝族 platform_unmapped_region）"
else
  bad "C11 registry 裁定缺失（A-17 原则 2）"
fi

if printf '%s' "${BODY}" | grep -q "vendor_list_currency" && \
   printf '%s' "${BODY}" | grep -q "overseas_min_in_per_1m" && \
   printf '%s' "${BODY}" | grep -q "domestic_min_in_per_1m" && \
   printf '%s' "${BODY}" | grep -q "a17_unmapped_rows"; then
  ok "C12 A-17 双地板写列（vendor 原生币 + overseas/domestic 地板 + 台账 a17_*）"
else
  bad "C12 A-17 双地板写列缺失"
fi

if printf '%s' "${BODY}" | grep -q "VALUES (%s, %s, 'CNY', %s," && \
   printf '%s' "${BODY}" | grep -q "a17_currency_mismatch_rows"; then
  ok "C13 币种列语义钉死（currency='CNY' 综合地板基准；原生币在 vendor_list_currency）"
else
  bad "C13 币种列语义漂移（currency 槽不再是 'CNY' 字面量或缺币种冲突计数）"
fi

if printf '%s' "${BODY}" | grep -q '"--source-container", default="acc-pg17"' && \
   ! printf '%s' "${BODY}" | grep -q 'default="wa-pg17"'; then
  ok "C14 源容器默认 acc-pg17（wa-pg17 退役根修，夜跑不再 FATAL）"
else
  bad "C14 源容器默认漂移（仍指 wa-pg17 或缺 acc-pg17 默认）"
fi

# C15 变异臂：改写 registry 读取与拒绝族 ⇒ C11 判据集合当场不再齐（门有牙）
MUT2_DIR="$(mktemp -d)"
TMP2="${MUT2_DIR}/mps-mutant2.py"
cp "${SCRIPT}" "${TMP2}"
if ! command -v python3 >/dev/null 2>&1; then
  skip "C15 跳过：本机无 python3"
  rm -f "${TMP2}"; rm -rf "${MUT2_DIR}"
else
python3 - "${TMP2}" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = '" FROM public.market_platform_registry")'
if old not in s:
    sys.exit(3)
s = s.replace(old, '" FROM public.nonexistent_table_xyz")')
s = s.replace("platform_unmapped_region", "platform_redacted_family")
open(p, 'w', encoding='utf-8', newline='').write(s)
PYEOF
MUT2_RC=$?
# ★ 变异必须真的改了文件（同 C10 教训：改前改后必须不同 ⇒ 否则自报红）。
if [ "${MUT2_RC}" != "0" ]; then
  bad "C15a 变异臂自身失效：变异脚本 rc=${MUT2_RC}（锚点失配）=> 变异未施加"
elif cmp -s "${SCRIPT}" "${TMP2}"; then
  bad "C15a 变异臂自身失效：变异后文件与原文逐字相同 => 变异未生效"
else
  ok "C15a 变异确实施加（registry 读取与拒绝族均被改写）"
  MUT2_BODY="$(cat "${TMP2}")"
  if printf '%s' "${MUT2_BODY}" | grep -q "FROM public.market_platform_registry" && \
     printf '%s' "${MUT2_BODY}" | grep -q "platform_unmapped_region"; then
    bad "C15b 变异臂未检出（门无牙）"
  else
    ok "C15b 变异臂：registry 读取/拒绝族被改写 ⇒ C11 判据集合当场不再齐（有牙）"
  fi
fi
fi
rm -f "${TMP2}"
rm -rf "${MUT2_DIR}"

printf '\n================ 汇总 ================\n'
printf '通过 %d / 失败 %d\n' "${pass}" "${fail}"
[ "${fail}" = "0" ]
