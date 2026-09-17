#!/usr/bin/env bash
# Local deployment entry point. See .agents/skills/llm-gateway-deploy/SKILL.md.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# P1.1 SSOT 软链后，deploy-lib/db-changelog.sh 的 repo_root 第三级 fallback
# 解析到共享库目录而非本仓库；显式钉住 resolver 的 override #1（与 seamless 同）。
export DB_CHANGELOG_REPO_ROOT="$PROJECT_ROOT"
[[ -d "$DB_CHANGELOG_REPO_ROOT/sql/migrations" ]] || {
  echo "FATAL: DB_CHANGELOG_REPO_ROOT=$DB_CHANGELOG_REPO_ROOT 下没有 sql/migrations" >&2
  exit 64
}

# shellcheck source=deploy-local-lib.sh
source "$SCRIPT_DIR/deploy-local-lib.sh"
# 共享部署库 SSOT（P1.1）：导出 AIAN_DEPLOY_LIB 并预载 prereqs + 镜像解析。
# 历史副本留档于 deploy-lib.legacy/；scripts/deploy-lib 为共享 SSOT 的相对软链。
# shellcheck source=_shared-lib.sh
source "$SCRIPT_DIR/_shared-lib.sh"
# shellcheck source=deploy-lib/lock.sh
source "$AIAN_DEPLOY_LIB/lock.sh"
# shellcheck source=deploy-lib/post-deploy-verify.sh
source "$AIAN_DEPLOY_LIB/post-deploy-verify.sh"

# Track whether the caller provided the database configuration before the
# import below, so diagnostics can say where the DSN came from.
DL_DATABASE_CONFIG_SOURCE=none
if [[ -n "${LLM_GATEWAY_DATABASE_URL:-}" || -n "${DATABASE_URL:-}" ]]; then
  DL_DATABASE_CONFIG_SOURCE=caller
fi

# Config accepts DATABASE_URL as a compatibility fallback; normalize it once
# so all local probes, migrations, and generated runtime env use one DSN.
# This must run BEFORE the .env.local import: a caller-supplied DSN then
# occupies the canonical LLM_GATEWAY_DATABASE_URL key and wins over the
# file's DSN instead of splitting the two keys across different values.
if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" && -n "${DATABASE_URL:-}" ]]; then
  export LLM_GATEWAY_DATABASE_URL="$DATABASE_URL"
fi
if [[ -z "${DATABASE_URL:-}" && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
  export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
fi

# Import the project-local configuration for every key the caller left unset
# or empty; caller-provided values stay authoritative so CI and production
# wrappers can inject their own DSN without being replaced. This import used
# to run only when the caller had no DSN at all, so deploys launched from
# shells that exported just DATABASE_URL skipped .env.local entirely and lost
# LLM_GATEWAY_SECRET_KEY: the gateway came up unable to sign admin sessions
# (every login returned "token generation failed") and previously issued
# tokens stopped verifying (incident 2026-09-05, all 2.5.0.x local deploys).
dl_load_project_env "$PROJECT_ROOT/.env.local"
# Second normalization pass: when the caller had no DSN at all, the import
# above is what sets LLM_GATEWAY_DATABASE_URL — fill the DATABASE_URL compat
# alias from it (the first pass ran before the import and saw both empty).
# Idempotent: with a caller-provided DSN both passes are no-ops.
if [[ -z "${DATABASE_URL:-}" && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
  export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
fi
if [[ "$DL_DATABASE_CONFIG_SOURCE" == "none" && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
  DL_DATABASE_CONFIG_SOURCE=project-env
fi


ACTION=deploy
DRY_RUN=0
SKIP_FRONTEND=0
MINIMAL_DEPLOY=0
ROOT_OVERRIDE=
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-60}"
KEEP_RELEASES="${KEEP_RELEASES:-3}"
CLEANUP_DOWNLOADS=0
DEPLOY_BUILD_LOCK_HELD=0
LOCK_LOCAL_BUILD_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-build.lock"
LOCK_BUILD_TARGET=local

# Reject legacy path overrides so a stray INSTALL_ROOT / LLM_GATEWAY_FILES_ROOT
# from older scripts cannot repoint a deploy into ~/Downloads/kaixuan/llm-gateway.
if [[ -n "${INSTALL_ROOT:-}" || -n "${LLM_GATEWAY_FILES_ROOT:-}" ]]; then
  if [[ "${DEPLOY_LOCAL_ALLOW_LEGACY_ROOT:-0}" != 1 ]]; then
    printf '[deploy-local] error: legacy INSTALL_ROOT/LLM_GATEWAY_FILES_ROOT is set; unset them and use --root to target the new project root explicitly\n' >&2
    exit 64
  fi
  printf '[deploy-local] warning: DEPLOY_LOCAL_ALLOW_LEGACY_ROOT=1 — using legacy root variable; the shared ~/kaixuan layout will not be used\n' >&2
fi

usage() {
  cat <<'EOF'
Usage: deploy-local.sh [deploy|status|verify|rollback|start|stop|logs] [options]

Options:
  --root PATH             install root (otherwise OS default)
  --dry-run               print the plan without changing files, containers or processes
  --no-frontend           reuse the existing web/dist output
  --minimal               minimal deployment using SQLite (no Redis required)
  --timeout SECS          health probe timeout (default: 60)
  --cleanup-downloads     remove the obsolete ~/Downloads/llm-gateway-files copies
                          (only after PG migration is verified). Off by default.
  --help

Project layout:
  ~/kaixuan/llm-gateway-go/{bin,logs,raw-logs,run,backups,attachments}

Shared services under ~/kaixuan:
  ~/kaixuan/postgres/{logs,backups,run}    ← PostgreSQL data + service logs
  ~/kaixuan/redis/{logs,run}                ← Redis data (existing volume)

Active listen port is 8782 (candidate 8781). Override with LLM_GATEWAY_ACTIVE_PORT
to repurpose a host port, or use --root to move the project.

Existing PostgreSQL and Redis instances are reused; no data or password is reset
automatically. A deploy allocates the next build sequence through
scripts/bump-version.sh; --dry-run never changes version files.

Minimal deployment mode (--minimal):
  Uses SQLite instead of Redis for session storage. Suitable for development
  and testing environments. Redis container is not required or created.

Redis container configuration:
  Set LLM_GATEWAY_REDIS_CONTAINER environment variable to specify a custom
  Redis container name. If not set, auto-discovery will search for common
  Redis container names (nbjl-redis, llm-gateway-redis, redis, kx-redis).
EOF
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  while [[ $# -gt 0 ]]; do
    case "$1" in
      deploy|status|verify|rollback|start|stop|logs) ACTION=$1; shift ;;
      --root) ROOT_OVERRIDE=${2:?--root requires a path}; shift 2 ;;
      --dry-run) DRY_RUN=1; shift ;;
      --no-frontend) SKIP_FRONTEND=1; shift ;;
      --minimal) MINIMAL_DEPLOY=1; shift ;;
      --timeout) HEALTH_TIMEOUT=${2:?--timeout requires seconds}; shift 2 ;;
      --cleanup-downloads) CLEANUP_DOWNLOADS=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) printf 'error: unknown argument: %s\n' "$1" >&2; usage >&2; exit 64 ;;
    esac
  done
fi

if [[ -n "$ROOT_OVERRIDE" ]]; then export LLM_GATEWAY_ROOT="$ROOT_OVERRIDE"; fi
ROOT_DIR=$(dl_root)
BIN_DIR="$ROOT_DIR/bin"
RUN_DIR="$ROOT_DIR/run"
LOG_DIR="$ROOT_DIR/logs"
SHARED_ROOT=$(dl_shared_root)
SHARED_PG_DIR=$(dl_shared_pg_dir)
SHARED_REDIS_DIR=$(dl_shared_redis_dir)
SHARED_PG_LOG_DIR=$(dl_pg_log_dir)
SHARED_PG_BACKUP_DIR=$(dl_pg_backup_dir)
SHARED_PG_RUN_DIR=$(dl_pg_run_dir)
SHARED_REDIS_LOG_DIR=$(dl_redis_log_dir)
SHARED_REDIS_RUN_DIR=$(dl_redis_run_dir)
VERSION_JSON="$PROJECT_ROOT/version.json"
VERSION_FILE="$PROJECT_ROOT/VERSION"

