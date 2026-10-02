# R76 —— handoff 确认链路：真库抓到的 P0 与 pending 提案语义定案

日期：2026-10-01
范围：`domains/hooks/handoff/confirmation_pg.go`（PGStore 记账路径）、`domains/streaming/handoff_confirmation.go`（HTTP 契约）
触发：R74 遗留的显式阻塞——「`confirmation_pg.go` 的 SQL 只做静态阅读 + fake driver 单测；本机 PG 未运行，故 D3 类 DB 语义问题**未实证**（这也是我撤回该修法的直接原因之一）」。

**该阻塞已在 R75 被我自己推翻**（本机 PG 一直可用，见 45 号报告 §7.2）。本轮把 R74 没能实证的那部分补上，结论比预期严重得多。

| # | 缺陷 | 严重度 | 状态 |
|---|---|---|---|
| 1 | `Confirm` 的记账 UPDATE 把同一个 `$3` 绑给 `timestamp` 与 `timestamptz` 两列 ⇒ 服务端 42P08 ⇒ **确认功能 100% 失败**，且对外伪装成 404 | **P0** | ✅ 已修 + 真库门 |
| 2 | pending 提案「后一份就地替换前一份」= 有意不变量，但**无任何门钉住**（既有测试全是 sqlmock） | P2 | ✅ 已补真库门 |
| 3 | D3 在真库上证伪：事实成立，但 R74 撤回其「bug」定级是对的 | — | 定案 |

---

## 1. 【P0】handoff 确认的记账路径从未成功执行过一次

**位置**：`domains/hooks/handoff/confirmation_pg.go:133`

**事实**（本机 PostgreSQL 17，`llm_gateway` 库，裸 `PREPARE` 复现）：

```
ERROR:  inconsistent types deduced for parameter $3
DETAIL:  timestamp without time zone versus timestamp with time zone
```

两列的类型在**全部五处 schema 声明里彼此一致且确实不同**：

| 列 | 类型 | 声明处 |
|---|---|---|
| `session_summaries.last_handoff_at` | `timestamp without time zone` | 655 / 677 / `sql/objects/tables/` / `01-schema.sql` / deploy baseline |
| `session_summaries.last_trigger_at` | `timestamp with time zone` | 同上 |

而该 UPDATE 把同一个 `$3`（Go 的 `now`）同时绑给两列。**PostgreSQL 每个参数号只能推导出一种类型**，故服务端直接拒绝。改成独立参数号后 `PREPARE` 正常。

### 1.1 后果链

1. `Confirm` 的**每一次**调用都在这一步失败（不是罕见路径，是必达语句）。
2. 失败在事务内 ⇒ `defer tx.Rollback()` ⇒ **它前面已经 INSERT 成功的 `handoff_logs_hot` 一并回滚**，什么都不留。
3. 裸 PG 错误落到 `handoffConfirmationError` 的 `default` 分支 ⇒ 客户端拿到
   **HTTP 404 `handoff_confirmation_invalid`**——与「提案根本不存在」**完全无法区分**，
   且带同一个 `Idempotency-Key` 重试仍然 404，直到提案 5 分钟 TTL 过期。
4. ⇒ **handoff 确认功能在本 schema 下从未成功执行过一次**。objective 要求的
   「1M 会话自动切换到新会话」这条链，确认环节是全断的。

### 1.2 为什么长期没被发现（三重掩护）

- **本包的 PGStore 测试全部走 sqlmock**（`confirmation_pg_test.go`、`confirmation_g2_acceptance_test.go`）：只断言「发出了这段 SQL 字符串」，**看不见服务端的参数类型推导**。参数个数变了它们会红（实测确实红了），但 42P08 是运行期语义，字符串断言原理上抓不到。
- **唯一的真库门 `migration_527_testcontainers_integration_test.go` 只验 DDL**，不跑任何 Go 语句——它验的是「527 迁移后列类型对不对」，而列类型按 schema 声明本来就是对的，错的是 Go 里的 SQL。
- **`handoff.enabled` 默认 `false`**（R74 已核实），生产上根本没人走到这里。

### 1.3 修法：只改 SQL，刻意不动表结构

给 `last_trigger_at` 独立参数号 `$7`。**为什么不改 schema**：

- 同库的**另一个写方本来就是对的**——`domains/sessionsummary/summarizer.go:949` 的
  `UpdateHandoffMetrics` 用的是**分开的 `$3` / `$7`**。同一张表、同一组列，
  那边能跑通，说明 schema 可用，错的是这一行 SQL 抄错了形态。
- `last_handoff_at` 在本地 **33.4 万行里全为 NULL**（实测），没有需要解释的历史
  时区数据；`ALTER TABLE ... TYPE` 会重写整表，代价与风险都不必要。

> 顺带记录一个**本轮自己制造又自己撤回的假警报**：`\d handoff_pending_confirmations`
> 输出被截断在 40 行，我没看到 `uq_handoff_pending_active_session`，一度判「
> `ON CONFLICT` 目标索引不存在 ⇒ `SavePending` 每次报错」。查迁移 517 与
> `pg_indexes` 后确认索引**存在**，`\d` 只是截断。**先核对迁移文件再下结论**，
> 是这条纪律又一次生效。

---

## 2. D3 定案：事实成立，但「是 bug」的定级仍应撤回

R74-C 报「同会话二次提案作废第一次凭据」定 P1，我以「token 轮换是刻意的安全属性」
为由撤回了它的**定级**。本轮在真库上把事实层面钉死：

