#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# api-sweep.sh —— admin API 逐条巡检（GET-only）+ 失败分类 + 宿主机负载同窗采样
#
# WHAT THIS IS
# ------------
# 本仓的「逐个 API 验证 + 效率检查」需要一个**仓内可复现**的巡检器。
# 此前只有仓外脚本（/tmp/kx-api-sweep.sh），它有三个硬伤：
#
#   ① **没有失败分类** ⇒ 输出只有「状态码 + 一句 note」，
#      读的人分不清「这个端点坏了」与「这个端点本来就要 POST」；
#   ② **没有宿主机负载采样** ⇒ ★ 本机曾有外部任务把 CPU 烧到 load average 400+，
#      **污染了所有慢读数**（慢 SQL / 慢 API 的读数在饱和机器上没有意义）；
#   ③ **没有自检** ⇒ 量具静默少跑了一半也照样返回 0。
#
# 用法：
#   ROUTES=/path/to/routes.txt COOKIE=/path/to/cookies.txt \
#     bash scripts/audit/api-sweep.sh
#
# 路由清单文件（ROUTES）每行一条，支持三种形态：
#   /api/admin/health              ← 只有路径（视为 GET）
#   GET /api/admin/health          ← 带方法前缀（非 GET 的行会被跳过）
#   /api/admin/health⇥GET⇥admin/handler.go:138   ← ★ 三列形态（★ 仓内正本
#                                                  ★ `scripts/audit/asset/admin-routes.txt`
#                                                  ★ 就是这个形态；★ METHOD=ANY 视为 GET）
#   {id} / {name} 之类的占位符会被替换成 1。
#
# 环境变量：
#   BASE        被测服务根地址，默认 http://127.0.0.1:8782
#   ROUTES      路由清单文件，默认 ${TMPDIR:-/tmp}/llmgw-api-sweep-routes.txt
#   COOKIE      cookie jar 文件（Netscape 格式），默认 ${TMPDIR:-/tmp}/llmgw-api-sweep-cookies.txt
#   OUT         输出 TSV，默认 ${TMPDIR:-/tmp}/llmgw-api-sweep.tsv
#   TIMEOUT     单条请求上限（秒），默认 20
#   CONC        并发度，默认 2（低并发 ⇒ 别把排队读成「慢」）
#   LOAD_EVERY  每多少条请求采一次宿主机负载，默认 25
#
# 退出码：
#   0   巡检完成（TSV 已写出，行数自检通过）
#   64  输入/用法错误（路由清单或 cookie 文件缺失等）
#   70  ★ 自检失败（写出的数据行数 != 期望巡检的路由数 ⇒ 量具静默少跑）
#   73  输出文件写不进去
#
# ★★ 失败分类（TSV 的 class 列，**互斥**；每类的语义如下）
# ---------------------------------------------------------------------------
#   ok                  2xx。端点可用。
#   method_not_allowed  HTTP 405。
#                       ★ 语义：**该端点只支持其它方法**（Go 的 mux.HandleFunc 注册时
#                         没有方法前缀、handler 内部自己判方法）。
#                       ★ **这不是产品缺陷**，但它意味着 GET-only 巡检
#                         **覆盖不到该端点的真实方法** ⇒ note 里必须写明
#                         「该路径需 POST/PUT/DELETE 才能验证」。
#   bad_request         HTTP 400。
#                       ★ 语义：**缺必填参数**（query/body）⇒ 覆盖缺口，不是缺陷。
#   not_found           HTTP 404。
#                       ★ 语义：路径形态或资源 ID 不存在 ⇒ 覆盖缺口，不是缺陷。
#   server_error        HTTP 5xx。
#                       ★★ **这一类才是真故障** ⇒ 响应体前 200 字符保留到 note 列。
#   timeout             curl 退出码非 0 或 status 为 000。
#                       ★ **必须区分「客户端超时」与「连接失败」**（note 里写清）：
#                         client_timeout（客户端撞 TIMEOUT）/ conn_failed（连不上）。
#   other_4xx           其它 4xx。
#   unexpected_status   非 2xx 且不属于上面任何一类（1xx / 3xx / 不可解析的状态码）。
#                       ★ 分类名必须诚实：把 3xx 塞进 other_4xx 是**撒谎**。
# ---------------------------------------------------------------------------
#
# ★★ 关于 SSE / 长轮询端点（**必须跳过**，否则是量具假象）
# ---------------------------------------------------------------------------
#   像 /api/admin/live-stream、/api/admin/probe/stream、
#   /api/admin/system-monitor/stream、/api/free-pool/stream 这类端点
#   **响应永不结束** ⇒ GET 上去会一直挂到客户端超时：
#     · 每条白白烧满 TIMEOUT（4 条 = 80 s 纯浪费）；
#     · 且会被误分类成 timeout **真故障** ⇒ 这是**量具假象**不是产品缺陷。
#   ⇒ 本脚本用 SSE_RE 单独排除它们，**计数与清单单独统计**。
#   ★★ **跳过 ≠ 已验证**：这些端点**未经本次 GET-only 巡检验证**，
#      需要 SSE 客户端或非 GET 方法另行验证。
#   ★★ 排除式同时覆盖**斜杠形态**（/stream）与**短横线形态**（live-stream）：
#      只写 /stream 匹配不到 live-stream ⇒ 这是实测踩过的坑。
#
# ★★ 宿主机负载同窗采样
# ---------------------------------------------------------------------------
#   巡检开始、结束、以及每 LOAD_EVERY 条请求各采一次 load average，
#   写进 TSV 末尾的 #meta 行，同时打到 stderr。
#   ★ 优先 sysctl -n vm.loadavg，回退 uptime ⇒ macOS 与 Linux 都能跑
#     （不得依赖 macOS 专有命令而让 Linux 挂掉）。
#   ★ 目的：**让慢读数能自证「当时机器是不是在饱和」**。
#
# ★★ cookie / 口令的处理
# ---------------------------------------------------------------------------
#   cookie **只以文件形式**存在；curl 只拿到**文件路径**（argv 里没有 cookie 值）。
#   本脚本**从不打印 cookie 文件的内容**（只会在文件缺失时打印路径与原因）。
#
# ★★ 输出（TSV，表头固定）
# ---------------------------------------------------------------------------
#   route <TAB> status <TAB> latency_ms <TAB> bytes <TAB> class <TAB> note
#   末尾追加若干 #meta 开头的元数据行（route 列以 #meta 开头，note 列是键值对）。
# ---------------------------------------------------------------------------
set -uo pipefail

