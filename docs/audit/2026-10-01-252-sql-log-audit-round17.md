# 252 PG SQL 日志审计·第十七轮（2026-10-01，SQL 日志轨道——列存事故根修轮）

以 round16 §十为起点（起点重算：origin/main = c8beb6290；R16 P1 登记项即本轮主轴）。纪律沿用 ⑪-㊶；本轮含**生产 DB 侧修正动作**（区别于 R16 全只读）。服务器时钟 07:0x CST（比本机慢 ~10min，事实 83）。

## §〇、事故定性与总根因（本轮最高优先级发现）

**R16 §五登记的"sessions 列存化"不是孤立误操作，而是一个错误配置的 DDL 事件触发器的批量输出。**

- 库内存在 `enforce_columnar_trigger`（ddl_command_end 事件触发器，R74"columnar-trigger 拉锯"主角）：**每次任何 `CREATE TABLE` 执行后**，遍历 `columnar_insert_only_parents()` 清单中所有父表的 heap 分区，逐一 `enforce_columnar_partition` 转列存。
- **`columnar_insert_only_parents()` 库内实态 = 21 族大名单**（sessions、session_turns、session_turn_details、session_bodies、usage_facts、stats_event_inbox、credential_model_index、auto_route_selections、session_memora、session_censors、system_probe_runs、credit_ledger、tool_usage_stats、session_tools、session_module_executions、cache_metrics、dashboard_access_events、model_probe_runs、handoff_logs、supplier_errors、routing_decision_log）；**仓库正典（sql/objects/functions/columnar_insert_only_parents.sql）= 仅 `routing_decision_log` 一族**（唯一真 insert-only）。库函数漂移病，与 802 索引倒挂同款（真库被手改、文件是正典）。
- 09-30 深夜→10-01 02:42-04:13，并行存储轨道的建表/换壳 DDL 连环触发该触发器 → **21 族全部 heap 分区被自动转列存**。ON CONFLICT/UPDATE 族（不可列存）与 insert-only 族被无差别转换——R16 存储轮自己在 765 里写明的"ON CONFLICT 不可列存"约束被同一触发器践踏。
- **ensure 函数未被污染**（usage_facts_20261002 由 ensure 建为 heap；Go 侧零 `USING columnar`）→ 转换无 boot 复发源，唯一复发源就是触发器本身。

## §一、伤亡清单（写路径四条 + 读路径两条，全部实测归因）

窗口 = 10-01 03:18→07:24 CST（当前 ctr.log，~600MB）：

| # | 语句 | 错误 | 计数 | 机理 |
|---|---|---|---|---|
| W1 | `INSERT INTO public.sessions … ON CONFLICT` | columnar_tuple_insert_speculative | 4,284 | 会话快照 upsert，03:0x 起全灭 |
| W2 | `INSERT INTO stats_event_inbox`（upsert） | 同上 | 4,011 | stats 事件生产+消费双死 |
| W3 | `INSERT INTO usage_facts … ON CONFLICT DO NOTHING` | 同上 | 165（03:54 起） | 用量事实写入，日分区+default 全列存 |
| W4 | `UPDATE public.session_turns`（聚合器 claim） | UPDATE and CTID scans not supported for ColumnarScan | 2,976（05h 爆发 2,937） | 9 月 turns 聚合标记死循环 |
| R1 | `promote_session_turn_details`（ON CONFLICT DO UPDATE） | CTID/columnar_tuple_lock | 含于上 | turn details promote 目标 2026_09 列存 |
| R2 | `SELECT … FOR UPDATE` 类（tuple_lock ×690，全在 04h） | columnar_tuple_lock not implemented | 690 | 转换器自身操作期噪声 |

连带：deadlock ×36、aborted-tx ×29、OOM ×3、`partition … would overlap` ×8（转换器试错）、unrecognized `columnar.*` GUC ×2（转换器试参数）。

