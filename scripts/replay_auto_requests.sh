#!/bin/bash
# 回放 auto 请求到 154 生产环境，收集真实数据到 252 共享库
# 用途：为 P2.4/P2.5 真实数据重训练提供样本

set -euo pipefail

# 配置
TARGET_HOST="${TARGET_HOST:-172.16.2.154:8080}"
NUM_REQUESTS="${NUM_REQUESTS:-100}"
DELAY_MS="${DELAY_MS:-500}"
LOG_FILE="replay_auto_$(date +%Y%m%d_%H%M%S).log"

# 多样化请求模板（从生产日志中提取的真实模式）
PROMPTS=(
  "请帮我写一个 Python 快速排序算法"
  "如何优化 MySQL 查询性能？"
  "解释一下 Go 语言的 goroutine 工作原理"
  "请生成一个 Vue3 表单组件示例"
  "什么是 Docker 容器？如何使用？"
  "帮我分析这段代码的时间复杂度"
  "如何设计一个高并发的秒杀系统？"
  "请解释 RESTful API 最佳实践"
  "如何在 Java 中实现单例模式？"
  "请帮我写一个正则表达式匹配邮箱"
)

USERS=("user_1001" "user_1002" "user_1003" "user_1004" "user_1005")

echo "=== Auto 请求回放脚本 ===" | tee "$LOG_FILE"
echo "目标主机: $TARGET_HOST" | tee -a "$LOG_FILE"
echo "请求数量: $NUM_REQUESTS" | tee -a "$LOG_FILE"
echo "请求间隔: ${DELAY_MS}ms" | tee -a "$LOG_FILE"
echo "日志文件: $LOG_FILE" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

success_count=0
error_count=0

for ((i=1; i<=NUM_REQUESTS; i++)); do
  # 随机选择 prompt 和 user_id
  prompt="${PROMPTS[$((RANDOM % ${#PROMPTS[@]}))]}"
  user_id="${USERS[$((RANDOM % ${#USERS[@]}))]}"
  
  # 构造请求
  request_body=$(cat <<EOF
{
  "model": "auto",
  "messages": [
    {"role": "user", "content": "$prompt"}
  ],
  "user": "$user_id",
  "temperature": 0.7,
  "max_tokens": 512
}
EOF
)
  
  echo "[${i}/${NUM_REQUESTS}] 发送请求 (user=$user_id)..." | tee -a "$LOG_FILE"
  
  # 发送请求
  response=$(curl -s -w "\n%{http_code}" \
    -X POST "http://$TARGET_HOST/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer sk-test-key" \
    -d "$request_body" 2>&1)
  
  http_code=$(echo "$response" | tail -n1)
  body=$(echo "$response" | sed '$d')
  
  if [[ "$http_code" == "200" ]]; then
    # 提取实际路由的模型
    actual_model=$(echo "$body" | jq -r '.model // "unknown"')
    echo "  ✓ 成功 (HTTP $http_code, routed to: $actual_model)" | tee -a "$LOG_FILE"
    ((success_count++))
  else
    echo "  ✗ 失败 (HTTP $http_code)" | tee -a "$LOG_FILE"
    echo "  响应: $body" >> "$LOG_FILE"
    ((error_count++))
  fi
  
  # 控制请求频率
  sleep $(awk "BEGIN {print $DELAY_MS/1000}")
done

echo "" | tee -a "$LOG_FILE"
echo "=== 回放完成 ===" | tee -a "$LOG_FILE"
echo "成功: $success_count" | tee -a "$LOG_FILE"
echo "失败: $error_count" | tee -a "$LOG_FILE"
echo "成功率: $(awk "BEGIN {printf \"%.2f%%\", $success_count*100/$NUM_REQUESTS}")" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"
echo "下一步:" | tee -a "$LOG_FILE"
echo "1. 登录 252: ssh root@172.16.2.252" | tee -a "$LOG_FILE"
echo "2. 验证数据: psql -U llm_gateway -d llm_gateway -c 'SELECT COUNT(*) FROM auto_route_selections_all;'" | tee -a "$LOG_FILE"
echo "3. 导出数据: cd /root/llm-gateway && ./scripts/export_production_data.sh" | tee -a "$LOG_FILE"
