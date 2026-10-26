#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# check-v1-family-retirement.sh —— v1「request_logs」族退役状态检查器（**只读**）
#
# WHAT
# ----
# 本仓的长期目标之一是「把 request_logs 及其相关表退役、全部切到 session_turns 会话族」。
# 退役的**执行件**是迁移 890（sql/migrations/startup/890_drop_request_logs_v1_family.sql）：
# 它把 v1 族整族删掉，并把 canonical 视图 `request_logs_with_current_month`
# 换成**会话两臂体**（`FROM public.session_turns_hot … UNION ALL FROM public.session_turns …`）。
#
# ★★ 仓内已有一道门 cmd/tools/sql_source_indirection_audit/manifest_test.go，
#   但它**只扫仓内 SQL 文本** ⇒ ★★ 它查不到「生产/开发库里的 v1 表被复活」。
#   本脚本补的就是这个缺口：对**任意** PG DSN 跑一次 ⇒ 报告「v1 族退役状态」。
#
# ★★ 只读（硬约束）
#   全部语句都是 SELECT / pg_class / pg_namespace / pg_get_viewdef 之类的元数据查询；
#   **绝不**写库。
#   ⇒ 它对生产是**零写入**的：跑一次只会产生一条 SELECT 的读。
#
# 判定口径（什么叫「退役完好」）
# --------------------------
#   retired     exit 0   v1 基表 0 个 **且** v1 臂视图 0 个 **且** 接管视图确实读会话族
#   degraded    exit 2   v1 表已退役，但接管视图**没读会话族**（或接管视图缺失）⇒ 半退役
#   v1-revived  exit 3   **有 v1 基表或 v1 臂视图存在** ⇒ 这一档要报警
#
#   另有一个非判定类退出码（不是判定结果，是脚本自己出错了）：
#     64  用法/环境错误（没给 DSN、psql 不在 PATH、清单读不了、探库失败）
#
#   ★ 判定顺序是**从坏到好**：先看 v1 基表与 v1 臂视图（存在 ⇒ v1-revived），
#     再看接管视图（不读会话族 ⇒ degraded），最后才是 retired。
#     ⇒ 「接管视图的体里还引用 v1 关系」也算 v1-revived（不是 degraded）：
#       那种形态要么是 v1 表被复活、要么是视图指向**已不存在**的关系（运行期 42P01）
#       ⇒ 两种都比「半退役」更严重。
#
# 判定用的两个真值源（**各一份，不重复**）
# --------------------------------------
#   V1_REL_RE       「这个名字属于 v1 族吗」——**唯一**一份判据。
#                   用**族形态正则**而不是逐个表名枚举，是刻意的：
#                   枚举（`request_logs` / `_request_logs` / `request_logs_hot` / …）
#                   漏一个变体就是漏一次复活（仓内已登记的教训：§9.226.3 的
#                   「精确相等 + 前缀兜底」多真相源问题 —— 行为被两个真相源同时决定，
#                   任何删掉其中一个的变异都测不出来）。
#                   ★ 形如 `request_logs_<任何后缀>`（含 `_` 前缀那一代物理名）
#                     一律算 v1 族 ⇒ 新增变体不会漏。
#   CANONICAL_VIEWS canonical 视图名集合（当前只有 `request_logs_with_current_month`）。
#                   ★ 它**不是** v1 关系（与仓内 v1Tables 的口径一致：
#                     视图名本来就不在基表集合里），只是「会话族接管视图」。
#                   ★ 所以 `routing_analytics_source`（体里读 canonical 视图）
#                     **不会**被误判成 v1 臂视图。
#
# 两种模式（★ 关键设计：把「判定逻辑」与「查库」分开）
# --------------------------------------------------
#   probe     从 LLM_GATEWAY_DATABASE_URL（或 $1 传入的 DSN）查库，
#             生成一份「对象清单」，然后把清单喂给 classify。
#   classify  从 **stdin** 读一份「对象清单 TSV」，输出判定结果。
#             ★★ 门（scripts/ursmcheck/v1_family_retirement_gate_test.go）
#               用**合成清单**测这个模式 ⇒ 不需要真库 ⇒ 秒级。
#
# 对象清单 TSV 的格式（**没有表头**，一行一个对象）
# -----------------------------------------------
#   第 1 列  kind  对象类型：`table` / `view` / `other`
#                     （也接受 pg_class.relkind 的字母：r/p ⇒ table，v ⇒ view，m ⇒ other）
#   第 2 列  name  关系名（public schema，不带 schema 前缀）
#   第 3 列  body  视图定义原文（pg_get_viewdef，制表符/换行折叠成空格）；
#                     表与其它对象留空
#
#   ★★ 清单里**没有**预计算的布尔列（「这个视图还引用 v1 吗」「接管视图读没读会话族」）
#      ⇒ 这两个事实**全部由 classify 从 name/body 现场推导**。
#      ⇒ 门才能用合成清单直接测判定逻辑（而不是测「探针脚本有没有算好」）。
#   ★ 清单里 `#` 开头的行是注释，classify 会跳过。
#
# 输出
# ----
#   stdout  机器读的 TSV：
#             `#meta<TAB>verdict<TAB>retired|degraded|v1-revived`
#             `#meta<TAB>exit_code<TAB>0|2|3`
#             `#meta<TAB>v1_base_tables<TAB><n>` …（其余 meta 键见下）
#             `object<TAB><kind><TAB><name><TAB><role>`
#             ⇒ 别的脚本 `script.sh … > out.tsv` 就能直接消费。
#   stderr  人读的摘要（判定 + 计数 + 命中清单）。
#
# ★★ 绝不打印 DSN 或口令
#   ★ DSN 由脚本自己拆开用（实测约束见「DSN 解析」那一节的注释）：
#     · host/port/user/dbname ⇒ psql 命令行选项（不是秘密）
#     · password             ⇒ **PGPASSWORD 环境变量**（不进 argv、不会打印）
#     · 其它连接参数         ⇒ 0600 临时 service file（**里面没有口令**）
#   ★ psql 的 stderr 在打印前会被**逐字抹掉** DSN 与口令（scrub），
#     只留「连不上 / 认证失败 / 语法错」这类可读信息。
#   ★★ 已知偏差：DSN 里的百分号编码（%40 之类）**不做解码**
#     ⇒ 用 URI 形态且口令含特殊字符时，请改用 conninfo 形态
#     （`host=… port=… dbname=… user=… password=…`）或 libpq 原生环境变量。
#
# ★ 已知偏差（读数之前必须知道）
# -------------------------------
#   ① **只看 public schema**：其它 schema 里的同名对象不查。
#   ② **只看关系**（表/视图/物化视图）：序列、索引、触发器、函数、策略不查。
#      ⇒ 残留的 `request_logs_id_seq` 之类**不会**影响判定（它既不是基表也不是视图）。
#   ③ 视图体的关系名提取只看 **FROM/JOIN 之后的第一个标识符** ⇒
#      嵌在子查询里的关系名可能取不到。
#      ⇒ 缓解：任何**活着的** v1 基表都会作为对象出现在清单里 ⇒ 由名字判据兜住；
#      取不到名字的只有「表已经没了、视图还指着它」这一形态 ⇒ 那正是 v1-revived 要报的。
#   ④ **不递归**检查二级视图（`routing_analytics_source` 读的是 canonical 视图，
#      不是会话表本身）⇒ 它不在接管检查范围。
#      ⇒ 接管视图若被改回 v1 体，二级视图不会被单独标记，但接管视图会被标记
#        （要么判 v1-revived，要么判 degraded）⇒ 报警仍然发生。
#   ⑤ **只判「对象还在不在」，不判数据完整性**（不数行、不比历史）。
#   ⑥ 接管视图缺失 ⇒ 判 degraded（不判 retired）：口径上「退役完好」要求接管视图在位。
#      ⇒ 在没建 canonical 视图的库上跑，得到 degraded 是**口径的必然结果**，不是缺陷。
#
# 复跑命令
# --------
#   # 探库（DSN 只经环境变量；本脚本不会把它打印出来）
#   LLM_GATEWAY_DATABASE_URL='postgres://user@host:5432/llm_gateway?sslmode=disable' \
#     bash scripts/audit/check-v1-family-retirement.sh probe
#
#   # 也可以把 DSN 作为第一个参数
#   bash scripts/audit/check-v1-family-retirement.sh probe "$DSN"
#
#   # 只判不探（喂合成清单，秒级；CI/门用这条）
#   bash scripts/audit/check-v1-family-retirement.sh classify < inventory.tsv
#
#   # 退出码：0 retired / 2 degraded / 3 v1-revived / 64 用法或环境错误
# ---------------------------------------------------------------------------
set -uo pipefail

