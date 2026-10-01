# bodies 列存化收尾对账第 1 轮（R16 §六观察项，2026-10-01 凌晨）

任务来源：用户指令"继续 252 存储轮收尾"五项。执行窗口 2026-10-01 01:37-01:55 CST（252 时钟；本机快 +10min）。
起点重算：本地 HEAD fc10b91c5（R16 提交），落后 origin/main 12 提交（新增 765 函数链登记+802 回填、部署 2.5.8.2367 序列、docs 轮），本轮只读生产、不动工作区在制。

## 一、R16 登记值重测（纪律：执行前重测，全部复现）

| 登记项 | R16 登记值 | 本轮实测 | 判定 |
|---|---|---|---|
| V1 2026_10 分区 | columnar（options zstd/3/150000/10000） | columnar、16kB、0 行；columnar_internal.options 参数同值（库内其它 columnar 表逐行同参） | ✓ |
| V2 ensure 函数 | columnar 分支 + heap 回退 | prosrc 双分支在位 | ✓ |
| V3 lz4 五目标 21 列 | 5 目标 21 列 | bodies 父/hot 各 3 列 + session_bodies 父/hot/2026_10 子各 5 列 = 21 | ✓ |
| V4 stage_events 三死索引 | 已清、pkey 160MB + created_at 7.3MB 在位 | 三索引绝迹；pkey 160MB/created_at 7312kB（另有 redis_miss、upstream_failure 8kB 部分索引） | ✓ |
| V5 台账 765 | 落账 | applied_at=2026-10-01 00:16:51 | ✓ |
| 2026_09 体量 | 53GB | 53GB（1,404,892 行，主叉扫 15s） | ✓ |
| hot 体量 | 4.3GB | 4315MB / 9,513 行 | ✓ |
| ursm_node_snapshot_min | 20GB / 38.4M 行 | 20GB / 38,372,464 行 | ✓ |
| 根盘 | 93%（余 15G） | 93%（/dev/vda3 197G，used 174G，余 15G） | ✓ 持平 |

## 二、①首批生产写入：今晚 2026_10 仍为空是**预期形态**，非故障

`request_logs_bodies_2026_10` = 0 行（count 7.9ms）。原因链（代码+settings_kv 实测定谳）：

- bodies 写路径只有 hot 双写，分区行**只能来自 promote**（R16 §二已证无 Go 直写分区名）。
- promote 保留窗实值：`lifecycle.request_logs_bodies_retention_hours = 8`（settings_kv platform，非代码默认 24）。
- promote tick=1h、bodies 批=500 行/次（`requestLogsBodiesPromoteBatchSize=500`，TOAST 重行防 tick 爆量）。
- 10-01 00:00 后的行到 08:00 后才可 promote，且此前 hot 内 09-30 积压（约 3-4h 批限滞后）先排空 ⇒ **首批 2026_10 行预计 252 时钟 10:00-14:00 落地**。

已排程今日 17:30（本机）正式对账：行数/ts 界/尺寸/月轨迹外推（~2GB vs 原 50GB）/stripe 统计（columnar_internal.stripe，storage_id↔pg_class.relfilenode 映射 best-effort）/drawer 冷读（2026_10 分区直查 EXPLAIN ANALYZE + 视图 cost-only 形态——2026_09 臂 53GB 无索引，ANALYZE 全扫禁止）。

## 三、promote 速率判定：**正常（批限排空形态），非卡死**

| 证据 | 值 |
|---|---|
| hot 水位 | 最老行 11.95h（vs 名义 8h 保留） |
| 解释 | 500 行/h 批 < 夜间入流 ~600-800/h ⇒ 水位滞后 3-4h 属批限动力学 |
| 分区增长 | 2026_09 = 1,404,892 行（R15 基线 1.39M，+~12k/日 = 500×24 上限量级吻合） |
| 对照组 | session_bodies_hot 水位 8.05h ≈ 名义 8h 精确（该表批限未饱和） |

**登记勘误**：R16 "request_logs_bodies_hot 4.3GB / 8h 保留" 的"8h"是 settings 名义值；实测水位=批限滞后 ~12h。风险面：hot 体量有界（4.3GB 量级）、drawer 读视图含 hot 臂不受影响，无需处置。

## 四、③ lz4 TOAST 实测（attcompression=l 新行，2026-10-01 00:16:51 后）

| 表 | 大行样本 | raw（::text octet_length 和） | lz4 存储（pg_column_size 和） | 总压缩比 |
|---|---|---|---|---|
| request_logs_bodies_hot | 24 行（>8KB） | 5,764kB | 605kB | **9.53×**（req 9.89 / resp 1.92 / out 10.61） |
| session_bodies_hot | 31 行（>8KB） | 7,921kB | 1,459kB | **5.43×** |

