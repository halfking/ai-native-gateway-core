# 92-R89-d：`MaybeGenerateTitle` 车道覆盖核实 + **用生产函数实证推翻 91 号两处机制描述**

- 轮次：R89-d
- HEAD 基线：`81f5e3bd6`
- 触发：91 号 §1 末尾与 §4 自列的未验证项——「`MaybeGenerateTitle` 的三个调用点
  （`handler.go:1101/1623/6776`）**是否覆盖 responses 车道**」
- 结论：**覆盖**（且只有一个真实调用点）；但**推翻 91 号两处机制描述**——
  一处是错的根因，一处是**与同一份报告 §3 的表格自相矛盾**
- 改动：**零生产代码、零配置、零门**。临时实证测试已删除（见 §6）

> ⚠️ **本报告订正 91 号**。按 conventions「更正必须回灌被更正的原文件」，
> 91 号 §2.1/§2.2 已加指向本报告的警示框，**原文不改写**。

---

## 1. 追调用点：`1101` 与 `1623` 都不是调用点

91 号（以及我此前的印象）把 `grep MaybeGenerateTitle` 的三个命中都当成调用点。逐个读包围函数后：

| 行号 | 实际是什么 | 证据 |
|---|---|---|
| `handler.go:1101` | **接口字段声明**（`autoTitleGenerator` 字段的 interface 方法签名） | 位于 `ChatHandler` struct 的字段区，其上 1090-1100 全是字段注释 |
| `handler.go:1623` | **setter 里的接口方法签名**（`SetAutoTitleGenerator`） | `func (h *ChatHandler) SetAutoTitleGenerator(atg interface{ MaybeGenerateTitle(...) })` |
| `handler.go:6776` | ✅ **唯一真实调用点** | 位于 `emitTelemetry`（起 5840）的成功路径 |

⇒ **真实调用点只有 1 个**，不是 3 个。这本身是对「grep 命中数 = 调用点数」的又一次修正。

## 2. 车道覆盖：三条车道都经过这一个调用点

`emitTelemetry` 的调用方恰好就是三条入口车道：

| 入口 | 调用点 | `requestMode` |
|---|---|---|
| `/v1/chat/completions` | `handler.go:5775` | `"chat"` |
| `/v1/responses` | `responses.go:1000`（`h.chatHandler.emitTelemetry`） | `"responses"` |
| `/v1/messages`（Anthropic） | `messages.go:1002`（`h.chatHandler.emitTelemetry`） | `"messages"` |

⇒ **responses 车道确实覆盖**，91 号「未来接入的坑」这一定性**不成立**。

### 2.1 但覆盖 ≠ 能用：传入的 body 形状取决于**上游协议**

`handler.go:6766-6776` 传给 `MaybeGenerateTitle` 的是 `reqLog.RequestBody`，
而 `reqLog.RequestBody = requestBodyText`（`handler.go:5908-5911, 6108`），
其源头是 `requestBody []byte` 形参 = `result.InboundBody`（`preferCapturedBody` 主源、
`logCtx.Body` 兜底）。追 `InboundBody` 的赋值（`executor_chat.go:1558/1658/1678/1775/1952`
统一写 `InboundBody: sourceBody`）：

- **非 native responses 分支**（`executor_chat.go:501`）：`sourceBody = params.BodyBytes`，
  而 `responses.go:722` 传的 `BodyBytes` 是 **已转换的 chat 体**（`chatBodyBytes`，
  `responses.go:386-387`）⇒ **有 `messages` 键 ⇒ `extractMessagesForTitle` 可用**。
- **native responses 分支**（`executor_chat.go:477`）：`sourceBody = params.ResponsesBodyBytes`，
  而 `responses.go:395` 传的是**原始 responses 体**（`bodyBytes`，用 `input` 键）
  ⇒ **无 `messages` 键 ⇒ `extractMessagesForTitle` 恒返回 `""`**（91 号 §1 判读正确）。

