#!/usr/bin/env bash
# 验证已应用补丁的正确性

DOC="01-需求分析与架构设计.md"

echo "========================================"
echo "补丁验证脚本 - V3.1"
echo "========================================"
echo ""

PASS=0
FAIL=0

# 检查函数
check() {
    local name="$1"
    local pattern="$2"
    
    if grep -q "$pattern" "$DOC"; then
        echo "✅ $name"
        ((PASS++))
    else
        echo "❌ $name - 未找到: $pattern"
        ((FAIL++))
    fi
}

echo "📋 验证已应用的补丁..."
echo ""

# Patch 12: 文档元信息
check "Patch 12 - 版本号" "版本: v3.1"
check "Patch 12 - 核心变更" "## 核心变更（V3.0 → V3.1）"

# Patch 01: 需求深化
check "Patch 01 - 需求深化" "## 1.5 需求深化（V3.1 补充）"
check "Patch 01 - 队列处理不清晰" "### 1.5.1 队列处理流程不清晰"
check "Patch 01 - 会话层级需深化" "### 1.5.2 会话层级结构需要深化"

# Patch 02: 队列生命周期
check "Patch 02 - 队列生命周期" "#### 2.2.6 请求在三层队列中的生命周期"
check "Patch 02 - 9阶段定义" "9 阶段定义"

# Patch 03: 数据结构
check "Patch 03 - 新增数据结构" "### 2.3 V3.1 新增数据结构"
check "Patch 03 - 队列时间戳" "QueuedRequestWithTimestamps"

# Patch 04: 会话层级
check "Patch 04 - 会话层级设计" "## 2.4 会话层级结构设计（V3.1 新增）"
check "Patch 04 - 五级层级" "### 2.4.1 五级层级定义"
check "Patch 04 - Schema扩展" "### 2.4.2 数据库 Schema 扩展"
check "Patch 04 - 健康度评分" "### 2.4.3 会话健康度评分算法"

# Patch 05: P0功能清单
check "Patch 05 - P0更新" "### 3.1 P0 功能（核心必做）- V3.1 更新"
check "Patch 05 - 队列瀑布流" "#### 3.1.1 队列流转可视化（V3.1 新增 ⭐核心价值）"
check "Patch 05 - 会话层级功能" "#### 3.1.2 会话层级结构（V3.1 新增 ⭐核心价值）"

echo ""
echo "========================================"
echo "验证结果"
echo "========================================"
echo "✅ 通过: $PASS"
echo "❌ 失败: $FAIL"
echo ""

if [ $FAIL -eq 0 ]; then
    echo "🎉 所有补丁验证通过！"
    exit 0
else
    echo "⚠️  部分补丁验证失败，请检查"
    exit 1
fi
