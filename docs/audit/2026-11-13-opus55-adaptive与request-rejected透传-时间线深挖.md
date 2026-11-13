# 2026-11-13 claude-opus-5-5「之前可以→现在不行」时间线深挖 + request_rejected 错误透传（r1050）

> owner 追问原文：「这类的错误应该返回给调用方，不要造成误解。但之前都一直可以，为何现在不行？需要深挖问题。」
> 本档回答第二问（时间线取证），第一问（错误透传）的实施记录在 §三。

## 一、时间线取证（211 生产库 request_logs_with_current_month，只读）

判据：`client_model='claude-opus-5-5' AND request_preview LIKE '%enable_thinking%'`——
ZCode 客户端的**固定请求形态**（`{"enable_thinking":true,"max_completion_tokens":128000,...}`），
跨 10-29 → 11-13 逐日分布：

| 日期 | 结果 | error_kind | 笔数 |
|---|---|---|---|
| 2026-10-29 | **成功** | —（1 笔 stream_error 无关族） | **64** |
| 2026-10-31 | 失败 | prompt_too_large（无关族） | 1 |
| 2026-11-09 | 失败 | transient（载荷限制轮已知族，~150KB+ 高熵） | 4 |
| 2026-11-10 ~ 11-11 | （该形态无流量） | — | 0 |
| **2026-11-12** | **失败** | **request_rejected（首起 11:15:45）** | 4 |
| 2026-11-13 | 失败 | request_rejected（含复现 1 笔） | 3 |

关键交叉证据（进一步收窄翻转窗口）：

1. **2026-11-11 的 paramguard 阴性对照实测**（claude-fable-5-2 事故轮，
   `internal/paramguard/adaptive_thinking_models_test.go` 原注释）：当时对
   `claude-opus-5-5` 的 enabled/disabled 五种 thinking 形态**直连实测全 200**，
   并据此把 opus-5-5 钉进「不得改写」的阴性对照组。
2. **2026-11-12 11:15:45** 同一客户端、同一形态、同一中继（apiclaude）开始
   400「requires adaptive thinking; omit thinking or use thinking.type=adaptive
   and output_config.effort」，此后**每一笔同形态请求都失败**（非间歇）。

### 结论

**翻转窗口 = 2026-11-11 下午 ~ 2026-11-12 11:15 之间，apiclaude 中继把
claude-opus-5-5 升级为 adaptive-only 模型**（与 11-11 当天首发的
claude-fable-5-2 同款语义、同款错误文案）。11-11 fable-5-2 首发即 adaptive-only，
opus-5-5 是**被回溯推广**的存量模型——这正是「之前一直可以（10-29 六十四连
胜、11-11 实测五形态全 200）、现在不行（11-12 起全败）」的完整解释：

- 网关无回归：11-13 出站序列化与 10-29 逐字节同形（thinking 原样透传）；
- 客户端无变化：ZCode 固定形态 enable_thinking:true 从未变过；
- 变的是**上游**：错误文案点名 `output_config.effort`——这是 Anthropic 新
  API 表面（非 classic Messages API 字段），说明 apiclaude 背后的协议层/
  模型版本在该窗口升级，且升级后对 opus-5-5 只接受 `thinking.type=adaptive`
  或省略 thinking，不再接受显式 `enabled(+budget_tokens)`。

### 与 r1049 修复的关系

r1049（fa15a4d36）的 `adaptiveOnlyModels` 补名是**首发防线**：出站前把
opus-5-5 的 enabled/disabled 改写成 adaptive（上游实测 adaptive=200）。
本档 §三 是**错误面防线**：上游语义再变（新模型/新形态）时，客户端至少
能看到上游的真实拒绝原因，而不是「没有可用节点」。

## 二、错误包装链定位（为何造成误解）

修前的客户端可见错误（211 活体，2.5.9-6a9335855）：

```json
{"error":{"code":"model_not_found","type":"server_error",
  "message":"No available provider for model 'claude-opus-5-5'. All 3 candidates failed.",
  "kind":"request_rejected","gateway_debug":{...,"retryable":false}}}
```

三重误导（HTTP 503 + 上述 body）：
1. `code=model_not_found`——模型存在且已登记，误导最深；
2. `type=server_error` + 503——上游明明是 400 invalid_request_error（客户端侧问题），
   SDK 按 5xx 退避重试一个**永远会被拒**的请求；
3. `message="No available provider..."`——把「参数被上游拒」说成「没有可用
   节点」，即 owner 报障时看到的字面意思。

根因：三协议面的 all-candidates-failed 渲染分支族（context_length →
unsupported_feature → client_bug → model_deprecated → rate_limit → **通用兜底**）
里没有 `KindRequestRejected` 分支——它落进通用兜底（503 + model_not_found）。
归因层（error_kind=request_rejected 落库）2026-11-02 已修，缺的是**客户端
响应面**——与 2026-11-11 fable-5-2 形态门缺口同构（kind 是对的，协议面没接）。

## 三、实施（r1050）

三面对称（`domains/streaming/`）：

- **chat 面 handler.go**：KindClientBug 分支后新增 KindRequestRejected 分支——
  **400 + invalid_request_error + request_rejected** + message 透传上游
  reason（extractUpstreamReason：200 字符截断 + SanitizeErrorText 脱敏，
  model_deprecated/client_bug 同款）+ X-Gateway-Last-Kind 头 + prewarmed
  流形态。落库 failAndMark("request_rejected")。
- **anthropic 面 messages.go / responses 面 responses.go**：errCode 判定
  **提前到统一 record 之前**（Exhausted 分支里 request_rejected →
  errCode=request_rejected + errMsg 含 reason，保证**单行落库**且分类正确，
  不走 ContentFilter 先例的双行形态）；渲染分支回 400 + invalid_request_error
  + 透传 message。
- **chat 面入口统一**（生产等价重构）：serveWithExecutor 的
  `h.executor.Execute(...)` 改走 `h.executeForRequest(...)`——override 恒 nil
  时同一路径；同时让三面错误渲染钉测共享同一枚 test hook（此前 chat 面在
  stub 环境直连真实 Router 必 panic，无法端到端驱动错误分支）。

钉测（`request_rejected_transparency_test.go`）：三面端到端各 1 例
（400 + type/code + message 含 "requires adaptive thinking" + 不含
"No available provider"/"All providers unavailable" + 落库 request_rejected）
+ 阴性对照（transient 维持 503 老路）。变异 ×3（chat 分支禁用 / messages
errCode 还原 model_not_found / responses 渲染分支禁用）全红→还原绿。

## 四、遗留

- apiclaude 若继续把 adaptive-only 推广到其它存量模型（sonnet-5 当日实测
  仍兼容 enabled），症状会以本档修复后的**诚实 400**出现（客户端可见
  "requires adaptive thinking"），补 `adaptiveOnlyModels` 一行即恢复首发改写。
- `model_not_found` 通用兜底的 legacy code 保留（旧客户端 pattern-match 兼容，
  2026-06-20 注释口径），本档不动。