⇒ **精确结论**：responses 车道「**走 chat 协议上游时正常，走 native responses 上游时失效**」，
不是整个车道失效。

真库交叉验证（`request_logs_2026_09`，2,151,342 行）：

| `request_mode` | `egress_protocol` | 行数 | 标题提取 |
|---|---|---|---|
| chat | openai-completions | 182,394 | ✅ |
| chat | anthropic-messages | 22,907 | ✅（顶层 `system` 是字符串，不在 `messages` 里） |
| chat | openai-responses | 3,559 | ✅（`ResponsesBodyBytes` 由 **chat 体**填充，见 `executor_chat.go:380-393` 注释） |
| **responses** | **openai-responses** | **26** | ❌ **失效** |
| responses | openai-completions | 245 | ✅ |
| responses | anthropic-messages | 44 | ✅ |
| messages | openai-messages / anthropic-messages | 52 | ✅ |

⇒ **失效面真库实测 = 26 行**（2026-09 全月）。

## 3. 【自纠 91 号 §1】26 行的后果**不是**进 P1 那种污染

91 号 §1 写的影响链是「corpus 回落 preview → `extractUserPrompt` 把这行 JSON 当用户行留下
→ 标题 = `[IDE] {` + JSON」，**方向错了**。读 `generateTitleFromFirstRequest`
（`auto_title_generator.go:389-434`）：

```
extractMessagesForTitle(body) == ""   （native responses）
  → corpus = requestPreview（:401-404）   ← 320 字节，有内容
  → len(TrimSpace(corpus)) < 10 ?        （:426）  ← 否，320 字节远大于 10
  ⇒ 不落回退，而是**把这段截断 JSON 当语料喂给标题 LLM**
```

⇒ 后果是**标题质量降级**（LLM 读一段残缺 JSON 后生成标题），**不是**把 JSON 灌进
`session_titles.title` 字段。**这条不进搜索污染面，P1 定级不因它变化。**

用生产函数实证（`extractTitleFromPreview` 直接调用）：

```
C) extractTitleFromPreview(responses preview)
   = "{\"model\":\"gpt-5\",\"input\":[{\"role\":\"user\",\"content\":[{\"type\":…"
   extractMessagesForTitle(responses body) = ""   ← 恒空（与 91 号一致）
```

## 4. 【自纠 91 号 §2.1】形态甲的真因不是「320 字节只装得下 system 行」

91 号 §2.1 写「多行 `role: text` 语料，320 字节预算内只装得下 `system:` 行」——
**这个输入形态不存在**。真库 `request_preview` 原文（`request_logs_2026_09` 直接取样）：

```
length = 314
system: You are ZCode, an interactive coding agent You are ZCode Explore, a file search
and codebase research special… | user: [smm_v1:733fd64ad8c769c] …
```

**① 是单行**（`summarizeMessagesContainer` 用 `" | "` 拼接，`telemetry_summary.go:152`），
**② 用户段其实就在里面**，**③ 整行以 `system:` 开头**。

⇒ 真因是：`extractUserPrompt` 按 `\n` 切行后**只有一行**，而 `skipPatterns` 做的是
**整行前缀匹配**（`auto_title_generator.go:727-767`，`strings.HasPrefix(line, pattern)`）
⇒ 整行（含 `| user:` 段）被**一起**判为 system 行丢弃 ⇒ `userPrompt == ""`
⇒ 落 3c 分支「原样用 preview」。

> 这个区别不是措辞问题：91 号的说法暗示「用户内容根本不在 preview 里」，
> 真相是「**在，但被行级前缀匹配连坐丢弃**」。只有后者才能解释「为什么明明有 user 段
> 却还是回退」。

真库 preview 形态分布（797,647 条非空 preview）：

