#!/usr/bin/env bash
# Shared local deployment primitives. This file is sourced by deploy-local.sh.
set -euo pipefail

_dl_die() { printf 'error: %s\n' "$*" >&2; return 1; }
_dl_have() { command -v "$1" >/dev/null 2>&1; }
_dl_bool() { [[ "${1:-}" == 1 || "${1:-}" == true || "${1:-}" == yes ]]; }

# Use native paths for the current shell. Git Bash/MSYS accepts /d/kaixuan,
# while the displayed contract remains D:/kaixuan.
dl_default_root() {
  local os
  os=$(uname -s 2>/dev/null || printf unknown)
  case "$os" in
    Darwin) printf '%s\n' "$HOME/kaixuan/llm-gateway-go" ;;
    Linux) printf '%s\n' "/opt/kaixuan/llm-gateway-go" ;;
    MINGW*|MSYS*|CYGWIN*)
      if [[ -d /d && -w /d ]]; then printf '%s\n' '/d/kaixuan/llm-gateway-go';
      elif [[ -d /c && -w /c ]]; then printf '%s\n' '/c/kaixuan/llm-gateway-go';
      else printf '%s\n' "$HOME/kaixuan"; fi
      ;;
    *) printf '%s\n' "$HOME/kaixuan/llm-gateway-go" ;;
  esac
}

# The unified deploy-local entry point intentionally ignores the legacy
# INSTALL_ROOT variable, whose historical default was under ~/Downloads. Use
# only the explicit new override or the OS-specific default.
dl_root() { printf '%s\n' "${LLM_GATEWAY_ROOT:-$(dl_default_root)}"; }
dl_bin_dir() { printf '%s\n' "$(dl_root)/bin"; }
dl_run_dir() { printf '%s\n' "$(dl_root)/run"; }

# Shared service root (multi-project). The PostgreSQL and Redis data live here
# so multiple project installs on this host can reuse them. Override only when
# the host layout intentionally differs.
dl_default_shared_root() {
  local os
  os=$(uname -s 2>/dev/null || printf unknown)
  case "$os" in
    Darwin) printf '%s\n' "$HOME/kaixuan" ;;
    Linux) printf '%s\n' "/opt/kaixuan" ;;
    MINGW*|MSYS*|CYGWIN*)
      if [[ -d /d && -w /d ]]; then printf '%s\n' '/d/kaixuan';
      elif [[ -d /c && -w /c ]]; then printf '%s\n' '/c/kaixuan';
      else printf '%s\n' "$HOME/kaixuan"; fi
      ;;
    *) printf '%s\n' "$HOME/kaixuan" ;;
  esac
}
dl_shared_root() { printf '%s\n' "${KAIXUAN_ROOT:-$(dl_default_shared_root)}"; }
dl_shared_pg_dir() { printf '%s\n' "$(dl_shared_root)/postgres"; }
dl_shared_redis_dir() { printf '%s\n' "$(dl_shared_root)/redis"; }
dl_pg_log_dir() { printf '%s\n' "$(dl_shared_pg_dir)/logs"; }
dl_pg_backup_dir() { printf '%s\n' "$(dl_shared_pg_dir)/backups"; }
dl_pg_run_dir() { printf '%s\n' "$(dl_shared_pg_dir)/run"; }
dl_redis_log_dir() { printf '%s\n' "$(dl_shared_redis_dir)/logs"; }
dl_redis_run_dir() { printf '%s\n' "$(dl_shared_redis_dir)/run"; }

dl_active_version() { local p; p="$(dl_bin_dir)/current"; [[ -L "$p" ]] && basename "$(readlink "$p")" || true; }
dl_layout() {
  local r; r=$(dl_root)
  printf 'root=%s\nattachments=%s/attachments\nbin=%s/bin\nbackups=%s/backups\nlogs=%s/logs\nraw_logs=%s/raw-logs\nrun=%s/run\n' "$r" "$r" "$r" "$r" "$r" "$r" "$r"
  printf 'shared_root=%s\nshared_pg_dir=%s\nshared_pg_logs=%s\nshared_pg_backups=%s\nshared_redis_dir=%s\nshared_redis_logs=%s\n' \
    "$(dl_shared_root)" "$(dl_shared_pg_dir)" "$(dl_pg_log_dir)" "$(dl_pg_backup_dir)" "$(dl_shared_redis_dir)" "$(dl_redis_log_dir)"
}

