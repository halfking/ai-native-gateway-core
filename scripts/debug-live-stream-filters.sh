#!/bin/bash

# 实时请求流筛选选项诊断脚本
# 用于诊断为什么筛选弹窗中的可选项数据过少

set -e

echo "=========================================="
echo "实时请求流筛选选项诊断"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置
REDIS_HOST="${REDIS_HOST:-localhost}"
REDIS_PORT="${REDIS_PORT:-6379}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-postgres}"

echo "配置:"
echo "  Redis: ${REDIS_HOST}:${REDIS_PORT}"
echo "  PostgreSQL: ${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}"
echo ""

# 1. 检查 Redis 数据
echo "=========================================="
echo "1. 检查 Redis 实时流数据"
echo "=========================================="

echo ""
echo "主队列大小:"
MAIN_SIZE=$(redis-cli -h ${REDIS_HOST} -p ${REDIS_PORT} ZCARD llmgw:live:main 2>/dev/null || echo "0")
echo "  llmgw:live:main: ${MAIN_SIZE} 条记录"

if [ "$MAIN_SIZE" -eq 0 ]; then
  echo -e "${RED}警告: 主队列为空！实时流没有数据${NC}"
else
  echo -e "${GREEN}✓ 主队列有数据${NC}"
fi

echo ""
echo "维度队列统计:"

# 检查各维度的队列
for dim in vendor provider model; do
  echo ""
  echo "  ${dim} 维度:"
  KEYS=$(redis-cli -h ${REDIS_HOST} -p ${REDIS_PORT} --scan --pattern "llmgw:live:dim:${dim}:*" 2>/dev/null || echo "")
  if [ -z "$KEYS" ]; then
    echo -e "    ${YELLOW}没有找到 ${dim} 维度的队列${NC}"
  else
    COUNT=0
    while IFS= read -r key; do
      SIZE=$(redis-cli -h ${REDIS_HOST} -p ${REDIS_PORT} ZCARD "$key" 2>/dev/null || echo "0")
      LANE_NAME=$(echo "$key" | sed "s/llmgw:live:dim:${dim}://")
      if [ "$SIZE" -gt 0 ]; then
        echo "    - ${LANE_NAME}: ${SIZE} 条"
        COUNT=$((COUNT + 1))
      fi
    done <<< "$KEYS"
    if [ "$COUNT" -eq 0 ]; then
      echo -e "    ${YELLOW}所有队列都为空${NC}"
    else
      echo -e "    ${GREEN}✓ 共 ${COUNT} 个泳道有数据${NC}"
    fi
  fi
done

# 2. 检查请求详情
echo ""
echo "=========================================="
echo "2. 检查请求详情字段完整性"
echo "=========================================="

# 获取最近的几个请求ID
REQUEST_IDS=$(redis-cli -h ${REDIS_HOST} -p ${REDIS_PORT} ZREVRANGE llmgw:live:main 0 4 2>/dev/null || echo "")

if [ -z "$REQUEST_IDS" ]; then
  echo -e "${RED}无法获取请求ID${NC}"
