#!/usr/bin/env bash
# pg17-duplicate-index-check.sh —— 重复索引巡检（2026-10-06，审计 §10.55）
#
# 为什么这道巡检存在：重复索引**同时**吃掉存储和写放大。
# 两者都占：索引本体占空间，而每次 INSERT/UPDATE/DELETE 都要多维护一份。
# 实测（154 直连生产，2026-10-06 13:1x）：全库 **19 张表**存在
# 「同一张表上定义完全相同的两个索引」，最贵的一对是
#
#   session_summaries.session_summaries_pkey
#   session_summaries.session_summaries_session_key_uidx
#   两者定义均为 btree (session_key)，单个 57 MB
#
# session_summaries 本体 914 MB 且**持续写入** ⇒ 每次写都要多维护一个 57 MB 索引。
# 另有 session_turns 族的 (ts, id) WHERE digest IS NULL 在 2026_09 / 2026_11 /
# default 三个分区上各重复一次（当前分区为空所以只有 8 KB，一旦分区填上就会真涨）。
#
# 判据分三段，各自抓一类「重复维护」（段2 于 2026-10-18 §78 新增，
# 段3 于同日 §79 新增）：
#
#   段1 逐字相同：去掉索引名之后的定义本体是否完全一致。
#   段2 前缀覆盖：较窄索引的每一列都是较宽索引的前缀，且它的 INCLUDE 列
#                  也都被覆盖 ⇒ 较窄的那份**在功能上被完全覆盖**。
#   段3 父表前缀覆盖：段2 的同一个判据，但跑在**分区父表**上，并按
#                  pg_inherits 聚合出跨分区的**级联实际占用**。
#
# ★ 段3 为什么不是段2 的重复，而是它唯一的**处置依据**：
#   段2 报的是「分区上有一个 168 MiB 的窄索引」，但那个读数有个致命
#   的附加条件 —— CREATE TABLE … PARTITION OF 会克隆父表声明的每个索引。
#   段2/段1 都写死了 relkind 'r'/'m' 与索引 'i'，**看不到父表**，
#   于是「删了会不会长回来」这个问题它答不出来。
#   §79 实测：只删分区实例是无效的，真实代价从一次性 345 MiB 变成
#   每月 ≈243 MiB ≈ 2.9 GB/年。段3 让工具自己答这个问题。
#
# 为什么第一段不够（§78 实测）：第一段对「两份索引在每一列上都一样」
# 才成立，而生产上最贵的一类冗余恰恰是**不等长**的。最贵的两对是
# usage_ledger_2026_09/2026_10 的 request_id_idx（单列 169 MB / 50 MB），
# 它们的每一个列都是 request_id_ts_provider_id_prompt_token_idx 的前缀，
# 而两段判据都看不见它们 —— 逐字比较当然不成立，它们连长度都不同。
#
# 为什么第二段必须比四项而不是只比列名（实测踩中两个假阳性）：
#   1. opclass：text_pattern_ops 与默认 opclass 是**两种索引**，前者服务
#      LIKE 'x%'，后者服务等号/小于/排序。生产上 request_logs_2026_10 的
#      client_model_idx 与 client_model_idx2 就是这一对，只比列名会误报冗余。
#   2. 排序方向：ts 与 ts DESC 的前缀关系不成立（范围扫描方向相反）。
#   3. 排序规则 indcollation。
#   4. INCLUDE 列必须也都被覆盖 —— btree 的 INCLUDE 列不进索引主体，
#      所以 INCLUDE 的列**不参与前缀判定**，得单独比一次。
#
# ★ 第一段的判据不能退化：比「索引名」「建表模板」都不行 —— 重复索引通常
#   来自两条不同的迁移路径，名字必然不同，比名字会漏掉全部。也不能只比列
#   清单：WHERE 子句、唯一性、排序方向不同就是不同的索引。
#
# ★ 两段是**结构上互斥**的，不是「可能重复」：段2 强制 a.indnkeyatts <
#   b.indnkeyatts，而键数相等且键/opclass/排序/规则都一致时，段1 必然命中。
#   所以并起来不会双报。
#
# 退出码（与同目录其余巡检同一套契约）：
#   0  没有重复索引
#   1  检出重复索引
#   3  **本次没有结论**（psql 不可用 / 输出不合契约）
#
# ★ 与 ursm-snapshot-payload-bloat.sh 的**语义差异**，不要照抄它的判据：
#   bloat 脚本里「0 行 ⇒ exit 3」，因为它需要参照系，0 行意味着**量具坏了**。
#   本脚本里「0 行 ⇒ exit 0」，因为 0 对重复索引**就是健康的结论**。
#   把两种语义混起来，会出现「查不到 ⇒ 报健康」这个已经坑过本项目的形态。
#
# ★ 这道巡检不 DROP 任何东西，只报告。删索引要先确认没有查询依赖它
#   （PG 没有「索引是否被用」的权威答案，见 §10.55.3）。

