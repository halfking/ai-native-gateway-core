#!/bin/bash
# apply_redis_ttl_optimization.sh
# 2026-07-23: 应用 Redis TTL 优化
#
# 这是代码改动后的辅助脚本：
# - 清掉已存在的"过长 TTL" key（不再需要的）
# - 缩短关键 key 的 TTL 到新默认值
# - 删除已确认无用的"过宽"缓存
#
# 改动：
# - session TTL: 7d → 3d
# - pending_response TTL: 7d → 1h
# - stats baseline/board TTL: 7d → 1d
#
# 用法:
#   DRY_RUN=1 ./apply_redis_ttl_optimization.sh       # 预览
#   ./apply_redis_ttl_optimization.sh                # 实际执行
#   SKIP_CONFIRM=1 ./apply_redis_ttl_optimization.sh  # 跳过确认（CI 用）

set -euo pipefail

REDIS_HOST="${REDIS_HOST:-172.16.2.210}"
REDIS_PORT="${REDIS_PORT:-6389}"
REDIS_PASS="${REDIS_PASS:-Veritrans9900}"
DRY_RUN="${DRY_RUN:-0}"
SKIP_CONFIRM="${SKIP_CONFIRM:-0}"

run_redis() {
  redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" -a "$REDIS_PASS" "$@"
}

# Count keys with optional pattern filter
count_keys() {
  local pattern=$1
  run_redis EVAL "
    local cursor = '0'
    local n = 0
    repeat
      local result = redis.call('SCAN', cursor, 'MATCH', '$pattern', 'COUNT', 1000)
      cursor = result[1]
      n = n + #result[2]
    until cursor == '0'
    return n
  " 0
}

# Delete keys matching pattern
delete_pattern() {
  local pattern=$1
  local label=$2
  local before=$(count_keys "$pattern")
  echo "  [DELETE] $label ($pattern): $before keys"
  if [ "$DRY_RUN" = "1" ]; then
    return
  fi
  if [ "$SKIP_CONFIRM" != "1" ]; then
    read -p "    delete $before keys? (yes/no): " ans
    if [ "$ans" != "yes" ]; then
      echo "    skipped"
      return
    fi
  fi
  run_redis EVAL "
    local cursor = '0'
    local n = 0
    repeat
      local result = redis.call('SCAN', cursor, 'MATCH', '$pattern', 'COUNT', 1000)
      cursor = result[1]
      for _, k in ipairs(result[2]) do
        redis.call('DEL', k)
        n = n + 1
      end
    until cursor == '0'
    return n
  " 0
  echo "    ✓ deleted"
}

# Update TTL of keys matching pattern (only if current TTL is longer than new_ttl_seconds)
update_ttl() {
  local pattern=$1
  local new_ttl=$2
  local label=$3
  echo "  [UPDATE_TTL] $label ($pattern) -> ${new_ttl}s"
  if [ "$DRY_RUN" = "1" ]; then
    return
  fi
  if [ "$SKIP_CONFIRM" != "1" ]; then
    read -p "    update TTL? (yes/no): " ans
    if [ "$ans" != "yes" ]; then
      echo "    skipped"
      return
    fi
  fi
  run_redis EVAL "
    local cursor = '0'
    local n = 0
    repeat
      local result = redis.call('SCAN', cursor, 'MATCH', '$pattern', 'COUNT', 1000)
      cursor = result[1]
      for _, k in ipairs(result[2]) do
        local ttl = redis.call('TTL', k)
        if ttl == -1 or ttl > tonumber('$new_ttl') then
          redis.call('EXPIRE', k, tonumber('$new_ttl'))
          n = n + 1
        end
      end
    until cursor == '0'
    return n
  " 0
  echo "    ✓ updated"
}

echo "============================================"
echo " Redis TTL Optimization"
echo "============================================"
echo "Target: $REDIS_HOST:$REDIS_PORT"
echo "DRY_RUN=$DRY_RUN  SKIP_CONFIRM=$SKIP_CONFIRM"
echo ""

echo "=== Step 1: Baseline stats (before) ==="
echo "  Total: $(run_redis DBSIZE)"
echo "  Memory: $(run_redis INFO memory | grep used_memory_human | cut -d: -f2 | tr -d '\r')"
echo ""

echo "=== Step 2: Clean up known-stale caches ==="
# 永不过期的活跃度 key（stats rebuild 索引用）— 保留
# 已停止会话索引 — 保留（24h TTL 自动清理）
# stats delta 过长 TTL — 缩短到 1 天
update_ttl "llmgw:stats:delta:tenant:*" 86400 "stats delta per-tenant (long TTL)"
update_ttl "llmgw:stats:delta:global:*" 86400 "stats delta global (long TTL)"
update_ttl "llmgw:stats:baseline:tenant:*" 86400 "stats baseline (7d -> 1d)"
update_ttl "llmgw:stats:board:tenant:*" 86400 "stats board (7d -> 1d)"
update_ttl "llmgw:stats:board:global:*" 86400 "stats board global (7d -> 1d)"
echo ""

echo "=== Step 3: Apply session TTL cap (3 days) ==="
# 仅对 TTL > 3 天的 session 缩到 3 天
update_ttl "session:gw_*" 259200 "session hash (7d+ -> 3d)"
update_ttl "session:key:*" 259200 "session key map (7d+ -> 3d)"
update_ttl "session_pref:*" 259200 "session preferences (7d+ -> 3d)"
update_ttl "session:apiKey:*:active" 259200 "active session set (7d+ -> 3d)"
echo ""

echo "=== Step 4: Apply pending_response TTL (1 hour) ==="
update_ttl "pending_response:*" 3600 "pending response cache (7d+ -> 1h)"
update_ttl "pending_response:index:*" 3600 "pending response index (7d+ -> 1h)"
echo ""

echo "=== Step 5: Clean up orphan session entries ==="
# session:stopped:{tenant} 应该 24h 后自动清理，但部分可能泄漏
delete_pattern "session:stopped:*" "stopped session indexes (orphan)"
echo ""

echo "=== Final stats (after) ==="
echo "  Total: $(run_redis DBSIZE)"
echo "  Memory: $(run_redis INFO memory | grep used_memory_human | cut -d: -f2 | tr -d '\r')"
echo ""
echo "============================================"
echo " Done!"
echo "============================================"
