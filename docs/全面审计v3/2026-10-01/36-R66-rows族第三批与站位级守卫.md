# 36 — R66 rows 族第三批：245 处迭代静默截断收口 + 站位级守卫（替代文件级 contains）

> 2026-10-01 02:53 起，基线 `541c766ba`（先 `git fetch` 并 fast-forward 合并 origin/main 的 761 回填超时修复）。
> 轮次命名沿用多会话共享 R 序（R65 之后）。本轮直接对应 35 号文档 §7 登记的第一条 backlog：
> 「rows 族剩余域（ops_overview/pricing/probe_dashboard/routing/providers/work_types/tool_policy 等 ~130 处）」。

## 0. 本轮的问题陈述

`pgx.Rows.Next()` 在迭代中途因连接断开/服务端错误而返回 false 时，**与正常读完完全无法区分**——除非显式调用 `rows.Err()`。叠加扫描期 `if err != nil { continue }` 的裸跳行，聚合读面会静默返回 200 与一个被截断的列表，而服务端不留任何痕迹。

R35-N1 / R65 已在 `admin` 建立了分类三门（`writeAggRowsErr` / `warnRowSkip` / `writeLookupErr`）并迁移两批（15 + 8 + 29 循环）。R66 把该纪律推到全仓剩余面。

### 0.1 先量后动手

用 AST + 括号配平双路扫描（非 `tail` 窗口，见 35 号教训 1）清点全仓 `for X.Next()` 站点：

| 分类 | 站点数 |
|---|---|
| 已有 `rows.Err()` 终检或留痕 | 519 |
| **待迁移** | **245**（133 文件） |
| 其中：**裸 `if err != nil { continue }`、零痕迹** | 6（最高价值） |

6 处零痕迹站点逐个回源确认全部是生产路径：

- `admin/tool_policy_api.go:148/338/433` — 租户工具策略列表端点
- `admin/work_types.go:525` — 工作类型列表端点
- `admin/swim_lane_init.go:126` — swim lane 统计
- `security/ipblocklist/store_pgx.go:194` — IP 黑名单扫描（`scanEntries` 被多处复用）

## 1. 共享 helper：`internal/dbrows`

R65 的三门是 `admin` 包私有的。推到 `bg`/`security`/`center`/`domains` 等包时没有可复用语义——各包各自发明 warn 形态，只会让同一种缺陷以不同面貌继续潜伏。故新建**与传输层无关**的共享包：

| 符号 | 语义 |
|---|---|
| `dbrows.Err(rows)` | 迭代终检；正常收敛返回 nil |
| `dbrows.WarnRowSkip(op, err)` | 单行 Scan 失败的 `slog.Warn` 留痕 |
| `dbrows.SkipOrFail(op, err) bool` | 「跳行 + 留痕」惯用形态 |

HTTP 状态码分类（42P01→503 / 其余→500）**刻意不放这里**——那是各包响应面的事，admin 继续用自己的 `writeAggRowsErr`。

## 2. 站位级守卫：`internal/rowsguard`（本轮的方法论增量）

### 2.1 为什么必须换判据

R65 的接线守卫是**文件级 contains**。R65 自己已记录该缺口：同一文件多个站点时删掉其中一处，守卫不会红——「那不是守卫失效，是守卫的粒度契约」。R65 的变异验证只证明了「删唯一引用位点会红」，而它挑的恰好都是单站点文件。

R66 把判据从「文件里有没有出现某个 helper 名」换成「**每一个 `for X.Next()` 循环**是否都有终检或留痕」，逐循环独立判定，删任意一处独立报红并给出 `file:line` + 所属函数。

### 2.2 豁免表必须逐条写理由

豁免的本质是「让某些站点不再受检」，因此 key 精确到 `file:line` 且**每条必须带理由**。行号漂移导致的失配方向是安全的：报的是「该行未被豁免且缺守卫」，真正需要豁免的站点会重新暴露出来，而不是被一条失效豁免长期假装有效。

14 条豁免全部是 one-shot CLI / 测试夹具 / 手动探针（`cmd/**` 的 main 包、`tests/**`、`scripts/injection-test`）——失败即进程退出，截断不会传播给客户端，其语义需单独裁决，本轮明确不处理。

### 2.3 我自己把守卫写成了「假绿」，被自检门当场抓住两次

这是本轮最重要的教训，单独记录：

