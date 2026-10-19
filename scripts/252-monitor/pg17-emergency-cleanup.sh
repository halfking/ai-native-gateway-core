#!/bin/bash
# 252 PG17 紧急清理（disk > 90% 时跑 / 手动触发）
#
# 三级回退（按"危险度"递增，每级先 LOW 然后 HIGH）：
#   L1 (safe):     DROP n_live_tup = 0 AND size >= 100MB 的死表 / 默认分区
#   L2 (medium):   VACUUM FULL columnar_internal.{chunk,stripe,chunk_group}
#                  + VACUUM FULL 任何 dead_pct > 50% 的 1GB+ 表
#   L3 (warning):  TRUNCATE 所有 _hot 表（保留 *_default / 当前月 _2026_MM）
#                  TRUNCATE token / record / session 等清零表
#
# 触发方式:
#   1. cron: */15 * * * * /opt/scripts/pg17-emergency-cleanup.sh --auto  (自动按 disk% 触发)
#   2. 手动: /opt/scripts/pg17-emergency-cleanup.sh --level L1 [--yes]
#
# 创建：2026-07-15
set -euo pipefail

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

CONTAINER=${CONTAINER:-pg-252-pg17}
# 可注入：让本脚本的行为门能用一个假 `docker` 跑到真实判定分支，
# 而不是只能对源码做子串断言（那量的是「写过这句话」，不是「行为变了」）。
# 与 pg17-proactive-empty-table-cleanup.sh 同款。
DOCKER_BIN=${DOCKER_BIN:-docker}
PG_USER=${PG_USER:-llm_gateway}
PG_DB=${PG_DB:-llm_gateway}
LOG=${LOG:-/var/log/pg17-emergency-cleanup.log}

AUTO=false
LEVEL=L1
ASSUME_YES=false
DISK_TRIGGER_PCT=90

while [ $# -gt 0 ]; do
  case "$1" in
    --auto)       AUTO=true; shift;;
    --level)      LEVEL="${2^^}"; shift 2;;
    --yes|-y)     ASSUME_YES=true; shift;;
    *) echo "[error] unknown arg: $1" >&2; exit 2;;
  esac
done

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
echo "[$(date -Iseconds)] === emergency-cleanup invoked AUTO=$AUTO LEVEL=$LEVEL ===" >> "$LOG"

used_pct=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$(date -Iseconds)] current disk used=${used_pct}%" >> "$LOG"

if [ "$AUTO" = "true" ]; then
  if [ "$used_pct" -lt "$DISK_TRIGGER_PCT" ]; then
    echo "[$(date -Iseconds)] auto + disk < ${DISK_TRIGGER_PCT}%, skip" >> "$LOG"
    exit 0
  fi
  # 自动触发则升级到 L2
  LEVEL="L2"
  echo "[$(date -Iseconds)] auto-triggered, escalate to L2" >> "$LOG"
  # 先清文件系统层（/tmp 大文件、悬空镜像等），再跑 PG 操作。
  # 2026-09-11 复盘：磁盘 100%（0 可用）时 VACUUM FULL 因无临时空间必然失败，
  # 日志留下 "100% -> 100%" 的空转记录。
  if [ -x /opt/scripts/cleanup-known-junk.sh ]; then
    echo "[$(date -Iseconds)] running filesystem cleanup first to free VACUUM FULL workspace" >> "$LOG"
    /opt/scripts/cleanup-known-junk.sh --force-now >> "$LOG" 2>&1 || true
  fi
fi

if [ "$ASSUME_YES" = "false" ] && [ -t 0 ]; then
  read -p "L1/L2/L3  are progressive; ${LEVEL} 下执行。输入 yes 继续: " ans
  [ "$ans" = "yes" ] || { echo "[$(date -Iseconds)] abort by user" >> "$LOG"; exit 1; }
fi

docker_exec() { "$DOCKER_BIN" exec "$CONTAINER" "$@" 2>/dev/null; }

