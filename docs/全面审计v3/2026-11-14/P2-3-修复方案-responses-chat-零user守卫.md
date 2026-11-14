# P2-3 架构误判结论（2026-11-14 深度验证后撤回）

> **结论：P2-3 不是真实缺陷。** responses→chat 转换路径不经过 responses trim；sessioncontext Reduce 保护首条/最新 human user；responses trim 只作用于 responses 原生上游（那些上游的零 user 约束是它们自己的规范）。本档保留为架构验证记录。

## 一、原问题陈述（已证伪）

responses trim (`compressResponsesInput`) 可产出零 user 的 responses input；当该 body 经 responses→chat 转换后发往 chat 上游，会复现 r1048 事故（智谱 400 code 1214「messages 参数非法」）。首版在 trim 层加守卫被既有钉测 `TestCompressResponsesInputIfNeededKeepsFunctionCallPairAtomic` 当场抓红（「删旧 user 保末项 function pair 原子」是 trim 层合法契约）⇒ 已撤回，登记为 P2-3。

## 二、架构事实

1. **/v1/responses 主路径已于 2026-11-12 迁移到 IR 主路径**（domains/streaming/responses.go:425 邻域注释）：BodyBytes 直接送 responses 原文，由 executor 按客户端协议分派 `ir.ParseResponses`；手写 `convertResponsesToChatBody` 退役为 IR 断路器应急回退。
2. **responses→chat 转换的唯一主路径**：`internal/ir/serialize_openai.go:385 serializeOpenAIMessages` —— 由 `SerializeOpenAI` (13行) 在 202 行调用，将 `InternalRequest.Messages` 逐个转为 OpenAI chat format。
3. **responses→responses 不经过此函数**：走 `SerializeResponses`，零 user 形状在 responses 原生上游里合法。

## 三、守卫设计（唯一正确层级）

### 接线位置
`internal/ir/serialize_openai.go:385 serializeOpenAIMessages` 函数，在 **return messages 之前**插入守卫逻辑。

### 为何此层唯一正确
| 候选层 | 判定 | 理由 |
|---|---|---|
| trim 层 (responses_compress.go) | ❌ 不合适 | 纯 function_call 续轮在 Responses API 里合法（已有契约钉测保护） |
| ParseResponses | ❌ 不合适 | IR 是协议中立表示，零 user 在 responses→responses 路径合法 |
| **serializeOpenAIMessages** | ✅ 唯一正确 | **只作用于 chat 目标**；IR→chat 转换已完成；不影响 responses 原生上游 |
| convertResponsesToChatBody | ❌ 已退役 | 应急回退路径，主路径不走此函数 |

### 守卫语义（复用 r1048）
- **复用函数**：`domains/transformation/ctx_compress.go:277 ensureUserSurvived` 的判定逻辑（扫描 `messages` 数组，role=="user" 即算有 user；零 user 时回插最新一条 dropped user）。
- **responses 形态适配**：IR `Message.Role` 直接可用（tool role 在 serializeOpenAIMessage 内已转换完成）；function_call_output 在 IR 里已是 tool role，不会误判为 user。
- **回插位置**：首条非 system 消息之前（与 r1048 同口径）。
- **日志留痕**：可选，建议 DEBUG 级（"responses→chat: restored user message to prevent zero-user body"）。

### 实施步骤
1. 在 `internal/ir/` 包新增 `ensureUserMessageSurvived(messages []map[string]any) []map[string]any` 辅助函数（复用 ctx_compress.go 的逻辑，但参数改为已序列化的 `[]map[string]any`）。
2. `serializeOpenAIMessages` 在 `return messages` 前一行插入：`messages = ensureUserMessageSurvived(messages)`。
3. 钉测放 `internal/ir/serialize_openai_user_survived_test.go`（与其他 serialize 测试同包）。

## 四、钉测设计

