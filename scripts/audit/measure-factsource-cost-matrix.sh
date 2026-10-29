#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# measure-factsource-cost-matrix.sh —— 「切事实源」的**二维**代价表实测（可复跑脚本）
#
# 2026-10-21（R90 轮）入仓。由来：§19.20.35 只量了「切事实源」的一半（变量集），
# 而另一半（DSN 指向哪个库）是有生产风险的那一维 ⇒ ⇒ 本脚本把两维都量出来。
#
# # 它量什么
#
#   档 A1 = 官方 19 变量（run-integration-gate.sh 的 env 块） + DSN 指一次性 itgate 库
#   档 B1 = 事实源 33 变量（datasource_env.sh）           + DSN 指一次性 itgate 库   ← §19.20.35 的那一档
#   档 B2 = 事实源 33 变量                                 + DSN 指**产品名库**（模拟真产品库）
#
#   轴 1 = A1→B1（变量集 ⇒ 哪些腿因此拿到 DSN）
#   轴 2 = B1→B2（DSN 指向 ⇒ 跑的库是谁）
#
# # 为什么必须「在 origin/main 的干净副本上跑」
#
#   共享工作树里可能存在**未跟踪**的测试文件（并行会话的在途改动）。
#   它们会被「15 个缺失变量 → 在范围文件」这条 grep 命中 ⇒ ⇒ **分母会依赖未提交的文件**
#   ⇒ ⇒ 从 origin/main 重跑得不到同一组数 ⇒ ⇒ **代价表不可复现**。
#   ⇒ 本脚本默认在 `/tmp/factsource-matrix-wt`（origin/main 的独立 worktree）里跑。
#   ⇒ R90 轮实测：共享树 92 个在范围函数 vs 干净副本 87 个 ⇒ 差 5 个（全 PASS），
#      **FAIL 分母两口径完全一致** ⇒ 「既存红」本来就是可复现的，不可复现的是 PASS 计数。
#
# # 用法
#
#   bash scripts/audit/measure-factsource-cost-matrix.sh              # 自动建形态库 + 三档全跑
#   KEEP_DB=1 bash scripts/audit/measure-factsource-cost-matrix.sh    # 保留形态库
#   SHAPE_DB=itgate_xxx bash scripts/audit/measure-factsource-cost-matrix.sh   # 复用已有形态库
#
# # 耗时 / 资源
#
#   形态库构建 ≈ 10~15 min（探针脚本）；三档 × 15 包 ≈ 10~15 min。
#   全部在**一次性库**上跑，退出即 DROP ⇒ **绝不指向真产品库**。
#
# # ★★★★ 「在范围」的口径（R96 轮订正 —— 这一条改的是**分母**）
#
#   ★ 旧口径（口径 A，本文件 R90 轮起的实现）：`grep -rl <变量名> *_test.go`
#     ⇒ **文本里出现名字就算**，且**不要求该文件真的打开 PG 连接**。
#   ★ 现口径（口径 D）：**官方门自己的口径** ——
#     (1) 文件里出现「DSN 类环境变量名」的**字符串字面量**（不是随便提到这个名字）
#         （注释里提到的不算 ⇒ 官方门用 `dsnNameLiterals`，它自己跳过注释）
#     (2) 且该文件**打开 PG 连接**（分类轴：名字长得像 DSN 不算数，会不会连库才算数）
#   ⇒ 为什么必须换成它：「切事实源」的成本 = **有多少测试会因为多注入 DSN 而行为改变**
#     ⇒ 只有「**真的读这个变量去连库**」的文件才算 ⇒ 口径 A 把
#     「注释里提一句」和「读了变量但压根不开 PG」的文件也算进去 ⇒ **分母被高估**。
#   ⇒ 实测（基线 9e7a9242e）：口径 A = 29 文件 / 89 函数 ⇒ 口径 D = 19 文件 / 71 函数
#     ⇒ **文件维 −34.6%、函数维 −20.2%** ⇒ 「切事实源」成本被高估了约 1/3。
#
#   ★★ **零漂移的做法**：本脚本**运行时**往 worktree 里生成一个 Go 探针，
#     探针**直接调用官方门自己的未导出函数**（`dsnNameLiterals` / `opensPostgresConn`）
#     ⇒ 官方门改了判据，本脚本的口径**自动跟着改** ⇒ 不存在「副本漂移」。
#     探针跑完即删（trap 兜底）⇒ 不入仓、不长期存在。
#     ⚠ 探针刻意**不含任何 DSN 名字面量**、**不含 PG 连接器** ⇒
#       官方门扫全仓 `_test.go` 时既不会把它算成违规、也不会扰动 `pgFiles` 计数。
#
# # ★ 三个必须写进代码的量具纪律（都是 R89 轮真踩过的）
#
#   ① **库名全小写**：PostgreSQL 把未加引号的标识符折叠成小写 ⇒
#      `CREATE DATABASE itgate_A1` 建出来的是 `itgate_a1` ⇒ DSN 里写 `itgate_A1`
#      ⇒ 连接报 `3D000 database does not exist` ⇒ **所有需要库的测试都以「红」呈现**
#      ⇒ ⇒ 一眼看去像「形态差异 / 产品缺陷」⇒ ⇒ **整轮读数作废**。
#   ② **建库后立刻自检**：拿 DSN 里写的那个库名去连一次 ⇒ 通不过就**直接退出**，
#      绝不带着可疑读数往下走。
#   ③ **psql 读值要滤噪声行**：这个 citus 容器把
#      `WARNING: could not start maintenance background worker` 打在 **stdout**
#      ⇒ 直接 `head -1` 取到的是 WARNING 而**不是**查询结果
#      ⇒ ⇒ 一律 `grep -vE '^(WARNING|HINT|NOTICE|INFO|DETAIL)' | tail -1`。
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
PGPORT="${PGPORT:-5432}"
SHAPE_DB="${SHAPE_DB:-itgate_factmatrix_shape}"
WT="${WT:-/tmp/factsource-matrix-wt}"
OUT="${OUT:-/tmp/factsource_matrix_out}"
KEEP_DB="${KEEP_DB:-0}"
SCOPE_ONLY="${SCOPE_ONLY:-0}"
RECLAIM_ONLY="${RECLAIM_ONLY:-0}"
SCOPE_TSV="$OUT/scope.tsv"
SCOPE_LEGACY_TSV="$OUT/scope_legacy.tsv"
SCOPE_LEGACY_FILES="$OUT/scope_legacy_files.tsv"
PROBE_TSV="$OUT/scope_probe.tsv"
PROBE_FILES="$OUT/scope_probe_files.tsv"
PROBE_A_FILES="$OUT/scope_probe_A_files.tsv"
PROBE_LOG="$OUT/scope_probe.log"
# ★ 探针文件写在 **worktree** 里（不是共享树）⇒ 共享树永远干净。
PROBE_SRC_REL="scripts/ursmcheck/zz_matrix_scope_probe_test.go"

