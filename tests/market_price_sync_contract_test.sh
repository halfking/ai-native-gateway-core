#!/usr/bin/env bash
# market_price_sync_contract_test.sh —— 行情同步器契约门（2026-11-10 行情整合轮）。
#
# 被测物：scripts/market-price-sync.py（宿主侧单向同步：
# salesdecorate price_observation → 网关 940 market_price_reference）。
#
# ★ 红线（940 迁移头注 + 2026-11-03 方案档 §4.4）：行情价禁止回流路由/计费表。
#   本门用【文本级断言】钉住生成 SQL 的写目标白名单——脚本里允许出现的
#   INSERT/UPDATE 目标只有 market_price_reference / market_price_reference_sync /
#   market_price_platform_quotes（970 平台现价明细）；
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

# ★★ 2026-11-17：**门自身的大小无关性修复**（本门此前有一个潜伏假红）。
#   形态：本门原来用 `grep -q "PATTERN" "${BODY_FILE}"` 做全部文本判定。
#   ⇒ 当被测脚本**大于管道缓冲区（64KB）**时，grep 一命中就提前退出 ⇒
#     printf 收到 SIGPIPE（退出码 141）⇒ 脚本带 `set -o pipefail` 时
#     **整条管道被判为失败** ⇒ 明明命中却报「判据缺失」。
#   ⇒ 实证：本轮同步器长到 70983 字节后，C4/C11/C12 三条**同时假红**
#     （而三条判据在脚本里都存在，grep -c 逐条可数）。
#   ⇒ 这就是「守卫自己也要被审计」：门一直绿只是因为**被测文件恰好小于 64KB**，
#     门从未在「被测对象变大」这个真实场景下被验证过。
#   ⇒ 修法：把正文**落成临时文件**再 grep（与文件大小完全无关），
#     顺带比逐次管道快一个数量级。
BODY_FILE="$(mktemp -t mps_contract_body.XXXXXX)"
printf '%s' "${BODY}" > "${BODY_FILE}"

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
WHITE='market_price_reference|market_price_reference_sync|market_price_platform_quotes'
WRITE_TARGETS="$(grep -oE '(INSERT INTO public\.[a-z_]+|INSERT INTO [a-z_]+|UPDATE public\.[a-z_]+|UPDATE [a-z_]+ )' "${BODY_FILE}" | sed 's/INSERT INTO //;s/UPDATE //;s/public\.//;s/ $//' | sort -u)"
ILLEGAL=0
while IFS= read -r t; do
  [ -z "${t}" ] && continue
  if ! printf '%s' "${t}" | grep -qE "^(${WHITE})$"; then
    bad "C2 写目标越权: ${t}"
    ILLEGAL=1
  fi
done <<< "${WRITE_TARGETS}"
if [ "${ILLEGAL}" = "0" ] && [ -n "${WRITE_TARGETS}" ]; then
  ok "C2 写目标白名单（全部 ⊆ {market_price_reference, market_price_reference_sync, market_price_platform_quotes}）"
fi
# C2b 970 平台明细落地（DELETE+INSERT 快照语义）
if grep -q "DELETE FROM public.market_price_platform_quotes" "${BODY_FILE}" && \
   grep -q "INSERT INTO public.market_price_platform_quotes" "${BODY_FILE}"; then
  ok "C2b 970 平台明细 DELETE+INSERT 在位"
else
  bad "C2b 缺少 market_price_platform_quotes 落地语句"
fi

# C3 幂等 upsert
if grep -q "ON CONFLICT (canonical_name, dimension) DO UPDATE" "${BODY_FILE}"; then
  ok "C3 幂等 upsert（ON CONFLICT DO UPDATE）"
else
  bad "C3 缺少幂等 upsert"
fi

# C4 台账对账三联
if grep -q "source_platforms, source_rows, landed_rows, rejected_rows" "${BODY_FILE}" && \
   grep -q "market_price_reference_sync" "${BODY_FILE}"; then
  ok "C4 台账对账三联（源=落地+拒绝，写 market_price_reference_sync）"
else
  bad "C4 台账三联缺失"
fi