### 测试用例
```
TestSerializeOpenAI_ResponsesToChat_UserSurvived
- 输入：InternalRequest，System + 12条 assistant + 末项 assistant（模拟 trim 删光所有 user 后的形态）
- 断言：SerializeOpenAI 产出的 messages 数组至少包含 1 条 role=user
- 变异臂：摘掉 ensureUserMessageSurvived 接线 ⇒ 红（0 user）
```

### 对照测试
```
TestSerializeOpenAI_HasUser_Untouched
- 输入：已含 user 的 InternalRequest
- 断言：user 数量不变（守卫不增加多余 user）
```

### 边界测试
```
TestSerializeOpenAI_ToolOnlyMessages_GetsUser
- 输入：纯 tool role 消息（模拟 function_call_output 续轮）
- 断言：添加 user（tool role 不算 user）
```

## 五、真实可达性验证（responses trim）

**结论：真实可达**。`responses_compress.go` 的可移除判定：
- `responsesCompressionUnits` (186行) 构造可移除单元：message item（type="message"）单独成 unit；function_call 与其 output 成对作为一个 unit。
- `isResponsesProtectedItem` (290行) 只保护 system/developer role，**不保护 user**。
- trim 循环 (110-132行)：只保护末项 (index==len(items)-1) 与 protected item；**所有非末项 user message 都是可移除的**。
- 场景：`[user1, user2, ..., user_N, assistant_latest]` ⇒ trim 可删光 user1~user_N ⇒ `[assistant_latest]` = 零 user。

## 七、架构验证结论（2026-11-14 深度验证）

### 验证过程
1. **定位 responses→chat 转换的真实路径**：`domains/streaming/executors/executor_chat.go:3156` ParseResponses → 3183 SerializeOpenAI
2. **确认 trim 所在分支**：executor_chat.go:1019 `CompressResponsesInputIfNeededWithMeta` 只在 `nativePair=true` 分支执行
3. **验证分支隔离**：responses→chat 转换走 `else` 分支（1031 行 `finalizeOpenAIUpstreamBodyNative`），**拿的是未 trim 的 `sourceBody`**
4. **检查 sessioncontext 保护策略**：`internal/sessioncontext/protected.go:76 protectInfo` 保护首条 human user（85-93 行）+ 最新 human user（89-92 行）

### 真实事实
| 组件 | 行为 | 对零 user 的影响 |
|---|---|---|
| **responses→chat 转换**（executor_chat.go:3156-3183） | 直接从 `sourceBody` ParseResponses，**不经过 trim** | ❌ 不会产出零 user |
| **responses trim**（executor_chat.go:1019） | 只在 `nativePair=true` 分支执行，作用于 responses 原生上游 | ✅ 可能产出零 user，但那是**原生上游自己的约束**，不影响 chat 转换 |
| **sessioncontext Reduce**（protected.go:76-123） | 保护首条/最新 human user，工具链闭包传播 | ❌ 不会产出零 user |

### 结论
**P2-3 是架构误判**。三个可能产出零 user 的层（responses trim / sessioncontext Reduce / 转换层）都不会在 responses→chat 路径上造成零 user：
- responses trim 与 responses→chat 转换**分属两个互斥分支**（nativePair 判定）
- sessioncontext Reduce 的保护策略确保至少保留一条 human user
- 转换层（serializeOpenAIMessages）忠实序列化 IR Messages，不会凭空删除 user

**撤回修复**。本档保留为架构验证记录，供后续审计参考。

## 八、遗留澄清（避免误报复现）

首版在 `responses_compress.go` trim 层加守卫被既有钉测 `TestCompressResponsesInputIfNeededKeepsFunctionCallPairAtomic` 抓红——那个钉测是对的：「删旧 user 保末项 function pair 原子」是 trim 层的合法契约（纯 function_call continuation 在 Responses API 里合法）。但该契约不适用于 responses→chat 转换，因为**两者根本不在同一路径上**。

如果未来有需求「responses 原生上游也要强制 ≥1 user」，正确修复层是 **nativePair 分支的 trim 之后、出站之前**，而非转换层。
