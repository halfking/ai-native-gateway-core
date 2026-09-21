#!/usr/bin/env bash
# Smart container discovery with priority and validation
set -euo pipefail

detect_postgres_container() {
  local candidates=() c
  
  # Priority 1: Check for llm-gateway-pg first
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "llm-gateway-pg"; then
    if pg_container_usable "llm-gateway-pg"; then
      DL_PG_CONTAINER="llm-gateway-pg"
      log "PostgreSQL: using priority container llm-gateway-pg"
      return 0
    fi
  fi
  
  # Priority 2: Check environment variable if set
  if [[ -n "${LLM_GATEWAY_PG_CONTAINER:-}" ]]; then
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$LLM_GATEWAY_PG_CONTAINER"; then
      if pg_container_usable "$LLM_GATEWAY_PG_CONTAINER"; then
        DL_PG_CONTAINER="$LLM_GATEWAY_PG_CONTAINER"
        log "PostgreSQL: using environment variable container $LLM_GATEWAY_PG_CONTAINER"
        return 0
      fi
    fi
  fi
  
  # Priority 3: Search for other common names
  for c in postgres kx-citus; do
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then
      if pg_container_usable "$c"; then
        DL_PG_CONTAINER=$c
        log "PostgreSQL: using common name container $c"
        return 0
      fi
    fi
  done
  
  # Priority 4: Scan all running containers for pg17 images
  log "PostgreSQL: scanning for pg17 containers..."
  while IFS=$'\t' read -r name image; do
    [[ -z "$name" ]] && continue
    # Check if image looks like PostgreSQL 17
    if [[ "$image" =~ (postgres|postgresql).*17 ]] || [[ "$image" =~ (postgres|postgresql):17 ]]; then
      log "PostgreSQL: found pg17 candidate: $name (image: $image)"
      if pg_container_usable "$name"; then
        DL_PG_CONTAINER=$name
        log "PostgreSQL: using discovered pg17 container $name"
        return 0
      fi
    fi
  done < <(docker ps --format '{{.Names}}\t{{.Image}}' 2>/dev/null)
  
  log "PostgreSQL: no usable container found"
  return 1
}

pg_container_usable() {
  local container="$1"
  docker start "$container" >/dev/null 2>&1 || return 1

  # Try to get database info
  local db_user db_pass db_name db_port
  db_user=$(dl_container_env "$container" POSTGRES_USER); db_user=${db_user:-postgres}
  db_pass=$(dl_container_env "$container" POSTGRES_PASSWORD)
  db_name="llm_gateway"

  # Get port mapping
  db_port=$(docker port "$container" 5432/tcp 2>/dev/null | sed -n 's/.*://p' | head -n1 || true)
  if [[ -z "$db_port" ]]; then
    warn "PostgreSQL container $container has no host port mapping"
    return 1
  fi

  # 2026-09-17 audit fix: this used to be `sleep 2` + `psql -lqt | grep -c
  # llm_gateway`. Three defects fell out of that shape:
  #   1. During a PG restart/recovery window the connection failure was
  #      misread as "database missing" ("exists but no llm_gateway
  #      database"), then the follow-up probe failed too and a perfectly
  #      healthy container was declared "not usable" — leaving
  #      DL_PG_CONTAINER empty for dl_write_env. pg_isready (no auth) now
  #      gates the SQL probes.
  #   2. `psql -lqt` without -d connects to the database named after the
  #      user, so a generic container that had NOT been initialized with
  #      POSTGRES_DB=llm_gateway always failed the list probe → "not
  #      usable" instead of the "will create" path. All probes now pin
  #      -d postgres (always present after initdb).
  #   3. `grep -c llm_gateway` substring-matched sibling databases like
  #      llm_gateway_backup. Replaced by an exact pg_database query.
  local deadline=$(( $(date +%s) + 15 )) ready=0
  while (( $(date +%s) < deadline )); do
    if docker exec "$container" pg_isready -U "$db_user" -d postgres >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  if (( ! ready )); then
    warn "PostgreSQL: container $container not usable (pg_isready not accepting after 15s; still starting or crashlooping)"
    return 1
  fi

  # Exact existence probe: "1" = database present, "0" = connected but
  # genuinely absent (fresh container → caller may create it). A failed
  # connection after readiness is an auth/config problem, not a missing
  # database — say so.
  local exists
  if [[ -n "$db_pass" ]]; then
    exists=$(docker exec -e PGPASSWORD="$db_pass" "$container" psql -U "$db_user" -d postgres -Atqc \
      "SELECT 1 FROM pg_database WHERE datname='$db_name'" 2>/dev/null) \
      || { warn "PostgreSQL: container $container not usable (cannot authenticate as $db_user; check POSTGRES_PASSWORD drift)"; return 1; }
  else
    exists=$(docker exec "$container" psql -U "$db_user" -d postgres -Atqc \
      "SELECT 1 FROM pg_database WHERE datname='$db_name'" 2>/dev/null) \
      || { warn "PostgreSQL: container $container not usable (cannot connect as $db_user)"; return 1; }
  fi

  # Store credentials for later use (both outcomes are usable)
  export DL_DISCOVERED_PG_USER="$db_user"
  export DL_DISCOVERED_PG_PASS="$db_pass"
  export DL_DISCOVERED_PG_PORT="$db_port"

  if [[ "$exists" == "1" ]]; then
    log "PostgreSQL: container $container has llm_gateway database ✓"
    export DL_DISCOVERED_PG_HAS_DB=1
    return 0
  fi
  log "PostgreSQL: container $container is connectable, will create llm_gateway database"
  export DL_DISCOVERED_PG_HAS_DB=0
  return 0
}

