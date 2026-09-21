#!/usr/bin/env bash
# deploy-lib/redis-discover.sh — 远端 Redis 容器/端口三级发现（154/245 通过 252 使用）
#
# 与 scripts/deploy-local-lib.sh 内的同名函数语义保持一致：
#   1. named   — 固定列表（nbjl-redis / llm-gateway-redis / redis / kx-redis）
#   2. scan    — docker ps 模糊匹配 image 名含 redis/valkey/cache 或 6379/tcp 端口
#   3. system  — 远端宿主端口（ss/netstat）上的 redis-cli PING 验证
#
# 用法：
#   source "$SCRIPT_DIR/deploy-lib/redis-discover.sh"
#   info=$(deploy_discover_redis_on_remote "$SSH_CMD")
#
#   if [[ "$info" == *:* ]]; then
#     host_port=${info%%:*}
#   else
#     container=$info
#   fi
#
# 失败时返回空字符串，并打印警告。函数不会修改目标机状态。
set -euo pipefail

# Best-effort redis-cli PING inside a container. Falls back to inspecting
# the container's exposed private ports when redis-cli is not installed.
discover_redis_container_ping() {
  local ssh_cmd=$1 name=$2
  if _discover_redis_ssh "$ssh_cmd" "docker exec '$name' sh -c 'command -v redis-cli >/dev/null 2>&1 && redis-cli PING' 2>/dev/null | grep -q PONG"; then
    return 0
  fi
  local ports
  ports=$(_discover_redis_ssh "$ssh_cmd" "docker inspect -f '{{.Config.Image}} {{range .NetworkSettings.Ports}}{{.PrivatePort}} {{end}}' '$name'" 2>/dev/null || true)
  [[ "$ports" == *' 6379 '* ]] && return 0
  return 1
}

_discover_redis_ssh() {
  local ssh_cmd=$1
  shift
  # shellcheck disable=SC2086
  $ssh_cmd "$@"
}

# Probe a host listener (port arg $1) using redis-cli over the wire.
discover_redis_system_ping() {
  local ssh_cmd=$1 port=$2
  _discover_redis_ssh "$ssh_cmd" "command -v redis-cli >/dev/null 2>&1 || exit 1; redis-cli -h 127.0.0.1 -p '$port' PING" 2>/dev/null | grep -q PONG
}

# Run the three-level Redis discovery on a remote host via the given
# SSH command wrapper. Prints the discovered target ("container" or
# "host:port") on stdout; prints nothing when nothing works.
deploy_discover_redis_on_remote() {
  local ssh_cmd=$1
  local discovered=""

  # Level 1: hard-coded list of commonly used container names.
  local name
  for name in nbjl-redis llm-gateway-redis redis kx-redis; do
    if _discover_redis_ssh "$ssh_cmd" "command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq '$name'"; then
      if discover_redis_container_ping "$ssh_cmd" "$name"; then
        discovered="$name"
        break
      fi
    fi
  done

  # Level 2: docker ps scan for redis/valkey/cache images or 6379 ports.
  if [[ -z "$discovered" ]]; then
    local candidate image ports
    while IFS=$'\t' read -r candidate image ports; do
      [[ -z "$candidate" ]] && continue
      if [[ "$image" =~ (^|[/_-])(redis|valkey|cache)([/_-]|$) || "$ports" == *6379/tcp* ]]; then
        if discover_redis_container_ping "$ssh_cmd" "$candidate"; then
          discovered="$candidate"
          break
        fi
      fi
    done < <(_discover_redis_ssh "$ssh_cmd" "docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}' 2>/dev/null")
  fi

  # Level 3: host-side ss/netstat listeners on canonical Redis ports.
  if [[ -z "$discovered" ]]; then
    local port
    while read -r port; do
      [[ -z "$port" ]] && continue
      if discover_redis_system_ping "$ssh_cmd" "$port"; then
        discovered="127.0.0.1:${port}"
        break
      fi
    done < <(_discover_redis_ssh "$ssh_cmd" "
      if command -v ss >/dev/null 2>&1; then
        ss -ltn 2>/dev/null | awk 'tolower(\$0) ~ /:(6379|16379)[^0-9]/ {n=split(\$4,p,\":\"); print p[n]}'
      elif command -v netstat >/dev/null 2>&1; then
        netstat -an 2>/dev/null | awk 'tolower(\$0) ~ /\\.(6379|16379)[^0-9]/ {print \"6379\"}' | sort -u
      fi
    ")
  fi

  printf '%s\n' "$discovered"
}

# Append LLM_GATEWAY_REDIS_ADDR (and optional password) to the target env
# file, only when not already set. Honours SSOT and avoids clobbering.
deploy_redis_append_to_env() {
  local ssh_cmd=$1 env_file=$2 discovered=$3 password=$4
  [[ -n "$discovered" ]] || return 0
  if _discover_redis_ssh "$ssh_cmd" "grep -q '^LLM_GATEWAY_REDIS_ADDR=' '$env_file'"; then
    return 0
  fi
  local addr
  if [[ "$discovered" == *:* ]]; then
    addr="$discovered"
  else
    addr="${discovered}:6379"
  fi
  _discover_redis_ssh "$ssh_cmd" "chmod 0600 '$env_file' 2>/dev/null || true; {
    echo 'LLM_GATEWAY_REDIS_ADDR='
    printf '%s\n' '$addr'
    if [[ -n '$password' ]]; then
      printf 'LLM_GATEWAY_REDIS_PASSWORD=%q\n' '$password'
    fi
  } >> '$env_file'" >/dev/null 2>&1 || true
  _discover_redis_ssh "$ssh_cmd" "chmod 0600 '$env_file'" >/dev/null 2>&1 || true
}