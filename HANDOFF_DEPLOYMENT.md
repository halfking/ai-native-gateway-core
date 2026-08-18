# LLM Gateway 新增模型支持 - 部署与后续任务

## 已完成工作（Commit 6d9baf795）

### 新增模型支持

1. **grok-4.6** (xAI)
   - 500k 上下文
   - Vision 模型（支持图像+文本输入）
   - 推理能力：low, medium, high, xhigh

2. **glm-5.3** (Zhipu AI / Z.AI)
   - 1M 上下文
   - 文本模型
   - 推理能力：low, high, max（强制开启，不可关闭）

3. **Kimi 系列** (Moonshot AI)
   - `kimi-k3`: 1M 上下文，multimodal（文本+图像+视频）
   - `kimi-k2.6`: 256k 上下文，vision（文本+图像）
   - `kimi-k2.7-code`: 256k 上下文，文本模型（代码专用）
   - `kimi-k2.7-code-highspeed`: 文本模型（高速代码）

4. **Gemini 3 系列** (Google)
   - `gemini-3.6-flash`
   - `gemini-3.5-flash`
   - `gemini-3.5-flash-lite`
   - `gemini-3.1-flash-lite`
   - `gemini-3.1-flash-lite-image`
   - `gemini-3.1-pro-preview`
   - `gemini-3.1-flash-image`
   - `gemini-3-pro-image`
   - `gemini-3-flash-preview`
   - `gemini-omni-flash`

### 技术变更

- 新增迁移：352, 353, 354, 355, 356（含对应的 .down.sql 回滚脚本）
- 更新 `modelname/modality_defaults.go` 和测试
- 更新 `internal/reasoncap/reasoning_defaults.go` 和测试
- 所有迁移使用 JSONB 追加模式，避免覆盖现有模型目录
- 回滚脚本使用精确删除，避免误删其他迁移添加的模型

### 测试状态
✅ 所有 Go 测试通过
✅ 迁移 SQL 语法已验证（使用实际 schema 字段）
⚠️  **未执行实际数据库迁移**

---

## 部署步骤

### 前置检查

```bash
# 1. 确认数据库连接
export DATABASE_URL="postgresql://user:password@host:port/dbname"
psql "$DATABASE_URL" -c "SELECT version();"

# 2. 备份当前 provider_catalog
psql "$DATABASE_URL" -c "
COPY (SELECT code, models_manifest_json FROM provider_catalog 
      WHERE code IN ('xai', 'zhipu', 'moonshot', 'google-gemini'))
TO '/tmp/provider_catalog_backup_$(date +%Y%m%d_%H%M%S).csv' CSV HEADER;
"

# 3. 备份 models_canonical 和 model_aliases
pg_dump "$DATABASE_URL" -t models_canonical -t model_aliases -f /tmp/models_backup_$(date +%Y%m%d_%H%M%S).sql
```

### 执行迁移

```bash
cd /path/to/llm-gateway-go

# 运行迁移（352-356）
./scripts/run-migrations-strict.sh

# 验证迁移结果
psql "$DATABASE_URL" -c "
SELECT canonical_name, context_window, modality, status 
FROM models_canonical 
WHERE canonical_name IN ('grok-4.6', 'glm-5.3', 'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
ORDER BY canonical_name;
"

# 验证 provider manifest
psql "$DATABASE_URL" -c "
SELECT code, jsonb_pretty(models_manifest_json) 
FROM provider_catalog 
WHERE code IN ('xai', 'zhipu', 'moonshot', 'google-gemini');
"
```

### 回滚（如遇问题）

```bash
# 按逆序回滚迁移
psql "$DATABASE_URL" -f sql/migrations/domain/356_update_provider_catalogs_latest_models.down.sql
psql "$DATABASE_URL" -f sql/migrations/domain/355_add_gemini_3_series.down.sql
psql "$DATABASE_URL" -f sql/migrations/domain/354_add_latest_models_glm_kimi.down.sql
psql "$DATABASE_URL" -f sql/migrations/domain/353_update_xai_provider_add_grok_4_6.down.sql
psql "$DATABASE_URL" -f sql/migrations/domain/352_add_grok_4_6.down.sql

# 或恢复备份
psql "$DATABASE_URL" < /tmp/models_backup_YYYYMMDD_HHMMSS.sql
```

---

## 后续必要任务

### 1. Provider Credentials 配置

新增模型需要对应的 API credentials 才能实际调用。需要在 `credentials` 和 `credential_model_bindings` 表中配置：

```sql
-- 检查是否有可用的 xAI credentials
SELECT id, provider_id, name, status 
FROM credentials 
WHERE provider_id = (SELECT id FROM providers WHERE code = 'xai');

-- 如果没有，需要添加 xAI API key
-- INSERT INTO credentials (provider_id, tenant_id, name, api_key, status) VALUES ...

-- 绑定模型到 credentials
-- INSERT INTO credential_model_bindings (credential_id, raw_model_name, canonical_id) 
-- SELECT credential_id, 'grok-4.6', (SELECT id FROM models_canonical WHERE canonical_name = 'grok-4.6')
-- FROM credentials WHERE provider_id = ...;
```

