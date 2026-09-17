# R40 审计/专项轮 —— RLS Phase 1 收口 + durable 族 GUC/收敛 + R39 §三遗留

- 日期：2026-09-18
- 轮次性质：用户指定专项轮（R39 轮文档 §五 建议入口：RLS Phase 1 剩余 + R39 §三遗留处置）
- 执行方式：主代理直接执行（未派只读子代理——本窗口改动面集中、交接文档已划定优先级，惯例同 R38 专项轮的收敛形态）
- 基线：main @ 78b3acdef → 开局 fetch 发现并行会话已推 3 提交（f5328e13c 迁移 720 / 422b04c2d merge / b484efbac net import 修复），ff 至 **b484efbac** 后开工；本轮窗口有效增量为并行 720 单点提交 + 本轮全部改动
- 收尾 commit：见 git log（本轮单 commit）

## §一、发现与处置

| 级别 | 发现 | 处置 | 证据 |
|------|------|------|------|
| **P1** | 并行会话 f5328e13c 的**迁移 720 只落 embeddata 单点**：canonical `sql/migrations/startup/`、`runner.go StartupFiles`、`main.go go:embed+embeddedSQLFiles`、`apply-db-revision-sequence.sh` 登记全缺——`TestStartupFilesAreAllEmbedded` 在 main 上红（开胎实跑取证：`embeddata/startup file "720_..." is not registered`） | 五点同步补齐：720/722/723 canonical 副本（逐字节 cmp）+ runner.go + main.go embed var/map + stats_migrations_test.go parity map + revision-sequence.sh 登记；补齐后 installer 双包测试全绿 | 开局 go test FAIL → 补齐后 ok；§四 |
| **P1** | **516 中间形态漂移**（新发现）：本机真库 `durable_llm_tasks` 缺 `checkpoint_payload` 列、`durable_llm_task_events`/`durable_pending_outbox` 两表缺失——2026-08-15 19:14 建库时刻的 516 是从未入 git 的脏工作区中间版（列集 `commit_metadata/connection_attached/last_disconnect_at` 与最终版无交集），516 随后以最终形态（2d0037507/ca16dedb0 血统）提交。存量库**三无**：516 marker 锁死（sequence 不可重放）、ensure 链无 durable 条目（db/db.go 零命中）、代码直引缺失表/列（appendEvent/enqueuePendingOutbox/CheckpointCommitState）→ 首次 durable 事件写入即 42P01/42703 运行时失败 | **迁移 722_durable_family_schema_convergence**：把 516 最终形态的幂等体（两表+索引+RLS ENABLE+policy、checkpoint_payload 列）重放，全语句 IF NOT EXISTS/DROP POLICY IF EXISTS/ADD COLUMN IF NOT EXISTS——全新安装零变化，漂移库一次收敛；五点同步全注册；本机真库通道实跑落地（marker 登记，两表/列/policy 全就位）；中间形态的 3 个历史列保留为惰性数据（清理属运维决策） | §四 psql 输出；根因链见 722 文件头 |
| P2 | RLS Phase 1 长期空转：720（并行会话产出未接线）、723 ENABLE、durable GUC、census 守卫四项全悬 | 本轮全部落地（详见 §二） | §四 |
| **P2** | **迁移编号撞号**：收尾 fetch 发现并行会话 507d78cff 已推 **721**（凭据余额元数据）——本轮开局 fetch 时 721 空闲，工作期间被占用 | 本轮 ENABLE 迁移 721 → **723** 全量重编（文件名×4 + runner.go/main.go/stats test/sequence script + 文档引用）；教训：编号核对必须在**提交前**重复而非仅开局一次 | git fetch 输出 + grep 残留清零 |
| P3 | R39 §三#2 404 连续性口径偏差 | 择"修文档"：`modelNotServedRecheckInterval` 注释钉死 episode 语义 + `TestUnavailableBindingHorizon` 补 interleave 用例（404@attempt=3 仍升级） | bg/node_probe.go |
| P3 | R39 §三#3 endpoint_build 粒度混流 | 新识别器 `isDecryptShapedProbeDetail`（`decrypt: %w` 包装前缀）+ `credentialSpecificDecryptFailure`（解密形 ∧ 熔断计数 <5）；updateBindingAvailability 签名 +第 7 参 errDetail，sync/queue/probe_service 三路抑制条件携带豁免；源钉桩 ×2 同步更新 + 新行为测试 | bg/node_probe_credential_decrypt_test.go |

