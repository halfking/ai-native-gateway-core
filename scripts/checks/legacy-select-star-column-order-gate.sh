#!/usr/bin/env bash
# =====================================================================
# scripts/checks/legacy-select-star-column-order-gate.sh
#   —— 「legacy 跨面 `SELECT * UNION ALL` 视图」的**运行时列序对拍门**
#
# ★★ 这道门补的是哪块缺口（★ 必须写清楚 ★★）★★
#   ★ 仓里**早有静态那半**：★ `sql/migrations/startup/hot_surface_select_star_contract_test.go`
#     ★ 它禁止**新建**跨面 `SELECT *` 视图、★ 并把**历史写法**登记进
#     ★ `legacyCrossSurfaceSelectStar`（★ ★★ L34 八轮起＝**语句级台账**（★ 30 条 ★★）★★）
#     ★ `TestLegacyCrossSurfaceSelectStarRegistryIsExact`（★ 登记不许空转 ★★）★★
#   ★ ⇒ ★★ **但那只是【静态那半】：★ 它查的是「仓里现在有没有新的违规写法」★★**
#   ★ ⇒ ★★ **运行时那半从来没人核对过**：★ ★★ 「登记表里那些跨面视图的各个分支、★
#     ★★ 在**今天这台库上**、★ 列序是否**仍然对齐**」★★ ⇒ ★★ ⇒ ★★
#     ★★ **那才是真正会炸的那一半** ★★
#
# ★★ L34 八轮的升级（★ 与静态门同源 ★★）★★
#   ★ ① 台账升级为**语句级**（★ 键＝「<迁移文件 basename>|<视图名>」、★ 值＝**分支序列** ★★）
#   ★   ⇒ ★★ **分支序列直接来自登记表**、★ 本门**不再自己解析 SQL** ★★
#   ★   ⇒ ★★ **与静态门共用同一张表** ⇒ ★★ **两张门在结构上不可能分叉** ★★
#   ★ ② 比对从**两路**扩到 **N 路**（★ `340` 那一族是**三路**：父表 ∪ 月分区 ∪ default ★★）
#   ★ ③ 台账从 11 条扩到 **30 条**（★ **含过去从不被检出的 `340`／`342`／`330` 三批** ★★）★★
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
# ★★ 台账里的迁移文件可能落在**不同的迁移目录**下（★ 例：`330` 在 `domain/` ★）
# ★★ ⇒ ★★ 文件在场自证要**扫这几个目录**（★ 与静态门的扫描根对齐 ★★）★★
SQL_DIRS="sql/migrations/startup sql/migrations/domain"
DEPLOY_SCRIPT="scripts/apply-db-revision-sequence.sh"

