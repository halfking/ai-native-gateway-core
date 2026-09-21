# 多厂商协议对齐 P1 缺陷修复 — 后续会话提示词

## 上下文

前序会话（sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5）已完成多厂商协议对齐审计并修复 P0 + 2/4 P1 缺陷。审计报告见 `docs/2026-08-28-vendor-protocol-alignment-audit.md`，handoff 文档见 `docs/handoff/20260828-vendor-alignment/HANDOFF.md`。

**已完成**:
- ✅ P0-MiniMax-1: HTTP 200 包装的 base_resp.status_code 错误信号检测（流式+非流式）
- ✅ P1-GLM-2: GLM 非流式 finish_reason 错误通道检测

**剩余任务**:
- ❌ P1-Qwen-1: Qwen content 数组结构解包（`[{"text":"hello"}]` → `"hello"`）
- ❌ P1-Reasoning: reasoning_content 字段统一 IR 映射（支持 Anthropic thinking 块转换）

---

## 主提示词（用于新会话）

```
继续多厂商协议对齐审计的 P1 缺陷修复。前序会话已完成 P0-MiniMax-1 和 P1-GLM-2，剩余两项 P1 缺陷需要修复：

1. **P1-Qwen-1**: Qwen/DashScope 官方 API 返回 `content: [{"text":"..."}]` 数组结构，但网关 IR 解析器未解包，导致客户端收到数组而非字符串。需在 `internal/ir/parse_openai.go` 的 `ParseOpenAIResponse` / `ParseOpenAIResponseChunk` 中添加 Qwen 专用分支，检测 `providerHint == "qwen"` 时解包 content 数组。

2. **P1-Reasoning**: 7 个厂商（Kimi/GLM/Qwen/MiniMax/DeepSeek/Ernie）均返回 `reasoning_content` 字段存储推理过程，但 IR 未统一映射，导致协议转换时丢失。需扩展 `internal/ir/types.go` 的 `Message` 结构体新增 `ReasoningContent` 字段，并在 3 个序列化器中正确输出（Anthropic → thinking 块，OpenAI → reasoning_content 字段，Gemini 暂不支持）。

请阅读 `docs/handoff/20260828-vendor-alignment/HANDOFF.md` 和 `docs/2026-08-28-vendor-protocol-alignment-audit.md` 了解完整上下文，然后：
- 优先修复 P1-Qwen-1（4-6 小时）
- 再修复 P1-Reasoning（1-2 天）
- 每个修复独立提交，包含单元测试，运行全量测试确保无破坏
- 完成后推送到 main 并更新审计报告状态

如果你想加速执行，可以启动两个并行子 Agent 分别处理这两项任务（它们无依赖关系）。
```

---

## 并行子 Agent 模式提示词

如果选择并行执行，在主会话中使用以下提示词启动两个子 Agent：

### 主 Agent 协调提示词

```
启动两个并行子 Agent 完成多厂商协议对齐的剩余 P1 缺陷修复。两者无依赖，可同时进行：

**Agent A 任务**: 修复 P1-Qwen-1（Qwen content 数组解包）
- 读取 `docs/2026-08-28-vendor-protocol-alignment-audit.md` § P1-Qwen-1 实施方案
- 修改 `internal/ir/parse_openai.go` 在 ParseOpenAIResponse 中检测 providerHint=="qwen" 时解包 `content: [{"text":"..."}]` → `"..."`
- 编写 `TestQwenContentArrayUnpacking` 测试
- 运行 `go test ./internal/ir -count=1` 验证
- 提交为独立 commit：`fix(ir): P1-Qwen-1 unpack Qwen content array structure`

**Agent B 任务**: 修复 P1-Reasoning（reasoning_content 统一映射）
- 读取 `docs/2026-08-28-vendor-protocol-alignment-audit.md` § P1-Reasoning 实施方案
- 扩展 `internal/ir/types.go` Message 新增 `ReasoningContent string`
- 修改 `parse_openai.go` 填充该字段
- 修改 `serialize_anthropic.go` 输出 thinking 块
- 修改 `serialize_openai.go` 输出 reasoning_content 字段
- 编写跨协议转换测试
- 运行 `go test ./internal/ir ./domains/transformation -count=1` 验证
- 提交为独立 commit：`feat(ir): P1-Reasoning unified reasoning_content mapping across protocols`

两个 Agent 完成后汇总结果，推送到 main，更新审计报告状态标记为"P1 全部完成"。
```

### Agent A 专用提示词

