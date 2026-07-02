# llm-gateway-go 架构重构 - 任务分解与提示词

## 项目概述

**目标**: 重构 llm-gateway-go 的会话传输与转换架构，实现统一的 IR 转换层、明确的会话状态机、完整的客户端适配器和高效的附件处理。

**当前状态**: Phase 1 完成（审计 + 核心模块设计）  
**工作目录**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`

---

## Phase 2: 客户端适配器增强

### 任务 2.1: 实现 IRTransformer 接口基础设施

**预计时间**: 2-3 小时

**提示词**:
```
我正在重构 llm-gateway-go 的客户端适配器架构。请帮我完成以下任务：

1. 查看 ARCHITECTURE_REFACTOR_GUIDE.md 了解整体方案
2. 基于 domains/streaming/client_adapter.go，扩展以下接口：
   - IRTransformer 接口（TransformRequestIR, TransformResponseIR）
   - SessionAwareAdapter 接口（OnSessionStart, OnSessionEnd）
   - StreamAwareAdapter 接口（OnStreamStart, OnStreamChunk, OnStreamEnd）

3. 实现一个 BaseIRAdapter 基类，提供默认实现（空操作）

4. 为 CursorAdapter 实现完整的 IRTransformer：
   - TransformRequestIR: 确保所有 tool_call 有 ID，检测长上下文
   - TransformResponseIR: 格式化输出
   - 添加单元测试

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

要求：
- 参考 domains/session/state_machine.go 和 context.go 的设计风格
- 每个接口都要有详细的注释说明使用场景
- 单元测试覆盖率 >85%
- 完成后运行 go test ./domains/streaming/... 验证

完成后创建 git commit 并推送。
```

---

### 任务 2.2: Windsurf 和 Copilot 适配器迁移

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 2。请帮我完成：

1. 为 WindsurfAdapter 实现 IRTransformer 接口：
   - TransformRequestIR: 类似 Cursor，补充 tool_call_id
   - 支持长上下文和工具调用追踪
   - 单元测试

2. 为 CopilotAdapter 实现 IRTransformer 接口：
   - TransformRequestIR: 优化 max_tokens（限制 1024）
   - 优先低延迟（GetOptimizationHints）
   - 单元测试

3. 在 domains/streaming/client_adapter_registry.go 中：
   - 添加适配器注册机制
   - 支持按 User-Agent 自动选择适配器
   - 添加适配器能力查询（SupportIRTransform）

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- domains/session/state_machine.go（回调模式）
- ARCHITECTURE_REFACTOR_GUIDE.md（适配器示例）

完成后创建 git commit 并推送。
```

---

### 任务 2.3: 其余 5 个客户端适配器迁移

**预计时间**: 3-4 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 2。请帮我完成剩余客户端适配器的迁移：

1. VSCodeAdapter - 轻量级，类似 Copilot
2. ZedAdapter - 重质量，类似 Cursor
3. JetBrainsAdapter - 支持多语言，长上下文
4. ClaudeCodeAdapter - 原生 Anthropic 协议
5. RooCodeAdapter - 混合模式

每个适配器需要：
- 实现 IRTransformer 接口
- 定义 GetOptimizationHints（性能偏好）
- 至少 3 个单元测试用例（正常/边界/错误）
- 在注册表中注册

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考已完成的 CursorAdapter、WindsurfAdapter、CopilotAdapter。

完成标准：
- 所有适配器测试通过
- 覆盖率 >80%
- go vet 无警告

