# 格式转换审计与方案设计文档

**日期：** 2026-07-17  
**范围：** llm-gateway-go 多厂商数据格式转换兼容性审计、规范下载、现状确认与修改路线图  
**状态：** Design Approved

---

## 执行摘要

### 目标

在不破坏现有 IR 三层架构和已有 8 个格式转换文档的前提下，完成：

1. **下载 9 厂商 API 规范**到 `docs/格式转换/厂家数据格式/`（OpenAI、Anthropic、Gemini、GLM、MiniMax、DeepSeek、Qwen、Ollama、Doubao）
2. **整合 `docs/会话优化v2/` 关键内容**到 `docs/格式转换/09-会话优化集成与统一转换标准.md`
3. **编写现状确认**文档 `10-现状确认与原厂对比.md`，提供字段级差异表
4. **编写问题分级与修改路线图** `11-问题分级与修改路线图.md`，按 P0/P1/P2 分级，给出修改位置、测试验证、门禁要求、验收步骤

### 核心原则（不可妥协）

1. **客户端协议与原厂协议分离**；OpenAI Chat 和 Responses 不共用请求格式
2. **先解析到统一 IR，再按目标原厂协议序列化**；不能用字符串替换代替结构化转换
3. **标准字段必须保留**；供应商私有字段只能在同厂同协议方向恢复，不能跨厂泄漏
4. **多模态能力按 model offer 判断**，不从厂商名称推断
5. **工具调用必须保持 call ID、工具名和结果的关联**；工具轮次不能被压缩或转换拆散
6. **未知字段和未知流式事件必须保留**或产生明确 loss report
7. **不得删除 IR 中的特有保留字段**（Thinking、CacheControl、Documents、FrequencyPenalty、PresencePenalty、Logprobs、TopLogprobs、Seed、ResponseFormat、N、User、Metadata、Reasoning、Extensions 等）

### 新增文件清单

```
docs/格式转换/
├── 01-系统总览与数据流.md                    ← 保持不变
├── 02-客户端协议.md                         ← 保持不变
├── 03-原厂协议适配.md                       ← 保持不变
├── 04-多模态转换.md                         ← 保持不变
├── 05-审计矩阵.md                          ← 保持不变
├── 06-模拟测试与发布门禁.md                  ← 保持不变
├── 07-问题清单与修正记录.md                  ← 保持不变
├── 08-MiniMax-M3-审计与统一标准.md           ← 保持不变
├── 09-会话优化集成与统一转换标准.md           ← 新增
├── 10-现状确认与原厂对比.md                  ← 新增
├── 11-问题分级与修改路线图.md                ← 新增
├── README.md                               ← 增量更新导航表
└── 厂家数据格式/                            ← 新增子目录
    ├── README.md                           ← 下载来源、版本、脱敏说明
    ├── openai/
    │   ├── README.md
    │   ├── chat-completions.md
    │   └── responses.md
    ├── anthropic/
    │   ├── README.md
    │   └── messages.md
    ├── gemini/
    │   ├── README.md
    │   └── generate-content.md
    ├── glm/
    │   ├── README.md
    │   └── glm-api.md
    ├── minimax/
    │   ├── README.md
    │   └── minimax-api.md
    ├── deepseek/
    │   ├── README.md
    │   └── deepseek-chat.md
    ├── qwen/
    │   ├── README.md
    │   └── dashscope-api.md
    ├── ollama/
    │   ├── README.md
    │   └── ollama-chat.md
    └── doubao/
        ├── README.md
        └── volcengine-api.md
```

---

## 架构与数据流（复述现有，作为基线）

### 三层 IR 架构

```text
客户端请求
  → inbound parser (parse_openai.go / parse_anthropic.go / parse_gemini.go)
  → InternalRequest / InternalResponse / StreamChunk (internal/ir/types.go)
  → target provider serializer (serialize_openai.go / serialize_anthropic.go / serialize_gemini.go)
  → 原厂请求或响应
```

### 客户端入口

| 客户端入口 | 处理位置 | 目标 |
|---|---|---|
| `/v1/chat/completions` | `domains/streaming/handler.go` | OpenAI Chat 兼容请求 |
| `/v1/messages` | `domains/streaming/messages.go` | Anthropic Messages 兼容请求 |
| `/v1/responses` | `domains/streaming/responses.go` | Responses input item 转 Chat/IR |
| Gemini native | `internal/ir/parse_gemini.go` | `generateContent` 请求转 IR |

