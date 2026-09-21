#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/user/client-deploy.sh
# SYNC_POLICY: 修改本脚本时同步改 maintain 对应位置。service identity 由本仓库维护。
# ADAPTATIONS: 加 loong64 严格门；INSTALL_ROOT 默认智能探测；新增 --install-mode flag（host|docker|auto），host 模式委托 install-host.sh。

# client-deploy.sh — 客户端一键自动化部署编排器（Linux / macOS）。
#
# 串联完整部署生命周期（Windows 见同目录 client-deploy.ps1）：
#   阶段 1  检测并自动安装 Docker（全平台）
#   阶段 2  部署 PostgreSQL（pg17-circus，bind-mount /opt/llm-gateway/data）
#   阶段 3  下载网关镜像 tar（经 maintain ticket + sha256 校验）→ 部署网关
#   阶段 4  健康检查验证
#   阶段 5  建立 DB 备份 / 日志清理 / 版本记录 / 回退目录等长期任务
#
# 与现有 install-docker.sh / upgrade.sh 的关系：本脚本是面向"一次性全自动"
# 场景的编排层，复用同一套 ticket/sha256/compose 约定，不改动那三个脚本，
# 保持它们可独立 curl 调用。
#
# 子命令:
#   deploy (默认=run)  全流程 1→5
#   docker             仅阶段 1
#   db                 仅阶段 2
#   gateway            仅阶段 3（含版本轮转、.env 备份）
#   verify             仅阶段 4
#   setup-cron         仅阶段 5
#   rotate             手动清理：保留 versions/ 与 releases/ 各最新 3 份
#   rollback [ver]     从 releases/ 最近快照回退（可指定版本）
#   record             追加一条 CHANGELOG 记录
#   list               列出本地已有版本与快照
#
# 用法:
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/client-deploy" | bash
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/client-deploy" | VERSION=1.2.0 bash
#   bash client-deploy.sh deploy
#   DRY_RUN=1 bash client-deploy.sh deploy
#   bash client-deploy.sh rollback
#
# Env:
#   MAINTAIN_BASE          API 根（默认 https://llmgo.kxpms.cn/maintain-api）
#   CHANNEL                stable
#   VERSION                目标版本（空=取最新）
#   INSTALL_ROOT           安装根（默认 /opt/llm-gateway）
#   GATEWAY_PORT           网关主机端口（默认 8080）
#   POSTGRES_PASSWORD      PG 密码（默认随机生成并写入 .env）
#   POSTGRES_USER          PG 用户（默认 llm_user）
#   POSTGRES_DB            预建库名（默认 llm_gateway）
#   PG_IMAGE_OVERRIDE      覆盖 PG 镜像（设置后不再走 ticket 下载）
#   GATEWAY_IMAGE_OVERRIDE 覆盖网关镜像（设置后不再走 ticket 下载）
#   ALLOW_DB_IMAGE_FALLBACK 1(默认)=pg17-circus 不可得回退 postgres:17-alpine
#   DRY_RUN                1=只打印计划，不下载/不 compose
#   NO_SUDO                1=不使用 sudo（容器/已 root/测试）
#   HEALTH_RETRIES         健康检查重试秒数（默认 30）
#   KEEP_VERSIONS          保留版本目录数（默认 3 = 当前+前2）
#   KEEP_RELEASES          保留部署快照数（默认 3）
#   DB_BACKUP_KEEP         DB 备份保留份数（默认 7）
set -euo pipefail

# ─── 配置 ────────────────────────────────────────────────────────────────────
MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
VERSION="${VERSION:-}"
# BUNDLE_DIR 仅在离线模式（OFFLINE_BUNDLE 提供）时由 init_offline_bundle 赋值；在线/docker 流程必须先 declare 才能被 set -u 容忍引用。
BUNDLE_DIR="${BUNDLE_DIR:-}"
# INSTALL_ROOT 默认值用 uname 直接探测（detect_platform 函数在下面定义，OS_KERNEL 是 local 变量）。
# host mode：直接调 install-host.sh。docker mode：下面 compose 路径。
INSTALL_MODE="${INSTALL_MODE:-auto}"
# auto 探测规则：INSTALL_ROOT/docker-compose.yml 存在 → docker，否则 host。
_uname_s="$(uname -s)"
case "$_uname_s" in
  Darwin*) default_root="$HOME/kaixuan/llm-gateway-go" ;;
  Linux*)  default_root=/opt/kaixuan/llm-gateway-go ;;
  *) default_root="$HOME/kaixuan/llm-gateway-go" ;;
esac
if [[ -z "${INSTALL_ROOT:-}" && -d /opt/llm-gateway ]]; then
  default_root=/opt/llm-gateway
elif [[ -z "${INSTALL_ROOT:-}" && -d "$HOME/Downloads/llm-gateway-files" ]]; then
  default_root="$HOME/Downloads/llm-gateway-files"
fi
INSTALL_ROOT="${INSTALL_ROOT:-$default_root}"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
POSTGRES_USER="${POSTGRES_USER:-llm_user}"
POSTGRES_DB="${POSTGRES_DB:-llm_gateway}"
ALLOW_DB_IMAGE_FALLBACK="${ALLOW_DB_IMAGE_FALLBACK:-1}"
DRY_RUN="${DRY_RUN:-0}"
NO_SUDO="${NO_SUDO:-0}"
HEALTH_RETRIES="${HEALTH_RETRIES:-30}"
KEEP_VERSIONS="${KEEP_VERSIONS:-3}"
KEEP_RELEASES="${KEEP_RELEASES:-3}"
DB_BACKUP_KEEP="${DB_BACKUP_KEEP:-7}"
LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS:-30}"
# 网关运行所需的密钥/配置（首次生成并持久化到 .env，后续复用，避免升级轮换导致 token 失效）。
GATEWAY_ENV_MODE="${GATEWAY_ENV_MODE:-production}"
GATEWAY_API_KEY="${GATEWAY_API_KEY:-}"
GATEWAY_ADMIN_API_KEY="${GATEWAY_ADMIN_API_KEY:-}"
GATEWAY_JWT_SECRET="${GATEWAY_JWT_SECRET:-}"
GATEWAY_SECRET_KEY="${GATEWAY_SECRET_KEY:-}"
GATEWAY_CRED_ENC_KEY="${GATEWAY_CRED_ENC_KEY:-}"
GATEWAY_CORS_ORIGINS="${GATEWAY_CORS_ORIGINS:-http://127.0.0.1:${GATEWAY_PORT},http://localhost:${GATEWAY_PORT}}"
# 离线包模式：指向离线包目录或 .tar.gz，检测到即不连 maintain API、从 bundle 本地 load 镜像。
OFFLINE_BUNDLE="${OFFLINE_BUNDLE:-}"
CMD="${1:-deploy}"
# 解析 --install-mode host|docker|auto（仅影响 deploy 子命令）。auto = 看 INSTALL_ROOT/docker-compose.yml 是否存在。
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-mode) INSTALL_MODE="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) show_help; exit 0 ;;
    *) echo "[client-deploy] unknown flag: $1" >&2; exit 2 ;;
  esac
done
case "$INSTALL_MODE" in
  auto|host|docker) ;;
  *) echo "[client-deploy] invalid INSTALL_MODE=$INSTALL_MODE (auto|host|docker)" >&2; exit 2 ;;
esac

# 版本字符白名单（与 install-docker.sh / commandBuilder.ts 对齐）。
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[client-deploy] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi

DATA_DIR="${INSTALL_ROOT}/data"
VERSIONS_DIR="${INSTALL_ROOT}/versions"
RELEASES_DIR="${INSTALL_ROOT}/releases"
ENV_FILE="${INSTALL_ROOT}/.env"
ENV_BAK_DIR="${INSTALL_ROOT}/.env.bak"
CHANGELOG="${INSTALL_ROOT}/CHANGELOG.log"
COMPOSE_FILE="${INSTALL_ROOT}/docker-compose.yml"
VERSION_FILE="${INSTALL_ROOT}/VERSION"
CURRENT_VERSION="$(cat "$VERSION_FILE" 2>/dev/null || echo "")"
PG_CONTAINER="llm-gateway-pg"
GATEWAY_CONTAINER="llm-gateway"

# ─── 平台探测（阶段 0）──────────────────────────────────────────────────────
detect_platform() {
  OS_KERNEL="$(uname -s 2>/dev/null || echo Unknown)"
  case "$OS_KERNEL" in
    Linux*)  OS="linux" ;;
    Darwin*) OS="darwin" ;;
    *) echo "[client-deploy] unsupported kernel: $OS_KERNEL (Windows 请用 client-deploy.ps1)" >&2; exit 2 ;;
  esac
  arch="${ARCH:-$(uname -m)}"
  case "$arch" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    loongarch64|loong64) ARCH=loong64 ;;
    *) echo "[client-deploy] unsupported arch: $arch" >&2; exit 2 ;;
  esac
  # loong64 默认严格：未显式 LOONG64_OK=1 时直接退出（CI 默认不发 loong64 artifact）。
  if [[ "$ARCH" == "loong64" && "${LOONG64_OK:-0}" != "1" ]]; then
    echo "[client-deploy] loongarch64 默认不启用（CI 默认不发 loong64 artifact）。如需安装请设 LOONG64_OK=1。" >&2
    exit 2
  fi
  # docker 镜像 tar 是 Linux 镜像，按 OS+arch 目录保存：
  # {version}/docker/linux-amd64/ 或 {version}/docker/linux-arm64/。
  DOCKER_SUBDIR="linux-${ARCH}"
}

