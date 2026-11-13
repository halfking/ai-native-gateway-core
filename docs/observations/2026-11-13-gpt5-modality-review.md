# 后续发现：其他可能需要修复的 OpenAI 模型

## 观察

在修复 gpt-6 系列后，发现数据库中还有其他 OpenAI 模型可能存在类似问题：

### gpt-5 系列中标记为 text 的模型

1. gpt-5-image - **特别可疑**（名字明确包含 "image"）
2. gpt-5-image-mini - **特别可疑**（名字明确包含 "image"）
3. gpt-5.2-chat-latest
4. gpt-5.2-pro
5. gpt-5.4-image-2 - **特别可疑**（名字明确包含 "image"）
6. gpt-5.5-pro
7. gpt-5.6-astra
8. gpt-5.6-luna-pro
9. gpt-5.6-sol-pro
10. gpt-5.6-terra-pro
11. gpt-5.6-terra-pro …

### 分析

**高优先级**（名字明确表明支持图片）：
- `gpt-5-image`
- `gpt-5-image-mini`
- `gpt-5.4-image-2`

这些模型名字中明确包含 "image"，但却被标记为 text，这几乎肯定是错误的。

**中优先级**（-pro 变体）：
多个 gpt-5.x-pro 变体被标记为 text，而它们的非 pro 版本是 multimodal。
这可能表明：
1. Pro 版本确实不支持多模态（不太可能）
2. 或者这是数据不一致（更可能）

### 建议

1. **立即修复**：带 "image" 关键字的模型
   ```sql
   UPDATE models_canonical
   SET modality = 'multimodal', modality_source = 'inferred', updated_at = now()
   WHERE canonical_name LIKE 'gpt-5%-image%' 
     AND modality = 'text';
   ```

2. **调查后修复**：-pro 变体
   - 需要查阅 OpenAI 官方文档确认这些 pro 变体是否支持多模态
   - 或者查看供应商返回的模型能力信息

3. **代码层面**：当前 `inferModalityFromName` 已经有 `gpt-5` 前缀匹配，
   理论上新发现的模型应该会被正确推断。可能是历史数据问题。

### 其他发现

还有一些旧版本模型（gpt-3.5-turbo, gpt-4 等）被标记为 text，
这是正确的，因为这些早期模型确实不支持多模态。

## 结论

- ✅ gpt-6 系列已完全修复
- ⚠️ gpt-5 系列中有 11 个模型需要进一步调查
- 🔴 特别是包含 "image" 的 3 个模型几乎肯定需要修复

建议在下一个维护窗口期处理这些发现。