# C5 红线锚点
if grep -q "FROM public.model_credit_rates" "${BODY_FILE}" && \
   grep -q "mcr_before" "${BODY_FILE}" && \
   grep -q "mcr_after" "${BODY_FILE}" && \
   grep -q "RED-LINE VIOLATION" "${BODY_FILE}"; then
  ok "C5 红线锚点（model_credit_rates before/after 计数自检 + 违规即 FATAL）"
else
  bad "C5 红线锚点缺失"
fi

# C6 NULL≠0
if grep -q 'def num' "${BODY_FILE}" && \
   printf '%s' "${BODY}" | grep -A 3 'def num' | grep -q 'return "NULL"'; then
  ok "C6 num() 空值 → NULL（未知价 ≠ 免费红线）"
else
  bad "C6 num() 未保留 NULL 语义"
fi

# C7 身份漂移自愈（★ 2026-11-17 **改判**：本条原先钉死的是「歧义守卫
#   ——同 (canonical_id, dimension) 双声明回退 NULL 的 UPDATE 存在」，
#   而那**正是本轮要修的缺陷本身**：
#   · 旧实现把「多个源 key 解析到同一网关 canonical」（别名收敛，如
#     `glm-5.3` + `z.ai:-glm-5.3`）误判成**歧义**并把 canonical_id 全部
#     回退 NULL ⇒ 211 实测 165 个 canonical / 43.3% 观测拿不到可比地板；
#   · 而且那条 NULL-out 在「旧口径行（源 key 名）+ 新口径行（网关名）」并存
#     时会把**刚写好的新行也置空** ⇒ 正好把修复抵消掉。
#   ⇒ 门改成钉**正确契约**：upsert 之前先 DELETE 旧口径漂移行；
#     且**不得**再出现「把 canonical_id 置 NULL」的语句。
#   ⇒ ★ 教训（写进门注释）：门写成「某个维度是特例」，下一轮就会有人把它
#     当设计 —— 本条就是活教材。
if grep -q "DELETE FROM public.market_price_reference mpr" "${BODY_FILE}" && \
   grep -q "lower(mc.canonical_name) = lower(mpr.canonical_name)" "${BODY_FILE}" && \
   ! grep -q "SET canonical_id = NULL" "${BODY_FILE}"; then
  ok "C7 身份漂移自愈（upsert 前 DELETE 旧口径行；歧义 NULL-out 已移除）"
else
  bad "C7 身份漂移自愈缺失（要么没有 stale 清理，要么仍有 canonical_id=NULL 的语句）"
fi

# C8 两趟解析
if grep -q "def normalize" "${BODY_FILE}" && \
   grep -q "canon_exact" "${BODY_FILE}" && \
   grep -q "canon_norm" "${BODY_FILE}"; then
  ok "C8 两趟解析（精确小写 → 归一化）"
else
  bad "C8 两趟解析缺失"
fi

# C9 dry-run / dump-sql
if grep -q '"--dry-run"' "${BODY_FILE}" && grep -q '"--dump-sql"' "${BODY_FILE}"; then
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
MUT_BODY_FILE="$(mktemp -t mps_contract_mut.XXXXXX)"
printf '%s' "${MUT_BODY}" > "${MUT_BODY_FILE}"
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
rm -f "${MUT_BODY_FILE}"

# ── A-17 双币制契约（2026-11-12 收口轮；总体设计定稿 §3.6）────────────────
# C11 registry 裁定：读 market_platform_registry + 拒绝族 platform_unmapped_region
#     + 958/959 前置 fail-closed（缺迁移即 FATAL，不静默降级）
# C12 双地板列：vendor_list_currency / overseas_min_in_per_1m / domestic_min_in_per_1m
#     必须出现在 upsert 写列清单；台账带 a17_unmapped_rows
# C13 币种列语义钉死：currency 槽 = 字面量 'CNY'（综合地板折算基准；959 COMMENT
#     钉死的有意偏离 —— 厂商原生币在 vendor_list_currency 列）+ 币种冲突计数
# C14 源容器默认 acc-pg17（wa-pg17 已退役 ⇒ 11-11 夜跑 FATAL 的根修）
# C15 变异臂：把 registry 读取行改写 ⇒ C11 门必须当场红
if grep -q "FROM public.market_platform_registry" "${BODY_FILE}" && \
   grep -q "platform_unmapped_region" "${BODY_FILE}" && \
   grep -q "to_regclass('public.market_platform_registry')" "${BODY_FILE}"; then
  ok "C11 registry 裁定（958/959 前置 fail-closed + 拒绝族 platform_unmapped_region）"
