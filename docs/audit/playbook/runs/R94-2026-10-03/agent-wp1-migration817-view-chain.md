# WP-1 迁移817收编+视图契约链 子代理报告（窗口 a0da9066d..HEAD）

窗口内相关提交：ab1c1b804（817 本体+测试+Go 镜像体）、9f09ed39d（installer 五点同步）、783e5df8f（通道 files 数组补登）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **817 up 的 view 链守卫是结构性死守卫**：第一个 DO 块链不全时 `RAISE NOTICE ... skipping` + `RETURN`，但 `RETURN` 只退出**该 DO 块**；第二个 DO 块（重建体）照样执行，其第 115 行 `'public.request_logs_with_current_month_without_request_class_due_at'::regclass` 在链不全时直接崩 42P01（第三校验块 ：311 同病）。所谓「skipping — db.ensure rebuilds at startup」实际是 fail-closed 崩溃，与 815 同款「skip 路径必然终止迁移」的已登记矛盾（runner.go 注释原文："every 'skip' path actually terminates the migration... fail-closed is the real behavior"）。对照：**down 文件结构是对的**——链守卫放在重建块**内部**（down:37-42），第三块还显式重复守卫并注明「两个 DO 块之间没有共享状态，第一个块早退时第二个块照样会跑」（down:259-267）；up 却没有把守卫放进重建块。实际暴露面窄：通道序里 815 在链不全形态下先崩（runner 注释），fresh install 链必在；仅「操作员对无链库单跑 817」或「wrappers 也在但整链缺」的场景触达 | sql/migrations/startup/817_request_logs_view_client_ip_semantic_guard.sql:55-60（NOTICE+RETURN）、:115、:311（无守卫的 regclass）；对照 down:37-42、262-267；installer/internal/dbinit/runner.go:644-657（815 同款矛盾已登记） | 属 815/816 家族继承形态且 R32 已登记为 owner 裁决项，非 817 新增缺陷。若 owner 批准动 817（本机已应用，内容改动=通道重放决策），把链守卫挪进/复制进第二 DO 块（照抄 down 的正确结构）；否则按 815 先例登记为已知 fail-closed 行为即可 |
| 2 | P3 | **up 头部「Idempotent: 是（viewdef 里已是 pg_input_is_valid 形态即 no-op）」措辞失实**：重放时块 1 在 pg_input_is_valid 探针上 RETURN 后，块 2 仍**无条件重跑整条 CREATE OR REPLACE VIEW 重建**（结果幂等、合法，但非 no-op）。与发现 1 同根（两 DO 块无共享状态）。重放安全性不受影响（见二.3） | 817 up:38（头部声明）、:70-73（块1 RETURN）、:79-294（块2 无条件重建） | 措辞订正随发现 1 一并处理，或登记同款「false advertising」已知项 |
| 3 | P3 | **README 八语种迁移计数停在设计时刻的 816**：一致性清扫 8fcb0d392（23:27:42）提交树里**已含** 817（ab1c1b804 更早落地，cat-file 实证 "817 IN SWEEP TREE"），但八语种 README 全部写成 "startup series currently at 816"。817 收编轮未跟这处文档面 | README.md:112 及 README.ar/es/fr/de/ja/zh-CN/zh-TW.md 同行；git show 8fcb0d392 --stat（809→816 订正）| 下轮 sweep 把八处计数 816→817 |
| 4 | Info | **pg_input_is_valid 要求 PG ≥ 16**：守卫是完整语义判据的前提是版本。本仓钉 PG17（scripts/deploy-local.sh:429 默认 postgres:17-alpine、scripts/deploy-local-pg17-docker.sh:27），失败模式为迁移报错 fail-closed；仅未钉版的外接 PG 15- 会触达 | 817 up:28-32；deploy-local.sh:429 | 无需处置；保持 PG17 钉版纪律即可 |
| 5 | Info | **行为门的敌意值清单未含「多 IP 链」与空串**（`1.2.3.4, 5.6.7.8`、`''`——816 头注释里提过的两类）：不影响正确性，pg_input_is_valid 按构造就是 ::inet 的接受谓词，矩阵在语义上封闭；清单只是抽样 | migration_817_test.go:51-66 | 可选补两值，非必须 |

