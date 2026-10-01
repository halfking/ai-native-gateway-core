# 173 号 · R89-CI —— `KeyInfo.IsInternal` 零写入点 ⇒ 「内部 key 永不限流」这条豁免是**死代码**

> **日期**：2026-10-01
> **轮次**：R89-CI（第 73 轮，审计第 173 号）
> **类型**：**收掉 172 号的 F3** + 一条教科书级的 §41 桶①
> **改动生产代码**：无　**改动数据库**：无
> **上一轮**：172 号（F4 闭环：`NULL ≠ 0` 陷阱，自检 key 静默吃到 system tier 的 300 RPM）

---

## 〇、起手

172 号 §三 留下：

> `rate_limit.go:66` 存在 `keyInfo.IsInternal ⇒ Skipped` 豁免，**但 727/736 仍被限流 32.6 万次**
> ⇒ **行为上确定没被豁免**；**本轮未定位到 `IsInternal` 的赋值点 ⇒ 不能断言「豁免机制坏了」**。

**本轮定位到了 —— 而且答案比「坏了」更明确：那个字段从来没有人写。**

---

## 一、F1：`KeyInfo.IsInternal` 零写入点

**两种检索形态，各自独立确认**（playbook：「判定不存在前至少两种检索方式」）：

**形态一（赋值形态）**：`is_internal|IsInternal:|\.IsInternal =`

```
domains/authentication/verifier.go:95:	IsInternal           bool     `json:"is_internal"`
```

**全仓 1 处命中 —— 正是结构体字段声明本身。**

**形态二（全出现点计数）**：`IsInternal`，46 处 / 23 文件。剔除
测试文件、`vendor/`、以及三个**同名不同符号**之后，
与本条相关的**生产代码**只剩 **2 处**：

| 位置 | 性质 |
|---|---|
| `domains/authentication/verifier.go:95` | **声明**：`IsInternal bool` + json tag `is_internal` |
| `domains/streaming/rate_limit.go:66` | **读取**：`... \|\| keyInfo.IsInternal {` |

**同名的另外三个符号不是它**（避免误判，逐一排除）：
- `telemetry.IsInternalAutoEntry(entry)` —— 内部回环判据（`internal_loopback.go`）
- `georesolve.IsInternal(ipStr)` —— 判断私网 IP（`pkg/georesolve/geo.go:176-185`）
- `domain/tenant.go` / `autoroute/shadow_actors.go` —— 各自包内的独立符号

**⇒ 零个写入点。**
**⇒ Go 的零值规则 ⇒ `KeyInfo.IsInternal` 在生产恒为 `false`。**
**⇒ 没有任何字段把它从 `api_keys` 读进来**（`api_keys` 里那两列是 `is_system` 与 `key_tier`，**不是 `is_internal`**）。

---

## 二、🔴 F2：§41 桶① 教科书案例 —— 承诺已接线，实际不可达

**注释明确承诺**（`ratelimit/redis_sliding.go:338-339`）：

```go
// CheckRPM returns true if the request is within the RPM limit.
// **Internal callers (IsInternal=true) are always allowed through.**
func (l *RedisLimiter) CheckRPM(keyID int, limit int) bool {
```

**豁免逻辑写得完整**（`domains/streaming/rate_limit.go:66`）：

```go
if keyInfo == nil || rl == nil || keyInfo.IsInternal {
	return rateLimitOutcome{Skipped: true}
}
```

**但 `IsInternal` 永远为 `false` ⇒ 这条分支在生产不可达。**

⇒ **§41 桶①「注释承诺已接线 = 缺陷」完全成立**，且是本仓最干净的一个样本：
**逻辑完整、注释明确、就是没有任何生产路径能把那个字段置为 true。**

---

## 三、⚠️ F3：172 号的修法方案 2 需要修正

172 号我给的方案 2 是「**让 `IsInternal` 真正映射自 `is_system`**」。

**本轮发现这个描述不准确** —— 根因不是「映射写错了」或「映射漏了一个来源」，而是：

> **根本没有任何映射代码。`IsInternal` 是一条从头到尾没有生产写入方的字段。**

⇒ **方案 2 应当重写为**：
1. **补写入方**（从 `api_keys.is_system` 读进 `KeyInfo.IsInternal`），**并且**
2. **同步确认** `KeyInfo` 的其它构造路径（内存 key store / keystore 快照往返）
   都会带上这个字段 —— `verifier.go:104-107` 的注释提到
   「Serialized for the local snapshot round-trip (HMAC hash + metadata only)」，
   **而 `IsInternal` 带 `json:"is_internal"` 标签（无 `omitempty`）⇒ 会进快照往返**，
   **所以补写入方时要一并确认快照的反序列化路径**。

**⚠️ 若只改「读入」而漏了快照路径，会出现「重启前后行为不一致」的新缺陷** ⇒ 这正是 playbook **§55**「切换类配置必须有不变式检查」的又一例。