set -uo pipefail

# === 单实例闸门（2026-10-10 生产事故修复）================================
# 起因：2026-10-10 252 现场，pg17-disk-watch.sh 堆到 310 份副本、149 个
# `docker exec ... psql` 僵死，把 15G 内存吃到 swap 100% / load 300，
# 反而把 PG 压到无法服务 154 网关（SQLSTATE 57P03、一小时 194 个 503）。
# 根因是本目录**所有**巡检都缺同一道闸门：PG 半死（容器还在 listen、
# 事务卡死）时 psql 会一直挂着，而 cron 的下一次触发照常发生 ——
# 进程只增不减。⇒ 每 10 分钟一跑的巡检，在最需要它的时刻（PG 出问题）
# 恰恰是最不能多跑的时刻。
#
# ★ 为什么 inline 而不是 source 一个公共 helper：
#   本目录按**整文件覆盖**部署到 /opt/scripts/（见 etc.cron.d.pg17 头部
#   「漏一条 = 删一条」）。helper 没跟着传上去，source 失败会让**整个目录**
#   的巡检同时哑掉。本目录已经吃过一次这种耦合的亏，不再引入第二次。
#
# ★ 拿不到锁时**放行**（fail-open），不拒绝执行：
#   闸门不可用时让监控整体静默，比重复采样更糟 —— 静默的监控不会被任何人
#   发现已经坏了。下次采样行会记 gate=FAIL_OPEN，让 fail-open 本身可见。
#
# ★ 抢不到锁时退出 0 而不是 3：本轮确实没有新采样，但巡检**没有失效**
#   （上一轮正在产出结论）。3 在本目录契约里是「量具不可用」，报它会
#   触发一次假的「巡检失效」告警。
LLMGW_LOCK_DIR=${LLMGW_LOCK_DIR:-/var/run/lock}
_llmgw_lock_file="$LLMGW_LOCK_DIR/llmgw-$(basename "$0" .sh).lock"

_llmgw_single_instance_guarded=false
# ★ 打开锁文件的那一行**不得**把 stderr 一起重定向（§143.14）：
#   exec 不带命令时，重定向施加到**当前 shell** ⇒ "exec 9>file 2>/dev/null"
#   会让本脚本此后所有 stderr 输出永久消失 ⇒ log()（写 stderr）全部失声。
#   ⇒ 用 { exec 9>file; } 2>/dev/null：组的重定向只作用于组本身，组结束即恢复；
#     fd 9 的语义不变（exec 的重定向仍然持久）。
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && { exec 9>"$_llmgw_lock_file"; } 2>/dev/null; then
  if ! flock -n 9; then
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: $(basename "$0") 上一轮仍在运行（lock=${_llmgw_lock_file}），本轮不重复执行" \
      >> "${LLMGW_LOCK_SKIP_LOG:-/var/log/llmgw-single-instance-skip.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

# 判定阈值：**删掉其中一个能回收的字节数**下限。
# 8 KB 是空索引的地板值，绝大多数重复对都是刚建好、还空着的，
# 逐条告警只会把信号埋掉。默认 64 KB：能捞到 session_summaries(57MB)、
# runtime_metrics(1.2MB/440KB)、routing_audit_log(216KB) 这些真实占用。
# 想看全量（含空索引）设 MIN_PAIR_BYTES=0。
MIN_PAIR_BYTES=${MIN_PAIR_BYTES:-65536}

# 诊断输出最多列前几对（不参与判定）。
TOP_N=${TOP_N:-20}

CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-postgres}
DBNAME=${PG17_DB:-llm_gateway}
# 保留可注入：让「0 行 ⇒ exit 0」与「psql 失败 ⇒ exit 3」两条分支真的被跑到。
PSQL_CMD=${PSQL_CMD:-docker exec -i "$CONTAINER" psql -U "$PG_USER" -X -q -A -t -F'|' -v ON_ERROR_STOP=1 -d "$DBNAME" -c}