# ★★ pipefail 竞态消除（2026-10-27 R153 轮）：本脚本原先用
#     `if printf '%s' "${path}" | grep -qiE "${RE}"; then`
#    来做正则匹配 ⇒ ★ **`grep -q` 一旦命中就提前退出** ⇒ 写端 `printf` 可能收到
#    SIGPIPE(141) ⇒ ★★ **`set -o pipefail` 会把整条管道判成非零** ⇒
#    ⇒ **条件可能误判成「不匹配」** ⇒ ★★ 该形态被本仓的
#    `scripts/ursmcheck/pipefail_race_ledger_test.go` 列为竞态候选点。
# ⇒ ⇒ ★★ 正解 = **消除管道**：改用 bash 原生 `[[ str =~ RE ]]`（POSIX ERE，
#    语义与 `grep -E` 等价）⇒ ★★ **没有写端 ⇒ 没有 SIGPIPE ⇒ 没有竞态**。
# ⇒ ★★ `grep -qi` 是**大小写不敏感**的 ⇒ ★ 配套 `shopt -s nocasematch`，
#    ★ （本脚本内无其它 `=~`，★ 开全局开关不影响别处）。
shopt -s nocasematch

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

BASE="${BASE:-http://127.0.0.1:8782}"
ROUTES="${ROUTES:-${TMPDIR:-/tmp}/llmgw-api-sweep-routes.txt}"
COOKIE="${COOKIE:-${TMPDIR:-/tmp}/llmgw-api-sweep-cookies.txt}"
OUT="${OUT:-${TMPDIR:-/tmp}/llmgw-api-sweep.tsv}"
TIMEOUT="${TIMEOUT:-20}"
CONC="${CONC:-2}"
LOAD_EVERY="${LOAD_EVERY:-25}"
# ★★★ R157 轮（D16）：★ load 阈值 ⇒ ★ 读数**自标可信度**。
#   ★ 缘由（★ R153 轮的实测教训）：★ 一次 35 小时的孤儿压测进程
#   ★ ⇒ ★★ 让**连续多轮**的本地读数**集体失真**（★ 端点耗时塌缩 4~100 倍）
#   ★ ⇒ ★★ 而当时的巡检输出里**没有任何字段**能让人看出「这批读数不可信」。
#   ★ ⇒ ★★ 本轮把 load **从「只记录」升级到「参与判定」**。
#   ★ 阈值口径：★ 默认 = **核数 × 2**（★ 可用 LOAD_UNTRUSTED_MAX 覆盖）
#   ★ ⇒ ★ load 超过它 ⇒ ★ 这批**延迟读数**不可信（★ **状态码与分类仍可信**）。
#   ★ ★ 为什么不按「load > 1」：★ 单核机器上 load=1 就是满载 ⇒ ⇒ 阈值必须跟核数走。
LOAD_UNTRUSTED_MAX="${LOAD_UNTRUSTED_MAX:-}"
# ★★★ R157 轮（D15）：★ 「可安全 POST」的白名单文件。
#   ★ ★★ **默认关**：★ 保持既有行为（★ GET-only）⇒ ★★ 谁想开谁显式开
#   ★★ （★ 开关名：PROBE_POST=1）。
#   ★ ★★ 为什么默认关：★ 白名单是**人工逐条读过源码**得出的
#   ★★ ⇒ ★★ 白名单一旦过期（★ 端点改了语义）⇒ ★★ 探测就可能产生副作用
#   ★★ ⇒ ★★ **默认关 = 最坏情况也只是「少验证几条」**，★ 而不是「意外改了生产」。
PROBE_POST="${PROBE_POST:-0}"
SAFE_POST_ROUTES="${SAFE_POST_ROUTES:-${SCRIPT_DIR}/asset/safe-post-routes.txt}"
if [[ "${PROBE_POST}" != "1" ]]; then
  SAFE_POST_ROUTES=""
