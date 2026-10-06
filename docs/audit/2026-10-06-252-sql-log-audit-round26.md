# 252 PG SQL 日志审计第二十六轮（2026-10-06）—— 证据保全层停摆 14h 根因闭合与修复（PG 容器被删除重建）+ 提取器四轮返工定版（全局变量不变量）+ ursm v2 并行轨半成品 P1 移交 + 832 探测错误自愈销案

以 round25 §五为起点。**起点重算**：开局本地 main=212ffc658（含未推送三提交），`git fetch` 后 origin/main=e6b97aab3 领先（并行线 audit-48h R48 收口轨，触碰 cmd/gateway、bg/、apihub/、admin/ 等约 60 文件）；工作区有 5 个脏文件（VERSION/version.json/db-changelog/menu-config/web version.json = 并行 154 部署线 build_seq 2472 身份工件，全程原样保留未提交，纪律 61）；本地与 origin **真分叉**（merge-base=1008a5b05），收尾时对账合并。**取证窗 = 2026-10-05 13:16 → 10-06 06:12（约 17h，跨 PG 容器重建事件，分段标注）**。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检：证据保全层停摆 14.3h —— PG 容器被删除重建，快照脚本硬编码哈希阵亡（P0 运维，本轮修复）

纪律 60 三件先验（06:02 实测）：归档目录只有 `ctrlog-20261005.log`（14.48MB，mtime 10-05 15:47），**10-06 归档不存在**——快照自 10-05 15:47 后停止产出，而 offset 状态文件 mtime=05:47（cron 在跑但空转）。

### §零.1 事件时间线（全部实证）

| 时刻（服务器钟） | 事件 | 证据 |
|---|---|---|
| 10-05 13:23 | R25 部署快照脚本，`CONTAINER_LOG` **硬编码**容器 `3131c977…` 的 ctr.log 路径 | 脚本原文 |
| 10-05 14:46:55 | 旧容器 PG 日志尾行（此后静默，无慢查/错误产生） | 归档尾内容 |
| 10-05 15:40:00-02 | 网关仍与 PG 正常交互（REFRESH 锁裁决 + auto index refreshed 要求活连接） | journal |
| 10-05 15:46:31 | 网关开始 `connection refused`（5432 无人监听 = PG 容器已停） | journal gateway[3933448] |
| 10-05 15:46:35 | root SSH 登录（117.147.54.78，ED25519）+ aliyun-assist 并发活动 | journal sshd |
| 10-05 15:47:01 | 快照 cron 照跑：stat 死路径失败→CUR=0→`CUR<OFF` 判「悬崖」打界标→**offset=0**，此后每小时空转（offset 反复写 0，归档零字节） | 归档内界标行 + offset 文件 |
| 10-05 15:48:35 / 15:49:42 | **Podman API Service 两次 socket-activation 启动** = 有客户端经 podman socket 删除并重建容器 | journal systemd |
| 10-05 15:49:41 | 新容器 `dc5e5427…` 启动（restarts=0 = 显式重建非崩溃重启），PG 15:49:43 起服务 | podman inspect |
| 10-05 15:50:53 | 网关自愈（health check 重启、probe queue revived 8），此后 `connection refused` **0 条** | journal |

- **DB-less 窗 = 15:46:31 → 15:50:53（约 4.4 分钟），网关自愈，无残留。**
- **日志损失面**：旧容器日志已保全至 15:47:01（归档 14.48MB，覆盖 01:54 悬崖后全部）；15:47-15:49 容器停机无日志；**新容器 15:49:42 起的日志（06:09 时 40.4MB）抢救性全量外抄**至 `ctrlog-20261006-container-switch-gap.log`，随后修复版脚本首跑又全量回填 41.5MB 至 `ctrlog-20261006.log`（双通道冗余）。**零不可恢复损失。**
- **操作者定位（对账合并后闭合）**：podman events 不持久化、`last` 无该时段 pts 记录；**收尾与本轮合并 origin/main 时在 `scripts/252-monitor/etc.cron.d.pg17` 头注发现并行 R48 线的同步登记**——「起因：2026-10-05 15:46 生产容器 pg-252-pg17 **被误删**，15:49:41 恢复，停机约 3~4 分钟，全程无任何告警」，其补救 = 新增每分钟可用性心跳（该心跳自身第一版又误写 `59 * * * *` 变每小时一跳，R48-C1 订正——即本轮合并唯一冲突的由来，已取并集解决）。双方证据链互相印证：删除经由 Podman API socket，定性误删。
- **配置漂移排查（否定）**：新容器 `log_statement=none, log_min_duration_statement=1000` 均为**命令行旗标**（非 auto.conf），R46 基线在重建后完好。新容器日志速率 ~790B/s vs 旧 ~290B/s 为负载窗口差异，非配置漂移。
- `llmgo-252-dev.service` failed 状态起于 10-05 06:50:43，**早于**重建事件，系旧态非次生。

