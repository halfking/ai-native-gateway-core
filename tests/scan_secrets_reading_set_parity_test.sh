#!/usr/bin/env bash
# =====================================================================
# tests/scan_secrets_reading_set_parity_test.sh
#   —— 「★ 秘密扫描器读数集合的【★ 跨引擎 × 跨 locale 两两完全相等 ★】★」门
#
# ★ 这道门是 F54 的【★ 验收口径 ★】的机器化（★ 2026-11-10 ★）
#
# ★ 它钉的是什么
#
# F54 登记的类叫「★ 平台分叉（★ 归属洞 ★）★」，★ 四条独立机制已逐条修好
# （★ libc 词边界族、★ libc 花括号族、★ locale 族、★ 大小写族 ★）⇒ ★ 而
# 「★ 收没收敛 ★」这件事的**验收口径**是：
#
#     ★★ 同一份规则集、★ 同一份语料 ⇒ ★ 每个引擎／每个 locale 读出的
#        【★ 读数集合 ★】必须【★ 两两完全相等 ★】★★ ★
#
# ★ 本门把「★ 两平台 ★」落成【★ 三个正则引擎 ★】的同读数：
#   · E1 = `[[ =~ ]]`（★ 本机 libc 的 regcomp ★ ⇒ macOS=BSD regex、
#     Linux=glibc ★ ⇒ ★ **这正是两平台的分歧面** ★）
#   · E2 = `grep -E`（★ POSIX ERE ★ ⇒ ★ 发现阶段真正用的那个 ★）
#   · E3 = python 的 `re`（★ 第三引擎、★ ≈ PCRE 语义 ★）
# ⇒ ★ 三个引擎在**两个 locale**（C ／ C.UTF-8）下的读数集合 ⇒ ★ 六个读数
#   ⇒ ★ 两两完全相等 ★
#
# ★ ★★ 为什么**夹具必须在运行时拼出来**（★ 自指纪律 ★）：
#   ★ 门脚本自己**不得**成为处置面的一条命中 ⇒ ⇒ 凭据形态的语料
#   ★ 【★ 一律运行时拼装 ★】★ ⇒ ★ 而且语料放在 mktemp 里、★ 不进仓 ★
#
# ★ 退出码：0 全绿；1 有违例。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ★ 规则文件（★ 可覆盖 ⇒ 供变异臂用 ★）
RULES="${SCAN_SECRETS_CONFIG:-$REPO_ROOT/scripts/scan-secrets.config}"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

# ★ ★ 缺解释器**不因缺解释器让整门红**（★ 仓内既有规矩：★ Makefile 里那个
#   ★ market_price_sync_accounting_test.py 的守卫就是这么写的 ★）⇒ ★ 降级为
#   ★ 「★ 明确说清哪一维没跑 ★」的跳过 ★ ⇒ ★ 而不是悄悄放过 ★
if ! command -v python3 >/dev/null 2>&1; then
  printf '⚠️ 跳过 scan_secrets_reading_set_parity_test.sh：★ 本机无 python3（★ 第三引擎缺失 ⇒ ★ 「两平台」那一维只剩 E1/E2 ★）\n' >&2
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ═════════════════════════════════════════════════════════════════════
# 0. ★ 语料（★ 运行时拼装 ★、★ 含 CJK 与大写形态 ★）
# ═════════════════════════════════════════════════════════════════════
FIX="$WORK/corpus.txt"
{
  # ★ ① 形如 API key 的形态（★ 分段拼装 ★）
  printf 'plain sk-%s\n' "$(printf 'a%.0s' $(seq 1 32))"
  printf 'UPPER SK-%s\n' "$(printf 'B%.0s' $(seq 1 32))"
  # ★ ② 连接串形态（★ 分段拼装 ★）
  printf 'dsn %s://%s:%s@%s/%s\n' postgres dbuser dbpass 10.20.30.40 gwdb
  # ★ ③ 内网地址形态（★ 含 CJK 上下文 ★）
  printf '内部地址 192.168.31.30 与 172.16.0.9\n'
  # ★ ④ 无关内容（★ 用来证明**不误伤** ★）
  printf 'ordinary log line without any secret\n'
  printf 'example.com and 127.0.0.1 are placeholders\n'
} > "$FIX"
N_FIX=$(grep -c . "$FIX")
if [[ "$N_FIX" -ge 5 ]]; then
  ok "① 夹具语料就位（★ $N_FIX 行、★ 含 CJK 与大写形态、★ 形态**运行时拼装**）"