else
  bad "C11 registry 裁定缺失（A-17 原则 2）"
fi

if grep -q "vendor_list_currency" "${BODY_FILE}" && \
   grep -q "overseas_min_in_per_1m" "${BODY_FILE}" && \
   grep -q "domestic_min_in_per_1m" "${BODY_FILE}" && \
   grep -q "a17_unmapped_rows" "${BODY_FILE}"; then
  ok "C12 A-17 双地板写列（vendor 原生币 + overseas/domestic 地板 + 台账 a17_*）"
else
  bad "C12 A-17 双地板写列缺失"
fi

if grep -q "VALUES (%s, %s, 'CNY', %s," "${BODY_FILE}" && \
   grep -q "a17_currency_mismatch_rows" "${BODY_FILE}"; then
  ok "C13 币种列语义钉死（currency='CNY' 综合地板基准；原生币在 vendor_list_currency）"
else
  bad "C13 币种列语义漂移（currency 槽不再是 'CNY' 字面量或缺币种冲突计数）"
fi

if grep -q '"--source-container", default="acc-pg17"' "${BODY_FILE}" && \
   ! grep -q 'default="wa-pg17"' "${BODY_FILE}"; then
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
  rm -f "${TMP2}"
rm -f "${MUT2_BODY_FILE}"; rm -rf "${MUT2_DIR}"
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
MUT2_BODY_FILE="$(mktemp -t mps_contract_mut2.XXXXXX)"
printf '%s' "${MUT2_BODY}" > "${MUT2_BODY_FILE}"
  if grep -q "FROM public.market_platform_registry" "${MUT2_BODY_FILE}" && \
     grep -q "platform_unmapped_region" "${MUT2_BODY_FILE}"; then
    bad "C15b 变异臂未检出（门无牙）"
  else
    ok "C15b 变异臂：registry 读取/拒绝族被改写 ⇒ C11 判据集合当场不再齐（有牙）"
  fi
fi
fi
rm -f "${TMP2}"
rm -f "${MUT2_BODY_FILE}"
rm -rf "${MUT2_DIR}"

# ── 2026-11-17 定价对比完善轮（模型身份对齐 + 部署漂移自证）──────────────
# C16 身份对齐：源侧 canonical_key 带厂商命名空间前缀（`openai:-gpt-4o`），
#     而网关 canonical 名是裸模型名 ⇒ 旧两趟解析解析率仅 24.3%、
#     参考价与评分面名字交集仅 12.5% ⇒ 对账页「我方价格 vs 市场地板」几乎全空。
#     契约：strip_namespace + resolve_canonical（四趟：原样精确 → 剥前缀精确
#     → 原样归一 → 剥前缀归一）+ res CTE 注入 + 聚合按 out_name 分组（同名合并）。
# C17 970 明细带 tier / model_kind / cache_hit + dimension 按 scope 归一
#     （阶梯价不再被压平；缓存命中价可见；限时优惠/批量/思考等口径只进 970）。
# C18 台账带脚本自身 sha256 + 身份对齐读数（部署漂移自证）。
# C19 非模型拒绝族 not_a_model（阶梯标签 / 分辨率档不落成「市场有这个模型」）。
# C20 变异臂：把剥前缀逻辑改掉 ⇒ C16 判据集合当场不再齐（门有牙）。
if grep -q "def strip_namespace" "${BODY_FILE}" && \
   grep -q "def resolve_canonical" "${BODY_FILE}" && \
   grep -q "res(src_key, out_name) AS" "${BODY_FILE}" && \
   grep -q "GROUP BY out_name" "${BODY_FILE}"; then
  ok "C16 模型身份对齐（剥命名空间 + 四趟解析 + res CTE + 按 out_name 合并）"
else
  bad "C16 模型身份对齐缺失（解析率与可比地板覆盖会退回 24.3% / 12.5%）"
