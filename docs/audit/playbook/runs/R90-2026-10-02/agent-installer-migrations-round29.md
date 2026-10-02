# R90 / 第三十轮 installer + R20 迁移修复 复审报告（HEAD=20fbdba74）

只读审计，未修改任何文件。主会话复核注：F1/F2/F3/F4/F5 已亲验属实并落地修复/订正；F2 中 SSOT 烤有 08:00 边界部分由主会话二次实证（grep 到 14 处 ATTACH ... 08:00:00+08 + 列存 model_probe_runs_2026_07），登记为独立 P2。

## 一、发现（候选，待主代理复核）

**F1（P2）「棘轮 74→77」未落地——commit 声称与 diff 直接矛盾**
- 提交信息声称 "down 三件双侧镜像（棘轮 74→77）"，但 diff 与工作树中 `installer/cmd/llm-gw-installer/stats_migrations_test.go:95` 仍是 `if mirrored < 74`。实际 embeddata down 数确已 74→77（`git ls-tree 20fbdba74^ .../embeddata/startup/` = 74，现 = 77），棘轮常量却未同步提升 → 3 个新镜像可被静默删除而测试不红，守卫强度未按声称收紧。

**F2（P2，理由失真 / 功能风险有限）豁免组核心论据 "fresh install 不受存量缺陷影响（687+ 基线）" 两处不实**
- 811 类缺陷烤在 fresh 基线里且链内修不掉：`sql/schema/01-schema.sql:19535`（request_logs_2026_08 上界 '2026-08-01 08:00:00+08'）、`:19577`/`:19584`（routing_decision_log_2026_07/08）及 credential_model_index / credit_ledger / request_wal / tool_usage_stats / usage_ledger 的 07/08 分区全部烤入 473 型 08:00 污染边界。链内修复者 687 的检测谓词 `sql/migrations/startup/687_fix_473_partition_0800_bounds.sql:55` 同样依赖会话时区渲染，而 687 **没有** SET LOCAL TIME ZONE；installer 会话无任何时区钉扎（`installer/internal/dbinit/runner.go:677-687` 裸 docker exec psql、`scripts/deploy-local-pg17-docker.sh:175-177` 无 TZ/PGTZ/INITDB_ARGS、全库无 ALTER DATABASE SET timezone）→ UTC 会话下 pg_get_expr 渲染 '00:00:00+00'，687 在 fresh install 上静默漏检全部污染分区。豁免注释 "the 687+ baseline already builds heap+toast partitions with +08 bounds" 不成立——+08 边界只由 694/714 钉扎后的 ensure 在运行期为未来月创建。实际影响有界：污染缺口 [09-01 08:00, 10-01) 全在过去、ensure 只建未来月、request_logs 有 808 default 兜底、811 走序列通道首跑自愈——但论证链是错的，fresh 装将永久带 off-grid 边界 + 历史缺口。
- 812 类状态同样烤在基线里：`sql/schema/01-schema.sql:9972` `SET default_table_access_method = columnar;` 紧接 `:9978` CREATE model_probe_runs_2026_07（`:19521` ATTACH）→ fresh 装确实烤入一个列存 probe-run 分区，"A fresh install never creates those states" 字面为假。缓解：该链路已废弃（`bg/partition_manager.go:50` "全仓无 model_probe_runs 的非 hot 写入"；ensure 是 stub `01-schema.sql:1681-1688`），updater 谓词剪枝扫不到 2026_07，序列通道首跑 812 转 heap。

**F3（P3）down 未镜像计数注释改反了方向**
- `stats_migrations_test.go:66` 现写 "canonical 侧另有 248 个 down 未镜像"；实测 HEAD = 322 canonical down − 77 已镜像 = **245**（改前注释 245 vs 实际 322−74=248，同样错）。3 个新增镜像被加到了错误的计数上；正确落点是 F1 的棘轮常量。

**F4（P3）"explicit BEGIN/COMMIT cannot ride psql --single-transaction" 机制表述失真**
- installer 通道 199 个 embedded up 文件中 **129 个**带顶层 `BEGIN;`/`COMMIT;`（含 694、806 等，main.go:449/114），长期 riding `--single-transaction`（psql 内层 BEGIN/COMMIT 仅产生 WARNING，rc=0，不 abort）。810-812 豁免成立的实际理由是"一次性存量修复 + 操作员门"（这部分成立），不是事务机制（`stats_migrations_test.go:310-312` 与提交信息所述会误导后续审计）。

**F5（P3）.DS_Store 修复真实性缩水 + 机制归因错误**
- `embeddata/startup/.DS_Store` 从未入库（.gitignore:5；`git log --all` 无记录；父提交 ls-tree 无），本提交 name-status 亦无该文件 → "顺清"只是本地工作区清理，CI/fresh clone 从未红。且 go:embed 全为逐文件显式指令（main.go:36-693），不存在"通配扫入"路径；真正会红的是 `TestStartupFilesAreAllEmbedded` 的 ReadDir⊆StartupFiles 方向（`stats_migrations_test.go:246-253` 只跳过 `*.down.sql`）。工作区现存 `./.DS_Store` 与 `./deploy/.DS_Store` 两枚未清。

