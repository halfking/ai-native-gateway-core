# R93 域A 报告：视图契约链 817 迁移本体 + 迁移通道域（第三十三轮）

审计人：分域子代理 A（只读，除本报告外未改任何文件）。窗口 a0da9066d..HEAD，逐提交直读 diff + 代码/测试静态核验；本机真库面（817 五点 md5、通道台账收敛、敌意值行为门真库 PASS）沿用前一会话运维复核结论，本轮不重复。域上下文：runs/R92-2026-10-02/agent-view-contract-816-chain.md。

## 一、逐提交判定

| 提交 | 判定 | 证据（file:line） |
|---|---|---|
| ab1c1b804 §9.64 817 本体+契约测试 | **成立（附 P2-1 守卫链缝隙）** | proj 118 列与 815/816 逐字节对照仅 client_ip 一行演化（815:115 `NULL::inet` → 816:264 字符类 → 817:115 `pg_input_is_valid`，awk 提取四方 diff 实证）；names 118 列三方一致（816:270=817:247=817down:199）；down 真退回 816 字符类形态（down:193 与 816:264 逐字节同）且头部带弱点警告（down:6/9/14 三关键词）；Go 镜像体同步（db/request_logs_view_schema.go:505）；contract test 重放链补 817 up、down 链先 817 后 816、re-up 含 817（db/view_schema_v2_contract_test.go 本提交 diff） |
| 9f09ed39d installer 817 五点同步 | **成立** | embeddata up/down 与 canonical md5 逐字节同（`ba55b679…`/`db7d5a5c…` 双对实测）；main.go:719-720 go:embed 变量、:936 映射；runner.go:685 StartupFiles 条目+无自愈通道理由（:679-684，与 request_logs_view_schema.go:60-77 早退探针只认 `session_turns`/`session_turn_details` 片段的实况吻合）；TSV 第 206 行（文件 :224） |
| 783e5df8f 817 补登通道数组 | **成立** | scripts/apply-db-revision-sequence.sh 817 条目在位，注释与迁移正文逐点相符（字符类→语义、192.168.1/deadbeef/::: 样本、22P02、幂等）；`bash scripts/apply-db-revision-sequence_test.sh` HEAD 实跑 PASS |
| 8f79ad402 815/816 补登通道数组 | **成立（816 条目注释陈旧，见 P3-1）** | 815 条目注释的承重引用实证：admin/compression_stats.go:210-214 确读 token_band、runner.go:647-649 确记 42703 被吞；816 条目内容描述与 816 正文一致，唯「CASE 守卫…防畸形值」一句已被同窗口 ab1c1b804 证伪 |
| fe40c0802 selfcheck se 臂形状门同步 | **成立** | 门字面量（bg/credential_selfcheck_s4_guard_test.go:83-88）与实际 SQL（bg/credential_selfcheck.go:309/311 SELECT 四列+partition_date、:321 裁剪谓词）逐字一致；「裁剪谓词在位」断言承重：提取是臂定位+注释剥离（selfcheckPickSQL :139-158 stripSQLLineComments、lateralArmSrc :152-170 按 `) se ON` 边界），谓词为精确字面量 Contains——删谓词/改窗口即红。`go test ./bg/ -run TestSelfcheckErrorArmCoversSessionFamily` PASS |
| 7d55159b4 baseline-lag 门翻转口径 | **成立（needle 块弱判据，见 P3-2）** | sql/schema/baseline_drift_test.go:115-127 对象必须仍在 canonical（:117-121 Errorf+continue）、副本出现/缺席双态合法；:132-144 needle 块对全部 10 对象**无条件**承重（收敛态也查）——「收敛态+供给迁移缺席」合并场景 ReadFile 失败即 Errorf，无静默绿；HEAD 实跑 PASS（收敛 10/滞后 0，canonical=installer=2612 对象） |

## 二、发现清单

**P2-1｜817 up 第二 DO 块无链守卫——头部宣称已修的崩溃形态在本体仍在**：up 文件块 1（817:55-60）链不全时 NOTICE+RETURN，但 DO 块间无共享状态，块 2（:79-294）照样执行，在 :115 对 `request_logs_with_current_month_without_request_class_due_at` 做 `::regclass` 时崩「relation does not exist」——恰是头部 :52-54 声称该守卫要避免的结局（「那不是 no-op 是崩溃」），也是提交信息「顺带修 817 自身四处」第 1 条宣称已修的缺陷。down 文件作者显然懂这个机制（down:259-261「第一个块早退时第二个块照样会跑」）并把三个块全部补了守卫（:37-42/:262-267/:291），up 却只有块 1。测试 TestMigration817KeepsViewChainGuard（migration_817_test.go:161-183）只查两文件中三视图 to_regclass **存在性**，查不出块级无守卫，其文档叙述会让读者以为崩溃形态已被门住。缓解：①该行为是 fail-loud（迁移报错中止，非静默交付）；②同族缝隙 815/816 均有（816:102 块 2 同型无守卫），且 runner.go:651-657 已把「skip 路径实际全部终止迁移、no-op 措辞是虚假广告」登记为 12h 审计 P2-F、列为主人裁决的通道重放决策——817 是已知家族行为的第三实例，生产增量暴露≈0（降级链上 815 先炸）。建议与 P2-F 一并裁决时同修。

