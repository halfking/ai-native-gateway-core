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

## 3. R67-B：存储层/后台任务语义复审 —— 判 PARTIAL

判 **PARTIAL 而非 PASS**。`bg/` 的纪律守住了：22 个循环站点里 20 个是
warn-and-continue，代理逐个追到生产调用方，**没有发现 worker 循环被杀、
调度被跳过、或熔断/健康状态被写坏**。它找到的两处缺陷都不是过度升格。

### 3.1 D4（P1）：`ExportFromTx` 在调用方持有的事务里丢弃未关闭的 Rows

`domains/sessionforensics/export.go:176` 的 `tx.Query` **没有**
`defer rows.Close()`——而同文件另两个函数（`:304` / `:426`）都有。

这个洞本身是**潜伏的**：循环总是跑到耗尽，而 pgx 在 `Next()` 返回 false 时
会自动 `Close()`，所以「不 Close」从不显形。R66 在循环中加了
`return nil, ...`（取证包必须完整，见 3.3），这条路径**于是变得可达**：
rows 在结果集仍挂载时被丢下，而 tx 是**调用方持有**的，调用方随后带着半读的
结果集去 rollback / 释放事务。

性质要说准：文件里 `:234` 的 `rows.Err()` 早退**早就有同样的形状**，所以这是
**被扩大的既存洞**，不是全新设计错误——但让 mid-loop 路径变可达的是 R66。

**修法**：补 `defer rows.Close()`。注意形式差异：另两个函数走 `e.store`
（`database/sql`，`Close()` 返回 error，故写 `defer func(){ _ = rows.Close() }()`）；
本函数走 `pgx.Tx.Query`（`Close()` **无返回值**），只能直接 defer。
我第一版照抄了邻近函数的写法，`go build` 立刻报
`rows.Close() (no value) used as value`——**这正是形式不能混用的实证**。

### 3.2 D5（P2）：`model_tier` 的全量替换集合只 warn，与 `hotconfig` 口径不一致

`bg/model_tier.go` 的 `refresh()` 新建整份 `featuredSet` 并**无条件**
`m.cur.Store(fs)`。这与 `hotconfig.reload` 的形状**完全相同**（整份配置的
全量替换），而 R66 正因如此把 `hotconfig` 升级成硬失败——两处口径不一致。

后果：usage Top-N 被截断会让高频模型静默退出「常用模型」层、深探优先级丢失，
且这份坏集合**永久替换掉**之前那份完整的，要等下一个 10 分钟 tick 才自愈。
R66 的注释甚至点名了这个后果（「Top-N 截断会让高频模型退出深探范围」），
然后只 warn。

**修法**：两处（static featured / usage top-N）截断时跳过 `m.cur.Store`，
保留上一份完整集合。10 分钟自愈不是不能接受，但**分类学不一致**才是问题——
同样的形状在两个地方得到不同的待遇。

### 3.3 代理核实为「正确、无需改动」的几处

- `ExportSession` / `ListRecentSessions` 的上抛：都有 `defer`，无泄漏；取证
  包与审计列表**跳行比失败更危险**（`turn++` 在 Scan 之后，跳行会让轮次序号
  与真实请求错位，产出一份「看起来自洽、实则缺轮」的证据包）。
- `hotconfig.reload`：硬失败是**承重**的，且 fail-safe——`main.go` 降级到
  `journeyConfig = requestjourney.DefaultConfig()`，而不是装一份缩水配置。
  代价是启动期一次 DB 抖动会永久失去热加载，对分型 5 而言这是对的取舍。
- `bg/shared_pick.go` 两处新硬失败：这是任务书担心的场景，但**成立**——
  `repickAll` 本来就有 `err != nil → continue` 分支，错误**不逃出**
  `repickAll`，而 `repickAll` 从 `run()` 调用且无 error 返回，小时级 worker
  未受影响。
- `default_probe_picker.go` 只 warn 也**正确**：选择发生在下一层的
  `PickProbeModelForCredential`（那里硬失败）；这一层丢的是「待重选的候选」，
  不是「选择本身」，凭据保留原有的 `default_probe_model` 而非被指派错的。