# ★★★ 回收兜底（R94 轮实测修）：本脚本原本把 DROP 放在**尾部** ⇒
#   `mk_db()` 里的量具自检失败会 `return 1` ⇒ 调用方 `continue`（不退出）
#   ⇒ 已建好的 `itgate_factmatrix_a1` / `_b1` **留在库里**（尾部回收只在全部跑完后执行）
#   ⇒ 修法：trap 兜住所有退出路径（★ shell 的 trap ≈ python 的 atexit ≈ go 的 defer）。
cleanup_dbs() {
  if [ "${KEEP_DB:-0}" = "1" ]; then return 0; fi
  # ★ 回收路径已经跑过（并核实过）⇒ 退出 trap 不必再来一次（避免重复报错）
  if [ "${RECLAIMED:-0}" = "1" ]; then return 0; fi
  # ★ 用数组：写成 "for db in \"$LIST\"" 会因引号而不分词 ⇒ 循环只跑一次 ⇒ 库不被删
  local db err rc failed=0
  for db in "${FACTMATRIX_DBS[@]}"; do
    [ -n "$db" ] || continue
    # ★★ R96 轮修：原来把 stderr 一起丢掉 + `|| true` ⇒ **DROP 失败会被完全吞掉**
    #   ⇒ 脚本第 4 节打印「DROP xxx」但库其实还在 ⇒ **回收是【声称】不是【核实】**。
    #   ⇒ 现在把真实报错**留下来**（供第 4 节核对用），但不让它污染读数 stdout。
    # ★ 防卡保护：SET / terminate / DROP 三条独立 -c
    #   ★ terminate 单独一条（输出丢 /dev/null）⇒ 下面 err 里只剩 DROP 的真实报错
    #   ★ DROP 放最后一条 ⇒ psql 退出码仍反映它 ⇒ 「失败可检出」不被削弱
    docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$db' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
    err="$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "SET statement_timeout = '120s'" \
      -c "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE);" 2>&1)"
    rc=$?
    if [ "$rc" -ne 0 ]; then
      failed=1
      say "  ✗ DROP $db 失败：$(printf '%s' "$err" | grep -vE '^(WARNING|HINT|NOTICE|INFO|DETAIL)' | head -2 | tr '\n' ' ')"
    fi
  done
  return "$failed"
}
FACTMATRIX_DBS=("itgate_factmatrix_a1" "itgate_factmatrix_b1" "factmatrixprod_b2" "$SHAPE_DB")

# ★★ R96 轮新增：通用的「库还在不在」判据（回收核实要用）
#   ★ **三态**（与 R94 量具的 C5 同一纪律）：0=存在 / 1=不存在 / **2=查不到**
#   ★ 「查不到」（容器不在 / 连不上）**不等于「不存在」** ⇒ ⇒ 不能算「已回收」。
db_state() {
  local raw n
  raw="$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "SELECT count(*) FROM pg_database WHERE datname='$1'" 2>&1)"
  n="$(printf '%s' "$raw" | grep -vE '^(WARNING|HINT|NOTICE|INFO|DETAIL)' | tail -1)"
  case "$n" in
    ''|*[!0-9]*) return 2 ;;
  esac
  [ "$n" = "0" ] && return 1
  return 0
}
shape_exists() {
  if db_state "$SHAPE_DB"; then return 0; else return 1; fi
}

# ★★ 回收 + **核实**（★ 回收不是「打印一行 DROP」，是「查库确认真的没了」）
#   ★ 三态判据：查不到（容器/连接不可用）⇒ **不能算已回收** ⇒ 报出来。
reclaim_dbs() {
  local db leaked=0 st
  if [ "${KEEP_DB:-0}" = "1" ]; then
    say "  ⚠ KEEP_DB=1 ⇒ 保留本脚本建的一次性库（$SHAPE_DB 等）⇒ **不做回收也不做核实**"
    return 0
  fi
  cleanup_dbs || say "  ⚠ 有库删不掉（真实报错见上）"
  RECLAIMED=1
  for db in "${FACTMATRIX_DBS[@]}"; do
    [ -n "$db" ] || continue
    if db_state "$db"; then
      st=0
    else
      st=$?
    fi
    case "$st" in
      0) say "  ✗ 回收未生效：$db 仍在库里 ⇒ ⇒ 一次性库泄漏 ⇒ **不把本轮当成功**"; leaked=1 ;;
      1) say "  ✓ 已回收 $db" ;;
      2) say "  ✗ 查不到 $db 是否还在（容器/连接不可用）⇒ ⇒ **不能算已回收**"; leaked=1 ;;
    esac
  done
  return "$leaked"
}

# ★ 探针回收（本脚本自己生成的临时文件 ⇒ 必须被回收，**不受 KEEP_DB 影响**）。
#   ★ 同 R93/R94 轮的教训：只挂 EXIT trap 不够 ⇒ INT/TERM 也要显式接管。★★ R101 轮实测订正了这句的【理由】（结论不变）：真正让「必须接管」成立的不是「EXIT trap 不够」，而是【终止性 ≠ 清理性】—— 实测 bash 在「前台外部命令」形态下会**忽略未 trap 的 SIGINT**（脚体继续跑、退出码 0），且【INT/TERM trap 不带 exit ⇒ 脚体也会继续】⇒ ⇒ 两者都会让脚本在终止信号后继续建库 ⇒ ⇒ 详见 scripts/ursmcheck/signal_path_mechanism_test.go
probe_cleanup() {
  [ -n "${PROBE_SRC:-}" ] || return 0
  # ★ 静默：回收是 best-effort；不让删除命令的输出污染读数 stdout
  rm -f "$PROBE_SRC" >/dev/null 2>&1 || true
}
trap 'probe_cleanup || true; cleanup_dbs || true' EXIT
trap 'probe_cleanup || true; cleanup_dbs || true; exit 143' INT TERM

