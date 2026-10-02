# 收口轮（2026-10-01）——fresh-install e2e 轮三项待拍板执行 + sequence 通道断链修复 + 757 security_invoker 根修

> 关联：docs/audit/2026-10-01-fresh-install-e2e-round.md（§五三项待拍板出处）、
> docs/audit/2026-10-01-802-form-adjudication.md（heal 配方出处）。
> 起点：origin/main ≥ de9870c10。主工作区接手（§5.9 并行会话成果已合入并独立复核通过）。

## 一、结论

1. **§5.9 并行会话成果独立复核通过**：合并后 HEAD 上 build/vet 绿、admin 会话族单测绿；
   真库门 `TestSessionSummaryV2FallbackServesRealV1Turns` PASS（24.6s）、
   `TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` 五子测试全 PASS
   （848s，limit=nil 169 轮对比：tuple 键 1/169 vs request_id 单键 169/169）。
   首跑该门 FAIL(826s) 经重跑证伪——红因是本会话 heal DDL（DROP INDEX 取
   ACCESS EXCLUSIVE）与门并发抢锁的环境性干扰，非代码缺陷（自审 §六.1）。
2. **待拍板①执行并纠偏**：本机跑 `apply-db-revision-sequence.sh` 落账。fe8 轮
   §五.1 的处方（"脚本幂等回填 718-806"）**两处与事实不符**：
   - 718/719/726/727 在 sequence 台账**早有 marker**（09-17~09-21，指纹 NULL
     遗留记账）——fe8 轮以 schema_migrations 无行为"从未应用"证据是**查错账本**
     （startup SQL 走脚本通道不写 schema_migrations，两账本本就错位）；
   - 且 NULL 指纹 marker 不在 `legacy_content_replays` 清单即被跳过，重跑脚本
     **不会**重放它们。实际执行动作 = 644 重放（登记项）+ 757 内容重放（本轮
     文件变更触发）+ 803-806 首应用（本轮清单注册解锁）。二跑 116 项全 skip 幂等。
3. **本机 4 个无效父索引全部闭合**（hand-heal，配方见 §三.3，机制先 scratch 实证）：
   `session_bodies_request_id_partition_date_key` /
   `session_bodies_session_id_turn_no_partition_date_key`（约束背载，DROP
   CONSTRAINT+ADD CONSTRAINT 递归重建，6/6 叶 valid+attached）；
   `idx_request_logs_discard_events_ts`（DROP+递归重建，6/6）；
   `idx_request_logs_ts_desc`（按 718 §A 正典**移除**——冗余方向影蔽索引）。
   public schema 现存**零**无效父索引。
4. **待拍板②security_invoker 收口——真凶改判 + 无需新迁移号**：
   - fe8 轮候选"807"已被并行会话十七轮占用（`807_request_logs_bodies_hot_drop_
     duplicate_request_id_index.sql`，220a0a…）；且血统复核发现 **713:43 实带
     `WITH (security_invoker = true)`，丢失真凶是 757**（重建视图时未带，
     757 在链上晚于 713）——fe8 轮文档归因 713 有误。
   - 根修 = 就地修 757 up/down + 尾部 reloptions 守卫（照抄 526 形态）+
     embeddata 副本同步 + 台账 SHA 更新；757 台账指纹非空 → 全部存量环境
     下次脚本运行**自动内容重放**收敛（本机已实证重放成功，live reloptions
     现 = `{security_invoker=true}`）。不需要、也不应该再开 808 号迁移。
   - 安全性：session_turns/_hot 均 relforcerowsecurity=false，owner（网关角色）
     读路径行为不变；加固只影响非 owner 读角色（视图不再吞 RLS）。
5. **待拍板③部署链断链修复**：803-806 此前**只进 installer StartupFiles，从未
   进脚本清单**——"随下次部署走 sequence 通道到 154/245/252"在当时不成立。
   已按 802 条目同款登记进 manifest（含位置约束注释），契约测试
   `apply-db-revision-sequence_test.sh` 两轮 PASS。本机实跑验证四件均为存量库
   no-op：803/804 ADD COLUMN IF NOT EXISTS 全 skip、805 索引/policy 与改前
   逐字一致、806 循环空集（session_bodies 6 分区全 heap）。
   **首署抽验台账 SHA 登记留给部署窗口**（154/245/252 各自首跑时核对 803-806
   marker 带指纹）。
6. **待拍板④**：itgate_fresh_85583 已删（零连接确认后 DROP DATABASE；另两个
   itgate_* 为活跃门数据库，未动）。
7. **顺带修复 main 上的 installer 模块编译断点**（十七轮遗留）：
   `stats_migrations_test.go:302` 引用 `…Migration803`，而 main.go:676 定义的是
   `…Migration807`（807 由 803 改号时测试没跟上）——installer 模块
   `go test/vet` 自该提交起编译红。已修 + 注释存证。

## 二、根因

1. **fe8 轮证据链错误（账本错位）**：startup SQL 的部署记账在
   `gateway_db_revision_sequences`（脚本通道），schema_migrations 是 Go runner
   通道的账；后者无行 ≠ 未应用。"两账本对齐时都要查"是既有纪律，本轮再证。
2. **清单断链机制**：脚本契约（"新迁移必须进本清单才会在存量库应用"）只约束
   脚本通道；fe8 轮为 fresh 链造的 803-806 没有对应触发"必须同时进清单"的
   门——contracts 测试只验证清单内条目，不验证"canonical 新文件必须在清单"。
   本轮以人工登记收口，缺口检测门仍开放（§五.3）。
3. **security_invoker 丢失路径**：526 建（带）→ 640/713 重建（带）→ **757 重建
   （丢）**。526 尾部有 reloptions 守卫但只在 526 时点执行，拦不住后续重建者；
   757 现已自带同款守卫，同型回归被钉。
