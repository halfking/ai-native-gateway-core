# R76-3 批判审计：candidate_diag 冷却闸把「归因变更」静默吞掉（2026-11-17）

> 审计对象：`llm-gateway-go` 的 **R-76-3「WARN 洪水分诊降噪」**（db_empty 同键冷却 +
> 探测判决去重）。该改动**已完整落地在 `origin/main`**（本轮复核时 SHA 为
> `32f041423`），两侧接线与判据都在位。本报告记的是**落地之后**才发现的缺陷。

---

## 一、结论 / 根因

**一句话**：provider 侧的 db_empty 日志冷却闸，**键里没有归因** ⇒ 同一
`(model, tenant)` 在 5 分钟冷却窗内归因从 A 变成 B 时，**B 被静默吞掉**；
而该文件自己的注释宣称「模型登记/状态闸被改时**会立即以新内容出现**」——
**那句话是假的**。

### 根因（三层）

1. **键缺字段。** `provider/candidate_diag_dedup.go` 的键是
   `event|model|tenant`，**不含归因**。而 bg 侧的同族闸
   `bg/probe_round_log_dedup.go` 的键是 `cause|outboundModel|providerID`，
   **根因在键里**，其文件注释明写「换根因（如 node→protocol）都会立刻放行」。
   ⇒ **两侧不对称**：bg 侧把「根因变化」当第一类语义处理，provider 侧没有。

2. **不对称没有判据钉住。** bg 侧有专门判据
   `TestLogProbeRoundRootCauseRootCauseChangeNotGated`；provider 侧**没有对应判据**。
   ⇒ 缺的语义不会被发现。

3. **注释把缺失的语义写成了已有的语义。** `candidate_diag_dedup.go` 原注释：

   > 5 分钟：Owner 处置周期以分钟计，窗口内重复打屏无信息增量；窗口外放行
   > 一条以确认归因未漂移（模型登记/状态闸被改时**会立即以新内容出现**）。

   ⇒ 「会立即以新内容出现」在原实现下不成立：状态闸被改 ⇒ 归因变了 ⇒
   **但键没变** ⇒ 要等冷却窗到期才出现。

### 危害不是「日志少了」，是**处置方向被误导**

- 旧归因「模型未登记（offers_total=0）⇒ 去登记模型」
- 新归因「模型已登记（3 offer），但 2 条被凭据配额态滤掉 ⇒ **充值/解封**」

后者是**可立即执行的运维动作**。被压掉意味着运营在最长 5 分钟内仍按旧结论排查
（甚至去登记一个**已经登记了**的模型）。

### 严重性如实标注

- **不是 P1**：不丢指标、不改状态机、不改返回结果。`recordCandidateDiagnostic`
  在闸**之前**调用 ⇒ `llmgw_routing_candidate_diagnostics_total` 口径不变
  （已实读 `provider/candidate_diagnostic_metrics.go:61` 确认是 `Inc()`）。
- **不是「功能没接上」**：闸确实在生产路径上（`provider/client.go:1151`），
  bg 侧同理（`bg/probe_root_cause.go:190`）。
- 是 **P3 级「结论失真 + 处置延迟」**：5 分钟窗口内的新归因不可见。

---

## 二、修正

### 2.1 键里加**有界归因类**（不是 `likely_cause` 原文）

★ **为什么不能把 `likely_cause` 原文塞进键**：`EmptyCandidateBreakdown.Summarize()`
把 offer **计数**也拼进句子（「模型已登记（3 offer），但 2 条…」）。计数抖动
（3↔4）会让键**交替出现** ⇒ 每条都是「该键首次」⇒ **洪水反而压不住**。
这是本轮修法里最容易做错的一步，已用判据钉住（见 §三 `TestCauseClassValuesAreBounded`）。

⇒ 新增 `EmptyCandidateBreakdown.CauseClass()`：与 `Summarize()` **分支顺序严格一致**
的**有限枚举**（11 个取值）：

| 常量 | 取值 | 含义 |
|---|---|---|
| `CauseClassNotRegistered` | `not_registered` | 模型压根没登记 |
| `CauseClassCredentialMissing` | `cred_missing` | offer 挂在已删除/缺失的凭据上 |
| `CauseClassCredentialStatus` | `cred_status` | credentials.status 闸 |
| `CauseClassCredentialManualDisabled` | `cred_manual_disabled` | manual_disabled 闸 |
| `CauseClassCredentialLifecycle` | `cred_lifecycle` | lifecycle_status 闸 |
| `CauseClassCredentialQuota` | `cred_quota` | 凭据配额态 |
| `CauseClassProviderDisabled` | `provider_disabled` | 供应商被停用 |
| `CauseClassUnavailableReason` | `unavailable_reason` | offer 人工下线标记 |
| `CauseClassProviderTenant` | `provider_tenant` | offer 属于其他租户的供应商 |
| `CauseClassGatesOpen` | `gates_open` | 状态闸全开 ⇒ 状态不是原因 |
| `CauseClassDiagError` | `diag_error` | 归因查询本身失败 |

### 2.2 改动清单

| 文件 | 改动 |
|---|---|
| `provider/candidate_empty_diag.go` | 新增 `CauseClass()` + 11 个常量 |
| `provider/candidate_diag_dedup.go` | 键加 `cause`；`allow()` 加参数；**订正那段不实注释** |
| `provider/client.go` | `diagArgs` 两分支各加 `cause_class`；`diagArgsKeyFields` 多返 `cause`；闸取键带上 `cause` |
| `provider/candidate_diag_dedup_test.go` | 既有 5 条用例跟签名；`TestCandidateDiagLogGateKeyIsolation` 增「归因类隔离」断言 |
| `provider/candidate_diag_dedup_cause_test.go`（新） | 5 条：归因变化放行 / 同归因仍压 / 租户仍隔离 / 分类与摘要一致 / 分类取值有界 |

