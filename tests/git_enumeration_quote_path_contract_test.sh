#!/usr/bin/env bash
# =====================================================================
# tests/git_enumeration_quote_path_contract_test.sh
#   —— 「★ 枚举面【★ 不得被 core.quotePath 转义 ★】」契约门
#
# ★ 它钉的是什么（★ 六条不变量 ★）：
#
#   ① 【★ 枚举面消费点【★ 必须被清点出来 ★】★】
#      ⇒ ★ 而【★ 清点必须【★ 剔掉注释与字符串常量 ★】★★ ★
#        ⇒ ★ 否则「★ 注释里提到 git ls-files ★」也会被算成消费点 ⇒ ★ 清单失真 ★★★ ★
#      ⇒ ★ 而【★ 粒度是【★ 调用点 ★】（★ 一行一个 ★）★★ ★
#
#   ② 【★ 不带 `-z` 的调用点【★ 必须逐条登记在豁免清单里 ★】】（★ 双向棘轮 ★）
#      ⇒ ★ 新出现 ⇒ 红；★ 已知项失效（★ 已 `-z` 化 或 删了 ★）⇒ 红
#      ⇒ ★ ★ 而【★ 这条是【★ 「★ 枚举洞 ★」这个类【★ 唯一的机器护栏 ★】★★ ★
#        （★ F-18 修的是【★ 扫描器自己 ★】那一处、★ 而【★ 别的消费点 ★】至今【★ 零覆盖 ★】★★ ★）
#
#   ③ 【★ 每条豁免【★ 必须用【★ 差分探针 ★】证明「★ 转义不改变它的判定 ★】★★ ★】
#      ⇒ ★ 门在【★ 合成仓 ★】里、★ 用【★ 转义形态 ★】与【★ `-z` 形态 ★】各跑一次【★ 同一条判定 ★】
#      ⇒ ★ 两次判定【★ 必须相同 ★】⇒ ★ ⇒ ★ **不同 ⇒ 【★ 那处是真洞 ★】⇒ 红 ★★ ★
#      ⇒ ★ ★ 而【★ 管道【★ 自己负责归约成「★ 判定结果 ★」★★ ★ —— ★ 而【★ 不是原始清单 ★】
#        ⇒ ★ 因为【★ 清单本身【★ 必然不同 ★】⇒ ★ 而【★ 判定才必须相同 ★】★★★ ★
#
#   ④ 【★ 门自己有牙 ★】：★ 合成仓里放一个非 ASCII 路径的探针
#      ⇒ ★ 断言【★ 转义形态【★ 认不出真实名 ★】、★ 而【★ `-z` 形态【★ 认得出 ★】★★ ★
#      ⇒ ★ ⇒ ★ 否则「★ 探针跑空 ★」会让③ 【★ 恒绿 ★】★★ ★
#
#   ⑤ 【★ 每条豁免【★ 必须带一个探针文件名 ★】★ —— ★ 而【★ 那个名字【★ 得真的在仓里 ★】
#      ⇒ ★ 否则「★ 两侧都没命中 ★」也会判成「相同」⇒ ★ ⇒ ★ 差分探针【★ 无牙 ★】★★ ★
#
#   ⑥ 【★ 门自身不得成为处置面的一条命中 ★】（★ 第十三轮那条自指纪律 ★）★★ ★
#
# ★ ★ 机制来源（★ 实测、★ 树外合成仓、★ `core.quotePath=true`（★ git 默认 ★）★★ ★：
#   · `git ls-files`     ⇒ 非 ASCII 名被转义成八进制 ＋ **外层双引号 ★**
#                          ⇒ ★ 而【★ 下游的 `^前缀` 锚点【★ 会落空 ★】★★ ★
#   · `git ls-files -z`  ⇒ 原始 UTF-8 字节、★ NUL 分隔、★ 无引号 ★★
#   ⇒ ★ ⇒ ★ 所以【★ 带 `-z` 的消费点【★ 天然免疫 ★】★★ ★
#
# ★ 零外部依赖、★ 全部在 mktemp 里 ⇒ 可进 CI 快门 ★
# Run: bash tests/git_enumeration_quote_path_contract_test.sh
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || { echo "cannot cd to repo root" >&2; exit 1; }

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

