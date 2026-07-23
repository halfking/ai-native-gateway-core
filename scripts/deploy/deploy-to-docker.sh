#!/usr/bin/env bash
# scripts/deploy/deploy-to-docker.sh - 部署到 Docker 环境
# 用法：bash deploy-to-docker.sh <docker_archive_file>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================================
# 配置
# ============================================================================

DOCKER_ARCHIVE="${1:?Usage: $0 <docker_archive_file>}"

if [[ ! -f "$DOCKER_ARCHIVE" ]]; then
    echo "❌ 文件不存在: $DOCKER_ARCHIVE"
    exit 1
fi

WORK_DIR=$(mktemp -d)
trap "rm -rf $WORK_DIR" EXIT

echo "========================================="
echo "部署到 Docker"
echo "========================================="
echo ""
echo "归档文件: $DOCKER_ARCHIVE"
echo "工作目录: $WORK_DIR"
echo ""

# ============================================================================
# 步骤 1: 解压归档
# ============================================================================

echo "📦 解压归档文件..."
tar xzf "$DOCKER_ARCHIVE" -C "$WORK_DIR"

EXTRACTED_DIR=$(ls "$WORK_DIR" | head -1)
cd "${WORK_DIR}/${EXTRACTED_DIR}"

echo "   ✅ 已解压到: ${WORK_DIR}/${EXTRACTED_DIR}"

# ============================================================================
# 步骤 2: 检查 Docker 环境
# ============================================================================

echo ""
echo "🐳 检查 Docker 环境..."

if ! command -v docker &>/dev/null; then
    echo "   ❌ Docker 未安装"
    exit 1
fi

if ! docker info &>/dev/null; then
    echo "   ❌ Docker daemon 未运行"
    exit 1
fi

echo "   ✅ Docker 可用"

if ! command -v docker-compose &>/dev/null && ! docker compose version &>/dev/null; then
    echo "   ❌ Docker Compose 未安装"
    exit 1
fi

echo "   ✅ Docker Compose 可用"

# ============================================================================
# 步骤 3: 加载镜像
# ============================================================================

echo ""
echo "📥 加载 Docker 镜像..."

if [[ -f scripts/load-images.sh ]]; then
    bash scripts/load-images.sh
else
    echo "   手动加载镜像..."
    for img in images/*.tar.gz; do
        if [[ -f "$img" ]]; then
            echo "   加载: $(basename "$img")"
            docker load -i "$img"
        fi
    done
fi

echo "   ✅ 镜像加载完成"

# ============================================================================
# 步骤 4: 配置环境变量
# ============================================================================

echo ""
echo "⚙️  配置环境变量..."

if [[ ! -f .env ]]; then
    if [[ -f .env.example ]]; then
        cp .env.example .env
        echo "   ✅ 已创建 .env 文件"
        
        # 生成随机密码
        DB_PASSWORD=$(openssl rand -hex 16)
        sed -i.bak "s/DB_PASSWORD=.*/DB_PASSWORD=$DB_PASSWORD/" .env
        rm .env.bak
        
        echo "   ✅ 已生成随机数据库密码"
    else
        echo "   ❌ .env.example 不存在"
        exit 1
    fi
else
    echo "   ℹ️  .env 文件已存在，跳过"
fi

# ============================================================================
# 步骤 5: 停止旧容器
# ============================================================================

echo ""
echo "🛑 停止旧容器..."

if docker-compose ps &>/dev/null 2>&1 || docker compose ps &>/dev/null 2>&1; then
    echo "   停止现有容器..."
    docker-compose down 2>/dev/null || docker compose down 2>/dev/null || true
    echo "   ✅ 旧容器已停止"
else
    echo "   ℹ️  没有运行中的容器"
fi

# ============================================================================
# 步骤 6: 启动新容器
# ============================================================================

echo ""
echo "🚀 启动新容器..."

# 使用 docker-compose 或 docker compose
if command -v docker-compose &>/dev/null; then
    docker-compose up -d
else
    docker compose up -d
fi

echo "   ✅ 容器已启动"

# ============================================================================
# 步骤 7: 等待服务就绪
# ============================================================================

echo ""
echo "⏳ 等待服务就绪..."

sleep 10

# 检查容器状态
echo ""
echo "容器状态:"
if command -v docker-compose &>/dev/null; then
    docker-compose ps
else
    docker compose ps
fi

# ============================================================================
# 步骤 8: 健康检查
# ============================================================================

echo ""
echo "🏥 健康检查..."

# 获取网关容器的端口
GATEWAY_PORT=$(docker-compose ps 2>/dev/null | grep llm-gateway | awk '{print $NF}' | cut -d: -f1)
if [[ -z "$GATEWAY_PORT" ]]; then
    GATEWAY_PORT=8781
fi

bash "${SCRIPT_DIR}/health-check.sh" "http://localhost:${GATEWAY_PORT}"

if [[ $? -eq 0 ]]; then
    echo "   ✅ 健康检查通过"
else
    echo "   ❌ 健康检查失败"
    echo ""
    echo "查看日志:"
    if command -v docker-compose &>/dev/null; then
        docker-compose logs --tail=50
    else
        docker compose logs --tail=50
    fi
    exit 1
fi

# ============================================================================
# 步骤 9: 显示访问信息
# ============================================================================

echo ""
echo "========================================="
echo "✅ 部署完成"
echo "========================================="
echo ""
echo "访问地址:"
echo "  http://localhost:${GATEWAY_PORT}"
echo ""
echo "管理命令:"
echo "  查看日志:   docker-compose logs -f"
echo "  查看状态:   docker-compose ps"
echo "  停止服务:   docker-compose down"
echo "  重启服务:   docker-compose restart"
echo ""
echo "配置文件:"
echo "  ${WORK_DIR}/${EXTRACTED_DIR}/.env"
echo ""

