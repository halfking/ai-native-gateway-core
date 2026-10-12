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
# 判据：**去掉索引名之后的定义本体**是否逐字相同。
# 不能比「索引名」「建表模板」——重复索引通常来自两条不同的迁移路径，
# 名字必然不同，比名字会漏掉全部。也不能只比列清单：`WHERE` 子句、
# 唯一性、排序方向不同就是不同的索引，重复维护不成立。
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
if [ -z "${LLMGW_NO_SINGLE_INSTANCE_LOCK:-}" ] \
   && command -v flock >/dev/null 2>&1 \
   && mkdir -p "$LLMGW_LOCK_DIR" 2>/dev/null \
   && exec 9>"$_llmgw_lock_file" 2>/dev/null; then
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

out=$(psql_17 "$SQL")
rc=$?
if [ $rc -ne 0 ]; then
  log "ABORT: psql 失败 rc=$rc —— 量具不可用，本次不出结论（不报健康）。"
  printf '%s\n' "$out" >&2
  exit 3
fi

# 字段数契约：table|idx_a|idx_b|bytes_a|bytes_b|sum。psql 出错但 rc=0 的形态要挡住。
n=0; nfield=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  n=$((n + 1))
  nfield=$(printf '%s' "$line" | awk -F'|' '{print NF}')
  [ "$nfield" = "10" ] || {
    log "ABORT: 期望 10 个字段，实得 ${nfield}（行：${line}）"
    exit 3
  }
done <<< "$out"

# ★ 这里 0 行是**健康**，不是「没有结论」。见文件头「语义差异」。
if [ "$n" -eq 0 ]; then
  log "OK: 未检出超过 ${MIN_PAIR_BYTES} 字节的重复索引（0 对是健康结论）。"
  exit 0
fi

printf '表\t索引A\t索引B\tA字节\tA+B字节\tA扫描\tB扫描\tA受约束\tB受约束\n'
log "检出 ${n} 对重复索引（删一个可回收 ≥ ${MIN_PAIR_BYTES} 字节的组合）。"
log "读法：受约束=1 的那个被 PK/UNIQUE 约束背着，不能直接删索引，"
log "      要先 ALTER TABLE … DROP CONSTRAINT；另一侧才是可裸删的那个。"
log "      扫描=idx_scan，窗口见 pg_stat_database.stats_reset；两者皆 0 才谈得上『无人用』。"
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