4. **无效父索引成因**（本机，非生产）：无效父层使新分区 ATTACH 时叶子不传播
   （2026_09/10/default 缺叶），写路径实际承重的是 valid 的 tenant_* 键
   （6/6 叶），故 24h 网关日志零 42P10、无活性事故；属 2026-07 旧代键的
   结构性残留 + ON ONLY 烙印同族。

## 三、改动

1. `scripts/apply-db-revision-sequence.sh`：manifest 注册 803/804/805/806
   （802 与 807 之间，含 no-op 语义与位置约束注释）。
2. `sql/migrations/startup/757_session_turns_origin_actor_projection.sql`：
   重建语句补 `WITH (security_invoker = true)`；post-check DO 块加 reloptions
   守卫（RAISE EXCEPTION 形态照抄 526）。`.down` 对称补回（713 体本就携带）。
   `installer/cmd/llm-gw-installer/embeddata/startup/757_….sql` 副本逐字节同步
   （sha 76740af0… 双侧一致）。
3. `installer/cmd/llm-gw-installer/stats_migrations_test.go`：807 条目 const
   引用 `…Migration803`→`…Migration807`（main 编译断点修复）。
4. `docs/db-changelog.md`：757 行 SHA 同步 + 修订说明。
5. 本机 DB 运维（无迁移、不入库）：
   - heal：`DROP INDEX idx_request_logs_ts_desc`；`DROP INDEX+CREATE INDEX`
     （递归）`idx_request_logs_discard_events_ts`；两个约束键
     `DROP CONSTRAINT+ADD CONSTRAINT`（递归重建）。全部带
     lock_timeout/statement_timeout 守卫，单事务语义按语句分组执行。
   - 脚本实跑两遍（首跑落地、二跑幂等验证）。
   - `DROP DATABASE itgate_fresh_85583`。

## 四、测试

- `go build ./...`、`go vet ./admin/ ./db/ ./cmd/gateway/` 绿。
- `go test ./sql/migrations/startup/ ./sql/schema/ ./tests/48h-audit/D07-hot-columnar/data/` 绿。
- installer 模块（独立 module）`go test ./cmd/llm-gw-installer/` 绿（修复后；
  修复前编译红=十七轮遗留断点）。
- `bash scripts/apply-db-revision-sequence_test.sh` 两轮 PASS。
- §5.9 复核真库门：见 §一.1（admin -tags=integration，TEST_PG_URL 本机）。
- DB 终态验证：4 父索引 indisvalid=t + attached 6/6（ts_desc 移除）、public
  零无效父索引、视图 reloptions={security_invoker=true}、session_bodies 6 分区
  全 heap、session_dim policy 逐字不变、803-806 marker 全带指纹、二跑 116 全 skip。

## 五、遗留与移交

1. **803-806 首署抽验**：154/245/252 下次部署跑脚本时，核对各自
   `gateway_db_revision_sequences` 新增 803-806 marker 带 content_sha256
   （均为存量库 no-op，预期零行为差异）。
2. **757 内容重放同窗口发生**：154/245/252 首跑将同时重放 757（reloptions
   补齐）——owner 读路径无感，属预期；如需回验：
   `SELECT reloptions FROM pg_class WHERE relname='session_turns_with_current_month'`。
3. **"canonical 新文件必须进脚本清单"的缺口检测门**（§二.2）未建——本轮
   不扩面，登记为后续候选。
4. 13 项 legacy NULL 指纹 marker（560-685 段）仍在跳过态——机制如此（未登记
   重放清单），非缺陷；如未来个别文件内容加固，按 644 先例登记
   `legacy_content_replays` 即可。

## 六、本轮自审（四错）

1. **并发 DDL 干扰了自己的复核门**：heal 的 DROP INDEX 与等价性门并发跑，
   门首跑 FAIL(826s)。教训：真库门运行窗口内禁止任何 DDL，哪怕锁超时只有
   15s——门的时间预算（900s ctx）经不起分片损耗。重跑隔离后全绿。
2. **台账 SHA 写错一轮**：757 的 changelog SHA 在 sed 改称谓**之前**计算，
   且 sed 又把"2026-10-01 收口轮"复制成"2026-10-01 2026-10-01"。sha 必须
   在文件内容**冻结后**计算；sed 批量替换后必须重读受影响行。
3. **枚举误报**：用宽松正则从脚本提"清单文件"把**注释里提及的弃用文件**
   （666_llm_hourly_stats_direct_trigger）当成了缺口。清单提取必须剥离注释。
4. **工作被并行会话卷走**：11:04 并行会话清扫式提交 5c61408c2 把本会话未提交
   的清单注册+757 修复卷入其提交并推送（内容逐字为本会话终版，已核）。教训：
   主工作区多会话并行时，**单元改动完成即提交**，不留长寿命未提交态；
   接手方在 git status 之外还要 `git log --check` 新到提交里有没有自己的东西。
   本轮剩余三件（embeddata/const/changelog-SHA）由本会话收口提交。

## 七、收尾六项（handoff 契约）

- **结论/根因**：见 §一/§二。
- **改动**：见 §三（提交=本轮 commit；注意 803-806 注册与 757 修复主体已随
  并行会话 5c61408c2 在 main，本 commit 携 embeddata/const/changelog-SHA/文档）。
- **测试**：见 §四。
- **风险**：154/245/252 首跑脚本将首次执行 803-806 与 757 重放（均为存量库
  no-op/幂等收敛，已在本机同形态验证）；无其他行为变更。
- **handoff 更新**：本文档即本轮 handoff；fe8 轮 §五三项待拍板全部关闭。
- **下一轮提示词**：见 docs/handoff/ 对应更新（若未建，以本文档 §五为起点）。