# ─── INSTALL_MODE auto 探测 ──────────────────────────────────────────────────
# auto：INSTALL_ROOT 下存在 docker-compose.yml → docker，否则 host。
resolve_install_mode() {
  if [[ "$INSTALL_MODE" != "auto" ]]; then
    echo "$INSTALL_MODE"
    return 0
  fi
  if [[ -f "${INSTALL_ROOT}/docker-compose.yml" ]] && command -v docker >/dev/null 2>&1; then
    echo "docker"
  else
    echo "host"
  fi
}

# ─── 权限封装（与 upgrade.sh 同语义）──────────────────────────────────────────
run_priv() {
  if [[ "${NO_SUDO:-0}" == "1" || "$(id -u)" == "0" ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

# ─── 日志 ─────────────────────────────────────────────────────────────────────
ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }

log() { echo "[client-deploy] $*"; }
warn() { echo "[client-deploy] WARN: $*" >&2; }
die() { echo "[client-deploy] ERROR: $*" >&2; exit 1; }

append_changelog() {
  # status=ok|failed; 不让日志写入失败拖垮主流程。
  local action="$1" ver="$2" status="$3" extra="${4:-}"
  run_priv mkdir -p "$INSTALL_ROOT" 2>/dev/null || true
  printf '[%s] %s ver=%s prev=%s status=%s %s\n' \
    "$(ts)" "$action" "${ver:-?}" "${CURRENT_VERSION:-none}" "$status" "$extra" \
    | run_priv tee -a "$CHANGELOG" >/dev/null 2>/dev/null || true
}

# ─── sha256 校验（与现有脚本同实现，独立维护以免 source 依赖）──────────────
sha256_of() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    return 1
  fi
}

# 版本序排序：sort -V 不一定可用（busybox），不可用时用 awk 拆点分段比较。
# 输入：每行一个路径，末尾是 /<ver> 或 /<ver>-<ts>；按嵌入的版本号排序输出。
version_sort() {
  if sort -V </dev/null >/dev/null 2>&1; then
    sort -V
  else
    awk -F/ '{
      seg=$NF; sub(/-.*$/,"",seg);          # 取末段，去掉时间戳后缀
      n=split(seg,p,"."); key="";
      for(i=1;i<=4;i++){ key=sprintf("%s%020d", key, p[i]+0) }
      print key "\t" $0
    }' | sort -k1,1 | cut -f2-
  fi
}

verify_sha256() {
  local expected="$1" file="$2" actual=""
  if [[ -z "$expected" ]]; then
    echo "[client-deploy] sha256 skip (no expected value) $(basename "$file")"
    return 0
  fi
  if ! actual="$(sha256_of "$file")"; then
    echo "[client-deploy] no sha256 tool — cannot verify $file" >&2; return 1
  fi
  if [[ "${actual,,}" != "${expected,,}" ]]; then
    echo "[client-deploy] checksum MISMATCH $file (expected=$expected actual=$actual)" >&2
    return 1
  fi
  echo "[client-deploy] sha256 OK $(basename "$file")"
}

# ─── catalog / ticket 下载（复用 maintain 公共接口，与 install-docker.sh 同协议）─
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

# ticket：返回 url / file_name 两行
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

# version-check 取最新版本号
latest_version() {
  curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=docker&arch=${ARCH}" 2>/dev/null \
    | python3 -c 'import sys,json; print(json.load(sys.stdin).get("latest_version",""))' 2>/dev/null || true
}

# ─── 离线包（air-gapped）支持 ────────────────────────────────────────────────
# OFFLINE_BUNDLE 指向离线包目录或 .tar.gz。init_offline_bundle 解压（如需）并把
# BUNDLE_DIR 指向含 MANIFEST.json 的根目录。后续 resolve_target_version /
# resolve_*_image 读 MANIFEST、从 images/ 本地 docker load，全程不联网。

# 解析 MANIFEST.json 字段：$1=字段名。返回空表示无 bundle 或字段缺失。
manifest_field() {
  local key="$1"
  [[ -n "$BUNDLE_DIR" && -f "$BUNDLE_DIR/MANIFEST.json" ]] || return 0
  MANIFEST_PATH="$BUNDLE_DIR/MANIFEST.json" WANT_KEY="$key" python3 - <<'PY' 2>/dev/null || true
import json, os
try:
    d = json.load(open(os.environ["MANIFEST_PATH"], encoding="utf-8"))
except Exception:
    raise SystemExit(0)
v = d.get(os.environ["WANT_KEY"])
print("" if v is None else v)
PY
}

init_offline_bundle() {
  [[ -n "$OFFLINE_BUNDLE" ]] || return 0
  if [[ -d "$OFFLINE_BUNDLE" ]]; then
    BUNDLE_DIR="$OFFLINE_BUNDLE"
  elif [[ -f "$OFFLINE_BUNDLE" ]]; then
    # .tar.gz：解压到临时目录（同名子目录或当前目录）。
    local tmp
    tmp="$(mktemp -d)"
    if ! tar -xzf "$OFFLINE_BUNDLE" -C "$tmp" 2>/dev/null; then
      rm -rf "$tmp"
      die "OFFLINE_BUNDLE 解压失败: $OFFLINE_BUNDLE"
    fi
    # 找含 MANIFEST.json 的根（可能在子目录）。
    BUNDLE_DIR="$(find "$tmp" -name MANIFEST.json -maxdepth 3 -print -quit 2>/dev/null | head -1 | xargs dirname 2>/dev/null)"
    if [[ -z "$BUNDLE_DIR" || ! -f "$BUNDLE_DIR/MANIFEST.json" ]]; then
      rm -rf "$tmp"
      die "OFFLINE_BUNDLE 未含 MANIFEST.json: $OFFLINE_BUNDLE"
    fi
    # 注：不清理 $tmp，因为 BUNDLE_DIR 指向其子目录，整个部署期间需保留；进程退出时 OS 清理。
  else
    die "OFFLINE_BUNDLE 不存在: $OFFLINE_BUNDLE"
  fi
  [[ -f "$BUNDLE_DIR/MANIFEST.json" ]] || die "离线包缺少 MANIFEST.json: $BUNDLE_DIR"
  log "离线模式: bundle=$BUNDLE_DIR version=$(manifest_field version) arch=$(manifest_field arch)"
}

# 从 bundle/images/ load 单个镜像 tar 并校验 sha256。
# $1=what(gateway|db) $2=tar 字段名 $3=sha 字段名 $4=image 字段名
load_bundle_image() {
  local what="$1" tar_field="$2" sha_field="$3" img_field="$4"
  local tar_name sha img
  tar_name="$(manifest_field "$tar_field")"
  sha="$(manifest_field "$sha_field")"
  img="$(manifest_field "$img_field")"
  [[ -n "$tar_name" ]] || { warn "离线包 MANIFEST 缺 $tar_field"; return 1; }
  # 约定：MANIFEST tar 字段包含 images/ 前缀（bundler 生成格式）。
  # 先尝试相对 BUNDLE_DIR 的完整路径，再回退到 images/<basename>（兼容手动构造的 bundle）。
  local tar="${BUNDLE_DIR}/${tar_name}"
  if [[ ! -f "$tar" ]]; then
    local base; base="$(basename "$tar_name")"
    tar="${BUNDLE_DIR}/images/${base}"
  fi
  [[ -f "$tar" ]] || { warn "离线包缺镜像文件 ${tar_name}（尝试了 $BUNDLE_DIR/$tar_name 与 $BUNDLE_DIR/images/$(basename "$tar_name"))"; return 1; }
  if [[ -n "$sha" ]]; then
    verify_sha256 "$sha" "$tar" || return 1
  else
    warn "离线包 ${what} 无 sha256 — 跳过校验（来自可信 bundle）"
  fi
  echo "[client-deploy] ${what}: docker load (offline) $tar"
  if ! docker load -i "$tar"; then
    warn "离线包 ${what} docker load 失败"
    return 1
  fi
  [[ -n "$img" ]] && echo "$img"
  return 0
}

