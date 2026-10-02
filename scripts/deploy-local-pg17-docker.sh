#!/usr/bin/env bash
# =============================================================================
# deploy-local-pg17-docker.sh — 在本机 Docker 中安装 PostgreSQL 17（gateway 数据库）
#
# 定位：llm-gateway-go 本地系统化部署（scripts/deploy-local-sys.sh）的数据库前置。
# gateway 本体直接安装在系统上（非 Docker），只有 pg17 跑在 Docker 里。
#
# 前置：Docker 已在本机安装并可运行（由独立任务负责安装 Docker；
#       本脚本只检测，不安装 Docker。Docker 未就绪时用 --wait 轮询等待）。
#
# 用法：
#   bash scripts/deploy-local-pg17-docker.sh              # 安装/复用 pg17 容器
#   bash scripts/deploy-local-pg17-docker.sh --wait 1800  # 最多等 30min Docker 就绪
#   bash scripts/deploy-local-pg17-docker.sh --status     # 查看容器与库状态
#
# 敏感信息（postgres 密码）不写入本脚本/仓库：
#   - 优先读取环境变量 LLM_GATEWAY_PG_PASSWORD（或既有容器的 POSTGRES_PASSWORD）
#   - 未设置时生成强随机密码，并写入 $HOME/.llm-gateway-go/pg17.env（仅本用户）
#   - deploy-local-sys.sh 会从该文件读取密码组装 LLM_GATEWAY_DATABASE_URL
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 可调参数（非敏感项可用环境变量覆盖；密码一律走环境变量/密钥文件）──
PG_CONTAINER="${LLM_GATEWAY_PG_CONTAINER:-kx-pg17}"
PG_IMAGE="${LLM_GATEWAY_PG_IMAGE:-postgres:17-alpine}"
PG_PORT="${LLM_GATEWAY_PG_PORT:-5432}"
PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}"
PG_DB="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}"
PG_VOLUME="${LLM_GATEWAY_PG_VOLUME:-kx-pg17-data}"
PG_DATA_DIR="${LLM_GATEWAY_PG_DATA_DIR:-}"   # 设置后用 bind mount（保留在项目外）
SECRETS_DIR="${LLM_GATEWAY_SYS_SECRETS_DIR:-$HOME/.llm-gateway-go}"
SECRETS_FILE="$SECRETS_DIR/pg17.env"

log()  { printf '[pg17] %s\n' "$*"; }
warn() { printf '[pg17] WARN: %s\n' "$*" >&2; }
die()  { printf '[pg17] FATAL: %s\n' "$*" >&2; exit 64; }

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
}

WAIT_SECONDS=0
DO_STATUS=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --wait)   WAIT_SECONDS="${2:?--wait SECONDS}"; shift 2 ;;
    --status) DO_STATUS=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

# ── 等待 Docker 就绪（Docker 由另外的任务安装）─────────────────────
docker_ready() {
  command -v docker >/dev/null 2>&1 || return 1
  docker info >/dev/null 2>&1
}

if ! docker_ready; then
  if (( WAIT_SECONDS == 0 )); then
    die "Docker 未就绪（未安装或守护进程未运行）。请先完成 Docker 安装任务后重试；
       或使用 --wait SECONDS 轮询等待：bash scripts/deploy-local-pg17-docker.sh --wait 1800"
  fi
  log "等待 Docker 就绪（最长 ${WAIT_SECONDS}s，每 15s 探测一次）..."
  waited=0
  while ! docker_ready; do
    (( waited >= WAIT_SECONDS )) && die "等待超时：Docker 在 ${WAIT_SECONDS}s 内未就绪"
    sleep 15; waited=$((waited + 15))
    printf '.'
  done
  printf '\n'
fi
log "Docker 就绪: $(docker --version 2>/dev/null | head -1)"

if (( DO_STATUS )); then
  docker ps -a --filter "name=^/${PG_CONTAINER}$" --format 'container: {{.Names}}  {{.Status}}  {{.Image}}'
  if docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" -d "$PG_DB" >/dev/null 2>&1; then
    log "database: ${PG_DB} ready (user ${PG_USER})"
  fi
  exit 0
fi

# ── 密码解析：env > 既有容器 > 随机生成并落盘密钥文件 ──────────────
load_container_pg_pass() {
  docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null || true
}

PG_PASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}"
PG_PASSWORD_SOURCE="env LLM_GATEWAY_PG_PASSWORD"
if [[ -z "$PG_PASSWORD" ]] && docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
  PG_PASSWORD="$(load_container_pg_pass)"
  PG_PASSWORD_SOURCE="existing container ${PG_CONTAINER}"
fi
if [[ -z "$PG_PASSWORD" && -f "$SECRETS_FILE" ]]; then
  # shellcheck source=/dev/null
  PG_PASSWORD="$(set -a; source "$SECRETS_FILE"; printf '%s' "${LLM_GATEWAY_PG_PASSWORD:-}")"
  PG_PASSWORD_SOURCE="$SECRETS_FILE"
