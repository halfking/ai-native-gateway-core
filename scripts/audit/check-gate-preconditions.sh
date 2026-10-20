#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/check-gate-preconditions.sh
# Purpose:       Read-only pre-flight for "切事实源" (switching the official
#                integration gate to `source scripts/audit/datasource_env.sh`).
#
#                It answers exactly one question:
#                **如果现在把官方 runner 切到事实源，还差哪些前置？**
#
# Why this exists (measured, R71-R73 rounds):
#   · 切事实源的**收益**已实测：16 条腿从「静默 SKIP」变成真跑
#     （它们各自缺的 DSN 名都在事实源里）。
#   · 切事实源的**代价**也已实测：那 5 条隔离型腿从「静默 SKIP」变成**红**，
#     而红的性质取决于两件**环境前置**：
#       ① 隔离守卫的放行条件 = 「库名合规」AND「隔离开关 = 1」⇒ 两者缺一即红；
#       ② Citus worker 节点缺失 ⇒ 分布组元数据在并发下不稳
#          （pg_dist_local_group XX000 / could not open relation with OID / deadlock 40P01）。
#   · 这两件前置**都不在**「环境变量名」这个维度里 ⇒ 事实源**装不下**它们
#     ⇒ 光看 `datasource_env.sh` 是看不出缺口的 ⇒ 本脚本补这一层。
#
# Safety: **只读**。只做 SELECT 与读文件；不建库、不改库、不改任何仓内文件。
#
# Usage:
#   scripts/audit/check-gate-preconditions.sh [DB_NAME]
#
#   DB_NAME  可选。默认用最近一个由官方 runner 建出来的门禁库
#            （形如 itgate_<pid>_<rand>）；找不到就用 postgres 库做形态检查。
#
# Exit:  0 = 前置齐备；1 = 有缺项（逐条列出）；2 = 用法/环境错误。
# -----------------------------------------------------------------------------
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"

DB_NAME="${1:-}"

fail() { echo "✗ $*" >&2; exit 2; }

# ── 0. 环境检查 ────────────────────────────────────────────────────────────────
command -v docker >/dev/null 2>&1 || fail "docker 不在 PATH"
docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" || fail "容器 $PG_CONTAINER 未运行"

psql_in() { # psql_in <db> <sql>
  docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$1" -tAc "$2" 2>/dev/null
}

if [[ -z "$DB_NAME" ]]; then
  DB_NAME="$(psql_in postgres "select datname from pg_database where datname ~ '^itgate_[0-9]+_[0-9]+$' order by datname desc limit 1")"
  DB_NAME="${DB_NAME:-postgres}"
  echo "── 目标库（未指定，取最近的 runner 门禁库）：$DB_NAME"
else
  echo "── 目标库：$DB_NAME"
fi

EXISTS="$(psql_in postgres "select count(*) from pg_database where datname='$DB_NAME'")"
[[ "$EXISTS" == "1" ]] || fail "库 $DB_NAME 不存在"

MISSING=0
note() { echo "  $1"; }
missing() { echo "  ✗ $1"; MISSING=$((MISSING+1)); }

# ── 1. Citus 形态：worker 节点是否存在 ────────────────────────────────────────
echo ""
echo "── ① Citus worker 节点（分布组元数据的来源）"
CITUS_VER="$(psql_in "$DB_NAME" "select extversion from pg_extension where extname='citus'")"
if [[ -z "$CITUS_VER" ]]; then
  missing "该库没有 citus 扩展 ⇒ 若被测对象含分布式表，任何分布组访问都会失败"
