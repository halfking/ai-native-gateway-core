#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/user/install-docker.sh
# SYNC_POLICY: 修改本脚本时同步改 maintain 对应位置。本仓库只维护 service identity (image 名 / container 名 / 端口)。
# ADAPTATIONS: 加 loong64 严格门（CI 默认不发 loong64 artifact）。

# install-docker.sh — 用 Docker Compose 部署最新网关（amd64/arm64 + 可选 pg17+circus/citus）。
# install-docker.sh — 用 Docker Compose 部署最新网关（amd64/arm64 + 可选 pg17+circus/citus）。
#
# Env:
#   MAINTAIN_BASE   API 根
#   CHANNEL         stable
#   COMPOSE_DIR     写出目录（默认 ./llm-gateway-docker）
#   IMAGE_DIR       镜像 tar 下载目录（默认 $COMPOSE_DIR/images）
#   INCLUDE_DB      1=写入 db 服务
#   DB_IMAGE        覆盖数据库镜像；设置后不再尝试下载/回退
#   GATEWAY_IMAGE   覆盖网关镜像；设置后不再要求本地存在 tar
#   FETCH_IMAGES    1(默认)=从 API 发现并下载 docker 镜像 tar；0=纯离线
#   LOAD_IMAGE_TAR  若设置路径，则 docker load -i 该 tar（网关镜像），优先于下载
#   LOAD_DB_TAR     若设置路径，则 docker load -i 该 tar（pg17-circus），优先于下载
#   ALLOW_DB_IMAGE_FALLBACK  1(默认)=pg17-circus 不可得时回退 postgres:17-alpine；0=直接失败
#   DRY_RUN         1=只写文件（不下载、不 docker load、不 compose up）
#   NO_INTERACTIVE  1=禁用交互提示（CI/管道）
#
# 配置解析层级（与 install-host.sh / upgrade.sh 一致）：
#   1) 环境变量  2) ~/.kxmaint/config  3) 交互回退（仅 tty+NO_INTERACTIVE!=1+DRY_RUN!=1）  4) default
set -euo pipefail

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"

# 与 install-host.sh / upgrade.sh 共用同一份解析逻辑；不 source config 文件
# 以防注入，仅按 KEY=VALUE 行读取；已显式 export 的环境变量不被 config 覆盖。
load_config_file() {
  [[ -f "$KXMAINT_CONFIG" ]] || return 0
  while IFS= read -r line; do
    case "$line" in
      '#'*|'') continue ;;
    esac
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    local k="${BASH_REMATCH[1]}" v="${BASH_REMATCH[2]}"
    if [[ -z "${!k:-}" ]]; then
      printf -v "$k" '%s' "$v"
    fi
  done < "$KXMAINT_CONFIG"
}

save_config_file() {
  local k
  umask 077
  run_priv mkdir -p "$(dirname "$KXMAINT_CONFIG")"
  {
    printf '# kxmaint user config — written by %s on %s\n' "${0##*/}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '# DO NOT edit by hand unless you know the format.\n'
    for k in "$@"; do
      printf '%s=%s\n' "$k" "${!k}"
    done
  } > "$KXMAINT_CONFIG.tmp"
  run_priv mv "$KXMAINT_CONFIG.tmp" "$KXMAINT_CONFIG"
  run_priv chmod 600 "$KXMAINT_CONFIG"
}

prompt_value() {
  local var="$1" prompt="$2" default="$3" reply=""
  if [[ "${NO_INTERACTIVE:-0}" == "1" || "${DRY_RUN:-0}" == "1" || ! -t 0 ]]; then
    printf -v "$var" '%s' "$default"
    return 0
  fi
  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " reply || true
    [[ -n "$reply" ]] || reply="$default"
  else
    read -r -p "$prompt: " reply || true
    [[ -n "$reply" ]] || { echo "[kxmaint] $prompt cannot be empty" >&2; return 1; }
  fi
  printf -v "$var" '%s' "$reply"
}

