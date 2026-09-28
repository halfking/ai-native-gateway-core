# 252 PG SQL 日志审计轮·第十三轮（2026-09-29）

以第十二轮（docs/audit/2026-09-28-252-sql-log-audit-round12.md）§八提示词为起点：①R12-F1 墙钟对齐 refresher 部署回验；②G2 观察期 Day 7 收口；③常规慢查/错误对账。纪律沿用 ⑪-㉜。

## §〇、取证与起点

- 起点：main HEAD = `ed70cb5a9`（本地 = origin/main）。R12-F1（`04910c8ce`）已随 09-29 凌晨部署上线（见部署身位）。
- 窗口：2026-09-29 04:23:35 → 06:01:49 CST（**98min**）。ctr.log 头部即 copytruncate 截断点。
- 快照：27,271 行 / 44MB（252:/tmp/pg252-r13-audit-0929-snapshot.log，本地同名解包）。**其中 26,695 行（97.8%）为转录污染**（§四 F1），真 PG 条目仅 577 条（/tmp/r13real.log，巨型语句截断）。**时间戳取证教训**：内容层 grep 到的"17:53 起点"是污染文本里的 git 时间戳，窗口必须以 podman 传输层前缀时间戳为准（新纪律㉝）。
- 解析：podman ctr.log → PG stderr 条目切分（r13 解析器重写，R12 解析器已不在本地）。总量：>1s 慢查 **356 条（sum 1,349.4s，max 45.6s）**、ERROR 37、FATAL 15。
- **部署身位（关键前置，均实测）**：
  - **154 = `e1b73e88`-2323，2026-09-29 03:20:18 CST 切流（ps lstart 实证），8782 现役 ready:true——含 R12-F1**（`git merge-base --is-ancestor 04910c8ce e1b73e88` PASS）；
  - **245 = `a8a34023`-2324，2026-09-29 03:27:51 CST 切流，8782 现役 ready:true——含 R12-F1**（ancestry PASS）；
  - 252-dev = `a0e4d9c5`-2246（09-25 起，**裸进程** pid 64980 `/opt/llm-gateway-go/bin/gateway` :8780，ready:false，不含 R12-F1）。**容器形态已消失**（docker/podman 均无 llmgo 容器）——D6' 主体从"过期容器"修正为"过期裸进程"。
  - pg_stat_activity 实测客户端：209(154)×25、241(245)×25、210(252-dev)×11。
  - 154 蓝绿角色翻转：R12 时现役 8781/canary 8782 failed；本轮现役 8782、8781 未监听。R12 §六 的"canary failed 残留"随本次切流自然复位。

## §一、对账（round12 50min 基线 vs 本轮 98min）

| 项 | 第十二轮 50min | **本轮 98min** | 评估 |
|---|---|---|---|
| claim UPDATE 慢查 / cancel | 0 / 0 | **0 / 0** | ✅ D13 零回归 |
| trace_events UPDATE cancel | 0 | 0 | ✅ |
| cancel 总量 | 12 | **14**（10×session_bodies 聚簇 + 4×散发客户端断开） | ✅ 同族稳态（§四 F1 聚簇归因） |
| statement timeout | 0 | 0 | ✅ |
| json hex（FIX-C） | 0 | 0 | ✅ provider_profile_alerts/provider_events 零复发 |
| index_hot DELETE >1s | 0 | 0 | ✅ |
| REFRESH 超时（180s pin） | 0 | 0 | ✅ |
| REFRESH 频次 | 3 次/10min（三相位） | **2 次/10min（双相位）** | ✅ **R12-F1 生效**（§二） |
| promote_request_logs | — | 5× sum 37.1s（avg 7.4s，60s 预算内） | ✅ |
| ANALYZE（stats） | 未上榜 | 5× sum 204.6s（27-45.6s，10min 预算内） | ✅ D7' 家族改善通道（R6 572s → ≤46s） |
| E6 pocket $4 | ×10/50min | ×20/98min（同率） | 维持移交 |
| E7 smm 认证失败 | ×10/50min | **×0** | 本窗口零发生，维持观察 |

## §二、R12-F1 部署回验 —— PASS（本轮核心交付）

154+245 于 09-29 03:20/03:27 携 R12-F1（`nextAlignedWait` 墙钟对齐）切流。98min 窗口 = 10 个完整 10min 窗，全部呈现**双相位**：

