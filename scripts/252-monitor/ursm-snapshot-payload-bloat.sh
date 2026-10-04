#!/usr/bin/env bash
# ursm-snapshot-payload-bloat.sh —— 快照 payload 膨胀巡检（2026-10-04）
#
# 存在的原因：818 把 31 个 hash 键提升为 typed 列后，writer 会把它们从
# payload 里剔除（domains/ursm/v2/persist/writer.go 的 payloadDuplicateKeys）。
# 实测这项收益极大：payload 205 B → 24 B、整行 396 B → 213 B（-46%），
# 折合 3.22M 行/天 ≈ -583 MB/天。
#
# ★ 为什么这道巡检必须放在**二进制之外**：
#   一旦有人把 154/245 回滚到 pre-818 的旧二进制，跑的就是那个**不带任何
#   检查的旧进程** —— 把判据写进新二进制，它自己就永远不会被触发。
#   818 这次就是无声发生的：切换发生在 06:50:48，直到按 payload 逐 5 分钟
#   统计才被发现；期间没有任何告警。
#   而且 245 的 Prometheus 采集一直是黑的、154 根本没采集
#   （见 runbook §5.12），所以「加个指标」在当前部署形态下没人会看见。
#
# 判据：**payload 的字节大小**，不硬编码键名。
#   键名清单会漂（writer.go 注释原话：「7 vs 31 就是这么来的」），而字节数不会；
#   且字节数直接就是我们真正要守住的东西。键名只出现在**诊断输出**里，
#   按实际占用字节排前 N，不参与判定 ⇒ 诊断与判定解耦，加字段不会让判据失效。
#
# 退出码（与 ursm-snapshot-health.sh 同一套契约）：
#   0  健康
#   1  检出膨胀
#   3  **本次没有结论**（量具不可用 / 无参照系 / 数据陈旧）
#   ★ 3 与 0 必须可区分：「查不到」不是「没问题」。
#     历史上被坑过的形态正是「量具跑不通 ⇒ 记成跳过 ⇒ exit 0 报成功」。

set -uo pipefail

CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
SAMPLE=${SAMPLE_ROWS:-5000}
# 阈值。依据：818 之后 payload 稳定在 ~24 B；切换前 ~205 B。
# 取 64 B 做线：既高于正常抖动（当前实测 23.7~25.2 B），又远低于异常态。
BLOAT_BYTES=${BLOAT_BYTES:-64}
# 样本新鲜度上限（秒）。快照写入停了的时候，最新行会越来越旧；
# 那时报告的是**陈旧**数据，必须报「没有结论」而不是「健康」。
STALE_SECONDS=${STALE_SECONDS:-3600}

log() { printf '%s %s\n' "$(date -Is)" "$*" >&2; }

# PSQL_CMD 可被测试覆盖（见 ../../scripts/252-monitor/ 下同名测试）。
# 生产默认走 252 的既有形态：sudo -u postgres + 显式 conf。
# ★ 保留成可注入的形式，是为了让「0 行 ⇒ exit 3」这类分支**真的被跑到**；
#   否则它们只在真出故障时才第一次被执行。
PSQL_CMD=${PSQL_CMD:-sudo -u postgres psql -X -q -A -t -F'|' -v ON_ERROR_STOP=1 --file "$CONF" -c}

psql_17() {
  local sql=$1
  # shellcheck disable=SC2086 # PSQL_CMD 是刻意按多词命令传入的
  $PSQL_CMD "$sql" 2>&1
}

# 判据 + 新鲜度 + 样本量，一次取回，字段数必须恰好 4。
#   1) n            样本行数
#   2) newest_age_s 最新样本的年龄（秒）；NULL = 表为空
#   3) avg_b        payload 平均字节
#   4) p95_b        payload 95 分位字节
SQL=$(cat <<EOF
WITH s AS (
  SELECT payload, snapshot_ts
  FROM public.ursm_node_snapshot_min
  ORDER BY snapshot_ts DESC
  LIMIT $SAMPLE
)
SELECT count(*)::text,
       coalesce(extract(epoch FROM (now() - max(snapshot_ts)))::int::text, ''),
       coalesce(round(avg(pg_column_size(payload)))::text, '0'),
       coalesce(round(percentile_cont(0.95) WITHIN GROUP (ORDER BY pg_column_size(payload)))::text, '0')
FROM s;
EOF
)

