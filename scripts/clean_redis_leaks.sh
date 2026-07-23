#!/bin/bash
# clean_redis_leaks.sh
# 2026-07-23: 清理 Redis 中没有 TTL 的 session/pending_response 残留 keys
# 触发场景：HSet 隐式清除 TTL 的 bug 导致部分 key 永不过期
# 运行后需要重启 LLM Gateway 触发一次 Touch/Set 让正常 keys 续期

set -euo pipefail

REDIS_HOST="${REDIS_HOST:-172.16.2.210}"
REDIS_PORT="${REDIS_PORT:-6389}"
REDIS_PASS="${REDIS_PASS:-Veritrans9900}"

# 安全网：dry-run 支持
DRY_RUN="${DRY_RUN:-0}"

if [ "$DRY_RUN" = "1" ]; then
  echo "[DRY RUN] No keys will be deleted"
  REDIS_ARGS=""
else
  REDIS_ARGS=""
fi

run_redis() {
  redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" -a "$REDIS_PASS" "$@"
}

count_no_ttl() {
  local pattern=$1
  local label=$2
  run_redis EVAL "
    local cursor = '0'
    local n = 0
    repeat
      local result = redis.call('SCAN', cursor, 'MATCH', '$pattern', 'COUNT', 1000)
      cursor = result[1]
      for _, k in ipairs(result[2]) do
        local ttl = redis.call('TTL', k)
        if ttl == -1 then
          n = n + 1
        end
      end
    until cursor == '0'
    return n
  " 0
}

delete_no_ttl() {
  local pattern=$1
  local label=$2
  echo "=== Scanning $label (pattern: $pattern) ==="
  local count=$(count_no_ttl "$pattern" "$label")
  echo "  found $count keys without TTL"
  if [ "$count" = "0" ]; then
    return
  fi
  if [ "$DRY_RUN" = "1" ]; then
    echo "  [DRY RUN] would delete $count keys"
    return
  fi
  read -p "Delete $count keys? (yes/no): " ans
  if [ "$ans" = "yes" ]; then
    echo "  deleting $count keys..."
    run_redis EVAL "
      local cursor = '0'
      local n = 0
      repeat
        local result = redis.call('SCAN', cursor, 'MATCH', '$pattern', 'COUNT', 1000)
        cursor = result[1]
        for _, k in ipairs(result[2]) do
          local ttl = redis.call('TTL', k)
          if ttl == -1 then
            redis.call('DEL', k)
            n = n + 1
          end
        end
      until cursor == '0'
      return n
    " 0
    echo "  ✓ deleted"
  else
    echo "  skipped"
  fi
}

# Step 1: scan all keyspace for no-TTL keys by prefix
echo "=== Step 1: Scanning all keyspace for no-TTL keys ==="
PREFIXES=(
  "session:*"
  "session_pref:*"
  "session:key:*"
  "pending_response:*"
  "llmgw:live:*"
  "llmgw:stats:*"
)
total_no_ttl=0
for pattern in "${PREFIXES[@]}"; do
  c=$(count_no_ttl "$pattern" "$pattern")
  if [ "$c" != "0" ]; then
    echo "  $pattern: $c"
    total_no_ttl=$((total_no_ttl + c))
  fi
done
echo "TOTAL no_ttl keys: $total_no_ttl"

# Step 2: per-pattern cleanup
echo ""
echo "=== Step 2: Cleanup options ==="
echo "1. session:gw_* (永不过期的活跃 session)"
echo "2. session:key:* (永不过期的 session_key 映射)"
echo "3. session_pref:* (永不过期的偏好)"
echo "4. pending_response:* (永不过期的响应缓存)"
echo "5. llmgw:live:* (永不过期的实时流)"
echo "6. llmgw:stats:* (永不过期的统计)"
echo "0. Exit"
echo ""
read -p "Which patterns to clean up? (e.g. 1,2,3 or 0): " choice

IFS=',' read -ra CHOICES <<< "$choice"
for c in "${CHOICES[@]}"; do
  case $c in
    0) echo "exiting"; exit 0 ;;
    1) delete_no_ttl "session:gw_*" "session hash" ;;
    2) delete_no_ttl "session:key:*" "session key map" ;;
    3) delete_no_ttl "session_pref:*" "session preferences" ;;
    4) delete_no_ttl "pending_response:*" "pending responses" ;;
    5) delete_no_ttl "llmgw:live:*" "live stream" ;;
    6) delete_no_ttl "llmgw:stats:*" "stats" ;;
    *) echo "unknown: $c" ;;
  esac
done

echo ""
echo "=== Step 3: verify ==="
for pattern in "${PREFIXES[@]}"; do
  c=$(count_no_ttl "$pattern" "$pattern")
  if [ "$c" != "0" ]; then
    echo "  $pattern: $c no_ttl remaining"
  fi
done
echo "done"
