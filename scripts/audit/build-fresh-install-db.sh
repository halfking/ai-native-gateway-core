#!/usr/bin/env bash
# =====================================================================
# scripts/audit/build-fresh-install-db.sh
#
# ★★★ 「新装环境」底账的【可复跑建造器】——★ 严格复刻安装器路径
#     （★ 依据：docs/audit/2026-10-27-R163-新装环境多环境同一标准与执行顺序.md
#       §2.2「★ 新装环境是怎么造出来的（★ 严格复刻安装器路径）」★）
#
# # 这道脚本解决什么（★ 2026-11-23 团队功能底座轮实测撞到）
#
# ★ `db-object-census.py reachability` 的判据是：★ 创建迁移**可达**、★ 而新装
#   环境**没有**它的对象 ⇒ ⇒ 判「installer-bug」（★ 棘轮基线 0 ⇒ 直接红）。
# ★ ⇒ ⇒ **这条判据隐含一个前提**：★ census 的 `fresh` 环境快照**必须晚于**
#   ★ 当次安装器清单（★ 否则「快照早于迁移」会被误判成「安装器有 bug」）。
# ★ ⇒ ★ 本脚本就是那条前提的**可执行形态**：★ 用**当前**的安装器清单造一个新装库
#   ⇒ ★ 采集它的 census ⇒ ★ 「fresh」列才是当下的真事实。
#
# # 建造口径（★ 三条，★ 与 installer/internal/dbinit/runner.go 逐条对应）
#
#   ① 基线三件：`00-prereqs.sql` → `01-schema.sql` → `02-seed.sql`
#      （★ 来自 sql/schema/；★ 顺序同 runner 的 SQLDir 顺序）
#   ② 启动迁移：**按 installed_startup_migrations.tsv 的 apply-order**逐条应用
#      ★ embeddata/startup/ 里的文件（★ TSV 由 startup_manifest_test.go 从
#      runner.go 的 StartupFiles 派生 ⇒ ★ 它就是安装器的真实 apply-order）
#   ③ 事务口径：★ 默认 `--single-transaction`；★ 文件的**注释行**里写了
#      `dbinit:no-transaction` ⇒ 走**非事务通道**
#      （★ 判定与 runner.go 的 requiresNoTransaction 同源：★ 只看 `--` 开头的
#        注释行、★ 不看正文 ⇒ ★ 避免把正文里的同名字串误判）
#
# # 用法
#
#   PG_CONTAINER=llm-gateway-pg PG_USER=postgres PG_DB=llm_gateway_fresh \
#     bash scripts/audit/build-fresh-install-db.sh
#
#   ★ 环境变量全部可覆盖；★ 默认只在本机沙箱容器里跑（★ 不碰任何生产库）。
#
#   ★ KEEP_DB 默认 **0**（★ 跑完即回收）：★ 「自建资源必须被兜底回收」是仓内纪律
#     ⇒ 默认留着就等于每次跑都泄漏一个库 ⇒ 需要留库采集 census 时【显式】 KEEP_DB=1。
#
# # 退出码：0 = 全部成功（★ 失败 0 条）；1 = 有失败（★ 逐条列名）。
# =====================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-llm_gateway_fresh}"
# ★ 默认**回收**（★ 自建资源必须兜底）⇒ 采集 census 时显式 KEEP_DB=1。
KEEP_DB="${KEEP_DB:-0}"

BASELINE_DIR="$ROOT_DIR/sql/schema"
EMBED_DIR="$ROOT_DIR/installer/cmd/llm-gw-installer/embeddata/startup"
MANIFEST="$ROOT_DIR/sql/schema/installed_startup_migrations.tsv"

fail=0
applied=0
# ★ 兜底开关：★ 正常路径跑完且要保留库 ⇒ 置 0（★ 采集 census 用）。
drop_on_exit=1

log() { printf '[fresh-db] %s\n' "$*"; }
bad() { printf '[fresh-db] ✗ %s\n' "$*" >&2; fail=$((fail + 1)); }

