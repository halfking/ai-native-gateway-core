# 126 号｜R89-AM：待裁决 48 的根因 —— 一次「循环合并」重构漏搬了 3 行契约，**并且连同唯一的守卫测试一起删掉**

- 日期：2026-10-01
- 轮次：R89-AM
- 起因：125 号把 `candidates_tried` 恒为 1 定性为「回归」，但只给了现象与分布，**没给是哪一次改动造成的**。本轮用 git 历史把根因定位到单个提交。
- 结论先行：
  1. **根因提交 = `f7eb0eb1b`（2026-08-17，`refactor(executors): retire legacy sync candidate loop + ursm/v2/reducer (AUDIT_24H B2a+B2b)`，34 文件 −4,027/+481 行）**。
  2. 它把 **sync/async 两条候选循环合并成一条**（`runAsyncRetry` 在当前文件已不存在），**合并时漏搬了成功路径上的 3 行契约**。
  3. **同一个提交把唯一的守卫测试 `executor_glm_test.go`（76 行）整个删掉了** —— 那个测试里有精确断言 `result.Trace.PlannedCandidates == 3`。
     ⇒ **提交信息写「651 tests PASS」是自洽的，因为能抓住这件事的测试在同一个提交里被删了。**
  4. 契约在删除前是**明确写在注释里的**（不是隐式约定）⇒ 这是**漏搬**，不是有意废弃。

---

## 一、`ExecuteResult.Trace` 的完整生命周期（git `-S` 全历史，仅两条命中）

```
$ git log --oneline -S "result.Trace = trace" -- domains/streaming/executors/executor.go
f7eb0eb1b refactor(executors): retire legacy sync candidate loop + ursm/v2/reducer (AUDIT_24H B2a+B2b)
134880683 feat(refactor): Phase 1.5 R1.1-R1.11 成熟代码复用 — 23 个领域包 46k 行 + 651 tests PASS
```

- **加于** `134880683`（同期也加了 `handler.go` 的 `CandidatesTried: 1`）。
- **删于** `f7eb0eb1b`（2026-08-17）。

`f7eb0eb1b` 在 `executor.go` 里删掉的 Trace 相关行（`git show` 摘录）：

```
-			trace.Chosen = &TraceCandidate{
-				ProviderID:   cand.ProviderID,
-				CredentialID: cand.CredentialID,
-				RawModel:     cand.RawModel,
-				Tier:         cand.Tier,
-				Reason:       "succeeded",
-			}
-			result.Trace = trace
-			if params.AuditBuilder != nil {
-				params.AuditBuilder.DecisionTrace(trace)
-			}
-			return result, nil
...
-				result.Trace.FallbackFromModel = params.ClientModel
-				result.Trace.FailureReason = ""
```

**这解释了两个此前悬空的 P3**：`Trace.Chosen` 与 `Trace.FallbackFromModel` 不是「从来没人设计过」，而是 **2026-08-17 之前一直在写、之后没人写**。125 号按「生产零赋值」登记它们是对的，但**成因要改述为「随重构丢失」**，不是「设计时留了空字段」。

## 二、这是**漏搬**，不是有意废弃 —— 契约明写在注释里

删除前的 `executor.go:5136-5140`（旧文件坐标）：

```go
// Note: result.Trace (planned candidates) and
// result.Candidate.CanonicalID are NOT forwarded here.
// RequestLogEntry doesn't have a trace field (it lives
// on DecisionLogEntry, written separately by the sync
// phase), and provider.Candidate has no CanonicalID.
```

**注释明确说：候选池数据会「由 sync 阶段单独写进 `DecisionLogEntry`」。** 而这个提交做的正是「retire legacy **sync** candidate loop」
⇒ **它删掉的正是那段被注释承诺的写入方。** 注释描述的契约与被删的代码严格对应，
所以这是**重构时漏搬**，不是「决定不再记录决策原因」。

⚠️ 诚实的反面：**该提交没有留下任何说明**（无 commit body 说明决策、无迁移说明、无 issue 引用），
所以**不能排除**「作者本意就是简化」。区分二者需要看 24 小时方案的 B2a/B2b 条目是否提到决策审计——
**本轮未查，列为下轮必核**（若方案原文确实写了「本次不记录决策 trace」，
定性要从「漏搬」改为「有意但未告知下游」，两者修法优先级不同）。

## 三、守卫与被守卫对象在同一个提交里被删除 —— 本审计最干净的一次「变异后仍绿」

```bash
# 提交前：executor_glm_test.go 里有精确断言
git show f7eb0eb1b^:domains/streaming/executors/executor_glm_test.go | sed -n '326,330p'
	if result.Trace == nil || len(result.Trace.PlannedCandidates) != 3 {
		t.Fatalf("planned candidates = %+v, want 3", result.Trace)
	}
	if len(result.Trace.BlockedCandidates) != 0 {
		t.Fatalf("blocked candidates = %+v, want no router-filtered candidates", ...)
	}
```

