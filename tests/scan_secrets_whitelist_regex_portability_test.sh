#!/usr/bin/env bash
# =====================================================================
# tests/scan_secrets_whitelist_regex_portability_test.sh
#   —— 「★ 秘密扫描器白名单的【★ 正则可移植性 ★】★」门
#
# ★ 它钉的是什么：
#   scripts/scan-secrets.sh 把 WHITELIST_PATTERNS【★ 拼成单条 alternation ★】
#   （scan-secrets.sh:548-555）⇒ 然后用 `[[ "$content" =~ $wl_combined ]]`
#   做整行白名单豁免判定。
#
# ★ 而 `[[ =~ ]]` 走的是【★ 本机 libc 的 regcomp ★】⇒ macOS（BSD regex）
#   与 Linux（glibc/musl）对【★ 同一条模式 ★】的【★ 合法性判断并不一致 ★】。
#
# ★ 本门钉住的不变量：
#   ① 【★ 静态、跨平台 ★】：白名单里【★ 不得出现未转义的花括号 ★】
#      （★ POSIX ERE 里 `{`/`}` 只有「区间」一种含义、★ 要当字面量必须转义 ★）
#      ⇒ ★ 这条【★ 与 libc 无关 ★】⇒ ★ 在 macOS 上也能守住 ✔
#   ② 【★ 运行时、当前平台 ★】：把白名单拼成 alternation 后、
#      `[[ =~ ]]` 的返回码【★ 不得是 2（正则编译错误）★】
#      ⇒ ★ 在 Linux 上能抓到【★ 任何 ★】未来的编译错误 ★
#
# ★ 为什么 ① 不可省（★ 这是本门存在的理由 ★）：
#   只留 ② 的话、★ 【★ macOS 上这条门永远绿 ★】——★ 因为 BSD regex 接受
#   那些 glibc 会拒绝的写法 ⇒ ★ 于是「★ 在 BSD 上碰巧合法 ★」的写法就能
#   一路溜到 CI 上、★ 而 CI 侧【★ 整条白名单静默失效 ★】（★ 后果是
#   ★ 白名单该豁免的行一行都豁免不掉 ⇒ ★ 读数虚高、★ 处置清单被污染 ★）★。
#
# ★ 修法与依据：★ 见 docs/12小时内修订审计-20261104-0329.md §8.20
#
# ★ ★ 本门自己也要守仓内既有的纪律 ★ ★：
#   · 「★ $变量 紧跟非 ASCII 字节 ★」⇒ ★ 在 UTF-8 locale 下会被 bash
#     解析成更长的变量名 ⇒ ★ 未绑定变量错误 ★ ⇒ ★ 本门一律用 ${var} 界定 ★
#     （★ 这条由 scripts/guards/check-shell-fullwidth-var.sh 强制 ★）
#   · 「★ $变量 紧跟 [A-Za-z0-9_] ★」同样会被吞进变量名 ★
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCANNER="$REPO_ROOT/scripts/scan-secrets.sh"

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

cd "$REPO_ROOT" || { echo "cannot cd to repo root" >&2; exit 1; }

[[ -f "$SCANNER" ]] || { echo "missing ${SCANNER}" >&2; exit 1; }

# ── 取白名单数组（★ 整块 eval、★ 含声明行与收尾行 ★）──────────────────
eval "$(sed -n '/^WHITELIST_PATTERNS=(/,/^)/p' "${SCANNER}")"

if [[ -z "${WHITELIST_PATTERNS[@]:-}" ]]; then
  echo "FAIL: 未能从 ${SCANNER} 取到 WHITELIST_PATTERNS" >&2
  exit 1
fi

# ── 断言 ①：★ 白名单里不得有【★ 未转义的花括号 ★】────────────────────
# ★ 纯字符扫描、★ 不依赖任何正则引擎 ⇒ ★ 跨平台一致 ★
# ★ 结果写进全局 BRACE_HIT（★ 不用命令替换、★ 免得子 shell 吞掉错误 ★）
BRACE_HIT=""
check_unescaped_brace() {
  local s="$1"
  local i=0
  local n
  local c
  n=${#s}
  while [[ ${i} -lt ${n} ]]; do
    c="${s:i:1}"
    if [[ "${c}" == "\\" ]]; then
      i=$((i + 2))
      continue
    fi
    if [[ "${c}" == "{" || "${c}" == "}" ]]; then
      BRACE_HIT="${s}"
      return 0
    fi
    i=$((i + 1))
  done
  BRACE_HIT=""
  return 1
}

brace_bad=0
for w in "${WHITELIST_PATTERNS[@]}"; do
  [[ -z "${w}" ]] && continue
  if check_unescaped_brace "${w}"; then
    bad "白名单模式含【★ 未转义的花括号 ★】：[${BRACE_HIT}]"
    brace_bad=$((brace_bad + 1))
  fi
done
if [[ ${brace_bad} -eq 0 ]]; then
  ok "白名单 ${#WHITELIST_PATTERNS[@]} 词、★ 无未转义花括号（★ 跨平台静态断言 ✔ ★）"
fi

# ── 断言 ②：★ 拼成 alternation 后、★ 当前平台的 regcomp 不得报错 ──────
wl_combined=""
for w in "${WHITELIST_PATTERNS[@]}"; do
  [[ -z "${w}" ]] && continue
  if [[ -n "${wl_combined}" ]]; then
    wl_combined="${wl_combined}|${w}"
  else
    wl_combined="${w}"
  fi
done

if [[ -n "${wl_combined}" ]]; then
  [[ "__probe__" =~ ${wl_combined} ]]
  rc=$?
  if [[ ${rc} -eq 2 ]]; then
    bad "白名单 alternation 在【★ 本平台 ★】编译失败（★ rc=2 ★）⇒ ★ 白名单整体失效 ⇒ ★ 该平台的读数虚高"
  else
    ok "白名单 alternation 在本平台编译成功（★ rc=${rc}、★ 与 libc 版本一致时结论有效 ★）"
  fi
else
  bad "白名单 alternation 为空"
fi

# ── 断言 ③：★ 白名单必须仍能真正豁免（★ 行为断言、★ 与平台无关 ★）───
# ★ 挑一个【★ 只可能被白名单豁免掉 ★】的样本：★ 含 ${VAR} 形态的连接串 ★
probe_line='DSN=postgres://appuser:${APP_DB_PASS}@db.internal'
wl_only_hit=0
for w in "${WHITELIST_PATTERNS[@]}"; do
  [[ -z "${w}" ]] && continue
  if [[ "${probe_line}" =~ ${w} ]]; then wl_only_hit=1; break; fi
done
if [[ ${wl_only_hit} -eq 1 ]]; then
  ok "含 \${VAR} 形态的行【★ 仍被白名单命中 ★】（★ 豁免行为未失效 ★）"
else
  bad "含 \${VAR} 形态的行【★ 没有被任何白名单词命中 ★】⇒ ★ 豁免行为已失效 ★"
fi

printf 'PASS=%s FAIL=%s\n' "${PASS}" "${FAIL}"
[[ ${FAIL} -eq 0 ]] || exit 1
exit 0
