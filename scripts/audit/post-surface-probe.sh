#!/usr/bin/env bash
# post-surface-probe.sh —— ★ 「POST 面」只读探测器（★ 覆盖【注册了但从未用 POST 验证过】的端点）
#
# # 它查什么
#
# ★ 本仓绝大多数 admin 路由是用**不带方法前缀**的形式注册的：
#
#     mux.HandleFunc("/api/admin/xxx", adminWrap(h.handleXxx))
#
# ⇒ ★★ Go 的 `http.ServeMux` 对这种注册**接受任意 HTTP 方法** ⇒ ★★★ **POST 也会被受理**。
# ⇒ 而 `scripts/audit/api-sweep.sh`（★ 日常巡检）**只发 GET**
# ⇒ ★★★ **⇒ 存在一片「注册了但从未用 POST 验证过」的端点面。**
#
# ★ 本脚本对每条路由发**空 body 的 POST**（★ `{}`），★ 并按【四档】分类结果：
#
#   ① `method-not-registered` —— 404 / 405 ⇒ ★★ **该 path+method 组合没注册** ⇒ ★ 零副作用
#   ② `validated`            —— 400 / 401 / 403 / 409 / 422 ⇒ ★★ **参数校验前置 ⇒ 可证明零副作用**
#   ③ `executed`             —— 2xx ⇒ ★★★ **它真的执行了** ⇒ ★★ 副作用已经发生 ⇒ ★ 需人工裁决
#   ④ `error`                —— 5xx ⇒ ★★★ **它执行了但失败** ⇒ ★★ 需人工裁决
#                                     （★ 且往往是「参数校验缺失」⇒ ★★ **缺陷候选**）
#
# ★★ ②这一档是【可证明的】：★ 空 body ⇒ ★★ 必然在副作用之前被 4xx 拦下
# ⇒ ★★ **⇒ 该端点的 POST 面【已被验证】（校验层）**，★ 且**零副作用**。
#
# # ★★ 安全模型（★ 三层，★ 都可单独失效而不致命）
#
#   ① **静态分级**（★ `post-surface-classify.py`）⇒ ★★ 只探测 `read` 类路由
#      （★ `write` / `external` / `unknown` **一律不探测** ⇒ ★★ 写库、外呼、解析不出来的都进人工清单）
#   ② **目标确认**（★ `CONFIRM_TARGET` 必须与 BASE 的 host:port 逐字相同）⇒ ★★ 防止误打生产
#   ③ **写库护栏**（★ `GUARD_DSN` 可选）⇒ ★★ 每批探测后核对关键表行数 ⇒ ★★ 一旦有变化 ⇒ 立刻中止并报出
#
# ★★ **诚实边界**：★ 静态分级是**词法级**的 ⇒ ★★ 它**看不穿调用链**
# （★ handler 调 `h.doThing()` 而 `doThing` 内部写库 ⇒ ★★ 分级会判成 `read`）。
# ⇒ ⇒ ★★★ **⇒ 所以真正的安全网是【护栏 ③】与【快照回滚】，★ 不是分级本身。**
#
# # 用法
#
#   bash scripts/audit/post-surface-probe.sh probe
#   bash scripts/audit/post-surface-probe.sh classify < 探测结果.tsv
#   bash scripts/audit/post-surface-probe.sh ledger < 探测结果.tsv> <分级.tsv>
#
# ★ 环境变量：
#   BASE          默认 http://127.0.0.1:8782
#   COOKIE        admin cookie jar（★ 路径；★ **绝不回显**）
#   OUT           输出 TSV（★ 默认 /tmp/post-surface-probe.tsv）
#   CLASSIFY_TSV  静态分级结果（★ 默认由 post-surface-classify.py 现算）
#   CONFIRM_TARGET  必须等于 BASE 的 host:port（★ 防误打）
#   GUARD_DSN     可选；★ 设了就启用写库护栏
#   BATCH         每批条数（★ 默认 25）
#   TIMEOUT       单请求超时（★ 默认 20s）
#
# # 退出码
#
#   0 = 全部落在 ①②（★ 零副作用、★ POST 面已验证）
#   2 = 存在 ③④（★ 有端点真的执行了 ⇒ ★ 需人工裁决）
#   3 = 探测失败（★ 连不上 / 护栏触发中止）
#   64 = 用法/环境错误
set -uo pipefail

MODE="${1:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

BASE="${BASE:-http://127.0.0.1:8782}"
COOKIE="${COOKIE:-}"
OUT="${OUT:-${TMPDIR:-/tmp}/post-surface-probe.tsv}"
CLASSIFY_TSV="${CLASSIFY_TSV:-}"
CONFIRM_TARGET="${CONFIRM_TARGET:-}"
GUARD_DSN="${GUARD_DSN:-}"
BATCH="${BATCH:-25}"   # ★ 保留兼容（★ ★★ 护栏已改为【全部逐条】核对 ⇒ ★ 本变量不再决定护栏粒度）
TIMEOUT="${TIMEOUT:-20}"