run_priv() {
  if [[ "${NO_SUDO:-0}" == "1" || "$(id -u)" == "0" ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

load_config_file

# 在 default 之前用交互补齐——同时供后续 validate 复用。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgo.kxpms.cn/maintain-api"
fi
CHANNEL="${CHANNEL:-stable}"
PLATFORM="${PLATFORM:-linux}"
[[ "$PLATFORM" == "linux" ]] || { echo "[install-docker] Docker installer requires PLATFORM=linux (got ${PLATFORM})" >&2; exit 2; }
VERSION="${VERSION:-}"
# ARCH may be supplied explicitly by the generated command. Normalize catalog
# aliases while retaining uname detection when it is omitted.
if [ -n "${ARCH:-}" ]; then
  case "$ARCH" in
    amd64|x86_64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    loong64|loongarch64) ARCH=loong64 ;;
    *) echo "[install-docker] unsupported ARCH: $ARCH" >&2; exit 2 ;;
  esac
else
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    loongarch64|loong64) ARCH=loong64 ;;
    *) echo "unsupported arch: $arch" >&2; exit 2 ;;
  esac
fi
# loong64 默认严格：未显式 LOONG64_OK=1 时直接退出（CI 默认不发 loong64 artifact）。
if [[ "$ARCH" == "loong64" && "${LOONG64_OK:-0}" != "1" ]]; then
  echo "[install-docker] loongarch64 默认不启用（CI 默认不发 loong64 artifact）。如需安装请设 LOONG64_OK=1。" >&2
  exit 2
fi

# Validate VERSION (semver + build_seq; same character set as the UI's
# commandBuilder.ts allowlist). Reject anything that could break the
# downstream JSON payload to /downloads/ticket.
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[install-docker] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi
COMPOSE_DIR="${COMPOSE_DIR:-$PWD/llm-gateway-docker}"
IMAGE_DIR="${IMAGE_DIR:-${COMPOSE_DIR}/images}"
INCLUDE_DB="${INCLUDE_DB:-1}"
FETCH_IMAGES="${FETCH_IMAGES:-1}"
ALLOW_DB_IMAGE_FALLBACK="${ALLOW_DB_IMAGE_FALLBACK:-1}"
DRY_RUN="${DRY_RUN:-0}"
POSTGRES_USER="${POSTGRES_USER:-gateway}"
POSTGRES_DB="${POSTGRES_DB:-gateway}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"

load_existing_password() {
  local env_file="${COMPOSE_DIR}/.env" line value
  [[ -f "$env_file" ]] || return 0
  line="$(grep -E '^POSTGRES_PASSWORD=' "$env_file" | tail -1 || true)"
  if [[ -n "$line" ]]; then
    value="${line#POSTGRES_PASSWORD=}"
    [[ -n "$value" ]] && POSTGRES_PASSWORD="$value"
  fi
}

generate_password() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import secrets; print(secrets.token_hex(24))'
  else
    echo "[install-docker] openssl or python3 is required to generate a database password" >&2
    return 1
  fi
}

ensure_database_password() {
  [[ "$INCLUDE_DB" == "1" ]] || return 0
  load_existing_password
  if [[ -z "$POSTGRES_PASSWORD" ]]; then
    POSTGRES_PASSWORD="$(generate_password)" || exit 1
    echo "[install-docker] generated a random database password; see ${COMPOSE_DIR}/.env (mode 600)"
  fi
  if [[ "${#POSTGRES_PASSWORD}" -lt 24 ]]; then
    echo "[install-docker] POSTGRES_PASSWORD must be at least 24 characters" >&2
    exit 2
  fi
  umask 077
  if [[ -f "${COMPOSE_DIR}/.env" ]]; then
    if ! grep -q '^POSTGRES_PASSWORD=' "${COMPOSE_DIR}/.env"; then
      printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD" >>"${COMPOSE_DIR}/.env"
    fi
  else
    printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD" >"${COMPOSE_DIR}/.env"
  fi
  chmod 600 "${COMPOSE_DIR}/.env"
}

