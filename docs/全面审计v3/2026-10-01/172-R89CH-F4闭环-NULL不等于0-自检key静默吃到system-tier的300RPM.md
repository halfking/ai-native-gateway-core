# 172 号 · R89-CH —— F4 闭环：`rate_limit_rpm` 的 **NULL ≠ 0** 陷阱，自检 key 静默吃到了 system tier 的 300 RPM

> **日期**：2026-10-01
> **轮次**：R89-CH（第 72 轮，审计第 172 号）
> **类型**：**收掉 171 号的未解项** + 定量可修的缺陷
> **改动生产代码**：无　**改动数据库**：无（全部只读 SELECT）
> **上一轮**：171 号（327,000 次限流的真身：凭据自检的 worker key 把自己限流了）

---

## 〇、起手

171 号 §四 留下的未解项：

> **727 与 736 的 `rate_limit_rpm` 都是 NULL**，可它们触发了 40 万次 `rate_limit_exceeded`。
> ⇒ **限流阈值不是从 `api_keys.rate_limit_rpm` 来的。本轮没有查清它从哪来（不猜）。**

**本轮把它查清了 —— 答案是：从「tier 默认值」来的，而这是一个真实的语义陷阱。**

---

## 一、F1：限流阈值的三级回落链

`domains/streaming/rate_limit.go:3223` → `checkGatewayRateLimit`（`rate_limit.go:58`）：

```go
limit := keyInfo.EffectiveRPM()
if limit <= 0 {
	// Explicit unlimited (DB=0). No headers, no check.
	return rateLimitOutcome{Limit: 0, Remaining: -1, ResetSec: 0}
}
```

`domains/authentication/verifier.go:120-137`：

```go
func (ki *KeyInfo) EffectiveRPM() int {
	if ki.RateLimitRPM != nil {
		if *ki.RateLimitRPM == 0 { return 0 }        // 0 = 显式无限制
		if *ki.RateLimitRPM > 0 { return *ki.RateLimitRPM }
	}
	tier := ki.KeyTier
	if tier == "" { tier = "default" }
	if d, ok := tierDefaults[tier]; ok { return d[0] }
	return tierDefaults["default"][0]                 // ← NULL 落这里
}
```

`verifier.go:74-79` 的 tier 表：

```go
var tierDefaults = map[string][2]int{
	"system":     {300, 50},
	"production": {60, 20},
	"default":    {12, 6},
	"applicant":  {6, 2},
}
```

**而 727 / 736 的真库配置是**：

| id | `key_prefix` | `rate_limit_rpm` | `is_system` | `key_tier` |
|---|---|---|---|---|
| **727** | `sk-selfche****` | **NULL** | **t** | **system** |
| **736** | `sk-selfche****` | **NULL** | **t** | **system** |

⇒ **`EffectiveRPM()` = `tierDefaults["system"][0]` = 300。**

**完整机制链（全部有据）**：

```
自检 worker key 727/736：is_system=t, key_tier='system', rate_limit_rpm=NULL
  └→ EffectiveRPM(): RateLimitRPM == nil ⇒ 跳过 0/正数两个分支
      └→ 回落 tierDefaults["system"][0] = 300 RPM
          └→ 凭据自检要逐个探全部凭据 ⇒ 探测速率远超 300 RPM
              └→ 撞闸门 326,126 次，每次直接 429，该轮探测无结果
```

**单元测试也把这个行为固化了**（`verifier_test.go:124-125`）：

```go
{"nil falls back to default tier", nil, "default", 12},
{"nil falls back to production tier", nil, "production", 60},
```

**⚠️ 但测试里没有 `system` tier 的用例，也没有「NULL 与 0 语义不同」的用例。**

---

## 二、🔴 F2：这是一个 **NULL ≠ 0** 的语义陷阱

`verifier.go:117-119` 的注释**只描述了 0**：

```go
// EffectiveRPM returns the applicable RPM limit (per-key or tier default).
// A per-key value of 0 means "unlimited" (CheckRPM treats limit<=0 as no cap).
// Negative values (should not exist in DB) fall through to the tier default.
```

**注释里没有一句提到 NULL。** 而 NULL 与 0 的行为**完全相反**：

| `rate_limit_rpm` | `EffectiveRPM()` | 语义 |
|---|---|---|
| **`0`** | `0` | **显式无限制**（不检查、不限流） |
| **`NULL`** | **tier 默认（system=300）** | **按 tier 限额限流** |
| 负数 | tier 默认 | （注释说「不应该存在」） |

⇒ **运维想给一把 key 放开限流，必须显式写 `0`；留空（NULL）不是「不限制」，而是「按 tier 默认限」。**

**⚠️ 强化证据（`api_keys` 全表）**：

| `is_system` | key 数 | `rate_limit_rpm` 为 NULL | `rate_limit_rpm = 0` |
|---|---|---|---|
| **t** | **89** | **88** | **1** |
| f | 93 | 57 | 5 |

⇒ **89 个系统 key 里 88 个的 RPM 是 NULL，也就是全部落在这个回落路径上；只有 1 个显式设了 0。**
⇒ **这不是一个孤例配置，是一个家族性的默认行为。**

---

## 三、⚠️ F3：还有一层豁免机制，但对自检 key 没生效

`rate_limit.go:66` 有一条更靠前的豁免：

