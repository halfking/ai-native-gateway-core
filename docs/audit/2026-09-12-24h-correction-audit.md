# 24小时修正审计报告 (2026-09-12)

**审计日期**: 2026-09-12
**审计范围**: 2026-09-11 00:00 +08 之后合入 main 的全部修正（103 个非合并提交，308 文件，+17.8k/−22.3k）
**基线提交**: 1ade98450（necessity gate round 22）→ 本次合并基 2bade0a2d
**审计模式**: 主代理 + 6 个并行只读子代理（分区存储 / 供应商错误闭环 / 队列并发 / 会话IR / 双存储+迁移通道 / Web可观测性）

---

## 一、执行摘要

24h 修正批次方向正确、质量总体良好，**无 P0**。6 条审计轴中三条给出"闭环成立"结论：

- **供应商错误三闭环（独立错误表按凭据聚合 / 降级不断流 / 凭据详情呈现）全部成立**：错误写入 candidate_failure_logs_hot + supplier_errors 事实源，聚合器三重防并发（advisory lock + FOR UPDATE watermark + 8段 ON CONFLICT）；流中终态失败以 parser-safe `: thinking:` SSE 注释帧通知客户端不污染会话（ADR-Disp-003）；error-detail/candidate-failures/provider-error-stats 三路 API 与前端字段全对齐。
- **dispatch 终态防线（649181ef4）自洽**：CAS 定胜负、MarkRetryScheduled 对 Completed 幂等、无僵尸复活；ticker/goroutine/map 竞态全面扫描无新增问题。
- **Web 面无阻断项**：vue-tsc 零错误、datetime 集中化基本完成、路由/守卫/指标/菜单四方对齐、告警阈值有文档+双契约测试背书。

本轮修复 **P1 ×4、P2 ×3**（见下），新增 2 个锁定测试 + 1 个契约测试扩面。

## 二、已修复问题（本轮提交）

| 级别 | 问题 | 根因 | 修复 | 提交 |
|---|---|---|---|---|
| P1 | 别名解析仍不确定 | `modelname/variants.go` versionPunctuationCartesian 输出按 map 迭代序构建，first-hit-wins 消费方跨进程命中不同 canonical，部分抵消 f494d0695 | 输出 sort.Strings 排序 + 确定性锁定测试 | 080c772e7 |
| P1 | cfl promote 每月初 8h 停摆+数据丢失窗口 | cfl 被排除出 ensureSpecs 预建（"空 body"注释在 689/694 后失真）；promote 函数内按会话时区算月份，UTC 会话把 +08 缝隙行路由到不存在的当月分区→整批永久失败→7d TTL trim 静默删数据（473 同族，潜伏至 2026-10-01） | cfl 接入 ensureSpecs 当前+次月预建，目标分区恒先于 promote 存在；契约测试同步 | 657828114 |
| P1 | rawFrameIndex 无界增长 | sync.Map 只增不删，每请求约 4 key，高并发线性吃内存 | 插入序号 + 100k 软上限，超限 Range 淘汰最旧 3/4，摊还 O(1)；锁定测试 | eaecd2a55 |
| P1 | 695 全新安装缺 promote 自愈 | 695 只有升级库通道（sequence 脚本），installer 四点全缺——全新安装拿到 688 体 promote（无 demote），claim-guard 回归时 23505 冷迁移卡死无自愈 | 补齐 embeddata/go:embed/map/StartupFiles 四点 + byte-equality 映射从 690 扩到 695（关闭 632/695 同款漂移方向的测试盲区） | be28cd506 |
| P2 | rotate 失败后审计 sink 永久死亡 | l.file 清空后只 warn 不重建不计数，一次 ENOSPC 抖动=审计管道静默死亡到重启 | 双写路径惰性重 rotate（l.closed 守卫保持 Close-then-log noop 契约）+ 每次失败计入 rawaudit_write_failed_total | eaecd2a55 |
| P2 | Async noteDroppedEntry 读锁内做 2s HTTP | 溢出上报在 stateMu.RLock 持有期内同步执行，阻塞 SetOverflowReporter/Close（Buffered 已修、Async 漂移） | 快照 reporter 后 goroutine 上报；CAS 告警窗已限频每窗口一次 | eaecd2a55 |
| P2 | 694 升级库通道缺失（合并前发现） | 694 只在共享 252 ledger 带外 applied，sequence 止于 693/695、installer 未嵌 | sequence 条目 + installer 五点同步 | 2895540c2（合并后即修） |

另：本次会话开头完成远端 75 提交合并（5 处冲突：版本文件取远端 2087、db-changelog 双侧保留），并确认本地 694 编号即远端 5c58bf346 为共享 252 ledger 让出的槽位。

## 三、确认通过的关键面（抽查证据）