```bash
# 该提交对测试文件的处置
$ git show f7eb0eb1b --stat -- domains/streaming/executors/ | grep glm
 domains/streaming/executors/executor_glm_test.go   |  76 -      ← 整个文件删除
```

**HEAD 上 `git grep '\.Trace' -- 'domains/streaming/executors/*_test.go'` 已无任何针对
`result.Trace` / `PlannedCandidates` / `BlockedCandidates` 的断言。**

⇒ **该测试是这个契约在仓库里的唯一守卫，而它与被守卫的行为在同一提交被删除。**
⇒ 提交信息的「651 tests PASS」与这次回归**完全自洽**：
**门是绿的，因为门本身没了。**

这与本会话早前记录的「变异后仍绿」是同一族，但**形态更极端**：
变异测试是「改坏→红→还原→绿」，证明门有效；
**本例是「连门一起删」，事后任何人都无法从 CI 结果回溯出这里曾经有契约。**

## 四、修法方向（**未动手**，待裁决 48/49 拍板）

修复只需在**当前成功分支**补回 3 行（旧代码的形状已完整取回）：

```go
trace.Chosen = &TraceCandidate{ProviderID: cand.ProviderID, CredentialID: cand.CredentialID,
                               RawModel: cand.RawModel, Tier: cand.Tier, Reason: "succeeded"}
result.Trace = trace
if params.AuditBuilder != nil { params.AuditBuilder.DecisionTrace(trace) }
```

- `trace` 变量**当前仍在作用域内**且仍在填充 `PlannedCandidates`（`executor.go:2351-2356`），
  数据侧无需重建。
- 补上后 `handler.go:5891` 与 `:6671-6673` 两个**死分支自动复活**，
  `candidates_tried` 与 `decision_trace` 两条回归同时修复。

⚠️ **诚实边界 —— 修复点未精确定位到当前行号。**
`Execute`（`executor.go:2128` 起）在 2351 构建 `trace` 之后，
主成功返回点**本轮未找到**（该区间内 `return result, nil` 只有 `:2562` 一处，
属 `sync_no_candidate_probe_recovered` 的递归恢复路径，非主成功路径；
`runAsyncRetry` 在当前文件**已不存在**，说明两条循环已被合并成一条）。
⇒ **下轮必核：当前合并后单条循环的成功返回点在哪一行。**
在此之前，上面的修法是「形状正确、位置待定」。

**同时建议补回守卫测试**（否则同样的合并会再发生一次）——
但这属于「加测试」而非「改生产代码」，仍按待裁决处理，本轮不擅自提交。

## 五、定性汇总（对 125 号的改述，不推翻结论）

| 项 | 125 号表述 | 本轮修正 |
|---|---|---|
| `candidates_tried` 恒 1 | 回归，成因未知 | **回归，成因 = `f7eb0eb1b` 循环合并漏搬** |
| `decision_trace` 成功腿为空 | 现状描述 | **同上；且旧代码有明确注释承诺 ⇒ 漏搬而非有意废弃** |
| `Trace.Chosen` / `FallbackFromModel` 零赋值 | P3，像「设计留空」 | **P3，成因 = 随同一次重构丢失**（2026-08-17 前一直在写） |
| 守卫测试 | 未查 | **`executor_glm_test.go` 在同一提交被删 ⇒ 门是被守卫对象一起删掉的** |

待裁决 **48 / 49 的级别与修法优先级不变**（48 仍 P1，49 仍 P2），
但**48 从「现象」升级为「根因 + 完整修法 + 缺失的守卫」**，可执行性显著提高。

## 六、playbook §25（本轮新增）

**「重构后某能力消失」类排查，固定三查：**
1. `git log -S "<被删的赋值语句>" -- <file>` —— 定位增删该赋值的**全部**提交（本例只有 2 条，干净可读）；
2. **看被删代码周围的注释** —— 注释里对「谁来写」的承诺，是判定「漏搬 vs 有意废弃」的决定性证据
   （本例 `// ... it lives on DecisionLogEntry, written separately by the sync phase`）；
3. **查守卫测试的处置** —— `git show <commit> --stat -- <被删代码所在目录>`，
   若守卫测试与被守卫行为**在同一提交被删**，则该提交的测试通过记录**不构成任何证据**。

⇒ 第 3 条对「AI 生成的重构提交」尤其重要：
一次提交同时删掉 4,000 行代码与其测试时，**「651 tests PASS」是最容易被误读为安全的信号**。
