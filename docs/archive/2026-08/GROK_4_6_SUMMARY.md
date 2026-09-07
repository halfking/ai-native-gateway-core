# Grok-4.6 模型支持添加总结

## 问题描述

用户在选择 grok-4.6 模型时，网关报错：
```
Model 'grok-4.6' is not supported by this gateway
```

## 根本原因

网关的数据库中缺少 grok-4.6 模型的配置记录。网关通过以下 SQL 查询来验证模型是否支持：

```sql
SELECT EXISTS (
    SELECT 1 FROM provider_models WHERE canonical_raw_name = $1
    UNION ALL
    SELECT 1 FROM provider_models WHERE standardized_name = $1
    UNION ALL
    SELECT 1 FROM model_aliases WHERE raw_name = $1
    LIMIT 1
)
```

由于 grok-4.6 不在这些表中，导致验证失败。

## 解决方案

### 1. 数据库迁移文件

创建了两个迁移文件来添加 grok-4.6 模型：

#### `sql/migrations/domain/352_add_grok_4_6.sql`
- 在 `models_canonical` 表中添加 grok-4.6 记录
- 配置参数：
  - `canonical_name`: grok-4.6
  - `family`: grok
  - `provider_name`: xAI
  - `context_window_k`: 500 (500k tokens)
  - `modality`: vision (支持图像+文本输入)
  - `reasoning_caps`: 支持 low, medium, high, xhigh 四个推理等级
- 在 `model_aliases` 表中添加别名：grok-4.6, grok-4-6

#### `sql/migrations/domain/353_update_xai_provider_add_grok_4_6.sql`
- 更新 `provider_catalog` 表中 xAI provider 的 `model_list`
- 将 grok-4.6 添加到可用模型列表中

### 2. 代码配置更新

#### `modelname/modality_defaults.go`
添加 grok-4.6 的模态配置：
```go
{"grok-4.6", "vision", 0},  // exact match, supports image input
```

这确保网关将 grok-4.6 识别为视觉模型，可以处理图像输入。

#### `internal/reasoncap/reasoning_defaults.go`
添加 grok-4.6 的推理能力配置：
```go
{"grok-4.6", 0, Caps{
    Supported: true, 
    Dialect: DialectGrok,
    Efforts: []string{"low", "medium", "high", "xhigh"},
    CanDisable: true,
}},
```

这配置了 grok-4.6 的推理能力：
- 使用 Grok dialect（reasoning_effort 参数）
- 支持 4 个推理等级：low, medium, high, xhigh
- 可以禁用推理

### 3. 测试验证

#### `modelname/modality_defaults_test.go`
添加测试用例验证 grok-4.6 被识别为 vision 模型：
```go
{"grok-4.6", "grok-4.6", "vision"},
```

#### `internal/reasoncap/caps_test.go`
添加测试用例验证 grok-4.6 的推理能力：
```go
{"grok-4.6", DialectGrok, true},
```

#### `internal/reasoncap/grok_4_6_test.go`
创建专门的测试文件验证 grok-4.6 的完整配置：
- 验证支持推理
- 验证使用 DialectGrok
- 验证可以禁用推理
- 验证支持 low, medium, high, xhigh 四个等级
- 验证不使用 budget-based 推理（使用 effort enum）

## Grok-4.6 模型特性

根据 xAI 官方文档：

- **上下文窗口**: 500,000 tokens (500k)
- **模态支持**: 
  - 输入: 文本 + 图像
  - 输出: 文本
- **推理能力**: 支持 4 个等级
  - low: 低等级推理
  - medium: 中等级推理
  - high: 高等级推理
  - xhigh: 超高等级推理
- **API 兼容性**: 完全兼容 OpenAI Chat Completions API
- **Base URL**: https://api.x.ai/v1

## 测试结果

所有测试均通过：

```bash
✅ modelname.TestInferModality - PASS
✅ reasoncap.TestPatterns - PASS
✅ reasoncap.TestGrok46Config - PASS
```

测试验证了：
1. grok-4.6 被正确识别为 vision 模型
2. grok-4.6 的推理能力配置正确
3. 推理等级（low, medium, high, xhigh）可用
4. 可以启用/禁用推理

## 部署步骤

### 1. 运行数据库迁移

```bash
export DATABASE_URL="postgresql://user:password@host:port/dbname"
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
./scripts/run-migrations-strict.sh
```

### 2. 验证数据库记录

```sql
-- 检查 models_canonical
SELECT id, canonical_name, family, context_window_k, modality, reasoning_caps
FROM models_canonical
WHERE canonical_name = 'grok-4.6';

-- 检查 model_aliases
SELECT ma.id, ma.raw_name, ma.status
FROM model_aliases ma
JOIN models_canonical mc ON ma.canonical_id = mc.id
WHERE mc.canonical_name = 'grok-4.6';

-- 检查 provider_catalog
SELECT code, name, model_list::jsonb
FROM provider_catalog
WHERE code = 'xai';
```

### 3. 重启网关服务

```bash
# 根据你的部署方式重启服务
# 例如：
systemctl restart llm-gateway
# 或
kubectl rollout restart deployment/llm-gateway
```

### 4. 验证 API 请求

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "grok-4.6",
    "messages": [
      {
        "role": "user",
        "content": "Hello, how are you?"
      }
    ],
    "reasoning_effort": "medium"
  }'
```

预期结果：请求应该成功，不再报 "Model 'grok-4.6' is not supported" 错误。

## 相关文件清单

### 新增文件
- `sql/migrations/domain/352_add_grok_4_6.sql`
- `sql/migrations/domain/352_add_grok_4_6.down.sql`
- `sql/migrations/domain/353_update_xai_provider_add_grok_4_6.sql`
- `sql/migrations/domain/353_update_xai_provider_add_grok_4_6.down.sql`
- `internal/reasoncap/grok_4_6_test.go`
- `test_grok_4_6_config.md` (验证文档)

### 修改文件
- `modelname/modality_defaults.go`
- `modelname/modality_defaults_test.go`
- `internal/reasoncap/reasoning_defaults.go`
- `internal/reasoncap/caps_test.go`

## 参考文档

- xAI API 文档: https://docs.x.ai/api
- Grok-4.6 模型文档: https://docs.x.ai/developers/grok-4-6
- OpenAI 兼容性: xAI 使用 OpenAI Chat Completions API 格式

## 注意事项

1. **Credentials 配置**: 用户需要在系统中配置有效的 xAI API credentials
2. **Model Bindings**: 需要确保有 credential_model_bindings 将 grok-4.6 绑定到可用的 credentials
3. **Provider 配置**: xAI provider 必须在 provider_catalog 中正确配置（code='xai'）
4. **网络访问**: 需要能够访问 https://api.x.ai/v1

## Grok 方言支持总结

网关的 `internal/paramreg/dialect.go` 已经包含了 Grok 方言的支持：

```go
DialectGrok Dialect = "grok"
```

映射关系：
- Catalog code: `xai`, `grok` → `DialectGrok`
- Base protocol: `DialectGrok` → `DialectOpenAIChat` (OpenAI 兼容)
- 参数支持: `reasoning_effort` (低、中、高、超高四个等级)