| 形态 | 行数 | 说明 |
|---|---|---|
| 以 `system:` 开头 | 52,445（chat 52,423 / responses 22） | ⇒ 形态甲/丙 |
| 以 `{` 开头 | 58,312（chat 57,998 / responses 283） | ⇒ 形态乙 |
| 含 `| user:` 分隔 | 52,694 | 与「以 `system:` 开头」高度重合 |

## 5. 【自纠 91 号 §2.2】「换一个更好的源并不能消除形态乙」是**错的**

91 号 §2.2 加粗断言：

> 形态乙里 `userPrompt != ""`，**所以「换一个更好的源」并不能消除它**——它是启发式本身对单行 JSON 失效。

**这句与同一份报告 §3 的表格直接矛盾**（§3 表格写形态乙「✅ 能，同上一条」）。
实测是**表格对、论断错**：因为 `corpus` = `extractMessagesForTitle(body)` 的产物
**本身就是多行 `role: text`**（`auto_title_generator.go:600` 用 `"\n"` join，
且 `:582-584` 只保留 user/assistant、**system 已被剔除**）
⇒ 喂给 `extractUserPrompt` 的第一行就是 `user: …`，行级前缀匹配不再连坐。

**实证（生产函数直接调用，真库 preview 原文入参）**：

```
preview 行数 = 1
extractUserPrompt(preview)     = ""                    ← 形态甲：整行被连坐丢弃
A) extractTitleFromPreview(preview)
   = "system: You are ZCode, an interactive coding agent You are ZCode Explore, a…"

corpus 行数 = 2
  user: 帮我查一下 T5 那个断连问题
  <latest user message (preserved)> user: 帮我查一下 T5 那个断连问题
extractUserPrompt(corpus)     = "帮我查一下 T5 那个断连问题"
B) extractTitleFromPreview(corpus) = "帮我查一下 T5 那个断连问题"    ← 真实用户文本
```

⇒ **方案甲（一处改动：4 个回退点实参 `requestPreview` → `corpus`）同时消除形态甲与形态丙**，
与 91 号 §3 的结论一致，但**依据是「corpus 是多行且已剔除 system」**，
**不是** 91 号 §2.2 说的「换源没用」。

### 5.1 形态乙的真因（91 号未给出）：body > 64KB ⇒ 摘要失败

`previewJSON`（`telemetry_summary.go:69-81`）先试 `summarizeJSON`，
它在 `len(body) > maxPreviewParseBytes`（**64 KiB**，`telemetry_summary.go:67`）时
**先把 body 截断到 64KB 再 Unmarshal** ⇒ 截断处 JSON 非法 ⇒ 解析失败 ⇒ 返回 `""`
⇒ 退回 `compactJSON(截断片段)`（同样非法）⇒ `normalizeWhitespace(原始片段)`
⇒ **单行原始 JSON**。

真库验证（形态乙判据：`request_preview LIKE '{%' AND lower(...) LIKE '%you are zcode%'`）：

- **47,305 行命中，其中 chat 入口 47,305（100%）、responses 入口 0 行**
  ⇒ 形态乙**不是** responses 车道造成的；
- 对这批 body 取 `octet_length(request_body::text)`：
  **`min_len = 66,652 > 65,536`，全部超 64KB，最大 6,953,713 字节（6.9 MB）**
  ⇒ **全量命中，无一例外**。

> 方向保守性说明：`request_body` 是 `jsonb`，`::text` 是**重新序列化**的结果，
> 会比原始字节**更短**（jsonb 去掉空白）。重新序列化后仍 >64KB ⇒ 原始字节必然更大。
> 判据方向是保守的（不会把小的判成大的）。
>
> ⚠️ 计数口径瑕疵（如实登记）：这条查询的 `LIMIT 200` **没有生效**（返回 40,000），
> 说明 Citus 下推后 LIMIT 被丢弃、实际做了全量扫描。**这不影响结论**——反而更强：
> `min_len` 取的是更大样本集的最小值且仍 >64KB。但**不能引用 `sampled=40000` 这个数**
> （与 `LIMIT 200` 自相矛盾，说明计划不可复现）。

