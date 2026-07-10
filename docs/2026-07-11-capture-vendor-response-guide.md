# GLM/DeepSeek/Doubao 真实响应抓包指南

**日期**: 2026-07-11  
**目的**: 验证并补充私有字段黑名单，确保字段过滤准确性

---

## 背景

当前已创建 3 个厂商的私有字段黑名单（`strip_zhipu_fields.go`, `strip_deepseek_fields.go`, `strip_doubao_fields.go`），但字段清单为**推测**，需要通过真实 API 响应验证。

---

## 抓包方法

### 方法 1：从 184 生产环境抓取（推荐）

```bash
# 1. SSH 到 184 服务器
ssh -p 25022 root@14.103.112.184

# 2. 查询最近的 GLM 请求日志
kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- \
  psql postgresql://llmgo:$PG_LLM_GATEWAY_PASS@postgres-citus-coordinator:5432/llm_gateway \
  -c "SELECT request_id, response_body FROM request_log 
      WHERE catalog_code = 'zhipu' 
      AND response_body IS NOT NULL 
      ORDER BY created_at DESC LIMIT 1;"

# 3. 查询 DeepSeek 请求日志
kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- \
  psql postgresql://llmgo:$PG_LLM_GATEWAY_PASS@postgres-citus-coordinator:5432/llm_gateway \
  -c "SELECT request_id, response_body FROM request_log 
      WHERE catalog_code = 'deepseek' 
      AND response_body IS NOT NULL 
      ORDER BY created_at DESC LIMIT 1;"

# 4. 查询 Doubao 请求日志
kubectl exec -n pms-test deployment/llm-gateway-go-deployment -- \
  psql postgresql://llmgo:$PG_LLM_GATEWAY_PASS@postgres-citus-coordinator:5432/llm_gateway \
  -c "SELECT request_id, response_body FROM request_log 
      WHERE catalog_code = 'doubao' 
      AND response_body IS NOT NULL 
      ORDER BY created_at DESC LIMIT 1;"
```

**优势**: 真实生产数据，包含所有实际字段  
**注意**: 需要脱敏处理，不要直接复制到公开文档

---

### 方法 2：直接调用上游 API（需凭据）

#### GLM (智谱)

```bash
# 获取凭据
ZHIPU_KEY=$(kubectl get secret -n pms-test llm-gateway-secrets -o jsonpath='{.data.zhipu-api-key}' | base64 -d)

# 调用 API
curl https://open.bigmodel.cn/api/paas/v4/chat/completions \
  -H "Authorization: Bearer $ZHIPU_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 100
  }' | jq '.' > /tmp/glm-response.json

# 检查响应中的所有顶层字段
jq 'keys' /tmp/glm-response.json
```

#### DeepSeek

```bash
DEEPSEEK_KEY=$(kubectl get secret -n pms-test llm-gateway-secrets -o jsonpath='{.data.deepseek-api-key}' | base64 -d)

curl https://api.deepseek.com/v1/chat/completions \
  -H "Authorization: Bearer $DEEPSEEK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "messages": [{"role": "user", "content": "测试"}],
    "max_tokens": 100
  }' | jq '.' > /tmp/deepseek-response.json

# 检查 usage 字段的所有子字段（关注 reasoning_tokens）
jq '.usage | keys' /tmp/deepseek-response.json
```

#### Doubao (豆包)

```bash
DOUBAO_KEY=$(kubectl get secret -n pms-test llm-gateway-secrets -o jsonpath='{.data.doubao-api-key}' | base64 -d)

curl https://ark.cn-beijing.volces.com/api/v3/chat/completions \
  -H "Authorization: Bearer $DOUBAO_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-pro-32k",
    "messages": [{"role": "user", "content": "你好"}]
  }' | jq '.' > /tmp/doubao-response.json

jq 'keys' /tmp/doubao-response.json
```

---

## 字段分析清单

### GLM 响应检查项

