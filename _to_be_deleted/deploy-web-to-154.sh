#!/usr/bin/env bash
# =====================================================================
# deploy-web-to-154.sh — 部署新构建的 web/dist 到 154
#
# 背景：
#   诊断发现 154 上部署的前端 bundle 缺少 maintain-api/healthz 探测逻辑，
#   导致「运维中心」菜单无法注入。本脚本部署新构建的 web/dist 修复此问题。
#
# 用法：
#   ./deploy-web-to-154.sh
#
# 前置条件：
#   1. 本地 web/dist 已构建（npm run build）
#   2. 新 bundle 包含探测逻辑（已验证）
# =====================================================================

set -euo pipefail

# ── 颜色 ──────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}!${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}═══════ $* ═══════${N}"; }

# ── 配置 ──────────────────────────────────────────────────────────
SSH_HOST="47.97.111.154"
SSH_PORT="25022"
SSH_USER="root"
REMOTE_DIR="/opt/llm-gateway-go"
LOCAL_WEB_DIST="web/dist"

# ── 预检 ──────────────────────────────────────────────────────────
phase "预检"

if [ ! -d "$LOCAL_WEB_DIST" ]; then
  err "本地 $LOCAL_WEB_DIST 不存在"
  echo "请先执行: cd web && npm run build"
  exit 1
fi

ENTRY_BUNDLE=$(grep -oE 'assets/index-[^"]*\.js' "$LOCAL_WEB_DIST/index.html" | head -1)
if [ -z "$ENTRY_BUNDLE" ]; then
  err "无法从 $LOCAL_WEB_DIST/index.html 提取入口 bundle"
  exit 1
fi

HEALTHZ_COUNT=$(grep -o "healthz" "$LOCAL_WEB_DIST/$ENTRY_BUNDLE" | wc -l | awk '{print $1}')
if [ "$HEALTHZ_COUNT" -lt 1 ]; then
  err "新 bundle 不包含 healthz 探测逻辑（出现次数: $HEALTHZ_COUNT）"
  echo "请确认源码已更新并重新构建"
  exit 1
fi

ok "本地 $LOCAL_WEB_DIST 存在"
ok "入口 bundle: $ENTRY_BUNDLE"
ok "healthz 探测逻辑已包含（出现 $HEALTHZ_COUNT 次）"

# ── 备份远端旧 web/dist ──────────────────────────────────────────
phase "备份远端旧 web/dist"

info "在 154 上备份旧的 web/dist..."
ssh -p "$SSH_PORT" "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR
  if [ -d web/dist ]; then
    BACKUP_NAME=\"web-dist-backup-\$(date +%Y%m%d-%H%M%S)\"
    echo \"备份到: \$BACKUP_NAME\"
    cp -r web/dist \$BACKUP_NAME
    echo \"✓ 备份完成\"
  else
    echo \"! 远端 web/dist 不存在，跳过备份\"
  fi
"

ok "远端备份完成"

# ── 上传新 web/dist ──────────────────────────────────────────────
phase "上传新 web/dist"

info "rsync 上传 $LOCAL_WEB_DIST 到 154:$REMOTE_DIR/web/dist..."
rsync -avz --delete \
  -e "ssh -p $SSH_PORT" \
  "$LOCAL_WEB_DIST/" \
  "$SSH_USER@$SSH_HOST:$REMOTE_DIR/web/dist/"

ok "web/dist 已上传"

# ── 验证远端部署 ──────────────────────────────────────────────────
phase "验证远端部署"

info "检查远端 web/dist 的入口 bundle..."
ssh -p "$SSH_PORT" "$SSH_USER@$SSH_HOST" "
  cd $REMOTE_DIR/web/dist
  ENTRY=\$(grep -oE 'assets/index-[^\"]*\.js' index.html | head -1)
  if [ -z \"\$ENTRY\" ]; then
    echo \"✗ 远端 index.html 无法提取入口 bundle\"
    exit 1
  fi
  echo \"远端入口 bundle: \$ENTRY\"
  
  if [ ! -f \"\$ENTRY\" ]; then
    echo \"✗ 远端 \$ENTRY 文件不存在\"
    exit 1
  fi
  
  HEALTHZ_COUNT=\$(grep -o 'healthz' \"\$ENTRY\" | wc -l | awk '{print \$1}')
  echo \"远端 bundle healthz 出现次数: \$HEALTHZ_COUNT\"
  
  if [ \"\$HEALTHZ_COUNT\" -lt 1 ]; then
    echo \"✗ 远端 bundle 不包含 healthz 探测逻辑\"
    exit 1
  fi
  
  echo \"✓ 远端 bundle 包含探测逻辑\"
"

ok "远端部署验证通过"

# ── 重启服务（可选）──────────────────────────────────────────────
phase "重启服务"

warn "静态文件已更新，但 Go 进程可能缓存了旧的 dist 目录句柄"
echo "建议重启 gateway 服务以确保生效："
echo ""
echo "  ssh -p $SSH_PORT $SSH_USER@$SSH_HOST"
echo "  cd $REMOTE_DIR"
echo "  systemctl restart llm-gateway  # 或你使用的进程管理器"
echo ""
read -p "是否现在重启服务? [y/N] " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  info "重启 gateway 服务..."
  ssh -p "$SSH_PORT" "$SSH_USER@$SSH_HOST" "
    systemctl restart llm-gateway 2>/dev/null || \
    supervisorctl restart llm-gateway 2>/dev/null || \
    (cd $REMOTE_DIR && ./stop.sh && ./start.sh) || \
    echo '! 无法自动重启，请手动重启服务'
  "
  ok "服务已重启"
else
  warn "跳过重启，请手动重启服务"
fi

# ── 完成 ──────────────────────────────────────────────────────────
phase "部署完成"

echo ""
ok "web/dist 已成功部署到 154"
echo ""
echo "验证步骤（浏览器）："
echo "  1. 访问 https://llm.kxpms.cn/dashboard"
echo "  2. 打开开发者工具 Network 标签"
echo "  3. 刷新页面，找到 index-*.js 的请求"
echo "  4. 确认新 bundle hash 与本地一致: $ENTRY_BUNDLE"
echo ""
echo "验证步骤（curl）："
echo "  curl -s https://llm.kxpms.cn/dashboard | grep -oE 'assets/index-[^\"]*\\.js'"
echo "  # 应输出: $ENTRY_BUNDLE"
echo ""
echo "最终功能验证："
echo "  1. 用超级管理员账号登录 llm.kxpms.cn"
echo "  2. 左侧导航栏应显示「运维中心」菜单组"
echo "  3. 点击「运维中心」任意子菜单，应跳转到 /maintain/ops/* 路径"
echo ""