log() { printf '%s %s\n' "$(date -Is)" "$*" >&2; }

psql_17() {
  local sql=$1
  # shellcheck disable=SC2086 # PSQL_CMD 是刻意按多词命令传入的
  $PSQL_CMD "$sql" 2>&1
}

# ★ 阈值必须先校验成非负整数再内联进 SQL。
#   不要用 psql 变量 $1：psql -c **不绑定位置参数**（那是 \set / -v 的东西），
#   写 $1 会得到 `there is no parameter $1` —— 而这条默认路径在真机上第一次
#   跑之前，仓库里的门与本地假 psql 桩都是绿的（见 §10.55.3）。
case "$MIN_PAIR_BYTES" in
  ''|*[!0-9]*) log "ABORT: MIN_PAIR_BYTES 必须是非负整数，实得 '$MIN_PAIR_BYTES'"; exit 3 ;;
esac

SQL=$(cat <<'SQL'
WITH idx AS (
  SELECT i.indrelid,
         i.indexrelid,
         ic.relname AS index_name,
         COALESCE(s.idx_scan, 0) AS idx_scan,
         -- 「被约束引用」= 该索引是否被 pg_constraint 背着（PK/UNIQUE 约束）。
         -- ★ 这是能不能直接 DROP INDEX 的决定项：约束背着的那个，
         --   DROP INDEX 会被 PG 拒绝，必须先 ALTER TABLE ... DROP CONSTRAINT。
         (SELECT count(*) FROM pg_constraint c WHERE c.conindid = i.indexrelid) AS constrained,
         -- 只保留定义本体：剥掉 'CREATE [UNIQUE] INDEX <名字> ON ' 前缀。
         -- 名字必然不同（重复索引通常来自两条不同迁移路径），比名字等于漏掉全部。
         regexp_replace(pg_get_indexdef(i.indexrelid),
           '^CREATE (UNIQUE )?INDEX [^ ]+ ON ', '') AS def_body,
         pg_relation_size(i.indexrelid) AS bytes
  FROM pg_index i
  JOIN pg_class c  ON c.oid = i.indrelid
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
  LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
  WHERE c.relkind IN ('r','m') AND ic.relkind = 'i'
)
SELECT c.relname,
       a.index_name,
       b.index_name,
       a.bytes,
       b.bytes,
       (a.bytes + b.bytes)::text,
       a.idx_scan::text,
       b.idx_scan::text,
       a.constrained::text,
       b.constrained::text
FROM idx a
JOIN idx b
  ON a.indrelid = b.indrelid
 AND a.indexrelid < b.indexrelid
 AND a.def_body = b.def_body
JOIN pg_class c ON c.oid = a.indrelid
WHERE (a.bytes + b.bytes) >= __MIN_PAIR_BYTES__::bigint
ORDER BY (a.bytes + b.bytes) DESC, c.relname, a.index_name;
SQL
)
SQL=${SQL/__MIN_PAIR_BYTES__/$MIN_PAIR_BYTES}

