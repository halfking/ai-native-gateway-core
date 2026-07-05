#!/usr/bin/env bash
# sync-objects.sh
# 从 sql/objects/ 同步数据库对象到 deploy/sql/objects/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SOURCE_DIR="$PROJECT_ROOT/sql/objects"
TARGET_DIR="$SCRIPT_DIR/objects"

echo "========================================"
echo "同步数据库对象到 deploy/sql/objects/"
echo "========================================"
echo "源目录: $SOURCE_DIR"
echo "目标目录: $TARGET_DIR"
echo ""

# 检查源目录
if [ ! -d "$SOURCE_DIR" ]; then
    echo "错误: 源目录不存在: $SOURCE_DIR"
    exit 1
fi

# 创建目标目录结构
echo "[1/3] 创建目录结构..."
mkdir -p "$TARGET_DIR"/{tables,views,functions,sequences,triggers,indexes,constraints,policies}

# 统计变量
TOTAL_FILES=0
COPIED_FILES=0

# 同步函数
sync_objects() {
    local object_type="$1"
    local source_path="$SOURCE_DIR/$object_type"
    local target_path="$TARGET_DIR/$object_type"
    
    if [ ! -d "$source_path" ]; then
        echo "  ⚠ 跳过 $object_type (源目录不存在)"
        return
    fi
    
    local file_count=$(find "$source_path" -name "*.sql" 2>/dev/null | wc -l | xargs)
    TOTAL_FILES=$((TOTAL_FILES + file_count))
    
    if [ "$file_count" -eq 0 ]; then
        echo "  ⚠ 跳过 $object_type (无 SQL 文件)"
        return
    fi
    
    echo "  同步 $object_type/: $file_count 个文件..."
    
    # 复制所有 SQL 文件
    find "$source_path" -name "*.sql" -exec cp -v {} "$target_path/" \; 2>/dev/null | wc -l | xargs | read copied
    COPIED_FILES=$((COPIED_FILES + file_count))
}

echo "[2/3] 同步对象文件..."
echo ""

# 按顺序同步各类对象
sync_objects "tables"
sync_objects "views"
sync_objects "functions"
sync_objects "sequences"
sync_objects "triggers"
sync_objects "indexes"
sync_objects "constraints"
sync_objects "policies"

echo ""
echo "[3/3] 创建对象索引文件..."

# 为每个对象类型创建 README
for obj_type in tables views functions sequences triggers indexes constraints policies; do
    target_path="$TARGET_DIR/$obj_type"
    
    if [ ! -d "$target_path" ]; then
        continue
    fi
    
    file_count=$(find "$target_path" -name "*.sql" 2>/dev/null | wc -l | xargs)
    
    if [ "$file_count" -eq 0 ]; then
        continue
    fi
    
    cat > "$target_path/README.md" <<EOF
# ${obj_type^}

> 本目录包含 ${obj_type} 对象的 DDL 定义，从 \`sql/objects/${obj_type}/\` 同步。

## 统计

- **文件数量**: $file_count
- **同步来源**: \`sql/objects/${obj_type}/\`
- **同步方式**: 通过 \`sync-objects.sh\` 自动同步

## 文件列表

\`\`\`
EOF
    
    # 列出所有文件
    find "$target_path" -name "*.sql" -exec basename {} \; | sort >> "$target_path/README.md"
    
    cat >> "$target_path/README.md" <<EOF
\`\`\`

## 使用说明

### 查看对象定义

\`\`\`bash
# 查看某个对象的定义
cat deploy/sql/objects/${obj_type}/<object_name>.sql
\`\`\`

### 应用对象

\`\`\`bash
# 应用单个对象
psql "\$DATABASE_URL" -f deploy/sql/objects/${obj_type}/<object_name>.sql

# 应用所有对象（按字母顺序）
for f in deploy/sql/objects/${obj_type}/*.sql; do
  psql "\$DATABASE_URL" -f "\$f"
done
\`\`\`

## 维护说明

- **不要手动编辑本目录**：所有更改应在 \`sql/objects/${obj_type}/\` 进行
- **同步方式**：运行 \`bash deploy/sql/sync-objects.sh\` 重新同步
- **验证方式**：运行 \`bash deploy/sql/verify-migration.sh\` 验证完整性

## 相关文档

- [sql/objects/README.md](../../../sql/README.md) - 源对象定义
- [deploy/sql/README.md](../README.md) - 部署 SQL 资产说明
EOF
    
done

echo ""
echo "========================================"
echo "同步完成"
echo "========================================"
echo ""
echo "对象统计:"
for obj_type in tables views functions sequences triggers indexes constraints policies; do
    target_path="$TARGET_DIR/$obj_type"
    if [ -d "$target_path" ]; then
        count=$(find "$target_path" -name "*.sql" 2>/dev/null | wc -l | xargs)
        printf "  %-15s: %4d 个文件\n" "$obj_type" "$count"
    fi
done

echo ""
echo "总计: $COPIED_FILES 个文件"
echo ""
echo "✓ 同步完成！"
echo ""
echo "下一步："
echo "1. 运行 'bash deploy/sql/verify-migration.sh' 验证"
echo "2. 查看 deploy/sql/objects/ 目录结构"
echo "3. 提交到 Git 仓库"
