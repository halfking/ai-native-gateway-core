# 子审计 B：迁移登记面（990/991）与探测桶统计修复

- 审计日期：2026-11-20。审计窗口：2026-11-18 后 48h。
- 方法：只读审计（git show / grep / 读 HEAD 代码）；门禁类测试以 `GOCACHE=/tmp` 实跑验证（sqlguard / installer / bg 定向用例）。
- 审计对象提交：`14b2c1923`（990/991 七面登记）、`d3f7ce95e`（探测桶一修）、`491ebeb25`（二修）、`0d32aebe5`/`02cec493f`（bg 刷新器，仅迁移登记视角）、`bc6c36695`/`e0ef81187`（981 台账）。
- 判定基准＝HEAD（`3745f26f9`）。注意：`14b2c1923` 之后 `edb297f6b`（并行车 211 试跑轮）与合并 `3f1de6b6f`/`bae5493ba` 又改写过 990/991 正本——**提交信息/台账行的部分自述相对 HEAD 已过期**，见 §8。

## 0. 结论总表

| # | 审计项 | 判定 | 关键证据 |
|---|--------|------|----------|
| 1 | 990/991 本体（用途/幂等/事务） | 已实现 | 990 事务性幂等 DDL；991 纯函数无事务 |
| 2 | 七面登记 | 已实现（HEAD 七面全在） | §2 逐一 file:line |
| 3 | 锁预算/自登记加固 | 已实现（991 锁预算后被上游字节替代，等价判定合理） | §3 |
| 4 | 投递门测试 | 已实现（每迁移 7 断言，HEAD 实跑绿） | §4 |
| 5 | 探测桶 sargable 二修 | 已实现（两侧同时有界 + 封顶；等价性：区间改写已双探针证实，窗口收窄为「结构性论证+有意收窄」） | §5 |
| 6 | probe_hot 残留非 sargable 查询 | 未发现残留 | §6 |
| 7 | 981 台账多行登记惯例 | 已实现（终态单行、SHA 与文件实测一致） | §7 |
| — | 缺陷 | P2×1、P3×4 | §9 |

## 1. 迁移 990/991 本体

**990**（`sql/migrations/startup/990_stats_consistency_fixes.sql`，首版 `444c509df`，加固 `14b2c1923`）：
- 用途：session_summaries 补 `total_credits_charged`（L22-23）与 `avg|min|max_ttft_ms`（L26-29）；stats_usage_daily/monthly 补 `input/output_cost_usd`（L42-44、L60-62）；建对账视图 `stats_consistency_check`（L200-251）；重写触发器函数 `update_session_summary()`（L71-194）。
- 幂等：`ADD COLUMN IF NOT EXISTS` / `CREATE OR REPLACE` / 台账 `ON CONFLICT DO UPDATE`（L254-256）⇒ 内容指纹重放安全。
- 事务：文件自带 `BEGIN…COMMIT`（L11/L258），锁预算用 `SET LOCAL`（L14-15，5s/10min）——SET LOCAL 只在事务块内有效，与自带事务匹配。
- ★ 文件头（L12-22 区段，加固轮补写）如实登记：重写的触发器函数是**死代码**——触发器宿主表已随 890 退役（890_drop_request_logs_v1_family 丢弃 v1 族表连带删 `trg_update_session_summary`；仓内核实：563 在 `_request_logs_hot` 上建触发器，890 down 在 L3045 恢复它）；session_summaries 现实唯一写方是 Go 侧 `internal/sessionv2mirror/session_summary_dim.go:107-114`，其 INSERT 列清单**确实不含** total_credits_charged/TTFT 三列（实读核实）⇒ 990 新增列在收口前恒 0/NULL。此缺口在迁移头/通道注释/台账三处一致登记，非隐瞒。

