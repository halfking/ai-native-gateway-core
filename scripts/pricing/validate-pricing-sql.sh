#!/usr/bin/env bash
# 在本地SQLite测试环境中验证定价SQL的正确性
# 无需连接远程数据库,完全在本地完成验证

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TEST_DB="/tmp/pricing_validation_$(date +%Y%m%d_%H%M%S).db"
SQL_FILE="/tmp/model_pricing_with_public_price_20261117.sql"

echo "╔═══════════════════════════════════════════════════════════════════════════╗"
echo "║           定价SQL验证 - 本地SQLite测试环境                                ║"
echo "╚═══════════════════════════════════════════════════════════════════════════╝"
echo ""
echo "测试数据库: $TEST_DB"
echo "SQL文件: $SQL_FILE"
echo ""

# 检查SQL文件是否存在
if [[ ! -f "$SQL_FILE" ]]; then
  echo "❌ 错误: SQL文件不存在: $SQL_FILE"
  echo "请先运行: ./scripts/pricing/generate-pricing-with-multiplier.sh > $SQL_FILE"
  exit 1
fi

echo "━━━ 步骤 1/5: 创建测试数据库结构 ━━━"

# 创建SQLite兼容的表结构
sqlite3 "$TEST_DB" <<'EOF'
CREATE TABLE IF NOT EXISTS model_pricing (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_canonical VARCHAR(64) NOT NULL UNIQUE,
    display_name VARCHAR(128) NOT NULL,
    input_credits_per_1m INTEGER NOT NULL,
    output_credits_per_1m INTEGER NOT NULL,
    cache_write_credits_per_1m INTEGER,
    cache_read_credits_per_1m INTEGER,
    provider VARCHAR(32) NOT NULL,
    provider_model VARCHAR(64),
    context_window INTEGER DEFAULT 128000 NOT NULL,
    max_output_tokens INTEGER DEFAULT 4096 NOT NULL,
    supports_streaming BOOLEAN DEFAULT 1 NOT NULL,
    supports_tools BOOLEAN DEFAULT 1 NOT NULL,
    supports_vision BOOLEAN DEFAULT 0 NOT NULL,
    supports_caching BOOLEAN DEFAULT 0 NOT NULL,
    tier VARCHAR(16) NOT NULL,
    daily_free_quota_credits INTEGER DEFAULT 0,
    min_credits_per_request INTEGER DEFAULT 0,
    active BOOLEAN DEFAULT 1 NOT NULL,
    deprecated BOOLEAN DEFAULT 0 NOT NULL,
    replacement_model VARCHAR(64),
    notes TEXT,
    CHECK (input_credits_per_1m >= 0),
    CHECK (output_credits_per_1m >= 0),
    CHECK (tier IN ('free', 'basic', 'standard', 'premium', 'enterprise'))
);
EOF

if [[ $? -eq 0 ]]; then
  echo "✓ 表结构创建成功"
else
  echo "❌ 表结构创建失败"
  exit 1
fi

echo ""
echo "━━━ 步骤 2/5: 转换并导入SQL数据 ━━━"

# 从PostgreSQL SQL提取INSERT语句并转换为SQLite兼容格式
grep "^INSERT INTO model_pricing" "$SQL_FILE" | \
  sed "s/'t'/'1'/g" | \
  sed "s/'f'/'0'/g" | \
  sed "s/true/1/g" | \
  sed "s/false/0/g" > /tmp/pricing_inserts_sqlite.sql

# 统计INSERT语句数量
INSERT_COUNT=$(wc -l < /tmp/pricing_inserts_sqlite.sql | tr -d ' ')
echo "提取了 $INSERT_COUNT 条INSERT语句"

# 导入数据
sqlite3 "$TEST_DB" < /tmp/pricing_inserts_sqlite.sql

if [[ $? -eq 0 ]]; then
  IMPORTED=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM model_pricing;")
  echo "✓ 数据导入成功: $IMPORTED 行"
else
  echo "❌ 数据导入失败"
  exit 1
fi

echo ""
echo "━━━ 步骤 3/5: 验证数据完整性 ━━━"

# 验证1: 检查是否所有模型都导入了
EXPECTED=16
ACTUAL=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM model_pricing WHERE active = 1;")

echo -n "验证1: 活跃模型数量 ... "
if [[ "$ACTUAL" -eq "$EXPECTED" ]]; then
  echo "✓ 通过 ($ACTUAL/$EXPECTED)"
else
  echo "❌ 失败 (预期$EXPECTED, 实际$ACTUAL)"
fi