# ── 兜底回收（★ EXIT trap 的目标函数）──────────────────────────────────
#
# ★★★ 形态依据（★ scripts/ursmcheck/selfcreated_cleanup_quality_ledger.tsv 头）：
#   ① shell 的 `trap … EXIT` 在 SIGTERM/SIGINT 下【会执行】
#      ⇒ ⇒ shell 侧**不必**额外接管信号（★ 那是 python/go 才需要的）；
#   ② 清理语句自带【防卡保护】⇒ 两条都要：
#      SET statement_timeout='120s'（★ 仓内标准值 120s：实测「terminate 之后 DROP
#      仍要等被 SIGTERM 的后端收尾」可达 89.37s ⇒ 15s 会把**合法**清理砍成失败
#      ⇒ 失败＝泄漏＝与守卫目的相反）
#      ＋ pg_terminate_backend（★ 先踢掉占用会话再 DROP）。
#
# ★★ 三条 -c 放在**同一次 psql 调用**里：psql 的多个 -c 各自独立执行（不进同一事务）
#   ⇒ ⇒ 而把 SET 与 DROP 塞进**同一条** -c ⇒ PG 报「cannot run inside a transaction block」
#   ⇒ ⇒ 清理会【直接失败且静默】⇒ ⇒ 所以必须拆成多条 -c。
drop_fresh_db() {
  [[ "${drop_on_exit:-1}" == "1" ]] || return 0
  [[ -n "${PG_DB:-}" ]] || return 0
  log "回收数据库 ${PG_DB}（★ 踢占用会话 → 设超时 → DROP）"
  if ! docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 \
    -c "SET statement_timeout='120s'" \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$PG_DB'" \
    -c "DROP DATABASE IF EXISTS $PG_DB WITH (FORCE)" >/dev/null 2>&1; then
    log "⚠ DROP 未成功（★ -c 形态的失败退出码可见）⇒ 查落点"
  fi
  # ★★ 查落点：★ 「命令退出 0」≠「它干了活」⇒ ⇒ 不靠退出码判断成功。
  local left
  left=$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "SELECT count(*) FROM pg_database WHERE datname='$PG_DB'" 2>/dev/null | tr -d '[:space:]')
  if [[ "${left:-0}" != "0" ]]; then
    log "⚠ 库里还剩 ${left} 行（★ 清理失败 ⇒ 库泄漏）"
    return 1
  fi
  return 0
}

# ★ 兜底挂载（★ 覆盖所有退出路径：成功 / 失败 / 中断）。
trap drop_fresh_db EXIT

# ── 前置核对：★ 三件事缺一件就不该跑（★ 静默跳过会造出一份假底账）────
for f in "$BASELINE_DIR/00-prereqs.sql" "$BASELINE_DIR/01-schema.sql" "$BASELINE_DIR/02-seed.sql" "$MANIFEST"; do
  [[ -f "$f" ]] || { bad "缺前置文件：$f"; exit 1; }
done
[[ -d "$EMBED_DIR" ]] || { bad "缺分发包目录：$EMBED_DIR"; exit 1; }
docker ps --filter "name=^${PG_CONTAINER}$" --format '{{.Names}}' | grep -qx "$PG_CONTAINER" \
  || { bad "PG 容器不在场：${PG_CONTAINER}（★ 本脚本只对本机沙箱跑）"; exit 1; }