**991**（`sql/migrations/startup/991_stats_timezone_standardization.sql`，首版 `418062435`）：
- 用途：`utc_day_start`/`utc_today`/`utc_days_ago` 三个 UTC 日边界辅助函数（L20-47，IMMUTABLE/STABLE PARALLEL SAFE）。**不修改任何现有查询**（纯增量工具函数）。
- 幂等：全 `CREATE OR REPLACE` + 台账 `ON CONFLICT DO UPDATE`（L79-81）。
- 事务：HEAD 版**无 BEGIN/COMMIT、无锁预算**（见 §3 注记——`14b2c1923` 加固版曾带 `SET lock_timeout` + `CREATE TABLE IF NOT EXISTS migration_metadata`，合流时采纳上游 245 已部署字节，二者皆被替代）。纯函数无强锁 DDL，逐语句原子，重放安全。

## 2. 「七面登记」逐一核实（HEAD）

提交 `14b2c1923` 自述七面，逐面 grep 确认如下：

| 面 | file:line | 状态 |
|----|-----------|------|
| ① embeddata 副本×2 | `installer/cmd/llm-gw-installer/embeddata/startup/990…sql`、`991…sql`；与正本 `diff` 逐字节一致（本轮实测双 IDENTICAL） | ✅ |
| ② dbinit runner StartupFiles | `installer/internal/dbinit/runner.go:1971`（990）、`:1975`（991），排序在 980/981 之后 | ✅ |
| ③ tsv（-update 再生） | `sql/schema/installed_startup_migrations.tsv:399-400`（序号 381/382；合流后因 992 加入顺延） | ✅ |
| ④ main.go go:embed/var/map | `installer/cmd/llm-gw-installer/main.go:1828-1831`（embed 指令）、`:2473-2474`（embeddedSQLFiles map） | ✅ |
| ⑤ docker 通道 apply-db-revision-sequence.sh | files 数组 `scripts/apply-db-revision-sequence.sh:2787`（990）、`:2791`（991）；`legacy_content_replays` 指纹 `:2862-2863`；`update_session_summary` clobber 链尾 `:2980`（990 在 661 之后，防 exit 5 abort） | ✅ |
| ⑥ 契约测试镜像同步 | `internal/sqlguard/migration_990_991_delivery_test.go`（内含 embeddata==正本字节断言 L50-59/L123-132）；另有通用门 `installer/cmd/llm-gw-installer/migration_embed_sync_gate_test.go`（≥849 全量强制 embed）与 tsv↔runner 门 `installer/internal/dbinit/startup_manifest_test.go:124`，二者 HEAD 实跑绿 | ✅ |
| ⑦ db-changelog 台账行 | `docs/db-changelog.md:11100`（990）、`:11101`（991）；另有 deploy-245 实证行 `:11108-11109`（applied+verified，seq 3134） | ✅ |

**与 979 先例「六处」的关系**：979 口径六处 = embeddata×2 + main.go embed/var/map + dbinit runner + tsv + docker 通道。本轮七面**多了两面**：⑥ 投递门契约测试（把登记面钉成可执行断言）与 ⑦ db-changelog 台账行（台账 SSOT）；且 docker 通道内对 990 额外多了两件登记物（clobber 链尾 + legacy 指纹），因为 990 重定义了通道内既有函数——这正是 962 先例（不登链尾 → 部署在任何文件应用前 abort exit 5）。七面 = 六处 ∪ {契约测试, 台账行}，**无遗漏面**。979 教训（漏 docker 通道 ⇒ 211 schema 不齐）在本轮被显式对齐（⑤ 全三点齐）。

## 3. 锁预算/自登记加固

