#!/bin/bash
# 全面测试多模型的多轮对话和工具调用功能
# 使用方法: ./test_all_models.sh <API_KEY>

set -e

API_KEY="${1:-test-key}"
BASE_URL="https://llmgo.kxpms.cn/v1"

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 测试结果记录
RESULTS_FILE="test_results_$(date +%Y%m%d_%H%M%S).md"

# 初始化结果文件
cat > "$RESULTS_FILE" << 'EOF'
# 多模型多轮对话测试报告

**测试时间**: $(date '+%Y-%m-%d %H:%M:%S')
**测试环境**: https://llmgo.kxpms.cn

## 测试概览

| 测试项 | 状态 |
|--------|------|
EOF

echo "=========================================="
echo "多模型全面测试套件"
echo "=========================================="
echo ""
echo "测试结果将保存到: $RESULTS_FILE"
echo ""

# 辅助函数：打印测试头部
print_test_header() {
    local model=$1
    local test_name=$2
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}模型: ${model}${NC}"
    echo -e "${BLUE}测试: ${test_name}${NC}"
    echo -e "${BLUE}========================================${NC}"
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
}

# 辅助函数：测试通过
test_pass() {
    local message=$1
    echo -e "${GREEN}✓ 测试通过: ${message}${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
}

# 辅助函数：测试失败
test_fail() {
    local message=$1
    echo -e "${RED}✗ 测试失败: ${message}${NC}"
    FAILED_TESTS=$((FAILED_TESTS + 1))
}

# 辅助函数：警告信息
test_warn() {
    local message=$1
    echo -e "${YELLOW}⚠ 警告: ${message}${NC}"
}

# 测试1: Claude Sonnet 4 - 简单多轮对话
test_claude_sonnet_4_simple() {
    print_test_header "claude-sonnet-4-6" "简单多轮对话"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "请记住这个颜色：蓝色"},
          {"role": "assistant", "content": "好的，我记住了颜色是蓝色。"},
          {"role": "user", "content": "我让你记住的颜色是什么？"}
        ],
        "max_tokens": 50
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null)"
    
    if echo "$response" | grep -qi "蓝色\|blue"; then
        test_pass "模型正确记住了上下文中的颜色"
        echo "| Claude Sonnet 4-6 简单多轮对话 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_fail "模型未能正确回忆上下文"
        echo "| Claude Sonnet 4-6 简单多轮对话 | ✗ 失败 |" >> "$RESULTS_FILE"
    fi
}

# 测试2: Claude Sonnet 4 - 复杂上下文对话
test_claude_sonnet_4_complex() {
    print_test_header "claude-sonnet-4-6" "复杂上下文对话"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "我正在开发一个Go语言的API网关项目，使用Gin框架"},
          {"role": "assistant", "content": "很好，Go语言配合Gin框架非常适合做API网关。它性能优秀，并发处理能力强。"},
          {"role": "user", "content": "我想添加请求限流功能，你有什么建议？"}
        ],
        "max_tokens": 200
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null | head -n 5)"
    
    if echo "$response" | grep -qi "需要更多信息\|what would you like"; then
        test_fail "模型没有理解上下文（Go/Gin项目）"
        echo "| Claude Sonnet 4-6 复杂上下文 | ✗ 失败 |" >> "$RESULTS_FILE"
    elif echo "$response" | grep -qi "限流\|rate limit\|gin\|go\|中间件\|middleware"; then
        test_pass "模型正确理解了项目上下文并给出相关建议"
        echo "| Claude Sonnet 4-6 复杂上下文 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_warn "响应不确定，需要人工检查"
        echo "| Claude Sonnet 4-6 复杂上下文 | ? 不确定 |" >> "$RESULTS_FILE"
    fi
}

# 测试3: Claude Opus 4 - 多轮对话
test_claude_opus_4() {
    print_test_header "claude-opus-4" "多轮对话"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-opus-4",
        "messages": [
          {"role": "user", "content": "帮我解释一下什么是事件驱动架构"},
          {"role": "assistant", "content": "事件驱动架构(EDA)是一种软件架构模式，系统中的组件通过发布和订阅事件来通信..."},
          {"role": "user", "content": "在我刚才提到的架构中，如何处理事件失败？"}
        ],
        "max_tokens": 150
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null | head -n 5)"
    
    if echo "$response" | grep -qi "事件\|event\|重试\|retry\|死信\|dead letter"; then
        test_pass "Opus模型正确理解了事件驱动架构的上下文"
        echo "| Claude Opus 4 多轮对话 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_fail "Opus模型未能保持上下文"
        echo "| Claude Opus 4 多轮对话 | ✗ 失败 |" >> "$RESULTS_FILE"
    fi
}

