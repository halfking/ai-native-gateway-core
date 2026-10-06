# request_logs 退役与 session_* 迁移 —— 审计门索引（SSOT）

> ⚠ **这份索引的唯一职责**：把本审计落地的每一道门**逐个点名登记**，并写清它归哪一轮、
> 归属哪份文档。门 `TestAuditGatesAreRegisteredInTheirDocs` 从**源码树正则发现**门名，
> 要求每一道都在本索引里出现 —— 所以「新门只进代码、不进报告」会立刻变红。

## 为什么需要它（一次真实的漏登记）

`TestBodiesSwitchPairIsPinnedAndPaired`（§9.265 补 P1 成对开关那道）写完代码、
推上 `origin/main`、在 commit message 里写明了自己的存在 ——
但 §9.265 的文档第五节仍写「门（4 道，常驻）」、表格只有 4 行。
**没有任何东西抓住它**：代码里有、提交说明里有、文档里没有。
下一轮现核「门名清单 vs 文档声明」时数门才发现：源码树里 16 道，那一节声称 4 道。

⇒ 硬约束写的是「新增的类/集合必须同时进报告和门」。**反向那一半**——
「已落地的门不能只进代码不进报告」——当时没人管，现在由本索引 + 那道门接管。

## 门清单（17 道）

| # | 门 | 定义文件 | 归属 | 归属文档 |
|---|---|---|---|---|
| 1 | `TestAuditGatesAreRegisteredInTheirDocs` | `admin/audit_gates_registered_in_docs_test.go` | 跨轮次 | **本文件** |
| 2 | `TestV1ReaderTieringDomainIsBothPopulations` | `admin/request_logs_retirement_tiering_test.go` | §9.262 | `2026-10-05-v1-reader-retirement-tiering.md` |
| 3 | `TestV1ReaderTieringEveryFileProvenToReadV1` | 同上 | §9.262 | 同上 |
| 4 | `TestV1ReaderTiersAssertMembers` | 同上 | §9.262 | 同上 |
| 5 | `TestV1ArmViewSetMatchesRuntimeTruth` | 同上 | §9.262 | 同上 |
| 6 | `TestV1ArmLiteralBlindSpotIsPinnedAndTiered` | 同上 | §9.262 | 同上 |
| 7 | `TestBodiesReadPathClassifierSeparatesTheTwoHelpers` | `admin/v1_bodies_read_path_test.go` | §9.265 | `2026-10-06-v1-bodies-read-path.md` |
| 8 | `TestV1BodiesReadPathsArePinned` | 同上 | §9.265 | 同上 |
| 9 | `TestBodiesReadPathAgreesWithTheShapeRegistry` | 同上 | §9.265 | 同上 |
| 10 | `TestBodiesSwitchPairIsPinnedAndPaired` | 同上 | §9.265 | 同上 |
| 11 | `TestBodiesNativeReadKeyInRealDB` | `admin/v1_bodies_read_path_realdb_test.go` | §9.265 | 同上 |
| 12 | `TestS4DriftScopeReadsBothStorageFaces` | `cmd/gateway/dual_read_both_faces_test.go` | §9.263 | `2026-10-06-v1-stopwrite-precondition.md` |
| 13 | `TestS4WindowFloorIsWeakerThanTheDocumentedExitCondition` | 同上 | §9.263 | 同上 |
| 14 | `TestV1WriteLivenessSQLReadsBothFaces` | `cmd/gateway/v1_write_liveness_test.go` | §9.264 | `2026-10-06-v1-write-liveness-signal.md` |
| 15 | `TestClassifyV1WriteLiveness` | 同上 | §9.264 | 同上 |
| 16 | `TestClassifyV1WriteLivenessIsNotDrivenByTheSwitch` | 同上 | §9.264 | 同上 |
| 17 | `TestV1WriteLiveness_RealDB` | `cmd/gateway/v1_write_liveness_realdb_test.go` | §9.264 | 同上 |

各归属文档里的「门（N 道）」小计（**不含**第 1 道，它是跨轮次的）：
§9.262 = 5 · §9.265 = 5 · §9.263 = 2 · §9.264 = 4 ⇒ 合计 **16**，加跨轮次 1 = **17**。

## 怎么跑

全部是 `-count=1` 的常驻门，随 `./admin/` 与 `./cmd/gateway/` 走。真库门
（`TestBodiesNativeReadKeyInRealDB` / `TestV1WriteLiveness_RealDB`）需要**同时**导出
`TEST_DATABASE_URL` 与 `TEST_PG_DSN`。

`TestV1BodiesReadersAreAssessed` 是**故意红**的基线门（27 个 bodies 读方未逐点评估），
**不在本索引内**（不是我新增的），也不追求它变绿。

## 变更规则

- **加一道门** ⇒ 必须同时在本文件加一行（门名 + 定义文件 + 归属 + 归属文档），
  并在**归属文档**的「门（N 道）」小节里加对应行。不加 ⇒ 第 1 道门变红。
- **删一道门** ⇒ 本文件与归属文档同时删，第 1 道门的总数断言（17）变红。
- 门名从**源码树**发现，所以本文件不需要（也不应该）另维护一份手写清单。