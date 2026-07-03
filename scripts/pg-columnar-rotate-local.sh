#!/bin/bash
# pg-columnar-rotate-local.sh
# 本地 Docker 环境版本：通过 docker exec 访问 r112_postgres

set -euo pipefail

# ============ 配置 ============
CONTAINER_NAME="${CONTAINER_NAME:-r112_postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-kxuser}"
AGE_DAYS="${AGE_DAYS:-30}"
BATCH_SIZE="${BATCH_SIZE:-150}"
DRY_RUN="${DRY_RUN:-false}"

# ============ 函数 ============
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

psql_exec() {
    local sql="$1"
    docker exec -i "$CONTAINER_NAME" psql -U "$DB_USER" -d "$DB_NAME" -tA -c "$sql" 2>&1
}

migrate_partition() {
    local parent="$1"
    local partition="$2"
    local partition_bounds="$3"
    
    log "开始迁移: $partition (parent: $parent)"
    
    # 1. Detach
    if [ -n "$partition_bounds" ]; then
        psql_exec "ALTER TABLE public.$parent DETACH PARTITION public.$partition;" || true
    fi
    
    # 2. 创建列存副本
    local col_table="${partition}_col_tmp"
    psql_exec "CREATE TABLE public.$col_table (LIKE public.$partition INCLUDING ALL EXCLUDING INDEXES) USING columnar;"
    
    # 3. 复制数据
    psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition;"
    
    # 4. 验证
    local orig_count
    local col_count
    orig_count=$(psql_exec "SELECT count(*) FROM public.$partition;")
    col_count=$(psql_exec "SELECT count(*) FROM public.$col_table;")
    
    if [ "$orig_count" != "$col_count" ]; then
        log "  ❌ 验证失败: 原表 $orig_count 行 != 列存 $col_count 行"
        psql_exec "DROP TABLE public.$col_table;"
        if [ -n "$partition_bounds" ]; then
            psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$partition $partition_bounds;" || true
        fi
        return 1
    fi
    
    # 5. ATTACH
    if [ -n "$partition_bounds" ]; then
        psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$col_table $partition_bounds;"
    fi
    
    # 6. Rename
    local backup_table="${partition}_heap_backup"
    psql_exec "ALTER TABLE public.$partition RENAME TO $backup_table;"
    psql_exec "ALTER TABLE public.$col_table RENAME TO $partition;"
    
    # 7. 获取大小
    local old_size
    local new_size
    old_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$backup_table'::regclass));")
    new_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$partition'::regclass));")
    
    log "  ✅ 迁移成功: $old_size -> $new_size"
    
    # 8. DROP
    psql_exec "DROP TABLE public.$backup_table CASCADE;"
    
    echo "$partition|$old_size|$new_size"
}

# ============ 主逻辑 ============
main() {
    log "========== PG Columnar Rotate (Local) 开始 =========="
    log "配置: CONTAINER=$CONTAINER_NAME, AGE_DAYS=$AGE_DAYS, DRY_RUN=$DRY_RUN"
    
    # 检查容器
    if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
        log "❌ 容器 $CONTAINER_NAME 未运行"
        exit 1
    fi
    
    # 扫描 heap 分区
    log "扫描 heap 分区..."
    local heap_partitions
    heap_partitions=$(psql_exec "
        SELECT 
            COALESCE(parent.relname, '') || '|' || child.relname || '|' || 
            pg_size_pretty(pg_total_relation_size(child.oid)) || '|' ||
            COALESCE((SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c WHERE c.oid = child.oid), '')
        FROM pg_class child
        LEFT JOIN pg_inherits i ON child.oid = i.inhrelid
        LEFT JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_am am ON child.relam = am.oid
        WHERE child.relname ~ '^(request_logs|credential_model_index|usage_ledger|request_wal)_2026_(0[1-9]|1[0-2])$'
          AND child.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
          AND am.amname = 'heap'
        ORDER BY child.relname;
    ")
    
    if [ -z "$heap_partitions" ]; then
        log "✅ 没有需要转换的 heap 分区"
        exit 0
    fi
    
    log "发现 $(echo "$heap_partitions" | wc -l) 个 heap 分区:"
    echo "$heap_partitions"
    
    if [ "$DRY_RUN" = "true" ]; then
        log "DRY_RUN 模式，跳过实际转换"
        exit 0
    fi
    
    # 逐个转换
    local success_list=""
    local failed_list=""
    
    while IFS='|' read -r parent partition size bounds; do
        if migrate_partition "$parent" "$partition" "$bounds"; then
            success_list="$success_list\n✅ $partition: $size"
        else
            failed_list="$failed_list\n❌ $partition: 迁移失败"
        fi
    done <<< "$heap_partitions"
    
    # VACUUM ANALYZE
    log "执行 VACUUM ANALYZE..."
    psql_exec "VACUUM ANALYZE;" >/dev/null 2>&1 || true
    
    # 获取最终数据库大小
    local db_size
    db_size=$(psql_exec "SELECT pg_size_pretty(pg_database_size('$DB_NAME'));")
    
    log "========== PG Columnar Rotate (Local) 完成 =========="
    log "数据库总大小: $db_size"
    log "成功:$success_list"
    [ -n "$failed_list" ] && log "失败:$failed_list"
}

main "$@"