## 二、核实为健康的面

1. **五点同步逐点核对（全部在位，且有静态门兜底）**
   - ① canonical SQL ↔ installer embeddata：`diff` 逐字节相同（up 与 down 均零差异）；门：parity sweep 逐文件 bytes.Equal（installer/cmd/llm-gw-installer/stats_migrations_test.go:46-52，ratchet ≥199）+ TestDownMigrationsMirrorCanonicalSources（:70-96，down 字节一致 + ≥77 棘轮）。
   - ② runner 注册：installer/internal/dbinit/runner.go:685 `"817_request_logs_view_client_ip_semantic_guard.sql"`，置于 816（:677）之后，依赖序正确；门：TestStartupFilesAreAllEmbedded（stats_migrations_test.go:220-253，双向）+ canonical ≥704 注册门（:368-389，817 不在任何豁免清单）。
   - ③ TSV 台账：sql/schema/installed_startup_migrations.tsv:224（`206	817_...`），紧随 816（:223=205）；门：TestStartupManifestMatchesStartupFiles（installer/internal/dbinit/startup_manifest_test.go:124）。
   - ④ main.go embed：installer/cmd/llm-gw-installer/main.go:719-720（go:embed var）+ :936（embeddedSQLFiles map 键）。
   - ⑤ 升级通道 files 数组：scripts/apply-db-revision-sequence.sh:808（783e5df8f 补登，置于 816 :801 之后）。
2. **817 SQL 本体——守卫逻辑真正堵住 816 缺陷**：投影换成 `(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END)`（817 up:241），pg_input_is_valid 与 ::inet 接受谓词同构，伪造值矩阵封闭：IPv6 合法（`2a06:98c0:3600::103`/`::1`）透传、IPv6 非法（`:::`/`...103x`）、`192.168.1`/`deadbeef`/`999.1.1.1`/多 IP/垃圾串/空串全落 NULL、NULL 入 NULL 出，均无 22P02 路径。**118 列契约不变**，client_ip 留在原位（up:241 在 credits_rate_multiplier 与 origin_stage 之间；names 串 ：247 同序）。
3. **幂等收敛两形态均正确**：
   - 816 已应用库首跑：块 1 探针 `position('pg_input_is_valid' in v_def)` 否 → 硬前置 `position('t.client_ip::inet' in v_def)` 是（816 自己的落地校验用同一 needle，816 up:334-337，真库已验证渲染形态）→ 通过；缺 816 投影则 EXCEPTION "run 816 first"（fail-closed，up:74-77）。
   - 干净库：runner 序 815→816→817，链与 session_turn_details 族（733）均在；硬前置缺失显式 EXCEPTION（up:64-67）。
   - **通道重放安全**（本机 22:54 已应用体、台账无行场景）：脚本报 per-file marker `<sequence>:<basename>`+content_sha256（apply-db-revision-sequence.sh:984-1020），无 marker → "applying" 重跑 817 → 块 1 NOTICE 早退、块 2 重建出逐字节同形视图（CREATE OR REPLACE 列名/类型/序号未动，合法）、块 3 校验过、schema_migrations upsert（ON CONFLICT DO UPDATE，up:323-325）→ marker 落账。psql ON_ERROR_STOP + `set -euo pipefail`，失败不落 marker，重试安全。
