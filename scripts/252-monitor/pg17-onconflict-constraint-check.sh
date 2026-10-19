#!/usr/bin/env bash
# pg17-onconflict-constraint-check.sh —— ON CONFLICT 推断列 vs 已部署唯一约束（2026-10-06，审计 §10.59）
#
# 为什么这道巡检必须存在：
#   domains/ursm/v2/persist/writer.go 的 INSERT 用
#     ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING
#   PG 要求存在一个**恰好**由这些列构成的唯一索引，否则每次插入抛
#     SQLSTATE 42P10  there is no unique or exclusion constraint matching
#                   the ON CONFLICT specification
#   线上曾连续 62 小时每分钟失败一次（701 条），而日志只记 WARN，不告警，
#   表 0 行 —— 一次 100% 写入失败的静默数据丢失。
#
#   仓库里已有的是「基线 ↔ writer」的门
#   （domains/ursm/v2/persist/writer_on_conflict_contract_test.go）。
#   ★ 那道门**管不到生产漂移** —— 本次线上出的正是后者。
#   本脚本补的正是这一段：真库上比对**已部署**的唯一约束。
#
# 期望列清单写死在本文件里，由 scripts/ursmcheck/onconflict_constraint_check_test.go
# 与 writer.go 逐列核对 —— 改 writer 忘了改这里，门会红。
# 变量之间不加隐式约定：宁可多一次人工同步，也不要一个会静默失配的自动推导。
#
# 退出码：
#   0  存在恰好覆盖期望列的唯一约束（健康）
#   1  不存在 —— 写入会 100% 失败（这正是线上发生过的形态）
#   3  本次没有结论（psql 不可用 / 表不存在 / 输出不合契约）
#
# ★ 语义提醒：本脚本「找不到违规」是**健康**（exit 0），
#   与同目录 bloat 脚本「0 行 ⇒ 没有结论」方向相反。混用就会出现
#   「查不到却报健康」这个已坑过本项目的形态。

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
    # ★ 措辞订正（2026-10-19，§143.14）：原文写「上一轮仍在运行」，
    #   而 252 实测到的**真实成因是「重复调度」** —— 同一分钟 crond 起了两个
    #   逐字相同的实例，输的那个在这里退出。⇒ 「上一轮还在跑」会让人
    #   去找「谁挂死了」，而实际上没有任何东西挂死。
    echo "[$(date +%Y-%m-%dT%H:%M:%S%z)] skip: $(basename "$0") 锁被占用（lock=${_llmgw_lock_file}），本轮不重复执行（成因可能是**重复调度**：同一分钟起了两个实例；也可能是上一轮真的尚未结束）" \
      >> "${LLMGW_LOCK_SKIP_LOG:-/var/log/llmgw-single-instance-skip.log}" 2>/dev/null || true
    exit 0
  fi
  _llmgw_single_instance_guarded=true
fi

# ★ 心跳（2026-10-19 新增，§143.14）。
#   本脚本原先**没有**心跳：唯一的活体证据是它的日志，而日志在
#   「巡检被重复调度挤掉」或「输出被 cron 行丢掉」时**都不增长** ——
#   ⇒ 「日志陈旧」与「巡检死了」在观感上完全同形（§143.14 实测：
#   252 上这道巡检的日志停在 2026-10-10，而它其实每天都被调度）。
#
#   trap 装在闸门**之后** ⇒ 拿不到锁的那一轮**不会**刷新心跳。
#   ⇒ 心跳新鲜 = 「本轮真的问过库」；心跳陈旧 = 「巡检没在问」。
#   这两个状态在 §143.14 里分开了 9 天，此前它们是同一个状态。
STATE_DIR=${ONCONFLICT_STATE_DIR:-/var/lib/pg17-check}
LAST_RUN="$STATE_DIR/pg-onconflict.lastrun"
mkdir -p "$STATE_DIR" 2>/dev/null || true
trap '{ date +%Y-%m-%dT%H:%M:%S%z > "$LAST_RUN"; } 2>/dev/null || true' EXIT

TABLE=${ONCONFLICT_TABLE:-ursm_node_snapshot_min}
# 期望的 ON CONFLICT 推断列（逗号分隔，顺序无关）。
# ⚠ 改这里必须同步改 writer.go，并由 ursmcheck 的门核对。
EXPECT_COLS=${ONCONFLICT_EXPECT_COLS:-snapshot_ts,tenant_id,credential_id,raw_model_name}