### §零.2 根因与修复（`/opt/scripts/pg17-ctrlog-snapshot.sh`，备份 `.bak-r26`）

根因三元组：**硬编码容器哈希**（重建即阵亡）× **stat 失败静默降级**（CUR=0→界标→offset=0，吞掉真实故障）× **cron 输出进 /dev/null**（空转 14h 无任何信号）。修复三件：

1. **动态解析**：`podman inspect pg-252-pg17 --format {{.Id}}` 现取现用，重建免疫；
2. **失败不静默**：inspect/stat 失败 → 保留 offset、落 `/var/log/pg17-ctrlog-snapshot.log`（尾部 500 行自清洁）、exit 1；
3. **容器切换界标**：state 增记容器哈希，哈希变化即打界标并从 0 起抄（新旧容器字节流不混抄）。

验证：`bash -n` OK；首跑回填 41,513,438B（覆盖容器启动→06:57）+ 容器哈希入态；二跑幂等（offset 不动）；归档尾行==活文件尾行。cron 表不变（`47 * * * *`）。回滚面 = 恢复 `.bak-r26`。

---

## §一、错误族全景（窗 13:16 → 06:12，17h；头锚定 `CST [pid] ERROR:` 二段计数）

二段计数：raw `ERROR` 432/1,311（A/B 段）vs 头锚定 425/1,214 vs 提取器 records 425/1,214——**三轴全等**（本轮提取器经四轮返工后达标，见 §七.A1）。FATAL 86/182（`connection to client lost` 为主体，已知自噪音）。每小时直方图：13-23 点 22-51/h 平稳，**02-05 点爆发 129/174/201/306**（=并行轨实验窗，与 §一.2 同源）。

| # | 家族 | n(17h) | 归因与处置 |
|---|---|---|---|
| 1 | `canceling statement due to user request` | **348**（20.5/h） | 小时分布 7-25/h（白天段）+ 05 点 110 spike（实验窗长查询被取消）。R25 白天 6.7/h 的抬升主要为**窗口构成差**（R25 窗=凌晨+上午）+ 实验窗贡献；应用侧取消已知容忍族，维持 |
| 2 | **ursm v2 集群**：`ursm_node_snapshot_min does not exist`（char 14/92/80/50 变体）331+8+4+2、`ensure_…_daily_partition(date) 不存在` ×10、`分区名…已被占用` ×4、`legacy 已存在——不要重放`（RAISE 守卫）×2、`there is no unique or exclusion constraint matching the ON CONFLICT specification` **×113（05:13→06:55+ 仍在持续）** | **~475** | **并行轨（URSM v2 分区化线）半成品态，P1 移交**，见 §一.2 |
| 3 | `inconsistent types deduced for parameter $4`（E6 pocket） | 190（11.2/h） | R25 12.0/h 恒定 ✓ 节拍器维持催办 |
| 4 | `canceling statement due to statement timeout` | 164（9.6/h） | R24 夜间 9.4/h 同量级，probe 族已知 ✓ |
| 5 | `relation "public.model_baseline_price_observation_health" does not exist` | 34（00:48→**04:26:39 戛止**） | **自愈销案**：并行轨于 04:26:53-54 将迁移 **832/833 应用到 252**（schema_migrations 实证），末次错误恰在应用前 15s |
| 6 | 股票筛选器 `$2` ×11 + `integer out of range` ×3 | 14 | 外部客户端已知族，催办维持 |
| 7 | `invalid input syntax for type json at character 41` | 4（02:24-02:27） | **探针自噪音**：STATEMENT=`INSERT INTO r22_evidence_probe VALUES ('\x7b22…')`（R22 hex 探针负例被并行审计轮复放）。capability/modality worker 路径 **json_hex 零复发第 4 天成立**（R25 判据闭合） |
| 8 | `only heap AM is supported` | 3（14:24-14:35，旧容器时代） | 无守卫 `pgstattuple_approx` 撞列存表；报错语句与现行 `pg-table-bloat-check.sh`（**已有** `amname='heap'` 守卫）不符 = 外部旧版/内联查询一次性。**10-11 vacuum-bloat 终验主脚本 `pg17-index-bloat.sh` 不用 pgstattuple（grep 实证）→ 终验安全** |
| 9 | 实验窗杂项：`invalid message format` ×3、syntax ×5、deadlock ×2（00:57）、division ×2、read-only txn ×2 | 14 | 并行轨实验噪音，随 §一.2 移交 |