**第一次**：`for rows.Next() {` 我断言成 `*ast.RangeStmt`。Go 里 `for rows.Next() {}` 是**条件循环**，即 `*ast.ForStmt`；`RangeStmt` 只对应 `for k := range x`。于是 `collectSites` 恒返回 **0 个站点**，`TestEveryRowsLoopIsGuarded` 恒绿且**不报任何错**——一个完全没有判别力的门，与一个健康的门看起来一模一样。

**第二次**：修完 AST 后，交叉复核抓到 go-redis 的 `for iter.Next(ctx)` 形态被判据漏掉（正则锚的是 `Next\(\)`）。

两次都是同一类错误：**判据锚错形状时，门对真实存在的对象是瞎的，而它自己不会报错**。与本仓既有教训同族（SQL 里 `''` 断言、注释里出现 helper 名、变异后门仍绿）。

### 2.3 守卫判据本身被自检门推翻两次（先文本窗口，后 AST）

第三次修正同样由自检门抓住，值得单列，因为它暴露的是**判据锚点选错层级**：

第一版判据是「循环行起 10 行文本窗口内是否出现 `<recv>.Err()`」。它对
`return devices, rows.Err()` 这种**惯用收尾**会漏判——`rows.Scan(...)`
多行时 `rows.Err()` 落在 10 行窗口之外，于是把已正确处理的站点误报成
违规（实测 334 条，其中约 89 条是这类假阳性）。

窗口宽度无论怎么调都是错的：短了漏报，长了把**下一个**循环的终检算给
当前循环。判据必须打在 **AST** 上。现行判据：对该 receiver，在循环体内
或循环之后（按 token 位置）是否存在 `<recv>.Err()` 调用点。

改到 AST 后从 334 降到 69，且 69 条逐条回源确认都是真站点。

**这个判据的边界必须写清楚**：它判的是「该 receiver 的迭代错误有没有被
消费」，**不判语义对错**——`if err := rows.Err(); err != nil { _ = err }`
也能通过。它防的是「完全没人看 `Err()`」这一类静默截断，这正是本轮要收
的缺陷；**语义分类（五种分型）由代码评审与变异验证承担，不由静态门承担**。
把能力边界写清楚，比让门看起来无所不能更重要。

### 2.4 守卫的判别力：变异验证

在 `admin/session_state_handlers.go`（多循环文件）中把唯一的
`rows.Err()` 消费点改成 `error(nil)`，守卫**独立报红**并精确指出
`admin/session_state_handlers.go:438 (enrichHealthData, recv=rows)`；还原后
恢复原状。这补上了 R65 记录的那个缺口——R65 的文件级 contains 守卫在
同一文件删一处不会红，本守卫会。

### 2.5 三条自检门

（见 2.3 前的 2.2 节末）——`rowsguard_selfcheck_test.go` 三条测试分别钉住
「站点数非空且 > 100 + 前 200 站点回源文本交叉复核」「正则双向判别力」
「条件循环是 `ForStmt` 而非 `RangeStmt`」。

## 2.6 顺手修掉的 P0：`main` 在本轮开工时**编译不过**

开工基线自检时 `go build ./...` 报：

```
admin/session_panorama_handler.go:179:12: undefined: rows
```

取证：这不是本轮引入。并发会话的合并 `317f28556`（合 `backup/other-session-s4-batch`）
在 `loadSessionDetailDataInTx` 里做了**冲突裁决错误**——diff 显示该函数
原先内联持有 `rows` 并自己迭代 timeline，另一侧把 timeline 抽成了
`loadSessionTimelineInTx`（其收尾是 `return timeline, rows.Err()`，已经正确
处理了迭代终检）。合并时**只保留了调用方的 `rows.Err()` 检查、丢掉了
它所属的循环**，留下一个引用不存在变量的孤儿语句。

修法：删除该孤儿检查，并补注释说明终检由 `loadSessionTimelineInTx` 承担
（`session_timeline_query.go:77`），防止下次合并再次分家；顺带移除因此
失效的 `fmt` import。

**附带一条方法论教训**：本轮第一版的基线自检写成
`go build ./... 2>&1 | head -20 && echo BUILD_OK`——**`head` 吃掉了
`go build` 的退出码**，`BUILD_OK` 无条件打印。这与 35 号教训 1
（「非 verbose + tail 窗口只显示 3 个 FAIL，是截断假象」）是同一个错误的
两个方向：**用管道截断输出时，判定必须取 `PIPESTATUS[0]` 而不是
`&&` 链尾**。本轮后续所有 build 判定都改用 `${PIPESTATUS[0]}`。

## 3. 迁移执行