# ★★★ **排除清单**（★ 「已实测观察到会写库」的路由 ⇒ ★★ 绝不批量探测）
EXCLUDE_ROUTES="${EXCLUDE_ROUTES:-${SCRIPT_DIR}/asset/unsafe-post-routes.txt}"
EXCLUDE_APPLY=1
if [[ ! -f "$EXCLUDE_ROUTES" ]]; then EXCLUDE_APPLY=0; fi

die() { printf 'FATAL: %s\n' "$*" >&2; }

usage() {
  cat >&2 <<'USAGE'
用法：
  bash scripts/audit/post-surface-probe.sh probe
  bash scripts/audit/post-surface-probe.sh classify < 探测结果.tsv
  bash scripts/audit/post-surface-probe.sh ledger < 探测结果.tsv> <分级.tsv>

★ 只发**空 body 的 POST**（★ `{}`）；★ 只探测静态分级判为 `read` 的路由。
★ 写库 / 外呼 / 解析不出来的路由 **一律不探测**（★ 进人工清单）。
USAGE
}

# ─────────────────────────── 目标确认（★ 防误打） ───────────────────────────
base_hostport() {
  local h="$1"
  h="${h#*://}"
  h="${h%%/*}"
  h="${h%%:*}"
  local p="$1"
  p="${p#*://}"
  p="${p%%/*}"
  p="${p##*:}"
  [[ "$p" == "$h" ]] && p=""
  printf '%s:%s' "$h" "$p"
}

# ─────────────────────────── classify（★ 判定逻辑与网络分开 ⇒ 门不需要服务） ───────────────────────────
classify_file() {
  local src="$1"
  local n mnr val exe err verdict code
  # ★★ 计数一律用 awk（★ `grep -vc` 在空输入/多文件时会输出多行 ⇒ ★★ `[[ "0\n0" -gt 0 ]]` 语法错）
  n="$(LC_ALL=C awk '!/^#/{c++} END{print c+0}' "$src" 2>/dev/null)"
  mnr="$(LC_ALL=C awk 'BEGIN{FS="\t"} !/^#/ && $6 ~ /^method-not-registered$/{c++} END{print c+0}' "$src" 2>/dev/null)"
  val="$(LC_ALL=C awk 'BEGIN{FS="\t"} !/^#/ && $6 ~ /^validated$/{c++} END{print c+0}' "$src" 2>/dev/null)"
  exe="$(LC_ALL=C awk 'BEGIN{FS="\t"} !/^#/ && $6 ~ /^executed$/{c++} END{print c+0}' "$src" 2>/dev/null)"
  err="$(LC_ALL=C awk 'BEGIN{FS="\t"} !/^#/ && $6 ~ /^error$/{c++} END{print c+0}' "$src" 2>/dev/null)"

  if [[ $(( exe + err )) -gt 0 ]]; then verdict="needs-manual-review"; code=2
  else verdict="post-surface-verified"; code=0; fi

  {
    printf '════════ POST 面（空 body 探测）════════\n'
    printf '判定            : %s\n' "$verdict"
    printf '退出码          : %s\n' "$code"
    printf '探测条数        : %s\n' "${n:-0}"
    printf '  ① method-not-registered : %s   （★ 该 path+method 没注册 ⇒ 零副作用）\n' "${mnr:-0}"
    printf '  ② validated            : %s   （★ 参数校验前置 ⇒ 可证明零副作用）\n' "${val:-0}"
    printf '  ③ executed             : %s   （★ 它真的执行了 ⇒ 需人工裁决）\n' "${exe:-0}"
    printf '  ④ error                : %s   （★ 执行了但失败 ⇒ 需人工裁决）\n' "${err:-0}"
    printf '判读：★ ①② = 「POST 面已验证且零副作用」；★ ③④ = 「有空 body 就执行的端点」。\n'
    printf '      ⇒ ★★ ③④ 的处置手段**各不相同**（★ 补参数校验 / 加门 / 加幂等）⇒ ★★ 需逐条裁决。\n'
  } >&2
  printf '#meta\tverdict\t%s\n' "$verdict"
  printf '#meta\texit_code\t%s\n' "$code"
  printf '#meta\tprobed\t%s\n' "${n:-0}"
  printf '#meta\tclass_method_not_registered\t%s\n' "${mnr:-0}"
  printf '#meta\tclass_validated\t%s\n' "${val:-0}"
  printf '#meta\tclass_executed\t%s\n' "${exe:-0}"
  printf '#meta\tclass_error\t%s\n' "${err:-0}"
  cat "$src"
  return "$code"
}


