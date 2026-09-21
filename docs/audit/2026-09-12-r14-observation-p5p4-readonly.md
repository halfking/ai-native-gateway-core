# R14 观察轮收尾 — 2084 上线观察 + P5 独立复核 + P4 根因（全只读）

日期：2026-09-12 02:03–03:10 CST · 观察窗：T+40min（24h 窗口刚开始，本轮为首个采样点）· 两节点运行 `2084-3c1195f2`（245 切换 02:03:45 / 154 切换 02:08:03，active 均轮换回 8781）· 本地已 fetch+merge origin/main 至 `aaf8d9edb`（含并行会话的 P5 修复 `8f4c13970` + 迁移 694，**未部署**）· **本轮零代码改动、零线上写操作、未使用 admin force_enable**

## A① staleness 归零保持 + auto-cool 首轮真实生效

| 项 | 154 | 245 |
|---|---|---|
| `failure log is stale`（2084 切换后） | **0**（修复前每周期触发） | **0** |
| `checkAlerts failed` | 0 | 0 |
| auto-cool trigger / credential cooled | 34 / 9（cred 18/19/8 各 3 次） | 2 / 1（cred 19） |
| `SQLSTATE 23505` | 6（promote 1 + hot cron 4 次重试内失败 + 2 次重试耗尽） | 10（promote 1 + hot cron 5 + 2） |

- auto-cool 触发负载带真实数据（`candidate_failure_logs_count_5m=5`，修复前 CTE 钉 0）——**d249604c7 读面修复在 alert/auto-cool 链路全面生效的直接证据**。
- 翻牌合理性：154 翻牌对象 cred 18/19/8，`failure_ratio=1.0`、`attempts_5m` 22~65；节奏为 cooling 2min → recover_at 到点翻回 ready → 仍 100% 失败 → 再翻（设计内短周期循环）。翻牌量 = 3 凭据（154）+ 1 凭据（245，两节点候选流量面不同），**全部是真故障凭据，无误伤**。DB 复核：cred 18/19 `cooling` + `auto_cool_high_failure_rate` + recover_at 在位。
- cred 41：`suspended/quota_permanent`，探针持续命中（02:44:28），等用户侧充值归属核对；cred 42 `ready/ok`。
- cfl 写入侧健康：`candidate_failure_logs_hot` max(ts) 距查询秒级、10min 32 行。

## A② 57014 频率（ensure 链 / vacuum worker / promote 三处）

- **网关侧（窗口 ~40min，权威口径）**：vacuum worker `VACUUM FULL failed` ×2（245 02:04:59 elapsed 30.3s；154 ×1）；`partition_manager: analyze stats failed` ×1（245 02:05:31）；ensure 链 0；promote 路径 0（其失败面是 23505 非 57014）。合计 ~3 次/40min，量级低，与 2083 首部署 ensure 超时同族（252 瞬时负载）。
- **252 服务端（环境受限）**：PG 容器 `logging_collector=off`，`docker logs`（json-file 驱动带轮转）实测仅保留约分钟级（首线 02:53:37），且全量 19.8 万行中 `canceling statement` 0 条——**服务端 24h 频率无法从该通道取得，如实记为环境受限**。`log_min_messages=warning` 本应记录 ERROR 级取消事件；如需服务端 57014 审计，需调大 docker 日志轮转或开启 logging_collector（运维建议，未动）。docker logs 中唯一 1 条 "57014" 命中系某应用存储文本中的子串，红鲱鱼。

## B P5 独立复核（只读）——并行会话根因与迁移 694 成立

origin 新增 `8f4c13970`（接线根除 + 694 自愈）/`d82c948aa`（门禁还债）/`aaf8d9edb`（审计文档）为并行会话产物，本轮为独立证据链复核，结论：**根因成立、694 方案完备、但尚未部署**。