detect_redis_container() {
  local c
  
  # Priority 1: Check for llm-gateway-redis first
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "llm-gateway-redis"; then
    if redis_container_usable "llm-gateway-redis"; then
      DL_REDIS_CONTAINER="llm-gateway-redis"
      log "Redis: using priority container llm-gateway-redis"
      return 0
    fi
  fi
  
  # Priority 2: Check environment variable if set
  if [[ -n "${LLM_GATEWAY_REDIS_CONTAINER:-}" ]]; then
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$LLM_GATEWAY_REDIS_CONTAINER"; then
      if redis_container_usable "$LLM_GATEWAY_REDIS_CONTAINER"; then
        DL_REDIS_CONTAINER="$LLM_GATEWAY_REDIS_CONTAINER"
        log "Redis: using environment variable container $LLM_GATEWAY_REDIS_CONTAINER"
        return 0
      fi
    fi
  fi
  
  # Priority 3: Search for other common names
  for c in redis kx-redis nbjl-redis memora-redis; do
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then
      if redis_container_usable "$c"; then
        DL_REDIS_CONTAINER=$c
        log "Redis: using common name container $c"
        return 0
      fi
    fi
  done
  
  # Priority 4: Scan all running containers for Redis images
  log "Redis: scanning for redis containers..."
  local line name image ports
  while IFS=$'\t' read -r name image ports; do
    [[ -z "$name" ]] && continue
    [[ "$name" == "$DL_REDIS_CONTAINER" ]] && continue
    if [[ "$image" =~ (^|[/_-])(redis|valkey)([/_-]|:|$) ]] || [[ "$ports" == *6379/tcp* ]]; then
      log "Redis: found candidate: $name (image: $image)"
      if redis_container_usable "$name"; then
        DL_REDIS_CONTAINER=$name
        log "Redis: using discovered container $name"
        return 0
      fi
    fi
  done < <(docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}' 2>/dev/null)
  
  log "Redis: no usable container found"
  return 1
}

