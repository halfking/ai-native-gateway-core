#!/usr/bin/env bash
# =====================================================================
# scripts/audit/verify_890_cutover_precheck_211.sh
#   —— 迁移 890（v1 `request_logs` 族退役）**在 211 上的**执行前置核验
# =====================================================================
#
# 为什么需要这个脚本（§143.39，生产只读实测 2026-10-21）：
#
#   §143.38 的核验脚本 `scripts/audit/verify_890_cutover_precheck.sql`
#   **依赖 `pg_stat_statements`**（判据 = 「生产上正在跑的那条语句」里
#   还有没有引用 v1 族）。⇒ ★ **211 上这条路走不通**：
#   211 的 `pg_stat_statements` 扩展虽已安装，但**没进 `shared_preload_libraries`**
#   ⇒ 慢查询量具在 211 上**完全不可用** ⇒ §143.38 的判据在 211 上是瞎的。
#
#   ⇒ 本脚本提供**不依赖任何量具的等价判据**（全部来自「产物」与「目录」）：
#     ① **已部署二进制的 SQL 形态** —— 直接从二进制里 `strings` 出聚合语句，
#        看它有没有 v1 臂（这与 `pg_stat_statements` 看到的是**同一份东西**：
#        生产上正在跑的那条语句的**源码**）。
#     ② **v1 族现状与静止性** —— 890 的非空闸会不会拒。
#     ③ **停写门** —— v1 族是不是已经死了。
#     ④ **canonical 视图的来源段** —— 890 会 `CREATE OR REPLACE` 它（去第三臂），
#        所以视图本身不是阻塞项；但**替换前的形态**决定 890 前能不能查。
#     ⑤ **升级链的列前提** —— ★★ 本轮新发现：211 缺 `session_turns.host` /
#        `session_turns_hot.tags`（迁移 885）⇒ **先部署新二进制会 42703 断写**。
#
# ★ 本脚本**只读**：全部是 SELECT + 读文件 + `strings`。
#   不做 DELETE / TRUNCATE / DROP / ALTER / CREATE / 迁移 / 改配置 / 改 cron。
#
# ── 用法 ────────────────────────────────────────────────────────────
#   # 从本机跑（默认 ssh 目标 211）
#   bash scripts/audit/verify_890_cutover_precheck_211.sh
#   bash scripts/audit/verify_890_cutover_precheck_211.sh 211
#
#   # 已经在目标机上（不经过 ssh）
#   LOCAL_ONLY=1 bash scripts/audit/verify_890_cutover_precheck_211.sh
#
#   # 可覆盖的连接参数
#   PG_CONTAINER=acc-pg17  PG_USER=acc_super  PG_DB=llm_gateway  GW_CONTAINER=llm-gateway-211-b
#
# ── 判词（verdict）──────────────────────────────────────────────────
#   BLOCKED — 至少一条阻塞项成立 ⇒ **现在不能执行 890**（或不能部署新二进制）
#   READY   — 阻塞项全清 ⇒ 890 的**机械**前置已满足
#   ⚠ 注意：READY **不等于**「建议执行」—— 890 仍会因**非空闸**而拒绝，
#     除非 v1 族已按「先归档核账再清空」处理（那是属主的语义决策）。
# =====================================================================
set -uo pipefail

SSH_TARGET="${1:-211}"
LOCAL_ONLY="${LOCAL_ONLY:-0}"
PG_CONTAINER="${PG_CONTAINER:-acc-pg17}"
PG_USER="${PG_USER:-acc_super}"
PG_DB="${PG_DB:-llm_gateway}"
GW_CONTAINER="${GW_CONTAINER:-}"

