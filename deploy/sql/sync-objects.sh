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
mkdir -p "$TARGET_DIR"/{tables,views,functions,sequences,triggers,indexes,constraints,policies,other}

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
    find "$source_path" -name "*.sql" -exec cp -f {} "$target_path/" \;
    
    # 删除目标目录中源已不存在的文件（保持与源完全一致）
    local stale_count=0
    while IFS= read -r stale; do
        local base
        base=$(basename "$stale")
        if [ ! -f "$source_path/$base" ]; then
            rm -f "$stale"
            stale_count=$((stale_count + 1))
        fi
    done < <(find "$target_path" -name "*.sql" 2>/dev/null)
    if [ "$stale_count" -gt 0 ]; then
        echo "  ✕ 清理 $object_type/: 删除 $stale_count 个源中已不存在的文件"
    fi
    
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
sync_objects "other"

echo ""
echo "[3/3] 创建对象索引文件..."

# 为每个对象类型创建 README
for obj_type in tables views functions sequences triggers indexes constraints policies other; do
    target_path="$TARGET_DIR/$obj_type"
    
    if [ ! -d "$target_path" ]; then
        continue
    fi
    
    file_count=$(find "$target_path" -name "*.sql" 2>/dev/null | wc -l | xargs)
    
    if [ "$file_count" -eq 0 ]; then
        continue
    fi

    # ★ bash 3.2 兼容（2026-10-21 修正）：`${obj_type^}` 是 **bash 4.0+** 的
    #   「首字母大写展开」，macOS 自带的是 bash 3.2 ⇒ 不支持 ⇒ 脚本在
    #   macOS 上以 `bad substitution` 直接退出（实测复现）。
    #   ★ 危害形态值得记：Linux（bash 5）上**完全正常** ⇒ CI 若跑在 Linux 上
    #   这条路径**恒绿**，缺陷只在 macOS 上现形 ⇒ 典型的「平台依赖的假绿」。
    #   ⇒ 这里用 POSIX 工具链拼首字母大写，所有 bash 版本都能跑。
    obj_title="$(printf '%s' "$obj_type" | cut -c1 | tr '[:lower:]' '[:upper:]')${obj_type:1}"

    # ★ 基线形态 fallback 体清单（带 dbinit:baseline-v1-fallback 标记的对象）。
    #
    # 这些对象是**基线时点**的形态（v1 fallback 体）：部署形态由启动序列里的
    # **切换迁移**（dbinit:baseline-v1-fallback-switch:）换上 ⇒ v1 族退役之后，
    # 单独 `psql -f` 这些文件**必然 42P01**（relation does not exist）。
    # ⇒ 它们必须从「应用对象」的清单里排除，并单列一节警示。
    #
    # ⚠ 这不是「把文件藏起来」：文件列表照旧全量列出，区别只在于
    #   **能不能照着 README 去 apply** ⇒ 照文档操作不再炸。
    baseline_list=""
    while IFS= read -r bf; do
        [ -n "$bf" ] || continue
        bname=$(basename "$bf")
        bswitch=$(grep -oE 'dbinit:baseline-v1-fallback-switch:[[:space:]]*[^[:space:]]+' "$bf" 2>/dev/null | head -1 | sed 's/^[^[:space:]]*[[:space:]]*//' || true)
        [ -n "$bswitch" ] || bswitch="（未声明切换迁移 ⇒ 不可单独应用）"
        baseline_list="${baseline_list}- \`${bname}\` —— 部署形态由迁移 \`${bswitch}\` 换上"$'\n'
    done < <(grep -lE '^[[:space:]]*--[[:space:]]*dbinit:baseline-v1-fallback([[:space:]]|$)' "$target_path"/*.sql 2>/dev/null || true)

    cat > "$target_path/README.md" <<EOF
# ${obj_title}

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

⚠ **只适用于「可应用对象」**：带 \`dbinit:baseline-v1-fallback\` 标记的对象是**基线形态**
（v1 fallback 体），v1 族退役后单独应用必然 **42P01** ⇒ 见下面「基线形态对象」一节。

\`\`\`bash
# 应用单个对象（⚠ 勿用于「基线形态对象」里的条目）
psql "\$DATABASE_URL" -f deploy/sql/objects/${obj_type}/<object_name>.sql

# 应用所有可应用对象（按字母顺序；基线形态对象自动跳过）
for f in deploy/sql/objects/${obj_type}/*.sql; do
  if grep -qE '^[[:space:]]*--[[:space:]]*dbinit:baseline-v1-fallback([[:space:]]|$)' "\$f"; then
    echo "跳过基线形态对象（勿单独应用）: \$f"
    continue
  fi
  psql "\$DATABASE_URL" -f "\$f"
done
\`\`\`
EOF

    # ★ 基线形态对象一节（只有本类型确实存在这类对象时才写）
    if [ -n "$baseline_list" ]; then
        cat >> "$target_path/README.md" <<EOF

## ⚠ 基线形态对象（勿单独应用）

以下对象是**基线时点**的形态（v1 fallback 体）：部署形态由启动序列的**切换迁移**换上
⇒ 单独 \`psql -f\` 会因 v1 族已退役而报 **42P01**（relation does not exist）。

${baseline_list}
⇒ 正确做法：让**启动序列**跑完（全新安装 / 存量升级都走这条路径），
**不要**手动 apply 这些文件。

EOF
    fi

    cat >> "$target_path/README.md" <<EOF

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
for obj_type in tables views functions sequences triggers indexes constraints policies other; do
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