# ─────────────────────────── 判定用的真值源 ───────────────────────────
#
# ★ V1_REL_RE：**唯一**一份「这个名字属于 v1 族吗」的判据。
#   · `_?`         ⇒ 覆盖两代物理名（带下划线的那一代 / 不带的那一代）
#   · `(_.*)?`     ⇒ 覆盖 hot / bodies / bodies_hot / archive / 未来任何变体
#   ⇒ 形如 `request_logs_<任意后缀>` 的关系名一律算 v1 族。
#
# ★ 为什么不用逐个表名枚举（仓内教训）：
#   枚举（10 个名字）漏一个变体 = 漏一次复活，而且**测不出来**
#   （枚举少一项时，其余项仍能命中 ⇒ 门照样绿）⇒ 必须是**族形态**。
V1_REL_RE='^_?request_logs(_.*)?$'

# ★ CANONICAL_VIEWS：会话族「接管视图」的名字（当前只有这一个）。
#   ★ 它**不是** v1 关系（与仓内 v1Tables 的口径一致：视图名不在基表集合里），
#     所以它出现在别人的视图体里时**不算** v1 引用
#     （否则 routing_analytics_source 会被误判成 v1 臂视图）。
CANONICAL_VIEWS='request_logs_with_current_month'

# ★ SESSION_FAMILY_RE：会话族的 turn 源关系（`session_turns` 与它的 hot 表）。
SESSION_FAMILY_RE='^session_turns(_hot)?$'