# ─────────────────────────── ledger（★ 台账：★ 一行一路由，★ 裁决列【机器可推导】） ───────────────────────────
# ★★ 裁决列的推导规则（★ ★★ 全部可从「分级 + 探测结果」重算 ⇒ ★★ 门能验它 ⇒ ★★ 它不是「人工裁决」）
#   post-verified-no-side-effect : 探测回 404/405/4xx ⇒ ★★ 可证明零副作用
#   post-executes-on-empty-body  : 探测回 2xx/5xx      ⇒ ★★★ 空 body 就执行
#   excluded-observed-writer     : 已被实测观察到写库 ⇒ ★★ 不参与批量探测
#   not-probed-writes            : 静态分级判「写」   ⇒ ★★ 不探测
#   not-probed-external          : 静态分级判「外呼」 ⇒ ★★ 不探测
#   not-probed-unresolved        : 静态分级判「未知」 ⇒ ★★ 不探测
ledger_mode() {
  local probe_file="$1" class_file="$2"
  [[ -f "$probe_file" ]] || { die "探测结果不存在：${probe_file}"; exit 64; }
  [[ -f "$class_file" ]] || { die "分级文件不存在：${class_file}"; exit 64; }
  local excluded="$EXCLUDE_ROUTES"
  # ★★ 排除清单必须【存在】⇒ ★★ 不存在就造一个空的（★ 让 awk 的 FILENAME 方案始终有三个文件）
  if [[ ! -f "$excluded" ]]; then
    excluded="$(mktemp "${TMPDIR:-/tmp}/post-surface-excl.XXXXXX")" || exit 64
    : > "$excluded"
  fi
  printf '#gen_tool\tpost-surface-probe.sh ledger\n'
  printf '#gen_classify\t%s\n' "$(basename "$class_file")"
  printf '#gen_probe\t%s\n' "$(basename "$probe_file")"
  printf '#gen_target\t%s\n' "$(LC_ALL=C awk -F'\t' '$1=="#meta" && $2=="target"{print $3}' "$probe_file")"
  printf '#gen_verdict_rule\tverdict=derive(class,bucket,excluded)\n'
  printf '#gen_exclude_asset\t%s\n' "$(basename "$excluded")"
  # ★★★★ ★★ **三个输入文件用【FILENAME】区分**（★ 不用 `-v` 传多行内容）：
  # ★★ ★★ awk 的 `-v` 会【处理转义序列】，★ 而多行字符串里含真换行 ⇒ ★★ 直接报
  # ★★ `awk: newline in string` ⇒ ★★ ⇒ **多行数据必须走【文件】而不是【变量】**。
  LC_ALL=C awk -F'\t' -v OFS='\t' \
    -v exclfile="$excluded" -v classfile="$class_file" -v probefile="$probe_file" '
    FILENAME == exclfile { if ($1 !~ /^#/) ex[$1] = 1; next }
    FILENAME == classfile {
      if ($1 ~ /^#/) next
      path = $1
      cls[path] = $5; bchk[path] = $7; mtd[path] = $2; src[path] = $3
      order[++n] = path
      next
    }
    FILENAME == probefile {
      if ($1 ~ /^#/) next
      bucket[$1] = $6; code[$1] = $4
    }
    END {
      for (i = 1; i <= n; i++) {
        p = order[i]
        b = (p in bucket) ? bucket[p] : ""
        c = (p in code) ? code[p] : ""
        if (p in ex) { v = "excluded-observed-writer"; note = "★ 实测：空 body 会写库" }
        else if (b == "method-not-registered" || b == "validated") { v = "post-verified-no-side-effect"; note = "★ 可证明零副作用" }
        else if (b == "executed" || b == "error") { v = "post-executes-on-empty-body"; note = "★ 空 body 就执行（★ 需逐条裁决）" }
        else if (cls[p] == "write") { v = "not-probed-writes"; note = "★ 静态分级：写库" }
        else if (cls[p] == "external") { v = "not-probed-external"; note = "★ 静态分级：出网调用" }
        else if (cls[p] == "unknown") { v = "not-probed-unresolved"; note = "★ 静态分级：解析不出" }
        else { v = "not-probed-not-reached"; note = "★ ★ 分级说只读却没探测到 ⇒ 需查" }
        print p, mtd[p], src[p], cls[p], (p in bchk ? bchk[p] : ""), c, b, v, note
      }
    }' "$excluded" "$class_file" "$probe_file"
}

case "$MODE" in
  classify)
    [[ -z "${2:-}" ]] && { die "classify 模式需要一个结果文件参数"; usage; exit 64; }
    src="${2}"
    [[ -f "$src" ]] || { die "结果文件不存在：${src}"; exit 64; }
    classify_file "$src"; exit $?
    ;;
  ledger)
    [[ -z "${2:-}" || -z "${3:-}" ]] && { die "ledger 模式需要两个参数：<探测结果.tsv> <分级.tsv>"; usage; exit 64; }
    ledger_mode "${2}" "${3}"
    exit $?
    ;;
  probe) ;;
  -h|--help|help) usage; exit 0 ;;
  *) printf 'FATAL: 未知模式（可用：probe / classify / --help）\n' >&2; usage; exit 64 ;;
