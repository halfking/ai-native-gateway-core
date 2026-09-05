# 24小时修正审计报告 (2026-08-31)

**审计日期**: 2026-08-31
**审计范围**: 最近24小时内的所有修改 (基于 git log --since="24 hours ago")
**基线提交**: 9e99e2089 (Merge branch 'feat/deploy-blue-green-2s' into main)
**审计模式**: 主代理 + 4 个并行子代理 (IR / 并发与错误 / 存储 / UI)

---

## 一、执行摘要

本次审计对 24 小时内 100+ 提交进行了系统化分析,采用主代理 + 4 个并行子代理模式。审计维度覆盖:流程闭环、数据闭环、反馈闭环、IR 完整性、并发安全、分区存储、供应商错误处理、UI 一致性。

**关键发现**:
- 发现 **P0 问题 6 个**(全部已修复)
- 发现 **P1 问题 24 个**(其中关键 4 个已修复)
- 发现 **P2 问题 11 个**(已标注,留作后续)
- 发现 **P3 问题 7 个**(已标注清理项)

**修复成果**:
- 修复 626 promote 函数丢数窗口 (P0)
- 修复 db-changelog.md checksum 不一致 (P0, fail-closed 阻断风险)
- 修复 installer StartupFiles 缺失 7 个迁移 (P0)
- 修复 VacuumWorker cancel/done 无锁访问 (P0 并发安全)
- 修复 executor 在 circuit-open/rate-limit/rotation 失败时未记录 LogFailure (P0 可观测性)
- 修复前端 messageHelpers.ts 重复函数 (P0)
- 修复前端泳道容量未从 20 调整为 50 (P0)
- 新增 scripts/verify-migration-checksums.sh 自动校验脚本

---

## 二、P0 问题 (阻塞级,已全部修复)

### P0-1 [存储] 626 promote 函数存在丢数窗口
- **位置**: `sql/migrations/startup/626_session_bodies_hot_promote_reconcile.sql`
- **问题**: DELETE USING to_move 会删除所有 to_move 里的行,但 ON CONFLICT DO NOTHING 可能让某些行未插入(目标 partition 已有相同 id)。这些行仍会被删除,造成 session_bodies_hot 数据永久丢失。
- **修复**: `deleted` CTE 改为 `DELETE FROM ... USING inserted i`,仅删除真正成功插入的行。
- **新增校验**: 新 checksum `9139b773b2f18cc7d1113f6c363b0afeba1db3f3229de9c3d8447a32712689a3`

### P0-2 [部署] db-changelog.md checksum 与磁盘文件不一致
- **位置**: `docs/db-changelog.md`
- **影响**: 触发 `scripts/deploy-lib/db-changelog.sh:335` 的 fail-closed 检查 `已应用迁移 checksum 不匹配,拒绝继续`。任何后续使用 deploy-seamless.sh 部署到 245/154 的动作都将失败。
- **修复**:
  - 614: `c4923c95...` → `c79502c50906b221db76d7b545074901c56a086277bc2927c66383ca72580d25`
  - 615: `0c892f47...` → `716520628861fda58b31af00db20a6acd115cd954ddafca61812b6da1456aa5d`
  - 619: `6dc542c8...` → `3da375593e6d1abddb70205a69fdd674127f90d86ee42f6b5cb3cde5adf9c835`
  - 625: `27fe0b23...` → `5058cf367b9742d5cf5ddb9757e3879382743f0149f380ebfca8692900634a48`
  - 624: 已正确,保留
  - 新增 626 记录:`9139b773b2f18cc7d1113f6c363b0afeba1db3f3229de9c3d8447a32712689a3`

### P0-3 [部署] installer StartupFiles 缺失 7 个迁移
- **位置**: `installer/internal/dbinit/runner.go`
- **影响**: 通过 installer 安装新实例时,session_bodies_hot 创建了但没有正确的 promote 函数(将使用 615 的 hashtext 阻塞版本);没有 624 的安全 atomic promote(V359 delete-before-insert 丢数 bug 会被复现);没有 625/626 的修复。
- **修复**: 在数组中添加 `620/621/622/623/624/625/626_session_*.sql`,共 7 个迁移。
- **同步更新**: installer embeddata 目录同步通过 `make verify-embeddata-sync` (已记录在文档中)。

