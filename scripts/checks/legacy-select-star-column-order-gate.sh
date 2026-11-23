#!/usr/bin/env bash
# =====================================================================
# scripts/checks/legacy-select-star-column-order-gate.sh
#   —— 「legacy 跨面 `SELECT * UNION ALL` 视图」的**运行时列序对拍门**
#
# ★★ 这道门补的是哪块缺口（★ 必须写清楚 ★★）★★
#   ★ 仓里**早有静态那半**：★ `sql/migrations/startup/hot_surface_select_star_contract_test.go`
#     ★ 它禁止**新建**跨面 `SELECT *` 视图、★ 并把**历史写法**登记进
#     ★ `legacyCrossSurfaceSelectStar`（★ 11 条 ★★）★ 另配反向门
#     ★ `TestLegacyCrossSurfaceSelectStarRegistryIsExact`（★ 登记不许空转 ★★）★★
#   ★ ⇒ ★★ **但那只是【静态那半】：★ 它查的是「仓里现在有没有新的违规写法」★★**
#   ★ ⇒ ★★ **运行时那半从来没人核对过**：★ ★★ 「登记表里这 11 对跨面视图、★
#     ★★ 在**今天这台库上**、★ 两侧的列序是否**仍然对齐**」★★ ⇒ ★★ ⇒ ★★
#     ★★ **那才是真正会炸的那一半** ★★
#
# ★★ 为什么列序漂移会炸（★ 机制、★ 不是推测 ★★）★★
#   ★ 跨面视图的写法是 `SELECT * FROM <hot> UNION ALL SELECT * FROM <parent>`
#   ★ ⇒ ★★ **`UNION ALL` 是按位置比类型的**（★ 不是按名字 ★★）★★
#   ★ ⇒ ★★ **两侧列数相同但顺序不同 ⇒ 视图重建必炸**（★ 实测报错：
#     ★ `ERROR:  UNION types integer and text cannot be matched` ★★）★★
#   ★ ⇒ ★★ 而这些迁移**大多不自记账**（★ 仓里 280/392 条 ★★）⇒ ★★ **⇒ 每次
#     ★ 增量部署都会重跑它们** ⇒ ★★ ⇒ ★★ **漂移＝下一次部署就炸** ★★
#
# ★★ 本门的三态（★ 「跑不了」与「跑红了」必须分得开 ★★★）★★
#   · 没有 DB 地址 ⇒ **显式 SKIP**（★ 不伪装成通过 ★★）
#   · 登记表 / 解析面为空 ⇒ **ABORT（退出码 2）**（★ 门自己的量具坏了 ★★）
#   · 至少一对**列序漂移** ⇒ **FAIL（退出码 1）**
#   · 否则 ⇒ **PASS（退出码 0）**
#
# ★★ 「不在场」为什么**不判红**（★ 免得恒红 ★★）★★
#   ★ `request_logs` 那一族已被 `890_drop_request_logs_v1_family.sql` 下线
#   ★ ⇒ ★★ 在**今天**的生产库上、★ 那一对关系**根本不存在** ⇒ ★★ **无重放风险**
#   ★ ⇒ ★★ ⇒ ★★ **「不在场」报出来、★ 但不判红**（★ 判红＝恒红＝等于没有 ★★）★★
#
# ★★ 本门**只读**：★ 一律 SELECT、★ 绝不写库、★ 绝不改 schema、★ 绝不调用迁移 ★★
#
# ★ 运行时依赖：★ 一个**可读**的 PG 地址
#   · `SELECT_STAR_GATE_DATABASE_URL`（★ 优先 ★）
#   · 或 `SELECT_STAR_GATE_PG_CONTAINER` ＋ `SELECT_STAR_GATE_PG_USER`
#     ＋ `SELECT_STAR_GATE_PG_DB`（★ 走容器内 psql、★ 不依赖宿主 psql ★★）
#   · 两者都没有 ⇒ SKIP
#   · `SELECT_STAR_GATE_PSQL_BIN`（★ 可覆盖 ★★）⇒ ★ **夹具测试用假 psql** ★★
#     ⇒ ★ **判据逻辑的可测性不依赖 PG** ★★
#
# ★ 调用方：★ `make guards-select-star-order`（★ 进 `make guards` 慢门族 ★★
#   ⇒ ★ codeup 载体的 4 核预算不受影响 ★★）★ 官方门 `verify.sh` 显式一行 ★★
# =====================================================================
set -euo pipefail

