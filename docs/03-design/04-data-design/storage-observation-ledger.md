# 存储优化 v2 —— S2→S4 停写 gate 观察台账（7 天零漂移）

> 依据：storage-optimization-plan.md §4-S2「对账 7 天零漂移」、§8-C/D。
> 观察起点：**2026-09-15**（本地网关 2.5.4.2113 / git 6622249e，dual-read 端点含 NULL↔0 归一化）。
> 每日一轮：`scripts/audit/storage_observation_round.sh`（端点抽样 + gate 分类），结果逐轮追加到本文末尾。

## gate 定义（单轮 PASS 判据）

硬指标（抽样会话内全部须为 0）：

| # | 指标 | 口径 |
|---|---|---|
| G1 | 字段漂移 | matched request_id 上 tokens/cost/success/credits 归一化（NULL↔0）不等，即端点 `token/cost/success/credits_drift_count` |
| G2 | 终态 v1 行缺镜像 | `only_in_v1` 且 v1 侧 `is_final_success=true` |
| G3 | 近期 turns 单侧行 | `only_in_v2` 且 ts ≥ 采样时刻−48h |

已知例外类（不计 gate，逐轮归因登记）：

| # | 类别 | 依据 |
|---|---|---|
| E1 | `only_in_v1` 非终态行 | 镜像链只入终态条目（mirror hook 设计），v1 的 `is_final_success=false` 行属预期缺失 |
| E2 | `only_in_v2` 历史行 | 观察起点之前的历史窗口差异（本机登记：09-10/09-11 时代行，v1 侧今日已无从对照；request_logs 无全局 TTL，成因为回填时代产物/历史清理，不属当前镜像漂移） |
| E3 | sys 合成会话 `v1_rows=0` | D4 设计：基表 request_logs 中探针行 `gw_session_id=NULL`，按 `sys:%` 基表对账天然失配；兼容视图已做 `sys:%`→NULL 保真。sys 会话对账走 §8-D 计费合计口径 |
| E4 | **shadow-write 丢失行**（v1 终态行缺 turns） | 镜像为 best-effort：hook 2000ms 预算超时/并发槽满 → in-process backlog（不落盘、进程重启即丢、无后台重放器，spec §12 GAP 2）。**这不是"例外"，是 G2 全局扫描要量化的阻塞项**——见下文 Round 1b |
| E5 | cost 精度漂移 | `session_turns.cost_usd` 为 numeric(12,6)（R29 勘误：行内原误记 14,6），镜像写入时对 v1 的 numeric(14,8) 舍入（实测 0.00001870→0.000019）。修复 **713**（原编 711 与并行线 hosted_tasks 撞号，R29 重编号）：turns cost 列 14,6→14,8 + 双写期窗口回填；修复前 G1-cost 按 ≤1e-6 绝对容差判等 |

S2→S4 停写 gate：**连续 7 个自然日每日一轮 PASS**（用户类 G1=G2=G3=0；sys 类按 E3 豁免 G2/G3、只查 G1）+ **GLOBAL_G2 全局扫描=0** + §8-D credits 合计等值。

## 逐轮记录

### Round 1 —— 2026-09-15 00:55 (+08)，gateway 2.5.4.2113 (git 6622249e)，**PASS**

分层抽样 10 会话（业务多轮 4 / 回环单轮 3 / sys 合成 3），端点 `GET /api/admin/sessions/{id}/dual-read?limit=5`：

| 会话 | 类 | v1/v2 行 | only_v1 | only_v2 | G1 字段漂移 | 归因 |
|---|---|---|---|---|---|---|
| gw_7a19bfa5… | biz_multi | 583/703 | 31 (E1) | 151 (E2) | 0/0/0/0 | only_v1 全部 is_final_success=false；only_v2 全部 09-10/09-11 时代 |
| gs_gw_7a19bfa5… | biz_multi | 172/177 | 31 (E1) | 36 (E2) | 0/0/0/0 | 同上 |
| gw_98bfbafe… | biz_multi | 73/68 | 5 (E1) | 0 | 0/0/0/0 | only_v1 全部 is_final_success=false |
| gw_8dd3d88c… | biz_multi | 54/50 | 4 (E1) | 0 | 0/0/0/0 | 同上 |
| gw_c843cea2… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| gw_fb953e57… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| gw_24fb3e8f… | loop | 1/1 | 0 | 0 | 0/0/0/0 | zero_drift=true |
| sys:probe:cred35:20260914 | sys | 0/1181 | 0 | 1181 (E3) | 0/0/0/0 | D4 设计失配 |
| sys:probe:cred11:20260914 | sys | 0/1090 | 0 | 1090 (E3) | 0/0/0/0 | 同上 |
| sys:probe:cred50:20260914 | sys | 0/358 | 0 | 358 (E3) | 0/0/0/0 | 同上 |