# ---------------------------------------------------------------------------
# 段2：前缀覆盖（2026-10-18 §78）
#
# ★★ 这一段的判据全部来自 pg_index 的向量列，而这四列是本仓最容易写错的
#    量具（实测 0 命中排查了四轮才定位，记在这里免得下一个人重踩）：
#
#    pg_index.indkey / indoption / indclass / indcollation 的类型是
#    int2vector / oidvector，**下界是 0**（不是普通数组的 1）。
#    而 indkey::int2[] 只换元素类型、**不换下界** —— 实测：
#
#      indkey::int2[]               => [0:1]={1,2}
#      (indkey::int2[])[1:1]        => {2}       ★ 取到第 2 个，不是第 1 个
#      (ARRAY[1,2]::int2[])[1:1]    => {1}       ★ 字面量是对的，只有走
#                                                列引用才会偏一位
#
#    后果不是「取不到数据」，而是**静默取到错误的列**：前缀比较恒为假 ⇒
#    段2 永远 0 行，看起来像「没有冗余」。而 0 行在本脚本是**健康结论**，
#    于是量具坏了反而报健康 —— 正是文件头警告的那个形态。
#    连带第二个后果：INCLUDE 列的下标区间也少截了最后一个列
#    （实测 5 个 INCLUDE 只剩 4 个），会让「a 的 INCLUDE 被 b 覆盖」误判。
#
#    修法：string_to_array(array_to_string(...)) 绕一圈重新定基，
#    新数组的下界回到 1。这是本脚本里唯一可用的写法。
# ---------------------------------------------------------------------------
SQL_PREFIX=$(cat <<'SQL'
-- seg: prefix-coverage
WITH idx AS (
  SELECT i.indrelid,
         i.indexrelid,
         ic.relname AS index_name,
         COALESCE(s.idx_scan, 0) AS idx_scan,
         (SELECT count(*) FROM pg_constraint c WHERE c.conindid = i.indexrelid) AS constrained,
         i.indisunique,
         i.indnkeyatts,
         i.indnatts,
         -- ★ 下面四个都是「重新定基」写法，原因见上方关于 oidvector 下界的说明。
         string_to_array(array_to_string(i.indkey::int2[], ','), ',')::int2[] AS keys,
         string_to_array(array_to_string(i.indoption::int2[], ','), ',')::int2[] AS opts,
         string_to_array(array_to_string(i.indclass::oid[], ','), ',')::oid[] AS opl,
         string_to_array(array_to_string(i.indcollation::oid[], ','), ',')::oid[] AS colls,
         -- INCLUDE 列（indnatts > indnkeyatts 的尾巴）。同样必须先定基。
         (string_to_array(array_to_string(i.indkey::int2[], ','), ',')::int2[])
           [i.indnkeyatts+1:i.indnatts] AS incs,
         (i.indpred IS NOT NULL) AS partial,
         (i.indexprs IS NOT NULL) AS has_expr,
         am.amname,
         pg_relation_size(i.indexrelid) AS bytes
  FROM pg_index i
  JOIN pg_class c  ON c.oid = i.indrelid
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_am am ON am.oid = ic.relam
  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
  LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
  WHERE c.relkind IN ('r','m') AND ic.relkind = 'i'
)
SELECT c.relname,
       a.index_name,
       b.index_name,
       a.bytes,
       b.bytes,
       (a.bytes + b.bytes)::text,
       a.idx_scan::text,
       b.idx_scan::text,
       a.constrained::text,
       b.constrained::text
FROM idx a
JOIN idx b
  ON a.indrelid = b.indrelid
 AND a.indexrelid <> b.indexrelid
JOIN pg_class c ON c.oid = a.indrelid
WHERE a.amname = 'btree' AND b.amname = 'btree'
  -- 部分索引与表达式索引都不是「前缀覆盖」这个概念：它们服务不同查询。
  AND NOT a.partial AND NOT b.partial
  AND NOT a.has_expr AND NOT b.has_expr
  -- ★ 严格小于：键数相等时若键/opclass/排序/规则都一致，段1 必然命中，
  --   写成 <= 会让两段双报同一对（实测双报 4 对）。
  AND a.indnkeyatts < b.indnkeyatts
  -- 前缀四项全等：列、排序方向、opclass、排序规则。少比任何一项都会造假阳性。
  AND (b.keys)[1:a.indnkeyatts] = a.keys
  AND (b.opts)[1:a.indnkeyatts] = a.opts
  AND (b.opl)[1:a.indnkeyatts] = a.opl
  AND (b.colls)[1:a.indnkeyatts] = a.colls
  -- a 自己的 INCLUDE 列也必须都在 b 里（键或 INCLUDE 都算）。
  AND (a.keys || COALESCE(a.incs, '{}'::int2[]))
      <@ (b.keys || COALESCE(b.incs, '{}'::int2[]))
  -- a 唯一而 b 不唯一 ⇒ b 不能替 a 兜住唯一性，删 a 会丢约束语义。
  AND NOT (a.indisunique AND NOT b.indisunique)
  -- a 被约束背着、b 没有 ⇒ 该删的是 b 不是 a，方向反了就没意义，剔掉。
  AND NOT (a.constrained > 0 AND b.constrained = 0)
  AND (a.bytes + b.bytes) >= __MIN_PAIR_BYTES__::bigint
ORDER BY (a.bytes + b.bytes) DESC, c.relname, a.index_name;
SQL
)
SQL_PREFIX=${SQL_PREFIX/__MIN_PAIR_BYTES__/$MIN_PAIR_BYTES}