CONTAINER=${PG17_CONTAINER:-pg-252-pg17}
PG_USER=${PG17_USER:-postgres}
DBNAME=${PG17_DB:-llm_gateway}
# ★ 这里**刻意不带 `-i`**（2026-10-06 真机验证时踩到，务必别加回去）。
#   SQL 是用 -c 传的，这个量具从头到尾**不需要** stdin，所以 -i 纯属多余。
#   而它有代价：`docker exec -i` 会把**继承来的 stdin 一并读掉**。
#   本脚本平时由 cron 以「文件」方式执行，stdin 是 /dev/null，无害；
#   但任何人用 `ssh host 'bash -s' < 本脚本` 去真机验证时，
#   bash 的 stdin **就是脚本文件本身** —— psql_17 执行的那一刻，
#   docker 会把 bash 还没读到的**脚本剩余部分**当成输入读走，
#   于是脚本静默截断：**退出码 0、输出 0 字节**。
#
#   ★ 这个形态的危险在于它伪装成结论：「exit 0」按本文件契约 = 健康。
#     我第一次真机验证就是这么把「量具被截断」读成「252 健康」的。
#     去掉 -i 之后同一条命令立刻给出真结论（exit 1 + 完整诊断）。
#   ⇒ 与 pg17-pg-availability-check.sh 里 C7-P3-2 那条同族：
#     **「跑完了」不等于「跑出结论了」**，退出码必须配上可核对的输出。
PSQL_CMD=${PSQL_CMD:-docker exec "$CONTAINER" psql -U "$PG_USER" -X -q -A -t -F'|' -v ON_ERROR_STOP=1 -d "$DBNAME" -c}

# ★ 查询超时（2026-10-19 新增，§143.14）。
#   本脚本原先**没有任何超时** —— 这是它「看起来一直在跑、其实早就死了」
#   的物理原因：PG 半死（容器还在 listen、事务卡死）时
#   `docker exec ... psql` 会**一直挂着** ⇒ 脚本持锁不放 ⇒
#   之后每一轮都在闸门处被挡掉 ⇒ 这道巡检实质上死亡，
#   而它的日志只是「陈旧」，没有任何东西说它死了。
#   ⇒ 查询必须有上限。缺 timeout(1) 时**放弃本轮**（fail-closed），
#     而不是无上限地挂着 —— 无上限地等一个可能永远不回来的答案，
#     等价于把自己的心跳也一起停掉。
QUERY_TIMEOUT=${ONCONFLICT_QUERY_TIMEOUT:-60}

# ★ 这里**不能**用 `date -Is`（GNU coreutils 写法）。BSD/macOS 的 date 不认，
#   会把每行时间戳变成一行 `date: invalid argument 's' for -I` 噪声，
#   把真正要读的那行挤下去。形态与 pg17-pg-availability-check.sh 保持一致。
now_iso() { date +%Y-%m-%dT%H:%M:%S%z; }
log() { printf '%s %s\n' "$(now_iso)" "$*" >&2; }

psql_17() {
  local sql=$1
  # ★ 缺 timeout(1) ⇒ 放弃本轮，不做无上限的查询（理由见 QUERY_TIMEOUT 处）。
  if ! command -v timeout >/dev/null 2>&1; then
    log "ABORT: 系统里没有 timeout(1) ⇒ 无法给查询设上限。"
    log "   不做无上限的查询（PG 半死时会永远挂着并永久占住单实例锁）⇒ 本次不出结论。"
    exit 3
  fi
  # shellcheck disable=SC2086 # PSQL_CMD 是刻意按多词命令传入的
  timeout "$QUERY_TIMEOUT" $PSQL_CMD "$sql" 2>&1
}

# 量具预检：先确认 PSQL_CMD 的**第一个词**在 PATH 上。
# 不做这一步时，`docker` 缺失会让 bash 打印一行 `docker: command not found`
# —— 退出码仍是 3（对的），但那行 shell 级报错会被
# monitor_script_exec_gate_test.go 的 shellFatal 正则判成「脚本崩了」。
# 那个门是对的：一个 shell 级错误签名在 cron 里会被读成崩溃。
# 正确做法是把它变成一句人话，而不是去放宽那道门。
# PSQL_CMD 可被整条覆盖，所以只看首词，不做完整解析。
check_toolchain() {
  local first=${PSQL_CMD%% *}
  if ! command -v "$first" >/dev/null 2>&1; then
    log "ABORT: 量具不可用 —— 找不到 '$first'（PSQL_CMD 的第一个词）。本次不出结论。"
    log "   在 252 上它由 docker 提供的 PG17 容器执行；本机没有 docker 就跑不了，属正常。"
    exit 3
  fi
}
check_toolchain