else
  echo "检查最近 5 条请求的字段:"
  echo ""
  
  MISSING_MODEL=0
  MISSING_VENDOR=0
  MISSING_PROVIDER=0
  TOTAL=0
  
  while IFS= read -r rid; do
    if [ -n "$rid" ]; then
      DETAIL=$(redis-cli -h ${REDIS_HOST} -p ${REDIS_PORT} GET "llmgw:live:req:${rid}" 2>/dev/null || echo "")
      if [ -n "$DETAIL" ]; then
        TOTAL=$((TOTAL + 1))
        MODEL=$(echo "$DETAIL" | jq -r '.model // empty' 2>/dev/null || echo "")
        CANONICAL=$(echo "$DETAIL" | jq -r '.canonical_name // empty' 2>/dev/null || echo "")
        CATEGORY=$(echo "$DETAIL" | jq -r '.model_category // empty' 2>/dev/null || echo "")
        PROVIDER=$(echo "$DETAIL" | jq -r '.provider_code // empty' 2>/dev/null || echo "")
        
        echo "  请求 ${rid:0:12}...:"
        echo "    model: ${MODEL:-<空>}"
        echo "    canonical_name: ${CANONICAL:-<空>}"
        echo "    model_category: ${CATEGORY:-<空>}"
        echo "    provider_code: ${PROVIDER:-<空>}"
        
        if [ -z "$MODEL" ] && [ -z "$CANONICAL" ]; then
          MISSING_MODEL=$((MISSING_MODEL + 1))
        fi
        if [ -z "$CATEGORY" ]; then
          MISSING_VENDOR=$((MISSING_VENDOR + 1))
        fi
        if [ -z "$PROVIDER" ]; then
          MISSING_PROVIDER=$((MISSING_PROVIDER + 1))
        fi
        echo ""
      fi
    fi
  done <<< "$REQUEST_IDS"
  
  echo "字段缺失统计:"
  echo "  模型字段缺失: ${MISSING_MODEL}/${TOTAL}"
  echo "  原厂字段缺失: ${MISSING_VENDOR}/${TOTAL}"
  echo "  供应商字段缺失: ${MISSING_PROVIDER}/${TOTAL}"
  
  if [ "$MISSING_MODEL" -gt 0 ] || [ "$MISSING_VENDOR" -gt 2 ] || [ "$MISSING_PROVIDER" -gt 2 ]; then
    echo -e "${RED}⚠ 发现字段缺失问题！${NC}"
  else
    echo -e "${GREEN}✓ 字段完整性良好${NC}"
  fi
fi

# 3. 检查数据库
echo ""
echo "=========================================="
echo "3. 检查数据库记录"
echo "=========================================="

