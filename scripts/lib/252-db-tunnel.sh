#!/usr/bin/env bash
# Shared, ownership-safe tunnel management for the 252 PostgreSQL container.
# Callers must load configs/env-252.sh before calling these functions.

# The local tunnel created by this process, if any. Consumers should call
# db252_tunnel_teardown from their EXIT trap.
DB252_TUNNEL_PID=""
DB252_TUNNEL_CREATED=false

_db252_log() {
  printf '[252-db-tunnel] %s\n' "$*" >&2
}

_db252_port_listener_pids() {
  lsof -tiTCP:"$TUNNEL_LOCAL_PORT" -sTCP:LISTEN 2>/dev/null || true
}

_db252_psql() {
  if [[ ! -x "$PG_PSQL_BIN" ]]; then
    _db252_log "native psql client not found: $PG_PSQL_BIN (install Homebrew libpq or set PG_PSQL_BIN)"
    return 1
  fi
  PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -X -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" \
    -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAc 'SELECT 1' >/dev/null 2>&1
}

# Resolves the currently running Podman/Docker container address on 252.
# A container CNI address is intentionally not persisted in configuration.
db252_resolve_remote_target() {
  local ips ip
  # The Go-template braces are literal input for the remote docker-compatible
  # runtime; the configured container name is expanded locally.
  # shellcheck disable=SC2029
  ips=$(ssh "$SSH_TARGET" "docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{println}}{{end}}' '$REMOTE_PG_CONTAINER'" 2>/dev/null) || {
    _db252_log "cannot inspect remote container '$REMOTE_PG_CONTAINER' through SSH target '$SSH_TARGET'"
    return 1
  }

  ips=$(printf '%s\n' "$ips" | sed '/^[[:space:]]*$/d' | sort -u)
  if [[ $(printf '%s\n' "$ips" | wc -l | tr -d ' ') -ne 1 ]]; then
    _db252_log "expected exactly one IPv4 address for '$REMOTE_PG_CONTAINER', got: ${ips:-none}"
    return 1
  fi

  ip=$(printf '%s\n' "$ips")
  if [[ ! "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    _db252_log "container '$REMOTE_PG_CONTAINER' returned an invalid IPv4 address: $ip"
    return 1
  fi
  IFS=. read -r a b c d <<< "$ip"
  if ((a > 255 || b > 255 || c > 255 || d > 255)); then
    _db252_log "container '$REMOTE_PG_CONTAINER' returned an invalid IPv4 address: $ip"
    return 1
  fi

  printf '%s:%s\n' "$ip" "$REMOTE_PG_PORT"
}

# Opens a tunnel only when no listener is present. A listener that does not
# authenticate to the expected database is never replaced or killed.
db252_tunnel_ensure() {
  local existing_pids remote_target new_pids
  if _db252_psql; then
    _db252_log "reusing healthy tunnel on 127.0.0.1:$TUNNEL_LOCAL_PORT"
    return 0
  fi

  existing_pids=$(_db252_port_listener_pids)
  if [[ -n "$existing_pids" ]]; then
    _db252_log "127.0.0.1:$TUNNEL_LOCAL_PORT is occupied but does not reach the expected 252 database; refusing to replace another process's listener (PIDs: $existing_pids)"
    return 1
  fi

  remote_target=$(db252_resolve_remote_target) || return 1
  _db252_log "opening tunnel 127.0.0.1:$TUNNEL_LOCAL_PORT -> $remote_target via $SSH_TARGET"
  if ! ssh -f -N -L "127.0.0.1:$TUNNEL_LOCAL_PORT:$remote_target" "$SSH_TARGET" \
    -o ExitOnForwardFailure=yes; then
    _db252_log "SSH failed to open the database tunnel"
    return 1
  fi

  sleep "${DB252_TUNNEL_WAIT_SECONDS:-2}"
  new_pids=$(_db252_port_listener_pids)
  if [[ $(printf '%s\n' "$new_pids" | sed '/^$/d' | wc -l | tr -d ' ') -ne 1 ]]; then
    _db252_log "tunnel start did not yield exactly one listener on 127.0.0.1:$TUNNEL_LOCAL_PORT"
    # Do not leave forwarding processes behind when SSH succeeded but the
    # listener ownership/shape check failed.
    while IFS= read -r pid; do
      [[ -z "$pid" ]] && continue
      kill "$pid" 2>/dev/null || true
    done < <(printf '%s\n' "$new_pids")
    return 1
  fi
  DB252_TUNNEL_PID="$new_pids"
  DB252_TUNNEL_CREATED=true

  if ! _db252_psql; then
    _db252_log "tunnel opened but PostgreSQL authentication/connectivity failed"
    db252_tunnel_teardown
    return 1
  fi
  _db252_log "tunnel ready (PID $DB252_TUNNEL_PID)"
}

db252_tunnel_teardown() {
  if [[ "$DB252_TUNNEL_CREATED" == true && -n "$DB252_TUNNEL_PID" ]]; then
    if kill -0 "$DB252_TUNNEL_PID" 2>/dev/null; then
      kill "$DB252_TUNNEL_PID" 2>/dev/null || true
      _db252_log "closed tunnel created by this process (PID $DB252_TUNNEL_PID)"
    fi
  fi
  DB252_TUNNEL_PID=""
  DB252_TUNNEL_CREATED=false
}