### IR 关键对象

- `InternalRequest`: model、messages、system、tools、tool choice、sampling 和扩展字段
- `Message`: user、assistant、tool、system，以及 content blocks 和 OpenAI `ToolCalls`
- `ContentBlock`: text、image、audio、video、document、tool_use、tool_result、thinking
- `Extensions`: 不属于当前标准字段的原始 JSON，受协议和 provider 边界控制
- `StreamChunk`: 文本、工具、思考、usage 和 finish reason 的流式中间态

### 保真边界

- **同协议往返**：标准字段结构相等，允许 JSON key 顺序变化
- **跨协议转换**：只保证有明确语义映射的字段；不可映射字段必须出现在 loss report
- **TransportIRConverter**：只有在 source/target protocol 和 provider catalog 允许时恢复 Extensions，避免 DeepSeek、GLM、MiniMax 或 Doubao 私有字段泄漏到其他原厂

---

## IR 字段保留与多模态边界

### A. IR 特有保留字段（不得删除）

继承并显式列出当前 `internal/ir/types.go` 已存在的字段，作为不可删除基线：

```
通用:
  Model, Messages, System, Tools, ToolChoice
  MaxTokens, Temperature, TopP, TopK, Stop, ParallelToolCalls, Stream

Anthropic 特有（IR 保留，序列化时按目标协议决定是否输出）:
  Thinking, CacheControl, Documents

OpenAI 特有:
  FrequencyPenalty, PresencePenalty, Logprobs, TopLogprobs
  Seed, ResponseFormat, N, User, Metadata

多模态 & 个性化字段（commit 2a06c704 audit-provider-multimodal）:
  Reasoning
  ContentBlock: text/image/audio/video/document/tool_use/tool_result/thinking

Extensions:
  map[string]interface{}  // 原厂私有字段，由 TransportIRConverter 按同厂同协议方向恢复
```

### B. 多模态边界规则

```
能力来源：
  model offer / profile，**不**从厂商名称推断

判定失败：
  fail closed（产生 unsupported_modality loss report），**不**静默丢媒体

序列化器：
  按目标原厂规范包裹（base64 data URL / file URI / inlineData 等）

未知 block：
  原始 JSON 进入 RawContent / Extensions，并在 loss report 记录

顺序保持：
  ContentBlock 顺序在跨协议转换时**不**能重新排序或合并

MIME 处理：
  - 保留已知 MIME type，不用默认 image/png 覆盖
  - 缺失 MIME 时才使用目标协议允许的默认值
  - 不把 provider file URI 当公网 URL
  - IR 保留 FileURI/FileID 语义

Document 处理：
  必须保留 document source，不得因为不是 image 而转成普通文本占位符
```

### C. 中转站纠错边界

```
允许纠错:
  - 补齐空 usage
  - 闭合半截 SSE
  - 修正 [DONE] 缺失
  - MiniMax EOF without [DONE] 且已有内容时补 [DONE]

不允许纠错:
  - 改写 assistant 文本内容
  - 改写 tool call ID
  - 删除 tool result
  - 把媒体降级成文本占位符

纠错记录:
  - 纠错行为进 telemetry
  - 响应 header 加 X-Gateway-Normalized: <kind> 标记
```

---

## 现状确认结构（10-现状确认与原厂对比.md）

### 目标

把"代码现状 vs 原厂规范"做成一张可一眼看清的差异表，作为方案与代码之间的桥梁。

### 字段级对比矩阵（每个原厂一份）

| 维度 | 原厂规范要求 | 当前代码（路径） | 状态 | 证据 |
|---|---|---|---|---|
| Chat Completions 请求字段 | messages, model, tools, ... | `internal/ir/parse_openai.go:line` | verified/partial/unsupported/unverified | tests |
| Responses input items | input_text, function_call, function_call_output | `domains/streaming/responses.go:line` | ... | ... |
| tool call 关联 | tool_call_id 闭环 | `internal/ir/sanitize_tool_messages.go` | verified | `sanitize_tool_messages_test.go` |
| 多模态：image | image_url / input_image | `internal/ir/parse_openai.go` | verified | `image_base64_test.go` |
| 多模态：audio | input_audio | `internal/ir/parse_openai.go` | partial | endpoint-specific |
| 多模态：video | video_url 等 | ... | unsupported / endpoint-specific | ... |
| 多模态：document | input_file / document block | ... | partial | document fixture required |
| 流式事件 | SSE delta、tool_calls、thinking | `internal/ir/stream.go` | verified for covered events | `stream_test.go` |
| Usage 细项 | prompt_tokens、cached_tokens | `internal/ir/response.go` | verified | response tests |
| 私有字段恢复 | Extensions 同厂同协议 | `domains/transformation/ir_converter.go` | verified | `ir_converter_catalog_test.go` |

