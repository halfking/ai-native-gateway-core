#!/usr/bin/env bash
# 生成包含对外定价的 model_pricing 导入SQL
# 基于确定的档位倍率: Free=1.0×, Basic=1.5×, Standard=1.8×, Premium=2.0×, Enterprise=2.5×

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

BASELINE_FILE="bg/data/model_baseline_prices.json"
OUTPUT_SQL="${1:-/tmp/model_pricing_with_public_price_$(date +%Y%m%d).sql}"

# 档位倍率映射
declare -A TIER_MULTIPLIERS=(
  ["free"]="1.0"
  ["basic"]="1.5"
  ["standard"]="1.8"
  ["premium"]="2.0"
  ["enterprise"]="2.5"
)

echo "-- Model Pricing Import Script with Public Pricing"
echo "-- Generated at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "-- Source: $BASELINE_FILE"
echo "-- Base model: minimax-m3 (internal settlement price: 100 = USD \$1.50 per 1M tokens)"
echo "-- Pricing multipliers: Free=1.0×, Basic=1.5×, Standard=1.8×, Premium=2.0×, Enterprise=2.5×"
echo ""
echo "-- 价格体系说明:"
echo "-- 1. internal_credits: 内部结算价格(相对于基准模型的倍数×100)"
echo "-- 2. public_credits: 对外销售价格 = internal_credits × tier_multiplier"
echo "-- 3. margin: 毛利润 = public_credits - internal_credits"
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
  
  # 定义档位倍率
  def multiplier_for_tier(tier):
    if tier == "free" then 1.0
    elif tier == "basic" then 1.5
    elif tier == "standard" then 1.8
    elif tier == "premium" then 2.0
    elif tier == "enterprise" then 2.5
    else 1.0
    end;
  
  .models | to_entries[] | 
  
  # 计算内部结算价格
  ((.value.input_per_1m + .value.output_per_1m) / base_total * 100) as $relative_price |
  ($relative_price | floor) as $internal_total_credits |
  
  # 计算输入输出比例
  (.value.input_per_1m / (.value.input_per_1m + .value.output_per_1m)) as $input_ratio |
  ($internal_total_credits * $input_ratio | floor) as $internal_input_credits |
  ($internal_total_credits - $internal_input_credits) as $internal_output_credits |
  
  # 确定档位和倍率
  tier_for_relative($relative_price) as $tier |
  multiplier_for_tier($tier) as $multiplier |
  
  # 计算对外价格
  ($internal_total_credits * $multiplier | floor) as $public_total_credits |
  ($public_total_credits * $input_ratio | floor) as $public_input_credits |
  ($public_total_credits - $public_input_credits) as $public_output_credits |
  
  # 计算毛利
  ($public_total_credits - $internal_total_credits) as $margin_total |
  
  # 生成 INSERT 语句
  "-- Model: \(.key) (\(.value.vendor))\n" +
  "-- Original price: $\(.value.input_per_1m)/$\(.value.output_per_1m) per 1M tokens\n" +
  "-- Internal settlement: \($internal_total_credits) credits (relative to base: \($relative_price | floor))\n" +
  "-- Tier: \($tier) (multiplier: \($multiplier)×)\n" +
  "-- Public price: \($public_total_credits) credits\n" +
  "-- Margin: \($margin_total) credits (\(($margin_total / $public_total_credits * 100) | floor)% gross margin)\n" +
  "INSERT INTO model_pricing (\n" +
  "  model_canonical,\n" +
  "  display_name,\n" +
  "  input_credits_per_1m,\n" +
  "  output_credits_per_1m,\n" +
  "  cache_write_credits_per_1m,\n" +
  "  cache_read_credits_per_1m,\n" +
  "  provider,\n" +
  "  tier,\n" +
  "  active,\n" +
  "  notes\n" +
  ") VALUES (\n" +
  "  \u0027\(.key)\u0027,\n" +
  "  \u0027\(.key | gsub("-"; " ") | ascii_upcase)\u0027,\n" +
  "  \($public_input_credits),  -- public price (internal: \($internal_input_credits))\n" +
  "  \($public_output_credits),  -- public price (internal: \($internal_output_credits))\n" +
  "  " + 
  (if .value.cache_write_per_1m then 
    "\(($public_total_credits * .value.cache_write_per_1m / (.value.input_per_1m + .value.output_per_1m)) | floor)"
  else "NULL" end) + ",\n  " +
  (if .value.cache_read_per_1m then
    "\(($public_total_credits * .value.cache_read_per_1m / (.value.input_per_1m + .value.output_per_1m)) | floor)"
  else "NULL" end) + ",\n" +
  "  \u0027\(.value.vendor)\u0027,\n" +
  "  \u0027\($tier)\u0027,\n" +
  "  true,\n" +
  "  \u0027Original: $\(.value.input_per_1m)/$\(.value.output_per_1m). Internal: \($internal_total_credits) credits. Public: \($public_total_credits) credits (\($multiplier)× multiplier). Margin: \($margin_total) credits. Source: \(.value.source_url)\u0027\n" +
  ");\n"
' "$BASELINE_FILE"

echo ""
echo "COMMIT;"
echo ""
echo "-- ═══════════════════════════════════════════════════════════════════════════"
echo "-- 验证查询"
echo "-- ═══════════════════════════════════════════════════════════════════════════"
echo ""
echo "-- 1. 按档位统计模型数量和价格范围"
echo "SELECT "
echo "  tier,"
echo "  COUNT(*) as model_count,"
echo "  MIN(input_credits_per_1m + output_credits_per_1m) as min_total_credits,"
echo "  MAX(input_credits_per_1m + output_credits_per_1m) as max_total_credits,"
echo "  AVG(input_credits_per_1m + output_credits_per_1m)::integer as avg_total_credits"
echo "FROM model_pricing"
echo "WHERE active = true"
echo "GROUP BY tier"
echo "ORDER BY min_total_credits;"
echo ""
echo "-- 2. 查看所有模型的对外价格(credits)"
echo "SELECT "
echo "  model_canonical,"
echo "  tier,"
echo "  (input_credits_per_1m + output_credits_per_1m) as public_total_credits,"
echo "  provider"
echo "FROM model_pricing"
echo "WHERE active = true"
echo "ORDER BY public_total_credits;"
echo ""
echo "-- 3. 换算成USD价格(基准: 100 credits = \$1.50)"
echo "SELECT "
echo "  model_canonical,"
echo "  tier,"
echo "  ROUND((input_credits_per_1m + output_credits_per_1m) * 1.5 / 100, 2) as public_usd_per_1m,"
echo "  provider"
echo "FROM model_pricing"
echo "WHERE active = true"
echo "ORDER BY public_usd_per_1m;"