cd "$REPO_ROOT" || exit 1

# ── 变量集：档 A（官方 19）与档 B（事实源 33）────────────────────────────────
A_VARS="AI_SESSION_MANAGER_DATABASE_URL CONTEXT_ATTRS_TEST_DATABASE_URL D07_S01_PG_URL DATABASE_URL DECISION_RETIREMENT_TEST_DATABASE_URL LICENSE_AUTHORITY_DATABASE_URL LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_PG_URL OMNIFREE_TEST_DB_URL ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL TEST_AUDIT_FRESH_SCHEMA_DB_URL TEST_AUDIT_ISOLATED_DB_URL TEST_DATABASE_URL TEST_DB_URL TEST_INSTALLER_FRESH_DB_URL TEST_PG_DSN TEST_PG_URL TEST_RESOLVE_INVARIANT_DB_URL TEST_TENANT_DATABASE_URL"
B_VARS="AI_SESSION_MANAGER_DATABASE_URL BOOTSTRAP_TEST_DSN CONTEXT_ATTRS_TEST_DATABASE_URL D07_S01_ADMIN_URL D07_S01_PG_URL DATABASE_URL DB_TEST_URL DECISION_RETIREMENT_TEST_DATABASE_URL IDENTITY_SHADOW_DSN LICENSE_AUTHORITY_DATABASE_URL LLM_GATEWAY_DATABASE_URL LLM_GATEWAY_DUALREAD_PG_DSN LLM_GATEWAY_PG_TEST_URL LLM_GATEWAY_PG_URL LLM_GATEWAY_ROLLUP_TEST_DSN LLM_GATEWAY_ROUTING_GATE_PG_DSN LLM_GATEWAY_SUPPLIER_PG_DSN LLM_GATEWAY_TEST_PG_DSN OMNIFREE_TEST_DB_ADMIN_URL OMNIFREE_TEST_DB_URL ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL SANITIZE_RULES_BROWSER_DSN TEST_ADMIN_500_DSN TEST_AUDIT_FRESH_SCHEMA_DB_URL TEST_AUDIT_ISOLATED_DB_URL TEST_DATABASE_URL TEST_DB_URL TEST_G6_TOOLS_DSN TEST_INSTALLER_FRESH_DB_URL TEST_PG_DSN TEST_PG_URL TEST_RESOLVE_INVARIANT_DB_URL TEST_RETIREMENT_HTTP_DSN"

# 15 个「事实源有、官方 runner 没有」的变量（档 B \ 档 A）⇒ 决定受测范围
MISSING_VARS="BOOTSTRAP_TEST_DSN D07_S01_ADMIN_URL DB_TEST_URL IDENTITY_SHADOW_DSN LLM_GATEWAY_DUALREAD_PG_DSN LLM_GATEWAY_PG_TEST_URL LLM_GATEWAY_ROLLUP_TEST_DSN LLM_GATEWAY_ROUTING_GATE_PG_DSN LLM_GATEWAY_SUPPLIER_PG_DSN LLM_GATEWAY_TEST_PG_DSN OMNIFREE_TEST_DB_ADMIN_URL SANITIZE_RULES_BROWSER_DSN TEST_ADMIN_500_DSN TEST_G6_TOOLS_DSN TEST_RETIREMENT_HTTP_DSN"

mkdir -p "$OUT"

say() { printf '%s\n' "$*"; }

# ── 0. 准备：干净 worktree（origin/main）＋ 形态库 ────────────────────────────
say "── 0. 准备 ──"
git fetch origin --quiet || { say "!! fetch 失败"; exit 1; }
BASE_COMMIT="$(git rev-parse origin/main)"
say "  基线提交 = ${BASE_COMMIT}"

# ★ 回收模式：只做「回收 + 核实」，不建库、不跑读数
#   ⇒ 用途一：单独清掉上一次跑批残留的一次性库（可复算、可核实）
#   ⇒ 用途二：★ 单独验证「回收」这条路径本身（否则它只在大跑批末尾被执行一次）
#   ⇒ ★★ 放在 worktree 基线自检【之前】⇒ 回收与「读数基线」无关，不该被读数门拦住。
if [ "$RECLAIM_ONLY" = "1" ]; then
  say "── 回收模式（只回收 + 核实，不建库、不跑读数）──"
  if reclaim_dbs; then
    say "  ✓ 全部回收并核实通过"
    exit 0
  fi
  say "!! 有一次性库没删干净 ⇒ exit 3"
  exit 3
fi

if [ ! -d "$WT" ]; then
  git worktree add --detach "$WT" "$BASE_COMMIT" >/dev/null 2>&1 || { say "!! worktree 创建失败"; exit 1; }
fi

# ★★★ ★ 量具自检：worktree 的**实际 HEAD**必须 == 宣称的基线（自检跑在读数之前）
#   实测缺陷（并行会话 R92 回执 ②E 指出，本轮独立复核成立）：
#   ★ 原实现是 `if [ ! -d "$WT" ]; then git worktree add …; fi`
#     ⇒ **$WT 已存在时根本不更新** ⇒ 日志里「基线提交 = X」与「worktree @ Y」是
#     **两个不同的提交** ⇒ 整份读数其实是从**旧树**算出来的，而日志宣称新基线
#     ⇒ ⇒ **没有任何一层会报警**（上次侥幸无害，只是因为两次提交之间只差注释）。
#   ⇒ 修法：自检 + **不一致即拒绝出结论（exit 3）**，而不是打一行需要人脑比对的日志。
WT_HEAD="$(git -C "$WT" rev-parse HEAD 2>/dev/null || echo "")"
if [ "$WT_HEAD" != "$BASE_COMMIT" ]; then
  say "  ✗ ★ 量具自检失败：worktree 的实际 HEAD ≠ 宣称的基线"
  say "      宣称基线   = ${BASE_COMMIT}"
  say "      worktree 实际 = ${WT_HEAD:-（读不到）}   ($WT)"
  say "      ⇒ 读数会从**旧树**算出来，而日志宣称新基线 ⇒ **拒绝出结论（exit 3）**"
  say "      ⇒ 修法二选一："
  say "        (a) 换一个 worktree 路径： WT=/tmp/新路径 bash $0"
  say "        (b) 显式刷新（★ 会丢弃该 worktree 里的本地改动）： MATRIX_REFRESH_WT=1 bash $0"
  if [ "${MATRIX_REFRESH_WT:-0}" = "1" ]; then
    say "      MATRIX_REFRESH_WT=1 ⇒ 强制刷新 ${WT} → ${BASE_COMMIT}"
    git -C "$WT" checkout --detach "$BASE_COMMIT" >/dev/null 2>&1 \
      || git -C "$WT" reset --hard "$BASE_COMMIT" >/dev/null 2>&1
    WT_HEAD="$(git -C "$WT" rev-parse HEAD 2>/dev/null || echo "")"
    if [ "$WT_HEAD" != "$BASE_COMMIT" ]; then
      say "      ✗ 刷新失败 ⇒ 拒绝出结论"
      exit 3
    fi
    say "      ✓ 刷新完成"
  else
    exit 3
  fi