```
修复 P1-Qwen-1: Qwen/DashScope content 数组解包。

**背景**: Qwen 官方 API 返回 `output.choices[].message.content: [{"text":"..."}]` 数组结构（与 OpenAI 的 string 类型不同），网关 IR 解析器未处理，导致客户端收到数组而非字符串。

**任务**:
1. 读取 `docs/2026-08-28-vendor-protocol-alignment-audit.md` § 1.3 Qwen 响应差异表格和 § 3 P1-Qwen-1 修复方案
2. 修改 `internal/ir/parse_openai.go`:
   - 在 `ParseOpenAIResponse` 函数中，解析 choices[].message.content 后，检测 providerHint（需从调用者传入或从上下文获取）
   - 如果 `providerHint == "qwen" || providerHint == "dashscope"`，且 content 是 `[]any` 类型且第一个元素是 `map[string]any` 包含 `"text"` 键，则解包为 string
3. 同样修改 `ParseOpenAIResponseChunk`（流式路径）
4. 编写测试 `internal/ir/parse_openai_qwen_test.go`:
   - TestQwenContentArrayUnpacking: 验证数组 → 字符串
   - TestQwenContentArrayEmpty: 验证空数组处理
   - TestNormalContentString: 验证非 Qwen 厂商仍按 string 处理
5. 运行 `go test ./internal/ir -v -run Qwen`
6. 提交：`fix(ir): P1-Qwen-1 unpack Qwen content array to string`

**注意**: 如果修改 ParseOpenAIResponse 签名（新增 providerHint 参数）会影响 ~10 处调用者，优先考虑在 `scopedConverter.ParseOpenAIResponse` 中后处理。
```

### Agent B 专用提示词

```
修复 P1-Reasoning: reasoning_content 字段统一 IR 映射。

**背景**: Kimi/GLM/Qwen/MiniMax/DeepSeek/Ernie 六个厂商均返回 `reasoning_content` 字段存储推理过程（o1 系列模型），但 IR 未映射该字段，导致协议转换时丢失（如 OpenAI → Anthropic 时无法生成 thinking 块）。

**任务**:
1. 读取 `docs/2026-08-28-vendor-protocol-alignment-audit.md` § 2.3 IR Response 字段缺失表格和 § 3 P1-Reasoning 修复方案
2. 扩展 `internal/ir/types.go`:
   ```go
   type Message struct {
       Role             string
       Content          string
       ReasoningContent string // 推理过程（Kimi/GLM/Qwen/MiniMax/DeepSeek/Ernie）
       // ... 现有字段
   }
   ```
3. 修改 `internal/ir/parse_openai.go`:
   - 在 ParseOpenAIResponse / ParseOpenAIResponseChunk 中填充 `ir.ReasoningContent = choice.Message.ReasoningContent`（已有此字段但未映射到 IR）
4. 修改 `internal/ir/serialize_anthropic.go`:
   - 如果 IR.Message.ReasoningContent 非空，输出单独的 thinking 内容块：`{type: "thinking", thinking: reasoningContent}`
   - 确认 Anthropic 官方格式（索引编号、stop 事件时机）
5. 修改 `internal/ir/serialize_openai.go`:
   - 输出 `reasoning_content` 字段（原样保留）
6. 在 `internal/ir/serialize_gemini.go` 中文档说明暂不支持（Gemini 无推理字段）
7. 编写测试 `internal/ir/reasoning_roundtrip_test.go`:
   - TestReasoningContentIRRoundtrip: Kimi 响应 → IR → Anthropic 请求（保留 thinking 块）
   - TestReasoningContentOpenAIPassthrough: GLM 响应 → IR → OpenAI 客户端（保留 reasoning_content）
8. 运行 `go test ./internal/ir ./domains/transformation -v -run Reasoning`
9. 提交：`feat(ir): P1-Reasoning unified reasoning_content mapping with Anthropic thinking block support`

**依赖**: 需确认 Anthropic thinking 块格式（查阅官方文档或测试已有的 Q2 bridge 实现）
```

---

## 使用说明

1. **串行执行**: 直接复制"主提示词"到新会话
2. **并行执行**: 
   - 复制"主 Agent 协调提示词"到新会话
   - ZCode 会自动启动两个子 Agent 并分配 Agent A/B 专用提示词
   - 主 Agent 等待子 Agent 完成后汇总结果

---

**创建日期**: 2026-08-28  
**前序会话**: sess_e73b023f-f2fd-4e95-975f-29ed3009d4b5  
**版本**: v1.0
