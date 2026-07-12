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

---

## 实际抓包执行结果（2026-07-11，252 生产）

**环境**: `prod-aliyun-252` (115.29.212.252:25022)，部署 `llm.itestu.cn (llm-gateway-go) + pg17 + redis:6389`  
**数据库**: `172.16.2.210:5432/llm_gateway`，host: `pg-data-252-pg17` (podman container)  
**凭据**: `llm_gateway / 4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg`（超级用户，跳过 RLS）  
**连接方法**: SSH tunnel `ssh -p 25022 root@115.29.212.252 -L 25432:172.16.2.210:5432 -N -f`

### 真实 7 天流量分布（`request_logs_hot ∪ request_logs_2026_07`）

| 厂商 | provider_id | code | total | success | has_body | 备注 |
|---|---|---|---|---|---|---|
| MiniMax | 14 | minimax | 16456 | 15970 | 15970 | 主流量，含 thinking mode |
| (匿名) | 587, 314 | (none) | 7212 | 5186 | 5181 | apiclaude.cc / apigpt.cc 中转 |
| xiaomi | 1 | xiaomi | 4356 | 4114 | 4114 | 标准 OpenAI 兼容 |
| nvidia | 18 | nvidia | 980 | 502 | 502 | 偶发高 fail 率 |
| **zhipu (GLM)** | 32 | zhipu | **560** | **524** | **524** | **本次验证数据来源** |
| volcano-normal (Doubao OpenAI) | 35 | volcano-normal | 104 | 0 | 0 | 全部失败，仅 embedding 调用 |
| sensenova | 24 | sensenova | 22 | 0 | 0 | 全部失败 |
| volcano-tokenplan | 34 | volcano-tokenplan | 8 | 0 | 0 | 全部失败 |
| **DeepSeek** | - | deepseek | **0** | 0 | 0 | **生产未启用** |
| **Doubao 原生** | - | doubao | **0** | 0 | 0 | **生产未启用** |

### 真实私有字段（已确认）

#### MiniMax（provider_id=14，~16K 次成功响应）

**顶层字段**（已有 stripper 覆盖 — 验证）：`nvext, audio_content, name, system_fingerprint, base_resp, request_id, ...`

**嵌套私有字段**（**新增到 `strip_minimax_fields.go`**）：
- `usage.total_characters` — 89 次
- `usage.cache_read_tokens` — 87 次
- `usage.prompt_tokens_details.cached_tokens` — 89 次
- `usage.completion_tokens_details.reasoning_tokens` — 49 次
- `choices.0.message.reasoning` — 4 次（legacy reasoning surface）

#### Zhipu / GLM（provider_id=32，524 次成功响应，glm-5.x 模型）

**顶层字段**（已加入 stripper）：`system_fingerprint, zhipu_request_id, web_search_results, retrieval_documents, model_version, sensitive_word_check, request_id`

**嵌套私有字段**（**已加入 `strip_zhipu_fields.go`**，本次 mainline 改进）：
- `usage.prompt_tokens_details.cached_tokens` — Anthropic-style cache hit 计费
- `usage.completion_tokens_details.reasoning_tokens` — GLM-5 / GLM-5.2 推理 token 计费
- `choices.0.message.reasoning_content` — GLM-5.x 思考链，与 MiniMax/DeepSeek-R1 同型

**真实样本**（provider_id=32, ts=2026-07-09，glm-5.2）：

```json
{
  "id": "202607091302388f6a7ff3cf8c400c",
  "model": "glm-5.2",
  "usage": {
    "total_tokens": 18,
    "prompt_tokens": 13,
    "completion_tokens": 5,
    "prompt_tokens_details": {"cached_tokens": 0},
    "completion_tokens_details": {"reasoning_tokens": 2}
  },
  "choices": [{
    "message": {
      "role": "assistant",
      "reasoning_content": "Let me think about it...",
      "content": "answer",
      "tool_calls": [...]
    },
    "finish_reason": "tool_calls"
  }],
  "system_fingerprint": "fp_glm5_2"
}
```

#### 未验证项（生产无数据）

