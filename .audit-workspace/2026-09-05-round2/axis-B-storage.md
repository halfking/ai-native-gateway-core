# 审计报告 · 轴 B（round2）：双模式存储架构 / 缓存 / 附件媒体

- 日期：2026-09-05（round2）
- 仓库：`llm-gateway-go`（只读审计，除本报告外未写任何文件）
- 范围：上一轮（`.audit-workspace/2026-09-05/axis-B-storage.md` B1-B12）之后、第二轮修复（29e177dcb）+ 闭环4（b08a2a20d）合入的现状复核
- 前置阅读：docs/audit-2026-09-05-24h-comprehensive.md §二/§四、docs/audit-2026-09-05-eight-closures.md 闭环4
- 结论速览：**P0=0，P1=1，P2=4，P3=5**；上轮 B1/B2/B3/B4(主体)/B5 确认闭环；闭环4 原语实现与测试质量良好但**零生产调用方**（B-#2）。

## 一、发现清单

### B-#1 [P1] lite request_logs.has_body 被终态后的 enrichment UPDATE 无条件清零（B2 接线闭环的契约断裂）

- 证据：`cmd/gateway/lite_telemetry_sink.go:111-114`（`journal` 布只在那一次写入为 true）、`:127-131`（仅 `journal==true` 时 `row.Body = json.RawMessage("{}")`）+ `storage/sqlite/request_log_store.go:20-28`（UPSERT `has_body = excluded.has_body` 无条件覆盖）。同一 request_id 的后续 UPDATE（cost/tokens/latency 回填，`domains/streaming/request_log_pipeline.go`、`domains/streaming/handler.go` 多次 `EmitRequestLogUpdate` 属常态；telemetry `releaseBodies` client.go:990-1000 在终态持久化后清空 RequestBody/ResponseBody）到达时 `journal=false`（body 已空 + alreadyJournaled）→ `has_body` 从 1 被覆盖回 0。
- 影响：lite「has_body=1 ↔ body 文件存在」的对应关系在常见时序（终态 → 回填 UPDATE）下必然被破坏；body 文件本身无损，但任何按 has_body 判定「有原文可回放」的读取端（lite 请求详情的设计前提，sink 注释 :128-130 自述该契约）都会漏报。当前 lite 尚无 SQLite request_logs 读端（潜伏），但这是 B2 闭环自身写入契约的断裂，`lite_telemetry_sink_test.go:110-150` 未覆盖「终态后再 UPDATE」场景。
- 最小修复：UPSERT 改 `has_body = MAX(has_body, excluded.has_body)`（SQLite 标记位语义，单调置位）；或 sink 端对已 journal 的 request_id 每次都置 `row.Body="{}"`。补回归：终态 → enrichment UPDATE 后 GetRequest().Body 仍非 nil。工作量 S。

### B-#2 [P2] 闭环4 原语（Reconcile/Repair）零生产调用方——孤儿/缺失在生产中永不检出

- 证据：全仓 grep `ReconcileTurnArtifacts|RepairTurnArtifacts` 仅 `storage/consistency.go`（定义）、`storage/file/bodies_store.go:200`（注释）与 `storage/consistency_lite_test.go`；无定时 worker、无 admin 端点、无启动钩子。`cmd/gateway/storage_mode_init.go` 只装配 cache/bodies 两个 trimmer（:136-148）。
- 影响：lite 跨介质不一致（崩溃孤儿 body / 丢 body）只会在磁盘占用层面被动体现；闭环4 的「重启恢复/孤儿检出」运行态证据仅存在于测试环境。与上轮 B2「原语在、接线无」同构。
- 最小修复：lite 启动完成后低频（每日）对近期活跃 session 跑 `Reconcile` + `RepairReportOnly`（结构化日志/告警），或先暴露为 admin 端点（与 `/metrics/storage` 同层鉴权）；确认无在途写入后再允许 `RepairDeleteOrphanBodies`。工作量 S-M。

### B-#3 [P2] lite 轮号续排只看 body 文件、且 bodies 有保留期而 SQLite 元数据无任何清理执行者——turn_no 复用覆盖旧 meta

