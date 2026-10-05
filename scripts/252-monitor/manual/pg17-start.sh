#!/bin/bash
# =============================================================================
# pg17-start.sh — pg-252-pg17 容器启动/重建参考脚本
#
# ★★ 2026-10-05：本文件此前**只存在于 252 的 /opt/scripts/ 下，不在任何版本
#    控制里**。这正是 2026-10-05 那次事故里 pgvector 恢复步骤静默失败
#    却长期无人 review 的根因 —— 没有正典，就没有 diff，就没有第二次发现。
#    现以本文件为正典；生产副本修改前必须先 diff 本文件。
#
# 用途：重建或重启 pg-252-pg17 容器时使用。
# ★ 这是一个**手动**脚本，绝不可进 cron。门：
#   scripts/ursmcheck/pg17_start_script_test.go 断言本文件不出现在
#   etc.cron.d.pg17 里，且任何被 cron 调度的脚本都不得含容器删除动词。
#   （它做的事是 stop + rm 一个生产数据库容器 —— 定时执行等于定时删库。）
#
# 经验（2026-07-29）：k8s-file 日志驱动无轮转，ctr.log 积累 49GB 撑爆磁盘
# 修复：改用 json-file + max-size=100m + max-file=3；logrotate 兜底
# 变更（2026-09-29 D6 受控重建，round11 §八 ⑤）：
#   - ctr.log 上限 100m×3 → 1g×2：100MB 实测 ≈30min 即轮转（none+1s 下单条
#     宽表语句数百 KB），取证窗口过短；1g×2 ≈12h，磁盘增量 ≤1.5GB。
#   - --shm-size 1g（2026-09-23 加入）随本次重建正式生效（DSM 根修收口）。
#   - LogConfig 持久化：log_statement=none + log_min_duration_statement=1000
#     固化到容器命令行，不再依赖 auto.conf 存活（容器重建/换卷双保险）。
#   - checkpoint/WAL：max_wal_size 2GB→4GB、checkpoint_timeout 300s→900s
#     （round11 实测 5min 一轮 checkpoint ×33/161min；磁盘 29G free 取保守档）。
#
# 退出码：
#   0  PG 已起来且全部自检通过
#   1  PG 起来了，但**降级**（本例：pgvector 没能恢复或加载失败）
#   2  PG 没起来（60s 内未 accepting）
#
#   ★ 2026-10-05 之前本脚本没有这个分级：pgvector 恢复失败时走的是
#     `… || echo "WARN: …"`，然后**继续跑完并退出 0**。
#     ⇒ 「PG 恢复完成」与「PG 恢复了一半」在退出码上同形。
#     事故当天我 tail 了输出，恰好把 WARN 那几行截掉了，于是读成「一切正常」。
#     分级退出的意义就在这里：让「部分失败」无法被读成「成功」。
# =============================================================================

set -euo pipefail

CONTAINER_NAME="${1:-pg-252-pg17}"
IMAGE="docker.io/library/kx-citus-pg17:amd64"
DATA_DIR="/data/pg-data-252-pg17"
LOG_OPTS="--log-driver json-file --log-opt max-size=1g --log-opt max-file=2"
PG_TUNE="-c max_wal_size=4GB -c checkpoint_timeout=900 -c log_statement=none -c log_min_duration_statement=1000"

echo "==> 停掉并移除旧容器 $CONTAINER_NAME"
# ★ 这两行是**破坏性**的。在一条复合命令里顺手写下它们，就是 2026-10-05
#   那次事故的成因（清理临时文件时把容器删除写进了同一条 ssh 里的子命令）。
#   ⇒ 破坏性动作单独成段、单独 echo，执行前先看清自己在删什么。
podman stop "$CONTAINER_NAME" 2>/dev/null || true
podman rm "$CONTAINER_NAME" 2>/dev/null || true

echo "==> 启动 $CONTAINER_NAME（数据目录 $DATA_DIR，bind mount）"
podman run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  --shm-size 1g \
  $LOG_OPTS \
  -p 172.16.2.210:5432:5432 \
  -v "$DATA_DIR:/var/lib/postgresql/data" \
  \
  "$IMAGE" $PG_TUNE

READY=0
# ★ 等待参数做成可注入：默认 20 次 × 3 秒 = 60 秒，正是生产该等的时长；
#   但门要能在不真的等 60 秒的前提下走完「PG 没起来」那条路径。
#   两者共用同一段代码 ⇒ 门测的就是生产跑的那段，不是替身。
READY_TRIES=${READY_TRIES:-20}
READY_INTERVAL=${READY_INTERVAL:-3}
for i in $(seq 1 "$READY_TRIES"); do
  if podman exec "$CONTAINER_NAME" pg_isready -U llm_gateway -d llm_gateway 2>/dev/null | grep -q "accepting"; then
    echo "PG READY"; READY=1; break
  fi
  sleep "$READY_INTERVAL"
done
if [ "$READY" != "1" ]; then
  echo "PG NOT READY AFTER $(( READY_TRIES * READY_INTERVAL ))s" >&2
  exit 2
fi