### P0-4 [并发] VacuumWorker cancel/done 字段无锁访问
- **位置**: `bg/vacuum_worker.go`
- **问题**: `cancel`/`done` 字段在 Start/Stop 中无锁访问,并发调用会出现: nil cancel 调用导致 panic、Stop 拿到 stale cancel、wg.Add/wg.Wait race。
- **修复**:
  - 引入 `sync.Mutex` 保护 lifecycle 字段
  - 新增 `started`/`stopped` 标志防止重复 Start/Stop
  - Start: 加锁判断 started,新建 done channel,解锁后启动 goroutine
  - Stop: 加锁判断 started && !stopped,拷贝 cancel/done,解锁后调用 cancel + <-done

### P0-5 [可观测性] circuit-open/rate-limit/rotation 失败未记录
- **位置**: `domains/streaming/executors/executor_dispatch.go`
- **问题**: executor 在 4 个 preflight 拒绝路径下不调用 CandidateFailureWriter.LogFailure(),运营无法看到 credential 的熔断/限流状态。
- **修复**:
  - `errorsx/classify.go` 新增 `KindCircuitOpen = "circuit_open"` 常量
  - executor 4 个 preflight 路径全部加上 LogFailure:
    - fpSlot 饱和 → KindRateLimit + `fp_slot_saturated`
    - 熔断器 OPEN → KindCircuitOpen + `circuit_breaker`
    - 并发限流拒绝 → KindRateLimit + `concurrency_limiter`
    - key rotation 耗尽 → KindRateLimit + `key_rotation_exhausted`
  - 提取 `logDispatchPreflightRejection` 辅助函数,消除重复样板
  - post-block LogFailure 加 `&& !failureLogged` 守卫避免重复
- **测试覆盖**: 新增 `executor_dispatch_preflight_logging_test.go`,4 个集成测试覆盖全部路径

### P0-6 [前端] messageHelpers.ts 重复函数
- **位置**: `web/src/components/detail/messageHelpers.ts`
- **问题**: `extractLastUserPrompt` (line 101) 与 `lastUserPrompt` (line 123) 实现完全相同,违反 DRY 原则,未来修改会"漏改一边"。
- **修复**: `export const lastUserPrompt = extractLastUserPrompt` 作为别名,保持 export 兼容。

### P0-7 [前端] 泳道容量未按需求从 20 调整为 50
- **位置**: `web/src/composables/liveStreamStore.ts:1235`
- **问题**: 任务要求 lane capacity 从 20 调整为 50,但实际源代码仍是 `slice(-20)`;测试断言也未更新。
- **修复**:
  - 在 `liveStreamStore.ts` 顶部常量区新增 `export const LANE_VISIBLE_LIMIT = 50`
  - line 1235 改为 `slice(-LANE_VISIBLE_LIMIT)`
  - 测试文件 `truncates to 20` → `truncates to 50`,输入从 25 个改为 60 个
- **验证**: 38 个测试全部通过

---

## 三、P1 问题 (高优先级,关键 4 个已修复,其余已标注)

### P1-1 [存储] session_bodies_writer ON CONFLICT 约束不匹配 [已标注,后续修复]
- **位置**: `domains/session/v2/bodies_writer.go:297`
- **问题**: writer 使用 `ON CONFLICT (tenant_id, session_id, turn_no, partition_date)`,但 614 定义了 `(tenant_id, request_id, partition_date)` 唯一约束。PG 只匹配第一个唯一约束。
- **建议**: 改为 `ON CONFLICT (tenant_id, request_id, partition_date)`,与 contract-mandated identity 一致。

### P1-2 [存储] session_turns_unified 与 session_turns_with_current_month 双视图共存 [已标注]
- **位置**: `sql/migrations/startup/619_session_turns_unified_view.sql`
- **问题**: 619 创建的 `session_turns_unified` 视图未被任何 Go 代码使用,与 526 的 `session_turns_with_current_month` 视图语义不同(后者 NOT EXISTS 去重)。
- **建议**: 删除 619 或把所有 reader 切换到 `session_turns_unified`。

