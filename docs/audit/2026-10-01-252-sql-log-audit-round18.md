# 252 PG SQL 日志审计·第十八轮（2026-10-01，R17 收口轮：columnar_old 清退 + 单族基线对账 + 冻结窗终账）

以 round17 §九为起点（起点重算：origin/main = bce8a9651；本轮 = R17 六项移交的 ①②③④⑤⑥ 全项落账）。纪律沿用 ⑪-㊷-㊹-㊺；本轮含**生产 DB 侧修正动作 ×2**（视图重挂、335 条 outbox 死行回放）。服务器时钟 ~11:0x CST。

## §一、任务①：_columnar_old ×3 复核后删除（R17 §九② 关闭）

**删除前复核（五向依赖扫描 + parity 复核）**：

| 表 | 尺寸 | 行数证据 | 依赖 |
|---|---|---|---|
| session_turns_2026_09_columnar_old | 639MB | 换壳完成时 parity=793,866（R17 §二.3）；本轮复核替换表 796,527（+2,661=恢复后正常增长） | 无 |
| session_turn_details_2026_09_columnar_old | 204MB | parity=781,611；替换表 784,285（+2,674） | 无 |
| candidate_failure_logs_columnar_old | 361MB | R74 时代遗留 | **2 视图依赖！** |

- 分区挂载（父/子双向）、FK（指向/持有）、序列所有权、函数体软引用：全部 0 命中。
- **新发现（换壳改名的 OID 跟随陷阱）**：`v_adaptive_probe_targets`（探活调度器活跃读路径，bg/model_probe.go:627）与 `v_candidate_failure_logs_diagnosis` 在库内引用的是 `candidate_failure_logs_columnar_old` 而非 live 父表——R74 时代换壳 RENAME 后视图透明跟到冻结旧表，此后一直读冻结数据。仓库正典（sql/objects/views/）引用的均为本名。
- **修正动作（先重挂、后删除）**：两视图 `CREATE OR REPLACE` 回仓库正典定义 → `pg_depend` 复核依赖已迁到 `candidate_failure_logs` → 冒烟（probe_targets=699 / diag_rows=172,060）→ `DROP TABLE` ×3 全部成功（**无 CASCADE 阻塞 = 硬依赖清零的硬证明**）。替换表健康：两父表 103+8 索引 indisvalid 全 true。
- live `candidate_failure_logs` 家族结构正常：4 个月度 heap 分区 + hot（heap）；hot 新鲜（max ts 10:39，1h 内 44 行）。

## §二、任务②：「名单=正典单族」基线对账（R17 §九③ 落账）

三方对账一致：

1. **DB 实态**：`columnar_insert_only_parents()` = `{routing_decision_log}`（R17 06:44 修复持续生效）；`enforce_columnar_trigger` 存在且 enabled='O'（ddl_command_end → fn_enforce_columnar_event_trigger）——「机制保留 + 名单正典」正是预期安全形态（触发器对真 insert-only 单族无害，复发源已拔）。
2. **仓库正典**：sql/objects/functions/columnar_insert_only_parents.sql 单族；sql/schema/01-schema.sql:904 同；healthcheck/heal/trigger 函数均经 parents() 间接引用、无硬编码名单；迁移链唯一提及 = 689（注释），**无任何迁移再断言 21 族**。
3. **宣告**：**「名单 = 正典单族」自本轮起为新基线**。存储轨道如需扩充名单，必须以其名义提交迁移/评审（R17 §九③ 的正式回答）；DB 手改一律视为漂移。21 族事故名单全文见附录 A，作为入档。

AM 全景基线快照：现存 53 个 columnar 关系（routing_decision_log 家族、credential_model_index/supplier_errors 等 insert-only 们、765 列存 bodies 家族、`test_columnar_new` 32kB 测试残留——下一轮可顺手清）。ensure 侧复核：usage_facts_{20260928..20261002,default} 全 heap，无 boot 复发源。

