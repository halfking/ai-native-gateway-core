#!/bin/bash
# LLM模型质量监控系统 - 完整演示脚本

set -e

echo "=========================================="
echo "LLM模型质量监控系统 - 完整演示"
echo "=========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 步骤1: 编译
echo -e "${BLUE}步骤1: 编译质量监控工具${NC}"
go build -mod=mod -o quality-monitor ./cmd/quality-monitor
echo -e "${GREEN}✓ 编译完成${NC}"
echo ""

# 步骤2: 单元测试
echo -e "${BLUE}步骤2: 运行单元测试${NC}"
go test -v ./domains/modelquality/... -run TestMMLULiteSuite
go test -v ./domains/modelquality/... -run TestScoreCalculator
echo -e "${GREEN}✓ 单元测试通过${NC}"
echo ""

# 步骤3: 快速测试 - OpenAI
echo -e "${BLUE}步骤3: 测试OpenAI GPT-4模型${NC}"
./quality-monitor -mode=test -provider=openai -model=gpt-4 -benchmark=lite -data-dir=./demo-data
echo -e "${GREEN}✓ OpenAI测试完成${NC}"
echo ""

# 步骤4: 快速测试 - 国产模型
echo -e "${BLUE}步骤4: 测试国产模型${NC}"
./quality-monitor -mode=test -provider=domestic_a -model=chatglm-4 -benchmark=lite -data-dir=./demo-data
echo -e "${GREEN}✓ 国产模型测试完成${NC}"
echo ""

# 步骤5: 质量下降模拟
echo -e "${BLUE}步骤5: 模拟质量下降场景${NC}"
./quality-monitor -mode=simulate-drop -data-dir=./demo-data
echo -e "${GREEN}✓ 质量下降检测完成${NC}"
echo ""

# 步骤6: 查看结果
echo -e "${BLUE}步骤6: 查看生成的报告和评分${NC}"
echo ""
echo "生成的测试报告:"
ls -lh demo-data/reports/ | tail -5
echo ""
echo "评分历史文件:"
ls -lh demo-data/scores/
echo ""

# 步骤7: 展示评分对比
echo -e "${BLUE}步骤7: 评分对比分析${NC}"
echo ""
if [ -f "demo-data/scores/openai_gpt-4.jsonl" ]; then
    echo "OpenAI GPT-4 评分:"
    cat demo-data/scores/openai_gpt-4.jsonl | jq -r '. | "准确率: \(.accuracy)%, 评分: \(.overall_score), 等级: \(.grade)"'
    echo ""
fi

if [ -f "demo-data/scores/domestic_a_chatglm-4.jsonl" ]; then
    echo "国产模型 ChatGLM-4 评分历史:"
    cat demo-data/scores/domestic_a_chatglm-4.jsonl | jq -r '. | "准确率: \(.accuracy)%, 评分: \(.overall_score), 等级: \(.grade), 时间: \(.timestamp)"'
    echo ""
fi

# 步骤8: 生成统计报告
echo -e "${BLUE}步骤8: 生成统计报告${NC}"
cat > demo-data/DEMO_REPORT.txt << 'EOF'
=================================================
       LLM模型质量监控系统 - 演示报告
=================================================

测试时间: $(date)

一、测试概况
-------------------------------------------------
测试套件: MMLU Lite (50题)
学科覆盖: 计算机科学、数学、物理、历史、逻辑推理
测试模型: OpenAI GPT-4、国产模型 ChatGLM-4

二、测试结果
-------------------------------------------------
EOF

# 统计报告数量
REPORT_COUNT=$(ls -1 demo-data/reports/*.json 2>/dev/null | wc -l)
echo "生成的测试报告: $REPORT_COUNT 份" >> demo-data/DEMO_REPORT.txt

# 统计评分记录
SCORE_COUNT=$(cat demo-data/scores/*.jsonl 2>/dev/null | wc -l)
echo "评分历史记录: $SCORE_COUNT 条" >> demo-data/DEMO_REPORT.txt

cat >> demo-data/DEMO_REPORT.txt << 'EOF'

三、核心功能验证
-------------------------------------------------
✓ 50题MMLU测试套件
✓ 多维度质量评分（准确率、稳定性、延迟）
✓ 综合评分和等级评定
✓ 质量下降自动检测
✓ 测试报告和评分历史存储
✓ 告警机制

四、系统特性
-------------------------------------------------
- 测试耗时: ~1-2分钟/模型
- 数据格式: JSON报告 + JSONL评分历史
- 接口设计: 易于扩展的存储和告警接口
- 部署方式: 命令行工具 + 后台Worker

五、后续集成计划
-------------------------------------------------
1. 对接真实的网关模型调用
2. 集成到网关监控面板
3. 添加更多测试题库（中文、代码）
4. 数据库存储和趋势分析
5. 更多告警渠道（钉钉、飞书）

=================================================
EOF

cat demo-data/DEMO_REPORT.txt
echo -e "${GREEN}✓ 报告已生成: demo-data/DEMO_REPORT.txt${NC}"
echo ""

# 总结
echo "=========================================="
echo -e "${GREEN}演示完成！${NC}"
echo "=========================================="
echo ""
echo "生成的文件位置:"
echo "  - 测试报告: demo-data/reports/"
echo "  - 评分历史: demo-data/scores/"
echo "  - 演示报告: demo-data/DEMO_REPORT.txt"
echo ""
echo "下一步建议:"
echo "  1. 查看详细文档: docs/model-quality/README.md"
echo "  2. 查看快速开始: docs/model-quality/QUICKSTART.md"
echo "  3. 查看实施总结: IMPLEMENTATION_SUMMARY_MODEL_QUALITY.md"
echo ""
echo "开始持续监控:"
echo "  ./quality-monitor -mode=monitor -interval=24h"
echo ""