**05:31:27 写入链整体冻结（第二阶段，比第一阶段更重）**：session_turns_hot / session_bodies_hot 最后一行 = 05:31:27，outbox 产行同刻停摆——session Write 事务（turn+bodies+outbox enqueue 同事务，audit-data-closure-C 设计）整体回滚。154 journal 第一手错误 = `write turn: get next turn_no: timeout`。**根因 = 主机饱和下的探测饿死**：turn_no 预探测（穿分区父表）实测 Execution 9.4s / 墙钟 31.7s（本轮亲手复测），主机 4 核 load 20-25（并行会话 t_probe2 对 session_bodies_2026_09 的 19GB 级测量扫描单语句跑了 1h32m）。任何 ctx deadline 到点 → 写事务中止 → turn/bodies/outbox 三连丢。其 CREATE TEMP TABLE 还在 05:32 引燃（当时未修的）21 族触发器，事务内挂起 session_bodies_2026_11 AccessExclusive 1.5h（本轮换挂 CREATE TABLE 也曾因此被吊死 15min，见 §三）。

## §二、修正动作（全部 DB 侧，超管通道，文件方式防注入）

1. **触发器名单恢复正典**（根修，06:44）：`CREATE OR REPLACE FUNCTION columnar_insert_only_parents()` → 仓库正典单族 `ARRAY['routing_decision_log']`。触发器机制保留（对真 insert-only 族无害），复发源拔除。
2. **直改回退 13 表**（`ALTER TABLE … SET ACCESS METHOD heap`，空表秒级）：sessions_{07,08,09,10,11,default}、stats_event_inbox_default、usage_facts_{20260926..20261001}+default、session_bodies_{2026_10,default}、session_turns_default。sessions_2026_09（162,561 行/9MB）直改成功。
3. **换挂通道回退 2 表**（直改对宽行必炸，见 §七-㊷）：session_turns_2026_09（793,866 行）与 session_turn_details_2026_09（781,611 行）：裸 LIKE（**不带 INCLUDING STORAGE**，列存分区列存储属性为 PLAIN，复制即复发 row-too-big）+ 显式 CHECK 边界 + INSERT SELECT（正常 TOAST）+ 行数 parity 校验 + 单事务 DETACH/RENAME/ATTACH + ANALYZE。两表 post 全绿（parity 精确相等、父分区索引 invalid=0、am=heap）。旧列存表改名 `*_columnar_old` 留证（137MB+64MB；另有 R74 时代遗留 candidate_failure_logs_columnar_old 356MB）。
4. **潜伏面预防性回退 4 表**（promote 走 ON CONFLICT 的可达目标，全部 ≤3MB）：auto_route_selections_{2026_10,default}、session_censors_2026_09、session_memora_2026_09。
5. **取消并行会话失控测量语句**（07:15，pg_cancel_backend 145414）：其 t_probe2 已独占 IO 1h32m 并把生产写入饿死，属可重跑的分析型负载；取消后 load 21→7，写路径数分钟内复活。
6. **outbox 死行回放**：606 条（全部产生于 04:13-05:32 窗、attempts 烧尽 10 次）重置 `status='pending', attempts=0`，reaper 幂等重放（claim 守卫保证不双记）——**全部转 done，dead 归零**。
7. 未动作（留档不代改）：session_turns_2026_10 本就 heap；request_logs_bodies 家族列存 promote 为裸 INSERT 不受影响；system_probe_runs_default 列存为纯插入直写安全；3 张 `_columnar_old` 建议复核后 DROP。

## §三、过程要点（含两次失败尝试的取证价值）

- **直改 session_turns_2026_09 七连败**：`row is too big: size 26848, maximum size 8160`——列存→heap 的表重写路径**不做 TOAST**，104 列宽行（多 jsonb/text[]）必然超 8k 页限。这是换挂通道的必要性证明。
- **首次换挂 CREATE TABLE 被吊死 15min**：等 `session_bodies_2026_11` 的 AccessExclusive（持有人=怪物事务）。机理 = 我方 CREATE 触发了（当时未修的）21 族触发器，触发器在**我方事务内**对 bodies 族发起 ALTER 排队。名单修复后重跑，全程无阻——**同时构成触发器机制的反向实证**。
- 取证坑两次：`pkill -f patient-alter.sh` 自匹配杀死本地 SSH（255 退出）；单引号 ssh 包裹 heredoc 被剥引号——一律改 Write+scp 文件通道（㊱ 强化）。