### §一.2 ursm v2 集群 P1 移交（证据链完整，本仓不可单方面修复）

- **底座缺位**：迁移 `830_ursm_node_snapshot_min_partitioned.sql` 在仓内已是 **`.sql.skip`**（10-04 决定：对 10GB 活表 RENAME，刻意不进 installer/升级通道自动序列，须人工窗口执行）。252 的 schema_migrations **无 830**（829 止，831-833 为并行轨今晨补上）。
- **重放被守卫拦截**：02:16-02:51 有人手工重放 830 → 自带 RAISE 守卫「legacy 已存在——本迁移已执行过，不要重放」×2 +「分区名 ursm_node_snapshot_min_20261006 已被占用，但它不是子分区」×4。
- **临时表不兼容**：05:38 后表出现，但 `relkind='r'` **普通表**（非分区），PK=**(snapshot_ts, credential_id, raw_model_name)** 三列——writer 的 `ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name)` 四列 arbiter 推导失败 → **写入持续失败，06:55 活日志仍每 ~30s 一条，表 0 行（快照数据在 252 持续丢失中）**。
- **读者/健康检查同炸**：52 次/17h 的 ursm 健康评估查询（扫 6M 行基线，max 70.2s）+ 331 次表不存在。
- **移交**： ursm 轨（设计稿 `2026-10-04-ursm-snapshot-partitioning-design.md` §5.2）在可控窗口完成 830，或把临时表的唯一约束补齐 tenant_id（四列）止血——**二选一是该轨的拍板，审计轮不代行**。

---

## §二、慢 SQL 全景（≥1s 共 4,389 条 / 18,500.6s / 17h ≈ 1,088 s/h；R25 白天 742、R24 夜间 593，抬升主因=实验窗 DDL/全表扫描 + analyze 锁等待。duration ms→s 显式换算，纪律 48）

| 形状 | n | sum(s) | max(s) | 判定 |
|---|---|---|---|---|
| **analyze_llm_gateway_table_stats('2')** | 44 | 3,246.5 | **130.0** | **锁等待驱动实锤**：语句尾挂 `LOG: process … still waiting / acquired ShareUpdateExclusiveLock`（DETAIL 通道新捕获）——与实验窗 `ALTER TABLE providers ADD COLUMN quality_fix_mode`（61.7s DDL）等同窗竞争。R25 max 62.5s 翻倍的根因是**锁而非数据增长**；10-11 后实验窗消退应回落，下轮复核 |
| **ursm 健康评估（WITH win hours=>3）** | 52 | 1,165.2 | 70.2 | 并行轨（语句自带 252 实测注释），随 §一.2 移交 |
| project_backfill（WITH targets） | 205 | 1,358.0 | 28.3 | R8 已知 |
| **credential_selfcheck（v1 臂 LEFT JOIN）** | 284 | 861.6 | 5.0 | `bg/credential_selfcheck.go:258` 注释实证 = 并行 v1 轨版本已在 252 网关二进制中运行；~50 s/h，量级登记不立项 |
| cmb/probe 候选族 | 165+215 | 1,532.2 | 27.3 | R8 已知 |
| WITH win（credential COUNT FILTER 聚合） | 285 | 803.9 | 25.0 | 路由健康统计族已知 |
| WITH used（MAX(ts) usage 族） | 105 | 642.6 | 15.4 | 已知 |
| 路由读臂 latest_bucket | 267 | 518.6 | 9.1 | avg 1.94s（R25 2.18 持平，tick 驱动）；pss 累计 187,473 次/134,616s/718.1ms（17h 增量 +457s=26.6 s/h 平稳，11-02 判据维持） |
| **session_turns 镜像 JOIN + 覆盖率** | 70 | 720.1 | 29.5 | **42 s/h，较 R25 的 113 s/h 下降**——R25 §三.6「不立项」观察项继续维持 |
| REFRESH routing_analytics_7d / audit_summary_7d | 66+ | 331.2 | 30.4 | pss：7d 累计 6,312 次/123,029s（17h +471s）；DELETE-IN 臂自 R25 窗 **0 增量**（该臂停发，活跃删除是 `bucket=$1` 臂 ×108,539/2.7ms=R8 根修成果持续） |
| stats_rollup（INSERT request_stats_minute） | 127 | 332.9 | 25.0 | 已知 |
| 股票族（stock_basic/daily_kline/fundamentals） | ~470 | ~570 | 21.3 | 外部客户端白天+晚间抬升，已知 |
| `SELECT 'wide=' … UNION ALL 'narrow='` | 1 | 307.6 | 307.6 | 02:06 实验窗一次性（宽窄表对账手工查询） |
| maas_buckets / DELETE state_transitions('168h') / promote | 63 | ~290 | 14.6 | 已知族 |

