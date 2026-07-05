#!/bin/bash
# pg-columnar-rotate.sh — 自动扫描并转换历史分区表为 columnar 存储（统一入口）
#
# 合并自: pg-columnar-rotate.sh + pg-columnar-rotate-local.sh
# 修订: 2026-07-05
#
# 模式:
#   (默认)  通过 kubectl 访问 184 K8s PG（生产环境）
#   --local 通过 docker exec 访问本地 r112_postgres
#
# 用法:
#   ./scripts/pg-columnar-rotate.sh                     # 184 K8s 环境
#   ./scripts/pg-columnar-rotate.sh --local             # 本地 Docker 环境
#   DRY_RUN=true ./scripts/pg-columnar-rotate.sh        # 预览模式
#   BATCH_SIZE=500 ./scripts/pg-columnar-rotate.sh      # 自定义批次大小

set -euo pipefail

# ============ 配置 ============
MODE="${1:-remote}"
[ "$MODE" = "--local" ] && MODE="local"

CONTAINER_NAME="${CONTAINER_NAME:-r112_postgres}"
NAMESPACE="${NAMESPACE:-pms-test}"
POD_SELECTOR="${POD_SELECTOR:-app=llm-gateway-pg}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"
AGE_DAYS="${AGE_DAYS:-30}"
BATCH_SIZE="${BATCH_SIZE:-150}"
DRY_RUN="${DRY_RUN:-false}"
FEISHU_WEBHOOK="${FEISHU_WEBHOOK:-}"

# local 模式下默认用户不同
[ "$MODE" = "local" ] && DB_USER="${DB_USER:-kxuser}"

# ============ 通用函数 ============
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

# ============ 执行引擎（策略模式）============
case "$MODE" in
  local)
    log "模式: local (docker exec $CONTAINER_NAME)"
    if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
      log "❌ 容器 $CONTAINER_NAME 未运行"; exit 1
    fi
    psql_exec() {
      docker exec -i "$CONTAINER_NAME" psql -U "$DB_USER" -d "$DB_NAME" -tA -c "$1" 2>&1
    }
    HEAP_QUERY="
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
    "
    ;;
  remote)
    log "模式: remote (kubectl exec $NAMESPACE/$POD_SELECTOR)"
    get_pod() {
      kubectl get pod -n "$NAMESPACE" -l "$POD_SELECTOR" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo ""
    }
    POD=""
    psql_exec() {
      local sql="$1"
      if [ -z "$POD" ]; then
        POD=$(get_pod)
        [ -z "$POD" ] && { log "❌ 找不到 Pod"; return 1; }
      fi
      kubectl exec -i -n "$NAMESPACE" "$POD" -- psql -U "$DB_USER" -d "$DB_NAME" -tA -c "$sql" 2>&1
    }
    HEAP_QUERY="
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
    "
    ;;
  *) log "❌ 未知模式: $MODE (支持: remote|--local)"; exit 1 ;;
esac

notify_feishu() {
  local title="$1"; local content="$2"
  if [ -n "$FEISHU_WEBHOOK" ]; then
    curl -s -X POST "$FEISHU_WEBHOOK" -H 'Content-Type: application/json' \
      -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"$title\n\n$content\"}}" >/dev/null
  fi
}

