# Grok-4.6 配置验证清单

## 已完成的配置更改

### 1. 数据库迁移文件
- ✅ `sql/migrations/domain/352_add_grok_4_6.sql` - 添加 grok-4.6 到 models_canonical 和 model_aliases
- ✅ `sql/migrations/domain/352_add_grok_4_6.down.sql` - 回滚脚本
- ✅ `sql/migrations/domain/353_update_xai_provider_add_grok_4_6.sql` - 更新 xAI provider_catalog
- ✅ `sql/migrations/domain/353_update_xai_provider_add_grok_4_6.down.sql` - 回滚脚本

### 2. 代码配置
- ✅ `modelname/modality_defaults.go` - 添加 grok-4.6 为 vision 模型（支持图像输入）
- ✅ `internal/reasoncap/reasoning_defaults.go` - 添加 grok-4.6 推理能力配置
  - Dialect: DialectGrok
  - Efforts: low, medium, high, xhigh
  - CanDisable: true

### 3. 模型配置详情

#### 上下文窗口
- 500k tokens (500,000)

#### 模态支持
- 输入：文本 + 图像
- 输出：文本

#### 推理能力
- 支持的等级：low, medium, high, xhigh
- 可以禁用推理
- 使用 Grok dialect（reasoning_effort 参数）

#### OpenAI 兼容性
- 完全兼容 OpenAI Chat Completions API
- Base URL: https://api.x.ai/v1
- 需要 xAI API Key

## 验证步骤

### 运行数据库迁移

```bash
# 设置数据库连接
export DATABASE_URL="postgresql://user:password@host:port/dbname"

# 运行迁移
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
./scripts/run-migrations-strict.sh
```

### 验证数据库记录

```sql
-- 检查 models_canonical
SELECT id, canonical_name, family, provider_name, context_window_k, modality, reasoning_caps
FROM models_canonical
WHERE canonical_name = 'grok-4.6';

-- 检查 model_aliases
SELECT ma.id, ma.canonical_id, ma.raw_name, ma.status
FROM model_aliases ma
JOIN models_canonical mc ON ma.canonical_id = mc.id
WHERE mc.canonical_name = 'grok-4.6';

-- 检查 provider_catalog
SELECT code, name, model_list
FROM provider_catalog
WHERE code = 'xai';
```

### 验证代码配置

```bash
# 测试 modality 推断
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test -v ./modelname -run TestInferModality

# 测试 reasoning 能力推断
go test -v ./internal/reasoncap -run TestResolve
```

### API 测试

创建一个测试请求验证网关是否正确识别 grok-4.6：

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

## 预期行为

1. ✅ 网关应该识别 `grok-4.6` 模型（不再报 "Model 'grok-4.6' is not supported"）
2. ✅ 模型应该被识别为 vision 模型（支持图像输入）
3. ✅ 推理参数 `reasoning_effort` 应该被正确处理
4. ✅ 请求应该被正确路由到 xAI provider

## 配置依赖

### provider_catalog 必须配置
确保 provider_catalog 中有 xAI provider 的记录，且 code='xai'

### credentials 必须配置
用户需要在系统中配置有效的 xAI API credentials

### provider_models 绑定
需要确保有 credential_model_bindings 将 grok-4.6 绑定到可用的 credentials

## 官方文档参考

- xAI API 文档: https://docs.x.ai/api
- Grok-4.6 模型详情: https://docs.x.ai/developers/grok-4-6
- OpenAI 兼容端点: https://api.x.ai/v1