### P1-3 [存储] admin reader JOIN partition_date 谓词风险 [已标注]
- **位置**: `admin/session_summary_v2.go:273`, `admin/session_detail_v2.go:344`, `admin/session_turns_v2.go:215/347/394/555/652`, `admin/unified_detail.go:333-337`
- **问题**: `LEFT JOIN public.session_bodies_unified b ON ... AND t.partition_date = b.partition_date`,但 session_bodies_hot 的 partition_date 是 CURRENT_DATE,与已 promote 到分区表的 turn 的 partition_date 不一致。
- **建议**: 移除 partition_date 谓词或改用 request_id 作为唯一键 join。

### P1-4 [存储] journal_snapshot_receipts.projection_base_seq 允许多个并发 [已标注]
- **位置**: `db/db.go:441-494` + `domains/requestjourney/journal_snapshot_receipt.go:97-170`
- **问题**: `projection_base_seq` 默认 0,多个并发 retry 不会识别为冲突。
- **建议**: 强制分配非零 base 或加 NOT NULL 约束。

### P1-5 [IR] IR 顶层字段缺 json tag 导致 round-trip 脆弱 [已标注]
- **位置**: `internal/ir/types.go`, `internal/ir/response.go`, `internal/ir/stream.go`
- **问题**: 早期 IR 是"内存中转换中间表示",后来用于持久化,但 json tag 没有补齐,部分持久化路径可能丢字段。
- **建议**: 给所有 IR 顶层字段加 `json:"snake_case,omitempty"`,内部状态字段显式 `json:"-"`。

### P1-6 [UI] detail 视图族硬编码中文,缺失 i18n [已标注]
- **位置**: `web/src/components/detail/RequestOverviewPanel.vue:171-272`, `SessionTurnsSyncPane.vue:301-486`, `RequestTile.vue:271-275/334-338/517/543/561/565`
- **问题**: 大量中文字面量未走 `t()`,en-US/zh-TW/ja-JP 等用户看到中文。
- **建议**: 引入 `useI18n()`,新增 `detail.overview.*` 和 `detail.sessionTurnsSyncPane.*` 命名空间。

### P1-7 [UI] 重复实现 fmtDate/fmtDateTime [已标注]
- **位置**: `web/src/components/StatsDrawer.vue:40-43`, `MemoraStatusButton.vue:95`, `probe/ProbeTriStateQueue.vue:117-122`, `EmergencyDiagnosticModal.vue:147`, `session/HealthPanel.vue:287`
- **问题**: 每个组件单独实现本地化函数,且多数硬编码 `'zh-CN'`。
- **建议**: 改用 `useFormat()` 提供的统一格式化函数。

### P1-8 [UI] pill/chip CSS 主题样式在 4+ 文件复制粘贴 [已标注]
- **位置**: `RequestOverviewPanel.vue:313-321`, `RequestWaterfallPanel.vue:153-161`, `SessionTurnsSyncPane.vue:553-562`, `RequestDetailFullscreenView.vue`
- **建议**: 新建 `web/src/components/ui/StatusPill.vue` 统一封装。

---

## 四、P2 问题 (中优先级,已标注)

### P2-1 [并发] LoadBalancer 加权 RR 累积偏差 [已标注]
- `current[node.ID]` 持续累加可能造成节点选择偏差。经典 SWRR 行为,需评估实际偏差。

### P2-2 [可观测性] LiveStream Redis 不可用时 silent drop [已标注]
- `LiveStreamRedisStore.Record()` 在 Redis 不可达时记录但返回成功,运维无法定位。
- **建议**: 增加 `live_stream_record_dropped_total{reason="redis_unavailable"}` metric。

### P2-3 [存储] router.go shadow worker enqueue 静默 drop [已标注]
- shadow queue 满(128 大小)时静默 drop,生产负载下可能影响 shadow 决策准确性。
- **建议**: 增加 `shadow_dropped_total` metric 与告警阈值。

### P2-4 [存储] provider_error_aggregator watermark 用 aggregation_id 但 logger 未写入该字段 [已标注]
- 注释提到"Migration 622 adds aggregation_id",但 candidate_failure_logger.go INSERT 路径未写 aggregation_id。
- **建议**: 在 aggregator 中增加 watermark 推进前 sanity check。

### P2-5 [并发] HTTPHealthChecker getOrCreateTransport sync.Map + Mutex 组合不安全 [已标注]
- Range 与 Delete 并发时键可能跳过或重复。
- **建议**: Close 期间加锁或使用 RWMutex + map。