完成后创建 git commit 并推送。
```

---

## Phase 3: 统一转换层实现

### 任务 3.1: UnifiedTransport 核心实现

**预计时间**: 4-5 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 3。请实现统一转换层：

1. 在 domains/transformation/unified_transport.go 中实现 UnifiedTransport：
   - TransformRequest(ctx, SessionContext): Client → IR → Upstream
   - TransformResponse(ctx, SessionContext, body): Upstream → IR → Client
   - TransformStream(ctx, SessionContext, resp, writer): 统一流式处理

2. 核心转换流程：
   - 协议检测（ProtocolDetector）
   - 扩展字段提取（ExtensionExtractor，lossless round-trip）
   - Client Protocol → IR 解析
   - 客户端适配器 IR 转换（调用 IRTransformer）
   - IR → Upstream Protocol 序列化
   - 模型映射和参数调整

3. 流式处理：
   - 逐块读取上游 SSE
   - Upstream Chunk → IR StreamChunk
   - 客户端适配器转换（StreamAwareAdapter）
   - IR StreamChunk → Client Protocol SSE

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- ARCHITECTURE_REFACTOR_GUIDE.md 中的 UnifiedTransport 设计
- domains/transformation/ir_transport.go（现有 IR 转换）
- domains/streaming/stream.go（流式处理）

要求：
- 复用 ir.ParseOpenAI、ir.SerializeAnthropic 等现有函数
- 不要删除 ir_transport.go 和 legacy_transport.go（保持兼容）
- 单元测试覆盖所有协议组合（OpenAI ↔ Anthropic）

完成后创建 git commit 并推送。
```

---

### 任务 3.2: UnifiedTransport 集成到 Factory

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 3。请完成 UnifiedTransport 的集成：

1. 修改 domains/transformation/factory.go：
   - 添加 unifiedTransport *UnifiedTransport 字段
   - 在 NewTransportFactory 中初始化
   - 添加 Unified() *UnifiedTransport 方法
   - Pick() 方法支持选择 UnifiedTransport（通过环境变量 USE_UNIFIED_TRANSPORT）

2. 实现灰度策略（优先级）：
   - 环境变量 USE_UNIFIED_TRANSPORT=true 强制启用
   - 租户白名单 UNIFIED_TENANT_WHITELIST
   - 模型白名单 UNIFIED_MODEL_WHITELIST
   - 百分比灰度 UNIFIED_ROLLOUT_PERCENT（基于 hash 稳定分配）
   - 默认使用 IRTransport（向后兼容）

3. 添加监控指标：
   - unified_transport_requests_total（按 protocol、client_type）
   - unified_transport_duration_seconds
   - unified_transport_errors_total

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- 现有的 TransportFactory 灰度逻辑
- domains/transformation/metrics.go

完成后创建 git commit 并推送。
```

---

### 任务 3.3: UnifiedTransport 端到端测试

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 3。请完成 UnifiedTransport 的测试：

1. 在 domains/transformation/unified_transport_test.go 中添加：
   - TestUnifiedTransport_OpenAIToAnthropic（非流式）
   - TestUnifiedTransport_AnthropicToOpenAI（非流式）
   - TestUnifiedTransport_StreamOpenAIToAnthropic
   - TestUnifiedTransport_StreamAnthropicToOpenAI
   - TestUnifiedTransport_WithClientAdapter（调用 IRTransformer）
   - TestUnifiedTransport_ErrorHandling（协议不支持、解析失败等）

2. 在 domains/transformation/integration_test.go 中添加：
   - TestFactory_PickUnifiedTransport（灰度策略）
   - TestUnifiedTransport_RoundTrip（Client → Upstream → Client，lossless）
   - TestUnifiedTransport_AttachmentHandling（mock）

3. 性能基准测试：
   - BenchmarkUnifiedTransport_TransformRequest
   - BenchmarkUnifiedTransport_TransformResponse
   - 对比 IRTransport 的性能（差异 <10%）

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

要求：
- 使用 httptest 构造 mock 上游响应
- 覆盖率 >90%
- 所有测试通过

完成后创建 git commit 并推送。
```

---

## Phase 4: 附件处理优化

### 任务 4.1: 附件 IR 转换器实现

**预计时间**: 3-4 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 4。请实现附件 IR 转换器：

1. 在 domains/attachments/ir_transformer.go 中实现：
   - TransformRequest(ctx, requestID, ir): 扫描 IR 中的 base64 图片
   - processImageBlock(): 将 data URI 保存到存储，替换为 URL
   - PersistMetadata(ctx, db, requestID, attachments): 元数据写入数据库

2. 核心逻辑：
   - 遍历 ir.Messages[].Content[]，找到 type=image 且 Image.Type=base64
   - 构造 data URI: "data:{mediaType};base64,{data}"
   - 调用 storage.SaveBase64Image() 保存文件
   - 替换 IR: Image.Type="url", Image.URL=storageURL, Image.Data=""
   - 返回 []AttachmentMetadata（hash, path, size, content_type）