else
  note "citus 扩展版本 = $CITUS_VER"
  NODES="$(psql_in "$DB_NAME" "select count(*) from citus_nodes")"
  GROUPS="$(psql_in "$DB_NAME" "select count(*) from pg_dist_local_group")"
  note "citus_nodes 行数 = ${NODES:-?}；pg_dist_local_group 行数 = ${GROUPS:-?}"
  if [[ "${NODES:-0}" == "0" ]]; then
    missing "**citus worker 节点 = 0** ⇒ 有本地分组也无节点 ⇒ 分布组元数据缓存拿不到
      ⇒ 症状：cache lookup failed for pg_dist_local_group (XX000) /
        could not open relation with OID … (XX000) / deadlock detected (40P01)
      ⇒ 这些红**不是产品缺陷**，而是环境形态。
      ⇒ 注册节点需要容器级配置（本机 citus_add_node 的第三参是 worker group id 不是密码；
        citus.node_conninfo 是 postmaster 级参数 ⇒ 需重启容器）⇒ 本脚本只报，不改。"
  else
    note "worker 节点已注册 ⇒ 这一项齐备"
  fi
fi

# ── 2. 隔离守卫的放行条件：库名合规 AND 开关=1 ───────────────────────────────
echo ""
echo "── ② 隔离守卫的放行条件（放行 = 库名合规 AND 开关=1）"
if [[ "$DB_NAME" == *_test ]]; then
  note "库名合规（以 _test 结尾）"
else
  missing "库名 $DB_NAME **不合规**（不以 _test 结尾）⇒ 隔离守卫必然拦人
      ⇒ 官方 runner 的库名 itgate_\$_\${RANDOM} 天然不合规 ⇒ 它与隔离型腿天然互斥"
fi
for sw in TEST_SESSION_INGEST_ISOLATED TEST_PG_CONTRACTS_ISOLATED; do
  val="${!sw:-}"
  if [[ "$val" == "1" ]]; then
    note "$sw = 1"
  else
    missing "$sw 未设为 1（当前='${val:-<unset>}'）⇒ 合规库名下守卫**照样拦人**
      ⇒ ⚠ 它**不能**由事实源默认打开（事实源是跑真库腿时被 source 的东西 ⇒
        默认打开 = 取消保护）⇒ 必须由跑的人与「合规库名」成对显式给出"
  fi
done

# ── 3. 官方 runner 的 env 块 vs 事实源：哪些腿在官方跑法下拿不到 DSN ──────────
echo ""
echo "── ③ 官方 runner 注入的 DSN 名 vs 事实源"
runner_env="$(sed -n '/^env \\/,/go test/p' "$REPO_ROOT/scripts/audit/run-integration-gate.sh" \
  | grep -oE '^\s+[A-Z][A-Z0-9_]*=' | tr -d ' =' | sort -u)"
# ★ 只取**名字**：事实源的 export 行形如 `export NAME="$__datasource_dsn"`
#   ⇒ 用 `grep -oE '^export [A-Z][A-Z0-9_]*'` 取到名字为止，
#   否则 awk 取到的会是 `NAME="$__datasource_dsn"` ⇒ 列表失真（本轮踩过）。
src_env="$(grep -oE '^export [A-Z][A-Z0-9_]*' "$REPO_ROOT/scripts/audit/datasource_env.sh" \
  | awk '{print $2}' | sort -u)"
only_src="$(comm -23 <(echo "$src_env") <(echo "$runner_env") | paste -sd' ' -)"
only_runner="$(comm -13 <(echo "$src_env") <(echo "$runner_env") | paste -sd' ' -)"
note "runner 注入 $(echo "$runner_env" | wc -w | tr -d ' ') 个；事实源 $(echo "$src_env" | wc -w | tr -d ' ') 个"
note "仅在事实源（切事实源后才会被注入）：${only_src:-（无）}"
if [[ -n "$only_runner" ]]; then
  note "仅在 runner（切事实源后仍由 runner 提供，属专用隔离）：${only_runner}"
fi

# ── 结论 ──────────────────────────────────────────────────────────────────────
echo ""
if [[ "$MISSING" -eq 0 ]]; then
  echo "═══ 前置齐备 ═══"
  exit 0
fi
cat <<EOF
═══ 有 $MISSING 项前置未齐备 ═══
  ⇒ 「切事实源」会把上面这些项对应的腿从**静默 SKIP**（绿但空）变成**红**
  ⇒ ⇒ 建议：先齐备上面各项，再切事实源；否则门禁会从「绿但空」变成「红」。
EOF
exit 1