# === L1: DROP 死表 / 默认分区 ===
if [ "$LEVEL" = "L1" ] || [ "$LEVEL" = "L2" ] || [ "$LEVEL" = "L3" ]; then
  # 找 n_live_tup=0 AND pg_total >= 100MB 的表
  #
  # ★★ 2026-10-19 修复（§116）：本分支此前**三道闸全缺**，而它不是死代码 ——
  #   cron 每 15 分钟以 `--auto` 调一次，磁盘 >= DISK_TRIGGER_PCT(90) 时 L1 会真的执行 DROP。
  #   252 实测：磁盘 71%，日志每 15 分钟一行 "auto + disk < 90%, skip" ⇒ 引信还剩 19 个点。
  #
  #   ① 不排除分区：**直接 `DROP TABLE` 一个已 ATTACH 的分区会成功**
  #      （本地 PG 17.10 实测）——父表静默丢掉该范围的行，之后该范围 INSERT
  #      直接报 `no partition of relation found`。这是**写路径事故**，不是省空间。
  #   ② 不做 COUNT(*) 复核：`n_live_tup` 是**估计值**。PG 重启后未被 ANALYZE 的表
  #      会一直停在 0（§93/§94/§116 三次同族）。**252 逐张 COUNT(*) 实测（§137）
  #      本判据当前命中 6 张表，其中 5 张是误伤，合计 5,926,343 真实行**：
  #        ursm_node_snapshot_min_legacy  n_live_tup=0 | COUNT(*)=2,836,620  relpages=69,875
  #        usage_ledger_2026_09           n_live_tup=0 | COUNT(*)=1,416,772  relpages=33,393
  #        session_turn_details_2026_09   n_live_tup=0 | COUNT(*)=  786,036  relpages=48,926
  #        request_wal_2026_09            n_live_tup=0 | COUNT(*)=  446,777  relpages=11,496
  #        routing_decision_log_2026_09   n_live_tup=0 | COUNT(*)=  440,138  relpages= 2,289
  #        _request_logs_bodies_hot       n_live_tup=0 | COUNT(*)=        0  relpages=     0  ← 唯一真空
  #      ⇒ **判据准确率 1/6**，且唯一判对的那张是靠「真的被 TRUNCATE 过」（§129）
  #      而非「判据准」。⇒ 故第三道闸必须是 COUNT(*)，**不是**调 n_live_tup 阈值。
  #      （若真要收紧候选层，正确的第二把尺是 `relpages=0`，它是「物理空壳」信号。）
  #   ③ docker_exec 把 psql 的 stderr 吞进黑洞（§96 已在 L2 修过，这里还没修），
  #      DROP 失败在日志里一个字都看不到。
  #   ⇒ 三道闸与同族脚本（pg17-proactive-empty-table-cleanup.sh）以及
  #      **本文件自己的 _default 分支**（第 132 行起，早就写了「验证是否真的为 0 行」）
  #      完全对齐 —— 正确写法就在隔壁，此前只是没抄过来。
  DEAD_TABLES=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tA -F'|' -c "
    SELECT format('%I.%I', s.schemaname, s.relname), c.relispartition::int
    FROM pg_stat_user_tables s
    JOIN pg_class c ON c.oid = s.relid
    WHERE s.n_live_tup = 0 AND pg_total_relation_size(s.relid) >= 100*1024*1024
      AND s.schemaname NOT IN ('columnar_internal')
      AND NOT c.relispartition
    ORDER BY pg_total_relation_size(s.relid) DESC;
  ")
  while IFS='|' read -r tname is_part; do
    [ -z "$tname" ] && continue
    # 行为层的第二道闸：候选行带出的结构标记也要看。
    # 只做 SQL 过滤的话，将来有人改了上面那条 WHERE，本脚本就会重新开始裸 DROP 分区。
    if [ "$is_part" = "1" ]; then
      echo "[$(date -Iseconds)] L1 SKIP ${tname}: 是分区，分区生命周期不归本脚本（需先 DETACH）" >> "$LOG"
      continue
    fi
    # 第三道闸：n_live_tup 只是估计，DROP 前必须 COUNT(*) 复核。
    actual_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM $tname" 2>/dev/null || echo "-1")
    if [ "$actual_count" != "0" ]; then
      echo "[$(date -Iseconds)] L1 SKIP ${tname}: n_live_tup=0 但 COUNT(*)=${actual_count}（统计不可信，拒绝 DROP）" >> "$LOG"
      continue
    fi
    echo "[$(date -Iseconds)] L1 DROP ${tname} (verified 0 rows)" >> "$LOG"
    docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "DROP TABLE IF EXISTS $tname" >> "$LOG" 2>&1 || true
  done <<< "$DEAD_TABLES"

  # 找 _default 分区（需验证是否真的为空）
  DEFAULT_PARTS=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
    SELECT child.relname, parent.relname
    FROM pg_inherits i
    JOIN pg_class child  ON child.oid  = i.inhrelid
    JOIN pg_class parent ON parent.oid = i.inhparent
    WHERE child.relname LIKE '%_default'
      AND parent.relname IN ('_request_logs','usage_ledger','request_wal','tool_usage_stats','routing_decision_log','credit_ledger','credential_model_index');
  ")
  while IFS='|' read -r child parent; do
    [ -z "$child" ] && continue
    # 验证是否真的为 0 行（防止意外删除有数据的默认分区）
    row_count=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT COUNT(*) FROM $child" 2>/dev/null || echo "-1")
    if [ "$row_count" = "0" ]; then
      echo "[$(date -Iseconds)] L1 DROP default partition $child (verified 0 rows)" >> "$LOG"
      docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
        ALTER TABLE ${parent} DETACH PARTITION $child;
        DROP TABLE IF EXISTS $child;
      " >> "$LOG" 2>&1 || true
    else
      echo "[$(date -Iseconds)] L1 SKIP default partition $child: has $row_count rows (NOT EMPTY!)" >> "$LOG"
    fi
  done <<< "$DEFAULT_PARTS"
