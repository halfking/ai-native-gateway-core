#!/usr/bin/env bash
# =====================================================================
# tests/scan_secrets_regex_determinism_test.sh
#   —— 「★ 秘密扫描器的【★ 正则口径确定性 ★】★」门
#
# ★ 它钉的是什么（★ 三条不变量 ★）：
#
#   ① 【★ 词边界必须可移植 ★】：规则文件里【★ 不得出现 `\b` / `\<` / `\>` ★】
#      ⇒ ★ 纯字符扫描、★ 不依赖任何正则引擎 ⇒ ★ 跨平台一致 ⇒ ★ 在 macOS 上也有牙 ★
#      ⇒ ★ 起因：★ `\b` 在 BSD libc（macOS）的 regcomp 里【不是词边界】、
#        ★ 而是字面字符 `b` ★ ⇒ ★ INTERNAL_IP 三条规则在 macOS 上【★ 整族失效、恒 0 命中 ★】
#        ★ 而 glibc 上它是词边界 ⇒ ★ 同一棵树两平台读数差 1145 条、★ 100% 落这一族 ★。
#      ⇒ ★ 顺带把 `\<` / `\>` 也禁掉：★ 它们在 glibc 上可用、★ 在 macOS 上同样不可用 ★
#        ⇒ ★ 不是可移植替代品 ★（★ 实测 ★）。
#
#   ② 【★ 发现阶段与归因阶段必须同口径 ★】：★ 归因必须大小写不敏感 ★
#      ⇒ ★ 起因：★ 发现阶段用 `grep -niE`（★ 不区分大小写 ★）、
#        ★ 归因阶段用 `[[ =~ ]]`（★ 区分大小写 ★）⇒ ★ 口径不一致 ★
#        ⇒ ★ 大写形态的凭据【★ 候选命中却被静默丢弃 ★】⇒ ★ BLOCK 级规则存在系统性假阴性 ★
#        ★ （★ 实测：★ `PASSWORD=` / `API_KEY=` / `SECRET=` 三种大写写法全部 0 命中 ★）。
#
#   ③ 【★ 词边界的语义不得被"过度修法"抹掉 ★】：★ 粘连形态必须【★ 不命中 ★】
#      ⇒ ★ 防止有人把 `\b` 直接删掉（★ 那会让【★ 粘连形态的 IP ★】也命中 ★ ⇒ 读数虚高 ★）。
#
# ★ ★ 为什么 ① 的静态断言不可省（★ 本门存在的理由 ★）：
#   只留行为断言的话、★ 【★ macOS 上这条门永远绿 ★】——
#   因为【★ 修好之后 macOS 与 glibc 的读数本来就该一致 ★】⇒ ★ 行为断言在 macOS 上
#   没有区分力 ⇒ ★ 必须在【★ 静态层 ★】把"不可移植写法"直接禁掉 ★。
#
# ★ ★ ★ ★★ 一条【★ 自指 ★】纪律（★ 2026-11-09 第十三轮 ★）：★★ ★★
#   ★ 本门自己【★ 不得成为处置面的一条命中 ★】——★★
#   ★ 探针用的 IP 形态与 API-key 形态【★ 一律在运行时拼出来 ★】、
#   ★ ★ ★ 【★ 本文件里不出现任何【★ 完整的 ★】四段数字或 key 形态 ★】★★★ ★
#   ★ ⇒ ★ 否则本门【★ 每加一条探针、★ 就给仓库自己的扫描面加一条命中 ★】
#   ★ ⇒ ★ 而这正是本档 §8.18 记下的那条教训（★ 审计档的引用形态 = 给处置面加命中 ★）★★
#
# ★ 修法与依据：★ 见 docs/12小时内修订审计-20261104-0329.md §8.21
#
# ★ ★ 本门自己也要守仓内既有的纪律 ★ ★：
#   · 「★ $变量 紧跟非 ASCII 字节 ★」⇒ ★ 在 UTF-8 locale 下会被 bash
#     解析成更长的变量名 ⇒ ★ 未绑定变量错误 ★ ⇒ ★ 本门一律用 ${var} 界定 ★
#     （★ 这条由 scripts/guards/check-shell-fullwidth-var.sh 强制 ★）。
#   · 「★ $变量 紧跟 [A-Za-z0-9_] ★」同样会被吞进变量名 ★ ⇒ ★ 一律 ${var} ★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCAN="${1:-$REPO_ROOT/scripts/scan-secrets.sh}"
CONFIG="${2:-$REPO_ROOT/scripts/scan-secrets.config}"

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