3. 存储原则：
   - 文件存储在 storage backend（Local/OSS/S3）
   - 数据库只存 JSONB: {hash, path, size, content_type, created_at}
   - SHA256 去重，相同内容只存一份

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- domains/attachments/storage.go（现有存储接口）
- domains/attachments/extractor.go（OpenAI 格式提取）

单元测试：
- TestIRTransformer_TransformRequest
- TestIRTransformer_TransformRequest_EmptyIR
- TestIRTransformer_TransformRequest_MultipleImages
- TestIRTransformer_TransformRequest_StorageFailure（best-effort）

完成后创建 git commit 并推送。
```

---

### 任务 4.2: 附件处理集成到 UnifiedTransport

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 4。请将附件处理集成到 UnifiedTransport：

1. 修改 domains/transformation/unified_transport.go：
   - 添加 attachmentTransformer *attachments.IRTransformer 字段
   - 在 NewUnifiedTransport() 中初始化（需要 storage 和 baseURL）
   - 在 TransformRequest() 中调用 attachmentTransformer.TransformRequest()
   - 将返回的 attachments 存入 SessionContext.Attachments

2. 修改 domains/session/context.go（如果需要）：
   - 确保 SessionContext.Attachments []attachments.AttachmentMetadata 已定义

3. 在 UnifiedTransport.TransformRequest 的流程中插入：
   ```
   // 4. 附件处理（在 IR 转换后，序列化前）
   if t.attachmentTransformer != nil {
       attachments, _ := t.attachmentTransformer.TransformRequest(ctx, sc.RequestID, sc.ClientIR)
       sc.Attachments = attachments
   }
   ```

4. 添加测试：
   - TestUnifiedTransport_WithAttachments
   - TestUnifiedTransport_AttachmentURLReplacement

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

要求：
- 附件处理失败不应阻断请求（best-effort）
- 记录 warning 日志

完成后创建 git commit 并推送。
```

---

### 任务 4.3: 数据库迁移 - 添加 attachments 列

**预计时间**: 1-2 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 4。请创建数据库迁移：

1. 在 db/migrations/ 中创建新的迁移文件：
   - 文件名: XXX_add_attachments_to_request_logs.up.sql
   - 添加 JSONB 列: ALTER TABLE request_logs ADD COLUMN attachments JSONB;
   - 创建 GIN 索引: CREATE INDEX idx_request_logs_attachments ON request_logs USING GIN (attachments);
   - 添加注释: COMMENT ON COLUMN request_logs.attachments IS '附件元数据列表';

2. 创建 down 迁移:
   - 文件名: XXX_add_attachments_to_request_logs.down.sql
   - DROP INDEX idx_request_logs_attachments;
   - ALTER TABLE request_logs DROP COLUMN attachments;

3. 测试迁移：
   - 运行 make migrate-up 验证
   - 运行 make migrate-down 验证回滚
   - 确认迁移幂等性

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- db/migrations/ 中的现有迁移文件格式
- PostgreSQL JSONB 最佳实践

完成后创建 git commit 并推送。
```

---

## Phase 5: 集成与部署

### 任务 5.1: ChatHandler 集成状态机和 UnifiedTransport

**预计时间**: 4-5 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 5。请将新架构集成到 ChatHandler：

1. 修改 cmd/gateway/main.go：
   - 初始化 SessionStateMachine，注册回调
   - 初始化 UnifiedTransport（附件支持）
   - 注入到 ChatHandler

2. 修改 ChatHandler.ServeHTTP：
   - 创建 SessionContext
   - 状态转换: INITIAL → RECEIVING_FROM_CLIENT
   - 调用 UnifiedTransport.TransformRequest (如果启用)
   - 状态转换: RECEIVING_FROM_CLIENT → PENDING_TO_LLM
   - 路由选择和模型映射
   - 状态转换: PENDING_TO_LLM → SENDING_TO_LLM
   - 调用上游 LLM
   - 状态转换: SENDING_TO_LLM → RECEIVING_FROM_LLM
   - 调用 UnifiedTransport.TransformResponse/TransformStream
   - 状态转换: RECEIVING_FROM_LLM → COMPLETED
   - 持久化附件元数据（PersistMetadata）

3. 注册状态机回调：
   - StateReceivingFromClient: SessionAuditHook.Check
   - StatePendingToLLM: SessionCompressor.Prepare
   - StateCompleted: AuditSink.Emit

4. Feature Flag 控制：
   - 环境变量 USE_UNIFIED_TRANSPORT=true 启用新架构
   - 默认使用现有流程（IRTransport）

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

参考:
- ARCHITECTURE_REFACTOR_GUIDE.md 中的 ChatHandler 示例
- 现有的 handler 逻辑

要求：
- 保持现有功能完整性
- 添加详细日志（request_id, state_transitions）
- 错误处理和回滚

完成后创建 git commit 并推送。
```

