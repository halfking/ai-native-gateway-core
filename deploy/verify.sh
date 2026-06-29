#!/usr/bin/env bash
# =====================================================================
# verify.sh — llm-gateway-go 部署后验证（rule 22 §7）
#
# 用法: ./deploy/verify.sh [--env local|prod] [--tag <tag>]
# 返回: 0 = 通过, 1 = 失败
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="llm-gateway-go"
CONTAINER_NAME="kx-${SERVICE_NAME}"

# ── 解析参数 ───────────────────────────────────────────────────────
ENV="local"
TAG="latest"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    --tag) TAG="$2"; shift 2 ;;
    --help) echo "用法: $0 [--env local|prod] [--tag <tag>]"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

PASS=0; FAIL=0

check() {
  if "$@"; then
    echo "  ✅ $1"; ((PASS++))
  else
    echo "  ❌ $1"; ((FAIL++))
  fi
}

echo "━━━ verify: ${SERVICE_NAME} (env=${ENV}, tag=${TAG}) ━━━"

# ── 1. 容器存活 ────────────────────────────────────────────────────
# check docker ps --filter "name=${CONTAINER_NAME}" --format "{{.Status}}" | grep -q "Up"

# ── 2. HTTP 健康检查 ──────────────────────────────────────────────
PORT=${PORT:-8080}
# check curl -sf "http://localhost:${PORT}/health" >/dev/null 2>&1

# ── 3. 应用层检查 ──────────────────────────────────────────────────
# 替换为实际服务的业务 check
# check curl -sf "http://localhost:${PORT}/api/v1/ping" >/dev/null 2>&1

# ── 4. 依赖检查（可选） ─────────────────────────────────────────────
# check pg_isready -h localhost -p 5432 >/dev/null 2>&1
# check redis-cli -h localhost ping >/dev/null 2>&1

echo "━━━ 验证结果 ━━━"
echo "  通过: $PASS, 失败: $FAIL"

# ── 返回 ───────────────────────────────────────────────────────────
if [[ $FAIL -gt 0 ]]; then
  echo "❌ 验证未通过请检查日志或执行 rollback"
  exit 1
fi
echo "✅ 验证通过"