# ★ PG_SERVICE_NAME：service file 里的服务名（残余连接参数走它）。
#   ★ service 文件是 0600 临时文件，里面**没有口令**（口令走 PGPASSWORD 环境变量）。
PG_SERVICE_NAME='llmgw_v1_retirement_check'

# ─────────────────────────── 探库用的 SQL（**只读**） ───────────────────────────
#
# ★ 只查 pg_class / pg_namespace / pg_get_viewdef ⇒ 纯元数据读。
# ★ 只取 public schema（已知偏差 ①）。
# ★ 只取关系（表/分区表/视图/物化视图）（已知偏差 ②）。
PROBE_SQL="SELECT
  CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'table' WHEN 'v' THEN 'view' ELSE 'other' END,
  c.relname,
  CASE WHEN c.relkind = 'v'
       THEN replace(replace(coalesce(pg_get_viewdef(c.oid), ''), E'\t', ' '), E'\n', ' ')
       ELSE '' END
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m')
ORDER BY 2"

# ★★★★ R157 轮新增：**库身份自报**（★ 两侧对照 ⇒ 「我以为连的是谁」vs 「库说它是谁」）。
#
# ★★ 为什么必须加（★ 本轮实测踩到的）：
#   ★ 本机 PATH 里的 `psql` 是**另一个项目的 docker shim**（★ 硬编码转发到 `acc-pg17`）
#   ⇒ ★★ 于是「psql 退出码 1 + 一个别的容器的名字」⇒ ★★ 报错**只说「psql 失败」**，
#      ★★ **不说「你连的不是你以为的那个库」** ⇒ ★★ 人只能去猜。
#   ⇒ ★★★ 修法 = ★ **让读数自带身份** ⇒ ★ 失败时也要说「我要连的是 host:port/db」。
#
# ★★ 身份取 6 项（★ 全部来自**库自己**，★ 不含口令）：
#   current_database / current_user / inet_server_addr:port /
#   pg_postmaster_start_time（★ 证明是哪一个 PG 实例）/ pg_is_in_recovery（★ 副本要能看出来）
IDENTITY_SQL="SELECT current_database(), current_user, COALESCE(inet_server_addr()::text,'(local)'), COALESCE(inet_server_port()::text,'(unix)'), pg_postmaster_start_time()::text, pg_is_in_recovery()::text"

# ─────────────────────────── classify 的判定程序（awk） ───────────────────────────
#
# ★★ 判定逻辑**全部**在这里 ⇒ probe 与 classify 共用同一份实现，
#    而门可以直接用合成清单测它（不需要真库）。
#
# ★★ 为什么把 awk 程序写成临时文件而不是 `AWK_PROG="$(cat <<'EOF' …)"`：
#   ★★ bash 的坑（实测）：heredoc 嵌在 `"$( … )"` 里时，**body 里的双引号会被计数**
#      ⇒ body 里只要有**奇数**个双引号（例如 `gsub(/[",]/, "", s)`），
#        整个脚本就报 "unexpected EOF while looking for matching `"'"。
#   ⇒ 正解 = heredoc 放在**顶层**（函数体内的 heredoc 也可以），
#     写进临时文件再 `awk -f`。
AWK_FILE=""

cleanup() {
  if [[ -n "${AWK_FILE}" && -f "${AWK_FILE}" ]]; then
    rm -f "${AWK_FILE}"
  fi
}
trap cleanup EXIT

