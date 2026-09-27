# A 存储归档域子代理报告（窗口：383c4b976..3909d56e0）

> R73 审计轮只读子代理原文（Explore，very thorough）。窗口 94 提交/293 文件。
> 主代理处置：#1 已修（A-1 接线守卫测试）、#4 已修（台账补登）、#6 已登记（752 头注）、
> #3/#2/#5/#7 见轮文档 §遗留与移交。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---------|------|---------------|---------|
| 1 | Medium（测试缺口） | cadence 测试**防不住 E1a 同款复发**：全部测试只钉住「hour 门在 `archiveOldRequestLogs` 函数体内被调用」，没有任何断言钉住**调用点位于 1h 的 runCleanup 循环**。把 `pm.archiveOldRequestLogs(ctx)` 移回 24h 定相的 `run`/`archiveOldPartitionsIfNeeded`（正是 3909d56e0 修掉的原始事故形态）后，现有套件全绿、相位锁死 bug 静默复发 | bg/partition_manager_request_logs_archive_cadence_test.go:85-104；调用点唯一存在于 bg/partition_manager.go:314 | 增加同款源码范围断言：`runCleanup` 函数体内必须含 `archiveOldRequestLogs(`，且 `archiveOldPartitionsIfNeeded`/`run` 体内不得含它 → **已落地** TestArchiveOldRequestLogs_CalledFromHourlyCleanupLoop |
| 2 | Low | **双实例同库时「每日一次」退化为「每实例每日一次」**：245/154 共享 PG，各自 runCleanup 独立跑归档；同 TZ 时同小时并发全量重扫（SQL 侧月级 advisory lock 串行，败者阻塞等胜者整个多月单事务）；异 TZ 则一天两次。正确性无损（ON CONFLICT 吸收），代价 2×/日 O(全部过期行) | bg/partition_manager.go:1275-1285、:314、:544-588 | 低优先：Go 侧 pg_try_advisory_lock skip（复用 promoteLockKey 模式）；或书面接受并注释声明 |
| 3 | Low（待亲验） | **hour 门用服务器本地时区，与本文件 Asia/Shanghai 钉扎不一致**：容器 TZ=UTC 时「03:00 低峰窗」实落 +08 的 11:00 白峰。同文件分区边界全钉 partitionTZ（:33），唯独归档窗随部署环境漂移 | bg/partition_manager.go:503-505、:548；对照 :33；settings/spec_lifecycle.go:46 已如实写「本地时区」 | 亲验生产容器 TZ；若 UTC 考虑 `time.Now().In(partitionTZ).Hour()` |
| 4 | Low | **754 漏登 docs/db-changelog.md 台账**：752/753 均有台账行，754 无——753 同类「补齐 embed 时漏登」复发 → **已补登（R73 A-4）** | docs/db-changelog.md（原无 754 行） | 已补一行含 canonical SHA + pending deploy 状态 |
| 5 | Observation（已知接受风险） | 归档无 ledger：每次日扫把所有过期月分区全部行再读再投影再 ON CONFLICT，单次 O(过期行总数) 永不收敛；且整个调用是单事务，任一月失败整窗回滚、advisory lock 持有贯穿全程 | 754 SQL:80-87、43-45；bg/partition_manager.go:530-539 | 维持 ledger follow-up（M-9）；可加 prometheus 计数（当前只有 slog） |
| 6 | Low | **mock_probe_history 日分区只建不删无 TTL**：752 头注自估 ≈1.2 万行/日（≈4.4M 行/年）有界增速但保留期无界；752 头注未声明此残留（usage_facts 750 已声明） → **已在 752 头注显式登记（R73 A-6）** | 752 SQL:64-136 | 已登记；TTL 由 owner 拍板后独立迁移 |
| 7 | Low（转交 admin 域） | **v2 会话读端点的 query/resolve 错误臂未接 503 降级**：session_list_v2:152-157、session_detail_v2:250-263 仍无条件 500，与各自 nil-pool 臂（已 503）不一致 → **已修（R73 A-7）** | admin/session_list_v2.go、admin/session_detail_v2.go | 两端点三臂补 IsStorageUnavailable→WriteStorageDegraded |

## 二、核实为健康的面

- **E1a 终态接线真实生效，无绕过路径**——archiveOldRequestLogs 全仓唯一调用点是 runCleanup 的 1h tick；16 轮 diff 确认旧调用点已物理删除。
- **1h ticker「每自然日恰一次落 [03:00,04:00)」不变量成立**（固定偏移/DST 时区均成立；时钟步进残余自愈无害）。
- **754 归档事务语义终态正确**——relnamespace 两处锚定；幂等 UNIQUE+ON CONFLICT；无 DELETE/TRUNCATE（钉桩测试在位）；游标双保险；down 只删函数保留资产。
- **753 TTL 三层联锁闭环**——spec=Go clamp=SQL RAISE [1,168]；保留期单一真相源是写入时烘焙的 expires_at；有界 DELETE 10k×600 批 5min 预算。
- **752 canonical 收编完整，五点同步全齐**——embeddata 字节一致；四点登记齐；双向对账守卫；运行时双 ensure CAS。
- **双重存储架构与 max(ttl,retention) 恒等式**成立，启动期断言读真实 worker 字段。
- **hot 8h 轮转真实执行**——promote 独立 worker 每小时跑，默认 retention=8h，四类 metrics；未发现绕过 hot 的分区侧 UPDATE/DELETE；归档与 hot 清理互不依赖。
- **归档失败观测性**——三处 slog.Error + 成功 Info；仅缺 prometheus 指标。

## 三、未覆盖项与原因

- 753/754 真库执行独立复跑（只读约束+无环境，采信 handoff §24）。
- 生产容器 TZ 与双实例时区配置（仓内不可证）。
- promote SQL 函数体与 columnar_invariant_check 深审（窗口未触碰）。
- admin 503 采纳面全景（admin 侧另有代理，本域只抽验 4+2 端点）。
- lite 模式 sqlite 工厂/ConsistencyWorker 内部语义（非窗口核心）。
- request_logs 源表 (request_id, ts) 真唯一性（依赖上游语义，留待真库抽样）。