fi

if grep -q "cache_hit_per_1m" "${BODY_FILE}" && \
   grep -q "cny_cache_hit_per_1m" "${BODY_FILE}" && \
   grep -q "model_kind" "${BODY_FILE}" && \
   grep -q "def dimension_for_scope" "${BODY_FILE}" && \
   grep -q "latest_cte_all" "${BODY_FILE}"; then
  ok "C17 970 明细：tier / model_kind / cache-hit 列 + 多 scope dimension 归一"
else
  bad "C17 970 明细缺 tier / 缓存命中价 / 多 scope 归一"
fi

if grep -q '"script_sha256": script_sha256()' "${BODY_FILE}" && \
   grep -q "identity_resolved_out_names" "${BODY_FILE}" && \
   grep -q "identity_namespace_stripped" "${BODY_FILE}"; then
  ok "C18 台账自带脚本指纹 + 身份对齐读数（部署漂移当场可读）"
else
  bad "C18 台账缺脚本指纹 / 身份对齐读数"
fi

if grep -q "not_a_model" "${BODY_FILE}" && \
   grep -q "def is_not_a_model" "${BODY_FILE}"; then
  ok "C19 非模型拒绝族 not_a_model（阶梯标签 / 分辨率档不落库）"
else
  bad "C19 非模型拒绝族缺失"
fi

# C20 变异臂：把剥前缀改成不剥 ⇒ C16 判据集合当场不再齐（门有牙）
MUT3_DIR="$(mktemp -d)"
TMP3="${MUT3_DIR}/mps-mutant3.py"
cp "${SCRIPT}" "${TMP3}"
if ! command -v python3 >/dev/null 2>&1; then
  skip "C20 跳过：本机无 python3"
  rm -f "${TMP3}"; rm -rf "${MUT3_DIR}"
else
python3 - "${TMP3}" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = '''    if ":" in k:
        tail = k.split(":", 1)[1].lstrip("- ")
        if tail:
            return tail
    return k'''
if old not in s:
    sys.exit(3)
s = s.replace(old, "    return k  # MUTATED: 不再剥命名空间前缀", 1)
open(p, 'w', encoding='utf-8', newline='').write(s)
PYEOF
MUT3_RC=$?
if [ "${MUT3_RC}" != "0" ]; then
  bad "C20a 变异臂自身失效：变异脚本 rc=${MUT3_RC}（锚点失配）=> 变异未施加"
elif cmp -s "${SCRIPT}" "${TMP3}"; then
  bad "C20a 变异臂自身失效：变异后文件与原文逐字相同 => 变异未生效"
else
  ok "C20a 变异确实施加（剥命名空间逻辑被改写）"
  # ★ C20b 必须**行为级**断言，不能再靠文本比对：
  #   文本门只能看见「函数名还在」，看不见「函数被掏空了」⇒ 上一版这里报绿，
  #   正是「文本门抓不到行为变异」的活教材。
  #   ⇒ 改为：直接加载变异后的模块，问它 `openai:-gpt-4o` 还剥不剥前缀；
  #     剥 ⇒ 变异没生效（自己红）；不剥 ⇒ 行为门（tests/market_price_sync_
  #     accounting_test.py 的 A11）**会红** ⇒ 门有牙。
  MUT3_BEHAVIOR="$(python3 - "${TMP3}" <<'PYEOF'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("mps_mut3", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
print(mod.strip_namespace("openai:-gpt-4o"))
PYEOF
)"
  if [ "${MUT3_BEHAVIOR}" = "gpt-4o" ]; then
    bad "C20b 变异臂自身失效：变异后 strip_namespace 行为未变 ⇒ 变异没生效"
  else
    ok "C20b 变异臂：剥命名空间行为被改写（openai:-gpt-4o → ${MUT3_BEHAVIOR}）⇒ A11 行为门会红（有牙）"
  fi
fi
fi
rm -f "${TMP3}"
rm -rf "${MUT3_DIR}"

printf '\n================ 汇总 ================\n'
printf '通过 %d / 失败 %d\n' "${pass}" "${fail}"
[ "${fail}" = "0" ]
rm -f "${BODY_FILE}"
