#!/bin/bash
# ============================================================================
# File: scripts/rollback-optimization.sh
# Purpose: Enhanced rollback script for optimization changes
# Coverage: Code + DB + Nginx + Env (AUDIT_V2 A3 fix)
# Usage: bash scripts/rollback-optimization.sh [--dry-run]
# ============================================================================

set -euo pipefail

DRY_RUN=false
BACKUP_DIR="/opt/llm-gateway/backups"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

# Parse arguments
for arg in "$@"; do
  case "$arg" in
    --dry-run)
      DRY_RUN=true
      echo "🔍 DRY-RUN MODE: No actual changes will be made"
      ;;
  esac
done

# Helper function
run_cmd() {
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] $*"
    else
        "$@"
    fi
}

echo "========================================"
echo "  LLM Gateway Optimization Rollback"
echo "========================================"
echo "Timestamp: $TIMESTAMP"
echo "Dry-run: $DRY_RUN"
echo ""

# ============================================================================
# Step 1: Backup current state
# ============================================================================
echo "=== Step 1: 备份当前状态 ==="
run_cmd mkdir -p "$BACKUP_DIR/$TIMESTAMP"

if [ -f bin/gateway ]; then
    run_cmd cp bin/gateway "$BACKUP_DIR/$TIMESTAMP/gateway.before-rollback"
    echo "✅ 二进制已备份"
fi

if [ -f /etc/systemd/system/llm-gateway.service.d/override.conf ]; then
    run_cmd cp /etc/systemd/system/llm-gateway.service.d/override.conf \
        "$BACKUP_DIR/$TIMESTAMP/override.conf.before-rollback"
    echo "✅ Systemd override 已备份"
fi

# ============================================================================
# Step 2: Rollback code
# ============================================================================
echo ""
echo "=== Step 2: 回滚代码 ==="

# Get previous stable tag
PREV_TAG=$(git describe --tags --abbrev=0 main~1 2>/dev/null || echo "main~1")
echo "目标版本: $PREV_TAG"

run_cmd git fetch origin
run_cmd git checkout "$PREV_TAG"
run_cmd go build -o bin/gateway cmd/gateway/main.go

echo "✅ 代码已回滚到 $PREV_TAG"

# ============================================================================
# Step 3: Rollback database config
# ============================================================================
echo ""
echo "=== Step 3: 回滚数据库配置 ==="

if [ -n "${DATABASE_URL:-}" ]; then
    SQL_ROLLBACK=$(cat <<'SQL'
-- Rollback to default values
UPDATE llm_gateway_config 
SET value = '100', updated_at = CURRENT_TIMESTAMP
WHERE key = 'http2_max_concurrent_streams';

UPDATE llm_gateway_config
SET value = '16', updated_at = CURRENT_TIMESTAMP
WHERE key = 'max_idle_conns_per_host';

UPDATE llm_gateway_config
SET value = '64', updated_at = CURRENT_TIMESTAMP
WHERE key = 'max_conns_per_host';

-- Verify
SELECT key, value, updated_at FROM llm_gateway_config 
WHERE key IN ('http2_max_concurrent_streams', 'max_idle_conns_per_host', 'max_conns_per_host')
ORDER BY key;
SQL
)
    
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] psql \$DATABASE_URL <<< \"$SQL_ROLLBACK\""
    else
        echo "$SQL_ROLLBACK" | psql "$DATABASE_URL"
    fi
    
    echo "✅ 数据库配置已回滚"
else
    echo "⚠️  DATABASE_URL 未设置，跳过数据库回滚"
fi

# ============================================================================
# Step 4: Rollback environment variables
# ============================================================================
echo ""
echo "=== Step 4: 回滚环境变量 ==="

PRE_OPT_OVERRIDE="$BACKUP_DIR/pre-optimization/override.conf"
if [ -f "$PRE_OPT_OVERRIDE" ]; then
    run_cmd cp "$PRE_OPT_OVERRIDE" \
        /etc/systemd/system/llm-gateway.service.d/override.conf
    run_cmd systemctl daemon-reload
    echo "✅ 环境变量已回滚"
else
    echo "⚠️  未找到预优化备份 ($PRE_OPT_OVERRIDE)"
    echo "   手动恢复环境变量（如果需要）："
    echo "   - LLM_GATEWAY_MAX_IDLE_CONNS_PER_HOST=16"
    echo "   - LLM_GATEWAY_MAX_CONNS_PER_HOST=64"
fi

# ============================================================================
# Step 5: Restart service
# ============================================================================
echo ""
echo "=== Step 5: 重启服务 ==="

run_cmd systemctl restart llm-gateway
echo "等待服务启动..."
sleep 10

# ============================================================================
# Step 6: Health check
# ============================================================================
echo ""
echo "=== Step 6: 健康检查 ==="

HEALTH_OK=false
for i in {1..5}; do
    echo "尝试 $i/5..."
    if curl -fsS http://localhost:8781/healthz > /dev/null 2>&1; then
        HEALTH_OK=true
        break
    fi
    sleep 2
done

if [ "$HEALTH_OK" = true ]; then
    echo "✅ 健康检查通过"
    echo ""
    echo "========================================"
    echo "  回滚成功完成"
    echo "========================================"
    echo "备份位置: $BACKUP_DIR/$TIMESTAMP"
    echo "当前版本: $(git describe --tags)"
    exit 0
else
    echo "❌ 健康检查失败"
    echo ""
    echo "========================================"
    echo "  回滚失败，请人工介入"
    echo "========================================"
    echo "检查日志: journalctl -u llm-gateway -n 50"
    echo "检查进程: systemctl status llm-gateway"
    exit 1
fi