esac

# ─────────────────────────── probe ───────────────────────────
[[ -n "$COOKIE" ]] || { die "COOKIE 未设（★ admin cookie jar 路径）"; exit 64; }
[[ -f "$COOKIE" ]] || { die "cookie jar 不存在：${COOKIE}"; exit 64; }

target="$(base_hostport "$BASE")"
if [[ -z "$CONFIRM_TARGET" ]]; then
  printf 'FATAL: CONFIRM_TARGET 未设（★ 必须逐字等于目标的 host:port = %s）\n' "$target" >&2
  printf '      ★ 这是防止误打生产的闸门 ⇒ ★ 要跑就显式确认一次。\n' >&2
  exit 64
fi
if [[ "$CONFIRM_TARGET" != "$target" ]]; then
  printf 'FATAL: CONFIRM_TARGET=%s 与实际目标 %s 不一致 ⇒ 拒绝探测\n' "$CONFIRM_TARGET" "$target" >&2
  exit 64
fi

# ★ 静态分级
if [[ -z "$CLASSIFY_TSV" ]]; then
  CLASSIFY_TSV="$(mktemp "${TMPDIR:-/tmp}/post-surface-classify.XXXXXX")" || exit 64
  python3 "${SCRIPT_DIR}/post-surface-classify.py" > "$CLASSIFY_TSV" || {
    printf 'FATAL: 静态分级失败\n' >&2; exit 3; }
fi
[[ -f "$CLASSIFY_TSV" ]] || { die "分级文件不存在：${CLASSIFY_TSV}"; exit 64; }

# ★ 护栏基线（★ 可选）
GUARD_TABLES="${GUARD_TABLES:-session_turns session_summaries session_bodies credentials credential_model_bindings}"
# ★★★ **PSQL 可覆盖**（★ R159 实测踩到的坑）：★ 本机 PATH 里的 `psql` 可能是**别的项目的转发脚本**
# ★★★ （★ `~/.local/bin/psql` ⇒ 连的是 acc-pg17，★ 而本项目在 54318）⇒ ★★ 于是「护栏连不上」，
# ★★★ **而真实原因是【psql 打错库】**，★ 不是「库没起」。
PSQL="${PSQL:-psql}"
guard_snapshot() {
  local dsn="$1" tables="$2"
  local q="" t
  for t in $tables; do
    q="${q}select '${t}' as t, count(*) as n from ${t}; "
  done
  [[ -z "$q" ]] && return 0
  # ★★ DSN 是【完整 URL】⇒ ★★ 必须解析出 host/port/user/db + 口令（★ 口令只进环境变量，★ 不进 argv）
  # ★★★★★ ★★ **【必须拆成多行 local】** —— bash 的 `local a=… b=$a` 陷阱：
  # ★★ 同一句 `local` 里，★ 后面的赋值看到的是【声明前的旧值（空）】★ 而**不是**前面刚赋的值。
  # ★★ ⇒ ⇒ ★★ host/port/user/db 全空 ⇒ ★★ psql 静默失败（★ `2>/dev/null` 还把报错吞了）
  # ★★ ⇒ ★★ **读数变成「空」而不是「报错」⇒ ★★ 极难定位。**
  local rest="${dsn#*://}"
  local cred="${rest%%@*}"
  local hostpart="${rest#*@}"
  hostpart="${hostpart%%\?*}"
  local user="${cred%%:*}"
  local pw="${cred#*:}"
  [[ "$pw" == "$user" ]] && pw=""
  local hp="${hostpart%%/*}"
  local host="${hp%%:*}"
  local port="${hp##*:}"
  [[ "$port" == "$host" ]] && port=""
  local db="${hostpart#*/}"
  # ★★★★ ★★ **必须把 stdin 隔离掉**（★ R159 实测踩到）：★ 这个函数是在【读文件的 while 循环】里调的
  # ★★ ⇒ ★★ 循环的 stdin 就是那个文件 ⇒ ★★ 这里的 `sort` 会把【剩余的探测清单】当成输入吃掉
  # ★★ ⇒ ★★★ **⇒ 402 条只跑了 26 条**，★ 而退出码仍然是 2（★ 看起来「跑完了」）。
  # ★★ ★★ **排序【绝不能】再喂 </dev/null**（★ R159 实测踩到）：★ `sort` 的 stdin 本来就是
  # ★★ **管道里 psql 的输出** ⇒ ★★ 那才是对的读法；★ ★★ 加了 `</dev/null` ⇒ ★★ **管道数据被丢掉**
  # ★★ ⇒ ★★ 护栏输出恒空 ⇒ ★★ 「护栏连不上库」⇒ ★★ **错误信息完全指错了方向**。
  # ★★ ★★ 而 psql 的 stdin 是【循环的文件】⇒ ★★ psql 用 `</dev/null` 隔断（★ psql -c 本来也不读 stdin，
  # ★★ 但【显式隔离】才是不依赖「它恰好不读」这种运气）。
  PGPASSWORD="$pw" "$PSQL" -X -q -A -t -F $'\t' \
    -h "$host" -p "$port" -U "$user" -d "$db" -c "$q" 2>/dev/null </dev/null | sort
}