**窗max 307.6s 与 30s+ 桶 66 条的 90% 落在 00:00-05:40 实验窗**——剔除实验窗后基线与 R25 相当。

---

## §三、R25 移交项逐项复核（§五①-⑥）

| R25 登记 | 本轮复核结果 |
|---|---|
| ① modality R26 首查（backlog/verdict/semantic 回写/json_hex 第 4 天） | **部分闭环**：表 28→33 行（末行 17:00:29）；carry accepted 28 / rejected 5；models_canonical semantic 仍 0（inferred 962，backlog 未排干=预期，R25 §三.1 机制判定不变）；**json_hex worker 路径零复发第 4 天成立**。**吞吐崩零根因归案**：cycle 日志 `scanned=200 probed=0 written=0` + `decrypt failed envelope_kid=legacy cannot decrypt: unknown format` = **R23 已知 legacy envelope 解密族**（实例钥与凭据信封不匹配），非新缺陷；催办随 252 网关重部署通道走 |
| ② podman 1GB / 证据保全层 | **本轮收口**：保全层自身停摆事件根因闭合+修复（§零）；1GB 悬崖未再发生（活文件 41MB≪1GB） |
| ③ 路由 7d 判据（11-02 复核） | 维持：读臂 26.6 s/h 平稳，day-2 DROP 判据不改 |
| ④ E6/股票外部催办 | E6 11.2/h 恒定维持；股票族同 |
| ⑤ 10-11 03:15 vacuum-bloat 终验 | **前置复核通过 + 新风险排除**：终验脚本 pg17-index-bloat.sh 不用 pgstattuple_approx（列存表免疫 heapAM 错误）；昨日 heapAM ×3 来自外部旧版查询，与终验无关 |
| ⑥ 会话镜像探针族 | 42 s/h 较 R25 113 s/h 下降，继续观察不立项 |

**新增跟踪**：capability 臂推进正常（credential_model_capabilities 97→138 行，max last_tested_at 06:35:35 推进中）。

---

## §四、本轮处置与拍板

1. **[修复·已上线]** 快照脚本动态解析+失败可观测+容器切换界标（§零.2），双跑验证通过。
2. **[拍板]** 832 探测错误 ×34 = 并行轨应用 832/833 前的窗口期错误，**销案**（末次错误早于迁移 15s，因果自洽）。
3. **[拍板]** analyze 翻倍=锁等待（实验窗 DDL），**不立项**独立修复；10-11 后复核回落，若仍 >90s 再查。
4. **[拍板]** ursm 集群 = 并行轨 P1，**移交不代修**（§一.2）；252 的 ursm 快照数据丢失窗口自 05:13 起累计，随移交件催办。

## §五、登记与移交清单