dl_prepare_layout() {
  # need_pg/need_redis are retained for backwards compatibility with callers
  # that toggle a local Docker dependency. Service data lives under the shared
  # root now, so the project directory no longer creates postgres/redis.
  local need_pg=${1:-0} need_redis=${2:-0} r; r=$(dl_root)
  mkdir -p "$r/attachments" "$r/bin" "$r/backups" "$r/logs" "$r/raw-logs" "$r/run"
  if _dl_bool "$need_pg" || _dl_bool "$need_redis"; then :; fi
}

dl_prepare_shared_service_dirs() {
  # Always create service-owned subdirectories so logs and backups have a
  # deterministic location regardless of who owns the underlying data.
  local pg; pg=$(dl_shared_pg_dir)
  local redis; redis=$(dl_shared_redis_dir)
  mkdir -p "$pg/logs" "$pg/backups" "$pg/run"
  mkdir -p "$redis/logs" "$redis/run"
}

# Archive and optionally remove the obsolete ~/Downloads/llm-gateway-files
# tree. Off by default; callers must set DL_CLEANUP_DOWNLOADS=1.
dl_cleanup_legacy_downloads() {
  [[ "${DL_CLEANUP_DOWNLOADS:-0}" == 1 ]] || { printf '[deploy-local] --cleanup-downloads not set; legacy Downloads copies left in place\n'; return 0; }
  local legacy="$HOME/Downloads/llm-gateway-files" archive
  [[ -d "$legacy" ]] || { printf '[deploy-local] no legacy Downloads copy at %s\n' "$legacy"; return 0; }
  dl_prepare_shared_service_dirs
  archive="$(dl_pg_backup_dir)/downloads-legacy-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
  printf '[deploy-local] archiving legacy Downloads copy to %s\n' "$archive"
  mkdir -p "$(dl_pg_backup_dir)"
  tar -C "$legacy" -czf "$archive" . || printf '[deploy-local] warning: failed to archive legacy Downloads copy\n'
  chmod 0600 "$archive"
  local sub
  for sub in postgres redis bin; do
    [[ -d "${legacy:?}/$sub" ]] || continue
    rm -rf "${legacy:?}/$sub" && printf '[deploy-local] removed %s/%s\n' "$legacy" "$sub"
  done
  printf '[deploy-local] legacy Downloads cleanup completed; review %s before deleting\n' "$archive"
}