GUARD_BASE=""
if [[ -n "$GUARD_DSN" ]]; then
  # ★★ 护栏的【身份自报】⇒ ★★ 读数必须说清「连的是哪个库」
  guard_identity() {
    local dsn="$1"
    # ★★ 同样必须拆行（★ 见 guard_snapshot 里的说明）
    local rest="${dsn#*://}"
    local cred="${rest%%@*}"
    local hostpart="${rest#*@}"
    hostpart="${hostpart%%\?*}"
    local user="${cred%%:*}"
    local pw="${cred#*:}"
    [[ "$pw" == "$user" ]] && pw=""
    local hp="${hostpart%%/*}"
    local host="${hp%%:*}"
    local port="${hp##*:}"
    [[ "$port" == "$host" ]] && port=""
    local db="${hostpart#*/}"
    PGPASSWORD="$pw" "$PSQL" -X -q -A -t -F $'\t' \
      -h "$host" -p "$port" -U "$user" -d "$db" \
      -c "select 'db_identity', current_database(), current_user, inet_server_addr()::text, inet_server_port()::text, pg_postmaster_start_time()::text, pg_is_in_recovery()::text;" 2>/dev/null </dev/null
  }
  # ★★ 脱敏后的 DB 目标（★ **绝不打印口令**）
  GUARD_DSN_TARGET="$(printf '%s' "$GUARD_DSN" | sed -E 's#://[^:]+:[^@]+@#://***:***@#')"
  GUARD_ID="$(guard_identity "$GUARD_DSN")"
  GUARD_BASE="$(guard_snapshot "$GUARD_DSN" "$GUARD_TABLES")"
  # ★★★ **原始基线**（★ 只读、★ 不被逐条护栏更新）⇒ ★★ 最终沉降复核拿它比对
  GUARD_BASE_ORIG="$GUARD_BASE"
  if [[ -z "$GUARD_BASE" || -z "$GUARD_ID" ]]; then
    printf 'FATAL: 护栏连不上库 ⇒ 拒绝探测（★ 护栏是安全网，★ 不能没有）\n' >&2
    printf '  ★ 我要连的目标 = %s\n' "$GUARD_DSN_TARGET" >&2
    printf '  ★ 用的 psql   = %s（★ ★★ PATH 里的 psql 可能是【别的项目的转发脚本】！）\n' "$(command -v "$PSQL" || printf '%s' "$PSQL")" >&2
    printf '      ⇒ ★★ 若 psql 路径可疑，★ 用 PSQL=/绝对路径/psql 覆盖后再跑。\n' >&2
    printf '      ⇒ ★★ 也可能库真的没起（★ 先看端口是否在监听）。\n' >&2
    exit 3
  fi
fi

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/post-surface-probe.XXXXXX")" || exit 64
trap 'rm -rf "$WORKDIR"' EXIT

# ★ 只探测 `read` 类（★ write / external / unknown 一律不探测 ⇒ 进人工清单）
# ★★★ **分级升级**（★ 关键安全设计）：
# ★★★   第一梯队 = ★★ 「校验了 body」的路由 ⇒ ★★ 空体 POST **可证明**必然 4xx ⇒ ★★ 零副作用
# ★★★   第二梯队 = ★★ 「没校验 body」的路由 ⇒ ★★ 空体 POST **会真的执行** ⇒ ★★ 靠护栏兜底
# ★★★ ⇒ ★★ **先跑第一梯队、护栏绿了再跑第二梯队** ⇒ ★★ **爆炸半径按梯队递增**。
awk -F'\t' '!/^#/ && $5=="read"{ if ($7!="") print "0\t"$1"\t"$2"\t"$3"\t"$7; else print "1\t"$1"\t"$2"\t"$3"\t"$7 }' \
  "$CLASSIFY_TSV" | sort -t$'\t' -k1,1 > "$WORKDIR/probe-list-all.tsv"

# ★★ 剔除排除清单里的路由（★ ★★ 它们已被实测观察到会写库）
: > "$WORKDIR/probe-list.tsv"
: > "$WORKDIR/excluded.tsv"
if [[ "$EXCLUDE_APPLY" -eq 1 ]]; then
  LC_ALL=C awk -F'\t' 'NR==FNR{ex[$1]=1; next} !($2 in ex)' \
    "$EXCLUDE_ROUTES" "$WORKDIR/probe-list-all.tsv" > "$WORKDIR/probe-list.tsv"
  LC_ALL=C awk -F'\t' 'NR==FNR{ex[$1]=1; next} ($2 in ex)' \
    "$EXCLUDE_ROUTES" "$WORKDIR/probe-list-all.tsv" > "$WORKDIR/excluded.tsv"
