#!/usr/bin/env bash
# install.sh — llm-gateway-go 安装入口：先确认安装需求，再派发到对应规模的安装器。
#
# 这是下载页与文档里唯一需要用户记住的命令：
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash
#
# 它自己不做安装，只做三件事，然后把控制权交给 host / docker 安装器：
#   1. 体检（doctor）：平台、架构、命令依赖、磁盘、权限、Docker；
#   2. 需求确认（mode）：lite = 本地小规模（主机二进制 + SQLite）
#                        full = 全量（Docker 编排 + PostgreSQL + Redis）
#   3. 派发：把 CHANNEL/VERSION/INSTALL_ROOT 等环境原样传给对应安装器。
#
# 非交互（CI / 无人值守）：
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | MODE=full bash
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash -s -- --mode full --yes
#
# 只看版本与获取方式（不安装）：
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash -s -- --check
#
# 用法：
#   bash install.sh [install|doctor|check|modes|help] [选项]
#
# 选项：
#   --mode lite|full   跳过模式提问
#   --yes              接受默认值，不再提问
#   --check            只查新版本与获取方式后退出
#   --doctor           只体检后退出
#   --dry-run          只演练（转交 DRY_RUN=1）
#
# Env：
#   MODE            lite | full（比选项更弱的默认值来源，仍可被 --mode 覆盖）
#   CHANNEL         发布通道（默认 stable）
#   VERSION         指定版本（默认取通道上最新版）
#   MAINTAIN_BASE   Maintain 入口（默认 https://llmgo.kxpms.cn/maintain-api）
#   INSTALL_ROOT    安装根（透传给 lite 模式）
#   COMPOSE_DIR     编排目录（透传给 full 模式）
#   NO_INTERACTIVE  1 = 不提问
#   DRY_RUN         1 = 只演练
#   NO_SUDO         1 = 不提权（容器 / rootless 环境）
#
# 可移植性硬约束（改动前先看 scripts/tests/user-scripts-portability-test.sh）：
#   - 不依赖 python3 / jq；本文件不做 JSON 解析，版本查询整段委托给 upgrade.sh。
#   - 不用 bash 4.0+ 语法（mapfile / ${var,,}）：下载页把 darwin 列为正式平台，
#     而 macOS 自带的 /bin/bash 是 3.2。
set -euo pipefail

MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
MODE="${MODE:-}"
YES="${YES:-0}"
ACTION="install"

log()  { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARN: %s\n' "$*" >&2; }
die()  { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

# 帮助文本用 here-doc 写死，不从脚本自身 sed 出来：`curl … | bash` 管道路径下
# BASH_SOURCE 为空，${BASH_SOURCE[0]} 展开成 shell 名 "bash"（同一类缺陷在
# lib/kaixuan-layout.sh 上已经犯过一次，见 90b84c0）。
usage() {
  cat <<'USAGEEOF'
install.sh — llm-gateway-go 安装入口：先确认安装需求，再派发到对应规模的安装器。

用法：
  bash install.sh [install|doctor|check|modes|help] [选项]

选项：
  --mode lite|full   跳过模式提问
  --yes              接受默认值，不再提问
  --check            只查新版本与获取方式后退出
  --doctor           只体检后退出
  --dry-run          只演练（转交 DRY_RUN=1）

环境变量：
  MODE / CHANNEL / VERSION / MAINTAIN_BASE / INSTALL_ROOT / COMPOSE_DIR
  NO_INTERACTIVE=1 / DRY_RUN=1 / NO_SUDO=1

示例：
  curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash
  curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | MODE=full bash
  curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash -s -- --check
USAGEEOF
}

# 非交互判定：--yes / NO_INTERACTIVE / 演练、或 stdin 不是终端时都不提问。
interactive() {
  [[ "$YES" == "1" ]] && return 1
  [[ "${NO_INTERACTIVE:-0}" == "1" ]] && return 1
  [[ "${DRY_RUN:-0}" == "1" ]] && return 1
  [ -t 0 ] || return 1
  return 0
}

# ── 平台探测 ──────────────────────────────────────────────────────────
# PLATFORM 显式给出时直接采信：host / docker 安装器本来就用它选平台制品，
# 这里必须得出同一个结论，否则会出现「按 linux 派发、装 linux 制品」在
# Windows 上跑通的假象。留空才真去 uname。
detect_os() {
  if [[ -n "${PLATFORM:-}" ]]; then printf '%s\n' "$PLATFORM"; return 0; fi
  local s
  s="$(uname -s 2>/dev/null || printf 'unknown')"
  case "$s" in
    Linux)               printf 'linux\n' ;;
    Darwin)              printf 'darwin\n' ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT) printf 'windows\n' ;;
    *)                   printf '%s\n' "$s" ;;
  esac
}

