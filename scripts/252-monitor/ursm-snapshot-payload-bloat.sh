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

# ★★★ 2026-10-04 首次上机时改的。原来写的是
#     sudo -u postgres psql ... --file "$CONF"
#   注释自称「走 252 的既有形态」—— **这句是错的**，实测证否：
#   252 的 PG17 跑在容器 pg-252-pg17 里，宿主机上既没有可 sudo 的 postgres
#   角色，也不存在 /etc/llmgw/pg17.conf。仓库里唯一在 252 上被验证能跑通的
#   写法是 pg17-vacuum-bloat.sh 那套 `docker exec "$CONTAINER" psql`。
#
#   ★ 为什么这个错误一直没被发现：这两个脚本**从来没在 252 上真跑过**。
#     cron_registration_test.go 只断言「有对应 cron 行」，不执行脚本；
#     本地单测全部用注入的假 PSQL_CMD，绕开了默认分支。
#     ⇒ 「默认值从没被执行过」是一类特别隐蔽的缺陷：
#        门是绿的、测试是绿的、注释还言之凿凿，只有真机第一次跑才炸。
#     补门见 scripts/ursmcheck/monitor_script_exec_gate_test.go。
#
# 保留可注入：为了让「0 行 ⇒ exit 3」这类分支**真的被跑到**。
CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-postgres}
DBNAME=${PG17_DB:-llm_gateway}
PSQL_CMD=${PSQL_CMD:-docker exec -i "$CONTAINER" psql -U "$PG_USER" -X -q -A -t -F'|' -v ON_ERROR_STOP=1 -d "$DBNAME" -c}

psql_17() {
  local sql=$1
  # shellcheck disable=SC2086 # PSQL_CMD 是刻意按多词命令传入的
  $PSQL_CMD "$sql" 2>&1
}

# ---------------------------------------------------------------------------
# --footprint：稳态占用核验（设计稿 §12.6「待实测」那一条的可执行形态）
#
# 为什么要有这个模式：§12.6 算出「表内空闲约 5%，DROP 能回收约 0.4 GB」，
# 那是**算术推演**不是实测；同节写明了 10-11 胖行滚出后该用什么办法验，
# 但只留了一段散文。这里把它变成一条命令，避免"知道该验却没人验"。
#
# ★ 不注册进 cron：它是**一次性/周期性人工核验**，不是每小时告警。
#   每小时告警由默认模式承担。少一条 cron 就少一分漂移面
#   （见 etc.cron.d.pg17 头部的同步契约：漏一条 = 覆盖时删掉一条）。
#
# 三态同前：0 = 与解析期望一致 / 1 = 偏离超过容差 / 3 = 没有结论。
# 分区化前后口径不同（父表 pg_total_relation_size 返回近乎 0），
# 两种形态都算，取非零者。
EXPECT_BYTES_PER_ROW=${EXPECT_BYTES_PER_ROW:-248}   # 213 数据 + 31 header/bitmap + 对齐
TOLERANCE=${TOLERANCE:-0.20}                        # ±20%；超出即偏离

