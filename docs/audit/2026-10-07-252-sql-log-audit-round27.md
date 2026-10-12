# 252 PG SQL 日志审计第二十七轮（2026-10-07）—— compliance 列名跨表复制三个月根修（守卫钉死）+ 357 物化视图族补应用收口 analytics 503 + ursm 写入收敛实锤 + 股票族夜间爆炸定案

以 round26 §五.6 为起点。**起点重算**：开局本地 main=10f7d10af（=origin/main，干净），上一审计轮 R26=2026-10-06（证据保全层修复轮）；其间并行线产出 runbook §10.99-10.105 实测文档与 v1 退役门族（audit-gates-index 17 道）。**取证窗 = 2026-10-06 06:12:18 → 10-07 06:03:20（服务器钟，23.85h）**，取自轮转正本 `ctr.log-20261007` 字节偏移 40,498,370 起切 + 活 `ctr.log` 全量，433,151 行 / 230MB。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检：快照层连续产出 ✓ + copytruncate 每日轮转的 10 秒洞申报

1. **首查⑤ PASS**：`/var/log/pg17-ctrlog-archive/` 当日文件 `ctrlog-20261007.log`（64.4MB，mtime 05:47）+ `ctrlog-20261006.log`（199.5MB）连续，R26 修复的动态解析脚本自 10-06 06:57 起每小时产出无空转；snapshot.log 唯一边界记录 = 10-07 03:47:02（同容器哈希、cur_size 骤降 = 正确识别轮转）。
2. **轮转机制定案**：`/etc/logrotate.d/podman-ctr`（daily/maxsize 100M/**copytruncate**/rotate 3/compress）每天 ~03:45 轮转 `ctr.log` → `ctrlog-20261007`（262MB，10-05 15:49 容器启动起全量）。copytruncate 的复制→截断间隙产生 **03:45:02 → 03:45:12 约 10 秒洞（≈3 行：2 duration + 1 ERROR，按两侧文件尾首时间戳与 7,601/7,599、2,524/2,523 差值交叉证实），永久不可恢复**。这是 R22 稀疏洞家族的机制性重述：洞不是快照脚本的错，是 copytruncate 语义本身；快照脚本的界标行为正确。**纪律：凡经 copytruncate 通道的窗口审计，边界 10s 级缺口须显式申报而非假装全量。**
3. **三轴对账（纪律 66）**：raw ERROR 4,740 / 头锚定 2,524 / 提取器 records 2,523（Δ1=洞）；duration 7,601 / 提取器 7,599（Δ2=洞）；P 行 23,933 全部按纪律 67 拼接后解析。提取器首版重踩 R26 v1 同款坑（LOG: 双空格致前导空格、SLOW=0），三轴对账当即揭穿后修正。

---

## §一、错误族全景（2,523 条 / 23.85h；头锚定二段计数）

| # | 家族 | n | 归因与处置 |
|---|---|---|---|
| 1 | `no unique or exclusion constraint…ON CONFLICT`（ursm writer） | **1,155** | **R26 P1 移交项，本轮实锤收敛**：06:12→15:49:34 以 ~120/h 燃烧后**戛止**；库上实证并行 ursm 轨 ~15:49 将临时表重建为四列 PK（snapshot_ts, tenant_id, credential_id, raw_model_name），首行 15:50:10 起 14.4h 写入 1,921,795 行（~133k 行/h），窗口内零复发。**首查① 闭环**。残余：`ensure_ursm_node_snapshot_min_daily_partition(date) does not exist` ×4（10-07 04:28，830 仍未应用、函数仍缺，但普通表形态可用）；830 二选一仍归 ursm 轨拍板 |
| 2 | `canceling statement due to user request` | 621（26/h） | R26 20.5/h 同量级抬升（窗口构成+实验/实测窗贡献），应用侧取消已知容忍族。top 形状：候选 nps ×117、turns MAX(turn_no) ×107、selfcheck v1 臂 ×48、bodies promote INSERT ×45、request_status ×37——全是 R8 已知族 |
| 3 | `canceling statement due to statement timeout` | 273（11.4/h） | R24 夜间 9.4/h 同量级，probe 族已知 ✓ |
| 4 | E6 pocket `inconsistent types deduced for parameter $4` | 286（12.0/h 恒定） | **归因定案**：`UPDATE scheduled_tasks … enabled = CASE WHEN $4 = 0`（timestamp 参数与 0 比较）——scheduled_tasks **不在 llm_gateway 库**（跨库 pocket，R46 登记一致），ACC 侧 SQL 缺陷，本仓不可修，催办维持 |
| 5 | `column "total_detections" does not exist` | **98**（01:26→05h，仍在持续） | **本轮根修（仓内真缺陷）**，见 §四.1 |
| 6 | `relation "session_client_stats" does not exist` | 22（01:32→03:30） | **本轮补应用 357 收口**，见 §四.2 |
| 7 | `column "total_tokens" does not exist`（system_probe_runs） | 21（01:32→） | 外部客户端：`SELECT … SUM(total_tokens) FROM system_probe_runs`——仓内零 Go 引用、baseline 亦无该列，纯外部漂移；催办并入实测会话通道 |
| 8 | 单发杂项 ~55 | ~55 | 并行 runbook 实测会话自噪音（16:38-20:47、01:05-04:48 两窗，试错型查询：pg_stat_checkpoints ×2、round(double precision) ×5、indexrelname、indpred、z/w/ge1000 CTE、schema_migration_log、recommendations、pg_size_pretty(numeric,integer) 等）+ 04:50:49 猜表名四连（llm_usage_stats/llm_optimization_reports/llm_optimization_records 同秒） |
| 9 | FATAL 382 | 382 | `connection to client lost` 372（已知自噪音）+ parallel worker admin 终止 ×7（实测会话 kill 查询）+ 前端协议探测 ×2 |