---

### 任务 5.2: MessagesHandler 集成（Anthropic 端点）

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 5。请将新架构集成到 MessagesHandler：

1. 修改 MessagesHandler.ServeHTTP：
   - 类似 ChatHandler，使用 SessionStateMachine 和 UnifiedTransport
   - 客户端协议固定为 anthropic-messages
   - 支持流式和非流式

2. 与 ChatHandler 的差异：
   - 不需要协议检测（已知 Anthropic）
   - 可能需要特殊的 Anthropic 参数处理

3. Feature Flag: 使用相同的 USE_UNIFIED_TRANSPORT

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建 git commit 并推送。
```

---

### 任务 5.3: 集成测试和性能测试

**预计时间**: 3-4 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 5。请完成集成测试和性能测试：

1. 在 tests/integration/ 中添加：
   - TestChatHandler_WithUnifiedTransport_OpenAI
   - TestChatHandler_WithUnifiedTransport_Anthropic
   - TestChatHandler_WithUnifiedTransport_Stream
   - TestChatHandler_WithUnifiedTransport_Attachments
   - TestChatHandler_StateMachineCallbacks（验证回调执行）

2. 性能对比测试：
   - BenchmarkChatHandler_IRTransport_vs_UnifiedTransport
   - 测量延迟、吞吐量、内存占用
   - 目标：UnifiedTransport 性能损失 <5%

3. 压力测试：
   - 并发 1000 QPS，持续 5 分钟
   - 监控错误率、P99 延迟、内存泄漏

4. 功能验证清单：
   - [ ] 8 个客户端适配器正常工作
   - [ ] OpenAI ↔ Anthropic 协议转换正确
   - [ ] 流式和非流式都正常
   - [ ] 附件保存到存储并替换 URL
   - [ ] 状态机回调正常执行
   - [ ] 错误处理和降级正常

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建测试报告和 git commit，推送。
```

---

### 任务 5.4: 灰度发布和监控

**预计时间**: 2-3 小时

**提示词**:
```
继续 llm-gateway-go 架构重构 Phase 5。请准备灰度发布：

1. 更新配置文件（config/production.yaml）：
   - USE_UNIFIED_TRANSPORT: false（初始）
   - UNIFIED_ROLLOUT_PERCENT: 0
   - UNIFIED_TENANT_WHITELIST: ""
   - UNIFIED_MODEL_WHITELIST: ""

2. 添加监控指标（如果未添加）：
   - unified_transport_enabled（gauge）
   - unified_transport_rollout_percent（gauge）
   - unified_transport_requests_total（counter）
   - unified_transport_errors_total（counter）
   - unified_transport_duration_seconds（histogram）
   - state_machine_transitions_total（counter，按 state）

3. 创建灰度发布计划文档（GRADUAL_ROLLOUT_PLAN.md）：
   - Week 1: 内部测试（UNIFIED_TENANT_WHITELIST=internal）
   - Week 2: 1% 灰度（UNIFIED_ROLLOUT_PERCENT=1）
   - Week 3: 10% 灰度（监控错误率和延迟）
   - Week 4: 50% 灰度
   - Week 5: 100% 灰度（USE_UNIFIED_TRANSPORT=true）
   - Week 6: 删除 Legacy Transport 和 anthropic_bridge

4. 回滚预案：
   - 设置 USE_UNIFIED_TRANSPORT=false 立即回滚
   - 保留旧代码至少 2 周

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建 git commit 并推送。
```