**说明**：本宣告是仓库侧+库侧的对账结论；「存储轨道确认」以其下轮复核签字为准，如实登记，不代替对方表态。

## §三、任务③：cancel 回落曲线 + 冻结窗业务终账（R17 §九④ 关闭）

**cancel 曲线（user-request，30min 桶）**：

```
03:30  2,818 ── 04:00  1,779 ── 04:30  1,196 ── 05:00  1,107
05:30    716 ── 06:00    734 ── 06:30    922 ── 07:00    474
07:30      0 ── 08:00      2 ── 08:30      0 ── 09:00      6
09:30      2 ── 10:00      3
```

statement timeout 分时：03h=61 / 04h=272 / 05h=206 / 06h=179 / 07h=36 / **08h 起归零**。结论：**完全回落**，恢复后 ≤12/h，且低于 R16 时代 208/h 旧稳态参考（该参考含当期探针级联，已不可比）。

**冻结窗（05:31-07:05，94min）业务终账**：

| 数据面 | 终账 | 对 R17 §四的修正 |
|---|---|---|
| request_logs | 窗内 709 条请求账完整（等长后窗 937 对照） | 无变化 ✅ |
| session_turns/bodies | 207 行 ts 落窗的 turns 现存于 hot：**118 行 t9 里程碑在窗内（窗内完成、插入重试至恢复后迟到落库）+ 87 行 t9 NULL（窗内客户端放弃/在途作废）+ 2 边界** | **「不可恢复」修正为「大部分迟到落库」**：完成请求的会话账基本追回，真实缺口≈放弃/作废的在途 |
| outbox | 窗内 enqueue=0（04:31-07:05 仅 1 行）——补偿登记与写事务同回滚，无行可重放 | 维持不可恢复（会话聚合增量子窗） |
| usage_facts | EventWriter 内存死信，量级未定 | 维持 R17 A5 |
| sessions 元数据 | 见下：R17 回放漏 cohort 335 条本轮已补清 | 修正 |

**新发现 N1（已修复）：R17 死行回放漏 cohort**。R17 §二.6 回放的 WHERE 是 created 04:13-05:32（606 条）；库内尚有 **335 条 dead（334 hook + 1 claim）created 03:43-04:13**——R17「dead 归零」不成立（sweep 时点即有 282+ 条已死在窗外）。错误全为 incident 期 `write turn: acquire request advisory lock: conn closed`（1 条 insert timeout），烧尽时点 07:00-07:14（36 条在 07:00 整分）+ 07:38 最后 1 条。**修正动作**：依 R17 先例重置 `status='pending', attempts=0`（335 条）→ reaper 幂等重放 → **数分钟内全部转 done 并被 done 清退通道清走**（今日队列终态空，all-time dead 仅剩历史遗留）。遗留登记：all-time dead=147 条、created 全部 2026-09-23（legacy cohort，非本轮与事故产物，处置留下一轮）。

**恢复面佐证**：sessions_2026_10 = 307（10:39 首查）→ 357（11:08）持续增长（R17 §九① 翻日验证 ✓）；turns 11:00-11:10 流入 33 行（≈200/h 档）；无 >1min active 查询。

## §四、任务④：E6/F2/E7 外部轨续期（R17 §九⑤ 落账）

| 项 | 签名 | 本窗（03:18→11:08） | R17 基线 | 结论 |
|---|---|---|---|---|
| E6 pocket $4 | `inconsistent types deduced for parameter $4 at character 135`（外部 JDBC） | 每小时 11-12 条，03-10 时合计 91 | 44 次/4h 窗 ≈12/h | **续期：稳态 12/h 无变化** |
| F2 smm daily_kline | 慢日志 `duration:` 含 daily_kline | 03-10 时合计 47 ≈6.3/h | 29 条/4h 窗 ≈7/h | **续期：稳态无恶化** |
| E7 smm 认证失败 | `password authentication failed` | 0 | 0 | 安静 ✓ |