# 校验 sha256：Linux 用 sha256sum，缺失时退回 shasum；两者都没有就必须失败，
# 不能"没有工具就当校验通过"。
verify_sha256() {
  local expected="$1" file="$2" actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    echo "[install-docker] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  if [[ "${actual,,}" != "${expected,,}" ]]; then
    echo "[install-docker] checksum MISMATCH $file" >&2
    echo "[install-docker]   expected=$expected" >&2
    echo "[install-docker]   actual  =$actual" >&2
    return 1
  fi
  echo "[install-docker] sha256 OK $(basename "$file")"
}

# version-check 返回 target_artifacts[]，字段是 artifact_name / sha256 / storage_uri
# （storage_uri 在服务端被换成签名直链；签名失败时仍是 cloudreve:// 原值）。
api_artifact() {
  local platform="$1" want_arch="$2" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=${platform}&arch=${want_arch}" 2>/dev/null)" || return 1
  ART_JSON="$json" python3 - <<'PY'
import json, os
try:
    d = json.loads(os.environ["ART_JSON"])
except Exception:
    d = {}
arts = d.get("target_artifacts") or []
art = arts[0] if isinstance(arts, list) and arts and isinstance(arts[0], dict) else {}
print(art.get("artifact_name") or "")
print(art.get("sha256") or "")
print(art.get("storage_uri") or "")
PY
}

# catalog/versions 里的 items[] 是 CatalogItem：platform/arch/artifact_name/sha256。
# ticket 接口不返回 sha256，所以走 ticket 时必须回到目录里取校验值。
catalog_sha() {
  local version="$1" platform="$2" want_arch="$3" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)" \
    || json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  LOOKUP_JSON="$json" WANT_VERSION="$version" WANT_PLATFORM="$platform" WANT_ARCH="$want_arch" python3 - <<'PY'
import json, os
try:
    d = json.loads(os.environ["LOOKUP_JSON"])
except Exception:
    raise SystemExit(0)
want_v = os.environ["WANT_VERSION"].lstrip("v")
want_p = os.environ["WANT_PLATFORM"]
want_a = os.environ["WANT_ARCH"]
for v in (d.get("items") or d.get("versions") or []):
    if not isinstance(v, dict):
        continue
    if (v.get("version") or "").lstrip("v") != want_v:
        continue
    for a in (v.get("items") or v.get("artifacts") or []):
        if isinstance(a, dict) and a.get("platform") == want_p and a.get("arch") == want_a:
            print(a.get("sha256") or "")
            raise SystemExit(0)
PY
}

ticket_download() {
  local version="$1" platform="$2" want_arch="$3" body="" resp=""
  body="$(TV="$version" TP="$platform" TA="$want_arch" python3 -c '
import json, os
print(json.dumps({"version": os.environ["TV"], "platform": os.environ["TP"], "arch": os.environ["TA"]}))
')" || return 1
  resp="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" \
    -H 'Content-Type: application/json' -d "$body" 2>/dev/null)" || return 1
  TICKET_JSON="$resp" python3 - <<'PY'
import json, os
try:
    d = json.loads(os.environ["TICKET_JSON"])
except Exception:
    d = {}
print(d.get("url") or "")
print(d.get("file_name") or "")
PY
}