- **694 迁移本体**：13 个 ensure_* 函数 SET LOCAL 均先于边界计算；与 687/689 边界约定一致（月初 00:00+08）。
- **695 promote 自愈**：demote 只降级"过保留期+is_final_success+heap 已有 TRUE"；hot 删除与分区插入单 CTE 原子；23505 回滚不丢行；claim 侧 wiring gap 已由 8f4c13970 结构性封死。
- **recent-window 改道**（d249604c7/d812e1a53）：10 处全部走 current-month 面，剩余 3 处裸父表读均为有意保留且有测试锁定（恰好 N 处断言）。
- **升级顺序**：deploy-local 先 migrate 后 start_instance；ApplyMigrations 失败中止 boot——不存在带缺口二进制先行服务的窗口。
- **双存储对等**：sqlite 模式对 24h 新列（modality 原生、user_intent/cleared_at 为 PG 专有 nil 降级）无缺口；redis 可全量摘除。
- **流式计费闭环**：client_cancel/disconnected 且有内容仍计费；estimated 行真实 usage 异步回填；断连后 capturer 继续收帧可重放。

## 四、遗留风险（按优先级，未在本轮修复）

### P1-级（下一轮首位）
1. **promote 函数族月份路由仍读会话时区**（本轮用预建缓解了 cfl 暴露面，但 16 个 promote_* 函数体的 `date_trunc('month', ts)` 本身未钉 +08）。生产集群 default TZ=Asia/Shanghai 故当前无害；任何 UTC 会话环境（新宿主机、维护连接、裸 pgx）仍触发批次失败。**建议 698 迁移**（696/697 已被 fingerprint 线占用）：对 promote 函数体统一加 `SET LOCAL TIME ZONE 'Asia/Shanghai'`（694 同款），机械变换+5点同步，编号先 fetch 核对远端。
2. **IR 未知 content block 静默丢弃**（`internal/ir/response.go:191-257` switch 无 default）：上游仅返回未知块（server_tool_use 等）→ empty_response 硬失败 → failover 重试 + billing.go 拒计入账（上游成本网关吸收）。需产品决策：抢救 text 还是计数+可观测。

### 后记（同日二次合并补充，2026-09-12 晚）
- 远端 fingerprint 线（696 视图列 + 697 promote 携带 X-System-Fingerprint，83bf582dd/d03f0ada4）落地时只做了 embeddata 拷贝、四点未接线——TestStartupFilesAreAllEmbedded direction-2 在合并树上红，已由 1f17dc2ac 补齐（byte-equality 映射同步扩到 697）。
- 并行会话 055cecc78 将 694 回滚改为 replace-safe 并把 689 自愈收敛到 694 体，与本审计 §二 的 694 项互补。
- 8af09c799 已登记 promote 695→697 intentional function chain；clobber 守卫经核无其他缺口。

### P2-级
3. **dump 基线局部陈旧**：`sql/schema/01-schema.sql` 与 baseline 的 `ensure_request_logs_bodies_partition` 仍是 pre-562 columnar 体；`ensure_credit_ledger_partition`/`ensure_tool_usage_stats_partition` 在 dump 中无定义（悬空 PERFORM）；695 的 promote 体未回写 sql/objects/functions；installer 的 01-schema.sql 嵌入版无 694 pinning。全新安装路径靠后续 startup 迁移自愈，但**纯基线重导路径会复现 bodies columnar 停摆**（§5.5 重导法验证）。
4. **BufferedRawSink 大条目降级**：单条 >4MiB 无条件 stub（legacy Async 正常落盘），灰度切档会丢超大多模态 body 的原始审计——灰度验收清单需加"大条目奇偶"项。
5. **launcher/upgrader 升级路径无 694/695 兜底**：二者无 migrate/sequence 钩子，只有 Go ensure 镜像的 693 有兜底；经 offline_apply 的升级永不应用 694/695。

### P3-级（打磨/巡检，择机批量）
6. 分区层后无生命周期终点：request_logs/request_wal/usage_ledger 等大表月分区无限增长、default 分区无排水与告警、无持续行数对账（687/689 的守恒校验均为一次性）。
7. `bg/opslog_trimmer.go` 每 24h 单批 LIMIT 5000，事故级写入速率下 7d 语义滞后。
8. user_intent 692 加宽后 Go 侧无长度护栏（>200 字符 LLM 输出会再触发 22001 整条 upsert 失败）。
9. Web：statusBadge 八处独立实现；datetime 双轨（utils/datetime 59 处 vs useFormat 23 处）输出形态不同；deadcode-scan.mjs 假阳性（误判 api/keys.ts）且真死代码未清完（api/stats.ts、useBootstrapChannel.ts）；RouteIncidentDrawer.vue:999 冗余 Date→ISO 转换。
10. dispatch registry 小项：RegisterPending 对 completed 条目的复活通道（仅同 ID 重 Submit 可达）、unfinishedCount 负值钳制掩盖记账失衡、removeCompletedLocked O(n)。
11. `bg/partition_manager.go` ensureSpecs 引用 `ensure_supplier_errors_partition` 全仓 sql/ 无定义（V371 疑带外安装），fresh DB 每 24h tick 报 function-not-exist。
12. Windows cleanup 失败仅 Warn 无 metric；discovery 写路径每周期重播种裸家族别名（运营弃用不持久，需台账/告警跟踪）。

