#!/bin/bash
# Quick Start Guide - Credential Monitor Heatmap
# This script will help you get started quickly

set -e

echo "=========================================="
echo "凭据监控热力图 - 快速启动指南"
echo "=========================================="
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Step 1: Check prerequisites
echo "步骤 1/5: 检查前置条件"
echo "-------------------"

# Check if in correct directory
if [ ! -f "go.mod" ]; then
  echo -e "${RED}✗ 错误: 请在项目根目录运行此脚本${NC}"
  exit 1
fi
echo -e "${GREEN}✓ 当前目录正确${NC}"

# Check Go
if ! command -v go &> /dev/null; then
  echo -e "${RED}✗ 错误: 未安装 Go${NC}"
  exit 1
fi
echo -e "${GREEN}✓ Go 已安装: $(go version)${NC}"

# Check pnpm
if ! command -v pnpm &> /dev/null; then
  echo -e "${YELLOW}⚠ 警告: 未安装 pnpm，将尝试使用 npm${NC}"
else
  echo -e "${GREEN}✓ pnpm 已安装: $(pnpm -v)${NC}"
fi

# Check psql
if ! command -v psql &> /dev/null; then
  echo -e "${YELLOW}⚠ 警告: 未安装 psql，需要手动执行数据库迁移${NC}"
else
  echo -e "${GREEN}✓ psql 已安装${NC}"
fi

echo ""

# Step 2: Database migration
echo "步骤 2/5: 数据库迁移"
echo "-------------------"
echo "需要创建 3 个索引来优化查询性能"
echo ""
read -p "是否现在执行数据库迁移? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  read -p "数据库地址 (默认: localhost): " DB_HOST
  DB_HOST=${DB_HOST:-localhost}
  read -p "数据库名称 (默认: llm_gateway): " DB_NAME
  DB_NAME=${DB_NAME:-llm_gateway}
  read -p "数据库用户 (默认: postgres): " DB_USER
  DB_USER=${DB_USER:-postgres}
  
  echo "执行迁移..."
  psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -f migrations/035_add_heatmap_indexes.sql
  
  if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 数据库迁移成功${NC}"
  else
    echo -e "${RED}✗ 数据库迁移失败，请检查连接信息${NC}"
    exit 1
  fi
else
  echo -e "${YELLOW}⚠ 跳过数据库迁移。请稍后手动执行:${NC}"
  echo "  psql -h <host> -U <user> -d llm_gateway -f migrations/035_add_heatmap_indexes.sql"
fi

echo ""

# Step 3: Backend build
echo "步骤 3/5: 构建后端"
echo "-------------------"
read -p "是否现在构建后端? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  echo "构建中..."
  make build
  
  if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 后端构建成功${NC}"
  else
    echo -e "${RED}✗ 后端构建失败${NC}"
    exit 1
  fi
else
  echo -e "${YELLOW}⚠ 跳过后端构建${NC}"
fi

echo ""

# Step 4: Frontend setup
echo "步骤 4/5: 前端设置"
echo "-------------------"
read -p "是否现在安装前端依赖? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  cd web
  
  if command -v pnpm &> /dev/null; then
    echo "使用 pnpm 安装依赖..."
    pnpm install
  else
    echo "使用 npm 安装依赖..."
    npm install
  fi
  
  if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 前端依赖安装成功${NC}"
  else
    echo -e "${RED}✗ 前端依赖安装失败${NC}"
    exit 1
  fi
  
  cd ..
else
  echo -e "${YELLOW}⚠ 跳过前端依赖安装${NC}"
fi

echo ""

# Step 5: Instructions
echo "步骤 5/5: 启动服务"
echo "-------------------"
echo ""
echo "后端服务:"
echo "  ./llm-gateway-go"
echo ""
echo "前端服务 (开发模式):"
echo "  cd web && pnpm dev"
echo ""
echo "前端服务 (生产构建):"
echo "  cd web && pnpm build"
echo ""
echo "测试 API:"
echo "  ./test-heatmap-api.sh"
echo ""
echo "访问页面:"
echo "  http://localhost:5173/routing-v2/credentials"
echo ""
echo -e "${GREEN}=========================================="
echo "快速启动指南完成!"
echo "==========================================${NC}"
echo ""
echo "接下来的步骤:"
echo "1. 启动后端服务: ./llm-gateway-go"
echo "2. 在新终端启动前端: cd web && pnpm dev"
echo "3. 打开浏览器访问: http://localhost:5173/routing-v2/credentials"
echo "4. 点击 '热力图' Tab 查看新功能"
echo ""
echo "文档链接:"
echo "- 需求规格: docs/credential-monitor-heatmap-requirements.md"
echo "- 实施指南: docs/credential-monitor-heatmap-implementation.md"
echo "- 部署指南: DEPLOYMENT_GUIDE.md"
echo "- 项目总结: PROJECT_SUMMARY.md"
echo ""
echo "遇到问题? 查看 DEPLOYMENT_GUIDE.md 的故障排查章节"
