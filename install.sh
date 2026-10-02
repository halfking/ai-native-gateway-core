#!/usr/bin/env bash
# install.sh — llm-gateway-go 安装引导（从源码仓库直接可用，无需先下载 release 包）
#
# 业界主流的安装方式按"上手难度"大致是这条从低到高的曲线：
#   1. 包管理器一行（brew / winget / scoop）—— 需要把 manifest 提交到上游
#      社区仓库，我们没有那个发布通道，所以不做主推；
#   2. `npm install -g` —— Windows 上 Node 装机率最高，摩擦最小；
#   3. `go install pkg@latest` —— Go 生态的标准装法；
#   4. `curl … | bash` / `irm … | iex` —— 基础设施类项目的事实标准；
#   5. 从源码编译（go build）—— 拿到仓库就能装，不依赖任何注册中心。
#
# 本脚本把 2/3/4/5 统一到一个入口，并且**先问清楚要装哪种规模再动手**：
#   lite  本地小规模：SQLite + 单机，不起 PostgreSQL / Redis
#   full  全量：PostgreSQL + Redis，面向生产 / 多副本
#
# 用法：
#   bash install.sh                      # 交互：选安装方式 + 选规模，然后安装
#   bash install.sh --channel source     # 直接从当前源码树编译安装
#   bash install.sh --channel npm        # npm install -g
#   bash install.sh --channel goinstall  # go install ...@latest
#   bash install.sh --channel maintain   # 官方一键脚本（curl | bash）
#   bash install.sh --mode lite          # 指定规模，跳过规模提问
#   bash install.sh doctor               # 只看本机具备哪些安装条件
#   bash install.sh build                # 只编译，不安装
#   bash install.sh version              # 只查版本与可获取的更新
#   bash install.sh upgrade|uninstall|activate|heartbeat|completion
#                                      # 透传给 llm-gw-installer
#
# 注意 doctor / version 归本脚本自己（回答"这台机器能装什么"、"我是什么版本、
# 怎么拿更新"），不透传——还没装 installer 时问这个更有用。子命令放行前先
# 对照 KNOWN_SUBCOMMANDS 校验，未知参数直接报错，不塞给二进制。
#
# 选项：
#   --channel source|goinstall|npm|binary|maintain
#   --mode lite|full
#   --dir <path>        安装目录（默认 $LLM_GATEWAY_HOME 或 ~/llm-gateway）
#   --yes               全部用默认值，不再提问
#   --dry-run           只打印将要执行的命令
#   --keep              强制本机平台（默认自动探测）
#
# 环境变量：
#   LLM_GATEWAY_HOME    安装目录
#   MAINTAIN_BASE       官方分发入口（默认 https://llmgo.kxpms.cn/maintain-api）
#   NO_INTERACTIVE=1    不提问
#   KXMAINT_STUB_DOCKER 仅测试用：假装本机有 Docker
#
# 可移植性：不用 bash 4.0+ 语法（mapfile / ${var,,}），macOS 自带 bash 3.2
# 必须能跑；不依赖 python3 / jq。
set -euo pipefail

# `curl … | bash` 管道路径下 BASH_SOURCE 为空，${BASH_SOURCE[0]} 会展开成
# shell 名而不是文件路径，所以自身目录只在真的以文件方式运行时才取。
SELF="${BASH_SOURCE[0]:-}"
SCRIPT_DIR="$(cd "$(dirname "$SELF")" 2>/dev/null && pwd)" || SCRIPT_DIR="$PWD"
REPO_DIR="$SCRIPT_DIR"

INSTALL_DIR="${LLM_GATEWAY_HOME:-$HOME/llm-gateway}"
MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL=""
MODE=""
ACTION="install"
DRY_RUN=0
ASSUME_YES=0
PASSTHRU=""
# 数组必须显式声明：set -u 下对空数组做 "${arr[@]}" 会报未绑定。
PASSTHRU_ARGS=()

