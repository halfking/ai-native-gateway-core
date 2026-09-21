# IR 审计关键发现摘要（来自 agent_f3859a6a）

## P1 问题（重要，近期修复）

### 1. 流式转换数据丢失风险
- **位置**：`domains/transformation/ir_transport.go:486-527`
- **问题**：SSE chunk 解析失败时静默跳过，未保存原始数据
- **影响**：客户端收到不完整的流式响应
- **修复**：添加 RawDataLogger 和 AnomalyReporter 记录

### 2. session_turns 表缺少协议字段
- **位置**：`sql/objects/tables/session_turns.sql`
- **问题**：缺少 `client_protocol`, `upstream_protocol`, `ir_metadata`
- **影响**：无法追溯协议转换历史
- **修复**：添加 SQL 字段和索引

### 3. parse 函数未知字段处理不一致
- **位置**：`internal/ir/parse_openai.go`, `parse_anthropic.go`
- **问题**：部分 parse 函数未调用 `ReportUnknownField`
- **影响**：扩展字段静默丢失
- **修复**：统一添加未知字段检测和报告

## P2 问题（中等，计划修复）

1. IR 缺少 tags/annotation 字段
2. IR 缺少项目/任务/用户元数据（GatewayMeta）
3. 扩展字段恢复失败缺少降级策略
4. session_bodies 增量存储缺少快照机制

## P3 问题（低优，长期优化）

1. requestdetail Meta 缺少协议信息
2. IR 缺少路由元数据（RoutingMeta）
3. Protocol Buffer 序列化路径缺失
4. IR 往返转换可能丢失块结构

## 架构优势

- ✅ IR 三层架构清晰（Parser → IR → Serializer）
- ✅ 已修复 Gemini SafetySettings 静默丢失问题
- ✅ Extensions 扩展机制设计完善
- ✅ 错误处理和异常报告体系健全

## 风险点

- ⚠️ 流式转换错误容错过于宽松
- ⚠️ 存储层与 IR 结构存在映射缺失
- ⚠️ 缺少统一的 IR 完整性验证框架

---

详细报告见：`/Users/xutaohuang/.zcode/cli/agents/sess_15d75c70-8085-4139-9ad4-d71fff7995b2/agent_f3859a6a-d8d0-467b-950f-eb6e840ceaf2/output.txt`