- 证据：`cmd/gateway/lite_telemetry_sink.go:203-228`（`nextTurnNo` 初值仅取 `BodiesLister.ListTurns` 最大值+1，不查 `turns.GetTurnsMeta`）；`bg/bodies_trimmer.go:149-158`（按目录 mtime 整目录删除，默认 retention 30 天，config/storage.go:170-172）；SQLite sessions/session_turns 无任何清理路径（`retention.request_logs_days` 有配置无执行者——上轮记录项仍在，storage_mode_init.go:159 仅打日志；`DeleteSession` 亦无生产调用方）。
- 影响：长生命周期会话跨过 bodies 保留期后：`ListTurns=[]` → 轮号从 1 重新分配 → `WriteTurnMeta` UPSERT（turns_store.go:15-22）覆盖旧轮 meta（ts/tokens 漂移、轮号回退）；同时 Reconcile（若接线，见 B-#2）会把这些会话永久报成 MissingBodies 噪音。
- 最小修复：`nextTurnNo` 初值取 max(body 侧, meta 侧)（一个 GetTurnsMeta 调用，S）；中期给 sessions/session_turns/request_logs 一个与 bodies 同步的保留执行者（M）。

### B-#4 [P2] B4 分片锁修复的残余窗口：L3 读在锁外，「Invalidate 整体落在 L3 读之后、回填取锁之前」仍会重播种旧状态

- 证据：`domains/session/v2/cache_v2.go:250`（`LoadState` 在 guard 锁外执行）→ :267-277（lite 回填 L1+L1.5 在锁内，但数据是锁外读到的旧值）；full 模式 L3 命中回填 L1 完全无守卫（:278-280，与 Invalidate 的锁内删 L1 :350-362 无互斥）。分片锁正确闭合的是「L1.5 命中→回填 L1」路径（cache_v2_integration_test.go:228-268 已锁定），未覆盖「L3 旧读值回填」路径——注释 :264-266 只论证了「失效落在回填之后」的正确序，未处理「失效落在回填之前、L3 读之后」的序。
- 影响：与上轮 B4 同性质的脏数据残留（最长 30min TTL），但窗口从「Invalidate 全程」缩窄为「一次 L3 查询耗时」，且需 Invalidate 与冷启动回源精确交错，概率低。属降级残余而非回归。
- 最小修复：per-session 世代计数（Invalidate 时世代++；Get 在 L3 读前记录世代、回填锁内复检不一致则丢弃），或回填锁内重查 L1/L1.5 是否已有更新数据再决定是否覆盖。full 模式 :278-280 一并纳入守卫。工作量 S。

### B-#5 [P2] 附件磁盘清理按 mtime 无引用检查，与内容寻址去重语义互斥——可删掉仍被近期会话引用的附件（已知 F-#5 的具体恶化路径，建议升级其优先级）

- 证据：`admin/data_lifecycle_attachments_filesystem.go:206,221-251`（按 `older_than_days` 对 mtime 早于 cutoff 的文件无条件 `os.Remove`，无任何引用检查）；而 `domains/attachments/storage.go:313-328` 去重命中（同 hash 已存在）时跳过写入且**不更新 mtime**——30 天前保存的图片可被今天的请求 dedup 复用，mtime 清理会删掉新会话正在引用的文件。
- 影响：前端轮次附件「可见不可开」在清理执行后回归；审计表（audit_attachments_filesystem_cleanup）只记删除事实不阻断误删。F-#5（版本管理/引用计数缺失）为已记录 P3，本条给出其真实触发链，建议按 P2 跟踪。
- 最小修复：dedup 命中时 touch 目标文件 mtime（或 sidecar 记 last_referenced_at），使 mtime 清理隐式近似 LRU——一行改动可消除误删主路径；根治需引用注册表（L）。

### B-#6 [P3] RepairTurnArtifacts 在 bodies 未实现 TurnFileDeleter 时静默 no-op

- 证据：`storage/consistency.go:125-135`——`action=RepairDeleteOrphanBodies` 但类型断言失败时不返回错误、不打日志。当前唯一实现 FileBodiesStore 实现了该接口，但接口契约上调用方会误以为修复成功。最小修复：断言失败返回 error。S。

### B-#7 [P3] Reconcile 的 TOCTOU：与在途写入并发时把合法在途 body 误报为孤儿

- 证据：`storage/consistency.go:76-83`（先读 meta 后列 body，无与 sink 写路径的互斥）。`lite_telemetry_sink.go:139-141` 的写入序正是「body 先、meta 后」，并发 Reconcile 会把该窗口内的在途 body 判为 OrphanBodies——未来若把 B-#2 的周期 Repair 直接设为 Delete 策略，会误删在途轮次内容。当前无调用方故无实害；接线时须限定空闲 session 或双重确认。S。

### B-#8 [P3] FileCache 记账自愈（B3 修复）在「自愈后写失败」路径残留小额欠账