- **机制**：① 强锁 DDL 迁移声明 `lock_timeout=5s` + `statement_timeout=10min`（990 用 `SET LOCAL` 配自带事务，L14-15）——ALTER TABLE 三张统计表拿 ACCESS EXCLUSIVE，抢不到锁 5s 快速失败、事务整体回滚、不半应用；这是 `installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go` 对 >870 新迁移的硬门（cutoff=870，L46-50），HEAD 实跑该门绿。② `schema_migrations` 自登记（≥952 纪律，990 L254-256 / 991 L79-81）——不依赖部署通道补登记，任何直跑路径都会落台账。③ 991 首版的 `INSERT INTO migration_metadata` 指向全仓零建表方的表（真跑 42P01）；`14b2c1923` 用「建表 + 守卫 INSERT」收口，合流后 HEAD 采纳上游「删引用」方案（改自登记、不建表）——两方案等价消除 42P01，HEAD 的投递门同步改为禁止活体 `INSERT INTO migration_metadata`（delivery_test L105-114），仓内已无任何活体引用（grep 仅注释命中）。
- **对既有迁移重放的影响**：无。锁预算是每文件会话/事务局部 GUC，不持久、不外溢；自登记 `ON CONFLICT DO UPDATE` 幂等；990 的死代码函数 `CREATE OR REPLACE` 无行为副作用。docker 通道按 `psql_file` 逐文件流式执行（`scripts/apply-db-revision-sequence.sh:3217-3261`），无外层事务包裹 ⇒ 990 自带 COMMIT 与通道兼容（注：edb297f6b 登记过一次「手工外层回滚被文件自带 COMMIT 切断」的试跑事故，属量具操作面而非通道面，DDL 幂等无损失，已如实入账）。
- **注记（文档自述 vs HEAD）**：`14b2c1923` 提交信息「991 补 migration_metadata 自建」在 HEAD 已不成立（上游字节替代）；runner.go:1974、db-changelog:11101 均已带合流注记说明，台账行 SHA 已更新为 HEAD 实测值（本轮 `shasum` 复核：990=`e3ae272c…`、991=`eb5fec70…` 与台账/通道指纹逐字一致）。台账行的旧描述（`14b2c1923` 版）与新描述并存的历史在 git 里可追溯，SSOT 语义清晰。

## 4. 投递门测试（`internal/sqlguard/migration_990_991_delivery_test.go`，136 行，对齐 980 七断言形态）

钉住（每迁移 7 条）：
- 990（L23-78）：① 幂等形态（ADD COLUMN IF NOT EXISTS，防指纹重放 42701/42P16）；② 对账视图在场；③ `SET LOCAL lock_timeout`（锁预算护栏）；④ `VALUES ('990',` 自登记；⑤ embeddata 副本逐字一致；⑥ docker 通道 files 数组登记；⑦ update_session_summary clobber 链尾（并显式注明「链尾是守卫要求，不是活库终态体语义」——触发器已死）。
- 991（L80-147）：① 三函数在场；② 自登记 + **负向断言**（活体 `INSERT INTO migration_metadata` 必红——把首版 42P01 雷钉成防回归）；③ 锁预算显式声明「不适用」（纯函数无强锁 DDL）；④ `VALUES ('991',`；⑤ 副本一致；⑥ 通道 files 登记；⑦ legacy 指纹清单登记。
- 实跑：`go test ./internal/sqlguard/ -run '…990…|…991…|Registry…'` 绿（本轮）。门的价值＝把 §2 的七面里「脚本可断言的面」变成 CI 红/绿，登记断链不再靠人记。

## 5. 探测桶退役 sargable 二修

演进：旧形态 `date_trunc('minute', r.ts …)=m.bucket`（非 sargable）→ 一修 `d3f7ce95e` 改 `r.ts >= m.bucket AND r.ts < m.bucket+1min`（单侧 sargable）→ 生产证伪（211 每 tick 跑满 2min ctx cancel、游标停摆 68min+ 死亡螺旋）→ 二修 `491ebeb25` 两侧同时有界。