detect_arch() {
  local a
  a="$(uname -m 2>/dev/null || printf 'unknown')"
  case "$a" in
    x86_64|amd64)       printf 'amd64\n' ;;
    aarch64|arm64)      printf 'arm64\n' ;;
    loongarch64|loong64) printf 'loong64\n' ;;
    *)                  printf '%s\n' "$a" ;;
  esac
}

# 校验和工具：sha256 是安装硬门（服务端未登记校验和时安装器会拒绝安装），
# 所以体检阶段就要把「算不出 sha256」挡下来，而不是等下载完才失败。
sha256_cmd() {
  if command -v sha256sum >/dev/null 2>&1; then printf 'sha256sum\n'
  elif command -v shasum >/dev/null 2>&1; then printf 'shasum\n'
  elif command -v openssl >/dev/null 2>&1; then printf 'openssl dgst -sha256\n'
  else printf '\n'
  fi
}

has_docker_compose() {
  # KXMAINT_STUB_DOCKER=1：测试专用，模拟"这台机器有 Docker"。构建机多半
  # 没有 Docker（Windows/Git Bash 上尤其如此），而 full 模式的前置门与
  # 派发链路必须在没有 Docker 的机器上也能被自动化覆盖，否则这条路径只能
  # 靠人手点。语义与上条 commit 给 releasebuild 注入 dirOf 复现 Windows
  # 路径的做法一致。
  [[ "${KXMAINT_STUB_DOCKER:-0}" == "1" ]] && return 0
  command -v docker >/dev/null 2>&1 || return 1
  docker compose version >/dev/null 2>&1 && return 0
  command -v docker-compose >/dev/null 2>&1
}

disk_free_mb() {
  df -Pk "$(pwd)" 2>/dev/null | awk 'NR==2 { printf "%d", $4/1024 }' || printf '0\n'
}

# ── 体检 ──────────────────────────────────────────────────────────────
# 输出人可读的体检报告，并写全局 HAVE_SHA256 / HAVE_DOCKER / OS_KIND。
doctor_report() {
  local os arch sha bashv free_mb
  os="$(detect_os)"; arch="$(detect_arch)"; sha="$(sha256_cmd)"
  bashv="${BASH_VERSION:-unknown}"
  free_mb="$(disk_free_mb)"

  HAVE_SHA256=0; [[ -n "$sha" ]] && HAVE_SHA256=1
  HAVE_DOCKER=0; has_docker_compose && HAVE_DOCKER=1
  OS_KIND="$os"

  log "体检报告"
  printf '  %-12s %s\n' "系统"     "${os}/${arch} ($(uname -sr 2>/dev/null || printf '?'))"
  printf '  %-12s %s\n' "bash"     "$bashv"
  printf '  %-12s %s\n' "curl"     "$(command -v curl >/dev/null 2>&1 && command -v curl || printf '缺失（必需）')"
  printf '  %-12s %s\n' "sha256"   "${sha:-缺失（必需：安装器硬门）}"
  printf '  %-12s %s\n' "tar"      "$(command -v tar >/dev/null 2>&1 && command -v tar || printf '缺失（lite 需要）')"
  printf '  %-12s %s\n' "docker"   "$(has_docker_compose && docker --version 2>/dev/null || printf '不可用（full 需要）')"
  printf '  %-12s %s\n' "权限"     "$([ "$(id -u 2>/dev/null || printf '1')" = "0" ] && printf 'root' || { command -v sudo >/dev/null 2>&1 && printf '非 root，可用 sudo 提权' || printf '非 root，无 sudo'; })"
  printf '  %-12s %s\n' "磁盘"     "${free_mb} MB 可用"
  printf '\n'
}

