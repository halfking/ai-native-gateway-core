# 168 号 · R89-CD —— 撤回 167 号的一条证据：`request_class='immediate'` 是默认值，不是写入方签名

> **日期**：2026-10-01
> **轮次**：R89-CD（第 68 轮，审计第 168 号）
> **类型**：**自我撤回一条证据** + 三项排除 + playbook §61
> **改动生产代码**：无　**改动数据库**：无（全部只读 SELECT）
> **上一轮**：167 号（两个假设皆证伪；`request_logs` 混着 135 万行非网关数据）

---

## 〇、起手：167 号留给我的那半句

167 号结尾写着：

> ⚠️ **写入方身份未查**（无 pid、无来源标记、无 api_key_id）⇒ **需运维取数**

我上一轮把它当成「只能问你」。**先试一次静态能不能答** —— `request_class='immediate'` 是代码里的一个具体取值，**它的写入/取值规则应该能在本仓查到**。

**结果：查到了，而且它推翻了我自己的一条证据。**

---

## 一、⚠️ F1（撤回）：`request_class` 根本不是判别器

### 1.1 代码

`domains/hooks/observability/telemetry/client.go:3814-3822`：

```go
// requestClassArg resolves the request_class bind value. The database invariant
// requires immediate rows to have no due_at and scheduled rows to have one;
// normalizeRequestClassAndDueAt enforces that invariant before every write.
func requestClassArg(c *string) string {
	if c != nil && *c == "scheduled" {
		return "scheduled"
	}
	return "immediate"          // ← 一切「不是 scheduled」的，都落这里
}
```

`client.go:3827-3837` 更强 —— `normalizeRequestClassAndDueAt` 在**每次写入前强制**：

```go
immediate := "immediate"
entry.RequestClass = &immediate
entry.DueAt = nil
```

`internal/ir/class.go:17-19` 的注释也写明：

```go
const (
	// ClassImmediate is the default: execute as soon as a lane is free.
	ClassImmediate RequestClass = "immediate"
```

⇒ **`immediate` 是全域默认值，不是某个写入方的签名。**

### 1.2 对照组（167 号漏跑的那一步）

167 号只查了旁路侧的取值分布，**没查正常流量侧的对照**。补上：

| 组 | `request_class` 取值 | 行数 | 占比 |
|---|---|---|---|
| 正常流量（**有** session） | `immediate` | 798,205 | **100.00%** |
| 旁路流量（**无** session） | `immediate` | 1,357,556 | **100.00%** |

`due_at` 交叉也一致（两组都是 100% `immediate` + `due_at IS NULL`，`scheduled` 行数两组皆为 0）。

⇒ **两组完全相同 ⇒ 这一列携带零信息量。**

### 1.3 撤回声明

**167 号 F2 证据表的第一条「100% 是 `request_class='immediate'`」作废。**
它不是「这批数据是合成的」的证据 —— 它对**所有**流量都成立。

⚠️ **而这一条恰好被我排在证据表第一位、并在结论摘要里点名。** 这不是笔误级别的错误：
**它是「我列了一个听起来很像签名、实际是常量的字段」**。

---

## 二、F2：结论仍然成立，但证据从 5 条降到 4 条

被撤的那条**不是承重墙**。逐条复核其余四条是否受影响：

| # | 证据 | 旁路流量 | 正常流量 | 状态 |
|---|---|---|---|---|
| ~~1~~ | ~~`request_class='immediate'` 100%~~ | ~~100%~~ | ~~100%~~ | **🔴 撤回（两侧相同 ⇒ 无信息量）** |
| 2 | 有 `api_key_id` 的比例 | **28 / 1,357,556 = 0.002%** | 99.98% | ✅ 不受影响 |
| 3 | 有 `prompt_tokens>0` 的比例 | 7.6% | **100%** | ✅ 不受影响 |
| 4 | 有 `request_wal` 行的比例 | **≈0** | **99.99%** | ✅ 不受影响 |
| 5 | `gw_session_id` 非空 | 0% | 100% | ✅ 不受影响 |
| 6 | 模型分布 | **跨 6 厂商近乎均匀** | 高度集中 | ✅ 不受影响 |

⇒ **「这批不是网关流量」的结论仍然成立**，但支撑它的是 **api_key_id / prompt_tokens / WAL / session / 模型分布**五条，不是六条。

⚠️ **诚实说明**：五条里最强的其实是**第 4 条（WAL 命中率 0/262,432 对 99.99%）**，
因为 WAL 是请求主路径的旁证表；`api_key_id` 排第二。**我上一轮把最弱的一条排在了第一位。**

---

## 三、F3：排除 `cmd/auto-testbench`（名字最像，但读源码就被排除）

167 号那批数据的 `is_auto_request` 几乎全为 TRUE，而仓库里有一个 `cmd/auto-testbench/`
—— 名字上最像的嫌疑人。**读源码后排除**：