- 证据：`domains/session/v2/cache_v2_file.go:291-307`（healed 重置 sizeUsed 不含 excludePath）+ :230-234（healed 后按新值累加）；若随后的 MkdirAll/CreateTemp/Rename 失败（:197-224），旧文件仍在盘但已不在记账内 → 欠账 oldSize，直到下次溢出全树 Walk 自愈。缓存 fail-open 语义下无正确性影响。最小修复：Set 失败分支若 healed 则 `sizeUsed += oldSize`。S。

### B-#9 [P3] bodies 目录内崩溃残留的孤儿 .tmp 文件无清理者

- 证据：`storage/file/async_writer.go:210`（tmp 名 `.{base}-*` 唯一化，崩溃残留不自动消失）；`bg/bodies_trimmer.go` 只按会话目录 mtime 整目录删除（:149-158），活跃会话目录 mtime 恒新 → 其目录内孤儿 tmp 永不清理；FileBodiesStore.ListTurns 的 `Sscanf("turn_%d.json.gz")`（bodies_store.go:222）对 `.` 开头 tmp 不匹配（无误报，但也没人清）。量级小（每次崩溃每路径至多 1 个）。最小修复：BodiesTrimmer 顺带删除会话目录内超龄 `.*.tmp`。S。

### B-#10 [P3] 遗留确认与优化备注（合并，不展开）

- 上轮记录项全部仍在：`ListRequests(nil)` 跨租户（request_log_store.go:130-160）、`GetRequest` 无租户限定、`DeleteSession` 不级联、`enqueue` 持 enqMu 阻塞（async_writer.go:120-133）、FileCache 全程持锁 Walk+文件 IO（cache_v2_file.go:183-229）、shard 目录 byte（cache_v2_file.go:105-111）vs rune（bodies_store.go:48-56）截断不一致、`NormalizeMode` 注释与 TrimSpace 实现矛盾（config/storage.go:82-88）、`ApplyLiteDefaults` 零值兜底导致无法显式关闭保留期（config/storage.go:127-179）。
- B7（lite 下 L2 客户端泄漏）在当前接线下**休眠**：lite 路径 `main.go:2491` 传 redisAddr=""，`cache_v2_redis.go:51-58` 直接返回 disabled 实例，无客户端可泄漏；仅未来「lite + 显式 Redis 地址」装配才会触发，保持记录。
- MemoryStateStore 无容量上限（仅 TTL+junitor，state_store.go:89-97）——当前零生产调用方（已记录边界），接线前需补容量界。
- `liteRequestLogSink.nextTurn` map 按会话累积、无淘汰（lite_telemetry_sink.go:57,206-228）——长生命周期进程内存慢涨（每会话约几十字节），建议与 journaled 集合同款 FIFO 上限。
- L1 命中每次经 JSON round-trip 深拷贝（cache_v2.go:465-506,513-534）——每请求一次微秒级开销，正确性无虞；如需优化可改不可变共享+COW（与闭环7 的 selector 池化同思路）。
- 存储优化正面确认：bodies gzip（70%+ 压缩，测试覆盖中文无损）、telemetry releaseBodies 大对象旁路（client.go:990-1000）、L1 LRU 1024+TTL、L1.5 maxSize+TTL、journaled 集合 10000 FIFO 有界——TTL/容量边界总体处处有界（除上述两处 dormant 例外）。

## 二、已确认闭环（本轮复核通过，不再报告）