# ---------------------------------------------------------------------------
# 段3：分区父表的前缀覆盖（2026-10-18 §79）
#
# ★★ 段1/段2 都写死了 `c.relkind IN ('r','m') AND ic.relkind = 'i'`，
#    所以它们**永远看不到分区父表的索引集**。而那恰恰是决定处置方式的
#    那个问题：「删掉会不会下个月又长回来？」
#
#    机制：CREATE TABLE … PARTITION OF 会**克隆父表上声明的每一个索引**
#    （见 sql/objects/functions/ensure_usage_ledger_partition_*.sql）。
#    实测：只删分区实例是无效的 —— 父表还声明着，下个月建分区时原样长回来。
#    真实代价因此不是「一次性 345 MiB」而是「每月 ≈243 MiB ≈ 2.9 GB/年」。
#
# ★ relkind 的大小写：分区索引的 pg_class.relkind 是 **'I'（大写）**，
#   普通索引是 'i'。写错大小写 ⇒ 本段恒 0 行，而 0 行在本脚本是**健康结论**
#   ⇒ 量具坏了反而报健康。（同一个坑还有第二个成因：下界，见段2 注释。）
#
# ★ 本段报的「字节」不是父表索引的大小（父表索引恒为 0 字节，那是
#   分区父表的正常渲染），而是**跨分区级联的实际占用** ——
#   用 pg_inherits 聚合。索引在 pg_inherits 里也有继承关系
#   （inhparent = 父索引 oid、inhrelid = 分区索引 oid）。
#   不这么做的话本段只能报「有个 0 字节的索引冗余」，没有处置价值。
#   idx_scan 同样聚合成跨分区总和 —— 父表上的 idx_scan 恒为空。
# ---------------------------------------------------------------------------
SQL_PARENT=$(cat <<'SQL'
-- seg: parent-prefix-coverage
WITH idx AS (
  SELECT i.indrelid,
         i.indexrelid,
         ic.relname AS index_name,
         COALESCE((SELECT sum(COALESCE(cs.idx_scan, 0))
                     FROM pg_inherits ch
                     JOIN pg_stat_user_indexes cs ON cs.indexrelid = ch.inhrelid
                    WHERE ch.inhparent = i.indexrelid), 0)::bigint AS idx_scan,
         (SELECT count(*) FROM pg_constraint c WHERE c.conindid = i.indexrelid) AS constrained,
         i.indisunique,
         i.indnkeyatts,
         i.indnatts,
         string_to_array(array_to_string(i.indkey::int2[], ','), ',')::int2[] AS keys,
         string_to_array(array_to_string(i.indoption::int2[], ','), ',')::int2[] AS opts,
         string_to_array(array_to_string(i.indclass::oid[], ','), ',')::oid[] AS opl,
         string_to_array(array_to_string(i.indcollation::oid[], ','), ',')::oid[] AS colls,
         (string_to_array(array_to_string(i.indkey::int2[], ','), ',')::int2[])
           [i.indnkeyatts+1:i.indnatts] AS incs,
         (i.indpred IS NOT NULL) AS partial,
         (i.indexprs IS NOT NULL) AS has_expr,
         am.amname,
         -- ★ 跨分区级联的实际占用，不是父表索引的 0 字节。
         COALESCE((SELECT sum(pg_relation_size(ch.inhrelid::regclass::oid))
                     FROM pg_inherits ch
                    WHERE ch.inhparent = i.indexrelid), 0)::bigint AS bytes
  FROM pg_index i
  JOIN pg_class c  ON c.oid = i.indrelid
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_am am ON am.oid = ic.relam
  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
  WHERE c.relkind = 'p' AND ic.relkind = 'I'
)
SELECT c.relname,
       a.index_name,
       b.index_name,
       a.bytes,
       b.bytes,
       (a.bytes + b.bytes)::text,
       a.idx_scan::text,
       b.idx_scan::text,
       a.constrained::text,
       b.constrained::text
FROM idx a
JOIN idx b
  ON a.indrelid = b.indrelid
 AND a.indexrelid <> b.indexrelid
JOIN pg_class c ON c.oid = a.indrelid
WHERE a.amname = 'btree' AND b.amname = 'btree'
  AND NOT a.partial AND NOT b.partial
  AND NOT a.has_expr AND NOT b.has_expr
  AND a.indnkeyatts < b.indnkeyatts
  AND (b.keys)[1:a.indnkeyatts] = a.keys
  AND (b.opts)[1:a.indnkeyatts] = a.opts
  AND (b.opl)[1:a.indnkeyatts] = a.opl
  AND (b.colls)[1:a.indnkeyatts] = a.colls
  AND (a.keys || COALESCE(a.incs, '{}'::int2[]))
      <@ (b.keys || COALESCE(b.incs, '{}'::int2[]))
  AND NOT (a.indisunique AND NOT b.indisunique)
  AND NOT (a.constrained > 0 AND b.constrained = 0)
  AND (a.bytes + b.bytes) >= __MIN_PAIR_BYTES__::bigint
ORDER BY (a.bytes + b.bytes) DESC, c.relname, a.index_name;
SQL
)
SQL_PARENT=${SQL_PARENT/__MIN_PAIR_BYTES__/$MIN_PAIR_BYTES}