- **git 考古坐实**：`d2cbaf88b`（08-26 merge）删除 main.go 的 `telemetry.SetClaimClient(telemetryClient)`（diff 第 179 行 `-` 行）；`git log -S` 确认该符号在 main.go 的变更仅两次（`06408163c` 加入、`d2cbaf88b` 删除），至 `8f4c13970` 前无任何提交重加——`6c5056ec7` 只修了 client.go 契约侧。守卫（promoted 分区 NOT EXISTS）自 08-26 起从未生效。
- **线上实证（02:45 只读查 252 PG）**：
  - 约束确认为部分唯一索引：`CREATE UNIQUE INDEX uq_request_logs_2026_09_final_success_session ON request_logs_2026_09 (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL AND gw_session_id <> ''`；
  - **conflict_pairs = 7**（hot 侧 TRUE ∩ 分区侧 TRUE，与 694 demote 谓词同构的只读复算），与并行会话口径一致，自其测量后未再增长（新冲突需 >8h 长会话二次成功，稀有）；
  - heap 分区 = 2026_07 / 2026_09 / 2026_10；hot 积压 **96,069 行**（较其记录 84,610 继续增长）、retention 外 TRUE 行 1,621。
- **一处叙述修正**：父表（裸表全分区）max(ts) = **09-09 19:35:29**，非其文档所记"冻结 17:16:27"。停摆期内发生过批次越迁——机制推断：双节点 promote tick 并发时，`FOR UPDATE SKIP LOCKED` 使后到批次跳过被锁（含冲突行）的头部行、取次 5000 行成功提交，冲突行本身永留 hot 头部。**"每 tick 必败"应弱化为"含冲突行的首批恒败、偶发并发越迁"**；根因结论与修复方案不受影响。
- **694 完备性挑战通过**：唯一需要排除的残余变体是"同会话两条 TRUE 同在 hot、同批 promote"——已被 hot 侧硬兜底 `uq_request_logs_hot_final_success_session` 唯一索引挡住（claim 时 23505 由 SAVEPOINT 隔离，不伤业务行）；因此任何时刻至多一条 TRUE 在 hot，demote 谓词（分区侧已有 TRUE 才降级）覆盖全部冲突形态。demote 仅触 retention 外行、仅动单列；partition_manager 与 data-lifecycle hot cron 走同一函数，双通道一并自愈。
- **部署状态**：两节点 2084 不含 8f4c13970/694，窗口内 23505 双通道持续（每节点 promote failed 1 次 + hot cron 重试耗尽 2 次）。随下一轮部署上线，观测要点：journalctl `demoted N hot final-success claim(s)`（预期 N≥7）→ promote 恢复 → 父表 max(ts) 追平 now-8h → hot 从 9.6 万排空。

## C P4 根因（只读梳理）——严重度上调：persist 落 PG 已停摆 5.7h+

- **现象量化**：窗口内 `ursm.v2: persist collect failed` 154=34 / 245=37（persist 间隔 60s ≈ 每 tick 必败）；`ursm_node_snapshot_min` max(snapshot_ts) = **2026-09-11 21:16:09 CST** → PG 快照面停摆 5.7h+（1,167,409 行止步）。redis 侧运行时状态本身不受影响；受影响的是快照落库、审计与重启恢复兜底面。
- **根因链（代码 + lua + DB 三方互证）**：
  1. `28f6afb65`（dual-schema record，L2 core）引入去重标记键 `<nodeKey>:request_dedup:<sha256hex>`，由 `record_request_dual.lua` 以 **`SET NX EX node_ttl`（默认 3600s）写成 STRING**，流量期常驻（每个请求的 DedupKey 哈希不同 → 键持续新生）；
  2. `persist.Collect`（writer.go）以 `SCAN <prefix>node:*` 全量扫描并把每个键当 hash 状态键读；
  3. legacy 语法 `ParseNodeKey`（keys.go）的模型段 **rejoin**（`raw = strings.Join(parts[2:], ":")`）把 `gpt-5.6-sol:request_dedup:<sha>` 整段"成功解析"为 RawModel——绕过了 ambiguous-key 排除（k2 语法严格、不会放行，泄漏路径仅在 legacy 分支）；
  4. `SafeHGetAll` 类型检查报 `expected hash, got string`（safe_operations.go TypedError）→ `Collect` 按 writer.go:105 的**刻意设计整批 abort**（"坏键不可从快照中静默消失"）→ main.go 跳过本 tick 的 Flush。