- 对照 R16 基线：现行 heap+pglz 对 bodies 负载仅 2.1×，lz4 新行实测 9.53×——jsonb 正文列上 lz4 显著优于当年 pglz 样本，写路径 CPU 同步受益。
- **异常登记**：session_bodies 的 request_attachments/response_attachments 两列 ratio 0.40-0.44（pg_column_size > ::text 长度），疑似 jsonb datum/序列化形态 artifact 或压缩未按预期生效——已排入 17:30 轮单行取证（length/octet_length/pg_column_size/jsonb_typeof 逐列）。
- 方法论教训：`octet_length(jsonb)` 不存在（42883），bodies 家族取样一律 `octet_length(col::text)`。

## 五、④ 根盘复核：93% 持平，满盘赛跑（D19）实质解除

- 现值 93%/15G 与 R15 完全持平（10 月首日夜间低峰）。2026_10 列存后 bodies 日增量预期从 ~1.6GB 降两个数量级（9.53-56.6× 压缩带），10-08 释放 53GB 后余量预计回到 60G+。
- session_bodies_2026_09（20GB）本轮未动、不在 10-08 释放范围。
  > **R78 事实订正**：原文写「按自身 TTL 退役」不成立。session_bodies 父表
  > 没有任何 TTL——`stateTableTTLSpecs()` 的 7 项里不含 session_* 族，SQL 侧
  > `drop_old_state_partitions` 的 target_tables 也不含，settings 无对应项
  > （`retention_session_bodies_days` 是 lite 文件系统热区配置，与 PG 分区无关）。
  > session_bodies / session_turns / session_turn_details 三个父表当前均无
  > 退役路径，历史分区无界增长。本机实测 8361MB / 6380MB / —。这是待裁决项。

## 六、⑤ URSM v2 cutover 判定：**已完成（154 权威），下调建议升级为容量安全项**

| 证据 | 值 |
|---|---|
| 154 生产 | env 无 URSM_V2_MODE 键 = 代码默认 **authoritative**；healthz `2.5.8-2d750fb4-2356 ready:true` |
| 245 生产 | env 显式 `URSM_V2_MODE=shadow`（有意 pin，未随默认切权威）；healthz 2d750fb4-2355 ready:true |
| DB 写入 | 近 1h 144,568 行、最新 snapshot_ts 滞后查询 3 秒 ⇒ v2 persist writer 全速在产 |
| 表窗口 | earliest 09-06 23:56，>30d 行 = 0 ⇒ 首个清理点 ~10-06 23:56（worker hourly/批 5000 走 ts_idx） |

**容量外推**：当前写入 ~144k 行/h（夜间）≈ 0.8-1.7GB/日；30 天名义稳态 = 25-50GB，**对 15G 余量盘是不可接受项**。`URSM_SNAPSHOT_RETENTION_DAYS`（30 默认，构造期 os.Getenv、重启生效；0/负=禁用）下调已从 R16 的"可选优化"升级为容量安全项。

**提议（待拍板，本轮未动）**：`URSM_SNAPSHOT_RETENTION_DAYS=14`（稳态 ~12GB，保守）或 `=7`（稳态 ~6GB，积极）。生效动作 = 154/245（及 252-dev）env 追加键 + 蓝绿/滚动重启，属维护窗口操作。

## 七、排程

| 时点 | 内容 |
|---|---|
| 今日 17:30（本机，一次性自动化） | ①正式对账：2026_10 首批行/stripe/月轨迹/drawer 冷读 + lz4 附件列异常取证 —— **已建 automation-5c6e92e1-31fd-4b48-b7dc-232760f8e029** |
| 10-08 10:00（本机，一次性自动化） | 2026_09 TTL DROP 确认：边界函数源已核（`month_end=2026-10-01 ≤ CURRENT_DATE-7` ⇒ 10-08 当日首个 partition_manager tick 执行，10-07 不触发）+ 盘量回落（预期 +53GB）+ drawer 视图退化臂复核 —— **创建被拒（本会话已归属调度任务，单会话一调度限制）**，提示词存档如下，待新会话创建或当日直接指令执行 |

10-08 轮提示词存档（新会话 CronCreate 一次性 `0 10 8 10 *`，或当日直接发给任何会话执行）：