# 字段数契约：table|idx_a|idx_b|bytes_a|bytes_b|sum|scan_a|scan_b|con_a|con_b（10 个）。
# psql 出错但 rc=0 的形态要挡住；两段都要过这道校验 —— 段2 崩了而没人拦，
# 就会把「段2 量具不可用」写成「段2 没查到」。
check_contract() {
  local label=$1 rows=$2 line nfield
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    nfield=$(printf '%s' "$line" | awk -F'|' '{print NF}')
    [ "$nfield" = "10" ] || {
      log "ABORT: [${label}] 期望 10 个字段，实得 ${nfield}（行：${line}）"
      return 3
    }
  done <<< "$rows"
  return 0
}

# 非空行数。段1/段2 各自要报「第几段命中几对」，所以不能只看合计。
count_rows() {
  local rows=$1 line n=0
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    n=$((n + 1))
  done <<< "$rows"
  printf '%s' "$n"
}

run_segment() {
  local label=$1 sql=$2 out rc
  out=$(psql_17 "$sql")
  rc=$?
  if [ $rc -ne 0 ]; then
    log "ABORT: [${label}] psql 失败 rc=$rc —— 量具不可用，本次不出结论（不报健康）。"
    printf '%s\n' "$out" >&2
    return 3
  fi
  # 失败只能以 return 3 传出去，调用点必须 `|| exit 3` 接住。
  # ★ 这里曾写过一个错误的理由（说 exit 只会结束子 shell、return 才传得出去）——
  #   实测两者在 $(...) 里**完全等价**：命令替换的退出码就是子 shell 的退出码，
  #   3 一路传回父脚本。真正的风险是**调用点漏掉 `|| exit 3`**：
  #   那时无论 return 还是 exit，父脚本都会拿到一段正常输出 + 一个没人看的 3，
  #   于是「段2 量具不可用」被吞成「段2 没查到」。
  check_contract "$label" "$out" || return 3
  printf '%s' "$out"
}

