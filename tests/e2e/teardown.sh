#!/usr/bin/env bash
set -euo pipefail

# E2E 测试环境清理脚本
# 功能：停止并删除 Docker 容器、网络、卷

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> 停止并删除 E2E 测试环境..."

cd "$SCRIPT_DIR"

# 停止所有容器
docker-compose -f docker-compose.e2e.yml down -v --remove-orphans

echo "✓ E2E 测试环境已清理"
