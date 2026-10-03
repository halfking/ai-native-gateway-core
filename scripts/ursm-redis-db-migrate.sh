#!/usr/bin/env bash
# =====================================================================
# scripts/ursm-redis-db-migrate.sh — URSM v2 键迁移到专用 Redis db
#
# 用途
#   URSM v2 的节点键目前与网关会话键混在同一个 db（默认 db2，约 158 万键）。
#   persist writer 每分钟 SCAN <prefix>node:* 采集快照；Redis 的 MATCH 是
#   服务端过滤、游标遍历躲不掉，所以它必须走完整个键空间，实测 12.95~30.40s，
#   正好压在 writer 的 30s 预算上（main.go 的 writer 预算），于是
#   "ursm.v2: persist collect failed: redis scan failed: context deadline exceeded"
#   成为常态故障。把 URSM 挪到独立 db 后，SCAN 只遍历约 1.3K 键。
#
# ★ 为什么必须在流量静默窗口内做（不要只停 persist writer）
#   ursm:v2:node:* 由**请求路径**每请求写一次：
#     domains/streaming/executors/executor_nodehealth.go:313
#       -> Manager.RecordRequest
#       -> store.RecordRequestKeySet
#       -> record_request.lua
#   停 persist writer 只停掉"读"，停不掉"写"。只要还有请求进来，节点键就会
#   继续落在旧 db；等切到新 db 后，这段窗口内的更新就丢了 —— 而 URSM 靠
#   coverage manifest 重建时只覆盖部分节点（实测 714/1240，缺 563 个旧文法
#   节点），不能靠重启自愈。所以必须先静默流量。
#
# 用法
#   # 1) 只读预检（不写任何数据，可在任意时刻跑）
#   bash scripts/ursm-redis-db-migrate.sh --plan 172.16.2.210:6389 2 14
#
#   # 2) 静默窗口内：执行复制 + 校验（目标 db 必须为空，否则拒绝执行）
#   bash scripts/ursm-redis-db-migrate.sh --copy 172.16.2.210:6389 2 14
#
#   # 3) 脚本只**打印**后续动作，不会替你改 env 或重启服务
#
# 约束
#   - 需要 redis-cli 与目标实例可达；密码从 154 的 /etc/llm-gateway-go/env 读，
#     不接受命令行明文传入。
#   - 只迁移 <prefix>* 这一族键，不碰同 db 的其它业务键。
#   - 不做 TTL 删除语义变更：RESTORE 保留原 TTL。
# =====================================================================
set -euo pipefail

PREFIX_DEFAULT="ursm:v2:"
MODE=""
SRC_ADDR=""
SRC_DB=""
DST_DB=""

die() { printf '[die] %s\n' "$*" >&2; exit 1; }
info() { printf '[info] %s\n' "$*"; }
warn() { printf '[warn] %s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --plan|--copy) MODE="${1#--}"; shift ;;
    -a) SRC_ADDR="${2:-}"; shift 2 ;;
    -s) SRC_DB="${2:-}"; shift 2 ;;
    -d) DST_DB="${2:-}"; shift 2 ;;
    -p) PREFIX_DEFAULT="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) die "未知参数: $1" ;;
  esac
done

[ -n "$MODE" ]        || die "必须给 --plan 或 --copy"
[ -n "$SRC_ADDR" ]    || die "必须给 -a <host:port>"
[ -n "$SRC_DB" ]      || die "必须给 -s <src db>"
[ -n "$DST_DB" ]      || die "必须给 -d <dst db>"

case "$SRC_DB" in *[!0-9]*) die "src db 必须是数字: $SRC_DB";; esac
case "$DST_DB" in *[!0-9]*) die "dst db 必须是数字: $DST_DB";; esac
[ "$SRC_DB" != "$DST_DB" ] || die "src db 与 dst db 不能相同"

ENV_FILE="/etc/llm-gateway-go/env"
[ -r "$ENV_FILE" ] || die "读不到 ${ENV_FILE}，无法取 Redis 密码"
REDIS_PW="$(grep -oP '(?<=^LLM_GATEWAY_REDIS_PASSWORD=).*' "$ENV_FILE" | head -1)"
[ -n "$REDIS_PW" ] || die "$ENV_FILE 里没有 LLM_GATEWAY_REDIS_PASSWORD"

R() { redis-cli -h "${SRC_ADDR%:*}" -p "${SRC_ADDR##*:}" -a "$REDIS_PW" --no-auth-warning "$@"; }

# ------------------------------------------------------------------
# 预检
# ------------------------------------------------------------------
dst_keys() { R -n "$DST_DB" dbsize | tr -d '[:space:]'; }
src_keys() { R -n "$SRC_DB" dbsize | tr -d '[:space:]'; }

# ★ 刻意不提供「统计前缀键数」的便捷函数：它必须遍历整个键空间
#   （158 万键约 13-30s），而 Redis 上正有 writer 每分钟做同样的遍历并因此
#   超时。预检一律只用 O(1) 的 DBSIZE；精确前缀计数在 copy 阶段的校验里做。

info "源 $SRC_ADDR db$SRC_DB (总键 $(src_keys)) -> 目标 db$DST_DB (总键 $(dst_keys))"
info "前缀: ${PREFIX_DEFAULT}*"