cd "$REPO_ROOT" || { echo "cannot cd to repo root" >&2; exit 1; }

[[ -f "$SCAN" ]]   || { echo "missing ${SCAN}" >&2; exit 1; }
[[ -f "$CONFIG" ]] || { echo "missing ${CONFIG}" >&2; exit 1; }

# ── 扫描器需要 bash ≥ 4（★ 它自带的守卫会拒绝 bash 3.2 ★）────────────
BASH4="$HOME/sdk/bash5/bin/bash"
[[ -x "$BASH4" ]] || BASH4="$(command -v bash)"
if [[ "${BASH_VERSINFO[0]}" -lt 4 && "$BASH4" == "$(command -v bash)" ]]; then
  echo "SKIP: 本机没有 bash ≥ 4（★ 扫描器自带的守卫会拒绝 bash 3.2 ★）" >&2
  exit 0
fi

# ── ★★ 探针串在运行时拼出（★ 本文件里不出现完整的命中形态 ★）★★
# ★ 用 printf 分段拼：★ 文件里只留下【★ 空格分隔的数字 ★】⇒ 任何一行都不构成四段数字 ★
IP_A="$(printf '%s.%s.%s.%s' 10 1 2 3)"       # ★ 四段 IPv4、★ 首段两位、★ 末段一位 ★
IP_B="$(printf '%s.%s.%s.%s' 110 1 2 3)"      # ★ 首段【★ 三位 ★】的形态 ★
IP_C="$(printf '%s.%s.%s.%s' 10 1 2 345)"     # ★ 末段【★ 三位 ★】的形态 ★
IP_D="$(printf '%s.%s.%s.%s' 192 168 1 10)"   # ★ 首段【★ 三位 ★】、★ 第二段【★ 三位 ★】的形态 ★

