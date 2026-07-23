#!/usr/bin/env bash
# scripts/install/upgrade.sh - 用户升级工具
# 用法：sudo ./upgrade.sh [--check | --apply | --rollback] [--version VERSION]

set -euo pipefail

# ============================================================================
# 配置
# ============================================================================

INSTALL_DIR="/opt/llm-gateway-go"
SERVICE_USER="llm-gateway"
SERVICE_NAME="llm-gateway-go"
BACKUP_DIR="/var/lib/llm-gateway-go/backups"
CURRENT_VERSION_FILE="${INSTALL_DIR}/VERSION"

# 默认值
ACTION="check"
TARGET_VERSION=""

# 解析参数
for arg in "$@"; do
    case "$arg" in
        --check)
            ACTION="check"
            ;;
        --apply)
            ACTION="apply"
            ;;
        --rollback)
            ACTION="rollback"
            ;;
        --version=*)
            TARGET_VERSION="${arg#--version=}"
            ;;
        --help|-h)
            cat << HELP
用法: sudo $0 [选项]

选项:
  --check              检查可用更新（默认）
  --apply              执行升级
  --rollback           回滚到上一个版本
  --version=VERSION    指定目标版本
  --help, -h           显示帮助

示例:
  sudo $0 --check
  sudo $0 --apply --version=2.4.8-1348
  sudo $0 --rollback
HELP
            exit 0
            ;;
    esac
done

# ============================================================================
# 权限检查
# ============================================================================

if [[ $EUID -ne 0 ]]; then
    echo "❌ 此脚本需要 root 权限运行"
    echo "   请使用: sudo $0"
    exit 1
fi

echo "========================================="
echo "LLM Gateway Go 升级工具"
echo "========================================="
echo "操作: $ACTION"
[[ -n "$TARGET_VERSION" ]] && echo "目标版本: $TARGET_VERSION"
echo ""

# ============================================================================
# 获取当前版本
# ============================================================================

get_current_version() {
    if [[ -f "$CURRENT_VERSION_FILE" ]]; then
        cat "$CURRENT_VERSION_FILE"
    else
        echo "unknown"
    fi
}

# ============================================================================
# 检查可用更新
# ============================================================================

check_update() {
    echo "📋 检查可用更新..."
    echo ""
    
    CURRENT=$(get_current_version)
    echo "当前版本: $CURRENT"
    echo ""
    
    # 模拟版本检查（实际应调用主控端API）
    LATEST=$(curl -fsS "https://llm.kxpms.cn/api/v1/updates/latest" 2>/dev/null | \
        jq -r '.version // "unknown"' 2>/dev/null || echo "unknown")
    
    echo "最新版本: $LATEST"
    echo ""
    
    if [[ "$LATEST" == "unknown" ]] || [[ "$LATEST" == "$CURRENT" ]]; then
        echo "✅ 已是最新版本"
        return 0
    fi
    
    echo "🆕 发现新版本: $LATEST"
    echo ""
    echo "查看更新内容: https://docs.kxpms.cn/llm-gateway-go/changelog"
    echo ""
    echo "执行升级: sudo $0 --apply --version=$LATEST"
}

# ============================================================================
# 执行升级
# ============================================================================

