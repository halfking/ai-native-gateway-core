#!/bin/bash
# 部署 252 自动化空表清理方案
#
# 功能：
#   1. 上传新脚本 pg17-proactive-empty-table-cleanup.sh 到 252:/opt/scripts/
#   2. 更新 pg17-emergency-cleanup.sh（修复默认分区删除逻辑）
#   3. 更新 /etc/cron.d/pg17（追加预防性清理任务）
#   4. 验证语法和执行 dry-run 测试
#   5. 生成部署报告
#
# 用法：
#   bash scripts/deploy-252-auto-cleanup.sh [--dry-run]
#
# 创建：2026-09-06
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[✓]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[✗]${NC} $*"; }

DRY_RUN=false
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=true
  log_warn "DRY RUN 模式：只显示操作，不实际部署"
fi

SSH_HOST="115.29.212.252"
SSH_PORT="25022"
SSH_USER="root"

# 加载环境变量
log_info "加载环境变量..."

# 跳过可能有依赖问题的环境变量文件，直接尝试密钥认证
# 如果用户已设置 SSH_PASS，则使用密码认证
if [ -n "${SSH_PASS:-}" ]; then
  log_info "使用密码认证"
  USING_KEY_AUTH=false
  SSH_CMD="sshpass -p '$SSH_PASS' ssh -p $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null $SSH_USER@$SSH_HOST"
  SCP_CMD="sshpass -p '$SSH_PASS' scp -P $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
else
  log_info "尝试使用 SSH 密钥认证..."
  # 测试是否可以用密钥登录
  if ssh -p $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o PasswordAuthentication=no $SSH_USER@$SSH_HOST "echo ok" >/dev/null 2>&1; then
    log_success "SSH 密钥认证可用"
    SSH_CMD="ssh -p $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null $SSH_USER@$SSH_HOST"
    SCP_CMD="scp -P $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
    USING_KEY_AUTH=true
  else
    log_error "无法连接到 252 服务器"
    log_error "请确保："
    log_error "  1. 设置 SSH_PASS 环境变量使用密码认证，或"
    log_error "  2. SSH 密钥已配置到 252 服务器"
    exit 1
  fi
fi

log_info "=========================================="
log_info "252 自动化空表清理方案部署"
log_info "=========================================="
echo ""

# 1. 验证 SSH 连接
log_info "步骤 1/7: 验证 SSH 连接..."
if eval "$SSH_CMD 'echo ok'" >/dev/null 2>&1; then
  log_success "SSH 连接正常"
else
  log_error "SSH 连接失败"
  exit 1
fi
echo ""

# 2. 备份现有配置
log_info "步骤 2/7: 备份现有配置..."
BACKUP_TS=$(date +%Y%m%d-%H%M%S)
if [ "$DRY_RUN" = "false" ]; then
  eval "$SSH_CMD '
    mkdir -p /opt/scripts/backups
    [ -f /opt/scripts/pg17-emergency-cleanup.sh ] && cp /opt/scripts/pg17-emergency-cleanup.sh /opt/scripts/backups/pg17-emergency-cleanup.sh.bak.$BACKUP_TS
    [ -f /etc/cron.d/pg17 ] && cp /etc/cron.d/pg17 /etc/cron.d/pg17.bak.$BACKUP_TS
  '" >/dev/null 2>&1
  log_success "备份完成: /opt/scripts/backups/*.bak.$BACKUP_TS"
else
  log_info "[DRY-RUN] 会备份到 /opt/scripts/backups/*.bak.$BACKUP_TS"
fi
echo ""

# 3. 上传新脚本
log_info "步骤 3/7: 上传脚本到 252 服务器..."
SCRIPTS_TO_UPLOAD=(
  "$PROJECT_ROOT/scripts/252-monitor/pg17-proactive-empty-table-cleanup.sh"
  "$PROJECT_ROOT/scripts/252-monitor/pg17-emergency-cleanup.sh"
)

for script in "${SCRIPTS_TO_UPLOAD[@]}"; do
  if [ ! -f "$script" ]; then
    log_error "脚本不存在: $script"
    exit 1
  fi
  
  script_name=$(basename "$script")
  if [ "$DRY_RUN" = "false" ]; then
    eval "$SCP_CMD '$script' $SSH_USER@$SSH_HOST:/opt/scripts/$script_name" >/dev/null 2>&1
    eval "$SSH_CMD 'chmod +x /opt/scripts/$script_name'" >/dev/null 2>&1
    log_success "已上传: $script_name"
  else
    log_info "[DRY-RUN] 会上传: $script_name"
  fi
done
echo ""