write_awk_prog() {
  AWK_FILE="$(mktemp "${TMPDIR:-/tmp}/v1-awk.XXXXXX")" || return 64
  cat > "${AWK_FILE}" <<'AWK_EOF'
# norm_rel：把一个关系名归一（小写、去引号、去 schema 前缀）。
function norm_rel(tok,   s) {
  s = tolower(tok)
  gsub(/[",]/, "", s)
  sub(/^.*\./, "", s)
  return s
}
# is_v1_rel：这个名字属于 v1 族吗（canonical 视图不算 v1 关系）。
function is_v1_rel(s) {
  if (s == "") return 0
  if (s in canonical) return 0
  return (s ~ v1_re)
}
# is_session_rel：这个名字属于会话族 turn 源吗。
function is_session_rel(s) {
  if (s == "") return 0
  return (s ~ session_re)
}
# extract_refs：抽出视图体里 FROM/JOIN 之后的关系名（空格分隔）。
function extract_refs(body,   words, nw, i, lw, out, tok) {
  out = ""
  nw = split(body, words, /[^A-Za-z0-9_.]+/)
  for (i = 1; i < nw; i++) {
    lw = tolower(words[i])
    if (lw == "from" || lw == "join") {
      tok = norm_rel(words[i + 1])
      if (tok != "" && tok != "select" && tok != "lateral" && tok != "only" && tok != "values") {
        out = out " " tok
      }
    }
  }
  return out
}
# body_refs_v1：这个视图体还引用 v1 族关系吗。
function body_refs_v1(refs,   rl, n, i) {
  n = split(refs, rl, " ")
  for (i = 1; i <= n; i++) if (is_v1_rel(rl[i])) return 1
  return 0
}
# body_refs_session：这个视图体读会话族吗（refs 优先，正则兜底）。
function body_refs_session(body, refs,   rl, n, i) {
  n = split(refs, rl, " ")
  for (i = 1; i <= n; i++) if (is_session_rel(rl[i])) return 1
  if (body ~ /(^|[^A-Za-z0-9_])session_turns(_hot)?([^A-Za-z0-9_]|$)/) return 1
  return 0
}
BEGIN {
  FS = "\t"
  canonical[""] = 0
  nv = split(canonical_views, cl, ",")
  for (i = 1; i <= nv; i++) {
    cn = norm_rel(cl[i])
    if (cn != "") canonical[cn] = 1
  }
  n = 0; n_v1_tables = 0; n_v1_arm_views = 0; n_v1_leftovers = 0
  n_takeover = 0; to_session = 0
}
{
  # ★★ R157 轮新增：**身份行必须在通用 `#` 跳过之前捕获**（★ 否则它会被当注释丢掉）。
  #
  # ★★★★ 判据的可移植性（本轮实测踩到）：
  #   ★ **awk 的【正则字面量】里的 `\t` 不是所有实现都当制表符**
  #     （★ BWK awk / macOS 自带 awk 把它当字面的 't' ⇒ `/^#identity\t/` 匹配不上）
  #   ★★ 而 **awk 的【字符串字面量】里的 `\t` 是 POSIX 标准**（★ 所有实现都支持）
  #   ⇒ ★★ 正解：★ 判据用**不带 `\t` 的正则**（`/^#identity/`）+ ★ 取分隔用 `index($0, "\t")`
  #   ⇒ ★★★ **同族教训**：★ 「文本判据必须剥到对的那一层」⇒ ★★ 这里还要再加一条
  #     **「正则写在哪一层也有讲究」** ⇒ ★★ **同一段文本，★ 在正则字面量里和字符串字面量里语义不同**。
  if ($0 ~ /^#identity/) {
    line = $0
    p = index(line, "\t")
    if (p > 0) line = substr(line, p + 1)
    db_identity = line
    next
  }
  if ($0 ~ /^[ \t]*#/) next
  kind = $1; name = $2; body = $3
  gsub(/^[ \t]+/, "", kind); gsub(/[ \t]+$/, "", kind)
  gsub(/^[ \t]+/, "", name); gsub(/[ \t]+$/, "", name)
  if (kind == "r" || kind == "p") kind = "table"
  else if (kind == "v") kind = "view"
  else if (kind == "m") kind = "other"
  if (name == "") next
  n++
  nname = norm_rel(name)
  refs = extract_refs(body)
  is_tbl = 0; is_arm = 0; is_to = 0; is_left = 0
  if (kind == "table" && is_v1_rel(nname)) is_tbl = 1
  if (kind == "view") {
    if (is_v1_rel(nname)) is_arm = 1
    if (body_refs_v1(refs)) is_arm = 1
    if (nname in canonical) {
      is_to = 1
      if (body_refs_session(body, refs)) to_session = 1
    }
  }
  if (kind == "other" && is_v1_rel(nname)) is_left = 1
  if (is_tbl) n_v1_tables++
  if (is_arm) n_v1_arm_views++
  if (is_left) n_v1_leftovers++
  if (is_to) { n_takeover++; takeover_name = name }
  obj_kind[n] = kind; obj_name[n] = name
  if (is_tbl) obj_role[n] = "v1-table"
  else if (is_arm) obj_role[n] = "v1-arm-view"
  else if (is_left) obj_role[n] = "v1-leftover"
  else if (is_to) obj_role[n] = "session-takeover"
  else obj_role[n] = "neutral"
  if (is_tbl) v1_list = v1_list "  · " name "\n"
  if (is_arm) arm_list = arm_list "  · " name "\n"
}
END {
  if (n_v1_tables > 0 || n_v1_arm_views > 0) { verdict = "v1-revived"; code = 3 }
  else if (n_takeover == 0 || to_session == 0) { verdict = "degraded"; code = 2 }
  else { verdict = "retired"; code = 0 }
  if (n_takeover > 0) to_present = "true"; else to_present = "false"
  if (to_session > 0) to_sess = "true"; else to_sess = "false"
  if (takeover_name == "") takeover_name = "(none)"
  print "#meta\tverdict\t" verdict
  print "#meta\texit_code\t" code
  print "#meta\tv1_base_tables\t" n_v1_tables
  print "#meta\tv1_arm_views\t" n_v1_arm_views
  print "#meta\tv1_leftover_objects\t" n_v1_leftovers
  print "#meta\ttakeover_present\t" to_present
  print "#meta\ttakeover_reads_session\t" to_sess
  print "#meta\ttakeover_view\t" takeover_name
  print "#meta\tobjects_total\t" n
  print "#meta\tdb_identity\t" (db_identity == "" ? "(not provided)" : db_identity)
  for (i = 1; i <= n; i++) print "object\t" obj_kind[i] "\t" obj_name[i] "\t" obj_role[i]
  if (summary_file != "") {
    s = "════════ v1 族退役检查 ════════\n"
    s = s "判定            : " verdict "\n"
    s = s "退出码          : " code "\n"
    s = s "v1 基表         : " n_v1_tables "\n"
    s = s v1_list
    s = s "v1 臂视图       : " n_v1_arm_views "\n"
    s = s arm_list
    s = s "接管视图        : " takeover_name "（在库：" to_present "；读会话族：" to_sess "）\n"
    s = s "残留其它对象    : " n_v1_leftovers "\n"
    s = s "对象总数        : " n "\n"
    if (verdict == "retired") s = s "判读：v1 族已退役，接管视图读会话族 ⇒ 完好。\n"
    else if (verdict == "degraded") s = s "判读：v1 表没了，但接管视图没读会话族（或缺失）⇒ 半退役。\n"
    else s = s "判读：v1 基表或 v1 臂视图还在 ⇒ 有人在复活 v1 族，需人工确认。\n"
    print s > summary_file
    close(summary_file)
  }
  exit code
}
AWK_EOF
  return 0
}

usage() {
  cat >&2 <<'USAGE'
用法：
  bash scripts/audit/check-v1-family-retirement.sh probe [DSN]
      探库并判定。DSN 缺省取环境变量 LLM_GATEWAY_DATABASE_URL。
      ★ DSN 不会被打印。

  bash scripts/audit/check-v1-family-retirement.sh classify < inventory.tsv
      从 stdin 读「对象清单 TSV」并判定（不需要真库）。

退出码：0 retired / 2 degraded / 3 v1-revived / 64 用法或环境错误。
USAGE
}

# ─────────────────────────── classify：从清单判定 ───────────────────────────
#
# ★ 参数：$1 = 清单文件（"-" 表示 stdin）
classify_file() {
  local src="$1"
  local ts sum rc=0
  ts="$(mktemp "${TMPDIR:-/tmp}/v1-verdict.XXXXXX")" || return 64
  sum="$(mktemp "${TMPDIR:-/tmp}/v1-summary.XXXXXX")" || return 64
  awk -v v1_re="${V1_REL_RE}" \
      -v canonical_views="${CANONICAL_VIEWS}" \
      -v session_re="${SESSION_FAMILY_RE}" \
      -v summary_file="${sum}" \
      -f "${AWK_FILE}" "${src}" > "${ts}" || rc=$?
  if [[ -s "${sum}" ]]; then
    cat "${sum}" >&2
  fi
  cat "${ts}"
  rm -f "${ts}" "${sum}"
  return "${rc}"
}

# ─────────────────────────── DSN 解析（bash） ───────────────────────────
#
# ★★ 为什么要自己解析（**实测**出来的三条约束，不是猜的）：
#
#   ① `PGDATABASE` **只认纯库名**：给它一条 URI 或 conninfo 字符串，
#      libpq 会**整条忽略**（实测：连到了 socket + OS 用户，库根本没选上）
#      ⇒ ⇒ **不能**用 `PGDATABASE=<整条 DSN>` 这种写法。
#   ② Debian 的 `/usr/bin/psql` 是个 wrapper，它**无条件**设 `PGHOST`
#      （实测 wrapper 源码第 117 行）⇒ 而 libpq 的优先级是
#      **命令行 > 环境变量 > service file** ⇒ ⇒ 只靠 service file 会被 wrapper 压掉。
#   ③ service file 里的 `dbname=<URI>` **不会**被当成 URI 展开（实测）。
#
# ⇒ ⇒ 正解（与仓内 scripts/audit/psql-isolated.sh 同款思路）：
#   · host / port / user / dbname ⇒ 走 **psql 命令行选项**（优先级最高，环境变量压不过）
#   · password                   ⇒ 走 **PGPASSWORD 环境变量**（不进 argv、不会打印）
#   · 其它连接参数               ⇒ 走 **service file**（0600 临时文件，无口令）
#   ⇒ ★ **口令既不在 argv 里，也不在磁盘上的 service file 里**。
#
# 解析结果（全局变量）：
#   DSN_HOST / DSN_PORT / DSN_USER / DSN_DBNAME / DSN_PASSWORD / DSN_EXTRA
#   DSN_EXTRA 是 `k=v&k=v` 形态的**残余**连接参数。
parse_dsn() {
  local dsn="$1"
  DSN_HOST=""; DSN_PORT=""; DSN_USER=""; DSN_DBNAME=""; DSN_PASSWORD=""; DSN_EXTRA=""
  case "${dsn}" in
    postgres://*|postgresql://*)
      local rest="${dsn#*://}" params="" dbname="" userinfo="" hostport=""
      local user="" password="" host="" port=""
      case "${rest}" in
        *\?*) params="${rest#*\?}"; rest="${rest%%\?*}" ;;
      esac
      case "${rest}" in
        */*) dbname="${rest#*/}"; rest="${rest%%/*}" ;;
      esac
      case "${rest}" in
        *@*) userinfo="${rest%@*}"; hostport="${rest#*@}" ;;
        *)   hostport="${rest}" ;;
      esac
      case "${userinfo}" in
        *:*) user="${userinfo%%:*}"; password="${userinfo#*:}" ;;
        *)   user="${userinfo}" ;;
      esac
      case "${hostport}" in
        \[*\]*) host="${hostport%%\]*}"; host="${host}]"
                case "${hostport}" in *\]:*) port="${hostport#*]:}" ;; esac ;;
        *:*)   host="${hostport%%:*}"; port="${hostport#*:}" ;;
        *)     host="${hostport}" ;;
      esac
      DSN_HOST="${host}"; DSN_PORT="${port}"; DSN_USER="${user}"
      DSN_DBNAME="${dbname}"; DSN_PASSWORD="${password}"; DSN_EXTRA="${params}"
      ;;
    *)
      # conninfo 形态：`key=value` 空格分隔
      # ★ 引号里带空格的值不做拆分（libpq conninfo 语法的细节）⇒ 记为已知偏差。
      local kv k v
      for kv in ${dsn}; do
        k="${kv%%=*}"; v="${kv#*=}"
        case "${k}" in
          host)     DSN_HOST="${v}" ;;
          port)     DSN_PORT="${v}" ;;
          user)     DSN_USER="${v}" ;;
          dbname)   DSN_DBNAME="${v}" ;;
          password) DSN_PASSWORD="${v}" ;;
          *)        DSN_EXTRA="${DSN_EXTRA}${DSN_EXTRA:+&}${k}=${v}" ;;
        esac
      done
      ;;
  esac
}

# dsn_param_env_name：某个连接参数有没有对应的 PG* 环境变量。
#
# ★ 只认**确实存在**的环境变量（libpq 文档里的那批）⇒ 认错会让参数静默失效。
# ★ 认不出来的参数走 service file（见 probe_db）⇒ 宁可多写一个临时文件，
#   也不要「以为传了其实没传」。
dsn_param_env_name() {
  case "$1" in
    sslmode)             printf 'PGSSLMODE' ;;
    connect_timeout)     printf 'PGCONNECT_TIMEOUT' ;;
    application_name)    printf 'PGAPPNAME' ;;
    client_encoding)     printf 'PGCLIENTENCODING' ;;
    options)             printf 'PGOPTIONS' ;;
    target_session_attrs) printf 'PGTARGETSESSIONATTRS' ;;
    sslrootcert)         printf 'PGSSLROOTCERT' ;;
    sslcert)             printf 'PGSSLCERT' ;;
    sslkey)              printf 'PGSSLKEY' ;;
    sslcrl)              printf 'PGSSLCRL' ;;
    requirepeer)         printf 'PGREQUIREPEER' ;;
    channel_binding)     printf 'PGCHANNELBINDING' ;;
    *)                   printf '' ;;
  esac
}

# ─────────────────────────── probe：查库 → 清单 → 判定 ───────────────────────────
probe_db() {
  local dsn="$1"
  local inv err needles rc=0
  if ! command -v psql >/dev/null 2>&1; then
    printf 'FATAL: PATH 里没有 psql ⇒ 无法探库（用 classify 模式喂清单即可）\n' >&2
    return 64
  fi
  inv="$(mktemp "${TMPDIR:-/tmp}/v1-inv.XXXXXX")" || return 64
  err="$(mktemp "${TMPDIR:-/tmp}/v1-err.XXXXXX")" || return 64
  needles="$(mktemp "${TMPDIR:-/tmp}/v1-needles.XXXXXX")" || return 64

  # ★★ 需要抹掉的串：完整 DSN + 单独的口令（URI 形态与关键字形态各抽一次）。
  {
    printf '%s\n' "${dsn}"
    printf '%s\n' "${dsn}" | sed -nE 's#^[a-zA-Z][a-zA-Z0-9+.-]*://[^@/]*:([^@]*)@.*#\1#p'
    printf '%s\n' "${dsn}" | sed -nE 's#.*password=([^[:space:];]+).*#\1#p'
  } > "${needles}"

  local -a psql_args=(-X -q -A -t -F $'\t' -v ON_ERROR_STOP=1)
  local -a pg_env=("PGCONNECT_TIMEOUT=${PGCONNECT_TIMEOUT:-10}" "PGAPPNAME=llmgw-v1-retirement-check")
  local svc=""

  if [[ -n "${dsn}" ]]; then
    parse_dsn "${dsn}"
    [[ -n "${DSN_HOST}" ]]   && psql_args+=(-h "${DSN_HOST}")
    [[ -n "${DSN_PORT}" ]]   && psql_args+=(-p "${DSN_PORT}")
    [[ -n "${DSN_USER}" ]]   && psql_args+=(-U "${DSN_USER}")
    [[ -n "${DSN_DBNAME}" ]] && psql_args+=(-d "${DSN_DBNAME}")
    [[ -n "${DSN_PASSWORD}" ]] && pg_env+=("PGPASSWORD=${DSN_PASSWORD}")
    # ★ 残余连接参数：有 PG* 环境变量对应项的走环境变量，其余走 service file。
    if [[ -n "${DSN_EXTRA}" ]]; then
      local svc_lines="" kv k v envname
      local IFS_SAVE="${IFS}"
      while IFS= read -r kv; do
        [[ -z "${kv}" ]] && continue
        k="${kv%%=*}"; v="${kv#*=}"
        envname="$(dsn_param_env_name "${k}")"
        if [[ -n "${envname}" ]]; then
          pg_env+=("${envname}=${v}")
        else
          svc_lines="${svc_lines}${kv}"$'\n'
        fi
      done < <(printf '%s\n' "${DSN_EXTRA}" | tr '&' '\n')
      IFS="${IFS_SAVE}"
      if [[ -n "${svc_lines}" ]]; then
        svc="$(mktemp "${TMPDIR:-/tmp}/v1-pgsservice.XXXXXX")" || return 64
        chmod 600 "${svc}"
        { printf '[%s]\n' "${PG_SERVICE_NAME}"; printf '%s' "${svc_lines}"; } > "${svc}"
        pg_env+=("PGSERVICEFILE=${svc}" "PGSERVICE=${PG_SERVICE_NAME}")
      fi
    fi
  elif [[ -n "${PGHOST:-}${PGDATABASE:-}${PGUSER:-}" ]]; then
    # ★ 没有 DSN，但 libpq 的原生环境变量在 ⇒ 直接用（不解析、不落盘、不进 argv）。
    #   ★ PGPASSWORD 也由继承的环境带进来 ⇒ 这里**不要**再显式加一个空值：
    #   空的 PGPASSWORD 会让 libpq 用「空口令」而不是回退到 ~/.pgpass。
    :
  fi

  # ★★★★ R157 轮新增：**先算「我要连的目标」标签**（★ 只含 host:port/db，★ 不含口令）。
  #   ★ 用途：★ 失败时也要说清「我本来要连的是谁」⇒ ★ 否则读者只能猜。
  local target=""
  if [[ -n "${dsn}" ]]; then
    target="${DSN_HOST:-?}:${DSN_PORT:-?}/${DSN_DBNAME:-?}"
  else
    target="(env) ${PGHOST:-local}:${PGPORT:-?}/${PGDATABASE:-?}"
  fi

  # ★★ 身份自报（★ 单独一次查询 ⇒ ★ 与清单查询解耦 ⇒ ★ 身份查询失败不致命）。
  #   ★ psql_args 此时还没有 -c ⇒ ★ 这里先追加身份语句、跑完、**再把基座还原**。
  #   ★★★★ 还原是必须的：★ psql 支持**多个 -c**（★ 按顺序执行）⇒ ★ 若不还原，
  #     ★★ 清单查询会**顺带再跑一遍身份查询** ⇒ ★★ 身份那一行会混进清单里 ⇒ ★★ 判定读数被污染。
  local ident=""
  local -a base_args=("${psql_args[@]}")
  psql_args+=(-c "${IDENTITY_SQL}")
  ident="$(env -u PGSERVICEFILE -u PGSERVICE "${pg_env[@]}" psql "${psql_args[@]}" 2>/dev/null | head -1)" || ident=""
  psql_args=("${base_args[@]}")

  psql_args+=(-c "${PROBE_SQL}")
  # ★ 先把继承来的 PGSERVICEFILE / PGSERVICE **清掉**：环境里若残留一个空串，
  #   libpq 会直接报 "service file \"\" not found"（实测）⇒ 不能让它漏进来。
  #   需要 service file 时下面会显式重新设置。
  env -u PGSERVICEFILE -u PGSERVICE "${pg_env[@]}" psql "${psql_args[@]}" > "${inv}" 2> "${err}" || rc=$?
  if [[ -n "${svc}" ]]; then rm -f "${svc}"; fi
  # ★★ 身份行：★ **成功与失败共用同一个 printf**（★ 单一真值源 ⇒ ★ 格式只有一处）。
  #   ★★ 为什么必须共用（★ 本轮变异验证实测）：★ 若两条分支各写一个 printf，
  #     ★★ 门喂的**手写身份行**就只覆盖其中一条 ⇒ ★★ **改另一条的门看不见**
  #     ★★ ⇒ ★★ **门永远绿，而产物已经错了** ⇒ ★★ **⇒ 格式必须是单一真值源**。
  #   ★★ 内部分隔符用 **`|`** 而不是 TAB：★ `#meta` 行是**三字段 TSV**
  #     （★ `#meta` / 键 / 值）⇒ ★★ 值里若带 TAB ⇒ ★★ 这一行就变成多字段 ⇒
  #     ★★ 取值方（`f[2]`）只拿到**第一个片段** ⇒ ★★ 读数被静默截断。
  #     ⇒ ★★ **TSV 的值里不能有 TSV 分隔符** ⇒ ★★ 这是格式契约，不是风格问题。
  emit_identity_line() {
    local t="$1" id="$2"
    local f_db f_user f_server f_pms f_rec
    if [[ -n "${id}" ]]; then
      f_db="$(printf '%s' "${id}" | cut -f1)"
      f_user="$(printf '%s' "${id}" | cut -f2)"
      f_server="$(printf '%s' "${id}" | cut -f3):$(printf '%s' "${id}" | cut -f4)"
      f_pms="$(printf '%s' "${id}" | cut -f5)"
      f_rec="$(printf '%s' "${id}" | cut -f6)"
    else
      f_db="(unknown)"; f_user="(unknown)"; f_server="(unknown)"
      f_pms="(unknown)"; f_rec="(unknown)"
    fi
    # ★★★★ **这行 printf 是身份行的【唯一真值源】**（★ 成功与失败都走它）。
    #   ★★ 为什么必须唯一（★ 本轮变异验证实测）：★ 若按「成功/失败」分两个 printf，
    #     ★★ 门覆盖的那条分支改了，另一条**门看不见** ⇒ ★★ **实测 M3 变异：门依然绿**。
    #   ⇒ ★★ **⇒ 格式只能有一处** ⇒ ★★ 字段值不同没关系，★ 格式一样就行。
    printf '#identity\trequested=%s|db=%s|user=%s|server=%s|postmaster_start=%s|in_recovery=%s\n' \
      "${t}" "${f_db}" "${f_user}" "${f_server}" "${f_pms}" "${f_rec}"
  }

  if [[ "${rc}" -ne 0 ]]; then
    # ★★★ 失败时**也要**把身份行印到 stdout（★ 读者才知道「我要连的是谁、它是谁」）。
    emit_identity_line "${target}" "${ident}"
    printf 'FATAL: 探库失败（psql 退出码 %s）★ 我要连的目标 = %s\n' "${rc}" "${target}" >&2
    printf '★ 提示：本机 PATH 里的 psql 可能是【别的项目的转发脚本】⇒ ★ 它会连到别的容器。\n' >&2
    printf '★ 若是如此 ⇒ 用 classify 模式喂清单（★ 清单从目标库自己取），★ 别信 probe 模式。\n' >&2
    awk -v nf="${needles}" '
      BEGIN { while ((getline line < nf) > 0) { if (line != "") { k++; nd[k] = line } } close(nf) }
      { out = ""; rest = $0
        for (j = 1; j <= k; j++) {
          s = nd[j]
          while (index(rest, s) > 0) {
            p = index(rest, s)
            out = out substr(rest, 1, p - 1) "***"
            rest = substr(rest, p + length(s))
          }
        }
        print out rest }' "${err}" >&2
    rm -f "${inv}" "${err}" "${needles}"
    return 64
  fi

  # ★★ 把身份行**前置**到清单里 ⇒ ★ classify 会把它印成 `#meta db_identity`。
  emit_identity_line "${target}" "${ident}" > "${inv}.withid"
  cat "${inv}" >> "${inv}.withid"
  mv "${inv}.withid" "${inv}"
  rm -f "${err}" "${needles}"
  classify_file "${inv}"
  rc=$?
  rm -f "${inv}"
  return "${rc}"
}

# ─────────────────────────── 入口 ───────────────────────────
MODE="${1:-}"
case "${MODE}" in
  probe)
    shift
    DSN="${1:-${LLM_GATEWAY_DATABASE_URL:-}}"
    if [[ -z "${DSN}" ]]; then
      printf 'FATAL: 没有 DSN ⇒ 请设 LLM_GATEWAY_DATABASE_URL 或把 DSN 作为第一个参数\n' >&2
      exit 64
    fi
    write_awk_prog || exit 64
    probe_db "${DSN}"
    exit $?
    ;;
  classify)
    shift
    write_awk_prog || exit 64
    classify_file "-"
    exit $?
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    printf 'FATAL: 未知模式（可用：probe / classify / --help）\n' >&2
    usage
    exit 64
    ;;
esac