---

## 四、F4：影响面比自检 key 大得多

**不只 727/736。** 172 号 D3 已量化：

| `is_system` | key 数 | `rate_limit_rpm` = NULL | `= 0` |
|---|---|---|---|
| **t** | **89** | **88** | **1** |
| f | 93 | 57 | 5 |

⇒ **89 个系统 key 全部拿不到内部豁免** ⇒ 全部落进 `tierDefaults['system'] = 300 RPM`。
⇒ **其中 88 个连一个显式的「无限制」都没配。**

**⇒ 影响面表述（按 §62 纪律逐维给）**：

| 维度 | 答案 |
|---|---|
| 会不会失败 | **会** —— 任何超过 300 RPM 的系统 key 流量都会被自家闸门挡回 |
| 会不会波及 | **会** —— 89 个 key 共享同一个失效的豁免路径 |
| 当事方知不知道 | 不知道 —— 注释说「永远放行」，运维会以为系统 key 不受限流约束 |
| 有没有留痕迹 | **有** —— `X-LLM-Gateway-RateLimit-Scope: shared_key` 响应头 + `gw_rpm_exceeded` 分类 |

⇒ **这是 171/172 号那条 32.6 万次事件的根因，也是它的放大器。**

---

## 五、定级与修法

**维持 P2**（同 171/172 号：机制 ✅ / 规模 ✅ / **实际后果 ❌ 仍缺「哪些系统 key 真的被误伤」的清单**）。

| 方案 | 做法 | 代价 | 风险 |
|---|---|---|---|
| **1（推荐，仍是 172 号那条）** | 给自检 worker key 设 `rate_limit_rpm = 0` | 一条 UPDATE、零代码 | 只修自检一把 key，**89 个系统 key 的问题仍在** |
| **2（本轮修正版）** | **补 `IsInternal` 的生产写入方**（`api_keys.is_system` → `KeyInfo.IsInternal`），并**同步确认快照往返** | 改认证核心路径 | **会让 89 个系统 key 立即从「300 RPM」变成「不限流」** ⇒ 属运行时行为变更，**须确认上游能扛住**（自检只占 1.4% token，风险可控，但**其它系统 key 的流量本轮未统计**） |

**⇒ 建议：方案 1 先止血（可立即执行、影响面 = 一把 key），方案 2 排期但必须先统计「89 个系统 key 各有多少流量」。**

---

## 六、playbook §66 新增

> **§66 读到一个「豁免分支 / 守卫分支」时，必须确认触发它的那个字段有生产写入方 —— 守卫存在 ≠ 守卫可达**

**由来**（R89-CI / 173 号）：`rate_limit.go:66` 有一段写得完整、注释明确的
`keyInfo.IsInternal ⇒ Skipped` 豁免，`redis_sliding.go:338-339` 还专门写了
「Internal callers (IsInternal=true) are always allowed through」——
**看上去这个守卫是好的**。实际是**死代码**：该字段全仓零写入点。

**⇒ 规则一：看到 `if … <field> { return / skip / allow }` 这类守卫，不要只读守卫本身，要读 `<field>` 的写入方。**
一个守卫的有效性 = **守卫分支存在** × **它的输入在生产里真的会被置位**。
后者经常被忽略，因为**读守卫时眼睛跟着控制流走，不会回头找赋值**。

**⇒ 规则二：判「这个字段有没有生产写入方」要用两种检索形态：**
- 形态一（赋值形态）：`<field>:|\.<field> =|new\(|<field>\s*=`（含复合字面量字段、显式赋值、构造传参）
- 形态二（全出现点计数）：`<field>`，然后**剔除**测试文件 / vendor / **同名不同符号**

本轮正是靠形态一「全仓 1 命中 = 声明本身」直接定性的；形态二用于排除
`telemetry.IsInternalAutoEntry` / `georesolve.IsInternal` 这类**同名干扰项**。
**⚠️ 同名不同符号是这一条最大的误判来源** —— 不剔除就会得出「有很多处赋值」的错误结论。

**⇒ 规则三（与 §41 桶① 配套）：§41 让你「别把注释当承诺」，§66 让你「也别把代码当接线」。**
一个分支写在那里、逻辑完整、测试覆盖了它能走通的那半边
（本轮 `verifier_test.go` 确实覆盖了 `EffectiveRPM` 的各种取值），
**完全可能仍然是不可达的**。
**⇒ 判断「接线了没有」的标准是：有生产写入方（§66 规则二），而不是「代码里看得到」。**

**同族**：§41（注释三桶）/ §55（切换类配置要有不变式检查）/
§65（多态取值要有文档）/ §16（0 行不等于缺陷）/ §12（死代码判定不可信 —— 本条是它的可执行版）。