fi
n_excluded="$(LC_ALL=C awk 'END{print NR+0}' "$WORKDIR/excluded.tsv")"
n_list="$(awk 'END{print NR+0}' "$WORKDIR/probe-list.tsv")"
n_tier1="$(awk -F'\t' '$1=="0"{c++} END{print c+0}' "$WORKDIR/probe-list.tsv")"
n_tier2="$(awk -F'\t' '$1=="1"{c++} END{print c+0}' "$WORKDIR/probe-list.tsv")"

: > "$OUT"
: > "$WORKDIR/writers.tsv"
{
  printf '#meta\tprobe_version\t1\n'
  printf '#meta\ttarget\t%s\n' "$target"
  printf '#meta\tbase\t%s\n' "$BASE"
  printf '#meta\troutes_total\t%s\n' "$(awk '!/^#/{c++} END{print c+0}' "$CLASSIFY_TSV")"
  printf '#meta\tprobe_list\t%s\n' "$n_list"
  printf '#meta\ttier1_body_checked\t%s\n' "$n_tier1"
  printf '#meta\ttier2_no_body_check\t%s\n' "$n_tier2"
  printf '#meta\texcluded_observed_writers\t%s\n' "${n_excluded:-0}"
  printf '#meta\tguard\t%s\n' "$( [[ -n "$GUARD_DSN" ]] && printf 'on' || printf 'off' )"
  printf '#meta\tguard_psql\t%s\n' "$( [[ -n "$GUARD_DSN" ]] && (command -v "$PSQL" || printf '%s' "$PSQL") || printf '-' )"
  # ★★ 身份行必须带 `#` 前缀（★ 否则会被当成【数据行】混进计数里）
  if [[ -n "$GUARD_ID" ]]; then printf '#%s\n' "$GUARD_ID"; fi
} > "$OUT"

if [[ "$n_list" -eq 0 ]]; then
  printf 'FATAL: 探测清单为空（★ 静态分级没产出 read 类）\n' >&2
  exit 3
fi

subst() { printf '%s' "$1" | sed -E 's/[{][a-zA-Z0-9_]+([.][.][.])?[}]/1/g'; }