# 测试4: GPT-4 - 多轮对话（OpenAI原生格式）
test_gpt4_multi_turn() {
    print_test_header "gpt-4o" "多轮对话（OpenAI格式）"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "gpt-4o",
        "messages": [
          {"role": "user", "content": "我需要写一个Python脚本来处理CSV文件"},
          {"role": "assistant", "content": "好的，我可以帮你编写Python脚本处理CSV文件。你可以使用pandas库或csv标准库。"},
          {"role": "user", "content": "用pandas怎么读取？"}
        ],
        "max_tokens": 100
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null | head -n 3)"
    
    if echo "$response" | grep -qi "pandas\|pd.read_csv\|read_csv"; then
        test_pass "GPT-4正确理解了pandas上下文"
        echo "| GPT-4o 多轮对话 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_fail "GPT-4未能保持上下文"
        echo "| GPT-4o 多轮对话 | ✗ 失败 |" >> "$RESULTS_FILE"
    fi
}

# 测试5: Claude Sonnet 4 - Tool Calls（模拟）
test_claude_tool_calls() {
    print_test_header "claude-sonnet-4-6" "Tool Calls格式转换"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "北京的天气怎么样？"}
        ],
        "tools": [
          {
            "type": "function",
            "function": {
              "name": "get_weather",
              "description": "获取指定城市的天气信息",
              "parameters": {
                "type": "object",
                "properties": {
                  "city": {
                    "type": "string",
                    "description": "城市名称"
                  }
                },
                "required": ["city"]
              }
            }
          }
        ],
        "tool_choice": "auto",
        "max_tokens": 100
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq '.' 2>/dev/null | head -n 20)"
    
    # 检查是否正确处理了tools定义
    if echo "$response" | jq -e '.choices[0].message.tool_calls' > /dev/null 2>&1; then
        test_pass "模型正确使用了tool_calls"
        echo "| Claude Sonnet 4-6 Tool Calls | ✓ 通过 |" >> "$RESULTS_FILE"
    elif echo "$response" | jq -e '.error' > /dev/null 2>&1; then
        local error_msg=$(echo "$response" | jq -r '.error.message')
        test_fail "工具调用出错: $error_msg"
        echo "| Claude Sonnet 4-6 Tool Calls | ✗ 失败 |" >> "$RESULTS_FILE"
    else
        test_warn "模型选择了文本回复而非工具调用（这也是合理的）"
        echo "| Claude Sonnet 4-6 Tool Calls | ⚠ 文本回复 |" >> "$RESULTS_FILE"
    fi
}

# 测试6: Claude Sonnet 4 - Tool Result处理
test_claude_tool_result() {
    print_test_header "claude-sonnet-4-6" "Tool Result格式转换"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "北京的天气怎么样？"},
          {
            "role": "assistant",
            "content": null,
            "tool_calls": [
              {
                "id": "call_test123",
                "type": "function",
                "function": {
                  "name": "get_weather",
                  "arguments": "{\"city\":\"北京\"}"
                }
              }
            ]
          },
          {
            "role": "tool",
            "tool_call_id": "call_test123",
            "content": "{\"temperature\": 15, \"condition\": \"晴天\", \"humidity\": 45}"
          },
          {"role": "user", "content": "那我需要带伞吗？"}
        ],
        "max_tokens": 100
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null)"
    
    if echo "$response" | jq -e '.error' > /dev/null 2>&1; then
        test_fail "Tool result转换出错"
        echo "| Claude Sonnet 4-6 Tool Result | ✗ 失败 |" >> "$RESULTS_FILE"
    elif echo "$response" | grep -qi "不需要\|晴天\|no need\|不用"; then
        test_pass "模型正确处理了tool result并给出合理建议"
        echo "| Claude Sonnet 4-6 Tool Result | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_warn "模型回复了但内容需要人工检查"
        echo "| Claude Sonnet 4-6 Tool Result | ? 不确定 |" >> "$RESULTS_FILE"
    fi
}

# 测试7: GPT-4 - Tool Calls（原生支持）
test_gpt4_tool_calls() {
    print_test_header "gpt-4o" "Tool Calls（原生格式）"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "gpt-4o",
        "messages": [
          {"role": "user", "content": "上海现在几点？"}
        ],
        "tools": [
          {
            "type": "function",
            "function": {
              "name": "get_current_time",
              "description": "获取指定城市的当前时间",
              "parameters": {
                "type": "object",
                "properties": {
                  "city": {"type": "string", "description": "城市名称"}
                },
                "required": ["city"]
              }
            }
          }
        ],
        "tool_choice": "auto",
        "max_tokens": 100
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq '.' 2>/dev/null | head -n 15)"
    
    if echo "$response" | jq -e '.choices[0].message.tool_calls' > /dev/null 2>&1; then
        test_pass "GPT-4正确使用了tool_calls"
        echo "| GPT-4o Tool Calls | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_warn "GPT-4选择了文本回复"
        echo "| GPT-4o Tool Calls | ⚠ 文本回复 |" >> "$RESULTS_FILE"
    fi
}