elif [[ ! -f "${SAFE_POST_ROUTES}" ]]; then
  die "PROBE_POST=1 但白名单文件不存在：${SAFE_POST_ROUTES}"
  exit 64
fi

# ★ 副作用路径黑名单（GET 也可能触发写 / 外呼 / 状态变更）。
SKIP_RE='execute|cleanup|delete|resume|trigger|reset|purge|revoke|recover|enable|test-now|session-ping|/actions$|rotate|invalidate'

# ★ SSE / 长轮询端点排除（**独立于**副作用黑名单，单独计数）。
#   ★ 必须同时覆盖 /stream（斜杠）与 live-stream（短横线）两种形态。
SSE_RE='(/stream|live-stream|event-stream)'

die() { printf 'FATAL: %s\n' "$*" >&2; }
fatal_usage() { die "$*"; exit 64; }

# subst 把 {id} / {name...} 之类的占位符替换成 1。
# ★ 用 [{] / [}] 而不是 \{ / \}：BSD sed 与 GNU sed 对 \{ 的处理并不一致。
subst() {
  printf '%s' "$1" | sed -E 's/[{][a-zA-Z0-9_]+([.][.][.])?[}]/1/g'
}

# load1 读一次宿主机 1 分钟 load average。
# ★ 优先 sysctl -n vm.loadavg（macOS 原生），回退 uptime（Linux 通用）⇒ 两边都能跑。
load1() {
  local raw="" t=""
  if command -v sysctl >/dev/null 2>&1; then
    raw="$(sysctl -n vm.loadavg 2>/dev/null || true)"
  fi
  if [[ -z "${raw}" ]] && command -v uptime >/dev/null 2>&1; then
    raw="$(uptime 2>/dev/null || true)"
  fi
  if [[ -z "${raw}" ]]; then
    printf 'unknown'
    return 0
  fi
  # 两种形态都要吃得下：
  #   macOS: "{ 434.62 433.75 471.15 }"  /  "load averages: 434.62 433.75 471.15"
  #   Linux: "vm.loadavg = { 0.52 0.58 0.59 }"  /  "load average: 1.20, 0.98, 0.77"
  t="$(printf '%s' "${raw}" | awk '{
      for (i = 1; i <= NF; i++) {
        if ($i ~ /averages?:/) { v = $(i + 1); gsub(/[^0-9.]/, "", v); if (v != "") { print v; exit } }
      }
      for (i = 1; i <= NF; i++) {
        v = $i; gsub(/[{},=]/, "", v)
        if (v ~ /^[0-9]+(\.[0-9]+)?$/) { print v; exit }
      }
    }')"
  if [[ -z "${t}" ]]; then t="unknown"; fi
  printf '%s' "${t}"
}

# cpu_count 读宿主机逻辑核数（★ macOS 与 Linux 两条路径）。
cpu_count() {
  local n=""
  if command -v sysctl >/dev/null 2>&1; then
    n="$(sysctl -n hw.ncpu 2>/dev/null || true)"
  fi
  if [[ -z "${n}" ]] && command -v nproc >/dev/null 2>&1; then
    n="$(nproc 2>/dev/null || true)"
  fi
  if [[ -z "${n}" ]] && command -v getconf >/dev/null 2>&1; then
    n="$(getconf _NPROCESSORS_ONLN 2>/dev/null || true)"
  fi
  case "${n}" in
    ''|*[!0-9]*) n="" ;;
  esac
  printf '%s' "${n}"
}