apply_upgrade() {
    echo "🚀 执行升级..."
    echo ""
    
    if [[ -z "$TARGET_VERSION" ]]; then
        echo "❌ 请指定目标版本: --version=VERSION"
        exit 1
    fi
    
    # 创建备份
    echo "📦 创建备份..."
    mkdir -p "$BACKUP_DIR"
    BACKUP_PATH="${BACKUP_DIR}/${CURRENT_VERSION}"
    
    if [[ -f "$INSTALL_DIR/bin/llm-gateway-go" ]]; then
        cp -p "$INSTALL_DIR/bin/llm-gateway-go" "$BACKUP_PATH.bin"
        echo "   ✅ 二进制已备份: $BACKUP_PATH.bin"
    fi
    
    if [[ -f "$INSTALL_DIR/config/config.yaml" ]]; then
        cp -p "$INSTALL_DIR/config/config.yaml" "$BACKUP_PATH.yaml"
        echo "   ✅ 配置已备份: $BACKUP_PATH.yaml"
    fi
    
    # 下载新版本
    echo ""
    echo "📥 下载新版本..."
    DOWNLOAD_URL="https://files.kxpms.cn/llm-gateway-go/releases/${TARGET_VERSION}/llm-gateway-go-${TARGET_VERSION}-linux-amd64.tar.gz"
    TEMP_FILE="/tmp/upgrade-$$.tar.gz"
    
    if ! curl -fsSL -o "$TEMP_FILE" "$DOWNLOAD_URL"; then
        echo "   ❌ 下载失败: $DOWNLOAD_URL"
        rm -f "$TEMP_FILE"
        exit 1
    fi
    
    echo "   ✅ 下载完成"
    
    # 停止服务
    echo ""
    echo "🛑 停止服务..."
    systemctl stop "$SERVICE_NAME"
    sleep 2
    echo "   ✅ 服务已停止"
    
    # 解压并安装
    echo ""
    echo "📦 安装新版本..."
    TEMP_DIR=$(mktemp -d)
    tar xzf "$TEMP_FILE" -C "$TEMP_DIR"
    EXTRACTED_DIR=$(ls "$TEMP_DIR" | head -1)
    NEW_BINARY="$TEMP_DIR/$EXTRACTED_DIR/bin/llm-gateway-go"
    
    if [[ ! -f "$NEW_BINARY" ]]; then
        echo "   ❌ 未找到二进制文件"
        systemctl start "$SERVICE_NAME"
        rm -rf "$TEMP_DIR" "$TEMP_FILE"
        exit 1
    fi
    
    # 备份并替换
    if [[ -f "$INSTALL_DIR/bin/llm-gateway-go" ]]; then
        mv "$INSTALL_DIR/bin/llm-gateway-go" "$INSTALL_DIR/bin/llm-gateway-go.old"
    fi
    
    cp "$NEW_BINARY" "$INSTALL_DIR/bin/llm-gateway-go"
    chmod +x "$INSTALL_DIR/bin/llm-gateway-go"
    
    rm -rf "$TEMP_DIR" "$TEMP_FILE"
    echo "   ✅ 新版本已安装"
    
    # 启动服务
    echo ""
    echo "🚀 启动服务..."
    systemctl start "$SERVICE_NAME"
    sleep 5
    
    if ! systemctl is-active --quiet "$SERVICE_NAME"; then
        echo "   ❌ 服务启动失败，执行回滚"
        rollback
        exit 1
    fi
    
    echo "   ✅ 服务已启动"
    
    # 健康检查
    echo ""
    echo "🏥 健康检查..."
    for i in {1..10}; do
        if curl -fsS http://localhost:8781/healthz | grep -q '"status":"ok"'; then
            echo "   ✅ 健康检查通过"
            echo ""
            echo "🎉 升级完成！"
            echo "   新版本: $TARGET_VERSION"
            echo "   备份位置: $BACKUP_DIR"
            echo "   如需回滚: sudo $0 --rollback"
            return 0
        fi
        sleep 2
    done
    
    echo "   ❌ 健康检查失败，执行回滚"
    rollback
    exit 1
}

# ============================================================================
# 回滚
# ============================================================================

rollback() {
    echo ""
    echo "⏪ 执行回滚..."
    echo ""
    
    # 查找最新备份
    LATEST_BACKUP=$(ls -t "$BACKUP_DIR"/*.bin 2>/dev/null | head -1)
    
    if [[ -z "$LATEST_BACKUP" ]]; then
        echo "   ❌ 未找到备份文件"
        return 1
    fi
    
    echo "   备份文件: $LATEST_BACKUP"
    
    # 停止服务
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    sleep 2
    
    # 恢复二进制
    if [[ -f "$LATEST_BACKUP" ]]; then
        if [[ -f "$INSTALL_DIR/bin/llm-gateway-go" ]]; then
            mv "$INSTALL_DIR/bin/llm-gateway-go" "$INSTALL_DIR/bin/llm-gateway-go.failed"
        fi
        cp "$LATEST_BACKUP" "$INSTALL_DIR/bin/llm-gateway-go"
        chmod +x "$INSTALL_DIR/bin/llm-gateway-go"
        echo "   ✅ 二进制已恢复"
    fi
    
    # 恢复配置
    YAML_BACKUP="${LATEST_BACKUP%.bin}.yaml"
    if [[ -f "$YAML_BACKUP" ]] && [[ -f "$INSTALL_DIR/config/config.yaml" ]]; then
        cp -p "$YAML_BACKUP" "$INSTALL_DIR/config/config.yaml"
        echo "   ✅ 配置已恢复"
    fi
    
    # 启动服务
    systemctl start "$SERVICE_NAME"
    sleep 5
    
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        echo "   ✅ 服务已启动"
        echo ""
        echo "🎉 回滚完成"
    else
        echo "   ❌ 服务启动失败"
        echo "   查看日志: journalctl -u $SERVICE_NAME -n 50"
        return 1
    fi
}

# ============================================================================
# 主流程
# ============================================================================

case "$ACTION" in
    "check")
        check_update
        ;;
    "apply")
        apply_upgrade
        ;;
    "rollback")
        rollback
        ;;
esac

