#!/usr/bin/env bash
# 生成 model_pricing 表的批量导入SQL
# 基于已确认的基准价格和内部结算基数

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

BASELINE_FILE="bg/data/model_baseline_prices.json"
OUTPUT_SQL="${1:-/tmp/model_pricing_import_$(date +%Y%m%d).sql}"

echo "-- Model Pricing Import Script"
echo "-- Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "-- Source: $BASELINE_FILE"
echo "-- Base model: minimax-m3 (total price: \$1.50 per 1M tokens = 100 credits)"
echo ""
echo "BEGIN;"
echo ""
echo "-- 清理现有数据 (可选,生产环境请谨慎)"
echo "-- TRUNCATE TABLE model_pricing RESTART IDENTITY CASCADE;"
echo ""

# 使用 jq 处理 JSON 并生成 SQL
jq -r '
  # 定义基准模型价格
  def base_total: 1.5;
  
  # 定义档位划分规则
  def tier_for_relative(rel):
    if rel <= 100 then "free"
    elif rel <= 500 then "basic"
    elif rel <= 1500 then "standard"
    elif rel <= 3000 then "premium"
    else "enterprise"
    end;
  
  .models | to_entries[] | 
  
  # 计算相对价格和内部 credits
  ((.value.input_per_1m + .value.output_per_1m) / base_total * 100) as $relative_price |
  ($relative_price | floor) as $total_credits |
  
  # 计算输入输出比例
  (.value.input_per_1m / (.value.input_per_1m + .value.output_per_1m)) as $input_ratio |
  ($total_credits * $input_ratio | floor) as $input_credits |
  ($total_credits - $input_credits) as $output_credits |
  
  # 确定档位
  tier_for_relative($relative_price) as $tier |
  
  # 生成 INSERT 语句
  "INSERT INTO model_pricing (\n  model_canonical,\n  display_name,\n  input_credits_per_1m,\n  output_credits_per_1m,\n  cache_write_credits_per_1m,\n  cache_read_credits_per_1m,\n  provider,\n  tier,\n  active,\n  notes\n) VALUES (\n  \u0027\(.key)\u0027,\n  \u0027\(.key | gsub("-"; " ") | ascii_upcase)\u0027,\n  \($input_credits),\n  \($output_credits),\n  " + 
  (if .value.cache_write_per_1m then 
    "(\($total_credits) * \(.value.cache_write_per_1m) / (\(.value.input_per_1m) + \(.value.output_per_1m)) | floor)" 
  else "NULL" end) + ",\n  " +
  (if .value.cache_read_per_1m then
    "(\($total_credits) * \(.value.cache_read_per_1m) / (\(.value.input_per_1m) + \(.value.output_per_1m)) | floor)"
  else "NULL" end) + ",\n  \u0027\(.value.vendor)\u0027,\n  \u0027\($tier)\u0027,\n  true,\n  \u0027Original price: $\(.value.input_per_1m)/$\(.value.output_per_1m) per 1M tokens. Relative to base: \($relative_price | floor). Source: \(.value.source_url)\u0027\n);\n"
' "$BASELINE_FILE"

echo ""
echo "COMMIT;"
echo ""
echo "-- 验证导入"
echo "SELECT "
echo "  tier,"
echo "  COUNT(*) as model_count,"
echo "  MIN(input_credits_per_1m + output_credits_per_1m) as min_total_credits,"
echo "  MAX(input_credits_per_1m + output_credits_per_1m) as max_total_credits"
echo "FROM model_pricing"
echo "WHERE active = true"
echo "GROUP BY tier"
echo "ORDER BY min_total_credits;"
