#!/usr/bin/env bash
# uninstall.sh — 卸载 llm-gateway-go（保留数据）
# 用法: ./uninstall.sh [--purge]

set -euo pipefail

PURGE=false
[[ "${1:-}" == "--purge" ]] && PURGE=true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

if [[ ! -f compose.yml ]]; then
  echo "❌ 当前目录未发现 compose.yml，请 cd 到安装目录"
  exit 1
fi

echo "═══ 卸载 llm-gateway-go ═══"
echo ""

if command -v docker >/dev/null 2>&1; then
  echo "▶ 停止 Docker 容器 ..."
  docker compose down || true
else
  echo "⚠️  docker 未安装，跳过容器停止"
fi

if [[ "${PURGE}" == "true" ]]; then
  echo ""
  echo "⚠️  --purge 模式将删除所有持久化数据（数据库、Redis、附件、日志、.env）"
  read -p "确认彻底清理? (yes/no): " -r CONFIRM
  if [[ "${CONFIRM}" != "yes" ]]; then
    echo "已取消"
    exit 1
  fi
  echo "▶ 删除数据卷 ..."
  docker compose down -v 2>/dev/null || true
  echo "▶ 删除容器外持久化目录 ..."
  rm -rf db/data redis/data attachments app/logs
  echo "▶ 删除 .env ..."
  rm -f .env
  echo ""
  echo "✅ 已彻底清理"
else
  echo ""
  echo "✅ 已停止容器（数据保留在 db/data、redis/data、attachments、app/logs）"
  echo "   如需彻底清理数据: ./uninstall.sh --purge"
fi
