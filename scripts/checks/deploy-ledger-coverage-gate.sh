#!/usr/bin/env bash
# =====================================================================
# scripts/checks/deploy-ledger-coverage-gate.sh —— 「部署通道投递面 ↔ 权威账」覆盖门
#
# ★★ 2026-11-23 L34 续轮新增（★ 方案 C：★ owner 清单里**唯一可在本仓自主落地**
# ★★ 的一项 —— ★ 它**只加前置断言门**、★ **不改任何投递行为** ★★）。
#
# ★★ 这道门为什么存在（一句话）★★：
#   ★★ §7.31 实测：★ 「新装库 ⇒ 直接交给部署通道」这条路**断在两处** ★★
#   ★★ 而断点**只在跑到一半时**才暴露（★ 臂 D：★ 第 13 个文件炸 ★）★★
#   ★★ ⇒ ★★ 「断层」这件事**没有可观测的信号**：★ 没人能在跑之前看出
#   ★★ ★ 这条通道会重放、★ 会炸 ★★ ⇒ ★★ ⇒ ★★ **本门把「断层」变成一条
#   ★★ 跑之前就能看出的、可判定的红** ★★。
#
# ★★ ★★ 与方案 A / A′ 的关系（★ 必须写清楚 ★★）：
#   ★ 方案 A（★ 部署通道也认 `schema_migrations` ★）⇒ **触及生产升级路径 ⇒ owner 轴**
#   ★ 方案 A′（★ A ＋ 安装器也投递那 26 条 ★）⇒ **对既有生产库＝新增 schema 变更 ⇒ owner 轴**
#   ★ 方案 C（★ **本门** ★）⇒ ★ **不改任何投递行为**、★ **零生产写**、★
#     ★ **零 schema 变更**、★ **零在役切换**、★ **零迁移字节变更** ★★
#     ★ ⇒ ★ **它只是【把断层说出来的那个声音】** ★★ ⇒ ★★ **⇒ 因此它可以自主落地**
#
# ★★ ★★ 本门**不改**部署通道的任何一行 ★★：★★
#   ★ 本门**只读**数据库（★ 只 SELECT、★ 绝不写）★★
#   ★ 本门**不调用**部署通道（★ 它只做【静态解析 ＋ 只读对账】）★★
#   ★ ⇒ ★★ **⇒ 本门跑绿不代表这条路通、★ 跑红也不代表部署通道坏了** ★★
#   ★ ⇒ ★★ **它只回答一件事**：★ **「通道将要投递的 N 个文件、★ 权威账里有几条
#   ★ 记录」** ★★ ⇒ ★★ **⇒ 这是【观测】、★ 不是【放行或拦截】** ★★。
#
# ★ ★★ 判据（三条、★ 全部可判定）★★：
#   ① **解析面自证**：★ 通道的 `files=()` 必须能解析出文件（★ 解析面为空 ⇒
#      ★ **ABORT**：★ 门自己的量具坏了、★ 不许判红 ★★）
#   ② **通道投递面 ↔ 权威账的覆盖数**：★ 打印「投递面 N 条 / 账里有记录 M 条 /
#      ★ **缺口 K 条**」⇒ ★★ **K > 0 ⇒ 红**（★ 这就是那道断层 ★★）
#   ③ **账的形状自证**：★ 权威账表**不存在** ⇒ ★ 与「空账」**区分开** ⇒ ★
#      ★★ 两者都要报出来（★ 缺表＝通道从未在这个库上跑过；★ 空表＝跑了但没记账）★★
#
# ★★★ 三态（★ 「跑不了」与「跑红了」必须分得开 ★★★）：
#   · 没有 DB 地址 ⇒ **显式 SKIP**（★ 不伪装成通过 ★★）
#   · 解析面为空 / 量具自身出错 ⇒ **ABORT（退出码 2）**（★ 门坏了、★ 不是判据红 ★★）
#   · 缺口 K > 0 ⇒ **FAIL（退出码 1）**
#   · 缺口 K = 0 ⇒ **PASS（退出码 0）**
#
# ★ 运行时依赖：★ 一个**可读**的 PG 地址（★ 只读连接即可 ★）
#   · 环境变量 `DEPLOY_LEDGER_GATE_DATABASE_URL`（★ 优先 ★）
#   · 或 `DEPLOY_LEDGER_GATE_PG_CONTAINER` ＋ `DEPLOY_LEDGER_GATE_PG_USER`
#     ＋ `DEPLOY_LEDGER_GATE_PG_DB`（★ 走容器内 psql、★ 不依赖宿主 psql ★★）
#   · 两者都没有 ⇒ SKIP（★ 仓内先例：★ 缺件时明确 SKIP、★ 不伪装成通过 ★★）
#
# ★ 调用方：★ `make guards-deploy-ledger`（★ 进 `make guards` 慢门族 ★★
#   ⇒ ★ codeup 载体的 4 核预算不受影响 ★★）★ 官方门 `verify.sh` 显式一行 ★★
# =====================================================================
set -euo pipefail

