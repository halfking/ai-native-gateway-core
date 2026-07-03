#!/bin/bash
# pg-columnar-rotate.sh
# 自动扫描并转换历史分区表为 columnar 存储
# 适用于 llm-gateway PostgreSQL Citus 11.3
# 部署: K8s CronJob 每月 1 号 02:00 执行

set -euo pipefail

# ============ 配置 ============
NAMESPACE="${NAMESPACE:-pms-test}"
POD_SELECTOR="${POD_SELECTOR:-app=llm-gateway-pg}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"
AGE_DAYS="${AGE_DAYS:-30}"
BATCH_SIZE="${BATCH_SIZE:-150}"
DRY_RUN="${DRY_RUN:-false}"

# 飞书 webhook (可选)
FEISHU_WEBHOOK="${FEISHU_WEBHOOK:-}"

# ============ 函数 ============
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

get_pod() {
    kubectl get pod -n "$NAMESPACE" -l "$POD_SELECTOR" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo ""
}

psql_exec() {
    local sql="$1"
    kubectl exec -i -n "$NAMESPACE" "$POD" -- psql -U "$DB_USER" -d "$DB_NAME" -tA -c "$sql" 2>&1
}

notify_feishu() {
    local title="$1"
    local content="$2"
    if [ -n "$FEISHU_WEBHOOK" ]; then
        curl -s -X POST "$FEISHU_WEBHOOK" \
            -H 'Content-Type: application/json' \
            -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"$title\n\n$content\"}}" >/dev/null
    fi
}

migrate_partition() {
    local parent="$1"
    local partition="$2"
    local partition_bounds="$3"
    
    log "开始迁移: $partition (parent: $parent)"
    
    # 1. Detach
    psql_exec "ALTER TABLE public.$parent DETACH PARTITION public.$partition;"
    
    # 2. 创建列存副本
    local col_table="${partition}_col_tmp"
    psql_exec "CREATE TABLE public.$col_table (LIKE public.$partition INCLUDING ALL EXCLUDING INDEXES) USING columnar;"
    
    # 3. 获取 id 范围
    local id_range
    id_range=$(psql_exec "SELECT min(id), max(id) FROM public.$partition WHERE id IS NOT NULL;")
    local min_id=$(echo "$id_range" | cut -d'|' -f1)
    local max_id=$(echo "$id_range" | cut -d'|' -f2)
    
    if [ -z "$min_id" ] || [ "$min_id" = "" ]; then
        log "  表为空或无 id 列，使用全量复制"
        psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition;"
    else
        log "  ID 范围: $min_id - $max_id"
        # 4. 分批迁移
        local cur=$min_id
        local total=0
        while [ "$cur" -le "$max_id" ]; do
            local end=$((cur + BATCH_SIZE - 1))
            [ "$end" -gt "$max_id" ] && end=$max_id
            
            psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition WHERE id BETWEEN $cur AND $end;" >/dev/null 2>&1
            
            local batch_count
            batch_count=$(psql_exec "SELECT count(*) FROM public.$col_table WHERE id BETWEEN $cur AND $end;")
            total=$((total + batch_count))
            
            if [ $((total % 1000)) -lt "$BATCH_SIZE" ]; then
                log "  进度: $total 行"
            fi
            
            cur=$((end + 1))
        done
        log "  完成: $total 行"
    fi
    
    # 5. 验证
    local orig_count
    local col_count
    orig_count=$(psql_exec "SELECT count(*) FROM public.$partition;")
    col_count=$(psql_exec "SELECT count(*) FROM public.$col_table;")
    
    if [ "$orig_count" != "$col_count" ]; then
        log "  ❌ 验证失败: 原表 $orig_count 行 != 列存 $col_count 行"
        psql_exec "DROP TABLE public.$col_table;"
        psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$partition $partition_bounds;"
        return 1
    fi
    
    # 6. ATTACH columnar 表
    psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$col_table $partition_bounds;"
    
    # 7. Rename
    local backup_table="${partition}_heap_backup"
    psql_exec "ALTER TABLE public.$partition RENAME TO $backup_table;"
    psql_exec "ALTER TABLE public.$col_table RENAME TO $partition;"
    
    # 8. 获取压缩前后大小
    local old_size
    local new_size
    old_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$backup_table'::regclass));")
    new_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$partition'::regclass));")
    
    log "  ✅ 迁移成功: $old_size -> $new_size"
    
    # 9. DROP 旧表
    psql_exec "DROP TABLE public.$backup_table CASCADE;"
    
    echo "$partition|$old_size|$new_size"
}

# ============ 主逻辑 ============
main() {
    log "========== PG Columnar Rotate 开始 =========="
    log "配置: NAMESPACE=$NAMESPACE, AGE_DAYS=$AGE_DAYS, DRY_RUN=$DRY_RUN"
    
    POD=$(get_pod)
    if [ -z "$POD" ]; then
        log "❌ 找不到 Pod (selector: $POD_SELECTOR)"
        exit 1
    fi
    log "目标 Pod: $POD"
    
    # 扫描需要转换的分区
    log "扫描 heap 分区..."
    local heap_partitions
    heap_partitions=$(psql_exec "
        SELECT 
            parent.relname || '|' || child.relname || '|' || 
            pg_size_pretty(pg_total_relation_size(child.oid)) || '|' ||
            (SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c WHERE c.oid = child.oid)
        FROM pg_inherits i
        JOIN pg_class parent ON parent.oid = i.inhparent
        JOIN pg_class child ON child.oid = i.inhrelid
        JOIN pg_am am ON child.relam = am.oid
        WHERE parent.relname IN ('request_logs', 'credential_model_index', 'usage_ledger', 'request_wal')
          AND child.relname ~ '_2026_(0[1-9]|1[0-2])$'
          AND am.amname = 'heap'
        ORDER BY parent.relname, child.relname;
    ")
    
    if [ -z "$heap_partitions" ]; then
        log "✅ 没有需要转换的 heap 分区"
        notify_feishu "PG Columnar Rotate" "✅ 没有需要转换的分区 ($(date '+%Y-%m-%d %H:%M:%S'))"
        exit 0
    fi
    
    log "发现 $(echo "$heap_partitions" | wc -l) 个 heap 分区:"
    echo "$heap_partitions"
    
    if [ "$DRY_RUN" = "true" ]; then
        log "DRY_RUN 模式，跳过实际转换"
        notify_feishu "PG Columnar Rotate (DRY RUN)" "发现以下分区需转换:\n$heap_partitions"
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
    
    # VACUUM ANALYZE 所有已转换的表
    log "执行 VACUUM ANALYZE..."
    psql_exec "VACUUM ANALYZE;" >/dev/null 2>&1
    
    # 获取最终数据库大小
    local db_size
    db_size=$(psql_exec "SELECT pg_size_pretty(pg_database_size('$DB_NAME'));")
    
    log "========== PG Columnar Rotate 完成 =========="
    log "数据库总大小: $db_size"
    
    notify_feishu "PG Columnar Rotate 完成" "数据库: $db_size\n成功:$success_list\n失败:$failed_list"
}

main "$@"