```
对齐臂（154+245 竞争收敛后）：启动 = :X0:00.000 整（边界），完成 = :X0:18-26（=边界+刷新时长）
  routing_analytics_7d   20 次完成时刻：04:30:24 04:40:21 04:50:25 05:00:21 05:10:18 05:20:19 05:30:19 05:40:21 05:50:22 06:00:18
  routing_audit_summary_7d 20 次：同臂 +3~6s（refreshAll 串行第二刷）
252-dev 旧相位臂（a0e4d9c5 未含 R12-F1，进程启动相位）：完成 = :X9:09-19 → 启动 ≈ :X8:52
  与 R12 §〇 相位 B（:X9:11-13）同源——该实例自 09-25 未动，相位未漂
```

- **对齐验证**：对齐臂 10 窗启动时刻全部钉在 :X0:00（完成时刻的 18-26s 散布=刷新时长方差），秒级相位收敛成立；
- **去重验证**：154+245 双实例同秒 tick，Redis 选举 + advisory 互斥把竞争收敛为**每窗恰 1 次**（非 2 次）——纪律㉜"对齐先于去重"获得生产实证；
- **频次**：2 次/窗 = R12 §八 预测的过渡形态（"252-dev 重建前 2 次"）逐字吻合；
- **墙钟**：REFRESH 家族 = analytics 440.6s + audit_summary 49.0s + 漂移核查（mv_data）40×120.9s = **610.5s/5880s = 10.4%**（过渡期 2×；252-dev 重建后预期 ~5.2%）。单次 REFRESH 15-27s（R12 15-22s，+对齐碰撞后竞争开销，可接受）。

## §三、G2 观察期 Day 7 —— S4 停写 gate 达标

- 本轮直查（06:05，REPEATABLE READ 单事务，父表∪hot 并集口径，纪律㉛）：**claimed_24h = 1,134，g2_leak = 0** → Day 7 PASS。
- 连续 7 天 PASS（09-23 重起计 Day1-7 全绿，探针流量放大背景下零复发）。
- outbox：pending/claimed 空、dead:147（09-23 前尸体不变）；claim 源登记 0 行（D17 维持：D12 补偿登记秒级被 replay+trim，端态零漏镜像成立）。
- **官方台账流程今日落账**：09:00 每日观察轮（automation-15b858fd）写台账 + 09:30 收口判定（automation-3497ecaa）标注达标并提醒删 cron——本轮直查为佐证，不抢跑台账。
- S4 停写 gate 观察期达标；停写触发评估归 S4 轨道。

## §四、本轮登记（网关侧零新缺陷，无代码修复）

| 项 | 证据 | 处置 |
|---|---|---|
| **F1：转录污染事件** | 04:23:35-04:27:53，某并行会话把 ZCode 会话转录当 SQL 灌入生产库 psql——3,722 条巨型 STATEMENT 日志（`''` 双写转义证实走 PG statement 日志通道）+ 474 条尾随，26,695 行/44MB（占快照 97.8%），伴随 broken pipe FATAL ×13；**session_bodies upsert cancel ×10 聚簇**与之重叠（另重叠 ANALYZE 45.6s + REFRESH）。**零丢失复证：窗口 17 turns 缺 bodies = 0**（delta 幂等兜回） | 外部日志卫生事件。教训固化：psql stdin 严禁喂非 SQL 内容；生产库操作须 guard。cancel 族维持观察（R12 同族 ×12 同结论） |
| **F2：smm 新负载** | `fundamentals` could not determine data type of parameter $2 ×2（05:55:18/05:57:03）+ `limit_up_temp` integer out of range ×1（05:55:48，INSERT ... VALUES $1-9）+ `daily_kline` COUNT(DISTINCT) 4.7s ×1。**全部实证位于 smm 库**（to_regclass 逐库扫描） | 移交 smm 轨道（与 E7 同轨）。05:55:48 起突发，疑其 cron/任务新增 |
| E6：pocket $4 | `UPDATE scheduled_tasks … next_run_at=$4 … CASE WHEN $4=0` ×20/98min（5min 节奏，同 pid 1891075 持久连接）；**DETAIL 升级为 "integer versus bigint"，char 135 = `enabled = CASE WHEN $4 = 0`**。写方非本仓（grep 铁证：全仓无 scheduled_tasks 写方；goalrun 的 lease_until 属 goal_runs 表） | 维持移交 pocket 轨道，签名信息补入 |
| E7：smm 认证失败 | ×0（本窗口） | 维持观察 |
| startup packet ×30 | 每 5min 整点 :03-05 周期性 TCP 探测（外部监控探活），良性 | 登记，无需处置 |
| autovacuum skipping ×11 | REFRESH CONCURRENTLY 持锁窗口跳过 vacuum/analyze，预期行为 | 无需处置 |
| ANALYZE ×5（27-45.6s，3.5% 墙钟） | 设计内：partition tick 末尾 + 5min cooldown + SET LOCAL 10min 预算（R5 FIX-3 原案）；vs R6 D7' 572s 持续改善 | 维持登记观察 |
| latest_bucket（R12-F2） | 84×/98min（0.86/min，R12 1.08/min 略降），sum 128.3s = 2.2% 墙钟 | 维持 R12 决策（EXPLAIN 否决改写，观察） |

