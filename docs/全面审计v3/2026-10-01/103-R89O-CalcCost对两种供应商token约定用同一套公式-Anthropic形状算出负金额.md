# 103-R89-o：**自查发现的新 P1** —— `CalcCost` 对两种供应商 usage 约定用同一套公式，Anthropic 形状算出**负金额**

- 轮次：R89-o（主代理自查，非子代理产出）
- HEAD 基线：`bfc1f21db`
- 触发：复核 100 号 §2-F3「缓存 token 口径缺失」时，顺藤摸到的**更大问题**
- 结论：**P1**。同一笔物理用量，OpenAI 约定算出正确金额，Anthropic 约定算出**负数**；
  且**仓库里唯一的负值防护位于死代码**，活跃路径没有
- 取证：主代理回原代码 + **用生产函数 `CalcCost` 本体做量化实证**（临时测试跑完即删）
- 改动：**零生产代码、零配置、零门**

> **这条是三个子代理都没抓到的**，也是 R89 全部四轮里唯一一条由主代理独立
> 从「复核别人的发现」中顺势挖出的新缺陷。**复核别人的报告有二次收益。**

---

## 0. 一句话

`ExtractUsageFromChunk` 的变体表**同时支持两种互不兼容的供应商 token 约定**，
但 `CalcCost` 只有一套「先按 input 价减去缓存、再按缓存价加回」的公式——
**这个公式只在 OpenAI 约定下成立**。遇到 Anthropic 形状的 usage，
缓存 token 被从一个**本来就不含它**的基数里减掉，**金额算成负数**，且无任何钳制。

---

## 1. 事实链

### 1.1 变体表明确支持两种约定

`domains/streaming/usage.go:36-49`：

```go
usagePromptPaths     = []usagePath{{"prompt_tokens"}, {"input_tokens"}}
usageCompletionPaths = []usagePath{{"completion_tokens"}, {"output_tokens"}}
usageCacheReadPaths  = []usagePath{
    {"cache_read_input_tokens"},                                  // ← Anthropic：独立字段
    {"cache_write_tokens"},
    {"prompt_tokens_details", "cached_tokens"},                   // ← OpenAI：cached_tokens 内嵌于 prompt_tokens
    {"input_token_details", "cache_read"},
}
```

**两者的语义根本不同：**

| 约定 | `PromptTokens` 取自 | 与缓存 token 的关系 |
|---|---|---|
| **OpenAI** | `prompt_tokens` | **包含** `prompt_tokens_details.cached_tokens` |
| **Anthropic** | `input_tokens` | **不包含** `cache_read_input_tokens`（后者是**兄弟字段**） |

**变体表里专门放了 `input_tokens` 回退这一项，本身就说明作者预期 Anthropic 形状的 payload 会到达。**

### 1.2 `CalcCost` 只有一套公式，且只在 OpenAI 约定下成立

`usage.go:214-226`：

```go
promptCost := promptCount * priceIn
if input.CacheReadPrice != nil && *input.CacheReadPrice > 0 && cacheReadCount > 0 {
    promptCost -= cacheReadCount * priceIn      // ← 只有当 promptCount「包含」cacheReadCount 时才对
    promptCost += cacheReadCount * *input.CacheReadPrice
}
```

**正确金额**（与约定无关）：`fresh×priceIn + cached×cachePrice`
**本代码实际算出**：`promptCount×priceIn − cached×priceIn + cached×cachePrice`

- OpenAI 约定（`promptCount = fresh + cached`）⇒ 化简为 `fresh×priceIn + cached×cachePrice` ✅
- Anthropic 约定（`promptCount = fresh`）⇒ **`fresh×priceIn − cached×priceIn + cached×cachePrice`** ❌
  **比正确金额整整少了 `cached×priceIn`。**

### 1.3 量化实证（生产函数本体）

临时测试直接调用 `streaming.CalcCost`，同一笔物理用量（1000 新输入 + 9000 缓存命中，
priceIn=3、priceOut=15、cachePrice=0.3 每百万）：

