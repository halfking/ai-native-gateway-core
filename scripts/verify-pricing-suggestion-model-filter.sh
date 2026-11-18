#!/usr/bin/env bash
# verify-pricing-suggestion-model-filter.sh —— 验证定价建议的模型过滤功能
#
# 使用方法：
#   ./scripts/verify-pricing-suggestion-model-filter.sh
#
# 验证要点：
#   1. 前端页面加载正常，模型选择器可见
#   2. 可以选择多个模型
#   3. 不选择模型时预览所有模型
#   4. 选择特定模型后只预览那些模型
#   5. canonical_ids 参数正确传递到后端

set -euo pipefail

BASE_URL="https://llm.itestu.cn"
PRICING_SUGGESTION_URL="${BASE_URL}/operations/price-compare#suggestion"

echo "=========================================="
echo "定价建议模型过滤功能验证"
echo "=========================================="
echo ""
echo "验证目标：${PRICING_SUGGESTION_URL}"
echo ""
echo "手动验证步骤："
echo ""
echo "1. 打开页面："
echo "   ${PRICING_SUGGESTION_URL}"
echo ""
echo "2. 检查模型选择器："
echo "   - 在策略条下方应该有一个模型选择卡片"
echo "   - 卡片中有 ModelPicker 组件（紧凑模式）"
echo "   - 提示文字：'不选择任何模型时，对所有模型定价；选择后只对选中的模型定价。'"
echo ""
echo "3. 测试场景 A - 不选择任何模型："
echo "   - 保持模型选择器为空"
echo "   - 点击「预览建议」"
echo "   - 应该显示所有有行情的模型（与之前行为一致）"
echo ""
echo "4. 测试场景 B - 选择单个模型："
echo "   - 点击模型选择器"
echo "   - 选择一个模型（如 gpt-4o）"
echo "   - 点击「确认」"
echo "   - 点击「预览建议」"
echo "   - 应该只显示该模型的定价建议"
echo ""
echo "5. 测试场景 C - 选择多个模型："
echo "   - 点击模型选择器"
echo "   - 选择多个模型（如 gpt-4o, claude-3-5-sonnet）"
echo "   - 点击「确认」"
echo "   - 点击「预览建议」"
echo "   - 应该只显示这些模型的定价建议"
echo ""
echo "6. 检查网络请求（浏览器开发者工具）："
echo "   - 打开 Network 标签"
echo "   - 执行预览操作"
echo "   - 查看 POST /api/admin/market/pricing-suggestions 请求"
echo "   - 请求体应包含 canonical_ids 数组："
echo "     - 未选择时：canonical_ids: []"
echo "     - 选择模型后：canonical_ids: [123, 456] (具体ID)"
echo ""
echo "7. 验证执行功能："
echo "   - 选择测试模型"
echo "   - 点击「预览建议」确认有可执行的建议"
echo "   - 点击「一键执行」"
echo "   - 应弹出确认对话框"
echo "   - 确认后应该只更新选中模型的价格"
echo ""
echo "=========================================="
echo "验证完成后的预期结果："
echo "=========================================="
echo ""
echo "✓ 模型选择器UI正常显示"
echo "✓ 可以选择单个或多个模型"
echo "✓ 不选择时对所有模型定价（默认行为）"
echo "✓ 选择后只对选中模型定价"
echo "✓ canonical_ids 参数正确传递"
echo "✓ 执行功能正常工作"
echo ""