**每小时直方图**：白天 150-176/h（ursm 燃烧期抬升，15:49 后回落 45-75/h）；01/03 点双峰 127/134 = compliance+session_client_stats+total_tokens 三族叠实测窗。**json_hex worker 路径零复发第 5 天成立**（窗口内 json 语法类错误 0 条，R25 判据继续成立）。

---

## §二、慢 SQL 全景（≥1s 共 7,599 条 / 25,796.7s / 23.85h ≈ 1,081 s/h；R26 1,088 s/h 持平）

| 形状 | n | sum(s) | max(s) | 判定 |
|---|---|---|---|---|
| **股票族（stock_basic/daily_kline/fundamentals）** | **3,253** | **7,508.6** | 44.3 | **夜间爆炸定案**：白天仅 4-18/h，**00:00-05:00 狂飙 196→510→581→592→626→511/h（≈每 6s 一次覆盖轮询）**——外部客户端午夜起跑的 kline 覆盖等待循环（`COUNT(*) stock_basic WHERE code IN (daily_kline MAX(date))` 三连 + `execute _pg3_3/_pg3_4` 预备语句）。daily_kline 在外部自有库（llm_gateway 库无此表），索引建议只能走催办；占本轮全部慢查时间 29%，**外部催办升级** |
| 路由候选族（cmb nps ×531 + WITH matched MATERIALIZED ×342 + mps ×…） | 1,601 | 6,191.1 | 29.8 | R8 已知族（matched CTE=provider/client.go:1631 形状测试在钉） |
| analyze_llm_gateway_table_stats('2') | 47 | 3,366.4 | **124.8** | **首查②不通过**（预测 <90s）：max 峰 19:53-00:01（101-125s）与 runbook 实测会话两窗（16:38-20:47、00:03-04:48）吻合——实验/实测窗没有按 R26 预期在白天消退而是延续到晚间。维持 R26 拍板（锁等待驱动、非数据增长），**消退判据顺延到实测会话真正收尾后**；窗口内 still-waiting 行 156 条 |
| credential_selfcheck v1 LEFT JOIN | 238 | 786.8 | 5.0 | R26 ~50 s/h→本轮 33 s/h，继续不立项 |
| REFRESH routing_analytics_7d | 143 | 677.7 | 14.8 | pss 累计 6,456 次/123,709.6s（24h 增量 +680s≈28 s/h，R26 27.7 持平）；11-02 复核判据不改 |
| WITH used（MAX(ts) usage 族） | 90 | 670.6 | 18.8 | 已知 |
| project_backfill（WITH targets） | 34 | 690.7 | 29.2 | R8 已知（R26 205→34 大幅回落） |
| **全库尺寸普查（pg_total_relation_size(c.oid)）** | **175** | **558.1** | 8.4 | **新归因**：`pg17-disk-watch.sh` 每 10 分钟自采样（144/天）+ bloat-check 零头 ≈175 ✓。自家监控的自噪音，登记为已知，不值得为消日志改采样 |
| 路由读臂 latest_bucket | 265 | 513.3 | 11.0 | pss +586 次/+444s/24h≈18.5 s/h（R26 26.6 回落），11-02 判据维持 |
| WITH win（credential COUNT FILTER） | 216 | 485.3 | 12.4 | 已知 |
| **v_probe_system_health（legacy）** | **36** | **452.9** | 26.8 | **新面孔**：admin/probe_dashboard.go:605 `loadLegacySystemHealth` 直读 legacy 视图，13-16s/查询，01:31-03:58 夜间被面板轮询 + 00:02 ×6 用户取消。**登记 P3**（legacy 视图重写或下线随 probe dashboard 演进走） |
| reportrollup（usage_facts JOIN stats_event_dedup） | 64 | 409.0 | 25.2 | 新形状登记：domains/reportrollup 报表域，14 s/h 量级，观察 |
| stats_rollup DELETE（request_stats_dim_minute date_trunc 区间） | 173 | 312.0 | 8.7 | rollup 已知族旁支 |
| WITH pick（DISTINCT ON 路由取优） | 122 | 308.3 | 7.8 | 已知 |
| **apihub 注册 upsert（kind, ref_id…）** | **76** | **163.8** | — | 新形状登记：apihub/pg_store 注册写入臂 2.2s 均值，观察不立项 |
| 夜间实测窗杂项（WITH q ×34/150.6s、request_logs 计数 ×26/186.3s 等） | ~60 | ~340 | 16.2 | 时窗与实测会话吻合，随会话结束消退 |