DB_RESULT=$(psql -h ${DB_HOST} -p ${DB_PORT} -U ${DB_USER} -d ${DB_NAME} -t -c "
SELECT 
  COUNT(*) as total,
  COUNT(CASE WHEN model_category IS NULL OR model_category = '' THEN 1 END) as missing_category,
  COUNT(CASE WHEN provider_code IS NULL OR provider_code = '' THEN 1 END) as missing_provider
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
" 2>/dev/null || echo "0|0|0")

IFS='|' read -r DB_TOTAL DB_MISSING_CAT DB_MISSING_PROV <<< "$DB_RESULT"

DB_TOTAL=$(echo $DB_TOTAL | xargs)
DB_MISSING_CAT=$(echo $DB_MISSING_CAT | xargs)
DB_MISSING_PROV=$(echo $DB_MISSING_PROV | xargs)

echo "最近 1 小时的请求统计:"
echo "  总请求数: ${DB_TOTAL}"
echo "  model_category 缺失: ${DB_MISSING_CAT}"
echo "  provider_code 缺失: ${DB_MISSING_PROV}"

if [ "$DB_TOTAL" -gt 0 ]; then
  CAT_PERCENT=$(awk "BEGIN {printf \"%.1f\", ($DB_MISSING_CAT / $DB_TOTAL) * 100}")
  PROV_PERCENT=$(awk "BEGIN {printf \"%.1f\", ($DB_MISSING_PROV / $DB_TOTAL) * 100}")
  
  echo ""
  echo "  model_category 缺失率: ${CAT_PERCENT}%"
  echo "  provider_code 缺失率: ${PROV_PERCENT}%"
  
  if (( $(echo "$CAT_PERCENT > 10" | bc -l) )) || (( $(echo "$PROV_PERCENT > 10" | bc -l) )); then
    echo -e "${RED}⚠ 字段缺失率过高！${NC}"
  else
    echo -e "${GREEN}✓ 字段缺失率正常${NC}"
  fi
fi

# 4. 检查唯一值数量
echo ""
echo "=========================================="
echo "4. 检查可选项唯一值数量"
echo "=========================================="

UNIQUE_RESULT=$(psql -h ${DB_HOST} -p ${DB_PORT} -U ${DB_USER} -d ${DB_NAME} -t -c "
SELECT 
  COUNT(DISTINCT COALESCE(canonical_name, model)) as unique_models,
  COUNT(DISTINCT model_category) as unique_categories,
  COUNT(DISTINCT provider_code) as unique_providers
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
  AND (canonical_name IS NOT NULL OR model IS NOT NULL);
" 2>/dev/null || echo "0|0|0")

IFS='|' read -r UNIQUE_MODELS UNIQUE_CATS UNIQUE_PROVS <<< "$UNIQUE_RESULT"

UNIQUE_MODELS=$(echo $UNIQUE_MODELS | xargs)
UNIQUE_CATS=$(echo $UNIQUE_CATS | xargs)
UNIQUE_PROVS=$(echo $UNIQUE_PROVS | xargs)

echo "最近 1 小时的唯一值统计:"
echo "  唯一模型数: ${UNIQUE_MODELS}"
echo "  唯一原厂数: ${UNIQUE_CATS}"
echo "  唯一供应商数: ${UNIQUE_PROVS}"

if [ "$UNIQUE_MODELS" -lt 3 ] || [ "$UNIQUE_CATS" -lt 2 ] || [ "$UNIQUE_PROVS" -lt 2 ]; then
  echo -e "${YELLOW}⚠ 数据多样性较低，这可能是筛选选项少的原因${NC}"
else
  echo -e "${GREEN}✓ 数据多样性良好${NC}"
fi

# 5. 总结
echo ""
echo "=========================================="
echo "诊断总结"
echo "=========================================="
echo ""

ISSUES=0

if [ "$MAIN_SIZE" -eq 0 ]; then
  echo -e "${RED}✗ Redis 主队列为空 - 实时流没有数据${NC}"
  ISSUES=$((ISSUES + 1))
fi

if [ "$MISSING_MODEL" -gt 0 ] || [ "$MISSING_VENDOR" -gt 2 ] || [ "$MISSING_PROVIDER" -gt 2 ]; then
  echo -e "${RED}✗ Redis 请求详情中字段缺失严重${NC}"
  ISSUES=$((ISSUES + 1))
fi

if [ "$DB_TOTAL" -gt 0 ]; then
  CAT_PERCENT=$(awk "BEGIN {printf \"%.0f\", ($DB_MISSING_CAT / $DB_TOTAL) * 100}")
  PROV_PERCENT=$(awk "BEGIN {printf \"%.0f\", ($DB_MISSING_PROV / $DB_TOTAL) * 100}")
  
  if [ "$CAT_PERCENT" -gt 10 ] || [ "$PROV_PERCENT" -gt 10 ]; then
    echo -e "${RED}✗ 数据库记录中字段缺失率过高${NC}"
    ISSUES=$((ISSUES + 1))
  fi
fi

if [ "$UNIQUE_MODELS" -lt 3 ] || [ "$UNIQUE_CATS" -lt 2 ] || [ "$UNIQUE_PROVS" -lt 2 ]; then
  echo -e "${YELLOW}⚠ 数据多样性较低${NC}"
  ISSUES=$((ISSUES + 1))
fi

echo ""
if [ "$ISSUES" -eq 0 ]; then
  echo -e "${GREEN}✓ 未发现明显问题，筛选选项应该正常显示${NC}"
  echo ""
  echo "建议:"
  echo "  - 在浏览器控制台检查前端状态"
  echo "  - 确认 SSE 连接正常工作"
  echo "  - 检查前端合并逻辑"
else
  echo -e "${RED}发现 ${ISSUES} 个潜在问题${NC}"
  echo ""
  echo "建议修复措施:"
  
  if [ "$MAIN_SIZE" -eq 0 ]; then
    echo "  1. 检查 SSE hub 是否正常运行"
    echo "  2. 检查 live stream 异步记录器是否工作"
    echo "  3. 验证 Redis 连接"
  fi
  
  if [ "$MISSING_MODEL" -gt 0 ] || [ "$MISSING_VENDOR" -gt 2 ] || [ "$MISSING_PROVIDER" -gt 2 ]; then
    echo "  1. 检查路由层是否正确设置 provider_code"
    echo "  2. 检查模型目录查询是否正确设置 model_category"
    echo "  3. 查看后端日志中的 'missing model_category' 警告"
  fi
  
  if [ "$UNIQUE_MODELS" -lt 3 ]; then
    echo "  1. 等待系统积累更多不同类型的请求"
    echo "  2. 考虑发起一些测试请求"
  fi
fi

echo ""
echo "=========================================="
echo "详细诊断文档: docs/troubleshooting/live-stream-filter-options-diagnostic.md"
echo "=========================================="