# 发现 → 下载 → 校验 → docker load。返回非 0 表示这个镜像没拿到（调用方决定是否致命）。
fetch_and_load() {
  local platform="docker" want_arch="$1" what="$2"
  local name="" sha="" uri="" url=""
  local fields=()
  if [[ -n "${VERSION:-}" ]]; then
    mapfile -t fields < <(ticket_download "$LATEST_BARE" "$platform" "$want_arch" || true)
    url="${fields[0]:-}"
    name="${fields[1]:-}"
    sha="$(catalog_sha "$LATEST_BARE" "$platform" "$want_arch" || true)"
  else
    mapfile -t fields < <(api_artifact "$platform" "$want_arch" || true)
    name="${fields[0]:-}"
    sha="${fields[1]:-}"
    uri="${fields[2]:-}"
    if [[ -n "$uri" ]]; then
      case "$uri" in
        http://*|https://*) url="$uri" ;;
      esac
    fi
    if [[ -z "$url" ]]; then
      local tfields=()
      mapfile -t tfields < <(ticket_download "$LATEST_BARE" "$platform" "$want_arch" || true)
      url="${tfields[0]:-}"
      [[ -n "${tfields[1]:-}" ]] && name="${tfields[1]}"
      if [[ -z "$sha" ]]; then
        sha="$(catalog_sha "$LATEST_BARE" "$platform" "$want_arch" || true)"
      fi
    fi
  fi
  if [[ -z "$url" || -z "$name" ]]; then
    echo "[install-docker] ${what}: no published docker/${want_arch} artifact for ${LATEST}" >&2
    return 1
  fi
  case "$url" in
    http://*|https://*) ;;
    *) echo "[install-docker] ${what}: refusing unsupported download URI: $url" >&2; return 1 ;;
  esac
  if [[ -z "$sha" ]]; then
    echo "[install-docker] ${what}: artifact ${name} has no published sha256 — refusing to load unverified image" >&2
    return 1
  fi
  mkdir -p "$IMAGE_DIR"
  local tar="${IMAGE_DIR}/${name}"
  echo "[install-docker] ${what}: downloading ${name}"
  if ! curl -fsSL "$url" -o "$tar"; then
    echo "[install-docker] ${what}: download failed ${name}" >&2
    rm -f "$tar"
    return 1
  fi
  if ! verify_sha256 "$sha" "$tar"; then
    rm -f "$tar"
    return 1
  fi
  load_tar "$tar" "$what"
}

load_tar() {
  local tar="$1" what="$2"
  echo "[install-docker] ${what}: docker load -i $tar"
  if ! docker load -i "$tar"; then
    echo "[install-docker] ${what}: docker load failed ($tar)" >&2
    return 1
  fi
}

docker_image_present() {
  docker image inspect "$1" >/dev/null 2>&1
}

# If VERSION is set explicitly, skip version-check (which always returns "latest")
# and resolve the requested version directly via the ticket endpoint. This makes
# historical versions installable on demand. VERSION="" (default) preserves the
# old "latest on channel" path.
if [ -n "${VERSION:-}" ]; then
  LATEST="${VERSION}"
  LATEST_BARE="${LATEST#v}"
else
  echo "[install-docker] fetching version-check for linux/${ARCH}"
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=linux&arch=${ARCH}")"
  LATEST="$(printf '%s' "$CHECK_JSON" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("latest_version",""))')"
  [[ -n "$LATEST" ]] || { echo "no published release" >&2; exit 1; }
  LATEST_BARE="${LATEST#v}"
fi

GATEWAY_IMAGE_DEFAULT="llm-gateway-go:${LATEST_BARE}-${ARCH}"
PG17_IMAGE="pg17-circus:${LATEST_BARE}"
DB_IMAGE_FALLBACK="postgres:17-alpine"

mkdir -p "$COMPOSE_DIR"
ensure_database_password

HAVE_DOCKER=0
if command -v docker >/dev/null 2>&1; then HAVE_DOCKER=1; fi

GATEWAY_IMAGE_EFFECTIVE="${GATEWAY_IMAGE:-$GATEWAY_IMAGE_DEFAULT}"
DB_IMAGE_EFFECTIVE="${DB_IMAGE:-$PG17_IMAGE}"