batch_no=0
bumped=0
prev_tier=""
# ★★★ **循环从【专用 FD 3】读清单**（★ 而不是 stdin）⇒ ★★ 子命令随便碰 stdin 都不会吃掉清单。
while IFS=$'\t' read -r tier path method src bcheck <&3; do
  [[ -z "$path" ]] && continue
  src_class="$(awk -F'\t' -v p="$path" '$1==p{print $5; exit}' "$CLASSIFY_TSV")"
  probe_path="$(subst "$path")"
  body_file="$WORKDIR/body"
  code="$(curl -s -m "$TIMEOUT" -o "$body_file" -w '%{http_code}' \
      -X POST "${BASE}${probe_path}" \
      -H 'Content-Type: application/json' \
      -b "$COOKIE" \
      --data '{}' </dev/null 2>/dev/null)"
  [[ -z "$code" ]] && code="000"
  # ★★★★★ ★★ **snippet 只保留【可打印 ASCII】**（★ R159 实测踩到）：
  # ★★ ① `LC_ALL=C`（★ 否则 body 里有非 UTF-8 字节 ⇒ ★★ `tr: Illegal byte sequence` ⇒ ★★ snippet 变空串）
  # ★★ ② `tr -cd '[:print:]'`（★ ★★ 否则**响应体里的非 UTF-8 字节会写进 TSV** ⇒
  # ★★    ⇒ ★★ 后续读这个 TSV 的 **BWK awk 会报 `multibyte conversion failure`** ⇒ ★★ **连计数都算不出来**
  # ★★    ⇒ ★★ ★★ **「输出文件不可被 awk 读」比「输出里有乱码」严重得多**）
  snippet="$(head -c 120 "$body_file" 2>/dev/null | LC_ALL=C tr '\t\n\r' '   ' | LC_ALL=C tr -cd '[:print:]')"
  case "$code" in
    404|405) bucket="method-not-registered" ;;
    400|401|403|409|422) bucket="validated" ;;
    2??) bucket="executed" ;;
    5??) bucket="error" ;;
    *) bucket="other-$code" ;;
  esac
  # ★★ 列序：path, method, src, code, body_check, bucket, snippet
  #    ★★★ **bucket 必须在第 6 列**（★ `classify` 模式按 $6 计数 ⇒ ★★ 两个模式必须同构）
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$path" "$method" "$src" "$code" "$bcheck" "$bucket" "$snippet" >> "$OUT"

  batch_no=$(( batch_no + 1 ))
  # ★★★ **梯队边界** ⇒ ★★ 护栏在这里再核一次（★ 第一梯队全绿才进第二梯队）
  if [[ "$tier" == "1" && "$prev_tier" == "0" ]]; then
    if [[ -n "$GUARD_DSN" ]]; then
      now="$(guard_snapshot "$GUARD_DSN" "$GUARD_TABLES")"
      if [[ "$now" != "$GUARD_BASE" ]]; then
        printf 'FATAL: 写库护栏在【梯队切换点】触发 ⇒ 中止探测\n' >&2
        printf '  基线:\n%s\n' "$GUARD_BASE" >&2
        printf '  现在:\n%s\n' "$now" >&2
        bumped=1
        break
      fi
    fi
    printf '⇒ 第一梯队（可证明零副作用）已完成 ⇒ 护栏绿 ⇒ 进入第二梯队\n' >&2
  fi
  prev_tier="$tier"
  # ★★★★ **护栏粒度 = 爆炸半径**（★ R159 实测教训）：
  # ★★ 第一版每 **25** 条才核一次 ⇒ ★★ 护栏触发时**已经污染了 17 行**（★ session_summaries 2427→2444）
  # ★★ ⇒ ★★★ **⇒ 第二梯队必须【逐条】核对** ⇒ ★★ 一次触发最多污染【1 条】。
  # ★★★★ ★★ **护栏必须【全部逐条】核对**（★ R159 实测判别性实验）：
  # ★★ `/api/telemetry/decision-log` 是【同步写】⇒ ★★ 它的 delta 应当在它被探测的那一刻可见
  # ★★ ⇒ ★★★ **但护栏报的是别的路由** ⇒ ★★★ **⇒ 因为它在【第一梯队】而第一梯队【每 25 条才核一次】**
  # ★★ ⇒ ★★★★ **⇒ 归因偏移量 = 检查间隔条数** ⇒ ★★★ **⇒ 只逐条核对【第二梯队】是不够的**。
  # ★★ ★★ （★ 与「检查频率 = 爆炸半径」互补：★ 同一频率，★ 一边是【损失上限】，★ 一边是【归因精度】。）
  need_check=1
  if [[ "$need_check" -eq 1 && -n "$GUARD_DSN" ]]; then
    now="$(guard_snapshot "$GUARD_DSN" "$GUARD_TABLES")"
    if [[ "$now" != "$GUARD_BASE" ]]; then
      if [[ "${GUARD_MODE:-stop}" == "stop" ]]; then
        printf 'FATAL: 写库护栏触发 ⇒ 关键表行数变了 ⇒ ★ 中止探测（★ 请回滚快照）\n' >&2
        printf '  ★★★ 触发时刚探测的路由 = %s （★ 就是它写的）\n' "$probe_path" >&2
        printf '  ★ 该路由的静态分级 = %s（★ ★★ 若分级说它「只读」⇒ ★★ 那是【分级漏判】）\n' "$src_class" >&2
        printf '  基线:\n%s\n' "$GUARD_BASE" >&2
        printf '  现在:\n%s\n' "$now" >&2
        bumped=1
        break
      fi
      # ★★★ **continue 模式**（★ 只用于【本地一次性环境】+ ★ 有快照可回滚）：
      # ★★ **记录**这个写入者 ⇒ ★★ **更新基线** ⇒ ★★ **继续探测**
      # ★★ ⇒ ★★★ **一次跑完 ⇒ 拿到【全部】写入者** ⇒ ★★ 最后统一回滚。
      printf '%s\t%s\t%s\n' "$probe_path" "$src_class" "$bcheck" >> "$WORKDIR/writers.tsv"
      printf '★ 护栏：探测到写库 ⇒ %s （★ 分级=%s）⇒ ★ 记录为【疑似】并继续\n' "$probe_path" "$src_class" >&2
      printf '  ★★★ ★★ 注意：★ 这只是【疑似】⇒ ★★ **不能当成归因**：★ 写入可能是【异步】的\n' >&2
      printf '  ★★★ ★★ ⇒ ★★ 观测时刻与写入时刻有偏移 ⇒ ★★ **归因必须靠【差分实验】**（★ 排除一个，再测一次）\n' >&2
      GUARD_BASE="$now"
    fi
  fi
done 3< "$WORKDIR/probe-list.tsv"

if [[ "$bumped" -eq 1 ]]; then
  exit 3
fi

# ★★★ **自检：★ 写出的行数必须等于探测清单数**（★ 对不上 ⇒ ★ 循环被提前打断了 ⇒ ★ 直接报错）
n_rows="$(LC_ALL=C awk '!/^#/{c++} END{print c+0}' "$OUT" 2>/dev/null)"
if [[ "$n_rows" -ne "$n_list" ]]; then
  printf 'FATAL: 写出行数 %s ≠ 探测清单 %s ⇒ ★ 循环被提前打断（★ 请查 stdin 隔离）\n' \
    "$n_rows" "$n_list" >&2
  exit 3
