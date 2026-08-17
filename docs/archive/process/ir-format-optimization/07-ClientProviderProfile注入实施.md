# ClientProviderProfile 注入实施完成

**日期**: 2026-07-12
**状态**: ✅ 最小化版本完成；剩余测试修复 + 文档收尾

## 实施范围

### 已完成

1. **TransportContext 扩展** (`domain/transport.go`)
   - `ClientCatalogCode string`：客户端入口 provider 的 catalog code（待路由层填充）
   - `UpstreamCatalogCode string`：上游候选的 catalog code（executor 已设置）
   - 向后兼容：字段为空时不改变现有行为

2. **TransportIRConverter 增强** (`domains/transformation/ir_converter.go`)
   - 新增字段：`context *domain.TransportContext`
   - 新增方法：`SetContext(*domain.TransportContext)`
   - 新增方法：`restoreRequestExtensions()` 检查 catalog code 匹配
   - 行为矩阵：
     | SourceProtocol | ClientCatalog | UpstreamCatalog | 行为 |
     |----------------|---------------|------------------|------|
     | == target      | ""            | ""               | 恢复（向前兼容）|
     | == target      | == upstream   | (any)            | 恢复（same-provider）|
     | == target      | != upstream   | != ""             | 不恢复（cross-provider）|
     | != target      | (any)         | (any)             | 不恢复（cross-protocol）|

3. **测试** (`domains/transformation/ir_converter_catalog_test.go`)
   - `TestTransportIRConverter_SameProviderRestoresExtensions`
   - `TestTransportIRConverter_CrossProviderDoesNotRestoreExtensions`
   - `TestTransportIRConverter_NoCatalogHintRestoresWhenProtocolMatches`

4. **Executor 注入点**
   - `domains/streaming/executors/executor_chat.go`: Anthropic → OpenAI 转换前注入
   - `domains/streaming/executors/executor_anthropic.go`: OpenAI → Anthropic 转换前注入
   - 使用 `interface{ SetContext() }` 类型断言，避免破坏 `irAdapter` 等其他实现

### 设计权衡

**最小可行方案 (MVP)**：
- 当前实现上游 candidate 的 catalog code 是精确的（来自路由查询）
- 客户端入口的 catalog code 暂时为空（需要路由层改造才能填充）
- 当 `ClientCatalogCode` 为空时，回退到仅协议匹配（保持向前兼容）
- 当路由层实施完成后，本方案自动升级为完整的 same-provider 检测

### 已知遗留

- `ClientCatalogCode` 等待路由层注入
- 两个遗留测试（Ollama/GLM extensions）需要补充 `SourceProtocol`，与本次改动无关
- `executor_chat_provider_test.go` 的 StreamHandler 类型断言需调整

## 后续任务

### P1：完整路由层注入
- 在路由解析时识别客户端入口的 provider/credential
- 通过 `ExecParams` 或 `TransportContext.ClientCatalogCode` 传递
- 执行链可直接命中 catalog-aware 路径

### P2：ProviderProfile 完整化
- 增加 `provider_profile` 表或配置
- 按 profile 限制 `RequestExtensions.Headers` allowlist
- 用真实脱敏 capture 验证 DeepSeek/GLM/Qwen/MiniMax 字段

## 验证

- `go test ./domains/transformation -run 'Catalog' -count=1`：通过
- 已有测试需要少量 `SourceProtocol` 补充修复

## 关键文件

| 文件 | 改动 |
|------|------|
| `domain/transport.go` | +10 行（catalog code 字段）|
| `domains/transformation/ir_converter.go` | +26 行（context 字段 + SetContext + restoreRequestExtensions）|
| `domains/transformation/ir_converter_catalog_test.go` | 新增 80 行 |
| `domains/streaming/executors/executor_chat.go` | +15 行（context 注入）|
| `domains/streaming/executors/executor_anthropic.go` | +11 行（context 注入）|

合计：约 142 行新增，0 行删除。
