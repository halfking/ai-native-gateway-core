#!/usr/bin/env bash
# setup-auto-monitoring.sh - 设置Auto模式24小时持续监控
#
# 功能：
# 1. 设置cron定时任务，每小时运行一次监控
# 2. 创建日志目录和配置文件
# 3. 提供启动/停止/查看状态的命令

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MONITOR_SCRIPT="$SCRIPT_DIR/auto-monitor.sh"
LOG_DIR="/tmp/auto_monitoring"
CRON_JOB_MARKER="# Auto Mode Monitoring - DO NOT EDIT"

# 颜色
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[✓]${NC} $*"; }
log_warning() { echo -e "${YELLOW}[⚠]${NC} $*"; }

# 创建日志目录
setup_log_directory() {
    if [ ! -d "$LOG_DIR" ]; then
        mkdir -p "$LOG_DIR"
        log_success "创建日志目录: $LOG_DIR"
    else
        log_info "日志目录已存在: $LOG_DIR"
    fi
}

# 设置cron任务
setup_cron() {
    log_info "设置cron定时任务..."
    
    # 检查是否已存在
    if crontab -l 2>/dev/null | grep -q "$CRON_JOB_MARKER"; then
        log_warning "监控任务已存在，跳过创建"
        return 0
    fi
    
    # 添加cron任务
    local cron_entry="0 * * * * cd $(pwd) && bash $MONITOR_SCRIPT >> $LOG_DIR/auto_monitor.log 2>&1 $CRON_JOB_MARKER"
    
    (crontab -l 2>/dev/null || true; echo "$cron_entry") | crontab -
    
    log_success "已添加cron任务：每小时运行一次监控"
    log_info "日志位置: $LOG_DIR/auto_monitor.log"
}

# 移除cron任务
remove_cron() {
    log_info "移除cron定时任务..."
    
    if ! crontab -l 2>/dev/null | grep -q "$CRON_JOB_MARKER"; then
        log_warning "未找到监控任务"
        return 0
    fi
    
    crontab -l 2>/dev/null | grep -v "$CRON_JOB_MARKER" | crontab -
    log_success "已移除cron任务"
}

# 查看状态
show_status() {
    log_info "Auto模式监控状态"
    echo ""
    
    # 检查cron任务
    if crontab -l 2>/dev/null | grep -q "$CRON_JOB_MARKER"; then
        log_success "Cron任务: 已启用（每小时运行）"
        crontab -l 2>/dev/null | grep "$CRON_JOB_MARKER"
    else
        log_warning "Cron任务: 未启用"
    fi
    
    echo ""
    
    # 检查日志
    if [ -f "$LOG_DIR/auto_monitor.log" ]; then
        log_info "最近的监控日志:"
        tail -20 "$LOG_DIR/auto_monitor.log"
    else
        log_warning "还没有监控日志"
    fi
}

# 手动运行一次
run_once() {
    log_info "手动运行监控检查..."
    bash "$MONITOR_SCRIPT"
}

# 查看监控日志
view_logs() {
    if [ -f "$LOG_DIR/auto_monitor.log" ]; then
        less "$LOG_DIR/auto_monitor.log"
    else
        log_warning "监控日志不存在: $LOG_DIR/auto_monitor.log"
    fi
}

# 主菜单
show_menu() {
    cat <<EOF

====================================
Auto模式监控管理
====================================

当前状态:
$(crontab -l 2>/dev/null | grep -q "$CRON_JOB_MARKER" && echo "  ✅ 监控已启用" || echo "  ⚠️  监控未启用")

操作选项:
  1) 启用24小时监控（设置cron）
  2) 停止监控（移除cron）
  3) 查看状态
  4) 手动运行一次
  5) 查看监控日志
  6) 退出

EOF
}

# 交互式菜单
interactive_menu() {
    while true; do
        show_menu
        read -p "请选择操作 [1-6]: " choice
        
        case $choice in
            1)
                setup_log_directory
                setup_cron
                log_success "监控已启用！"
                ;;
            2)
                remove_cron
                ;;
            3)
                show_status
                ;;
            4)
                run_once
                ;;
            5)
                view_logs
                ;;
            6)
                log_info "退出"
                exit 0
                ;;
            *)
                log_warning "无效选择，请重试"
                ;;
        esac
        
        echo ""
        read -p "按Enter继续..."
    done
}

# 命令行参数处理
case "${1:-menu}" in
    start|enable)
        setup_log_directory
        setup_cron
        ;;
    stop|disable)
        remove_cron
        ;;
    status)
        show_status
        ;;
    run)
        run_once
        ;;
    logs)
        view_logs
        ;;
    menu|*)
        interactive_menu
        ;;
esac
