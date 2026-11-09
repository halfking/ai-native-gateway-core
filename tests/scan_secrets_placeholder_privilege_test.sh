#!/usr/bin/env bash
# =====================================================================
# tests/scan_secrets_placeholder_privilege_test.sh
#   —— 「★ 白名单豁免【★ 不得掩盖生产代码 ★】★」门
#
# ★ 它钉的是什么（★ 五条不变量 ★）：
#
#   ① 【★ 生产代码路径上【★ 那 35 个已判定为占位符形态的口令值 ★】★
#      【★ 一条都不许【★ 出现在凭据位置 ★】★】
#      ⇒ ★ 起因（★ 2026-11-09 第五十轮续记之三实测 ★）：
#        ★ ②-0 那 115 条连接串族逐条甄别完 ⇒ 桶1（占位符形态）112 条、
#          ★ 38 组、35 个 distinct 口令值 ⇒ 而【★ 这 35 个值在生产代码里
#          ★ 一次都没出现过 ★】★★ ★
#        ★ 而【★ 白名单是【★ 按值豁免 ★】的 ★】⇒ ★ 一旦这 35 个值进了白名单、
#          ★ 【★ 同一个值哪天出现在生产代码里、★ 也一样会被豁免 ★】
#          ⇒ ★ ⇒ ★ **⇒ 那一类问题会【★ 永久变成不可见 ★】★★ ★**
#      ⇒ ★ 所以【★ 这道门【★ 不依赖扫描器的白名单 ★】、★ 而是自己独立扫一遍 ★】
#        ⇒ ★ 白名单落地之后、★ 它【★ 依然有牙 ★】★★ ★
#
#   ② 【★ 量具本身不能退化 ★】：★ 生产代码路径的文件数【★ 必须够大 ★】
#      ⇒ ★ 否则「★ 零命中 ★」可能只是「★ 清单空了 ★」⇒ ★ 阴性断言【★ 不可信 ★】★★ ★
#
#   ③ 【★ 门自己有区分力 ★】：★ 合成夹具里放一个占位符值 ⇒ ★ 门必须抓到 ★
#      ⇒ ★ 而且【★ 夹具走的是与正式扫描【★ 同一条管线 ★】—— ★ 否则
#      ⇒ ★ 【★ 正式扫描那条管线坏了、★ 而夹具走的是另一条、★ 门照样绿 ★】★★ ★
#
#   ④ 【★ 运维脚本侧的命中集合 == 已知清单 ★】（★ 棘轮 ★）
#      ⇒ ★ 新出现 ⇒ 红；★ 已知项失效 ⇒ 红（★ 清单必须与现实一致 ★）★★ ★
#
#   ⑤ 【★ 门自身不得成为处置面的一条命中 ★】（★ 第十三轮那条自指纪律 ★）★★ ★
#
# ★ ★ 判据面【★ 直接取自扫描器的规则表 ★】★ ⇒ ★ 零漂移 ★：
#   ★ DSN 形态用扫描器那几条 `DB_CONNSTRING` 规则、★ 赋值形态用 `CRED_ASSIGN` ★
#   ★ ⇒ ★ 【★ 门不会比扫描器更宽 ★】—— ★ 免得出现「★ 门红、★ 而扫描器不报 ★」★★ ★
#
# ★ ★ ★ ★★ 三条【★ 本轮踩过的坑 ★】★★ ★★（★ 都写在这里、★ 免得下次再踩 ★）：
#   ① ★ 规则表是 CAT|SEV|DESC|PATTERN 四格、★ 而【★ PATTERN 本身含 "|" ★】★
#      ⇒ ★ 用 cut -f4 提取会【★ 在第一个 "|" 处截断 ★】⇒ ★ 规则变半截 ⇒ ★ 匹配静默失效 ★
#      ⇒ ★ 必须用【★ f4- ★】★★ ★
#   ② ★ 【★ 值必须在凭据位置 ★】：★ 预筛【★ 搜「★ 值出现在行的任何位置 ★」】只是粗定位
#      ⇒ ★ 而判定必须【★ 把值从【★ 凭据位置 ★】里取出来、再比对 ★】
#      ⇒ ★ 否则「★ EnvAPIKey ＝ 某个含 llm_gateway 的值 ★」会【★ 因为含 "llm_gateway"
#      ★   这个【★ 用户名 ★】、★ 而被误判成「★ 出现了占位符口令 ★】★★ ★
#      ⇒ ★ ★ 而【★ 这条纪律的真正落点 ★】＝【★ 预筛本身就要按凭据形态收紧 ★】★★ ★
#      ⇒ ★ 否则像 postgres / llm_gateway 这种【★ 满仓都是 ★】的值会让候选爆炸、
#      ★   而门跑到几分钟才出结果 ⇒ ★ 【★ 门必须秒级 ★】★★ ★
#   ③ ★ 写固定串参数表时【★ 别写成 `out="${out}" "-e" "${v}"` ★】—— ★ 那个空格会让
#      ★ 【★ 后两个词变成命令 ★】⇒ ★ 整条扫描【★ 静默为空 ★】⇒ ★ 阴性断言变成假绿 ★★ ★
#
# ★ ★ 路径口径（★ 必须写死、★ 不能靠猜 ★）：
#   · 处置面 ＝ 扫描器自己扫的那批文件 ⇒ ★ 【★ 排除项必须与扫描器一致 ★】
#     ⇒ ★ 本门【★ 逐条从扫描器里读那个排除清单 ★】★ ⇒ ★ 两者【★ 不会漂移 ★】★★ ★
#   · 再从处置面里扣掉三类【★ 占位符的正当所在地 ★】：
#     文档（★ docs/ 与 *.md ★）｜ 测试（★ *_test.* / test_* / tests/ / *.test.* / *.spec.* ★）｜
#     示例（★ *.example / *.sample / envs.samples/ / *.env ★）★★ ★
#   · 剩下的【★ 就是生产代码路径 ★】★ ⇒ ★ 实测【★ 命中 0 条 ★】★★ ★
#   · 而【★ 运维脚本目录（scripts/ 与 deploy/）★】单独一层：
#     ⇒ ★ 那里的命中【★ 全部是【★ 验证脚本 / 提示文案 / 靶点清单 ★】★★ ★
#     ⇒ ★ 所以【★ 它们进【★ 已知清单 ★】、★ 并按棘轮管 ★】★★ ★
#
# ★ ★ ★ ★★ 一条【★ 自指 ★】纪律（★ 2026-11-09 第十三轮 ★）：★★ ★★
#   ★ 本门自己【★ 不得成为处置面的一条命中 ★】——
#   ★ 判别力夹具用的 DSN 形态【★ 一律在运行时拼出来 ★】（★ 连 "://" 都不写字面量 ★）
#   ★ ★ ★ 【★ 本文件里不出现任何【★ 完整的 ★】连接串形态 ★】★★★ ★
#
# ★ 本门自己也要守仓内既有的纪律 ★ ★：
#   · 「★ $变量 紧跟非 ASCII 字节 ★」⇒ ★ 一律用 ${var} 界定 ★
#     （★ 由 scripts/guards/check-shell-fullwidth-var.sh 强制 ★）。
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
[[ -f "${SCAN}" ]]   || { echo "missing ${SCAN}" >&2; exit 1; }
[[ -f "${CONFIG}" ]] || { echo "missing ${CONFIG}" >&2; exit 1; }

