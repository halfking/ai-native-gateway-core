#!/usr/bin/env bash
# fetch-pricing.sh — 一键抓取各厂商最新价目 (agent-reach 路由)
# 输出: docs/pricing/raw/{vendor}.md
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUT="$REPO_ROOT/services/llm-gateway-go/docs/pricing/raw"
mkdir -p "$OUT"

fetch() {
  local vendor="$1" url="$2"
  echo "📥 Fetching $vendor ..."
  curl -sL --max-time 30 "https://r.jina.ai/$url" \
    -H "User-Agent: Mozilla/5.0" > "$OUT/${vendor}.md"
  echo "  → $OUT/${vendor}.md ($(wc -c < "$OUT/${vendor}.md") bytes)"
}

# Tier 1+2 厂商官方价目
fetch openai       "https://platform.openai.com/docs/models"
fetch openai-pricing "https://platform.openai.com/docs/pricing"
fetch anthropic    "https://docs.anthropic.com/en/docs/about-claude/models/overview"
fetch google-gemini "https://ai.google.dev/gemini-api/docs/models"
fetch deepseek     "https://api-docs.deepseek.com/quick_start/pricing"
fetch xai          "https://docs.x.ai/docs/pricing"
fetch xai-models   "https://docs.x.ai/docs/models"
fetch zhipu        "https://open.bigmodel.cn/pricing"
fetch minimax      "https://platform.minimax.io/docs/guides/pricing-paygo"
fetch minimax-tokenplan "https://platform.minimax.io/docs/guides/pricing-token-plan"
fetch moonshot     "https://platform.moonshot.ai/docs/pricing/chat"
fetch mistral      "https://docs.mistral.ai/getting-started/models/models_overview"
fetch doubao       "https://www.volcengine.com/docs/82379/1544106"

# 聚合站（交叉验证）
fetch openrouter   "https://openrouter.ai/api/v1/models"
fetch artificial-analysis "https://artificialanalysis.ai"

echo ""
echo "✅ Done. $OUT has $(ls $OUT | wc -l) files."
ls -la "$OUT"