- [ ] 顶层是否有 `zhipu_request_id` / `request_id`
- [ ] `usage` 中是否有 `cache_read_tokens` / `cache_creation_tokens`
- [ ] 是否有 `web_search_results` / `web_search` 字段
- [ ] 是否有 `retrieval_documents` / `retrieval` 字段
- [ ] 是否有 `model_version` / `engine_version`
- [ ] 是否有 `system_fingerprint`

**当前黑名单**（需验证）:
```go
var zhipuPrivateFields = []string{
    "zhipu_request_id",
    "cache_read_tokens",
    "cache_creation_tokens",
    "web_search_results",
    "retrieval_documents",
    "model_version",
    "system_fingerprint",
}
```

### DeepSeek 响应检查项

- [ ] `usage` 中是否有 `reasoning_tokens` (R1 系列)
- [ ] `usage` 中是否有 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`
- [ ] 顶层是否有 `deepseek_request_id`
- [ ] 是否有 `model_type` 字段（reasoning / standard）

**当前黑名单**（需验证）:
```go
var deepseekPrivateFields = []string{
    "deepseek_request_id",
    "reasoning_tokens",  // 关键：R1 计费字段
    "prompt_cache_hit_tokens",
    "prompt_cache_miss_tokens",
    "model_type",
}
```

### Doubao 响应检查项

- [ ] 顶层是否有 `doubao_request_id` / `seeddance_request_id`
- [ ] 是否有 `content_safety_score` / `safety_score`
- [ ] 是否有 `model_endpoint` / `endpoint`
- [ ] 是否有 `ab_test_group` / `test_group`
- [ ] 是否有 `seed_token_usage` / `seed_usage`

**当前黑名单**（需验证）:
```go
var doubaoPrivateFields = []string{
    "doubao_request_id",
    "seeddance_request_id",
    "content_safety_score",
    "model_endpoint",
    "ab_test_group",
    "seed_token_usage",
}
```

---

## 验证步骤

1. **抓取响应样本**（选择方法 1 或方法 2）

2. **对比当前黑名单**:
   ```bash
   # 检查响应中是否有不在黑名单中的敏感字段
   jq 'keys' /tmp/glm-response.json
   ```

3. **更新黑名单文件**（如发现新字段）:
   ```bash
   # 编辑对应文件
   vim domains/streaming/strip_zhipu_fields.go
   vim domains/streaming/strip_deepseek_fields.go
   vim domains/streaming/strip_doubao_fields.go
   ```

4. **补充测试用例**:
   ```bash
   # 在 strip_vendor_fields_test.go 中添加真实字段示例
   vim domains/streaming/strip_vendor_fields_test.go
   ```

5. **运行测试验证**:
   ```bash
   go test ./domains/streaming -run "TestStrip" -v
   ```

---

## 安全注意事项

1. **脱敏处理**: 抓包后的响应可能包含真实用户内容，需脱敏后才能用于文档
2. **凭据保护**: 不要将 API Key 写入文件或提交到 Git
3. **生产影响**: 从生产数据库查询时限制条数（`LIMIT 1`）
4. **隐私合规**: 仅用于技术验证，不得用于其他用途

---

## 预期输出

完成抓包验证后，应产出：

1. **验证报告** `docs/2026-07-11-vendor-fields-verification.md`:
   - GLM 实际响应字段清单
   - DeepSeek 实际响应字段清单
   - Doubao 实际响应字段清单
   - 黑名单补充建议

2. **更新后的代码**（如有新发现）:
   - `domains/streaming/strip_zhipu_fields.go`
   - `domains/streaming/strip_deepseek_fields.go`
   - `domains/streaming/strip_doubao_fields.go`

3. **更新后的测试**:
   - `domains/streaming/strip_vendor_fields_test.go`

---

## 时间估算

- 方法 1（生产环境）: 30 分钟
- 方法 2（直接调用）: 1 小时（需获取凭据）
- 分析与更新: 30 分钟

**总计**: 1-2 小时