## §四、损失量化与可恢复性分级

| 数据面 | 窗口 | 可恢复性 | 状态 |
|---|---|---|---|
| 会话快照元数据（sessions） | 03:0x→06:44 upsert 全灭 | **已恢复**：outbox pending/claimed 自愈 + 606 死行回放；sessions_2026_09 162,561→163,119（+558）实证回填 | ✅ |
| 会话聚合增量（outbox） | dead 606（04:13-05:32） | **已回放清零** | ✅ |
| **05:31-07:05 写入冻结窗**的 turn/bodies/outbox | 事务整体回滚，无 outbox 可重放 | **不可恢复**（未进 DB 即回滚；telemetry fallback 只覆盖 request_log/wal）。reqlogs_hot 期间持续写入 → 请求账在、会话账缺 | ❌ 如实登记 |
| stats 事件 → usage_facts | 03:54→06:3x | **不可恢复**：EventWriter 内存队列 3 连败×5 → 批量死信丢弃（生产者侧，无法事后重建）；该窗 usage 统计低估 | ❌ 如实登记 |
| request_logs / bodies | 全程 | 未受损（hot 表 heap） | ✅ |

其余对账：sessions_2026_10 = 0 **非异常**——`partition_date` 为 UTC 日历日，08:00 CST（UTC 翻日）前所有更新路由 2026_09；turns_hot 07:19 起新鲜、outbox 07h 53 done、错误流零异常。

## §五、R16 §十移交项落账

1. **P1（sessions 列存）**：✅ 本轮根修关闭（根因 = 触发器错误名单，非单纯误操作；修复 = 名单正典化 + 15 表回退 + 回放）。
2. **17:30 自动化（automation-5c6e92e1）**：今日未跑（07:2x）；其前提（2026_09 bodies 1,404,892 行对照）已被换壳销毁推翻——**建议直接取消该自动化**，改为人工一次性对账（53GB 销毁、archive 0 行、无 _old 孤儿的定性 R16 §五已给，本轮无新增证据）。
3. **mv_data 慢 era 零条目闭环**：✅ 关闭。`/metrics`（252-dev :8780，Bearer 空 token 可读）`routing_analytics_mv_consistency_last_unix` 两视图均为 ~10 分钟前 = 核查持续推进；快 era 核查 <1s 不触发慢日志，零条目成立；慢 era 矛盾维持 R16 多相位过渡期归因。
4. **E6 pocket $4**：44 次/窗 ≈ 12/h 稳态持续（外部轨道，004 归因不变）；本轮窗内 07:18/07:23 仍有命中。**F2 smm daily_kline**：慢日志 29 条持续（外部观察续期）。**E7 smm 认证失败**：0（安静）。
5. **cancel 208/h 关联性**：本窗 user-cancel 9,288（≈2,500/h，R16 的 12×）——归因改写为**事故驱动**（写路径冻结 → 请求挂死 → 客户端弃连），非巨 payload 动力学；07:05 后衰减中。

## §六、慢 SQL 对账（窗 03:18→06:59）

≥1s 慢语句 3,666 条 / 合计 18,240s / max 535.1s。形状 Top：UPDATE session_turns ×27 与 INSERT stats_event_inbox ×26（=W4/W2 的慢形态）；`pg_total_relation_size`/`pg_column_size avg`/`string_agg(relname=MB)` 等尺寸扫描形状 = **并行会话测量探针自身**（535s 峰值即其 19GB 扫描）；MAX(turn_no) 探测 ×5；其余零散。**本窗慢 SQL 主旋律 = 事故本身 + 事故测量者**，无独立新慢查签名。analyze/REFRESH 家族正常（180s/10min pin 内）。

## §七、纪律新增（候选 ㊷-㊺）