# psql_in 跑一条 SQL 文件；★ 失败时把 psql 的 stderr 留在 PSQL_ERR 里。
#
# ★★ 为什么必须留错误详情：★ 「失败」本身没有诊断价值 ⇒ ★ 建造器是**排障工具**
#   ⇒ ⇒ 把 psql 的原始报错（★ 哪一行、什么错）吞掉等于把工具废掉。
# ★ 捕获形态：`2>&1 >/dev/null` ⇒ stderr 进捕获变量、stdout 丢弃（★ 顺序不能反）。
PSQL_ERR=""
psql_in() {  # psql_in <db> <file> <single-transaction|plain>
  local db=$1 file=$2 mode=$3 out rc=0
  if [[ "$mode" == "single" ]]; then
    out=$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$db" \
      -v ON_ERROR_STOP=1 --single-transaction <"$file" 2>&1 >/dev/null) || rc=$?
  else
    out=$(docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$db" \
      -v ON_ERROR_STOP=1 <"$file" 2>&1 >/dev/null) || rc=$?
  fi
  PSQL_ERR="$out"
  return $rc
}

# ── ① 基线三件 ──────────────────────────────────────────────────────
log "重建数据库 ${PG_DB}（★ 旧库先删 ⇒ 每次都是干净的新装）"
# ★ 旧库回收走【与兜底同一份实现】⇒ 避免两处漂移（★ 只留一份 DROP）。
if ! drop_fresh_db; then
  bad "DROP DATABASE 失败（★ 查落点见上）"
  exit 1
fi
docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE $PG_DB;" >/dev/null 2>&1 \
  || { bad "CREATE DATABASE 失败"; exit 1; }

for b in 00-prereqs 01-schema 02-seed; do
  if psql_in "$PG_DB" "$BASELINE_DIR/$b.sql" single; then
    log "基线 $b ✅"
  else
    bad "基线 $b 失败"
    exit 1
  fi
done

# ── ② 启动迁移链（★ TSV apply-order ＋ no-transaction 分流）──────────
total=$(grep -v '^#' "$MANIFEST" | grep -c . || true)
log "启动迁移链：$total 条（★ 按 installed_startup_migrations.tsv 的 apply-order）"
while IFS=$'\t' read -r idx name; do
  [[ -n "${name:-}" ]] || continue
  src="$EMBED_DIR/$name"
  if [[ ! -f "$src" ]]; then
    bad "分发包缺文件：$name"
    continue
  fi
  mode="single"
  # ★ 与 runner.go 的 requiresNoTransaction 同口径：★ 只看 `--` 开头的注释行。
  if grep -E '^\s*--' "$src" | grep -q 'dbinit:no-transaction'; then
    mode="plain"
  fi
  if psql_in "$PG_DB" "$src" "$mode"; then
    applied=$((applied + 1))
  else
    bad "迁移失败：${name}（★ mode=${mode}）"
    # ★ 失败详情（★ 建造器是排障工具 ⇒ 报错不能被吞掉）。
    # ★★ 这里【不用管道】（★ `printf … | head -6 | sed …` ⇒ 消费端 head 早退 ⇒ 写端挨 EPIPE
    #   ⇒ 在 pipefail 语义下会把整条命令的表达成 141）⇒ ★ 改用 here-string 循环：
    #   ⇒ 既保留「只打前 6 行」的意图，★ 又不引入 SIGPIPE 面。
    if [[ -n "$PSQL_ERR" ]]; then
      n=0
      while IFS= read -r line; do
        printf '[fresh-db]   │ %s\n' "$line" >&2
        n=$((n + 1))
        [[ "$n" -ge 6 ]] && break
      done <<<"$PSQL_ERR"
    fi
  fi
done < <(grep -v '^#' "$MANIFEST" | grep .)

log "启动迁移：成功 $applied / 共 ${total}（★ 失败 ${fail}）"
if [[ "$fail" -gt 0 ]]; then
  bad "有 $fail 条失败 ⇒ ★ 这份库**不是**干净的新装 ⇒ 不得用于采集 census"
  exit 1
fi
if [[ "$KEEP_DB" == "1" ]]; then
  # ★ 保留模式 ⇒ 关掉兜底回收（★ 采集 census 需要这个库）。
  drop_on_exit=0
  log "库已保留：${PG_DB}（★ 采集 census 用它）"
  log "  DSN：postgresql://$PG_USER@127.0.0.1:54318/${PG_DB}（★ 端口按 docker ps 的映射调整）"
else
  log "库将在退出时回收（★ KEEP_DB=1 可保留）"
fi
exit 0
