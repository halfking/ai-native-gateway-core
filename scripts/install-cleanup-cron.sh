#!/bin/bash
# install-cleanup-cron.sh
# 安装数据清理定时任务到 crontab

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

function log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
function log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
function log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
function log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# 检查权限
if [[ $EUID -ne 0 ]]; then
   log_error "此脚本需要 root 权限运行"
   log_info "请使用: sudo $0"
   exit 1
fi

log_info "开始安装 LLM Gateway 数据清理定时任务..."
echo ""

# 1. 检查 cron 服务
log_info "检查 cron 服务状态..."
if systemctl is-active --quiet cron || systemctl is-active --quiet crond; then
    log_success "cron 服务正在运行"
else
    log_warn "cron 服务未运行，尝试启动..."
    systemctl start cron || systemctl start crond || {
        log_error "无法启动 cron 服务"
        exit 1
    }
    log_success "cron 服务已启动"
fi
echo ""

# 2. 创建必要的目录
log_info "创建必要的目录..."
mkdir -p /opt/llm-gateway-archive
mkdir -p /var/log
log_success "目录创建完成"
echo ""

# 3. 安装 crontab
CRON_FILE="/etc/cron.d/llm-gateway-cleanup"
TEMPLATE_FILE="$SCRIPT_DIR/crontab.template"

if [[ ! -f "$TEMPLATE_FILE" ]]; then
    log_error "模板文件不存在: $TEMPLATE_FILE"
    exit 1
fi

log_info "安装 crontab 到 $CRON_FILE..."
cp "$TEMPLATE_FILE" "$CRON_FILE"
chmod 644 "$CRON_FILE"
log_success "crontab 已安装"
echo ""

# 4. 验证 crontab 语法
log_info "验证 crontab 语法..."
if crontab -l 2>/dev/null | grep -q "llm-gateway"; then
    log_info "当前用户 crontab 中已有 llm-gateway 任务"
fi

# 检查 /etc/cron.d/ 文件
if [[ -f "$CRON_FILE" ]]; then
    log_success "crontab 文件已存在: $CRON_FILE"
else
    log_error "crontab 文件安装失败"
    exit 1
fi
echo ""

# 5. 显示已安装的任务
log_info "已安装的定时任务:"
grep -E "^[^#]" "$CRON_FILE" | grep -v "^$" || log_warn "没有启用的任务（所有任务都被注释）"
echo ""

# 6. 配置提示
log_info "=========================================="
log_info "安装完成！"
log_info "=========================================="
echo ""
log_info "📋 下一步操作:"
echo ""
log_info "1. 编辑配置（如需修改）:"
log_info "   sudo vim $CRON_FILE"
echo ""
log_info "2. 查看日志:"
log_info "   tail -f /var/log/llm-gateway-cleanup.log"
echo ""
log_info "3. 手动测试清理任务（dry-run）:"
log_info "   cd /opt/llm-gateway-go"
log_info "   DRY_RUN=true bash scripts/cleanup-request-logs.sh"
echo ""
log_info "4. 手动执行清理任务:"
log_info "   cd /opt/llm-gateway-go"
log_info "   bash scripts/cleanup-request-logs.sh"
echo ""
log_info "5. 卸载定时任务:"
log_info "   sudo rm $CRON_FILE"
echo ""
log_success "✅ 定时任务已成功安装"