# 验证2: 检查所有档位是否都有模型
echo -n "验证2: 档位完整性 ... "
TIERS=$(sqlite3 "$TEST_DB" "SELECT COUNT(DISTINCT tier) FROM model_pricing WHERE active = 1;")
if [[ "$TIERS" -eq 5 ]]; then
  echo "✓ 通过 (5个档位)"
else
  echo "❌ 失败 (只有$TIERS个档位)"
fi

# 验证3: 检查价格字段非空
echo -n "验证3: 价格字段非空 ... "
NULL_PRICES=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM model_pricing WHERE input_credits_per_1m IS NULL OR output_credits_per_1m IS NULL;")
if [[ "$NULL_PRICES" -eq 0 ]]; then
  echo "✓ 通过 (无NULL价格)"
else
  echo "❌ 失败 (有${NULL_PRICES}行价格为NULL)"
fi

# 验证4: 检查价格非负
echo -n "验证4: 价格非负 ... "
NEGATIVE_PRICES=$(sqlite3 "$TEST_DB" "SELECT COUNT(*) FROM model_pricing WHERE input_credits_per_1m < 0 OR output_credits_per_1m < 0;")
if [[ "$NEGATIVE_PRICES" -eq 0 ]]; then
  echo "✓ 通过 (无负价格)"
else
  echo "❌ 失败 (有${NEGATIVE_PRICES}行负价格)"
fi

echo ""
echo "━━━ 步骤 4/5: 验证档位倍率 ━━━"

# 验证各档位的价格范围是否符合预期
echo ""
sqlite3 -header -column "$TEST_DB" <<'EOF'
SELECT 
  tier,
  COUNT(*) as model_count,
  MIN(input_credits_per_1m + output_credits_per_1m) as min_total,
  MAX(input_credits_per_1m + output_credits_per_1m) as max_total,
  ROUND(AVG(input_credits_per_1m + output_credits_per_1m)) as avg_total
FROM model_pricing
WHERE active = 1
GROUP BY tier
ORDER BY min_total;
EOF

echo ""
echo "预期档位价格范围:"
echo "  Free:       100"
echo "  Basic:      300-600"
echo "  Standard:   1890-2160"
echo "  Premium:    4000"
echo "  Enterprise: 10000"

echo ""
echo "━━━ 步骤 5/5: 验证具体模型定价 ━━━"

# 验证关键模型的定价
echo ""
echo "关键模型定价验证:"
echo ""

sqlite3 -header -column "$TEST_DB" <<'EOF'
SELECT 
  model_canonical,
  tier,
  (input_credits_per_1m + output_credits_per_1m) as public_total_credits,
  ROUND((input_credits_per_1m + output_credits_per_1m) * 1.5 / 100, 2) as public_usd
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
echo "预期定价(USD per 1M tokens):"
echo "  minimax-m3:       $1.50"
echo "  claude-haiku-4-5: $9.00"
echo "  claude-sonnet-4-5: $32.40"
echo "  claude-opus-4-8:  $60.00"
echo "  claude-fable-5:   $150.00"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "高级验证 - 倍率一致性检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 从notes字段提取内部价格并计算倍率
echo ""
sqlite3 "$TEST_DB" <<'EOF'
.mode column
.headers on
SELECT 
  model_canonical,
  tier,
  (input_credits_per_1m + output_credits_per_1m) as public_credits,
  CAST(
    SUBSTR(
      notes, 
      INSTR(notes, 'Internal: ') + 10,
      INSTR(SUBSTR(notes, INSTR(notes, 'Internal: ')), ' credits') - 10
    ) AS INTEGER
  ) as internal_credits,
  ROUND(
    CAST(input_credits_per_1m + output_credits_per_1m AS FLOAT) / 
    CAST(
      SUBSTR(
        notes, 
        INSTR(notes, 'Internal: ') + 10,
        INSTR(SUBSTR(notes, INSTR(notes, 'Internal: ')), ' credits') - 10
      ) AS FLOAT
    ),
    2
  ) as actual_multiplier,
  CASE tier
    WHEN 'free' THEN 1.0
    WHEN 'basic' THEN 1.5
    WHEN 'standard' THEN 1.8
    WHEN 'premium' THEN 2.0
    WHEN 'enterprise' THEN 2.5
  END as expected_multiplier
FROM model_pricing
WHERE active = 1
LIMIT 10;
EOF

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "验证总结"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "✅ SQL验证完成!"
echo ""
echo "测试数据库保存在: $TEST_DB"
echo "可以使用以下命令手动检查:"
echo ""
echo "  sqlite3 $TEST_DB"
echo "  sqlite> SELECT * FROM model_pricing WHERE tier='premium';"
echo "  sqlite> .quit"
echo ""
echo "如需清理测试数据库:"
echo "  rm $TEST_DB"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
