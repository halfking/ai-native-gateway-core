#!/usr/bin/env bash
# =====================================================================
# scripts/install-logrotate.sh — llm-gateway-go stderr/stdout 日志轮转配置管理
#
# 用途:
#   在目标服务器上安装/卸载/验证 /etc/logrotate.d/llm-gateway-go 配置。
#   配套 deploy/logrotate-llm-gateway-go (仓内 SSOT 真源) 一起使用。
#
# 触发原因:
#   llm-gateway-go.service 使用 StandardOutput/StandardError=append:
#   /var/log/llm-gateway-go/gateway.{stdout,stderr}.log (systemd 持有写 fd
#   不释放, 详见 deploy/llm-gateway-go.service:19-20)。
#   应用内 lumberjack 轮转 (LLM_GATEWAY_LOG_FILE) 默认关闭 (.env.example:82)
#   因此 stderr 没有应用层轮转, 必须靠外部 logrotate + copytruncate 模式管。
#
# 子命令:
#   install [config-file]   安装配置到 /etc/logrotate.d/llm-gateway-go (幂等)
#   uninstall               卸载配置
#   status                  查看当前安装状态
#   verify                  仅跑 logrotate -d 模拟验证 (不改任何东西)
#   -h | --help             显示本帮助
#
# 配置源优先级:
#   1. 第一个位置参数 install <config-file>
#   2. 环境变量 LOGROTATE_CONFIG_FILE
#   3. 默认: 脚本同目录的上级 deploy/logrotate-llm-gateway-go
#
# 幂等:
#   - install: 目标已存在 + 内容相同 → skip;内容不同 → 备份 .bak.<ts> 后覆盖
#   - uninstall: 目标不存在 → skip
#
# 用法示例:
#   # 直接在目标服务器跑 (有 root 权限)
#   bash scripts/install-logrotate.sh install
#
#   # 从开发机通过 ssh 远程执行
#   ssh server 'bash -s' < scripts/install-logrotate.sh install
#
#   # 用 scp 先传配置再远程装
#   scp deploy/logrotate-llm-gateway-go server:/tmp/
#   ssh server 'bash scripts/install-logrotate.sh install /tmp/llm-gateway-go.logrotate'
#
#   # pipe 方式 (stdin)
#   cat deploy/logrotate-llm-gateway-go | ssh server \
#     "LOGROTATE_CONFIG_FILE=/dev/stdin bash scripts/install-logrotate.sh install"
#
# 依赖: logrotate 命令 (apt: logrotate / yum: logrotate)
#
# 引用: rule 03 §6b (日志归档) + rule 11 §14 (持续验证)
# =====================================================================

set -euo pipefail

# SCRIPT_DIR 用 realpath 解析, 防止脚本被 cp/symlink 后算错位置
SCRIPT_PATH="${BASH_SOURCE[0]}"
if command -v realpath >/dev/null 2>&1; then
  SCRIPT_PATH="$(realpath "$SCRIPT_PATH" 2>/dev/null || echo "$SCRIPT_PATH")"
fi
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_CONFIG="$PROJECT_ROOT/deploy/logrotate-llm-gateway-go"
TARGET="/etc/logrotate.d/llm-gateway-go"
SERVICE_NAME="${SERVICE_NAME:-llm-gateway-go}"
LOG_DIR_DEFAULT="/var/log/${SERVICE_NAME}"

# ── 颜色 ─────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
else
  G=''; Y=''; R=''; B=''; N=''
fi
ok()    { echo -e "${G}  ✓${N} $*"; }
info()  { echo -e "${B}  ▶${N} $*"; }
warn()  { echo -e "${Y}  !${N} $*"; }
err()   { echo -e "${R}  ✗${N} $*" >&2; }
hdr()   { echo -e "\n${B}── $* ──${N}"; }