log()  { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARN: %s\n' "$*" >&2; }
die()  { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

# llm-gw-installer 自己的子命令：透传，不参与本脚本的选路逻辑。
KNOWN_SUBCOMMANDS="activate completion doctor heartbeat help uninstall upgrade version"

usage() {
  cat <<'USAGEEOF'
install.sh — llm-gateway-go 安装引导（两种规模：lite 本地小规模 / full 全量）

用法：
  bash install.sh [install|build|doctor|version|<installer 子命令>] [选项]

安装方式（--channel）：
  source     从当前源码树 go build（拿到仓库就能装，最通用）
  goinstall  go install github.com/kaixuan/llm-gateway-go/installer/cmd/llm-gw-installer@latest
  npm        npm install -g @kaixuan/llm-gw-installer
  binary     使用 release 包里自带的 llm-gw-installer-<os>-<arch> 二进制
  maintain   官方一键脚本：curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash

规模（--mode）：
  lite  SQLite + 单机，不起 PostgreSQL / Redis
  full  PostgreSQL + Redis，面向生产 / 多副本

示例：
  bash install.sh --channel source --mode lite
  bash install.sh doctor
  bash install.sh --channel npm
USAGEEOF
}

interactive() {
  [[ "$ASSUME_YES" == "1" ]] && return 1
  [[ "${NO_INTERACTIVE:-0}" == "1" ]] && return 1
  [[ "$DRY_RUN" == "1" ]] && return 1
  [ -t 0 ] || return 1
  return 0
}

run() {
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '[install] (dry-run) %s\n' "$*"
    return 0
  fi
  "$@"
}

# ── 平台探测 ──────────────────────────────────────────────────────────
detect_os() {
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
    x86_64|amd64)        printf 'amd64\n' ;;
    aarch64|arm64)       printf 'arm64\n' ;;
    loongarch64|loong64) printf 'loong64\n' ;;
    *)                   printf '%s\n' "$a" ;;
  esac
}

has() { command -v "$1" >/dev/null 2>&1; }

has_docker() {
  [[ "${KXMAINT_STUB_DOCKER:-0}" == "1" ]] && return 0
  has docker || return 1
  docker compose version >/dev/null 2>&1 && return 0
  has docker-compose
}

BIN_SUFFIX=""
case "$(detect_os)" in
  windows) BIN_SUFFIX=".exe" ;;
esac

# ── 体检 ──────────────────────────────────────────────────────────────
HAVE_GO=0;   has go   && HAVE_GO=1
HAVE_NPM=0;  has npm  && HAVE_NPM=1
HAVE_NODE=0; has node && HAVE_NODE=1
HAVE_DOCKER=0; has_docker && HAVE_DOCKER=1
HAVE_CURL=0; has curl && HAVE_CURL=1

doctor_report() {
  local os arch localbin
  os="$(detect_os)"; arch="$(detect_arch)"
  # `local x="$(cmd)"` 在 cmd 失败时会连带整条语句返回非 0，set -e 下直接
  # 退出脚本。local_binary 找不到二进制是正常情况，必须显式兜住。
  localbin="$(local_binary || true)"
  log "体检报告"
  printf '  %-10s %s/%s\n' "平台" "$os" "$arch"
  printf '  %-10s %s\n' "go"      "$([[ "$HAVE_GO" == 1 ]] && go version || printf '缺失（source / goinstall 需要）')"
  printf '  %-10s %s\n' "node"    "$([[ "$HAVE_NODE" == 1 ]] && node --version || printf '缺失（npm 方式需要）')"
  printf '  %-10s %s\n' "npm"     "$([[ "$HAVE_NPM" == 1 ]] && npm --version || printf '缺失（npm 方式需要）')"
  printf '  %-10s %s\n' "docker"  "$([[ "$HAVE_DOCKER" == 1 ]] && docker --version || printf '缺失（full 模式需要）')"
  printf '  %-10s %s\n' "curl"    "$([[ "$HAVE_CURL" == 1 ]] && command -v curl || printf '缺失（maintain 方式需要）')"
  printf '  %-10s %s\n' "源码树"  "$([[ -d "$REPO_DIR/installer" ]] && printf '%s' "$REPO_DIR" || printf '不在源码树内（source 不可用）')"
  printf '  %-10s %s\n' "本地二进制" "${localbin:-无}"
  printf '\n'
}