SELF_REL="tests/git_enumeration_quote_path_contract_test.sh"

# ── 抽取器（★ 只认【★ 命令位置 ★】的 git 调用 ★）★ ──────────────────
# ★ 形态覆盖：★ shell 的 `git ls-files` ／ `git diff --name-only` ★
#           ｜★ go 的 `exec.Command(…, "ls-files", …)` ★
#           ｜★ python 的 `git('ls-files', …)`（★ 仓内那个 helper ★）★★★ ★
# ★ ⇒ ★ 而【★ 注释行 ★】与【★ JSON 字符串常量里的命令 ★】都被排除 ★★★ ★
# ★ ★ 而【★ `git` 与动词之间【★ 允许出现任意参数 ★】—— ★ 而【★ 不能只写「★ `-flag` 组 ★」★★ ★
#   ⇒ ★ 因为【★ `git -C "$ROOT" ls-files` 里那个 `-C` 的【★ 参数是独立的 ★】⇒ ★ 而那种写法【★ 会漏匹配 ★】
#   ⇒ ★ ★ 这正是本门【★ 第一版抽取器 ★】的真实缺陷、★ 也是【★ 「★ 阴性断言【★ 看着成立、★ 而其实漏了 ★】★★ ★
#   · 注释：★ 行首 `#` ／ `//` ★
#   · JSON 常量：★ 行内含 `\"`（★ 转义引号 ★）★ ⇒ ★ 那处 `git` 前一个字符是字母 ⇒ 也不匹配 ★★★ ★
extract_raw() {
  grep -rnE \
    --include='*.sh' --include='*.bash' \
    --include='*.go' --include='*.py' --include='*.mjs' \
    '[^A-Za-z0-9_-]git[^|;]*ls-files|[^A-Za-z0-9_-]git[^|;]*--name-only' \
    . 2>/dev/null \
  | grep -vE '^\./(docs|vendor|node_modules|\.git|build|dist|out|coverage)/' \
  | grep -vE ':[[:space:]]*(#|//)' \
  | grep -vF '\\"' \
  | grep -vE ':[[:space:]]*["'"'"']' \
  | grep -vE 'fmt\.Errorf|t\.Fatalf|Sprintf|Printf|log\.' \
  | grep -vF "${SELF_REL}:" \
  | sed 's|^\./||'
}

is_z_line() { # 行内是否带 -z（★ 作为【★ 独立记号 ★】出现 ★）
  # ★ ★ 而【★ 不能只认 `ls-files` 紧跟 `-z` ★】—— ★ 因为【★ python 形态是 `'ls-files', '-z',` ★】
  #   ⇒ ★ 而【★ 那里面 `-z` 前面是【★ 引号 ★】、★ 不是空白 ★★★ ★
  #   ⇒ ★ ★ 而【★ 这正是本门【★ 第二版抽取器 ★】漏判的形态 ★★ ★
  printf '%s' "$1" | grep -qE '(^|[[:space:]"'"'"'])-z([[:space:]"'"'"',;)]|$)'
}