fi
say "  worktree = $WT @ $(git -C "$WT" rev-parse --short HEAD)  ✓ 与基线一致"

SHAPE_MARK="matrix-baseline:${BASE_COMMIT}"
shape_mark() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "SELECT COALESCE(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname='$SHAPE_DB'" 2>/dev/null | head -1
}
shape_set_mark() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "COMMENT ON DATABASE \"$SHAPE_DB\" IS '$SHAPE_MARK'" >/dev/null 2>&1 || true
}
shape_drop() {
  # ★ 防卡保护：SET / terminate / DROP 三条独立 -c
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$SHAPE_DB' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "SET statement_timeout = '120s'" \
    -c "DROP DATABASE IF EXISTS \"$SHAPE_DB\" WITH (FORCE);" >/dev/null 2>&1 || true
}

if [ "$SCOPE_ONLY" = "1" ]; then
  say "  [形态库] SCOPE_ONLY=1 ⇒ **跳过形态库**（scope 是纯静态计算，与库无关）"
else
  if shape_exists; then
    # ★★★ 形态库也是「跨基线复用的产物」⇒ 必须校验它是不是**当前基线**建的
    #   （同族缺陷：worktree 那个是「旧树算读数」，这个是「旧形态算读数」）
    _mark="$(shape_mark)"
    if [ "$_mark" != "$SHAPE_MARK" ]; then
      say "  [形态库] $SHAPE_DB 的基线指纹 ≠ 当前基线 ⇒ **重建**（旧指纹: ${_mark:-（无）}）"
      shape_drop
    else
      say "  [形态库] 复用已存在的 ${SHAPE_DB}（基线指纹一致 ✓）"
    fi
  fi
  if ! shape_exists; then
    say "  [形态库] 构建 ${SHAPE_DB}（探针脚本，ensure 链 PASS 才算产品形态）…"
    DB_NAME="$SHAPE_DB" KEEP_DB=1 PG_CONTAINER="$PG_CONTAINER" PG_USER="$PG_USER" \
      bash scripts/audit/probe-onetime-db-shape.sh >"$OUT/probe.log" 2>&1 \
      || { say "!! 形态库构建失败（见 $OUT/probe.log）"; exit 1; }
    shape_set_mark
    say "  [形态库] 已标记基线指纹 = ${SHAPE_MARK}"
  fi
fi

# ── 1. 在范围函数（★ 在**干净副本**里算 ⇒ 排除未跟踪文件）──────────────────
say "── 1. 算在范围（15 个缺失变量 → 测试文件 → 测试函数）──"

# ── 1a. 旧口径 A（文本 grep）⇒ **只作对照 + 对拍**，不作结论口径 ────────────
python3 - "$WT" "$MISSING_VARS" "$SCOPE_LEGACY_TSV" "$SCOPE_LEGACY_FILES" \
  2>"$OUT/scope_legacy.count" >/dev/null <<'PY'
import re, os, subprocess, sys
REPO, missing = sys.argv[1], sys.argv[2].split()
SCOPE_TSV, FILES_TSV = sys.argv[3], sys.argv[4]
files = set()
for v in missing:
    r = subprocess.run(["grep", "-rl", "--include=*_test.go", v, REPO],
                       capture_output=True, text=True)
    for h in r.stdout.split():
        if h.startswith(REPO + "/"):
            files.add(h[len(REPO) + 1:])
tf = re.compile(r"^func (Test[A-Za-z0-9_]*)\(", re.M)
# ★ 官方门**排除自己**（`datasource_env_test.go` 里全是这些变量名，
#   但它是「定规矩的人」不是「被变量影响的人」）⇒ 口径 A 也不能把它算进去。
#   ★ 不排 ⇒ 口径 A 会比官方门多 1 个文件 / 3 个函数（实测 30/92 vs 29/89）。
kept = sorted(f for f in files if os.path.basename(f) != "datasource_env_test.go")
bypkg = {}
with open(FILES_TSV, "w") as fh:
    for f in kept:
        d = os.path.dirname(f).lstrip("./")
        pkg = "./" + (d if d else ".")
        fh.write(f + "\t" + pkg + "\n")
        for n in tf.findall(open(os.path.join(REPO, f), errors="replace").read()):
            bypkg.setdefault(pkg, set()).add(n)
with open(SCOPE_TSV, "w") as fh:
    for pkg in sorted(bypkg):
        names = sorted(bypkg[pkg])
        fh.write(pkg + "\t" + "^(" + "|".join(names) + ")$\n")
print(f"files={len(kept)} functions={sum(len(v) for v in bypkg.values())} packages={len(bypkg)}",
      file=sys.stderr)
PY
LEGACY_COUNT="$(cat "$OUT/scope_legacy.count" 2>/dev/null)"
if [ -z "$LEGACY_COUNT" ]; then
  say "!! 旧口径统计为空 ⇒ 量具自检失败 ⇒ 拒绝出读数（exit 3）"
  exit 3
fi
say "  口径 A（旧：文本 grep，不剥注释、不要求开 PG）= ${LEGACY_COUNT}"

# ── 1b. 口径 D（官方门自己的实现）⇒ **结论口径**（零漂移）────────────────────
PROBE_SRC="$WT/$PROBE_SRC_REL"
mkdir -p "$(dirname "$PROBE_SRC")"
cat > "$PROBE_SRC" <<'GO'
package ursm_test