# ★ 远端模式是 `bash -s`（脚本从 stdin 进来）⇒ **BASH_SOURCE 未绑定**
#   （`set -u` 下会直接报错并把 SCRIPT_PATH 打成空）⇒ 用 `${BASH_SOURCE[0]:-}`
#   的默认展开，且**只在需要 re-exec 的那一侧**才求值。
_self="${BASH_SOURCE[0]:-}"
SCRIPT_PATH=""
if [[ -n "$_self" && -f "$_self" ]]; then
  SCRIPT_PATH="$(cd "$(dirname "$_self")" && pwd)/$(basename "$_self")"
fi

# ── 远端模式：把脚本自己 pipe 过去跑（单一真相源，不复制两份）────────
if [[ "$LOCAL_ONLY" != "1" && -n "$SSH_TARGET" ]]; then
  exec ssh -o ConnectTimeout=15 "$SSH_TARGET" "LOCAL_ONLY=1 PG_CONTAINER='$PG_CONTAINER' PG_USER='$PG_USER' PG_DB='$PG_DB' GW_CONTAINER='$GW_CONTAINER' bash -s --" < "$SCRIPT_PATH"
fi

echo "============================================================"
echo "§143.39 迁移 890 前置核验 —— 211 独立生产实例"
echo "  主机: $(hostname)   PG 容器: $PG_CONTAINER   库: $PG_DB"
echo "  时间: $(date -Is)"
echo "============================================================"

# ── 0. 找到网关容器（读 docker ps，不改任何东西）────────────────────
if [[ -z "$GW_CONTAINER" ]]; then
  GW_CONTAINER="$(docker ps --format '{{.Names}}' | grep -E '^llm-gateway-211-' | head -1)"
fi
if [[ -z "$GW_CONTAINER" ]]; then
  echo "FATAL: 找不到运行中的 llm-gateway-211-* 容器 ⇒ 判不出「二进制切没切」" >&2
  exit 2
fi
echo "[0] 网关容器: $GW_CONTAINER"

# ── 1. 已部署二进制的聚合语句形态（★ 不依赖 pg_stat_statements）─────
echo
echo "--- [1] 已部署二进制的 auto-route 亲和度聚合语句形态 ---"
# 取二进制里那段 SQL 的**上下文**：以 auto_route_selections_all 为锚点，
# 往后若干行里找 FROM _request_logs* 的臂。
BIN_SQL="$(docker exec "$GW_CONTAINER" sh -lc "strings -n 8 /app/llm-gateway | grep -A 120 'FROM auto_route_selections_all s'" 2>/dev/null || true)"
if [[ -z "$BIN_SQL" ]]; then
  echo "FATAL: 二进制里抽不到亲和度聚合语句 ⇒ 量具失效（不能判「切没切」）" >&2
  exit 3
fi
# ★ 判据的**可移植性**：远端容器是 alpine（busybox grep），本机是 BSD/GNU grep
#   ⇒ **不用 `\b` / `[[:<:]]`**（两者都不是 POSIX ERE）⇒ 用「标识符后必须跟空白」
#   的等价写法：`FROM (public\.)?_request_logs(_hot)?[[:space:]]`
#   ⇒ `_request_logs_with_current_month` / `_request_logs_bodies` 后面跟的是 `_`
#   ⇒ 不匹配 ⇒ canonical 视图与 bodies 表**不会**被误判成 v1 臂。
V1_ARM_COUNT="$(printf '%s\n' "$BIN_SQL" | grep -cE 'FROM (public\.)?_request_logs(_hot)?[[:space:]]' || true)"
SESSION_ARM="$(printf '%s\n' "$BIN_SQL" | grep -cE 'SELECT request_id, origin_actor FROM session_turns' || true)"
echo "    v1 臂（_request_logs / _request_logs_hot）条数: $V1_ARM_COUNT"
echo "    会话族臂条数: $SESSION_ARM"
if [[ "$V1_ARM_COUNT" -ge 2 ]]; then
  echo "    ⇒ 判定: **旧二进制（合成流量排除臂仍是多条臂）**"
  echo "    ⇒ 该二进制里存在引用 v1 表的语句 ⇒ 890 执行后这些语句会 **42P01**"
  BLOCK_BINARY=1