# 4. 更新 cron 配置
log_info "步骤 4/7: 更新 /etc/cron.d/pg17..."
if [ "$DRY_RUN" = "false" ]; then
  eval "$SSH_CMD '
    # 检查是否已存在预防性清理任务
    if grep -q \"pg17-proactive-empty-table-cleanup\" /etc/cron.d/pg17 2>/dev/null; then
      echo \"预防性清理任务已存在，跳过添加\"
    else
      # 追加新任务
      cat >> /etc/cron.d/pg17 << \"EOF\"

# === 预防性空表清理（2026-09-06 新增）===
0 2 * * * root /opt/scripts/pg17-proactive-empty-table-cleanup.sh >/dev/null 2>&1
EOF
      echo \"已追加预防性清理任务\"
    fi
  '" 2>&1 | grep -v "Warning: Permanently added"
  log_success "cron 配置已更新"
else
  log_info "[DRY-RUN] 会追加以下内容到 /etc/cron.d/pg17:"
  echo "0 2 * * * root /opt/scripts/pg17-proactive-empty-table-cleanup.sh >/dev/null 2>&1"
fi
echo ""

# 5. 验证语法
log_info "步骤 5/7: 验证脚本语法..."
for script_name in pg17-proactive-empty-table-cleanup.sh pg17-emergency-cleanup.sh; do
  if eval "$SSH_CMD 'bash -n /opt/scripts/$script_name'" >/dev/null 2>&1; then
    log_success "$script_name 语法正确"
  else
    log_error "$script_name 语法错误"
    exit 1
  fi
done
echo ""

# 6. 执行 dry-run 测试
log_info "步骤 6/7: 执行 dry-run 测试..."
if [ "$DRY_RUN" = "false" ]; then
  log_info "运行: pg17-proactive-empty-table-cleanup.sh --dry-run"
  eval "$SSH_CMD '/opt/scripts/pg17-proactive-empty-table-cleanup.sh --dry-run'" 2>&1 | grep -v "Warning: Permanently added" | tail -20
  log_success "dry-run 测试完成"
else
  log_info "[DRY-RUN] 会执行: /opt/scripts/pg17-proactive-empty-table-cleanup.sh --dry-run"
fi
echo ""

# 7. 验证 cron 配置
log_info "步骤 7/7: 验证 cron 配置..."
if [ "$DRY_RUN" = "false" ]; then
  log_info "当前 /etc/cron.d/pg17 中的任务:"
  eval "$SSH_CMD 'grep -E \"^[^#]\" /etc/cron.d/pg17 | grep -v \"^\$\"'" 2>&1 | grep -v "Warning: Permanently added"
  log_success "cron 配置验证完成"
else
  log_info "[DRY-RUN] 会显示 /etc/cron.d/pg17 中的任务"
fi
echo ""

# 生成部署报告
log_info "=========================================="
log_success "部署完成！"
log_info "=========================================="
echo ""

if [ "$DRY_RUN" = "false" ]; then
  log_info "📋 部署摘要:"
  echo ""
  log_info "✅ 新增脚本:"
  log_info "   - /opt/scripts/pg17-proactive-empty-table-cleanup.sh"
  echo ""
  log_info "✅ 更新脚本:"
  log_info "   - /opt/scripts/pg17-emergency-cleanup.sh (修复默认分区删除逻辑)"
  echo ""
  log_info "✅ cron 任务:"
  log_info "   - 每日 02:00 执行预防性空表清理"
  echo ""
  log_info "📊 监控:"
  log_info "   - 日志: tail -f /var/log/pg17-proactive-cleanup.log"
  log_info "   - 告警: 飞书群「股龙」"
  echo ""
  log_info "🧪 手动测试:"
  log_info "   - Dry-run: ssh root@252 '/opt/scripts/pg17-proactive-empty-table-cleanup.sh --dry-run'"
  log_info "   - 立即执行: ssh root@252 '/opt/scripts/pg17-proactive-empty-table-cleanup.sh --force'"
  echo ""
  log_info "🔄 回滚方法:"
  log_info "   - 恢复脚本: ssh root@252 'cp /opt/scripts/backups/*.bak.$BACKUP_TS /opt/scripts/'"
  log_info "   - 恢复 cron: ssh root@252 'cp /etc/cron.d/pg17.bak.$BACKUP_TS /etc/cron.d/pg17'"
  echo ""
  log_success "✅ 自动化清理方案已成功部署到 252 服务器"
else
  log_info "这是 DRY RUN 模式，没有实际修改任何文件"
  log_info "移除 --dry-run 参数以执行实际部署"
fi
