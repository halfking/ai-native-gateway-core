#!/usr/bin/env bash
# Shared local deployment primitives. This file is sourced by deploy-local.sh.
set -euo pipefail

_dl_die() { printf 'error: %s\n' "$*" >&2; return 1; }
_dl_have() { command -v "$1" >/dev/null 2>&1; }
_dl_bool() { [[ "${1:-}" == 1 || "${1:-}" == true || "${1:-}" == yes ]]; }

# Fail-closed guard for dl_wait_pg_isready (DL_PG_PREFLIGHT_REQUIRED=1) and
# any other lib primitive that must TERMINATE the deploy, not merely return
# nonzero — a bare return would fall through `|| true` callers and fail open.
# deploy-local.sh defines its own branded die() after sourcing this file and
# overrides this fallback; the fallback exists so sourcing the lib alone
# (tests, upgrade/seamless tooling) never degrades `die` into
# "command not found" (exit 127) where fail-closed only holds by accident.
if ! declare -F die >/dev/null 2>&1; then
  die() { printf 'error: %s\n' "$*" >&2; exit 1; }
fi

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
  # R37: removed a no-op `if _dl_bool "$need_pg" || _dl_bool "$need_redis"; then :; fi`
  # left over from the shared-root migration (params kept for caller compat).
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
  # 2026-09-18: docker info 偶发卡住（macOS Docker Desktop 繁忙时可能超时），
  # 导致 deploy-local.sh status 15s+ 挂死。加 timeout 5s 防护，超时时跳过
  # Docker 路径但不失败（外部 DSN/Redis 模式仍可用）。
  if _dl_have docker && timeout 5 docker info >/dev/null 2>&1; then
    DL_DOCKER=1
    timeout 3 docker compose version >/dev/null 2>&1 && DL_COMPOSE=1 || true
    for c in llm-gateway-pg postgres kx-citus; do
      if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then DL_PG_CONTAINER=$c; break; fi
    done
    # Skip Redis detection in minimal mode
    if [[ "${MINIMAL_DEPLOY:-0}" == 0 ]]; then
      # Three-level Redis discovery: named → docker-scan → host ss+redis-cli.
      # The first level that yields a healthy endpoint short-circuits the rest.
      if dl_redis_try_named; then
        printf '[deploy-local] redis-discover: named → %s\n' "$DL_REDIS_CONTAINER" >&2
      elif dl_redis_try_scan; then
        printf '[deploy-local] redis-discover: scan → %s\n' "$DL_REDIS_CONTAINER" >&2
      fi
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
  if [[ "${MINIMAL_DEPLOY:-0}" == 1 ]]; then
    DL_REDIS_MODE=minimal
  elif [[ "$DL_REDIS_MODE" == none && -n "${LLM_GATEWAY_REDIS_ADDR:-}" ]]; then
    DL_REDIS_MODE=external
  elif [[ "$DL_REDIS_MODE" == none ]] && dl_redis_try_system; then
    printf '[deploy-local] redis-discover: system → %s\n' "$LLM_GATEWAY_REDIS_ADDR" >&2
  fi
  if [[ "$DL_DB_MODE" == none ]] && { _dl_have pg_isready || _dl_have psql; }; then
    if _dl_have pg_isready && pg_isready -h 127.0.0.1 -p "${PGPORT:-5432}" >/dev/null 2>&1; then DL_DB_MODE=system; fi
  fi
  export DL_DOCKER DL_COMPOSE DL_PG_CONTAINER DL_REDIS_CONTAINER DL_PG_SOURCE DL_REDIS_SOURCE DL_PG_MOUNT_TYPE DL_REDIS_MOUNT_TYPE DL_DB_MODE DL_REDIS_MODE
}