# load_threshold 算「load 超过多少 ⇒ 延迟读数不可信」。
# ★ 默认 = 核数 × 2；★ 核数取不到 ⇒ 退回 4（★ 一个保守的绝对值）。
load_threshold() {
  if [[ -n "${LOAD_UNTRUSTED_MAX}" ]]; then
    printf '%s' "${LOAD_UNTRUSTED_MAX}"
    return 0
  fi
  local c
  c="$(cpu_count)"
  if [[ -z "${c}" ]]; then c=2; fi
  printf '%s' "$((c * 2))"
}

# ─────────────────────────── 输入自检（fail-fast） ───────────────────────────
# ★ 路由清单不存在 ⇒ 立即退 64。
if [[ ! -f "${ROUTES}" ]]; then
  fatal_usage "路由清单文件不存在：${ROUTES}（ROUTES 环境变量指向它）"
fi
if [[ ! -r "${ROUTES}" ]]; then
  fatal_usage "路由清单文件不可读：${ROUTES}"
fi
# ★ cookie 文件不存在 ⇒ 立即退 64；**只打印路径与原因，绝不打印 cookie 内容**。
if [[ ! -f "${COOKIE}" ]]; then
  fatal_usage "cookie 文件不存在：${COOKIE}（先完成 admin 登录，把 cookie jar 写到该文件）"
fi
if [[ ! -r "${COOKIE}" ]]; then
  fatal_usage "cookie 文件不可读：${COOKIE}"
fi

# ★ 输出文件写不进去 ⇒ 立即退 73。
OUT_DIR="$(dirname "${OUT}")"
if [[ ! -d "${OUT_DIR}" ]]; then
  die "输出目录不存在：${OUT_DIR}"
  exit 73
fi
if ! : > "${OUT}" 2>/dev/null; then
  die "输出文件无法创建：${OUT}"
  exit 73
fi
if [[ ! -w "${OUT}" ]]; then
  die "输出文件不可写：${OUT}"
  exit 73
fi

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/api-sweep.XXXXXX")"
cleanup() {
  if [[ -n "${WORKDIR:-}" && -d "${WORKDIR}" && "${WORKDIR}" == *"/api-sweep."* ]]; then
    rm -rf "${WORKDIR}"
  fi
}
trap cleanup EXIT

# ─────────────────────────── 路由清单解析 + 跳过分类 ───────────────────────────
n_read=0
n_skip_method=0
n_skip_sse=0
n_skip_effect=0
n_expected=0

: > "${WORKDIR}/paths.tsv"
: > "${WORKDIR}/indices.txt"
: > "${WORKDIR}/skip_method.txt"
: > "${WORKDIR}/skip_sse.txt"
: > "${WORKDIR}/skip_effect.txt"