out1=$(run_segment "段1/逐字相同" "$SQL") || exit 3
out2=$(run_segment "段2/前缀覆盖" "$SQL_PREFIX") || exit 3
out3=$(run_segment "段3/父表前缀覆盖" "$SQL_PARENT") || exit 3

n1=$(count_rows "$out1")
n2=$(count_rows "$out2")
n3=$(count_rows "$out3")
n=$((n1 + n2 + n3))

# 合并后按「A+B 字节」降序排。段1 已在 SQL 侧排过序，这里保持稳定一致。
out=$(printf '%s\n%s\n%s\n' "$out1" "$out2" "$out3" | grep . | sort -t'|' -k6,6nr)

# ★ 这里 0 行是**健康**，不是「没有结论」。见文件头「语义差异」。
if [ "$n" -eq 0 ]; then
  log "OK: 未检出超过 ${MIN_PAIR_BYTES} 字节的重复/被覆盖索引（0 对是健康结论）。"
  exit 0
fi

printf '表\t索引A\t索引B\tA字节\tA+B字节\tA扫描\tB扫描\tA受约束\tB受约束\n'
log "检出 ${n} 对（段1/逐字相同 ${n1} 对 + 段2/前缀覆盖 ${n2} 对 + 段3/父表前缀覆盖 ${n3} 对）。"
log "读法：索引A=较窄的那一份，索引B=覆盖它的那一份 ⇒ 删 A 的功能由 B 兜住。"
log "      段1 里 A/B 对等，两边定义逐字相同，删任一个都行。"
log "      ★ 段3 的表名是**分区父表**，字节是**跨分区级联占用**。"
log "        段3 命中 ⇒ 删 A 时必须在**父表**上删：CREATE TABLE … PARTITION OF"
log "        会克隆父表声明的每个索引，只删分区实例的话下个月原样长回来。"
log "        而在父表上删索引会级联到所有分区（PG 不支持父表 CONCURRENTLY）。"
log "      受约束=1 的那个被 PK/UNIQUE 约束背着，不能直接删索引，"
log "      要先 ALTER TABLE … DROP CONSTRAINT；另一侧才是可裸删的那个。"
log "      扫描=idx_scan，窗口见 pg_stat_database.stats_reset；两者皆 0 才谈得上「无人用」。"
log "      ★「被覆盖」不等于「该删」：planner 按字节偏好更小的索引，"
log "        删掉窄索引会让点查改走更大的宽索引，代价要自己判断。"
log "      ★★「功能覆盖」≠「成本等价」（§84）：前缀索引在**查询能力**上被覆盖，"
log "        但它**物理上更窄**，只按前缀做范围扫描时更便宜 ——"
log "        实测某健康巡检的主判据就是这种情况：窄 4–5× 的 PK 前缀索引让扫描"
log "        少读约 4/5 的索引字节。**窄侧明显窄于宽侧时，它恰恰不该被删**。"
log "        判据：把 A字节 与 B字节 比一比，A < B/2 时先别删。"
i=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  i=$((i + 1))
  [ $i -gt "$TOP_N" ] && { log "… 其余 $((n - TOP_N)) 对省略（TOP_N=${TOP_N}）"; break; }
  t=$(printf '%s' "$line" | cut -d'|' -f1)
  a=$(printf '%s' "$line" | cut -d'|' -f2)
  b=$(printf '%s' "$line" | cut -d'|' -f3)
  ba=$(printf '%s' "$line" | cut -d'|' -f4)
  sum_b=$(printf '%s' "$line" | cut -d'|' -f6)
  sa=$(printf '%s' "$line" | cut -d'|' -f7)
  sb=$(printf '%s' "$line" | cut -d'|' -f8)
  ca=$(printf '%s' "$line" | cut -d'|' -f9)
  cb=$(printf '%s' "$line" | cut -d'|' -f10)
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$t" "$a" "$b" "$ba" "$sum_b" "$sa" "$sb" "$ca" "$cb"
done <<< "$out"

log "本脚本只报告，不 DROP。删索引前须自行确认无查询依赖（见 §10.55.3）。"
exit 1