## §二、RLS Phase 1 四项落地明细（设计 §五，"真库实跑后定稿"纪律全程践行）

1. **迁移 720 policy 词汇统一**（并行会话产出，本轮接线+实跑）：59 条 policy 重写为标准形（app.tenant_id→get_current_tenant() 57 条 + app.is_super_admin 残留 2 条→旁路分支）；通道重放幂等（并行会话曾直跑 psql 未登记 marker，本轮通道正式跑：DROP/CREATE POLICY 重放 + marker `...:720_...` 登记）；§六 验收——**public 弃用 GUC policy 数 = 0**（跑前跑后双 census，总 199 条不变）。注：非 public schema 的 106 条 app.tenant_id 引用属共享本机 PG 实例的其它项目，不在网关 scope。
2. **迁移 723 ENABLE attachments/candidate_failure_logs_columnar_old**：两表 policy 已在且词汇标准形但 relrowsecurity=false 沦为摆设；ENABLE（不 FORCE）superuser 时期零行为变化；真库实跑后 `rls=true force=false` ×2 + marker 登记。
3. **durable 族 store GUC**（durable/rls.go）：前台写 CreateAndClaim `SET LOCAL app.current_tenant`；worker 族 17 条路径（ClaimRunnable/Reschedule/RenewLease/CheckpointCommitState/CommitTerminal/ReapDeadlines/ReapUnsafeCheckpointed/PersistSettlementIntent/claimSettlementIntents/RetrySettlementIntent/FinalizeSettlement/ProjectPendingOutbox/LoadSnapshot/ActiveTaskCounts/Load+SaveDecisionHistory）`super_admin+bypass_rls` 双 GUC——**关键坑：autocommit 单语句的 is_local GUC 即设即收，必须包显式事务**（execWithBypassTx/queryWithBypassTx），否则降权后假性 ErrLeaseLost/ErrNoRows。pgxmock 用例 ×32 同步补 Begin/GUC/Commit 期望。D4 RESET 结论适用（全程事务内 is_local，无需配对 RESET）。
4. **census 守卫**（db/rls_policy_census_test.go）三驾：startup 迁移静态扫（CREATE POLICY 窗口提取，剥整行注释防 720 头注释误报，`.down.sql` 豁免——回滚本就要恢复旧词汇）+ Go ensure 静态扫（db.go/db_omnifree.go）+ 活库 census（TEST_DB_URL opt-in，本机实跑 PASS 0.1s）。

## §三、核实为健康的关键面（本轮留档，下轮不再重复怀疑）

- 并行会话 b484efbac（net import "重复修复"）实为对其旧基线的修复，与 R39 P0 修复合并后 main.go 无实质差异——diff 亲核仅 720+docs+version。
- 720 迁移体行为中性论证成立：重放前后 public 总 policy 数 199 不变；analysis_events 出现新旧两条 bypass policy 共存（`analysis_events_super_admin_bypass` 预存 + `super_admin_analysis_events` 720 新建），PERMISSIVE OR 语义下冗余无害。
- 本机 PG 崩溃为环境事件非本轮 SQL 所致：通道首次运行重放历史积压未登记迁移（重 WAL），容器内 postmaster 崩溃自恢复（216MB WAL checkpoint 重放）；**通道可续跑设计经受住考验**——恢复后重跑 69 skip + 1 apply 干净收口。容器无内存限制、宿主 15.6GiB 余量充足。
- get_current_tenant() 函数体读 `app.current_tenant`（001 定义与活库一致）——census 守卫无误报面。