fi

# ★★★★★ ★★ **沉降复核**（★ settle check）：
# ★★ R159 实测：★ `/api/telemetry/request-log` 是【队列异步】写入 ⇒ ★★ **请求返回后立刻核对必然漏**
# ★★ （★ 实测：★ 基线 2427 → 探测后 2432，★ 而逐条护栏只抓到 3 个 ⇒ ★★ **2 个是异步写的**）
# ⇒ ⇒ ★★★ **⇒ 护栏的【观测时机】必须晚于写入的实际发生时机** ⇒ ★★ 这里【等一会儿再核一次】。
# ★★★★★ ★★ **沉降复核必须【重复到稳定】且【比对【原始基线】】** —— R159 实测两次教训：
# ★★ ① 单次等待 10 秒就判「clean」⇒ ★★★ 但**随后**又落了 9 行（★ 异步写滞后于窗口）
# ★★ ② ★★★ 那些行是**上一轮**探测的异步写 ⇒ ★★★ **异步写会跨越「回滚」边界**
# ★★    ⇒ ⇒ ★★★ **⇒ 回滚之后必须【等队列排空】再取基线**，★ 否则上轮的异步写会落在本轮基线之后。
# ★★ ⇒ ⇒ ★★★ **⇒ 最终判读必须比对【原始基线】**（★ 不是被逐条护栏更新过的基线）。
SETTLE_SEC="${SETTLE_SEC:-10}"
SETTLE_ROUNDS="${SETTLE_ROUNDS:-3}"
if [[ -n "$GUARD_DSN" ]]; then
  settle="$GUARD_BASE_ORIG"
  stable=0
  prev=""
  for _r in $(seq 1 "$SETTLE_ROUNDS"); do
    sleep "$SETTLE_SEC"
    settle="$(guard_snapshot "$GUARD_DSN" "$GUARD_TABLES")"
    if [[ "$settle" == "$prev" ]]; then stable=$(( stable + 1 )); else stable=0; fi
    prev="$settle"
    [[ "$stable" -ge 2 ]] && break
  done
  if [[ "$settle" != "$GUARD_BASE_ORIG" ]]; then
    printf '#meta\tsettle_check\tasync-write-detected\n' >> "$OUT"
    printf '⇒ ★★★ 沉降复核发现【异步写入】（★ 请求返回后才落库 ⇒ ★★ 逐条护栏漏掉了）\n' >&2
    printf '  ★ 原始基线（★ 本轮开始时）:\n%s\n' "$GUARD_BASE_ORIG" >&2
    printf '  沉降后:\n%s\n' "$settle" >&2
  else
    printf '#meta\tsettle_check\tclean\n' >> "$OUT"
  fi
fi

# ★★★ **写入者清单**（★ ★★ 「静态分级漏判了哪些」⇒ ★★ 这是分级器需要改进的证据）
if [[ -s "$WORKDIR/writers.tsv" ]]; then
  printf '#meta\tsuspected_writers\t%s\n' "$(LC_ALL=C awk 'END{print NR+0}' "$WORKDIR/writers.tsv")" >> "$OUT"
  # ★★★★★ ★★ **诚实边界必须落在【输出字段】上**（★ 不是只写在注释里）：
  # ★★ R159 实测：★ 「疑似写入者」列表**每轮探测都完全不同**（★ dashboard/operational、data-horizon、
  # ★★ dispatch/waterfall、extension-points、format-anomaly-summary、fp-slot-policy …）
  # ★★ ⇒ ★★★★ **⇒ 逐条护栏的「归因」是【随机漂移】，★ 毫无信息量** ⇒ ★★ **⇒ 不能当成「谁写了库」的证据**。
  # ★★ ⇒ ★★ **可靠的是【delta 存在】（★ 阴性对照证实）；★ 不可靠的是【delta 归因】。**
  printf '#meta\tsuspected_writers_trusted\tno\t★ ★★ 每轮漂移 ⇒ ★★ 不可作归因（★ 归因要靠差分实验）\n' >> "$OUT"
  LC_ALL=C awk -F'\t' '{print "#suspected-writer\t"$1"\t"$2}' "$WORKDIR/writers.tsv" >> "$OUT"
  printf '⇒ ★★★ 本次探测共【疑似】%s 个「空 body 就写库」的路由 ⇒ ★★ 已记录在 #suspected-writer 行\n' \
    "$(LC_ALL=C awk 'END{print NR+0}' "$WORKDIR/writers.tsv")" >&2
fi

classify_file "$OUT" > /dev/null 2>&1
rc=$?
# ★ 把判定 meta 补进输出（★ 读数自标）
{
  printf '#meta\tverdict\t%s\n' "$( [[ $rc -eq 0 ]] && printf 'post-surface-verified' || printf 'needs-manual-review' )"
  printf '#meta\texit_code\t%s\n' "$rc"
} >> "$OUT"
exit "$rc"
