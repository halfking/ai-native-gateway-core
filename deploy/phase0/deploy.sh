#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# ============================================================================
# File: deploy/phase0/deploy.sh
# Purpose: Deploy Phase 0 optimization config to target environment
# Usage: bash deploy/phase0/deploy.sh --target=<local|kaixuan-1>
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
    echo "用法: bash deploy/phase0/deploy.sh --target=<local|kaixuan-1>"
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
  
  kaixuan-1)
    SSH_TARGET="root@192.168.31.28"
    
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
  
  kaixuan-1)
    SSH_TARGET="root@192.168.31.28"
    
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