# ── 两种规模模式 ──────────────────────────────────────────────────────
print_modes() {
  cat <<'MODEEOF'
两种安装规模：

  [1] lite — 本地小规模
      主机二进制 + SQLite，不装 PostgreSQL / Redis
      前置：bash curl tar sha256；需要能写安装根
      适合：单机、内网、评估、CI
      安装到：Linux /opt/kaixuan/llm-gateway-go，macOS ~/kaixuan/llm-gateway-go

  [2] full — 全量
      Docker 编排 + PostgreSQL + Redis
      前置：bash curl sha256 docker（含 docker compose）
      适合：生产、多副本、高并发
      编排目录：$COMPOSE_DIR（默认 $PWD/llm-gateway-docker）

MODEEOF
}

# 选定模式的前置条件硬门：在下载任何东西之前先挡住明显跑不起来的组合。
# 历史上 host / docker 安装器是「下载全部成功之后才失败」，用户白等一轮。
require_mode_prereqs() {
  local mode="$1" missing=""
  case "$mode" in
    lite)
      if [[ "$HAVE_SHA256" != "1" ]]; then missing="${missing} sha256sum/shasum"; fi
      if ! command -v tar >/dev/null 2>&1; then missing="${missing} tar"; fi
      if [[ "$OS_KIND" == "windows" ]]; then
        warn "lite（主机二进制模式）在 Windows 上不适用：主机脚本按设计拒绝并指向 client-deploy.ps1。"
        warn "Windows 客户机请用："
        warn "  powershell -ExecutionPolicy Bypass -File client-deploy.ps1"
        warn "或用 full 模式（需 Docker Desktop + WSL2）。"
        exit 2
      fi
      ;;
    full)
      if [[ "$HAVE_SHA256" != "1" ]]; then missing="${missing} sha256sum/shasum"; fi
      if [[ "$HAVE_DOCKER" != "1" ]]; then missing="${missing} docker(docker compose)"; fi
      ;;
    *)
      die "未知模式 '${mode}'（应为 lite 或 full）"
      ;;
  esac
  if [[ -n "$missing" ]]; then
    die "选定 ${mode} 模式，但缺少前置依赖：${missing# }"
  fi
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    die "选定 ${mode} 模式，但缺少 curl 或 wget，无法取回安装器"
  fi
}

# ── 交互式需求确认 ────────────────────────────────────────────────────
confirm_mode() {
  if [[ -n "$MODE" ]]; then
    log "模式由环境变量指定：${MODE}"
    return 0
  fi
  if ! interactive; then
    MODE="lite"
    log "非交互（NO_INTERACTIVE/DRY_RUN/非 tty），默认模式 lite（本地小规模）"
    return 0
  fi
  print_modes
  local reply=""
  printf '请选择模式 [1]: '
  read -r reply || true
  case "${reply:-1}" in
    2|full)  MODE="full" ;;
    1|lite|"") MODE="lite" ;;
    *) warn "无法识别 '${reply}'，按默认 lite 处理" ; MODE="lite" ;;
  esac
  log "已选择模式：${MODE}"
}

# ── 取回并转交子安装器 ────────────────────────────────────────────────
# 子安装器不在本文件里内嵌：embed 只带 *.sh，而 host / docker 已经是
# 各自独立分发的两条路径。入口按需取回，避免三份逻辑分叉。
run_installer() {
  # 第一个参数是被取回的脚本名，其余参数原样透传给子安装器。
  # 透传不是锦上添花：upgrade.sh 的 `CMD="${1:-run}"`，少传一个子命令就会
  # 静默变成"执行完整升级"（下载→安装→切换→测试→上报）。`--check` 曾经因为
  # 少传 `list` 而真的去升级机器，与它自己的帮助文本"只查不装"相反。
  local script="$1" tmpdir tmp url
  shift
  url="${MAINTAIN_BASE}/distribution/install-scripts/${script}"
  tmpdir="$(mktemp -d 2>/dev/null || printf '%s/llmgo-install-%s' "${TMPDIR:-/tmp}" "$$")"
  mkdir -p "$tmpdir"
  tmp="${tmpdir}/install-${script}.sh"
  log "取回安装器：${url}"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 2 -o "$tmp" "$url" \
      || die "取回 ${script} 安装器失败（${url}）"
  else
    wget -q -O "$tmp" "$url" || die "取回 ${script} 安装器失败（${url}）"
  fi
  if [[ ! -s "$tmp" ]]; then die "${script} 安装器内容为空（${url}）"; fi
  chmod +x "$tmp" 2>/dev/null || true
  if [ "$#" -gt 0 ]; then
    log "转交给 ${script} 安装器（参数：$*）"
  else
    log "转交给 ${script} 安装器（CHANNEL=${CHANNEL} VERSION=${VERSION:-latest}）"
  fi
  set +e
  bash "$tmp" "$@"
  local rc=$?
  set -e
  # 临时目录交给 trap 之外的显式清理失败也无妨：mktemp -d 在客户机上属临时文件。
  return "$rc"
}