分两轮六个子代理并行，文件所有权互斥。**第一轮四个子代理全部被网络故障
打断**（`ERR_CONNECTION_RESET` / `ERR_HTTP2_PING_FAILED`），但改动已落盘
85 文件 / +1297 行；据此重扫并派第二轮收尾。

| 组 | 范围 | 站点 | 结果 |
|---|---|---|---|
| 第一轮 A/B/C/D | admin 三域 + 非 admin | 245 | 落盘 85 文件后网络中断 |
| 第二轮 E | admin/routing + bg + 杂项 | 40 | 完成 |
| 第二轮 F | 存储层 / domains | 23 | 完成 |
| 主代理 | autoupdate + 守卫自身缺陷 | 2 | 完成 |

最终 **764 个 `for X.Next()` 站点全部有迭代终检或显式豁免，0 违规**。

### 3.1 分型结果（不搞一刀切）

| 分型 | 处置 |
|---|---|
| 1 HTTP 聚合 handler | `writeAggRowsErr(w, op, rows.Err())` + `warnRowSkip` |
| 2 存储/仓储读 | 上抛，`return out, rows.Err()` 或 `%w` 包裹 |
| 3 后台 worker / 探针 | `slog.Warn` 点名 worker 与被截断的批次，**不崩循环** |
| 4 best-effort 富化 / 降级 | 留痕后按已取得数据继续，**不升格 500** |
| 5 硬失败（选择类） | 上抛——截断会选错凭据/配置 |

**分型 5 是本轮风险最高的一类**：`bg/shared_pick.go`、
`bg/default_probe_picker.go`、`hotconfig/hotconfig.go` 的截断不会让端点报错，
而是**静默选中另一个凭据/配置**。`domains/moduleexec/admin.go` 的
`GetSessionSummary` 还有一个隐蔽点——终检必须放在**主导状态统计之前**，
否则截断会凭空造出一个「ok」主导状态。

### 3.2 子代理挖出的真生产 bug（已修）

**`admin/routing.go:4852` / `:4897`**：两处循环用
`//nolint:errcheck // best-effort` 包着裸 `rows.Scan(...)`，扫描失败**不
continue、直接把零值行 append 进结果**——状态端点会报出一个 label 为空的
幽灵凭据，且 `0` 被塞进 `staleIDs` 送进
`UPDATE credentials ... WHERE id = ANY($1)`。这比「静默截断」更糟：它
**污染输出**而不只是少数据。

**`bg/model_probe.go:1129` `applyPassiveBoosts`**：守卫清单外的裸
`if err := rows.Scan(...); err != nil { continue }`，零痕迹。

### 3.3 守卫自身被两个子代理独立指出两处缺陷（均已修）

两名子代理**各自独立**报告了同一组问题，这本身就是交叉验证：

1. **`dbrows.Err(rows)` 对守卫不可见**（假阴性方向的坑）。`errCallPositions`
   只认 `X.Err()` 选择器，而 `dbrows.Err(rows)` 的选择器是 `dbrows.Err`，
   于是**用官方共享 helper 正确处理的站点反而报红**——这会把后续作者推回
   「别用 helper，裸写 `rows.Err()`」。**门不该奖励绕过它的正确写法。**
   已扩展为同时认 `dbrows.Err(<recv>)`，并加自检钉住两种形态都认、
   `other.Err(rows)` 不被误记到 `rows` 头上。
2. **`skipTraces` 声明即死**：包注释声称「留痕即可」，实现只认终检。
   文档与实现不一致本身就是缺陷。已删除该变量并**改写包注释说明取舍**：
   留痕解决「单行坏数据」，终检解决「迭代中断」，**不是同一件事**；
   只有 `warnRowSkip` 没有 `rows.Err()` 的循环在连接断开时仍会静默截断。
   判据取更严的那个。

## 4. 验证矩阵

| 验证 | 命令 | 结果 |
|---|---|---|
| 全仓构建 | `go build ./...` | **exit 0** |
| 改动包 vet | `go vet` ×19 包 | **exit 0** |
| **站位级守卫** | `go test ./internal/rowsguard/ -count=1` | **7 测试全绿，0 违规 / 764 站点** |
| 共享 helper | `go test ./internal/dbrows/ -count=1` | **ok** |
| 守卫判别力（变异） | 抽掉多循环文件里唯一的 `rows.Err()` 消费点 | **独立报红**并指出 `file:line (FuncName, recv)`；还原恢复 |
| **admin 真库全量** | `go test ./admin/ -count=1`（DSN → 33.4 万行真实库） | **ok 80.2s，0 FAIL** |
| **admin race** | `go test -race ./admin/ -count=1` | **ok 93.2s，无 DATA RACE** |
| 其余改动包 | center/discovery/credentialstate/moduleexec/sessionforensics/hotconfig/collector/handlers/registry/settings/telemetry 等 | 全部 ok |
| gofmt | 本轮改动文件 | 干净 |

