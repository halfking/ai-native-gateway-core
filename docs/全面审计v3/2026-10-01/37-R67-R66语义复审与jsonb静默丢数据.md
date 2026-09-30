# 37 — R67：R66 语义复审 + jsonb 静默丢数据收口

> 2026-10-01 03:27 起，基线 `0265aef47`（R66 推送后的合并态）。
> 本轮编号独立：R66 是 rows 迭代截断的**实施轮**，R67 是它的**独立复审轮**
> 加上同族下一件的收口。

## 0. 本轮为什么做这件事

R66 把 245 处 rows 迭代截断收口，守卫全绿。但**守卫只证明「`rows.Err()`
被调用了」，不证明「语义分型选对了」**。而 R66 的分型里有一条硬约束来自
项目原则：「不要影响客户端的感观」。具体风险是——

**把本该 best-effort（降级返回 200 + 部分数据）的站点改成 fail-fast
（500），就是把一次「面板少一块」变成「端点整个挂掉」。** 守卫对此完全
无感：两种写法都满足「循环里有 `rows.Err()`」。

所以本轮第一件事不是继续找新缺陷，而是**回头验 R66 自己**：派两个独立
复审代理，一个只看 `admin`（查过度升格），一个只看 `admin` 之外的存储层/
后台任务（查反向的过度上抛）。

**「门全绿」和「门覆盖了我」之间，隔着一次语义复审。**

## 1. R67-C：jsonb 列反序列化静默丢数据（15 处，已完成并提交 `c121a34ad`）

R66 收的是「读没读全」（rows 截断）。本轮收同族的另一半：**读到了但解不开**。

### 1.1 缺陷形态

```go
if len(argsJSON) > 0 {
    _ = json.Unmarshal(argsJSON, &cmd.Args)   // 失败？没人知道
}
```

调用方拿到零值对象，且**无法区分「这行本来就是空的」与「这行的数据坏了」**。
在命令审计、消息正文、评审结果这类完整性通道上，这个区分直接决定运维能不能
查出问题——而现状是两者长得一模一样。

覆盖 15 处：`center/store_pgx.go` 5（`GetCommand` / `ListPendingCommands` /
`GetCommandHistory` 的 args 与 result）、`vibecoding/store_pgx.go` 10
（`project.Settings` / `session.Messages` / `session.Metadata` /
`review.ReviewResult`）。

### 1.2 共享 helper `internal/jsoncol.Decode(op, raw, dst)`

| 输入 | 返回 | dst |
|---|---|---|
| raw 为空（NULL / 空串） | `true` | 不动 |
| raw 非空、解析成功 | `true` | 已填充 |
| raw 非空、解析失败 | `false` + `slog.Warn` | 保持调用方原值 |

**未知字段不算失败**——`jsonb` 里的额外键不应让整条记录判为坏数据，否则滚动
升级期的旧数据会整片变零值。这条有专门的测试钉住。

### 1.3 语义选择：留痕后继续，**不上抛**

单行 jsonb 损坏而上抛，会把整个列表端点变成 500——**把「一行数据坏了」升格
成「接口不可用」**，与 R66 定下的原则相反。故与 R66 的「best-effort 通道」
同类处置：留痕、按零值继续、端点照常返回。

### 1.4 验证

| 项 | 结果 |
|---|---|
| `go build ./center/ ./vibecoding/` | exit 0 |
| `go vet`（三包含 jsoncol） | exit 0 |
| `center` 全包（真库） | **ok 8.7s** |
| `internal/jsoncol` | **3 测试全绿**，含「空 vs 坏必须可区分」「未知字段不算失败」两条针对性断言 |
| `vibecoding` | **无测试文件**，如实登记 |

## 2. R67-A：admin 语义复审 —— 找到的是**反方向**的缺陷

派复审代理时我给的方向是「查 best-effort 被误升格为 500」（过度升格）。
**代理在主聚合族里一个都没找到**——R66 在那儿的分辨力比我自己写的任务书还好：

- `tenants.go` 的 `byModel`/`byApplication` 硬失败是对的，因为它们的**查询
  错误兄弟本来就已经硬失败**，而真正可选的 `attachTenantUsage7d` 正确地
  只 warn 不 500。这正是想要的分辨力。
- SSE（`live_stream_sse.go`）两个调用方都 warn 后继续，**没有** flush 之后再
  写错误的 `superfluous WriteHeader`；CSV 导出（`pricing.go`）头部已写出，
  R66 明确选了 warn。头已提交的情形处理正确。
- `auto_route.go handleAudit` 的早退跳过了 `rows.Close()`，但 pgx 的
  `Next()` 返回 false 时会自动 `Close()`，**无连接泄漏**（对照 pgx v5.9.2
  `rows.go` 核实）。
