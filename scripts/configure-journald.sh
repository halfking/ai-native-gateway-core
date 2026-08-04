#!/usr/bin/env bash
# =====================================================================
# scripts/configure-journald.sh — 限制 systemd journald 日志大小 (drop-in 模式)
#
# 用途:
#   在 systemd-journald 系统 (llm-gateway-go.stderr 走 journal 的服务器)
#   上部署 /etc/systemd/journald.conf.d/llm-gateway-go.conf drop-in, 限制
#   journal 总大小 + 保留期, 防止 journal 把磁盘撑爆
#   (154 历史撑到 4.0G 时已接近 systemd 默认 ~4G 上限)。
#
# 适用场景:
#   llm-gateway-go.service 的 StandardOutput=journal (systemd 默认),
#   stderr 不写文件, logrotate 管不到, 必须靠 systemd-journald 自带轮转。
#
# 与 install-logrotate.sh 的关系:
#   - install-logrotate.sh: 走 StandardOutput=append:... 模式的服务器 (245)
#   - configure-journald.sh: 走 StandardOutput=journal 模式的服务器 (154)
#   - deploy-seamless.sh / deploy-154.sh 用 systemctl show 检测后分支调用
#
# 设计: drop-in 模式 (不修改主配置 /etc/systemd/journald.conf)
#   - systemd 自动合并 /etc/systemd/journald.conf.d/*.conf
#   - 优先级: drop-in > 主配置
#   - uninstall = rm drop-in 文件 (干净回滚)
#   - 优点: 主文件不动, 跨 distro 通用, 零解析/merge 逻辑
#
# 子命令:
#   install [snippet-file]   cp snippet -> /etc/systemd/journald.conf.d/llm-gateway-go.conf + restart
#   uninstall               rm drop-in + restart
#   status                  查看当前生效配置 + journal 实际大小
#   verify                  校验 drop-in 文件被 systemd-journald 识别 (offline check)
#   -h | --help             显示帮助
#
# 配置源优先级:
#   1) install <snippet-file> 位置参数
#   2) 环境变量 JOURNALD_CONFIG_FILE
#   3) 仓内默认 deploy/journald-conf-snippet.conf
#   4) stdin (cat snippet | bash script install -)
#
# 幂等:
#   - install: drop-in 内容相同 → skip; 不同 → 备份 .bak.<ts> 后覆盖
#   - uninstall: drop-in 不存在 → skip
#
# 用法:
#   bash scripts/configure-journald.sh install
#   bash scripts/configure-journald.sh install /tmp/snippet.conf
#   bash scripts/configure-journald.sh uninstall
#   bash scripts/configure-journald.sh status
#
# 依赖: systemd-journald (systemd 系统) + journalctl 命令
#
# 引用: rule 03 §6b (日志归档) + rule 11 §14 (持续验证)
# =====================================================================

set -euo pipefail

SCRIPT_PATH="${BASH_SOURCE[0]}"
if command -v realpath >/dev/null 2>&1; then
  SCRIPT_PATH="$(realpath "$SCRIPT_PATH" 2>/dev/null || echo "$SCRIPT_PATH")"
fi
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_SNIPPET="$PROJECT_ROOT/deploy/journald-conf-snippet.conf"
TARGET_DIR="/etc/systemd/journald.conf.d"
TARGET="$TARGET_DIR/llm-gateway-go.conf"
SERVICE="systemd-journald.service"

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
  sed -n '3,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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

# ── 解析 snippet 源 ──────────────────────────────────────────
resolve_snippet() {
  local cfg="${1:-${JOURNALD_CONFIG_FILE:-}}"

  if [[ "$cfg" == "-" || "$cfg" == "/dev/stdin" ]]; then
    printf '%s\n' "-"
    return 0
  fi

  if [[ -n "$cfg" ]]; then
    if [[ -f "$cfg" ]]; then
      printf '%s\n' "$cfg"
      return 0
    fi
    err "指定的 snippet 文件不存在: $cfg"
    return 1
  fi

  if [[ -f "$DEFAULT_SNIPPET" ]]; then
    printf '%s\n' "$DEFAULT_SNIPPET"
    return 0
  fi

  if [[ ! -t 0 ]]; then
    printf '%s\n' "-"
    return 0
  fi

  err "未找到 journald snippet, 请通过位置参数或 JOURNALD_CONFIG_FILE 指定"
  err "默认路径: $DEFAULT_SNIPPET"
  return 1
}