# Detect without stopping or recreating anything. Results are exported for the
# caller and deliberately distinguish an existing service from a new local one.
dl_detect_resources() {
  DL_DOCKER=0; DL_COMPOSE=0; DL_PG_CONTAINER=; DL_REDIS_CONTAINER=
  DL_PG_SOURCE=; DL_REDIS_SOURCE=; DL_PG_MOUNT_TYPE=; DL_REDIS_MOUNT_TYPE=
  DL_DB_MODE=none; DL_REDIS_MODE=none
  if _dl_have docker && docker info >/dev/null 2>&1; then
    DL_DOCKER=1
    docker compose version >/dev/null 2>&1 && DL_COMPOSE=1 || true
    for c in llm-gateway-pg postgres kx-citus; do
      if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then DL_PG_CONTAINER=$c; break; fi
    done
    # Three-level Redis discovery: named → docker-scan → host ss+redis-cli.
    # The first level that yields a healthy endpoint short-circuits the rest.
    if dl_redis_try_named; then
      printf '[deploy-local] redis-discover: named → %s\n' "$DL_REDIS_CONTAINER" >&2
    elif dl_redis_try_scan; then
      printf '[deploy-local] redis-discover: scan → %s\n' "$DL_REDIS_CONTAINER" >&2
    fi
    if [[ -n "$DL_PG_CONTAINER" ]]; then
      DL_DB_MODE=docker
      DL_PG_SOURCE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Source}}{{end}}{{end}}' "$DL_PG_CONTAINER" 2>/dev/null || true)
      DL_PG_MOUNT_TYPE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Type}}{{end}}{{end}}' "$DL_PG_CONTAINER" 2>/dev/null || true)
    fi
    if [[ -n "$DL_REDIS_CONTAINER" ]]; then
      DL_REDIS_MODE=docker
      DL_REDIS_SOURCE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$DL_REDIS_CONTAINER" 2>/dev/null || true)
      DL_REDIS_MOUNT_TYPE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}}{{end}}{{end}}' "$DL_REDIS_CONTAINER" 2>/dev/null || true)
    fi
  fi
  if [[ "$DL_DB_MODE" == none && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then DL_DB_MODE=external; fi
  if [[ "$DL_REDIS_MODE" == none && -n "${LLM_GATEWAY_REDIS_ADDR:-}" ]]; then DL_REDIS_MODE=external; fi
  if [[ "$DL_REDIS_MODE" == none ]] && dl_redis_try_system; then
    printf '[deploy-local] redis-discover: system → %s\n' "$LLM_GATEWAY_REDIS_ADDR" >&2
  fi
  if [[ "$DL_DB_MODE" == none ]] && { _dl_have pg_isready || _dl_have psql; }; then
    if _dl_have pg_isready && pg_isready -h 127.0.0.1 -p "${PGPORT:-5432}" >/dev/null 2>&1; then DL_DB_MODE=system; fi
  fi
  export DL_DOCKER DL_COMPOSE DL_PG_CONTAINER DL_REDIS_CONTAINER DL_PG_SOURCE DL_REDIS_SOURCE DL_PG_MOUNT_TYPE DL_REDIS_MOUNT_TYPE DL_DB_MODE DL_REDIS_MODE
}

# First-pass Redis discovery: hard-coded list of names the project or its
# siblings commonly use. Returns 0 and sets DL_REDIS_CONTAINER on success.
dl_redis_try_named() {
  local c
  for c in nbjl-redis llm-gateway-redis redis kx-redis; do
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then
      if dl_redis_container_ping "$c"; then
        DL_REDIS_CONTAINER=$c
        return 0
      fi
    fi
  done
  return 1
}

# Second-pass Redis discovery: scan running containers, look for images
# whose name looks like Redis / Valkey / cache, or ports that publish
# 6379/tcp. Validate with redis-cli PING inside the container before
# accepting the candidate.
dl_redis_try_scan() {
  local line name image ports
  while IFS=$'\t' read -r name image ports; do
    [[ -z "$name" ]] && continue
    [[ "$name" == "$DL_REDIS_CONTAINER" ]] && continue
    if [[ "$image" =~ (^|[/_-])(redis|valkey|cache)([/_-]|$) || "$ports" == *6379/tcp* ]]; then
      if dl_redis_container_ping "$name"; then
        DL_REDIS_CONTAINER=$name
        return 0
      fi
    fi
  done < <(docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}' 2>/dev/null)
  return 1
}

# Third-pass Redis discovery: probe host listeners on the canonical ports
# (6379 / 16379) using ss (preferred) or netstat, and confirm with
# redis-cli PING. Sets LLM_GATEWAY_REDIS_ADDR and DL_REDIS_MODE on success.
dl_redis_try_system() {
  _dl_have redis-cli || return 1
  local port
  while read -r port; do
    [[ -z "$port" ]] && continue
    if redis-cli -h 127.0.0.1 -p "$port" ping >/dev/null 2>&1; then
      export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:${port}"
      DL_REDIS_MODE=system
      return 0
    fi
  done < <(dl_redis_host_listen_ports)
  return 1
}

# Probe a running Redis-shaped container with a non-destructive PING. The
# redis-cli binary is optional in alpine-based images; we tolerate its
# absence and still accept the container when the inspect result exposes
# a 6379 port, because future restart will be configured via the bind.
dl_redis_container_ping() {
  local name=$1
  docker exec "$name" sh -c 'command -v redis-cli >/dev/null 2>&1 && redis-cli PING' 2>/dev/null | grep -q PONG && return 0
  local ports; ports=$(docker inspect -f '{{.Config.Image}} {{range .NetworkSettings.Ports}}{{.PrivatePort}} {{end}}' "$name" 2>/dev/null || true)
  if [[ "$ports" == *' 6379 '* ]]; then
    printf '[deploy-local] redis-discover: %s lacks redis-cli but exposes 6379; trusting image/ports\n' "$name" >&2
    return 0
  fi
  return 1
}

# Enumerate candidate host ports for the system Redis fallback. Prefers
# `ss -ltn` (Linux) and falls back to `netstat -an` (macOS / older systems).
dl_redis_host_listen_ports() {
  if _dl_have ss; then
    ss -ltn 2>/dev/null | awk '
      tolower($0) ~ /:(6379|16379)[^0-9]/ {
        n = split($4, parts, ":"); print parts[n]
      }'
    return
  fi
  if _dl_have netstat; then
    netstat -an 2>/dev/null | awk '
      tolower($0) ~ /\.6379[^0-9]/ || tolower($0) ~ /\.16379[^0-9]/ {print "6379"}
    ' | sort -u
  fi
}

# A pre-existing container is never moved. If the detected bind mount lives
# outside the shared service directory, ensure the shared path resolves to
# that mount via a symlink. The shared service directories are multi-project
# roots, so linking is idempotent and never replaces existing entries.
dl_link_existing_data() {
  local pg_dir; pg_dir=$(dl_shared_pg_dir)
  local redis_dir; redis_dir=$(dl_shared_redis_dir)
  mkdir -p "$pg_dir" "$redis_dir"
  if [[ "${DL_DB_MODE:-}" == docker && -n "${DL_PG_SOURCE:-}" ]]; then
    if [[ ! -e "$pg_dir" && ! -L "$pg_dir" ]]; then
      ln -s "$DL_PG_SOURCE" "$pg_dir"
    fi
  fi
  if [[ "${DL_REDIS_MODE:-}" == docker && -n "${DL_REDIS_SOURCE:-}" ]]; then
    if [[ ! -e "$redis_dir" && ! -L "$redis_dir" ]]; then
      ln -s "$DL_REDIS_SOURCE" "$redis_dir"
    fi
  fi
}

dl_version_fields() {
  local f="${1:-$(dirname "$SCRIPT_DIR")/version.json}"
  [[ -f "$f" ]] || _dl_die "version file missing: $f"
  python3 - "$f" <<'PY'
import json, sys
x=json.load(open(sys.argv[1]))
for k in ('git_tag','git_sha','build_seq','build_date','version'):
    print(f'{k}={x.get(k, "")}')
PY
}

dl_release_name() {
  local f="$1" tag seq
  # 消费端是 RELEASE_VERSION="$(dl_release_name ...)" 赋值语境：函数内失败
  # 命令不触发 set -e（2026-09-05 陈旧二进制事故的 bash 怪癖），若静默继续
  # 到末尾 printf 会产出 ".0" 这类残缺版本号。此处把失败转成函数非零返回，
  # 让赋值语境的 set -e 在消费端触发。
  tag=$(python3 -c 'import json,sys; x=json.load(open(sys.argv[1])); print(x.get("git_tag") or x.get("version") or "dev")' "$f") || return 1
  seq=$(python3 -c 'import json,sys; x=json.load(open(sys.argv[1])); print(x.get("build_seq",0))' "$f") || return 1
  if [[ -z "$seq" ]]; then seq=0; fi
  [[ -n "$tag" && "$seq" =~ ^[0-9]+$ ]] || return 1
  printf '%s.%s\n' "$tag" "$seq"
}

dl_container_env() {
  local container="$1" key="$2"
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" 2>/dev/null \
    | sed -n "s/^${key}=//p" | head -n1
}

dl_sha256() {
  if _dl_have sha256sum; then sha256sum "$@"; elif _dl_have shasum; then shasum -a 256 "$@"; else _dl_die 'sha256sum or shasum is required'; fi
}

# Import .env.local keys that the calling environment left unset or empty.
# Caller-provided values stay authoritative (CI and production wrappers
# inject their own DSN), but gating the whole file on DATABASE_URL — the
# behavior this replaces — also dropped LLM_GATEWAY_SECRET_KEY whenever a
# deploy ran from a shell that had only the DSN exported: dl_write_env then
# emitted an empty key, the gateway started unable to sign admin sessions,
# every login returned "token generation failed", and previously issued
# tokens stopped verifying (incident 2026-09-05: every local deploy launched
# with DATABASE_URL preset). An empty caller value is treated as absent so
# exported-but-blank defaults still pick up the project configuration.
# Multi-line quoted values (PEM blocks in .env.local) survive because the
# sourcing subshell hands its environment over as NUL-delimited `env -0`
# records instead of line-parsed text.
dl_load_project_env() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    # Silent skip is how a deploy from a clean checkout (no gitignored
    # .env.local) came up with an empty LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY
    # and every stored credential undecryptable (incident 2026-09-05,
    # provider 587). Make the absence visible; deploy() gates the fatal case.
    printf '[deploy-lib] warning: %s not found; keys/DSN must all come from the calling environment\n' "$file" >&2
    return 0
  fi
  local kv key val
  while IFS= read -r -d '' kv; do
    key="${kv%%=*}"
    case "$key" in ''|*[!A-Za-z0-9_]*) continue ;; esac
    val="$(printenv "$key" || true)"
    if [[ -z "$val" ]]; then
      export "$key=${kv#*=}"
    fi
  done < <(
    # .env.local prints a friendly summary when sourced; suppress it so
    # deploy diagnostics stay redacted and the env dump stays clean.
    # shellcheck disable=SC1090
    { source "$file" >/dev/null 2>&1; env -0; }
  )
}