| 厂商 | 现状 | 后续动作 |
|---|---|---|
| **Doubao** (OpenAI 兼容模式) | 仅 volcano-normal 走 OpenAI 兼容，0 成功；doubao code 全 0 调用 | 启用实际 chat 调用后回归验证 |
| **DeepSeek** | 0 调用 | 模型未在 provider_catalog 注册或未分配 key，需先启用再验证 |
| **Doubao 原生协议** (非 OpenAI 兼容) | 不存在该 protocol | 架构层 P1.2 — Gemini 完整协议时一起评估 |
| **Gemini** | (代码未启用 Gemini provider) | 同 P1.2 — 完整协议接入时验证 |

### 自动化改进

1. **新增 helper**: `domains/streaming/strip_zhipu_fields.go` 内联 `stripNestedPath()` / `stripPathDescend()` — 支持点路径（如 `usage.prompt_tokens_details`）+ 数组索引（如 `choices.0.message.reasoning_content`）。
2. **新增测试**: `domains/streaming/strip_vendor_fields_test.go` — `TestStripMinimaxNestedFields` + `TestStripZhipuFieldsBody/strips_GLM-5_nested_*` 共 5 个新场景，全部通过。
3. **保持向后兼容**: 原 `TestStripMinimaxFieldsBody` 和 `TestStripDoubaoFieldsBody` / `TestStripDeepSeekFieldsBody` 不变。

### 已知遗留风险

- **doubao / deepseek**: 推测字段清单（基于 docs）未经生产验证，需启用流量后回归。
- **嵌套路径性能**: 每次 stripper 触发会 unmarshal → 处理 → re-marshal 一次。预期 ~0.1ms/响应（路径只针对嵌套字段存在时），可接受。
- **OpenAI Responses API 协议**（`provider_id=587` 的 4 次响应）: 包含 `output / output[].content[].type / annotations` 等新字段未纳入现有 stripper — 见后续 P1 任务。

### 1785 次调用 vs 524 次成功的统计观察

| 厂商 | fail 比例 | 失败原因典型 |
|---|---|---|
| xiaomi | 5.6% | model_not_found |
| nvidia | 48.7% | 高 transient 失败（疑似供应商问题） |
| volcano-normal | 100% | 全 embedding 调用失败 |
| sensenova | 100% | 配置或网络层问题 |
| zhipu | 6.4% | 主要是 glm-4.7 的 model_not_found |

## 查询参考 SQL

### 全厂商字段 top-level key 频率

```sql
WITH all_keys AS (
    SELECT pc.code, ks.key AS top_key
    FROM request_logs_hot r
    LEFT JOIN providers p ON p.id = r.provider_id
    LEFT JOIN provider_catalog pc ON pc.code = p.code
    CROSS JOIN LATERAL jsonb_object_keys(r.response_body) AS ks(key)
    WHERE r.ts > now() - interval '7 days'
      AND r.success AND r.response_body IS NOT NULL
      AND jsonb_typeof(r.response_body) = 'object'
)
SELECT code, top_key, count(*) AS n_occurrences
FROM all_keys
GROUP BY 1, 2
ORDER BY code, n_occurrences DESC;
```

### usage 子字段频率

```sql
WITH usage_keys AS (
    SELECT pc.code, ks.key AS usage_key
    FROM request_logs_hot r
    LEFT JOIN providers p ON p.id = r.provider_id
    LEFT JOIN provider_catalog pc ON pc.code = p.code
    CROSS JOIN LATERAL jsonb_object_keys(r.response_body->'usage') AS ks(key)
    WHERE r.ts > now() - interval '7 days'
      AND r.success AND jsonb_typeof(r.response_body->'usage') = 'object'
)
SELECT code, usage_key, count(*) AS n
FROM usage_keys
GROUP BY 1, 2 ORDER BY code, n DESC;
```

### message 子字段频率

```sql
WITH msg_keys AS (
    SELECT pc.code, ks.key AS msg_key
    FROM request_logs_hot r
    LEFT JOIN providers p ON p.id = r.provider_id
    LEFT JOIN provider_catalog pc ON pc.code = p.code
    CROSS JOIN LATERAL jsonb_object_keys(r.response_body->'choices'->0->'message') AS ks(key)
    WHERE r.ts > now() - interval '7 days'
      AND r.success AND jsonb_typeof(r.response_body->'choices'->0->'message') = 'object'
)
SELECT code, msg_key, count(*) AS n
FROM msg_keys
GROUP BY 1, 2 ORDER BY code, n DESC;
```