---

## §三、R26 移交项逐项复核（§五.6 首查①-⑤）

| R26 登记 | 本轮复核结果 |
|---|---|
| ① ursm writer 收敛 | **闭环**：§一.1 全套实证（15:49 四列 PK 重建、15:50:10 首行、1.92M 行/14.4h、零复发）；残余 = ensure 函数缺失 ×4 + 830 二选一待 ursm 轨 |
| ② analyze max <90s | **不通过（124.8s）**，重归因 = 实测窗延续到晚间；判据顺延（§二） |
| ③ modality 推进 | **闭环**：33→**60 行**（max checked_at 10-07 06:35:34，吞吐恢复）；decrypt legacy-envelope 催办随 252 网关重部署通道维持 |
| ④ capability last_tested 推进 | **闭环**：138 行持平（R26 已达），max last_tested_at 10-07 06:05:19 推进中 |
| ⑤ 快照层连续产出 | **PASS**：§零.1 |

**维持项**：路由 7d 读臂 18.5 s/h 回落（11-02 判据不改）；E6 12.0/h 节拍器；股票催办升级（§二头条）；10-11 03:15 vacuum-bloat 终验前置不变（本轮无新增风险）。

---

## §四、本轮处置与拍板

### 1. [根修·仓内] compliance 策略 GET 三个月 42703：prompt-injection 列名跨表复制

- **事实链**：`admin/output_compliance_handler.go` 的 `outputCompliancePolicyColumns` 选了 `total_detections, total_blocks, last_detection_at`——这三列属于**兄弟表 `prompt_injection_policies`**（baseline 2337 行）；`output_compliance_policies` 正典列名是 `total_checks/total_issues/total_redactions/last_check_at`，baseline 与 252 真表（64 列）逐列 diff **完全一致**。引入 = 4b09f56a8/b9a222c11（2026-07-09 "refactor(security): thin-adapter plugin…"，git log -S 实证）。**测试夹具自己建了带 `total_detections` 的迷你表**——夹具跟着中毒查询走而非跟着 schema 走，单测三个月全绿。252 窗内 01:26 起某网关二进制的合规设置页轮询 ×98（35/h），且写臂（INSERT/ON CONFLICT）用列全对——只有读臂中毒。
- **修复**：列清单/结构体/Scan 三处对齐正典列名（`TotalChecks/TotalIssues/TotalRedactions/LastCheckAt`，json 键同步 `total_checks/total_issues/total_redactions/last_check_at`——前端 OutputComplianceView.vue 只消费 `total_checks`，旧 JSON 键自端点 500 起就从未被消费过，改名零风险）；夹具列集同步修正。
- **守卫**：新增 `admin/output_compliance_policy_columns_test.go`——把列清单常量的每个裸标识对 **baseline schema 的 CREATE TABLE 列集**静态对账（表达式项排除）。**牙口验证**：临时回退 handler 到中毒版本，守卫精确点名三列 FAIL；恢复后 PASS。**真库实跑**：本机 llm_gateway 真实 schema 上旧查询复现生产 42703、修复版全列查询干净通过。
- **生效条件**：需网关重部署（154/245/252-dev）。**本轮不代部署**——并行 v1 线正在活跃部署（01:26 行为变化 + 故意红门族在推进），按纪律 59/61 避免部署窗相撞；已登记为下一部署窗首项。

### 2. [运维修复·已上线] 357 物化视图族补应用 252，session-analytics 503 收口