if [[ "$DRY_RUN" != "1" && "$HAVE_DOCKER" == "1" ]]; then
  # 网关镜像：手工 tar > 在线发现下载 > 本地已有镜像 > GATEWAY_IMAGE 覆盖。
  GATEWAY_READY=0
  if [[ -n "${GATEWAY_IMAGE:-}" ]]; then
    echo "[install-docker] gateway: using GATEWAY_IMAGE=${GATEWAY_IMAGE} (operator override)"
    GATEWAY_READY=1
  elif [[ -n "${LOAD_IMAGE_TAR:-}" ]]; then
    [[ -f "$LOAD_IMAGE_TAR" ]] || { echo "[install-docker] LOAD_IMAGE_TAR not found: $LOAD_IMAGE_TAR" >&2; exit 1; }
    load_tar "$LOAD_IMAGE_TAR" "gateway" || exit 1
    GATEWAY_READY=1
  elif docker_image_present "$GATEWAY_IMAGE_DEFAULT"; then
    echo "[install-docker] gateway: ${GATEWAY_IMAGE_DEFAULT} already present locally"
    GATEWAY_READY=1
  elif [[ "$FETCH_IMAGES" == "1" ]] && fetch_and_load "$ARCH" "gateway"; then
    GATEWAY_READY=1
  fi
  if [[ "$GATEWAY_READY" != "1" ]]; then
    echo "[install-docker] gateway image ${GATEWAY_IMAGE_DEFAULT} unavailable:" >&2
    echo "[install-docker]   - 没有本地镜像，且未能从 ${MAINTAIN_BASE} 下载 docker/${ARCH} 镜像" >&2
    echo "[install-docker]   - 离线安装请设 LOAD_IMAGE_TAR=/path/llm-gateway-go-${LATEST_BARE}-${ARCH}.tar" >&2
    echo "[install-docker]   - 或设 GATEWAY_IMAGE=<registry 镜像> 使用自有仓库" >&2
    exit 1
  fi
  if [[ -z "${GATEWAY_IMAGE:-}" ]] && ! docker_image_present "$GATEWAY_IMAGE_DEFAULT"; then
    echo "[install-docker] loaded tar did not provide expected tag ${GATEWAY_IMAGE_DEFAULT}" >&2
    echo "[install-docker] 本地镜像列表：" >&2
    docker image ls --format '  {{.Repository}}:{{.Tag}}' >&2 || true
    exit 1
  fi

  # 数据库镜像：回退到 postgres:17-alpine 是安装前的显式决定，
  # 不是 compose 失败之后再重试 —— 那样会把真实错误当成"镜像缺失"掩盖掉。
  if [[ "$INCLUDE_DB" == "1" ]]; then
    if [[ -n "${DB_IMAGE:-}" ]]; then
      echo "[install-docker] db: using DB_IMAGE=${DB_IMAGE} (operator override, no fallback)"
    else
      DB_READY=0
      if [[ -n "${LOAD_DB_TAR:-}" ]]; then
        [[ -f "$LOAD_DB_TAR" ]] || { echo "[install-docker] LOAD_DB_TAR not found: $LOAD_DB_TAR" >&2; exit 1; }
        load_tar "$LOAD_DB_TAR" "db" || exit 1
        DB_READY=1
      elif docker_image_present "$PG17_IMAGE"; then
        echo "[install-docker] db: ${PG17_IMAGE} already present locally"
        DB_READY=1
      elif [[ "$FETCH_IMAGES" == "1" ]] && fetch_and_load multi "db"; then
        DB_READY=1
      fi
      if [[ "$DB_READY" == "1" ]] && docker_image_present "$PG17_IMAGE"; then
        DB_IMAGE_EFFECTIVE="$PG17_IMAGE"
      elif [[ "$ALLOW_DB_IMAGE_FALLBACK" == "1" ]]; then
        DB_IMAGE_EFFECTIVE="$DB_IMAGE_FALLBACK"
        echo "[install-docker] db: ${PG17_IMAGE} 不可得 — 按 ALLOW_DB_IMAGE_FALLBACK=1 回退 ${DB_IMAGE_FALLBACK}" >&2
        echo "[install-docker] db: 回退镜像不含 circus/citus 运维侧车，仅用于引导" >&2
      else
        echo "[install-docker] db: ${PG17_IMAGE} 不可得，且 ALLOW_DB_IMAGE_FALLBACK=0" >&2
        echo "[install-docker] db: 请提供 LOAD_DB_TAR=/path/pg17-circus-${LATEST_BARE}.tar 或设 DB_IMAGE=" >&2
        exit 1
      fi
    fi
  fi
elif [[ "$DRY_RUN" != "1" ]]; then
  echo "[install-docker] docker not found — skip image fetch/load"
fi