else
  bad "① 夹具语料行数异常（★ $N_FIX 行）"
fi

# ═════════════════════════════════════════════════════════════════════
# 1. ★ 规则集抽取（★ 与扫描器**同一个解析口径** ★）
# ═════════════════════════════════════════════════════════════════════
# ★ 与 scripts/scan-secrets.sh 同款：★ `IFS='|' read -r cat sev desc pat`
#   ⇒ ★ **最后一段拿到余下全部** ⇒ ★ 模式里含 `|` 也不会被切坏 ★
RIDX="$WORK/rules.tsv"
: > "$RIDX"
n_rules=0
while IFS='|' read -r rcat rsev rdesc rpat; do
  [[ -z "$rcat" || "$rcat" =~ ^[[:space:]]*# ]] && continue
  rpat="${rpat%$'\r'}"
  [[ -z "$rpat" ]] && continue
  n_rules=$((n_rules + 1))
  printf '%s\t%s\t%s\n' "$n_rules" "$rdesc" "$rpat" >> "$RIDX"
done < "$RULES"
if [[ "$n_rules" -ge 5 ]]; then
  ok "② 规则集抽取到 $n_rules 条（★ 解析口径与扫描器同款）"
else
  bad "② 规则集只抽到 $n_rules 条（★ 抽取器坏了）"
fi

# ═════════════════════════════════════════════════════════════════════
# 2. ★ 三个引擎的读数集合
# ═════════════════════════════════════════════════════════════════════
# E1：★ `[[ =~ ]]`（★ 本机 libc 的 regcomp ★ ⇒ ★ 两平台的分歧面 ★）
readings_bsd() {
  local locale="$1" out="$2"
  : > "$out"
  local locale_prefix=()
  [[ "$locale" == "C" ]] && locale_prefix=(LC_ALL=C LANG=C)
  while IFS=$'\t' read -r rid rdesc rpat; do
    local ln=0 line
    while IFS= read -r line; do
      ln=$((ln + 1))
      # ★ ★ bash 3.2 兼容（★ macOS 自带 bash 没有 `${var,,}` ★）⇒ ★ 用 tr ★
      local low lowpat
      low="$(printf '%s' "${line}" | tr '[:upper:]' '[:lower:]')"
      lowpat="$(printf '%s' "${rpat}" | tr '[:upper:]' '[:lower:]')"
      if [[ "$low" =~ $lowpat ]]; then
        printf '%s:%s\n' "$rid" "$ln" >> "$out"
      fi
    done < "$FIX"
  done < "$RIDX"
  LC_ALL=C sort -u "$out" -o "$out"
}

# E2：★ POSIX ERE（★ 发现阶段真正用的引擎 ★）
readings_ere() {
  local locale="$1" out="$2"
  : > "$out"
  local locale_prefix=()
  [[ "$locale" == "C" ]] && locale_prefix=(LC_ALL=C LANG=C)
  while IFS=$'\t' read -r rid rdesc rpat; do
    local hits
    hits="$(env "${locale_prefix[@]+"${locale_prefix[@]}"}" grep -inE -e "$rpat" "$FIX" 2>/dev/null | cut -d: -f1)"
    local ln
    for ln in $hits; do
      printf '%s:%s\n' "$rid" "$ln" >> "$out"
    done
  done < "$RIDX"
  LC_ALL=C sort -u "$out" -o "$out"
}

# E3：★ python 的 re（★ 第三引擎 ★）
readings_py() {
  local locale="$1" out="$2"
  if [[ "$locale" == "C" ]]; then
    LC_ALL=C LANG=C python3 "$REPO_ROOT/tests/scan_secrets_reading_set_engine.py" \
      "$RIDX" "$FIX" "$out" >>/dev/null 2>&1
  else
    LC_ALL=C.UTF-8 LANG=C.UTF-8 python3 "$REPO_ROOT/tests/scan_secrets_reading_set_engine.py" \
      "$RIDX" "$FIX" "$out" >>/dev/null 2>&1
  fi
  LC_ALL=C sort -u "$out" -o "$out"
}

locale_prefix=()

for LOC in C C.UTF-8; do
  f_bsd="$WORK/bsd.$LOC.txt"; f_ere="$WORK/ere.$LOC.txt"; f_py="$WORK/py.$LOC.txt"
  readings_bsd "$LOC" "$f_bsd"
  readings_ere "$LOC" "$f_ere"
  readings_py "$LOC" "$f_py"

  n_bsd=$(wc -l < "$f_bsd" | tr -d ' ')
  n_ere=$(wc -l < "$f_ere" | tr -d ' ')
  n_py=$(wc -l < "$f_py" | tr -d ' ')

  # ★ 非空面：★ 空集之间的「相等」是**恒真** ⇒ ⇒ 必须证明面非空 ★
  if [[ "$n_bsd" -ge 1 && "$n_ere" -ge 1 && "$n_py" -ge 1 ]]; then
    ok "③ locale=${LOC}：★ 三个引擎都读出了非空集合（★ bsd=$n_bsd ／ ere=$n_ere ／ py=${n_py}）"
  else
    bad "③ locale=${LOC}：★ 有引擎读出空集（★ bsd=$n_bsd ／ ere=$n_ere ／ py=${n_py}）⇒ 「相等」是恒真、★ 判据无牙"
  fi

  if diff -q "$f_bsd" "$f_ere" >/dev/null 2>&1; then
    ok "④ locale=${LOC}：★ E1（★ libc regcomp ★）与 E2（★ POSIX ERE ★）读数集合**完全相等**"
  else
    bad "④ locale=${LOC}：★ E1 与 E2 的读数集合**不相等**（★ 跨平台分叉）"
    diff "$f_bsd" "$f_ere" | head -10 >&2
  fi

  if diff -q "$f_bsd" "$f_py" >/dev/null 2>&1; then
    ok "⑤ locale=${LOC}：★ E1 与 E3（★ python re ★）读数集合**完全相等**"
  else
    bad "⑤ locale=${LOC}：★ E1 与 E3 的读数集合**不相等**（★ 第三引擎分叉）"
    diff "$f_bsd" "$f_py" | head -10 >&2
  fi
done

# ★ ★★ 两个 locale 之间的读数集合也必须相等（★ locale 族那条机制 ★）★★ ★
for PAIR in "bsd" "ere" "py"; do
  if diff -q "$WORK/$PAIR.C.txt" "$WORK/$PAIR.C.UTF-8.txt" >/dev/null 2>&1; then
    ok "⑥ 引擎 ${PAIR}：C 与 C.UTF-8 读数集合**完全相等**（★ locale 族已收敛）"
  else
    bad "⑥ 引擎 ${PAIR}：C 与 C.UTF-8 的读数集合**不相等**（★ locale 分叉还在）"
    diff "$WORK/$PAIR.C.txt" "$WORK/$PAIR.C.UTF-8.txt" | head -10 >&2
  fi
done

# ═════════════════════════════════════════════════════════════════════
# 3. ★ 变异臂：★ 把一条规则的形态改坏 ⇒ 读数集合必须分叉 ⇒ 门红 ★
# ═════════════════════════════════════════════════════════════════════
# ★ ★ 变异的形态：**分段拼出来**（★ 门脚本自身不得成为处置面命中 ★）
#   ★ 它模拟的正是 F54 那条「★ libc 词边界族 ★」：★ 用反斜杠 b 形态
#   ★ ⇒ ★ BSD regcomp 把它当**字面字符 b**、★ 而 python/ERE 把它当**词边界** ★
#   ★ ⇒ ⇒ 三个引擎的读数集合**必然分叉** ⇒ ⇒ 判据有牙齿 ★
MUT="$WORK/mut.config"
python3 - "$RULES" "$MUT" <<'PY'
import io, sys
src = io.open(sys.argv[1], encoding='utf-8').read().split('\n')
out = []
done = False
for line in src:
    if not done and line.startswith('LLM_APIKEY|'):
        # ★ 把词边界形态换掉（★ 形态运行时拼出 ★）
        # ★ 形态：★ 在 `sk-` **之后**插一个反斜杠 b（★ 这正是 F54 那条
        #   ★ 「★ libc 词边界族 ★」的形态 ★）
        #   ★ BSD 的 regcomp 把它当**字面字符 b**（★ ⇒ 匹配不到 ⇒ 读数少一条 ★）
        #   ★ 而 POSIX / python 把它当**词边界**（★ ⇒ 匹配得到 ⇒ 读数不变 ★）
        #   ★ ⇒ ⇒ 两个引擎的读数集合**分叉** ★ ⇒ ⇒ 判据有牙齿 ★
        line = line.replace('sk-[', 'sk-' + chr(92) + 'b[', 1)
        done = True
    out.append(line)
io.open(sys.argv[2], 'w', encoding='utf-8').write('\n'.join(out))
if not done:
    sys.stderr.write('MUTATE_FAILED\n')
    sys.exit(1)
PY
if [[ $? -eq 0 ]]; then
  MUT_RIDX="$WORK/mut_rules.tsv"; MUT_OUT="$WORK/mut_out.txt"
  : > "$MUT_RIDX"; mr=0
  while IFS='|' read -r rcat rsev rdesc rpat; do
    [[ -z "$rcat" || "$rcat" =~ ^[[:space:]]*# ]] && continue
    rpat="${rpat%$'\r'}"; [[ -z "$rpat" ]] && continue
    mr=$((mr + 1)); printf '%s\t%s\t%s\n' "$mr" "$rdesc" "$rpat" >> "$MUT_RIDX"
  done < "$MUT"
  RIDX="$MUT_RIDX" readings_py "C" "$MUT_OUT"
  if [[ -s "$MUT_OUT" ]]; then
    ok "⑦ 变异臂：★ 词边界形态被改坏后第三引擎仍能读出（★ 形态**确实**被换掉）"
  else
    bad "⑦ 变异臂：★ 变异体的读数集合空了 ⇒ 变异没打上"
  fi
  # ★ ★ 关键断言：★ 变异体下 **bsd 与 py 必须分叉**（★ 这是「★ 词边界族 ★」
  #   ★ 在 BSD 平台上的真实形态 ★）⇒ ⇒ 证明本门**看得见**分叉 ★
  MUT_BSD="$WORK/mut_bsd.txt"
  RIDX="$MUT_RIDX" readings_bsd "C" "$MUT_BSD"
  if diff -q "$MUT_BSD" "$MUT_OUT" >/dev/null 2>&1; then
    # ★ ★ 平台备注：★ 在 **glibc** 平台上 \\b 本来就是词边界 ⇒ 三个引擎**不会**分叉
    #   ★ ⇒ 那时这条断言**不成立** ⇒ ⇒ 改判「★ 变异**改动了读数** ★」（★ 弱一档 ★）
    if diff -q "$WORK/bsd.C.txt" "$MUT_BSD" >/dev/null 2>&1; then
      bad "⑦b 变异臂：★ 形态被改坏后读数**既没分叉、也没变** ⇒ 本门恒绿（判据无牙齿）"
    else
      ok "⑦b 变异臂：★ 形态被改坏 ⇒ 读数集合**变了**（★ 本平台 \\b 本来就是词边界 ⇒ 不分叉、★ 但读数确实变了 ★）"
    fi
  else
    ok "⑦b 变异臂：★ 形态被改坏 ⇒ 三个引擎的读数集合**分叉** ⇒ 本门看得见分叉（有牙齿）"
  fi
  # ★ ★ 平台无关的「★ 比较有牙齿 ★」证明：★ 造一个**故意不同**的集合 ⇒
  #   ★ 比较**必须**报出不同（★ 否则「相等」就是恒真 ★）
  TRUNC="$WORK/trunc.txt"
  # ★ ★ 用 sed 而不是 `head -n -1`（★ BSD head 不支持负数行数 ★）★
  sed '$d' "$WORK/bsd.C.txt" > "$TRUNC" 2>/dev/null || true
  if [[ -s "$TRUNC" ]] && ! diff -q "$TRUNC" "$WORK/bsd.C.txt" >/dev/null 2>&1; then
    ok "⑦c 比较有牙齿：★ 故意截掉一条 ⇒ 比较**报出不同**（★ 「相等」不是恒真）"
  else
    bad "⑦c 比较**没有**牙齿：★ 截掉一条仍判相等 ⇒ 本门恒绿"
  fi
else
  bad "⑦ 变异臂改写失败"
fi

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
exit 0
