#!/usr/bin/env bash
# F3 智谱 trial 配额归因完整验证套件
# 
# 功能：
#   1. 执行生产数据验证（SQL 查询）
#   2. 执行活体测试（主动触发配额错误）
#   3. 生成验证报告
#
# 使用方法：
#   ./scripts/f3-verification-suite.sh [--via-mesh] [--skip-live-test]
#
# 选项：
#   --via-mesh        使用 mesh 中继连接
#   --skip-live-test  跳过活体测试（仅执行 SQL 查询）

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPORT_DIR="${REPO_ROOT}/verification-reports"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
REPORT_FILE="${REPORT_DIR}/f3-verification-${TIMESTAMP}.md"

# 默认配置
SSH_TARGET="211"
USE_MESH=false
SKIP_LIVE_TEST=false

# 解析参数
for arg in "$@"; do
    case "$arg" in
        --via-mesh)
            USE_MESH=true
            SSH_TARGET="211-mesh"
            ;;
        --skip-live-test)
            SKIP_LIVE_TEST=true
            ;;
        *)
            echo "未知参数: $arg"
            echo "使用方法: $0 [--via-mesh] [--skip-live-test]"
            exit 1
            ;;
    esac
done

# 自动探测（如果没有显式指定）
if [ "$USE_MESH" = false ]; then
    echo "🔍 自动探测网络连通性..."
    if timeout 3 ping -c 1 192.168.31.211 &>/dev/null 2>&1 || true; then
        echo "✅ 内网直连可用"
        SSH_TARGET="211"
    else
        echo "⚠️  内网不通，回落 mesh 中继"
        SSH_TARGET="211-mesh"
    fi
fi

# 创建报告目录
mkdir -p "$REPORT_DIR"

# 初始化报告
cat > "$REPORT_FILE" << 'REPORT_HEADER'
# F3 智谱 trial 配额归因验证报告

**验证时间**：$(date '+%Y-%m-%d %H:%M:%S %Z')  
**执行方式**：自动化定时任务  
**SSH 连接**：${SSH_TARGET}

---

## 一、生产数据验证

REPORT_HEADER

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🚀 F3 智谱 trial 配额归因完整验证套件"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📅 验证时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo "📡 SSH 连接: $SSH_TARGET"
echo "📂 报告目录: $REPORT_DIR"
echo ""

# ============================================================================
# 步骤 1: 执行 SQL 验证
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📊 步骤 1/3: 执行生产数据 SQL 验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

SQL_OUTPUT=$(mktemp)
if ssh "$SSH_TARGET" "docker exec -i acc-pg17 psql -U postgres -d llm_gateway" < "${SCRIPT_DIR}/verify-f3-quota-attribution.sql" > "$SQL_OUTPUT" 2>&1; then
    echo "✅ SQL 查询执行成功"
    
    # 追加到报告
    cat >> "$REPORT_FILE" << REPORT_SQL
### SQL 查询结果

