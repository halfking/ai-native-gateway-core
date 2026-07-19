#!/bin/bash
# ============================================================================
# 生产环境修复部署脚本
# 用途: 在生产数据库执行会话压缩配置修复
# ============================================================================

set -e

# 颜色输出
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
SQL_FILE="sql/hotfix/20260719-fix-compression-relay-nodes.sql"
BACKUP_FILE="/tmp/provider_settings_backup_$(date +%Y%m%d_%H%M%S).sql"

echo -e "${BLUE}============================================${NC}"
echo -e "${BLUE}   会话压缩配置修复 - 生产部署${NC}"
echo -e "${BLUE}============================================${NC}"
echo ""

# 检查 SQL 文件
if [ ! -f "$SQL_FILE" ]; then
    echo -e "${RED}错误: 找不到 SQL 文件: $SQL_FILE${NC}"
    exit 1
fi

echo -e "${YELLOW}修复内容:${NC}"
echo "  - 启用 apigpt(587) 和 apiclaude(2451) 的智能压缩"
echo "  - 修复 gpt-5.6-luna 的 NULL contextWindow"
echo ""

# 确认数据库连接信息
echo -e "${YELLOW}请确认数据库连接信息:${NC}"
read -p "数据库主机 (默认 localhost): " DB_HOST
DB_HOST=${DB_HOST:-localhost}

read -p "数据库名称 (默认 llm_gateway): " DB_NAME
DB_NAME=${DB_NAME:-llm_gateway}

read -p "数据库用户 (默认 postgres): " DB_USER
DB_USER=${DB_USER:-postgres}

echo ""
echo -e "${YELLOW}将连接到:${NC} ${DB_USER}@${DB_HOST}/${DB_NAME}"
echo ""

# 最终确认
read -p "$(echo -e ${YELLOW}确认执行修复？[y/N]:${NC}) " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${RED}已取消${NC}"
    exit 0
fi

echo ""
echo -e "${GREEN}开始执行修复...${NC}"
echo ""

# 步骤 1: 备份当前配置
echo -e "${BLUE}[1/5]${NC} 备份当前配置..."
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
COPY (
    SELECT * FROM provider_settings 
    WHERE provider_id IN (587, 2451) 
      AND key = 'compression.mode'
) TO STDOUT;" > "$BACKUP_FILE"

if [ -f "$BACKUP_FILE" ]; then
    echo -e "${GREEN}✓${NC} 备份已保存至: $BACKUP_FILE"
else
    echo -e "${YELLOW}⚠${NC} 备份文件为空（可能原配置不存在）"
fi
echo ""

# 步骤 2: 显示修复前状态
echo -e "${BLUE}[2/5]${NC} 显示修复前状态..."
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
SELECT 
    p.id AS provider_id,
    p.label AS provider_name,
    p.category,
    COALESCE(ps.value, 'NULL') AS compression_mode
FROM providers p
LEFT JOIN provider_settings ps 
    ON ps.provider_id = p.id 
    AND ps.key = 'compression.mode'
WHERE p.id IN (587, 2451);
"
echo ""

# 步骤 3: 执行修复
echo -e "${BLUE}[3/5]${NC} 执行修复 SQL..."
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -f "$SQL_FILE"
echo ""

# 步骤 4: 验证修复结果
echo -e "${BLUE}[4/5]${NC} 验证修复结果..."
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
SELECT 
    p.id AS provider_id,
    p.label AS provider_name,
    p.category,
    ps.value AS compression_mode,
    ps.updated_at
FROM providers p
LEFT JOIN provider_settings ps 
    ON ps.provider_id = p.id 
    AND ps.key = 'compression.mode'
WHERE p.id IN (587, 2451);
"
echo ""

# 步骤 5: 提供监控命令
echo -e "${BLUE}[5/5]${NC} 生成监控脚本..."

cat > /tmp/monitor_compression_fix.sh << 'MONITOR_EOF'
#!/bin/bash
# 监控会话压缩修复效果

DB_HOST="${1:-localhost}"
DB_USER="${2:-postgres}"
DB_NAME="${3:-llm_gateway}"

echo "监控压缩效果（实时更新，按 Ctrl+C 停止）..."
echo ""

watch -n 10 "psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c \"
SELECT 
    DATE_TRUNC('minute', created_at) AS time,
    COUNT(*) AS total,
    COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) AS compressed,
    ROUND(100.0 * COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) / COUNT(*), 1) AS compress_pct,
    COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) AS ctx_errors
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
WHERE c.provider_id IN (587, 2451)
  AND created_at > NOW() - INTERVAL '30 minutes'
GROUP BY time
ORDER BY time DESC
LIMIT 10;
\""
MONITOR_EOF

chmod +x /tmp/monitor_compression_fix.sh

echo -e "${GREEN}✓${NC} 监控脚本已生成: /tmp/monitor_compression_fix.sh"
echo ""

# 完成
echo -e "${GREEN}============================================${NC}"
echo -e "${GREEN}   修复完成！${NC}"
echo -e "${GREEN}============================================${NC}"
echo ""
echo -e "${YELLOW}下一步操作:${NC}"
echo ""
echo "1. 启动实时监控（观察30分钟）:"
echo -e "   ${BLUE}/tmp/monitor_compression_fix.sh $DB_HOST $DB_USER $DB_NAME${NC}"
echo ""
echo "2. 检查错误率（应降至 <1%）:"
echo -e "   ${BLUE}psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c \"
   SELECT COUNT(*) AS total,
          COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) AS errors,
          ROUND(100.0 * COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) / COUNT(*), 2) AS error_rate_pct
   FROM request_logs
   WHERE created_at > NOW() - INTERVAL '30 minutes';
   \"${NC}"
echo ""
echo "3. 如需回滚，执行:"
echo -e "   ${BLUE}psql -h $DB_HOST -U $DB_USER -d $DB_NAME${NC}"
echo "   然后粘贴 $SQL_FILE 文件末尾的回滚语句"
echo ""
echo -e "${YELLOW}备份文件:${NC} $BACKUP_FILE"
echo ""