# ★★ ERR trap（★ 仓内硬约定：★ 任何意外错误 ⇒ 立刻带行号判红 ★★）
trap 'printf "FAIL: 脚本在第 %s 行异常退出（★ 这是门自身的缺陷、★ 不是判据红）\n" "$LINENO" >&2; exit 1' ERR

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

REGISTRY_GO="sql/migrations/startup/hot_surface_select_star_contract_test.go"
SQL_DIR="sql/migrations/startup"

SKIP=0
FAIL=0
note() { printf '%s\n' "$*"; }
skip() { printf 'SKIP: %s\n' "$*"; SKIP=1; }
bad()  { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
abort() { printf 'ABORT: %s\n' "$*" >&2; exit 2; }

DB_URL="${SELECT_STAR_GATE_DATABASE_URL:-}"
PG_CONTAINER="${SELECT_STAR_GATE_PG_CONTAINER:-}"
PG_USER="${SELECT_STAR_GATE_PG_USER:-llm_gateway}"
PG_DB="${SELECT_STAR_GATE_PG_DB:-llm_gateway}"
PSQL_BIN="${SELECT_STAR_GATE_PSQL_BIN:-psql}"

if [ -z "$DB_URL" ] && [ -z "$PG_CONTAINER" ]; then
  skip "没有 DB 地址（★ SELECT_STAR_GATE_DATABASE_URL / SELECT_STAR_GATE_PG_CONTAINER 都未给）⇒ ★ 这道门在本环境跑不了（★ 不是通过）"
  exit 0
fi

# ★★ 只读查询（★ 一律 SELECT ★★）★★
q() {
  local sql="$1"
  local out=""
  local rc=0
  if [ -n "$PG_CONTAINER" ]; then
    out="$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -Atc "$sql" 2>/dev/null)" || rc=$?
  else
    out="$("$PSQL_BIN" -X -v ON_ERROR_STOP=1 -Atqc "$sql" "$DB_URL" 2>/dev/null)" || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    Q_OUT=""
  else
    Q_OUT="$out"
  fi
  printf '%s' "$Q_OUT"
  return "$rc"
}

# ═══════════════════════════════════════════════════════════════════════
# ── 解析面自证（★ 解析不出来 ⇒ ABORT、★ 不许判红 ★★）─────────────────
# ═══════════════════════════════════════════════════════════════════════
if [ ! -f "$REGISTRY_GO" ]; then
  abort "找不到登记表 ${REGISTRY_GO} ⇒ ★ 门无法解析要核对的视图对"
fi

registry_file="$(mktemp "${TMPDIR:-/tmp}/select-star-registry.XXXXXX")"
pairs_file="$(mktemp "${TMPDIR:-/tmp}/select-star-pairs.XXXXXX")"
cols_file="$(mktemp "${TMPDIR:-/tmp}/select-star-cols.XXXXXX")"
trap 'rm -f "${registry_file:-}" "${pairs_file:-}" "${cols_file:-}"' EXIT

# ★★ 登记表解析：★ 从 Go 的 map 字面量里取「视图名 → 迁移文件」★★
awk '
  /^var legacyCrossSurfaceSelectStar = map\[string\]string\{/ { inmap = 1; next }
  inmap && /^\}/ { inmap = 0; next }
  inmap {
    line = $0
    if (line !~ /^[[:space:]]*"/) next
    if (match(line, /^[[:space:]]*"[A-Za-z0-9_]+"/) == 0) next
    key = substr(line, RSTART, RLENGTH)
    gsub(/"/, "", key)
    rest = substr(line, RSTART + RLENGTH)
    if (match(rest, /"[^"]*"/) == 0) next
    val = substr(rest, RSTART, RLENGTH)
    gsub(/"/, "", val)
    print key "\t" val
  }
' "$REGISTRY_GO" >"$registry_file"

registry_count=$(wc -l <"$registry_file" | tr -d ' ')
if [ "${registry_count:-0}" -eq 0 ]; then
  abort "从 ${REGISTRY_GO} 解析出 0 条登记 ⇒ ★ 门自己的解析器坏了（★ **不是**判据红）"
fi
note "legacy 跨面视图登记：${registry_count} 条（★ 解析自 ${REGISTRY_GO}）"

# ★★ 视图对解析：★ 在迁移文件里找 `CREATE ... VIEW <view>`、★ 再找它下面那两个
# ★★ `SELECT * FROM <rel>`（★ 形态：★ 第一个是 hot、★ 第二个是 parent ★★）★★
while IFS="$(printf '\t')" read -r view desc; do
  [ -n "$view" ] || continue
  # ★ 描述字段里文件名前可能带 `347/` 前缀、★ 后面可能跟中文说明 ⇒ 只取到 `.sql` ★
  file=""
  case "$desc" in
    *".sql"*) file="${desc%%.sql*}.sql" ;;
  esac
  [ -n "$file" ] || { abort "登记表条目「${view}」的描述里没解析出文件名 ⇒ ★ 门自己的解析器坏了" ; }
  file="${file##*/}"
  path="${SQL_DIR}/${file}"
  if [ ! -f "$path" ]; then
    abort "登记表条目「${view}」指向的迁移文件不存在（${path}）⇒ ★ 门自己的解析器坏了"
  fi
  pair="$(awk -v VIEW="$view" '
    { lines[NR] = $0 }
    END {
      view = VIEW
      start = 0
      for (i = 1; i <= NR; i++) {
        l = tolower(lines[i])
        if (l ~ /create[ \t]+(or[ \t]+replace[ \t]+)?view[ \t]+/ && index(lines[i], view) > 0) { start = i; break }
      }
      if (start == 0) { print "?"; exit }
      a = ""; b = ""
      for (i = start; i <= NR && i <= start + 80; i++) {
        l = lines[i]
        sub(/--.*$/, "", l)
        if (tolower(l) ~ /select[ \t]+\*[ \t]+from[ \t]+/) {
          rel = l
          sub(/.*[Ss][Ee][Ll][Ee][Cc][Tt][ \t]+\*[ \t]+[Ff][Rr][Oo][Mm][ \t]+/, "", rel)
          # ★ 去掉 schema 前缀（★ 625 的 down 文件写的是 `public.xxx` ★★）★★
          # ★ ★★ 顺序很关键：★ **先**去掉 `public.`、★ **后**再截断在非标识符字符 ★★
          # ★ ★★ （★ 反过来会把它截成 `public` ★★）★★
          sub(/^public[.]/, "", rel)
          sub(/[^A-Za-z0-9_].*$/, "", rel)
          if (a == "") { a = rel } else if (b == "" && rel != a) { b = rel }
        }
      }
      print a "\t" b
    }
  ' "$path")"
  a="$(printf '%s' "$pair" | cut -f1)"
  b="$(printf '%s' "$pair" | cut -f2)"
  if [ -z "$a" ] || [ -z "$b" ] || [ "$a" = "?" ] || [ "$b" = "?" ]; then
    abort "登记表条目「${view}」的视图对解析不出来（★ 迁移 ${file} 里找不到「CREATE VIEW <view>」下面那两个「SELECT * FROM」）⇒ ★ 门自己的解析器坏了"
  fi
  printf '%s\t%s\t%s\t%s\n' "$view" "$a" "$b" "$file" >>"$pairs_file"
done <"$registry_file"

pair_count=$(wc -l <"$pairs_file" | tr -d ' ')
if [ "${pair_count:-0}" -ne "$registry_count" ]; then
  abort "登记表 ${registry_count} 条、★ 解析出视图对 ${pair_count} 条 ⇒ ★ 数量对不上 ⇒ ★ 门自己的解析器坏了"
fi
note "视图对解析：${pair_count} 对（★ 形态＝「SELECT * FROM <hot> UNION ALL SELECT * FROM <parent>」★★）"

# ═══════════════════════════════════════════════════════════════════════
# ── 只读取列序（★ 一次查完、★ 一次判完 ★★）─────────────────────────
# ═══════════════════════════════════════════════════════════════════════
rel_list="'"
first=1
while IFS="$(printf '\t')" read -r view a b file; do
  [ -n "$view" ] || continue
  for rel in "$a" "$b"; do
    if [ "$first" = "1" ]; then
      first=0
    else
      rel_list="${rel_list},"
    fi
    rel_list="${rel_list}${rel}"
  done
done <"$pairs_file"
rel_list="${rel_list}'"

# ★★ ★★ 查询面**自证**（★ 本轮在生产 211 上真跑、★ **实测撞到的真缺陷** ★★）★★
# ★★ 第一版用的是 `c.relname = any (array['a,b,c'])` ⇒ ★★ 那是【一个元素】★★
# ★★ ⇒ ★★ 真库上返回 **0 行** ⇒ ★★ ⇒ ★★ **11 对全部被误判成「两侧都不在场」** ★★
# ★★ ⇒ ★★ **⇒ 而那正是「恒绿」的形态：★ 门报 PASS、★ 而它其实什么都没查到** ★★
# ★★ ⇒ ★★ **修法（★ 两处一起 ★★）**：★★
# ★★   ① **不再在 SQL 里按名字过滤**：★ 改成「**把 public schema 的列序全量取回来**、
# ★★     ★ 然后在 awk 里本地筛」⇒ ★★ ⇒ ★★ **过滤条件错了也不会漏数据** ★★
# ★★   ② **加一条查询面自证**：★ 先数「public schema 里有多少个关系」⇒ ★
# ★★     ★ **有关系、★ 而列序查询返回 0 行 ⇒ ⇒ ⇒ ABORT**（★ 门坏了、★ 不是判据绿 ★★）★★
total_rc=0
total_relations=$(q "-- select-total
select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace where n.nspname = 'public' and c.relkind in ('r','p','v','m','f')") || total_rc=$?
if [ "$total_rc" -ne 0 ]; then
  abort "读 public schema 的关系数失败（★ 连不上库 / PG 未就绪）⇒ ★ 门无法判定"
fi
if [ "${total_relations:-0}" -eq 0 ]; then
  abort "public schema 里一个关系都没有（★ 读到 ${total_relations:-0} 个 ★）⇒ ★★ **要么连错了库、★ 要么查询面不对** ⇒ ★ 门无法判定"
fi

rows=""
rows_rc=0
# ★★ ★★ 用 `concat_ws(chr(9), …)` 显式指定 **TAB** 分隔 ★★ ⇒ ★★ ⇒ ★★
# ★★ **不要依赖 psql `-A` 的默认分隔符**（★ 实测它是 `|`、★ 不是 TAB ★★）★★
rows=$(q "-- select-columns
select concat_ws(chr(9), c.relname, a.attnum, a.attname, format_type(a.atttypid, a.atttypmod)) from pg_attribute a join pg_class c on c.oid = a.attrelid join pg_namespace n on n.oid = c.relnamespace where n.nspname = 'public' and a.attnum > 0 and not a.attisdropped order by c.relname, a.attnum") || rows_rc=$?
if [ "$rows_rc" -ne 0 ]; then
  abort "读列序失败（★ 连不上库 / PG 未就绪）⇒ ★ 门无法判定"
fi
if [ -n "${rows:-}" ]; then
  printf '%s\n' "$rows" >"$cols_file"
else
  : >"$cols_file"
fi
col_rows=$(wc -l <"$cols_file" | tr -d ' ')
if [ "$col_rows" -eq 0 ]; then
  abort "public schema 里有 ${total_relations} 个关系、★ 而列序查询返回 **0 行** ⇒ ★★ **查询面坏了**（★ 门自己的缺陷、★ 不是判据绿 ★★）★★"
fi
note "  ℹ public schema 关系数 ${total_relations}｜列序读取 ${col_rows} 行（★ 只读、★ 一律 SELECT ★★）"

# ═══════════════════════════════════════════════════════════════════════
# ── 判据：★ 逐对比对两侧的列序（★ 按位置比类型、★ 与 UNION ALL 的语义一致 ★★）
# ═══════════════════════════════════════════════════════════════════════
aligned=0
absent=0
half=0
drift=0

while IFS="$(printf '\t')" read -r view a b file; do
  [ -n "$view" ] || continue
  a_cols="$(awk -F'\t' -v r="$a" '$1 == r { print $3 " " $4 }' "$cols_file" | tr '\n' '|')"
  b_cols="$(awk -F'\t' -v r="$b" '$1 == r { print $3 " " $4 }' "$cols_file" | tr '\n' '|')"
  if [ -z "$a_cols" ] && [ -z "$b_cols" ]; then
    absent=$((absent+1))
    note "  · ${view}（★ ${file} ★）：两侧都不在场（★ ${a} / ${b} ★）⇒ ★ **这套资产在本库上不存在 ⇒ 无重放风险** ⇒ 不判红"
    continue
  fi
  if [ -z "$a_cols" ] || [ -z "$b_cols" ]; then
    half=$((half+1))
    present=""
    [ -n "$a_cols" ] && present="${a}"
    [ -n "$b_cols" ] && present="${present}${present:+ +}${b}"
    note "  · ${view}（★ ${file} ★）：**只有一侧在场**（★ 在场＝${present} ★★）⇒ ★ 报出来"
    continue
  fi
  if [ "$a_cols" = "$b_cols" ]; then
    aligned=$((aligned+1))
    note "  · ${view}（★ ${file} ★）：两侧列序**逐位置对齐**（★ ${a} ↔ ${b} ★）✔"
    continue
  fi
  drift=$((drift+1))
  # ★★ 报出第一处错位的具体列名与类型（★ 让人能自己判断严重度 ★★）★★
  first_diff="$(awk -F'\t' '
    $1 == A { la[++na] = $3 " " $4 }
    $1 == B { lb[++nb] = $3 " " $4 }
    END {
      n = (na < nb ? na : nb)
      for (i = 1; i <= n; i++) {
        if (la[i] != lb[i]) { print "第 " i " 位：" la[i] "  ↔  " lb[i]; exit }
      }
      print "列数不同：" na " ↔ " nb
    }
  ' A="$a" B="$b" "$cols_file")"
  bad "跨面视图 ${view} 的两侧**列序已漂移**（★ ${a} ↔ ${b} ★）⇒ ★★ **\`UNION ALL\` 按位置比类型 ⇒ 重建该视图必炸**（★ 典型报错：\`UNION types integer and text cannot be matched\` ★★）★★ ★★ 而这些迁移**大多不自记账** ⇒ ★★ **⇒ 下一次增量部署就会重跑它** ⇒ ★★ ⇒ ★★ ${first_diff}"
done <"$pairs_file"

note ""
note "汇总：对齐 ${aligned} 对｜列序漂移 ${drift} 对｜只有一侧在场 ${half} 对｜两侧都不在场 ${absent} 对（★ 登记表共 ${registry_count} 条 ★★）"

if [ "$drift" -gt 0 ]; then
  note ""
  note "FAIL: ★★ ${drift} 对 legacy 跨面视图的**列序已漂移** ⇒ ★★ **下一次增量部署重跑它们时必炸**（★ 这不是「可能」：★「UNION ALL」的位置比对是确定性的 ★★）★★"
  note "  ⇒ ★★ 处置：★ ① **先查这两张表的历史**（★ 谁在什么时候加过列、★ 加在哪一侧 ★★）★ ② 要么把两侧列序**对齐回去**（★ owner 轴：★ schema 变更 ★★）★ ③ 要么把那几条迁移改成**逐列枚举**（★ 改**已 applied** 迁移的字节 ⇒ ★ 分叉风险 ★★）★★"
  exit 1
fi

note ""
note "PASS: ★★ 登记表里 ${registry_count} 对 legacy 跨面视图、★ 在本库上**没有列序漂移**（★ 对齐 ${aligned}／不在场 ${absent}／单侧在场 ${half} ★★）★★"
exit 0
