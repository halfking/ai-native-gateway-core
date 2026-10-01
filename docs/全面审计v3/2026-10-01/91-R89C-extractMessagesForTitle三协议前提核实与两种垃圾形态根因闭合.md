# 91-R89-c：`extractMessagesForTitle` 三协议前提核实 + 两种垃圾形态的根因闭合（关闭 90 号 §4）

- 轮次：R89-c
- HEAD 基线：`1a7817ba0`
- 触发：90 号 §4 自列的未验证项——「`extractMessagesForTitle` 是否在 chat/messages/responses 三协议下都能取到 user message，这是方案甲能否成立的前提」
- 结论：**前提部分成立**（chat/messages 可用，**responses 不可用**）；同时**闭合了 44.42% 里两种不同垃圾形态的根因**，它们来自**两个不同分支、两种不同失效**
- 改动：**零生产代码、零配置、零门**。仅本报告 + README

---

## 1. 前提核实：`extractMessagesForTitle` 的协议覆盖面

`admin/auto_title_generator.go:539-552`：

```go
var parsed struct {
    Messages []struct {
        Role    string `json:"role"`
        Content any    `json:"content"`
    } `json:"messages"`
}
if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 {
    return ""            // ← 解析不出 messages 就返回空
}
```

⇒ 它**只认顶层 `messages` 数组**。逐协议判读：

| 协议 | 请求体形状 | `messages` 键 | 结论 |
|---|---|---|---|
| OpenAI **chat/completions** | `{"messages":[…]}` | ✅ 有 | **可用** |
| Anthropic **/v1/messages** | `{"system":"…","messages":[…]}` | ✅ 有（system 是顶层字符串，不在 messages 里，**不影响 user 提取**） | **可用** |
| OpenAI **/v1/responses** | `{"input":[…]}` | ❌ **无 `messages`，用 `input`** | **不可用 ⇒ 返回 `""`** |

**⇒ 对 responses 车道，本函数恒返回空。**

**影响链**（与 90 号 §1 的 corpus 优先级串起来）：

```
responses 请求体
  → extractMessagesForTitle  == ""                  (键名不匹配)
  → corpus 回落 requestPreview（320 字节原始 body 开头）  ← 必然是 {"model":…,"input":[…]
  → extractUserPrompt 把这行 JSON 当"用户行"留下
  → 标题 = [IDE] + {"model":…,"input":…            ← 纯 JSON 进标题
```

⚠️ **本轮未核实**：`MaybeGenerateTitle` 的三个调用点（`domains/streaming/handler.go:1101 / 1623 / 6776`）**是否覆盖 responses 车道**。若不覆盖，本节对现网无影响、但仍是**未来接入 responses 时的坑**。**列为下一轮的验证项**（与 88 号补的 `safeWindowSource` 测试、90 号的触发归因并列为未做项）。

---

## 2. 【闭合 90 号 §1】两种垃圾形态来自**两个不同分支**

90 号指出「44.42% 的回退产物形态不同」，本轮把机制完全对上。关键在 `extractUserPrompt`（`:727-767`）的**行式启发式**：

```go
lines := strings.Split(preview, "\n")
for _, line := range lines {
    line = strings.TrimSpace(line); if line == "" { continue }
    isSystemLine := false
    for _, pattern := range skipPatterns {   // "[system]" "system:" "You are" "You're" …
        if strings.HasPrefix(line, pattern) { isSystemLine = true; break }
    }
    if !isSystemLine && len(line) > 10 { … append(line) }   // ⇒ userPrompt
}
```

它**假设 preview 是多行 `role: text` 语料**。两种输入形态于是走出两条不同的坏路：

### 2.1 形态甲：`system: You are ZCode, an interactive coding agent…`（115,615 行）