# ---------------------------------------------------------------- pgvector
# 背景（2026-09-30 R15 审计轮引入）：镜像 kx-citus-pg17 不含 vector .so，
# 但 /data 持久卷的 pg_extension 目录记录仍在（llm_gateway vector 0.8.x /
# memora / redclaw）—— D6 重建（09-29）起监控探针碰 vector 索引即报
# could not access file "$libdir/vector"。
# 容器无外网，走宿主下载 deb -> podman cp -> dpkg；幂等：.so 已在则跳过。
VECTOR_SO=/usr/lib/postgresql/17/lib/vector.so
VECTOR_DEGRADED=0

if ! podman exec "$CONTAINER_NAME" test -f "$VECTOR_SO" 2>/dev/null; then
  echo "==> pgvector .so missing in image - restoring from Aliyun PGDG mirror"
  DEB="/tmp/postgresql-17-pgvector_latest_amd64.deb"
  URL="https://mirrors.aliyun.com/postgresql/repos/apt/dists/trixie-pgdg/main/binary-amd64/Packages.gz"
  # ★ 原写法是 `curl -s … | zcat | awk "/^Package: …/"`，2026-10-05 实测解析不出
  #   文件名（静默为空 ⇒ 走「restore skipped」分支 ⇒ 脚本继续并退出 0）。
  #   这里不追求修好 awk：解析失败的风险面太大，而**失败的后果已经由下面的
  #   功能自检兜住**。解析不出来就大声说，然后以降级退出。
  FNAME=$(curl -s --max-time 60 "$URL" | zcat 2>/dev/null \
    | awk '/^Package: postgresql-17-pgvector/{f=1} f&&/^Filename:/{print $2; exit}' || true)
  if [ -n "${FNAME:-}" ]; then
    if curl -s --max-time 120 -o "$DEB" "https://mirrors.aliyun.com/postgresql/repos/apt/$FNAME" \
       && podman cp "$DEB" "$CONTAINER_NAME":/tmp/pgvector.deb \
       && podman exec "$CONTAINER_NAME" dpkg -i /tmp/pgvector.deb >/dev/null 2>&1; then
      echo "==> pgvector deb installed"
    else
      echo "!! pgvector deb 下载/安装失败" >&2
      VECTOR_DEGRADED=1
    fi
  else
    echo "!! 解析不到 pgvector deb 文件名，恢复跳过（这不是无害的）" >&2
    VECTOR_DEGRADED=1
  fi
fi

# ------------------------------------------------- ★ 功能自检（不是存在性检查）
# 问的是「vector 到底能不能用」，不是「文件在不在」。
# ★ 为什么必须这样：2026-10-05 恢复现场里，.so 是**缺失**的，cp 没成功；
#   而原脚本只做到「.so 在不在」这一步，且失败后不影响退出码。
#   更一般地：文件在 ≠ ABI 对 ≠ 扩展能加载。COPY 到容器成功也可能拿到
#   为别的 PG 大版本编译的 .so。
# ★ 用 LOAD 'vector' 而不是 CREATE EXTENSION：LOAD 只把共享库拉进本会话，
#   **不建任何对象**，所以这是一个零副作用的功能自检。
#   （CREATE EXTENSION 会改 catalog —— 恢复脚本不该顺手改库。）
if ! podman exec "$CONTAINER_NAME" psql -U llm_gateway -d llm_gateway -X -q -t -c \
     "LOAD 'vector'" >/dev/null 2>&1; then
  echo "!! pgvector 功能自检失败：LOAD 'vector' 报错。依赖 vector 的查询会失败。" >&2
  VECTOR_DEGRADED=1
else
  echo "==> pgvector 功能自检通过（LOAD 'vector' 成功）"
fi

# ---------------------------------------------------------------- 调优参数核对
echo "==> 生效的调优参数"
podman exec "$CONTAINER_NAME" psql -U llm_gateway -d llm_gateway -At -c \
  "SELECT 'max_wal_size', setting FROM pg_settings WHERE name='max_wal_size';
   SELECT 'checkpoint_timeout', setting FROM pg_settings WHERE name='checkpoint_timeout';
   SELECT 'log_statement', setting FROM pg_settings WHERE name='log_statement';
   SELECT 'log_min_duration_statement', setting FROM pg_settings WHERE name='log_min_duration_statement';"

if [ "$VECTOR_DEGRADED" = "1" ]; then
  cat >&2 <<'DEGRADED'
============================================================
 PG 已恢复运行，但**处于降级状态**（本脚本退出码 1）。
 已知降级项：pgvector 扩展不可用。
 影响：llm_gateway / memora / redclaw 三个库里凡是碰 vector 索引的
       查询会直接报错。已建好的 vector 索引仍在，不会丢。
 处置：先确认业务是否真的在查这些索引；需要的话按本文件头部注释里的
       步骤重跑 pgvector 恢复，并注意**本脚本不会修好一个已经降级的库**。
       恢复后请再跑一次 scripts/252-monitor/ 下的巡检确认。
============================================================
DEGRADED
  exit 1
fi

echo "PG 恢复完成，全部自检通过"
exit 0
