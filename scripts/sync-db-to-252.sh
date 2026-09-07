#!/bin/bash
#
# 数据库结构同步脚本：本地 → 252服务器
#
# 用途：将本地开发的新表结构推送到252测试/生产环境
# 特点：幂等、安全、无事务（避免单点故障）
#
# 使用方法：
#   ./scripts/sync-db-to-252.sh                    # 增量同步（推荐）
#   ./scripts/sync-db-to-252.sh --full             # 全量重新生成并同步
#   ./scripts/sync-db-to-252.sh --dry-run          # 仅生成SQL，不执行
#   ./scripts/sync-db-to-252.sh --tables "table1 table2"  # 仅同步指定表
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMP_DIR="${TEMP_DIR:-/tmp}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# 2026-09-07: llm-gateway-pg (kx-citus-pg17) 的本地超级用户是 llm_gateway，不是
# postgres（旧脚本对 252 远端假设的 postgres 在本地不存在）。所有本地
# docker exec 改用 LLM_GATEWAY_PG_USER 模板，与 apply-db-revision-sequence.sh
# 一致；远端 252 调用仍按远端自己的环境（PG_USER_*）解析，保留 postgres。
LOCAL_PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}"

# 默认参数
DRY_RUN=false
FULL_SYNC=false
SPECIFIC_TABLES=""

# 解析参数
while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --full)
      FULL_SYNC=true
      shift
      ;;
    --tables)
      SPECIFIC_TABLES="$2"
      shift 2
      ;;
    --help)
      grep "^#" "$0" | grep -v "#!/bin/bash" | sed 's/^# \?//'
      exit 0
      ;;
    *)
      log_error "未知参数: $1"
      exit 1
      ;;
  esac
done

log_info "数据库同步：本地 → 252服务器"
log_info "========================================"

# 1. 加载环境变量
log_info "加载环境配置..."
if [ -f "$PROJECT_ROOT/../envs/loader.sh" ]; then
  source "$PROJECT_ROOT/../envs/loader.sh" --project llm-gateway-go 2>/dev/null || true
fi

if [ -f "$PROJECT_ROOT/configs/env-252.sh" ]; then
  source "$PROJECT_ROOT/configs/env-252.sh"
else
  log_error "找不到 env-252.sh 配置文件"
  exit 1
fi

# 2. 检查SSH隧道
log_info "检查SSH隧道..."
REMOTE_PG_IP="${REMOTE_PG_IP:-10.88.0.79}"
REMOTE_SSH_HOST="${REMOTE_SSH_HOST:-115.29.212.252}"
REMOTE_SSH_PORT="${REMOTE_SSH_PORT:-25022}"
LOCAL_TUNNEL_PORT="${LOCAL_TUNNEL_PORT:-15432}"

if ! pgrep -f "ssh.*${LOCAL_TUNNEL_PORT}:${REMOTE_PG_IP}:5432" > /dev/null; then
  log_warn "SSH隧道未运行，尝试建立..."
  pkill -f "ssh.*${LOCAL_TUNNEL_PORT}:${REMOTE_PG_IP}:5432" 2>/dev/null || true
  sleep 1
  ssh -f -N -L ${LOCAL_TUNNEL_PORT}:${REMOTE_PG_IP}:5432 -p ${REMOTE_SSH_PORT} root@${REMOTE_SSH_HOST}
  sleep 2
  log_success "SSH隧道已建立"
else
  log_success "SSH隧道已存在"
fi

# 3. 生成同步SQL
OUTPUT_SQL="$TEMP_DIR/sync_local_to_252_$(date +%Y%m%d_%H%M%S).sql"