# ── 值表：★ 35 个已判定为【★ 占位符形态 ★】的口令值 ★ ──────────────
# ★ 词典词 19 ／ 口令＝用户名 11 ／ 单字符 2 ／ 词表漏收但形态仍是占位符 3
DICT_WORDS=(
  kxpass example-test-password secret nopass pw audit_admin_pw_local_only
  example-db-password example-redis-password llm_gateway_db_pass_2026_secure
  yourpassword gw local_password probepass GENERATED_PASSWORD audit g4
  omnifree_test_pwd your_password example-tenant-password
)
SAME_AS_USER=( maintain postgres tester env fake invalid test gateway llm_gateway nobody switch )
SINGLE_CHAR=( p x )
EXTRA_PLACEHOLDER=( KAIXUAN_PW postgres123 sentinel_probe_target )
VALUES=( "${DICT_WORDS[@]}" "${SAME_AS_USER[@]}" "${SINGLE_CHAR[@]}" "${EXTRA_PLACEHOLDER[@]}" )

# ── 运维脚本侧的已知命中（★ 棘轮：★ 集合必须与现实完全一致 ★）────────
# ★ 格式：file:line ★ ★ 每一条都是【★ 正当的占位符用法 ★】★★ ★
KNOWN_SCRIPT_HITS=(
  "scripts/.mutate-840-throttle.sh:25"
  "scripts/audit/scan_startup_chain.py:20"
  "scripts/omnifree/e2e-verify.sh:46"
  "scripts/start-full.sh:40"
  "scripts/test-migration-430.sh:23"
  "scripts/test-migration-430.sh:340"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:8"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:11"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:12"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:14"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:15"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:16"
  "scripts/ursmcheck/hardcoded_dsn_targets.tsv:23"
  "scripts/verify-fpslot-fix.sh:299"
)

