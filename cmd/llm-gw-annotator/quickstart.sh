#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
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
# P2.1 Human Annotation Workflow - 快速开始脚本
# 
# 用途: 演示完整的标注工作流
# 
# 使用前提:
#   1. 已运行Migration 664
#   2. 已编译llm-gw-annotator
#   3. 已设置DATABASE_URL环境变量

set -e

echo "=========================================="
echo "P2.1 Human Annotation Workflow - Quick Start"
echo "=========================================="
echo ""

# 配置
START_DATE="2026-08-01"
END_DATE="2026-09-01"
MAX_CONFIDENCE="0.7"
LIMIT="100"
OUTPUT_CSV="annotations_quickstart.csv"

# 检查环境
if [ -z "$DATABASE_URL" ]; then
    echo "❌ Error: DATABASE_URL not set"
    echo "   Please set: export DATABASE_URL='postgresql://user:pass@host:5432/dbname'"
    exit 1
fi

if [ ! -f "./llm-gw-annotator" ]; then
    echo "❌ Error: llm-gw-annotator not found"
    echo "   Please build: cd cmd/llm-gw-annotator && go build"
    exit 1
fi

# Step 1: 导出低置信度样本
echo "Step 1: Exporting low-confidence samples..."
echo "----------------------------------------"
./llm-gw-annotator export \
    --start "$START_DATE" \
    --end "$END_DATE" \
    --max-confidence "$MAX_CONFIDENCE" \
    --limit "$LIMIT" \
    --output "$OUTPUT_CSV"

if [ ! -f "$OUTPUT_CSV" ]; then
    echo "❌ Export failed: $OUTPUT_CSV not created"
    exit 1
fi

echo ""
echo "✅ Export completed: $OUTPUT_CSV"
echo ""

# 显示CSV前5行
echo "CSV Preview (first 5 rows):"
echo "----------------------------------------"
head -5 "$OUTPUT_CSV"
echo "..."
echo ""

# Step 2: 人工标注（这里只是示例，实际需要人工编辑）
echo "Step 2: Human annotation (simulated)"
echo "----------------------------------------"
echo "In real workflow, you would:"
echo "  1. Open $OUTPUT_CSV in Excel/Google Sheets"
echo "  2. Fill in: human_provider, is_correct, reason, annotator"
echo "  3. Save the file"
echo ""
echo "For this demo, we'll create a mock annotated file..."

# 创建模拟标注文件（只取前3行）
ANNOTATED_CSV="annotations_annotated_demo.csv"
head -1 "$OUTPUT_CSV" > "$ANNOTATED_CSV"  # header

# 添加3行模拟标注
if [ $(wc -l < "$OUTPUT_CSV") -gt 1 ]; then
    # 第1行：正确预测
    sed -n '2p' "$OUTPUT_CSV" | sed 's/,,,$/,openai,TRUE,correct,demo_annotator/' >> "$ANNOTATED_CSV"
    
    # 第2行：错误预测（如果有）
    if [ $(wc -l < "$OUTPUT_CSV") -gt 2 ]; then
        sed -n '3p' "$OUTPUT_CSV" | sed 's/,,,$/,anthropic,FALSE,cost,demo_annotator/' >> "$ANNOTATED_CSV"
    fi
    
    # 第3行：错误预测（如果有）
    if [ $(wc -l < "$OUTPUT_CSV") -gt 3 ]; then
        sed -n '4p' "$OUTPUT_CSV" | sed 's/,,,$/,aws_bedrock,FALSE,performance,demo_annotator/' >> "$ANNOTATED_CSV"
    fi
fi

echo "Created demo annotated file: $ANNOTATED_CSV"
echo ""

# Step 3: 验证CSV格式
echo "Step 3: Validating CSV format..."
echo "----------------------------------------"
./llm-gw-annotator validate "$ANNOTATED_CSV"

if [ $? -ne 0 ]; then
    echo "❌ Validation failed"
    exit 1
fi

echo ""

# Step 4: 导入标注结果
echo "Step 4: Importing annotations..."
echo "----------------------------------------"
./llm-gw-annotator import "$ANNOTATED_CSV"

if [ $? -ne 0 ]; then
    echo "❌ Import failed"
    exit 1
fi

echo ""

# Step 5: 查看统计
echo "Step 5: Viewing statistics..."
echo "----------------------------------------"
./llm-gw-annotator stats

echo ""
echo "=========================================="
echo "✅ Quick Start Demo Completed!"
echo "=========================================="
echo ""
echo "What you learned:"
echo "  1. How to export low-confidence samples"
echo "  2. What the CSV format looks like"
echo "  3. How to validate CSV before import"
echo "  4. How to import annotations"
echo "  5. How to view statistics"
echo ""
echo "Next steps:"
echo "  1. Review the exported CSV: $OUTPUT_CSV"
echo "  2. Try manual annotation in Excel/Sheets"
echo "  3. Read the deployment guide:"
echo "     docs/deployment/p2.1-human-annotation-workflow.md"
echo ""
echo "Clean up demo files:"
echo "  rm $OUTPUT_CSV $ANNOTATED_CSV"
echo ""
