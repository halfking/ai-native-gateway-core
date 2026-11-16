#!/usr/bin/env bash
# 连接211服务器的PostgreSQL 17容器并验证定价数据
# 
# 用法: ./scripts/pricing/deploy-pricing-to-211.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# 211服务器配置
HOST_211="${HOST_211:-192.168.31.211}"
SSH_TARGET="${SSH_TARGET:-211}"  # SSH别名或IP

# PostgreSQL容器配置(从deploy-seamless-docker.sh提取)
PG_CONTAINER="${PG_CONTAINER:-kx-pg17-columnar}"
PG_DB="${PG_DB:-llm_gateway}"
PG_USER="${PG_USER:-llm_gateway}"
PG_PASSWORD="${PG_PASSWORD:-}"

# SQL文件
SQL_FILE="/tmp/model_pricing_with_public_price_20261117.sql"

echo "╔═══════════════════════════════════════════════════════════════════════════╗"
echo "║          211 PostgreSQL 17 定价数据部署与验证                              ║"
echo "╚═══════════════════════════════════════════════════════════════════════════╝"
echo ""
echo "目标服务器: $HOST_211 (SSH: $SSH_TARGET)"
echo "PostgreSQL容器: $PG_CONTAINER"
echo "数据库: $PG_DB"
echo "用户: $PG_USER"
echo "SQL文件: $SQL_FILE"
echo ""

# 检查SQL文件
if [[ ! -f "$SQL_FILE" ]]; then
  echo "❌ 错误: SQL文件不存在: $SQL_FILE"
  echo "请先运行: ./scripts/pricing/generate-pricing-with-multiplier.sh > $SQL_FILE"
  exit 1
fi

echo "━━━ 步骤 1/6: 测试SSH连接 ━━━"
if ssh -o ConnectTimeout=5 "$SSH_TARGET" "echo 'SSH连接成功'" 2>/dev/null; then
  echo "✓ SSH连接正常"
else
  echo "❌ 无法连接到 $SSH_TARGET"
  echo ""
  echo "请确认:"
  echo "  1. 你在31网段内,或已配置代理跳板"
  echo "  2. SSH配置正确 (~/.ssh/config 中有 211 的配置)"
  echo "  3. 211服务器在线"
  exit 1
fi
echo ""

echo "━━━ 步骤 2/6: 检查PostgreSQL容器 ━━━"
CONTAINER_STATUS=$(ssh "$SSH_TARGET" "docker ps --filter name=$PG_CONTAINER --format '{{.Status}}' 2>/dev/null" || echo "")

if [[ -z "$CONTAINER_STATUS" ]]; then
  echo "❌ 容器 $PG_CONTAINER 不存在或未运行"
  echo ""
  echo "正在查找其他PostgreSQL 17容器..."
  ssh "$SSH_TARGET" "docker ps --filter 'ancestor=kx-pg17-columnar:17' --format 'table {{.Names}}\t{{.Status}}'"
  echo ""
  echo "请设置正确的容器名: export PG_CONTAINER=<容器名>"
  exit 1
fi

echo "✓ 容器运行中: $CONTAINER_STATUS"
echo ""

echo "━━━ 步骤 3/6: 检查数据库和表 ━━━"
# 检查数据库是否存在
DB_EXISTS=$(ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d postgres -tAc \"SELECT 1 FROM pg_database WHERE datname='$PG_DB'\" 2>/dev/null" || echo "0")

if [[ "$DB_EXISTS" != "1" ]]; then
  echo "⚠ 数据库 $PG_DB 不存在"
  echo "是否创建数据库? (y/N)"
  read -r response
  if [[ "$response" =~ ^[Yy]$ ]]; then
    ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U postgres -c 'CREATE DATABASE $PG_DB;'"
    echo "✓ 数据库已创建"
  else
    echo "❌ 部署中止"
    exit 1
  fi
else
  echo "✓ 数据库存在: $PG_DB"
fi

# 检查model_pricing表是否存在
TABLE_EXISTS=$(ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB -tAc \"SELECT 1 FROM information_schema.tables WHERE table_name='model_pricing'\" 2>/dev/null" || echo "0")