```go
if keyInfo == nil || rl == nil || keyInfo.IsInternal {
	return rateLimitOutcome{Skipped: true}      // 内部 key 完全跳过限流
}
```

`ratelimit/redis_sliding.go:338-339` 也写着：

> `// Internal callers (IsInternal=true) are always allowed through.`

**⇒ 代码里存在一条「内部 key 不限流」的路径。**
**⚠️ 但 727/736 `is_system=t` 却仍被限流 32.6 万次 ⇒ 这条豁免对它们没有生效。**

**⚠️ 诚实登记**：本轮**没有定位到 `KeyInfo.IsInternal` 的赋值点**（检索命中的都是注释与调用侧，没有 `KeyInfo.IsInternal = ...` 的构造代码）。
**所以我不能断言「豁免机制坏了」** —— 可能是：
(a) `IsInternal` 并不映射自 `is_system`（是另一个来源）；或
(b) 映射了但自检走的是另一条不经过 `checkGatewayRateLimit` 的路径。
**⇒ 登记为未核，不猜。** 但**行为侧的事实是确定的：自检 key 没有被豁免。**

---

## 四、定级与修法（P2，不升 P1）

**机制成立** ✅（F1 三级回落链每一环都有代码 + 真库配置双重证据）
**规模已量化** ✅（326,126 次）
**是否已造成实际后果** ❌ **未证实** —— 仍需「自检在这些天漏检了哪些凭据」的证据

⇒ **维持 171 号的 P2，不升 P1**（§52：「已发生」需要的是漏检清单，不是我这边的推演）。

**修法两条（建议，⚠️ 本代理不擅自动手）**：

| 方案 | 做法 | 代价 | 风险 |
|---|---|---|---|
| **1（推荐）** | 给自检 worker key 设 **`rate_limit_rpm = 0`**（显式无限制） | **一条 UPDATE，零代码** | 需要确认自检流量不会真的打爆上游 —— 它是探活请求，本就极短（169 号实测探测只占 1.4% token） |
| **2** | 修 `EffectiveRPM()`：对 `is_system` 的 key 显式返回 0，或让 `IsInternal` 真正映射自 `is_system` | 改认证核心路径，影响 89 个 key | 改的是**所有系统 key 的限流语义** ⇒ 属于运行时行为变更，需按纪律登记 |

**⚠️ 无论选哪个，都建议顺手补上**：
- `verifier.go:117-119` 的注释**补一句 NULL 的行为**（这是本条缺陷的认知根源）；
- `verifier_test.go` **补两个用例**：`{"nil on system tier", nil, "system", 300}` 与一条**显式断言 NULL ≠ 0** 的用例。

**⇒ 这直接连到待裁决 52**（API key 预算闸门 fail-open 四条）：
**52 说的是「闸门不生效时默认放行（fail-open）」，而本条揭示的是「RPM 闸门在没配时默认按 tier 限（fail-closed 到默认值）」。**
**两者的默认值方向相反，而两者都没有被真实流量检验过（171 号 F3 已证 `budget_usd` 等三道闸门 0 触发系流量太小）。**
⇒ **建议 52 与本条合并裁决：所有 key 级闸门应当有统一的「未配置」语义定义，而不是各自回落到不同的默认值。**

---

## 五、playbook §65 新增

> **§65 读「0 = 无限制」这类注释时，必须同时确认 NULL 走哪条路 —— NULL 与 0 在这里语义完全相反**

**由来**：`verifier.go:118` 的注释只说「per-key value of **0** means unlimited」，
**没有一个字提到 NULL**。而 NULL 走的是「回落到 tier 默认（system=300）」——
**与 0 的语义恰好相反**。运维按注释理解「留空 = 不限」就会得到相反结果。

**⇒ 规则一：凡是有「0 / NULL / 空字符串 / -1」多态取值的配置列，
读注释时必须问一句「另外几个取值分别是什么」。**
本仓已确认的多态列（后续可按此表复核）：
- `api_keys.rate_limit_rpm` —— **0 = 无限制；NULL = tier 默认**（本条）
- `api_keys.rate_limit_tpm` / `rate_limit_concurrent` / `budget_usd` —— **同类嫌疑，尚未逐列核实**

**⇒ 规则二：查「某配置是否生效」时，先查 `information_schema` 的 nullable 与 DEFAULT，再读代码。**
本轮的关键一步是发现 `api_keys` 有 `is_system` 与 `key_tier` 两列 ——
**如果没先查列名，只看 `rate_limit_rpm` 是 NULL，会得出「没配限流所以不该限流」的错误结论。**

**⇒ 规则三：注释与测试用例都是「作者当时的理解」，不是「契约」。**
本轮 `verifier_test.go` 覆盖了 `default` 与 `production` 两个 tier 的 NULL 回落，
**唯独没覆盖 `system`** —— 而出问题的恰恰是 `system`。
**⇒ 「测试覆盖了 X」不等于「X 的所有取值都被覆盖」。**

**同族**：§64（「配了却不触发」要看实际流量）/ §63（「没有 X」的两种读法）/
§59（尺子错的两种形态）/ §61（默认值不能当判别器）/
**§55（切换类配置必须有不变式检查）**。
**§55 与本条是同一族的两端**：§55 说「配置切换要有守卫」，本条说「**配置本身的多态取值要有文档**」。