### 跨协议映射矩阵

| IR 块 | OpenAI Chat | OpenAI Responses | Anthropic | Gemini |
|---|---|---|---|---|
| text | string/content | input_text | text | part.text |
| image | image_url | input_image | image source | inlineData/fileData |
| audio | input_audio | (endpoint-specific) | (provider-specific) | inlineData |
| video | (endpoint-specific) | (endpoint-specific) | (unsupported base) | inlineData |
| document | input_file | input_file | document | inlineData |
| tool call | tool_calls | function_call | tool_use | functionCall |
| tool result | tool/function_call_output | function_call_output | tool_result | functionResponse |
| thinking | reasoning item | reasoning item | thinking/redacted_thinking | thought |

### 多模态不可丢失清单（每个原厂）

```
必检项（fixture 必须覆盖）:
  - 文本 + image base64
  - 文本 + image URL/file URI
  - 文本 + audio
  - 文本 + video（如原厂支持）
  - 文本 + document
  - 多模态 + tool call（顺序保持）
  - 多模态 + tool result（ID 闭环）
  - 多模态 + thinking 混合
```

### 状态定义（继承 docs/格式转换/README.md）

```
verified       代码 + 测试 + 原厂规范三方一致
partial        主路径已实现，存在 endpoint/字段子集限制
unsupported    明确未实现
unverified     缺一手原厂资料或真实脱敏 fixture
```

---

## 问题分级与修改路线图（11-问题分级与修改路线图.md）

### 问题分级标准

| 等级 | 触发条件 | 处置 |
|---|---|---|
| **P0** | 跨厂商数据丢失、工具 ID 不闭环、多模态静默降级、私有字段跨厂泄漏 | 必须立即修；阻塞晋级 |
| **P1** | 单厂商字段子集缺失、流式事件个别类型未识别、纠错缺失 | 当迭代必修；非阻塞但需登记 |
| **P2** | 历史快照缺失、fixture 覆盖不全、文档与代码细微不一致 | 排期修复 |
| **P3** | 优化项、可读性、可观测性增强 | 视情况 |

### 现状发现模板（每个问题一条）

```markdown
### [等级] 问题标题

- **原厂规范**：<引用 厂家数据格式/xxx/yyy.md 行号>
- **当前代码**：<路径:行号>
- **影响面**：<哪些客户端/厂商组合触发>
- **复现 fixture**：<路径或"缺失">
- **修复方案**：<最小改动描述，遵循 SRP/OCP>
- **回归测试**：<必加测试>
- **门禁**：<哪条 fixture 必须通过>
```

### 路线图（按时间盒而非固定日期）

```
Box A（首次提交必须包含）:
  ✅ 9 厂商规范下载完成 + README 索引
  ✅ docs/格式转换/09 会话优化集成与统一转换标准.md
  ✅ docs/格式转换/10 现状确认与原厂对比.md
  ✅ docs/格式转换/11 问题分级与修改路线图.md（本文件）
  ✅ 现有 8 个文档不做改动
  ✅ 不动 IR 任何保留字段
  ✅ 不动现有 parser/serializer/sanitizer 行为

Box B（后续迭代，按 P0 → P1 → P2 排序）:
  - P0 修复：每个问题一个独立 PR，含 fixture + 单测 + 集成测试
  - 每修复一个 P0 → 在 docs/格式转换/07 问题清单与修正记录.md 登记
  - 修复完毕同步刷新 05 审计矩阵 与 10 现状确认
```

### 修改纪律（铁律）

```
1. 不得删除 IR 中已声明的保留字段（Thinking, CacheControl, Documents,
   FrequencyPenalty, PresencePenalty, Logprobs, TopLogprobs, Seed,
   ResponseFormat, N, User, Metadata, Reasoning, Extensions 等）

2. 不得修改现有 parser/serializer 的公开签名

3. 不得改变现有 fixture 行为预期（除非该 fixture 标记为 outdated）

4. 不得在格式转换修复中顺手改动 usage modality 计费

5. 新增能力必须走"IR + Extensions + 同厂同协议恢复"三件套

6. 多模态：fail closed，不静默降级

7. 工具 ID：闭环校验，孤儿 result 必须 fail

8. 私有字段：跨厂必须隔离，不得泄漏
```