**P3-1｜816 通道条目注释被 §9.64 证伪未订正**：scripts/apply-db-revision-sequence.sh:801-802（816 条目）仍写「CASE 守卫 ::inet 转换防畸形值打挂全读方；幂等」——ab1c1b804 的全部论点就是这句话不成立（字符类合法语义非法的值全部穿透）。注释写于 8f79ad402（817 诞生前 22 分钟），783e5df8f 补登 817 条目时未回订。下方 20 行的 817 条目已写明真相，实际误导风险低；与 41af920d5 订正「815 skip 失实注释」同款处置即可。

**P3-2｜baseline-lag needle 块判据偏弱**：①needle 是 Contains 级——条目 1（baseline_drift_test.go:86）的 needle 就是 `revision`，566 正文必然含该词，实际退化为「566 文件在位」检查；其余条目虽钉了具体标识（如 `credentials_governor_revision_seq`），但 DDL 被瘦身、正文仅存注释引用时仍假绿（提交信息自述「首轮单处替换变异被 Contains 多次命中假绿」，已知局限）。②:136 的报错文案「不在 installer 启动集」实际只查 `sql/migrations/startup/` 目录在位，不查 StartupFiles 注册——566/608/609 若被从 runner.go 摘注册，本门仍绿（通道 required 列表止于 800 且不含这三个号、delivery-path 门只管 >=690，<704 的注册确实无门）。收敛态下 fresh install 由基线供对象，暴露面是升级链存量库，风险低但与文案不符。

**P3-3｜通道同形遗漏的结构性温床（信息项）**：apply-db-revision-sequence_test.sh 对通道数组的硬性要求只有「最高号启动迁移必须在数组」（:93-101）；中段条目（如未来 817 在 818 落地后被漏登）可经 StartupFiles 腿通过 delivery-path 检查（:26-28 三选一）而全绿。legacy_content_replays 指纹重放通道仅含 644 一条，不覆盖 815/816/817——817 是新迁移且已由序列+top-guard 覆盖，不构成缺口，但「第五次同类遗漏」的复发条件（top 号易主后的中段漏登）依然无门。

**P3-4｜817 静态门探针白名单的理论缝**：migration_817_test.go:116-119 对可执行体中 `client_ip ~ ` 残留按「该行含 position( 且含 in v_def」白名单豁免——真残留若被刻意写成探针形状可漏检。分层设计（proj 块零残留 + 全文逐处判定）已是正确结构，纯理论风险，记录备查。

## 三、核实为健康的面

1. **118 列契约四方对照**：815/816/817up/817down 的 proj 块各 118 行，逐字节 diff 仅 client_ip 一行按 815→816→817 演化、down 精确还原 816 形态；names 块三方逐字节一致。817 不需要对照 815 抽查——是全块等价。
2. **817 三段守卫链主体质量**：块 1 三视图链 no-op + details 族缺失 EXCEPTION（:64-67，fail-closed 指向 733）+ viewdef 幂等判据（:70-73）与 cast 前置 EXCEPTION（:74-77，防「永远为真的空操作」）；块 3 对账三重 fail-closed（118 列 :308-310、pg_input_is_valid 必须在场 :312-314、字符类必须消失 :317-319——后两条判据都用渲染后仍存在的片段，816:330-333 的教训已吸收）。幂等判据顺序正确（先语义守卫 no-op、后 cast 缺失 EXCEPTION，两判据不相吃）。
3. **migration_817_test.go 质量较高的点**：静态门钉在 `proj := $proj$ … $proj$;`（:80）而非全文，头部解释性注释喂不饱门（:96-99 明说）；敌意值双向分类完整（7 语义非法→NULL + 4 语义合法→透传 + 11 行全返回下限，:310-336）；行为门带 817 形态前置 skip 而非假绿（:256-259）；反向证据在事务内剥壳跑 down、断言恰好一层 BEGIN/COMMIT（:374-389，防内层 COMMIT 提交掉外层回滚）、错误类型精确匹配 inet 22P02（:353-359）；无 R32 §四#8 那类恒真断言（所有 NULL/非 NULL 断言都有反向臂，行数下限防空结果）。
4. **installer 五点**全部逐点实测在位（见上表），且 runner.go:679-684 的「无自愈通道」理由与 request_logs_view_schema.go:60-77 早退探针实况吻合——这句理由是真的。
5. **7d55159b4 无静默绿**：needle 块对收敛态照常承重；门翻转与 d5932d26c 快照收敛决策一致，且把「对象离开 canonical」和「供给迁移瘦身」两个方向都留在 RED 侧。
6. **实跑**：`apply-db-revision-sequence_test.sh` PASS；`go test ./sql/migrations/startup/ -run TestMigration817` PASS（敌意值门离线 SKIP，真库 PASS 为前会话已实测）；`go test ./bg/ -run TestSelfcheckErrorArmCoversSessionFamily` PASS；`go test ./sql/schema/ -run TestDerivedBaselineLagIsSuppliedByMigrations` PASS；`go vet ./sql/migrations/startup/ ./bg/ ./db/` 0。

## 四、不确定项

1. 敌意值行为门本轮未对活库重跑（无 TEST_DSN 环境）——测试逻辑静态复核无恒真/无漏分类，真库 PASS 采信前会话运维复核。
2. P2-1 的崩溃路径未在真库降级链上演练（结论来自 plpgsql DO 块语义 + :115 `::regclass` 静态推演；down 文件 :259-261 与 runner.go:651-657 两处在库注释佐证同族机制成立）。