# release 包里自带的二进制优先，其次是已安装到 INSTALL_DIR 的。
local_binary() {
  local os arch cand
  os="$(detect_os)"; arch="$(detect_arch)"
  for cand in \
    "$SCRIPT_DIR/llm-gw-installer-${os}-${arch}${BIN_SUFFIX}" \
    "$SCRIPT_DIR/llm-gw-installer${BIN_SUFFIX}" \
    "$INSTALL_DIR/bin/llm-gw-installer${BIN_SUFFIX}"; do
    if [[ -f "$cand" ]]; then printf '%s\n' "$cand"; return 0; fi
  done
  return 1
}

built_binary() { printf '%s/bin/llm-gw-installer%s\n' "$INSTALL_DIR" "$BIN_SUFFIX"; }

# ── 选安装方式 ────────────────────────────────────────────────────────
print_channels() {
  cat <<'CHEOF'
可选安装方式：

  [1] source     从当前源码树编译（go build）—— 拿到仓库就能装，不依赖注册中心
  [2] goinstall  go install ...@latest —— Go 生态标准装法
  [3] npm        npm install -g —— Windows 上摩擦最小
  [4] binary     使用 release 包里自带的 llm-gw-installer 二进制
  [5] maintain   官方一键脚本（curl … | bash）—— 总是拿到已发布的最新制品

CHEOF
}

# 不可用的方式要说明原因，而不是列出来让人选中后才失败。
channel_available() {
  case "$1" in
    source)    [[ -d "$REPO_DIR/installer" ]] || { warn "source 需要在源码树内运行（缺 installer/）"; return 1; }
               [[ "$HAVE_GO" == "1" ]] || { warn "source 需要 Go 工具链"; return 1; } ;;
    goinstall) [[ "$HAVE_GO" == "1" ]] || { warn "goinstall 需要 Go 工具链"; return 1; } ;;
    npm)       [[ "$HAVE_NPM" == "1" ]] || { warn "npm 方式需要 Node.js / npm"; return 1; } ;;
    binary)    local_binary >/dev/null || { warn "binary 方式需要 release 包里的 llm-gw-installer 二进制"; return 1; } ;;
    maintain)  [[ "$HAVE_CURL" == "1" ]] || { warn "maintain 方式需要 curl"; return 1; } ;;
    *) warn "未知安装方式 '$1'"; return 1 ;;
  esac
  return 0
}

choose_channel() {
  if [[ -n "$CHANNEL" ]]; then
    log "安装方式由参数指定：${CHANNEL}"
    return 0
  fi
  # 非交互时按"手上有什么就用什么"排序，与业界一键脚本的兜底思路一致。
  if ! interactive; then
    if [[ -n "$(local_binary || true)" ]]; then CHANNEL="binary"
    elif [[ "$HAVE_GO" == "1" && -d "$REPO_DIR/installer" ]]; then CHANNEL="source"
    elif [[ "$HAVE_GO" == "1" ]]; then CHANNEL="goinstall"
    elif [[ "$HAVE_NPM" == "1" ]]; then CHANNEL="npm"
    else CHANNEL="maintain"
    fi
    log "非交互，自动选择安装方式：${CHANNEL}"
    return 0
  fi
  print_channels
  local reply=""
  printf '请选择安装方式 [1]: '
  read -r reply || true
  case "${reply:-1}" in
    1|source)     CHANNEL="source" ;;
    2|goinstall)  CHANNEL="goinstall" ;;
    3|npm)        CHANNEL="npm" ;;
    4|binary)     CHANNEL="binary" ;;
    5|maintain)   CHANNEL="maintain" ;;
    *) warn "无法识别 '${reply}'，按默认 source 处理"; CHANNEL="source" ;;
  esac
  log "已选择安装方式：${CHANNEL}"
}

# ── 选规模 ────────────────────────────────────────────────────────────
print_modes() {
  cat <<'MODEOF'
可选规模：

  [1] lite —— 本地小规模
      SQLite + 单机进程，不起 PostgreSQL / Redis
      适合：单机、内网、评估、CI、开发调试

  [2] full —— 全量
      PostgreSQL + Redis，面向生产 / 多副本 / 高并发
      适合：正式环境

MODEOF
}