if [ "$FULL_SYNC" = true ] || [ ! -f "$TEMP_DIR/sync_from_local_to_252_no_tx.sql" ]; then
  log_info "生成同步SQL脚本..."
  
  # 获取本地表列表
  docker exec llm-gateway-pg psql -U "$LOCAL_PG_USER" -d llm_gateway -t -c \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;" \
    | tr -d ' ' > "$TEMP_DIR/local_tables.txt"
  
  # 获取252表列表
  PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" /opt/homebrew/opt/libpq/bin/psql \
    -h 127.0.0.1 -p ${LOCAL_TUNNEL_PORT} -U llm_gateway -d llm_gateway -t -c \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;" \
    | tr -d ' ' > "$TEMP_DIR/252_tables.txt"
  
  # 计算差异（本地有但252没有的表）
  TABLES_TO_SYNC=$(comm -23 <(sort "$TEMP_DIR/local_tables.txt") <(sort "$TEMP_DIR/252_tables.txt") | grep -v '^$')
  
  if [ -z "$TABLES_TO_SYNC" ]; then
    log_success "没有需要同步的表，本地和252已对齐"
    exit 0
  fi
  
  log_info "发现 $(echo "$TABLES_TO_SYNC" | wc -l) 个需要同步的表"
  
  # 导出表结构
  echo "-- ========================================" > "$OUTPUT_SQL"
  echo "-- 数据库同步：本地 → 252" >> "$OUTPUT_SQL"
  echo "-- 生成时间: $(date '+%Y-%m-%d %H:%M:%S')" >> "$OUTPUT_SQL"
  echo "-- 表数量: $(echo "$TABLES_TO_SYNC" | wc -l)" >> "$OUTPUT_SQL"
  echo "-- ========================================" >> "$OUTPUT_SQL"
  echo "" >> "$OUTPUT_SQL"
  echo "SET client_min_messages = WARNING;" >> "$OUTPUT_SQL"
  echo "" >> "$OUTPUT_SQL"
  
  for table in $TABLES_TO_SYNC; do
    # 跳过auth_*系列（redclaw外部服务）
    if [[ "$table" == auth_* ]]; then
      log_warn "跳过外部服务表: $table"
      continue
    fi
    
    log_info "  导出表: $table"
    docker exec llm-gateway-pg pg_dump -U "$LOCAL_PG_USER" -d llm_gateway \
      --schema-only --no-owner --no-privileges \
      -t "public.$table" \
      2>/dev/null >> "$OUTPUT_SQL" || {
      log_warn "  导出失败: $table (可能是视图或依赖问题)"
    }
    echo "" >> "$OUTPUT_SQL"
  done
  
  log_success "SQL脚本已生成: $OUTPUT_SQL"
else
  log_info "使用已有的同步脚本"
  OUTPUT_SQL="$TEMP_DIR/sync_from_local_to_252_no_tx.sql"
fi

# 4. 如果是指定表同步
if [ -n "$SPECIFIC_TABLES" ]; then
  log_info "仅同步指定的表: $SPECIFIC_TABLES"
  SPECIFIC_SQL="$TEMP_DIR/sync_specific_$(date +%Y%m%d_%H%M%S).sql"
  
  echo "-- 指定表同步" > "$SPECIFIC_SQL"
  echo "SET client_min_messages = WARNING;" >> "$SPECIFIC_SQL"
  
  for table in $SPECIFIC_TABLES; do
    log_info "  导出表: $table"
    docker exec llm-gateway-pg pg_dump -U "$LOCAL_PG_USER" -d llm_gateway \
      --schema-only --no-owner --no-privileges \
      -t "public.$table" \
      2>/dev/null >> "$SPECIFIC_SQL"
  done
  
  OUTPUT_SQL="$SPECIFIC_SQL"
fi

# 5. 执行同步
if [ "$DRY_RUN" = true ]; then
  log_warn "DRY RUN 模式，SQL已生成但未执行"
  log_info "SQL文件: $OUTPUT_SQL"
  log_info "查看: cat $OUTPUT_SQL"
  exit 0
fi

log_info "开始推送到252服务器..."
log_warn "使用无事务模式（单表失败不影响其他表）"

PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" /opt/homebrew/opt/libpq/bin/psql \
  -h 127.0.0.1 -p ${LOCAL_TUNNEL_PORT} -U llm_gateway -d llm_gateway \
  -v ON_ERROR_STOP=0 \
  -f "$OUTPUT_SQL" \
  2>&1 | tee "$TEMP_DIR/sync_to_252_$(date +%Y%m%d_%H%M%S).log" | \
  grep -E "(CREATE TABLE|CREATE SEQUENCE|CREATE INDEX|ERROR)" | \
  while IFS= read -r line; do
    if echo "$line" | grep -q "ERROR"; then
      log_warn "  $line"
    elif echo "$line" | grep -q "CREATE TABLE"; then
      log_success "  $line"
    fi
  done

# 6. 验证结果
log_info "验证同步结果..."

LOCAL_COUNT=$(docker exec llm-gateway-pg psql -U "$LOCAL_PG_USER" -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public';" | tr -d ' ')

REMOTE_COUNT=$(PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" /opt/homebrew/opt/libpq/bin/psql \
  -h 127.0.0.1 -p ${LOCAL_TUNNEL_PORT} -U llm_gateway -d llm_gateway -t -c \
  "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public';" | tr -d ' ')

log_info "========================================"
log_info "同步完成"
log_info "========================================"
log_success "本地表数量: $LOCAL_COUNT"
log_success "252表数量:  $REMOTE_COUNT"

DIFF=$((LOCAL_COUNT - REMOTE_COUNT))
if [ $DIFF -eq 0 ]; then
  log_success "✅ 表数量完全一致"
elif [ $DIFF -lt 5 ]; then
  log_warn "⚠️  差异: $DIFF 个表（可能是分区表或配置差异）"
else
  log_warn "⚠️  差异: $DIFF 个表，建议检查同步日志"
fi

log_info ""
log_info "日志文件: $TEMP_DIR/sync_to_252_*.log"
log_info "SQL文件: $OUTPUT_SQL"