## 五、审计方法备注

- 子代理覆盖：分区存储（promote/trim/边界/改道/对账）、供应商错误（写入/聚合/降级/呈现/681/684 幂等）、队列并发（dispatch 终态/churn 有界/raw sink/submit gate/ticker/goroutine/map 竞态）、会话 IR（别名确定性/协议转换/流式保真/计费/690/692）、双存储+迁移通道（四通道对等/sqlite 对等/clobber 守卫）、Web（datetime/死代码/路由对齐/指标对齐/控件复用）。
- 本轮修复后验证：`go build ./...`、`go vet ./...`（全仓零输出）、`go test ./modelname/ ./internal/logging/ ./bg/`（全绿）、`cd installer && go test ./...`（全绿，含 TestStartupFilesAreAllEmbedded 与 691-695 byte-equality）。
- pre-commit 六项门禁（go vet / SQL SET+占位符 / 迁移编号唯一 / down.sql 配对 / vue-tsc / token 合规）逐提交全过。

## 六、第三轮后记：两项 P1 闭环（2026-09-12 深夜，同日第三会话）

### §四.1 promote 时区钉扎 → 迁移 698（2ad8d64ad）
- **编号核对三重**：本地/远端 git 无 698；共享 252 生产双账本 `gateway_db_revision_sequences`/`schema_migrations` 均 0 命中（账本顶端为 697，证明 695-697 已在生产应用）。
- **范围修正**：审计原文"16 个 promote_*"实为全部 18 个 promote 文件中 **9 个 `*_hot_to_partition`** 含 `date_trunc` 月份分组（9 个 `*_default_batch` 不做月份计算，无需钉扎）；model_probe_runs promote 已退役（partition_manager.go:939 注释）。
- **体谱系验证**：request_logs = 697 线上体（695 自愈 + fingerprint 三列）+ 钉扎；其余 8 个经逐函数切取比对 **byte-identical 于 688 线上体**（659 原子 CTE 形状、688 对齐 Go 调度器默认值）+ 钉扎——objects/ 是 688 谱系而非 659，迁移以 objects/ 机械变换生成，无回退。
- **clobber 守卫实战**：guard 抓出 659（在序列数组内）与 698 的 7 个同名函数重定义，登记 7 条 `659|698` 链 + 扩展 request_logs 链 `695|697|698`；guard 只查顺序不查内容，内容等价由上述逐函数比对补位。688 不在序列数组（guard 盲区），已写入 chain 注释留档。
- **五点同步**：embeddata 副本（byte-identical）/go:embed/embeddedSQLFiles/runner.go StartupFiles/byte-equality 映射全接；本地 llm-gateway-pg ROLLBACK 包裹全文件执行（9×CREATE FUNCTION+账本 upsert，零残留）。

### §四.2 IR 未知块 → 计数+可观测+conversion 归因（b2639182b）
- **决策**：不抢救 text（server_tool_use 等承载工具载荷而非文本，语义不安全）；采用计数+可观测，并修正错误归因。
- **实现**：`ParseAnthropicResponse` 补 default 分支记录 `UnknownBlockTypes`（去重、上限 8）+ `OnlyUnsupportedBlocks()` 判定；executor IR 路径对 unknown-only 以 **KindConversion 失败**（消息含块型）——stage=gateway → 拒计费（网关吸收自身能力缺口，归因诚实）、供应商不再被 bandit/weight-nudge 按 empty_response 降权、conversion_error 可按块型 SQL 检索；legacy 转换器守卫同款拆分。
- **行为变化**：IR 主线从"序列化成空 body 按 success 计费"转为 conversion 失败（可 failover）；mixed 响应已知块保留不再静默丢未知块；真空响应维持 empty_response 桶。
- **残留**（记录不阻塞）：`ParseOpenAIResponse` 的 `parseOpenAIResponseContentBlock` 对未知块保留为不透明 IR 块，序列化层静默丢弃——OpenAI 侧同族问题呈不同故障形状（空序列化、按 success 计费），待下轮评估是否同款处理。

### 并行会话协同（同窗口）
- f08e0a95b（并行会话）收敛三处 01-schema baseline（§四.3 的 dump 陈旧主体）；**699_supplier_errors_ensure_timezone_pin** 在其暂存区推进中（对应 §四.11 supplier_errors ensure 缺定义 + cfl 同款钉扎补漏），与本轮 698 无文件重叠。
- 竞态纪律生效：两轮提交均用精确 pathspec（`git commit -- <paths>`），699 的 staged 集合在本会话两次提交间完好无损。