# ── 帮助 ─────────────────────────────────────────────────────
usage() {
  sed -n '3,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# ── 解析子命令 ───────────────────────────────────────────────
ACTION="${1:-}"
shift 2>/dev/null || true

case "$ACTION" in
  install|uninstall|status|verify) ;;
  -h|--help|"") usage 0 ;;
  *) err "未知子命令: $ACTION"; usage 1 ;;
esac

# ── 解析配置源 ───────────────────────────────────────────────
resolve_config() {
  local cfg="${1:-${LOGROTATE_CONFIG_FILE:-}}"

  # 1) 显式 stdin
  if [[ "$cfg" == "-" || "$cfg" == "/dev/stdin" ]]; then
    printf '%s\n' "-"
    return 0
  fi

  # 2) 显式 file path
  if [[ -n "$cfg" ]]; then
    if [[ -f "$cfg" ]]; then
      printf '%s\n' "$cfg"
      return 0
    fi
    err "指定的配置文件不存在: $cfg"
    return 1
  fi

  # 3) 无参 → DEFAULT_CONFIG (仓内 SSOT)
  if [[ -f "$DEFAULT_CONFIG" ]]; then
    printf '%s\n' "$DEFAULT_CONFIG"
    return 0
  fi

  # 4) 无参 + DEFAULT_CONFIG 不存在 + stdin 被重定向 → 自动走 stdin
  if [[ ! -t 0 ]]; then
    printf '%s\n' "-"
    return 0
  fi

  # 5) 全无
  err "未找到 logrotate 配置文件, 请通过位置参数或 LOGROTATE_CONFIG_FILE 指定"
  err "默认路径: $DEFAULT_CONFIG"
  return 1
}

# ── 公共预检 ─────────────────────────────────────────────────
preflight() {
  if [[ $EUID -ne 0 ]]; then
    err "需要 root 权限 (EUID=0, 当前=$EUID)"
    echo "  提示: 用 sudo 重新执行, 或通过 ssh root@server 运行"
    return 1
  fi
  if ! command -v logrotate >/dev/null 2>&1; then
    err "未找到 logrotate 命令"
    echo "  安装: apt-get install -y logrotate  /  yum install -y logrotate"
    return 1
  fi
  return 0
}

# ── read_config_to_file: 把源 (file 或 stdin) 复制到临时文件, 返回路径 ──
# 注意: command substitution `$(cat ...)` 会消费 stdin, 所以 stdin 模式
# 必须先 cat 到 tmp file, 再 cat tmp file 进变量。
read_config_to_file() {
  local cfg="$1"
  if [[ "$cfg" == "-" || "$cfg" == "/dev/stdin" ]]; then
    local tmp
    tmp="$(mktemp)"
    cat > "$tmp"
    printf '%s\n' "$tmp"
  else
    printf '%s\n' "$cfg"
  fi
}

# ── 备份 + 写目标 (用于 install) ─────────────────────────────
write_target() {
  local content="$1"
  if [[ -f "$TARGET" ]]; then
    if diff -q <(printf '%s\n' "$content") "$TARGET" >/dev/null 2>&1; then
      info "目标已存在且内容一致, 跳过 ($TARGET)"
      return 0
    fi
    local bak
    bak="$TARGET.bak.$(date +%Y%m%d-%H%M%S)"
    cp -p "$TARGET" "$bak"
    warn "目标已存在但内容不同, 已备份为 $bak"
  fi
  install -m 0644 -o root -g root <(printf '%s\n' "$content") "$TARGET"
  ok "已安装 $TARGET"
}

# ── verify_config: logrotate -d 模拟跑 ───────────────────────
verify_config() {
  local target="$1"
  hdr "logrotate -d $target (模拟跑)"
  if logrotate -d "$target"; then
    ok "logrotate 配置语法 OK, 模拟轮转无错误"
    return 0
  fi
  err "logrotate -d 返回非零, 配置有语法或逻辑错误"
  return 1
}

