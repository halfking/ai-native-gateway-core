# 252 PG SQL 日志审计轮·第十二轮（2026-09-28）

以第十一轮（docs/audit/2026-09-27-252-sql-log-audit-round11.md）§八提示词为起点：①五项对账延续 + claim 慢查/cancel 塌方归因（154 部署回验）；②FIX-C 部署回验；③G2 观察期 Day 6；④E6/E7/D15/D16/D17 移交项跟进。纪律沿用 ⑪-㉛。

## §〇、取证与起点

- 起点：origin/main = local HEAD = `3909d56e0`（12h 修订审计十六轮收口提交；本轮代码基线与其一致）。
- 窗口：2026-09-28 05:15:20 → 06:05:08 CST（50min）。ctr.log 头部时间戳 05:13:47——logrotate copytruncate 刚切过，窗口上界由截断点决定（取证先快照纪律照旧）。
- 快照：17,812 行 / 48MB（252:/tmp/pg252-r12-audit-0928-0604.log.gz，12MB；本地 /tmp/pg252-r12-audit-0928-0604.log）。**命名勘误**：本地 /tmp/pg252-round12-window*.log 是并行会话的无关文件（文件名撞车），非 PG 日志，不可引用。
- 解析：podman ctr.log → PG stderr 条目切分（r11 解析器复用）。总量：>1s 慢查 **211 条（sum 828.7s，max 42.9s）**、ERROR 22（user cancel 12 + pocket $4 10）、FATAL 22（conn lost 12 + smm 认证失败 10）。
- **部署身位（关键前置，均已实测）**：
  - **154 = `774ad643`-2282，2026-09-28 02:51:21 CST 切流（ps lstart 实证），ready:true——已含 D13**（`774ad643a` 为 main 上 09-27 merge 提交，908256008 即 round11 HEAD 为其祖先，ancestry 验证过）；
  - 245 = `774ad643`-2281（09-27，ready:true，含 D13）；
  - 252-dev = `a0e4d9c5`-2246（09-25，ready:false，未变——D6' 维持）。
  - pg_stat_activity 实测客户端：209(154)×21、241(245)×20、210(252-dev)×14。

## §一、对账（round11 161min 基线 vs 本轮 50min）

| 项 | 第十一轮 161min | **本轮 50min** | 评估 |
|---|---|---|---|
| claim UPDATE 慢查（D13 真形状） | ×234 | **0** | ✅ 塌方（§二归因） |
| claim UPDATE cancel | ×499 | **0** | ✅ |
| trace_events UPDATE cancel | ×452 | **0** | ✅ |
| cancel 总量 | 1,017 | **12**（全部 session_bodies upsert） | ✅ **-98.8%**（§二归因） |
| statement timeout | ×3 | **0** | ✅ |
| json hex（FIX-C 真形状） | ×33 | **0** | ✅（§三：FIX-C 已部署且落库） |
| index_hot DELETE >1s（FIX-2 09-25） | 0 | 0 | ✅ |
| REFRESH 超时（180s pin） | 0 | 0 | ✅ |
| policy/alternatives 取消（FIX-1 真形状） | 0 | 0 | ✅ 零回归 |

**归因**：154 于 02:51:21 携 D13（`uq_request_logs_2026_09_final_success_session` 部分索引 promoted 臂 Index Only Scan）切流——round11 遗留的"154 流量份额全扫 claim UPDATE（1.27M 行/次）+ 饱和放大 cancel 风暴"主源消灭。本轮 max 慢查 42.9s = promote_request_logs_hot_to_partition（其 SET LOCAL 60s 预算内，非击杀）。

## §二、D13 部署回验（round11 §二 收口）

154 部署回验 **PASS**：claim 慢查 234→0、claim cancel 499→0、trace_events cancel 452→0（50min 窗，历史三夜间窗口同量级流量形态）。round11 §一 的对账基线（234+499/161min）就此关闭。D13 三台全覆盖（154/245 均含，252-dev 旧版但其份额本轮零 claim 慢查——等待 D6' 重建窗口统一）。

## §三、FIX-C 部署回验（round11 §五 收口）

`774ad643` 包含 FIX-C（providerprofile jsonb []byte hex 三断点根修，57adc2343）。真库验证：

- `provider_profile_alerts`：total 888（round11 时 805），**details 非 NULL 60 行**（此前两个月 0 行）；
- `provider_events`：**60 行**（此前双库 0 行——序列缺失 + jsonb 双断点全灭）；
- 本轮窗口 json hex 报错 **0**。

FIX-C 三断点（alerts details / provider_events / reconciliation payload）生产闭环确认。

## §四、G2 观察期（GLOBAL_G2，本轮充当 Day 6 检查）