# 期望列先归一：去空白、转小写、按字典序排序。
# ★ 必须用 `paste -sd,` 而不是 `tr '\n' ','`：`sort` 会给输出补一个**尾换行**，
#   tr 把它换成逗号就留下尾逗号（`a,b,c,d,`），于是跟 psql 回来的 `a,b,c,d`
#   永远不相等 —— 而这个脚本对**完全健康**的库报 exit 1。
#   即「量具自己坏了，报的是假违规」。paste 的语义就是「按分隔符合并行」，
#   行数 N 产 N-1 个分隔符，天然无尾逗号。
EXPECT_SORTED=$(printf '%s' "$EXPECT_COLS" | tr ',' '\n' \
  | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//' | tr 'A-Z' 'a-z' | sort | paste -sd, -)

SQL=$(cat <<'SQL'
-- 列出该表上**全部**唯一索引/约束的列集合（按列名排序，顺序无关）。
-- 用 pg_index.indkey → attnum → attname，不依赖 pg_get_indexdef 的文本格式，
-- 免得因为换行/缩进差异误判。
SELECT ic.relname,
       (SELECT string_agg(a.attname, ',' ORDER BY a.attname)
          FROM unnest(x.indkey::int2[]) AS k(attnum)
          JOIN pg_attribute a
            ON a.attrelid = x.indrelid AND a.attnum = k.attnum
         WHERE a.attname IS NOT NULL) AS sorted_cols,
       x.indisunique::text,
       (SELECT count(*) FROM pg_constraint c WHERE c.conindid = x.indexrelid)::text
FROM pg_index x
JOIN pg_class c  ON c.oid = x.indrelid
JOIN pg_class ic ON ic.oid = x.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.relname = '__TABLE__'
  AND x.indisunique
ORDER BY 2, 1;
SQL
)
SQL=${SQL/__TABLE__/$TABLE}

out=$(psql_17 "$SQL")
rc=$?
if [ $rc -ne 0 ]; then
  # rc=124 是 timeout 自己的码：查询还挂着但问不出结果。它的含义是
  # 「PG 卡死」而不是「PG 拒连」——处置动作不同，措辞必须分开。
  if [ "$rc" -eq 124 ]; then
    log "ABORT: 查询在 ${QUERY_TIMEOUT}s 内没有返回（PG 卡死，不是拒连）—— 量具不可用，本次不出结论（不报健康）。"
  else
    log "ABORT: psql 失败 rc=$rc —— 量具不可用，本次不出结论（不报健康）。"
  fi
  printf '%s\n' "$out" >&2
  exit 3
fi

# 表不存在 ⇒ 没有结论（不是「健康」，也不是「违规」）。
if [ -z "$(printf '%s' "$out" | tr -d '[:space:]')" ]; then
  log "ABORT: 表 ${TABLE} 上没有任何唯一索引 —— 多半是表不存在，本次不出结论。"
  exit 3
fi

hit=0
lines=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  lines=$((lines + 1))
  nfield=$(printf '%s' "$line" | awk -F'|' '{print NF}')
  [ "$nfield" = "4" ] || { log "ABORT: 期望 4 个字段，实得 ${nfield}（行：${line}）"; exit 3; }
  name=$(printf '%s' "$line" | cut -d'|' -f1)
  cols=$(printf '%s' "$line" | cut -d'|' -f2)
  if [ "$cols" = "$EXPECT_SORTED" ]; then
    log "OK: 索引 ${name} 的列集合恰好覆盖 ON CONFLICT 推断列（${EXPECT_SORTED}）。"
    hit=1
  fi
done <<< "$out"

if [ "$hit" = "1" ]; then
  log "该表共有 ${lines} 个唯一索引/约束。"
  exit 0
fi

log "🔴 表 ${TABLE} 上**没有**任何唯一索引的列集合等于 (${EXPECT_SORTED})。"
log "   ⇒ writer 的 INSERT ... ON CONFLICT (${EXPECT_COLS}) 在本库会每次抛"
log "     SQLSTATE 42P10，**写入成功率 0**。这正是 2026-10-03 起线上发生过的形态。"
log "   已部署的唯一索引清单（索引名 | 列集合 | 是否唯一 | 被约束引用）："
printf '%s\n' "$out" >&2
log "   修法：把 PK/唯一约束恢复成 (${EXPECT_COLS})。**不要改 writer 去迁就 schema**。"
exit 1