# First-pass Redis discovery: check environment variable first, then fall back
# to hard-coded list of names the project or its siblings commonly use.
# Returns 0 and sets DL_REDIS_CONTAINER on success.
dl_redis_try_named() {
  local c
  # Check if user specified a Redis container name via environment variable
  if [[ -n "${LLM_GATEWAY_REDIS_CONTAINER:-}" ]]; then
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$LLM_GATEWAY_REDIS_CONTAINER"; then
      if dl_redis_container_ping "$LLM_GATEWAY_REDIS_CONTAINER"; then
        DL_REDIS_CONTAINER=$LLM_GATEWAY_REDIS_CONTAINER
        return 0
      fi
    fi
  fi
  # Fall back to common names
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
    # set -a is load-bearing: the file uses plain KEY=VALUE dotenv
    # assignments (no `export`), which land as shell-only variables that
    # `env -0` never sees. Without it the loader imports zero keys and
    # deploy() dies on the empty SECRET_KEY gate (incident 2026-09-07,
    # "LLM_GATEWAY_SECRET_KEY is empty" on an otherwise valid .env.local).
    # shellcheck disable=SC1090
    { set -a; source "$file" >/dev/null 2>&1; set +a; env -0; }
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
    # R37: the CORS default was previously emitted twice with DIFFERENT
    # defaults (this one without localhost, the later one with) — docker
    # --env-file takes the last assignment, silently masking the divergence.
    # The later, more permissive default is the single source now.
    dl_emit_env_line LLM_GATEWAY_VERSION_FILE "$version_file"
    dl_emit_env_line LLM_GATEWAY_LOG_FILE "$log_file"
    dl_emit_env_line LLM_GATEWAY_LOG_DIR "$log_dir"
    dl_emit_env_line LLM_GATEWAY_RAW_LOG_DIR "$raw_log_dir"
    dl_emit_env_line LLM_GATEWAY_ATTACHMENT_DIR "$attachment_dir"
    dl_emit_env_line LLM_GATEWAY_BACKUP_DIR "$backup_dir"
    dl_emit_env_line LLM_GATEWAY_PERSISTENT_LOG_DIR "$persistent_log_dir"
    dl_emit_env_line LLM_GATEWAY_DATABASE_URL "${LLM_GATEWAY_DATABASE_URL:-}"
    dl_emit_env_line DATABASE_URL "${DATABASE_URL:-${LLM_GATEWAY_DATABASE_URL:-}}"
    # 2026-09-17：local 部署 2114 cutover 复盘 — gateway 内部
    # openDBWithBootRetry 默认 20s 预算不够覆盖 PG 在上一容器停服后的
    # 恢复窗口（cat /readyz 期间反复落 database:null）。脚本侧
    # dl_wait_pg_isready 已先行 SELECT 1 探测，理论上到这里 DSN 已可
    # 用；上调到 90s 作为 gateway 进程内的最后兜底，避免本地切流时偶发
    # 单次 ping 失败就触发 nil 降级。生产 245 走更保守默认 20s。
    # R37 fix (2026-09-17): the value MUST carry a duration unit ("90s") —
    # the gateway parses it with time.ParseDuration and a bare "90" fails
    # ("missing unit"), silently falling back to the 20s default and
    # neutralizing this defense layer (caught by R37 audit).
    dl_emit_env_line LLM_GATEWAY_DB_BOOT_RETRY_SECONDS "${LLM_GATEWAY_DB_BOOT_RETRY_SECONDS:-90s}"
    dl_emit_env_line LLM_GATEWAY_PG_DATA_DIR "$(dl_shared_pg_dir)"
    # In minimal mode, clear Redis configuration to force SQLite usage
    if [[ "${DL_REDIS_MODE:-}" == "minimal" ]]; then
      dl_emit_env_line LLM_GATEWAY_REDIS_ADDR ""
      dl_emit_env_line LLM_GATEWAY_REDIS_PASSWORD ""
    else
      dl_emit_env_line LLM_GATEWAY_REDIS_ADDR "${LLM_GATEWAY_REDIS_ADDR:-}"
      dl_emit_env_line LLM_GATEWAY_REDIS_PASSWORD "${LLM_GATEWAY_REDIS_PASSWORD:-}"
    fi
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
    # 2026-09-18：配合 PG max_connections=1000 落地（ALTER SYSTEM）。
    # LLM_GATEWAY_DB_MAX_CONNS 是 full 模式 gateway 实际使用的 db.Open
    # pgxpool 池上限（默认 32，见 db/db.go poolMaxConnsFromEnv）；
    # LLM_GATEWAY_STORAGE_MAX_CONNECTIONS 只作用于 storage factory（当前
    # 仅 lite 模式构造，full 模式 gateway 不消费）。两者均 honor .env.local
    # 显式值，未设时写空行（gateway 侧视为未设置走默认）。
    dl_emit_env_line LLM_GATEWAY_DB_MAX_CONNS "${LLM_GATEWAY_DB_MAX_CONNS:-}"
    dl_emit_env_line LLM_GATEWAY_STORAGE_MAX_CONNECTIONS "${LLM_GATEWAY_STORAGE_MAX_CONNECTIONS:-}"
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

# Load a dl_write_env-produced env file WITHOUT shell-sourcing it.
# Values are written verbatim (docker --env-file contract) so they may
# contain shell metacharacters — an unquoted '&' in LLM_GATEWAY_ADMIN_PASSWORD
# made `source` truncate the value and execute the rest as a command
# (mock system test 2026-09-07 §5.5). Exporting the whole KEY=value word is
# safe: bash treats the argument as a name=value assignment, never as syntax.
dl_load_env_file() {
  local file="$1" line
  [[ -f "$file" ]] || { printf 'error: env file not found: %s\n' "$file" >&2; return 1; }
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" != *=* ]] && continue
    # shellcheck disable=SC2163  # line deliberately holds the whole KEY=value word
    export "$line" || { printf 'error: invalid env line: %s\n' "$line" >&2; return 1; }
  done < "$file"
}

dl_stage_release() {
  local bundle="$1" binary="$2" web="$3" version_json="$4" version_file="$5" name="$6"
  # 本函数经 deploy-local.sh 的 var=$(stage_release ...) 赋值语境调用：
  # 该语境下 bash 不因函数内失败命令中断（2026-09-05 陈旧二进制事故的
  # set -e 怪癖），必须逐项显式检查。SHA256SUMS 放在最后生成——任一关键
  # 步骤失败都 return 1 且不留 SHA256SUMS，保证消费端 dl_verify_release
  # （|| die）对残缺 bundle 必然 fail-closed。
  mkdir -p "$bundle" || return 1
  # 2026-09-09：之前 install 失败只看到 bash 自带的 'No such file or
  # directory'，操作员误以为是脚本 bug。这里把"binary 不存在"翻译成
  # 明确信号——build_backend 一定把 binary 写到 $out，$binary 由调用方
  # 传入 = build_backend 的输出。若 binary 不在，说明 build_backend
  # 这一步**没有把 $out 留给 dl_stage_release**（并发 deploy 互踩、
  # 容器构建 SIGKILL 后遗症、或 docker run 返回 0 但产物不在）。
  if [[ ! -s "$binary" ]]; then
    printf '    [stage] binary missing or empty: %s\n' "$binary" >&2
    printf '    [stage] build_backend must produce the binary before stage_release.\n' >&2
    printf '    [stage] Likely cause: concurrent deploy on shared RUN_DIR, or CGO build silently failed.\n' >&2
    printf '    [stage] Inspect: %s/build-host.log, %s/build-cgo*.log\n' "$RUN_DIR" "$RUN_DIR" >&2
    return 1
  fi
  install -m 0755 "$binary" "$bundle/gateway" || { printf '    [stage] install failed: cannot copy %s to %s/gateway\n' "$binary" "$bundle" >&2; return 1; }
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

# 准备 bundle 目录供 dl_stage_release 写入：
#   - 目录不存在：no-op，返回 0
#   - 目录存在且就是 active 发布：mv 到 .prev-<epoch> 让路
#   - 目录存在且含完整 SHA256SUMS（旧发布）：mv 到 .prev-<epoch> 让路
#   - 目录存在但完全空（上次 dl_stage_release 在 mkdir -p 之后被打断留下
#     的残留，例如 Ctrl+C、容器构建 SIGKILL、build_backend 失败导致 install
#     找不到 gateway.build ——这就是 deploy-local-lib.sh:483 的
#     'SHA256SUMS: No such file or directory' 根因）：rmdir 自愈，允许本次
#     deploy 继续
#   - 目录存在且 *包含任何文件*（含隐藏文件）：fail-closed，要求人工确认，
#     因为可能是操作员数据；这是 deploy_local_contract_test 用 sentinel
#     文件守护的契约底线
#
# 2026-09-09 事故修正：早期实现的 rm -rf 会静默销毁含任意内容的同名目录
# （0c465a815），现已替换为只对"完全空"目录自愈，其余一律 fail-closed。
dl_ensure_release_available() {
  local bundle="${1:-}"
  local active_version="${2:-}"
  local aside
  [[ -n "$bundle" ]] || return 64
  if [[ -z "$active_version" ]]; then
    active_version=$(dl_active_version 2>/dev/null || printf '')
  fi
  [[ ! -e "$bundle" && ! -L "$bundle" ]] && return 0
  if [[ -n "$active_version" && "$(basename "$bundle")" == "$active_version" ]]; then
    aside="${bundle}.prev-$(date +%s)"
    printf '[deploy-local] warning: release %s is the active deployment; moving it aside to %s before restaging\n' "$bundle" "$(basename "$aside")" >&2
    mv "$bundle" "$aside" || { printf 'error: failed to move active release aside: %s\n' "$bundle" >&2; return 1; }
    return 0
  fi
  if [[ -e "$bundle/SHA256SUMS" ]]; then
    aside="${bundle}.prev-$(date +%s)"
    printf '[deploy-local] warning: release %s already exists (intact, not active); moving it aside to %s to allow same-version redeploy\n' "$bundle" "$(basename "$aside")" >&2
    mv "$bundle" "$aside" || { printf 'error: failed to move release aside: %s\n' "$bundle" >&2; return 1; }
    return 0
  fi
  if [[ -d "$bundle" ]] && [[ -z "$(ls -A "$bundle" 2>/dev/null || true)" ]]; then
    printf '[deploy-local] warning: release %s is empty (residue from a previous failed deploy); auto-cleaning and retrying staging\n' "$bundle" >&2
    rmdir "$bundle" || { printf 'error: failed to remove empty residue dir: %s (manual cleanup required)\n' "$bundle" >&2; return 1; }
    return 0
  fi
  printf 'error: release already exists: %s (no SHA256SUMS — not a verifiable release; inspect and remove it manually if it is a stale artifact)\n' "$bundle" >&2
  return 1
}

dl_verify_release() {
  local b="$1" line sum path actual
  # dl_stage_release 在 staging 任一关键步骤失败后会主动 rm -f SHA256SUMS
  # （deploy-local-lib.sh :471-472 注释），所以这里读不到 SHA256SUMS 不
  # 是 bash 自带的 'No such file or directory' 重定向错误，而是 staging
  # 早期失败的明确信号。把它翻译成可读错误，避免操作员误以为是脚本
  # bug 或文件权限问题（2026-09-09 事故：'SHA256SUMS: No such file or
  # directory' 让操作员去查 ~/.bashrc，没意识到是 staging 早期失败残留）。
  if [[ ! -f "$b/SHA256SUMS" ]]; then
    printf 'error: %s/SHA256SUMS is missing — the bundle was never successfully staged (dl_stage_release failed before the final checksum generation)\n' "$b" >&2
    return 1
  fi
  (cd "$b" && while IFS= read -r line; do
    sum=${line%% *}
    path=${line#*  }
    [[ -f "$path" ]] || { printf 'error: SHA256SUMS references missing file: %s\n' "$path" >&2; return 1; }
    actual=$(dl_sha256 "$path" | cut -d ' ' -f1)
    if [[ "$actual" != "$sum" ]]; then
      printf 'error: checksum mismatch for %s (expected %s, got %s)\n' "$path" "$sum" "$actual" >&2
      return 1
    fi
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

# 2026-09-19（部署工单）：蓝绿轮换前先实测哪个端口真的有网关在监听。
# deploy-local.sh 与网关同机，127.0.0.1 是合法探测目标（远程部署路径
# deploy-seamless.sh 的对应逻辑走 remote_ssh，勿混用）。
dl_port_listening() {
  local port="$1"
  curl -fsS -o /dev/null --max-time 1 "http://127.0.0.1:${port}/healthz" 2>/dev/null && return 0
  # /healthz 未应答不等于没人监听（网关可能在启动 ensure 链上，还没 bind
  # 完 / 或 503）——退回裸 TCP 探测，只要端口有人占就当它在监听。
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null
}

# 输出 8781/8782 中正在监听的端口（0-2 行）。
dl_detect_active_port() {
  local port
  for port in 8781 8782; do
    dl_port_listening "$port" && printf '%s\n' "$port"
  done
  return 0
}

# dl_active_port 的文件/env 链与实测监听复核后的 active 端口（2026-09-20 修订）。
# 本地无代理拓扑里 active 端口就是对外契约端口（客户端直连 127.0.0.1:8782），
# 实测永远不能改写它——否则一次失败部署留下的残留候选（8781）会把下一次
# 部署整体劫持到 8781，对外端口 8782 静默死亡，再部署一次又可能翻回来，
# 形成用户观察到的 8781/8782 交替。修订后的语义：
#   文件/env 链结果永远优先（操作者意图 = 对外契约）
#   实测仅用于诊断：declared 没人监听而另一侧有人 → 大声 warn，本次部署
#   仍落回 declared（部署流程会在 cutover 阶段把 declared 端口重新拉起，
#   并清掉另一侧残留），对外端口自愈而不是漂移。
#   双监听 → declared（另一侧是残留候选，start_instance 起候选时先清理）
#   都没监听 → declared（全新安装 / 网关已停）
# 显式 LLM_GATEWAY_ACTIVE_PORT/SERVICE_PORT 自定义端口（非 8781/8782）时
# 同样透传，尊重操作者意图。
dl_resolve_active_port() {
  local declared probed
  declared=$(dl_active_port)
  [[ "$declared" == 8781 || "$declared" == 8782 ]] || { printf '%s\n' "$declared"; return 0; }
  probed=$(dl_detect_active_port)
  if ! printf '%s\n' "$probed" | grep -qx "$declared"; then
    local other
    other=$([[ "$declared" == 8781 ]] && printf 8782 || printf 8781)
    if printf '%s\n' "$probed" | grep -qx "$other"; then
      printf '[deploy-local] warning: 对外端口 %s 当前无人监听，而另一侧 %s 有残留网关 —— 仍按文件链部署到 %s（对外契约端口不可漂移），%s 将在本次部署中被清理\n' "$declared" "$other" "$declared" "$other" >&2
    else
      printf '[deploy-local] warning: 对外端口 %s 当前无人监听（全新安装或网关已停止）—— 部署仍落 %s\n' "$declared" "$declared" >&2
    fi
  fi
  printf '%s\n' "$declared"
}
dl_wait_http() {
  local url="$1" deadline=$(( $(date +%s)+${2:-60} ))
  while (( $(date +%s) < deadline )); do curl -fsS --max-time 2 "$url" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
# Pre-flight PostgreSQL reachability probe (2026-09-17, incident 2114).
# Symptom: the freshly started gateway's openDBWithBootRetry exhausted its
# 20s default budget while PG was still warming after a previous cutover,
# returned nil, and /readyz locked to database:null + not_ready forever.
# Fix: before docker run / nohup, prove PG answers SELECT 1 — via docker exec
# into the pg container (Docker mode; container-local view) or directly at
# the DSN host:port (native mode). Timeout 90s covers PG recovery windows
# seen locally.
# R37 (2026-09-17): DSN component validation made reachable (the old
# sed-exit-code guard never fired — both sessions found it independently) and
# the container name is resolved once per call (the log previously expanded
# the function name literally).
# 2026-09-17 audit wrap (origin): env DL_PG_PREFLIGHT_REQUIRED gates the
# timeout outcome. Default 0 (warn-only) keeps the historical `|| true`
# behavior so callers degrade gracefully and the in-gateway
# LLM_GATEWAY_DB_BOOT_RETRY_SECONDS budget (90s, see dl_write_env) still has
# a chance to recover. Set DL_PG_PREFLIGHT_REQUIRED=1 to make a probe timeout
# fatal (die); R39 correction: 245 deploys via deploy-seamless.sh do NOT
# source this library, so that gate can never be exercised there — validate
# it on the deploy-local path (local/252) before enabling it broadly.
dl_wait_pg_isready() {
  local required="${DL_PG_PREFLIGHT_REQUIRED:-0}"
  local dsn="${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}"
  if [[ -z "$dsn" ]]; then
    log 'no DATABASE_URL configured; skipping PG pre-flight'
    return 0
  fi
  local user pass host port db
  # R37 (2026-09-17, both sessions independently): sed -n returns exit 0 even
  # with no match, so the original `if ! user=$(... ) || ! ...` chain could
  # never fire — an unparseable DSN slid through with empty parts and burned
  # the full 90s probing nothing. Extract first, then require the parts every
  # probe needs (pass may be empty for passwordless DSNs).
  # R39: the user segment also accepts the passwordless `user@host` shape —
  # `([^:]+):` required a colon, so `postgresql://u@host:5432/db` slid to the
  # unparseable branch (or worse, matched `u@host` as user with -U).
  user=$(printf '%s' "$dsn" | sed -nE 's|^postgres(ql)?://([^@:/]+)(:[^@]*)?@.*|\2|p')
  pass=$(printf '%s' "$dsn" | sed -nE 's|^postgres(ql)?://[^:]+:([^@]+)@.*|\2|p')
  host=$(printf '%s' "$dsn" | sed -nE 's|^.*@([^:]+):.*|\1|p')
  port=$(printf '%s' "$dsn" | sed -nE 's|^.*@[^:]+:([0-9]+).*|\1|p')
  db=$(printf '%s' "$dsn" | sed -nE 's|^.*/([^?]+).*|\1|p')
  if [[ -z "$user" || -z "$host" || -z "$port" || -z "$db" ]]; then
    if [[ "$required" == "1" ]]; then
      die 'PG pre-flight: could not parse DATABASE_URL — fail-closed (DL_PG_PREFLIGHT_REQUIRED=1)'
    fi
    warn 'could not parse DATABASE_URL; skipping PG pre-flight'

    return 0
  fi
  # R37 fix: the old probe_host remap only fired under DL_DOCKER but was only
  # READ by the non-docker psql/TCP branches — dead logic. The psql/TCP path
  # probes the DSN host as written; the docker-exec path probes from inside
  # the pg container (container-local view — proves PG answers, which is the
  # uncertainty that mattered in incident 2114, but NOT the full
  # host.docker.internal path the gateway container traverses).
  # 2026-09-17 audit: always log the probe target so operators can see when
  # the pre-flight actually fired (success on attempt 1 was previously silent,
  # making it indistinguishable from "function never called").
  local pg_container
  pg_container=$(dl_pg_container_name)
  # R39: never exec into a local container when the caller brings its own PG
  # (DL_DB_MODE=external), and never exec into a STOPPED container —
  # dl_pg_container_name scans `docker ps -a`, so a stopped llm-gateway-pg
  # would fail every exec and burn the whole 90s window (false die under
  # DL_PG_PREFLIGHT_REQUIRED=1 for a healthy external PG).
  if [[ -n "$pg_container" ]] && { [[ "${DL_DB_MODE:-}" == "external" ]] || ! docker ps --filter "name=^${pg_container}$" --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q .; }; then
    pg_container=""
  fi
  log "PG pre-flight: probing host=$host port=$port db=$db (docker=$DL_DOCKER pg_container=${pg_container:-<none>} required=$required)"
  local deadline=$(( $(date +%s) + 90 )) attempt=0
  while (( $(date +%s) < deadline )); do
    attempt=$(( attempt + 1 ))
    if (( DL_DOCKER )) && [[ -n "$pg_container" ]]; then
      docker exec -i -e PGPASSWORD="$pass" "$pg_container" \
        psql -X -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' \
        -h 127.0.0.1 -p 5432 -U "$user" -d "$db" >/dev/null 2>&1 && {
        log "PG pre-flight: ready after $attempt probe(s) via docker exec"
        return 0
      }
    elif _dl_have psql; then
      PGPASSWORD="$pass" psql -X -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' \
        -h "$host" -p "$port" -U "$user" -d "$db" >/dev/null 2>&1 && {
        log "PG pre-flight: ready after $attempt probe(s) via psql"
        return 0
      }
    else
      # No psql and no usable pg container — TCP-connect fallback. LIMITATION:
      # a successful TCP handshake only proves something LISTENS on the port
      # (proxy, lingering socket), not that PG answers SQL; this branch is
      # advisory and may pass while PG is still warming.
      (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null && { exec 3<&-; exec 3>&-; log "PG pre-flight: ready after $attempt probe(s) via /dev/tcp"; return 0; }
    fi
    sleep 2
  done
  if [[ "$required" == "1" ]]; then
    die "PG pre-flight timed out after 90s ($host:$port, db=$db) — fail-closed (DL_PG_PREFLIGHT_REQUIRED=1); gateway would have hit database:null in 20s; refusing to start the container"
  fi
  warn "PG pre-flight timed out after 90s ($host:$port, db=$db); gateway boot retry will have to absorb the remaining warmup"
  return 1
}
dl_pg_container_name() {
  # Best-effort detection of the local PG container name; empty string when
  # running in system/host mode (dl_wait_pg_isready only execs when set).
  if [[ -n "${DL_PG_CONTAINER:-}" ]]; then printf '%s\n' "$DL_PG_CONTAINER"; return; fi
  for c in llm-gateway-pg postgres kx-citus; do
    if (( DL_DOCKER )) && docker ps -a --format '{{.Names}}' 2>/dev/null | grep -Fxq "$c"; then
      printf '%s\n' "$c"; return
    fi
  done
  printf '\n'
}
dl_record_verify() {
  local root="$1" db="$2" redis="$3" version="$4" port="$5"
  printf 'VERIFY_TOOL=deploy-local.sh\nVERIFY_DEVICE=local\nVERIFY_PASS=1\nVERIFY_ROOT=%s\nVERIFY_RELEASE=%s\nVERIFY_PORT=%s\nVERIFY_DATABASE=%s\nVERIFY_REDIS=%s\n' "$root" "$version" "$port" "$db" "$redis"
}
