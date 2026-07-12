#!/bin/bash
set -euo pipefail

# PostgreSQL 双向同步脚本
# 用法: ./pg-bidirectional-sync.sh <direction> [table_name]
# direction: local-to-remote, remote-to-local, check
# table_name: 可选，指定同步特定表

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# 配置
LOCAL_CONTAINER="llm-gateway-pg"
LOCAL_USER="llm_gateway"
LOCAL_DB="llm_gateway"

REMOTE_HOST="115.29.212.252"
REMOTE_PORT="25022"
REMOTE_CONTAINER="pg-252-pg17"
REMOTE_USER="llm_gateway"
REMOTE_DB="llm_gateway"

# 检查参数
if [ $# -lt 1 ]; then
    echo "用法: $0 <direction> [table_name]"
    echo "  direction: local-to-remote, remote-to-local, check"
    echo "  table_name: 可选，指定同步特定表"
    exit 1
fi

DIRECTION=$1
TABLE_NAME=${2:-""}

# 函数：执行本地命令
local_cmd() {
    docker exec "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -t -A -c "$1"
}

# 函数：执行远程命令
remote_cmd() {
    export SSHPASS='Kaixuan2026&#*9527'
    sshpass -e ssh -o StrictHostKeyChecking=no -p "$REMOTE_PORT" root@"$REMOTE_HOST" \
        "docker exec $REMOTE_CONTAINER psql -U $REMOTE_USER -d $REMOTE_DB -t -A -c \"$1\""
}

# 函数：检查差异
check_diff() {
    echo -e "${YELLOW}▶ 检查数据库结构差异${NC}"
    
    # 获取本地统计
    local local_tables=$(local_cmd "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public';")
    local local_columns=$(local_cmd "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public';")
    local local_indexes=$(local_cmd "SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public';")
    
    # 获取远程统计
    local remote_tables=$(remote_cmd "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public';")
    local remote_columns=$(remote_cmd "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public';")
    local remote_indexes=$(remote_cmd "SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public';")
    
    echo "  指标            252    本地"
    echo "  tables          $remote_tables    $local_tables"
    echo "  columns         $remote_columns    $local_columns"
    echo "  indexes         $remote_indexes    $local_indexes"
    
    # 检查差异
    if [ "$local_tables" -eq "$remote_tables" ] && \
       [ "$local_columns" -eq "$remote_columns" ] && \
       [ "$local_indexes" -eq "$remote_indexes" ]; then
        echo -e "${GREEN}✓ 结构完全一致${NC}"
        return 0
    else
        echo -e "${RED}✗ 结构存在差异${NC}"
        return 1
    fi
}

# 函数：同步表结构
sync_table() {
    local table=$1
    local direction=$2
    
    echo -e "${YELLOW}▶ 同步表: $table ($direction)${NC}"
    
    if [ "$direction" = "local-to-remote" ]; then
        # 本地 → 252
        docker exec "$LOCAL_CONTAINER" pg_dump -U "$LOCAL_USER" -d "$LOCAL_DB" -t "$table" --schema-only --no-owner --no-privileges > /tmp/sync_table.sql
        
        export SSHPASS='Kaixuan2026&#*9527'
        sshpass -e scp -o StrictHostKeyChecking=no -p "$REMOTE_PORT" /tmp/sync_table.sql root@"$REMOTE_HOST":/tmp/
        
        sshpass -e ssh -o StrictHostKeyChecking=no -p "$REMOTE_PORT" root@"$REMOTE_HOST" \
            "docker exec -i $REMOTE_CONTAINER psql -U $REMOTE_USER -d $REMOTE_DB < /tmp/sync_table.sql"
        
        echo -e "${GREEN}✓ 已同步 $table 到 252${NC}"
        
    elif [ "$direction" = "remote-to-local" ]; then
        # 252 → 本地
        export SSHPASS='Kaixuan2026&#*9527'
        sshpass -e ssh -o StrictHostKeyChecking=no -p "$REMOTE_PORT" root@"$REMOTE_HOST" \
            "docker exec $REMOTE_CONTAINER pg_dump -U $REMOTE_USER -d $REMOTE_DB -t $table --schema-only --no-owner --no-privileges > /tmp/sync_table.sql"
        
        sshpass -e scp -o StrictHostKeyChecking=no -p "$REMOTE_PORT" root@"$REMOTE_HOST":/tmp/sync_table.sql /tmp/
        
        docker exec -i "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" < /tmp/sync_table.sql
        
        echo -e "${GREEN}✓ 已同步 $table 到本地${NC}"
    fi
}

# 主逻辑
case $DIRECTION in
    "check")
        check_diff
        ;;
    
    "local-to-remote")
        if [ -n "$TABLE_NAME" ]; then
            sync_table "$TABLE_NAME" "local-to-remote"
        else
            echo "需要指定表名"
            exit 1
        fi
        ;;
    
    "remote-to-local")
        if [ -n "$TABLE_NAME" ]; then
            sync_table "$TABLE_NAME" "remote-to-local"
        else
            echo "需要指定表名"
            exit 1
        fi
        ;;
    
    *)
        echo "未知的方向: $DIRECTION"
        exit 1
        ;;
esac