read_snippet_to_file() {
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

# ── 公共预检 ─────────────────────────────────────────────────
preflight() {
  if [[ $EUID -ne 0 ]]; then
    err "需要 root 权限 (EUID=0, 当前=$EUID)"
    return 1
  fi
  if ! command -v journalctl >/dev/null 2>&1; then
    err "未找到 journalctl (非 systemd 系统?)"
    return 1
  fi
  return 0
}

# ── 校验 snippet 内容 (防误覆盖) ────────────────────────────
validate_snippet() {
  local snippet="$1"
  # 必须有 [Journal] section
  if ! grep -qE '^\[Journal\]' "$snippet"; then
    err "snippet 缺少 [Journal] section, 拒绝"
    return 1
  fi
  # 必须有至少一个容量/保留期限制
  if ! grep -qE '^(SystemMaxUse|RuntimeMaxUse|SystemKeepFree|RuntimeKeepFree|MaxRetentionSec|MaxFileSec)\s*=' "$snippet"; then
    err "snippet 未包含容量/保留期限制 (SystemMaxUse/MaxRetentionSec 等), 拒绝"
    return 1
  fi
  return 0
}

# ── restart systemd-journald ─────────────────────────────────
restart_journald() {
  info "restarting $SERVICE (应用新 drop-in + 截断旧 journal 到新上限)"
  if systemctl restart "$SERVICE" 2>&1; then
    ok "$SERVICE restarted"
    return 0
  fi
  err "restart $SERVICE 失败"
  return 1
}

# ── install 子命令 ───────────────────────────────────────────
do_install() {
  local cfg; cfg="$(resolve_snippet "${1:-}")" || return 1
  preflight || return 1

  hdr "install journald drop-in"
  info "源: $cfg"
  info "目标: $TARGET"

  local source
  source="$(read_snippet_to_file "$cfg")" || { err "读取 snippet 失败"; return 1; }
  validate_snippet "$source" || { rm -f "$source"; return 1; }

  # 创建 drop-in 目录
  if [[ ! -d "$TARGET_DIR" ]]; then
    mkdir -p "$TARGET_DIR"
    ok "创建目录 $TARGET_DIR"
  fi

  # 备份现有 drop-in (如果有)
  if [[ -f "$TARGET" ]]; then
    if diff -q "$source" "$TARGET" >/dev/null 2>&1; then
      info "drop-in 内容相同, 跳过 install ($TARGET)"
      rm -f "$source"
      return 0
    fi
    local bak; bak="$TARGET.bak.$(date +%Y%m%d-%H%M%S)"
    cp -p "$TARGET" "$bak"
    warn "drop-in 内容不同, 已备份为 $bak"
  fi

  # cp snippet -> drop-in
  install -m 0644 "$source" "$TARGET"
  rm -f "$source"
  ok "已安装 $TARGET"

  # restart (drop-in 需 restart 才生效)
  if restart_journald; then
    ok "journald 已应用新限制"
    return 0
  fi
  warn "restart 失败 (请手动: systemctl restart $SERVICE)"
  return 1
}

# ── uninstall 子命令 ────────────────────────────────────────
do_uninstall() {
  preflight || return 1
  hdr "uninstall journald drop-in"
  if [[ ! -f "$TARGET" ]]; then
    info "$TARGET 不存在, 无需卸载"
    return 0
  fi
  # 备份
  local bak; bak="$TARGET.uninstalled.$(date +%Y%m%d-%H%M%S)"
  cp -p "$TARGET" "$bak"
  info "已备份为 $bak"
  # rm
  rm -f "$TARGET"
  ok "已删除 $TARGET"

  # restart
  if restart_journald; then
    ok "journald 已撤销限制"
  else
    warn "restart 失败 (请手动: systemctl restart $SERVICE)"
    return 1
  fi
  return 0
}

# ── status 子命令 ───────────────────────────────────────────
do_status() {
  hdr "journald 当前配置 + journal 状态"
  info "drop-in 文件:"
  if [[ -f "$TARGET" ]]; then
    ls -la "$TARGET" | sed 's/^/    /'
    info "  内容:"
    sed 's/^/    /' "$TARGET"
  else
    warn "    $TARGET 不存在 (未部署 drop-in)"
  fi
  # 同时检查主配置 (以防有人手工改了)
  info "/etc/systemd/journald.conf 关键配置:"
  if [[ -f /etc/systemd/journald.conf ]]; then
    grep -E '^(SystemMaxUse|RuntimeMaxUse|MaxRetentionSec|MaxFileSec|Storage|ForwardToSyslog)=' /etc/systemd/journald.conf 2>/dev/null \
      | sed 's/^/    /' || warn "    未找到限制配置"
  else
    warn "    /etc/systemd/journald.conf 不存在"
  fi

  echo ""
  info "journal 实际使用:"
  du -sh /var/log/journal/ 2>/dev/null | sed 's/^/    /' || warn "    无法读取 journal 大小"
  journalctl --disk-usage 2>&1 | sed 's/^/    /'

  echo ""
  info "$SERVICE 状态:"
  systemctl is-active "$SERVICE" 2>&1 | sed 's/^/    /'
  systemctl show "$SERVICE" --property=ActiveState,MainPID,ActiveEnterTimestamp 2>/dev/null | sed 's/^/    /'

  echo ""
  info "llm-gateway-go stderr/stdout 配置 (确认是 journal 还是 append:):"
  systemctl show llm-gateway-go --property=StandardOutput,StandardError 2>/dev/null | sed 's/^/    /'

  echo ""
  info "最近 5 条 llm-gateway-go stderr (验证 journal 仍写入):"
  journalctl -u llm-gateway-go -p err --no-pager -n 5 2>&1 | head -10 | sed 's/^/    /' || true

  return 0
}

# ── verify 子命令 ───────────────────────────────────────────
do_verify() {
  preflight || return 1
  hdr "verify drop-in 是否被 systemd-journald 识别"
  if [[ ! -f "$TARGET" ]]; then
    err "$TARGET 不存在"
    return 1
  fi
  # 检查主配置 + 所有 drop-in 的合并视图
  local merged
  merged="$(systemd-analyze cat-config systemd/journald.conf 2>/dev/null || journalctl --verify 2>&1 || true)"
  if echo "$merged" | grep -qE '^\s*SystemMaxUse\s*=\s*200M'; then
    ok "merged config 包含 SystemMaxUse=200M (drop-in 已生效)"
    return 0
  fi
  warn "merged config 未找到 SystemMaxUse=200M, 可能 drop-in 未生效"
  echo "--- merged config (SystemMaxUse/MaxRetentionSec 行) ---"
  echo "$merged" | grep -E '(SystemMaxUse|MaxRetentionSec|MaxFileSec)' | sed 's/^/    /' | head -10
  return 1
}

# ── 派发 ─────────────────────────────────────────────────────
case "$ACTION" in
  install)   do_install "${1:-}" ;;
  uninstall) do_uninstall ;;
  status)    do_status ;;
  verify)    do_verify ;;
esac