\`\`\`
$(cat "$SQL_OUTPUT")
\`\`\`

### 结果分析

REPORT_SQL
    
    # 简单分析（查找关键字）
    if grep -q "upstream_quota_permanent" "$SQL_OUTPUT"; then
        echo "✅ 发现 upstream_quota_permanent 记录"
        echo "- ✅ 发现 \`upstream_quota_permanent\` 记录" >> "$REPORT_FILE"
    else
        echo "⚠️  未发现 upstream_quota_permanent 记录"
        echo "- ⚠️  未发现 \`upstream_quota_permanent\` 记录（可能是时间窗口无数据）" >> "$REPORT_FILE"
    fi
    
    if grep -q "transient\|request_rejected" "$SQL_OUTPUT"; then
        echo "⚠️  发现 transient 或 request_rejected 记录"
        echo "- ⚠️  发现 \`transient\` 或 \`request_rejected\` 记录（可能是修复前的旧数据）" >> "$REPORT_FILE"
    fi
else
    echo "❌ SQL 查询执行失败"
    cat "$SQL_OUTPUT"
    
    cat >> "$REPORT_FILE" << REPORT_SQL_ERROR
### SQL 查询执行失败

\`\`\`
$(cat "$SQL_OUTPUT")
\`\`\`

REPORT_SQL_ERROR
fi

rm -f "$SQL_OUTPUT"
echo ""

# ============================================================================
# 步骤 2: 活体测试（主动触发配额错误）
# ============================================================================
if [ "$SKIP_LIVE_TEST" = false ]; then
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🧪 步骤 2/3: 执行活体测试"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    
    cat >> "$REPORT_FILE" << REPORT_LIVE_HEADER

---

## 二、活体测试

### 测试方法

主动向 211 发送请求，使用 \`glm-5.3-flash-trial\` 模型触发配额错误。

REPORT_LIVE_HEADER
    
    # 检查环境变量中是否有临时 key
    if [ -z "${LLM_GATEWAY_TEST_KEY:-}" ]; then
        echo "⚠️  未设置 LLM_GATEWAY_TEST_KEY 环境变量"
        echo "   跳过活体测试（需要有效的 API key）"
        
        cat >> "$REPORT_FILE" << REPORT_LIVE_SKIP
### 测试结果

⚠️  **跳过**：未设置 \`LLM_GATEWAY_TEST_KEY\` 环境变量

REPORT_LIVE_SKIP
    else
        echo "✅ 使用临时 key 执行活体测试..."
        
        LIVE_TEST_OUTPUT=$(mktemp)
        REQUEST_ID=""
        
        # 发送测试请求
        if RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "https://llm.itestu.cn/v1/chat/completions" \
            -H "Authorization: Bearer ${LLM_GATEWAY_TEST_KEY}" \
            -H "Content-Type: application/json" \
            -d '{
                "model": "glm-5.3-flash-trial",
                "messages": [{"role": "user", "content": "test F3 verification"}]
            }'); then
            
            HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
            BODY=$(echo "$RESPONSE" | head -n-1)
            
            echo "HTTP 状态码: $HTTP_CODE"
            
            # 提取 request_id
            REQUEST_ID=$(echo "$BODY" | grep -o '"request_id":"[^"]*"' | head -1 | cut -d'"' -f4 || echo "")
            
            if [ -n "$REQUEST_ID" ]; then
                echo "Request ID: $REQUEST_ID"
                
                # 等待 3 秒让 DB 写入完成
                echo "等待 3 秒让 DB 写入完成..."
                sleep 3
                
                # 查询该请求的 error_kind
                echo "查询 DB 中的归因..."
                DB_QUERY_OUTPUT=$(ssh "$SSH_TARGET" "docker exec acc-pg17 psql -U postgres -d llm_gateway -t -c \"
SELECT 
    error_kind,
    upstream_status_code,
    client_status_code,
    success,
    LEFT(error_message, 100) AS error_msg_preview
FROM _request_logs
WHERE request_id = '$REQUEST_ID';
\"" 2>&1)
                
                echo "$DB_QUERY_OUTPUT" | tee "$LIVE_TEST_OUTPUT"
                
                cat >> "$REPORT_FILE" << REPORT_LIVE_RESULT
### 测试结果

**HTTP 状态码**：$HTTP_CODE  
**Request ID**：\`$REQUEST_ID\`

**响应内容**：
\`\`\`json
$BODY
\`\`\`

**DB 查询结果**：
\`\`\`
$DB_QUERY_OUTPUT
\`\`\`

### 结果分析

REPORT_LIVE_RESULT
                
                # 分析结果
                if echo "$DB_QUERY_OUTPUT" | grep -q "upstream_quota_permanent"; then
                    echo "✅ error_kind = upstream_quota_permanent（正确）"
                    echo "- ✅ \`error_kind = upstream_quota_permanent\`（修复已生效）" >> "$REPORT_FILE"
                elif echo "$DB_QUERY_OUTPUT" | grep -q "transient\|request_rejected"; then
                    echo "❌ error_kind = transient/request_rejected（错误）"
                    echo "- ❌ \`error_kind = transient/request_rejected\`（修复未生效，需进一步调查）" >> "$REPORT_FILE"
                else
                    echo "⚠️  未找到该 request_id 的记录，或 error_kind 为其他值"
                    echo "- ⚠️  未找到该 request_id 的记录，或 \`error_kind\` 为其他值" >> "$REPORT_FILE"
                fi
            else
                echo "⚠️  未能提取 request_id"
                
                cat >> "$REPORT_FILE" << REPORT_LIVE_NO_ID
### 测试结果

**HTTP 状态码**：$HTTP_CODE

**响应内容**：
\`\`\`json
$BODY
\`\`\`

⚠️  未能提取 \`request_id\`，无法查询 DB

REPORT_LIVE_NO_ID
            fi
        else
            echo "❌ 活体测试请求失败"
            
            cat >> "$REPORT_FILE" << REPORT_LIVE_FAILED
### 测试结果

❌ **失败**：活体测试请求发送失败

REPORT_LIVE_FAILED
        fi
        
        rm -f "$LIVE_TEST_OUTPUT"
    fi
    echo ""
else
    echo "⏭️  跳过活体测试（--skip-live-test）"
    
    cat >> "$REPORT_FILE" << REPORT_LIVE_SKIP2

---

## 二、活体测试

⏭️  **跳过**：使用了 \`--skip-live-test\` 参数

REPORT_LIVE_SKIP2
    echo ""
fi

# ============================================================================
# 步骤 3: 生成总结
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📝 步骤 3/3: 生成验证总结"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 获取当前部署版本
CURRENT_VERSION=$(curl -s "https://llm.itestu.cn/version" | grep -o '"git_sha":"[^"]*"' | cut -d'"' -f4 || echo "unknown")

cat >> "$REPORT_FILE" << REPORT_FOOTER

---

## 三、验证总结

**当前部署版本**：$CURRENT_VERSION  
**验证时间**：$(date '+%Y-%m-%d %H:%M:%S %Z')

### 综合结论

□ ✅ 验证通过 - 代码路径正确，DB 归因准确  
□ ⚠️ 部分通过 - 存在部分问题（见上方分析）  
□ ❌ 验证失败 - 修复未生效或存在其他问题  
□ ⏳ 数据不足 - 时间窗口内无匹配记录

### 下一步建议

根据上述验证结果，建议采取以下行动之一：

1. **如果验证通过**：
   - 确认修复已生效，关闭调查任务
   - 将结果回填到原始审计档

2. **如果部分通过或失败**：
   - 分析具体失败原因
   - 检查部署版本是否包含修复（e2242e078）
   - 必要时重新部署或进行代码审查

3. **如果数据不足**：
   - 扩大时间窗口重新查询
   - 使用临时 key 执行活体测试

---

**报告生成时间**：$(date '+%Y-%m-%d %H:%M:%S %Z')  
**报告文件**：\`$REPORT_FILE\`
REPORT_FOOTER

echo "✅ 验证报告已生成: $REPORT_FILE"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ 验证套件执行完成"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📊 查看报告："
echo "   cat $REPORT_FILE"
echo ""
echo "📁 所有报告："
echo "   ls -lh $REPORT_DIR"
echo ""