# 通用：发现 → 下载 → 校验 → docker load。返回 0/1。
# 参数：what(gateway|db) want_arch version
fetch_and_load() {
  local what="$1" want_arch="$2" ver="$3" name="" sha="" url=""
  local fields=()
  mapfile -t fields < <(ticket_download "$ver" "docker" "$want_arch" || true)
  url="${fields[0]:-}"; name="${fields[1]:-}"
  sha="$(catalog_sha "$ver" "docker" "$want_arch" || true)"
  if [[ -z "$url" || -z "$name" ]]; then
    echo "[client-deploy] ${what}: no published docker/${want_arch} artifact for ${ver}" >&2
    return 1
  fi
  case "$url" in
    http://*|https://*) ;;
    *) echo "[client-deploy] ${what}: refusing unsupported URI: $url" >&2; return 1 ;;
  esac
  [[ -n "$sha" ]] || { echo "[client-deploy] ${what}: ${name} 无已发布 sha256 — 拒绝加载未校验镜像" >&2; return 1; }
  local dest_dir="${VERSIONS_DIR}/${ver}/docker/${DOCKER_SUBDIR}"
  [[ "$what" == "db" ]] && dest_dir="${VERSIONS_DIR}/${ver}/docker/multi"
  run_priv mkdir -p "$dest_dir"
  local tar="${dest_dir}/${name}"
  echo "[client-deploy] ${what}: downloading ${name}"
  if ! curl -fsSL "$url" -o "$tar"; then
    echo "[client-deploy] ${what}: download failed ${name}" >&2; rm -f "$tar"; return 1
  fi
  if ! verify_sha256 "$sha" "$tar"; then rm -f "$tar"; return 1; fi
  echo "[client-deploy] ${what}: docker load -i $tar"
  if ! docker load -i "$tar"; then
    echo "[client-deploy] ${what}: docker load failed ($tar)" >&2; return 1
  fi
  # 记录 tar 元信息，便于回退/审计
  printf '%s  %s\n' "$sha" "$name" | run_priv tee "${dest_dir}/SHA256SUMS" >/dev/null
}

docker_image_present() { docker image inspect "$1" >/dev/null 2>&1; }

# ─── 阶段 1：Docker 检测与安装 ───────────────────────────────────────────────
docker_ready() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }

ensure_root_dir() { run_priv mkdir -p "$INSTALL_ROOT"; }

install_docker_linux() {
  # 幂等：装过就跳。优先用发行版包管理器，最后 get.docker.com 兜底。
  if command -v docker >/dev/null 2>&1; then
    log "docker binary already present"
  elif command -v apt-get >/dev/null 2>&1; then
    log "installing docker via apt-get"
    run_priv apt-get update -y
    run_priv apt-get install -y ca-certificates curl gnupg
    run_priv install -m 0755 -d /etc/apt/keyrings
    run_priv curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    run_priv chmod a+r /etc/apt/keyrings/docker.asc
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
      | run_priv tee /etc/apt/sources.list.d/docker.list >/dev/null
    run_priv apt-get update -y
    run_priv apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  elif command -v dnf >/dev/null 2>&1; then
    log "installing docker via dnf"
    run_priv dnf install -y dnf-plugins-core
    run_priv dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    run_priv dnf install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  elif command -v yum >/dev/null 2>&1; then
    log "installing docker via yum"
    run_priv yum install -y yum-utils
    run_priv yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    run_priv yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  elif command -v pacman >/dev/null 2>&1; then
    log "installing docker via pacman"
    run_priv pacman -Sy --noconfirm docker docker-compose
  else
    log "no distro pkg manager found — falling back to get.docker.com"
    run_priv sh -c "$(curl -fsSL https://get.docker.com)"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    run_priv systemctl enable --now docker || warn "systemctl enable docker failed — 请手动启动 docker"
  fi
}

install_docker_macos() {
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    log "docker already running"
    return 0
  fi
  if command -v brew >/dev/null 2>&1; then
    log "installing Docker Desktop via Homebrew cask"
    brew install --cask docker || {
      warn "brew cask install 失败。也可安装 OrbStack: brew install --cask orbstack"
      return 1
    }
  else
    warn "未检测到 Homebrew。请手动安装 Docker Desktop 或 OrbStack 后重试。"
    warn "  Docker Desktop: https://www.docker.com/products/docker-desktop"
    warn "  OrbStack:        https://orbstack.dev/"
    return 1
  fi
  # GUI 应用需用户首次启动；后台等待 daemon 就绪。
  warn "Docker Desktop 已安装，请启动它（首次需手动同意许可）。等待 daemon 就绪…"
  local _wait
  for _wait in $(seq 1 60); do
    docker info >/dev/null 2>&1 && { log "docker daemon ready"; return 0; }
    sleep 3
  done
  warn "等待 3 分钟后 docker daemon 仍未就绪 — 请确认 Docker Desktop 已启动后重跑"
  return 1
}

stage_docker() {
  log "阶段 1: 检测并安装 Docker ($OS/$ARCH)"
  if docker_ready; then
    log "docker 已就绪，跳过安装"
    return 0
  fi
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将在 $OS 上安装 docker（实际执行略）"
    return 0
  fi
  case "$OS" in
    linux)  install_docker_linux ;;
    darwin) install_docker_macos ;;
  esac
  docker_ready || { die "docker 安装后仍未就绪 — 请手动排查（docker info）"; }
  log "docker 就绪: $(docker --version)"
}

# ─── 阶段 2：部署 PostgreSQL ─────────────────────────────────────────────────
resolve_pg_image() {
  # PG 镜像：优先 override > 已有 pg17-circus > 离线 bundle > ticket 下载 > 回退。
  if [[ -n "${PG_IMAGE_OVERRIDE:-}" ]]; then
    PG_IMAGE_EFFECTIVE="$PG_IMAGE_OVERRIDE"
    log "db: 使用 PG_IMAGE_OVERRIDE=${PG_IMAGE_OVERRIDE}"
    return 0
  fi
  local target="${TARGET_VERSION:-$LATEST_VERSION}"
  local pg17="pg17-circus:${target#v}"
  PG_IMAGE_EFFECTIVE="$pg17"
  if docker_image_present "$pg17"; then
    log "db: ${pg17} 本地已存在"
    return 0
  fi
  # 离线模式：从 bundle 本地 load，不联网。
  if [[ -n "$BUNDLE_DIR" ]]; then
    if [[ "$DRY_RUN" == "1" ]]; then
      log "DRY_RUN=1 — 将从离线包加载 ${pg17}"
      return 0
    fi
    local loaded
    if loaded="$(load_bundle_image db pg_tar pg_sha256 pg_image)"; then
      [[ -n "$loaded" ]] && PG_IMAGE_EFFECTIVE="$loaded"
      log "db: 已从离线包加载 ${PG_IMAGE_EFFECTIVE}"
      return 0
    fi
    warn "db: 离线包加载 ${pg17} 失败"
    # 尊重 ALLOW_DB_IMAGE_FALLBACK：离线 load 失败也应尝试回退。
    if [[ "$ALLOW_DB_IMAGE_FALLBACK" == "1" ]]; then
      PG_IMAGE_EFFECTIVE="postgres:17-alpine"
      warn "db: 回退 ${PG_IMAGE_EFFECTIVE}（无 circus/citus 侧车，仅基础 PG）"
      return 0
    fi
    die "db: 离线包加载失败且 ALLOW_DB_IMAGE_FALLBACK=0（检查 bundle MANIFEST/images/ 完整性）"
  fi
  if [[ "$DRY_RUN" != "1" ]] && fetch_and_load db multi "$target"; then
    log "db: 已加载 ${pg17}"
    return 0
  fi
  if [[ "$ALLOW_DB_IMAGE_FALLBACK" == "1" ]]; then
    PG_IMAGE_EFFECTIVE="postgres:17-alpine"
    warn "db: ${pg17} 不可得 — 回退 ${PG_IMAGE_EFFECTIVE}（无 circus/citus 侧车，仅基础 PG）"
  else
    die "db: ${pg17} 不可得且 ALLOW_DB_IMAGE_FALLBACK=0；请设 PG_IMAGE_OVERRIDE 或上传 pg17-circus"
  fi
}

wait_pg_ready() {
  local _retry
  for _retry in $(seq 1 "${HEALTH_RETRIES}"); do
    if docker exec "$PG_CONTAINER" pg_isready -U "$POSTGRES_USER" >/dev/null 2>&1; then
      log "db: pg_isready OK"
      return 0
    fi
    sleep 1
  done
  die "db: pg_isready 超时（${HEALTH_RETRIES}s）— 见 docker logs $PG_CONTAINER"
}

seed_database() {
  # 预建库与 role；schema/种子交由网关启动时 ApplyMigrations（已与用户确认）。
  log "db: 预建库 ${POSTGRES_DB}（schema 由网关启动迁移）"
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — skip CREATE DATABASE"
    return 0
  fi
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='${POSTGRES_DB}'" \
    | grep -q 1 || \
    docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
      psql -U "$POSTGRES_USER" -d postgres -c "CREATE DATABASE \"${POSTGRES_DB}\";"
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c \
    "GRANT ALL PRIVILEGES ON DATABASE \"${POSTGRES_DB}\" TO \"${POSTGRES_USER}\";" >/dev/null
  log "db: 库 ${POSTGRES_DB} 就绪"

  # ─── maintain schema 初始化（2026-08-06 新增）─────────────────────────────
  # 若离线包/版本目录中包含 maintain SQL 快照，先应用 baseline，
  # 后续网关启动时 migrations.Apply() 会幂等跳过已有的 schema_migrations 行。
  apply_maintain_schema_if_available
}

