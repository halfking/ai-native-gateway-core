# 71服务器路由问题热修复指南

## 问题描述

1. **无法发起请求**: 通过 `llmgateway.internal.example.com/v1` 无法发起任何请求
2. **路由匹配失败**: 无法正确匹配到可用凭据
3. **大量 empty_response**: 184数据库的 request_logs 中大量记录显示为 empty_response

## 根因分析

### 1. 路由匹配问题

可能的原因：
- **质量门限过滤过严**: 最近引入的多层质量门限（0.3 → 0.0）可能将所有凭据都过滤掉
- **凭据状态异常**: 凭据的 `availability_state`、`lifecycle_status` 或 `circuit_state` 不正常
- **探测状态错误**: `model_probe_state` 表中凭据被标记为 `broken_confirmed`
- **视图不同步**: `v_routable_credential_models` 视图返回 `is_routable=false`

### 2. empty_response 问题

已在提交 `78de1295` 中修复，但可能：
- 71服务器未部署最新代码
- 响应体处理逻辑仍有问题
- 流式响应的 chunk 计数不准确

## 诊断步骤

### 步骤1: 运行诊断脚本

```bash
# 在71服务器上执行
cd /path/to/llm-gateway-go-2
./scripts/diagnose_routing_issue.sh minimax-m3
```

诊断脚本会检查：
1. 最近1小时的请求统计
2. 指定模型的候选凭据状态
3. 最近50次请求的成功率
4. 模型探测状态
5. 最近失败请求详情
6. 质量门限过滤情况

### 步骤2: 检查服务版本

```bash
# 检查当前部署的代码版本
cd /path/to/llm-gateway-go-2
git log --oneline -1

# 确认是否包含 empty_response 修复
git log --oneline | grep "empty_response"
```

**期望输出**: 应该看到提交 `78de1295 fix(relay): resolve empty_response misclassification + telemetry SQL fixes`

### 步骤3: 检查数据库连接

```bash
# 测试数据库连接
psql "$LLM_GATEWAY_DATABASE_URL" -c "SELECT COUNT(*) FROM credentials WHERE status = 'active';"
```

## 快速修复方案

### 方案A: 临时降低质量门限（紧急）

如果所有凭据都被质量门限过滤，可以临时设置环境变量降低阈值：

```bash
# 在71服务器上
export LLM_GATEWAY_QUALITY_GATE_THRESHOLD="0.0"  # 禁用质量门限
systemctl restart llm-gateway
```

**注意**: 这只是临时方案，会允许低质量凭据参与路由。

### 方案B: 手动启用被过滤的凭据

```sql
-- 查看被过滤的凭据
SELECT 
    c.id, c.label, c.status, c.availability_state, c.lifecycle_status
FROM credentials c
WHERE c.status = 'active'
  AND c.availability_state != 'ready';

-- 重置 availability_state
UPDATE credentials 
SET availability_state = 'ready',
    availability_recover_at = NULL
WHERE id IN (/* 凭据ID列表 */);

-- 重置 circuit_state
UPDATE credentials 
SET circuit_state = 'closed',
    cooling_until = NULL
WHERE circuit_state = 'open';
```

### 方案C: 清除探测状态

```sql
-- 查看 broken_confirmed 状态
SELECT * FROM model_probe_state 
WHERE state = 'broken_confirmed'
  AND raw_model_name = 'minimax-m3';

-- 重置探测状态（允许重新尝试）
UPDATE model_probe_state 
SET state = 'pending',
    consecutive_failures = 0,
    next_probe_at = NOW()
WHERE state = 'broken_confirmed'
  AND raw_model_name = 'minimax-m3';
```

### 方案D: 更新到最新代码（推荐）

```bash
# 在71服务器上
cd /path/to/llm-gateway-go-2

# 拉取最新代码
git fetch origin
git checkout server-71
git pull origin server-71

# 确认包含最新修复
git log --oneline -5

# 重新编译
go build -o llm-gateway ./cmd/gateway

# 重启服务
systemctl restart llm-gateway

# 查看日志确认启动成功
journalctl -u llm-gateway -f
```