(a) **两侧同时有界**：HEAD `bg/stats_minute_probe_rollup.go:191-212` —— m 侧 `m.bucket < $1 AND m.bucket >= $1 - interval '2 hours'`（L195-196）；r 侧全局窗 `r.ts >= $1 - interval '2 hours' AND r.ts < $1 + interval '1 minute'`（L199-200）+ 原区间谓词保留（L201-202）。211 EXPLAIN ANALYZE：m 侧 pkey 759 行 + r 侧 idx_ts 3341 行 = 61.6ms（无界 >20s，约 300 倍）。判定：**已实现**；生产证伪过程在代码注释（L160-181）完整留痕（含一修被证伪的原因：候选无界时 planner 仍 Seq Scan 建 Hash Anti Join，sargable 谓词沦为 Join Filter + 哈希溢盘 Batches 8）。

(b) **补窗封顶**：`statsRollupCatchupCap = 30min`（`bg/stats_minute_rollup.go:33-39`）；`rollupCatchupBounds`（L42-56）把停摆后的补窗钳到 [lastTS, lastTS+30min)，游标推进到 **until（处理终点）** 而非 now（L194-203）⇒ 缺口**顺延不清零**（下一 tick 从 until 续跑），不会漏桶；rollup 本身整桶替换幂等，重叠安全。封顶值 30min = resweep span（生产已验证能装进 tick 预算的最大窗）。测试 `bg/stats_minute_rollup_catchup_test.go` 钉四态（正常/恰线/停摆 3h/时钟倒挂），实跑绿。retire 侧 2h 窗口与 30min 封顶的互洽：游标追平后候选窗回归 [now-约2min-2h, now-约2min)；停摆恢复首 tick 覆盖 [lastTS-2h, lastTS)，更老幽灵桶交 TTL。

(c) **等价性**：
- 区间改写本身（date_trunc→半开区间）：**已证明**——一修提交引用 211 只读双探针，近 3 天 26,523 组桶三元组两形态集合完全一致（only_old=0/only_new=0），且桶全部分钟对齐（前提成立）。
- 二修的两侧有界：**结构性论证成立**——候选桶 ∈ [since-2h, since) ⇒ 任何匹配源行必 ⊆ [m.bucket, m.bucket+1min) ⊆ [since-2h, since+1min)，r 侧全局窗不排除任何潜在匹配（数学上无漏配）。但**没有跑等价性对拍探针**（一修有 26,523 组对拍，二修只有 EXPLAIN 计划/耗时证据）——按窗口数学论证已足够，此处如实标注「窗口内等价=论证；全历史等价=未证明」。
- **有意的行为收窄（非缺陷，登记在案）**：旧形态会退役**全历史**无源桶；新形态只退役 [since-2h, since)。超过 2h 的幽灵桶改由 TTL 硬天花板清理——但 TTL 口径三方漂移见 §9-D。

## 6. probe_hot / probe_minute 残留非 sargable 查询（grep 全仓）

逐点核查全部触点（`grep session_turns_probe_hot|request_stats_probe_minute` 全仓 23 文件，剔除测试/嵌入副本）：
- `bg/stats_minute_probe_rollup.go:94`（rollup WHERE `r.ts >= $1 AND r.ts <= $2`，sargable；L80 的 date_trunc 只在 SELECT 列表算桶，不进 WHERE，不致全表扫）；retire 语句 HEAD 已两侧有界（§5）。
- `bg/partition_manager_probe_lane_ttl.go:67-82`（TTL 批删 `ts/bucket < now() - make_interval(...)`，函数包的是 now() 不是列 ⇒ sargable，且 ORDER BY ts LIMIT 批删）。
- `admin/probe_lane_read.go:336-342`（bucket 范围）、`:379-388`（ts 范围 + 可选等值）——sargable。
- `admin/request_detail_probe_fallback.go:53-56`、`internal/trace/trace.go:496/621`——按 `request_id = $1`（主键，`sql/objects/tables/session_turns_probe_hot.sql:20`）。
- 其余为 INSERT/UPSERT（`domains/stats/minute_flush.go:64-77`、`internal/probelane/persist.go`）或开关/存在性检查（`internal/probelane/ready.go`）。
- sql/objects 视图函数中的 EXTRACT 全部命中 model_probe 族（不同表族），与 probe_hot 无关。
- 结构测试 `bg/stats_minute_probe_rollup_test.go:176-191` 把「date_trunc 禁用 + 区间形状 + 两侧有界」钉成断言（实跑绿）。

