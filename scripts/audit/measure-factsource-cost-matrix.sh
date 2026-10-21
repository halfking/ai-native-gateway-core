#!/usr/bin/env bash
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
SCOPE_TSV="$OUT/scope.tsv"

# ★★★ 回收兜底（R94 轮实测修）：本脚本原本把 DROP 放在**尾部** ⇒
#   `mk_db()` 里的量具自检失败会 `return 1` ⇒ 调用方 `continue`（不退出）
#   ⇒ 已建好的 `itgate_factmatrix_a1` / `_b1` **留在库里**（尾部回收只在全部跑完后执行）
#   ⇒ 修法：trap 兜住所有退出路径（★ shell 的 trap ≈ python 的 atexit ≈ go 的 defer）。
cleanup_dbs() {
  if [ "${KEEP_DB:-0}" = "1" ]; then return 0; fi
  # ★ 用数组：写成 "for db in \"$LIST\"" 会因引号而不分词 ⇒ 循环只跑一次 ⇒ 库不被删
  local db
  for db in "${FACTMATRIX_DBS[@]}"; do
    [ -n "$db" ] || continue
    docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE)" >/dev/null 2>&1 || true
  done
}
FACTMATRIX_DBS=("itgate_factmatrix_a1" "itgate_factmatrix_b1" "factmatrixprod_b2" "$SHAPE_DB")
trap cleanup_dbs EXIT

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
shape_exists() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='$SHAPE_DB'" 2>/dev/null | grep -q 1
}
shape_mark() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "SELECT COALESCE(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname='$SHAPE_DB'" 2>/dev/null | head -1
}
shape_set_mark() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "COMMENT ON DATABASE \"$SHAPE_DB\" IS '$SHAPE_MARK'" >/dev/null 2>&1 || true
}
shape_drop() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS \"$SHAPE_DB\" WITH (FORCE)" >/dev/null 2>&1 || true
}

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

# ── 1. 在范围函数（★ 在**干净副本**里算 ⇒ 排除未跟踪文件）──────────────────
say "── 1. 算在范围（15 个缺失变量 → 测试文件 → 测试函数）──"
python3 - "$WT" "$MISSING_VARS" > "$SCOPE_TSV" <<'PY'
import re, os, subprocess, sys
REPO, missing = sys.argv[1], sys.argv[2].split()
files = set()
for v in missing:
    r = subprocess.run(["grep", "-rl", "--include=*_test.go", v, REPO],
                       capture_output=True, text=True)
    for h in r.stdout.split():
        if h.startswith(REPO + "/"):
            files.add(h[len(REPO) + 1:])
tf = re.compile(r"^func (Test[A-Za-z0-9_]*)\(", re.M)
bypkg = {}
for f in sorted(files):
    pkg = "./" + os.path.dirname(f).lstrip("./")
    for n in tf.findall(open(os.path.join(REPO, f), errors="replace").read()):
        bypkg.setdefault(pkg, set()).add(n)
for pkg in sorted(bypkg):
    names = sorted(bypkg[pkg])
    print(pkg + "\t" + "^(" + "|".join(names) + ")$")
print(f"# files={len(files)} functions={sum(len(v) for v in bypkg.values())} packages={len(bypkg)}",
      file=sys.stderr)
PY
say "  在范围：$(grep -vc '^#' "$SCOPE_TSV" 2>/dev/null || wc -l < "$SCOPE_TSV") 个包"

# ── 2. 三档跑 ────────────────────────────────────────────────────────────────
PW="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null | head -1)"
if [ -z "$PW" ]; then say "!! 取不到 POSTGRES_PASSWORD"; exit 1; fi

BASE_ENV=(PATH="$PATH" HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" GOPATH="${GOPATH:-$HOME/go}"
          GOCACHE="${GOCACHE:-$HOME/.cache/go-build}" GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"
          GOPROXY="${GOPROXY:-off}" GOFLAGS="${GOFLAGS:-}" GOTOOLCHAIN="${GOTOOLCHAIN:-local}")

mk_db() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS \"$1\" WITH (FORCE)" >/dev/null 2>&1
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
python3 - "$OUT" "$SCOPE_TSV" <<'PY'
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


def collect(cfg):
    v = {}
    for pkg in slugs:
        slug = pkg.lstrip("./").replace("/", "_")
        p = os.path.join(OUT, f"{cfg}__{slug}.log")
        if not os.path.exists(p):
            continue
        for line in open(p, errors="replace"):
            m = RE.match(line)
            if m and m.group(1) == "" and (pkg, m.group(3)) in scope:
                v[(pkg, m.group(3))] = m.group(2)
    return v


data = {c: collect(c) for c in ("A1", "B1", "B2")}
print(f"{'档':<5} {'PASS':>6} {'SKIP':>6} {'FAIL':>6} {'合计':>6}")
for c in ("A1", "B1", "B2"):
    cnt = defaultdict(int)
    for x in data[c].values():
        cnt[x] += 1
    print(f"{c:<5} {cnt['PASS']:>6} {cnt['SKIP']:>6} {cnt['FAIL']:>6} {sum(cnt.values()):>6}")
print()
for label, x, y in [("A1→B1（轴 1 = 变量集）", "A1", "B1"),
                     ("B1→B2（轴 2 = DSN 指向）", "B1", "B2")]:
    sx, sy = data[x], data[y]
    changed = [(k, sx.get(k, "-"), sy.get(k, "-")) for k in sorted(set(sx) | set(sy))
               if sx.get(k) != sy.get(k)]
    print(f"== {label}：判定变化 {len(changed)} 条 ==")
    for k, a, b in changed:
        print(f"   {a} → {b}   {k[0]}::{k[1]}")
    print()
PY

# ── 4. 回收（形态库按 KEEP_DB，其余全删）─────────────────────────────────────
say "── 4. 回收 ──"
cleanup_dbs
trap - EXIT   # ★ 正常跑完 ⇒ 交回 trap（避免退出时重复 DROP）
for db in itgate_factmatrix_a1 itgate_factmatrix_b1 factmatrixprod_b2; do
  say "  DROP $db"
done
if [ "$KEEP_DB" != "1" ]; then
  say "  DROP $SHAPE_DB"
fi
say "  原始日志留在 ${OUT}（可复算）"
say "★ 提示：worktree 留在 ${WT}（origin/main 的干净副本）⇒ 复核可重复跑。"