## §五、任务⑤：名单防漂移加固项登记（R17 §九⑥ 关闭，实施留下一轮）

**现状缺口（双通道皆断，实证）**：

1. `columnar_healthcheck()` 设计上由网关启动调用（sql/scripts/phase-23-columnar-invariant/03-healthcheck-and-heal.sql 头注），但 **Go 侧零调用点**（grep cmd/ bg/ internal/ 零命中）——「启动检查」从未接线。
2. `scripts/columnar-daily-cron.sh` 仓库在位，但 **252 crontab/cron.d 未部署**。
3. sql/objects 无运行时 bootstrap 通道（objects 只进 installer dbinit 与迁移链，网关启动不复写）→ **库函数一经手改（如 21 族）即持久**，无任何机制回断正典。这就是 09-30/10-01 事故的接缝：漂移函数 × DDL 事件触发器 = 批量 AM 改写。

**加固处方（按优先序）**：

- **P1 启动对账**：网关启动尾段（迁移+objects 后）比对 `pg_get_functiondef('columnar_insert_only_parents')` 与内嵌正典（installer embeddata 已携带 01-schema）；不一致 → ERROR 日志 + metrics 计数 + （建议）自动 `CREATE OR REPLACE` 自愈——名单函数只有 4 行，自愈风险可控，且 heal/trigger 在正典名单下无害。
- **P2 每日 cron 部署**：columnar-daily-cron.sh 上 252 crontab（部署前确认其 heal 分支读的就是 parents()——是，则正典名单下天然安全）。
- **P3 契约测试**：realdb 型测试（仿 bg/sql_audit_realdb_test.go 通道）断言 DB 函数体 == sql/objects 文件；静态测试防止 heal/trigger 文件内出现硬编码族名单。
- **P4 入档**：21 族名单全文（附录 A）+ 本轮 AM 全景快照即台账；memory 已记。

## §六、新发现登记（下一轮候选）

- **N2（P2，开放）**：`v_adaptive_probe_targets.recent_passive_failures` **系统性恒 0**——视图子查询读 `candidate_failure_logs` 父表（=分区），而行 0-8h 滞留 hot（promote p_retention=8h），5 分钟窗永远扫不到。实测 sum=0（699 targets）vs hot 同期 ~300 条失败。探针被动失败计数自 341（07-05 引入 hot 通道）起死亡，probe urgency 降级（不致故障）。修法=子查询改 hot 或统一视图，涉调度语义需 owner 拍板。
- **N3（P3，开放）**：outbox legacy dead=147（全部 2026-09-23）——下一轮盘点：重放或归档销毁。
- **N4（P2，开放）**：§五 双通道断线实施（P1-P3）。
- **N5（P4，微）**：`test_columnar_new`（32kB）测试残留表，下一轮顺手清理。

## §七、纪律新增（候选 ㊻-㊼，强化㊱）

- **㊻ 换壳改名的 OID 跟随陷阱**：`ALTER TABLE … RENAME` 后视图透明跟随旧表（OID 引用），「删旧表前无依赖」的扫描必须在**重建视图之后**做；换壳通道（裸 LIKE + DETACH/ATTACH）的标准收尾应含 `pg_depend` 反查 + 视图正典重挂（802/㊷ 的视图面补充）。
- **㊼ 「最后一行=」式冻结断言不可靠**：turns/bodies 无 insert-time 列、全部时间戳是业务里程碑（ts/t0-t9），迟到落库会把窗内里程碑写进恢复后的表。冻结窗量化必须用队列/账本侧证据（outbox enqueue=0、错误窗、reqlogs 对照），不能只看目标表 max(ts)。
- **㊱ 强化**：podman 容器 /tmp 与宿主隔离——scp 到宿主后必须 `podman cp` 进容器再 `psql -f`（本轮又踩一次）；另 `pg_event_trigger`（事件触发器）不在 `pg_trigger` 里，查触发器先分类。