**判定：未发现残留非 sargable 形态。** 索引面齐备：`session_turns_probe_hot` 有 ts/stage_ts/tenant_ts 三索引（sql/objects/tables/session_turns_probe_hot.sql:53/60/67）。

## 7. 981 台账多行登记惯例（bc6c36695 / e0ef81187）

- 背景：`edb297f6b`（并行车）曾把 981 撤出全部投递通道并写台账行「未就绪·已撤出」+旧 SHA 83eb1ae；会话分析列车随后整件重写 981（真库对齐版，SHA 4b6c5fc3）。
- `e0ef81187`（01:09）：台账**补新行保留旧行**（同文件两行：83eb1ae 撤出行 + 4b6c5fc3 重写行，行内记因）——符合 `internal/sqlguard/migration_registry_divergence_test.go` 头注所述 730 先例（「原登记行 … 保留」）与多 SHA 必须消歧的门（markers 清单 L55-60）。
- `bc6c36695`（01:28）：把旧行**改写成事实**（applied+verified + 重写后 SHA），理由是旧行内容与现实相反（写着「已撤出」而重写件已六面登记并真实落地 211/245）⇒「台账是投递了什么的 SSOT，写反了等于让下一轮以为没这回事」。
- 终态（HEAD）：台账仅一行 981（`docs/db-changelog.md:11099`，SHA `4b6c5fc3…`），与文件实测 SHA 逐字一致（本轮复核）；旧行被收敛掉——**与 730「保留旧行」惯例有偏差，但可辩护**：83eb1ae 从未在任何库应用（撤出在先），不属于「投递过什么」，保留只会制造分歧行；divergence 门只要求「多 SHA 必须消歧」，单行准确态满足。通道侧 981 六面全在（runner.go:1966、main.go:1817-1820/2470-2471、tsv:398、通道 :2774 + 产物探针 :3594 + fingerprint 管理内建）。
- 判定：**已实现**，惯例执行轨迹（先补行后收口）在 git 历史可完整追溯；`TestRegistryShaIsActuallyTheFile` 门实跑绿。

## 8. 0d32aebe5 / 02cec493f（迁移登记视角）

两笔只动 `bg/materialized_view_refresher.go` + 测试 + 主档（02cec493f 另带 `web/public/menu-config.json` 构建戳）。**不触碰任何登记面**（sql/、installer/、tsv、通道、db-changelog 零改动）。迁移登记视角结论：
- 它们是 981 产物（4 个物化视图）的**消费方接线**（SessionAnalyticsViews 清单扩容 7 项 + 成本重排 + 串行刷新防 53100）。981 本身七面登记齐备（§7），不存在「视图清单引用未投递对象」的耦合缺口。
- 刷新器有 `skipped_missing_view` 守卫（`bg/materialized_view_refresher.go:348/388` 注释：357 未应用的库不报错）⇒ 未应用 981 的库上清单多 4 项不致 42P01。
- 视角提示（非缺陷）：该清单是**第五个**「迁移产物 → 消费方登记」面（视图建好不接刷新器 = 2026-10-07 245 事故形态，注释 L414-424 已自引先例）；981 的登记清单（§2 七面口径）未把「刷新器清单」算作登记面——若未来再有建物化视图的迁移，此处是第八面候选。

## 9. 缺陷清单