**F6（P4 观察）豁免三组无不变式守卫**
- psqlConcurrencyRequired / operatorGatedCleanup / sequenceChannelRepairs（`stats_migrations_test.go:288-317`）均无类似 no-transaction 标记验证（"入 map 必须真含对应特征"）的守卫——误放文件会永久静默跳过 installer 注册。本轮三条目经核验属实（均真有 BEGIN/COMMIT），守卫面不对称仅作记录。

**F7（P4 观察）序列通道门只盯最高编号**
- `scripts/apply-db-revision-sequence_test.sh:93-98` 仅验证最高编号在 files 数组；同批多迁移仍可漏登非最高者（534/612/808 已知同型，脚本注释自认）。810/811/812 当前已登记（script:759/766/772，由 R20 提交 d36806367 登记，非本提交），本轮无新断链。

**F8（P4 观察）811 次月兜底时序 + 游标内 DDL**
- bak 含跨月 beyond 行且次月分区未建时直接 EXCEPTION（811:119-122），而创建次月分区的 ensure 在循环后（811:138-139）——fail-closed 无数据丢失，纯可用性 nit（252 实跑未触发）。另 810/811/812（及 687）的 plpgsql FOR-IN-SELECT 游标在循环体内 DETACH/DROP/CREATE，属 806/687 既定先例延续，非本轮引入。

## 二、核实为健康的面

1. **豁免组本体真实且不弱化守卫**：`sequenceChannelRepairs` 存在（stats_migrations_test.go:313-317 + 367-370 检查点），按文件名精确匹配；新迁移 813+ 不会自动落入（≥704 注册测试仍强制"注册或显式入 map"二选一）；755 先例（operatorGatedCleanup，:301-303）确为同层同理由（显式事务 + 操作员门 + fresh 无遗产）。
2. **810 TOCTOU 修复属实且正确**：预检 count(68) → DETACH(74) → 同事务 AccessExclusive 锁内二次 count(80) → 非空 ATTACH 回退(82-84) fail-closed；空则 DROP+CREATE(88-90)，default 窗口收行时 CREATE/ATTACH 报错整事务回滚，无静默丢行。
3. **812 TOCTOU 同款属实**（56→58→62→64-67）；**to_regclass 守卫真拦截**（41-44：RETURN 退出 DO 块 = 整迁移 no-op，恰好护住 52 行 `::regclass` 字面引用，不报错不继续）。
4. **811 时区钉扎语义正确**：BEGIN(39)→SET LOCAL TIME ZONE(48)→DO 块同显式事务，SET LOCAL 覆盖全程；检测谓词在 +08 会话命中 UTC 污染、不误报 +08 干净边界；重建边界为显式 '+08' 字面量（时区无关）；序列通道 = 裸 `psql -f`（apply-db-revision-sequence.sh:11）无外层事务，文件自身 BEGIN 使 SET LOCAL 生效。
5. **SHA 追认登记与 730 先例一致且真实**：独立 ## 节 + 同构表 + 旧行保留（db-changelog.md:798-802 vs 812-820）；`verify-migration-checksums.sh:56-70/94-103` 确按文件名聚合多 SHA、任一命中即过；三个新 SHA 实算吻合，无 STALE-IN-REGISTRY。
6. **down 三件双侧镜像逐字节一致**（cmp IDENTICAL ×3）；三件 down 均为有理由的 no-op 占位（811 down 明示不还原污染边界），up/down 不对称系有意设计且有文档。
7. **三通道对账清晰**：installer StartupFiles/embeddedSQLFiles 无 810-812（豁免意图达成）；序列数组 810(759)/811(766)/812(772) 序合规；无 Go ensure 镜像（与一次性修复定位一致）；序列台账 sha 机制保证 252 老字节库下次部署自动重放加固版。
8. **TSV 无断裂**：810-812 不在 installed_startup_migrations.tsv（至 809）与其非 installer 通道身份自洽，TSV 消费方（startup_manifest_test.go:36）无 ≥704 全量断言。
9. **canonical down 计数无暗变**：父提交 323 条 ls-tree 命中含 1 个 `.down.sql.skip`，实际双侧均 322，无删除。

## 三、未覆盖项与原因

- 未实跑 go test / psql 行为验证（只读纪律）；psql 嵌套事务 WARNING 语义基于 psql 既定行为，未起库实测。
- 252 生产库实况未验证（无 DB 访问）："已治愈 8 分区 / 计数守恒"等仅采信 db-changelog 台账自述。
- deploy/.DS_Store 与根 ./.DS_Store 是否触发其它 ReadDir 型测试未排查（不在本提交范围）。
- db-changelog R28 以前历史节格式未逐一核对。