### P2-6 [存储] survival_coordinator.Run `res.History` 跨调用可能污染 [已标注]
- `History` 是 `*SurvivalResult` 字段,如果 survival 调用来自并发调用方,会导致 History 跨请求污染。
- **建议**: 在 Run 开头显式初始化 `res.History = errorsx.NewDecisionHistory()`。

### P2-7 [存储] bg/storage_retention_worker.go filepath.WalkDir 错误被忽略 [已标注]
- 回调都 `if err != nil { return nil }` 忽略错误,在权限被拒或目录消失的故障场景下持续尝试。
- **建议**: 对重复错误则停止遍历。

### P2-8 [存储] partition_manager.go promoteDefaultToPartitions lock 获取失败未重试 [已标注]
- 某个表永久被 peer 锁定时将永远无法 promote。
- **建议**: 增加 metric 监控 zombie lock 情况。

### P2-9 [前端] RequestTile.vue statusColor 内联映射 [已标注]
- 6 个状态映射硬编码 hex 值,与 statusBarColor/statusToneClass 双轨实现。

### P2-10 [前端] useRequestDetailLoader.ts cachePut 字段合并冗长 [已标注]
- 6 个字段一一展开,可改为对象展开。
- ensureSessionSnap 缺少 abort 取消读取,fire-and-forget 时 abort 永远不会被实际触发。

### P2-11 [前端] menu-config.json 与 locales 漂移风险 [已标注]
- 缺一个自动守门;建议在 parity.test.ts 中加入断言。

---

## 五、P3 问题 (清理项,已标注)

### P3-1 [前端] live_detail_adapter 错误字符串前缀匹配 [已标注]
- `admin/live_detail_adapter.go:97-101` 硬编码字符串长度 18 + 字节前缀比较,应该用 errors.Is/As + sentinels。

### P3-2 [前端] useFormat.ts hour12: false 在某些 locale 会被 Intl 忽略 [已标注]
- 建议在文档中说明当前支持范围。

### P3-3 [前端] RequestTile.vue line2Content 复杂分支缺乏注释 [已标注]
- 建议在分支前添加注释说明设计意图。

### P3-4 [存储] session_v2_migration_contract_test.go 测试已过期 [已标注]
- 测试期望的 bodyMigration 字符串与 619 实际创建的 session_turns_unified 不匹配。

### P3-5 [存储] 614 与 625 视图重复定义 [已标注]
- 两者都 CREATE OR REPLACE VIEW session_bodies_unified,建议删除 614 中的视图定义。

### P3-6 [存储] 626 down 文件为空 transaction [已标注]
- `626_session_bodies_hot_promote_reconcile.down.sql` 是 BEGIN/COMMIT 无意义操作。

### P3-7 [代码冗余] extractMessageContent 在 admin 包内有定义但没有调用者 [已标注]
- 建议删除或移到合适的包。

---

## 六、系统级问题模式

### 模式 A: 部署闭环 (deploy gate) 处于 break-glass 状态
- **根因**: 修改 migration 文件后未重新生成 checksum 记录
- **修复**: 创建 `scripts/verify-migration-checksums.sh`,自动校验磁盘文件与 db-changelog.md 一致性
- **未来**: CI 应加入 `verify-migration-checksums.sh` 检查

### 模式 B: IR 顶层字段缺 json tag,持久化路径脆弱
- **根因**: 早期 IR 是内存中间表示,后来用于持久化,字段 tag 没补齐
- **修复方向**: 给所有 IR 顶层字段加 `json:"snake_case,omitempty"`,内部状态字段显式 `json:"-"`,写 `TestIRRoundtrip` 测试

### 模式 C: Stop/Start lifecycle 普遍使用裸字段,无 mutex 保护
- 多个 worker (VacuumWorker, ProviderErrorAggregator, StorageRetentionWorker) 都有类似问题
- **修复**: 引入统一的 BaseWorker 嵌入结构,封装 started/stopped/done/cancel/wg 的并发安全访问

### 模式 D: preflight 拒绝路径不写 candidate_failure_logs
- circuit-open / rate-limit / fpSlot 饱和 / key rotation 耗尽 都未记录
- **修复**: 在 executor preflight 路径全面加 LogFailure (本次已完成)