dl_write_env() {
  # docker --env-file consumes the file as KEY=value pairs without any shell
  # parsing. We avoid both %q-style backslash escapes and outer single
  # quotes because Docker does not strip them; values are written verbatim
  # unless they contain whitespace, in which case they are rejected because
  # DSNs and secret keys must not contain whitespace.
  local file="$1" port="$2"; umask 077
  local version_file log_file log_dir raw_log_dir attachment_dir backup_dir persistent_log_dir
  if [[ "${DL_DOCKER:-0}" == 1 ]]; then
    version_file=/opt/llm-gateway-go/version.json
    log_file=/opt/llm-gateway-go/logs/gateway.log
    log_dir=/opt/llm-gateway-go/logs
    raw_log_dir=/opt/llm-gateway-go/raw-logs
    attachment_dir=/opt/llm-gateway-go/attachments
    backup_dir=/opt/llm-gateway-go/backups
    persistent_log_dir=/opt/llm-gateway-go/logs
  else
    version_file="${LLM_GATEWAY_VERSION_FILE:-}"
    log_file="$(dl_root)/logs/gateway-${port}.log"
    log_dir="$(dl_root)/logs"
    raw_log_dir="$(dl_root)/raw-logs"
    attachment_dir="$(dl_root)/attachments"
    backup_dir="$(dl_root)/backups"
    persistent_log_dir="$log_dir"
  fi
  {
    printf 'LLM_GATEWAY_LISTEN=:%s\n' "$port"
    dl_emit_env_line LLM_GATEWAY_CORS_ORIGINS "${LLM_GATEWAY_CORS_ORIGINS:-http://127.0.0.1:${port}}"
    dl_emit_env_line LLM_GATEWAY_VERSION_FILE "$version_file"
    dl_emit_env_line LLM_GATEWAY_LOG_FILE "$log_file"
    dl_emit_env_line LLM_GATEWAY_LOG_DIR "$log_dir"
    dl_emit_env_line LLM_GATEWAY_RAW_LOG_DIR "$raw_log_dir"
    dl_emit_env_line LLM_GATEWAY_ATTACHMENT_DIR "$attachment_dir"
    dl_emit_env_line LLM_GATEWAY_BACKUP_DIR "$backup_dir"
    dl_emit_env_line LLM_GATEWAY_PERSISTENT_LOG_DIR "$persistent_log_dir"
    dl_emit_env_line LLM_GATEWAY_DATABASE_URL "${LLM_GATEWAY_DATABASE_URL:-}"
    dl_emit_env_line DATABASE_URL "${DATABASE_URL:-${LLM_GATEWAY_DATABASE_URL:-}}"
    dl_emit_env_line LLM_GATEWAY_PG_DATA_DIR "$(dl_shared_pg_dir)"
    dl_emit_env_line LLM_GATEWAY_REDIS_ADDR "${LLM_GATEWAY_REDIS_ADDR:-}"
    dl_emit_env_line LLM_GATEWAY_REDIS_PASSWORD "${LLM_GATEWAY_REDIS_PASSWORD:-}"
    dl_emit_env_line LLM_GATEWAY_REDIS_DB "${LLM_GATEWAY_REDIS_DB:-2}"
    dl_emit_env_line LLM_GATEWAY_REDIS_DATA_DIR "$(dl_shared_redis_dir)"
    dl_emit_env_line LLM_GATEWAY_SECRET_KEY "${LLM_GATEWAY_SECRET_KEY:-}"
    dl_emit_env_line LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY "${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}"
    dl_emit_env_line LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID "${LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID:-}"
    dl_emit_env_line LLM_GATEWAY_API_KEY "${LLM_GATEWAY_API_KEY:-}"
    dl_emit_env_line LLM_GATEWAY_ADMIN_API_KEY "${LLM_GATEWAY_ADMIN_API_KEY:-}"
    dl_emit_env_line LLM_GATEWAY_ADMIN_USER "${LLM_GATEWAY_ADMIN_USER:-}"
    dl_emit_env_line LLM_GATEWAY_ADMIN_PASSWORD "${LLM_GATEWAY_ADMIN_PASSWORD:-}"
    # Local gateways run URSM authoritative mode and the runtime requires the
    # system monitor opt-in; surface this for both Docker and native modes.
    dl_emit_env_line LLM_GATEWAY_SYSTEM_MONITOR_ENABLED "${LLM_GATEWAY_SYSTEM_MONITOR_ENABLED:-true}"
    dl_emit_env_line URSM_V2_MODE "${URSM_V2_MODE:-authoritative}"
    # CORS is fail-closed in the gateway (middleware.NewCORSMiddleware panics
    # on an empty list). Honor an explicit LLM_GATEWAY_CORS_ORIGINS (e.g.
    # sourced from .env.local) and otherwise default to the loopback origin of
    # the actual listen port, which is safe for the 127.0.0.1-bound local
    # deployment and keeps the co-served web UI same-origin.
    dl_emit_env_line LLM_GATEWAY_CORS_ORIGINS "${LLM_GATEWAY_CORS_ORIGINS:-http://127.0.0.1:${port},http://localhost:${port}}"
  } > "$file"
  chmod 0600 "$file"
}