choose_mode() {
  if [[ -n "$MODE" ]]; then
    log "规模由参数指定：${MODE}"
    return 0
  fi
  if ! interactive; then
    MODE="full"
    log "非交互，规模默认 full（与 llm-gw-installer 的非 TTY 默认一致）"
    return 0
  fi
  print_modes
  local reply=""
  printf '请选择规模 [1]: '
  read -r reply || true
  case "${reply:-1}" in
    1|lite) MODE="lite" ;;
    2|full) MODE="full" ;;
    *) warn "无法识别 '${reply}'，按默认 lite 处理"; MODE="lite" ;;
  esac
  log "已选择规模：${MODE}"
}

# ── 各安装方式 ────────────────────────────────────────────────────────
GO_MODULE="github.com/kaixuan/llm-gateway-go/installer"
GO_PKG="${GO_MODULE}/cmd/llm-gw-installer"

# 解析结果放在全局变量里而不是命令替换：log() 走 stdout，若用
# `x="$(build_from_source)"` 会把日志行一起吞进路径里。
RESOLVED_BINARY=""

# 从当前源码树编译。installer 是独立 Go module，必须在它自己的目录里 build。
build_from_source() {
  local moddir="$REPO_DIR/installer" out
  [[ -d "$moddir" ]] || die "不在源码树内：找不到 $moddir"
  out="$(built_binary)"
  mkdir -p "$INSTALL_DIR/bin"
  log "从源码编译：$moddir → $out"
  run env CGO_ENABLED=0 go build -C "$moddir" -trimpath -o "$out" ./cmd/llm-gw-installer \
    || die "源码编译失败"
  if [[ "$DRY_RUN" != "1" && ! -f "$out" ]]; then
    die "编译未产出二进制：$out"
  fi
  RESOLVED_BINARY="$out"
}

install_via_goinstall() {
  log "go install ${GO_PKG}@latest"
  run go install "${GO_PKG}@latest" || die "go install 失败"
  local gobin
  gobin="$(go env GOBIN 2>/dev/null || true)"
  if [[ -z "$gobin" ]]; then gobin="$(go env GOPATH)/bin"; fi
  RESOLVED_BINARY="${gobin}/llm-gw-installer${BIN_SUFFIX}"
}

install_via_npm() {
  log "npm install -g @kaixuan/llm-gw-installer"
  run npm install -g @kaixuan/llm-gw-installer || die "npm 全局安装失败"
  local prefix
  prefix="$(npm prefix -g 2>/dev/null || true)"
  if [[ -n "$prefix" ]]; then
    RESOLVED_BINARY="${prefix}/llm-gw-installer${BIN_SUFFIX}"
  fi
}

# maintain 通道自己就把装完了，不需要再解析出本地二进制。
install_via_maintain() {
  log "转交官方一键脚本（它会再问一次规模）"
  run env MAINTAIN_BASE="$MAINTAIN_BASE" \
    bash -c 'curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash' \
    || die "官方一键脚本执行失败"
  RESOLVED_BINARY=""
}

# 解析出真正要执行的 llm-gw-installer 路径（maintain 通道除外）。
resolve_installer() {
  case "$CHANNEL" in
    source)    build_from_source ;;
    goinstall) install_via_goinstall ;;
    npm)       install_via_npm ;;
    binary)    RESOLVED_BINARY="$(local_binary || true)" ;;
    maintain)  install_via_maintain ;;
    *)         die "未知安装方式 '$CHANNEL'" ;;
  esac
}

# ── 版本检查 ──────────────────────────────────────────────────────────
cmd_version() {
  local os arch bin
  os="$(detect_os)"; arch="$(detect_arch)"
  bin="$(local_binary || true)"
  if [[ -n "$bin" ]]; then
    log "本机已安装的 installer 版本："
    "$bin" version 2>/dev/null || warn "已安装的 installer 无法报告版本"
  else
    warn "本机没有已安装的 llm-gw-installer"
  fi
  printf '\n'
  log "可获取的最新版本与安装方式："
  printf '  交互安装（推荐，会先问规模）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/install" | bash\n' "$MAINTAIN_BASE"
  printf '  本地小规模（lite，主机二进制 + SQLite）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/host" | bash\n' "$MAINTAIN_BASE"
  printf '  全量（full，Docker + PostgreSQL + Redis）：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/docker" | bash\n' "$MAINTAIN_BASE"
  printf '  已有实例的本机升级：\n'
  printf '    curl -fsSL "%s/distribution/install-scripts/upgrade" | bash -s -- run\n' "$MAINTAIN_BASE"
  printf '  当前平台：%s/%s\n' "$os" "$arch"
}