# apply_maintain_schema_if_available — 自动应用 maintain schema 快照（如果存在）
# 优先级：
#   1. 离线包中的 sql/maintain-snapshot/
#   2. 版本目录中的 sql/maintain-snapshot/
#   3. 跳过（由网关启动时 migrations.Apply() 处理）
apply_maintain_schema_if_available() {
  local snapshot_dir=""

  # 查找快照路径
  if [[ -n "${BUNDLE_DIR:-}" && -d "${BUNDLE_DIR}/sql/maintain-snapshot/init-maintain-schema" ]]; then
    snapshot_dir="${BUNDLE_DIR}/sql/maintain-snapshot"
    log "db: 发现离线包中的 maintain schema 快照"
  elif [[ -d "${VERSION_DIR:-}/sql/maintain-snapshot/init-maintain-schema" ]]; then
    snapshot_dir="${VERSION_DIR}/sql/maintain-snapshot"
    log "db: 发现版本目录中的 maintain schema 快照"
  else
    log "db: 未发现 maintain schema 快照，将由网关启动时自动迁移"
    return 0
  fi

  # 检查 maintain schema 是否已存在（幂等保护）
  local schema_exists
  schema_exists="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
    "SELECT 1 FROM information_schema.schemata WHERE schema_name='maintain'" 2>/dev/null | tr -d '[:space:]' || true)"

  if [[ "$schema_exists" == "1" ]]; then
    log "db: maintain schema 已存在，跳过 baseline 应用"
    return 0
  fi

  local apply_script="${snapshot_dir}/init-maintain-schema/apply.sh"
  if [[ ! -f "$apply_script" ]]; then
    warn "db: apply.sh 不存在: ${apply_script}，跳过"
    return 0
  fi

  log "db: 应用 maintain schema baseline..."

  # 将快照目录复制到容器中
  if ! docker cp "${snapshot_dir}/init-maintain-schema" "${PG_CONTAINER}:/tmp/maintain-schema"; then
    warn "db: 复制 maintain schema 到容器失败，跳过"
    return 0
  fi

  # 在容器中执行 apply.sh。
  # 用 set -o pipefail 确保 apply.sh 失败时管道退出码反映失败（不被 tee 吞掉）。
  # apply.sh 内部用 psql 连 DB_URL，容器内 localhost 连接走 trust/密码均可。
  local apply_log="/tmp/maintain-schema-apply-$$.log"
  set -o pipefail
  if docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
      bash -c 'cd /tmp/maintain-schema && bash apply.sh "$@"' _ \
      "postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:5432/${POSTGRES_DB}?sslmode=disable" \
      2>&1 | tee "$apply_log"; then
    log "db: maintain schema baseline 应用成功"
  else
    warn "db: maintain schema baseline 应用失败（非致命），网关启动时会自动迁移"
    [[ -f "$apply_log" ]] && { log "db: 最后 10 行日志:"; tail -10 "$apply_log" | sed 's/^/    /'; }
  fi
  set +o pipefail

  # 清理
  docker exec "$PG_CONTAINER" rm -rf /tmp/maintain-schema 2>/dev/null || true
  rm -f "$apply_log" 2>/dev/null || true
}

# 重建前备份 PG 数据（数据非空时）。用于 stage_db 镜像变更/容器重建路径。
backup_pg_before_rebuild() {
  local bak_dir="${DATA_DIR}/backups"
  run_priv mkdir -p "$bak_dir"
  # 库是否存在/非空：pg_database 行 + datallowconn
  local has_data
  has_data="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='${POSTGRES_DB}'" 2>/dev/null | tr -d '[:space:]' || true)"
  if [[ "$has_data" != "1" ]]; then
    log "db: 库 ${POSTGRES_DB} 不存在，重建前无需备份"
    return 0
  fi
  local out
  out="${bak_dir}/pre-upgrade-${POSTGRES_DB}-$(date +%Y%m%d-%H%M%S).dump"
  log "db: 重建前备份 → ${out}"
  if docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/_preupgrade.dump 2>/dev/null; then
    docker cp "${PG_CONTAINER}:/tmp/_preupgrade.dump" "$out" 2>/dev/null
    docker exec "$PG_CONTAINER" rm -f /tmp/_preupgrade.dump 2>/dev/null || true
    [[ -s "$out" ]] && log "db: 重建前备份完成 ($(du -h "$out" | cut -f1))" || warn "db: 重建前备份为空文件"
  else
    warn "db: 重建前 pg_dump 失败 — 数据未备份，继续重建（bind-mount 仍保留物理数据）"
  fi
}

stage_db() {
  log "阶段 2: 部署 PostgreSQL"
  resolve_pg_image
  run_priv mkdir -p "$DATA_DIR"
  # PG 容器以 uid 999 运行；宿主目录需放权，否则 initdb 报 permission denied。
  if [[ "$OS" == "linux" ]]; then
    run_priv chown -R 999:999 "$DATA_DIR" 2>/dev/null || warn "chown 999 $DATA_DIR 失败（macOS/桌面版 docker 可忽略）"
  fi
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将以 ${PG_IMAGE_EFFECTIVE} 启动 ${PG_CONTAINER}，bind-mount ${DATA_DIR}"
    return 0
  fi
  # 幂等：已有同镜像容器在跑则跳过重建（避免不必要停服 + reset）。
  local existing_img=""
  if docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
    existing_img="$(docker inspect --format '{{.Config.Image}}' "$PG_CONTAINER" 2>/dev/null || true)"
  fi
  if [[ "$existing_img" == "$PG_IMAGE_EFFECTIVE" ]] && docker inspect -f '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null | grep -q true; then
    log "db: ${PG_CONTAINER} 已运行同镜像（${PG_IMAGE_EFFECTIVE}），跳过重建"
    wait_pg_ready
    seed_database
    return
  fi
  # 镜像变更或容器不存在 → 备份后重建。bind-mount 保物理数据，备份是额外安全网。
  if [[ -n "$existing_img" ]]; then
    log "db: 镜像变更（${existing_img} → ${PG_IMAGE_EFFECTIVE}）或容器已停，重建"
    backup_pg_before_rebuild
  fi
  docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$PG_CONTAINER" \
    --restart unless-stopped \
    -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
    -e POSTGRES_USER="$POSTGRES_USER" \
    -e POSTGRES_DB="$POSTGRES_DB" \
    -v "${DATA_DIR}:/var/lib/postgresql/data" \
    "$PG_IMAGE_EFFECTIVE" >/dev/null || die "db: 启动 PG 容器失败"
  wait_pg_ready
  seed_database
}

# ─── 阶段 3：部署网关 ─────────────────────────────────────────────────────────
resolve_gateway_image() {
  local target="${TARGET_VERSION:-$LATEST_VERSION}"
  GATEWAY_IMAGE_DEFAULT="llm-gateway-go:${target#v}-${ARCH}"
  if [[ -n "${GATEWAY_IMAGE_OVERRIDE:-}" ]]; then
    GATEWAY_IMAGE_EFFECTIVE="$GATEWAY_IMAGE_OVERRIDE"
    log "gateway: 使用 GATEWAY_IMAGE_OVERRIDE=${GATEWAY_IMAGE_OVERRIDE}"
    return 0
  fi
  GATEWAY_IMAGE_EFFECTIVE="$GATEWAY_IMAGE_DEFAULT"
  if docker_image_present "$GATEWAY_IMAGE_DEFAULT"; then
    log "gateway: ${GATEWAY_IMAGE_DEFAULT} 本地已存在"
    return 0
  fi
  # 离线模式：从 bundle 本地 load，不联网。
  if [[ -n "$BUNDLE_DIR" ]]; then
    if [[ "$DRY_RUN" == "1" ]]; then
      log "DRY_RUN=1 — 将从离线包加载 ${GATEWAY_IMAGE_DEFAULT}"
      return 0
    fi
    local loaded
    if loaded="$(load_bundle_image gateway gateway_tar gateway_sha256 gateway_image)"; then
      [[ -n "$loaded" ]] && GATEWAY_IMAGE_EFFECTIVE="$loaded"
      log "gateway: 已从离线包加载 ${GATEWAY_IMAGE_EFFECTIVE}"
      return 0
    fi
    die "gateway: 离线包加载 ${GATEWAY_IMAGE_DEFAULT} 失败（检查 bundle MANIFEST gateway_tar/gateway_sha256 字段、images/ 目录完整性）"
  fi
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将经 ticket 下载 ${GATEWAY_IMAGE_DEFAULT}"
    return 0
  fi
  fetch_and_load gateway "$ARCH" "$target" || \
    die "gateway: 无法获取 ${GATEWAY_IMAGE_DEFAULT}（ticket 下载失败）"
}