// ⚠ 本文件由 scripts/audit/measure-factsource-cost-matrix.sh **运行时生成**，跑完即删。
//   它存在的唯一理由：**用官方门自己的实现算「在范围」** ⇒ 口径零漂移。
//
// ⚠ 刻意【不含任何 DSN 名字面量】⇒ 官方门扫全仓 _test.go 时不会把它算成违规。
// ⚠ 刻意【不含 pgxpool.New / pgx.Connect / sql.Open("postgres"】⇒ 不会被分类轴
//   判成「打开 PG 的测试文件」⇒ 官方门的 pgFiles 计数不被扰动。
//
// ★ 口径（与官方门 TestRealDBLegsUseDeclaredDSNEnvNames 完全一致）：
//   (1) 「DSN 类环境变量名」的**字符串字面量**（dsnNameLiterals：全部大写字面量
//       + 含 DSN 或以 _URL 结尾；注释里的名字不算）
//   (2) 该文件**打开 PG 连接**（opensPostgresConn）
//   ⇒ 合起来 = 「这个名字真的决定了这个文件连不连得上库」。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestMatrixScopeProbe(t *testing.T) {
	root := filepath.Join("..", "..")
	names := strings.Fields(os.Getenv("MATRIX_SCOPE_NAMES"))
	if len(names) == 0 {
		t.Fatalf("MATRIX_SCOPE_NAMES 未设置 ⇒ 探针无效（拒绝出读数）")
	}
	outPath := os.Getenv("MATRIX_SCOPE_OUT")
	filesPath := os.Getenv("MATRIX_SCOPE_FILES_OUT")
	aFilesPath := os.Getenv("MATRIX_SCOPE_A_FILES_OUT")
	if outPath == "" || filesPath == "" || aFilesPath == "" {
		t.Fatalf("MATRIX_SCOPE_OUT / MATRIX_SCOPE_FILES_OUT / MATRIX_SCOPE_A_FILES_OUT 未设置 ⇒ 探针无效")
	}
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	// ★★★ 名字形态自检（R96 轮自曝的**真实 bug 类别**）：
	//   前一版探针用「从脚本文本里解析 MISSING_VARS=」拿名字 ⇒ 那一行带引号
	//   ⇒ 首个名字粘上前引号、末个名字粘上后引号 ⇒ **静默少算 2 个文件 / 4 个函数**
	//   ⇒ 而「19/71」这个数**看起来完全合理** ⇒ 没有任何一层会报警。
	//   ⇒ 修法一：名字从**环境变量**来（已改，不再解析脚本文本）。
	//   ⇒ 修法二：**名字必须过形态闸门**（与官方门 reRSNNameValue 同一个正则）——
	//   ⇒ 带引号/带空格/带小写的名字在**出读数之前**就中止。
	nameRe := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	for _, n := range names {
		if !nameRe.MatchString(n) {
			t.Fatalf("变量名 %q 不是合法环境变量名形态（^[A-Z][A-Z0-9_]*$）⇒ 探针口径会静默漏算 ⇒ 拒绝出读数", n)
		}
	}
	tf := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)

	bypkg := map[string]map[string]bool{}
	files := map[string]string{}
	aFiles := map[string]bool{}
	perName := map[string]int{}
	cal := map[string]int{}
	calFn := map[string]int{}
	keys := []string{"A", "B", "C", "D", "F", "E"}
	for _, k := range keys {
		cal[k] = 0
		calFn[k] = 0
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, "_test.go") {
			return nil
		}
		// ★ 官方门排除自己；探针也排除自己（否则会把自己算进「在范围」）。
		if base == "datasource_env_test.go" || base == "zz_matrix_scope_probe_test.go" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(raw)
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		nFuncs := len(tf.FindAllStringSubmatch(src, -1))
		stripped := stripGoComments(src)
		pg := opensPostgresConn(src)
		textHit, stripHit, litHit := false, false, false
		for _, n := range names {
			if strings.Contains(src, n) {
				textHit = true
			}
			if strings.Contains(stripped, n) {
				stripHit = true
			}
		}
		for _, v := range dsnNameLiterals(src) {
			if nameSet[v] {
				litHit = true
				perName[v]++
			}
		}
		rec := func(k string) {
			cal[k]++
			calFn[k] += nFuncs
		}
		if textHit {
			rec("A")
			aFiles[rel] = true
		}
		if stripHit {
			rec("B")
		}
		if pg {
			rec("E")
			if textHit {
				rec("C")
			}
			if stripHit {
				rec("F")
			}
			if litHit {
				rec("D")
			}
		}
		if !(pg && litHit) {
			return nil
		}
		dir := strings.TrimLeft(filepath.ToSlash(filepath.Dir(rel)), "./")
		if dir == "" {
			dir = "."
		}
		pkg := "./" + dir
		files[rel] = pkg
		if bypkg[pkg] == nil {
			bypkg[pkg] = map[string]bool{}
		}
		for _, m := range tf.FindAllStringSubmatch(src, -1) {
			bypkg[pkg][m[1]] = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(files) == 0 {
		t.Fatalf("在范围文件数 = 0 ⇒ 探针失效（拒绝出读数）")
	}

	pkgs := make([]string, 0, len(bypkg))
	for p := range bypkg {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	var sb strings.Builder
	nf := 0
	for _, p := range pkgs {
		ns := make([]string, 0, len(bypkg[p]))
		for n := range bypkg[p] {
			ns = append(ns, n)
		}
		sort.Strings(ns)
		if len(ns) == 0 {
			t.Fatalf("包 %s 在范围内却没有测试函数 ⇒ 探针失效", p)
		}
		nf += len(ns)
		sb.WriteString(p + "\t^(" + strings.Join(ns, "|") + ")$\n")
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("写 scope: %v", err)
	}
	rels := make([]string, 0, len(files))
	for r := range files {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	var fb strings.Builder
	for _, r := range rels {
		fb.WriteString(r + "\t" + files[r] + "\n")
	}
	if err := os.WriteFile(filesPath, []byte(fb.String()), 0o644); err != nil {
		t.Fatalf("写文件清单: %v", err)
	}
	// ★ 口径 A 的文件清单（给脚本侧做**两实现对拍**：Go 走出来的 A 必须
	//   与 python 文本 grep 走出来的 A **逐字相同** ⇒ 不等即 exit 3）。
	arels := make([]string, 0, len(aFiles))
	for r := range aFiles {
		arels = append(arels, r)
	}
	sort.Strings(arels)
	var ab strings.Builder
	for _, r := range arels {
		ab.WriteString(r + "\n")
	}
	if err := os.WriteFile(aFilesPath, []byte(ab.String()), 0o644); err != nil {
		t.Fatalf("写 A 口径文件清单: %v", err)
	}
	labels := map[string]string{
		"A": "文本含名字（不剥注释）+ 不要求开 PG",
		"B": "剥注释后含名字 + 不要求开 PG",
		"C": "文本含名字（不剥注释）+ 要求开 PG",
		"D": "官方门口径：DSN 字面量 + 要求开 PG（结论口径）",
		"F": "剥注释后【文本】含名字 + 要求开 PG",
		"E": "官方门自己的分母：全部打开 PG 的 _test.go",
	}
	for _, k := range keys {
		t.Logf("PROBE-CAL %s files=%d functions=%d  # %s", k, cal[k], calFn[k], labels[k])
	}
	t.Logf("PROBE files=%d functions=%d packages=%d", len(files), nf, len(bypkg))
	// ★ 每个变量各自命中几个文件 ⇒ **哪个名字一个文件都没命中**必须一眼可见
	//   （上一轮那个 bug 正好藏在这里：BOOTSTRAP_TEST_DSN 命中数 = 0 却没人报警）。
	for _, n := range names {
		t.Logf("PROBE-NAME %-32s 命中文件 %d", n, perName[n])
	}
}
GO
say "  [探针] 生成 ${PROBE_SRC_REL}（直接调官方门的 opensPostgresConn / dsnNameLiterals）"
if ! ( cd "$WT" && MATRIX_SCOPE_NAMES="$MISSING_VARS" MATRIX_SCOPE_OUT="$PROBE_TSV" \
        MATRIX_SCOPE_FILES_OUT="$PROBE_FILES" MATRIX_SCOPE_A_FILES_OUT="$PROBE_A_FILES" \
        go test ./scripts/ursmcheck/ -run '^TestMatrixScopeProbe$' -count=1 -v ) >"$PROBE_LOG" 2>&1; then
  say "!! 探针失败 ⇒ 拒绝出读数（exit 3）；见 $PROBE_LOG"
  tail -20 "$PROBE_LOG"
  exit 3
fi
probe_cleanup
PROBE_COUNT="$(grep -om1 -E 'PROBE files=[0-9]+ functions=[0-9]+ packages=[0-9]+' "$PROBE_LOG" 2>/dev/null)"
if [ -z "$PROBE_COUNT" ]; then
  say "!! 探针没留下读数标记 ⇒ 探针没真的跑 ⇒ 拒绝出读数（exit 3）"
  exit 3
fi
cp "$PROBE_TSV" "$SCOPE_TSV"
say "  口径 D（现：官方门口径，**结论口径**）= ${PROBE_COUNT}"
say "  ── 六套口径对照（探针实测，基线 ${BASE_COMMIT:0:8}）──"
grep -oE 'PROBE-CAL [A-Z] files=[0-9]+ functions=[0-9]+.*' "$PROBE_LOG" 2>/dev/null | sed 's/^/  /'

# ── 1c. 口径自检（★ 跑在读数之前；不一致即 exit 3）──────────────────────────
#   ① **两实现对拍**：Go 走出来的「口径 A」必须与 python 文本 grep 走出来的
#      「口径 A」**逐字相同** ⇒ 不等 ⇒ 两个实现里有一个坏了 ⇒ 拒绝出读数。
#   ② **包含关系**：口径 D 的文件集必须 ⊆ 口径 A 的文件集 ——
#      因为 A 是「文本里出现名字」的**超集**语义；
#      若 D 认了某个「文本里压根没有那些变量名」的文件 ⇒ 探针坏了。
CHECK_OUT=""
if CHECK_OUT="$(python3 - "$PROBE_A_FILES" "$SCOPE_LEGACY_FILES" "$PROBE_FILES" 2>&1 <<'PY'
import sys


def load(p):
    out = set()
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        out.add(line.split("\t")[0])
    return out


a_go = load(sys.argv[1])
a_py = load(sys.argv[2])
d_go = load(sys.argv[3])
ok = True
if a_go != a_py:
    print("✗ 两实现对拍失败：Go 的口径 A 与 python 文本 grep 的口径 A 不一致 ⇒ 必有一侧坏了")
    print(f"    只在 Go 侧（{len(a_go - a_py)} 个）: {sorted(a_go - a_py)[:10]}")
    print(f"    只在 python 侧（{len(a_py - a_go)} 个）: {sorted(a_py - a_go)[:10]}")
    ok = False
else:
    print(f"✓ 两实现对拍：口径 A 的文件集一致（{len(a_go)} 个）")
bad = sorted(d_go - a_go)
if bad:
    print(f"✗ 包含关系失败：口径 D 认了 {len(bad)} 个「文本里压根没有那些变量名」的文件 ⇒ 探针坏了")
    for x in bad[:10]:
        print("    " + x)
    ok = False
else:
    print(f"✓ 包含关系：口径 D（{len(d_go)} 个文件）⊆ 口径 A（{len(a_go)} 个文件）")
sys.exit(0 if ok else 1)
PY
)"; then
  say "  $CHECK_OUT"
else
  say "$CHECK_OUT"
  say "!! 口径自检失败 ⇒ 拒绝出读数（exit 3）"
  exit 3
fi

if [ "$SCOPE_ONLY" = "1" ]; then
  say "★ SCOPE_ONLY=1 ⇒ 只算 scope，不建库、不跑三档"
  say "  scope 文件 = $SCOPE_TSV"
  exit 0
fi

# ── 2. 三档跑 ────────────────────────────────────────────────────────────────
PW="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
if [ -z "$PW" ]; then say "!! 取不到 POSTGRES_PASSWORD"; exit 1; fi

BASE_ENV=(PATH="$PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" GOPATH="${GOPATH:-$HOME/go}"
          # ★★ r1048 轮（★ 2026-10-29）：★★ 这一段用 `env -i` 起 go test ⇒ ⇒ ★★ `env -i`
          # ★★ **会清空环境** ⇒ ⇒ ★★ ⇒ 顶部那个 `export GOCACHE` **被抹掉** ⇒ ⇒ ★★ ⇒
          # ★★ ⇒ 隔离会**失效**，而「文件里设了 GOCACHE」这个判据**照样绿** ⇒ ⇒ ★★ ⇒
          # ★★ ⇒ 所以必须**显式把它带进这个 env 数组**（★ 「设了」≠「用上了」）。
          # ★★ ⇒ ★★ 这里带的是**前导块已经设好的那个专用目录**（★ 原样带出 ⇒ ★★ 无条件
          # ★★ ⇒ ★★ ⇒ 不会回落用用户全局缓存）。
          GOCACHE="$GOCACHE" GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"
          GOPROXY="${GOPROXY:-off}" GOFLAGS="${GOFLAGS:-}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}")

mk_db() {
  # ★ 防卡保护：SET / terminate / DROP 三条独立 -c
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$1' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "SET statement_timeout = '120s'" \
    -c "DROP DATABASE IF EXISTS \"$1\" WITH (FORCE);" >/dev/null 2>&1
  local out
  out=$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
        -c "CREATE DATABASE \"$1\" TEMPLATE $SHAPE_DB" 2>&1)
  if [ $? -ne 0 ]; then say "!! CREATE $1 FAILED: $out"; return 1; fi
  # ② 建库后自检
  local got
  got=$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$1" -tAc "SELECT current_database()" 2>/dev/null \
        | grep -vE '^(WARNING|HINT|NOTICE|INFO|DETAIL)' | tail -1)
  if [ "$got" != "$1" ]; then
    say "!! 量具自检失败：期望 '$1'，实际 '$got'（PostgreSQL 折叠未加引号标识符 ⇒ 库名必须逐字一致）"
    return 1
  fi
  say "  [量具自检 OK] $1"
}