fi
if [[ -z "$PG_PASSWORD" ]]; then
  mkdir -p "$SECRETS_DIR"
  if command -v openssl >/dev/null 2>&1; then
    PG_PASSWORD="$(openssl rand -base64 24 | tr '+/' '-_' | tr -d '=')"
  else
    PG_PASSWORD="$(head -c 24 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
  fi
  PG_PASSWORD_SOURCE="generated -> $SECRETS_FILE"
  {
    printf '# PostgreSQL 17 (Docker %s) 密钥 — 由 deploy-local-pg17-docker.sh 生成\n' "$PG_CONTAINER"
    printf '# 本文件含敏感信息：不要提交到任何仓库；ACL 限定当前用户\n'
    printf 'export LLM_GATEWAY_PG_USER=%q\n' "$PG_USER"
    printf 'export LLM_GATEWAY_PG_DATABASE=%q\n' "$PG_DB"
    printf 'export LLM_GATEWAY_PG_PORT=%q\n' "$PG_PORT"
    printf 'export LLM_GATEWAY_PG_CONTAINER=%q\n' "$PG_CONTAINER"
    printf 'export LLM_GATEWAY_PG_PASSWORD=%q\n' "$PG_PASSWORD"
  } > "$SECRETS_FILE"
  chmod 600 "$SECRETS_FILE" 2>/dev/null || true
  # Windows NTFS：进一步收紧为仅当前用户可读（Git Bash chmod 在 NTFS 上有限）
  if command -v icacls >/dev/null 2>&1; then
    win_path=$(cygpath -w "$SECRETS_FILE" 2>/dev/null || echo "$SECRETS_FILE")
    icacls "$win_path" /inheritance:r /grant:r "$USERNAME:R" >/dev/null 2>&1 || true
  fi
fi
log "PG 密码来源: $PG_PASSWORD_SOURCE"

# ── 容器：已存在则复用并确保健康；不存在则创建 ────────────────────
pg_healthy() {
  docker exec -e PGPASSWORD="$PG_PASSWORD" "$PG_CONTAINER" \
    pg_isready -U "$PG_USER" -d "$PG_DB" >/dev/null 2>&1
}

# ensure_image 拉取镜像：主源失败时按序尝试国内镜像源兜底（网络受限环境）
ensure_image() {
  docker image inspect "$PG_IMAGE" >/dev/null 2>&1 && { log "镜像 $PG_IMAGE 已存在"; return 0; }
  local mirrors=("$PG_IMAGE" \
    "docker.1ms.run/library/${PG_IMAGE#docker.io/}" \
    "docker.m.daocloud.io/library/${PG_IMAGE#docker.io/}" \
    "dockerproxy.net/library/${PG_IMAGE#docker.io/}")
  local img
  for img in "${mirrors[@]}"; do
    log "拉取镜像 $img ..."
    if docker pull "$img" >/dev/null 2>&1; then
      # 统一打上期望的本地 tag，容器引用不变
      [[ "$img" != "$PG_IMAGE" ]] && docker tag "$img" "$PG_IMAGE" && docker rmi "$img" >/dev/null 2>&1 || true
      log "  镜像就绪: $PG_IMAGE"
      return 0
    fi
    warn "  拉取 $img 失败，尝试下一个源"
  done
  die "所有镜像源均拉取失败：$PG_IMAGE（可手动 docker pull 后重跑）"
}

if docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
  state="$(docker inspect -f '{{.State.Status}}' "$PG_CONTAINER")"
  log "容器 $PG_CONTAINER 已存在（状态: $state），复用"
  if [[ "$state" != "running" ]]; then
    log "启动既有容器..."
    docker start "$PG_CONTAINER" >/dev/null
  fi
else
  ensure_image
  log "创建容器 $PG_CONTAINER（image=$PG_IMAGE, port=$PG_PORT, volume=$PG_VOLUME）..."
  mkdir -p "$SECRETS_DIR"
  mount_args=()
  if [[ -n "$PG_DATA_DIR" ]]; then
    mkdir -p "$PG_DATA_DIR"
    mount_args=(-v "$(cygpath -w "$PG_DATA_DIR" 2>/dev/null || echo "$PG_DATA_DIR"):/var/lib/postgresql/data")
  else
    mount_args=(-v "$PG_VOLUME:/var/lib/postgresql/data")
  fi
  docker run -d --name "$PG_CONTAINER" \
    --restart unless-stopped \
    -p "127.0.0.1:${PG_PORT}:5432" \
    -e POSTGRES_USER="$PG_USER" \
    -e POSTGRES_PASSWORD="$PG_PASSWORD" \
    -e POSTGRES_DB="$PG_DB" \
    "${mount_args[@]}" \
    "$PG_IMAGE" >/dev/null
fi

# ── 健康等待 + 数据库确认 ─────────────────────────────────────────
log "等待 PostgreSQL 就绪..."
for i in $(seq 1 60); do
  if pg_healthy; then
    log "PostgreSQL 就绪（容器 $PG_CONTAINER, ${PG_USER}@${PG_DB}:${PG_PORT}）"
    break
  fi
  (( i == 60 )) && die "PostgreSQL 60 次探测后仍未就绪，查看日志: docker logs $PG_CONTAINER"
  sleep 2
done

# 容器初始化时 POSTGRES_DB 已建库；此处兜底确认（幂等）
if ! docker exec -e PGPASSWORD="$PG_PASSWORD" "$PG_CONTAINER" \
     psql -X -Atqc "SELECT 1 FROM pg_database WHERE datname='$PG_DB'" | grep -q 1; then
  log "创建数据库 $PG_DB ..."
  docker exec -e PGPASSWORD="$PG_PASSWORD" "$PG_CONTAINER" \
    psql -X -U "$PG_USER" -d postgres -c "CREATE DATABASE \"$PG_DB\" OWNER \"$PG_USER\";" >/dev/null
fi

log "完成。密钥文件: $SECRETS_FILE"
log "下一步: bash scripts/deploy-local-sys.sh deploy"