migrate_partition() {
  local parent="$1"; local partition="$2"; local partition_bounds="$3"

  log "开始迁移: $partition (parent: $parent)"

  # 1. Detach
  psql_exec "ALTER TABLE public.$parent DETACH PARTITION public.$partition;" || true

  # 2. 创建列存副本
  local col_table="${partition}_col_tmp"
  psql_exec "CREATE TABLE public.$col_table (LIKE public.$partition INCLUDING ALL EXCLUDING INDEXES) USING columnar;"

  # 3. 复制数据 (local: 全量; remote: 分批)
  local orig_count
  orig_count=$(psql_exec "SELECT count(*) FROM public.$partition;")

  if [ "$MODE" = "local" ] || [ "$orig_count" -le "$BATCH_SIZE" ]; then
    psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition;"
  else
    local id_range min_id max_id
    id_range=$(psql_exec "SELECT min(id), max(id) FROM public.$partition WHERE id IS NOT NULL;")
    min_id=$(echo "$id_range" | cut -d'|' -f1 | tr -d ' ')
    max_id=$(echo "$id_range" | cut -d'|' -f2 | tr -d ' ')
    if [ -n "$min_id" ] && [ "$min_id" != "" ]; then
      log "  ID 范围: $min_id - $max_id, 分批迁移 (batch=$BATCH_SIZE)"
      local cur=$min_id total=0
      while [ "$cur" -le "$max_id" ]; do
        local end=$((cur + BATCH_SIZE - 1))
        [ "$end" -gt "$max_id" ] && end=$max_id
        psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition WHERE id BETWEEN $cur AND $end;" >/dev/null 2>&1
        total=$((total + BATCH_SIZE))
        if [ $((total % 1000)) -lt "$BATCH_SIZE" ]; then
          log "  进度: $total 行"
        fi
        cur=$((end + 1))
      done
      log "  分批完成: $total 行"
    else
      log "  无 id 列，全量复制"
      psql_exec "INSERT INTO public.$col_table SELECT * FROM public.$partition;"
    fi
  fi

  # 4. 验证
  local col_count
  col_count=$(psql_exec "SELECT count(*) FROM public.$col_table;")
  if [ "$orig_count" != "$col_count" ]; then
    log "  ❌ 验证失败: 原表 $orig_count 行 != 列存 $col_count 行"
    psql_exec "DROP TABLE public.$col_table;"
    [ -n "$partition_bounds" ] && psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$partition $partition_bounds;" || true
    return 1
  fi

  # 5. ATTACH 新表
  if [ -n "$partition_bounds" ]; then
    psql_exec "ALTER TABLE public.$parent ATTACH PARTITION public.$col_table $partition_bounds;"
  fi

  # 6. Rename
  local backup_table="${partition}_heap_backup"
  psql_exec "ALTER TABLE public.$partition RENAME TO $backup_table;"
  psql_exec "ALTER TABLE public.$col_table RENAME TO $partition;"

  # 7. 大小对比
  local old_size new_size
  old_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$backup_table'::regclass));" | tr -d ' ')
  new_size=$(psql_exec "SELECT pg_size_pretty(pg_total_relation_size('public.$partition'::regclass));" | tr -d ' ')
  log "  ✅ 迁移成功: $old_size -> $new_size"

  # 8. DROP
  psql_exec "DROP TABLE public.$backup_table CASCADE;"
  echo "$partition|$old_size|$new_size"
}

# ============ 主逻辑 ============
main() {
  log "========== PG Columnar Rotate ($MODE) 开始 =========="
  log "配置: DB=$DB_NAME, AGE_DAYS=$AGE_DAYS, DRY_RUN=$DRY_RUN"

  if [ "$MODE" = "remote" ]; then
    POD=$(get_pod)
    [ -z "$POD" ] && { log "❌ 找不到 Pod"; exit 1; }
    log "目标 Pod: $POD"
  fi

  # 扫描 heap 分区
  log "扫描 heap 分区..."
  local heap_partitions
  heap_partitions=$(psql_exec "$HEAP_QUERY")

  if [ -z "$heap_partitions" ]; then
    log "✅ 没有需要转换的 heap 分区"
    [ "$MODE" = "remote" ] && notify_feishu "PG Columnar Rotate" "✅ 没有需要转换的分区"
    exit 0
  fi

  log "发现 $(echo "$heap_partitions" | wc -l) 个 heap 分区:"
  echo "$heap_partitions"

  if [ "$DRY_RUN" = "true" ]; then
    log "DRY_RUN 模式，跳过实际转换"
    [ "$MODE" = "remote" ] && notify_feishu "PG Columnar Rotate (DRY RUN)" "发现以下分区需转换:\n$heap_partitions"
    exit 0
  fi

  local success_list="" failed_list=""
  while IFS='|' read -r parent partition size bounds; do
    if migrate_partition "$parent" "$partition" "$bounds"; then
      success_list="$success_list\n✅ $partition: $size"
    else
      failed_list="$failed_list\n❌ $partition: 迁移失败"
    fi
  done <<< "$heap_partitions"

  log "执行 VACUUM ANALYZE..."
  psql_exec "VACUUM ANALYZE;" >/dev/null 2>&1 || true

  local db_size
  db_size=$(psql_exec "SELECT pg_size_pretty(pg_database_size('$DB_NAME'));" | tr -d ' ')
  log "========== PG Columnar Rotate ($MODE) 完成 =========="
  log "数据库总大小: $db_size"
  log "成功:$success_list"
  [ -n "$failed_list" ] && log "失败:$failed_list"

  [ "$MODE" = "remote" ] && notify_feishu "PG Columnar Rotate 完成" "数据库: $db_size\n成功:$success_list\n失败:$failed_list"
}

main