- **事实链**：252 台账 **从未有 357/358**（375 条，351/353/354 后跳空）——357 是「历史自守卫迁移未在存量环境应用」家族；admin session-analytics 端点按 R33 guard 设计以 42P01→503+引导报错，01:32-03:30 被使用 ×22。三视图（session_client_stats/session_task_stats/session_client_task_matrix）在 252 relkind 全空。
- **执行**：先做事务内演练（剥掉初始 REFRESH、ROLLBACK 兜底）验证列集无漂移——全绿（三视图 911/104,299/105,851 组、82s）；CONCURRENTLY-in-txn 先在本机库实证可跑；随后正式应用（60s、COMMIT、初始刷新干净）+ 台账补账 `('357', …) @06:32:48`。端点形状查询 `SELECT COUNT(*) FROM session_client_stats` = 911 行 ✓。358 的 matrix 唯一索引与 357 相同（357 独立自洽）。
- **登记缺口**：仓内**无任何 refresh_session_analytics_views() 调用方**——物化视图刷一次即定格，数据随时点老化（迁移注释「建议每小时或每日执行」无人执行）。473 族（建了却忘了接线）新例，移交运维轨。

### 3. [拍板] 其余

- E6 / total_tokens(system_probe_runs) / 股票族 = 外部，催办维持或升级，不代修。
- analyze 判据顺延；vpsh P3；census/disk-watch 自噪音登记。
- 运维轨仍开放（自 R24/R26 顺延）：disk-watch 4× 同秒 CROND、podman-pg-252 死信文件、log_size_max/journald 提案。

## §五、登记与移交清单

1. **[部署 pending·下一窗口首项]** compliance 根修提交随下一次 154/245/252-dev 部署生效；部署后以「output_compliance_policies 42703 零复发」为验收判据。
2. **[运维轨]** ① refresh_session_analytics_views 未接线（建议并入 252-monitor cron 每日低峰一次，需先过 cron 整文件覆盖契约登记）；② 358 未应用（触发器重写 + 回填，必须与现行 update_session_summary 血缘（713/747/822 演化）重新核对后再排窗口）；③ 830 二选一（ursm 轨）；④ vpsh legacy 视图 P3；⑤ disk-watch CROND/死信文件/journald（R24/R26 顺延）。
3. **[催办]** 股票族夜间 6s 轮询循环（29% 慢查时间）；E6 12.0/h；total_tokens 外部漂移。
4. **[终验节点]** 10-11 03:15 vacuum-bloat 首跑；11-02 路由 7d 复核。
5. **[下轮首查]** ① compliance 42703 是否归零（部署后）；② session_client_stats 42P01 是否归零 + 物化数据老化幅度（refresh 未接线后果）；③ ursm ensure 函数 ×4 是否复现（830 推进信号）；④ 股票族夜间循环是否仍在；⑤ analyze max 随实测会话收尾回落复核。

## §六、新纪律候选

- **68**：**测试夹具不得镜像被测 SQL 的列清单**——夹具跟查询走会把 bug 固化为绿；凡 SQL 列清单常量，必须另有一道对正典 schema（baseline DDL）的静态对账门，两者不一致即红（本轮 compliance 三个月潜伏的直接教训）。
- **69**：**「迁移已应用」不等于「关系存在」**——自守卫历史迁移可能从未在存量环境落地（357 三个月零视图）；对「台账有编号」的关系型依赖，首查必须 `pg_class` 实证 relkind，不能只看 schema_migrations。
- **70**：copytruncate 轮转通道的窗口审计必须申报复制→截断间隙的秒级洞（本轮 10s/3 行），并把它与快照脚本故障（R26 §零）区分开——前者是语义固有，后者是事故。

## §七、自审计

- **A1**：提取器 v1 重踩 R26 同款前导空格坑（SLOW=0），由三轴对账（duration 7,601 vs 0）当场揭穿修正；最终三轴：anchored ERROR 2,524=2,523+1（洞）、duration 7,601=7,599+2（洞）。
- **A2**：本轮自噪音 2 条已剔除并在此登记：06:38:39 `column "verified_at" does not exist` + `relation "daily_kline" does not exist`（本轮流数查询猜错列/表名，各 1 条）。
- **A3**：归因修正一次：`total_tokens` 最初因消息体含该字符串被误并进 cancel 族，改用 STATEMENT 全文通道后归因改正为 system_probe_runs 查询（外部）。
- **A4**：并行轨零触碰——ursm 表/830/832-833、v1 门族、runbook 实测会话均只读取证；357 应用与台账补账是该迁移在 252 的 designed ops 路径（guard 引导文案即操作说明），且先演练后应用、全程 ON_ERROR_STOP。
- **A5**：357 应用的 IO 影响 60s（低峰 06:31-06:32 执行）；演练 ROLLBACK 未留痕；台账 version='357' 与现有混名风格中的数字形态一致，后续 runner 若有线重放可被 ON CONFLICT 兜住。
- **A6**：admin 全套测试唯一 FAIL = `TestV1BodiesReadersAreAssessed`，其测试体自证「本门故意红」（并行 v1 线 2026-10-06 登记门），与本轮改动无关（改动面 = output_compliance 三文件 + 本守卫，grep 引用面先行核过）。