cat >"${COMPOSE_DIR}/docker-compose.yml" <<YAML
# Generated by install-docker.sh for ${LATEST} (${ARCH})
# Offline image tars (when published) live under files.kxpms.cn:
	#   cloudreve://my/release/llm-gateway-go/${LATEST_BARE}/docker/linux-${ARCH}/
services:
  gateway:
    image: \${GATEWAY_IMAGE:-${GATEWAY_IMAGE_EFFECTIVE}}
    ports:
      - "\${GATEWAY_PORT:-8080}:8080"
    environment:
      - MAINTAIN_BASE=${MAINTAIN_BASE}
    restart: unless-stopped
YAML

if [[ "$INCLUDE_DB" == "1" ]]; then
  cat >>"${COMPOSE_DIR}/docker-compose.yml" <<YAML
  db:
    # 安装前已确定的镜像（pg17-circus 可得时优先，否则 ${DB_IMAGE_FALLBACK}）。
    image: \${DB_IMAGE:-${DB_IMAGE_EFFECTIVE}}
    environment:
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
      POSTGRES_DB: \${POSTGRES_DB:-${POSTGRES_DB}}
    volumes:
      - pgdata:/var/lib/postgresql/data
    restart: unless-stopped
volumes:
  pgdata:
YAML
fi

cat >"${COMPOSE_DIR}/.env.example" <<EOF
GATEWAY_IMAGE=${GATEWAY_IMAGE_EFFECTIVE}
GATEWAY_PORT=8080
DB_IMAGE=${DB_IMAGE_EFFECTIVE}
POSTGRES_USER=${POSTGRES_USER}
POSTGRES_DB=${POSTGRES_DB}
# Copy the generated ${COMPOSE_DIR}/.env or set a strong POSTGRES_PASSWORD before starting.
EOF

cat >"${COMPOSE_DIR}/INSTALL-DOCKER.md" <<EOF
# Docker 安装说明（${LATEST}）

1. 镜像获取：脚本默认从 ${MAINTAIN_BASE} 发现并下载 docker 产物（校验 sha256 后 docker load）：
   - \`llm-gateway-go-${LATEST_BARE}-${ARCH}.tar\`（platform=docker, arch=${ARCH}）
   - \`pg17-circus-${LATEST_BARE}.tar\`（platform=docker, arch=multi）
   离线场景：\`LOAD_IMAGE_TAR=… LOAD_DB_TAR=… FETCH_IMAGES=0 bash install-docker.sh\`
2. \`cp .env.example .env\` 并按需修改
3. \`docker compose up -d\`
4. 健康检查后访问 Maintain 激活页完成在线/离线激活

当前编排使用：gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}
离线包与 Docker 通道并行；主机安装请用 install-scripts/host。
EOF

echo "[install-docker] wrote ${COMPOSE_DIR}/docker-compose.yml for ${LATEST}"
if [[ "$DRY_RUN" == "1" ]]; then
  echo "[install-docker] DRY_RUN=1 — no download / no docker load / no compose up"
  exit 0
fi
if [[ "$HAVE_DOCKER" != "1" ]]; then
  echo "[install-docker] docker not found — compose file ready for manual start"
  exit 0
fi
# compose 失败必须让安装失败：以前这里吞掉了错误并打印"attempted"。
if ! (cd "$COMPOSE_DIR" && docker compose up -d); then
  echo "[install-docker] docker compose up FAILED in ${COMPOSE_DIR}" >&2
  echo "[install-docker] gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}" >&2
  echo "[install-docker] 排查：docker compose -f ${COMPOSE_DIR}/docker-compose.yml logs" >&2
  exit 1
fi
echo "[install-docker] compose up OK (gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}). Activate via maintain UI."
# 仅持久化可复用项：不含 IMAGE_* / LOAD_*_TAR / POSTGRES_PASSWORD / 单次 VERSION。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "${DRY_RUN:-0}" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL COMPOSE_DIR IMAGE_DIR INCLUDE_DB ALLOW_DB_IMAGE_FALLBACK || true
}
persist_config
