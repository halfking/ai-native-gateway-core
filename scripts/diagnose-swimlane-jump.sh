#!/usr/bin/env bash
# =============================================================================
# 实时请求流泳道跳动 — 生产 Redis 诊断脚本
#
# 目标：确认根因 #1（请求详情 4h TTL < 维度队列 24h TTL，读快照时过期详情被丢弃
#       导致泳道条数在 20 ↔ 3 之间跳变）。
#
# 用法：
#   REDIS_HOST=10.0.0.1 REDIS_PORT=6379 REDIS_PASSWORD=xxx \
#     VENDOR=minimax TENANT="" ./scripts/diagnose-swimlane-jump.sh
#
# 参数说明（都有默认值，可只设 REDIS_HOST/PASSWORD）：
#   REDIS_HOST      Redis 地址（必填）
#   REDIS_PORT      端口，默认 6379
#   REDIS_PASSWORD  密码，默认空
#   REDIS_DB        DB 号，默认 0
#   VENDOR          要观测的供应商泳道，默认 minimax
#   TENANT          租户ID；为空观测全局队列，否则观测租户队列，默认空
#   ITERATIONS      观测轮数，默认 12（每 10s 一轮，约 2 分钟）
#   INTERVAL_SEC    每轮间隔秒数，默认 10
#
# 判读：
#   - 若 ZCARD 稳定在 ~20，但 "队列成员 / 详情存活" 比例忽高忽低 → 确诊根因 #1
#     （详情过期导致读快照时丢成员）。
#   - 若 ZCARD 本身在 3 ↔ 20 之间跳 → 写入侧问题（队列被频繁清空重建），
#     根因在 Record() 的 removeLiveRequestFromQueues / lock 失败丢弃路径。
# =============================================================================
set -euo pipefail

REDIS_HOST="${REDIS_HOST:?REDIS_HOST is required}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
REDIS_DB="${REDIS_DB:-0}"
VENDOR="${VENDOR:-minimax}"
TENANT="${TENANT:-}"
ITERATIONS="${ITERATIONS:-12}"
INTERVAL_SEC="${INTERVAL_SEC:-10}"

CLI=(redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB")
if [[ -n "$REDIS_PASSWORD" ]]; then
  CLI+=(-a "$REDIS_PASSWORD" --no-auth-warning)
fi

# 选定要观测的队列 key：租户优先，否则全局
if [[ -n "$TENANT" ]]; then
  QUEUE_KEY="llmgw:live:tenant:${TENANT}:dim:vendor:${VENDOR}"
else
  QUEUE_KEY="llmgw:live:dim:vendor:${VENDOR}"
fi

printf '=%.0s' {1..78}; echo
echo "观测目标   : $QUEUE_KEY"
echo "观测轮数   : $ITERATIONS （每 ${INTERVAL_SEC}s 一轮）"
echo "Redis      : $REDIS_HOST:$REDIS_PORT db=$REDIS_DB"
printf '=%.0s' {1..78}; echo
printf '%-10s %8s %14s %14s %14s\n' "时间" "ZCARD" "成员存活率" "详情存活/总" "详情过期数"
printf '%.0s-' {1..78}; echo

for i in $(seq 1 "$ITERATIONS"); do
  ts=$(date +%T)
  # 1) 队列里实际存了多少成员（应当稳定 ~20）
  zcard=$("${CLI[@]}" ZCARD "$QUEUE_KEY" 2>/dev/null || echo "ERR")

  if [[ "$zcard" =~ ^[0-9]+$ && "$zcard" -gt 0 ]]; then
    # 2) 取出所有成员（slim tile JSON），解析出 request_id
    members=$("${CLI[@]}" ZRANGE "$QUEUE_KEY" 0 -1 2>/dev/null || echo "")
    # slim tile 形如 {"rid":"req-xxx",...}；提取 rid
    rids=$(echo "$members" | grep -oE '"rid":"[^"]+"' | sed 's/"rid":"//; s/"//')
    total=0
    detail_alive=0
    detail_expired=0
    while IFS= read -r rid; do
      [[ -z "$rid" ]] && continue
      total=$((total+1))
      # 详情 key：全局 llmgw:live:req:<id>；用 EXISTS 判断，比 GET 省
      if [[ "$("${CLI[@]}" EXISTS "llmgw:live:req:${rid}" 2>/dev/null || echo 0)" -eq 1 ]]; then
        detail_alive=$((detail_alive+1))
      else
        detail_expired=$((detail_expired+1))
      fi
    done <<< "$rids"
    if [[ "$total" -gt 0 ]]; then
      ratio=$((detail_alive * 100 / total))
    else
      ratio=0
    fi
    printf '%-10s %8s %13s%% %12s/%-4s %14s\n' \
      "$ts" "$zcard" "${ratio}%" "${detail_alive}/${total}" "$detail_expired"
  else
    # 队列为空或读取失败：列出所有 vendor 泳道，看是不是 key 名变了
    printf '%-10s %8s %14s %14s %14s\n' "$ts" "$zcard" "-" "-" "-"
  fi

  [[ "$i" -lt "$ITERATIONS" ]] && sleep "$INTERVAL_SEC"
done

printf '=%.0s' {1..78}; echo
echo "辅助：当前所有 vendor 维度队列及其长度（确认 key 命名 / 有哪些泳道）"
"${CLI[@]}" --scan --pattern 'llmgw:live:dim:vendor:*' 2>/dev/null | while read -r k; do
  printf '  %-60s %s\n' "$k" "$("${CLI[@]}" ZCARD "$k" 2>/dev/null || echo '?')"
done
printf '=%.0s' {1..78}; echo
echo "结论判读："
echo "  - ZCARD 稳定但'详情存活率'忽高忽低 → 根因#1（TTL 不一致，详情过期被丢）"
echo "  - ZCARD 本身跳变                  → 写入侧问题（Record lock 失败/队列重建）"