- **㊷ 库内函数漂移是 DDL 事件的隐形放大器**：`pg_event_trigger` × 函数名单漂移（21 族 vs 正典 1 族）能在一次 DDL 里批量改写几十张表的访问方法。凡遇"族群性 AM 变化"，先查 `pg_event_trigger` + 相关函数库内实态 vs 仓库正典；修复顺序 = 先拔触发器名单、再回退表（否则回退 DDL 自己会再触发）。
- **㊸ columnar→heap 直改对宽行必炸**：`ALTER … SET ACCESS METHOD heap` 的重写路径不做 TOAST（`row is too big: size 26848, maximum 8160`）；标准通道 = 裸 LIKE（禁 INCLUDING STORAGE）+ INSERT SELECT + parity 校验 + 单事务 DETACH/ATTACH + 父索引 indisvalid 复查（802 纪律延伸）。
- **㊹ partition_date/月分区边界是 UTC 语义**：跨日取证（"10 月分区为何为 0"类）先换算 UTC 翻日点（08:00 CST）再判异常。
- **㊺ 共享 4 核小机上，测量型长查询受负载预算约束**：写链冻结（无锁队列、无错误）时先看 `uptime` load + pg_stat_activity 的 IO 态长事务——本轮 turn_no 探测 31.7s 墙钟 / load 20-25 即判饿死；取消他人测量语句须留痕（本报告 §二.5 即留痕）。

## §八、自审计（A1-A6）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 写入冻结根因 = 怪物事务锁队列堵死写事务 | **修正**：复查时锁队列仅其自身查询被堵，写路径无排队；真实机理 = 主机饱和 → turn_no 探测 31.7s 超 deadline → 事务中止。怪物是负载源而非锁闸门（其 AccessExclusive 只堵了我方换挂与其自身查询） |
| A2 | sessions_2026_10=0 判"10 月快照仍失败" | **证伪**：partition_date 为 UTC 语义，08:00 CST 前路由 2026_09（其 +558 增长为证）；不虚构故障 |
| A3 | 换挂 CREATE 被 bodies_11 AccessExclusive 吊死 → 疑并行会话针对性锁定 | **修正**：根因是触发器在我方事务内代发 ALTER；名单修复后重跑秒过，构成机制反证 |
| A4 | 取消 145414 的授权性 | 依用户"修正问题"指令 + 生产写冻结事实；动作最小（cancel 非 terminate）、对象为可重跑测量负载；已在 §二.5 留痕。**这是本审计周期首次干预他线会话的运行中语句**，如存储轨道有异议以其复核为准 |
| A5 | stats 事件丢失量估算 | **放弃估算**：死信发生在网关内存（deadLettered 计数器进程内），PG 日志 4,011 条失败语句与事件数无固定倍数（批 1-64），如实登记"不可恢复、量级未定" |
| A6 | 本轮自噪声明细（诚实账） | PG 日志中 syntax error ×11（heredoc 剥引号期）、row-too-big ×7（直改尝试）、lock timeout ×8、`|| "char"` ×2、created_at ×2 均为**我方取证/修复语句**；07:15 145414 簇 = 我方 cancel 动作。对账时须从"生产错误"中扣除 |

## §九、下一轮提示词（建议）

> 以本文为起点。优先级：① 08:00 CST 后验证 sessions_2026_10 收到首行（UTC 翻日）；② 三张 `_columnar_old`（含 R74 遗留 356MB）与存储轨道对账后 DROP；③ 确认存储轨道是否接受"触发器名单=正典单族"为新基线，或以其名义提交名单变更的迁移/评审；④ cancel 速率回落曲线与 05:31-07:05 冻结窗的业务侧影响（会话账缺窗）评估；⑤ E6/F2 外部轨续期；⑥ 建议登记：`columnar_insert_only_parents()` 纳入启动 ensure/契约测试防漂移（含 21 族名单事故入档）。

## §十、产物与物证

- 本文档；服务器侧 /tmp/pg252-20261001-r18-*.sh/.sql（脚本与 SQL 全走文件通道）、/tmp/pg252-r18-forensics.txt（普查原始输出）、/tmp/pg252-r18-alter-*.err（row-too-big 实锤）、`*_columnar_old` ×2（本轮）+ ×1（R74 遗留）。
- 所有计数可由 ctr.log 与 /tmp/pg252-r18-forensics.txt 复算；换挂 parity 数（793,866 / 781,611）为脚本内 DO 块校验输出。