# 保留时间序最新的 N 个版本目录，其余删除。
rotate_versions() {
  [[ -d "$VERSIONS_DIR" ]] || return 0
  local keep="${KEEP_VERSIONS}" removed=0
  # 版本序排序（sort -V 不可用时回退 awk 分段），否则字典序会把 1.10.0 排在 1.2.0 前。
  mapfile -t all < <(ls -1d "${VERSIONS_DIR}"/*/ 2>/dev/null | version_sort)
  local total=${#all[@]}
  [[ "$total" -le "$keep" ]] && return 0
  local idx=0 remove=$((total - keep))
  for d in "${all[@]}"; do
    [[ "$idx" -lt "$remove" ]] || break
    run_priv rm -rf "${d%/}"
    log "rotate: 删除旧版本目录 ${d}"
    idx=$((idx + 1)); removed=$((removed + 1))
  done
  log "rotate: versions 保留最新 ${keep} 份，删除 ${removed} 份"
}

backup_env() {
  if [[ -f "$ENV_FILE" ]]; then
    run_priv mkdir -p "$ENV_BAK_DIR"
    local bak
    bak="${ENV_BAK_DIR}/.env.$(date +%Y%m%d%H%M%S)"
    run_priv cp -a "$ENV_FILE" "$bak"
    log "env: 已备份 → ${bak}"
  fi
}

write_env() {
  # 密码/密钥已由 ensure_password/ensure_secrets 持久化；这里只合并本阶段管理的
  # 镜像/端口/URL 等配置。用 env_kv_merge 逐项更新（而非整体重写），避免覆盖
  # 已落盘的密钥。
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将更新 ${ENV_FILE}（端口 ${GATEWAY_PORT}，库 ${POSTGRES_DB}）"
    return 0
  fi
  # gateway 子命令可能未走 stage_db（PG_IMAGE_EFFECTIVE 未设）；从 .env 或 override 补齐。
  : "${PG_IMAGE_EFFECTIVE:=$(env_get DB_IMAGE)}"
  : "${PG_IMAGE_EFFECTIVE:=${PG_IMAGE_OVERRIDE:-pg17-circus:${TARGET_VERSION:-$LATEST_VERSION}}}"
  env_kv_merge "INSTALL_ROOT" "$INSTALL_ROOT"
  env_kv_merge "GATEWAY_IMAGE" "$GATEWAY_IMAGE_EFFECTIVE"
  env_kv_merge "GATEWAY_PORT" "$GATEWAY_PORT"
  env_kv_merge "DB_IMAGE" "$PG_IMAGE_EFFECTIVE"
  env_kv_merge "POSTGRES_USER" "$POSTGRES_USER"
  env_kv_merge "POSTGRES_DB" "$POSTGRES_DB"
  env_kv_merge "DATABASE_URL" "postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${PG_CONTAINER}:5432/${POSTGRES_DB}?sslmode=disable"
  env_kv_merge "MAINTAIN_BASE" "$MAINTAIN_BASE"
  log "env: 已更新 ${ENV_FILE}"
}

write_compose() {
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将写入 ${COMPOSE_FILE}（gateway + db）"
    return 0
  fi
  cat >"$COMPOSE_FILE" <<YAML
# Generated by client-deploy.sh for ${TARGET_VERSION:-$LATEST_VERSION} ($OS/$ARCH)
services:
  ${GATEWAY_CONTAINER}:
    image: \${GATEWAY_IMAGE}
    container_name: ${GATEWAY_CONTAINER}
    ports:
      - "\${GATEWAY_PORT}:8781"
    environment:
      - LLM_GATEWAY_ENV=\${GATEWAY_ENV_MODE:-production}
      - LLM_GATEWAY_LISTEN=:8781
      - LLM_GATEWAY_DATABASE_URL=\${DATABASE_URL}
      - LLM_GATEWAY_API_KEY=\${GATEWAY_API_KEY}
      - LLM_GATEWAY_ADMIN_API_KEY=\${GATEWAY_ADMIN_API_KEY}
      - LLM_GATEWAY_JWT_SECRET=\${GATEWAY_JWT_SECRET}
      - LLM_GATEWAY_SECRET_KEY=\${GATEWAY_SECRET_KEY}
      - LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=\${GATEWAY_CRED_ENC_KEY}
      - LLM_GATEWAY_CORS_ORIGINS=\${GATEWAY_CORS_ORIGINS}
      - MAINTAIN_BASE=\${MAINTAIN_BASE}
    depends_on:
      - ${PG_CONTAINER}
    restart: unless-stopped

  ${PG_CONTAINER}:
    image: \${DB_IMAGE}
    container_name: ${PG_CONTAINER}
    environment:
      - POSTGRES_USER=\${POSTGRES_USER}
      - POSTGRES_PASSWORD=\${POSTGRES_PASSWORD}
      - POSTGRES_DB=\${POSTGRES_DB}
    volumes:
      - ${DATA_DIR}:/var/lib/postgresql/data
    restart: unless-stopped
YAML
  log "compose: 写入 ${COMPOSE_FILE}"
}

snapshot_release() {
  # 快照指定版本（默认当前目标版本）到 releases/，供回退；保留最新 KEEP_RELEASES 份。
  [[ -d "$VERSIONS_DIR" ]] || return 0
  local target="${1:-${TARGET_VERSION:-$LATEST_VERSION}}"
  local snap
  snap="${RELEASES_DIR}/${target#v}-$(date +%Y%m%d%H%M%S)"
  run_priv mkdir -p "$RELEASES_DIR"
  if [[ -d "${VERSIONS_DIR}/${target}" ]]; then
    command -v rsync >/dev/null 2>&1 \
      && run_priv rsync -a "${VERSIONS_DIR}/${target}/" "$snap/" \
      || run_priv cp -a "${VERSIONS_DIR}/${target}" "$snap"
    # 同时快照当前 compose 与 env，保证回退可完整恢复
    [[ -f "$COMPOSE_FILE" ]] && run_priv cp -a "$COMPOSE_FILE" "$snap/docker-compose.yml.snapshot"
    [[ -f "$ENV_FILE" ]] && run_priv cp -a "$ENV_FILE" "$snap/.env.snapshot"
    log "snapshot: ${snap}"
  fi
  # 保留最新 KEEP_RELEASES 份（version_sort 去掉 -ts 后缀按版本序）
  local keep="${KEEP_RELEASES}" removed=0
  mapfile -t all < <(ls -1d "${RELEASES_DIR}"/*/ 2>/dev/null | version_sort)
  local total=${#all[@]}
  [[ "$total" -le "$keep" ]] && return 0
  local idx=0 remove=$((total - keep))
  for d in "${all[@]}"; do
    [[ "$idx" -lt "$remove" ]] || break
    run_priv rm -rf "${d%/}"; idx=$((idx + 1)); removed=$((removed + 1))
  done
  log "snapshot: releases 保留最新 ${keep} 份，删除 ${removed} 份"
}

stage_gateway() {
  log "阶段 3: 下载并部署网关"
  resolve_gateway_image
  # 版本目录轮转在落盘前做，避免新版本被立即清掉。
  rotate_versions
  backup_env
  write_env
  write_compose
  snapshot_release
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 跳过 docker compose up"
    return 0
  fi
  (cd "$INSTALL_ROOT" && run_priv docker compose -f "$COMPOSE_FILE" up -d) \
    || die "compose up 失败 — 见 docker compose -f $COMPOSE_FILE logs"
  printf '%s\n' "${TARGET_VERSION:-$LATEST_VERSION}" | run_priv tee "$VERSION_FILE" >/dev/null
  log "gateway: 已切换到 ${TARGET_VERSION:-$LATEST_VERSION}"
}

# ─── 阶段 4：验证（三段：网关 healthz + DB 连通性 + API 冒烟）──────────────
dump_logs() {
  warn "打印最近日志（compose logs --tail=80）："
  (cd "$INSTALL_ROOT" && docker compose -f "$COMPOSE_FILE" logs --tail=80) >&2 2>/dev/null || true
}

stage_verify() {
  # 1. 网关 healthz（重试）
  log "阶段 4.1: 健康检查 http://127.0.0.1:${GATEWAY_PORT}/healthz"
  local ok=0 _retry
  for _retry in $(seq 1 "$HEALTH_RETRIES"); do
    if curl -fsS "http://127.0.0.1:${GATEWAY_PORT}/healthz" >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done
  if [[ "$ok" != "1" ]]; then
    warn "healthz FAILED"
    dump_logs
    return 1
  fi
  log "healthz OK"

  # 2. DB 连通性 + 网关迁移完成：users 表应存在（网关 ApplyMigrations 创建）。
  log "阶段 4.2: 验证 DB 连通性与网关迁移（${POSTGRES_DB}.users）"
  # verify/rollback 子命令可能未走 ensure_password；从 .env 补齐。
  local pg_pw="${POSTGRES_PASSWORD:-$(env_get POSTGRES_PASSWORD)}"
  local users_ok
  users_ok="$(docker exec -e PGPASSWORD="$pg_pw" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
    "SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='users'" 2>/dev/null | tr -d '[:space:]' || true)"
  if [[ "$users_ok" != "1" ]]; then
    warn "DB users 表不存在 — 网关可能未完成迁移（检查 LLM_GATEWAY_DATABASE_URL / 启动日志）"
    dump_logs
    return 1
  fi
  log "DB 连通 OK（users 表存在）"

  # 3. 关键 API 冒烟：/healthz 已过，再探一个会过鉴权层的端点确认路由可用。
  #    用 /healthz 的 200 即可代表 HTTP 栈正常；额外探 /（前端或 404）只要非 5xx。
  log "阶段 4.3: API 冒烟"
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${GATEWAY_PORT}/healthz" 2>/dev/null || echo 000)"
  if [[ "$code" == "200" ]]; then
    log "API 冒烟 OK（healthz 200）"
  else
    warn "API 冒烟异常（healthz code=${code}）"
    dump_logs
    return 1
  fi
  log "阶段 4: 全部验证通过"
  return 0
}

# ─── 阶段 5：长期运行任务 ─────────────────────────────────────────────────────
LIB_DIR="${INSTALL_ROOT}/scripts"

