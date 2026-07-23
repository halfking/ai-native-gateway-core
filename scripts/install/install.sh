#!/usr/bin/env bash
# scripts/install/install.sh - 用户安装脚本
# 用法：sudo ./install.sh [--offline]

set -euo pipefail

# ============================================================================
# 配置
# ============================================================================

INSTALL_DIR="/opt/llm-gateway-go"
SERVICE_USER="llm-gateway"
SERVICE_NAME="llm-gateway-go"
DATA_DIR="/var/lib/llm-gateway-go"
LOG_DIR="/var/log/llm-gateway-go"

OFFLINE_MODE=false

# 解析参数
for arg in "$@"; do
    case "$arg" in
        --offline)
            OFFLINE_MODE=true
            ;;
    esac
done

# ============================================================================
# 检查权限
# ============================================================================

if [[ $EUID -ne 0 ]]; then
   echo "❌ 此脚本需要 root 权限运行"
   echo "   请使用: sudo $0"
   exit 1
fi

echo "========================================="
echo "LLM Gateway Go 安装程序"
echo "========================================="
echo ""

# ============================================================================
# 系统检查
# ============================================================================

echo "📋 系统检查..."

# 检查操作系统
if [[ ! -f /etc/os-release ]]; then
    echo "❌ 无法检测操作系统"
    exit 1
fi

source /etc/os-release
echo "   操作系统: $NAME $VERSION"

# 检查架构
ARCH=$(uname -m)
echo "   架构: $ARCH"

# 检查磁盘空间（至少 5GB）
AVAILABLE_GB=$(df / | tail -1 | awk '{print int($4/1024/1024)}')
if [[ $AVAILABLE_GB -lt 5 ]]; then
    echo "❌ 磁盘空间不足，至少需要 5GB，当前可用: ${AVAILABLE_GB}GB"
    exit 1
fi
echo "   磁盘空间: ${AVAILABLE_GB}GB 可用"

# 检查内存（至少 2GB）
AVAILABLE_MEM_MB=$(free -m | awk '/^Mem:/{print $7}')
if [[ $AVAILABLE_MEM_MB -lt 2048 ]]; then
    echo "⚠️  内存不足，推荐至少 2GB，当前可用: ${AVAILABLE_MEM_MB}MB"
fi
echo "   内存: ${AVAILABLE_MEM_MB}MB 可用"

echo "✅ 系统检查通过"
echo ""

# ============================================================================
# 创建用户和目录
# ============================================================================

echo "📁 创建用户和目录..."

# 创建服务用户
if ! id "$SERVICE_USER" &>/dev/null; then
    useradd -r -s /bin/false -d "$INSTALL_DIR" "$SERVICE_USER"
    echo "   ✅ 创建用户: $SERVICE_USER"
else
    echo "   ℹ️  用户已存在: $SERVICE_USER"
fi

# 创建目录
mkdir -p "$INSTALL_DIR"/{bin,web,config,logs}
mkdir -p "$DATA_DIR"
mkdir -p "$LOG_DIR"

echo "   ✅ 目录创建完成"
echo ""

# ============================================================================
# 安装文件
# ============================================================================

echo "📦 安装文件..."

# 当前目录（解压后的包目录）
PACKAGE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 复制二进制
cp "$PACKAGE_DIR/bin/llm-gateway-go" "$INSTALL_DIR/bin/"
chmod +x "$INSTALL_DIR/bin/llm-gateway-go"
echo "   ✅ 二进制文件"

# 复制前端
if [[ -d "$PACKAGE_DIR/web/dist" ]]; then
    cp -r "$PACKAGE_DIR/web/dist" "$INSTALL_DIR/web/"
    echo "   ✅ 前端文件"
fi

# 复制配置示例
if [[ ! -f "$INSTALL_DIR/config/config.yaml" ]]; then
    cp "$PACKAGE_DIR/config/config.yaml.example" "$INSTALL_DIR/config/config.yaml"
    echo "   ✅ 配置文件"
else
    echo "   ℹ️  配置文件已存在，跳过"
fi

# 设置权限
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR"

echo "   ✅ 权限设置完成"
echo ""

# ============================================================================
# 安装 systemd 服务
# ============================================================================

echo "⚙️  安装 systemd 服务..."

cp "$PACKAGE_DIR/systemd/llm-gateway-go.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo "   ✅ systemd 服务已启用"
echo ""

# ============================================================================
# 配置向导
# ============================================================================

echo "⚙️  配置向导..."
echo ""

read -p "是否现在配置数据库连接? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    read -p "数据库主机 [localhost]: " DB_HOST
    DB_HOST=${DB_HOST:-localhost}
    
    read -p "数据库端口 [5432]: " DB_PORT
    DB_PORT=${DB_PORT:-5432}
    
    read -p "数据库名称 [llm_gateway]: " DB_NAME
    DB_NAME=${DB_NAME:-llm_gateway}
    
    read -p "数据库用户 [postgres]: " DB_USER
    DB_USER=${DB_USER:-postgres}
    
    read -sp "数据库密码: " DB_PASSWORD
    echo
    
    # 更新配置文件
    cat > "$INSTALL_DIR/config/config.yaml" << YAML
server:
  host: 0.0.0.0
  port: 8781

database:
  host: $DB_HOST
  port: $DB_PORT
  database: $DB_NAME
  username: $DB_USER
  password: $DB_PASSWORD

redis:
  host: localhost
  port: 6379
YAML
    
    echo "   ✅ 配置已更新"
fi

echo ""

# ============================================================================
# 启动服务
# ============================================================================

echo "🚀 启动服务..."

systemctl start "$SERVICE_NAME"

# 等待服务启动
sleep 3

# 检查服务状态
if systemctl is-active --quiet "$SERVICE_NAME"; then
    echo "   ✅ 服务已启动"
else
    echo "   ❌ 服务启动失败"
    echo "   查看日志: journalctl -u $SERVICE_NAME -n 50"
    exit 1
fi

# 健康检查
echo ""
echo "🏥 健康检查..."

for i in {1..10}; do
    if curl -fsS http://localhost:8781/healthz >/dev/null 2>&1; then
        echo "   ✅ 健康检查通过"
        break
    fi
    
    if [[ $i -eq 10 ]]; then
        echo "   ❌ 健康检查失败"
        echo "   查看日志: journalctl -u $SERVICE_NAME -n 50"
        exit 1
    fi
    
    sleep 2
done

# ============================================================================
# 安装完成
# ============================================================================

echo ""
echo "========================================="
echo "✅ 安装完成!"
echo "========================================="
echo ""
echo "服务状态:"
echo "  systemctl status $SERVICE_NAME"
echo ""
echo "查看日志:"
echo "  journalctl -u $SERVICE_NAME -f"
echo ""
echo "访问地址:"
echo "  http://localhost:8781"
echo ""
echo "配置文件:"
echo "  $INSTALL_DIR/config/config.yaml"
echo ""
echo "下一步:"
echo "  1. 访问 Web 界面进行激活"
echo "  2. 创建第一个租户"
echo "  3. 配置 Provider 凭据"
echo ""
echo "文档: https://docs.kxpms.cn/llm-gateway-go"
echo ""