- G1=G2=G3=0 → **PASS**（7 天计第 1 天）
- 备注：①本日早些时候在旧二进制（2110，16:28 构建）上首调抓到的 cost/credits"漂移"为占位零值伪影——NULL↔0 归一化修复（a789a05ad，16:48 提交）晚于该镜像构建，重部署 2113 后归零；②outbound_body 停写复测（S1b）：2026-09-14 16:32:24 开关 PUT 后父表+hot 违规均 0（§6 的 1.8GB/月收益计入，详见 plan §4.2-10 闭环）。

#### Round 1 官方脚本输出（storage_observation_round.sh，2026-09-15 01:06 +08 / 17:06Z，10/10 PASS）

```
sid|class|v1|v2|only_v1|only_v2|G1(tok,cost,succ,cred)|G2_final_missing|G3_recent_v2only|verdict
gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|589|708|32|151|0,0,0,0|0|0|PASS
gs_gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|173|178|31|36|0,0,0,0|0|0|PASS
gw_98bfbafe-3a03-48e5-a747-2e4b1ccfbc34|biz_multi|73|68|5|0|0,0,0,0|0|0|PASS
gw_8dd3d88c-1cca-4a40-b47d-801c7026a7bd|biz_multi|54|50|4|0|0,0,0,0|0|0|PASS
gw_24fb3e8f-f370-4a35-ae44-863f327137d1|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_c843cea2-354d-42eb-bd1a-324204c79afe|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_fb953e57-c9f2-409c-ab05-42110d6508bc|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
sys:probe:cred35:20260914|sys|0|1185|0|1185|0,0,0,0|0|1185|PASS
sys:probe:cred11:20260914|sys|0|1092|0|1092|0,0,0,0|0|1092|PASS
sys:probe:cred50:20260914|sys|0|362|0|362|0,0,0,0|0|362|PASS
ROUND_RESULT|sessions=10|fail=0|verdict=PASS|at=2026-09-14T17:06:20Z
```


### Round 1b —— 2026-09-15 01:31 (+08)，全局 G2 扫描（Round 1 补充），**FAIL → S4 前置阻塞项确立**

会话抽样之外补充全局扫描（已固化进 storage_observation_round.sh 的 GLOBAL_G2），结果远超抽样可见面：

```
GLOBAL_G2|v1_final_missing_turns_24h=258|verdict=FAIL
```

- **规模**：近 24h v1 终态行 6956 条中 258 条缺 turns 对应，**损失率 3.71%**；258 条 = 258 个不同会话各缺 1 轮（单轮会话整会话缺席，sessions 表亦无行——首轮 shadow write 失败即全会话丢失）。
- **时间分布**：全天连续（1~14 条/时），事故时段激增——09-14 02h=30、13h=29、**14h=75**、15h=31（14-15h 为 S1b 首次 cutover 失败/回滚窗口，进程重启清空 in-process backlog + DB-less 窗口批量丢）。
- **根因**（代码定位）：`internal/sessionv2mirror/hook.go:129-151`——写预算 2000ms 超时或 8 槽信号量满时条目入 `appendBacklog`；`internal/sessionv2mirror/backlog.go:16-25`——backlog 仅进程内存（cap 10000，不落盘），注释自证"drain via DrainBacklog or a **future** background replayer (spec §12 GAP 2)"——重放器至今未实现。每次容器重启（本窗口内有 09-14 16:30 cutover、09-15 00:51 重部署、01:15 外部 2118 重部署三次）backlog 全量蒸发。
- **影响**：credits/cost 随行丢失 → §8-D 计费等值与 D7「计费事实迁移先行」**不可能在该写路径现状下达标**；S4 停写 gate 冻结，直至：
  1. **S4 前置（新立工作项）**：GAP-2 落地——backlog 持久化（DB 表）+ 后台重放器，或 turn 写入 outbox 化（复用 session_aggregate_outbox 模式）；
  2. 重放器上线后回补现缺口（v1 终态行反查回填 turns），GLOBAL_G2 归零；
  3. 之后 7 天零漂移观察期方正式起算（本轮 Round 1/1b 的会话抽样 PASS 记录保留作基线，但天数计数**从重放器达标日起算**）。

### E5 实证（Round 1b 抓到首个 G1-cost 漂移样本）

`gw_c8879719…/13747961fb123b68f911b080aa146b25`：v1 `0.00001870` vs v2 `0.000019`（turns 列 numeric(14,6) 写入舍入）。处置见例外类 E5 行。

### Round 2 —— 2026-09-15 03:56 (+08)，GAP-2 闭环后首轮，**PASS —— GLOBAL_G2 归零，7 天观察期正式起算**

前置闭环（2026-09-15 02:30-03:56，commit 6df4de85d，本机部署 git_sha=6df4de85 / build_seq=2118）：