ensure_tenant() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$1" -q -v ON_ERROR_STOP=1 >/dev/null 2>&1 <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'factsource_tenant') THEN
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', 'factsource_tenant', 'factsource_tenant_pw');
  END IF;
END
$$;
SQL
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$1" -q -c \
    "GRANT CONNECT ON DATABASE \"$1\" TO factsource_tenant; GRANT USAGE ON SCHEMA public TO factsource_tenant; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO factsource_tenant; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO factsource_tenant;" >/dev/null 2>&1
}

run_cfg() { # $1=cfg $2=A|B $3=db
  local cfg="$1" which="$2" db="$3"
  # ★★ 这里**故意**写着真的 DSN 字面量 —— 本脚本真的要连库（不是夹具里的假 DSN）。
  #
  # ★ 扫描边界（写清楚，免得将来有人来「修」它）：
  #   仓内那道「写死 DSN」门 `scripts/ursmcheck/hardcoded_dsn_fallback_test.go`
  #   的 `collectHardcodedDSNTargets` 走全仓 WalkDir，但**只收 `*_test.go`**
  #   （`if !strings.HasSuffix(d.Name(), "_test.go") { return nil }`）
  #   ⇒ ⇒ **本脚本是 `.sh` ⇒ 不在那道门的射程内。**
  #   ⇒ ★ 那道门真正会抓的是「**门文件（`_test.go`）里作为夹具源码嵌入的假 DSN**」
  #   ⇒ ⇒ 那种假 DSN 要用**非 DSN 形态**的占位符（该门的 `reDSNShape` 只认
  #   `^(postgres(ql)?://|host=)`，所以 `fixture://not-a-real-dsn` 不会被当成 DSN）。
  #   ⇒ ⇒ **★ 若将来那道门扩到扫 `.sh`/`.sql`，本脚本会红 —— 那时请把本文件登记进
  #   它的豁免表（它真要连库，不是「写死的库名」），而**不要**把这里的 DSN 改成
  #   假形态 —— 改了脚本就跑不起来了。**
  local dsn="postgres://${PG_USER}:${PW}@127.0.0.1:${PGPORT}/${db}?sslmode=disable"
  local ten="postgres://factsource_tenant:factsource_tenant_pw@127.0.0.1:${PGPORT}/${db}?sslmode=disable"
  local vars="$A_VARS"; [ "$which" = "B" ] && vars="$B_VARS"
  local envargs=()
  for v in $vars; do
    if [ "$v" = "TEST_TENANT_DATABASE_URL" ]; then envargs+=("TEST_TENANT_DATABASE_URL=$ten")
    else envargs+=("$v=$dsn"); fi
  done
  while IFS=$'\t' read -r pkg runre; do
    [ -z "$pkg" ] && continue
    local slug; slug=$(echo "$pkg" | sed 's#^\./##; s#/#_#g')
    ( cd "$WT" && env -i "${BASE_ENV[@]}" "${envargs[@]}" \
        go test -tags=integration -count=1 -v -timeout 600s -run "$runre" "$pkg" ) \
        > "$OUT/${cfg}__${slug}.log" 2>&1
    say "  $cfg $pkg rc=$?"
  done < "$SCOPE_TSV"
}

