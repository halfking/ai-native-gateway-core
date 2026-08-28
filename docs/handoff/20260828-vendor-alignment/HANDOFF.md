# Handoff: 多厂商协议对齐审计与 P0/P1 修复

**会话**: sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5  
**日期**: 2026-08-28  
**状态**: ✅ P0 + 2/4 P1 已完成，剩余 P1 缺陷待实施  

---

## 执行摘要

对网关非 Big3 厂商（Moonshot/Zhipu/Qwen/MiniMax/Ernie/DeepSeek/Doubao）进行系统性协议对齐审计，对比官方 API 文档与当前实现，发现 **8 项缺陷**（P0: 1, P1: 3, P2: 4）。已修复：

- ✅ **P0-MiniMax-1**: HTTP 200 包装的 `base_resp.status_code` 错误信号丢失（流式+非流式）
- ✅ **P1-GLM-2**: GLM 非流式 `finish_reason` 错误通道未检测

剩余 **2 项 P1 缺陷** 需继续实施（估算 2-3 天）。

---

## 审计发现（完整列表）

### P0 缺陷（阻塞性）

| 编号 | 厂商 | 问题 | 影响 | 状态 |
|------|------|------|------|------|
| P0-MiniMax-1 | MiniMax | `base_resp.status_code` 被 strip 删除 | 流式+非流式失败被误判为成功（HTTP 200 + base_resp.status_code=1002/1008/1027 等）| ✅ **已修复** (ed64a2291) |

### P1 缺陷（功能性）

| 编号 | 厂商 | 问题 | 影响 | 状态 |
|------|------|------|------|------|
| P1-Qwen-1 | Qwen | `content` 数组结构未解包 | 客户端收到 `[{text:"hello"}]` 而非 `"hello"` | ❌ **待修复** |
| P1-GLM-2 | Zhipu GLM | 非流式 `finish_reason` 异常值未检测 | network_error/sensitive/model_context_window_exceeded 被当成功 | ✅ **已修复** (fb188483d) |
| P1-Reasoning | 全厂商 | `reasoning_content` 字段无统一 IR 映射 | 推理输出在协议转换时丢失或格式不一致 | ❌ **待修复** |

### P2 缺陷（数据完整性，优先级较低）

| 编号 | 厂商 | 问题 | 影响 |
|------|------|------|------|
| P2-MiniMax-2 | MiniMax | `input_sensitive_type`/`output_sensitive_type` 被删 | 内容审核细粒度分类丢失 |
| P2-Ernie-1 | Baidu Ernie | `search_info.search_results[]` 被删 | 搜索来源引用丢失 |
| P2-DeepSeek-1 | DeepSeek | `reasoning_content` 已保留但 IR 未映射 | 纳入 P1-Reasoning 统一修复 |
| P2-Doubao-1 | Doubao | 多模态 embedding 独立端点未映射 | 不支持视觉向量化 |

---

## 已完成修复详情

### ✅ P0-MiniMax-1: base_resp.status_code 错误信号检测

**Commit**: `ed64a2291`  
**文件**:
- `domains/streaming/minimax_error.go` (新增): 解析器 + 分类器 + 格式化函数
- `domains/streaming/minimax_error_test.go` (新增): 单元测试（8 status codes + 边缘情况）
- `domains/streaming/executors/executor_chat.go`: 非流式路径在 L1319 检测（stripVendorFields 之前）
- `domains/streaming/stream.go`: 流式路径在 stripChunkFields 中检测并中断

**根因**: MiniMax 用 HTTP 200 + `{base_resp: {status_code: 1002/1008/1027/1039, status_msg}}` 编码错误，而非标准 4xx/5xx。`stripMinimaxFieldsBody` 在检测之前删除 `base_resp`，导致错误信号永久丢失。

**修复**:
- **非流式**: `executor_chat.go` L1319 在 strip 之前调用 `parseMiniMaxBaseResp`，检测 `status_code != 0` 时返回 `*upstreampkg.Error{Kind: classified}`
- **流式**: `stream.go` L1318 `stripChunkFields` 返回 `(line, errCode, errMsg)`，调用方 L1045 检测 `errCode != 0` 时中断流并返回 `StreamOutcome{Interrupted: true, Kind: classified}`
- **分类映射**: 1002→RateLimit, 1004→Auth, 1008→Quota, 1027→ContentFilter, 1039→ContextLength, 1001→Timeout, 2013→ClientBug

**测试**: 全量 streaming + executors 测试通过（67s + 17s）

---

### ✅ P1-GLM-2: GLM finish_reason 错误通道（非流式）

**Commit**: `fb188483d`  
**文件**:
- `internal/ir/parse_error.go` (新增): `ParseError` 类型（包装 `errorsx.ErrorKind`）
- `internal/ir/response.go`: `ParseOpenAIResponse` 在 L330 检测 GLM 异常 `finish_reason`，返回 `*ParseError`
- `domains/streaming/executors/executor_chat.go`: Q2 转换路径 L1358 用 `errors.As` 检测 `*ParseError`，返回 `*upstreampkg.Error`
- `internal/ir/response_glm_test.go` (新增): 测试 3 个错误值 + 4 个正常值