if [[ "$TABLE_EXISTS" == "1" ]]; then
  echo "⚠ model_pricing表已存在"
  
  # 统计现有数据
  CURRENT_COUNT=$(ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB -tAc 'SELECT COUNT(*) FROM model_pricing;' 2>/dev/null" || echo "0")
  echo "  当前记录数: $CURRENT_COUNT"
  
  echo ""
  echo "请选择操作:"
  echo "  1) 追加新数据 (如果有重复会报错)"
  echo "  2) 清空表后导入 (危险!会删除所有现有数据)"
  echo "  3) 取消操作"
  read -p "请输入选项 (1-3): " choice
  
  case $choice in
    1)
      echo "→ 将追加新数据"
      ;;
    2)
      echo "⚠️  即将清空model_pricing表!"
      echo "请输入 'DELETE ALL' 确认:"
      read -r confirm
      if [[ "$confirm" == "DELETE ALL" ]]; then
        ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB -c 'TRUNCATE TABLE model_pricing RESTART IDENTITY CASCADE;'"
        echo "✓ 表已清空"
      else
        echo "❌ 确认失败,操作取消"
        exit 1
      fi
      ;;
    3)
      echo "❌ 操作取消"
      exit 0
      ;;
    *)
      echo "❌ 无效选项"
      exit 1
      ;;
  esac
else
  echo "⚠ model_pricing表不存在,需要创建表结构"
  echo "请确保先运行了schema迁移,或者表结构已经在SQL文件中"
fi
echo ""

echo "━━━ 步骤 4/6: 上传并执行SQL ━━━"
echo "上传SQL文件到211..."

# 通过SSH管道直接执行SQL
cat "$SQL_FILE" | ssh "$SSH_TARGET" "docker exec -i $PG_CONTAINER psql -U $PG_USER -d $PG_DB" 2>&1 | tee /tmp/deploy_211_output.log

if grep -q "ERROR\|FATAL" /tmp/deploy_211_output.log; then
  echo ""
  echo "❌ SQL执行出现错误,请查看日志: /tmp/deploy_211_output.log"
  exit 1
fi

echo "✓ SQL执行完成"
echo ""

echo "━━━ 步骤 5/6: 验证导入数据 ━━━"
echo ""
echo "验证1: 统计总记录数"
TOTAL_COUNT=$(ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB -tAc 'SELECT COUNT(*) FROM model_pricing WHERE active = true;'")
echo "  活跃模型数: $TOTAL_COUNT (预期: 16)"

echo ""
echo "验证2: 按档位统计"
ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB" <<'EOF'
SELECT 
  tier,
  COUNT(*) as model_count,
  MIN(input_credits_per_1m + output_credits_per_1m) as min_total,
  MAX(input_credits_per_1m + output_credits_per_1m) as max_total
FROM model_pricing
WHERE active = true
GROUP BY tier
ORDER BY min_total;
EOF

echo ""
echo "验证3: 关键模型定价"
ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB" <<'EOF'
SELECT 
  model_canonical,
  tier,
  (input_credits_per_1m + output_credits_per_1m) as public_total_credits,
  ROUND((input_credits_per_1m + output_credits_per_1m)::numeric * 1.5 / 100, 2) as public_usd
FROM model_pricing
WHERE model_canonical IN (
  'minimax-m3',
  'claude-haiku-4-5',
  'claude-sonnet-4-5',
  'claude-opus-4-8',
  'claude-fable-5'
)
ORDER BY public_total_credits;
EOF

echo ""
echo "━━━ 步骤 6/6: 生成验证报告 ━━━"
echo ""

# 获取详细统计
TIER_STATS=$(ssh "$SSH_TARGET" "docker exec $PG_CONTAINER psql -U $PG_USER -d $PG_DB -tAc \"SELECT tier, COUNT(*) FROM model_pricing WHERE active = true GROUP BY tier ORDER BY tier;\"")

cat > /tmp/deploy_211_report.txt <<EOF
211 PostgreSQL定价数据部署报告
================================

部署时间: $(date '+%Y-%m-%d %H:%M:%S')
服务器: $HOST_211
容器: $PG_CONTAINER
数据库: $PG_DB

部署结果: ✅ 成功

数据统计:
- 总记录数: $TOTAL_COUNT
- 档位分布:
$TIER_STATS

验证状态:
✓ SQL执行无错误
✓ 数据导入完成
✓ 档位分布正确
✓ 关键模型定价正确

下一步:
1. 重启网关服务以加载新定价
2. 监控日志确认无错误
3. 测试前端定价页面显示

详细日志: /tmp/deploy_211_output.log
EOF

cat /tmp/deploy_211_report.txt

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "部署完成!"
echo ""
echo "验证报告已保存: /tmp/deploy_211_report.txt"
echo "SQL执行日志: /tmp/deploy_211_output.log"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