install_helpers() {
  # 把备份/清理脚本写到 LIB_DIR（cron 指向它们）。
  # 不能依赖源码同目录的 lib/ —— curl|bash 时 $0 是临时流，磁盘上没有 lib/。
  # 因此用 heredoc 内联生成，保证自包含。
  run_priv mkdir -p "$LIB_DIR"

  run_priv tee "$LIB_DIR/backup-db.sh" >/dev/null <<'BACKUP_EOF'
#!/usr/bin/env bash
# backup-db.sh — 由 client-deploy.sh 生成。PG 定时备份（cron/launchd 调用）。
set -euo pipefail
INSTALL_ROOT="${INSTALL_ROOT:-/opt/llm-gateway}"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
POSTGRES_USER="${POSTGRES_USER:-llm_user}"
POSTGRES_DB="${POSTGRES_DB:-llm_gateway}"
DB_BACKUP_KEEP="${DB_BACKUP_KEEP:-7}"
ENV_FILE="${INSTALL_ROOT}/.env"
BACKUP_DIR="${INSTALL_ROOT}/data/backups"
log() { echo "[backup-db] $*"; }
die() { echo "[backup-db] ERROR: $*" >&2; exit 1; }
if [[ -z "${POSTGRES_PASSWORD:-}" && -f "$ENV_FILE" ]]; then
  POSTGRES_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
[[ -n "${POSTGRES_PASSWORD:-}" ]] || die "POSTGRES_PASSWORD 未设置（亦不在 ${ENV_FILE}）"
mkdir -p "$BACKUP_DIR"
out="${BACKUP_DIR}/${POSTGRES_DB}-$(date +%Y%m%d-%H%M%S).dump"
log "pg_dump → ${out}"
if ! docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
  pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/_bkup.dump; then
  die "pg_dump 失败（容器 $PG_CONTAINER）"
fi
docker cp "${PG_CONTAINER}:/tmp/_bkup.dump" "$out"
docker exec "$PG_CONTAINER" rm -f /tmp/_bkup.dump
log "备份完成: ${out} ($(du -h "$out" | cut -f1))"
mapfile -t files < <(ls -1 "${BACKUP_DIR}"/${POSTGRES_DB}-*.dump 2>/dev/null | sort)
total=${#files[@]}
if [[ "$total" -gt "$DB_BACKUP_KEEP" ]]; then
  remove=$((total - DB_BACKUP_KEEP))
  for f in "${files[@]:0:remove}"; do rm -f "$f"; log "轮转删除: $(basename "$f")"; done
fi
BACKUP_EOF

  run_priv tee "$LIB_DIR/clean-logs.sh" >/dev/null <<'CLEAN_EOF'
#!/usr/bin/env bash
# clean-logs.sh — 由 client-deploy.sh 生成。请求日志定时清理（cron/launchd 调用）。
set -euo pipefail
INSTALL_ROOT="${INSTALL_ROOT:-/opt/llm-gateway}"
PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
POSTGRES_USER="${POSTGRES_USER:-llm_user}"
POSTGRES_DB="${POSTGRES_DB:-llm_gateway}"
LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS:-30}"
ENV_FILE="${INSTALL_ROOT}/.env"
log() { echo "[clean-logs] $*"; }
die() { echo "[clean-logs] ERROR: $*" >&2; exit 1; }
if [[ -z "${POSTGRES_PASSWORD:-}" && -f "$ENV_FILE" ]]; then
  POSTGRES_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
[[ -n "${POSTGRES_PASSWORD:-}" ]] || die "POSTGRES_PASSWORD 未设置"
psql_exec() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1"
}
LOG_TABLE="$(psql_exec "SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('request_logs','request_logs_hot') LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"
[[ -n "$LOG_TABLE" ]] || { log "未找到 request_logs 表，跳过"; exit 0; }
COL="$(psql_exec "SELECT column_name FROM information_schema.columns WHERE table_name='${LOG_TABLE}' AND column_name IN ('created_at','requested_at','created_time') LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"
[[ -n "$COL" ]] || { log "${LOG_TABLE} 无可识别时间列，跳过"; exit 0; }
cutoff="$(date -u -d "${LOG_RETENTION_DAYS} days ago" +%Y-%m-%dT00:00:00Z 2>/dev/null \
  || date -u -v-${LOG_RETENTION_DAYS}d +%Y-%m-%dT00:00:00Z 2>/dev/null || echo "")"
[[ -n "$cutoff" ]] || die "无法计算截止日期"
count_before="$(psql_exec "SELECT count(*) FROM ${LOG_TABLE} WHERE ${COL} < '${cutoff}'" 2>/dev/null || echo 0)"
log "${LOG_TABLE}.${COL} < ${cutoff}：待清理 ${count_before} 行"
if [[ "$count_before" -gt 0 ]] 2>/dev/null; then
  deleted=0
  while :; do
    n="$(psql_exec "WITH del AS (DELETE FROM ${LOG_TABLE} WHERE ctid IN (SELECT ctid FROM ${LOG_TABLE} WHERE ${COL} < '${cutoff}' LIMIT 5000) RETURNING 1) SELECT count(*) FROM del" 2>/dev/null || echo 0)"
    n="${n:-0}"; [[ "$n" -gt 0 ]] 2>/dev/null || break
    deleted=$((deleted + n)); log "已删除 ${deleted} 行（本批 ${n}）"
  done
  log "清理完成：共删除 ${deleted} 行"
else
  log "无需清理"
fi
CLEAN_EOF

  # rotate 也落地为独立脚本：cron 不能依赖主脚本（curl|bash 时主脚本不在磁盘）。
  run_priv tee "$LIB_DIR/rotate.sh" >/dev/null <<ROTATE_EOF