while IFS= read -r raw || [[ -n "${raw}" ]]; do
  raw="${raw%$'\r'}"
  [[ -z "${raw}" ]] && continue
  [[ "${raw}" == \#* ]] && continue
  n_read=$((n_read + 1))

  # ★★ R156 轮改：★ 支持仓内路由清单的三列形态（★ path⇥method⇥file:line）。
  #   ★ 起因：★ `scripts/audit/asset/admin-routes.txt`（★ R154 轮新增的**仓内正本**）
  #     是**制表符分隔的三列**，★ 而本脚本只认「单列路径」或「方法␣路径」
  #     ⇒ ★★ 两者直接组合 ⇒ ★★ **544 条全部 URL 畸形**（★ curl exit 3 = URL malformed）
  #     ⇒ ★★ **仓内正本与仓内工具互相不认** ⇒ ★ **必须让工具吃下正本**。
  #   ★ 兼容三种形态（★ 依次判断）：
  #     ① `path`                       ⇒ 视为 GET
  #     ② `METHOD path`                ⇒ METHOD 非 GET 则跳过
  #     ③ `path⇥METHOD⇥file:line`     ⇒ ★ 仓内正本形态
  method="GET"
  path="${raw}"
  if [[ "${raw}" == *$'\t'* ]]; then
    # ③ 三列形态：★ 第一列是路径，★ 第二列是方法（★ ANY = 源码无方法前缀 ⇒ 视为 GET）。
    path="${raw%%$'\t'*}"
    _rest="${raw#*$'\t'}"
    _m="${_rest%%$'\t'*}"
    method="$(printf '%s' "${_m}" | tr 'a-z' 'A-Z')"
    if [[ "${method}" == "ANY" ]]; then
      method="GET"
    fi
  elif [[ "${raw}" == *" "* ]]; then
    method="${raw%% *}"
    path="${raw#* }"
  fi
  method="$(printf '%s' "${method}" | tr 'a-z' 'A-Z')"

  # ★ ★ R157 轮（D15）：★ **白名单优先**。
  #   ★ 命中白名单 ⇒ ★ 用 POST + **空 JSON body** 探测（★ 空 body ⇒ 必然在
  #   ★   参数校验处被拦住 ⇒ ★ **零副作用**）⇒ ★★ 这 6 条端点从此**被真正验证过**。
  #   ★ ★ 白名单里每条都**逐条读过源码**（★ 见 scripts/audit/asset/safe-post-routes.txt）。
  #   ★ ★ 匹配必须**精确**（★ 比较第一列）：★ `grep -F "$path"` 会**前缀误匹配**
  #   ★ ★ ⇒ ★★ `/api/x` 会命中 `/api/x/y` ⇒ ★★ 于是会 POST 一个**没审过**的端点。
  if [[ -n "${SAFE_POST_ROUTES}" ]] && awk -F'\t' -v p="${path}" \
       '$1 == p && $1 !~ /^#/ { found=1 } END { exit !found }' "${SAFE_POST_ROUTES}" 2>/dev/null; then
    method="POST"
    printf '%s\n' "${raw}" >> "${WORKDIR}/probe_post.txt"
    n_expected=$((n_expected + 1))
    printf '%s\t%s\t%s\n' "${n_expected}" "$(subst "${path}")" "POST" >> "${WORKDIR}/paths.tsv"
    printf '%s\n' "${n_expected}" >> "${WORKDIR}/indices.txt"
    continue
  fi

  # ① 非 GET ⇒ 跳过（该端点需要别的方法才能验证）。
  if [[ "${method}" != "GET" ]]; then
    n_skip_method=$((n_skip_method + 1))
    printf '%s\n' "${raw}" >> "${WORKDIR}/skip_method.txt"
    continue
  fi
  # ② SSE / 长轮询 ⇒ 跳过（**单独计数**：否则会被误读成 timeout 真故障）。
  if [[ "${path}" =~ ${SSE_RE} ]]; then
    n_skip_sse=$((n_skip_sse + 1))
    printf '%s\n' "${path}" >> "${WORKDIR}/skip_sse.txt"
    continue
  fi
  # ③ 带副作用 ⇒ 跳过（GET 也可能触发写 / 外呼）。
  if [[ "${path}" =~ ${SKIP_RE} ]]; then
    n_skip_effect=$((n_skip_effect + 1))
    printf '%s\n' "${path}" >> "${WORKDIR}/skip_effect.txt"
    continue
  fi

  n_expected=$((n_expected + 1))
  # ★ ★ R157 轮（D15）：★ paths.tsv 多带一列 **method**（★ 默认 GET）。
  # ★ ★ 为什么：★ 白名单里的端点要发 POST ⇒ ★ 巡检器必须知道**这一条**用哪个方法。
  printf '%s\t%s\t%s\n' "${n_expected}" "$(subst "${path}")" "${method}" >> "${WORKDIR}/paths.tsv"
  printf '%s\n' "${n_expected}" >> "${WORKDIR}/indices.txt"
done < "${ROUTES}"

# ─────────────────────────── 宿主机负载采样 ───────────────────────────
LOAD_SAMPLES="${WORKDIR}/load_samples.txt"
: > "${LOAD_SAMPLES}"
LOAD_PEAK="0"
LOAD_START="$(load1)"

record_load() {
  local v="$1" tag="$2"
  printf '%s\t%s\n' "${tag}" "${v}" >> "${LOAD_SAMPLES}"
  if [[ "${v}" != "unknown" ]]; then
    if awk -v a="${v}" -v b="${LOAD_PEAK}" 'BEGIN { exit !(a + 0 > b + 0) }'; then
      LOAD_PEAK="${v}"
    fi
  fi
}

record_load "${LOAD_START}" "load_start"

# ─────────────────────────── 巡检（低并发） ───────────────────────────
# ★ 每条请求写一个独立的结果文件（row.<序号>.tsv）⇒ 并发写互不干扰，
#   且最终 TSV 的行序由序号决定（可复现）。
sweep_one() {
  local idx="$1"
  local line path body errfile out rc code bytes time_total ms class note
  line="$(sed -n "${idx}p" "${WORKDIR}/paths.tsv")"
  path="$(printf '%s' "${line}" | cut -f2)"
  http_method="$(printf '%s' "${line}" | cut -f3)"
  [[ -z "${http_method}" ]] && http_method="GET"
  body="${WORKDIR}/body.${idx}"
  errfile="${WORKDIR}/err.${idx}"

  # ★ 不做 `|| echo "000"` 那种兜底：curl 的退出码单独取，
  #   否则退出标记会被**拼接到状态码后面**（LESSONS.md §187）。
  # ★ ★ R157 轮（D15）：★ POST 时发**空 JSON body**（★ `{}`）
  # ★ ★ ⇒ ★ 端点必然在**参数校验**处被拦住 ⇒ ★★ 零副作用。
  if [[ "${http_method}" == "POST" ]]; then
    out="$(curl -sS -m "${TIMEOUT}" -b "${COOKIE}" -o "${body}" \
          -X POST -H 'Content-Type: application/json' --data '{}' \
          -w '%{http_code}\t%{size_download}\t%{time_total}' "${BASE}${path}" 2>"${errfile}")"
  else
    out="$(curl -sS -m "${TIMEOUT}" -b "${COOKIE}" -o "${body}" \
          -w '%{http_code}\t%{size_download}\t%{time_total}' "${BASE}${path}" 2>"${errfile}")"
  fi
  rc=$?

  code="000"
  bytes="0"
  time_total="0"
  if [[ -n "${out}" ]]; then
    IFS=$'\t' read -r code bytes time_total <<< "${out}"
    code="${code:-000}"
    bytes="${bytes:-0}"
    time_total="${time_total:-0}"
  fi

  ms="$(awk -v t="${time_total}" 'BEGIN { printf "%d", (t + 0) * 1000 }')"

  case "${code}" in
    000)
      # ★ 区分「客户端超时」与「连接失败」。
      class="timeout"
      case "${rc}" in
        28) note="客户端超时 client_timeout（TIMEOUT=${TIMEOUT}s）" ;;
        7)  note="连接失败 conn_failed（cannot_connect）" ;;
        6)  note="连接失败 conn_failed（dns_unresolved）" ;;
        35) note="连接失败 conn_failed（tls_error）" ;;
        52) note="连接失败 conn_failed（empty_reply）" ;;
        55) note="连接失败 conn_failed（send_failed）" ;;
        56) note="连接失败 conn_failed（recv_failed）" ;;
        *)  note="连接失败 conn_failed（curl_exit=${rc}）" ;;
      esac
      ;;
    2[0-9][0-9])
      class="ok"
      note=""
      ;;
    405)
      class="method_not_allowed"
      note="该路径需 POST/PUT/DELETE 才能验证；GET-only 巡检覆盖不到该端点的真实方法（非产品缺陷）"
      ;;
    400)
      class="bad_request"
      note="缺必填参数（query/body）；覆盖缺口，非产品缺陷"
      ;;
    404)
      class="not_found"
      note="路径形态或资源 ID 不存在；覆盖缺口，非产品缺陷"
      ;;
    5[0-9][0-9])
      # ★ 真故障 ⇒ 保留响应体前 200 字符。
      class="server_error"
      note="$(head -c 200 "${body}" 2>/dev/null | tr '\t\n\r' '   ')"
      if [[ -z "${note}" ]]; then note="(empty_body)"; fi
      ;;
    4[0-9][0-9])
      class="other_4xx"
      note="HTTP ${code}"
      ;;
    *)
      class="unexpected_status"
      note="HTTP ${code}"
      ;;
  esac

  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${path}" "${code}" "${ms}" "${bytes}" "${class}" "${note}" \
    > "${WORKDIR}/row.${idx}.tsv"
}
export -f sweep_one
export WORKDIR BASE COOKIE TIMEOUT