## 验证修复

### 1. 测试单个请求

```bash
# 使用测试脚本
./scripts/test_routing_fix.sh

# 或手动测试
curl -X POST http://llmgateway.internal.example.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好，请用一句话回复"}],
    "max_tokens": 20,
    "stream": false
  }'
```

**期望结果**: 
- HTTP 200 状态码
- 返回包含 `choices` 的 JSON 响应
- `choices[0].message.content` 不为空

### 2. 检查路由解析

```bash
curl "http://llmgateway.internal.example.com/api/routing/resolve?model=minimax-m3" | jq '.'
```

**期望输出**:
```json
{
  "client_model": "minimax-m3",
  "canonical_name": "minimax-m3",
  "resolution_path": "canonical",
  "candidates": [
    {
      "credential_id": 123,
      "provider_id": 45,
      "routable": true,
      "runtime_routable": true,
      ...
    }
  ],
  "quality_gate": {
    "threshold_used": 0.3,
    "routable_count": 2,
    "filtered_count": 0
  }
}
```

### 3. 监控 request_logs

```sql
-- 查看最近10分钟的请求
SELECT 
    ts,
    request_id,
    client_model,
    success,
    error_kind,
    credential_id,
    LEFT(response_preview, 50) as preview
FROM request_logs 
WHERE ts > NOW() - INTERVAL '10 minutes'
ORDER BY ts DESC
LIMIT 20;
```

**期望结果**:
- `success = true` 的请求增多
- `error_kind = 'empty_response'` 的请求减少
- `error_kind = 'no_candidate'` 消失

## 预防措施

### 1. 监控告警

添加以下监控指标：

```sql
-- 凭据可用性监控
SELECT 
    COUNT(*) FILTER (WHERE availability_state = 'ready') as ready_count,
    COUNT(*) FILTER (WHERE availability_state != 'ready') as not_ready_count,
    COUNT(*) FILTER (WHERE circuit_state = 'open') as circuit_open_count
FROM credentials 
WHERE status = 'active';

-- 质量门限过滤监控
SELECT 
    client_model,
    COUNT(*) FILTER (WHERE error_kind = 'no_candidate') as no_candidate_count
FROM request_logs 
WHERE ts > NOW() - INTERVAL '5 minutes'
GROUP BY client_model
HAVING COUNT(*) FILTER (WHERE error_kind = 'no_candidate') > 10;
```

### 2. 定期健康检查

```bash
# 添加到 crontab
*/5 * * * * /path/to/scripts/health_check.sh
```

### 3. 日志告警

配置 journalctl 告警，监控以下关键字：
- `"no available provider"`
- `"quality gate filtered all candidates"`
- `"all candidates unavailable"`

## 回滚方案

如果修复后问题仍未解决：

```bash
# 回滚到上一个稳定版本
cd /path/to/llm-gateway-go-2
git checkout <上一个稳定版本的commit>
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway
```

## 联系支持

如果问题持续存在，请收集以下信息：

1. 诊断脚本输出：`./scripts/diagnose_routing_issue.sh > diagnosis.txt`
2. 服务日志：`journalctl -u llm-gateway --since "1 hour ago" > service.log`
3. Git 版本信息：`git log --oneline -10 > version.txt`
4. 数据库快照：执行诊断脚本中的所有 SQL 查询

发送至：[支持邮箱或Slack频道]

## 附录：关键代码位置

- 路由匹配逻辑：`provider/client.go:loadCandidatesDB` (第578行)
- 质量门限配置：`provider/client.go:578-615` (多层回退策略)
- empty_response 检测：`relay/handler.go:detectEmptyStreamResponse` (第3064行)
- 候选凭据视图：`v_routable_credential_models` (数据库视图)

## 更新历史

- 2026-06-26: 初始版本
- 2026-06-26: 添加质量门限多层回退策略
- 2026-06-26: 修复 empty_response 误判问题（提交 78de1295）