```text
执行 252 R16 存储轮收尾②"2026_09 bodies 分区 TTL DROP 确认 + 盘量回落"。背景与基线见
docs/audit/2026-10-01-bodies-columnar-followup.md（根盘基线 93%/15G@10-01、库 119GB、2026_09 分区 53GB；
边界函数源已核：month_end(2026-10-01)<=CURRENT_DATE-7 ⇒ 10-08 当日首个 partition_manager tick 整分区 DROP，预期释放 ~53GB）。
纪律：先 git fetch 重算起点；只读生产；SQL 本地文件通道（/tmp/pg252-20261008-ttldrop-p1.sql）经
ssh -p 25022 root@115.29.212.252 "podman exec -i pg-252-pg17 psql -U postgres -d llm_gateway -X -P pager=off -f -"；
禁 JOIN pg_class ON tableoid；禁全扫 2026_09（若它还在）。
采集：1) to_regclass('public.request_logs_bodies_2026_09') 应为空 + 154 journalctl -u llm-gateway-go-canary@8782 --since "2026-10-08 00:00" grep drop_old_request_logs_bodies_partitions 钉时刻（未 DROP 则核 settings_kv lifecycle.request_logs_bodies_ttl_days=7 与 worker ERROR）；2) df -h / 与 pg_database_size（回落不足则查句柄持有）；3) drawer 视图 EXPLAIN（cost-only）确认 2026_09 臂消失，2026_10 分区直查 EXPLAIN ANALYZE 冷读对照 §九；4) 2026_10 月轨迹终值 vs ~2GB + lz4 附件列异常未结案则续查；5) URSM：154/245(/opt/llm-gateway-go/.env)与 252-dev(.env.dev) 是否已加 URSM_SNAPSHOT_RETENTION_DAYS（加过核 /proc/<pid>/environ 生效态，该值构造期读取须重启，只核不代重启）+ ursm_node_snapshot_min min(snapshot_ts) 与 >30d 计数（首个清理点 10-06 23:56 后应归 0）。
产出：§十 追加同文档；worktree 模式提交推送 main（禁主工作区 commit）；禁止运维动作；简报：结论/关键数字/异常/遗留。
```

## 八、自审计

- A1 octet_length(jsonb) 首跑 42883——bodies 家族是 jsonb 不是 text，取样口径立即修正重跑，未带病出数。
- A2 hot 水位 12h 初判"promote 滞后/卡死"——核对批常量（500）与 tick（1h）及分区日增量后改判批限动力学正常；"先入为主 8h 水位"差点误登记故障。
- A3 245=shadow 与 154=authoritative 身位差异如实登记，不擅自代改 env（运维动作需拍板+维护窗口）。
- A4 2026_10 空分区未强行制造探针行——R16 已有回滚包裹探针验证路由，本轮不为"看到数据"而写生产。

## 九、首批写入正式对账（17:30 自动化轮，2026-10-01 17:31-17:39 CST 执行）

**前提变化声明**：并行存储轨道今日凌晨已对 2026_09 bodies 执行提前换壳（53GB heap 销毁无归档→16kB 列存空壳；十六轮审计 753299a19 §五登记 P1+授权性待确认；R19 独立验证轮 6846c05c8 登记回填 4,408 行解除）。本轮按"执行前重测"纪律发现并适配：53GB 全扫禁令对象已消失、2026_09 count 对照值已变、②10-08 轮前提失效（范围修订见文末）。

### 9.1 2026_10 列存首批生产写入：**成功**

| 项 | 实测（17:31） |
|---|---|
| 行数 / ts 界 | **6,591 行**，ts 00:00:00.346 → 07:12:52（count 79.7ms） |
| 分区体量 | 8,048kB（行均 **1.22KB**，vs 9 月 heap 行均 37.7KB） |
| promote 水位 | hot 最老行 07:14:05（10.29h，凌晨 11.95h → 持续收敛，批限形态正常）；hot 与分区边界无缝衔接（07:12↔07:14） |
| settings_kv | retention_hours=8 / ttl_days=7 未被改 ✓ |

**月轨迹外推**：行数归一（按 9 月 1.4M 行）≈ **1.7GB/月**；小时归一（00:00-07:12 夜间权重）≈ 0.8GB/月 ⇒ 区间 **0.8-1.7GB/月，对 ~2GB 目标达标**，vs 原 ~50GB/月 **-97%**。

**stripe 统计**（storage_id 映射：relfilenode≠storage_id 不成立，按行数/字节数钉归属）：2026_10=storage 10000000250（**18 条带**/6,591 行/7,952kB）、2026_09=10000000258（15 条带/4,408 行/7,778kB）。条带均 ~440 行 << stripe_row_limit 150000——粒度由 promote 500 行批决定，压缩效率轻微折损，月体量 MB 级无实质影响。