- 孤儿/错循环检查：把所有 `writeAggRowsErr(w, op, X.Err())` 与最近的
  `for X.Next()` 交叉比对，61 个文件**0 处错配**；已知的 panorama 孤儿已在
  合并 `763b2ea11` 修掉。

**但代理找到了两处真缺陷，形状与我预期的相反**。

### 2.1 D1（P1）：provider refresh 把「什么都没刷」记成成功

`admin/provider_refresh.go:237` 的调用点是 `creds, _ := h.fetchActiveCredentialsForProvider(...)`，
而 R66 把该函数的单行 Scan 失败从 `continue` 改成了 `return nil, fmt.Errorf(...)`。
两件事叠加：

```
creds == nil  →  for 循环一次都不执行  →  totalUpserted = 0, totalFailed = 0
              →  `totalFailed > 0 && totalUpserted == 0` 不成立
              →  记为 providerRefreshSucceed
              →  文案「新增/更新 0 个模型（凭据 0 个，失败 0 个）」
```

一次什么都没做的刷新被记成**绿色成功**，且没有任何错误可追。这比原缺陷更糟：
原来是「跳过坏行继续刷」，现在是「静默不刷且报告成功」。

值得注意的是，R66 留在原处的注释还写着「漏扫的凭据会被静默跳过刷新」——
**注释描述的是「跳过」语义，实现却改成了「失败」**，两者已经对不上。

**修法**：在调用点接住错误并**记成失败**（`c121a34ad` 之后），而不是只打日志。

### 2.2 D2（P1）：别名索引被截断，analytics 的 canonical 列静默退化

`admin/model_normalize.go:44`，同一形状：`loadModelAliasIndex` 的单行 Scan
失败被改成 `return idx, fmt.Errorf(...)`，但两个调用方
（`analytics.go:356` matrix、`:435` funnel）都是 `aliasIdx, _ :=` **丢弃错误**。

于是上抛的实际效果不是「让调用方知道」，而是**在第一行坏数据处截断整个索引**，
且截断对调用方完全不可见。后果：analytics 的 canonical 列静默退化成 raw 名，
看起来像「模型换了名字」——**静默的数据质量回归**。

R66 当时的注释还专门论证了「调用方对 err 是降级容忍的，所以直接上抛」——
这句推理**恰恰是错的**：调用方容忍错误，意味着上抛的收益为零，而代价是截断。

**修法**：单行 Scan 失败回到「跳行留痕」；同时给两个调用方加留痕，让
「索引缺失/截断」不再是静默的。迭代终检 `rows.Err()` 保留（那是另一种故障）。

### 2.3 D3：复审代理标为「机制不精确」，复核后判定**原修正确**

`work_types.go fetchL1Counts` 的 `return nil, fmt.Errorf` 同样配 `dbCounts, _ :=`。
但这里的**上抛是承重的**：它把「半个 map」换成 nil，使
`mergeL1TaskTypes(nil)` 走调用方**自己文档化的** canonical-only 回退。
若降级成 warn，调用方会把半个 map 当全量用。

⇒ 判定为复审误报，**不改**，并加测试把这个「看似该简化」的地方钉住，
防止后来者顺手改成 warn。

### 2.4 回归测试（4 项，均经变异验证）

`admin/r67a_regression_test.go`，钉的是**失败形态**而非实现细节：

| 测试 | 钉住 |
|---|---|
| `TestR67A_ProviderRefreshCallSiteMustNotDiscardError` | D1：调用点不得丢弃错误、必须记 `providerRefreshFailed` |
| `TestR67A_ModelAliasIndexPerRowScanMustNotTruncate` | D2：单行 Scan 不得 `return idx,`；且 `rows.Err()` 终检不得被一起删掉 |
| `TestR67A_AliasIndexCallersMustTraceLoadFailure` | D2 调用方侧：不得 `aliasIdx, _ :=` |
| `TestR67A_WorkTypeL1CountsUpThrowIsLoadBearing` | D3：把「看似该简化」的上抛钉成有意决策 |

**变异验证**（两项都按预期变红、还原后恢复绿）：

- 撤回 D1 修复（恢复 `creds, _ :=`）⇒ D1 测试红，报「call site discards the error again」
- 撤回 D2 修复（恢复 `return idx, fmt.Errorf`）⇒ D2 测试红，报「must warn+continue」

写这两条测试时我犯了两个自己的 bug 并当场修正：① 断言窗口 200 字符装不下
整个 if 块；② 用「Scan 之后到函数末尾」划界，把下面 `rows.Err()` 的**合法**
上抛误判成 per-row 返回。第二个尤其值得记——**测试的判据边界错了，会把正确
代码判成缺陷**。

## 3. R67-B：存储层/后台任务语义复审

（待补）

## 4. 保持开放

（待补）