if [ "${1:-}" = "--footprint" ]; then
  FR_SQL=$(cat <<EOF
SELECT count(*)::text,
       -- ★ 分母用 pg_class.reltuples（VACUUM/ANALYZE 更新），**不是**
       --   pg_stat_user_tables.n_live_tup。后者是统计采集器的估算，可能为 0
       --   或滞后；拿它当分母、又用 greatest(...,1) 兜底，会把"统计没跟上"
       --   放大成"每行字节巨大"，然后被报成「偏高 ⇒ 有空闲可回收」——
       --   一个会误导人的结论。两者任一为 0 都判「没有结论」。
       coalesce(round((pg_relation_size('public.ursm_node_snapshot_min')::numeric
                      / greatest((SELECT c.reltuples::bigint FROM pg_class c
                                   WHERE c.oid='public.ursm_node_snapshot_min'::regclass), 1)))::text, '0'),
       coalesce((SELECT n_live_tup::text FROM pg_stat_user_tables
                  WHERE relname='ursm_node_snapshot_min'), '-1'),
       coalesce(pg_size_pretty(pg_relation_size('public.ursm_node_snapshot_min')), '?'),
       coalesce(pg_size_pretty(pg_indexes_size('public.ursm_node_snapshot_min')), '?'),
       coalesce((SELECT (c.relkind = 'p')::int::text
                   FROM pg_class c WHERE c.oid='public.ursm_node_snapshot_min'::regclass), '?'),
       coalesce((SELECT count(*)::text FROM pg_inherits
                   WHERE inhparent='public.ursm_node_snapshot_min'::regclass), '0')
FROM public.ursm_node_snapshot_min;
EOF
)
  fr=$(psql_17 "$FR_SQL")
  if [ $? -ne 0 ]; then
    echo "ABORT: 占用核验查询失败: ${fr//$'\n'/ }" >&2
    exit 3
  fi
  fr_n=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $1+0}')
  fr_bpr=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $2+0}')
  fr_heap=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $3}')
  fr_idx=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $4}')
  fr_ispart=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $5}')
  fr_parts=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $6}')
  fr_live=$(printf '%s' "$fr" | head -1 | awk -F'|' '{print $7+0}')
  [ "$fr_n" -gt 0 ] || { echo "ABORT: 表里 0 行 —— 没有参照系，本次不出结论。" >&2; exit 3; }
  # 行数估算不可用或两套统计严重不一致 ⇒ 分母不可信 ⇒ 判「没有结论」，
  # 绝不拿一个坏分母去报「偏高/偏低」——那会把量具故障说成被测对象的性质。
  if [ "$fr_bpr" -le 0 ] || [ "$fr_live" -le 0 ]; then
    # ★ 必须写 ${fr_live} 而不是 $fr_live：`$fr_live` 紧跟全角「）」时，
    #   bash 在 UTF-8 locale 下把括号的高位字节当成变量名合法字符，
    #   变量名变成 `fr_live）` ⇒ set -u 报未绑定变量 ⇒ **本分支整个不执行**，
    #   反而落到后面的偏离判定去报 exit 1（把「量具坏了」说成「行宽偏高」）。
    #   这条是 7 条 --footprint 测试里「分母不可信」那条首次跑出来的。
    echo "ABORT: 行数估算不可用（reltuples 推出的每行字节=${fr_bpr}, n_live_tup=${fr_live}）——" >&2
    echo "      分母不可信时每行字节是除零产物，本次不出结论。" >&2
    echo "      处理：ANALYZE public.ursm_node_snapshot_min; 后重跑。" >&2
    exit 3
  fi

  echo "===== URSM 快照稳态占用核验（设计稿 §12.6 待实测项）====="
  printf '  形态           : %s\n' "$([ "$fr_ispart" = 1 ] && echo "分区父表（$fr_parts 个分区，825 已落地）" || echo "普通表（825 未落地）")"
  printf '  活行数         : %s\n' "$fr_n"
  printf '  堆 / 索引      : %s / %s\n' "$fr_heap" "$fr_idx"
  printf '  活行数(统计)   : %s   (n_live_tup，分母可靠性已校验)\n' "$fr_live"
  printf '  堆每活行字节   : %s   (解析期望 %s ±%s%%)\n' "$fr_bpr" "$EXPECT_BYTES_PER_ROW" "$TOLERANCE"
  dev=$(awk -v a="$fr_bpr" -v e="$EXPECT_BYTES_PER_ROW" 'BEGIN{if(e>0){d=(a-e)/e; if(d<0)d=-d; printf "%.1f", d*100}}')
  echo
  if awk -v d="$dev" -v t="$(awk -v t="$TOLERANCE" 'BEGIN{printf "%d", t*100}')" 'BEGIN{exit !(d<=t)}'; then
    echo "  判定           : OK —— 与解析期望一致（偏离 ${dev}%）"
    echo "  解读           : 堆里没有可观的空闲，825 能回收的字节确实很少（§12.6 推论成立）"
    exit 0
  fi
  echo "  判定           : ⚠️ 偏离解析期望 ${dev}%（容差 ±$(awk -v t="$TOLERANCE" 'BEGIN{printf "%d", t*100}')%）"
  echo "  解读           : 偏离方向决定结论——"
  echo "                    偏高 ⇒ 堆里有可观空闲，825 的回收价值比 §12.6 估的大"
  echo "                    偏低 ⇒ 行宽比解析值更小（例如 payload 进一步瘦身），不是坏事"
  exit 1
fi

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