# ★★ ERR trap（★ 仓内硬约定：★ 任何意外错误 ⇒ 立刻带行号判红、★ 绝不静默 0 ★★）
# ★★ ★★ 但注意：★ ABORT 与 FAIL 必须分得开 ★★ ⇒ ★★ 所以「量具自身坏了」用
# ★★ 显式的 `abort()`（退出码 2）、★ 而 ERR trap 只兜**没预料到的**错误 ★★。
trap 'printf "FAIL: 脚本在第 %s 行异常退出（★ 这是门自身的缺陷、★ 不是判据红）\n" "$LINENO" >&2; exit 1' ERR

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

# ★★ 共享库：★ 「部署通道投递面」解析的**唯一真源**（★ 见 scripts/lib/deploy-surface.sh ★★）★★
# shellcheck source=scripts/lib/deploy-surface.sh
. "$REPO_ROOT/scripts/lib/deploy-surface.sh"

DEPLOY_SCRIPT="scripts/apply-db-revision-sequence.sh"
SEQUENCE_NAME="session-summary-and-integrity-2026-09"

SKIP=0
FAIL=0
note() { printf '%s\n' "$*"; }
skip() { printf 'SKIP: %s\n' "$*"; SKIP=1; }
bad()  { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
# ★★ ABORT（★ 退出码 2）：★ 门自己的量具坏了 ⇒ ★ **不许**当成判据红 ★★
abort() { printf 'ABORT: %s\n' "$*" >&2; exit 2; }

# ── 读 DB 地址（★ 两种形态、★ 只读）──────────────────────────────────
DB_URL="${DEPLOY_LEDGER_GATE_DATABASE_URL:-}"
PG_CONTAINER="${DEPLOY_LEDGER_GATE_PG_CONTAINER:-}"
PG_USER="${DEPLOY_LEDGER_GATE_PG_USER:-llm_gateway}"
PG_DB="${DEPLOY_LEDGER_GATE_PG_DB:-llm_gateway}"
# ★★ psql 可执行文件（★ 可覆盖 ★★）：★ 默认用 PATH 上的 psql；★ 覆盖点存在的
# ★★ 意义有二：★ ① 本机没装真 psql（★ 只有一个会丢 host/port 的转发 shim ★★）
# ★★ ⇒ ★ 允许指到任意实现；★ ② **夹具测试**：★ 用一个读预置夹具的假 psql
# ★★ ⇒ ★ 就能在**没有 PG** 的机器上验证判据的红/绿两态 ★★ ⇒ ★ ⇒ ★
# ★★ **判据逻辑的可测性不依赖 docker** ★★（★ 这对本门很关键：★ 它的判据是
# ★★ 「投递面 vs 账」的集合运算、★ 而**不是** SQL 执行成功与否 ★★）★★。
PSQL_BIN="${DEPLOY_LEDGER_GATE_PSQL_BIN:-psql}"

if [ -z "$DB_URL" ] && [ -z "$PG_CONTAINER" ]; then
  skip "没有 DB 地址（★ DEPLOY_LEDGER_GATE_DATABASE_URL / DEPLOY_LEDGER_GATE_PG_CONTAINER 都未给）⇒ ★ 这道门在本环境跑不了（★ 不是通过）"
  exit 0
fi

# ★★ 只读查询（★ 一律 SELECT ★★）★★
# ★★ 形态①：★ 容器内 psql（★ 不依赖宿主 psql、★ 也不依赖端口映射 ★★）
# ★★ 形态②：★ 宿主 psql ＋ DATABASE_URL ★★
q() {
  local sql="$1"
  # ★★ ★★ `q` 的语义（★ 本轮夹具测试抓到的真缺陷驱动了这版重写 ★★）★★
  # ★★ ★★ 「输出是空串」**不能**用来表示「查不到」★★
  # ★★ ⇒ ★ 因为「查到了、★ 而结果是零行」**也是空串** ⇒ ★★ ⇒ ★ 那两种情况
  # ★★ 混在一起时、★ 「账表存在但一行都没有」这个**关键的断层形态**就会被误判成
  # ★★ 「门无法判定」（★ ABORT）⇒ ★★ ⇒ ★ 而本轮夹具臂 B 就当场抓到了它 ★★
  # ★★ ⇒ ★★ 修法：★ **用退出码区分「查询失败」与「查询成功但零行」** ★★
  # ★★ ⇒ ★ 0 ＝ 查询成功（★ 输出可能为空 ★★）／★ 非 0 ＝ 查询失败 ★★。
  local out=""
  local rc=0
  if [ -n "$PG_CONTAINER" ]; then
    out="$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -Atc "$sql" 2>/dev/null)" || rc=$?
  else
    out="$("$PSQL_BIN" -X -v ON_ERROR_STOP=1 -Atqc "$sql" "$DB_URL" 2>/dev/null)" || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    Q_RC="$rc"
    Q_OUT=""
  else
    Q_RC=0
    Q_OUT="$out"
  fi
  printf '%s' "$Q_OUT"
  return "$Q_RC"
}

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ①：★ 解析面自证（★ 解析不出来 ⇒ ABORT、★ 不许判红）─────────
# ═══════════════════════════════════════════════════════════════════════
if [ ! -f "$DEPLOY_SCRIPT" ]; then
  abort "找不到部署通道脚本 ${DEPLOY_SCRIPT} ⇒ ★ 门无法解析投递面"
fi

# ★★ 解析 `files=()` 数组（★ 与仓内 Go 门 `deployChannelStartupOrder` **同口径**：
# ★★ 正则 `"$ROOT_DIR/sql/migrations/startup/([^"]+\.sql)"`、★ 跳过 `.down.sql`、
# ★★ **去重且保留首次位置**（★ 800 出现两次 ⇒ 首次才是投递起点 ★★）★★
# ★★★ ★★ 2026-11-24 修正（★ 真缺陷、★ 实测坐实）：★★
# ★★★ 第一版是**整段正则匹配** ⇒ ★★ **被注释掉的行也会被匹配到** ⇒ ★★
# ★★★ 而通道脚本里有**两条被注释掉的**投递条目：★★
# ★★★   · `666_llm_hourly_stats_direct_trigger.sql`（★ 注释写明 Abandoned ★★）
# ★★★   · `890_drop_request_logs_v1_family.sql`（★ 注释写明「已移出自动重放列表」★★）
# ★★★ ⇒ ★★ 第一版解析出 **357** 条、★ 而**真实投递面是 355 条** ⇒ ★★
# ★★★ ⇒ ★★ **修法**：★ **逐行解析**、★ **只取未被注释的行**（★ `#` 开头 ★★）★★
plan_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-plan.XXXXXX")"
trap 'rm -f "$plan_file"' EXIT
# ★★ 2026-11-24（L34 七轮）：★ 解析逻辑搬进**共享库** `scripts/lib/deploy-surface.sh` ★★
# ★★ ⇒ ★★ 本门与「列序对拍门」**共用同一份投递面解析** ⇒ ★★ **口径不可能分叉** ★★
# ★★ （★ 仓内既有教训：★ 「同一条规定写了两份」⇒ ★ 两边各改各的 ⇒ ★ 门与门互相矛盾 ★★）★★
# ★★ 口径：★ 逐行解析 files=() ｜★ 只取未注释的行 ｜★ 剥 `$ROOT_DIR/sql/migrations/startup/`
# ★★ 前缀（★ index+substr、★ 零 awk 方言依赖 ★★）｜★ 去重且保留首次位置 ｜★ 跳过 .down.sql ★★
surface_rc=0
deploy_surface_extract "$DEPLOY_SCRIPT" "$plan_file" || surface_rc=$?
if [ "$surface_rc" -ne 0 ]; then
  abort "从 ${DEPLOY_SCRIPT} 的 files=() 里解析出 0 条投递文件 ⇒ ★ 门自己的解析器坏了（★ **不是**判据红）"
fi

plan_count=$(wc -l <"$plan_file" | tr -d ' ')
if [ "${plan_count:-0}" -eq 0 ]; then
  abort "从 ${DEPLOY_SCRIPT} 的 files=() 里解析出 0 条投递文件 ⇒ ★ 门自己的解析器坏了（★ **不是**判据红）"
fi
note "部署通道投递面：${plan_count} 条（★ 解析自 ${DEPLOY_SCRIPT} 的 files=()）"

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ③：★ 账的形状自证（★ 缺表 ≠ 空表 ★★）────────────────────────
# ═══════════════════════════════════════════════════════════════════════
ledger_table_exists=""
ledger_table_rc=0
ledger_table_exists=$(q "select count(*) from pg_class where relname='gateway_db_revision_sequences'") || ledger_table_rc=$?
if [ "$ledger_table_rc" -ne 0 ]; then
  abort "读权威账的表形状失败（★ 连不上库 / PG 未就绪）⇒ ★ 门无法判定"
fi

ledger_rows=0
if [ "${ledger_table_exists:-0}" != "0" ]; then
  ledger_rows_rc=0
  ledger_rows=$(q "select count(*) from public.gateway_db_revision_sequences") || ledger_rows_rc=$?
  if [ "$ledger_rows_rc" -ne 0 ]; then
    abort "读权威账行数失败 ⇒ ★ 门无法判定"
  fi
  # ★ ★★ 注意：★ 这里**不能**把「零行」当成失败（★ 零行是**合法且关键**的
  # ★ ★★ 断层形态：★ 「跑了但没记账」★★）⇒ ★ 零行就是一个合法的 0 ★★
fi

if [ "${ledger_table_exists:-0}" = "0" ]; then
  note "  ℹ 权威账表 public.gateway_db_revision_sequences **不存在** ⇒ ★ 这台库上部署通道**从未跑过**（★ 与「跑了但没记账」是两回事）"
else
  note "  ℹ 权威账 public.gateway_db_revision_sequences 存在、★ 当前 ${ledger_rows:-0} 行"
fi

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ②：★ 投递面 ↔ 权威账的覆盖（★ **断层在这里显形**）────────────
# ═══════════════════════════════════════════════════════════════════════
# ★★ marker 口径与通道**逐字一致**：★ `<sequence_name>:<basename>` ★★
# ★★ ★★ 一次批量查询（★ 而不是逐文件查 357 次 ★★）：★★
# ★★   · 把投递面的 basename 拼成一个数组字面量、★ 一次 `= ANY(...)` 查回来 ★★
# ★★   · 同时把**有记录但 content_sha256 为空**的行单列出来（★ legacy 记账行 ★★）
# ★★     ⇒ ★★ **它们不是断层**：★ 通道对它们走的是 legacy 分支（★ 通道脚本
# ★★     第 3294 行起 ★★）⇒ ★ **把它们算成缺口＝误报** ⇒ ★★ ⇒ ★ 分类必须做 ★★
covered=0
missing=0
legacy=0
fault=0
pending=0
missing_list=""
legacy_list=""
fault_list=""
pending_list=""
ledger_markers_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-markers.XXXXXX")"
class_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-class.XXXXXX")"
missing_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-missing.XXXXXX")"
class2_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-class2.XXXXXX")"
installer_version_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-instver.XXXXXX")"
trap 'rm -f "${plan_file:-}" "${ledger_markers_file:-}" "${class_file:-}" "${missing_file:-}" "${class2_file:-}" "${installer_version_file:-}"' EXIT
: >"$ledger_markers_file"

if [ "${ledger_table_exists:-0}" != "0" ]; then
  # ★★ 构造数组字面量：★ `'{a.sql,b.sql,...}'`（★ 全部来自仓内解析、★ 无注入面 ★★）
  arr_literal="'"
  first=1
  while IFS= read -r base; do
    [ -n "$base" ] || continue
    if [ "$first" = "1" ]; then
      first=0
    else
      arr_literal="${arr_literal},"
    fi
    arr_literal="${arr_literal}${base}"
  done <"$plan_file"
  arr_literal="${arr_literal}'"

  # ★★ 一次查回：★ marker 全名 + 内容指纹 ★★
  # ★★ ★★ 用 `-At` + 换行分隔（★ 逐行读；★ 指纹可能为空 ⇒ ★ 用 `<TAB>` 分隔、
  # ★★ 空的指纹用哨兵 `<NULL>` 顶替、★ 免得两列粘在一起 ★★）★★
  # ★★ ★★ 判据位一律用**真实退出码**（★ 不用 `|| true` 兜、★ 因为「查询失败」
  # ★★ 与「查询成功但零行」必须分得开 ★★ ⇒ ★★ 失败 ⇒ ABORT ★★）★★
  rows=""
  rows_rc=0
  # ★★ ★★ 用 `concat_ws(chr(9), …)` 显式指定 **TAB** 分隔 ★★ ⇒ ★★ ⇒ ★★
# ★★ **不要依赖 psql `-A` 的默认分隔符** —— ★★ 实测（★ 本轮在生产 211 上
# ★★ 实测坐实 ★★）：★ **`-A` 的默认字段分隔符是 `|`（竖线）**、★ 不是 TAB ★★
# ★★ ⇒ ★★ ⇒ ★★ 门按 TAB 切 ⇒ ★★ 切不出来 ⇒ ★★ ⇒ ★★ **每行都被误判成
# ★★ 「账里没有这条」⇒ ★★ ⇒ ★★ 缺口数虚高到全量 ★★（★ 本轮实测：★ 真值
# ★★ 是 0 条缺口、★ 门却报 355 条 ★★）★★ ⇒ ★★ ⇒ ★★ **⇒ 必须显式给分隔符** ★★
rows=$(q "select concat_ws(chr(9), sequence_name, coalesce(content_sha256, '<NULL>')) from public.gateway_db_revision_sequences where sequence_name like '${SEQUENCE_NAME}:%'") || rows_rc=$?
  if [ "$rows_rc" -ne 0 ]; then
    abort "批量读权威账失败（★ 连不上库 / PG 未就绪）⇒ ★ 门无法判定"
  fi
  # ★★ 「查询成功但结果为空」＝★ 账里**确实没有**本序列的行（★ 合法、★ 断层就是全部）
  # ★★ ⇒ ★★ **不 abort** ★★ ⇒ ★ 直接进「全部缺口」分支 ★★
  # ★★ 把账里的 marker 收进一张对照表（★ basename → 指纹状态 ★★）
  # ★★ ★★ 2026-11-24 改写：★ **落成文件**（★ 而不是留在一个 shell 变量里 ★★）★★
  # ★★ ⇒ ★★ 因为下一遍要在**一次 awk** 里做集合判定（★ 逐文件查 356 次太慢 ★★）★★
  if [ -n "${rows:-}" ]; then
    printf '%s\n' "$rows" | awk -v seq="${SEQUENCE_NAME}:" '
      BEGIN { FS = "\t" }
      {
        name = $1
        sub("^" seq, "", name)
        printf "%s\t%s\n", name, $2
      }
    ' >"$ledger_markers_file"
  fi
fi

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ⑤：★ 投递文件的**当前内容指纹**（★ 与通道 `sha256_file` **同口径** ★★）──
# ═══════════════════════════════════════════════════════════════════════
# ★★ 为什么需要它（★ 2026-11-24 L34 四轮、★ **实测坐实的盲区** ★★）：★★
# ★★ 通道的跳过判据**不是「账里有这个文件」**、★ 而是**「账里的指纹 == 当前文件的指纹」** ★★
# ★★ ⇒ ★★ **⇒ 账里有、★ 而指纹过期的文件、★ 通道会【内容重放】**（★ 通道脚本自己的注释：
# ★★ 「★ 文件在应用后被内容加固过 ⇒ 按契约（★ 每个迁移幂等 ★★）兜底重放安全性 ★★」）★★
# ★★ ⇒ ★★ **⇒ 而旧口径把它算成「覆盖」（★ 不判红 ★★）⇒ ★★ ⇒ ★★ **那是一个盲区** ★★
# ★★ ★★ 口径纪律：★ 指纹算法必须与通道**逐字一致**（★ 通道用 `sha256sum` → `shasum -a 256`
# ★★ ★★ 的回退链、★ 取的是**文件原始字节**的 sha256 ★★）⇒ ★★ ⇒ ★★ 本门照抄那条链 ★★
SQL_DIR="${REPO_ROOT}/sql/migrations/startup"
if command -v sha256sum >/dev/null 2>&1; then
  SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SHA_CMD="shasum -a 256"
else
  abort "找不到 sha256sum / shasum ⇒ ★ 门无法计算内容指纹（★ 通道用的是同一条回退链 ★★）"
fi
sha_map_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-shas.XXXXXX")"
present_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-present.XXXXXX")"
absent_file="$(mktemp "${TMPDIR:-/tmp}/deploy-ledger-absent.XXXXXX")"
trap 'rm -f "${plan_file:-}" "${ledger_markers_file:-}" "${class_file:-}" "${missing_file:-}" "${class2_file:-}" "${installer_version_file:-}" "${sha_map_file:-}" "${present_file:-}" "${absent_file:-}"' EXIT
: >"$present_file"
: >"$absent_file"
while IFS= read -r base; do
  [ -n "$base" ] || continue
  path="${SQL_DIR}/${base}"
  if [ -f "$path" ]; then
    printf '%s\n' "$path" >>"$present_file"
  else
    printf '%s\n' "$base" >>"$absent_file"
  fi
done <"$plan_file"
: >"$sha_map_file"
if [ -s "$present_file" ]; then
  # ★ 一次算完（★ 356 个文件一次 sha256sum ⇒ 比逐文件快两个数量级 ★★）
  xargs $SHA_CMD <"$present_file" 2>/dev/null | awk -v d="${SQL_DIR}/" '
    { p = $2; if (index(p, d) == 1) { b = substr(p, length(d) + 1) } else { b = p }; print b "\t" $1 }
  ' >"$sha_map_file"
fi
sha_count=$(wc -l <"$sha_map_file" | tr -d ' ')
absent_count=$(wc -l <"$absent_file" | tr -d ' ')
note "  ℹ 内容指纹：${sha_count} 条已算（★ 口径＝通道的 \`sha256_file\` ★★）｜磁盘上不在场 ${absent_count} 条"

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ② 之第一遍：★ 投递面 ↔ 权威账（★ covered / legacy / stale / missing / absent ★★）
# ═══════════════════════════════════════════════════════════════════════
# ★★ 为什么改成「两遍」而不是「一遍到底」★★：
# ★★   第一遍只做【投递面 ↔ 权威账 ＋ 当前指纹】的集合判定（★ 不需要任何别的信息 ★★）
# ★★   第二遍只对【missing 的那些】做【是不是断层】的判定（★ 需要安装器那本账 ★★）
# ★★ ⇒ ★★ **⇒ 没有缺口时压根不用去查第二本账**（★ 少一次只读查询 ★★）★★
awk '
  BEGIN { FS = "\t" }
  FILENAME == ARGV[1] { cur[$1] = $2; next }
  FILENAME == ARGV[2] { stored[$1] = $2; next }
  FILENAME == ARGV[3] { gone[$1] = 1; next }
  {
    base = $0
    if (base in gone) { print base "\tabsent\t投递面文件在磁盘上不在场 ⇒ 通道跑到它会直接失败"; next }
    if (!(base in stored)) { print base "\tmissing\t"; next }
    if (stored[base] == "<NULL>") { print base "\tlegacy\t"; next }
    if ((base in cur) && stored[base] == cur[base]) { print base "\tcovered\t"; next }
    print base "\tstale\t账里指纹与当前文件不一致 ⇒ 通道会按当前内容重放"
  }
' "$sha_map_file" "$ledger_markers_file" "$absent_file" "$plan_file" >"$class_file"

covered=$(awk -F'\t' '$2=="covered"{c++} END{print c+0}' "$class_file")
legacy=$(awk -F'\t' '$2=="legacy"{c++} END{print c+0}' "$class_file")
missing=$(awk -F'\t' '$2=="missing"{c++} END{print c+0}' "$class_file")
stale=$(awk -F'\t' '$2=="stale"{c++} END{print c+0}' "$class_file")
absent=$(awk -F'\t' '$2=="absent"{c++} END{print c+0}' "$class_file")
awk -F'\t' '$2=="missing"{print $1}' "$class_file" >"$missing_file"

# ═══════════════════════════════════════════════════════════════════════
# ── 判据 ④：★ 安装器那本账（★ `public.schema_migrations` ★）★★
# ── ★★ 「缺口」不等于「断层」——★ 这一段就是把那两者分开的地方 ★★─────
# ═══════════════════════════════════════════════════════════════════════
# ★★★ ★★ 为什么要加这一段（★ 2026-11-24 L34 三轮、★ 撞出来的**恒红型缺陷** ★★★）：
# ★★★ ★★ 「不在权威账里」有两个**互斥**的成因（★ 而旧判据把它们混成一个 ★★）：★★
# ★★★ ★★   · **已被别的路径应用过**（★ 典型：★ 安装器先应用了、★ 权威账不知道 ★★）
# ★★★ ★★     ⇒ ★ ⇒ ★ 通道会**重放**它 ⇒ ★★ ⇒ ★ **这才是断层**（★ §7.31 臂 D ★★）★★
# ★★★ ★★   · **从没被任何路径应用过**（★ 典型：★ 上游刚合进来的新迁移 ★★）
# ★★★ ★★     ⇒ ★ ⇒ ★ 通道会**首次**应用它 ⇒ ★★ ⇒ ★ **这是设计内行为**（★ 不是断层 ★★）★★
# ★★★ ★★ ⇒ ★★ 旧判据把第二种也判成红 ⇒ ★★ **⇒ 每当上游合进一条新迁移、★ 部署窗口内
# ★★★ ★★ 这道门就是红的** ⇒ ★★ ⇒ ★★ **⇒ 恒红的门会被忽略 ⇒ 等于没有** ★★★
# ★★★ ★★ ⇒ ★★ **⇒ 判据升级为「已被任何路径应用过」** ⇒ ★★ 需要第二本账 ★★ ⇒ ★★ ⇒ ★★
# ★★★ ★★ **⇒ 只读查 `public.schema_migrations`**（★ 安装器那本账 ★★）★★ ★★★
# ★★★ ★★ ★★ 方案 C 的边界**不变**（★ 必须写清楚 ★★）：★★
# ★★★ ★★ ★★ 本门**只读**第二本账**只为分类缺口**、★ **不写、不改、不回灌** ★★★
# ★★★ ★★ ★★ ⇒ ★★ **部署通道本身仍然只认它自己那本账**（★ 那正是方案 A 要解决的 ★★）★★
# ★★★ ★★ ★★ ⇒ ★★ **⇒ 本门没有让部署通道「认安装器的账」**（★ 仍是纯观测 ★★★）
if [ "${missing:-0}" -gt 0 ]; then
  installer_versions_ok=0
  installer_versions_rc=0
  installer_versions="$(q "select version from public.schema_migrations")" || installer_versions_rc=$?
  if [ "$installer_versions_rc" -eq 0 ]; then
    installer_versions_ok=1
    # ★★ 版本号归一化：★ 生产上存的是**零填充 3 位**（★ `087` ★★）⇒ ★★ ⇒ ★★
    # ★★ 两边都**去掉前导零**再比（★ 数字相等即可、★ 不比写法 ★★）★★
    printf '%s\n' "$installer_versions" | awk '
      { v = $1; sub(/^0+/, "", v); if (v == "") { v = "0" }; print v }
    ' | sort -u >"$installer_version_file"
  fi

  awk -v installer_ok="$installer_versions_ok" '
    BEGIN { FS = "\t" }
    FILENAME == ARGV[1] { inst[$0] = 1; next }
    {
      base = $1
      ver = ""
      if (match(base, /^[0-9]+/)) {
        ver = substr(base, RSTART, RLENGTH)
        sub(/^0+/, "", ver)
        if (ver == "") { ver = "0" }
      }
      if (installer_ok != "1") {
        print base "\tfault\t安装器那本账不可读 ⇒ 不可判定（★ 保守判红 ★★）"
        next
      }
      if (ver == "") {
        print base "\tfault\t文件名无数字版本号 ⇒ 不可判定（★ 保守判红 ★★）"
        next
      }
      if (ver in inst) {
        print base "\tfault\t版本 " ver " 已在安装器那本账里 ⇒ 已被别处应用过 ⇒ 通道会重放"
        next
      }
      print base "\tpending\t版本 " ver " 从未被应用过 ⇒ 通道将首次应用（★ 设计内行为 ★★）"
    }
  ' "$installer_version_file" "$missing_file" >"$class2_file"

  fault=$(awk -F'\t' '$2=="fault"{c++} END{print c+0}' "$class2_file")
  pending=$(awk -F'\t' '$2=="pending"{c++} END{print c+0}' "$class2_file")
  fault_list="$(awk -F'\t' '$2=="fault"{print "      · " $1 "（" $3 "）"}' "$class2_file")"
  pending_list="$(awk -F'\t' '$2=="pending"{print "      · " $1 "（" $3 "）"}' "$class2_file")"
else
  fault=0
  pending=0
  fault_list=""
  pending_list=""
fi

note "  ℹ 投递面 ${plan_count} 条 ｜ 覆盖（★ 指纹一致、★ 通道会跳过 ★）${covered} 条 ｜ legacy 行（★ 无指纹 ★）${legacy} 条 ｜ **内容加固重放（★ 指纹过期、★ 通道按设计重放 ★★）${stale} 条** ｜ 缺口 ${missing} 条 ⇒ ★ **断层 ${fault} 条** ｜ 待部署 ${pending} 条 ｜ 磁盘不在场 ${absent_count} 条"

# ★★ 「文件不在场」也是红（★ 通道跑到它会**直接失败**、★ 那不是重放风险 ★★）★★
absent_list="$(awk -F'\t' '$2=="absent"{print "      · " $1 "（" $3 "）"}' "$class_file")"
fault_total=$((fault + absent_count))

if [ "${fault_total:-0}" -gt 0 ]; then
  # ★★ 判红（★ 这就是断层 ★★）：★ 「通道将要投递的文件里、★ 有 N 个**已被别的路径
  # ★★ 应用过**（★ 而权威账不知道 ★★）」⇒ ★★ ⇒ ★ **通道会重放它们** ⇒ ★★ ⇒ ★ 而
  # ★★ §7.31 实测：★ 重放在新装库上会炸（★ 臂 D 第 13 个文件 ⇒ `relation
  # ★★ "request_logs_hot" does not exist` ★★）★★ ⇒ ★★ ⇒ ★ **这道红就是那道闸门** ★★。
  bad "★★ 部署通道投递面有 ${fault_total} 条**判为断层**（★ 要么【版本在安装器那本账 public.schema_migrations 里 ⇒ 已被别处应用过 ⇒ 通道会重放】、★ 要么【投递面文件在磁盘上不在场 ⇒ 通道跑到它会直接失败】 ★★）⇒ ★★ ⇒ ★ 而 §7.31 实测：★ 重放在新装库上**必炸**（★ 臂 D：★ 第 13 个文件 ⇒ relation \"request_logs_hot\" does not exist）★★ ⇒ ★★ ⇒ ★ **处置**：★ ① 先按报告 §7.31 的授权轴定方案（A／A′ ⇒ owner 轴）★ ② 或确认这台库**本就该**由部署通道从头铺（★ 那就该由通道自己建账、★ 而不是本门红）★★"
  # ★★ 打印前 20 条：★ **不用 `head`、★ 改用 awk**（★ 2026-11-24 L34 三轮 ★★）
  # ★★ 根因：★ `head -20` 会**提前关闭读端** ⇒ ★★ 写端挨 EPIPE ⇒ ★★ 写端退出码
  # ★★ 变 141 ⇒ ★★ 而本脚本 `set -o pipefail` ⇒ ★★ **管道状态取 141** ⇒ ★★ ⇒ ★★
  # ★★ ⇒ ★★ 实测（★ 本轮 macOS bash 3.2 / 5.2 双版本 ★★）：★ 写端 ≤ 63,599 B
  # ★★ ⇒ 管道状态 0（不翻转 ★★）；★ 写端 264,999 B ⇒ 管道状态 **141（翻转）** ★★
  # ★★ ⇒ ★★ ⇒ ★★ **⇒ 边界就是管道容量（≈ 64 KiB）** ★★ ⇒ ★★ ⇒ ★★
  # ★★ ⇒ ★★ **⇒ 而这里的写端体量随缺口条数与理由文本长度变化 ⇒ ★★ ⇒ ★★ 不该把判据
  # ★★ 押在「它恰好小于 64 KiB」上** ⇒ ★★ ⇒ ★★ **⇒ 改用 `awk 'NR<=20'`**（★ awk
  # ★★ 会把输入读完、★ **不提前关管道** ⇒ ★★ 写端永不挨 EPIPE ⇒ ★★ 与体量无关 ★★★）
  printf '%s\n' "$fault_list" | awk 'NR<=20 { print "  " $0 }'
  printf '%s\n' "$absent_list" | awk 'NR<=20 { print "  " $0 }'
  if [ "${fault_total:-0}" -gt 20 ]; then
    printf '      · ……（★ 其余 %d 条省略）\n' "$((fault_total - 20))"
  fi
else
  if [ "${pending:-0}" -gt 0 ]; then
    note "  ✔ **无断层**（★ 另有 ${pending} 条**从没被应用过** ⇒ ★ 通道会**首次**应用它们 ⇒ ★ **不判红**、★ 但**报出来** ★★）"
    printf '%s\n' "$pending_list" | awk 'NR<=20 { print "  " $0 }'
    if [ "${pending:-0}" -gt 20 ]; then
      printf '      · ……（★ 其余 %d 条省略）\n' "$((pending - 20))"
    fi
  else
    note "  ✔ 投递面每一条都在权威账里有记录 ⇒ ★ **无断层**（★ ⇒ 这台库上重放不会因「账不互通」而炸）"
  fi
fi

if [ "${stale:-0}" -gt 0 ]; then
  # ★★ 这**不是**断层（★ 通道**自己知道**、★ 它主动选择重放 ★★）⇒ ★★ 报出来、★ 不判红 ★★
  note "  ℹ 另有 **${stale} 条**【内容加固型】：★ 权威账里有记录、★ 但**指纹与当前文件不一致** ⇒ ★★ 通道会**按当前内容重放**它们（★ 这是通道**设计内**的行为、★ 它的契约是「每个迁移幂等」 ★★）⇒ ★★ **⇒ 不判红**（★ 报出来是为了让人知道「这次部署会重放这 ${stale} 条」 ★★）★★"
  awk -F'\t' '$2=="stale"{print "      · " $1 "（" $3 "）"}' "$class_file" | awk 'NR<=20 { print "  " $0 }'
  if [ "${stale:-0}" -gt 20 ]; then
    printf '      · ……（★ 其余 %d 条省略）\n' "$((stale - 20))"
  fi
fi

if [ "$FAIL" -ne 0 ]; then
  printf 'deploy-ledger-coverage-gate: FAIL（%d 条判据不满足）\n' "$FAIL" >&2
  exit 1
fi
if [ "$SKIP" -ne 0 ]; then
  printf 'deploy-ledger-coverage-gate: SKIP\n'
  exit 0
fi
note "deploy-ledger-coverage-gate: PASS"
exit 0