say "── 2. 三档跑 ──"
for spec in "A1 A itgate_factmatrix_a1" "B1 B itgate_factmatrix_b1" "B2 B factmatrixprod_b2"; do
  set -- $spec
  cfg="$1"; which="$2"; db="$3"
  say "=== cfg ${cfg}（${which} 变量集，DSN=${db}）==="
  mk_db "$db" || { say "!! cfg $cfg 跳过（建库/自检失败）"; continue; }
  ensure_tenant "$db"
  run_cfg "$cfg" "$which" "$db"
done

# ── 3. 读数 ──────────────────────────────────────────────────────────────────
say "── 3. 读数 ──"
# ★★★ R96 轮改这里：verdict 是**三义**的 —— PASS / SKIP / FAIL / **未观测**。
#   旧实现 `sx.get(k,"-") != sy.get(k,"-")` 把「某档没跑出这条 verdict」也算成
#   「判定变化」⇒ ⇒ 实测（R90 轮旧日志）A1 档有个包只跑了一半（进程中断），
#   4 条函数的 verdict 缺失 ⇒ **凭空多出 4 条「判定变化」** ⇒ 分母/分子都被污染。
#   ⇒ 现实现：判定变化 = **两档都观测到**且 verdict 不同；
#     「未观测」单列，**不计入判定变化**，并附另一档同一条的错误文本。
#   ⇒ 再加一道**每包观测完整性**自检：期望函数数 vs 实测 verdict 数。
if ! python3 - "$OUT" "$SCOPE_TSV" <<'PY'
import re, os, sys
from collections import defaultdict
OUT, SCOPE_TSV = sys.argv[1], sys.argv[2]
RE = re.compile(r"^(\s*)--- (PASS|SKIP|FAIL): ([A-Za-z0-9_]+)")
scope, slugs = {}, []
for line in open(SCOPE_TSV):
    line = line.rstrip("\n")
    if not line or line.startswith("#"):
        continue
    pkg, runre = line.split("\t")
    slugs.append(pkg)
    for n in re.match(r"^\^\((.*)\)\$$", runre).group(1).split("|"):
        scope[(pkg, n)] = True