- 24h 窗（纪律㉛ 父表∪hot 并集口径）：claimed **1,554 行**，**漏镜像 0**。连续第 6 天 PASS（09-23 重起计 Day1-6 全绿），**earliest 09-29 收口**。
- outbox：现存仅 dead:147（09-23 前尸 体）；pending/claimed 空；**claim 源登记仍 0 行可观测**（D17 维持：D12 补偿登记要么秒级被 replay+trim 要么未触发，端态零漏镜像成立）。
- claim grant 率 1,554/24h ≈ 65/h（round11 22-50/h，继续上漂）——随流量放大，不阻塞 S4 前置，维持观察项。

## §五、本轮修复（R12-F1）：matview refresher 三实例错峰 tick 3× 放大——墙钟对齐根修

**发现**（本轮窗口 REFRESH 家族 = REFRESH 318s + 漂移核查 mv_data 99s ≈ **417s/3000s = 13.9% 墙钟**）：

15 次 `REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d`（单次 15-22s，sum 280.6s）+ 15 次 routing_audit_summary_7d（sum 37.1s）+ 30 次漂移核查（sum 99s）。tick 时刻呈**三个固定相位**：

```
相位 A：05:15:20  05:25:15  05:35:20  05:45:13  05:55:15   （+10min）
相位 B：05:19:12  05:29:13  05:39:11  05:49:12  05:59:11   （+10min）
相位 C：05:23:04  05:33:04  05:43:02  05:53:02  06:03:07   （+10min）
```

= 三台网关（154/245/252-dev）各持一个相位随机的 10min ticker，**每 tick 都真刷**。

**根因**：`bg/materialized_view_refresher.go` 的跨实例去重（Redis token-bucket 选举 + FIX-4 全局 advisory 互斥）都只是**重叠窗口内互斥**——token 在 refreshAll 结束即 Release、advisory lock 会话级用完即放。错峰的 tick（相隔 2-8min）到达时锁早已空置，`pg_try_advisory_lock` 次次成功。该去重设计只能消灭"并发叠跑"（FIX-4 的原始目标：252-dev 180s 饿死），从未实现头注声称的"exactly one gateway instance should redeem it"——09-21 日志（73 次 REFRESH/6.3h ≈ 11.6/h）与 round9-11 日志中该形状持续在榜，属**长期稳态浪费**而非新退化；30s 击杀时代被超时噪声掩盖，180s pin 落地后显形为纯负载。

**修复**（代码根修，零迁移）：`refreshLoop` 弃 `time.NewTicker`（相位=进程启动时刻）改墙钟对齐——新纯函数 `nextAlignedWait(now, interval)` 锚定 Unix epoch UTC 边界（CST=UTC+8 为 10min 整倍数，边界集一致），每轮重算等待防漂移自愈。对齐后三实例 tick 落在同一瞬间，既有 Redis/advisory 互斥随即把竞争收敛为**每窗口恰好一次 REFRESH**。首刷仍保留 InitialDelay+30s（部署日一次性错峰可接受）；时钟偏移超过单轮刷新时长时退化为旧行为（正确性无损）。预期：REFRESH 家族 13.9% → ~4.6% 墙钟（252-dev 重建前过渡期 ~9%）。

**测试钉**：`TestNextAlignedWait`（bg/materialized_view_refresher_test.go）——边界恰整返回全间隔（禁零长自旋）、中段返回余量、**同窗口双相位收敛到同一边界**（修复的核心性质）、等待 ∈ (0, interval]。bg 包全测绿（24.7s）、build/vet/gofmt 净。

## §六、登记（本轮不修，移交/顺延）

| 项 | 证据 | 处置 |
|---|---|---|
| **F2：autoroute 索引快照读 latest_bucket** | `WITH latest_bucket AS (…credential_model_index_with_current_month GROUP BY…)` ×54/50min（1-2.7s，sum 76s = 2.5% 墙钟）。触发 = 5min ticker ×3 实例 + NOTIFY 广播（同秒 3-5 连发）。EXPLAIN ANALYZE：CTE 双扫 union 视图 318K 行 ×2 = 297ms；**MATERIALIZED 变体实测 379ms 更慢，SQL 改写否决**（纪律⑳） | 维持观察。背景负载非关键路径（Decide 用内存快照）；若要根治需 (credential_id, raw_model, bucket DESC) 分区索引（DDL 归维护窗口）或触发端去抖，收益 <2.5% 不紧迫 |
| session_bodies upsert cancel ×12 | 聚簇 05:42-05:45（6 条），与 05:43/05:45 两轮 REFRESH+promote 重叠；round11 ×13/161min 同族 | **数据零丢失实证**：窗口内 turns（父表∪hot）缺 bodies 行 = 0（后续 delta flush 幂等兜回）。维持观察，无需修复 |
| E6：pocket $4 | `UPDATE scheduled_tasks … next_run_at=$4 … CASE WHEN $4=0` ×10/50min（~4.5min 节奏，round11 同率） | 维持移交 pocket 轨道（外部服务参数类型不一致，非本仓代码） |
| E7：smm 认证失败 | ×10/50min（round11 ×32/161min 同率） | 维持移交 kaixuan smm 轨道（stale 凭据客户端未清） |
| D15：board fallback COUNT(DISTINCT) | 本轮窗口零击杀零上榜 | 维持 admin 轨道移交（stats_usage_daily 语义断裂未修前挂账） |
| D16：provider_events 表形态漂移 | FIX-C 代码侧 max+1 已兼容，60 行正常写入；无 PK/无序列契约漂移仍在 | 控制窗口对齐表契约（DDL 不代执行） |
| D17：claim grant 率漂移 | 65/h（round11 22-50/h）；outbox claim 源 0 行 | 维持观察（§四） |
| 154 `llm-gateway-go-canary@8782` failed | 蓝绿切换后候选 slot 停在 failed 态 | 蓝绿残留（8781 现役 ready:true），下次部署时自然复位，登记备忘 |