# ── 已知豁免清单（★ 双向棘轮 ★）★ ───────────────────────────────────
# ★ 格式：★ file ｜ 指纹（★ 该调用点的枚举命令片段、★ 必须出现在非 -z 行里 ★）
#     ｜ 探测参数（★ 在合成仓里复现同一条枚举 ★）｜ 归约管道 ｜ 探针文件名 ★
# ★ ★ 而【★ 表里的路径【★ 一律不写字面的 `.sh` ★】—— ★ 而【★ 写成 `@@` ★】★★ ★
#   ⇒ ★ 因为【★ 本文件在 `tests/` 下、★ 而它【★ 本身就是一个「★ 调用方候选 ★」★★ ★
#     ⇒ ★ 而【★ 登记册门会按「★ 非注释行里出现过哪个 `.sh` 名 ★」建索引 ★】★★★ ★
#     ⇒ ★ ⇒ ★ 【★ 本文件一旦写出别的门脚本的 `.sh` 名、★ 那道门就会以为它「★ 有调用方 ★」★★ ★
#   ⇒ ★ ⇒ ★ **⇒ 所以【★ 路径后缀在运行时拼出来 ★】★★ ★
EXEMPT_TABLE_RAW=(
  "scripts/guards/check-shell-fullwidth-var@@|ls-files '*.sh'|'*.sh'|wc -l|probe-sh.sh"
  "scripts/check-frontend-lockfiles@@|ls-files --error-unmatch \"\$lf\"|--error-unmatch -- web/package.json|wc -l|web/package.json"
  "scripts/sync-to-github@@|ls-files --error-unmatch -- scripts/scan-secrets.private.replacements|--error-unmatch -- scripts/scan-secrets.private.replacements|wc -l|scripts/scan-secrets.private.replacements"
  "scripts/ursmcheck/hollowout_lock_test.go|\"ls-files\", \"--error-unmatch\", hollowoutLockName|--error-unmatch -- .ursmcheck-hollowout.lock|wc -l|.ursmcheck-hollowout.lock"
  "tests/deploy_sops_test@@|ls-files -- '.env.*.enc'|-- '.env.*.enc'|wc -l|.env.252.enc"
  "tests/deploy_sops_test@@|ls-files --error-unmatch -- scripts/scan-secrets.private.replacements|--error-unmatch -- scripts/scan-secrets.private.replacements|wc -l|scripts/scan-secrets.private.replacements"
  "tests/env_injector_test@@|ls-files -- '.env.252.enc'|-- '.env.252.enc'|wc -l|.env.252.enc"
  "tests/deploy_sync_prune_contract_test@@|ls-files \"\$rel\"| |wc -l|probe-sh.sh"
)
EXEMPT_TABLE=()
for _r in "${EXEMPT_TABLE_RAW[@]}"; do EXEMPT_TABLE+=("${_r//@@/.sh}"); done
unset EXEMPT_TABLE_RAW

# ═══ 断言 ①：★ 抽取器本身不退化 ═════════════════════════════════════
RAW="$(extract_raw)"
R_N="$(printf '%s\n' "${RAW}" | grep -c . || true)"
NONZ="$(printf '%s\n' "${RAW}" | while IFS= read -r l; do [[ -n "${l}" ]] || continue; is_z_line "${l}" || printf '%s\n' "${l}"; done)"
NZ_N="$(printf '%s\n' "${NONZ}" | grep -c . || true)"
Z_N=$(( R_N - NZ_N ))
if [[ "${R_N}" -ge 20 && "${NZ_N}" -ge 3 && "${Z_N}" -ge 5 ]]; then
  ok "枚举面消费点清点到 ${R_N} 处（★ 带 -z ${Z_N} ／ 不带 -z ${NZ_N} ★）⇒ ★ 抽取器在工作 ★"
else
  bad "清点结果异常（★ 原始 ${R_N} ／ 带 -z ${Z_N} ／ 不带 -z ${NZ_N} ★）⇒ ★ 抽取器坏了 ⇒ ★ 下面的判定【★ 不可信 ★】"
fi

# ═══ 断言 ②：★ 不带 -z 的调用点【★ 必须逐条登记 ★】══ ★ 双向棘轮 ══
MISSING=""
while IFS= read -r line; do
  [[ -z "${line}" ]] && continue
  f="${line%%:*}"
  hit=0
  while IFS= read -r row; do
    [[ -z "${row}" ]] && continue
    ef="${row%%|*}"
    fp="${row#*|}"
    fp="${fp%%|*}"
    [[ "${f}" == "${ef}" ]] || continue
    [[ "${line}" == *"${fp}"* ]] && hit=1
  done <<< "$(printf '%s\n' "${EXEMPT_TABLE[@]}")"
  [[ "${hit}" -eq 0 ]] && MISSING="${MISSING}${line}"$'\n'
