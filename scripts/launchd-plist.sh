#!/bin/bash
# scripts/launchd-plist.sh — launchd plist 生成/卸载/加载
#
# 用法:
#   ./scripts/launchd-plist.sh install       # 生成 + 加载
#   ./scripts/launchd-plist.sh uninstall     # 卸载
#   ./scripts/launchd-plist.sh status        # 检查状态
#   ./scripts/launchd-plist.sh generate      # 只生成不加载

set -euo pipefail

SERVICE_LABEL="${SERVICE_LABEL:-com.kaixuan.llm-gateway-go}"
REMOTE_DIR="${REMOTE_DIR:-/Users/kaixuan/workspace/official-deploy/services/llm-gateway-go}"
LISTEN_PORT="${LISTEN_PORT:-8080}"
PLIST_PATH="$HOME/Library/LaunchAgents/${SERVICE_LABEL}.plist"

G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; N='\033[0m'
ok()   { echo -e "${G}✓${N} $*"; }
info() { echo -e "${Y}▶${N} $*"; }
err()  { echo -e "${R}✗${N} $*" >&2; }

generate_plist() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${SERVICE_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${REMOTE_DIR}/bin/llm-gateway-go</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${REMOTE_DIR}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin</string>
    </dict>
    <key>StandardOutPath</key>
    <string>${REMOTE_DIR}/logs/gateway.log</string>
    <key>StandardErrorPath</key>
    <string>${REMOTE_DIR}/logs/gateway.err.log</string>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>SoftResourceLimits</key>
    <dict>
        <key>NumberOfFiles</key>
        <integer>65536</integer>
    </dict>
</dict>
</plist>
EOF
}

case "${1:-}" in
  install)
    mkdir -p "$HOME/Library/LaunchAgents"
    generate_plist > "$PLIST_PATH"
    chmod 644 "$PLIST_PATH"
    ok "已生成 $PLIST_PATH"
    if launchctl list | grep -q "$SERVICE_LABEL"; then
      info "已加载, 重新加载..."
      launchctl bootout "gui/$(id -u)/${SERVICE_LABEL}" 2>/dev/null || true
      sleep 1
    fi
    launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null \
      || launchctl load "$PLIST_PATH"
    sleep 2
    ok "已加载 launchd job"
    launchctl list | grep "$SERVICE_LABEL" || warn "未在 list 中找到"
    ;;

  uninstall)
    launchctl bootout "gui/$(id -u)/${SERVICE_LABEL}" 2>/dev/null \
      || launchctl unload "$PLIST_PATH" 2>/dev/null || true
    rm -f "$PLIST_PATH"
    ok "已卸载并删除 plist"
    ;;

  status)
    if launchctl list | grep -q "$SERVICE_LABEL"; then
      launchctl list | grep "$SERVICE_LABEL"
      lsof -iTCP:${LISTEN_PORT} -sTCP:LISTEN 2>/dev/null || true
    else
      err "launchd job 未加载"
      exit 1
    fi
    ;;

  generate)
    generate_plist
    ;;

  *)
    echo "用法: $0 {install|uninstall|status|generate}"
    exit 1
    ;;
esac