#!/usr/bin/env bash
# rotate.sh — 由 client-deploy.sh 生成。版本目录/快照轮转（cron 调用）。
set -euo pipefail
INSTALL_ROOT="\${INSTALL_ROOT:-${INSTALL_ROOT}}"
KEEP_VERSIONS="\${KEEP_VERSIONS:-${KEEP_VERSIONS}}"
KEEP_RELEASES="\${KEEP_RELEASES:-${KEEP_RELEASES}}"
VERSIONS_DIR="\${INSTALL_ROOT}/versions"
RELEASES_DIR="\${INSTALL_ROOT}/releases"
version_sort() { sort -V "\$@" 2>/dev/null || awk -F/ '{s=\$NF;sub(/-.*\$/,"",s);n=split(s,p,".");k="";for(i=1;i<=4;i++)k=sprintf("%s%020d",k,p[i]+0);print k"\\t"\$0}'|sort -k1,1|cut -f2-; }
rotate_dir() {
  local dir="\$1" keep="\$2" what="\$3"
  [[ -d "\$dir" ]] || return 0
  mapfile -t all < <(ls -1d "\$dir"/*/ 2>/dev/null | version_sort)
  local total=\${#all[@]}
  [[ "\$total" -gt "\$keep" ]] || return 0
  local idx=0 remove=\$((total - keep)) removed=0
  for d in "\${all[@]}"; do
    [[ "\$idx" -lt "\$remove" ]] || break
    rm -rf "\${d%/}"; idx=\$((idx+1)); removed=\$((removed+1))
  done
  echo "[rotate] \${what}: 保留 \${keep} 份，删除 \${removed} 份"
}
rotate_dir "\$VERSIONS_DIR" "\$KEEP_VERSIONS" "versions"
rotate_dir "\$RELEASES_DIR" "\$KEEP_RELEASES" "releases"
ROTATE_EOF

  run_priv chmod +x "$LIB_DIR"/*.sh 2>/dev/null || true
  log "helpers: 已生成 → ${LIB_DIR}/{backup-db,clean-logs,rotate}.sh"
}

setup_cron_linux() {
  # 写 /etc/cron.d（root）；备份每日 03:00，清理每周日 04:00。
  local cronfile="/etc/cron.d/llm-gateway-maintain"
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将写入 ${cronfile}（备份 03:00 / 清理 周日 04:00）"
    return 0
  fi
  run_priv tee "$cronfile" >/dev/null <<CRON
# Managed by client-deploy.sh — do not edit; rerun 'client-deploy.sh setup-cron' to update.
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
KEEP_VERSIONS=${KEEP_VERSIONS}
KEEP_RELEASES=${KEEP_RELEASES}
DB_BACKUP_KEEP=${DB_BACKUP_KEEP}
LOG_RETENTION_DAYS=${LOG_RETENTION_DAYS:-30}
INSTALL_ROOT=${INSTALL_ROOT}

# 每日 03:00 备份 PG
0 3 * * * root ${LIB_DIR}/backup-db.sh >> ${INSTALL_ROOT}/.backup.log 2>&1
# 每周日 04:00 清理请求日志
0 4 * * 0 root ${LIB_DIR}/clean-logs.sh >> ${INSTALL_ROOT}/.clean.log 2>&1
# 每周日 02:30 版本目录轮转（独立脚本，不依赖主 client-deploy.sh 在磁盘上）
30 2 * * 0 root ${LIB_DIR}/rotate.sh >> ${INSTALL_ROOT}/.rotate.log 2>&1
CRON
  run_priv chmod 644 "$cronfile"
  log "cron: 已安装 ${cronfile}（重启 cron 由系统自动识别；无需 reload）"
}

setup_cron_macos() {
  local label="cn.kxpms.llm-gateway-maintain"
  local plist_dir="$HOME/Library/LaunchAgents"
  local plist="${plist_dir}/${label}.plist"
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将写入 ${plist}"
    return 0
  fi
  mkdir -p "$plist_dir"
  cat >"$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>${label}</string>
  <key>ProgramArguments</key><array>
    <string>/bin/bash</string><string>${LIB_DIR}/backup-db.sh</string>
  </array>
  <key>StartCalendarInterval</key><dict>
    <key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer>
  </key>
  <key>StandardOutPath</key><string>${INSTALL_ROOT}/.backup.log</string>
  <key>StandardErrorPath</key><string>${INSTALL_ROOT}/.backup.log</string>
</dict></plist>
PLIST
  # 单独的清理任务
  local plist2="${plist_dir}/${label}.clean.plist"
  cat >"$plist2" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>${label}.clean</string>
  <key>ProgramArguments</key><array>
    <string>/bin/bash</string><string>${LIB_DIR}/clean-logs.sh</string>
  </array>
  <key>StartCalendarInterval</key><dict>
    <key>Weekday</key><integer>0</integer><key>Hour</key><integer>4</integer><key>Minute</key><integer>0</integer>
  </key>
  <key>StandardOutPath</key><string>${INSTALL_ROOT}/.clean.log</string>
  <key>StandardErrorPath</key><string>${INSTALL_ROOT}/.clean.log</string>
</dict></plist>
PLIST
  launchctl unload "$plist" >/dev/null 2>&1 || true
  launchctl unload "$plist2" >/dev/null 2>&1 || true
  launchctl load "$plist" || warn "launchctl load 失败（$plist）"
  launchctl load "$plist2" || warn "launchctl load 失败（$plist2）"
  log "launchd: 已安装 ${plist} 与清理任务"
}

stage_setup_cron() {
  log "阶段 5: 建立长期运行任务"
  install_helpers
  case "$OS" in
    linux)  setup_cron_linux ;;
    darwin) setup_cron_macos ;;
  esac
  log "阶段 5 完成：DB 备份 + 日志清理 + 版本轮转 已注册"
}

# 生成密码：openssl > /dev/urandom > python3 secrets。绝不返回弱默认。
gen_password() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 18 2>/dev/null | tr -d '/+=' | cut -c1-24 && return
  fi
  if [[ -r /dev/urandom ]]; then
    tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 24 && return
  fi
  python3 -c 'import secrets,string;print("".join(secrets.choice(string.ascii_letters+string.digits) for _ in range(24)))' 2>/dev/null && return
  die "无法生成随机密码（缺 openssl/urandom/python3）"
}

# 在任何使用 POSTGRES_PASSWORD 的阶段之前确定密码。
# 优先级：显式 env > 已有 .env > 生成新密码并立即落盘到 .env。
# 必须在 stage_db 与 write_env 之前调用，否则 stage_db 用空密码初始化 PG、
# write_env 又生成新密码 → 鉴权永久不一致。
ensure_password() {
  if [[ -z "${POSTGRES_PASSWORD:-}" && -f "$ENV_FILE" ]]; then
    POSTGRES_PASSWORD="$(grep -E '^POSTGRES_PASSWORD=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)"
  fi
  if [[ -z "${POSTGRES_PASSWORD:-}" ]]; then
    POSTGRES_PASSWORD="$(gen_password)"
    log "db: 已生成新 POSTGRES_PASSWORD"
  fi
  env_kv_merge "POSTGRES_PASSWORD" "$POSTGRES_PASSWORD"
}

# 把 KEY=VALUE 写入/更新到 .env（存在则替换该行，不存在则追加）。
# 所有持久化配置（密码、密钥）都经此，保证单一来源、原子替换、权限 600。
env_kv_merge() {
  local key="$1" value="$2"
  run_priv mkdir -p "$INSTALL_ROOT"
  local tmp="${ENV_FILE}.kv.$$"
  printf '%s=%s\n' "$key" "$value" >"$tmp"
  run_priv chmod 600 "$tmp"
  if [[ -f "$ENV_FILE" ]]; then
    grep -vE "^${key}=" "$ENV_FILE" 2>/dev/null | cat "$tmp" - >"${tmp}.2" \
      && run_priv mv "${tmp}.2" "$ENV_FILE" || run_priv mv "$tmp" "$ENV_FILE"
    run_priv rm -f "$tmp"
  else
    run_priv mv "$tmp" "$ENV_FILE"
  fi
}

# 生成较长的 base64 token（用于网关密钥，长度 >= 32 字节）。
gen_token() {
  local len="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$len" 2>/dev/null | tr -d '\n' && return
  fi
  if [[ -r /dev/urandom ]]; then
    head -c "$len" /dev/urandom | base64 | tr -d '\n' && return
  fi
  python3 -c "import secrets,base64;print(base64.b64encode(secrets.token_bytes($len)).decode())" 2>/dev/null && return
  die "无法生成随机 token（缺 openssl/urandom/python3）"
}

# 从 .env 读某 key 的值（不存在返回空）。
env_get() {
  local key="$1"
  [[ -f "$ENV_FILE" ]] || return 0
  grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true
}

# 网关密钥：首次随机生成 + 持久化 .env，后续复用。
# 升级绝不能轮换密钥（否则已签发的 JWT/凭据全失效），所以一旦 .env 里有就复用。
ensure_secrets() {
  local generated=0
  # API_KEY / ADMIN_API_KEY：可选前缀，便于识别来源。
  [[ -z "$GATEWAY_API_KEY" ]] && GATEWAY_API_KEY="$(env_get GATEWAY_API_KEY)"
  if [[ -z "$GATEWAY_API_KEY" ]]; then
    GATEWAY_API_KEY="gw_$(gen_token 24)"; generated=1
  fi
  [[ -z "$GATEWAY_ADMIN_API_KEY" ]] && GATEWAY_ADMIN_API_KEY="$(env_get GATEWAY_ADMIN_API_KEY)"
  if [[ -z "$GATEWAY_ADMIN_API_KEY" ]]; then
    GATEWAY_ADMIN_API_KEY="ops_$(gen_token 32)"; generated=1
  fi
  # JWT_SECRET / SECRET_KEY：>=32 字节
  [[ -z "$GATEWAY_JWT_SECRET" ]] && GATEWAY_JWT_SECRET="$(env_get GATEWAY_JWT_SECRET)"
  if [[ -z "$GATEWAY_JWT_SECRET" ]]; then
    GATEWAY_JWT_SECRET="$(gen_token 48)"; generated=1
  fi
  [[ -z "$GATEWAY_SECRET_KEY" ]] && GATEWAY_SECRET_KEY="$(env_get GATEWAY_SECRET_KEY)"
  if [[ -z "$GATEWAY_SECRET_KEY" ]]; then
    GATEWAY_SECRET_KEY="$(gen_token 32)"; generated=1
  fi
  # CREDENTIAL_ENCRYPTION_KEY：AES 密钥（base64，32 字节 = AES-256）
  [[ -z "$GATEWAY_CRED_ENC_KEY" ]] && GATEWAY_CRED_ENC_KEY="$(env_get GATEWAY_CRED_ENC_KEY)"
  if [[ -z "$GATEWAY_CRED_ENC_KEY" ]]; then
    GATEWAY_CRED_ENC_KEY="$(gen_token 32)"; generated=1
  fi
  # CORS：有默认，允许 env 覆盖；不强制持久化但写入以便审计。
  [[ -n "$GATEWAY_CORS_ORIGINS" ]] || GATEWAY_CORS_ORIGINS="http://127.0.0.1:${GATEWAY_PORT},http://localhost:${GATEWAY_PORT}"
  if [[ "$generated" == "1" ]]; then
    log "gateway: 已生成新密钥集（API_KEY/ADMIN_API_KEY/JWT_SECRET/SECRET_KEY/CRED_ENC_KEY）"
  else
    log "gateway: 复用 .env 中已有密钥（升级不轮换）"
  fi
  # 持久化全部（DRY_RUN 下也写 .env —— 密钥必须落盘，否则下次 deploy 又生成新的）。
  env_kv_merge "GATEWAY_ENV_MODE" "$GATEWAY_ENV_MODE"
  env_kv_merge "GATEWAY_API_KEY" "$GATEWAY_API_KEY"
  env_kv_merge "GATEWAY_ADMIN_API_KEY" "$GATEWAY_ADMIN_API_KEY"
  env_kv_merge "GATEWAY_JWT_SECRET" "$GATEWAY_JWT_SECRET"
  env_kv_merge "GATEWAY_SECRET_KEY" "$GATEWAY_SECRET_KEY"
  env_kv_merge "GATEWAY_CRED_ENC_KEY" "$GATEWAY_CRED_ENC_KEY"
  env_kv_merge "GATEWAY_CORS_ORIGINS" "$GATEWAY_CORS_ORIGINS"
}

# ─── 子命令实现 ───────────────────────────────────────────────────────────────
resolve_target_version() {
  if [[ -n "${VERSION:-}" ]]; then
    TARGET_VERSION="${VERSION}"
  elif [[ -n "$BUNDLE_DIR" ]]; then
    # 离线模式：版本来自 bundle MANIFEST，不联网。
    TARGET_VERSION="$(manifest_field version)"
    [[ -n "$TARGET_VERSION" ]] || die "离线包 MANIFEST 缺 version 字段"
  else
    TARGET_VERSION="$(latest_version)"
    [[ -n "$TARGET_VERSION" ]] || die "无法从 ${MAINTAIN_BASE} 获取最新版本（设 VERSION= 显式指定）"
  fi
  LATEST_VERSION="$TARGET_VERSION"
  log "目标版本: ${TARGET_VERSION}（当前: ${CURRENT_VERSION:-none}）$( [[ -n "$BUNDLE_DIR" ]] && echo ' [offline]' )"
}

cmd_deploy() {
  ensure_root_dir
  resolve_target_version
  # 密码/密钥必须在 stage_db / write_env 之前确定并落盘。
  ensure_password
  ensure_secrets
  # 升级路径（VERSION_FILE 已存在且版本不同）：先 snapshot 当前版本/compose/.env，
  # 验证失败时用它自动回退。
  local is_upgrade=0
  if [[ -n "$CURRENT_VERSION" && "${CURRENT_VERSION#v}" != "${TARGET_VERSION#v}" ]]; then
    is_upgrade=1
    log "升级路径: ${CURRENT_VERSION} → ${TARGET_VERSION}，预先生成回退快照"
    snapshot_release "$CURRENT_VERSION"
  fi
  stage_docker
  stage_db
  stage_gateway
  if stage_verify; then
    stage_setup_cron
    append_changelog deploy "$TARGET_VERSION" ok
    log "✅ 部署完成: ${TARGET_VERSION} @ http://127.0.0.1:${GATEWAY_PORT}"
    log "   数据目录: ${DATA_DIR}"
    log "   版本目录: ${VERSIONS_DIR}（保留 ${KEEP_VERSIONS} 份）"
    log "   回退快照: ${RELEASES_DIR}（保留 ${KEEP_RELEASES} 份）"
  else
    append_changelog deploy "$TARGET_VERSION" failed "verify"
    if [[ "$is_upgrade" == "1" ]]; then
      warn "验证失败 — 自动回退到升级前快照"
      if cmd_rollback "$CURRENT_VERSION"; then
        append_changelog upgrade "$TARGET_VERSION" failed "auto_rolled_back_to_${CURRENT_VERSION}"
        die "已自动回退到 ${CURRENT_VERSION}（本次升级到 ${TARGET_VERSION} 失败）"
      else
        append_changelog upgrade "$TARGET_VERSION" failed "auto_rollback_also_failed"
        die "验证失败且回退未成功 — 请手动检查 docker compose logs 与 releases/ 快照"
      fi
    else
      die "首次部署验证失败 — 无可回退版本，请检查 docker compose logs"
    fi
  fi
}

cmd_rollback() {
  # 版本经 ${@:2} 传入（$1 是子命令名）；cmd_deploy 自动回退也走同一约定。
  local want="${1:-}"
  ensure_root_dir
  if [[ ! -d "$RELEASES_DIR" ]] || [[ -z "$(ls -A "$RELEASES_DIR" 2>/dev/null)" ]]; then
    die "无可用回退快照（${RELEASES_DIR} 为空）"
  fi
  # 选择快照：指定 ver 取最新匹配；否则取时间序最新。
  local snap=""
  if [[ -n "$want" ]]; then
    snap="$(ls -1d "${RELEASES_DIR}"/${want#v}-*/ 2>/dev/null | sort | tail -1)"
  else
    snap="$(ls -1d "${RELEASES_DIR}"/*/ 2>/dev/null | sort | tail -1)"
  fi
  [[ -n "$snap" && -d "$snap" ]] || die "未找到匹配的回退快照（want=${want:-latest}）"
  snap="${snap%/}"
  log "rollback: 从 ${snap} 恢复"
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN=1 — 将恢复 ${snap}/.env.snapshot 与 docker-compose.yml.snapshot 并 compose up"
    return 0
  fi
  if [[ -f "${snap}/.env.snapshot" ]]; then
    run_priv mkdir -p "$ENV_BAK_DIR"
    [[ -f "$ENV_FILE" ]] && run_priv cp -a "$ENV_FILE" "${ENV_BAK_DIR}/.env.$(date +%Y%m%d%H%M%S).prerollback"
    run_priv cp -a "${snap}/.env.snapshot" "$ENV_FILE"
    run_priv chmod 600 "$ENV_FILE"
  fi
  if [[ -f "${snap}/docker-compose.yml.snapshot" ]]; then
    run_priv cp -a "${snap}/docker-compose.yml.snapshot" "$COMPOSE_FILE"
  fi
  (cd "$INSTALL_ROOT" && run_priv docker compose -f "$COMPOSE_FILE" up -d) \
    || die "rollback compose up 失败"
  if stage_verify; then
    append_changelog rollback "${snap##*/}" ok
    log "✅ 回退成功: ${snap##*/}"
  else
    append_changelog rollback "${snap##*/}" failed "health_check"
    die "回退后健康检查仍失败 — 请检查 docker compose logs"
  fi
}

