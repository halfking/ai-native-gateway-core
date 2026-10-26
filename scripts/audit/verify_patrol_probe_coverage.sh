#!/usr/bin/env bash
# =============================================================================
# verify_patrol_probe_coverage.sh —— 实测「巡检基线覆盖探针」的新判据。
#
# 2026-10-22（R75 轮 · P11）新增。
#
# -----------------------------------------------------------------------------
# 它要证明的那件事
# -----------------------------------------------------------------------------
# 巡检脚本 ursm-snapshot-health.sh 在选基线路径时问了一个问题：
#   「汇总表 ursm_snapshot_slot_daily 够不够覆盖基线窗？」
#
# ★ 旧判据问的是【另一个问题】：
#     min(day) <= 今天 - 基线天数
#   ⇒ 它问的是「表里有没有【足够老】的数据」。
# ★★ 而快路真正需要的是「有没有【足够新】的数据」——
#   快路读的窗口是 [今天-基线天数, 今天)，**缺一天就少一天样本**。
# ⇒⇒ 旧判据在【表停更】时依然成立（min(day) 不会随时间前进）
#   ⇒ 巡检会【继续走快路】，而快路读到一天比一天少的基线
#   ⇒ 第 8 天左右快路在窗口内【一行都读不到】⇒ 全部桶判 NO-BASELINE
#   ⇒ ★★ **巡检静默失明**（与 892 自己的失败模式同型）。
#
# ⇒ 新判据：窗口内的【每一天】都有数据
#     count(DISTINCT day) over [今天-基线天数, 今天) == 基线天数
#   ⇒ 缺任何一天 ⇒ 探测失败 ⇒ 回退源表路 ⇒ **慢，但不失明**。
#
# -----------------------------------------------------------------------------
# 这个脚本怎么做到「判别」
# -----------------------------------------------------------------------------
# 单看本地现状**区分不了**新旧判据（本地数据本来就少一天，两边都回退）。
# ⇒ 必须构造一个**只有新旧判据才会给出不同答案**的场景：
#     · 在窗口【中间】挖掉一天（模拟「那天的刷新失败了」）
#     · 再补一条【更老】的（窗口起点那天）⇒ 把 min(day) 拉到今天-基线天数
#   ⇒ 此时：
#       旧判据：min(day)=今天-7 <= 今天-7 ⇒ 「覆盖已足」⇒ 走快路 ⇒ ★★ 误判
#       新判据：窗口内只有 6/7 天 ⇒ 「没覆盖」⇒ 回退源表路 ⇒ ★ 正确
#
# ⚠ 全程只碰**汇总表**（可由幂等函数完整重建），不碰任何源表。
# ⚠ 结束时用 refresh_ursm_snapshot_slot_daily 把汇总表恢复到真实内容。
#
# 用法：
#   bash scripts/audit/verify_patrol_probe_coverage.sh
#   DSN='postgresql://user:pass@host:5432/db' bash scripts/audit/verify_patrol_probe_coverage.sh
# =============================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PATROL="$REPO_ROOT/scripts/252-monitor/ursm-snapshot-health.sh"
DB="${PG_DB:-llm_gateway}"
PG_USER="${PG_USER:-llm_gateway}"
BASELINE_DAYS="${BASELINE_DAYS:-7}"

fail() { echo "FAIL: $*" >&2; exit 1; }

# ---- 连接 ----
if [ -n "${DSN:-}" ]; then
  PSQL_BASE=(psql -X -At "$DSN")
else
  : "${PGHOST:?需要 DSN 或 PGHOST/PGPORT/PGUSER/PGPASSWORD}"
  : "${PGPORT:?}"
  PSQL_BASE=(psql -X -At -h "$PGHOST" -p "$PGPORT" -U "$PG_USER" -d "$DB")
fi
q() { "${PSQL_BASE[@]}" -c "$1"; }

echo "== 前置：汇总表与填充函数必须在位 =="
[ "$(q "SELECT coalesce(to_regclass('public.ursm_snapshot_slot_daily')::text,'');")" = "ursm_snapshot_slot_daily" ] \
  || fail "public.ursm_snapshot_slot_daily 不存在 ⇒ 先应用迁移 892"
[ -n "$(q "SELECT coalesce(to_regprocedure('public.refresh_ursm_snapshot_slot_daily(integer,text)')::text,'');")" ] \
  || fail "public.refresh_ursm_snapshot_slot_daily(integer,text) 不存在"

TODAY=$(q "SELECT (now() AT TIME ZONE current_setting('TimeZone'))::date;")
NEED_FROM=$(q "SELECT ((now() AT TIME ZONE current_setting('TimeZone'))::date - make_interval(days => $BASELINE_DAYS))::date;")
echo "   today=$TODAY  window=[$NEED_FROM, $TODAY)  baseline_days=$BASELINE_DAYS"

run_patrol() {
  # ★ 用脚本自带的 PSQL_CMD 注入点顶替 podman（本机没有 podman 容器）。
  # ★★ PSQL_CMD 的契约：**必须自带结尾的 -c**（脚本的默认值里那个 -c 属于默认值，
  #   一旦注入 PSQL_CMD 就替换掉整个默认值 ⇒ 漏了 -c ⇒ psql 把 SQL 当位置参数
  #   ⇒ 报「extra command-line argument」这种**误导性**错误。
  #   （scripts/audit/verify_892_patrol_e2e.sh 里的写法是 `... -tAX -c`，同款契约。）
  PSQL_CMD="psql -X -At -h ${PGHOST:-127.0.0.1} -p ${PGPORT:-5432} -U $PG_USER -d $DB -c" \
  PGPASSWORD="${PGPASSWORD:-}" \
    bash "$PATROL" 2>&1 |  grep -E "base_source=" | tail -1
}