# ── install 子命令 ───────────────────────────────────────────
do_install() {
  local cfg; cfg="$(resolve_config "${1:-}")" || return 1
  preflight || return 1

  hdr "install logrotate 配置"
  info "源: $cfg"
  info "目标: $TARGET"

  # 把 stdin (若) 或 file 复制到 tmp file, 然后从 tmp 读内容到变量
  # 必须先落盘, 否则 `$(cat)` command substitution 会消费 stdin
  local source
  source="$(read_config_to_file "$cfg")" || { err "读取配置失败"; return 1; }
  local content
  content="$(cat "$source")" || { err "读取内容失败: $source"; rm -f "$source"; return 1; }
  if [[ "$source" != "$cfg" ]]; then
    rm -f "$source"
  fi

  # 必填字段预检 (避免空内容 / 错配覆盖)
  if [[ -z "$content" ]]; then
    err "配置内容为空, 拒绝安装"
    return 1
  fi
  if ! grep -qE '^/var/log/' <<<"$content"; then
    err "配置缺少 /var/log/... 路径规则, 拒绝安装 (避免误覆盖其它服务)"
    return 1
  fi
  if ! grep -qE '^\s*rotate\s+[0-9]+' <<<"$content"; then
    err "配置缺少 rotate 指令, 拒绝安装"
    return 1
  fi

  write_target "$content" || return 1
  verify_config "$TARGET" || return 1

  ok "install 完成"
  return 0
}

# ── uninstall 子命令 ────────────────────────────────────────
do_uninstall() {
  preflight || return 1
  hdr "uninstall logrotate 配置"
  if [[ ! -e "$TARGET" ]]; then
    info "$TARGET 不存在, 无需卸载"
    return 0
  fi
  if [[ -f "$TARGET.bak" ]] || compgen -G "$TARGET.bak.*" >/dev/null; then
    info "历史备份保留: $(ls -1 $TARGET.bak* 2>/dev/null | tr '\n' ' ')"
  fi
  rm -f "$TARGET"
  ok "已删除 $TARGET"
  return 0
}

# ── status 子命令 ───────────────────────────────────────────
do_status() {
  hdr "logrotate 配置状态"
  if [[ -f "$TARGET" ]]; then
    ok "$TARGET 已安装"
    info "  sha256: $(sha256sum "$TARGET" | awk '{print $1}')"
    info "  size:   $(wc -c < "$TARGET") bytes"
    info "  mtime:  $(stat -c '%y' "$TARGET" 2>/dev/null || stat -f '%Sm' "$TARGET" 2>/dev/null)"
    echo ""
    info "rotate 目标文件:"
    awk '/^\/var\/log\// {print "    " $0}' "$TARGET" || true
  else
    warn "$TARGET 未安装"
  fi

  echo ""
  info "服务日志目录状态:"
  if [[ -d "$LOG_DIR_DEFAULT" ]]; then
    ls -lh "$LOG_DIR_DEFAULT" 2>/dev/null | sed 's/^/    /' || true
  else
    warn "$LOG_DIR_DEFAULT 目录不存在 (服务未运行? 或日志路径不同?)"
  fi

  echo ""
  info "systemd 服务 $SERVICE_NAME 配置 (确认 append: 模式):"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl show "$SERVICE_NAME" 2>/dev/null \
      | grep -E '^(StandardOutput|StandardError)=' \
      | sed 's/^/    /' || warn "  无法读取 $SERVICE_NAME 状态"
  else
    warn "  systemctl 不可用, 跳过"
  fi

  echo ""
  verify_config "$TARGET" || return 1
  return 0
}

# ── verify 子命令 ───────────────────────────────────────────
do_verify() {
  preflight || return 1
  hdr "verify logrotate 配置"
  if [[ ! -f "$TARGET" ]]; then
    err "$TARGET 不存在, 无法 verify (请先 install)"
    return 1
  fi
  verify_config "$TARGET" || return 1
  return 0
}

# ── 派发 ─────────────────────────────────────────────────────
case "$ACTION" in
  install)   do_install "${1:-}" ;;
  uninstall) do_uninstall ;;
  status)    do_status ;;
  verify)    do_verify ;;
esac
