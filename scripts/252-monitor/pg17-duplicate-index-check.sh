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
         -- 只保留定义本体：剥掉 'CREATE [UNIQUE] INDEX <名字> ON ' 前缀。
         -- 名字必然不同（重复索引通常来自两条不同迁移路径），比名字等于漏掉全部。
         regexp_replace(pg_get_indexdef(i.indexrelid),
           '^CREATE (UNIQUE )?INDEX [^ ]+ ON ', '') AS def_body,
         pg_relation_size(i.indexrelid) AS bytes
  FROM pg_index i
  JOIN pg_class c  ON c.oid = i.indrelid
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
  WHERE c.relkind IN ('r','m') AND ic.relkind = 'i'
)
SELECT c.relname,
       a.index_name,
       b.index_name,
       a.bytes,
       b.bytes,
       (a.bytes + b.bytes)::text
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
  [ "$nfield" = "6" ] || {
    log "ABORT: 期望 6 个字段，实得 ${nfield}（行：${line}）"
    exit 3
  }
done <<< "$out"

# ★ 这里 0 行是**健康**，不是「没有结论」。见文件头「语义差异」。
if [ "$n" -eq 0 ]; then
  log "OK: 未检出超过 ${MIN_PAIR_BYTES} 字节的重复索引（0 对是健康结论）。"
  exit 0
fi

log "检出 $n 对重复索引（删一个可回收 ≥ ${MIN_PAIR_BYTES} 字节的组合）。"
i=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  i=$((i + 1))
  [ $i -gt "$TOP_N" ] && { log "… 其余 $((n - TOP_N)) 对省略（TOP_N=${TOP_N}）"; break; }
  t=$(printf '%s' "$line" | cut -d'|' -f1)
  a=$(printf '%s' "$line" | cut -d'|' -f2)
  b=$(printf '%s' "$line" | cut -d'|' -f3)
  ba=$(printf '%s' "$line" | cut -d'|' -f4)
  printf '%s\t%s\t%s\t%s\t%s\n' "$t" "$a" "$b" "$ba" "$(printf '%s' "$line" | cut -d'|' -f6)"
done <<< "$out"

log "本脚本只报告，不 DROP。删索引前须自行确认无查询依赖（见 §10.55.3）。"
exit 1
