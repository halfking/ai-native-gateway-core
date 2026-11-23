#!/usr/bin/env bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# 价格数据采集脚本 - 整合原厂价格、供应商价格和内部结算价格
# 
# 用法:
#   ./scripts/pricing/collect-pricing-data.sh -out /tmp/pricing-report.json
#
# 输出包含:
#   1. 原厂标准价格 (从 bg/data/model_baseline_prices.json)
#   2. 供应商实际价格 (从提案工具提取的最新快照)
#   3. 内部结算价格 (以最便宜模型为100基数的相对价格)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# 默认输出路径
OUTPUT_FILE="${OUTPUT_FILE:-/tmp/pricing-collection-$(date +%Y%m%d-%H%M%S).json}"
RAW_DIR="docs/02-resources/research/pricing/raw"
BASELINE_FILE="bg/data/model_baseline_prices.json"

# 解析命令行参数
while [[ $# -gt 0 ]]; do
  case $1 in
    -out)
      OUTPUT_FILE="$2"
      shift 2
      ;;
    -raw)
      RAW_DIR="$2"
      shift 2
      ;;
    *)
      echo "未知参数: $1" >&2
      echo "用法: $0 [-out OUTPUT_FILE] [-raw RAW_DIR]" >&2
      exit 1
      ;;
  esac
done

echo "═══════════════════════════════════════════════════════════"
echo "价格数据采集任务"
echo "═══════════════════════════════════════════════════════════"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "原厂快照目录: $RAW_DIR"
echo "基准价格文件: $BASELINE_FILE"
echo "输出文件: $OUTPUT_FILE"
echo ""

# 步骤1: 提取最新的原厂价格提案
echo "━━━ 步骤 1/4: 提取原厂价格提案 ━━━"
PROPOSAL_FILE="/tmp/baseline-proposal-$(date +%Y%m%d-%H%M%S).json"

if go run ./cmd/tools/propose-baseline-prices \
  -raw "$RAW_DIR" \
  -out "$PROPOSAL_FILE" 2>&1 | tee /tmp/pricing-extract.log; then
  echo "✓ 原厂价格提案已生成: $PROPOSAL_FILE"
  
  # 统计提取结果
  READY_COUNT=$(jq '.ready_to_review | length' "$PROPOSAL_FILE")
  UNUSABLE_COUNT=$(jq '.counts_by_confidence.unusable // 0' "$PROPOSAL_FILE")
  echo "  - 可用价格: $READY_COUNT 条"
  echo "  - 不可用: $UNUSABLE_COUNT 条"
else
  echo "⚠ 原厂价格提取有警告,请查看日志: /tmp/pricing-extract.log"
fi
echo ""

# 步骤2: 读取现有基准价格
echo "━━━ 步骤 2/4: 读取基准价格 SSOT ━━━"
if [[ ! -f "$BASELINE_FILE" ]]; then
  echo "✗ 基准价格文件不存在: $BASELINE_FILE"
  exit 1
fi

BASELINE_COUNT=$(jq '.models | keys | length' "$BASELINE_FILE")
echo "✓ 基准价格文件包含 $BASELINE_COUNT 个模型"
echo ""

# 步骤3: 计算内部结算价格基数
echo "━━━ 步骤 3/4: 计算内部结算价格 ━━━"
echo "规则: 以最便宜模型的原厂价格作为100基数"
echo "      其他模型按原厂定价倍率计算相对价格"
echo ""