## §八、自审计（A1-A6）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | candidate_failure_logs promote 自 02:35 停摆（分区 max(ts)=02:35:54、hot 积压 283 行>30min） | **证伪**：函数签名 `p_retention DEFAULT '08:00:00'`——hot 行满 8h 才晋升；每小时批次正常搬「恰好过 8h 线」的行（63/2/108/66），02:35:59 后的行尚未到线。先读函数体再断言停滞 |
| A2 | R17「dead 归零」可信 | **修正**：其 sweep WHERE（created 04:13-05:32）漏了 03:43-04:13 cohort 334 条（sweep 时点即已烧死）；本轮补清零。跨轮引用的「归零」断言必须带 WHERE 窗口复核 |
| A3 | 沿用 R17「冻结窗 turns 不可恢复」 | **修正**：t9 里程碑分析证明 118/207 行系迟到落库；冻结断言的「最后一行=05:31:27」是插入序观察而非业务时间边界（㊼） |
| A4 | 我方自噪声（诚实账） | 本轮 DB 侧错误语句：`created_at does not exist` ×4（P3 三次执行 + 旧表 max(created_at) 探测——旧列存表无该列）、UNION arity ×1、indisvalid 列名 ×1、错误表名 ×1、psql 引号告警 ×2（无 DB 影响）。小时 10-11 观测 created_at ×6，其余 2 条疑似并行轨道同款探测，不计我方 |
| A5 | 探针视图 recent_passive_failures=0 归因 | 不虚构「正常」：实锤列死亡（N2），登记开放项 |
| A6 | 修正动作授权性 | 335 回放 = R17 §二.6 同款先例 + DB 已健康 + claim 守卫幂等；3 表 DROP 前五向依赖扫描 + 视图先行重挂 + parity 复核 + 冒烟，全部留痕本文；147 legacy dead 未动 |

## §九、下一轮提示词（建议）

> 以本文为起点。优先级：① N2 探针视图 recent_passive_failures 死列修复拍板（hot/统一视图二选一）；② §五 防漂移 P1-P3 实施（启动对账最优先，21 族事故即接缝）；③ outbox legacy dead 147（09-23）盘点处置；④ 与存储轨道对签「名单=正典单族」基线（我方已宣告，见 §二）；⑤ test_columnar_new 残留清理；⑥ E6/F2 外部轨继续按窗续期；⑦ 观察恢复后首个整点批次（12:00）与 sessions_2026_10 增长斜率是否达稳态。

## §十、产物与物证

- 本文档；服务器侧 /tmp/pg252-20261001-r18closeout-*.sql（oldtables / oldtables2 / viewfix-drop / baseline / promote / promote2 / freeze / houroly / outbox-dead / replay335，全走 Write+scp+podman cp 文件通道）。
- 关键复算路径：cancel 曲线与 E6/F2/E7 = `/var/lib/containers/storage/overlay-containers/<cid>/userdata/ctr.log`（568MB，覆盖 03:18→now；轮转 ctr.log-20261001 438MB 覆盖前窗）；335 回放终态 = `session_mirror_outbox` 今日队列空 + all-time dead=147（09-23）；视图依赖迁移 = `pg_depend` 查询（§一脚本 [3]）。
- 换壳 parity 数（796,527/784,285）与翻日分区（sessions_2026_10=307→357）可由 §一/§三脚本复跑复算。

## 附录 A：21 族事故名单（入档，2026-09-30 深夜→10-01 04:13 被错误触发器批量转列存的族群）

sessions、session_turns、session_turn_details、session_bodies、usage_facts、stats_event_inbox、credential_model_index、auto_route_selections、session_memora、session_censors、system_probe_runs、credit_ledger、tool_usage_stats、session_tools、session_module_executions、cache_metrics、dashboard_access_events、model_probe_runs、handoff_logs、supplier_errors、routing_decision_log（21 族；正典=仅 routing_decision_log）。
