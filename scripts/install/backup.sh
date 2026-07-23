#!/usr/bin/env bash
# scripts/install/backup.sh - 备份工具
# 用法：sudo ./backup.sh [backup_name]

set -euo pipefail

INSTALL_DIR="/opt/llm-gateway-go"
DATA_DIR="/var/lib/llm-gateway-go"
CONFIG_DIR="/etc/llm-gateway-go"
BACKUP_ROOT="/var/backups/llm-gateway-go"

BACKUP_NAME="${1:-backup-$(date +%Y%m%d-%H%M%S)}"
BACKUP_PATH="${BACKUP_ROOT}/${BACKUP_NAME}"

if [[ $EUID -ne 0 ]]; then
    echo "❌ 需要 root 权限"
    exit 1
fi

echo "========================================="
echo "LLM Gateway Go 备份工具"
echo "========================================="
echo "备份名称: $BACKUP_NAME"
echo "备份路径: $BACKUP_PATH"
echo ""

mkdir -p "$BACKUP_PATH"

# 备份配置
echo "📦 备份配置..."
if [[ -f "${INSTALL_DIR}/config/config.yaml" ]]; then
    cp -p "${INSTALL_DIR}/config/config.yaml" "$BACKUP_PATH/"
    echo "   ✅ config.yaml"
fi

# 备份二进制版本
if [[ -f "${INSTALL_DIR}/VERSION" ]]; then
    cp -p "${INSTALL_DIR}/VERSION" "$BACKUP_PATH/"
    echo "   ✅ VERSION"
fi

# 备份数据库
echo ""
echo "📦 备份数据库..."
if command -v pg_dump &>/dev/null && [[ -f "${INSTALL_DIR}/config/config.yaml" ]]; then
    DB_PASS=$(grep "password:" "${INSTALL_DIR}/config/config.yaml" | awk '{print $2}' || echo "")
    if [[ -n "$DB_PASS" ]]; then
        DB_NAME=$(grep "database:" "${INSTALL_DIR}/config/config.yaml" | awk '{print $2}' || echo "llm_gateway")
        if PGPASSWORD="$DB_PASS" pg_dump -h localhost -U postgres "$DB_NAME" > "$BACKUP_PATH/database.sql" 2>/dev/null; then
            SIZE=$(du -h "$BACKUP_PATH/database.sql" | cut -f1)
            echo "   ✅ database.sql ($SIZE)"
        fi
    fi
fi

# 备份数据目录
echo ""
echo "📦 备份数据..."
if [[ -d "$DATA_DIR" ]]; then
    tar czf "$BACKUP_PATH/data.tar.gz" -C "$(dirname $DATA_DIR)" "$(basename $DATA_DIR)" 2>/dev/null || true
    if [[ -f "$BACKUP_PATH/data.tar.gz" ]]; then
        SIZE=$(du -h "$BACKUP_PATH/data.tar.gz" | cut -f1)
        echo "   ✅ data.tar.gz ($SIZE)"
    fi
fi

# 压缩备份
echo ""
echo "🗜️  压缩备份..."
cd "$BACKUP_ROOT"
tar czf "${BACKUP_NAME}.tar.gz" "$BACKUP_NAME"
rm -rf "$BACKUP_PATH"

SIZE=$(du -h "${BACKUP_NAME}.tar.gz" | cut -f1)
echo "   ✅ ${BACKUP_NAME}.tar.gz ($SIZE)"

echo ""
echo "========================================="
echo "✅ 备份完成"
echo "========================================="
echo "文件: ${BACKUP_ROOT}/${BACKUP_NAME}.tar.gz"
echo ""
echo "恢复方法: tar xzf ${BACKUP_NAME}.tar.gz -C /tmp/ && 手动恢复各文件"