| 形态 | `CalcCost` 返回 |
|---|---|
| **正确金额** | **0.005700 USD** |
| **OpenAI 约定** | **0.005700 USD** ✅ 完全正确 |
| **Anthropic 约定** | **−0.021300 USD** ❌ **负数** |
| 两形态差额 | 0.027 USD（= 正确金额的 **4.7 倍**） |

> 测试跑完即删（取证工具不是门；留着会变成「修复前必红」的红门）。

### 1.4 唯一的负值防护在死代码里

| 位置 | 有无负值防护 | 状态 |
|---|---|---|
| `usage.go:230-232` `CalcCost` | 仅 `math.IsNaN \|\| math.IsInf` | **活跃路径，无负值防护** |
| `provider/client.go:253` | `cost < 0` **有** | ⚠️ **死代码**（100 号 §1.6 已证 `Candidate.CalcCost` 零非测试调用） |
| `usage.go:255-287` `AssignRequestCost` | 直接透传 `native`；非 USD 走 `native / CNYToUSDFX` | **负数除以正数仍是负数** |

**⇒ 仓库里唯一防住负金额的那行代码，是没人调用的那份。**

## 2. 影响面

负数 `cost_usd` 一旦落库，会沿着 100 号 §1.3 那 **30 个不去重读点**扩散：

| 消费方 | 后果 |
|---|---|
| `domains/authentication/verifier.go:786` **API Key 预算强制** | `SUM(cost_usd)` 被拉低/变负 ⇒ **预算永远触发不了** ⇒ 租户可超支不被拦 |
| `admin/usage_enhanced.go` / `usage_provider_detail.go` 等费用面板 | 金额显示为负或被系统性拉低 |
| `domains/providerprofile` 对账 `gateway_total_cost` | 网关侧金额偏低 ⇒ `cost_diff_rate` **结构性偏正**，与 F2/F3 的偏差叠加后**差异率完全不可解释** |
| `usage_ledger_hot` / `request_logs_hot` | 负值长期沉淀，污染一切下游聚合 |

**这一条的严重性高于 100 号 §1 那条**：100 号的重复计账需要「歧义提交」这个罕见事件才能触发，
而本条**只要走 Anthropic 原生协议且缓存命中量超过一定比例就会发生**——**这是常规流量，不是罕见事件**。

## 3. 诚实登记：未证实项

- **生产实际发生率我未量化**。需要知道：走 `executor_anthropic.go` 的流量里，
  Anthropic 形状的 usage 是否有多少比例**未经归一化**直接进入 `ExtractUsageFromChunk`。
  若上游代理/网关已把 `input_tokens + cache_read_input_tokens` 重写为
  `prompt_tokens`（含缓存）形状，本条不触发。
- **但代码层面无条件成立**：没有任何约定检测、没有负值钳制。
  **是否已有上游归一化，属于部署/供应商配置事实，不是代码能保证的**——
  而计费正确性不应该依赖「上游刚好归一化过」。
- 按纪律**未做真库查询**（Citus 列存 join 计数不可复现），本条不依赖任何真库数字。

## 4. 建议修法（登记待裁决，本轮不动）

1. **加约定判定**：在 `ExtractUsageFromChunk` 里标记 `promptIncludesCache bool`
   —— 命中 `prompt_tokens_details.cached_tokens` 路径 ⇒ true；命中 `input_tokens`
   路径 ⇒ false。`CalcCost` 按该标志决定是否做「先减后加」。
2. **补负值钳制**：`CalcCost` 出口加 `if total < 0 { total = 0 }`
   （把死代码里已有的那道防线搬进活跃路径）。
3. **存量回算**：已落库的负 `cost_usd` 需要一次修正脚本。
4. 三项都**不改客户端观感**；第 1、2 项是纯计算层修正。

**定级 P1**（活跃路径、无钳制、影响预算强制、常规流量可触发），
但**排在待裁决 42 之前**——42 需要罕见事件，本条不需要。