## §七、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | latest_bucket 双扫 union 视图 → 加 MATERIALIZED 消一次扫描 | **EXPLAIN 实测更慢（379ms vs 297ms）**——planner 的 hash join 已最优，改写否决。纪律⑳（先 EXPLAIN 后改写）再次拦截一次想当然 |
| A2 | session_bodies cancel 聚簇 → 疑数据丢失查重试拓扑 | 先量化：窗口内 turns 缺 bodies = 0（delta 幂等兜回）。免于一次无的放矢的代码改动 |
| A3 | TestNextAlignedWait 首版"任意相位收敛同一边界"断言失败 | 测试写错非代码错：不同 10min 窗口的 tick 本就落不同边界（t2 在下一窗口）。收敛性质是**同窗口内**归一——修断言不改实现 |
| A4 | REFRESH ×15 是 09-27 后新管线引入 | round9-11 解析产物复核：r11 同窗口密度 0.6/min = 本轮 0.6/min，09-21 日志 73 次/6.3h 同量级——**长期稳态非新发**，归因改为"去重设计缺陷被超时噪声掩盖" |
| A5 | cancel 塌方想归因于"夜间流量低" | 慢查总量归一化 5.4/min(r11)→4.2/min(本轮)，同量级；且 154 切流时刻 02:51:21 实证早于窗口——流量假说排除，D13 归因成立 |
| A6 | 本地 /tmp/pg252-round12-window*.log 以为是前轮遗留物证 | 实为并行会话无关文件（内容是 Go 代码/方案文档）。快照改用带日期独立命名，文档中显式勘误防误引 |

## §八、下一轮提示词（建议）

> 以本文 §一 §三 §四 §五为起点。优先级：
> 1) **R12-F1 部署回验**：154/245 携墙钟对齐 refresher 部署后，对账 REFRESH 频次应从 3 次/10min 收敛到 1 次/10min（252-dev 重建前 2 次）；tick 相位应聚敛到同一边界秒级；
> 2) **G2 观察期收口**：earliest 09-29 满 7 天（父表∪hot 并集，纪律㉛）；D17 grant 率漂移顺带归因；
> 3) **252-dev 重建窗口（D6）**：max_wal_size/checkpoint_timeout + LogConfig 持久化 + /dev/shm 1g + D6' 修复 + D16 provider_events 契约对齐，一并做；
> 4) E6（pocket $4）/E7（smm）外部轨道回执跟进；D15 admin 轨道跟进；
> 5) F2 若后续要根治：分区索引 DDL 归维护窗口，触发端去抖为代码侧备选。
> 纪律沿用 ⑪-㉛ + 本轮新增 **㉜ 周期任务跨实例去重：用完即放的锁（token/advisory）只覆盖重叠窗口，去重错峰 tick 必须先对齐相位（或改持久租约 leader）**。

## §九、handoff 更新

- 本轮合入 main：R12-F1（bg/materialized_view_refresher.go 墙钟对齐 + TestNextAlignedWait）+ 本文档。无迁移、无 schema 变更。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第十二轮；`pg-252-sql-log-audit-facts` 增补（三相位 REFRESH 实证、锁去重只覆盖重叠窗口、FIX-C/D13 部署回验数据、G2 Day6、F2 EXPLAIN 否决、session_bodies cancel 零丢失、并行会话文件名撞车勘误）。
- 原始物证：252:/tmp/pg252-r12-audit-0928-0604.log.gz + 本地同名解包、/tmp/r12sql_parse.py、/tmp/r12sql_parsed.json、/tmp/r12sql_g2.sql（252:/tmp 同名）。
- 部署状态：154=774ad643-2282（09-28 02:51 切流，含 D13+FIX-C，**不含本轮 R12-F1**）、245=774ad643-2281（含 D13+FIX-C）、252-dev=a0e4d9c5-2246（ready:false）。R12-F1 归代码轨道，随下次受控部署上线。