done <<< "${NONZ}"
if [[ -z "${MISSING}" ]]; then
  ok "所有【★ 不带 -z ★】的调用点都在豁免清单里（★ ${NZ_N} 处 ★）"
else
  bad "有【★ 不带 -z 且未登记 ★】的枚举面调用点："
  printf '%s' "${MISSING}" | sed 's/^/     · /' >&2
fi

GONE=""
while IFS= read -r row; do
  [[ -z "${row}" ]] && continue
  ef="${row%%|*}"
  fp="${row#*|}"
  fp="${fp%%|*}"
  hit=0
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    [[ "${line%%:*}" == "${ef}" ]] || continue
    [[ "${line}" == *"${fp}"* ]] && hit=1
  done <<< "${NONZ}"
  [[ "${hit}" -eq 0 ]] && GONE="${GONE}${ef}｜${fp}"$'\n'
done <<< "$(printf '%s\n' "${EXEMPT_TABLE[@]}")"
if [[ -z "${GONE}" ]]; then
  ok "豁免清单与现实完全一致（★ ${#EXEMPT_TABLE[@]} 条 ★）"
else
  bad "豁免清单里有【★ 已失效 ★】的条目（★ 已 -z 化 或 已删除 ★）："
  printf '%s' "${GONE}" | sed 's/^/     · /' >&2
fi

# ═══ 断言 ③：★ 差分探针（★ 转义形态 vs -z 形态 ★）★ ═══════════════
TMP="$(mktemp -d)"
trap 'cd / 2>/dev/null; rm -rf "${TMP}"' EXIT
SYN="$TMP/repo"
mkdir -p "${SYN}"
cd "${SYN}" || exit 1
git init -q . >/dev/null 2>&1
git config core.quotePath true

# ★ 先按豁免表把探针文件放进合成仓（★ 断言⑤：★ 那个名字【★ 得真的在仓里 ★】★★ ★）
for row in "${EXEMPT_TABLE[@]}"; do
  probe="$(printf '%s' "${row}" | cut -d'|' -f4)"
  [[ -z "${probe}" ]] && continue
  mkdir -p "${SYN}/$(dirname "${probe}")" 2>/dev/null || true
  printf 'x\n' > "${SYN}/${probe}"
done
# ★ 再放通用探针 ★
mkdir -p "${SYN}/sql/migrations/startup"
printf 'x\n' > "${SYN}/ascii-probe.txt"
printf 'x\n' > "${SYN}/中文探针.txt"
printf 'x\n' > "${SYN}/probe-sh.sh"
printf 'x\n' > "${SYN}/sql/migrations/startup/123_中文迁移.sql"
git add -A >/dev/null 2>&1
git -c user.email=t@t -c user.name=t commit -qm probe >/dev/null 2>&1

probe_diff() { # 枚举参数 ｜ 归约管道 → 0=判定相同（★ 豁免成立 ★）
  local args="$1" pipe="$2" d1 d2
  d1="$(eval "git ls-files ${args} 2>/dev/null | ${pipe:-cat}" 2>/dev/null)"
  d2="$(eval "git ls-files -z ${args} 2>/dev/null | tr '\\0' '\\n' | ${pipe:-cat}" 2>/dev/null)"
  if [[ "${d1}" == "${d2}" ]]; then return 0; fi
  printf '     · D1（转义形态）＝[%s]\n' "${d1}" >&2
  printf '     · D2（-z 形态）＝[%s]\n' "${d2}" >&2
  return 1
}