fi

# === L2: VACUUM FULL columnar 元数据 + 业务 bloat 表 ===
#
# ★★ 2026-10-18 修复（§96）：下面两处 VACUUM 与 §88 在
#   pg17-drop-old-columnar-partitions.sh 里发现的是**完全同一个缺陷**，
#   而这一份**更隐蔽**：
#     ① 语句串是 "SET …; SET …; VACUUM FULL …" ⇒ PostgreSQL 视作
#        **隐式事务块** ⇒ VACUUM 每次都报 "cannot run inside a transaction block"；
#     ② 用的是 `|| true`（比 `|| echo WARN` 吞得更彻底，**连失败都不留痕**）；
#     ③ `docker_exec()` 内部是 `docker exec … 2>/dev/null`
#        ⇒ psql 的 ERROR 走 docker 的 stderr ⇒ **直接进黑洞，日志里一个字都没有**。
#   ⇒ 结论：这条分支**从未成功执行过一次，且失败完全不可见**。
#   而它恰好在 **LEVEL 升到 L2（磁盘 ≥90% 自动升级，第 86 行）** 时才跑 ——
#   也就是最需要它的时刻。
#   修法与 drop 脚本同款：超时走 PGOPTIONS，语句串里只留 VACUUM；
#   并且**绕开 docker_exec 直接调 docker exec**，让错误能进日志。
if [ "$LEVEL" = "L2" ] || [ "$LEVEL" = "L3" ]; then
  vac_ok=0
  vac_fail=0
  for meta in chunk stripe chunk_group; do
    size=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_total_relation_size('columnar_internal.${meta}')/1024/1024" 2>/dev/null || echo 0)
    [ "${size:-0}" -lt 10 ] && { echo "[$(date -Iseconds)] L2 skip columnar_internal.${meta} (small=${size}MB)"; continue; }
    echo "[$(date -Iseconds)] L2 VACUUM FULL columnar_internal.${meta} (${size}MB)" >> "$LOG"
    if docker exec -e "PGOPTIONS=-c statement_timeout=30min -c lock_timeout=5min" \
         "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
         "VACUUM FULL columnar_internal.${meta}" >> "$LOG" 2>&1; then
      vac_ok=$((vac_ok+1))
    else
      echo "[$(date -Iseconds)]   WARN: VACUUM FULL columnar_internal.${meta} FAILED (see above)" >> "$LOG"
      vac_fail=$((vac_fail+1))
    fi
  done

  # bloat > 50% AND size > 1GB
  BLOATS=$(docker_exec psql -U "$PG_USER" -d "$PG_DB" -tAc "
    SELECT format('%I.%I', schemaname, relname)
    FROM pg_stat_user_tables
    WHERE schemaname='public'
      AND n_dead_tup > 0 AND n_live_tup > 0
      AND 100.0*n_dead_tup/(n_live_tup+n_dead_tup) > 50
      AND pg_total_relation_size(relid) > 1024*1024*1024
    ORDER BY pg_total_relation_size(relid) DESC;
  ")
  while IFS= read -r tname; do
    [ -z "$tname" ] && continue
    echo "[$(date -Iseconds)] L2 VACUUM FULL ${tname}" >> "$LOG"
    if docker exec -e "PGOPTIONS=-c statement_timeout=30min -c lock_timeout=5min" \
         "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
         "VACUUM FULL ${tname}" >> "$LOG" 2>&1; then
      vac_ok=$((vac_ok+1))
    else
      echo "[$(date -Iseconds)]   WARN: VACUUM FULL ${tname} FAILED (see above)" >> "$LOG"
      vac_fail=$((vac_fail+1))
    fi
  done <<< "$BLOATS"

  # ★ 只有全成功才打 ok；有任何一条失败就要让运维看见 —— 旧写法没有这一行。
  if [ "$vac_fail" -gt 0 ]; then
    echo "[$(date -Iseconds)] L2 VACUUM FULL **PARTIAL**: ok=${vac_ok} failed=${vac_fail}" >> "$LOG"
  else
    echo "[$(date -Iseconds)] L2 VACUUM FULL ok (${vac_ok} table(s))" >> "$LOG"
  fi
fi

# === L3: TRUNCATE hot 表 + 清零的小清零表 ===
if [ "$LEVEL" = "L3" ]; then
  echo "[$(date -Iseconds)] L3 DANGER ZONE - 需 --yes 确认" >> "$LOG"
  if [ "$ASSUME_YES" = "false" ] && [ "$AUTO" = "false" ]; then
    # L1/L2 已通过一次确认;L3 这里强制交互
    read -p "L3 will TRUNCATE hot tables (回收 ~数 GB). Continue? (yes): " ans
    [ "$ans" = "yes" ] || { echo "[$(date -Iseconds)] abort L3" >> "$LOG"; exit 1; }
  fi

  # 清零的小表（casdoor token/record/session 等）
  for db in casdoor kaixuan crm kxmemory; do
    ZERO_TABLES=$(docker_exec psql -U "$PG_USER" -d "$db" -tAc "
      SELECT format('%I.%I', schemaname, relname)
      FROM pg_stat_user_tables
      WHERE n_live_tup = 0 AND pg_total_relation_size(relid) >= 1024*1024;
    " 2>/dev/null || echo "")
    while IFS= read -r tname; do
      [ -z "$tname" ] && continue
      echo "[$(date -Iseconds)] L3 TRUNCATE $tname (in $db)" >> "$LOG"
      docker_exec psql -U "$PG_USER" -d "$db" -tAc "TRUNCATE TABLE $tname" >> "$LOG" 2>&1 || true
    done <<< "$ZERO_TABLES"
  done
fi

used_after=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$(date -Iseconds)] done. disk: ${used_pct}% -> ${used_after}%" >> "$LOG"