# ★ 分块：每 LOAD_EVERY 条一块 ⇒ 块与块之间采一次宿主机负载。
mkdir -p "${WORKDIR}/chunks"
awk -v n="${LOAD_EVERY}" -v d="${WORKDIR}/chunks" \
  '{ printf "%s\n", NR > sprintf("%s/c%05d", d, int((NR - 1) / n)) }' "${WORKDIR}/indices.txt"

for cf in "${WORKDIR}"/chunks/c*; do
  [[ -f "${cf}" ]] || continue
  xargs -P "${CONC}" -n 1 bash -c 'sweep_one "$@"' _ < "${cf}"
  record_load "$(load1)" "load_mid"
done

LOAD_END="$(load1)"
record_load "${LOAD_END}" "load_end"

# ─────────────────────────── 组装 TSV ───────────────────────────
n_rows=0
: > "${WORKDIR}/data.tsv"
i=1
while [[ "${i}" -le "${n_expected}" ]]; do
  if [[ -f "${WORKDIR}/row.${i}.tsv" ]]; then
    cat "${WORKDIR}/row.${i}.tsv" >> "${WORKDIR}/data.tsv"
    n_rows=$((n_rows + 1))
  fi
  i=$((i + 1))
done

# ★★ 自检：写出的数据行数必须 == 期望巡检的路由数。
#    （跳过规则已在 n_expected 里体现：被跳过的路由不计入期望行数。）
if [[ "${n_rows}" -ne "${n_expected}" ]]; then
  die "写出的数据行数(${n_rows}) != 期望巡检的路由数(${n_expected}) —— 量具静默少跑了"
  printf 'FATAL: 期望=%s 实际=%s 差=%s\n' "${n_expected}" "${n_rows}" "$((n_expected - n_rows))" >&2
  exit 70