# ── 版本检查与获取方式（不安装）────────────────────────────────────────
# 版本判定整段委托给 upgrade.sh list：它已经带共享 awk JSON 块、已经过
# version-check / catalog e2e。这里再抄一份解析器就是分叉的开始。
cmd_check() {
  local os arch
  os="$(detect_os)"; arch="$(detect_arch)"
  log "查询 ${CHANNEL} 通道可用版本（maintain: ${MAINTAIN_BASE}）"
  # 必须显式给 `list`。upgrade.sh 缺省子命令是 run（真升级），不是只读。
  run_installer upgrade list || true
  printf '\n'
  log "获取与安装方式"
  printf '  交互式安装（推荐，会先问你要 lite 还是 full）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/install" | bash\n' "$MAINTAIN_BASE"
  printf '  本地小规模（lite，主机二进制 + SQLite）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/host" | bash\n' "$MAINTAIN_BASE"
  printf '  全量（full，Docker + PostgreSQL + Redis）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/docker" | bash\n' "$MAINTAIN_BASE"
  printf '  已有实例的本机升级：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/upgrade" | bash -s -- run\n' "$MAINTAIN_BASE"
  printf '  当前平台：%s/%s\n' "$os" "$arch"
}

cmd_modes() {
  print_modes
  doctor_report
  log "本机建议："
  if [[ "$OS_KIND" == "windows" ]]; then
    printf '  Windows —— 主机二进制模式不适用；装 Docker Desktop 后用 full，或走 client-deploy.ps1。\n'
  elif [[ "$HAVE_DOCKER" == "1" ]]; then
    printf '  有 Docker —— 两种模式都能装；生产/多副本用 full，单机评估用 lite。\n'
  else
    printf '  没有 Docker —— 只能 lite（full 需要 docker compose）。\n'
  fi
}

# ── 主流程 ────────────────────────────────────────────────────────────
main() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      install|doctor|check|modes|help) ACTION="$1"; shift ;;
      --mode) MODE="${2:-}"; shift 2 || true ;;
      --mode=*) MODE="${1#*=}"; shift ;;
      --yes|-y) YES=1; shift ;;
      --check) ACTION="check"; shift ;;
      --doctor) ACTION="doctor"; shift ;;
      --dry-run) DRY_RUN=1; shift ;;
      -h|--help) ACTION="help"; shift ;;
      *) die "未知参数：$1（用 --help 看用法）" ;;
    esac
  done
  export MAINTAIN_BASE CHANNEL DRY_RUN="${DRY_RUN:-0}"

  case "$ACTION" in
    help)   usage; return 0 ;;
    modes)  doctor_report; cmd_modes; return 0 ;;
    doctor) doctor_report; return 0 ;;
    check)  cmd_check; return 0 ;;
  esac

  log "开始安装 llm-gateway-go（channel=${CHANNEL}）"
  doctor_report
  confirm_mode
  require_mode_prereqs "$MODE"
  log "模式 ${MODE} 前置条件检查通过"
  case "$MODE" in
    lite) run_installer host ;;
    full) run_installer docker ;;
  esac
}

# KXMAINT_LIB_ONLY=1 时只定义函数、不执行 main。与
# user-scripts-portability-test.sh 抽取函数直测的既有做法一致：交互分支
# 需要在非 tty 环境里驱动（自动喂答案），不给出这个口子就只能靠人手点。
if [[ "${KXMAINT_LIB_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
