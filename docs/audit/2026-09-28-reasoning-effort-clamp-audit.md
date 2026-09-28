# reasoning_effort 收窄链路审计（2026-09-28）

> 范围：`internal/reasonnorm` / `internal/paramguard` / `internal/reasoncap` / `internal/paramreg`
> 起因：`llmgateway.internal.example.com` auto 路由「长时间只有思考/补全、没有正常响应」报障的批判式复审。
> 结论提要：**本轮修的是潜伏缺陷，不是报障的直接原因**。报障根因见 §5。

## 1. 报障现象（本机 8782 实例实测）

用户提示词：*「请检查48小时内1小时前所有没有合并的不活跃的子分支…」*（agentic 任务）

| 请求形态 | 选中模型 | 耗时 | 结果 |
|---|---|---|---|
| 无 `tools[]`（流式） | `glm-5.2` | 85s（首字 12.4s） | 思考 6850 字符 vs 正文 3478 |
| 无 `tools[]`（非流式） | `glm-5.2` | 27.3s / 34.9s | 把工具调用**当纯文本臆造**，无 `tool_calls` 字段 |
| **带 `tools[]`** | `deepseek-v4-flash` | **8.8s** | **2 个结构化 `tool_calls`** |

## 2. 缺陷一：`ClampEffort` 把「关闭思考」静默反转为开启（潜伏）

`internal/reasonnorm/norm.go` 的 `ClampEffort` 对无法识别的取值走
`effortIndex` 兜底档（index 3 = `medium`），再做「就近映射」：

```
ClampEffort("disabled",  glm-5 档位 {max,xhigh,high,medium,low,minimal,none}) = "medium"
ClampEffort("off",       deepseek-v4 档位 {low,high,max})                      = "high"   ← 并列取高
ClampEffort("bogus",     glm-5 档位)                                            = "medium"
```

`disabled` / `off` 是**关闭推理**的跨供应商通用写法，不是"选一个推理档位"。
修复前它们被解析成 **medium（glm）/ high（deepseek）**——即
**要求关闭思考，却拿到中档 / 高档思考**。

更糟的是它**看起来是正常的**：paramguard 会把这次改写记成一次
`Action: "clamp"` 的合法收窄，paramledger 报告显示
`original=disabled sent=medium`，读起来像"已按模型能力收窄"，
而不是"方向被翻转"。

### 修复

新增 `disableIntentEfforts` 词表（`disabled/disable/off/false/none/no/0`），
在就近映射**之前**拦截，一律解析为该模型支持的**最便宜档**
（新增 `cheapestEffort`，按规范档位序取最低，不按切片顺序）。

未知值（`bogus_value` 等）维持既有 medium 兜底，**不改变**——
避免波及 grok/mistral 等既有路径。

## 3. 缺陷二：方言门漏掉 GLM / DeepSeek / Ark（缺陷一的成因）

`internal/paramguard/guard.go` 的 `reasonDialectMatchesParamDialect`
只覆盖 OpenAI / Grok / Mistral / KimiEffort，其余走
`default: return false` —— 于是 `fixReasoningEffort` 对
**GLM / DeepSeek / Ark 三个家族整体空转**。

这三族都在 OpenAI 线上暴露 `reasoning_effort` 字段，且 `reasoncap`
能力表里 `Efforts` 非空（已实测 `Resolve("glm-5.2")` →
`{max,xhigh,high,medium,low,minimal,none}`），完全具备收窄条件。

这违反了 `internal/paramreg/registry.go:206` 自己写下的契约：

> `Note: "枚举各厂商不一致：DeepSeek low|high|max / GLM 7 档 / Grok …。须按目标能力收窄"`

后果：客户端发来的 `reasoning_effort` **原样出站，既不收窄也不报错**。

### 修复

`reasonDialectMatchesParamDialect` 增加 GLM / DeepSeek / Ark 三个 case。
无线上 effort 字段的家族（Anthropic / Gemini / Qwen / MiniMax / Ollama）
**继续不列入**——它们的能力表 `Efforts` 本就为空，`fixReasoningEffort`
开头即早退，不受影响。

### 两处必须一起改

**这是本轮最关键的一点**：若只放宽方言门而不修 `ClampEffort`，
`disabled` 会从"原样透传给上游"变成"被改写成 medium/high"——
**从静默失效变成静默反转，更糟**。两处互为前提，必须同批。

## 4. 实测与验证

### 4.1 变异检验（证明测试能抓住 bug，非"声明式通过"）

| 变异 | 结果 |
|---|---|
| 摘掉 `disableIntentEfforts` 分支 | `reasonnorm` 5 条 + 2 组 FAIL |
| 摘掉 GLM/DeepSeek/Ark 三个 case | `paramguard` 3 条 + 4 组 FAIL |

### 4.2 回归

```
go test ./internal/reasonnorm/... ./internal/paramguard/... \
        ./internal/reasoncap/... ./internal/paramreg/...
→ 全部 ok
```

## 5. 诚实边界：本轮**没有**修掉报障本身

必须写明，避免读者误以为报障已闭环：

- **缺陷二的 live 影响目前为零**。因为方言门此前挡着，clamp 从未对 GLM
  执行过；修好方言门后 `disabled` 才第一次真正被收窄成 `none`。
  这是**正确性修复 / 拆雷**，不是性能修复。
- **报障根因是另一条链**：客户端未声明 `tools[]` → auto 选了非工具型
  思考模型 `glm-5.2` → 模型把工具调用当纯文本臆造，响应无 `tool_calls`
  字段 → 客户端无从执行，用户看到"一直在思考"。
  **修复在客户端侧（补 `tools[]`）**，网关的选模逻辑本身是对的
  （补上 `tools[]` 后 auto 换模型、8.8s 返回规范 tool_calls）。
- **另一条 live 缺陷（未修，另立）**：`glm-5.2` 思考预算默认开启且开销极大。
  `1+1等于几？` 逐档实测思考占 95~100% 输出 token；`max_tokens=128` 时
  **`content_len=0`（完全空回复）**，`finish_reason=length` 无任何兜底。
  正规开关是 `thinking:{"type":"disabled"}`（实测 reasoning_tokens=0、0.9s），
  而**不是** `reasoning_effort`。

## 6. 两次被实测推翻的错误归因（留档）

1. **`reasoning_effort:"disabled"` 导致 503** → 复跑 3 次全 200。真实原因是
   上游常态过载。**单次 503 不足以支撑归因。**
2. **`reasoning_effort:"disabled"` 导致 120s 挂起** → 间隔 6s 复跑 n=3
   全 200（5s/19s/4s），无挂起。同上，为上游抖动。
3. **`max_tokens=512` 带 `reasoning_effort` 大面积失败** → 连发 48 次触发限流，
   0.0s 快速失败是限流而非参数效应。**压测必须控制请求速率。**

判「参数有效/无效」需满足：多次采样 + 控制间隔 + 有明确对照组。
单样本落在噪声带里得出的"无效应"结论同样不可信（首轮即犯过）。