cmd_rotate() {
  # 独立 rotate 只做清理；snapshot 在部署流程（cmd_deploy）中产生。
  rotate_versions
  # releases/ 轮转：复用 snapshot_release 的保留逻辑，但不创建新快照。
  if [[ -d "$RELEASES_DIR" ]]; then
    local keep="${KEEP_RELEASES}"
      mapfile -t all < <(ls -1d "${RELEASES_DIR}"/*/ 2>/dev/null | version_sort)
      local total=${#all[@]} removed=0
      if [[ "$total" -gt "$keep" ]]; then
        local idx=0 remove=$((total - keep))
        for d in "${all[@]}"; do
          [[ "$idx" -lt "$remove" ]] || break
          run_priv rm -rf "${d%/}"; idx=$((idx + 1)); removed=$((removed + 1))
        done
        log "rotate: releases 保留最新 ${keep} 份，删除 ${removed} 份"
    fi
  fi
  log "rotate 完成"
}

cmd_list() {
  log "当前版本: ${CURRENT_VERSION:-none}"
  if [[ -d "$VERSIONS_DIR" ]]; then
    echo "[client-deploy] 已下载版本:"
    ls -1d "${VERSIONS_DIR}"/*/ 2>/dev/null | sed 's#^#  #'
  fi
  if [[ -d "$RELEASES_DIR" ]]; then
    echo "[client-deploy] 回退快照:"
    ls -1d "${RELEASES_DIR}"/*/ 2>/dev/null | sed 's#^#  #'
  fi
  if [[ -f "$CHANGELOG" ]]; then
    echo "[client-deploy] 最近变更（末 10 行）:"
    tail -10 "$CHANGELOG" | sed 's#^#  #'
  fi
}

cmd_record() {
  local action="${2:-deploy}" ver="${3:-${CURRENT_VERSION}}" status="${4:-ok}"
  append_changelog "$action" "$ver" "$status"
  log "record: ${action} ver=${ver} status=${status}"
}

show_help() { sed -n '2,40p' "$0"; }

# ─── 入口 ─────────────────────────────────────────────────────────────────────
detect_platform
init_offline_bundle

# host mode：委托 install-host.sh（包内 install.sh 走 lifecycle/install-service.sh 注册服务）。
# docker mode：原 compose 路径（cmd_deploy）。
cmd_deploy_host() {
  local script_dir host_script
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  host_script="${script_dir}/install-host.sh"
  if [[ ! -x "$host_script" ]]; then
    echo "[client-deploy] host mode 需要 $host_script (请确认 scripts/user/install-host.sh 存在)" >&2
    exit 1
  fi
  log "host mode: 委托 install-host.sh (INSTALL_ROOT=$INSTALL_ROOT)"
  INSTALL_ROOT="$INSTALL_ROOT" MAINTAIN_BASE="$MAINTAIN_BASE" CHANNEL="$CHANNEL" VERSION="$VERSION" \
  ARCH="$ARCH" PLATFORM="$OS" DRY_RUN="$DRY_RUN" NO_INTERACTIVE="${NO_INTERACTIVE:-1}" \
    bash "$host_script"
}

case "$CMD" in
  deploy|run|full|"")
    mode="$(resolve_install_mode)"
    log "install-mode=$mode (auto from INSTALL_ROOT=$INSTALL_ROOT)"
    if [[ "$mode" == "host" ]]; then
      cmd_deploy_host
    else
      cmd_deploy
    fi ;;
  docker)            ensure_root_dir; stage_docker ;;
  db)                ensure_root_dir; resolve_target_version; ensure_password; stage_db ;;
  gateway)           ensure_root_dir; resolve_target_version; ensure_password; ensure_secrets; stage_gateway; stage_verify ;;
  verify)            stage_verify ;;
  setup-cron|cron)   ensure_root_dir; stage_setup_cron ;;
  helpers)           ensure_root_dir; install_helpers ;;
  secrets)           ensure_root_dir; ensure_secrets ;;
  rotate)            ensure_root_dir; cmd_rotate ;;
  rollback)          cmd_rollback "${@:2}" ;;
  list)              cmd_list ;;
  record)            cmd_record "$@" ;;
  -h|--help|help)    show_help ;;
  *) echo "unknown command: $CMD (deploy|docker|db|gateway|verify|setup-cron|helpers|secrets|rotate|rollback|list|record)" >&2; exit 2 ;;
esac