## §五、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 头部 grep 时间戳 09-28 17:53 当作窗口起点（12h 大窗） | 实为污染转录文本中的 git log 时间戳。改用 podman 传输层前缀时间戳重定窗口（04:23 起，98min）。新纪律㉝ |
| A2 | REFRESH 仍 2 次/10min → 疑 R12-F1 失败 | 先核部署身位：154/245 均已携 R12-F1 切流，双相位 = 对齐臂 1 次 + 252-dev 旧相位 1 次，恰为 R12 预测的过渡形态。频次对账必须先对身位（新纪律㉞） |
| A3 | scheduled_tasks $4 错误疑本仓 goalrun 所写 | grep 全仓无 scheduled_tasks 写方；goalrun 的 lease_until 属 goal_runs 表（CREATE/UPDATE 路径核对）。外部铁证后才登记移交，避免误修 |
| A4 | ANALYZE ×5 ×40s 想当场调 cooldown | 预算内（10min）、D7' 家族在改善通道（572s→≤46s）、无 cancel/击杀证据——不动。避免无的放矢 |
| A5 | missing_bodies 首版 SQL 用 request_id 反查 session_id（子查询可能多行） | 改为 turns 直接携带 session_id 联 bodies（父表∪hot 双臂 NOT EXISTS）。修的是检查 SQL 本身 |
| A6 | llmgo-252-dev"容器消失"疑实例下线 | pg_stat_activity 仍有 210×11 连接 → 顺藤查到裸进程 pid 64980 :8780。D6' 主体修正为裸进程，防下次按"容器重建"方案扑空 |

## §六、下一轮提示词（建议）

> 以本文 §二 §三 §四为起点。优先级：
> 1) **252-dev 重建/重部署窗口（D6'）**：裸进程 `a0e4d9c5`-2246（ready:false）→ 携 R12-F1 新构建后 REFRESH 家族预期 10.4%→~5.2%（2 次/窗→1 次/窗，对账§二双相位应收敛为单相位）；同窗一并做 max_wal_size/checkpoint_timeout + LogConfig 持久化 + /dev/shm 1g + D16 provider_events 契约对齐；
> 2) **S4 停写 gate**：观察期已达标（§三），官方台账 09-29 09:00/09:30 落账后评估停写触发；automation-15b858fd 与 automation-3497ecaa 完成使命后删除；
> 3) E6（pocket $4 integer-vs-bigint，char135 CASE WHEN）/F2（smm 股票 SQL：fundamentals $2、limit_up_temp 越界、daily_kline COUNT DISTINCT）外部轨道回执跟进；
> 4) F1 教训固化：生产库 psql 操作纪律（stdin 喂脚本必须 guard 非 SQL 内容）。
> 纪律沿用 ⑪-㉜ + 本轮新增 **㉝ 日志取证时间戳一律取传输层（podman/docker 前缀）而非内容层 grep——内容层时间戳可能来自污染文本**；**㉞ 频次对账前先核部署身位——新旧代码混跑期"异常频次"常是预测中的过渡形态**。

## §七、handoff 更新

- 本轮合入 main：**仅本文档**（无代码改动、无迁移）。R12-F1 的部署回验与 G2 收口为本轮两项核心交付。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第十三轮；`pg-252-sql-log-audit-facts` 增补（转录污染事件与量级、252-dev 裸进程身位、154/245 新构建号、双相位收敛数据、G2 Day7、smm 库归属、传输层时间戳纪律）。
- 原始物证：252:/tmp/pg252-r13-audit-0929-snapshot.log + 本地同名解包（/tmp/pg252-r13-audit-0929.log）、/tmp/r13real.log（真条目）、/tmp/r13checks.sql（252:/tmp 同名）。
- 部署状态：154 = e1b73e88-2323（09-29 03:20 切流，含 R12-F1）、245 = a8a34023-2324（09-29 03:27，含 R12-F1）、252-dev = a0e4d9c5-2246 裸进程 :8780 ready:false（**不含 R12-F1，重建窗口唯一遗留**）。