1. `cmd/auto-testbench/generate.go` 文件头**自带红线声明**：

   > 隐私红线（规划 §七.4）：只 SELECT 结构化特征列，绝不取 prompt 原文；
   > 产出行的 prompt 是占位符，带 `generated=true` 标记
   > **本工具对数据库只读（仅 Query）。**

2. 对整个目录检索 `INSERT|UPDATE |Exec\(|http\.Post|/v1/chat|/v1/messages` ⇒ **零命中**。
   它既不写库也不发 HTTP 请求，**只产离线测试文件**（`testdata/`）。

3. ⚠️ **附带一条时间线事实**：该目录文件 mtime 为 **9月25–9月28**，而旁路数据从 **09-03** 就在
   ⇒ **时间上也对不上**，它不可能是写入方。

⇒ **排除。（顺带：`model_diversity_test.go` 那个「模型多样性」测试，与我看到的「跨 6 厂商均匀分布」形态很像，
但它打的是真实 HTTP、有 api_key、且时间线不符 ⇒ 不是。）**

---

## 四、⚠️ F4：把「写入方是谁」从「需要你」改写成「可静态回答」

167 号说「需运维取数」。**本轮查下来，这个问题的性质变了。**

`domains/hooks/observability/telemetry/client.go:206`：

```go
APIKeyID            *int            `json:"api_key_id,omitempty"`
```

⇒ **它是指针 + `omitempty`，写 NULL 是设计上允许的。**

⇒ 所以「这些行没有 `api_key_id`」**不能推出「不可能来自网关」** ——
**正确的问法是：哪条写入路径会把 `APIKeyID` 留成 nil？**
`client.go` 里 `entry.APIKeyID` 被用在 `:1079 / :1303 / :1651`（insert 参数）与
`:1832 / :1875 / :2464`（计费回写，后两处都显式判了 `entry.APIKeyID != nil && *entry.APIKeyID > 0`）。

**⇒ 这是一个纯静态可答的问题**（哪些构造 `RequestLogEntry` 的路径不设 `APIKeyID`），
**但本轮未穷举** ⇒ 登记为下一轮的活，**而不是甩给你**。

⚠️ **但要诚实**：即使找到某条 nil 路径，也只能证明「网关**可能**产生这种行」，
**不能证明「这 135 万行就是它写的」** —— 后者仍需运行时证据（写入方身份 / 时间线）。
**⇒ 待裁决 69 保持开放，但「需运维取数」的范围缩小为：只要一个『谁在写』的答案，不需要任何网关侧改动。**

---

## 五、playbook §61 新增

> **§61 每条证据都要单独配对照组；「两组都是 100%」的那一列不是判别器 —— 且证据表要按强度排序，不要按「听起来像签名」排序**

**本条的由来**：167 号把「`request_class='immediate'` 占 100%」列为「这是合成数据」的第一条证据。
它听起来**极像**一个写入方签名（一个具体的枚举值、恰好 100%），而实际上它是
`requestClassArg` 的**兜底返回值** + `normalizeRequestClassAndDueAt` 的**强制值** ——
**对所有流量恒为 100%。**

**⇒ 规则一：每条证据单独问「对照组长什么样」。**
本轮若在 167 号就跑了对照组（正常流量也是 100%），这条证据当场作废，**不会进报告**。
**这与 §60 是同一条纪律的两个方向**：
- §60：「某列全空」要配 **JOIN 命中率** 分母；
- §61：「某列 100%」要配 **对照组分布**。
**两者都是：单侧分布不构成结论。**

**⇒ 规则二：判别器的必要条件是「两组分布不同」。**
写证据表时，对每一行问一句：**「如果这批是正常流量，这一列会长什么样？」**
两侧相同 ⇒ 这一列不进表。

**⇒ 规则三：证据表按强度排序，最强那条放第一。**
本轮最强的其实是 `request_wal` 命中率（0/262,432 vs 99.99%），因为它是**主路径旁证表**；
`api_key_id`（0.002% vs 99.98%）排第二。**我上一轮把最弱的排在了第一位** ——
摘要里被点名的通常是第一条，**排错序 = 强调错的东西**。

**⇒ 规则四（推论）：默认值、兜底值、常量列，永远不能当判别器。**
本仓已确认的此类列：`request_class`（兜底 `immediate`）、`request_type`（`client.go:3811`
注释写明「Returns "main" for a plain client request (the column DEFAULT)」）。
**入库前先问「这个字段的默认值是什么」。**

**同族**：§47 / §48 / §50 / §57 / §59（尺子错的两种形态）/ **§60（单侧全空要配 JOIN 命中率）**。
**§60 与 §61 是同一条纪律的两端：一端管「全空」，一端管「全满」。**