for row in "${EXEMPT_TABLE[@]}"; do
  f="$(printf '%s' "${row}" | cut -d'|' -f1)"
  args="$(printf '%s' "${row}" | cut -d'|' -f2)"
  pipe="$(printf '%s' "${row}" | cut -d'|' -f3)"
  probe="$(printf '%s' "${row}" | cut -d'|' -f4)"
  if [[ -z "${probe}" || ! -e "${SYN}/${probe}" ]]; then
    bad "豁免条目【★ 缺探针文件 ★】：★ ${f}（★ 探针名【★ ${probe:-空 ★}★】★）⇒ ★ 差分探针【★ 无牙 ★】"
    continue
  fi
  if probe_diff "${args}" "${pipe}"; then
    ok "豁免差分成立：★ ${f}（★ 枚举参数【★ ${args:-无 ★}★】★）⇒ ★ 转义不改变它的判定 ★"
  else
    bad "豁免差分【★ 不成立 ★】：★ ${f}（★ 枚举参数【★ ${args:-无 ★}★】★）⇒ ★ 那处【★ 是真洞 ★】★"
  fi
done

# ═══ 断言 ④：★ 门自己有牙 ═══════════════════════════════════════════
cd "${SYN}" || exit 1
ESC="$(git ls-files | grep -c '中文探针' || true)"
RAWZ="$(git ls-files -z | tr '\0' '\n' | grep -c '中文探针' || true)"
if [[ "${ESC}" -eq 0 && "${RAWZ}" -ge 1 ]]; then
  ok "量具有牙：★ 转义形态【★ 认不出非 ASCII 真实名 ★】、★ 而 -z 形态【★ 认得出 ★】"
else
  bad "量具没牙：★ 转义形态命中 ${ESC} ／ -z 形态命中 ${RAWZ}（★ 期望 0 ／ ≥1 ★）⇒ ★ 下面的差分判定【★ 不可信 ★】"
fi
PRF="$(git ls-files -- 'sql/migrations/startup' | grep -cE '^sql/migrations/startup/[0-9]{3}_' || true)"
PRF_Z="$(git ls-files -z -- 'sql/migrations/startup' | tr '\0' '\n' | grep -cE '^sql/migrations/startup/[0-9]{3}_' || true)"
if [[ "${PRF}" -lt "${PRF_Z}" ]]; then
  ok "量具有牙：★ 转义形态【★ 会被 ^前缀 锚点挡住 ★】⇒ ★ 那正是真洞的成因 ★"
else
  bad "量具没牙：★ 前缀探针转义形态命中 ${PRF} ／ -z 形态命中 ${PRF_Z}（★ 期望前者更小 ★）"
fi

# ═══ 断言 ⑥：★ 门自身不得成为处置面的一条命中 ★════════════════════
SELF=0
# ★ ★★★ 【★ 本行的形态【★ 一律在运行时拼出来 ★】—— ★ 连【★ 协议分隔符 ★】都不写字面量 ★】★★ ★
# ★ 起因（★ 实测 ★）：★ 本门【★ 第一版 ★】把模式直接写在单引号里 ⇒ ★ 而扫描器判的是
# ★ 【★ 「有没有一段像连接串的文本」★★ ★ ⇒ ★ 【★ 那行正则文本【★ 自己就是那条命中 ★】★★ ★
# ★ ⇒ ★ 本门【★ 自己 【★ 就是一条 BLOCK 级处置面命中 ★】★★ ★ ⇒ ★ ⇒ ★
# ★ 【★ 所以【★ 自指纪律必须【★ 用与被检对象【★ 同口径的量具 ★】来验 ★】★★ ★
SL="/"; SL2="${SL}${SL}"; AT="@"
SELF_RE="sk-[0-9a-z]{32}|postgres(ql)?${SL2}[^[:space:]]+:[^[:space:]@]+${AT}"
if grep -qiE -- "${SELF_RE}" "${BASH_SOURCE[0]}" 2>/dev/null; then
  SELF=1
fi
if [[ "${SELF}" -eq 0 ]]; then
  ok "本门自身【★ 不是处置面的一条命中 ★】"
else
  bad "本门自身命中了处置面（★ 整理掉那些形态 ★）"
fi

printf 'PASS=%s FAIL=%s\n' "${PASS}" "${FAIL}"
[[ "${FAIL}" -eq 0 ]] || exit 1
