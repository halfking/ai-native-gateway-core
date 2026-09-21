# Handoff: 多厂商协议对齐审计与 P0/P1 修复

**会话**: sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5  
**日期**: 2026-08-28  
**状态**: ✅ P0 + 全部 P1 已完成；剩余为 P2 数据完整性与路由范围

---

## 执行摘要

对网关非 Big3 厂商（Moonshot/Zhipu/Qwen/MiniMax/Ernie/DeepSeek/Doubao）进行系统性协议对齐审计，对比官方 API 文档与当前实现，发现 **8 项缺陷**（P0: 1, P1: 3, P2: 4）。已完成全部 P0/P1：

- ✅ **P0-MiniMax-1**: HTTP 200 包装的 `base_resp.status_code` 错误信号丢失（流式+非流式）
- ✅ **P1-GLM-2**: GLM 非流式 `finish_reason` 错误通道未检测
- ✅ **P1-Qwen-1**: Qwen/DashScope `content: [{"text":"..."}]` 在流式与非流式 IR 中归一化为文本块
- ✅ **P1-Reasoning**: OpenAI-compatible 响应中的 `reasoning_content` 维持 round-trip；仅保留带原始 signature 的 Anthropic `thinking`，不伪造无签名 thinking

后续仅剩 P2 数据完整性和独立路由范围工作；不存在待实施的 P1 缺陷。

---

## 审计发现（完整列表）

### P0 缺陷（阻塞性）

| 编号 | 厂商 | 问题 | 影响 | 状态 |
|------|------|------|------|------|
| P0-MiniMax-1 | MiniMax | `base_resp.status_code` 被 strip 删除 | 流式+非流式失败被误判为成功（HTTP 200 + base_resp.status_code=1002/1008/1027 等）| ✅ **已修复** (ed64a2291) |

### P1 缺陷（功能性）

| 编号 | 厂商 | 问题 | 影响 | 状态 |
|------|------|------|------|------|
| P1-Qwen-1 | Qwen | `content` 数组结构未解包 | 客户端收到 `[{text:"hello"}]` 而非 `"hello"` | ✅ **已修复**（本轮） |
| P1-GLM-2 | Zhipu GLM | 非流式 `finish_reason` 异常值未检测 | network_error/sensitive/model_context_window_exceeded 被当成功 | ✅ **已修复** (fb188483d) |
| P1-Reasoning | 全厂商 | `reasoning_content` 响应侧安全映射 | OpenAI-compatible round-trip 保留；无签名内容不伪装为 Anthropic thinking | ✅ **已验证并回归覆盖**（本轮） |

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

## P1 完成记录与安全边界

### ✅ P1-Qwen-1: Qwen content 数组归一化

**实现（2026-08-28）**：
- `internal/ir/response.go` 将 Qwen/DashScope 风格的无 `type` 文本块 `[{"text":"..."}]` 映射为 IR `text` 内容块。
- `internal/ir/stream.go` 同时支持 `delta.content` 为标准 JSON string 或结构化文本数组；多个显式 text 值按顺序合并。
- 不引入 `providerHint` 或扩大 IR 接口：该安全兼容规则由结构本身决定，标准 OpenAI typed text 块仍保持原行为。
- 新增 `internal/ir/qwen_content_test.go`，覆盖非流式及流式 string、文本数组、多文本块、空数组与 null。

### ✅ P1-Reasoning: 响应侧安全映射

**复核结论（2026-08-28）**：不新增第二套 `Message.ReasoningContent` 字段。IR 已有携带 signature 的 Anthropic `thinking` 内容块；重复表示会损害块排序和签名语义。

**保证的行为**：
- OpenAI-compatible 响应的顶层 `reasoning_content` 解析为 `InternalResponse.ReasoningContent`，再序列化为 OpenAI-compatible 响应时保持不变。
- 原生 Anthropic `thinking` 块仅在保留其原始 `signature` 时输出；签名路径维持无损。
- 无 Anthropic signature 的 vendor `reasoning_content` 不会被伪造成 Anthropic `thinking`。正文仍可转换，这项保守限制避免生成无法验证的 Anthropic 历史上下文。
- 新增 `internal/ir/reasoning_response_test.go` 固定上述 round-trip 和签名安全语义。

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

### 本轮 P1 文件与测试
- **P1-Qwen-1**:
  - `internal/ir/response.go`: 识别无 `type` 的 `{ "text": "..." }` 结构化响应块
  - `internal/ir/stream.go`: 归一化 string 或结构化文本数组 `delta.content`
  - `internal/ir/qwen_content_test.go`: 非流式及流式结构化文本回归覆盖

- **P1-Reasoning**:
  - `internal/ir/reasoning_response_test.go`: OpenAI-compatible reasoning round-trip、已签名 Anthropic thinking 保留、无签名 vendor reasoning 不伪造 thinking
  - 不修改 `Message` 结构体；`InternalResponse.ReasoningContent` 与已有带 signature 的 thinking 内容块各自承担正确语义

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

### 本轮新增验证
- ✅ P1-Qwen-1：Qwen 数组 content 的流式与非流式归一化测试（string、文本数组、多文本块、空数组、null）。
- ✅ P1-Reasoning：OpenAI-compatible `reasoning_content` round-trip、原生 Anthropic 带 signature thinking 保留、无 signature vendor reasoning 不生成 Anthropic thinking。
- ✅ 本轮回归：`go test ./internal/ir ./domains/transformation -count=1`、`go test ./domains/streaming ./domains/streaming/executors -count=1`。

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

### P2 数据完整性（可并行）
1. **P2-MiniMax-2**：保留 `input_sensitive_type` / `output_sensitive_type` 的细粒度内容审核分类（预计 8h）。
2. **P2-Ernie-1**：保留 `search_info.search_results[]` 的搜索来源引用（预计 6h）。
3. **P2-Doubao-1**：评估多模态 embedding 独立端点的路由和能力注册；该项不是 IR 转换器问题。

### 长期优化
4. 提取 MiniMax vendor-specific 错误解析至独立内部包，消除 `streaming` 与 `executors` 的重复内联实现。
5. 评估对更多厂商扩展字段的显式 IR/extension 映射，例如 Ernie `system_memory` 和 Qwen `search_options`。

---

## 可并行子任务建议

后续会话可启动两个独立子 Agent：
- **Agent A**：实施 P2-MiniMax-2，重点验证错误检测仍发生在 vendor 字段 strip 之前。
- **Agent B**：实施 P2-Ernie-1，重点验证跨协议序列化不会丢失引用数组或生成无效 content block。

两项没有直接依赖；合并前须运行 `internal/ir`、`domains/transformation`、`domains/streaming` 和相关 executor 回归。

---

## Handoff 检查清单

- [x] 所有代码已提交并推送至 main
- [x] 测试全部通过（streaming / executors / ir）
- [x] 审计报告已归档（`docs/2026-08-28-vendor-protocol-alignment-audit.md`）
- [x] P0 + 全部 P1 缺陷已修复或以协议安全边界验证并回归覆盖
- [x] 剩余 P2 缺陷有明确实施路径
- [ ] 当前工作树包含其他并行任务改动；提交时仅暂存本审计任务文件
- [x] Git 历史按缺陷保持独立提交

---

**移交给**: 新会话实施 P2-MiniMax-2、P2-Ernie-1 或长期 vendor 解析重构
**联系人**: 当前会话 sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5  
**日期**: 2026-08-28