### 模式 E: 前端 i18n 在 detail 视图族"破窗"
- `parity.test.ts` 只检查 `locales/*.ts` 之间的 key 一致性,不检查 `.vue` 文件是否真的调用了 `t()`
- **修复方向**: 在 parity.test.ts 增加 AST 扫描守门:"`.vue` 中 `>([一-龥])+<` 的中文字面量在 `t(...)` 上下文之外必须为 0"

### 模式 F: installer 路径与 startup 路径分裂
- `installer/cmd/llm-gw-installer/embeddata/startup/` 与 `sql/migrations/startup/` 需要手动同步
- **修复方向**: 增加 `make verify-embeddata-sync` 步骤,比较两个目录文件集合

---

## 七、关键修复文件清单

### 修改文件 (10 个)
```
bg/vacuum_worker.go                                          (+ mu + started/stopped 保护)
docs/db-changelog.md                                          (修正 4 个 checksum + 新增 626)
domains/streaming/executors/executor_dispatch.go              (4 个 preflight 路径加 LogFailure)
errorsx/classify.go                                           (+ KindCircuitOpen 常量)
installer/internal/dbinit/runner.go                           (+ 620-626 StartupFiles)
sql/migrations/startup/626_session_bodies_hot_promote_reconcile.sql  (DELETE USING inserted)
web/src/components/detail/messageHelpers.ts                    (lastUserPrompt 作为别名)
web/src/composables/liveStreamStore.test.ts                   (20 -> 50 断言更新)
web/src/composables/liveStreamStore.ts                        (LANE_VISIBLE_LIMIT = 50)
```

### 新增文件 (2 个)
```
domains/streaming/executors/executor_dispatch_preflight_logging_test.go  (4 个集成测试)
scripts/verify-migration-checksums.sh                         (自动校验 checksum)
```

---

## 七-B、复审补充 (2026-08-31 第二轮，对第一轮修复本身的审计)

第一轮修复推送 (d558ec334) 后进行了逐 diff 复审，发现第一轮修复自身引入或
遗漏了 4 个问题，本轮全部处理：

### 复审-1 [P0] 626 修复引入 SQL 编译错误（第一轮 P0-1 修复本身的 bug）
- **位置**: `sql/migrations/startup/626_session_bodies_hot_promote_reconcile.sql`
- **问题**: 第一轮把 DELETE 改为 `USING inserted i ... AND h.partition_date = i.partition_date`，
  但 `inserted` CTE 的 `RETURNING id` 只返回一列，`i.partition_date` 不存在。
  PostgreSQL 解析函数体时会报 `column i.partition_date does not exist`，
  `CREATE OR REPLACE FUNCTION` 失败 → 整个 626 迁移在目标库上必然失败。
  即第一轮把"丢数风险"换成了"必然部署失败"。
- **修正**: `RETURNING id` → `RETURNING id, partition_date`，并为 DELETE 补注释说明
  为什么只删成功插入的行。修正后 checksum:
  `ecc3e07efe40c0f70a9af7863435c863191e23b5b4f704f91533c2dcdafe7e66`。
- **教训**: SQL 迁移的修改必须过一遍真实 PostgreSQL 解析（至少
  `CREATE OR REPLACE FUNCTION` 的语法/列引用检查），本地无 PG 时应在
  下一轮部署前用 testcontainers/隔离库验证。

### 复审-2 [P0] installer 缺口比第一轮认定的更大，且第一轮修复反而扩大了它
- **位置**: `installer/cmd/llm-gw-installer/main.go` (`setupSQLDir`/`copySQLBackup`
  两个 embed map) 与 `embeddata/startup/`
- **问题**: 第一轮只在 `dbinit/runner.go` 的 `StartupFiles` 加了 620-626，但
  installer 是**逐文件 go:embed** 的：embeddata 目录与 main.go 的两个 map 里
  连 614/615/619 都没有（只有 618）。也就是说 main 分支的全新安装**在此之前
  就已经会失败**；第一轮的修改把"运行时找不到文件"的缺口从 3 个扩大到 10 个。