### 2.3 日志格式变化

`[candidate_diag] db_empty` 的 args 多了 `cause_class=<有限枚举>` 一个字段。
**这是新增字段，不是改字段** ⇒ 既有日志消费方（grep / 看板）不受影响，
且分类可被机器检索（原文 `likely_cause` 是人读句子，不可聚合）。

---

## 三、判据与变异

### 3.1 负控（修复前必须红）

```
--- FAIL: TestLogCandidateDiagnosticDbEmptyCauseChangeNotGated
    归因变化必须在冷却窗内立刻放行（期望 2 条日志，实得 1 条）
```

⇒ 修复前**行为性红**（不是编译错）。修复后转绿。

### 3.2 变异复核（MUT-1：只回退「键里加归因」）

把键改成 `event|model|tenant`（**保留** `cause_class` 参数与日志字段）⇒

```
--- FAIL: TestLogCandidateDiagnosticDbEmptyCauseChangeNotGated
--- FAIL: TestCandidateDiagLogGateKeyIsolation
```

⇒ 负载的确实是**键的改动**，不是「多了个日志字段」这种表面变化。还原后
md5 `23c4661c3cb9f7fe29cd3968c7a3f0df` 逐字节一致，复跑绿。

### 3.3 反向对照（防恒真）

| 判据 | 作用 |
|---|---|
| `...SameCauseStillSuppressed` | 归因**没变**时仍要压 ⇒ 上一条不是恒真 |
| `...TenantStillIsolated` | 归因进键后**租户隔离**未被破坏 |
| `TestCauseClassAgreesWithSummarize` | 逐分支钉「分类与摘要说同一件事」⇒ 防两套结论自相矛盾 |
| `TestCauseClassValuesAreBounded` | 分类取值有界、且**offer 计数抖动不改变分类** ⇒ 防键空间炸开 |

### 3.4 门读数（合并后、当前 `origin/main` 基线上重跑）

| 命令 | 结果 |
|---|---|
| `go build ./...` | rc=0 |
| `go vet ./provider/ ./bg/` | rc=0 |
| `gofmt -l`（本轮 5 个文件） | 空 |
| `go test ./provider/ -count=1` | `ok 2.049s` |
| `go test ./bg/ -count=1` | `ok 33.987s` |

---

## 四、审计过程中我自己判错的两处（如实记账）

**① 「provider 侧闸没接线」——错。** 我用
`git show origin/main:provider/client.go | grep -n "candidateDiagLogGateInstance\|logCandidateDiagnostic" | head -8`
查，`head -8` 把第 1151 行截掉了，据此写下「未接线」。**接线是在的**
（`origin/main:provider/client.go:1151`）。⇒ **「grep + head」在多匹配时会把
关键行吃掉，而结论读起来毫无异样**——与本仓「用 `grep -c` 计数而不是 `head`」
的纪律同源。

**② 「R76-3 分支不存在、从未推过」——错。** `git branch -a --contains 8f8ac0c25`
返回空，我据此判定「没推」。实际是：R76-3 落地时被 **rebase 成了新 SHA**，
`8f8ac0c25` 是 rebase 前的**悬空对象**。核实办法是逐文件比对内容
（`provider/candidate_diag_dedup.go` 等：测试文件逐字节相同、两个 .go 仅
gofmt 注释重排）。⇒ **「分支列表里没有」不等于「没落地」**；要查内容。

**③ 顺带一条**：`filepath.Walk("..")` 的**根目录自身名字就是 `..`**，
按点目录跳过时若不豁免根会 `SkipDir` 掉整棵子树，读数是「写入方实测 0 个」，
与「不止一个」长得完全不一样。

---

## 五、遗留风险

1. **冷却窗内「同因重复」仍会压掉首条之后的全部条数**（设计如此）。若归因在
   5 分钟内**来回抖动**（A→B→A），会形成两个桶各压一半，**单桶的 suppressed 计数
   分别累积**，下一条放行时各自附自己的 `suppressed_repeats=N`。这是正确的
   （每种归因各算各的洪水），但运维读到的 N 会被拆成两份。
2. **`entries` map 无淘汰。** 键空间 = 归因类(11) × 模型 × 租户 ⇒ **有界**，
   但**只增不减**（租户/模型下线后条目仍在）。文件头原写「运行期自然收敛」
   措辞不准（是"有界"不是"收敛"）。⇒ 影响面小（本仓 960 个 canonical 模型、
   租户数有限），本轮未改措辞之外的任何行为。
3. **两侧闸仍是独立实现**（bg 的 probe 闸 / provider 的 candidate 闸）。
   R-76-3 的文件头已写明「两处键语义、冷却时长、并发形态都不同，抽公共件只会
   把两个口径焊死在一起」⇒ 本轮**维持不抽公共件**，只把「归因进键」这条语义对齐。
4. **`cause_class` 是新增日志字段**。若有下游对 `[candidate_diag] db_empty`
   的 args 做了**严格等值断言**（而非按键取值），会失败。全仓未见此类消费方
   （已 grep），但这是**日志契约变更**，已在 §2.3 显式登记。