## §四、测试与验证（实跑输出）

```
git fetch 开局        # 78b3acdef..b484efbac 并行 3 提交，ff-only 拉取
go build ./...                                        # OK
go vet ./bg/ ./db/ ./durable/                         # OK
go test ./bg/ -count=1                                # ok 7.7s（含 R40 新钉 ×4）
go test ./durable/ -count=1                           # ok 1.9s（32 用例期望更新后）
go test ./db/ -count=1                                # ok 1.5s
go test ./config/ ./autoroute/ -count=1               # ok
(cd installer && go test ./cmd/llm-gw-installer/ ./internal/dbinit/)   # ok ×2
bash scripts/apply-db-revision-sequence_test.sh       # contract passed
gofmt -l 本轮触碰文件                                  # 清零
TestRLSPolicyVocabularyLiveCensus（TEST_DB_URL=本机）  # PASS 0.1s

真库实跑（llm_gateway@127.0.0.1:5432）：
  apply-db-revision-sequence.sh  # 720: 重放+marker；723: ALTER TABLE×2 → rls=true force=false ×2 + marker
                                 # （PG 崩溃自恢复后）续跑：69 already applied + 722 applied → completed successfully
  census 跑前/跑后               # public 弃用 app.tenant_id=0（is_super_admin=0）、total=199
  durable 收敛验证               # durable_llm_task_events/durable_pending_outbox to_regclass 非空、
                                 # checkpoint_payload 列=1、三表 rls=true + policy 2/2/2
  durable 写路径冒烟             # BEGIN INSERT events → SELECT 1 行 → ROLLBACK
```

## §五、遗留登记

1. **P2 | 720/722/723 的 db-changelog.md 条目**：按惯例随下次 245/252 deploy 落（deploy 时采集 SHA-256 + applied+verified）。
2. **P2 | 719 ensure 复活扫描**：本机 8782 容器仍跑旧二进制，下次 deploy-local 后需重跑复活扫描（R39 遗留顺延）。
3. **P2 | RLS Phase 2 前置**：73 张 FORCE 表逐表路径审计清单（四路径 GUC 状态）——本轮 durable 族路径已具证明材料（durable/rls.go + 722），其余表待专项轮；D5（sessions owner_filter 产品语义）仍未决。
4. **P2 | 252 运维窗口**（R38 §五 顺延，未经 ops 确认不动）：717 假登记修复（加固版直跑+十列复核）、719/713 随部署低峰。
5. **P3 | 环境债**：本机 PG 容器崩溃自恢复一次（通道重放重 WAL 期间）；建议给 llm-gateway-pg 设内存上限前的压测基线。生产/245 是否存在同款 516 中间形态漂移未知——722 已随通道可自愈，无需人工介入。
6. **P3 | R39 §三#4-#8** 顺延（db-changelog override 静默回退、settle session_summaries 口径、RowsAffected 债、mDNS 广播面、discovery -tags integration 门控——均接受为低风险债）。

## §六、下一轮提示词（建议）

> 以本文 §五 + R39 §三 为起点：首位 **RLS Phase 2 前置**——73 张 FORCE 表逐表路径审计清单（每表一行：admin 读/网关写/worker/回填 四路径 GUC 状态；durable 族以 durable/rls.go 为范式），输出全绿后按设计 §五 Phase 2 执行降权演练（先非生产环境）；其次 **252 运维窗口**（717 加固版直跑+十列复核、719/713 随部署低峰，须 ops 确认）；再次 R39 §三#5 settle session_summaries 口径边界（连写侧一起改+两个集成测试期望）。审计入口 docs/audit/playbook/orchestrator-prompt.md（下一轮 R41）。