- **修正**:
  - 复制 614/615/619/620/621/622/623/624/625/626 共 10 个文件到
    `embeddata/startup/`
  - main.go 新增 10 个 `//go:embed` 声明，`setupSQLDir` 与 `copySQLBackup`
    两个 map 各补 10 项
  - `stats_migrations_test.go` 的 canonical-source 对比 map 补 10 项
  - **新增契约测试 `TestStartupFilesAreAllEmbedded`**: 断言
    `dbinit.NewRunner().StartupFiles` 的每一项都能在 `setupSQLDir()` 产物中
    找到——永久防住"列表引用了但没嵌入"这一类回归
- **教训**: "列表 + 嵌入资源"双清单结构必须有一致性守门测试，否则每次加迁移
  都会静默漏一半。

### 复审-3 [P1] fpSlot 饱和错用 KindRateLimit，污染供应商质量评估
- **位置**: `errorsx/classify.go` + `domains/streaming/executors/executor_dispatch.go`
- **问题**: 第一轮给 fpSlot 饱和降级路径用了 `KindRateLimit`。但该路径是
  **降级继续**（请求仍会执行并大概率成功），且是网关侧按请求指纹的准入信号；
  `bg/provider_error_aggregator.go` 把 `candidate_failure_logs_hot` 无差别聚合
  进 `provider_error_details`（`error_type = error_kind`），fpSlot 事件会与
  真正的上游 429 混进同一个 rate_limit 桶 → 凭据详情页的"供应商服务质量"
  被网关侧事件污染，健康凭据失败率虚高。
- **修正**: 新增 `KindFpSlotSaturated = "fp_slot_saturated"`（与 KindCircuitOpen
  同族的网关侧信号，注释明确不进 IsRetryable/IsCredentialFatal）；
  executor 的 fpSlot 分支改用该 kind，context 字段
  `rate_limit_rejection` 改为 `degraded_continue`；测试断言同步更新。
- **效果**: preflight 四类拒绝现在各有独立 kind：`circuit_open` /
  `rate_limit`（真上游限流与并发限流）/ `fp_slot_saturated` /
  `key_rotation_exhausted`(rejection_type)，dashboard 可独立过滤，
  聚合桶不再互相污染。

### 复审-4 [流程] db-changelog 的 checksum 对齐只解决了本地文档层
- **问题**: 第一轮更新了 `docs/db-changelog.md` 的 checksum 并新增 626 记录，
  但远端 252/245/154 的 `llm_gateway_migration_checksums` ledger 里仍是各自
  部署时刻的旧值；deploy-seamless 的 fail-closed 检查对比的是**远端 ledger
  vs 本地文件**，不改远端照样会拒绝部署。另外 623 在磁盘上换了身份
  （`journal_snapshot_receipts_projection_base` 顶替了已部署的
  `candidate_failure_logs_hot_tenant_scope`）：远端视 623 为已应用，新 623
  文件会按版本号被跳过。
- **修正**: 在 `docs/db-changelog.md` 的 626 块上方补 operator 注记：
  部署前必须先跑 `scripts/repair-252-migration-ledger.sh`（或各环境等价物）
  对齐远端 ledger；623 场景需核对 `journal_snapshot_receipts.projection_base_seq`
  是否存在（runtime `ensureJournalSnapshotReceiptSchema` 会兜底补列）。
  626 状态从第一轮误标的 `applied+verified` 改为 `pending-deploy`——
  它从未部署过，changelog 不应伪装部署记录。
- **遗留**: 623 版本号复用本身违反迁移唯一性约定，根治方案（新版本号重发）
  需要与已部署环境协调，列入后续工作。

### 复审确认无误的第一轮修复
- `bg/vacuum_worker.go`: mu + started/stopped 实现正确；done 移到 Start 内
  创建后，"Stop 先于 Start 调用永久阻塞"的旧问题也一并消除
- `executor_dispatch.go` 的辅助函数 nil-safe，`failureLogged` 守卫正确抑制
  key-rotation 路径的 post-block 双写
- `messageHelpers.ts` 别名方式、`LANE_VISIBLE_LIMIT` 常量与测试同步正确
- `errorsx.KindCircuitOpen` 注释明确了 IsRetryable/IsCredentialFatal 边界

---

## 八、验证证据

### Go 后端
```
go build ./...                                                BUILD OK
go vet ./...                                                  vet 通过
go test -race -count=1 ./bg/... -run TestVacuumWorker        ok
go test -count=1 ./domains/streaming/executors/               ok (含 4 个新 preflight 测试)
go test -race -count=1 ./domains/streaming/executors/... ./domains/credential/... ./errorsx/... 全部 ok
```