| 上轮项 | 复核结论 | 证据 |
|---|---|---|
| B1 lite 健康探针恒不就绪 | ✅ 修复 | `domains/streaming/handler.go:7245-7274`（serveReadyz depsOptional 分支：DB 旁路=local 语义、Redis 已配置才参与判定）、`:7301-7312`（dependenciesReady 同语义）；full 模式 :7276-7296 零变化 |
| B2 lite 五 store 零生产调用方 | ✅ 修复（Bodies/Session/Turns/RequestLog 四 store 接线） | `cmd/gateway/main.go:2187-2190`（SetRequestLogSink 注入）；telemetry 侧 `client.go:76-105`（RequestLogSink 注入缝）、`:490-492`（Enabled=pgEnabled∥requestSink）、`:680`（EmitRequestLog 用 Enabled 门）、`:940-955`（persistRequestLog sink 分支，5s 超时，成功后同走 onPersisted/releaseBodies）、`:652-672`（EmitDecisionLog 保持 pgEnabled 门并注明原因）；sink 实现 `lite_telemetry_sink.go` 全文 + 4 组回归测试（含重启续排/Shutdown 排空）。StateStore 仍未接线（已记录边界，见 B-#10） |
| B3 FileCache 记账自愈负漂移 | ✅ 修复（healed 口径正确） | `cache_v2_file.go:192-234`（ensureSpaceLocked 返回 healed，Set 按「新增文件」口径累加）；仅剩写失败边缘欠账（B-#8，P3） |
| B4 Invalidate 窗口 L1.5 命中回填 L1 | ✅ 主体修复（分片锁） | `cache_v2.go:51-73`（64 分片 guard）、Get 的 L1.5 读+回填 :208-229、lite L3 回填 :267-277、lite Set :299-315、Invalidate 冷→热删除 :350-362 全部同锁互斥；200 轮并发回归 `cache_v2_integration_test.go:228-268`。残余窗口见 B-#4 |
| B5 AsyncFileWriter 并发同路径写 | ✅ 修复（+fsync） | `async_writer.go:210`（CreateTemp 唯一 tmp）、`:229-234`（fsync→rename，闭环第二轮 #15）；关停语义（closeOnce+accepting+enqMu）与 Done 恰好一次（:186-197）复核无误 |
| 闭环4 原语与测试本身 | ✅ 实现质量良好 | `storage/consistency.go`（孤儿可删/缺失绝不伪造语义清晰）、`FileBodiesStore.ListTurns/DeleteTurnFile`（bodies_store.go:201-244，幂等、路径校验完备）；`consistency_lite_test.go` 四场景（重启往返/孤儿检出+两种策略/missing 不伪造/gzip 中文大内容）。缺口在生产接线（B-#2）与删除器缺位语义（B-#6） |
| 24h 报告 fix#11 session_summaries 守卫 | ✅ 修复 | `db/session_summaries_schema.go:45-55`（to_regclass 前置守卫，表缺失告警跳过） |
| 24h 报告 fix#12 附件打开通道 | ✅ 路由侧确认 | `/api/attachments/{path...}` admin 鉴权注册（admin/handler.go:1231）、nil 守卫 503（admin/attachments_routes.go:20-26） |

## 三、冗余 / 待清理代码

1. `storage/factory` full 分支 + `storage/factory/stubs.go`：生产不可达——`NewStorageFactory` 全仓唯一调用点是 `storage_mode_init.go:102`，且仅 lite 分支可达（:92-94 非 lite 提前返回 nil）。文件头自述「结构占位、有意保留」；若 dual-storage 不再计划收敛 full 装配，建议删除或加 build/doc gate，避免被误当 full 生产路径。
2. `AggregateTaskOutcome` 兼容 API（闭环3 报告已记录待随测试迁移后删除）——不重复展开。
3. `config/storage.go:82-88` NormalizeMode 文档注释（称「不做 trim」）与实现（TrimSpace）矛盾——一处注释修正（S）。
4. `storage/factory/factory.go:143` 两个函数共享一个 doc 注释块（`redisOptionsFromURL` 的注释覆盖到 `sqlitePragmasFromConfig` 头上），排版噪音。

## 四、审计清单逐项结论

1. **双架构完整性**：full 与 lite 的装配/健康检查/指标现已对等（B1/B2 闭环复核）；`/metrics/storage` 两模式可用（storage_metrics_endpoint.go:44-65）。遗留：Reconcile 未接线（B-#2）、StateStore 无消费者、request_logs 保留期无执行者（B-#3）。
2. **缓存一致性**：IR→四层链路（L1 clone 安全、L1.5 原子写+mtime TTL、L2 fail-open+SafeHGetAll、L3 ErrNoRows 语义）复核无回归；B4 主体闭环，残余 L3 读窗口见 B-#4；B3 自愈正确（B-#8 为边缘欠账）。
3. **文件缓存与 body 存储**：生命周期完整（写=CreateTemp+fsync+rename；清=CacheTrimmer/BodiesTrimmer 有界启动+ctx 优雅退出）；孤儿 tmp 见 B-#9；gzip 往返无损有测试。
4. **附件与媒体**：上传解析（data URI→流式解码→LimitReader 限 20MB→hash 分片存储→ext 由 content-type 推导）与路径逃逸防护复核到位；删除路径引用缺失见 B-#5；F-#5 建议升级 P2。
5. **lite 跨介质一致性**：原语正确、测试充分覆盖三类窗口；缺生产调度（B-#2）、删除器缺位语义（B-#6）、并发 TOCTOU（B-#7）。
6. **存储优化**：见 B-#10（序列化格式/池化/大对象旁路/边界总体良好，两处 dormant 无界）。