**判定基线纪律**：本轮所有 build/测试判定取 `${PIPESTATUS[0]}`，**不用
`&&` 链尾**（见 §2.6 教训）。

## 5. 保持开放（登记不修）

**三个红门全部经 stash 对照证实为既存、非本轮引入**（判据：把本轮在该
目录的改动 stash 掉后重跑，失败完全一致）：

| 红门 | 根因 | 性质 |
|---|---|---|
| `autoupdate.TestPgxStore_RecordUpdateReport` | 生产 `releaseID=0` 回退（注释称「允许主控端先上报」）与 `instance_release_status_release_id_fkey` 冲突 → `23503`，随后测试 nil 解引用 panic | 产品语义裁决：放宽 FK vs. 生产侧拒绝未知版本上报，**两条互斥** |
| `bg.TestLedgerReconciler_RunOnce_RealDB`、`bg.TestMigration762ProjectBackfillChain_RealDB` | 既存夹具/迁移状态漂移 | 待专项 |
| `provider.TestGetProbeCandidates` | 夹具写 `providers.name`，该列已不存在 → `42703` | 夹具漂移（同 35 号记录的「601 已删列」族） |

**取证实录**：首轮我曾把 `autoupdate` 判为「HEAD 通过」——那次**忘导出
DSN，测试根本没跑**。这与 §2.6 的退出码假绿是同一族错误的第三个变体：
**「门没报」可能是「门没跑」，不一定是「门通过」**。修正后的取证一律
「stash 改动 + 同一 DSN + 重跑」。

其余登记不修项：

- **`center/store_pgx.go` / `vibecoding/store_pgx.go` 的 `_ = json.Unmarshal(...)`**
  （子代理报告）：命令 args/result 的 jsonb 反序列化失败被静默吞成空值。
  与静默截断同族的**静默数据丢失**，但属反序列化面，不在本轮范围。
- **跳过惯用法 `if dbrows.SkipOrFail(...) { continue }` 嵌在 `if err != nil` 里**
  （`bg/credential_cycler.go`、`bg/model_tier.go`，既存）：行为正确
  （`SkipOrFail` 在 `err != nil` 时恒返回 true），但双层守卫读起来像
  「continue 是有条件的」，是未来被改错的形状。错误路径以外，本轮不动。
- **`admin/acc_projects.go` / `admin/providers.go` / `admin/user_profile.go`
  gofmt 不干净**：stash 对照确认**在 HEAD 即已如此**（struct 标签对齐），
  非本轮引入。本轮改动文件全部 gofmt 干净。
- **one-shot CLI / 夹具**（`cmd/**` 13 处、`tests/**` 2 处、`scripts/` 1 处）：
  列入守卫豁免表并逐条写明理由。失败即进程退出，截断不传播给客户端，
  语义需单独裁决。
- 真实供应商 / 浏览器 / 部署冒烟未做——本轮改动全部在错误路径，
  不改查询文本与返回值形状。

## 6. 本轮教训汇总

1. **静态守卫必须自带「我看见了东西」的证明**。判据锚错形状时，门不报错
   而是变成空集合断言——**恒绿且看起来完全健康**。本轮因此加了 3 条自检门。
2. **判据的边界比判据本身更容易出错**。`loop.Body.End()+100000` 让「循环
   之后」退化成「本文件任意更后位置」，制造了跨函数背书的假阴性。
3. **门不该奖励绕过它的正确写法**。守卫不认 `dbrows.Err(rows)` 时，用官方
   helper 正确处理的站点反而报红——这在教后来者不要用 helper。
4. **文档与实现不一致本身就是缺陷**。`skipTraces` 声明即死、包注释描述了
   一套并不存在的判据。取舍必须写进注释，而不是留在实现里。
5. **管道截断输出时，判定取 `${PIPESTATUS[0]}`**，不用 `&&` 链尾——
   `head`/`tail` 会吃掉上游退出码。
6. **「门没报」可能是「门没跑」**。忘导出 DSN 让一次 stash 对照得出
   「HEAD 通过」的反向错误结论。取证三件套：同一 DSN、stash 改动、重跑。
7. **子代理的独立交叉验证本身就是证据**。两名子代理在互不知情的情况下
   各自报告了同一组守卫缺陷，比我自查更快地暴露了问题。