# Emit a KEY=value line that docker --env-file can read without surprises.
# Whitespace and newlines are rejected: docker would treat them as separate
# lines, so a malformed env file is preferable to silently corrupted values.
dl_emit_env_line() {
  local key="$1" value="${2-}"
  if [[ "$value" == *$'\n'* ]]; then
    printf 'error: %s contains a newline; refusing to write env file\n' "$key" >&2
    return 1
  fi
  if [[ -z "$value" ]]; then
    printf '%s=\n' "$key"
  elif [[ "$value" == *[[:space:]]* ]]; then
    printf 'error: %s contains whitespace; refusing to write env file\n' "$key" >&2
    return 1
  else
    printf '%s=%s\n' "$key" "$value"
  fi
}

dl_stage_release() {
  local bundle="$1" binary="$2" web="$3" version_json="$4" version_file="$5" name="$6"
  # 本函数经 deploy-local.sh 的 var=$(stage_release ...) 赋值语境调用：
  # 该语境下 bash 不因函数内失败命令中断（2026-09-05 陈旧二进制事故的
  # set -e 怪癖），必须逐项显式检查。SHA256SUMS 放在最后生成——任一关键
  # 步骤失败都 return 1 且不留 SHA256SUMS，保证消费端 dl_verify_release
  # （|| die）对残缺 bundle 必然 fail-closed。
  mkdir -p "$bundle" || return 1
  install -m 0755 "$binary" "$bundle/gateway" || return 1
  [[ -s "$bundle/gateway" ]] || return 1
  if [[ -d "$web" ]]; then
    cp -R "$web" "$bundle/web" || return 1
  else
    mkdir -p "$bundle/web" || return 1
  fi
  cp "$version_json" "$bundle/version.json" || return 1
  cp "$version_file" "$bundle/VERSION" 2>/dev/null || true
  python3 - "$bundle/deployment.json" "$name" <<'PY' || return 1
import json,sys,datetime
json.dump({'target':'local','version':sys.argv[2],'verified':False,'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat()},open(sys.argv[1],'w'),indent=2); open(sys.argv[1],'a').write('\n')
PY
  (cd "$bundle"; find . -type f ! -name SHA256SUMS ! -name deployment.json -print0 | sort -z | while IFS= read -r -d '' f; do dl_sha256 "$f"; done) > "$bundle/SHA256SUMS" || { rm -f "$bundle/SHA256SUMS"; return 1; }
  [[ -s "$bundle/SHA256SUMS" ]] || { rm -f "$bundle/SHA256SUMS"; return 1; }
}

dl_verify_release() {
  local b="$1" line sum path actual
  (cd "$b" && while IFS= read -r line; do
    sum=${line%% *}
    path=${line#*  }
    [[ -f "$path" ]] || return 1
    actual=$(dl_sha256 "$path" | cut -d ' ' -f1)
    [[ "$actual" == "$sum" ]] || return 1
  done < SHA256SUMS)
}
dl_mark_verified() { python3 - "$1/deployment.json" <<'PY'
import json,sys,datetime
p=sys.argv[1]; x=json.load(open(p)); x['verified']=True; x['verified_at']=datetime.datetime.now(datetime.timezone.utc).isoformat(); json.dump(x,open(p,'w'),indent=2); open(p,'a').write('\n')
PY
}

dl_atomic_switch() {
  local name="$1" r; r=$(dl_root)
  ln -sfn "$name" "$r/bin/current"
  for link in gateway web version.json VERSION env; do
    [[ -e "$r/bin/current/$link" ]] || continue
    ln -sfn "current/$link" "$r/bin/$link"
  done
  printf '%s\n' "$name" > "$r/run/active-version"
  chmod 0600 "$r/run/active-version"
}
# Resolve the active listen port with a predictable priority chain. The
# project root is consulted first because the runtime env was generated
# against that root; env overrides win, then a per-project .env.local, and
# finally 8782 (the post-2026 port; the legacy default was 8781).
dl_active_port() {
  local f candidate
  f="$(dl_run_dir)/active-port"
  if [[ -s "$f" ]]; then cat "$f"; return; fi
  candidate="${LLM_GATEWAY_ACTIVE_PORT:-${SERVICE_PORT:-}}"
  if [[ -z "$candidate" ]]; then
    local env_root
    env_root=${DL_ROOT_DIR-$(dl_root)}
    if [[ -f "$env_root/.env.local" ]]; then
      # .env.local uses shell-style `export FOO=bar` lines; extract the first
      # assignment for SERVICE_PORT or LLM_GATEWAY_ACTIVE_PORT without sourcing
      # the file (which would re-export unrelated secrets).
      candidate=$(grep -E '^[[:space:]]*(export[[:space:]]+)?(SERVICE_PORT|LLM_GATEWAY_ACTIVE_PORT)=' \
        "$env_root/.env.local" 2>/dev/null \
        | tail -n1 | sed -E 's/^[[:space:]]*(export[[:space:]]+)?[^=]+=//' || true)
      # Strip leading/trailing single or double quotes the file may wrap around
      # the value. We avoid shell-level quote handling so the value can contain
      # any characters needed for a port.
      case $candidate in
        \"*\") candidate=${candidate#\"}; candidate=${candidate%\"} ;;
        \'*\') candidate=${candidate#\'}; candidate=${candidate%\'} ;;
      esac
    fi
  fi
  if [[ -z "$candidate" || "$candidate" == \'* || "$candidate" == \"* ]]; then
    candidate=8782
  fi
  printf '%s\n' "$candidate"
}
dl_candidate_port() { local cur; cur=$(dl_active_port); [[ "$cur" == 8782 ]] && printf 8781 || printf 8782; }
dl_wait_http() {
  local url="$1" deadline=$(( $(date +%s)+${2:-60} ))
  while (( $(date +%s) < deadline )); do curl -fsS --max-time 2 "$url" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
dl_record_verify() {
  local root="$1" db="$2" redis="$3" version="$4" port="$5"
  printf 'VERIFY_TOOL=deploy-local.sh\nVERIFY_DEVICE=local\nVERIFY_PASS=1\nVERIFY_ROOT=%s\nVERIFY_RELEASE=%s\nVERIFY_PORT=%s\nVERIFY_DATABASE=%s\nVERIFY_REDIS=%s\n' "$root" "$version" "$port" "$db" "$redis"
}
