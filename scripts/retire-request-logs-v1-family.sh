#!/usr/bin/env bash
# =====================================================================
# scripts/retire-request-logs-v1-family.sh
# §143.53 —— v1 request_logs 族的【显式退役步骤】
#
# ── 为什么需要这个脚本 ────────────────────────────────────────────────
#
# ★ 迁移 890（sql/migrations/startup/890_drop_request_logs_v1_family.sql）
#   自带**非空闸**：v1 族里有行就 RAISE EXCEPTION，把「要不要丢历史」交回人。
#   ★ 这个闸本身是**对的**（删表 = 删历史，不可回滚）。
#   ★ 但 890 同时躺在**每次部署的自动重放列表**里
#     （scripts/apply-db-revision-sequence.sh 的 files=(...)）⇒
#     ⇒ 自动通道每次部署都会真的执行它 ⇒ ⇒ 撞闸 ⇒ ⇒ 迁移腿中止
#     ⇒ ⇒ 部署中止，且【它后面的 891/892 永远上不去】。
#
# ★ 结构性结论：**退役类迁移（自带非空闸 ⇒ 天然不可自动重放）不能放在
#   每次部署的自动列表里**，必须拆成显式的、属主授权的退役步骤。
#   —— 本脚本就是那个「显式退役步骤」。
#
# ── 两个模式 ──────────────────────────────────────────────────────────
#
#   --defer    只在**台账**里登记「890 已刻意不由自动通道执行」。
#              自动通道的跳过规则是：
#                  台账行 content_sha256 == 文件当前 sha256 ⇒ 跳过
#              （scripts/apply-db-revision-sequence.sh 的执行循环）
#              ⇒ 本模式写一行 sha 相同的台账 ⇒ 自动通道下次读到即跳过
#              ⇒ ⇒ **部署不再被非空闸挡住**，且 v1 历史**原样保留**。
#              ⇒ 这是「留」选项的落地形态；随时可撤销（见 --undo-defer）。
#
#   --execute  执行退役：前置逐表计数 → 应用 890 → 台账换成真实执行记录。
#              ★ 这是**删历史** ⇒ 必须由属主显式授权。
#
#   --undo-defer  撤销 defer（删掉台账里的 defer 行）⇒ 自动通道恢复执行 890
#                 ⇒ ⇒ 下一次部署会撞非空闸（这是预期行为，不是 bug）。
#
# ── 为什么 defer 不需要改 apply-db-revision-sequence.sh ────────────────
#
# ★ 自动通道的跳过条件**只看台账行的 content_sha256**，不看有没有真的执行过。
#   ⇒ ⇒ 登记一行「sha 相同 + 运行时适配标记」就能让自动通道跳过，
#     **无需修改那个被并行会话同时编辑的文件**（同文件不要两个写者）。
#   ⇒ ★ 诚实的记账：execution_sha256 留 NULL（确实没有执行过），
#     runtime_adaptation 写 'deferred-to-explicit-retirement'（自述为什么）。
#
# ── 授权 ──────────────────────────────────────────────────────────────
#
# ★ 三个模式都是**生产写**（改台账 / 删表）⇒ 一律要求：
#     LLM_GATEWAY_RETIREMENT_ACK=1
#   不设这个变量 ⇒ 脚本在任何连接之前就退出（不产生任何写）。
#
# ── 用法 ──────────────────────────────────────────────────────────────
#
#   # 直接 psql：
#   DATABASE_URL='postgresql://...' LLM_GATEWAY_RETIREMENT_ACK=1 \
#     bash scripts/retire-request-logs-v1-family.sh --defer
#
#   # 容器内 psql（与 apply-db-revision-sequence.sh 同款）：
#   LLM_GATEWAY_PG_CONTAINER=llm-gateway-pg LLM_GATEWAY_PG_USER=llm_gateway \
#   LLM_GATEWAY_PG_DATABASE=llm_gateway LLM_GATEWAY_PG_PASSWORD=... \
#   LLM_GATEWAY_RETIREMENT_ACK=1 \
#     bash scripts/retire-request-logs-v1-family.sh --defer
#
#   # 只看前置计数（只读，不写）：
#   bash scripts/retire-request-logs-v1-family.sh --preflight
# =====================================================================
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# ★ 路径可被环境变量覆盖：远端主机（211/154/245）上通常**没有仓库副本**
#   ⇒ ⇒ 只把本脚本 + 890 迁移文件带过去执行时，用 LLM_GATEWAY_RETIREMENT_SQL 指过去。
#   ⚠ 覆盖路径下的文件必须与仓内 890 **逐字一致**（sha 相同）⇒ 否则 defer 写的
#   content_sha256 与自动通道算出的 sha 不一致 ⇒ ⇒ 自动通道【不会】跳过 ⇒ 静默失效。
RETIREMENT_SQL="${LLM_GATEWAY_RETIREMENT_SQL:-$ROOT_DIR/sql/migrations/startup/890_drop_request_logs_v1_family.sql}"