1. **[P1·移交 ursm 轨]** §一.2 全套证据（830 skip 状态/守卫 RAISE/临时表 3 列 PK/ writer 持续失败至 06:55+/表 0 行）。
2. **[催办·维持]** E6 pocket 11.2/h；股票客户端；modality legacy-envelope 解密失败（R23 族，252 网关重部署通道）。
3. **[销案]** 832 探测 ×34（自愈）；json41 ×4（探针自噪音）；heapAM ×3（外部旧查询一次性）；DB-less 窗 4.4min（自愈零残留）。
4. **[运维轨·仍开放]** disk-watch 4× 同秒 CROND 来源（R24 §零.5）；podman-pg-252 死信文件处置；log_size_max 调大/换 journald = 维护窗口提案。监控面现况：R48 线每分钟心跳（可用性）+ 本轮修复的小时快照（SQL 日志证据）互补，前者答「PG 活没活」、后者答「日志全不全」。
5. **[终验节点]** 10-11（周日）03:15 vacuum-bloat 首跑（前置全绿）；11-02 路由 7d 复核判据（R25 §四.2）。
6. **[下轮首查]** ① ursm writer 是否收敛（移交后）；② analyze max 是否回落 <90s（实验窗消退）；③ modality 33 行后是否有新增（decrypt 催办通道）；④ capability last_tested 推进；⑤ 快照层连续产出核查（`/var/log/pg17-ctrlog-archive/` 当日文件 mtime ≤1h 前）。

## §六、新纪律候选

- **64**：**证据保全层自身必须被巡检**——cron 输出进 /dev/null 的静默失败让停摆存活 14h；保全/巡检类脚本必须自带落盘日志、失败保留状态、且每轮审计首查「昨夜产出是否存在」。
- **65**：**硬编码资源标识符（容器哈希/绝对路径）在重建型运维面前都是定时炸弹**；动态解析 + 标识变化打界标 + 失败硬退出（不得降级为 0 继续跑）。
- **66**：**日志提取器不变量**：记录级全局（TYPE/DUR）只能由「即将创建的记录」写入，flush 必须先于任何新头赋值；DETAIL/STATEMENT 挂接与类型赋值不得共用一条 if/else 链（非记录型头走挂接分支时不得触碰 TYPE）。本轮 v2/v3 两个相反方向的单侧修复各制造一次假象（v2 计数对但丢语句；v3 语句对但类型错位），**裸 grep 地面真值三轴对账是唯一仲裁**。
- **67**：conmon P（partial）条目必须与后续 F 条目拼接后再解析（本轮 B 段 2,027 条 P 行），否则跨块语句/头部是提取盲区。

## §七、自审计

- **A1**：提取器四轮返工全程如实记录：v1（msg 前导空格致 SLOW=0）→ v2（records `>>` 追加未清致错误族翻倍假象）→ v3（TYPE 链先于 flush 致类型错位：canceling 被写成 SLOW、duration 记录被写成 ERROR）→ FINAL（flush 先于赋值 + TYPE/DUR 仅在 isstart 分支内赋 + P/F 拼接 + 多行串按行展开）。最终以裸 grep 三轴对账（anchored ERROR 1214=1214 / duration 3803=3803 / A 段 425=425）方才采用；中途 v2 的「正确计数」与 v3 的「正确语句」各骗过我一次，均由对账揭穿。
- **A2**：ON CONFLICT 首次抽样被 830 迁移注释文本误中（注释里引用了该错误消息原文，指纹过滤器误配）——改用 STATEMENT 全文通道后归因改正为 ursm writer 写入失败。
- **A3**：本轮自噪音 1 条已剔除：06:55:21 pid 92180 `function pg_get_partition_constraint does not exist` = 本轮 r26db.sql 猜错函数名（该函数在 citus 列存扩展而非 core），非生产。
- **A4**：窗 17h 跨容器重建，A/B 两段分别标注来源；服务器钟 +5min 常数沿用（纪律 55 系）；所有对比折率标注窗口基数。
- **A5**：并行轨资源零触碰——ursm 表/832-833/网关二进制/容器重建者均只读取证；抢救外抄与脚本修复均不改动 PG 与容器本身（脚本只读 ctr.log）。
- **A6**：`pg-table-bloat-check.sh` 现行版已有 heap 守卫的判定基于 grep 行 80-83；「报错语句≠现行脚本查询」的推断依据是别名结构差异（s vs c + CROSS JOIN LATERAL 形态），未逐字节 diff 旧版本——登记为推断而非实测。