- 输入：多行 `role: text` 语料，**整段都在 320 字节预算内只装得下 `system:` 行**；
- `system: …` 行被 skipPatterns 正确跳过 ⇒ `userPrompt == ""`；
- 落到 `extractTitleFromPreview` 的 **3c 分支**（`title = preview`）⇒ **把 system 语料原样当标题**。

### 2.2 形态乙：`[ZCode] {"max_tokens":128000,"messages":[{"content":"You are`（23,867 行）

- 输入：**单行原始 JSON**（JSON 文本里没有 `\n`，整段就是**一行**）；
- 该行以 `{"max_tokens":…` 开头，**不以任何 skipPattern 开头** ⇒ **没有被识别为 system 行**；
- `len(line) > 10` 成立 ⇒ **整行被收进 `userPrompt`**；
- 同时 `detectIDESource` 对整段做 `strings.Contains(lower, "you are zcode")` ⇒ 在 JSON **内部**命中 ⇒ 返回 `[ZCode]`；
- 落 **3a 分支**（`idePrefix + " " + userPrompt`）⇒ **标题 = `[ZCode] ` + 一段原始 JSON**。

> **这条最值得记**：形态乙里 `userPrompt != ""`，**所以「换一个更好的源」并不能消除它**——它是**启发式本身对单行 JSON 失效**（fail-open），不是源选错了。

### 2.3 第三种形态：`system: [gateway-handoff-v1] Resume the prior session using…`（39,778 行）

同形态甲：handoff 标记以 `system:` 开头被正确跳过、无 user ⇒ 3c ⇒ 原样当标题。

---

## 3. 对待裁决第 37 条「方案甲」的结论更新

90 号修正后的方案甲是「**回退改用已构建的 `corpus`（full body，含真实 user message）**」。

本轮的新证据**让方案甲更成立**，理由是它同时消掉三种形态：

| 形态 | 方案甲（改用 corpus）能否消除 | 原因 |
|---|---|---|
| 甲 `system: You are ZCode…` | ✅ 能 | corpus 是多行 `role: text`，其中 `user:` 行会被正确提取 |
| 乙 `[ZCode] {json…` | ✅ 能 | **同上一条**——一旦不再把单行原始 JSON 喂给 `extractUserPrompt`，形态乙就不会产生 |
| 丙 `system: [gateway-handoff-v1]…` | ✅ 能 | 同形态甲 |

⇒ **一处改动（4 个回退点的实参 `requestPreview` → `corpus`）覆盖全部三种形态**，比 89 号原方案甲（只让标题「不更丑」）高一个量级。

**但仍建议附带一条防御**（成本极低、不改客户端观感）：`extractUserPrompt` 增加「本行以 `{` 或 `[` 开头 ⇒ 判为结构化内容、非用户文本」的判断，避免**任何将来**再把 JSON 片段当标题——**这条是「判据自身不腐化」的应用**（conventions §9）：只靠调用方传对参数，防线只有一处。

## 4. 明确未做

- **未**核实 `MaybeGenerateTitle` 的三个调用点是否覆盖 responses 车道（§1 末尾，已列为下一轮）；
- **未**改任何生产代码（三处修法改变客户端观感，属裁决范围）；
- **未**给 `extractMessagesForTitle` 补协议覆盖测试（`responses` 键名不匹配是**已证事实**，补测试只会在修复前变红——按「红门不能进主干」纪律，本轮只登记不钉桩）。

## 5. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/91-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |
| `docs/全面审计v3/00-审计覆盖台账.md` | 第 37 条补「三种形态 + 一处改动全覆盖」 |

**零生产代码、零配置、零门、零 CI 行为变化。**

## 6. 交叉引用

- 90 号：本文关闭其 §4 未验证项，并补上「两种形态」的机制
- 89 号：原始 P1 报告
- conventions **§9**（判据自身不腐化 + 变异验证）——§3 的「防御」建议即其应用
- 88 号 §4：又一次「我自己的断言/描述先错、由下一轮回灌订正」的实例