### 测试与门禁

```
必跑:
  go test ./internal/ir
  go test ./domains/transformation
  go test ./domains/streaming
  go vet ./...

新增 fixture 必须覆盖（详见 docs/格式转换/06）:
  - text + image/audio/video/document 各种 source kind
  - 单工具、并行工具、工具结果、错误结果
  - thinking + text/tool 混合
  - 流式 usage、finish_reason、未知事件
  - 非法 MIME、能力不满足、provider error
```

### 验收标准

- 9 厂商规范可被新人在 `docs/格式转换/厂家数据格式/` 离线查阅
- 10-现状确认与原厂对比.md 每行都有代码路径 + 测试路径可点击
- 11-问题分级与修改路线图.md 每条 P0 都有 fixture 路径或明确"fixture 待补"
- Box A 完成后，`docs/格式转换/` 形成 SSOT：新人只需读 01-11 + 厂家数据格式/ 即可理解全貌
- 现有 8 个文档的内容不丢失，作为审计快照保留

---

## 文件写入计划

### Step 1：创建目录骨架（仅目录，不创建空文件）

```
docs/格式转换/厂家数据格式/
docs/格式转换/厂家数据格式/{openai,anthropic,gemini,glm,minimax,deepseek,qwen,ollama,doubao}/
```

### Step 2：下载 9 厂商规范

参考现有 `docs/格式转换/05-审计矩阵.md` 中的原厂标准核验来源：

```
openai/
  - https://platform.openai.com/docs/api-reference/chat
  - https://platform.openai.com/docs/api-reference/responses
  - 保存为 chat-completions.md, responses.md

anthropic/
  - https://docs.anthropic.com/en/api/messages
  - 保存为 messages.md

gemini/
  - https://ai.google.dev/api/generate-content
  - https://ai.google.dev/api/files
  - 保存为 generate-content.md

glm/
  - https://open.bigmodel.cn/dev/api
  - 保存为 glm-api.md

minimax/
  - https://platform.minimaxi.com/docs
  - 保存为 minimax-api.md

deepseek/
  - https://api-docs.deepseek.com/api/create-chat-completion
  - 保存为 deepseek-chat.md

qwen/
  - https://help.aliyun.com/zh/model-studio/developer-reference/
  - 保存为 dashscope-api.md

ollama/
  - https://github.com/ollama/ollama/blob/main/docs/api.md
  - 保存为 ollama-chat.md

doubao/
  - https://www.volcengine.com/docs/82379 (需确认访问权限)
  - 保存为 volcengine-api.md
```

每个厂商子目录下放：
- `README.md`：来源 URL、版本、下载时间、抓取方式（WebFetch / 浏览器导出）
- 规范正文（按原厂文档结构原样保留；如有访问受限则在 README 标注）

### Step 3：新增 3 个 markdown

**`docs/格式转换/09-会话优化集成与统一转换标准.md`**

内容结构：
```
# 会话优化集成与统一转换标准

## 1. 文档整合说明
本文档整合 docs/会话优化v2/ 中与格式转换直接相关的内容：
- 02-多模态能力审计报告.md
- 03-多模态技术方案.md
- 04-厂商标准与适配矩阵.md
- 05-融合实施方案-附件与模型契约.md

原文档保留作为历史快照，本文档提取关键结论形成统一标准。

## 2. 会话压缩约束（继承 08-MiniMax-M3-审计与统一标准.md）
<摘录关键规则>

## 3. 多模态能力矩阵（整合 02/03/04）
<整合后的表格>

## 4. 附件与模型契约（整合 05）
<关键要点>

## 5. 与现有 01-08 的关系
<指针索引>
```

**`docs/格式转换/10-现状确认与原厂对比.md`**