- `autoroute/recommend_v2.go`：签名不可改，但在写 2 分钟 TTL 缓存**之前**
  return——**截断结果只影响这一次请求，不会进缓存**，下个请求自愈。合理的缓解。
- `ipblocklist` 注释里「调用方各自紧跟 `rows.Err()` 上抛」的说法**核实为真**
  （`:61` / `:158`），内层检查是有意重复。
- 非错误路径漂移：**干净**。逐行 diff 确认无 SQL 变更（所有 SQL 行都是上下文，
  不是 `-`/`+` 对）、无导出签名变更（新增的 `+func` 只有 `internal/dbrows`
  三个符号）、无返回值/顺序/nil-空切片语义变化。

### 3.4 我自己把回归测试写成了假绿（第三次同类错误）

给 D4 写回归测试时，我用了「文本窗口 + `strings.Index` 定位函数」这种
R66 刚批判过的做法。结果**变异验证时把 `defer rows.Close()` 删掉，测试照样绿**。

两个原因叠加：

1. 我为这件事写的说明注释里出现了 `defer rows.Close()` 这个**字面量**，
   文本判据**匹配到了自己的注释**；
2. 窗口按「到下一个 `\nfunc `」截断，把邻近函数的 `Close` 也算了进来。

改法：判据换成 **AST 定位 + 剥注释**。改完再变异，`defer count 2 → 1` 时两个
测试都正确报红，还原后恢复绿。

另一条测试也犯了**判据层级错误**：写「取了 rows 就必须 Close」，结果把
`PgxStore.Query`（把 rows 包成 `RowIterator` 交给调用方的**工厂**）报了出来。
判据收紧为「**迭代**了 rows 才必须 Close」。

**这一轮我在同一个错误上栽了第三次**（R66 是 `RangeStmt` vs `ForStmt`，本轮
第一次是断言窗口装不下 if 块、第二次是 Scan 边界，第三次是这个）。
这不是记性问题，是**方法问题**：写静态判据时应当默认「文本窗口不可信」，
先写 AST 版。

## 4. 本轮总结

| 项 | 结果 |
|---|---|
| R66 语义复审 | 4 处真缺陷（D1/D2 admin、D4 sessionforensics、D5 model_tier），全部已修 |
| 复审判为误报并保留 | 3 处（work_types L1、default_probe_picker、autoroute） |
| 回归测试 | 6 项（admin 4 + sessionforensics 2），**全部经变异验证红/绿** |
| 静态守卫 | rowsguard 0 违规 / 764 站点；dbrows、jsoncol 全绿 |
| 构建/vet | 全仓 exit 0 |

**本轮的方法论增量**：R66 的守卫只判「`rows.Err()` 在不在循环旁边」，
**结构上无法发现分型错误**——D1/D2/D5 全部顺利通过了守卫。D5 尤其说明：
同一个缺陷形状（整份配置的全量替换）在 `hotconfig` 被正确升级成硬失败，
在 `model_tier` 却只留了警告。**静态门能保证「都做了」，不能保证「做得对」。**

## 5. 保持开放

- `autoupdate.TestPgxStore_RecordUpdateReport`（生产 `releaseID=0` 回退与 FK
  冲突，两条互斥修法属产品裁决）、`bg` 两个 RealDB 夹具、
  `provider.TestGetProbeCandidates`、`admin.TestReportRollup_HTTPContract`
  ——四个红门均经 stash + 同一 DSN 对照证实**既存非本轮**。
- 6 个新上抛函数**无生产调用方**（`feature_stats_worker.GetLatestStats`、
  `credentialstate.RegisterNodesForCredential`、`moduleexec.BatchCheck`、
  `registry.GetUsageStats`/`GetTopTools`、`telemetry.GetAccessStats`、
  `settings.List`/`ListTenant`）——上抛是惰性的。其中 `settings` 的分型 5
  理由成立却无人调用，值得后续接线时留意。
- 22 处 `if dbrows.SkipOrFail(op, err) { continue }` 惯用法**依赖 helper 的
  bool 契约**：写成裸 `if dbrows.SkipOrFail(op, rows.Scan(...))` 时 continue
  在扫描失败上无条件触发（当前是对的）。R66 提交信息已登记「两处双层守卫
  形状易被改错」，本轮复核**未见活的反例**。