log() { printf '[deploy-local] %s\n' "$*" >&2; }
warn() { printf '[deploy-local] warning: %s\n' "$*" >&2; }
die() { printf '[deploy-local] error: %s\n' "$*" >&2; exit 1; }

release_build_lock() {
  if (( DEPLOY_BUILD_LOCK_HELD )); then
    lock_release_build || true
    DEPLOY_BUILD_LOCK_HELD=0
  fi
}

cleanup_deploy() {
  local status=$?
  trap - EXIT INT TERM
  release_build_lock
  exit "$status"
}
trap cleanup_deploy EXIT INT TERM

if ! [[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || (( HEALTH_TIMEOUT < 1 )); then die 'timeout must be a positive integer'; fi


RELEASE_VERSION="$(dl_release_name "$VERSION_JSON")"

plan() {
  dl_detect_resources
  printf 'ACTION=%s\nROOT=%s\nSHARED_ROOT=%s\nSHARED_PG_DIR=%s\nSHARED_REDIS_DIR=%s\nRELEASE=%s\nACTIVE=%s\nACTIVE_PORT=%s\nCANDIDATE_PORT=%s\nDOCKER=%s\nCOMPOSE=%s\nDATABASE_MODE=%s\nREDIS_MODE=%s\nPG_CONTAINER=%s\nREDIS_CONTAINER=%s\nCLEANUP_DOWNLOADS=%s\nMINIMAL_DEPLOY=%s\n' \
    "$ACTION" "$ROOT_DIR" "$SHARED_ROOT" "$SHARED_PG_DIR" "$SHARED_REDIS_DIR" "$RELEASE_VERSION" \
    "$(dl_active_version)" "$(dl_active_port)" "$(dl_candidate_port)" \
    "$DL_DOCKER" "$DL_COMPOSE" "$DL_DB_MODE" "$DL_REDIS_MODE" "${DL_PG_CONTAINER:-none}" "${DL_REDIS_CONTAINER:-none}" "$CLEANUP_DOWNLOADS" "$MINIMAL_DEPLOY"
  printf 'VERIFY_TOOL=deploy-local.sh\nVERIFY_DEVICE=local\nVERIFY_PASS=0\n'
}

if (( DRY_RUN )); then plan; exit 0; fi

need_cmd() { _dl_have "$1" || die "$1 is required"; }
compose_cmd() {
  if (( DL_COMPOSE )); then docker compose "$@"; else docker-compose "$@"; fi
}

detect_existing_containers() {
  DL_PG_CONTAINER=""; DL_REDIS_CONTAINER=""
  if (( DL_DOCKER )); then
    # Source smart discovery functions if available
    if [[ -f "$AIAN_DEPLOY_LIB/smart-discovery.sh" ]]; then
      # shellcheck source=deploy-lib/smart-discovery.sh
      source "$AIAN_DEPLOY_LIB/smart-discovery.sh"
      
      # PostgreSQL smart discovery with priority and database checking
      detect_postgres_container || true
      
      # Skip Redis detection in minimal mode
      if [[ "${MINIMAL_DEPLOY:-0}" == 0 ]]; then
        # Redis smart discovery with priority and connection testing
        detect_redis_container || true
      fi
      
      # Configure PostgreSQL if found. if-form: under `set -e` a false
      # `[[ … ]] && cmd` statement would silently kill the whole deploy
      # when no PG container exists and the caller supplied an external
      # DATABASE_URL (external-DB mode, 2026-09-17 verify deploy).
      if [[ -n "$DL_PG_CONTAINER" ]]; then configure_postgres_container; fi

      # Configure Redis if found (same set -e hazard as above).
      if [[ -n "$DL_REDIS_CONTAINER" && "${MINIMAL_DEPLOY:-0}" == 0 ]]; then configure_redis_container; fi
    else
      # Fallback to original discovery logic if smart discovery not available
      for c in llm-gateway-pg postgres kx-citus; do
        if docker ps -a --format '{{.Names}}' | grep -Fxq "$c"; then DL_PG_CONTAINER=$c; break; fi
      done
      # Skip Redis detection in minimal mode
      if [[ "${MINIMAL_DEPLOY:-0}" == 0 ]]; then
        # Check environment variable first, then common names
        if [[ -n "${LLM_GATEWAY_REDIS_CONTAINER:-}" ]]; then
          if docker ps -a --format '{{.Names}}' | grep -Fxq "$LLM_GATEWAY_REDIS_CONTAINER"; then
            DL_REDIS_CONTAINER=$LLM_GATEWAY_REDIS_CONTAINER
          fi
        else
          for c in llm-gateway-redis redis kx-redis nbjl-redis; do
            if docker ps -a --format '{{.Names}}' | grep -Fxq "$c"; then DL_REDIS_CONTAINER=$c; break; fi
          done
        fi
      fi
      if [[ -n "$DL_PG_CONTAINER" ]]; then
        docker start "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
        DL_DB_MODE=docker
        DL_PG_SOURCE=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Source}}{{end}}{{end}}' "$DL_PG_CONTAINER" 2>/dev/null || true)
        local db_url
        db_url=$(dl_container_env "$DL_PG_CONTAINER" LLM_GATEWAY_DATABASE_URL)
        [[ -z "$db_url" ]] && db_url=$(dl_container_env "$DL_PG_CONTAINER" DATABASE_URL)
        if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" && -z "${DATABASE_URL:-}" ]]; then
          if [[ -n "$db_url" && "$db_url" =~ @((127\\.0\\.0\\.1)|(localhost))(:|/) ]]; then
            local db_port
            db_port=$(docker port "$DL_PG_CONTAINER" 5432/tcp 2>/dev/null | sed -n 's/.*://p' | head -n1 || true)
            if [[ -z "$db_port" ]]; then
              warn "PostgreSQL container $DL_PG_CONTAINER has no host port for 5432; will use environment DATABASE_URL if available"
              return 0
            fi
            export LLM_GATEWAY_DATABASE_URL="$db_url" DATABASE_URL="$db_url"
          else
            local db_user db_pass db_name db_port
            db_user=$(dl_container_env "$DL_PG_CONTAINER" POSTGRES_USER); db_user=${db_user:-llm_gateway}
            db_pass=$(dl_container_env "$DL_PG_CONTAINER" POSTGRES_PASSWORD)
            db_name=$(dl_container_env "$DL_PG_CONTAINER" POSTGRES_DB); db_name=${db_name:-llm_gateway}
            db_port=$(docker port "$DL_PG_CONTAINER" 5432/tcp 2>/dev/null | sed -n 's/.*://p' | head -n1 || true)
            if [[ -z "$db_port" ]]; then
              warn "PostgreSQL container $DL_PG_CONTAINER has no host port for 5432; will use environment DATABASE_URL if available"
              return 0
            fi
            if [[ -n "$db_pass" ]]; then
              export LLM_GATEWAY_PG_USER="$db_user" LLM_GATEWAY_PG_PASSWORD="$db_pass" LLM_GATEWAY_PG_DATABASE="$db_name"
              export LLM_GATEWAY_DATABASE_URL="postgresql://${db_user}:${db_pass}@127.0.0.1:${db_port}/${db_name}?sslmode=disable"
              export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
            fi
          fi
        fi
      fi
      if [[ -n "$DL_REDIS_CONTAINER" && "${MINIMAL_DEPLOY:-0}" == 0 ]]; then
        docker start "$DL_REDIS_CONTAINER" >/dev/null 2>&1 || true
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
      fi
    fi
  fi
}