# 使用jq计算最便宜的模型
MIN_PRICE_MODEL=$(jq -r '
  .models 
  | to_entries 
  | map({
      key: .key, 
      value: .value,
      total_price: (.value.input_per_1m + .value.output_per_1m)
    }) 
  | sort_by(.total_price) 
  | .[0] 
  | {
      model: .key, 
      input: .value.input_per_1m, 
      output: .value.output_per_1m, 
      total: .total_price,
      vendor: .value.vendor
    }
' "$BASELINE_FILE")

BASE_MODEL=$(echo "$MIN_PRICE_MODEL" | jq -r '.model')
BASE_INPUT=$(echo "$MIN_PRICE_MODEL" | jq -r '.input')
BASE_OUTPUT=$(echo "$MIN_PRICE_MODEL" | jq -r '.output')
BASE_TOTAL=$(echo "$MIN_PRICE_MODEL" | jq -r '.total')
BASE_VENDOR=$(echo "$MIN_PRICE_MODEL" | jq -r '.vendor')

echo "✓ 最便宜模型: $BASE_MODEL ($BASE_VENDOR)"
echo "  - 输入价格: \$$BASE_INPUT / 1M tokens"
echo "  - 输出价格: \$$BASE_OUTPUT / 1M tokens"
echo "  - 总价: \$$BASE_TOTAL / 1M tokens"
echo "  - 内部基数: 100"
echo ""

# 步骤4: 生成综合报告
echo "━━━ 步骤 4/4: 生成综合价格报告 ━━━"

jq -n \
  --arg timestamp "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg base_model "$BASE_MODEL" \
  --argjson base_input "$BASE_INPUT" \
  --argjson base_output "$BASE_OUTPUT" \
  --argjson base_total "$BASE_TOTAL" \
  --arg base_vendor "$BASE_VENDOR" \
  --slurpfile baseline "$BASELINE_FILE" \
  --slurpfile proposal "$PROPOSAL_FILE" \
  '{
    generated_at: $timestamp,
    summary: {
      baseline_models: ($baseline[0].models | keys | length),
      proposal_ready: ($proposal[0].ready_to_review | length),
      proposal_unusable: ($proposal[0].counts_by_confidence.unusable // 0)
    },
    internal_settlement_base: {
      model: $base_model,
      vendor: $base_vendor,
      original_price: {
        input_per_1m: $base_input,
        output_per_1m: $base_output,
        total_per_1m: $base_total,
        currency: "USD"
      },
      internal_base_value: 100,
      note: "所有其他模型的内部结算价格将基于此模型的原厂价格按倍率计算"
    },
    baseline_prices: $baseline[0].models,
    vendor_proposals: $proposal[0].ready_to_review,
    pricing_tiers: [
      {
        tier: "free",
        description: "免费档位",
        target_margin: "0%",
        use_case: "公开免费模型,吸引用户"
      },
      {
        tier: "basic", 
        description: "基础档位",
        target_margin: "20-30%",
        use_case: "入门级付费模型"
      },
      {
        tier: "standard",
        description: "标准档位", 
        target_margin: "30-50%",
        use_case: "主力商业模型"
      },
      {
        tier: "premium",
        description: "高级档位",
        target_margin: "50-80%",
        use_case: "旗舰模型和高端需求"
      },
      {
        tier: "enterprise",
        description: "企业档位",
        target_margin: "定制定价",
        use_case: "大客户专属方案"
      }
    ]
  }' > "$OUTPUT_FILE"

echo "✓ 综合价格报告已生成: $OUTPUT_FILE"
echo ""

# 生成易读的摘要
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "采集摘要"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
jq -r '
  "基准价格模型数: \(.summary.baseline_models)",
  "最新提案可用: \(.summary.proposal_ready)",
  "最新提案不可用: \(.summary.proposal_unusable)",
  "",
  "内部结算基准:",
  "  模型: \(.internal_settlement_base.model)",
  "  厂商: \(.internal_settlement_base.vendor)", 
  "  原厂价格: $\(.internal_settlement_base.original_price.input_per_1m) / $\(.internal_settlement_base.original_price.output_per_1m)",
  "  内部基数: \(.internal_settlement_base.internal_base_value)"
' "$OUTPUT_FILE"

echo ""
echo "═══════════════════════════════════════════════════════════"
echo "采集完成!"
echo "下一步: 审查 $OUTPUT_FILE 并决定:"
echo "  1. 哪些新价格应该更新到 $BASELINE_FILE"
echo "  2. 如何设置各档位的具体倍率"
echo "  3. 供应商价格与原厂价格的差异分析"
echo "═══════════════════════════════════════════════════════════"