---

## Phase 6: 清理和文档

### 任务 6.1: 删除弃用代码

**预计时间**: 1-2 小时

**提示词**:
```
llm-gateway-go 架构重构的最后清理工作：

1. 删除弃用的代码（在 UnifiedTransport 稳定运行 2 周后）：
   - domains/transformation/legacy_transport.go
   - domains/streaming/anthropic_bridge.go（如果完全不需要）
   - _to_be_deleted/ 目录

2. 更新 TransportFactory：
   - 移除 legacyTransport 字段
   - 移除灰度逻辑（统一使用 UnifiedTransport）

3. 更新相关测试：
   - 删除 Legacy Transport 相关测试
   - 确保所有测试通过

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建 git commit: "chore: remove deprecated transport layers"，推送。
```

---

### 任务 6.2: 更新文档和 API 文档

**预计时间**: 2-3 小时

**提示词**:
```
llm-gateway-go 架构重构的文档更新：

1. 更新 README.md：
   - 添加架构概述（状态机 + UnifiedTransport）
   - 更新性能指标
   - 添加客户端适配器列表

2. 更新 docs/architecture.md：
   - 数据流图（Client → SessionContext → IR → Upstream → IR → Client）
   - 状态机图（7 个状态）
   - 附件处理流程图

3. 创建 docs/client-adapters.md：
   - 8 个客户端适配器的特性对比
   - 如何添加新的客户端适配器
   - IRTransformer 接口使用指南

4. 创建 docs/attachment-storage.md：
   - 附件存储架构（文件系统 + 元数据）
   - 支持的存储后端（Local/OSS/S3）
   - 配置示例

5. 更新 API 文档（如果有 OpenAPI/Swagger）：
   - 附件相关的字段说明

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建 git commit 并推送。
```

---

### 任务 6.3: 发布重构完成公告

**预计时间**: 1 小时

**提示词**:
```
llm-gateway-go 架构重构完成，请帮我创建发布公告：

1. 在 CHANGELOG.md 中添加条目：
   - 版本号: v2.0.0（重大架构变更）
   - 发布日期
   - 主要变更列表
   - Breaking Changes（如果有）
   - 性能改进数据

2. 创建 ARCHITECTURE_REFACTOR_COMPLETE.md：
   - 重构前后对比
   - 性能提升数据
   - 功能完整性确认
   - 后续优化方向

3. 准备团队分享材料（ARCHITECTURE_REFACTOR_PRESENTATION.md）：
   - 为什么重构
   - 重构了什么
   - 如何使用新架构
   - 最佳实践

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

完成后创建 git commit，打 tag v2.0.0，推送。
```

---

## 使用说明

### 如何使用这些提示词

1. **按顺序执行**: 任务之间有依赖关系，建议按 Phase 2 → 3 → 4 → 5 → 6 的顺序完成
2. **独立会话**: 每个任务可以在新的 AI 会话中执行，提示词已包含足够的上下文
3. **检查点**: 每个任务完成后都会 git commit，便于回滚和审查
4. **并行执行**: 同一 Phase 内的某些任务可以并行（如 2.2 和 2.3）

### 每个任务的输出

- ✅ 代码实现（.go 文件）
- ✅ 单元测试（_test.go 文件）
- ✅ Git commit（带详细 commit message）
- ✅ 推送到远程仓库

### 估算总时间

- Phase 2: 7-10 小时
- Phase 3: 8-11 小时
- Phase 4: 6-9 小时
- Phase 5: 11-15 小时
- Phase 6: 4-6 小时

**总计**: 36-51 小时（约 5-7 个工作日）

---

## 注意事项

1. **环境变量**: 所有 Feature Flag 都通过环境变量控制，便于灰度发布
2. **向后兼容**: 新架构完全独立，可与旧代码共存
3. **测试覆盖**: 每个模块都要求 >80% 的测试覆盖率
4. **性能基准**: 新架构性能损失应 <5%
5. **文档同步**: 代码变更后及时更新文档

---

**祝重构顺利！🚀**