## 6. 临时实证测试已删除

本轮为取「生产函数真实输出」新建了两个临时测试文件：

- `domains/streaming/zz_r89d_tmp_test.go` —— 调用**生产函数** `requestPreview` 产出 preview
  （**未复刻实现**，避免"复刻版与生产版不一致"这一新的可信度缺口）；
- `admin/zz_r89d_tmp_test.go` —— 把真库 preview 原文喂给
  `extractTitleFromPreview` / `extractUserPrompt` / `extractMessagesForTitle`。

两者跑完即删（`mavis-trash` 移入回收站），`go build ./...` 通过。
**它们是取证工具，不是门**——按「红门不进主干」纪律，若不删除会变成修复前必红的门。

## 7. 对待裁决第 37 条的影响

**方案甲成立且依据比 91 号所述更硬**：

| 变更点 | 依据 |
|---|---|
| `auto_title_generator.go:429/441/463/474` 四处实参 `requestPreview` → `corpus` | 实证 B 组：corpus 提取出真实 user 文本；`corpus` 只含 user/assistant 行，system 已在 `extractMessagesForTitle` 内剔除 |
| （建议）`extractUserPrompt` 加「行以 `{`/`[` 开头 ⇒ 非用户文本」防御 | 面向**未来**：防止任何新调用方再把 JSON 片段当标题。这是 91 号 §3 已提的同一条建议，本轮证据更支持 |
| （新，**独立于方案甲**）`extractMessagesForTitle` 支持 `input` 键 | 真库 26 行失效面。**这是第四处建议，与方案甲独立**：不改它，则 native-responses 上游的语料永远是截断 JSON（§3 已说明后果是质量降级而非 P1） |

**本轮仍未改任何生产代码**——四处修法均改变客户端观感，属裁决范围。

## 8. 明确未做 / 局限

- **未**给 `extractMessagesForTitle` 补 `input` 键支持（26 行失效面已量化，登记为建议）；
- **未**改生产代码；
- **未**验证 `summarizeMessagesContainer` 只取前 3 条消息（`telemetry_summary.go:148-150`）
  对标题质量的影响——预览只含前 3 条，**长会话的第 4+ 轮对话在 preview 里根本不存在**，
  这可能是一条独立的可观测性局限，未量化；
- **未**追 `anthropic` 入口（`request_mode='anthropic'`，2 行）的 body 形状；
- 真库数据被多 worktree 共享（87 号 / 待裁决 36），本轮所有计数应视为**下限/非独占样本**。
  但本轮的核心结论（A/B/C 三组实证）**不依赖真库计数**，依赖的是生产函数的直接调用。

## 9. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/92-...md` | 新建（本文件） |
| `docs/全面审计v3/2026-10-01/91-...md` | **加两处警示框**指向本报告（§2.1 / §2.2），**原文不改写** |
| `docs/全面审计v3/README.md` | 追加索引 |
| `docs/全面审计v3/00-审计覆盖台账.md` | 第 37 条补「失效面 26 行 + 方案甲依据修正」 |

**零生产代码、零配置、零门、零 CI 行为变化。**

## 10. 交叉引用

- 91 号：本报告关闭其 §1/§4 未验证项，并**订正其 §2.1/§2.2 的机制描述**
- 90 号 / 89 号：原始 P1 与「丢弃好源读弱源」根因修正
- conventions **§10.1**（量化结论自己数一遍）：本轮把「形态乙来自 responses」的猜测
  用真库**证伪**（100% 是 chat 入口），换成 64KB 阈值这个**可验证的判据**，
  并对全量样本复算了一次 `min_len`。
- conventions **§10.2**（先定位函数体再决定搜什么名字）：`grep MaybeGenerateTitle`
  的三个命中里两个不是调用点——**低命中/高命中都不等于「我以为的那个东西」**。
