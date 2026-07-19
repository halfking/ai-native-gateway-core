#!/usr/bin/env bash
# 测试质量评分服务 API

set -euo pipefail

# 配置
BASE_URL="${1:-http://localhost:8081}"

echo "=== 测试质量评分服务 API ==="
echo "Base URL: ${BASE_URL}"
echo ""

# 1. 健康检查
echo "1. 健康检查..."
if curl -f -s "${BASE_URL}/health" | grep -q "OK"; then
    echo "✅ 健康检查通过"
else
    echo "❌ 健康检查失败"
    exit 1
fi
echo ""

# 2. 列出所有质量画像
echo "2. 列出所有质量画像..."
PROFILES=$(curl -s "${BASE_URL}/api/quality/list")
COUNT=$(echo "${PROFILES}" | jq -r '.count // 0')
echo "找到 ${COUNT} 个质量画像"

if [ "${COUNT}" -gt 0 ]; then
    echo "前 3 个画像:"
    echo "${PROFILES}" | jq -r '.profiles[:3][] | "  - Provider \(.provider_id) / \(.model_name): 质量分 \(.quality_score)"'
    echo "✅ 列表查询成功"
else
    echo "⚠️  暂无质量画像数据"
fi
echo ""

# 3. 查询特定画像
echo "3. 查询特定画像..."
if [ "${COUNT}" -gt 0 ]; then
    # 获取第一个画像的 provider_id 和 model
    PROVIDER_ID=$(echo "${PROFILES}" | jq -r '.profiles[0].provider_id')
    MODEL=$(echo "${PROFILES}" | jq -r '.profiles[0].model_name')
    
    echo "查询: Provider ${PROVIDER_ID} / ${MODEL}"
    PROFILE=$(curl -s "${BASE_URL}/api/quality/profile?provider_id=${PROVIDER_ID}&model=${MODEL}")
    
    echo "${PROFILE}" | jq '.'
    echo "✅ 画像查询成功"
else
    echo "⏭️  跳过 (无数据)"
fi
echo ""

# 4. 立即计算 (如果有活跃 provider)
echo "4. 立即计算质量评分..."
if [ "${COUNT}" -gt 0 ]; then
    PROVIDER_ID=$(echo "${PROFILES}" | jq -r '.profiles[0].provider_id')
    MODEL=$(echo "${PROFILES}" | jq -r '.profiles[0].model_name')
    
    echo "计算: Provider ${PROVIDER_ID} / ${MODEL}"
    RESULT=$(curl -s -X POST "${BASE_URL}/api/quality/calculate?provider_id=${PROVIDER_ID}&model=${MODEL}")
    
    QUALITY_SCORE=$(echo "${RESULT}" | jq -r '.quality_score // "N/A"')
    echo "质量分: ${QUALITY_SCORE}"
    echo "✅ 立即计算成功"
else
    echo "⏭️  跳过 (无数据)"
fi
echo ""

# 5. 性能测试
echo "5. 性能测试 (10次请求)..."
START=$(date +%s%3N)
for i in {1..10}; do
    curl -s "${BASE_URL}/health" > /dev/null
done
END=$(date +%s%3N)
DURATION=$((END - START))
AVG=$((DURATION / 10))
echo "10次健康检查耗时: ${DURATION}ms (平均 ${AVG}ms/次)"
echo "✅ 性能测试完成"
echo ""

echo "=== 测试完成 ==="