- **P2（B-1）TTL 口径三方漂移**：`bg/partition_manager_probe_lane_ttl.go:33` `defaultProbeLaneTTLDays = 7`（2026-11-17 从 14 收窄）；`settings/spec_lifecycle.go:48` 同键 spec `Default: 14` 且 DescriptionLong 自述「两者必须一致（14）」；`bg/partition_manager.go:648` 注释写「默认 14d」、`bg/stats_minute_probe_rollup.go:152/220` 注释写「7d TTL」。`getPlatformInt`（settings/helpers.go:63-81）在 spec 在场时走 `EffectiveValue` ⇒ **运行时生效默认=14（spec），Go fallback=7 只在 spec 缺失时生效**。后果：§5(b)「更老幽灵桶交 TTL」的实际天花板是 14d 而非代码注释宣称的 7d；spec 自述的「Default 即 fallback 宿主、必须一致」契约被 2026-11-17 的收窄破坏（fallback 改了、spec Default 与描述没跟）。修法二选一：spec Default 降 7 或 fallback 回 14，并把四处注释统一。（该漂移发生在 11-17，早于审计窗口 1 天，但直接决定本轮二修的兜底语义，故计入。）
- **P3（B-2）990/991 无台账↔产物对账探针**：`scripts/apply-db-revision-sequence.sh:3495-3594` 的 `ledger_artifact_probe` 覆盖 971/972/974/975/976/977/978/981，**无 990/991**。二者都在 `legacy_content_replays`（:2862-2863），首跑走「content replay (legacy, registered)」分支并进 `sha_matched_markers`——正是该机制要保护的对象；若重放因故未产出对账视图/三函数，无探针响亮失败（只能靠读侧 42P01 兜底）。建议补：990 → `to_regclass('public.stats_consistency_check')`；991 → `to_regclass('public.utc_day_start' 函数存在性)`。
- **P3（B-3）二修等价性对拍缺席**：§5(c)——区间改写有 211 双探针对拍，两侧有界只有结构性论证 + EXPLAIN；建议合入后补一轮「无界旧形态 vs 有界新形态」在准生产库的集合对拍（一次性，成本≈一修同款探针），把「论证」升级为「实测」。
- **P3（B-4）990/991 无 .down 文件**：embeddata 与 startup 目录均无（对照 980/981 均带 .down）。991 纯增量函数、990 幂等加列，回滚需求低；但与近邻迁移的惯例不一致，登记即可。
- **P3（B-5）990 新增列短期恒空（已登记待收口）**：sessionv2mirror 写入器不喂 total_credits_charged/avg|min|max_ttft_ms（`internal/sessionv2mirror/session_summary_dim.go:107-114` 实读核实）⇒ 在 Go 侧收口前，对账视图的 credit/TTFT 对照列恒 0/NULL。迁移头/通道注释/台账三处一致如实登记，非隐瞒缺陷；列在「遗留登记建议」。

## 10. 遗留登记建议

1. **B-1 升 P2 工单**：统一 `lifecycle.session_turns_probe_hot_ttl_days` 的 spec Default 与 Go fallback（连带四处注释），加一条「spec.Default == Go fallback」的结构测试（现有门只测 clamp 域，不测一致性）。
2. 补 990/991 的 ledger_artifact_probe（B-2），随下次动 apply-db-revision-sequence.sh 的列车顺带。
3. session_summaries 积分/TTFT 聚合缺口（B-5）：在 sessionv2mirror 写入器收口，或在对账视图注释「此三列暂无写方」——避免下一位读视图者把 0 当真值。
4. 981 台账的「撤出→重写」历史只存于 git：建议在 `docs/db-changelog.md` 981 行补一句「首版 83eb1ae 幻写 schema 版从未投递」的消歧短语（一行成本，杜绝未来考古误读）。
5. 二修等价性对拍（B-3）可并入下一次 211 部署验证轮，复用一修探针形态。

## 附：本轮实跑的门禁（全部绿）

- `internal/sqlguard`：990/991 投递门 + Registry SHA/divergence 门。
- `installer/cmd/llm-gw-installer`：通用 embed 同步门（≥849 全量）+ 锁预算门（cutoff 870）。
- `installer/internal/dbinit`：startup manifest（tsv↔StartupFiles）门。
- `bg`：TestRetireProbeMinutesIsBatched（date_trunc 禁用 + 区间形状 + 两侧有界）+ TestRollupCatchupCapsWindow（四态封顶）。