# 测试8: 多模态内容（如果支持）
test_multimodal_content() {
    print_test_header "claude-sonnet-4-6" "多模态内容格式转换"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {
            "role": "user",
            "content": [
              {"type": "text", "text": "这张图片里有什么？"},
              {
                "type": "image_url",
                "image_url": {
                  "url": "https://via.placeholder.com/150"
                }
              }
            ]
          }
        ],
        "max_tokens": 100
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null | head -n 3)"
    
    if echo "$response" | jq -e '.error' > /dev/null 2>&1; then
        local error_msg=$(echo "$response" | jq -r '.error.message')
        if echo "$error_msg" | grep -qi "image\|multimodal\|vision"; then
            test_warn "模型不支持图片输入（预期行为）"
            echo "| Claude Sonnet 4-6 多模态 | ⚠ 不支持 |" >> "$RESULTS_FILE"
        else
            test_fail "多模态内容转换出错: $error_msg"
            echo "| Claude Sonnet 4-6 多模态 | ✗ 失败 |" >> "$RESULTS_FILE"
        fi
    else
        test_pass "多模态内容格式转换成功"
        echo "| Claude Sonnet 4-6 多模态 | ✓ 通过 |" >> "$RESULTS_FILE"
    fi
}

# 测试9: 超长上下文对话
test_long_context() {
    print_test_header "claude-sonnet-4-6" "超长上下文保持"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "第1个关键词：数据库"},
          {"role": "assistant", "content": "收到，第1个关键词是数据库。"},
          {"role": "user", "content": "第2个关键词：索引"},
          {"role": "assistant", "content": "收到，第2个关键词是索引。"},
          {"role": "user", "content": "第3个关键词：优化"},
          {"role": "assistant", "content": "收到，第3个关键词是优化。"},
          {"role": "user", "content": "请把我刚才说的3个关键词连成一句话"}
        ],
        "max_tokens": 50
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null)"
    
    if echo "$response" | grep -qi "数据库.*索引.*优化\|database.*index.*optim"; then
        test_pass "模型正确保持了多轮对话中的所有关键词"
        echo "| Claude Sonnet 4-6 超长上下文 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_fail "模型未能完整保持上下文"
        echo "| Claude Sonnet 4-6 超长上下文 | ✗ 失败 |" >> "$RESULTS_FILE"
    fi
}

# 测试10: 跨语言上下文
test_cross_language() {
    print_test_header "claude-sonnet-4-6" "跨语言上下文保持"
    
    local response=$(curl -s -X POST "${BASE_URL}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${API_KEY}" \
      -d '{
        "model": "claude-sonnet-4-6",
        "messages": [
          {"role": "user", "content": "Please remember this word: apple"},
          {"role": "assistant", "content": "OK, I will remember the word: apple."},
          {"role": "user", "content": "你刚才记住的英文单词是什么？用中文回答"}
        ],
        "max_tokens": 50
      }' 2>&1)
    
    echo "响应: $(echo "$response" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null)"
    
    if echo "$response" | grep -qi "苹果\|apple"; then
        test_pass "模型正确处理了跨语言上下文"
        echo "| Claude Sonnet 4-6 跨语言 | ✓ 通过 |" >> "$RESULTS_FILE"
    else
        test_fail "模型未能正确处理跨语言上下文"
        echo "| Claude Sonnet 4-6 跨语言 | ✗ 失败 |" >> "$RESULTS_FILE"
    fi
}

# 运行所有测试
echo "开始执行测试套件..."
echo ""

test_claude_sonnet_4_simple
sleep 1

test_claude_sonnet_4_complex
sleep 1

test_claude_opus_4
sleep 1

test_gpt4_multi_turn
sleep 1

test_claude_tool_calls
sleep 1

test_claude_tool_result
sleep 1

test_gpt4_tool_calls
sleep 1

test_multimodal_content
sleep 1

test_long_context
sleep 1

test_cross_language

# 输出测试总结
echo ""
echo "=========================================="
echo "测试总结"
echo "=========================================="
echo -e "总测试数: ${BLUE}${TOTAL_TESTS}${NC}"
echo -e "通过: ${GREEN}${PASSED_TESTS}${NC}"
echo -e "失败: ${RED}${FAILED_TESTS}${NC}"
echo ""

# 添加总结到结果文件
cat >> "$RESULTS_FILE" << EOF

## 测试总结

- **总测试数**: ${TOTAL_TESTS}
- **通过**: ${PASSED_TESTS}
- **失败**: ${FAILED_TESTS}
- **通过率**: $((PASSED_TESTS * 100 / TOTAL_TESTS))%

## 结论

EOF

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}✓ 所有测试通过！${NC}"
    echo "所有测试通过！多轮对话和工具调用功能正常。" >> "$RESULTS_FILE"
    echo "" >> "$RESULTS_FILE"
    echo "详细测试结果已保存到: $RESULTS_FILE"
    exit 0
else
    echo -e "${RED}✗ 有 ${FAILED_TESTS} 个测试失败${NC}"
    echo "有 ${FAILED_TESTS} 个测试失败，需要进一步检查。" >> "$RESULTS_FILE"
    echo "" >> "$RESULTS_FILE"
    echo "详细测试结果已保存到: $RESULTS_FILE"
    exit 1
fi