# ── 取规则模式（★ 只取【★ 规则行 ★】的第 4 段、★ 跳过注释与空行 ★）──
# ★ 为什么跳过注释：★ 规则文件头部【★ 刻意写着 ★】`\b` 这个反例词 ★
#   ⇒ 若连注释一起扫、★ 门就会对自己的说明文字判红 ★ ⇒ ★ 门被关掉 ⇒ 真缺陷没人管 ★。
rule_patterns=()
while IFS='|' read -r rcat rsev rdesc rpat; do
  [[ -z "${rcat:-}" ]] && continue
  [[ "${rcat}" == \#* ]] && continue
  [[ -z "${rpat:-}" ]] && continue
  rule_patterns+=("${rpat}")
done < "$CONFIG"

if [[ ${#rule_patterns[@]} -eq 0 ]]; then
  echo "FAIL: 未从 ${CONFIG} 取到任何规则模式" >&2
  exit 1
fi

# ── 断言 ①：★ 规则模式里不得有【★ 不可移植的词边界 ★】────────────────
# ★ 纯字符扫描（★ 不查任何正则引擎 ★）⇒ ★ 跨平台一致 ★
nonportable_hit=""
for pat in "${rule_patterns[@]}"; do
  case "${pat}" in
    *'\b'*|*'\<'*|*'\>'*)
      nonportable_hit="${pat}"
      break
      ;;
  esac
done
if [[ -n "${nonportable_hit}" ]]; then
  bad "规则模式含【★ 不可移植的词边界 ★】（★ \\b / \\< / \\> ★）：[${nonportable_hit}]" \
      "⇒ ★ BSD libc（macOS）的 regcomp 不认它 ⇒ ★ 该规则在 macOS 上【★ 整族失效 ★】"
else
  ok "规则 ${#rule_patterns[@]} 条、★ 无不可移植词边界（★ 跨平台静态断言 ✔ ★）"
fi

# ── 断言 ②：★ INTERNAL_IP 三条规则必须用【★ 可移植形态 ★】────────────
ip_rules=0
ip_portable=0
for pat in "${rule_patterns[@]}"; do
  case "${pat}" in
    *10\\.*|*172\\.*|*192\\.*)
      ip_rules=$((ip_rules + 1))
      case "${pat}" in
        '(^|[^A-Za-z0-9_])'*) ip_portable=$((ip_portable + 1)) ;;
      esac
      ;;
  esac
done
if [[ ${ip_rules} -eq 0 ]]; then
  bad "规则文件里【★ 一条 INTERNAL_IP 规则都没识别到 ★】⇒ ★ 断言 ② 恒绿 ⇒ ★ 门没有区分力"
elif [[ ${ip_rules} -eq ${ip_portable} ]]; then
  ok "INTERNAL_IP ${ip_rules} 条规则【★ 全部使用可移植词边界形态 ★】"
else
  bad "INTERNAL_IP ${ip_rules} 条规则里只有 ${ip_portable} 条用了可移植形态 ⇒ ★ 仍有规则在 macOS 上失效"
fi

# ── 断言 ③：★ 可移植形态的【★ 语义表 ★】（★ 平台无关的期望值 ★）──────
# ★ 期望值取自【★ glibc 的 `\b` 语义 ★】⇒ ★ 这张表就是"修法不改变 CI 侧语义"的证明 ★
# ★ ★ 字段分隔符用 `;;` 而不是 `|`：★ 模式本身含 `|`（`(^|[^…])`）★
#   ★ ⇒ 用 `|` 分隔会把模式截断、★ 而门【★ 照样跑完并报红 ★】⇒ ★ 判据自身失真 ★。
# ★ 每行格式：<标签>;;<期望 0=不命中 1=命中> ★（★ 待测串由上面的零件拼出 ★）
IP_PAT='(^|[^A-Za-z0-9_])10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}([^A-Za-z0-9_]|$)'
sem_specs=(
  '行首IP;;1'
  '行尾IP;;1'
  'IP后接逗号;;1'
  '字母粘连;;0'
  '数字粘连;;0'
  '下划线粘连;;0'
  'IP后接字母;;0'
  '四段全数字;;1'
)
sem_subjects=(
  "${IP_A}"
  "text ${IP_A}"
  "ip=${IP_A},"
  "x${IP_A}"
  "${IP_B}"
  "_${IP_A}"
  "${IP_A}abc"
  "${IP_C}"
)
sem_bad=0
sem_idx=0
for spec in "${sem_specs[@]}"; do
  lbl="${spec%%;;*}"
  expect="${spec##*;;}"
  subject="${sem_subjects[$sem_idx]}"
  sem_idx=$((sem_idx + 1))
  if [[ "${subject}" =~ ${IP_PAT} ]]; then got=1; else got=0; fi
  if [[ "${got}" -ne "${expect}" ]]; then
    bad "词边界语义不符：${lbl}（★ 期望 ${expect}、★ 实得 ${got} ★）"
    sem_bad=$((sem_bad + 1))
  fi
done
if [[ ${sem_bad} -eq 0 ]]; then
  ok "可移植词边界语义表 ${#sem_specs[@]} 行【★ 全部符合 glibc 的 \\b 语义 ★】"
fi

# ── 断言 ④⑤：★ 行为断言（★ 用合成夹具跑真实扫描器 ★）──────────────────
# ★ ④ 归因阶段必须大小写不敏感（大写形态必须被报出来）
# ★ ⑤ 粘连形态必须不被报出来（防止"把词边界删掉"这种过度修法）
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/scripts"
cp "$SCAN" "$TMP/scripts/scan-secrets.sh"
cp "$CONFIG" "$TMP/scripts/scan-secrets.config"

# ★ 夹具里的【★ 值 ★】单独放一行、★ 不与【★ 键 ★】同处一行
#   ⇒ ★ 本文件自身【★ 不构成一条命中 ★】★★
CRED_VAL='Aa1b2c3d4e5'
printf 'PASSWORD = "%s"\n' "${CRED_VAL}" > "$TMP/fx_upper.txt"
printf 'password = "%s"\n' "${CRED_VAL}" > "$TMP/fx_lower.txt"
# ★ 大写形态的 key：★ 形态由零件拼出、★ 本文件里不出现完整字面量 ★
printf '%s%s-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' "S" "K" > "$TMP/fx_keyupper.txt"
printf 'host %s is internal\n' "${IP_D}" > "$TMP/fx_ip.txt"
printf 'host x%s glued\n' "${IP_D}" > "$TMP/fx_ipglue.txt"

OUT="$TMP/out.txt"
"$BASH4" "$TMP/scripts/scan-secrets.sh" --mode=strict --format=text \
  --repo-root="$TMP" --paths="$TMP" > "$OUT" 2>&1 || true

# ④-1：大写 CRED_ASSIGN 必须被报出来（★ 旧实现漏报 ★）
if grep -q "fx_upper.txt:1  CRED_ASSIGN" "$OUT"; then
  ok "★ 大写形态的凭据赋值【★ 被报出来 ★】（★ 归因阶段大小写不敏感 ✔ ★）"
else
  bad "★ 大写形态的凭据赋值【★ 没有被报出来 ★】⇒ ★ 归因阶段仍区分大小写 ⇒ ★ 假阴性仍在 ★"
fi

# ④-2：小写形态仍必须被报出来（★ 防"改成全不匹配"的假修法 ★）
if grep -q "fx_lower.txt:1  CRED_ASSIGN" "$OUT"; then
  ok "小写形态的凭据赋值仍被报出来（★ 未把判定放宽成全不匹配 ★）"
else
  bad "小写形态的凭据赋值也没被报出来 ⇒ ★ 归因判定坏了 ★"
fi

# ④-3：大写形态的 API key 形态必须被报出来（★ BLOCK 级假阴性 ★）
if grep -q "fx_keyupper.txt:1  LLM_APIKEY" "$OUT"; then
  ok "★ 大写形态的 API key【★ 被报出来 ★】（★ BLOCK 级假阴性已修 ✔ ★）"
else
  bad "★ 大写形态的 API key【★ 没有被报出来 ★】⇒ ★ BLOCK 级假阴性仍在 ★"
fi

# ⑤-1：正常 IP 必须被报出来（★ macOS 上原本恒 0 ★）
if grep -q "fx_ip.txt:1  INTERNAL_IP" "$OUT"; then
  ok "正常形态的内网 IP【★ 被报出来 ★】（★ 词边界修法在当前平台生效 ✔ ★）"
else
  bad "正常形态的内网 IP【★ 没有被报出来 ★】⇒ ★ 该规则在本平台仍失效 ★"
fi

# ⑤-2：粘连形态必须【★ 不被 ★】报出来（★ 防过度修法 ★）
if grep -q "fx_ipglue.txt" "$OUT"; then
  bad "★ 粘连形态的内网 IP【★ 被报出来了 ★】⇒ ★ 词边界被抹平了 ⇒ ★ 读数虚高 ★"
else
  ok "粘连形态的内网 IP 未被报出来（★ 词边界语义保住了 ✔ ★）"
fi

# ── 断言 ⑥：★ 读数必须【★ 自带口径标签 ★】────────────────────────────
# ★ 起因：★ 同一棵树、★ 同一扫描面、★ macOS 180 条 / glibc 1325 条 ⇒ ★ 读数不可比 ★
#   ⇒ ★ 所以摘要里必须印出【★ locale ＋ 引擎 ★】⇒ ★ 每一次读数自带口径 ★
if grep -qE "^   Locale: " "$OUT"; then
  ok "★ 扫描摘要【★ 印出了 locale 口径 ★】（★ 读数自带标签 ✔ ★）"
else
  bad "★ 扫描摘要没有印出 locale 口径 ⇒ ★ 跨平台读数不可自证 ★"
fi
if grep -qE "^   Engine: " "$OUT"; then
  ok "★ 扫描摘要【★ 印出了引擎版本 ★】"
else
  bad "★ 扫描摘要没有印出引擎版本 ⇒ ★ 读数不可自证 ★"
fi

# ── 零断言守卫：★ 夹具一个都没被报出来 ⇒ 门必须红 ★────────────────────
reported=0
for fx in fx_upper.txt fx_lower.txt fx_keyupper.txt fx_ip.txt; do
  grep -q "$fx" "$OUT" && reported=$((reported + 1))
done
if [[ ${reported} -eq 0 ]]; then
  bad "★ 四个夹具一个都没被报出来 ⇒ ★ 本门在本次运行上【★ 零断言、★ 没有区分力 ★】"
else
  ok "夹具命中 ${reported}/4（★ 门有区分力 ✔ ★）"
fi

printf 'PASS=%s FAIL=%s\n' "${PASS}" "${FAIL}"
[[ ${FAIL} -eq 0 ]] || exit 1
exit 0