### 9.2 drawer 冷读实测（2026_09 已列存 7.9MB，ANALYZE 安全化）

| 臂 | 计划 | 实测 |
|---|---|---|
| 2026_10 分区直查 | ColumnarScan+Filter（6590 rows removed，0 索引） | **59.5ms**（全 shared hit） |
| 视图真实路径 | Append：hot IndexScan 1.3ms + 09 列存 49.0ms + 10 列存 74.9ms + 11 空臂 0.04ms | **125.4ms** |

对照换壳前形态（2026_09=53GB heap 0 索引，drawer 非热 id 即全扫，量级为几十秒）：现全列存臂 **~125ms，改善 2-3 个数量级**。诚实登记：本次全部 shared hit（页缓存暖），冷盘未测；时延随月内体量线性增长，月末预计数百 ms 仍可用。

### 9.3 lz4 附件列异常：**结案（非压缩失败）**

今日 session_bodies_hot 全窗 1,718 行：request_attachments 全非空但 **max 388B**、response_attachments **max 2B**——全部 inline 域小值。凌晨轮 ratio 0.40-0.44 = 小值 jsonb 二进制 datum 头开销（`pg_column_size` 含 jsonb 头 vs `::text` 重序列化），**与 lz4 无关**；凌晨 31 大行样本的 8KB+ 来自 delta/body 列，附件列稀释总比 ⇒ session_bodies **5.43× 为下界口径**，bodies 9.53×（三列全大值）为干净口径。方法论：lz4 比只应在 TOAST 域大值上度量。

### 9.4 2026_09 现状与 session_bodies 路由语义（新登记 P3）

- request_logs_bodies_2026_09：**columnar 7,888kB / 4,408 行**（ts 09-30 18:42:52-23:59:53，= 换壳后回填残量，与 R19 登记数精确一致）；bodies 按 ts 路由，18:42-23:59→09、00:00+→10，干净衔接。
- **P3 新登记（既有语义非回归）**：`session_bodies` 按 **partition_date** 路由，而 partition_date 按 UTC 派生——每日 00:00-07:59 CST 行的 pdate=前一日 ⇒ 每月头 8 小时行落上月分区。实证：今日被 promote 排空的 00:00-07:36 行（3,620 行 ts≥今日）全在 session_bodies_2026_09（该分区 max_pdate=09-30、max_ts=今日 07:38）；session_bodies_2026_10 子分区 0 行=预期（08:00 CST 起行今晚起随批限推进落入）。非停摆非丢数；与该分区自身 TTL 退役交互（月首 8 小时行寿命跟上月分区走）。
- session_bodies_hot 水位 10.01h（批限形态同 bodies，正常）。

### 9.5 复测汇总

| 项 | 凌晨基线 | 本轮 | 判定 |
|---|---|---|---|
| 2026_09 行数 | 1,404,892（53GB heap） | 4,408（7.9MB columnar，换壳+回填） | 并行轮已改，归因清晰 |
| db size | 119GB | **58GB** | 并行轮释放 |
| 根盘 | 93%/15G | **60%/76G** | 回落提前兑现（十六轮 62%/74G→本轮 60%/76G，10 月列存增量继续压低） |
| bodies promote | 水位 11.95h | 10.29h | 批限收敛中，正常 |
| settings_kv 两键 | 8/7 | 8/7 | 未被改 ✓ |

### 9.6 ②范围修订与自审计

- **②（10-08 轮）范围修订**：原"53GB 释放确认"已被并行轮提前兑现；残余项=10-08 当日 `drop_old_request_logs_bodies_partitions` 对 7.9MB 列存残量分区（4,408 行）的例行 DROP + 盘量/视图臂复核。§七存档提示词中"库 119GB/53GB"基线数字作废，以本轮 §9.5 为准。
- A5 前提被并行轮推翻后未按旧清单硬跑——重测发现 2026_09=4,408/库 58GB/盘 60% 后立即转入归因取证（git 考古+DB 实证），报告按新事实重写。
- A6 附件列异常首查 0 行（>4KB 过滤在今日窗口无样本）→ 扩为全窗画像（H5）后用 max 值结案，未强凑样本。
- A7 storage_id↔relfilenode 映射假说被 H3 证伪（0 行）→ 改行数/字节钉归属，假说存废如实登记。
- A8 session_bodies 子分区 0 行初判"promote 停滞"→ 函数源码+分区今日行计数双证后改判 UTC 路由语义，避免误报 P1。