**根因**: GLM 流式路径已在 `ae1ecaedf` 修复（`anthropic_stream.go` L431-469），但非流式路径的 `ParseOpenAIResponse` 直接赋值 `ir.FinishReason = choice.FinishReason`，未检测异常值。

**修复**:
- `ParseOpenAIResponse` 在解析 `finish_reason` 后，检测三个异常值：
  - `network_error` → `ParseError{Kind: KindNetwork}`
  - `sensitive` → `ParseError{Kind: KindContentFilter}`
  - `model_context_window_exceeded` → `ParseError{Kind: KindContextLength}`
- executor Q2 路径检测 `*ParseError` 并转换为 `*upstreampkg.Error`（否则会把原始 OpenAI 格式响应转发给 Anthropic 客户端，仍然显示为成功）

**测试**: 全量 IR 测试通过（0.2s）

---

## 剩余 P1 缺陷实施路径

### ❌ P1-Qwen-1: content 数组结构未解包

**工作量**: 4-6 小时  
**位置**: `internal/ir/parse_openai.go` (需新增 Qwen 专用分支)

**方案**:
1. 扩展 `ParseOpenAIResponseChunk` / `ParseOpenAIResponse`，新增 `providerHint` 参数（或从上下文传入 `cand.CatalogCode`）
2. 检测 `providerHint == "qwen" || "dashscope"` 时，解包 `content: [{"text":"..."}]` → `content: "..."`：
   ```go
   if providerHint == "qwen" || providerHint == "dashscope" {
       if contentArr, ok := msg.Content.([]any); ok && len(contentArr) > 0 {
           if obj, ok := contentArr[0].(map[string]any); ok {
               if text, ok := obj["text"].(string); ok {
                   msg.Content = text
               }
           }
       }
   }
   ```
3. 确保 `providerHint` 从 executor 层传入（需修改 `IRConverter` 接口，或在 `scopedConverter` 中注入）
4. **测试**: `TestQwenContentArrayUnpacking` 验证 `[{"text":"hello"}]` → `"hello"`

**风险**: `ParseOpenAIResponse` 签名变更会影响所有调用者（约 10 处），需逐一修改。替代方案：在 `scopedConverter.ParseOpenAIResponse` 中根据 `providerID` 后处理。

---

### ❌ P1-Reasoning: reasoning_content 统一 IR 映射

**工作量**: 1-2 天  
**位置**: `internal/ir/types.go`, `internal/ir/serialize_*.go` (3 个序列化器)

**方案**:
1. 扩展 `IR.Message` 新增 `ReasoningContent string`：
   ```go
   type Message struct {
       Role             string
       Content          string
       ReasoningContent string // Kimi/GLM/Qwen/MiniMax/DeepSeek/Ernie 推理过程
       // ... 现有字段
   }
   ```

2. **解析器填充**（OpenAI 侧，`parse_openai.go`）:
   ```go
   if reasoningContent, ok := msg["reasoning_content"].(string); ok {
       irMsg.ReasoningContent = reasoningContent
   }
   ```

3. **序列化器输出**（按目标协议）:
   - **Anthropic 序列化器** (`serialize_anthropic.go`): `ReasoningContent` → 单独 `thinking` 内容块（`type: "thinking"`）
   - **OpenAI 序列化器** (`serialize_openai.go`): 原样输出 `reasoning_content` 字段
   - **Gemini 序列化器** (`serialize_gemini.go`): 暂不支持（Gemini 无推理字段）

4. **测试**:
   - `TestReasoningContentIRRoundtrip`: Kimi 响应 → IR → Anthropic 请求（保留推理块）
   - `TestReasoningContentProtocolConversion`: GLM 流式 → IR → OpenAI 客户端（保留 `reasoning_content`）

**依赖**: 需要明确 Anthropic `thinking` 块的序列化格式（索引编号、stop 事件时机）

---

## 文档与代码位置

### 核心文档
- **审计报告**: `docs/2026-08-28-vendor-protocol-alignment-audit.md` (505 行)
  - 7 厂商协议差异矩阵（请求/响应 × 流式/非流式）
  - IR 超集映射缺失分析
  - 8 项缺陷详细描述 + 修复方案 + 实施时间表
  - 官方文档链接汇总

### 已修复代码
- **P0-MiniMax-1**:
  - `domains/streaming/minimax_error.go` (89 行)
  - `domains/streaming/minimax_error_test.go` (131 行)
  - `domains/streaming/executors/executor_chat.go` (L1319-1338, +64 行 inline 函数)
  - `domains/streaming/stream.go` (L1045-1077, L1318-1350, +108 行)
  
