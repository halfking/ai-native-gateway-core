#!/usr/bin/env bash
# pg-table-bloat-check.sh —— 全库表空洞巡检（2026-10-04）
#
# 存在的原因：2026-10-04 一次手工普查发现**35 张表合计 2,556 MB 的空闲空间**
# 永远不会还给操作系统（PostgreSQL 的 VACUUM 只把空间记进 FSM 供将来复用，
# 不会 shrink 文件；只有 VACUUM FULL / pg_repack / DROP PARTITION 才会归还）。
# 其中三张最刺眼：
#   analysis_events        236 MB  85.8% 空闲（净积压 5.7 万「已处理」僵尸行）
#   assets                  74 MB  98.9% 空闲（1.73 GB/天 的无变化行重写冲刷所致）
#   session_mirror_outbox  70 MB  98.5% 空闲（队列 churn，但文件永不收缩）
# 另有一整片 `*_hot` 表空闲率 30~75%，说明这不是个别现象而是**形态问题**。
#
# ★ 为什么用 pgstattuple_approx 而不是 pgstattuple：
#   pgstattuple 要全表精确扫描。ursm_node_snapshot_min 有 10 GB —— 精确扫它
#   会和线上 IO 抢资源（design §12.6 已定过这条纪律）。approx 走采样，
#   秒级完成，代价是行数有 ±几个百分点的误差。
#   ★ 对「空闲率」这个判据来说，采样误差完全够用：
#     要判断的是「这张表空洞是不是大到值得动手」，不是「精确到 KB」。
#   ⇒ 用一个会抢生产的测量换精确度，是本末倒置。
#
# 退出码（与本目录其他巡检同一套契约）：
#   0  无表超过阈值
#   1  检出显著空洞
#   3  本次没有结论（量具不可用：扩展缺失 / 连不上 / 没读到表）
#   ★ 3 与 0 必须可区分：「查不到」不是「没问题」。

set -uo pipefail

# 空闲率超过此值算异常（%）。20% 是个保守线：低于它，即便 VACUUM FULL 也
# 收不回多少字节，不值得停机。
BLOAT_PCT=${BLOAT_PCT:-20}
# 只看总占用超过此值的表（MB）。避免对几十 KB 的表报警，那不叫问题叫噪音。
MIN_SIZE_MB=${MIN_SIZE_MB:-5}
# 低于此值的空闲量不报（MB）。一张 2 MB 的表空闲 90% 也没必要停机。
MIN_FREE_MB=${MIN_FREE_MB:-2}

PSQL_CMD=${PSQL_CMD:-sudo -u postgres psql -X -q -A -t -F'|' -v ON_ERROR_STOP=1 --file "$CONF" -c}
# ★ -t（tuples only）不能省：没有它，psql 会输出表头行和末尾的 `(N rows)`，
#   而下面那个 while 循环按 4 段管道解析 —— 表头会被当成一条空洞记录读进去。
#   （本目录 ursm-snapshot-payload-bloat.sh 的 PSQL_CMD 里也有 -t，
#     抄漏的代价就是「一行都解析不出来 ⇒ 报量具不可用」，
#     而那句话会被读成「巡检说没问题」的反面 —— 幸好它 exit 3 不是 exit 0。）
CONF=${LLMGW_PG17_CONF:-/etc/llmgw/pg17.conf}
DBNAME=${PG17_DB:-llm_gateway}

log() { printf '%s %s\n' "$(date -Is)" "$*" >&2; }

SQL=$(cat <<EOF
SELECT c.relname,
       pg_total_relation_size(c.oid),
       s.approx_free_space,
       s.approx_free_percent
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_am am ON am.oid = c.relam AND am.amname = 'heap'
CROSS JOIN LATERAL pgstattuple_approx(c.oid::regclass) s
WHERE n.nspname = 'public'
  AND c.relkind = 'r'
  AND pg_total_relation_size(c.oid) > $(( MIN_SIZE_MB * 1024 * 1024 ))
  AND s.approx_free_space > $(( MIN_FREE_MB * 1024 * 1024 ))
  AND s.approx_free_percent > $BLOAT_PCT
ORDER BY s.approx_free_space DESC;
EOF
)

OUT=$($PSQL_CMD "$SQL" 2>&1)
rc=$?

