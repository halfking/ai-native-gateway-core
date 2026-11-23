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
plan_file="$(mktemp -t deploy-ledger-plan)"
trap 'rm -f "$plan_file"' EXIT
awk '
  BEGIN { in_files = 0 }
  {
    line = $0
    if (!in_files) {
      if (line ~ /^files=\(/) { in_files = 1 }
      next
    }
    if (line ~ /^\)/) { in_files = 0; next }
    # ★ 只取 startup 目录下的 .sql（★ 与 Go 门同口径 ★）
    while (match(line, /"\$ROOT_DIR\/sql\/migrations\/startup\/[^"]+\.sql"/)) {
      frag = substr(line, RSTART, RLENGTH)
      gsub(/"/, "", frag)
      sub(/^\$ROOT_DIR\/sql\/migrations\/startup\//, "", frag)
      print frag
      line = substr(line, RSTART + RLENGTH)
    }
  }
' "$DEPLOY_SCRIPT" | awk '!seen[$0]++' | grep -v '\.down\.sql$' >"$plan_file"

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
missing_list=""
legacy_list=""

if [ "${ledger_table_exists:-0}" = "0" ]; then
  # ★★ 账表都不存在 ⇒ ★ 全部 ${plan_count} 条都算缺口（★ 一次性算完、★ 不查库 ★★）
  missing="${plan_count}"
  missing_list="$(cat "$plan_file")"
else
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
  rows=$(q "select sequence_name, coalesce(content_sha256, '<NULL>') from public.gateway_db_revision_sequences where sequence_name like '${SEQUENCE_NAME}:%'") || rows_rc=$?
  if [ "$rows_rc" -ne 0 ]; then
    abort "批量读权威账失败（★ 连不上库 / PG 未就绪）⇒ ★ 门无法判定"
  fi
  # ★★ 「查询成功但结果为空」＝★ 账里**确实没有**本序列的行（★ 合法、★ 断层就是全部）
  # ★★ ⇒ ★★ **不 abort** ★★ ⇒ ★ 直接进「全部缺口」分支 ★★
  # ★★ 把账里的 marker 收进一张对照表（★ basename → 指纹状态 ★★）
  ledger_markers=""
  if [ -n "${rows:-}" ]; then
    ledger_markers="$(printf '%s\n' "$rows" | awk -v seq="${SEQUENCE_NAME}:" '
      BEGIN { FS = "\t" }
      {
        name = $1
        sub("^" seq, "", name)
        printf "%s\t%s\n", name, $2
      }
    ')"
  fi

  while IFS= read -r base; do
    [ -n "$base" ] || continue
    state="$(printf '%s\n' "$ledger_markers" | awk -F'\t' -v b="$base" '$1 == b { print $2; exit }')"
    if [ -z "$state" ]; then
      missing=$((missing+1))
      missing_list="${missing_list}${base}"$'\n'
    elif [ "$state" = "<NULL>" ]; then
      # ★★ legacy 记账行（★ 指纹通道上线前应用、★ 无法证明当时应用的是哪个内容 ★★）
      # ★★ ⇒ ★ **不是断层**（★ 通道会走 legacy 分支、★ 且 legacy 重放清单另有裁决 ★★）
      legacy=$((legacy+1))
      legacy_list="${legacy_list}${base}"$'\n'
    else
      covered=$((covered+1))
    fi
  done <"$plan_file"
fi

note "  ℹ 投递面 ${plan_count} 条 ｜ 权威账有记录（含指纹）${covered} 条 ｜ legacy 记账行（★ 无指纹、★ 非断层）${legacy} 条 ｜ **缺口 ${missing} 条**"

if [ "${missing:-0}" -gt 0 ]; then
  # ★★ 判红（★ 这就是断层 ★★）：★ 「通道将要投递的 N 个文件里、★ M 个在权威账里
  # ★★ 完全没有记录」⇒ ★★ ⇒ ★ **通道会重放它们** ⇒ ★★ ⇒ ★ 而 §7.31 实测：
  # ★★ 重放在新装库上会炸（★ 臂 D 第 13 个文件 ⇒ `relation "request_logs_hot"
  # ★★ does not exist` ★★）★★ ⇒ ★★ ⇒ ★ **这道红就是那道闸门** ★★。
  bad "★★ 部署通道投递面有 ${missing} 条在权威账 public.gateway_db_revision_sequences 里**没有记录** ⇒ ★ ⇒ ★ 通道会**重放**它们 ⇒ ★★ ⇒ ★ 而 §7.31 实测：★ 重放在新装库上**必炸**（★ 臂 D：★ 第 13 个文件 ⇒ relation \"request_logs_hot\" does not exist）★★ ⇒ ★★ ⇒ ★ **处置**：★ ① 先按报告 §7.31 的授权轴定方案（A／A′ ⇒ owner 轴）★ ② 或确认这台库**本就该**由部署通道从头铺（★ 那就该由通道自己建账、★ 而不是本门红）★★"
  printf '%s' "$missing_list" | head -20 | sed 's/^/      · /'
  if [ "${missing:-0}" -gt 20 ]; then
    printf '      · ……（★ 其余 %d 条省略）\n' "$((missing - 20))"
  fi
else
  note "  ✔ 投递面每一条都在权威账里有记录 ⇒ ★ **无断层**（★ ⇒ 这台库上重放不会因「账不互通」而炸）"
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