4. **视图重建后 816 弱守卫无残留**：全仓扫描 `client_ip ~`/`^[0-9a-fA-F:.]+$`，可执行体仅存于 816 up 本体（历史件，运行时被 817 置换）与 817 down（有意回退件）；Go 侧非测试代码零残留；817 up 第三校验块双探针 fail-closed——pg_input_is_valid 必须在场、`client_ip ~ ` 必须消失（up:311-319）。
5. **down.sql 可逆性良好**：重建回 816 字符类确切形态（down:193），头部带「刻意危险/绝不要仅因 817 变慢就回滚」警告；三块结构正确——链守卫在重建块内（:37-42）、校验块重复守卫（:262-267）、**ledger 条件删除**：链不全/语义守卫仍在场时保留 817 行，只有真回滚才 DELETE（:289-302），避免「已应用」与「文件内容」分叉。对 817 未应用库跑 down 为无害幂等重建 + DELETE 空操作。
6. **817 测试门（migration_817_test.go，4 门）无恒真陷阱**：
   - 静态三门（离线可跑）：守卫形态钉在 proj 块（:78-151）——②的弱守卫残留判据刻意钉在**无注释的 proj 块**而非全文，并逐处判定文件级 `client_ip ~ ` 出现是否为探针（:115-123），规避了「被自己的探针/解释注释喂饱」的 §9.3 自证漏洞；③④钉渲染后对账（`position('pg_input_is_valid' in v_def)`、cnt<>118、names 尾序）。链守卫门覆盖 up+down 三视图（:161-183）。down 门钉回退形态+警告关键词+ledger 守卫（:186-225）。
   - 行为门（:236-363）**有牙且防恒过**：真库（TEST_PG_DSN，离线 skip）事务内插 7 非法+4 合法值→全行返回/非法 NULL/合法透传三向断言；**反向证据臂**在同一事务剥壳跑 down 换回 816 形态，要求同批值必须以 "invalid input syntax for type inet" 报错，报别的错或不出错都红（:348-362）；strip817TxnWrapper 断言恰好一层 BEGIN/COMMIT 防事务保护静默消失（:374-389）。
   - 816 测试（migration_816_test.go）与 817 兼容：其字符类守卫门注释显式改写为「守 816 历史形态不许被偷改，语义判据在 817」（:52-66），不与 817 冲突。
7. **Go 侧与 817 同源、无漂移**：db/request_logs_view_schema.go:505 与迁移投影逐字符同表达式（`(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END)`）；db/view_schema_v2_contract_test.go 三层钉：registeredProjectionAppends 表登记 817 表达式（:263）、ensure 侧 pg_input_is_valid 在场/`client_ip ~ ` 缺席（:429-442）、**live round-trip** 在 scratch 库重放 710+734+738+740+815+816+817 后与 Go ensure 产出逐字节相同 viewdef（:511-521，注释明言「漏掉 817 会在这里现形」）。
8. **816 未被 817 破坏**：816 的 idempotency 探针（`t.client_ip::inet` + `WHEN t.client_ip ~ ` 双在才 skip，816 up:85-90）语义是「816 形态已落地」；817 应用后该探针不再命中，816 重放会走重建路径回到 816 形态——通道序 816 在 817 前、重放后 817 紧随重放收敛，channel files 数组序保证最终态正确。

## 三、未覆盖项与原因

1. **真库实跑未验证**：本审计为纯静态（只读约束 + 本环境未连库）。行为门（TestMigration817Hostile...）与 live round-trip 均需 TEST_PG_DSN，本轮未执行；「本机 22:54 已应用 817、通道重放收敛」的结论由轮文档（docs/24h审计第三十二轮-20261002.md §四#1，783e5df8f 更新版）与 SQL/脚本静态推演交叉支撑，未独立复跑。
2. **runs/R92-2026-10-02/agent-view-contract-816-chain.md 不在本仓库快照中**（runs/ 目录不存在于 llm-gateway-go-2），域上下文仅能以窗口内轮文档与代码本身为据；其结论未被引用为事实。
3. **go test 门未实际运行**：五点同步的四个静态门（parity/manifest/≥704 注册/StartupFiles 双向）逻辑上对当前文件内容必然通过（逐点实证见二.1），但按只读纪律未执行 go test 确认绿态。
4. **815 自身块结构的完整走查**超出本轮包（属 815 收编时的已登记项）；仅就「815 先崩导致 817 守卫死代码在通道序下不可达」这一交叉影响做了定位，未逐行复审 815 全文。