elif [[ "$V1_ARM_COUNT" -eq 1 ]]; then
  echo "    ⇒ 判定: 单 v1 臂（介于两代之间）⇒ 仍引用 v1 ⇒ 同样阻塞"
  BLOCK_BINARY=1
else
  echo "    ⇒ 判定: **新二进制（只有会话族臂）** ⇒ 不再引用 v1 表"
  BLOCK_BINARY=0
fi

# ── 2~5. 库内只读核验 ─────────────────────────────────────────────
# 说明：用 psql 一次跑完，避免多次连接。分隔输出便于逐段解析。
SQL=$(cat <<'SQL'
\pset pager off
\echo '###META###'
SELECT current_database() AS db, current_user AS usr, pg_is_in_recovery() AS in_recovery;
\echo '###GATE###'
SELECT COALESCE((SELECT value::text FROM settings_kv WHERE key='storage.request_logs_write_enabled'),'<unset>') AS v1_write_gate,
       COALESCE((SELECT value::text FROM settings_kv WHERE key='sessions_v2.enabled'),'<unset>') AS v2_enabled;
\echo '###V1ROWS###'
SELECT (SELECT count(*) FROM _request_logs_hot) AS v1_hot_rows,
       COALESCE((SELECT max(ts)::text FROM _request_logs_hot),'-') AS v1_hot_max_ts,
       (SELECT count(*) FROM request_logs_2026_10) AS v1_part10_rows,
       COALESCE((SELECT max(ts)::text FROM request_logs_2026_10),'-') AS v1_part10_max_ts,
       (SELECT count(*) FROM request_logs_bodies_2026_10) AS v1_bodies10_rows;
\echo '###COLS###'
SELECT
  (SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_turns' AND column_name='host') AS st_host,
  (SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_turns_hot' AND column_name='host') AS st_hot_host,
  (SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_turns' AND column_name='tags') AS st_tags,
  (SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_turns_hot' AND column_name='tags') AS st_hot_tags;
\echo '###V1ARMVIEW###'
SELECT (pg_get_viewdef('public.request_logs_with_current_month'::regclass, true) ~ '_request_logs_hot') AS view_refs_v1_hot,
       (pg_get_viewdef('public.request_logs_with_current_month'::regclass, true)) ~ 'session_turns' AS view_refs_session;
\echo '###LEDGER###'
SELECT count(*) AS ledger_rows, max(version) AS ledger_max FROM schema_migrations;
SQL
)

PSQL_OUT="$(docker exec -i "$PG_CONTAINER" psql -X -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tA -F'|' -f - <<< "$SQL" 2>&1)"
PSQL_RC=$?
if [[ $PSQL_RC -ne 0 ]]; then
  echo "FATAL: 库内核验失败（rc=${PSQL_RC}）—— 量具不可用，不给判词：" >&2
  printf '%s\n' "$PSQL_OUT" >&2
  exit 4
fi

get_field() { printf '%s\n' "$PSQL_OUT" | grep -A1 "^$1$" | tail -1 | cut -d'|' -f"${2:-1}"; }

echo
echo "--- [2] 停写门（storage.request_logs_write_enabled）---"
GATE="$(get_field '###GATE###' 1)"
echo "    值: $GATE"

echo
echo "--- [3] v1 族现状（890 的非空闸）---"
V1HOT="$(get_field '###V1ROWS###' 1)"; V1HOTTS="$(get_field '###V1ROWS###' 2)"
V1P10="$(get_field '###V1ROWS###' 3)"; V1P10TS="$(get_field '###V1ROWS###' 4)"
V1B10="$(get_field '###V1ROWS###' 5)"
echo "    _request_logs_hot        : $V1HOT 行 (max ts $V1HOTTS)"
echo "    request_logs_2026_10     : $V1P10 行 (max ts $V1P10TS)"
echo "    request_logs_bodies_2026_10: $V1B10 行"

echo
echo "--- [4] 升级链列前提（迁移 885 的 host/tags）---"
ST_HOST="$(get_field '###COLS###' 1)"; ST_HOT_HOST="$(get_field '###COLS###' 2)"
ST_TAGS="$(get_field '###COLS###' 3)"; ST_HOT_TAGS="$(get_field '###COLS###' 4)"
echo "    session_turns.host=$ST_HOST  session_turns_hot.host=$ST_HOT_HOST"
echo "    session_turns.tags=$ST_TAGS  session_turns_hot.tags=$ST_HOT_TAGS"
if [[ "$ST_HOST" == "0" || "$ST_HOT_HOST" == "0" || "$ST_TAGS" == "0" || "$ST_HOT_TAGS" == "0" ]]; then
  echo "    ⇒ 判定: **缺 host/tags 列** ⇒ 新二进制（写路径显式列 host/tags）会 **42703**"
  echo "    ⇒ ⇒ 顺序必须是：**先应用迁移 885，再部署新二进制**（与 890 的顺序相反）"
  BLOCK_COLS=1
else
  echo "    ⇒ 判定: host/tags 列齐备 ⇒ 新二进制可安全部署"
  BLOCK_COLS=0
fi

echo
echo "--- [5] canonical 视图的来源段 ---"
VIEW_V1="$(get_field '###V1ARMVIEW###' 1)"; VIEW_SES="$(get_field '###V1ARMVIEW###' 2)"
echo "    引用 _request_logs_hot: $VIEW_V1   引用 session_turns: $VIEW_SES"
echo "    ⇒ 890 会 CREATE OR REPLACE 该视图（去第三臂）⇒ 视图本身不是阻塞项"

echo
echo "--- [6] 台账（⚠ 仅供参考，不能当形态口径）---"
LEDGER_ROWS="$(get_field '###LEDGER###' 1)"; LEDGER_MAX="$(get_field '###LEDGER###' 2)"
echo "    schema_migrations: $LEDGER_ROWS 行, 最大版本 $LEDGER_MAX"
echo "    ⇒ ⚠ 台账稀疏 ⇒ **形态必须用对象存在性判断**（见 runbook §143.39 的 5 缺口清单）"

# ── 判词 ───────────────────────────────────────────────────────────
echo
echo "============================================================"
BLOCKERS=0
if [[ "$BLOCK_BINARY" -eq 1 ]]; then
  echo "BLOCKER-1 已部署二进制仍含 v1 臂 ⇒ 890 执行后该聚合语句整句 42P01"
  echo "          ⇒ 必须**先部署新二进制**，再执行 890"
  BLOCKERS=$((BLOCKERS+1))
fi
if [[ "$BLOCK_COLS" -eq 1 ]]; then
  echo "BLOCKER-2 缺 host/tags 列 ⇒ 部署新二进制会 42703 断写（且 boot 不会失败）"
  echo "          ⇒ 必须**先应用迁移 885**，再部署新二进制"
  BLOCKERS=$((BLOCKERS+1))
fi
if [[ "${V1P10:-0}" != "0" || "${V1HOT:-0}" != "0" ]]; then
  echo "INFO-1    v1 族仍有行（$V1P10 / hot ${V1HOT}）⇒ 890 的**非空闸会拒绝执行**"
  echo "          ⇒ 需要「先归档核账再清空」的属主决策（语义决策，不是机械前置）"
fi
if [[ "$BLOCKERS" -eq 0 ]]; then
  echo "VERDICT: READY —— 阻塞项全清（890 的机械前置已满足）"
  echo "⚠ READY 不等于「建议执行」：v1 族的清空/归档是属主的语义决策。"
else
  echo "VERDICT: BLOCKED —— $BLOCKERS 项阻塞"
fi
echo "============================================================"
exit 0