```
第一次提案后： id=1111…  token_hash=hash-of-token-1
第二次提案后： id=2222…  token_hash=hash-of-token-2     ← 就地替换
旧 id 还能被 Confirm 的 SELECT 命中吗？ old_id_still_present = 0
              ⇒ Confirm 走 sql.ErrNoRows ⇒ ErrConfirmationInvalid
```

**事实完全成立。** 但它**不是缺陷**：

- 内存 store 的 `SavePending` 做同样的事（`confirmation.go` 的 `delete(s.byID, prior)`），
  两个 store 行为一致 ⇒ 「每会话一份 pending」是**跨实现的有意不变量**，不是 PG 侧跑偏。
- 迁移 517 显式建了 `uq_handoff_pending_active_session ... WHERE status='pending'`
  这条部分唯一索引来**强制**这个不变量，518 的注释还专门记述了它。

**但撤回的是「这是 bug」，不是「它不需要被钉住」。** 见下节。

### 2.1 真正的缺口：这道不变量没有任何门守着

本包既有测试能守住它的**内存侧**（`TestConfirmationProposalStoresOnlyHashAndRotates` 之类），
**PG 侧却完全没有**：sqlmock 看不见部分唯一索引、`ON CONFLICT` 谓词匹配、以及
「覆盖后旧 id 消失」这个结果。任何人将来把 `ON CONFLICT` 子句删掉、或改成
多提案共存，生产上要等到「第一个客户端的确认莫名失败」才会被发现——
而那个症状恰好被 §1.1 的 404 伪装成「提案无效」，**排查会被带偏**。

**已补真库门** `confirmation_pg_semantics_test.go`：跑**真的 `PGStore` Go 代码**
（不是 SQL 副本），三个子测试分别钉住「第二份就地替换第一份」「旧提案确认失败
（客户端可观察后果）」「现行提案确认成功且确认后唯一索引让位」。

### 2.2 D4 的触发面比 R74-C 假设的窄（重新定级依据）

R74 记「`CommitRequest` 无调用方 ⇒ 同一超限会话每个请求都重新提案」。读
`request_hook.go` 后确认触发面窄得多：

- `trigger_mode=auto` 且非手动调用时，**必须 `req.Explicit` 为真**（逐请求
  `X-Gw-Handoff-Mode: explicit` 或租户 `client_mode=explicit`）才继续；
- `trigger_mode=manual` 时**必须客户端真的发了技能调用**。

即「每个请求都重新提案」需要客户端在**每一个**超限轮次都显式 opt-in，或每轮都发
`/handoff`——不是普通聊天循环的形态。**D4 维持 P2**，但理由从「普遍现象」收窄为
「显式 opt-in 客户端下的自竞争」。是否把冷却前移到 prepare 阶段，仍是产品裁决
（见 §4），本轮不动。

---

## 3. 验证

```
go build ./...                                   exit 0
go test ./domains/hooks/handoff/                 ok
gofmt -l <本轮改动 3 个文件>                       空
真库门（LLM_GATEWAY_HANDOFF_PG_DSN 指向本机 PG 17）：
  go test ./domains/hooks/handoff/ -run TestPGStoreSavePendingSemantics -v   3/3 PASS
```

**变异验证 1 处**：`last_trigger_at` 退回共用 `$3`（并去掉第 7 实参）⇒
真库门第三个子测试红，报的正是原始错误 `inconsistent types deduced for parameter $3`。
判别力来源是**真库**，不是字符串断言——这正是这道门存在的理由。

**两处 sqlmock 跟随更新**：`confirmation_pg_test.go` 与
`confirmation_g2_acceptance_test.go` 的 `WithArgs` 补上第 7 个实参。
按纪律「门第一次红先假设门是对的」：这两个门红的原因是**实参个数**，
其断言意图（记账 UPDATE 以正确的值发生）本身是对的，属实现需跟随修复，
已在断言旁写明为什么多一个参数。

---

## 4. 待裁决（本轮新增 1 条，连前 7 条共 8 条）

- **【新】是否把 handoff 的冷却/频次前移到 prepare 阶段**（D4）。前移后同一超限
  会话在一段窗口内只提一份提案，客户端不再自竞争；代价是窗口内再次手动触发
  拿不到新提案。改默认值会改变客户端观感，未擅自决定。

---

## 5. 能力边界

- **未在 154/252 生产验证**。结论来自本机 PostgreSQL 17（schema 与生产同源，
  取自仓库迁移）+ 真库门跑真实 Go 代码。**生产 session_summaries 的
  `last_handoff_at` 实际分布未查**——若生产已有非空值且写入方时区口径与本地不同，
  §1.3「不动表结构」的判断需要重新评估（该判断的依据是本地 33.4 万行全 NULL）。
- **真库门在 CI 上恒跳过**：以 `LLM_GATEWAY_HANDOFF_PG_DSN` 门控，
  `verify.sh` 只跑 `go test ./... -timeout=300s`、不提供该 DSN。
  本轮这道 P0 门**在 CI 上不会执行**——这是本轮最实质的覆盖缺口，如实登记。
- **清理方式**：本门在真实表上播种（`PGStore` 的方法不接受 tx，无法靠 ROLLBACK
  兜底），用独立 tenant 前缀 + 按前缀 DELETE，并在 cleanup 里**断言归零**。
- **`domains/hooks/handoff` 另有 3 个文件未过 gofmt**（`confirmation.go`、
  `metrics.go`、`confirmation_g2_acceptance_test.go`），**改动前即如此**，
  非本轮引入；本轮不顺手格式化（会产生与审计无关的 diff）。
- **R71–R75 域未触碰**，不评价其结论。