- **P1-GLM-2**:
  - `internal/ir/parse_error.go` (22 行)
  - `internal/ir/response.go` (L1, L327-345, +26 行)
  - `internal/ir/response_glm_test.go` (97 行)
  - `domains/streaming/executors/executor_chat.go` (L1358-1377, +14 行)

### 待修改文件（剩余 P1）
- **P1-Qwen-1**:
  - `internal/ir/parse_openai.go` (需新增 Qwen 专用分支)
  - `internal/ir/serialize_openai.go` (需确保 content 序列化正确)
  - 调用者层（executor/converter）需传入 `providerHint`

- **P1-Reasoning**:
  - `internal/ir/types.go` (扩展 `Message` 结构体)
  - `internal/ir/parse_openai.go` (填充 `ReasoningContent`)
  - `internal/ir/serialize_anthropic.go` (输出 `thinking` 块)
  - `internal/ir/serialize_openai.go` (输出 `reasoning_content`)
  - `internal/ir/serialize_gemini.go` (暂不支持，文档说明)

---

## Git 提交历史

```
fb188483d fix(ir): P1-GLM-2 detect GLM finish_reason error channel in non-stream path
ed64a2291 fix(streaming): P0-MiniMax-1 detect base_resp.status_code error signal
c4b1fad70 docs(vendors): multi-vendor protocol alignment audit report
ae1ecaedf fix(streaming): second-round audit corrections on Q2 bridge
88ee49dd1 docs: document anthropic non-stream empty-response failover
```

**关键修改统计**:
- P0-MiniMax-1: +385 行（含测试）
- P1-GLM-2: +159 行（含测试）
- 审计报告: +505 行

---

## 测试覆盖

### 已验证
- ✅ `domains/streaming` 全量测试: 67.1s (通过)
- ✅ `domains/streaming/executors` 全量测试: 17.8s (通过)
- ✅ `internal/ir` 全量测试: 0.2s (通过)
- ✅ MiniMax 单元测试: 8 status codes + 3 边缘情况（无 base_resp / 无效 JSON / 空 body）
- ✅ GLM 单元测试: 3 错误 finish_reason + 4 正常 finish_reason

### 待补充（剩余 P1）
- ❌ P1-Qwen-1: 需测试 Qwen 数组 content 解包
- ❌ P1-Reasoning: 需测试 reasoning_content 跨协议转换（OpenAI ↔ Anthropic）

---

## 环境与依赖

- **Go 版本**: 1.22+ (使用 `errors.As` for type assertion)
- **关键依赖**:
  - `github.com/kaixuan/llm-gateway-go/errorsx` (ErrorKind 枚举)
  - `github.com/kaixuan/llm-gateway-go/upstream` (UpstreamError 类型)
  - `github.com/stretchr/testify` (测试断言)

- **循环依赖注意**:
  - `streaming` ↔ `executors` 循环：MiniMax error 函数在两个包中分别内联实现（`parseMiniMaxBaseResp` / `parseMiniMaxBaseRespInline`）
  - 未来重构建议：提取到 `internal/vendor/minimax` 独立包

---

## 后续任务优先级

### 立即执行（本周内）
1. **P1-Qwen-1**: content 数组解包（4-6h）
2. **P1-Reasoning**: reasoning_content 统一映射（1-2d）

### 中期（2-4 周）
3. **P2-MiniMax-2**: 内容审核细粒度字段保留（8h）
4. **P2-Ernie-1**: 搜索结果引用保留（6h）

### 长期优化
5. 重构 vendor-specific 逻辑到独立包（避免循环依赖）
6. 扩展 IR 支持更多厂商扩展字段（如 Ernie `system_memory`, Qwen `search_options`）

---

## 子任务分配建议（多 Agent 并行）

可启动 **2 个并行 Agent** 分别处理剩余 P1 缺陷：

### Agent A: P1-Qwen-1 修复
**输入**: `docs/2026-08-28-vendor-protocol-alignment-audit.md` § P1-Qwen-1  
**输出**: PR with `internal/ir/parse_openai.go` 修改 + 测试  
**估时**: 4-6h

### Agent B: P1-Reasoning 修复
**输入**: `docs/2026-08-28-vendor-protocol-alignment-audit.md` § P1-Reasoning  
**输出**: PR with IR 扩展 + 3 序列化器修改 + 测试  
**估时**: 1-2d

两者**无依赖**，可完全并行。完成后合并到 main 并更新审计报告状态。

---

## Handoff 检查清单

- [x] 所有代码已提交并推送至 main
- [x] 测试全部通过（streaming / executors / ir）
- [x] 审计报告已归档（`docs/2026-08-28-vendor-protocol-alignment-audit.md`）
- [x] P0 + 2/4 P1 缺陷已修复并验证
- [x] 剩余 P1 缺陷有明确实施路径
- [x] 工作树干净（unrelated changes 已 stash）
- [x] Git 历史清晰（独立 commit per 缺陷）

---

**移交给**: 新会话继续实施 P1-Qwen-1 + P1-Reasoning  
**联系人**: 当前会话 sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5  
**日期**: 2026-08-28