- **修复方案（下一轮实施，本轮不动代码）**：
  1. **推荐**：`Collect` 在 parse 之前显式跳过含 `:request_dedup:` 段的键（标记命名空间不是快照状态），附守卫测试锁"dedup 键永不到达 SafeHGetAll"；一行级改动、无需数据迁移、不动 lua；
  2. 备选：`ParseNodeKeyAny` 拒绝该后缀使其落入既有 ambiguous WARN 路径——会每 tick 刷 WARN 噪音，劣于方案 1；
  3. 结构性（可后置）：dedup 键移出 `node:*` 扫描命名空间，需双格式兼容窗口，收益不大于方案 1，暂不做；
  4. **保持 abort 语义不变**（未知坏键仍必须可见），仅精准豁免已知标记命名空间。
- **部署后验证口径**：`persist collect failed` → 0；`max(snapshot_ts)` 恢复推进。

## 验证分类（如实）

- **通过**：A① staleness 归零（双节点 journald）；auto-cool 真实生效（双节点 journald + DB cooling 状态）；P5 根因三方互证（git 考古 + 252 只读 SQL + 代码）；P4 根因链（代码 + lua + 快照水位）。
- **环境受限**：A② 252 服务端 57014 的 24h 频率（docker 日志轮转保留过短，非服务无错误）；245 `/etc/llm-gateway-go/env` 第 26 行 source 报错（`9527: 未找到命令`，`&` 未引号既有问题；所需 DSN 变量在其之前导出，psql 通道不受影响）。
- **未验证（不在本轮范围）**：694 上线后的积压排空效果、P4 修复后的快照恢复——均待下一轮部署。

## 遗留风险

1. 694 未上线期间 hot 积压以 ~1.4 万行/日量级增长，冷面（父表）持续缺数，视图 UNION 扫描面与 PG 压力随 hot 增大——**下一轮部署应尽快带上**。
2. ursm_node_snapshot_min 停摆期间若 redis 重启，窗口计数/节点状态无快照兜底可恢复——P4 修复宜与 694 同批。
3. cred 18/19/8 为 100% 故障面（读面修复新暴露的真实异常）：上游故障还是密钥失效待运营归因；cred 41 等充值（探针在位，严禁 force_enable）。
4. auto-cool 对 100% 死凭据的 2min 短周期循环，每 5min 仍烧 22~65 次失败尝试；cool_minutes 是否调优留运营判断（本轮不动参数）。

## 下一轮提示词（可直接使用）

> 请继续 llm-gateway-go 收尾——R15 部署轮（694 + P4 修复同批）。
> 工作目录：/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3
> 背景：两节点运行 2084-3c1195f2；origin 已含 P5 修复 8f4c13970 + 迁移 694（promote final-success demote 自愈），
> 独立复核与父表水位修正见 docs/audit/2026-09-12-r14-observation-p5p4-readonly.md；
> P4 根因已定性：persist.Collect 扫 node:* 误吞 request_dedup string 标记键整批 abort，
> ursm_node_snapshot_min 停摆自 09-11 21:16，方案=parse 前跳过该命名空间+守卫测试（文档 C 节）。
> 任务：① 按 docs/changelogs/2026-09-12-request-logs-promote-final-success-self-heal.md 的部署跟进
> 与 P4 方案实施代码改动（含守卫测试），本地 go test 全绿；
> ② deploy-seamless 上 245/154（首试失败重试一次即成的既有纪律）；
> ③ 部署后观测：demoted N（预期≥7）、promote 恢复、父表 max(ts) 追平 now-8h、hot 9.6 万排空、
> persist collect failed→0 且 max(snapshot_ts) 恢复推进；
> ④ cred 18/19/8 100% 故障归因（只读）；cred 41 仍严禁 force_enable；
> ⑤ fetch+merge 收敛、git diff HEAD --stat 甄别归属、menu-config.json 永不提交、绝不 force push。

## 关联

- 前置：docs/audit/2026-09-12-deploy-closeout-2083-bg-readsurface.md（读面修复 + P5/P4 立项与修订）
- P5 根因原文：docs/audit/2026-09-11-node-degrade-false-positive-fix.md §9；694 changelog：docs/changelogs/2026-09-12-request-logs-promote-final-success-self-heal.md
- P5/P4 立项：docs/audit/2026-09-11-deploy-closeout-2081-r12-p2p3.md