cmd_doctor() {
  doctor_report
  log "可用安装方式："
  local c
  for c in source goinstall npm binary maintain; do
    if channel_available "$c" 2>/dev/null; then
      printf '  %-10s 可用\n' "$c"
    else
      printf '  %-10s 不可用\n' "$c"
    fi
  done
  printf '\n'
  log "full 规模需要 docker compose：$([[ "$HAVE_DOCKER" == 1 ]] && printf '满足' || printf '不满足')"
}

# ── 主流程 ────────────────────────────────────────────────────────────
cmd_install() {
  doctor_report
  choose_channel
  channel_available "$CHANNEL" || exit 2
  choose_mode
  log "开始安装（方式=${CHANNEL} 规模=${MODE} 目录=${INSTALL_DIR}）"
  resolve_installer
  # maintain 通道自己装完了，没有本地二进制可跑。
  if [[ "$CHANNEL" == "maintain" ]]; then
    log "官方一键脚本已执行完毕"
    return 0
  fi
  [[ -n "$RESOLVED_BINARY" ]] || die "没有解析出可执行的 llm-gw-installer"
  [[ -f "$RESOLVED_BINARY" ]] || die "installer 二进制不存在：$RESOLVED_BINARY"
  mkdir -p "$INSTALL_DIR"
  log "启动安装向导：$RESOLVED_BINARY install --dir $INSTALL_DIR --mode $MODE"
  "$RESOLVED_BINARY" install --dir "$INSTALL_DIR" --mode "$MODE"
}

main() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      install|build|doctor|version|help) ACTION="$1"; shift ;;
      --channel)   CHANNEL="${2:-}"; shift 2 ;;
      --channel=*) CHANNEL="${1#*=}"; shift ;;
      --mode)      MODE="${2:-}"; shift 2 ;;
      --mode=*)    MODE="${1#*=}"; shift ;;
      --dir)       INSTALL_DIR="${2:-}"; shift 2 ;;
      --yes|-y)    ASSUME_YES=1; shift ;;
      --dry-run)   DRY_RUN=1; shift ;;
      -h|--help)   ACTION="help"; shift ;;
      *)
        # 其余的第一个参数只能是 llm-gw-installer 自己的子命令。放行前先
        # 对照 KNOWN_SUBCOMMANDS：旧写法是无条件透传，于是 `install.sh
        # --mode`（少写一个值）会把 "--mode" 当子命令塞给二进制，报错信息
        # 完全指不到本脚本身上。子命令之后的参数原样保留（数组，不合并）。
        PASSTHRU="$1"
        case " $KNOWN_SUBCOMMANDS " in
          *" $PASSTHRU "*) ;;
          *) die "未知子命令 '${PASSTHRU}'。本脚本的用法：${KNOWN_SUBCOMMANDS} 或 install|build|doctor|version|help（--help 看细节）" ;;
        esac
        shift
        PASSTHRU_ARGS=("$@")
        ACTION="passthrough"
        break
        ;;
    esac
  done

  case "$ACTION" in
    help)    usage; return 0 ;;
    doctor)  cmd_doctor; return 0 ;;
    version) cmd_version; return 0 ;;
    build)
      CHANNEL="${CHANNEL:-source}"
      build_from_source
      return 0
      ;;
    passthrough)
      # 透传 llm-gw-installer 自己的子命令，保持旧用法可用。
      local bin
      bin="$(local_binary || true)"
      [[ -n "$bin" ]] || die "找不到 llm-gw-installer（先跑 bash install.sh --channel source）"
      exec "$bin" "$PASSTHRU" ${PASSTHRU_ARGS[@]+"${PASSTHRU_ARGS[@]}"}
      ;;
  esac

  cmd_install
}

main "$@"