fi

n_samples="$(wc -l < "${LOAD_SAMPLES}" | tr -d ' ')"
n_skipped=$((n_skip_method + n_skip_sse + n_skip_effect))

meta_row() {
  printf '#meta\t%s\t%s\t0\tmeta\t%s=%s\n' "$1" "$2" "$1" "$2"
}

# ★★★ R157 轮（D16）：★ 读数**自标可信度**。
#   ★ 判据：★ 采样期内的 **load 峰值** > 阈值 ⇒ ★ 这批**延迟读数**不可信。
#   ★ ★ 为什么用「峰值」而不是「起止值」：★ 孤儿压测进程那种形态是
#   ★ ★ **中途**起来的 ⇒ ★ 起止两个采样点可能都正常 ⇒ ★ ★ 峰值才抓得住。
#   ★ ★ 为什么只标「延迟不可信」而不废掉整份读数：★ 状态码与分类
#   ★ ★ **不依赖**调度延迟 ⇒ ★★ 那些列仍然有效 ⇒ ★★ 全废是浪费信息。
LOAD_THRESHOLD="$(load_threshold)"
LOAD_TRUST="trustworthy"
if awk -v a="${LOAD_PEAK}" -v b="${LOAD_THRESHOLD}" \
     'BEGIN { exit !(a != "" && a != "unknown" && (a + 0) > (b + 0)) }'; then
  LOAD_TRUST="latency_untrusted"
fi
if [[ "${LOAD_TRUST}" != "trustworthy" ]]; then
  printf '★ ★★ load 峰值 %s > 阈值 %s ⇒ ★ 本批**延迟**读数**不可信**（★ 状态码/分类仍可信）\n' \
    "${LOAD_PEAK}" "${LOAD_THRESHOLD}" >&2
  printf '★ ★★ 输出里已自标 reading_trustworthy=%s ⇒ ★ 别把它当性能读数用\n' "${LOAD_TRUST}" >&2
fi

if ! {
  printf 'route\tstatus\tlatency_ms\tbytes\tclass\tnote\n'
  cat "${WORKDIR}/data.tsv"
  # ★ ★ R157 轮（D16）：★ 自标**读数可信度**（★ 必须写在**输出 TSV 里**，
  #   ★ ⇒ 消费方（人 / 脚本 / 下游门）**读 TSV 时就能看到** ⇒ ★ 不必去翻 stderr）。
  meta_row "reading_trustworthy" "${LOAD_TRUST}"
  meta_row "load_threshold" "${LOAD_THRESHOLD}"
  meta_row "cpu_count" "$(cpu_count)"
  meta_row "load_start" "${LOAD_START}"
  meta_row "load_end" "${LOAD_END}"
  meta_row "load_peak" "${LOAD_PEAK}"
  meta_row "load_samples" "${n_samples}"
  meta_row "routes_total" "${n_read}"
  meta_row "routes_expected" "${n_expected}"
  meta_row "rows_written" "${n_rows}"
  meta_row "skipped_total" "${n_skipped}"
  meta_row "skipped_method" "${n_skip_method}"
  meta_row "skipped_sse" "${n_skip_sse}"
  meta_row "skipped_sideeffect" "${n_skip_effect}"
  awk -F'\t' '
    { c[$5]++ }
    END {
      n = split("ok method_not_allowed bad_request not_found other_4xx server_error timeout unexpected_status", ord, " ")
      for (i = 1; i <= n; i++) { k = ord[i]; printf "#meta\tclass_%s\t%d\t0\tmeta\tclass_%s=%d\n", k, (k in c ? c[k] : 0), k, (k in c ? c[k] : 0) }
      for (k in c) {
        if (k !~ /^(ok|method_not_allowed|bad_request|not_found|other_4xx|server_error|timeout|unexpected_status)$/) {
          printf "#meta\tclass_%s\t%d\t0\tmeta\tclass_%s=%d\n", k, c[k], k, c[k]
        }
      }
    }' "${WORKDIR}/data.tsv"
} > "${OUT}"; then
  die "输出文件写入失败：${OUT}"
  exit 73