migrate_existing_pg_to_shared() {
  [[ "$DL_DB_MODE" == docker && "$DL_PG_CONTAINER" == llm-gateway-pg ]] || return 0
  [[ -n "$DL_PG_SOURCE" && "$DL_PG_SOURCE" == /* ]] || return 0
  local shared="$SHARED_PG_DIR" project_copy="$ROOT_DIR/postgres" old_name pg_backup
  old_name="llm-gateway-pg.pre-migrate.$(date -u +%Y%m%dT%H%M%SZ)"
  dl_prepare_shared_service_dirs
  [[ "$DL_PG_SOURCE" != "$shared" ]] || {
    log "llm-gateway-pg already bound to shared $shared; no copy required"
    return 0
  }
  log "migrating llm-gateway-pg data to $shared (source retained)"
  docker stop "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
  mkdir -p "$SHARED_PG_BACKUP_DIR"
  pg_backup="$SHARED_PG_BACKUP_DIR/llm-gateway-pg-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
  tar -C "$DL_PG_SOURCE" -czf "$pg_backup" .
  tar -C "$DL_PG_SOURCE" -cf - . | tar -C "$shared" -xf -
  if ! [[ -s "$shared/PG_VERSION" ]]; then
    die "PostgreSQL migration produced no PG_VERSION; source retained at $DL_PG_SOURCE"
  fi
  local env_file="$SHARED_PG_RUN_DIR/llm-gateway-pg.env"
  docker rename "$DL_PG_CONTAINER" "$old_name" || die "failed to rename $DL_PG_CONTAINER to $old_name"
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$old_name" > "$env_file"
  chmod 0600 "$env_file"
  local image network pg_user pg_db pg_pass
  image=$(docker inspect -f '{{.Config.Image}}' "$old_name")
  network=$(docker inspect -f '{{range $n, $cfg := .NetworkSettings.Networks}}{{println $n}}{{end}}' "$old_name" | head -n1)
  pg_user=$(dl_container_env "$old_name" POSTGRES_USER); pg_user=${pg_user:-llm_gateway}
  pg_db=$(dl_container_env "$old_name" POSTGRES_DB); pg_db=${pg_db:-llm_gateway}
  pg_pass=$(dl_container_env "$old_name" POSTGRES_PASSWORD)
  local -a run_args=(--name "$DL_PG_CONTAINER" --restart unless-stopped --env-file "$env_file" -v "$shared:/var/lib/postgresql/data" -p "127.0.0.1:5432:5432")
  [[ -n "$network" ]] && run_args+=(--network "$network")
  if ! docker run -d "${run_args[@]}" "$image" >/dev/null; then
    warn "new llm-gateway-pg failed to start; restoring original container"
    docker rm -f "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    docker rename "$old_name" "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    docker start "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    return 1
  fi
  local new_status=1
  for _ in {1..60}; do
    if [[ -n "$pg_pass" ]]; then
      docker exec -e PGPASSWORD="$pg_pass" "$DL_PG_CONTAINER" pg_isready -U "$pg_user" -d "$pg_db" >/dev/null 2>&1 && { new_status=0; break; }
    else
      docker exec "$DL_PG_CONTAINER" pg_isready -U "$pg_user" -d "$pg_db" >/dev/null 2>&1 && { new_status=0; break; }
    fi
    sleep 1
  done
  if (( new_status != 0 )); then
    warn "migrated PostgreSQL did not become ready; restoring original container"
    docker rm -f "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    docker rename "$old_name" "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    docker start "$DL_PG_CONTAINER" >/dev/null 2>&1 || true
    return 1
  fi
  docker rm "$old_name" >/dev/null 2>&1 || warn "old container retained as $old_name for manual cleanup"
  DL_PG_SOURCE="$shared"
  if [[ -d "$project_copy" && "$project_copy" != "$shared" ]]; then
    if ! [[ -L "$project_copy" ]]; then
      if dl_pg_clusters_equal "$shared" "$project_copy"; then
        rm -rf "$project_copy"
        log "removed legacy project-local copy at $project_copy"
      else
        warn "project-local copy at $project_copy differs from shared cluster; keeping for manual review"
      fi
    fi
  fi
  dl_link_existing_data
  log "llm-gateway-pg ready at $shared; backup=$pg_backup"
}

write_dependencies_compose() {
  local file="$RUN_DIR/dependencies.compose.yml" env_file="$RUN_DIR/dependencies.env"
  local pg_user="${LLM_GATEWAY_PG_USER:-llm_gateway}" pg_db="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}"
  local pg_pass="${LLM_GATEWAY_PG_PASSWORD:-}" redis_pass="${LLM_GATEWAY_REDIS_PASSWORD:-}"
  if [[ -z "$pg_pass" ]]; then pg_pass=$(openssl rand -hex 24); export LLM_GATEWAY_PG_PASSWORD="$pg_pass"; fi
  umask 077
  printf 'POSTGRES_USER=%s\nPOSTGRES_PASSWORD=%s\nPOSTGRES_DB=%s\nREDIS_PASSWORD=%s\n' "$pg_user" "$pg_pass" "$pg_db" "$redis_pass" > "$env_file"
  chmod 0600 "$env_file"
  local redis_command='["redis-server", "--appendonly", "yes"]'
  if [[ -n "$redis_pass" ]]; then
    redis_command='["redis-server", "--appendonly", "yes", "--requirepass", "'"$redis_pass"'"]'
  fi
  local redis_container_name="${LLM_GATEWAY_REDIS_CONTAINER:-llm-gateway-redis}"
  cat > "$file" <<EOF
services:
  postgres:
    image: \${LLM_GATEWAY_PG_IMAGE:-postgres:17-alpine}
    container_name: llm-gateway-pg
    restart: unless-stopped
    environment:
      POSTGRES_USER: \${POSTGRES_USER}
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
      POSTGRES_DB: \${POSTGRES_DB}
    volumes:
      - ${SHARED_PG_DIR}:/var/lib/postgresql/data
    ports:
      - "127.0.0.1:\${LLM_GATEWAY_PG_PORT:-5432}:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U \${POSTGRES_USER} -d \${POSTGRES_DB}"]
      interval: 5s
      timeout: 5s
      retries: 20
  redis:
    image: \${LLM_GATEWAY_REDIS_IMAGE:-redis:7-alpine}
    container_name: ${redis_container_name}
    restart: unless-stopped
    command: $redis_command
    volumes:
      - ${SHARED_REDIS_DIR}:/data
    ports:
      - "127.0.0.1:\${LLM_GATEWAY_REDIS_PORT:-6379}:6379"
EOF
  printf '%s\n' "$file"
}

wait_container() {
  local name="$1"; for _ in {1..60}; do
    [[ "$(docker inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || true)" == healthy ]] && return 0
    sleep 1
  done
  docker logs --tail=80 "$name" >&2 || true
  return 1
}

load_redis_image_if_needed() {
  # Check if Redis image is already available
  local redis_image="${LLM_GATEWAY_REDIS_IMAGE:-redis:7-alpine}"
  if docker image inspect "$redis_image" >/dev/null 2>&1; then
    log "Redis image $redis_image already available"
    return 0
  fi

  # Try to load from base images directory
  local base_images_dir="${DOCKER_BASE_IMAGES_DIR:-$HOME/work/docker-base-images}"
  local arch
  arch=$(uname -m)
  local redis_tar=""

  case "$arch" in
    arm64|aarch64)
      redis_tar="$base_images_dir/cache-mq/redis-7.4.3-alpine-arm64.tar.gz"
      if [[ ! -f "$redis_tar" ]]; then
        redis_tar="$base_images_dir/cache-mq/kx-redis-7.4.9-arm64.tar.gz"
      fi
      ;;
    x86_64|amd64)
      redis_tar="$base_images_dir/cache-mq/redis-7.0.15-alpine-amd64.tar.gz"
      if [[ ! -f "$redis_tar" ]]; then
        redis_tar="$base_images_dir/cache-mq/kx-redis-7.4.9-amd64.tar.gz"
      fi
      ;;
    *)
      warn "unknown architecture $arch; will try to pull Redis image from registry"
      return 1
      ;;
  esac

  if [[ -f "$redis_tar" ]]; then
    log "loading Redis image from $redis_tar"
    if docker load -i "$redis_tar" >/dev/null 2>&1; then
      log "Redis image loaded successfully"
      return 0
    else
      warn "failed to load Redis image from $redis_tar"
      return 1
    fi
  else
    log "Redis image tarball not found at $redis_tar"
    return 1
  fi
}

ensure_resources() {
  dl_detect_resources
  detect_existing_containers
  dl_prepare_shared_service_dirs
  migrate_existing_pg_to_shared || die 'existing PostgreSQL data migration failed; no application was started'
  local need_pg=0 need_redis=0
  [[ "$DL_DB_MODE" == none ]] && need_pg=1
  # In minimal mode, skip Redis entirely
  if (( MINIMAL_DEPLOY == 0 )); then
    [[ "$DL_REDIS_MODE" == none && -z "${LLM_GATEWAY_REDIS_ADDR:-}" ]] && need_redis=1
  else
    log "minimal deployment mode: skipping Redis"
    DL_REDIS_MODE=minimal
  fi
  dl_link_existing_data
  dl_prepare_layout "$need_pg" "$need_redis"
  if (( need_pg || need_redis )); then
    (( DL_DOCKER && DL_COMPOSE )) || die 'no usable PostgreSQL/Redis and Docker Compose is unavailable'
    # Try to load Redis image from base images if needed
    if (( need_redis )); then
      load_redis_image_if_needed || log "will attempt to pull Redis image from registry"
    fi
    local compose_file; compose_file=$(write_dependencies_compose)
    local -a services=()
    (( need_pg )) && services+=(postgres)
    (( need_redis )) && services+=(redis)
    compose_cmd --env-file "$RUN_DIR/dependencies.env" -f "$compose_file" up -d "${services[@]}"
    (( need_pg )) && DL_PG_CONTAINER=llm-gateway-pg
    if (( need_redis )); then
      DL_REDIS_CONTAINER="${LLM_GATEWAY_REDIS_CONTAINER:-llm-gateway-redis}"
    fi
    if (( need_pg )); then wait_container llm-gateway-pg || die 'local PostgreSQL did not become healthy'; fi
    if (( need_redis )); then wait_container "$DL_REDIS_CONTAINER" || die 'local Redis did not become healthy'; fi
    if (( need_pg )); then
      export LLM_GATEWAY_DATABASE_URL="postgresql://${LLM_GATEWAY_PG_USER:-llm_gateway}:${LLM_GATEWAY_PG_PASSWORD}@127.0.0.1:${LLM_GATEWAY_PG_PORT:-5432}/${LLM_GATEWAY_PG_DATABASE:-llm_gateway}?sslmode=disable"
      export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
      DL_DB_MODE=new-docker
    fi
    if (( need_redis )); then
      export LLM_GATEWAY_REDIS_ADDR="127.0.0.1:${LLM_GATEWAY_REDIS_PORT:-6379}"
      DL_REDIS_MODE=new-docker
    fi
  fi
  if [[ "$DL_DB_MODE" == none ]]; then die 'no PostgreSQL instance found; set LLM_GATEWAY_DATABASE_URL or enable Docker'; fi
  export DL_DB_MODE DL_REDIS_MODE
  # Idempotent role/database bootstrap for the local llm-gateway-pg container.
  # Safe on a healthy cluster (no-op when llm_gateway role + db already exist;
  # never alters passwords or touches table data). Skipped silently for any
  # other PG container or non-docker database modes. See
  # scripts/local-dev/ensure-llm-gateway-pg-role.sh for the create-only
  # contract that mirrors recreate-llm-gateway-pg.sh policy guard.
  if [[ "$DL_DB_MODE" == docker && "$DL_PG_CONTAINER" == llm-gateway-pg \
        && -x "$SCRIPT_DIR/local-dev/ensure-llm-gateway-pg-role.sh" ]]; then
    log 'running idempotent role/db bootstrap against llm-gateway-pg (data-safe)'
    if ! LLM_GATEWAY_PG_CONTAINER="$DL_PG_CONTAINER" \
         LLM_GATEWAY_PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}" \
         LLM_GATEWAY_PG_PASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" \
         LLM_GATEWAY_PG_DATABASE="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" \
         bash "$SCRIPT_DIR/local-dev/ensure-llm-gateway-pg-role.sh"; then
      warn 'role/db bootstrap returned non-zero; continuing (cluster may already be healthy)'
    fi
  fi
}

psql_query() {
  local sql="$1"
  if _dl_have psql; then psql -X -v ON_ERROR_STOP=1 -Atqc "$sql" "$LLM_GATEWAY_DATABASE_URL"
  elif [[ -n "${DL_PG_CONTAINER:-}" ]]; then
    local u="${LLM_GATEWAY_PG_USER:-llm_gateway}" d="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}"
    docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$DL_PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U "$u" -d "$d" -Atqc "$sql"
  else die 'psql is required for database verification'; fi
}

apply_schema_if_empty() {
  local count
  count=$(psql_query "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind IN ('r','p','v','m','S','f') AND relname <> 'repository_schema_migrations';") || die 'cannot inspect PostgreSQL schema'
  [[ "$count" == 0 ]] || return 0
  log 'empty PostgreSQL detected; applying schema snapshot once'
  local f log_file
  for f in 00-prereqs.sql 01-schema.sql 02-seed.sql; do
    log_file="$RUN_DIR/schema-${f}.log"
    if _dl_have psql; then
      if ! psql -X -v ON_ERROR_STOP=1 -q "$LLM_GATEWAY_DATABASE_URL" -f "$PROJECT_ROOT/sql/schema/$f" >"$log_file" 2>&1; then
        die "schema snapshot $f failed; see $log_file"
      fi
    else
      if ! docker exec -i -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$DL_PG_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" < "$PROJECT_ROOT/sql/schema/$f" >"$log_file" 2>&1; then
        die "schema snapshot $f failed; see $log_file"
      fi
    fi
  done
}

build_backend() {
  need_cmd go
  local out="$RUN_DIR/gateway.build"
  local target_os="${GOOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
  local target_arch="${GOARCH:-$(go env GOARCH)}"
  (( DL_DOCKER )) && target_os=linux
  # 先删除旧产物：go build 失败时绝不能把陈旧 gateway.build 留给后续
  # step_release 打包（2026-09-05 事故：编译失败被赋值语境的 set -e 怪癖
  # 静默吞掉，两个新版本号打包了同一个 4 小时前的旧二进制）。
  rm -f "$out"
  if ! (cd "$PROJECT_ROOT" && CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$out" ./cmd/gateway) 2>"$RUN_DIR/build-host.log"; then
    # 2026-09-07: 上游 0e3fa12f6 线引入了 CGO-only 依赖（mattn/go-sqlite3、
    # yalue/onnxruntime_go，见 Dockerfile 2026-09-05 的 CGO_ENABLED=1 注），
    # 纯静态 CGO=0 构建自此后必然失败（"build constraints exclude all Go
    # files"）。宿主机不一定有 linux 交叉 C 工具链，因此回退到
    # kx-base/golang:1.27-alpine-amd64 容器内 CGO 构建：musl 产物可直接
    # 跑在默认 alpine:3.22 运行时镜像上（LLM_GATEWAY_RUNTIME_IMAGE 可覆盖）。
    #
    # 默认值 2026-09-09 改：原 golang:1.27-alpine 在离线 + Apple Silicon 上
    # 会去 Docker Hub 拉 amd64 失败；kx-base/golang:1.27-alpine-amd64 在
    # ~/work/docker-base-images/lang-base/ 与 ~/work/docker-base-image/lang-base/
    # 都有离线 tar.gz，且已推 registry.itestu.cn/lang-base/kx-base-golang
    # 兜底。重新构建/保存：~/work/docker-base-images/scripts/build-kx-base-golang-1.27-alpine.sh
    #
    # 2026-09-09 进一步加固：之前 docker run 的 stderr 被 '>/dev/null'
    # 吞掉，go build 失败时操作员看到的就是空的 bash 错误；现在每个
    # 关键步骤都把 stderr 落到 $RUN_DIR/build-cgo-*.log，并用 [[ -s ]]
    # 验证 cgo_out 真的写出来了，避免 mv 一个空文件（mv -f 找不到源
    # 时只 print 不返回 1，致命失败被静默吞掉）。
    need_cmd docker
    local build_image="${LLM_GATEWAY_BUILD_IMAGE:-kx-base/golang:1.27-alpine-amd64}"
    local cgo_log="$RUN_DIR/build-cgo.log"
    # Ensure we pull the correct platform image matching target architecture
    local docker_platform
    if [[ "$target_arch" == "arm64" || "$target_arch" == "aarch64" ]]; then
      docker_platform="linux/arm64"
    elif [[ "$target_arch" == "amd64" || "$target_arch" == "x86_64" ]]; then
      docker_platform="linux/amd64"
    else
      die "unsupported target architecture: $target_arch (expected arm64 or amd64)"
    fi
    # 镜像解析走共享 SSOT resolve_build_image（UNIFICATION-PLAN-2026-09-09 §3.3，
    # P1.1 抽离原内联块）：本地 cache 含平台校验（平台不符视为未命中自动
    # 重解析，保留原 need_pull 防线）→ 离线 tar(~/work/{docker-base-images,
    # docker-base-image}/lang-base，dash 命名) → registry.itestu.cn → docker
    # hub；返回 0 保证本地可 inspect 该镜像且平台匹配。
    # >&2（2026-09-10）：SSOT 的进度日志（hit/loading/Loaded image）走 stdout，
    # 而 build_backend 的 stdout 被 binary=$(build_backend) 当返回值捕获；
    # 不重定向时镜像未命中场景（如 arm64 首次 load 离线 tar）会把
    # "Loaded image: ..." 混进 $binary，stage_release 的 [[ -s ]] 必失败。
    if ! resolve_build_image "$build_image" "$docker_platform" >&2; then
      die "CGO fallback needs image $build_image ($docker_platform); tried local cache, offline tar in ~/work/{docker-base-images,docker-base-image}/lang-base/, registry.itestu.cn/lang-base/kx-base-golang and docker hub"
    fi
    local cgo_out="$PROJECT_ROOT/.build-local/gateway.build.$$"
    mkdir -p "$PROJECT_ROOT/.build-local"
    # 2026-09-09：之前 cgo_out 用固定名 gateway.build，并发 deploy（本地
    # vs 远端 seamless 同时跑、或者 build-host.log 被外部清理工具触碰）
    # 会让 cgo_out 在 docker run 完成到 mv 之间被另一进程 rm，导致 mv
    # 报"No such file"但 build_backend 没炸——stage_release 拿着不存在的
    # $out 去 install，最后在 dl_verify_release 撞上 "no SHA256SUMS"。
    # 用 $$ 后缀给每次 build 一个独占路径，docker run 写到 .$$ 文件，mv
    # 到 $out 是单一原子动作；任何并发 deploy 不会互踩产物。
    if ! (cd "$PROJECT_ROOT" && HOST_UID="$(id -u)" HOST_GID="$(id -g)" docker run --rm \
        --platform="$docker_platform" \
        -v "$PWD":/src -w /src \
        -e HOST_UID -e HOST_GID \
        -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH="$target_arch" \
        -e GOCACHE=/tmp/go-build-cache -e GOPATH=/tmp/go-path \
        "$build_image" \
        sh -c 'apk add --no-cache gcc musl-dev >/dev/null && go build -trimpath -buildvcs=false -ldflags="-s -w" -o /src/.build-local/gateway.build.'"$$"' ./cmd/gateway && chown "$HOST_UID:$HOST_GID" /src/.build-local/gateway.build.'"$$") 2>"$cgo_log"; then
      printf '    [cgo-build stderr follows]\n' >&2
      sed 's/^/    /' "$cgo_log" >&2 || true
      die "backend CGO container build failed (GOOS=linux GOARCH=$target_arch); full log: $cgo_log"
    fi
    # docker run 返回 0 但产物缺失意味着容器内的 sh -c 在最后一节失败前
    # 已经悄悄 return 0（极少见），或者 chown 写到了别的路径。给出明确
    # 诊断而非把 mv 静默失败继续往下走。
    if [[ ! -s "$cgo_out" ]]; then
      printf '    [cgo-build host log follows]\n' >&2
      sed 's/^/    /' "$cgo_log" >&2 || true
      ls -la "$PROJECT_ROOT/.build-local/" >&2 || true
      rm -f "$cgo_out"  # cleanup stale $$ file
      die "backend CGO build produced no output at $cgo_out (docker run returned 0 but the file is missing or empty); inspect $cgo_log"
    fi
    # 用 install 而非 mv：原子替换 + 显式 mtime，便于后续检查。
    # mv -f 找不到源时只 print 不返回 1（bash 的 mv builtin 怪癖），且
    # 即便返回 1 也可能被 set -e 吞掉（之前用 `if ! mv ...` 守门但仍然
    # 失败过——偶发的 docker fsync race）。install 出错时一定有非零退出
    # 且 print 明确的 stat 失败原因，让 stage_release 永远不会拿到空 $out。
    if ! install -m 0755 "$cgo_out" "$out" 2>"$RUN_DIR/build-cgo-mv.log"; then
      printf '    [cgo-install stderr follows]\n' >&2
      sed 's/^/    /' "$RUN_DIR/build-cgo-mv.log" >&2 || true
      rm -f "$cgo_out"
      die "failed to install $cgo_out -> $out (see $RUN_DIR/build-cgo-mv.log)"
    fi
    rm -f "$cgo_out"
  fi
  [[ -s "$out" ]] || die "backend build produced no output at $out"
  printf '%s\n' "$out"
}

build_frontend() {
  (( SKIP_FRONTEND )) && return 0
  need_cmd node
  if [[ -f "$PROJECT_ROOT/web/package.json" ]]; then
    if [[ -x "$PROJECT_ROOT/web/node_modules/.bin/vite" ]]; then (cd "$PROJECT_ROOT/web" && npm run build) >/dev/null; else warn 'web/node_modules is missing; retaining existing web/dist'; fi
  fi
}

bump_local_version() {
  [[ -f "$SCRIPT_DIR/bump-version.sh" ]] || die "version bump script not found: $SCRIPT_DIR/bump-version.sh"
  LOCK_BUILD_TARGET=local
  lock_acquire_build || die 'shared build lock is held; retry local deployment later'
  DEPLOY_BUILD_LOCK_HELD=1
  log 'bump version (auto +1)'
  if ! bash "$SCRIPT_DIR/bump-version.sh" 2>&1 | sed 's/^/    /'; then
    die 'version bump failed'
  fi
  RELEASE_VERSION="$(dl_release_name "$VERSION_JSON")"
  log "release allocated: $RELEASE_VERSION"
}

ensure_release_available() {
  # 薄包装：把 deploy-local.sh 的 BIN_DIR/RELEASE_VERSION/dl_active_version
  # 状态透传给 dl_ensure_release_available（lib 层函数，详见
  # deploy-local-lib.sh 中的契约注释）。lib 版本是测试友好的纯函数，
  # 这里只是把脚本级状态和它接通：所有真正的逻辑、fail-closed 边界和
  # self-heal 分支都在 lib 里单测覆盖。
  dl_ensure_release_available "${1:-$BIN_DIR/$RELEASE_VERSION}" "$(dl_active_version 2>/dev/null || printf '')"
}


stage_release() {
  local binary="$1" bundle="$BIN_DIR/$RELEASE_VERSION"
  ensure_release_available "$bundle"
  # dl_stage_release 的失败路径会主动 rm -f SHA256SUMS（2026-09-05
  # 陈旧二进制事故的 set -e 怪癖防御）。一旦失败，必须 fail closed 且
  # 给出明确错误，让后续 dl_verify_release 不会撞上 bash 自带的
  # 'SHA256SUMS: No such file or directory' 这种让人误以为是脚本 bug
  # 的晦涩信息。
  dl_stage_release "$bundle" "$binary" "$PROJECT_ROOT/web/dist" "$VERSION_JSON" "$VERSION_FILE" "$RELEASE_VERSION" \
    || die "release staging failed at $bundle — bundle is incomplete (no SHA256SUMS); the binary install or checksum generation failed. Check the [deploy-local] logs above for the failing step."
  dl_verify_release "$bundle" || die 'release checksum verification failed'
  printf '%s\n' "$bundle"
}

write_instance_env() {
  local bundle="$1" port="$2"; export LLM_GATEWAY_VERSION_FILE="$bundle/version.json"
  dl_write_env "$bundle/env" "$port"
  # Also covers the rollback/start recovery paths: a regenerated env without
  # the signing key would bring up a gateway that breaks admin login at
  # cutover instead of failing loudly here.
  grep -q '^LLM_GATEWAY_SECRET_KEY=.\+$' "$bundle/env" || die "refusing to start instance :$port with an empty LLM_GATEWAY_SECRET_KEY (check .env.local or the calling environment)"
}

instance_name() { printf 'llm-gateway-local-%s\n' "$1"; }
pid_file() { printf '%s/gateway-%s.pid\n' "$RUN_DIR" "$1"; }

# Wait for a TCP port to be free after a previous container was killed.
# 2026-09-09 现象：active cutover 偶发失败，8782 verify 在 60s 内 /healthz
# 持续 connection refused；同时段 8781 candidate 一切正常。docker rm -f
# 在 macOS Docker Desktop 上偶尔不会立即释放 host port（端口处于
# TIME_WAIT 或 per-namespace 端口分配未回收），新容器 docker run 时
# -p 127.0.0.1:8782:8782 静默 bind 失败，curl /healthz 永远拿不到 200，
# verify_instance 返回 1 后被 fail-closed 当成"release 不健康"die。
# 修法：stop_instance 在 docker rm -f 之后等端口真正空闲再返回，避免
# 调用方拿一个还没释放的端口去 docker run（最多等 30s，curl 退出码 7
# = connection refused 算成功空闲信号；其他错误码继续等）。
dl_wait_port_free() {
  local port="$1" deadline=$(( $(date +%s) + 30 )) attempt=0
  while (( $(date +%s) < deadline )); do
    attempt=$(( attempt + 1 ))
    if curl -fsS --max-time 1 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
      sleep 1
      continue
    fi
    [[ $attempt -gt 1 ]] && printf '    [stop] port %s free after %d probe(s)\n' "$port" "$attempt" >&2
    return 0
  done
  printf '    [stop] warning: port %s still appears busy after 30s; start may fail with EADDRINUSE\n' "$port" >&2
  return 1
}

stop_instance() {
  local port="$1" name; name=$(instance_name "$port")
  if (( DL_DOCKER )) && docker ps -a --format '{{.Names}}' | grep -Fxq "$name"; then docker rm -f "$name" >/dev/null 2>&1 || true; fi
  local pf; pf=$(pid_file "$port")
  if [[ -f "$pf" ]]; then kill "$(cat "$pf")" 2>/dev/null || true; rm -f "$pf"; fi
  # 2026-09-09：docker rm -f 之后立即 docker run 同端口偶发 EADDRINUSE
  # （macOS Docker Desktop 端口分配/TIME_WAIT 释放慢）。verify_instance
  # 拿到 connection refused 就会在 60s 后 die，破坏 active cutover。
  # stop_instance 等端口真正空闲再返回，让 start_instance 的 docker run
  # 一定拿到端口。仅在 Docker 模式下有意义（host-mode pid_file 路径
  # 不参与 port 释放）。
  if (( DL_DOCKER )); then dl_wait_port_free "$port" || true; fi
}

start_instance() {
  local bundle="$1" port="$2" name; name=$(instance_name "$port")
  stop_instance "$port"
  write_instance_env "$bundle" "$port"
  # 2026-09-17：2114 cutover 复盘 — active 端口 /readyz 60s 内反复出现
  # database:null（openDBWithBootRetry 20s 预算耗尽，gateway 静默降级
  # 兼容模式、readyz 永久 not_ready、自愈无门）。根因：上一轮 active
  # 容器刚停时 PG 仍在恢复窗口，docker run 立刻把 gateway 拉起，gateway
  # 自身 20s 内连不上 PG → h.db == nil → readyz 锁 not_ready。脚本侧
  # 的 cutover 重试只在容器重启层面绕，无法绕过 gateway 进程内的
  # 一次性 openDBWithBootRetry 决策。修法：start_instance 顶层先做 PG
  # pre-flight，从 gateway 视角确认 DSN 可用 + SELECT 1 通过，再起容器。
  # pre-flight 超时（90s）默认仅 warn 不 fail：仍有 LLM_GATEWAY_DB_BOOT_RETRY_SECONDS
  # 兜底（dl_write_env 已上调到 90s）。设置 DL_PG_PREFLIGHT_REQUIRED=1
  # 切到 fail-closed 模式：超时直接 die，阻止容器启动进入 database:null
  # 死区；默认 0 保持向后兼容，245 preprod 验证后才会开 1。
  dl_wait_pg_isready || true
  if (( DL_DOCKER )); then
    local image="${LLM_GATEWAY_RUNTIME_IMAGE:-alpine:3.22}" image_file="$RUN_DIR/runtime.Dockerfile"
    docker image inspect "$image" >/dev/null 2>&1 || die "Docker runtime image $image is not available (set LLM_GATEWAY_RUNTIME_IMAGE)"
    cat > "$image_file" <<'EOF'
ARG BASE_IMAGE=alpine:3.22
FROM ${BASE_IMAGE}
COPY gateway /opt/llm-gateway-go/gateway
COPY web /opt/llm-gateway-go/web
COPY version.json /opt/llm-gateway-go/version.json
WORKDIR /opt/llm-gateway-go
ENTRYPOINT ["/opt/llm-gateway-go/gateway"]
EOF
    docker build -q --build-arg "BASE_IMAGE=$image" -f "$image_file" -t "kx-llm-gateway-local:${RELEASE_VERSION}" "$bundle" >/dev/null
    local runtime_env="$RUN_DIR/${name}.env"
    cp "$bundle/env" "$runtime_env"; chmod 0600 "$runtime_env"
    sed -i.bak -E \
      -e 's#@(127\.0\.0\.1|localhost):#@host.docker.internal:#g' \
      -e 's#^(LLM_GATEWAY_REDIS_ADDR=)(127\.0\.0\.1|localhost):#\1host.docker.internal:#g' \
      "$runtime_env"
    rm -f "$runtime_env.bak"
    local gateway_net_args=(--add-host host.docker.internal:host-gateway)
    local redis_network=""
    if [[ -n "${DL_REDIS_CONTAINER:-}" ]]; then
      for net in shared-infra nbjl_default; do
        if docker network inspect "$net" >/dev/null 2>&1 \
          && docker network inspect "$net" --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | grep -qw "$DL_REDIS_CONTAINER"; then
          redis_network="$net"; break
        fi
      done
    fi
    if [[ -n "$redis_network" ]]; then
      gateway_net_args+=(--network "$redis_network")
      sed -i.bak -E 's#^LLM_GATEWAY_REDIS_ADDR=.*#LLM_GATEWAY_REDIS_ADDR='"${DL_REDIS_CONTAINER}"':6379#' "$runtime_env"
      rm -f "$runtime_env.bak"
    fi
    # In Docker mode dl_write_env points LOG_DIR/RAW_LOG_DIR/ATTACHMENT_DIR/
    # BACKUP_DIR at /opt/llm-gateway-go/<dir> inside the container. Without
    # bind mounts those writes land in the container's ephemeral writable
    # layer: invisible on the host and wiped by the next deploy (docker rm -f
    # in stop_instance). Mount the host runtime-root state dirs at the exact
    # container paths the env references (incident 2026-09-05: attachments
    # persisted only inside the container and ~/kaixuan/llm-gateway-go/
    # attachments stayed empty).
    local gateway_bind_args=() state_dir
    for state_dir in attachments logs raw-logs backups; do
      mkdir -p "$ROOT_DIR/$state_dir"
      gateway_bind_args+=(-v "$ROOT_DIR/$state_dir:/opt/llm-gateway-go/$state_dir")
    done
    docker run -d --name "$name" --restart unless-stopped "${gateway_net_args[@]}" "${gateway_bind_args[@]}" --env-file "$runtime_env" -e "LLM_GATEWAY_LISTEN=:${port}" -e "LLM_GATEWAY_VERSION_FILE=/opt/llm-gateway-go/version.json" -p "127.0.0.1:${port}:${port}" "kx-llm-gateway-local:${RELEASE_VERSION}" >/dev/null
  else
    local pf; pf=$(pid_file "$port"); mkdir -p "$RUN_DIR" "$LOG_DIR"
    # Parse instead of source: env values are written verbatim for docker
    # --env-file and may contain shell metacharacters (e.g. '&' in the admin
    # password), which `source` would treat as shell syntax.
    dl_load_env_file "$bundle/env"
    LLM_GATEWAY_VERSION_FILE="$bundle/version.json" LLM_GATEWAY_LISTEN=":$port" \
      nohup "$bundle/gateway" >>"$LOG_DIR/gateway-${port}.log" 2>&1 &
    printf '%s\n' "$!" > "$pf"; chmod 0600 "$pf"
  fi
}

verify_instance() {
  local port="$1" bundle="$2" body expected expected_seq readyz_body
  expected=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("version", ""))' "$bundle/version.json")
  expected_seq=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("build_seq", ""))' "$bundle/version.json")
  # 2026-09-09：active cutover 偶发 verify 失败（"previous release was
  # restarted"），但 verify_instance 只 return 1 不告诉操作员哪一步超时
  # —— /healthz / /readyz / /version / version 一致性 都是候选原因，
  # 仅凭"failed"无法定位是端口 TIME_WAIT 释放慢、容器启动慢、还是
  # bundle 和 binary 不一致。每步都打 stderr 让操作员立刻看到断点。
  if ! dl_wait_http "http://127.0.0.1:${port}/healthz" "$HEALTH_TIMEOUT"; then
    printf '    [verify] %s:%s: /healthz did not return 200 within %ss\n' "$port" "$bundle" "$HEALTH_TIMEOUT" >&2
    return 1
  fi
  if ! dl_wait_http "http://127.0.0.1:${port}/readyz" "$HEALTH_TIMEOUT"; then
    # 2026-09-14：/readyz 是 DB+Redis 双 ping 严格门，超时时抓一次响应体
    # （不含 -f，503 也收），立刻分辨是 database 还是 redis 不通，还是端
    # 口根本无人监听——不再只留一句超时让操作员盲猜。
    readyz_body=$(curl -sS --max-time 3 "http://127.0.0.1:${port}/readyz" 2>&1 || true)
    printf '    [verify] %s:%s: /readyz did not return 200 within %ss (last body: %s)\n' "$port" "$bundle" "$HEALTH_TIMEOUT" "${readyz_body:-<endpoint unreachable>}" >&2
    return 1
  fi
  if ! body=$(curl -fsS --max-time 5 "http://127.0.0.1:${port}/version" 2>&1); then
    printf '    [verify] %s:%s: /version curl failed within 5s: %s\n' "$port" "$bundle" "$body" >&2
    return 1
  fi
  if ! VERSION_BODY="$body" EXPECTED="$expected" EXPECTED_BUILD_SEQ="$expected_seq" python3 - <<'PY'
import json, os, sys
x=json.loads(os.environ['VERSION_BODY'])
if x.get('version') != os.environ['EXPECTED']:
    print('version mismatch', x, os.environ['EXPECTED'], file=sys.stderr); raise SystemExit(1)
if str(x.get('build_seq')) != os.environ['EXPECTED_BUILD_SEQ']:
    print('build_seq mismatch', x, file=sys.stderr); raise SystemExit(1)
PY
  then
    printf '    [verify] %s:%s: /version body did not match bundle metadata\n' "$port" "$bundle" >&2
    return 1
  fi
  printf '    [verify] %s:%s: ok (version=%s build_seq=%s)\n' "$port" "$bundle" "$expected" "$expected_seq" >&2
  return 0
}

record_success() { dl_record_verify "$ROOT_DIR" "$DL_DB_MODE" "$DL_REDIS_MODE" "$RELEASE_VERSION" "$(dl_active_port)"; }

# A gateway deployed without LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY cannot
# decrypt stored provider credentials (incident 2026-09-05: every apikey on
# provider 587 showed decrypt_failed). The key travels through dl_write_env,
# but a deploy from a checkout without .env.local still produces an empty
# value — so fail closed when the target DB already holds ciphertext, and let
# a genuinely fresh DB proceed on the SHA-256(SECRET_KEY) fallback.
gate_credential_encryption_key() {
  [[ -n "${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}" ]] && return 0
  local count
  count=$(psql_query "SELECT count(*) FROM credentials WHERE secret_ciphertext IS NOT NULL AND secret_ciphertext <> ''" 2>/dev/null) || count=''
  if [[ "$count" =~ ^[0-9]+$ ]] && (( count > 0 )); then
    die "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY is empty but the database holds $count encrypted credential(s); the new gateway would be unable to decrypt any of them. Import .env.local or export the 245-synced SSOT key before deploying (incident 2026-09-05, provider 587)"
  fi
  warn 'LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY is empty and no stored credentials exist yet; AES keys will derive from SHA-256(LLM_GATEWAY_SECRET_KEY)'
}

# Post-cutover decrypt smoke: logs into the admin API and asserts the pinned
# (or top-credential) providers still decrypt. Catches a keyring/binary
# regression that healthz/readyz cannot see (245 incident 2026-09-04).
dl_local_exec() { bash -c "$1"; }
smoke_credential_decrypt() {
  local port="$1" env_file="$2"
  deploy_verify_credential_decrypt dl_local_exec "$port" "$env_file"
}

# Local counterpart of deploy-seamless.sh step 9.1 (env → users 同步): before
# the decrypt smoke, rewrite users.<admin>'s bcrypt hash from the exact env
# file the smoke authenticates with. handleLogin only checks the users table
# and never falls back to env (admin/auth.go), so an out-of-band hash edit
# (2026-09-07: a stale bundle-env password was hashed into users.admin) leaves
# every later deploy dead-locked behind a 401 the gateway cannot self-heal.
# Opt out with DEPLOY_SYNC_ADMIN_PASSWORD=false.
sync_admin_password_from_env() {
  local env_file="$1" port="$2" user pw hash old_hash backup=""
  # Read first matching line for each key, then take everything after the
  # first '=' (cut -d= -f2-) so a value containing '=' or trailing spaces
  # survives intact. grep -m1 stops at the first hit so a duplicate assignment
  # downstream cannot silently override the admin user/password we use here.
  user=$(grep -m1 '^LLM_GATEWAY_ADMIN_USER=' "$env_file" 2>/dev/null | cut -d= -f2-)
  pw=$(grep -m1 '^LLM_GATEWAY_ADMIN_PASSWORD=' "$env_file" 2>/dev/null | cut -d= -f2-)
  # Strip surrounding single/double quotes that dotenv style permits.
  user=${user%\"}; user=${user#\"}; user=${user%\'}; user=${user#\'}
  pw=${pw%\"}; pw=${pw#\"}; pw=${pw%\'}; pw=${pw#\'}
  if [[ -z "$user" || -z "$pw" ]]; then
    warn 'LLM_GATEWAY_ADMIN_USER/PASSWORD empty in env; skipping admin password sync'
    return 0
  fi
  user=${user//\'/\'\'}
  hash=$( (cd "$PROJECT_ROOT" && printf '%s' "$pw" | go run scripts/ops/bcrypt-hash.go) ) \
    || { warn 'bcrypt hashing failed; skipping admin password sync'; return 0; }
  old_hash=$(psql_query "SELECT password_hash FROM users WHERE username='$user'" 2>/dev/null) || old_hash=''
  if [[ -n "$old_hash" ]]; then
    backup="$RUN_DIR/admin-password-hash.$(date +%Y%m%dT%H%M%S).bak"
    # %s writes the bcrypt hash verbatim — no $ expansion, no trailing newline.
    printf '%s' "$old_hash" > "$backup"; printf '\n' >> "$backup"
    chmod 0600 "$backup"
  fi
  if ! psql_query "UPDATE users SET password_hash='$hash', must_change_password=false, updated_at=now() WHERE username='$user'" >/dev/null; then
    warn 'admin password sync UPDATE failed; continuing (decrypt smoke remains the gate)'
    return 0
  fi
  log "admin password synced env → users (user=$user, prior hash: ${backup:-none})"
  local probe
  probe=$(python3 - "$port" "$user" "$pw" <<'PY'
import json, sys, urllib.error, urllib.request
port, user, pw = sys.argv[1], sys.argv[2], sys.argv[3]
request = urllib.request.Request(
    'http://127.0.0.1:%s/api/auth/token' % port,
    data=json.dumps({'username': user, 'password': pw}).encode(),
    headers={'Content-Type': 'application/json'}, method='POST')
try:
    urllib.request.urlopen(request, timeout=5)
    print('ok')
except urllib.error.HTTPError as error:
    print(error.code)
except Exception:
    print('000')
PY
)
  [[ "$probe" == "ok" ]] || warn "admin login probe after sync returned ${probe:-000} (continuing; decrypt smoke remains the gate)"
}

migrate_database() {
  apply_schema_if_empty
  export LLM_GATEWAY_DATABASE_URL DATABASE_URL
  local migrate_log="$RUN_DIR/gateway-migrate.log"
  local revision_log="$RUN_DIR/db-revision-sequence.log"
  mkdir -p "$RUN_DIR"

  # Apply the explicitly scoped repair set before the application migration
  # path. This covers SQL files that db.Open() does not scan, while preserving
  # the existing Go ensure chain for startup compatibility.
  if ! LLM_GATEWAY_PG_CONTAINER="${DL_PG_CONTAINER:-}" \
       LLM_GATEWAY_PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}" \
       LLM_GATEWAY_PG_PASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" \
       LLM_GATEWAY_PG_DATABASE="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" \
       bash "$PROJECT_ROOT/scripts/apply-db-revision-sequence.sh" >"$revision_log" 2>&1; then
    printf '[deploy-local] error: database revision sequence failed; full output: %s\n' "$revision_log" >&2
    sed 's/^/    /' "$revision_log" >&2 || true
    return 1
  fi

  if [[ "${DL_DOCKER:-0}" == 1 ]]; then
    if (cd "$PROJECT_ROOT" && go run ./cmd/gateway migrate >"$migrate_log"); then
      return 0
    fi
  else
    if "$1" migrate >"$migrate_log"; then
      return 0
    fi
  fi
  printf '[deploy-local] error: database migration failed; structured report follows (full output: %s)\n' "$migrate_log" >&2
  if [[ -s "$migrate_log" ]]; then
    sed 's/^/    /' "$migrate_log" >&2
  fi
  return 1
}

deploy() {
  # A gateway deployed without a JWT signing key cannot issue admin sessions
  # ("token generation failed") and rejects every previously issued token.
  # Fail before the build instead of discovering it after cutover.
  [[ -n "${LLM_GATEWAY_SECRET_KEY:-}" ]] || die 'LLM_GATEWAY_SECRET_KEY is empty — refusing to deploy a gateway that cannot sign admin sessions (check .env.local or the calling environment)'
  bump_local_version
  ensure_release_available
  ensure_resources
  if [[ "${CLEANUP_DOWNLOADS}" == 1 ]]; then
    DL_CLEANUP_DOWNLOADS=1 dl_cleanup_legacy_downloads
  else
    DL_CLEANUP_DOWNLOADS=0 dl_cleanup_legacy_downloads
  fi
  local binary bundle active_port candidate_port active_bundle
  binary=$(build_backend)
  build_frontend
  migrate_database "$binary"
  gate_credential_encryption_key
  bundle=$(stage_release "$binary")
  release_build_lock
  active_port=$(dl_active_port); candidate_port=$(dl_candidate_port)
  active_bundle="$BIN_DIR/current"
  start_instance "$bundle" "$candidate_port"
  if ! verify_instance "$candidate_port" "$bundle"; then
    stop_instance "$candidate_port"; die "candidate failed health/readiness/version gates; active release was preserved"
  fi
  if [[ "${DEPLOY_SYNC_ADMIN_PASSWORD:-true}" == "true" ]]; then
    sync_admin_password_from_env "$bundle/env" "$candidate_port"
  fi
  if ! smoke_credential_decrypt "$candidate_port" "$bundle/env"; then
    stop_instance "$candidate_port"; die "candidate failed credential decrypt smoke; active release was preserved"
  fi
  if [[ -n "${LLM_GATEWAY_UPSTREAM_FILE:-}" && -f "$LLM_GATEWAY_UPSTREAM_FILE" ]]; then
    printf 'server 127.0.0.1:%s;\n' "$candidate_port" > "$LLM_GATEWAY_UPSTREAM_FILE"
    cp "$LLM_GATEWAY_UPSTREAM_FILE" "$RUN_DIR/active-upstream.conf"
  else
    warn 'no local proxy configured; using controlled restart (not zero-downtime)'
    # 2026-09-14：2102 cutover 首启在候选端口 verify 全绿、同一 bundle 在
    # active 端口 /readyz 60s 不 ready 一次（瞬态首启窗口，非 release 问
    # 题），单次失败即回滚把可恢复抖动变成部署失败。给一次完整的重启重
    # 试再判失败；配合 verify_instance 的 readyz 响应体输出，真依赖故障
    # 也能从日志直接看出 database/redis 哪一侧不通。
    # 2026-09-17：2114 cutover 在 2 次重试内均落 database:null（gateway
    # 进程内 openDBWithBootRetry 耗尽 20s 默认预算），即使起容器侧 PG
    # 已可达，gateway 自身仍可能因启动时序问题落入兼容模式。脚本侧把
    # 重试提到 3 次并在循环间隙 sleep 5s，让 container 完全清理、旧
    # gateway 进程的 openDBWithBootRetry 决策期被覆盖；同时
    # dl_wait_pg_isready 已在 start_instance 入口先 SELECT 1。
    local cutover_ok=0 cutover_attempt
    for cutover_attempt in 1 2 3; do
      (( cutover_attempt > 1 )) && printf '    [cutover] readiness gate failed; retry %d/3 (controlled restart)\n' "$cutover_attempt" >&2 && sleep 5
      stop_instance "$active_port"
      start_instance "$bundle" "$active_port"
      if verify_instance "$active_port" "$bundle"; then cutover_ok=1; break; fi
    done
    if (( ! cutover_ok )); then
      stop_instance "$active_port"; [[ -e "$active_bundle" ]] && start_instance "$active_bundle" "$active_port"; die 'active cutover failed; previous release was restarted'
    fi
    if ! smoke_credential_decrypt "$active_port" "$bundle/env"; then
      stop_instance "$active_port"
      [[ -e "$active_bundle" ]] && start_instance "$active_bundle" "$active_port"
      die 'credential decrypt smoke failed after cutover; previous release was restarted'
    fi
    stop_instance "$candidate_port"
    candidate_port="$active_port"
  fi
  dl_atomic_switch "$RELEASE_VERSION"
  printf '%s\n' "$candidate_port" > "$RUN_DIR/active-port"
  chmod 0600 "$RUN_DIR/active-port"
  dl_mark_verified "$bundle"
  printf 'active=%s\n' "$RELEASE_VERSION" > "$RUN_DIR/deployment-state"
  record_success
}

verify() {
  ensure_resources
  local port; port=$(dl_active_port); [[ -L "$BIN_DIR/current" ]] || die 'no active release'
  verify_instance "$port" "$BIN_DIR/current" || die 'active release failed verification'
  record_success
}

status() {
  dl_detect_resources
  detect_existing_containers
  printf 'root=%s\nactive=%s\nactive_port=%s\ndocker=%s\ndatabase=%s\nredis=%s\nminimal_mode=%s\n' "$ROOT_DIR" "$(dl_active_version)" "$(dl_active_port)" "$DL_DOCKER" "$DL_DB_MODE" "$DL_REDIS_MODE" "$MINIMAL_DEPLOY"
  if [[ -n "${DL_REDIS_CONTAINER:-}" ]]; then
    printf 'redis_container=%s\n' "$DL_REDIS_CONTAINER"
  fi
  if (( DL_DOCKER )); then docker ps --filter 'name=llm-gateway-local-' --format 'container={{.Names}} status={{.Status}}' || true; fi
}

rollback() {
  ensure_resources
  local current target bundle port
  current=$(dl_active_version); target="${1:-}"
  if [[ -z "$target" ]]; then
    for bundle in "$BIN_DIR"/*; do
      [[ -d "$bundle" && -f "$bundle/deployment.json" ]] || continue
      grep -q '"verified"[[:space:]]*:[[:space:]]*true' "$bundle/deployment.json" || continue
      [[ "$(basename "$bundle")" == "$current" ]] && continue
      target=$(basename "$bundle"); break
    done
  fi
  [[ -n "$target" ]] || die 'no verified rollback release available'
  bundle="$BIN_DIR/$target"; [[ -d "$bundle" ]] || die "rollback release not found: $target"
  grep -q '"verified"[[:space:]]*:[[:space:]]*true' "$bundle/deployment.json" || die 'rollback target is not verified'
  port=$(dl_active_port); start_instance "$bundle" "$port"; verify_instance "$port" "$bundle" || die 'rollback target failed verification'
  dl_atomic_switch "$target"; printf '%s\n' "$port" > "$RUN_DIR/active-port"; printf 'rollback=%s\n' "$target" > "$RUN_DIR/deployment-state"; record_success
}

start() { ensure_resources; local b="$BIN_DIR/current"; [[ -d "$b" ]] || die 'no active release'; start_instance "$b" "$(dl_active_port)"; verify; }
stop() { dl_detect_resources; stop_instance 8782; stop_instance 8781; stop_instance 8783; }
logs() { exec tail -f "$LOG_DIR/gateway-$(dl_active_port).log"; }

case "$ACTION" in
  deploy) deploy ;;
  status) status ;;
  verify) verify ;;
  rollback) rollback "${1:-}" ;;
  start) start ;;
  stop) stop ;;
  logs) logs ;;
esac