1. **GAP-2 落地（方案对比定案：失败路径 outbox 化）**：新迁移 712 建 `session_mirror_outbox`（pending→claimed→DELETE|退避|dead 状态机、request_id 唯一幂等、RLS 对齐 630）；hook 两条失败路径（8 槽满/写失败）改为持久登记（payload=完整 RequestLogEntry JSON，独立 500ms 预算、不占 8 槽，登记失败降级 in-proc backlog 保持 GAP-2 前行为）；重放器（internal/sessionv2mirror/replay.go，FOR UPDATE SKIP LOCKED + 指数退避 + claim lease，gate 与实时 hook 逐条一致）随网关启动。全量 outbox 化方案被否：session_aggregate_outbox 的 payload 是 SessionUpdate 聚合快照、重放不写 turns，无法承载；且 100% 写两遍与存储优化目标相悖。payload 自带全量事实，S4 停写后重放不依赖 v1。
2. **历史回填**：`scripts/audit/mirror_outbox_backfill.sql` 把 7 天窗口 v1 终态缺 turns 行（父表∪hot 双侧）按 entry json tag 投影灌入同一张表，reaper 无差别消化。**546+3 行全部回补，outbox 清空**。工程注记：本机 PG 对「RLS 分区父表 UNION ALL 多表组合」触发 `invalid perminfoindex 0 in RTE with relid 0` planner 错误，脚本改为 RLS/分区表逐张单表物化临时表后在临时表间合成；psql autocommit 下 ON COMMIT DROP 需 BEGIN/COMMIT 包裹。
3. **711 迁移应用**：`session_turns.cost_usd` numeric(12,6)→(14,8)（cost_display 为 double precision 无需动）；被 710 视图（request_logs_with_current_month，rule _RETURN）与 640 视图（session_turns_with_current_month）依赖，迁移内 DROP 后分别由 db.ensure（canonicalV2DDL 单一契约源）与 640 同款体内联重建。配套 `turns_cost_precision_backfill.sql` numeric 直拷回填 4651 行，**双写期 cost 漂移清零**（remaining_drift=0），G1-cost 的 ≤1e-6 临时容差自此不再被触发。
4. **并行线干扰实录**：外部并行线在本轮部署窗口（03:11 起，commit 2aa80828=build_seq 2118 撞号）持有构建锁并覆盖 8782；其运行期（03:43-47）按 GAP-2 前行为再产生 3 行缺失，已被本轮幂等回填兜回。撞号与 bundle 互踩再次印证：对账/验收前必须核对 /healthz 的 git_sha（本轮 6df4de85），勿信 build_seq/tag。

本轮结果：

```
sid|class|v1|v2|only_v1|only_v2|G1(tok,cost,succ,cred)|G2_final_missing|G3_recent_v2only|verdict
gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|597|715|33|151|0,0,0,0|0|0|PASS
gs_gw_7a19bfa5-27a1-4137-bd58-0f1dceb35c23|biz_multi|175|180|31|36|0,0,0,0|0|0|PASS
gw_98bfbafe-3a03-48e5-a747-2e4b1ccfbc34|biz_multi|73|68|5|0|0,0,0,0|0|0|PASS
gw_8dd3d88c-1cca-4a40-b47d-801c7026a7bd|biz_multi|54|50|4|0|0,0,0,0|0|0|PASS
gw_8c1e23e2-ca3c-495b-83e4-512b534bdda4|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_f37a2732-6b41-4071-8ebe-b6a42f0e1fd2|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
gw_c8879719-4fd3-49fd-b890-78587758d01f|loop_single|1|1|0|0|0,0,0,0|0|0|PASS
sys:probe:cred35:20260914|sys|0|1745|0|1745|0,0,0,0|0|1745|PASS
sys:probe:cred11:20260914|sys|0|1532|0|1532|0,0,0,0|0|1532|PASS
sys:probe:cred50:20260914|sys|0|450|0|450|0,0,0,0|0|450|PASS
GLOBAL_G2|v1_final_missing_turns_24h=0|verdict=PASS
ROUND_RESULT|sessions=10|fail=0|global_g2=0|verdict=PASS|at=2026-09-14T19:56:52Z
```

- **GLOBAL_G2 首次归零**（24h 窗口 0/6900+），7 天零漂移观察期**自 2026-09-15 起算**，达标 earliest 2026-09-22。
- biz_multi 会话的 only_v1=33/31/5/4 与 only_v2=151/36 为非终态行（is_final_success=FALSE 的 in_progress/失败占位行，端点不分终态故显示差集；SQL 侧 G2/G3 分类均 0）——与 Round 1 一致，非漂移。
- E5 例外自此关闭：cost 双侧 14,8 精度对齐，G1-cost=0 漂移实证。