fi

# ─────────────────────────── 汇总 ───────────────────────────
{
  printf '\n================ api-sweep 汇总 ================\n'
  printf '路由清单          : %s\n' "${ROUTES}"
  printf '清单总行数        : %s\n' "${n_read}"
  printf '期望巡检路由数    : %s\n' "${n_expected}"
  printf '实际写出数据行数  : %s\n' "${n_rows}"
  printf '跳过 - 非 GET     : %s\n' "${n_skip_method}"
  printf '跳过 - SSE 长连接 : %s\n' "${n_skip_sse}"
  printf '跳过 - 带副作用   : %s\n' "${n_skip_effect}"
  printf -- '---- 失败分类计数 ----\n'
  awk -F'\t' '
    { c[$5]++ }
    END {
      n = split("ok method_not_allowed bad_request not_found other_4xx server_error timeout unexpected_status", ord, " ")
      for (i = 1; i <= n; i++) { k = ord[i]; printf "  %-20s %d\n", k, (k in c ? c[k] : 0) }
    }' "${WORKDIR}/data.tsv"
  printf -- '---- 耗时 > 2000ms 的路由（按耗时倒序）----\n'
  slow="$(awk -F'\t' '$3 + 0 > 2000' "${WORKDIR}/data.tsv" | sort -t$'\t' -k3,3nr)"
  if [[ -n "${slow}" ]]; then
    printf '%s' "${slow}" | awk -F'\t' '{ printf "  %-52s %9s ms  %-20s %s\n", $1, $3, $5, $6 }'
  else
    printf '  （无）\n'
  fi
  printf -- '---- 跳过的 SSE 端点（★ 未经本次 GET-only 巡检验证）----\n'
  if [[ -s "${WORKDIR}/skip_sse.txt" ]]; then
    sed 's/^/  /' "${WORKDIR}/skip_sse.txt"
  else
    printf '  （无）\n'
  fi
  printf -- '---- 跳过的非 GET 端点（★ 未经本次 GET-only 巡检验证）----\n'
  if [[ -s "${WORKDIR}/skip_method.txt" ]]; then
    sed 's/^/  /' "${WORKDIR}/skip_method.txt"
  else
    printf '  （无）\n'
  fi
  printf -- '---- 跳过的带副作用端点（★ 未经本次 GET-only 巡检验证）----\n'
  if [[ -s "${WORKDIR}/skip_effect.txt" ]]; then
    sed 's/^/  /' "${WORKDIR}/skip_effect.txt"
  else
    printf '  （无）\n'
  fi
  printf -- '---- 宿主机负载（1 分钟 load average）----\n'
  printf '  load_start  : %s\n' "${LOAD_START}"
  printf '  load_end    : %s\n' "${LOAD_END}"
  printf '  load_peak   : %s\n' "${LOAD_PEAK}"
  printf '  采样点数    : %s\n' "${n_samples}"
  printf '  阈值        : %s（★ 核数 %s × 2）\n' "${LOAD_THRESHOLD}" "$(cpu_count)"
  printf '  ★ 慢读数请对照本节：机器饱和时的耗时不能当端点缺陷。\n'
  printf '===============================================\n'
  printf '结果 TSV: %s\n' "${OUT}"
} >&2

# ★★★ R157 轮（D16）：★ load 超阈值 ⇒ ★ **退出码 75**（★ 临时性失败，★ 与 0 区分开）。
#   ★ ★ 为什么要有退出码：★ 「读数不可信」这件事如果**只**写在 stderr 上，
#   ★ ★ 在管道里 / CI 里 / 别人的脚本里**都会被丢掉**（★ 与 §409 通用式四同源）。
#   ★ ★ 为什么是 75（★ 不是 1）：★ 1 通常被当成「工具坏了」⇒ ★ 75 = 临时性失败
#   ★ ★ ⇒ ★ 调用方可以**明确地**决定「这批延迟数据要不要用」。
#   ★ ★ 注意：★ **状态码与分类仍然有效** ⇒ ★★ 不要整份丢弃 ⇒ ★★ 用 meta 自标来过滤。
if [[ "${LOAD_TRUST}" != "trustworthy" ]]; then
  printf '★ 退出码 75：★ load 峰值 %s > 阈值 %s ⇒ ★ **延迟**读数不可信（★ 状态码/分类仍可信）\n' \
    "${LOAD_PEAK}" "${LOAD_THRESHOLD}" >&2
  exit 75
fi

exit 0