SKIP=0
FAIL=0
note() { printf '%s\n' "$*"; }
skip() { printf 'SKIP: %s\n' "$*"; SKIP=1; }
bad()  { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
abort() { printf 'ABORT: %s\n' "$*" >&2; exit 2; }

# ★★ 共享库：★ 「部署通道投递面」解析的**唯一真源**（★ 见 scripts/lib/deploy-surface.sh ★★）★★
# ★★ ⇒ ★★ 本门与「覆盖门」**共用同一份投递面解析** ⇒ ★★ **口径不可能分叉** ★★
# ★★ （★ 仓内既有教训：★ 「同一条规定写了两份」⇒ ★ 门与门开始互相矛盾 ★★）★★
# shellcheck source=scripts/lib/deploy-surface.sh
. "$REPO_ROOT/scripts/lib/deploy-surface.sh"

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

# ★★ 登记表解析（★ ★★ L34 八轮：★ 台账升级为【语句级】 ★★）★★
# ★★ 键＝"<迁移文件 basename>|<视图名>"、★ 值＝**分支关系序列**（★ 逗号分隔 ★★）★★
# ★★ ⇒ ★★ **分支序列直接来自登记表**、★ 本门**不再自己解析 SQL** ★★
# ★★ ⇒ ★★ **与静态门共用同一张表** ⇒ ★★ **两张门在结构上不可能分叉** ★★
# ★★ （★ 与 L34 七轮「投递面解析抽共享库」同一条纪律：★ **同一条规定只写一份** ★★）★★
awk '
  /^var legacyCrossSurfaceSelectStar = map\[string\]string\{/ { inmap = 1; next }
  inmap && /^\}/ { inmap = 0; next }
  inmap {
    line = $0
    if (line !~ /^[[:space:]]*"/) next
    # ★★ 键：★ 第一个引号串（★ ★★ gofmt 会把 map 字面量**对齐** ⇒
    # ★★   键与冒号之间可能是**多个空格** ★★ ⇒ ★★ 所以这里不假设空格个数 ★★）★★
    if (match(line, /^[[:space:]]*"[^"]+"/) == 0) next
    key = substr(line, RSTART, RLENGTH)
    gsub(/"/, "", key)
    # ★★ 剥掉**行首空白**（★ gofmt 会把 map 字面量缩进一个 TAB ★★）★★
    # ★★ ⇒ ★★ 否则这一行有**三个** TAB 分隔字段、★ `cut -f2` 会取错列 ★★
    sub(/^[[:space:]]+/, "", key)
    rest = substr(line, RSTART + RLENGTH)
    # ★★ 值：★ 第二个引号串（★ 分支关系序列 ★★）★★
    if (match(rest, /"[^"]*"/) == 0) next
    val = substr(rest, RSTART, RLENGTH)
    gsub(/"/, "", val)
    # ★★ 键必须含 "|"（★ "<文件>|<视图>" ★★）⇒ ★ **解析面自证** ★★
    if (index(key, "|") == 0) next
    print key "\t" val
  }
' "$REGISTRY_GO" >"$registry_file"

registry_count=$(wc -l <"$registry_file" | tr -d ' ')
if [ "${registry_count:-0}" -eq 0 ]; then
  abort "从 ${REGISTRY_GO} 解析出 0 条登记 ⇒ ★ 门自己的解析器坏了（★ **不是**判据红）"
fi
note "legacy 跨面视图登记：${registry_count} 条（★ 解析自 ${REGISTRY_GO} · ★ **语句级台账** ★★）"

# ★★ 台账 → 视图对清单：★ 键拆成「文件 + 视图」、★ 值就是**分支序列** ★★
while IFS="$(printf '\t')" read -r key val; do
  [ -n "$key" ] || continue
  file="${key%%|*}"
  view="${key#*|}"
  if [ "$file" = "$key" ] || [ -z "$file" ] || [ -z "$view" ]; then
    abort "登记表条目「${key}」拆不出「<文件>|<视图>」⇒ ★ 门自己的解析器坏了"
  fi
  if [ -z "$val" ]; then
    abort "登记表条目「${key}」的分支序列是空的 ⇒ ★ 门自己的解析器坏了"
  fi
  # ★★ 分支数自证：★ 跨面并集**至少两个分支**（★ 少于两个 ⇒ 那不是跨面并集 ★★）★★
  n_branch="$(printf '%s' "$val" | awk -F',' '{ print NF }')"
  if [ "${n_branch:-0}" -lt 2 ]; then
    abort "登记表条目「${key}」只有 ${n_branch:-0} 个分支 ⇒ ★ 门自己的解析器坏了"
  fi
  # ★★ 文件在场自证：★ 迁移文件必须真的在仓里（★ 静态门保证键对得上、★
  # ★★   但本门独立跑时仍要自证 ★★）★★
  found_path=""
  for d in $SQL_DIRS; do
    if [ -f "${d}/${file}" ]; then
      found_path="${d}/${file}"
      break
    fi
  done
  if [ -z "$found_path" ]; then
    abort "登记表条目「${key}」指向的迁移文件在 ${SQL_DIRS} 里都找不到（★ ${file} ★）⇒ ★ 门自己的解析器坏了"
  fi
  printf '%s\t%s\t%s\n' "$view" "$file" "$val" >>"$pairs_file"
done <"$registry_file"

pair_count=$(wc -l <"$pairs_file" | tr -d ' ')
if [ "${pair_count:-0}" -ne "$registry_count" ]; then
  abort "登记表 ${registry_count} 条、★ 解析出视图对 ${pair_count} 条 ⇒ ★ 数量对不上 ⇒ ★ 门自己的解析器坏了"
fi
note "视图对解析：${pair_count} 对（★ 形态＝「N 路 SELECT-STAR 并集」、★ ★★ 分支序列直接取自登记表 ★★）"

# ═══════════════════════════════════════════════════════════════════════
# ── 只读取列序（★ 一次查完、★ 一次判完 ★★）─────────────────────────
# ═══════════════════════════════════════════════════════════════════════
# ★★ 注意：★ 本门**不再**在 SQL 里按关系名过滤（★ L34 五轮实测过：★
# ★★   `c.relname = any (array['a,b,c'])` 那是**一个元素** ⇒ 0 行 ⇒ 恒绿 ★★）
# ★★ ⇒ ★★ **全量取回**、★ 本地筛 ⇒ ★★ 过滤条件写错也不会漏数据 ★★

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
# ── 投递面位次（★ 2026-11-24 L34 七轮新增 ★★）────────────────────────
# ═══════════════════════════════════════════════════════════════════════
# ★★ 为什么需要它（★ 这道门从「有漂移」升级成「**这次部署会在第几条炸**」 ★★）★★
# ★ 门原本只回答「哪些跨面视图的两侧列序已经漂移」⇒ ★★ 而操作者真正要回答的是
# ★★ 「**我这次部署会不会撞上它、★ 会撞在第几条**」 ★★ ⇒ ★★ ⇒ 差一个「投递面位次」
# ★★ 与「该迁移在不在投递面里」 ⇒ ★★ **这两条一补上、★ 门就直接可执行了** ★★
surface_file="$(mktemp "${TMPDIR:-/tmp}/select-star-surface.XXXXXX")"
trap 'rm -f "${registry_file:-}" "${pairs_file:-}" "${cols_file:-}" "${surface_file:-}"' EXIT
surface_rc=0
deploy_surface_extract "$DEPLOY_SCRIPT" "$surface_file" || surface_rc=$?
if [ "$surface_rc" -ne 0 ]; then
  # ★★ 拿不到投递面**只影响「位次」这一句**、★ 不影响漂移判据 ⇒ ★★ ⇒ **不 ABORT**
  # ★★ （★ 与「解析面自证」的处置不同：★ 那一半坏了会**掏空判据**、★ 这一半坏了只是**少一句话** ★★）★★
  note "  ℹ 拿不到部署通道投递面（★ ${DEPLOY_SCRIPT} 缺失或解析出 0 条 ★）⇒ ★ **位次未知**（★ 漂移判据不受影响 ★★）"
  : >"$surface_file"
else
  note "  ℹ 部署通道投递面 $(wc -l <"$surface_file" | tr -d ' ') 条（★ 解析自 ${DEPLOY_SCRIPT} · ★ 与覆盖门**共用同一份解析** ★★）"
fi

# ═══════════════════════════════════════════════════════════════════════
# ── 判据：★ 逐对比对两侧的列序（★ 按位置比类型、★ 与 UNION ALL 的语义一致 ★★）
# ═══════════════════════════════════════════════════════════════════════
aligned=0
absent=0
half=0
drift=0
# ★★ 会炸的文件与位次（★ L34 七轮：★ 让门回答「这次会在第几条炸」 ★★）★★
blast_positions=""
blast_files=""
blast_min=""
blast_min_file=""

while IFS="$(printf '\t')" read -r view file rels; do
  [ -n "$view" ] || continue
  # ★★ N 路分支列向量（★ L34 八轮：★ 台账里的分支序列可以是 2 路、★ 也可以是 3 路 ★★）★★
  IFS=',' read -ra branch_list <<<"$rels"
  base_rel=""
  base_cols=""
  present_rels=""
  missing_rels=""
  drift_rel=""
  drift_cols=""
  for rel in "${branch_list[@]}"; do
    [ -n "$rel" ] || continue
    cols="$(awk -F'\t' -v r="$rel" '$1 == r { print $3 " " $4 }' "$cols_file" | tr '\n' '|')"
    if [ -z "$cols" ]; then
      missing_rels="${missing_rels}${missing_rels:+,}${rel}"
      continue
    fi
    present_rels="${present_rels}${present_rels:+,}${rel}"
    if [ -z "$base_rel" ]; then
      base_rel="$rel"
      base_cols="$cols"
      continue
    fi
    if [ -z "$drift_rel" ] && [ "$cols" != "$base_cols" ]; then
      drift_rel="$rel"
      drift_cols="$cols"
    fi
  done
  # ★★ **全部分支都不在场** ⇒ ★ **绿**（★ 这套资产在本库上不存在 ⇒ 无重放风险 ★★）
  # ★★ （★ ★★ 别把它当「门坏了」★★：★ `request_logs` 那一族已被 `890` 下线、★
  # ★★   整族不在场是**正常状态** ★★）
  if [ -z "$missing_rels" ] && [ -z "$drift_rel" ]; then
    aligned=$((aligned+1))
    note "  · ${view}（★ ${file} ★）：各分支列序**逐位置对齐**（★ ${present_rels} ★）✔"
    continue
  fi
  if [ -n "$missing_rels" ] && [ -z "$drift_rel" ]; then
    if [ -z "$present_rels" ]; then
      absent=$((absent+1))
      note "  · ${view}（★ ${file} ★）：各分支**都不在场**（★ ${rels} ★）⇒ ★ **这套资产在本库上不存在 ⇒ 无重放风险** ⇒ 不判红"
    else
      half=$((half+1))
      note "  · ${view}（★ ${file} ★）：**只有部分分支在场**（★ 在场＝${present_rels} ／ 不在场＝${missing_rels} ★★）⇒ ★ 报出来"
    fi
    continue
  fi
  if [ -n "$missing_rels" ]; then
    # ★★ 分支**既缺场又对不齐** ⇒ ★ 报出来、★ 但不判红（★ 缺场那一支本身就没法并 ★★）
    half=$((half+1))
    note "  · ${view}（★ ${file} ★）：**只有部分分支在场**（★ 在场＝${present_rels} ／ 不在场＝${missing_rels} ★★）⇒ ★ 报出来"
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
  ' A="$base_rel" B="$drift_rel" "$cols_file")"
  # ★★ 投递面位次（★ L34 七轮：★ 把「有漂移」变成「**这次会在第几条炸**」 ★★）★★
  pos=""
  if [ -s "$surface_file" ]; then
    pos="$(deploy_surface_position "$surface_file" "$file")"
  fi
  if [ -n "$pos" ]; then
    blast_positions="${blast_positions}${pos} "
    blast_files="${blast_files}${file}(第${pos}条) "
    if [ -z "$blast_min" ] || [ "$pos" -lt "$blast_min" ]; then
      blast_min="$pos"
      blast_min_file="$file"
    fi
    blast_hint="★ 该迁移在部署通道投递面里排第 ${pos} 条 ⇒ ★★ **若本次部署重放它、★ 就会在第 ${pos} 条炸**（★ 单事务 ⇒ 回滚 ⇒ 无损坏、★ 但部署被挡住 ★★）★★"
  else
    blast_hint="★ 该迁移**不在**部署通道投递面里 ⇒ ★★ **本次部署不会重放它 ⇒ 没有即时风险**（★ 但它仍在登记表里、★ 将来一旦被投递就会炸 ★★）★★"
  fi
  bad "跨面视图 ${view} 的分支之间**列序已漂移**（★ ${base_rel} ↔ ${drift_rel} ★）⇒ ★★ **\`UNION ALL\` 按位置比类型 ⇒ 重建该视图必炸**（★ 典型报错：\`UNION types integer and text cannot be matched\` ★★）★★ ★★ 而这些迁移**大多不自记账** ⇒ ★★ **⇒ 下一次增量部署就会重跑它** ⇒ ★★ ⇒ ★★ ${first_diff} ★★ ★★ ${blast_hint} ★★"
done <"$pairs_file"

note ""
note "汇总：对齐 ${aligned} 对｜列序漂移 ${drift} 对｜只有部分分支在场 ${half} 对｜各分支都不在场 ${absent} 对（★ 登记表共 ${registry_count} 条 ★★）"

if [ "$drift" -gt 0 ]; then
  note ""
  if [ -n "$blast_min" ]; then
    note "FAIL: ★★ ${drift} 对 legacy 跨面视图的**列序已漂移** ⇒ ★★ **下一次增量部署重跑它们时必炸**（★ 这不是「可能」：★「UNION ALL」的位置比对是确定性的 ★★）★★"
    note "  ⇒ ★★ **会炸的文件与位次**：★ ${blast_files}★★（★ **最早的一条＝第 ${blast_min} 条（${blast_min_file}）** ★★）★★"
    note "  ⇒ ★★ 处置：★ ① **先查这两张表的历史**（★ 谁在什么时候加过列、★ 加在哪一侧 ★★）★ ② 要么把两侧列序**对齐回去**（★ owner 轴：★ schema 变更 ★★）★ ③ 要么把那几条迁移改成**逐列枚举**（★ 改**已 applied** 迁移的字节 ⇒ ★ 分叉风险 ★★）★ ④ **或先让通道认到那本账**（★ 方案 A：★ 账里有的就跳过 ⇒ ★ 那这条就不会被重放 ★★）★★"
  else
    note "FAIL: ★★ ${drift} 对 legacy 跨面视图的**列序已漂移** ⇒ ★★ **下一次增量部署重跑它们时必炸**（★ 这不是「可能」：★「UNION ALL」的位置比对是确定性的 ★★）★★"
    note "  ⇒ ★★ 处置：★ ① **先查这两张表的历史**（★ 谁在什么时候加过列、★ 加在哪一侧 ★★）★ ② 要么把两侧列序**对齐回去**（★ owner 轴：★ schema 变更 ★★）★ ③ 要么把那几条迁移改成**逐列枚举**（★ 改**已 applied** 迁移的字节 ⇒ ★ 分叉风险 ★★）★★"
  fi
  exit 1
fi

note ""
note "PASS: ★★ 登记表里 ${registry_count} 对 legacy 跨面视图、★ 在本库上**没有列序漂移**（★ 对齐 ${aligned}／不在场 ${absent}／单侧在场 ${half} ★★）★★"
exit 0