内容结构：
```
# 现状确认与原厂对比

## 1. 对比方法
基于 docs/格式转换/厂家数据格式/ 下载的原厂规范，逐字段对比当前代码。

## 2. OpenAI 对比
<字段级对比矩阵>

## 3. Anthropic 对比
<字段级对比矩阵>

## 4. Gemini 对比
<字段级对比矩阵>

## 5-9. GLM/MiniMax/DeepSeek/Qwen/Ollama/Doubao 对比
<字段级对比矩阵>

## 10. 跨协议映射矩阵
<Section 3 中的表格>

## 11. 多模态不可丢失清单
<Section 3 中的必检项>

## 12. 现状总结
<整体状态、已 verified 的比例、待补的 fixture>
```

**`docs/格式转换/11-问题分级与修改路线图.md`**

内容结构：
```
# 问题分级与修改路线图

## 1. 问题分级标准
<Section 4 中的表格>

## 2. 现状发现（Box A 完成后填充）
### P0 问题
<使用模板逐条列出>

### P1 问题
<使用模板逐条列出>

### P2 问题
<使用模板逐条列出>

## 3. 修改路线图
<Box A/B 时间盒>

## 4. 修改纪律（铁律）
<Section 4 中的 8 条铁律>

## 5. 测试与门禁
<Section 4 中的必跑命令与 fixture 覆盖要求>

## 6. 验收标准
<Section 4 中的验收标准>
```

### Step 4：刷新 docs/格式转换/README.md（增量更新，不重写）

在文档导航表中追加：

```markdown
| [09-会话优化集成与统一转换标准.md](./09-会话优化集成与统一转换标准.md) | 会话压缩约束、多模态能力矩阵、附件与模型契约（整合 docs/会话优化v2/ 关键内容） |
| [10-现状确认与原厂对比.md](./10-现状确认与原厂对比.md) | 基于下载规范的字段级对比、跨协议映射、多模态必检清单 |
| [11-问题分级与修改路线图.md](./11-问题分级与修改路线图.md) | P0/P1/P2 分级、修复方案、测试门禁、验收标准 |
| [厂家数据格式/](./厂家数据格式/) | 9 厂商（OpenAI/Anthropic/Gemini/GLM/MiniMax/DeepSeek/Qwen/Ollama/Doubao）原厂 API 规范下载 |
```

---

## 不做的事（避免破坏现有架构）

```
❌ 不修改 internal/ir/types.go 的任何字段定义
❌ 不修改现有 parse_*.go / serialize_*.go / response.go 行为
❌ 不修改 domains/transformation/ 现有公开 API
❌ 不重写现有 8 个文档（01-08）
❌ 不移动 docs/会话优化v2/ 现有 9 个文档（在 09 中以"指针 + 关键内容摘录"形式整合）
❌ 不顺手改动 usage modality 计费、MaAS rate component
❌ 不在本任务中实现任何 P0 修复（仅记录与分级）
```

---

## 任务边界确认

### 本任务交付物

```
✅ 9 厂商规范下载（docs/格式转换/厂家数据格式/）
✅ docs/格式转换/09 会话优化集成与统一转换标准.md
✅ docs/格式转换/10 现状确认与原厂对比.md
✅ docs/格式转换/11 问题分级与修改路线图.md
✅ docs/格式转换/README.md 导航表更新
```

### 本任务**不**交付

```
❌ 任何代码修改
❌ P0/P1 修复（仅记录）
❌ 新增 parser/serializer
❌ 测试代码改动
```

---

## 实施顺序

1. **创建目录骨架**
2. **下载 9 厂商规范**（按 Step 2 URL 列表，使用 WebFetch 或手动导出）
3. **编写 09-会话优化集成与统一转换标准.md**（整合 docs/会话优化v2/ 关键内容）
4. **编写 10-现状确认与原厂对比.md**（基于下载规范 + 现有代码做字段级对比）
5. **编写 11-问题分级与修改路线图.md**（识别 P0/P1/P2，给出修复方案模板，Box A 完成后填充具体问题）
6. **更新 docs/格式转换/README.md**（增量追加导航表）
7. **自检**：确认 8 条铁律未被破坏、现有 8 个文档未被改动、IR 保留字段 0 删除
8. **提交 commit**：`docs(format-conversion): add vendor specs + status confirmation + roadmap (Box A)`

---

## 设计审批

- [x] Section 1: 设计概览
- [x] Section 2: IR 字段保留与多模态边界
- [x] Section 3: 现状确认结构
- [x] Section 4: 问题分级与修改路线图
- [x] Section 5: 设计收尾 + 文件写入计划

**设计批准日期：** 2026-07-17  
**下一步：** 进入 writing-plans skill，生成详细实施计划