# ---- 真实内容快照（用于还原）----
echo "== 用幂等函数把汇总表恢复到真实内容 =="
q "SELECT public.refresh_ursm_snapshot_slot_daily(8, 'hour');" >/dev/null

echo
echo "== 场景 A：窗口完整 ⇒ 期望 base_source=daily =="
# 先补齐窗口内缺的天（本地源表可能本来就少一天 ⇒ 用一条占位行补上，
#   ★ 它只为把「窗口缺一天」与「表停更」这两种情形分开，测完即删）。
q "INSERT INTO public.ursm_snapshot_slot_daily(day, granularity, slot, bucket_ts, rows)
   SELECT g::date, 'hour', 0, g::timestamptz, 1
     FROM generate_series('$NEED_FROM'::date, ('$TODAY'::date - 1), '1 day') AS g
    WHERE NOT EXISTS (SELECT 1 FROM public.ursm_snapshot_slot_daily s
                       WHERE s.day = g::date AND s.granularity = 'hour');" >/dev/null
A=$(run_patrol)
echo "   ${A}"
echo "$A" | grep -q "base_source=daily" || fail "场景 A 期望 base_source=daily，实得：$A"

echo
echo "== 场景 B：窗口【中间】缺一天 + 有一条【更老】的行 ⇒ 期望 base_source=source =="
# ① 挖掉窗口中间的一天（模拟那天的刷新失败）
HOLE=$(q "SELECT ('$NEED_FROM'::date + 1)::text;")
q "DELETE FROM public.ursm_snapshot_slot_daily WHERE day = '$HOLE' AND granularity='hour';" >/dev/null
# ② 保证 min(day) 落在「今天 - 基线天数」⇒ ★ **旧判据在这里会判「覆盖已足」**
q "INSERT INTO public.ursm_snapshot_slot_daily(day, granularity, slot, bucket_ts, rows)
   SELECT '$NEED_FROM', 'hour', 99, '$NEED_FROM'::timestamptz, 1
    WHERE NOT EXISTS (SELECT 1 FROM public.ursm_snapshot_slot_daily
                        WHERE day='$NEED_FROM' AND granularity='hour');" >/dev/null

OLD_MIN=$(q "SELECT min(day)::text FROM public.ursm_snapshot_slot_daily WHERE granularity='hour';")
COV=$(q "SELECT count(DISTINCT day)::text FROM public.ursm_snapshot_slot_daily
          WHERE granularity='hour' AND day >= '$NEED_FROM'::date AND day < '$TODAY'::date;")
echo "   min(day)=$OLD_MIN  need_min=$NEED_FROM  窗口内天数=$COV/$BASELINE_DAYS"
if [ "$OLD_MIN" \< "$NEED_FROM" ] || [ "$OLD_MIN" = "$NEED_FROM" ]; then
  echo "   ⇒ ★ 旧判据 (min(day) <= need_min) 会判【覆盖已足】⇒ 走快路"
else
  echo "   ⇒ ⚠ 旧判据也会回退 ⇒ 本场景【不具判别性】（说明构造没生效）"
  fail "场景 B 构造失败：旧判据本来就会回退 ⇒ 判别性不成立"
fi
B=$(run_patrol)
echo "   ${B}"
echo "$B" | grep -q "base_source=source" || fail "场景 B 期望 base_source=source，实得：$B"

echo
echo "== 场景 C：表整个不存在 ⇒ 期望 base_source=source =="
q "ALTER TABLE public.ursm_snapshot_slot_daily RENAME TO ursm_snapshot_slot_daily_hidden;" >/dev/null
C=$(run_patrol)
echo "   ${C}"
echo "$C" | grep -q "base_source=source" || fail "场景 C 期望 base_source=source，实得：$C"
q "ALTER TABLE public.ursm_snapshot_slot_daily_hidden RENAME TO ursm_snapshot_slot_daily;" >/dev/null

# ---- 还原：把汇总表恢复成真实内容（删掉所有占位行）----
echo
echo "== 还原：清掉占位行并用幂等函数重建真实内容 =="
# ★ 还原 = 删掉占位行 + 用幂等函数整体重算。
#   （占位行的 bucket_ts 与真实整点桶相同 ⇒ 重算会原位覆盖它们 ⇒
#     最终表内容与「纯真实内容」逐字一致；实测还原后行数 = 首次回填行数。）
q "DELETE FROM public.ursm_snapshot_slot_daily WHERE slot IN (0, 99);" >/dev/null
q "SELECT public.refresh_ursm_snapshot_slot_daily(8, 'hour');" >/dev/null
FINAL=$(q "SELECT count(*)::text FROM public.ursm_snapshot_slot_daily WHERE granularity='hour';")
echo "   hour 行数=${FINAL}（还原完成）"

echo
echo "=== 三个场景全部符合预期 ⇒ 新判据在判别性场景下与旧判据不同，且新判据是对的 ==="