if [ "$(dst_keys)" != "0" ]; then
  die "目标 db$DST_DB 非空（$(dst_keys) 键）。拒绝执行：迁移会与既有键混合，回滚将无法区分来源。请换一个空 db 或先人工清理。"
fi

# 目标 db 必须不含任何 ursm 残留（历史踩过的坑：db15 留着陈旧 meta/ready）
leftover="$(R -n "$DST_DB" exists "${PREFIX_DEFAULT}meta:ready" | tr -d '[:space:]')"
[ "$leftover" = "0" ] || die "目标 db$DST_DB 已存在 ${PREFIX_DEFAULT}meta:ready，拒绝执行"

if [ "$MODE" = "plan" ]; then
  info "预检通过。以下为只读信息，未做任何变更："
  info "  - 目标 db$DST_DB 为空，可用作 URSM 专用 db"
  info "  - 复制需要在流量静默窗口执行（请求路径每请求写 node 键）"
  info ""
  info "静默窗口步骤（人工执行，脚本不代劳）："
  info "  1) 停止写入方网关，使请求不再落到旧 db"
  info "  2) bash \$0 --copy ${SRC_ADDR} -s ${SRC_DB} -d ${DST_DB}"
  info "  3) 校验通过后设置 URSM_V2_REDIS_DB=${DST_DB}（留空=沿用共享 client，行为不变）"
  info "  4) 启动网关，确认 persist committed 恢复且 row 数为迁移前量级"
  info ""
  info "回滚：把 URSM_V2_REDIS_DB 置回未设置并重启即可；旧 db 数据原样保留不动。"
  exit 0
fi

# ------------------------------------------------------------------
# 复制
# ------------------------------------------------------------------
warn "=== 即将复制 ${PREFIX_DEFAULT}* ：$SRC_ADDR db$SRC_DB -> db$DST_DB ==="
warn "=== 请确认此刻请求流量已静默；否则窗口内的节点更新会丢失 ==="
read -r -p "输入 MIGRATE 确认: " ans
[ "$ans" = "MIGRATE" ] || die "未确认，已取消"

copied=0
skipped=0
cur=0
while :; do
  batch="$(R -n "$SRC_DB" scan "$cur" count 500 2>/dev/null)"
  cur="$(printf '%s\n' "$batch" | head -1)"
  keys="$(printf '%s\n' "$batch" | tail -n +2 | grep "^${PREFIX_DEFAULT}" || true)"
  n=0
  while IFS= read -r k; do
    [ -n "$k" ] || continue
    # RESTORE 保留原 TTL；已存在则跳过（目标本应为空，重复即异常信号）
    out="$(R -n "$SRC_DB" --no-raw dump "$k" 2>/dev/null | R -n "$DST_DB" -x restore "$k" 0 replace 2>&1 || true)"
    case "$out" in
      *BUSYKEY*) skipped=$((skipped+1)); warn "目标已存在，跳过: $k" ;;
      *) copied=$((copied+1)) ;;
    esac
    n=$((n+1))
  done <<EOF
$keys
EOF
  printf '\r[copy] 已处理前缀键 %d（游标 %s）' "$copied" "$cur"
  [ "$cur" = "0" ] && break
done
printf '\n'

info "复制完成: copied=$copied skipped=$skipped"

# ------------------------------------------------------------------
# 校验
# ------------------------------------------------------------------
[ "$skipped" -eq 0 ] || warn "有 $skipped 个键在目标已存在 —— 目标 db 并非真正为空，请人工复核"

# 逐键点验：在源与目标各 EXISTS 同一批前缀键，比对计数
verify_count() {
  local db="$1" n=0 cur=0 batch
  while :; do
    batch="$(R -n "$db" scan "$cur" count 1000 2>/dev/null)"
    cur="$(printf '%s\n' "$batch" | head -1)"
    n=$(( n + $(printf '%s\n' "$batch" | tail -n +2 | grep -c "^${PREFIX_DEFAULT}" || true) ))
    [ "$cur" = "0" ] && break
  done
  printf '%s\n' "$n"
}

src_n="$(verify_count "$SRC_DB")"
dst_n="$(verify_count "$DST_DB")"
info "校验: 源 db$SRC_DB 前缀键=$src_n  目标 db$DST_DB 前缀键=$dst_n"

if [ "$src_n" != "$dst_n" ]; then
  die "校验失败：源 $src_n != 目标 $dst_n。不要设置 URSM_V2_REDIS_DB，保持现状并排查。"
fi
[ "$dst_n" -gt 0 ] || die "目标前缀键数为 0，迁移无效。"

info "✓ 校验通过（$dst_n 个前缀键两侧一致）"
info ""
info "后续人工步骤（脚本未执行）："
info "  1) 在 154 与 245 的 env 中设置 URSM_V2_REDIS_DB=${DST_DB}"
info "     ★ 必须在两侧都设；只设一台会造成两台快照节点集不一致"
info "  2) 启动网关，观察 'ursm.v2: persist committed' 的 rows 是否回到迁移前量级"
info "  3) 观察一轮 'persist collect failed' 是否消失"
info ""
info "回滚：把 URSM_V2_REDIS_DB 置回未设置并重启。旧 db$SRC_DB 数据原样保留。"