# 量具自身的三种失效，都要报「没有结论」而不是「健康」：
#   · 扩展没装（pgstattuple_approx 不存在）
#   · 连不上库
#   · 一行都没读出来（权限 / schema 不写对 / 名字写错）
#
# ⚠️ R44 修正：原实现只用「嗅输出」判失效，`rc` 抓了却从不使用，于是
# 「查询成功且**结果为空**」与「查询失败」被压成同一个 exit 3。
# 而 SQL 自带三道阈值过滤（大小/空闲量/空闲率），库里没有超阈表时输出
# **本就应该为空** ⇒ 空洞治理完成之后，这个巡检每天 05:07 都会打
# 「ABORT: 量具不可用」，把「一切正常」报成「量具坏了」。
# 换句话说：这个脚本自己破坏了自己要区分的 0/3 语义。
#
# 判据改为**先看退出码、再看输出形状**：
#   rc≠0 或输出里有 psql 报错特征 ⇒ 量具失效（exit 3）
#   rc=0 且输出解析不出记录      ⇒ 分两种：全空 = 没有超阈表（exit 0）；
#                                  有内容但一行都解析不出 = 格式漂移（exit 3）
if [ "$rc" -ne 0 ] || printf '%s' "$OUT" | grep -qiE 'pg_stat_statements|pgstattuple|does not exist|connection|refused|permission denied'; then
  log "ABORT: 空洞巡检查询失败(rc=$rc): ${OUT//$'\n'/ }"
  exit 3
fi

LINES=$(printf '%s' "$OUT" | grep -cE '^[^|]+\|[0-9]+\|[0-9]+\|[0-9.]+$' || true)
# 「成功但零行」= 没有表超过阈值 = 巡检通过，这是本脚本最常见的正常结局。
# 只有「有输出却一行都解析不出」才是格式漂移（量具失效）。
if [ "$LINES" -eq 0 ]; then
  if [ -z "$(printf '%s' "$OUT" | tr -d '[:space:]')" ]; then
    log "OK: 没有表空闲率 > ${BLOAT_PCT}%（阈值内，量具正常）"
    exit 0
  fi
  log "ABORT: 查询成功但一行结果都解析不出来（量具不可用）: ${OUT//$'\n'/ }"
  exit 3
fi

printf '总大小 | 空闲 | 空闲率 | 表\n'
TOTAL=0
while IFS='|' read -r name total free pct; do
  [ -n "${name:-}" ] || continue
  case "$total$free$pct" in *[!0-9.]*|'') continue ;; esac   # 跳过表头/杂行
  hs=$(numfmt --to=iec --suffix=B "$total" 2>/dev/null || echo "$total")
  hf=$(numfmt --to=iec --suffix=B "$free" 2>/dev/null || echo "$free")
  # ★ read 的变量顺序必须与 SQL 的 SELECT 顺序逐字对应：
  #     SELECT relname, pg_total_relation_size, approx_free_space, approx_free_percent
  #   写反了不会报错，只会让每列显示成另一列的值 —— 而那恰恰**最难**被发现，
  #   因为「有输出」看起来像「跑通了」。第一版就犯了这个错。
  printf '%-10s | %-10s | %6s%% | %s\n' "$hs" "$hf" "$pct" "$name"
  TOTAL=$(( TOTAL + free ))
done <<< "$OUT"

echo
echo "检出 $LINES 张表空闲率 > ${BLOAT_PCT}%，合计可回收 $(numfmt --to=iec --suffix=B "$TOTAL" 2>/dev/null || echo "$TOTAL")"
echo
cat <<'NOTE'
解读（不要跳过这一步就去做 VACUUM FULL）：
  · 空闲 ≠ 可无损回收。VACUUM FULL / pg_repack 会**独占锁表重写**，
    期间写入全阻塞；务必在窗口期做，且先确认这张表不是热写入路径。
  · 若某张表空闲率长期 >80% 且还在持续写入，先查**写入模式**
    （像 assets 那样「无变化的全表改写」），只 VACUUM FULL 是治标，
    不修的话几周内会长回来。本目录 ursm-snapshot-payload-bloat.sh 就是
    守住其中一例的门。
  · 空闲率是 pgstattuple_approx 的**采样估计**，判断量级够用，
    要精确数字再单独对目标表跑 pgstattuple（记得它会全表扫）。
NOTE

# 走到这里必有 LINES>0（零行已在上面分流：全空→exit 0 / 不可解析→exit 3）。
# 所以这里**只**表达「检出空洞」，不再需要兜底的 `-eq 0 && exit 0`
# ——R44 删掉了它：它被上面的 exit 3 抢先，是一段永不可达的死代码。
exit 1
