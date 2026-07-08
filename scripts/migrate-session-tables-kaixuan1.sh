#!/bin/bash
# migrate-session-tables-kaixuan1.sh - 在kaixuan-1 K3s环境执行session表迁移
#
# 用法:
#   ./scripts/migrate-session-tables-kaixuan1.sh [--dry-run]

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
  echo -e "${YELLOW}[DRY-RUN MODE]${NC} 将显示执行计划但不实际执行"
  echo ""
fi

echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}  Kaixuan-1 K3s 环境 - Session表迁移${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 迁移文件列表
MIGRATIONS=(
    "350_session_analytics_fix.sql"
    "351_session_analytics_tables.sql"
    "355_session_analytics_indexes.sql"
    "356_session_health_columns.sql"
    "357_session_analytics_aggregation_views.sql"
    "358_session_ownership.sql"
    "359_session_intent_evolution.sql"
)

# 检查kubectl连接
echo -e "${YELLOW}[1/5]${NC} 检查K3s集群连接..."
if [ $DRY_RUN -eq 0 ]; then
    if ! kubectl get nodes &>/dev/null; then
        echo -e "${RED}✗ 无法连接到K3s集群${NC}"
        echo "请先配置kubectl或使用正确的kubeconfig:"
        echo "  export KUBECONFIG=~/.kube/kaixuan-1-config"
        exit 1
    fi
    echo -e "${GREEN}✓ K3s集群连接正常${NC}"
else
    echo -e "${BLUE}[DRY-RUN] kubectl get nodes${NC}"
fi
echo ""

# 查找PostgreSQL Pod
echo -e "${YELLOW}[2/5]${NC} 查找PostgreSQL Pod..."
if [ $DRY_RUN -eq 0 ]; then
    PG_POD=$(kubectl get pods -n pms-test -l app=postgresql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [ -z "$PG_POD" ]; then
        echo -e "${RED}✗ 未找到PostgreSQL Pod${NC}"
        echo "请检查namespace和label是否正确"
        exit 1
    fi
    
    echo -e "${GREEN}✓ 找到Pod: $PG_POD${NC}"
else
    PG_POD="postgresql-0"
    echo -e "${BLUE}[DRY-RUN] Pod: $PG_POD${NC}"
fi
echo ""

# 上传迁移文件
echo -e "${YELLOW}[3/5]${NC} 上传迁移文件到Pod..."
if [ $DRY_RUN -eq 0 ]; then
    for migration in "${MIGRATIONS[@]}"; do
        local_file="$REPO_DIR/sql/migrations/startup/$migration"
        
        if [ ! -f "$local_file" ]; then
            echo -e "${YELLOW}⚠️  跳过: $migration (文件不存在)${NC}"
            continue
        fi
        
        echo "  上传: $migration"
        kubectl cp "$local_file" "pms-test/$PG_POD:/tmp/$migration"
    done
    echo -e "${GREEN}✓ 上传完成${NC}"
else
    for migration in "${MIGRATIONS[@]}"; do
        echo -e "${BLUE}[DRY-RUN] kubectl cp $migration → /tmp/${NC}"
    done
fi
echo ""

# 执行迁移
echo -e "${YELLOW}[4/5]${NC} 执行迁移..."
if [ $DRY_RUN -eq 0 ]; then
    for migration in "${MIGRATIONS[@]}"; do
        echo "  执行: $migration"
        
        kubectl exec -n pms-test "$PG_POD" -- \
            psql -U llm_gateway -d llm_gateway -f "/tmp/$migration" 2>&1 | \
            grep -E "CREATE|ALTER|ERROR|NOTICE" || true
        
        if [ ${PIPESTATUS[0]} -eq 0 ]; then
            echo -e "    ${GREEN}✓ 成功${NC}"
        else
            echo -e "    ${RED}✗ 失败${NC}"
        fi
    done
    echo -e "${GREEN}✓ 迁移执行完成${NC}"
else
    for migration in "${MIGRATIONS[@]}"; do
        echo -e "${BLUE}[DRY-RUN] kubectl exec psql -f /tmp/$migration${NC}"
    done
fi
echo ""

# 验证
echo -e "${YELLOW}[5/5]${NC} 验证迁移结果..."
if [ $DRY_RUN -eq 0 ]; then
    echo "检查 session_dim 表:"
    kubectl exec -n pms-test "$PG_POD" -- \
        psql -U llm_gateway -d llm_gateway -c \
        "SELECT COUNT(*) as record_count FROM session_dim" 2>&1 || echo "查询失败"
    
    echo ""
    echo "检查触发器:"
    kubectl exec -n pms-test "$PG_POD" -- \
        psql -U llm_gateway -d llm_gateway -c \
        "SELECT tgname FROM pg_trigger WHERE tgname = 'trg_update_session_summary'" 2>&1 || echo "查询失败"
    
    echo ""
    echo "检查所有session相关表:"
    kubectl exec -n pms-test "$PG_POD" -- \
        psql -U llm_gateway -d llm_gateway -c \
        "SELECT table_name FROM information_schema.tables WHERE table_name LIKE 'session%' ORDER BY table_name" 2>&1 || echo "查询失败"
    
    echo -e "${GREEN}✓ 验证完成${NC}"
else
    echo -e "${BLUE}[DRY-RUN] 验证表、触发器、视图${NC}"
fi
echo ""

echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  迁移完成！${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "后续步骤:"
echo "  1. 重启gateway deployment以应用数据库更改"
echo "  2. 验证仪表盘功能是否完整"
echo "  3. 检查 Top5 任务是否显示数据"
echo ""
echo "重启命令:"
echo "  kubectl rollout restart deployment/llm-gateway-go -n pms-test"
echo ""