# ★★★ 台账 marker 是按【basename】派生的：`<序列名>:<basename>`
#   ⇒ ⇒ **文件内容 sha 一致 ≠ 身份一致**：若把 890 传到远端时改了文件名，
#     defer 写进去的 marker 就与自动通道查找的 marker **不是同一个 key**
#     ⇒ ⇒ 自动通道查不到那一行 ⇒ ⇒ **照样会执行 890** ⇒ ⇒ 静默失效。
#   ⇒ ⇒ 这里**响亮失败**（不静默写一行没用的台账）。
CANONICAL_BASE="890_drop_request_logs_v1_family.sql"
ACTUAL_BASE="$(basename "$RETIREMENT_SQL")"
if [[ "$ACTUAL_BASE" != "$CANONICAL_BASE" ]]; then
  printf 'error: 退役迁移的文件名必须是 %s（台账 marker 按 basename 派生，改名 ⇒ 静默失效）\n' \
    "$CANONICAL_BASE" >&2
  printf '       当前：%s\n' "$ACTUAL_BASE" >&2
  exit 4
fi
LEDGER_TABLE="public.gateway_db_revision_sequences"
SEQUENCE_NAME="session-summary-and-integrity-2026-09"
DEFER_MARKER="deferred-to-explicit-retirement"

MODE=""
for arg in "$@"; do
  case "$arg" in
    --defer|--execute|--undo-defer|--preflight) MODE="$arg" ;;
    *) printf 'error: unknown argument %s\n' "$arg" >&2; exit 2 ;;
  esac
done
if [[ -z "$MODE" ]]; then
  printf 'usage: %s --defer|--execute|--undo-defer|--preflight\n' "$0" >&2
  exit 2
fi

# ── ★ 授权闸在最前面：必须在建立任何连接之前判定 ─────────────────────
#   三个写模式都是生产写 ⇒ 没有显式授权就退出，且**不产生任何写**。
#   ⚠ 顺序纪律：授权检查必须排在「建通道」之前 ⇒ 否则「没配 DSN」会掩盖
#     「没授权」⇒ 报错会指向错误的根因。
if [[ "$MODE" != "--preflight" && "${LLM_GATEWAY_RETIREMENT_ACK:-}" != "1" ]]; then
  printf 'error: %s is a production write; set LLM_GATEWAY_RETIREMENT_ACK=1 to proceed\n' "$MODE" >&2
  exit 3
fi

# ── psql 通道（与 apply-db-revision-sequence.sh 同款：直连 or 容器）────
# ★★ 2026-10-27（R165 轮）★★ 两处修正：
#   ① 直连通道的客户端优先用 $LLM_GATEWAY_PSQL（★ 显式指定）⇒ ★ 否则 PATH 首位的
#      `psql` 是个**容器转发 shim**，★ 它**故意丢掉 host/port** ⇒ ⇒ 走容器内默认
#      socket ⇒ ⇒ 连到**另一个集群** ⇒ ⇒ 报「库不存在」（★ 极具误导性）。
#   ② 容器通道必须**显式 TCP**（★ -h 127.0.0.1 -p 5432）：容器内**默认 socket** 指向
#      的是镜像自带的初始化集群，★ 不是挂载的共享集群 ⇒ ⇒ 不加 -h/-p 会连错库。
if [[ -n "${DATABASE_URL:-}" ]]; then
  _retire_psql_bin="${LLM_GATEWAY_PSQL:-}"
  if [[ -z "$_retire_psql_bin" || ! -x "$_retire_psql_bin" ]]; then _retire_psql_bin="psql"; fi
  psql_q() { "$_retire_psql_bin" -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -Atqc "$1"; }
  psql_f() {
    # ★ -1 = 单事务：退役要么整体成功，要么整体回滚
    #   （890 会 DROP TABLE + 换视图 + 换函数 ⇒ 半途失败会留下「半退役」形态）
    "$_retire_psql_bin" -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -1 -f -
  }
elif [[ -n "${LLM_GATEWAY_PG_CONTAINER:-}" ]]; then
  : "${LLM_GATEWAY_PG_PASSWORD:?set LLM_GATEWAY_PG_PASSWORD for Docker execution}"
  psql_q() {
    docker exec -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5432 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -Atqc "$1"
  }
  psql_f() {
    # ★ -1 = 单事务：退役要么整体成功，要么整体回滚
    #   （890 会 DROP TABLE + 换视图 + 换函数 ⇒ 半途失败会留下「半退役」形态）
    docker exec -i -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5432 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -1 -f -
  }
else
  printf 'error: set DATABASE_URL or LLM_GATEWAY_PG_CONTAINER\n' >&2
  exit 2
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