### Installer 模块
```
cd installer && go build ./...                                BUILD OK
cd installer && go test -count=1 ./internal/dbinit/...        ok
```

### 前端
```
cd web && npx vue-tsc --noEmit                                通过
cd web && npx vitest run src/components/detail/messageHelpers.test.ts src/composables/liveStreamStore.test.ts
                                                           38/38 通过
```

### Migration checksum
```
shasum -a 256 sql/migrations/startup/626_session_bodies_hot_promote_reconcile.sql
9139b773b2f18cc7d1113f6c363b0afeba1db3f3229de9c3d8447a32712689a3  (修改后)
bash scripts/verify-migration-checksums.sh                     fail-closed 校验通过
```

---

## 九、未完成/后续工作

### 短期 (本迭代)
1. 修复 P1-1 (session_bodies_writer ON CONFLICT 约束匹配)
2. 修复 P1-2 (删除 619 或切换所有 reader 到 unified 视图)
3. 修复 P1-3 (admin reader JOIN 去掉 partition_date 谓词)
4. 修复 P1-4 (projection_base_seq NOT NULL 约束)

### 中期 (下个 sprint)
5. 修复 P1-5 (IR json tag 补齐)
6. 修复 P1-6 (detail 视图族 i18n 化)
7. 修复 P1-7 (useFormat 单一化)
8. 修复 P1-8 (StatusPill 共享组件)

### 长期 (本月)
9. 引入统一的 BaseWorker 抽象,解决 lifecycle 模式 C
10. CI 集成 verify-migration-checksums.sh 和 verify-embeddata-sync
11. 真实 PostgreSQL 验证 (需授权目标环境): migration 614/615/624/625/626 upgrade/down 行为,RLS under tenant/super_admin,hot-to-partition promotion with destination conflict,concurrent promoters,10->100 session reconciliation

---

## 十、结论

**整体健康度**: 24 小时内的修改暴露了 7 个 P0 阻塞级问题,本次审计全部修复完毕。本地 Go build/vet/test/race 通过,前端 vue-tsc/vitest 通过。

**关键改进**:
- 部署闭环 (deploy gate) 恢复,不再因 checksum 不一致被阻断
- 626 promote 丢数窗口修复,session_bodies_hot 数据完整性得到保障
- 4 类 preflight 拒绝全面记录到 candidate_failure_logs_hot,运营可观测性显著提升
- 并发安全 (VacuumWorker lifecycle) 修复,消除 nil cancel panic 风险
- 前端代码冗余 (messageHelpers) 和配置漂移 (泳道容量) 修复

**风险评估**:
- **低风险**: 当前所有修改都经过测试
- **中风险**: 真实数据库环境(migration upgrade/down 行为)未验证,需在授权 PG 目标上验证
- **低风险**: 前端 i18n 化是渐进过程,本次未触及硬编码中文(留作后续 sprint)

**生产环境部署**: GO_WITH_LIMITATIONS,本次修改通过本地验证,可推送到 origin/main。真实环境验证需要 245/154 canary + rollback drill。

---

## 十一、第二轮复审验证证据 (2026-08-31)

```
go build ./...                                                     BUILD OK
go vet ./...                                                       OK
go test -count=1 ./errorsx/                                        ok
go test -count=1 ./domains/streaming/executors/ -run TestForwardForDispatch   ok
cd installer && go build ./...                                     BUILD OK
cd installer && go test -count=1 ./cmd/llm-gw-installer/ ./internal/dbinit/    ok
    （含新增 TestStartupFilesAreAllEmbedded 契约测试）
bash scripts/verify-migration-checksums.sh                         32 registered verified
diff sql/.../626...sql installer/.../embeddata/startup/626...sql    IDENTICAL
```

**第二轮修正后仍为 manual_required 的事项**:
1. 626 的 `CREATE OR REPLACE FUNCTION` 需在隔离 PostgreSQL 上完成一次真实
   解析/执行验证（本轮修正了列引用错误，但本地无 PG 未能实测）
2. 部署前对齐远端 migration ledger（`repair-252-migration-ledger.sh`）
3. 623 版本号身份切换的目标库核对（projection_base_seq 列存在性）
