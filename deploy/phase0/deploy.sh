#!/bin/bash
# ============================================================================
# File: deploy/phase0/deploy.sh
# Purpose: Deploy Phase 0 optimization config to target environment
# Usage: bash deploy/phase0/deploy.sh --target=<local|kaixuan-1|71|184>
# ============================================================================

set -euo pipefail

TARGET=""
DRY_RUN=false

# Parse arguments
for arg in "$@"; do
  case "$arg" in
    --target=*)
      TARGET="${arg#--target=}"
      ;;
    --dry-run)
      DRY_RUN=true
      ;;
  esac
done

if [ -z "$TARGET" ]; then
    echo "❌ 缺少 --target 参数"
    echo "用法: bash deploy/phase0/deploy.sh --target=<local|kaixuan-1|71|184>"
    exit 1
fi

echo "========================================"
echo "  Phase 0 配置部署"
echo "========================================"
echo "目标环境: $TARGET"
echo "Dry-run: $DRY_RUN"
echo ""

# ============================================================================
# Backup current config
# ============================================================================
echo "=== Step 1: 备份当前配置 ==="
BACKUP_DIR="/opt/llm-gateway/backups/pre-phase0-$(date +%Y%m%d-%H%M%S)"

case "$TARGET" in
  local)
    echo "本地环境，跳过备份"
    ;;
  kaixuan-1)
    ssh root@192.168.31.28 "mkdir -p $BACKUP_DIR && \
      cp /etc/systemd/system/llm-gateway.service.d/override.conf $BACKUP_DIR/ 2>/dev/null || true"
    echo "✅ 已备份到 kaixuan-1:$BACKUP_DIR"
    ;;
  71)
    ssh root@192.168.1.71 "mkdir -p $BACKUP_DIR && \
      cp /etc/systemd/system/llm-gateway.service.d/override.conf $BACKUP_DIR/ 2>/dev/null || true"
    echo "✅ 已备份到 71:$BACKUP_DIR"
    ;;
  184)
    ssh root@14.103.112.184 "mkdir -p $BACKUP_DIR && \
      cp /etc/systemd/system/llm-gateway.service.d/override.conf $BACKUP_DIR/ 2>/dev/null || true"
    echo "✅ 已备份到 184:$BACKUP_DIR"
    ;;
esac

# ============================================================================
# Deploy config
# ============================================================================
echo ""
echo "=== Step 2: 部署配置文件 ==="

case "$TARGET" in
  local)
    echo "本地环境部署："
    echo "  export \$(cat deploy/phase0/optimization.env | grep -v '^#' | xargs)"
    echo ""
    echo "然后运行："
    echo "  go run cmd/gateway/main.go"
    ;;
  
  kaixuan-1|71|184)
    # Determine SSH target
    case "$TARGET" in
      kaixuan-1) SSH_TARGET="root@192.168.31.28" ;;
      71) SSH_TARGET="root@192.168.1.71" ;;
      184) SSH_TARGET="root@14.103.112.184" ;;
    esac
    
    # Upload config file
    scp deploy/phase0/optimization.env $SSH_TARGET:/opt/llm-gateway/config/
    echo "✅ 配置文件已上传"
    
    # Create systemd override
    ssh $SSH_TARGET "mkdir -p /etc/systemd/system/llm-gateway.service.d && \
      cat > /etc/systemd/system/llm-gateway.service.d/phase0-optimization.conf <<EOC
[Service]
EnvironmentFile=/opt/llm-gateway/config/optimization.env
EOC"
    echo "✅ Systemd override 已创建"
    
    # Reload and restart
    ssh $SSH_TARGET "systemctl daemon-reload && systemctl restart llm-gateway"
    echo "✅ 服务已重启"
    
    # Wait for startup
    echo "等待服务启动..."
    sleep 10
    
    # Health check
    for i in {1..5}; do
      if ssh $SSH_TARGET "curl -fsS http://localhost:8781/healthz" > /dev/null 2>&1; then
        echo "✅ 健康检查通过"
        break
      fi
      echo "尝试 $i/5..."
      sleep 2
    done
    ;;
esac

# ============================================================================
# Verification
# ============================================================================
echo ""
echo "=== Step 3: 验证配置生效 ==="

case "$TARGET" in
  local)
    echo "验证命令（部署后运行）："
    echo "  curl http://localhost:8781/metrics | grep -E 'pool|http2'"
    ;;
  
  kaixuan-1|71|184)
    case "$TARGET" in
      kaixuan-1) SSH_TARGET="root@192.168.31.28" ;;
      71) SSH_TARGET="root@192.168.1.71" ;;
      184) SSH_TARGET="root@14.103.112.184" ;;
    esac
    
    echo "检查配置项..."
    ssh $SSH_TARGET "systemctl show llm-gateway | grep -i environment" | head -3
    
    echo ""
    echo "检查运行状态..."
    ssh $SSH_TARGET "systemctl status llm-gateway --no-pager | head -10"
    ;;
esac

echo ""
echo "========================================"
echo "  部署完成"
echo "========================================"
echo "环境: $TARGET"
echo "备份: $BACKUP_DIR"
echo ""
echo "监控命令:"
echo "  watch -n 5 'curl -s http://localhost:8781/metrics | grep -E \"ttfb|pool\"'"
echo ""
echo "回滚命令:"
echo "  bash scripts/rollback-optimization.sh"
echo ""