def logpath(cfg, pkg):
    slug = pkg.lstrip("./").replace("/", "_")
    return os.path.join(OUT, f"{cfg}__{slug}.log")


def collect(cfg):
    v = {}
    for pkg in slugs:
        p = logpath(cfg, pkg)
        if not os.path.exists(p):
            continue
        for line in open(p, errors="replace"):
            m = RE.match(line)
            if m and m.group(1) == "" and (pkg, m.group(3)) in scope:
                v[(pkg, m.group(3))] = m.group(2)
    return v


def err_text(cfg, pkg, func):
    """取该函数在日志里的错误文本（=== RUN 之后、verdict 行之前的缩进块）。"""
    p = logpath(cfg, pkg)
    if not os.path.exists(p):
        return ""
    out, grab = [], False
    for line in open(p, errors="replace").read().splitlines():
        if line.startswith("=== RUN") and line.rstrip().endswith(func):
            grab = True
            continue
        if grab:
            if re.match(r"^--- (PASS|SKIP|FAIL):", line) or line.startswith("=== RUN") \
               or line.startswith("FAIL") or line.startswith("ok "):
                break
            if line.strip():
                out.append(line.strip())
    return " / ".join(out)[:300]


data = {c: collect(c) for c in ("A1", "B1", "B2")}

# ★★★ 每包观测完整性：期望函数数 vs 实测 verdict 数
pkg_expect = defaultdict(int)
for (pkg, n) in scope:
    pkg_expect[pkg] += 1
pkg_got = {c: defaultdict(int) for c in ("A1", "B1", "B2")}
for c in ("A1", "B1", "B2"):
    for (p, n) in data[c]:
        pkg_got[c][p] += 1
incomplete = []
for c in ("A1", "B1", "B2"):
    for pkg in slugs:
        if pkg_got[c][pkg] < pkg_expect[pkg]:
            incomplete.append((c, pkg, pkg_expect[pkg], pkg_got[c][pkg]))

print(f"{'档':<5} {'PASS':>6} {'SKIP':>6} {'FAIL':>6} {'合计':>6} {'未观测':>8}")
for c in ("A1", "B1", "B2"):
    cnt = defaultdict(int)
    for x in data[c].values():
        cnt[x] += 1
    unobs = sum(1 for k in scope if k not in data[c])
    print(f"{c:<5} {cnt['PASS']:>6} {cnt['SKIP']:>6} {cnt['FAIL']:>6} "
          f"{sum(cnt.values()):>6} {unobs:>8}")
print()
if incomplete:
    print("== 每包观测完整性（★ 不足 ⇒ 该档读数**不完整**，不得当作结论发布）==")
    for c, pkg, exp, got in incomplete:
        print(f"   {c}  {pkg}  期望 {exp} / 实测 {got}  ✗ 未完整观测")
    print()

for label, x, y in [("A1→B1（轴 1 = 变量集）", "A1", "B1"),
                    ("B1→B2（轴 2 = DSN 指向）", "B1", "B2")]:
    sx, sy = data[x], data[y]
    changed, unobs = [], []
    for k in sorted(scope):
        a, b = sx.get(k), sy.get(k)
        if a is None or b is None:
            unobs.append((k, a, b))
        elif a != b:
            changed.append((k, a, b))
    print(f"== {label}：判定变化 {len(changed)} 条（**未观测不计入**）==")
    for k, a, b in changed:
        print(f"   {a} → {b}   {k[0]}::{k[1]}")
    if unobs:
        print(f"-- 未观测 {len(unobs)} 条（任一档缺 verdict ⇒ **不构成判定变化**）--")
        for k, a, b in unobs:
            other = y if a is None else x
            print(f"   {x}={a or '未观测'} / {y}={b or '未观测'}   {k[0]}::{k[1]}")
            t = err_text(other, k[0], k[1])
            if t:
                print(f"       {other} 侧错误文本: {t}")
    print()

if incomplete:
    print("★ 自检：存在「未完整观测」的包 ⇒ 本次读数**不完整** ⇒ 不得当作结论发布")
    sys.exit(3)
print("★ 自检：三档的每个包都观测到全部 verdict ✓")
PY
then
  say "!! 读数自检未过 ⇒ 本次读数**不完整** ⇒ 不得当作结论发布（exit 3）"
  exit 3
fi

# ── 4. 回收（★ 回收必须【核实】：查库确认，不得只打印一行「DROP」）──────────
say "── 4. 回收 ──"
trap - EXIT   # ★ 正常跑完 ⇒ 交回 trap（避免退出时重复 DROP）
               #   ⇒ ★ 顺序：先交还 trap 再回收 ⇒ 免得 EXIT trap 再跑一次 cleanup
if reclaim_dbs; then
  reclaimed=1
else
  reclaimed=0
fi
if [ "$KEEP_DB" = "1" ]; then
  say "  ⚠ KEEP_DB=1 ⇒ 保留形态库（${SHAPE_DB}）"
fi
say "  原始日志留在 ${OUT}（可复算）"
say "★ 提示：worktree 留在 ${WT}（origin/main 的干净副本）⇒ 复核可重复跑。"
if [ "$reclaimed" != "1" ]; then
  say "!! 有一次性库没删干净 ⇒ **本轮不成立**（exit 3）"
  exit 3
fi