redis_container_usable() {
  local container="$1"
  docker start "$container" >/dev/null 2>&1 || return 1
  
  # Wait a moment for container to be ready
  sleep 1
  
  # Try to ping Redis
  if docker exec "$container" sh -c 'command -v redis-cli >/dev/null 2>&1 && redis-cli PING' 2>/dev/null | grep -q PONG; then
    log "Redis: container $container is usable ✓"
    return 0
  fi
  
  # If redis-cli not available, check if port 6379 is exposed (we'll assume it's usable)
  local ports
  ports=$(docker inspect -f '{{.Config.Image}} {{range .NetworkSettings.Ports}}{{.PrivatePort}} {{end}}' "$container" 2>/dev/null || true)
  if [[ "$ports" == *' 6379 '* ]]; then
    log "Redis: container $container lacks redis-cli but exposes 6379, assuming usable"
    return 0
  fi
  
  warn "Redis: container $container not usable (PING failed)"
  return 1
}

configure_postgres_container() {
  DL_DB_MODE=docker
  DL_PG_SOURCE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Source}}{{end}}{{end}}' "$DL_PG_CONTAINER" 2>/dev/null || true)
  
  local db_user="${DL_DISCOVERED_PG_USER:-llm_gateway}"
  local db_pass="${DL_DISCOVERED_PG_PASS:-}"
  local db_name="llm_gateway"
  local db_port="${DL_DISCOVERED_PG_PORT:-}"
  
  if [[ -z "$db_port" ]]; then
    db_port=$(docker port "$DL_PG_CONTAINER" 5432/tcp 2>/dev/null | sed -n 's/.*://p' | head -n1 || true)
  fi
  
  if [[ -z "$db_port" ]]; then
    warn "PostgreSQL container $DL_PG_CONTAINER has no host port for 5432; will use environment DATABASE_URL if available"
    return 0
  fi
  
  # Create database if it doesn't exist
  if [[ "${DL_DISCOVERED_PG_HAS_DB:-0}" == 0 ]]; then
    log "Creating llm_gateway database in $DL_PG_CONTAINER..."
    if [[ -n "$db_pass" ]]; then
      docker exec -e PGPASSWORD="$db_pass" "$DL_PG_CONTAINER" psql -U "$db_user" -c "CREATE DATABASE llm_gateway" 2>/dev/null || log "Database may already exist or creation failed"
      # Also create user if needed
      docker exec -e PGPASSWORD="$db_pass" "$DL_PG_CONTAINER" psql -U "$db_user" -c "CREATE USER llm_gateway WITH PASSWORD '$db_pass'" 2>/dev/null || true
      docker exec -e PGPASSWORD="$db_pass" "$DL_PG_CONTAINER" psql -U "$db_user" -c "GRANT ALL PRIVILEGES ON DATABASE llm_gateway TO llm_gateway" 2>/dev/null || true
    else
      docker exec "$DL_PG_CONTAINER" psql -U "$db_user" -c "CREATE DATABASE llm_gateway" 2>/dev/null || log "Database may already exist or creation failed"
    fi
  fi
  
  # Set connection string
  if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" && -z "${DATABASE_URL:-}" ]]; then
    if [[ -n "$db_pass" ]]; then
      export LLM_GATEWAY_PG_USER="$db_user"
      export LLM_GATEWAY_PG_PASSWORD="$db_pass"
      export LLM_GATEWAY_PG_DATABASE="$db_name"
      export LLM_GATEWAY_DATABASE_URL="postgresql://${db_user}:${db_pass}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"
      export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
    else
      export LLM_GATEWAY_DATABASE_URL="postgresql://${db_user}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"
      export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
    fi
  fi
}

configure_redis_container() {
  DL_REDIS_MODE=docker
  local redis_addr
  redis_addr=$(dl_container_env "$DL_REDIS_CONTAINER" LLM_GATEWAY_REDIS_ADDR)
  if [[ -z "$redis_addr" ]]; then
    local redis_port
    redis_port=$(docker port "$DL_REDIS_CONTAINER" 6379/tcp 2>/dev/null | sed -n 's/.*://p' | head -n1 || true)
    redis_port=${redis_port:-6379}
    if [[ "$redis_port" == "6379" && -n "${LLM_GATEWAY_REDIS_HOST_PORT:-}" ]]; then
      redis_port="$LLM_GATEWAY_REDIS_HOST_PORT"
    fi
    redis_addr="127.0.0.1:${redis_port}"
  fi
  export LLM_GATEWAY_REDIS_ADDR="$redis_addr"
}