同样需要配置：
- Zhipu AI (Z.AI) credentials for glm-5.3
- Moonshot credentials for kimi-k3, kimi-k2.6, kimi-k2.7-code 系列
- Google Gemini credentials for Gemini 3 系列

### 2. 模型发现与同步

运行模型发现任务，让网关从 provider API 同步模型列表到 `provider_models` 表：

```bash
# 触发模型发现（具体命令取决于网关实现）
curl -X POST http://localhost:8080/internal/admin/discover-models \
  -H "Authorization: Bearer ADMIN_TOKEN" \
  -d '{"provider_codes": ["xai", "zhipu", "moonshot", "google-gemini"]}'
```

或通过定时任务等待自动发现。

### 3. 端到端测试

```bash
# 测试 grok-4.6
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer USER_API_KEY" \
  -d '{
    "model": "grok-4.6",
    "messages": [{"role": "user", "content": "Hello"}],
    "reasoning_effort": "medium"
  }'

# 测试 glm-5.3（reasoning 强制开启）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer USER_API_KEY" \
  -d '{
    "model": "glm-5.3",
    "messages": [{"role": "user", "content": "解释量子纠缠"}],
    "reasoning_effort": "high"
  }'

# 测试 kimi-k3（multimodal）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer USER_API_KEY" \
  -d '{
    "model": "kimi-k3",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "这张图片里有什么？"},
          {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}
        ]
      }
    ]
  }'
```

### 4. 监控与告警

配置监控指标：
- 新模型的请求量
- 新模型的成功率
- 新模型的延迟分布
- credential 可用性

### 5. 文档更新

- 更新用户文档，说明新模型的能力和限制
- 更新 API 文档中的模型列表
- 更新定价信息（如果有）

---

## 已知限制与注意事项

1. **GLM-5.3 reasoning 强制开启**
   - 官方要求 reasoning 不可关闭
   - `CanDisable: false` 已在代码中配置
   - 用户请求中即使设置 `reasoning_effort: "none"` 也会被忽略

2. **Kimi K2.7 Code 系列的多模态能力**
   - 官方文档未明确声明视觉输入支持
   - 当前配置为文本模型
   - 如后续官方确认支持视觉，需修改 modality_defaults.go

3. **Gemini 3 系列上下文窗口**
   - 迁移中未填充具体数值（NULL）
   - 需通过 Google API 的 `models.get` 端点查询实际值
   - 或等待模型发现任务自动更新

4. **Provider manifest 追加模式**
   - 所有 manifest 更新使用 JSONB 追加
   - 不会删除现有模型
   - 但可能导致重复条目（已用 DISTINCT 去重）

5. **迁移幂等性**
   - 所有迁移使用 `ON CONFLICT ... DO UPDATE`
   - 可安全重复执行
   - 回滚脚本使用精确删除，不会误删其他模型

---

## 技术债务与改进建议

1. **自动化上下文窗口同步**
   - 当前需要手动在迁移中填充 context_window
   - 建议实现从官方 API 自动同步的机制

2. **Reasoning 能力的数据库存储**
   - 当前 reasoning 能力只存在于 Go 代码
   - `models_canonical` 表没有 `reasoning_caps` 列
   - 建议添加该列并通过迁移持久化

3. **Provider manifest 版本管理**
   - 当前 manifest 更新没有版本追踪
   - 建议添加 manifest_version 字段

4. **模型能力的动态查询**
   - 当前模型能力（上下文、modality、reasoning）在代码中硬编码
   - 建议实现从 provider API 动态查询的机制

---

## Handoff Prompt for Next Session

```
Continue work on LLM Gateway model support deployment:

1. Deploy the committed changes (commit 6d9baf795) to staging environment:
   - Run database migrations 352-356
   - Verify models_canonical and model_aliases entries
   - Verify provider_catalog manifest updates

2. Configure provider credentials:
   - Add/verify xAI credentials for grok-4.6
   - Add/verify Z.AI credentials for glm-5.3
   - Add/verify Moonshot credentials for kimi-k3, kimi-k2.6, kimi-k2.7-code series
   - Add/verify Google Gemini credentials for Gemini 3 series

3. Run end-to-end tests:
   - Test grok-4.6 with reasoning (low/medium/high/xhigh)
   - Test glm-5.3 with mandatory reasoning
   - Test kimi-k3 with multimodal input (text + image)
   - Test kimi-k2.6 with vision input
   - Test Gemini 3 series models

4. Monitor and validate:
   - Check request success rates
   - Verify modality routing (vision models accept image inputs)
   - Verify reasoning parameter handling

5. Address known issues:
   - Query and fill actual context_window values for Gemini 3 models
   - Verify kimi-k2.7-code series modality (if official docs updated)
   - Add reasoning_caps column to models_canonical table (if needed)

Reference:
- Deployment steps: see HANDOFF_DEPLOYMENT.md in repo root
- Commit: 6d9baf795
- Files changed: 17 files, 576 insertions
```

保存此 handoff 到会话中供下次继续。