# ── 路径口径 ─────────────────────────────────────────────────────────
is_test()   { case "$1" in *_test.*|test_*|tests/*|*.test.*|*.spec.*) return 0;; esac; return 1; }
is_doc()    { case "$1" in docs/*|*.md) return 0;; esac; return 1; }
is_sample() { case "$1" in *.example|*.sample|envs.samples/*|*.env) return 0;; esac; return 1; }
is_script() { case "$1" in scripts/*|deploy/*) return 0;; esac; return 1; }

# ★ 扫描器自己的排除目录（★ 逐条从扫描器里读、★ 不复制一份 ★）
# ★ ★ 注意：★ 那是个【★ 多行数组 ★】⇒ ★ 必须整块取出来 ★
# ★ ★ ★ 而且：★ 【★ 结束判定必须用【★ 原始那行 ★】★★ ★】—— ★ 否则先把 ")" 替换掉、
# ★ ★ 就永远判不到结束、★ 于是【★ 整份脚本都会被吞进排除清单 ★】★ ⇒ ★ 正则非法 ★★★ ★
scanner_exclude_dirs() {
  awk '
    /^EXCLUDE_DIRS=\(/ { inblk=1 }
    inblk {
      line=$0
      last = (line ~ /\)/)
      gsub(/"/, " ", line); gsub(/\(/, " ", line); gsub(/\)/, " ", line)
      n=split(line, a, " ")
      for (i=1;i<=n;i++) if (a[i]!="" && a[i]!="EXCLUDE_DIRS=") print a[i]
      if (last) exit
    }' "${SCAN}"
}

# ★ ★ 枚举面：★ 必须与扫描器【★ 同一条枚举路径 ★】—— ★ `git ls-files -z` ＋
# ★ 每进程覆盖 `core.quotePath=false`（★ 与 `scripts/scan-secrets.sh:394` 同 ★）★★ ★
# ★ ★ 而【★ 不这么做、★ 会怎样 ★】—— ★ 本轮实测：★ `git ls-files` 默认把非 ASCII 路径
# ★   转义成八进制 ⇒ ★ 本门会把【★ 1256 个不存在的路径 ★】算进「生产代码文件数」★★ ★
# ★   ⇒ ★ 而【★ 那 1256 个路径【★ 扫不到 ★】⇒ ★ 阴性读数【★ 看着成立、★ 而实际少扫了 ★】★★ ★
# ★   ⇒ ★ ★ 而这【★ 正是 F-18 那个洞的同一形状 ★】—— ★ 扫描器已经修好、★ 而门没有 ⇒ ★ 门比扫描器更宽 ★★ ★
tracked_files() {
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.quotePath GIT_CONFIG_VALUE_0=false \
    git ls-files -z 2>/dev/null | tr '\0' '\n'
}

EXCLUDE_DIR_LIST="$(scanner_exclude_dirs)"
EXCLUDE_REGEX="$(printf '%s\n' "${EXCLUDE_DIR_LIST}" | grep -v '^$' | paste -sd'|' -)"
DISPOSITION_FILE="$(mktemp)"
trap 'rm -rf "${TMPD:-}" "${DISPOSITION_FILE:-}"' EXIT
tracked_files | grep -vE '(^|/)('"${EXCLUDE_REGEX}"')/' > "${DISPOSITION_FILE}" || true

# ★ 处置面上的文件（★ 与扫描器同一批 ★）
disposition_files() { cat "${DISPOSITION_FILE}"; }

prod_files() {
  local f
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    is_test "${f}" && continue
    is_doc "${f}" && continue
    is_sample "${f}" && continue
    is_script "${f}" && continue
    printf '%s\n' "${f}"
  done < <(disposition_files)
}

script_files() {
  local f
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    is_test "${f}" && continue
    is_doc "${f}" && continue
    is_sample "${f}" && continue
    is_script "${f}" || continue
    printf '%s\n' "${f}"
  done < <(disposition_files)
}

# ── 判据面：★ 直接取自扫描器的规则表 ★（★ 零漂移 ★）────────────────
# ★ ★ 规则表是四格、★ 而【★ PATTERN 本身含 "|" ★】⇒ ★ 必须用 f4- ★★★ ★
DSN_RULES=()
while IFS= read -r r; do [[ -n "${r}" ]] && DSN_RULES+=("${r}"); done \
  < <(grep -E '^DB_CONNSTRING\|' "${CONFIG}" | cut -d'|' -f4-)
CRED_RULES=()
while IFS= read -r r; do [[ -n "${r}" ]] && CRED_RULES+=("${r}"); done \
  < <(grep -E '^CRED_ASSIGN\|' "${CONFIG}" | cut -d'|' -f4-)

# ★ ★ 分隔符常量：★ 【★ 连 "://" 与 "@" 都不写字面量 ★】—— ★ 否则本门自己
# ★ 会变成处置面的一条命中（★ 第十三轮的自指纪律 ★）★★ ★
SL="/"; AT="@"

# ★ 预筛正则（★ 一次 ERE 扫全仓 ★；★ 而【★ 逐个固定串扫 35 遍 ★】慢一个量级 ★）
ERE=""
build_ere() { # minlen → 填 ERE
  ERE=""
  local v minlen="${1:-0}"
  for v in "${VALUES[@]}"; do
    [[ "${minlen}" -gt 0 && "${#v}" -lt "${minlen}" ]] && continue
    ERE="${ERE}|${v}"
  done
  ERE="${ERE#|}"
}

VALUES_LIST_LC="$(printf '%s\n' "${VALUES[@]}" | tr '[:upper:]' '[:lower:]' | tr '\n' ' ')"

# ★ ★ 判定：★ 【★ 值必须落在凭据位置 ★】★★ ★
#   · DSN 形态：★ 从 "://" 后取 "user:pass@" 的 pass 段 ★
#   · 赋值形态：★ 从关键字后面的引号里取值 ★（★ 且长度 ≥ 8 ★，★ 与扫描器同门槛 ★）
#   ⇒ ★ 一次 awk 做完（★ awk 自带 tolower、★ 零子进程 ★）★★ ★
scan_dsn_files() { # file-list → file:line
  local files="$1"
  [[ -z "${files}" ]] && return 0
  build_ere 0
  DSN_ERE=":(${ERE})${AT}"
  printf '%s\n' "${files}" | tr '\n' '\0' \
    | xargs -0 grep -nHEi "${DSN_ERE}" 2>/dev/null \
    | awk -v vlist="${VALUES_LIST_LC}" '
        BEGIN { n=split(vlist, a, " "); for (i=1;i<=n;i++) want[a[i]]=1 }
        {
          p1=index($0, ":"); if (p1==0) next
          f=substr($0, 1, p1-1)
          rest=substr($0, p1+1)
          p2=index(rest, ":"); if (p2==0) next
          ln=substr(rest, 1, p2-1)
          content=substr(rest, p2+1)
          i=index(content, "://"); if (i==0) next
          rest=substr(content, i+3)
          j=index(rest, ":"); if (j==0) next
          rest=substr(rest, j+1)
          k=index(rest, "@"); if (k==0) next
          pw=tolower(substr(rest, 1, k-1))
          if (pw in want) print f ":" ln
        }'
}

scan_assign_files() { # file-list → file:line
  local files="$1"
  [[ -z "${files}" ]] && return 0
  build_ere 8
  # ★ ★ 预筛必须【★ 与扫描器 CRED_ASSIGN 同形态 ★】★★ ★
  #   ★ 关键字 + 分隔符 + 引号 + 值 + 引号 ⇒ ★ 【★ 零漂移 ★】★★ ★
  #   ★ 而值表只取 ≥8 字符的（★ 与扫描器 `[^'"[:space:]]{8,}` 同门槛 ★）★★ ★
  CRED_ERE="(password|passwd|pwd|secret|api_key|apikey|auth_token|access_token|bearer_token)[[:space:]]*[:=][[:space:]]*['\"](${ERE})['\"]"
  printf '%s\n' "${files}" | tr '\n' '\0' \
    | xargs -0 grep -nHEi "${CRED_ERE}" 2>/dev/null \
    | awk -v vlist="${VALUES_LIST_LC}" '
        BEGIN {
          n=split(vlist, a, " "); for (i=1;i<=n;i++) want[a[i]]=1
          kw=" password passwd pwd secret api_key apikey auth_token access_token bearer_token "
          split(kw, kws, " ")
          nkw=split(kw, kws, " ")
        }
        # ★ 【★ 逐个关键字、逐个出现位置、逐次试解 ★】——
        # ★ 而不是一个「★ 取最靠后的关键字位置 ★】」的粗办法
        # ★ ⇒ ★ 否则「★ 关键字在值里又出现一次 ★」的行会被漏掉 ★
        function try_at(p,   rest, i, c, q, k, val) {
          rest = substr(content, p)
          i = 1
          while (substr(rest, i, 1) == " ") i++
          c = substr(rest, i, 1)
          if (c != "=" && c != ":") return 0
          i++
          while (substr(rest, i, 1) == " ") i++
          q = substr(rest, i, 1)
          if (q != "\"" && q != "\x27") return 0
          i++
          k = index(substr(rest, i), q)
          if (k == 0) return 0
          val = tolower(substr(rest, i, k - 1))
          if (length(val) < 8) return 0
          if (val in want) return 1
          return 0
        }
        {
          p1=index($0, ":"); if (p1==0) next
          f=substr($0, 1, p1-1)
          rest=substr($0, p1+1)
          p2=index(rest, ":"); if (p2==0) next
          ln=substr(rest, 1, p2-1)
          content=tolower(substr(rest, p2+1))
          matched=0
          for (i=1; i<=nkw && !matched; i++) {
            if (kws[i] == "") continue
            j=index(content, kws[i])
            while (j > 0) {
              if (try_at(j + length(kws[i]))) { matched=1; break }
              j=index(substr(content, j + 1), kws[i])
              if (j > 0) j = j + 1
            }
          }
          if (matched) print f ":" ln
        }'
}

detect_files() { # file-list → file:line
  scan_dsn_files "$1"
  scan_assign_files "$1"
}

# ═══ 断言 ①：值表不退化 ═══════════════════════════════════════════
if [[ "${#VALUES[@]}" -eq 35 && "${#DICT_WORDS[@]}" -eq 19 && "${#SAME_AS_USER[@]}" -eq 11 \
      && "${#SINGLE_CHAR[@]}" -eq 2 && "${#EXTRA_PLACEHOLDER[@]}" -eq 3 ]]; then
  ok "值表未退化（★ 35 = 19 + 11 + 2 + 3 ★）"
else
  bad "值表变形：总计 ${#VALUES[@]}（★ 期望 35 ★）／词典 ${#DICT_WORDS[@]}（19）／同值 ${#SAME_AS_USER[@]}（11）／单字符 ${#SINGLE_CHAR[@]}（2）／漏收 ${#EXTRA_PLACEHOLDER[@]}（3）"
fi

# ═══ 断言 ②：★ 生产代码路径清单非空（★ 量具不退化 ★）══════════════
PROD_FILES="$(prod_files)"
PROD_FILE_N="$(printf '%s' "${PROD_FILES}" | grep -c . || true)"
if [[ "${PROD_FILE_N}" -ge 1000 ]]; then
  ok "生产代码路径清单非空（★ ${PROD_FILE_N} 个文件 ★）⇒ ★ 量具确实在扫 ★"
else
  bad "生产代码路径清单只有 ${PROD_FILE_N} 个文件（★ 期望 ≥ 1000 ★）⇒ ★ 下面的零命中【★ 不可信 ★】"
fi

# ═══ 断言 ③：★ 生产代码路径上零命中 ★（★ 核心不变量 ★）════════════
PROD_HITS="$(detect_files "${PROD_FILES}")"
PROD_N="$(printf '%s' "${PROD_HITS}" | grep -c . || true)"
if [[ "${PROD_N}" -eq 0 ]]; then
  ok "生产代码路径上【★ 35 个占位符值 ★】零凭据形态命中"
else
  bad "生产代码路径上出现 ${PROD_N} 条占位符值的凭据形态命中 ⇒ ★ 白名单一落地、★ 它们就会【★ 永久不可见 ★】★★ ★"
  printf '%s\n' "${PROD_HITS}" | sed 's/^/     · /' >&2
fi

# ═══ 断言 ④：★ 脚本侧命中集合 == 已知清单（★ 棘轮 ★）═══════════════
SCRIPT_HITS="$(detect_files "$(script_files)" | sort -u)"
KNOWN_SORTED="$(printf '%s\n' "${KNOWN_SCRIPT_HITS[@]}" | sort -u)"
if [[ "${SCRIPT_HITS}" == "${KNOWN_SORTED}" ]]; then
  ok "运维脚本侧的命中集合与已知清单完全一致（★ ${#KNOWN_SCRIPT_HITS[@]} 条 ★）"
else
  NEW_ONES="$(comm -23 <(printf '%s\n' "${SCRIPT_HITS}") <(printf '%s\n' "${KNOWN_SORTED}") | grep -c . || true)"
  GONE_ONES="$(comm -13 <(printf '%s\n' "${SCRIPT_HITS}") <(printf '%s\n' "${KNOWN_SORTED}") | grep -c . || true)"
  bad "运维脚本侧命中集合与已知清单不一致（★ 新出现 ${NEW_ONES} ／ 已知失效 ${GONE_ONES} ★）"
  comm -23 <(printf '%s\n' "${SCRIPT_HITS}") <(printf '%s\n' "${KNOWN_SORTED}") | sed 's/^/     · 新出现：/ ' >&2
  comm -13 <(printf '%s\n' "${SCRIPT_HITS}") <(printf '%s\n' "${KNOWN_SORTED}") | sed 's/^/     · 已知失效：/ ' >&2
fi

# ═══ 断言 ⑤：★ 门自己有区分力（★ 合成夹具走【★ 同一条管线 ★】★★ ══
TMPD="$(mktemp -d)"
mkdir -p "${TMPD}/pkg/whatever"
# ★ 形态【★ 一律运行时拼出来 ★】—— ★ 连 "://" 都不写字面量 ★
SCHEME="postgres"; U_NAME="kxuser"; P_VAL="kxpass"; HOST_PART="db.internal"
DSN="${SCHEME}:${SL}${SL}${U_NAME}:${P_VAL}${AT}${HOST_PART}:5432/app"
printf 'dsn := "%s"\n' "${DSN}" > "${TMPD}/pkg/whatever/fixture.go"
FIX_HITS="$(detect_files "${TMPD}/pkg/whatever/fixture.go" | wc -l | tr -d ' ')"
if [[ "${FIX_HITS}" -ge 1 ]]; then
  ok "判别力成立（★ 合成夹具被抓到 ${FIX_HITS} 条 ★）"
else
  bad "判别力不成立（★ 合成夹具 0 命中 ⇒ ★ 这道门恒绿、★ 形同虚设 ★）"
fi

# ═══ 断言 ⑥：★ 门自身不得成为处置面的一条命中 ★════════════════════
# ★ ★ 判据直接用【★ 扫描器自己的规则 ★】＋【★ 扫描器自己的匹配引擎 ★】
#   （★ `grep -niE` —— ★ 与扫描器 Step 7 同口径 ★）⇒ ★ 零漂移 ★
# ★ ★ 而【★ 不在本门里写字面量形态 ★】⇒ ★ 判据形态也【★ 运行时从规则表来 ★】★★ ★
SELF_HITS=0
SELF_DETAIL=""
for r in ${DSN_RULES[@]+"${DSN_RULES[@]}"} ${CRED_RULES[@]+"${CRED_RULES[@]}"}; do
  n="$(grep -ciE -e "${r}" "${BASH_SOURCE[0]}" 2>/dev/null || true)"
  n="$(printf '%s' "${n}" | tr -dc '0-9')"
  n="${n:-0}"
  if [[ "${n}" -gt 0 ]]; then
    SELF_HITS=$((SELF_HITS + n))
    SELF_DETAIL="${SELF_DETAIL}     · 规则「$(printf '%s' "${r}" | cut -c1-60)」→ ${n} 行"$'\n'
  fi
done
if [[ "${SELF_HITS}" -eq 0 ]]; then
  ok "本门自身【★ 不是处置面的一条命中 ★】"
else
  bad "本门自身命中了 ${SELF_HITS} 条（★ DB_CONNSTRING ／ CRED_ASSIGN ★）⇒ ★ 整理掉拼装写法就会变成一条命中 ★"
  printf '%s' "${SELF_DETAIL}" >&2
fi

# ═══ 断言 ⑦⑧：★ 白名单在生产代码路径上的【★ 凭据族豁免面 ★】★★★★══════
# ★ ★ 起因（★ 本轮实测 ★）：★ 白名单不是「★ 将来的风险 ★」、★ 它【★ 今天就在吞真实凭据 ★】
# ★   ⇒ ★ `cmd/calc_quality/main.go` 那处硬编码 DSN（★ 口令是运维者名字拼音 ★）的
# ★     主机词落在白名单条目 `localhost` 上 ⇒ ★ 【★ 整行被豁免 ★】
# ★     ⇒ ★ 而【★ 它对扫描器【★ 完全不可见 ★】★★ ★
# ★   ⇒ ★ 而【★ 断言③（35 个占位符值）抓不到它 ★】—— ★ 它的口令【★ 不在那 35 个值里 ★】
# ★ ⇒ ★ 所以【★ 这里量的是【★ 豁免面本身 ★】、★ 而【★ 不是【★ 某几个已知值 ★】★★ ★
# ★   ⇒ ★ ★ 而【★ 这两条断言对【★ 将来新增的任何白名单条目 ★】同样有效 ★】★★ ★
#
# ★ 口径（★ 与扫描器【★ 快速路径 ★】逐条对齐 ★）：
#   · 发现阶段：★ `grep -niE`（★ 不区分大小写 ★）—— 与扫描器 Step 7 同
#   · 豁免判定：★ `[[ =~ ]]`（★ 区分大小写 ★）—— 与扫描器 Step 8 同
#   · 规则族：★ 只算【★ 凭据族 ★】（★ `DB_CONNSTRING` ／ `DB_URL_CREDENTIAL` ／
#     ★ `CRED_ASSIGN` ／ `SSH_PASSWORD` ★）—— ★ 地址族（★ `INTERNAL_IP` 等 ★）另算
#   · 键：★ 【★ 文件 ＋ 凭据位值 ＋ 形态 ＋ 白名单条目 ★】—— ★ 而【★ 不用行号 ★】
#     ⇒ ★ 无关的一行改动不会把清单顶红、★ 而【★ 豁免面本身变了 ★】一定红

# ★ 白名单条目：★ 从扫描器里【★ 原样读 ★】（★ eval 那段数组 ⇒ ★ 零漂移 ★）
WHITELIST_PATTERNS=()
WL_BLOCK="$(awk '/^WHITELIST_PATTERNS=\(/,/^\)$/' "${SCAN}")"
if [[ "${WL_BLOCK}" == "WHITELIST_PATTERNS=("*")" ]]; then
  eval "${WL_BLOCK}"
fi
WL_N="${#WHITELIST_PATTERNS[@]}"
WL_COMBINED=""
for wl in ${WHITELIST_PATTERNS[@]+"${WHITELIST_PATTERNS[@]}"}; do
  if [[ -n "${WL_COMBINED}" ]]; then WL_COMBINED="${WL_COMBINED}|${wl}"; else WL_COMBINED="${wl}"; fi
done
if [[ "${WL_N}" -ge 20 ]]; then
  ok "白名单条目从扫描器原样读出（★ ${WL_N} 条 ★）⇒ ★ 豁免面的判据面【★ 与扫描器同一个 ★】"
else
  bad "白名单只读到 ${WL_N} 条（★ 期望 ≥ 20 ★）⇒ ★ 抽取器坏了 ⇒ ★ 下面的豁免面【★ 不可信 ★】"
fi

# ★ 凭据族规则（★ 每个模式都要带 -e ★】—— ★ 否则 grep 把第 2 个起的模式【★ 当文件名 ★】
# ★   ⇒ ★ 报「No such file」、★ 而 stderr 被吞 ⇒ ★ 【★ 零输出、★ 而门照样绿 ★】★★ ★
# ★   ⇒ ★ ★ 这正是本轮【★ 量具自校准 ★】抓到的一条真坑 ★★ ★
CRED_FAM_RULES=()
for fam in DB_CONNSTRING DB_URL_CREDENTIAL CRED_ASSIGN SSH_PASSWORD; do
  while IFS= read -r r; do [[ -n "${r}" ]] && CRED_FAM_RULES+=("${r}"); done \
    < <(grep -E "^${fam}\\|" "${CONFIG}" | cut -d'|' -f4-)
done
CRED_GREP_ARGS=()
for r in ${CRED_FAM_RULES[@]+"${CRED_FAM_RULES[@]}"}; do CRED_GREP_ARGS+=(-e "${r}"); done

# ★ 凭据位取值的两条正则（★ 形态全部由变量拼出来 ⇒ ★ 本门自身【★ 不出现任何连接串形态 ★】★★ ★
DSN_VAL_RE="${SL}${SL}[^:/@\"[:space:]]+:([^@/\"[:space:]]+)@"
CRED_KW_RE="(password|passwd|pwd|secret|api_key|apikey|auth_token|access_token|bearer_token)"
CRED_VAL_RE="${CRED_KW_RE}[[:space:]]*[:=][[:space:]]*[\"']([^\"'[:space:]]+)[\"']"

# ★ 占位词典（★ 逐条对过本轮的豁免清单 ★）★★ ★
# ★ ★ 而【★ 它的【★ 词表漏收风险 ★】★ 是这道门的**已知局限**：★ 一条新的字面量豁免
# ★   【★ 若它的值恰好长得像占位词 ★】、★ 断言⑧就抓不到它 ⇒ ★ ★ 而断言⑦【★ 仍会红 ★】
# ★   ⇒ ★ ★ 所以【★ 断言⑦才是主判据、★ 断言⑧是加严 ★】★★ ★
PLACEHOLDER_WORDS=" user username usr pass password passwd pwd secret example placeholder your yours my test testing dummy sample demo temp tmp foo bar xxx xxxxxx host hostname dbname db database gateway default none null empty redacted masked changeme change_me postgres kxpass nopass gw audit maint maintain tester env fake invalid nobody switch audit_admin_pw_local_only example-db-password example-redis-password example-test-password example-tenant-password llm_gateway_db_pass_2026_secure local_password probepass GENERATED_PASSWORD omnifree_test_pwd your_password yourpassword postgres123 sentinel_probe_target KAIXUAN_PW "

# ★ ★ 形态正则【★ 全部走变量 ★】—— ★ 因为 `[[ v =~ ^<…>$ ]]` 里【★ `<` 会被词法器
# ★   当成重定向 ★】⇒ ★ 直接内联会【★ 语法错 ★】★★ ★（★ 本轮实测 ★）
RE_VAR='^\$\{[A-Za-z_][A-Za-z0-9_]*(:.*)?\}$'
RE_ANGLE='^<[^>]+>$'
RE_IDENT='^[A-Za-z_][A-Za-z0-9_]*$'
RE_DOTID='^[a-z0-9_]+(\.[a-z0-9_]+)+$'

value_shape() { # 值 → 形态
  local v="$1" lv
  [[ -z "${v}" ]] && { printf '空'; return 0; }
  lv="$(printf '%s' "${v}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${v}" =~ ${RE_VAR} ]]; then printf '变量形态'; return 0; fi
  if [[ "${v}" =~ ${RE_ANGLE} ]]; then printf '尖括号形态'; return 0; fi
  if [[ "${v}" == *"*"* || "${v}" == *"…"* ]]; then printf '掩码形态'; return 0; fi
  case " ${PLACEHOLDER_WORDS} " in *" ${lv} "*) printf '词典占位形态'; return 0;; esac
  if [[ "${v}" =~ ${RE_IDENT} ]]; then printf '标识符形态'; return 0; fi
  if [[ "${v}" =~ ${RE_DOTID} ]]; then printf '点号标识符形态'; return 0; fi
  printf '字面量'
}

# ★ ★ 判定：★ 【★ 凭据位上的值是不是【★ 字面量 ★】★★ ★
# ★   · DSN 形态：★ 标识符形态【★ 就是字面量 ★】—— ★ 它【★ 不是变量引用 ★】★★ ★
# ★     （★ 而这正是本轮抓到的那一条：★ `kaixuan` ★）★★ ★
# ★   · 赋值形态：★ 标识符／点号标识符【★ 是 i18n key ／ 事件名 ★】⇒ ★ 占位符 ★★ ★
is_literal_credential() { # 形态 值 位置 → 0=字面量
  local shape="$1" v="$2" where="$3"
  case "${shape}" in
    变量形态|尖括号形态|掩码形态|词典占位形态) return 1 ;;
    标识符形态|点号标识符形态)
      if [[ "${where}" == "DSN" ]]; then return 0; fi
      return 1 ;;
  esac
  return 0
}

# ★ 豁免面：★ 【★ 文件 ＋ 凭据位值 ＋ 形态 ＋ 白名单条目 ★】★★ ★
compute_exempt_cred() {
  local files="$1" ml f rest ln content shape val wlpat
  [[ -z "${files}" ]] && return 0
  while IFS= read -r ml; do
    f="${ml%%:*}"; rest="${ml#*:}"; ln="${rest%%:*}"; content="${rest#*:}"
    [[ -z "${WL_COMBINED}" ]] && continue
    [[ "${content}" =~ ${WL_COMBINED} ]] || continue
    shape=""; val=""
    if [[ "${content}" =~ ${DSN_VAL_RE} ]]; then
      shape="DSN"; val="${BASH_REMATCH[1]}"
    else
      # ★ ★ 【★ 取值必须【★ 不区分大小写 ★】★★ ★ —— ★ 因为扫描器的【★ 发现阶段 ★】
      # ★   是 `grep -niE`（★ 不敏感 ★）⇒ ★ 而 `PGPASSWORD=` 这类【★ 大写形态 ★】
      # ★   【★ 会被发现 ★】⇒ ★ ★ 而本门用 `=~`（★ 敏感 ★）⇒ ★ 【★ 取不到值 ★】★★ ★
      # ★   ⇒ ★ 而那条行【★ 就被整条漏掉 ★】⇒ ★ ★ 所以【★ 这里临时开 nocasematch ★】★★ ★
      # ★ ★ 而【★ 白名单判定【★ 必须保持敏感 ★】★★ ★ —— ★ 因为扫描器 Step 8 的
      # ★   `=~` 【★ 就是敏感的 ★】⇒ ★ ⇒ ★ 所以【★ 开关只在取值这一小段里开 ★】★★ ★
      shopt -s nocasematch
      if [[ "${content}" =~ ${CRED_VAL_RE} ]]; then
        # ★ ★ 【★ 组号是 2、★ 不是 1 ★】—— ★ 因为这条正则有【★ 两个捕获组 ★】★★ ★
        # ★   ① 关键词组 ② 值组 ⇒ ★ 而 bash 的 `=~` 【★ 只认第 N 个括号 ★】★★ ★
        # ★   ⇒ ★ 取 `[1]` 会拿到【★ 关键词本身 ★】⇒ ★ 而值永远是 "password" ★★ ★
        # ★   ⇒ ★ ★ 这条错【★ 不会报错、★ 只会静默取错值 ★】⇒ ★ ★ 而它【★ 是靠
        # ★   【★ 两条独立读数对不上 ★】才被抓到的（★ python 量具给的是另一个值 ★）★★ ★
        shape="ASSIGN"; val="${BASH_REMATCH[2]}"
      fi
      shopt -u nocasematch
    fi
    [[ -z "${val}" ]] && continue
    wlpat=""
    for wl in ${WHITELIST_PATTERNS[@]+"${WHITELIST_PATTERNS[@]}"}; do
      if [[ "${content}" =~ ${wl} ]]; then wlpat="${wl}"; break; fi
    done
    printf '%s\t%s\t%s\t%s\n' "${f}" "${val}" "${shape}" "${wlpat}"
  done < <(printf '%s\n' "${files}" | tr '\n' '\0' \
      | xargs -0 grep -nHEi "${CRED_GREP_ARGS[@]}" 2>/dev/null)
}

EXEMPT_CRED="$(compute_exempt_cred "${PROD_FILES}" | sort -u)"
EXEMPT_CRED_N="$(printf '%s' "${EXEMPT_CRED}" | grep -c . || true)"

# ★ 已知豁免面（★ 棘轮：★ 集合必须与现实完全一致 ★）★★ ★
KNOWN_EXEMPT_CRED=(
  'cmd/calc_quality/main.go	kaixuan	DSN	localhost'
  'cmd/llm-gw-annotator/quickstart.sh	pass	DSN	user:pass@host'
  'cmd/sessionmeta-bench/testdata/sessions_100.jsonl	password	DSN	example\.com'
  'cmd/tools/backfill-modality/main.go	pass	DSN	user:pass@host'
  'cmd/tools/migration-456-test/main.go	pass	DSN	user:pass@host'
  'cmd/tools/validate_sessions_v2/main.go	pass	DSN	user:pass@host'
  'config.example.yaml	password	DSN	127\.0\.0\.1'
  'domains/credential/rpm_redis.go	pass	DSN	:pass@'
  'installer/cmd/llm-gw-installer/embeddata/compose.yml	${POSTGRES_PASSWORD}	DSN	\$\{[A-Z_][A-Z0-9_]*\}'
  'installer/templates/compose.yml	${POSTGRES_PASSWORD}	DSN	\$\{[A-Z_][A-Z0-9_]*\}'
  'pkg/identity-migrate/identity-migrate.go	pass	DSN	user:pass@host'
  'proxy/parser.go	pass	DSN	user:pass@host'
  'proxy/parser.go	password	DSN	:password@'
  'sql/scripts/dump-standard-models.sh	${PGPASSWORD:-}	ASSIGN	127\.0\.0\.1'
  'storage/factory/factory.go	pass	DSN	:pass@'
  'tools/benchreport/main.go	pass	DSN	127\.0\.0\.1'
  'web/src/App.vue	openChangePassword	ASSIGN	@[a-z-]+="'
  'web/src/views/OutputComplianceView.vue	output_compliance.redact_password	ASSIGN	["'\''][a-z0-9_]+(\.[a-z0-9_]+)+["'\'']'
  'web/src/views/UsersView.vue	onDrawerResetPassword	ASSIGN	@[a-z-]+="'
)

KNOWN_SORTED="$(printf '%s\n' "${KNOWN_EXEMPT_CRED[@]}" | grep -v '^__FILL__$' | sort -u)"
if [[ "${EXEMPT_CRED}" == "${KNOWN_SORTED}" ]]; then
  ok "白名单在生产代码路径上的【★ 凭据族豁免面 ★】与已知清单完全一致（★ ${EXEMPT_CRED_N} 条 ★）"
else
  bad "白名单在生产代码路径上的凭据族豁免面与已知清单不一致（★ 实测 ${EXEMPT_CRED_N} 条 ★）⇒ ★ 白名单一改、★ 豁免面就【★ 静默地 ★】变掉"
  printf '     · 实测清单（文件→凭据位值→形态→白名单条目）：\n' >&2
  printf '%s\n' "${EXEMPT_CRED}" | sed 's/^/       /' >&2
  printf '     · 已知清单：\n' >&2
  printf '%s\n' "${KNOWN_SORTED}" | sed 's/^/       /' >&2
fi

# ═══ 断言 ⑧：★ 字面量豁免【★ 只许那已知的一条 ★】★★ ★
# ★ ★ 而【★ 这一条【★ 正是那处生产代码硬编码 DSN ★】—— ★ 它【★ 今天对扫描器不可见 ★】
# ★   ⇒ ★ 而【★ 它怎么处置（★ 改环境变量 ★】 还是 【★ 先轮换口令 ★】★ ⇒ ★ ★ 不代裁 ★★★ ★
LITERAL_LIST=""
while IFS=$'\t' read -r ef ev esh ewl; do
  [[ -z "${ef}" ]] && continue
  eshape="$(value_shape "${ev}")"
  if is_literal_credential "${eshape}" "${ev}" "${esh}"; then
    LITERAL_LIST="${LITERAL_LIST}${ef}|${ev}"$'\n'
  fi
done <<< "${EXEMPT_CRED}"
LITERAL_SORTED="$(printf '%s' "${LITERAL_LIST}" | grep -v '^$' | sort -u)"
KNOWN_LITERAL_CRED="cmd/calc_quality/main.go|kaixuan"

if [[ "${LITERAL_SORTED}" == "${KNOWN_LITERAL_CRED}" || -z "${LITERAL_SORTED}" ]]; then
  ok "豁免面里【★ 凭据位上是字面量 ★】的只有已知那一条（★ ${KNOWN_LITERAL_CRED} ★）⇒ ★ 其余豁免全是占位符／变量形态"
  [[ -n "${LITERAL_SORTED}" ]] && printf '     · ★ 该条【★ 对扫描器完全不可见 ★】、★ 处置方式【★ 不代裁 ★】★\n' >&2
else
  bad "豁免面里出现【★ 新的字面量凭据 ★】（★ 实测 ${LITERAL_SORTED//$'\n'/ ／ }★）⇒ ★ 白名单正在吞真凭据"
  printf '%s\n' "${LITERAL_SORTED}" | sed 's/^/       · /' >&2
fi

printf 'PASS=%s FAIL=%s\n' "${PASS}" "${FAIL}"
[[ "${FAIL}" -eq 0 ]] || exit 1