rows=$(psql_17 "$SQL")
if [ $? -ne 0 ]; then
  log "ABORT: payload 采样查询失败: ${rows//$'\n'/ }"
  echo "ABORT: 量具不可用 —— 本次**没有结论**。" >&2
  exit 3
fi
# 多行/空行都不能当健康：psql 输出意外换行时下游解析会取到空值并当成 0。
n=$(printf '%s' "$rows" | head -1 | awk -F'|' '{print $1+0}')
nfield=$(printf '%s' "$rows" | head -1 | awk -F'|' '{print NF}')
[ "$nfield" = "4" ] || { echo "ABORT: 期望 4 个字段，实得 $nfield" >&2; exit 3; }
[ "$n" -gt 0 ]    || { echo "ABORT: 样本为 0 行（表空？）—— 没有参照系，本次不出结论。" >&2; exit 3; }

newest_age=$(printf '%s' "$rows" | head -1 | awk -F'|' '{print $2+0}')
avg_b=$(printf '%s' "$rows"       | head -1 | awk -F'|' '{print $3+0}')
p95_b=$(printf '%s' "$rows"       | head -1 | awk -F'|' '{print $4+0}')

if [ "$newest_age" -gt "$STALE_SECONDS" ] 2>/dev/null; then
  echo "ABORT: 最新样本已陈旧 ${newest_age}s（> ${STALE_SECONDS}s）—— 快照写入可能已停，" >&2
  echo "      此刻报告的 payload 大小是历史值，**不能代表现状**。" >&2
  exit 3
fi
if [ "$SAMPLE" -gt 0 ] && [ "$n" -lt $((SAMPLE / 10)) ]; then
  echo "ABORT: 只取到 $n/$SAMPLE 行（不足 1/10）—— 样本可能不具代表性，本次不出结论。" >&2
  exit 3
fi

echo "===== URSM 快照 payload 膨胀巡检（最近 $n 行，最新样本 ${newest_age}s 前）====="
printf '  payload 平均   : %s B   (818 后基线 ~24 B)\n' "$avg_b"
printf '  payload p95    : %s B\n' "$p95_b"
printf '  阈值           : %s B\n' "$BLOAT_BYTES"

if [ "$p95_b" -le "$BLOAT_BYTES" ]; then
  echo "  判定           : OK（未检出膨胀）"
  exit 0
fi

# ── 检出膨胀：给出「到底哪些键在吃字节」的诊断 ──
# 按实际占用字节排序，而不是按预置名单 —— 加字段不会让这份诊断失效。
echo
echo "----- 诊断：样本内 payload 键的占用字节（前 10）-----"
DIAG_SQL=$(cat <<EOF
WITH s AS (
  SELECT payload FROM public.ursm_node_snapshot_min
  ORDER BY snapshot_ts DESC LIMIT $SAMPLE
), kv AS (
  SELECT k.key, pg_column_size(k.value) AS b
  FROM s, LATERAL jsonb_each(s.payload) AS k(key, value)
)
SELECT k.key || ' | ' || count(*) || ' | ' || round(avg(b))::int
       || ' | ' || pg_size_pretty(sum(b)::bigint)
FROM kv k GROUP BY k.key ORDER BY sum(b) DESC LIMIT 10;
EOF
)
diag=$(psql_17 "$DIAG_SQL") || diag=""
if [ -n "$diag" ]; then
  printf '  %-34s %8s %10s %12s\n' "key" "rows" "avg_B" "total"
  while IFS='|' read -r k cnt ab tot; do
    [ -n "$k" ] && printf '  %-34s %8s %10s %12s\n' "$k" "$cnt" "$ab" "$tot"
  done <<<"$diag"
else
  echo "  (诊断查询失败 —— 判定仍然成立，只是没有键级明细)"
fi

echo
echo "  判定           : 🔴 检出 payload 膨胀（p95 ${p95_b} B > ${BLOAT_BYTES} B）"
echo
echo "  下一步：核对两台实例的二进制是否都含 818 的 payload 剥离"
echo "        （writer.go payloadDuplicateKeys；实测 06:50:48 那次切换前是 81.7% 未剥离）。"
exit 1