LEDGER_MARKER="${SEQUENCE_NAME}:$(basename "$RETIREMENT_SQL")"

# ── 前置计数：与 890 的非空闸同款（两种物理名都试 + 真 count）────────
# ⚠ 必须用 count(*) 而不是 reltuples：闸判的是「真有没有行」，
#   而 reltuples 是估算（252 上 _request_logs_bodies_hot est=0 但 291 MB）。
preflight_counts() {
  psql_q "
DO \$\$
DECLARE
  v_name text;
  v_cnt  bigint;
BEGIN
  FOREACH v_name IN ARRAY ARRAY[
      'request_logs', '_request_logs',
      'request_logs_hot', '_request_logs_hot',
      'request_logs_bodies', '_request_logs_bodies',
      'request_logs_bodies_hot', '_request_logs_bodies_hot',
      'request_logs_archive', '_request_logs_archive'
  ] LOOP
    IF to_regclass('public.' || v_name) IS NOT NULL THEN
      EXECUTE format('SELECT count(*) FROM public.%I', v_name) INTO v_cnt;
      RAISE NOTICE 'preflight % -> % rows', v_name, v_cnt;
    ELSE
      RAISE NOTICE 'preflight % -> absent', v_name;
    END IF;
  END LOOP;
END \$\$;"
}

case "$MODE" in
  --preflight)
    # 只读模式：不要求授权，不产生任何写。
    printf 'preflight for %s\n' "$RETIREMENT_SQL"
    printf 'ledger marker: %s\n' "$LEDGER_MARKER"
    preflight_counts
    exit 0
    ;;
esac

FILE_SHA="$(sha256_of "$RETIREMENT_SQL")"

case "$MODE" in
  --defer)
    # ★ 登记「已刻意不由自动通道执行」：content_sha256 填文件当前 sha
    #   ⇒ 自动通道读到 sha 相同 ⇒ 跳过（scripts/apply-db-revision-sequence.sh）
    #   ⇒ execution_sha256 留 NULL（诚实：确实没执行过）
    #   ⇒ runtime_adaptation 写自述标记
    printf 'deferring %s (sha %s) in %s\n' "$LEDGER_MARKER" "${FILE_SHA:0:12}" "$LEDGER_TABLE"
    psql_q "
ALTER TABLE ${LEDGER_TABLE} ADD COLUMN IF NOT EXISTS content_sha256 text;
ALTER TABLE ${LEDGER_TABLE} ADD COLUMN IF NOT EXISTS execution_sha256 text, ADD COLUMN IF NOT EXISTS runtime_adaptation text;
INSERT INTO ${LEDGER_TABLE} (sequence_name, content_sha256, execution_sha256, runtime_adaptation)
VALUES ('${LEDGER_MARKER}', '${FILE_SHA}', NULL, '${DEFER_MARKER}')
ON CONFLICT (sequence_name) DO UPDATE
  SET content_sha256 = EXCLUDED.content_sha256,
      execution_sha256 = NULL,
      runtime_adaptation = EXCLUDED.runtime_adaptation;"
    printf 'deferred. v1 family stays; automatic deploy channel will skip %s\n' "$(basename "$RETIREMENT_SQL")"
    ;;

  --undo-defer)
    printf 'removing defer row for %s\n' "$LEDGER_MARKER"
    psql_q "
DELETE FROM ${LEDGER_TABLE}
 WHERE sequence_name = '${LEDGER_MARKER}'
   AND runtime_adaptation = '${DEFER_MARKER}';"
    printf 'defer removed. the automatic deploy channel will attempt %s again\n' "$(basename "$RETIREMENT_SQL")"
    ;;

  --execute)
    # ★ 真正退役：先把 defer 行删掉（否则自动通道会以为已经处理过），
    #   再前置计数，再应用 890，最后写一条真实执行记录。
    printf 'executing retirement: %s\n' "$RETIREMENT_SQL"
    printf 'preflight (same rule as the 890 non-empty gate):\n'
    preflight_counts
    psql_q "DELETE FROM ${LEDGER_TABLE} WHERE sequence_name = '${LEDGER_MARKER}' AND runtime_adaptation = '${DEFER_MARKER}';"
    psql_f < "$RETIREMENT_SQL"
    psql_q "
INSERT INTO ${LEDGER_TABLE} (sequence_name, content_sha256, execution_sha256, runtime_adaptation)
VALUES ('${LEDGER_MARKER}', '${FILE_SHA}', '${FILE_SHA}', 'executed-via-explicit-retirement')
ON CONFLICT (sequence_name) DO UPDATE
  SET content_sha256 = EXCLUDED.content_sha256,
      execution_sha256 = EXCLUDED.execution_sha256,
      runtime_adaptation = EXCLUDED.runtime_adaptation;"
    printf 'retirement executed and recorded.\n'
    ;;
esac
