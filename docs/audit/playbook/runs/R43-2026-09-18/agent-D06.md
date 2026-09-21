# D06 双重存储架构 子代理报告（窗口：643735a28^..HEAD，48h；重点未审计增量：b75c91900..HEAD）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| 1 | **P0/P1** | **726 未进 installer 交付链，且两个钉桩测试在 HEAD 实测红**：`726_restore_credential_model_index_hot_unique.sql` 只做了 embeddata 文件镜像（58384b0d8 自述"721 先例约定"），但 StartupFiles、go:embed var + embeddedSQLFiles map、parity map 三点全缺。本机实跑 `go test ./cmd/llm-gw-installer/` 确认 `TestStartupFilesAreAllEmbedded` 与 `TestCanonicalStartupMigrationsAtOrAbove704AreRegistered` 双红（该 commit 只跑了 `./sql/migrations/startup/` 测试） | installer/internal/dbinit/runner.go:30-185（无 726）；installer/cmd/llm-gw-installer/main.go（grep 726 零命中）；installer/cmd/llm-gw-installer/stats_migrations_test.go:308-387（红）；embeddata/startup/726_*.sql 存在 | 全新装机：installer 顺序应用 718（drop 三套等价唯一索引中的两套）→ 726 不在链上 → credential_model_index_hot 无唯一索引 → 网关首启 auto route rollup `ON CONFLICT (bucket,credential_id,raw_model)` 必 42P10（154 生产事故在全新装通道精确复现，且无 Go 侧 ensure 兜底——db.go 无该索引的自愈） | 按 721/724 五点同步补齐三点（runner.go StartupFiles + main.go embed/map + parity map 条目），补跑 installer 包测试 |
| 2 | **P2** | **installer 内嵌 01-schema.sql 的 api_key_auto_profile 仍是旧形态（缺 PK）**：窗口内 3a9dca46d 给 canonical 两处补了 `api_key_auto_profile_pkey PRIMARY KEY (api_key_id)`，但 embeddata 副本未同步（最后改动停在窗口前的 R36 ad68c91ac）。新装库拿到无身份表，靠网关首启 ensure 补 UNIQUE INDEX（非 PK 约束）——R40"中间形态漂移"在 installer 通道重现；钉桩测试只查 canonical 两路径，安装器副本是盲区 | installer/cmd/llm-gw-installer/embeddata/01-schema.sql:5145-5151（无 PK）；sql/schema/01-schema.sql:5279（有 PK）；db/db_api_key_auto_profile_test.go:68-81（只读 `../sql/` 两路径）；git log 两文件归属佐证 | 全新装机 → 01-schema 建出无 PK 表 → 首个网关进程 ensureApiKeyAutoProfileIdentity 建出 `api_key_auto_profile_api_key_id_key` 唯一索引——ON CONFLICT 功能恢复，但与 canonical 库的对象类别（constraint vs index）与名字永久分叉，未来引用 pkey 名的 SQL 在装机器库上 42P01 | 同步 embeddata/01-schema.sql；把安装器副本纳入 TestApiKeyAutoProfileSchemaHasIdentity（或 01-schema 纳入 parity 测试） |
| 3 | P3 | **STORAGE_MAX 误导护栏 Warn 在标准部署上是永久噪音，且 lite 侧静默死配置**：(a) deploy-252-gateway.sh 无条件向 252 网关 .env 写 `LLM_GATEWAY_STORAGE_MAX_CONNECTIONS=200`、start-full.sh 默认 export 100——二者均为 full 模式，每次启动必触发新加的 Warn；(b) main.go 注释称该 env 喂"lite 的 SQLite MaxOpenConns"不实：initStorageMode 从不把 MaxConnections 传给工厂，且 applyEnvOverrides 只填 Full 段——lite 下该 env 同样死但无任何告警 | cmd/gateway/main.go:440-453（Warn 与注释）；scripts/deploy-252-gateway.sh:124；scripts/start-full.sh:27；cmd/gateway/storage_mode_init.go:110-119（未传 MaxConnections）；config/storage.go:369-372 | 运维按 252 部署脚本起 full 网关 → 每次启动一条误导护栏 Warn（脚本注释自知 env 无效却保留）；lite 运维设该 env 期望调 SQLite 池 → 静默无效 | 从 252/start-full 脚本移除该行（或注释明示接受告警）；修正 main.go:441 注释；lite 下同告警或文档声明死配置 |
| 4 | P3 | **724 重编号残留**：迁移文件头注释仍写 `-- 721_task_type_corrections.sql`；Go 侧 ensure 镜像缺文件里的 `COMMENT ON COLUMN agrees`（其余 DDL 逐字一致） | sql/migrations/startup/724_task_type_corrections.sql:1,43-44；db/db.go:1581-1613 | 阅读迁移目录/对账 ensure 与文件形态时混淆版本归属；无运行时后果 | 头注释改 724；ensure 补 COMMENT ON COLUMN（幂等） |
| 5 | P3 | **taskprofile "独立性"注释过誉**：initTaskProfile 声称 "Independent of ROUTING_OPT_ENABLED"，但整个调用点位于 `if dbConn != nil && dbConn.Enabled() && !config.IsCredRecoveryDisabled()` 大块内（3711 开、5590 闭）——设 `LLM_GATEWAY_CRED_RECOVERY_DISABLED=true` 会连带关闭 overlay 加载/优化器/autoroute 装配（既有结构，非本窗口引入；lite 下 dbConn=nil 跳过属正确降级） | cmd/gateway/main.go:3711,4993,4998,5590；cmd/gateway/routing_optimizer_init.go:145-147 | 运维关凭据恢复巡检 → 模型 auto 解析与 task-profile overlay 一并消失，注释承诺与实际门控不符 | 至少修注释；中期把 autoroute/taskprofile 装配挪出 cred-recovery 门控（P3 登记） |
| 6 | P3 | **TestDurableFamilyPrerequisitesRegistered 只钉 516 前置，未钉 520**：依赖物仅校验 `pos < position["516..."]`，520（settlement intents，FK→tasks）被挪到 657/722 之后不会被测试捕获 | installer/cmd/llm-gw-installer/stats_migrations_test.go:414-425 | 后续重排 StartupFiles 时 520 顺序回归不可拦截（当前实际顺序正确） | 循环里加 base520 同样断言 |

## 二、核实为健康的面

- **DB_MAX_CONNS 配置链闭环**：env → `poolMaxConnsFromEnv`（db/db.go:94-110，坏值 Warn+回落默认、TrimSpace、int32 溢出防护）→ `cfg.MaxConns`（db/db.go:54）→ pgxpool，启动日志带 `max_conns/min_conns`（db/db.go:85）；8 形态用例钉桩（db/poolsize_test.go）。lite 模式 main 强制空 URL 跳过 db.Open（main.go:491-496），该旋钮天然无关。
- **ApplyDefaults 死配置注释纠偏准确**：全仓无生产调用方（仅测试）；工厂 full 分支为桩（storage/factory/stubs.go + factory.go:78-99），initStorageMode 仅 lite 构造工厂——注释"生产 pool 只有 DB_MAX_CONNS"与代码一致，storage_test.go 默认值 200 断言同步。
- **taskprofile 在 lite/no-DB 的降级诚实**：lite 下 dbConn=nil → 3711 大块整体跳过（无任何 pgx 调用）；buildRoutingOptimizer 对 nil pool 显式 Warn+返回 nil 走 baseline（routing_optimizer_init.go:48-51）；admin 端点仅 `h.db != nil` 才注册（admin/handler.go:1329/1355-1359，lite=404，与 annotations/routing-opt 等兄弟端点同一既有约定），注册后还有 handler 级 ensurePool 503 兜底（taskprofile/handler.go:298-304）；routingopt 修正混合在 CorrectionStats 出错时仅告警降级不破坏路由（routingopt/confidence.go:109-118）。
- **ensureApiKeyAutoProfileIdentity 实现质量**：SET lock_timeout 走专用连接而非 pooled 连接上的 SET LOCAL（R40 教训落地，测试 db/db_api_key_auto_profile_test.go:61-63 钉死）；55P03 重试 ×3 + RESET；canonical 库上 PK 底层索引使探针命中即 no-op；探针/DDL 失败经 R39 IsSchemaMismatchError fast-fail 进 no-DB 降级而非烧重试预算。
- **installer 516/520 R42 修复本体**：StartupFiles 中 516/520 位于 515 之后、521/657/722 之前，启动链中引用 durable_llm_tasks 的仅 516/520/657/722 四文件（全目录 grep 佐证）；embeddata 副本与 canonical 逐字节一致（diff 实测）；516/520/724 幂等（IF NOT EXISTS）、725 幂等（DROP POLICY IF EXISTS）；每文件 `--single-transaction + ON_ERROR_STOP`。
- **settings ttl_cache 失效一致性**：StoreDB.Set 两分支均 InvalidatePlatformValue；Rollback 仅 platform 作用域失效（缓存只存 platform 条目，语义正确）；Delete platform 成功才失效；失效不缓存失败值、registry 指针防换 Global 串味、5s TTL；InvalidatePlatformInt/InvalidateSettingsCache 双缓存同清。CachedPlatformBool/String/Float 当前无生产消费者（opt-in），唯一生产缓存读者 CachedPlatformInt 维持 TTL 界（预存在）。
- **lite 六 env 收口禁 Redis 未回归**：liteRedisEnvKeys 六键 Unset+Warn 逻辑无窗口改动（storage_mode_init.go:215-240）；窗口新模块（taskprofile/db ensures）零 Redis 依赖。
- **mDNS 装配抽检**：main.go:7021-7082 独立段装配、启动失败仅 Error 不阻断、关闭序最先 Stop；b484efbac 缺 net import 已修，D06 相关包 `go build` 全绿。
- **窗口内未引入 lite 下会误启动的分区/PG 专属调用**：726 等 SQL 仅入 installer/full 链；无新分区管理器装配点。

## 三、未覆盖项与原因

- **真机全新装实跑**：#1/#2 的 42P10 后果由链路推演 + 钉桩测试实测红佐证，未在 docker/Citus 环境执行真实 installer（无沙箱凭据）。
- **ensure 双兄弟在真 PG 上的实跑**：ensureApiKeyAutoProfileIdentity 在"表整体缺失"的远古库上会 42P01 fast-fail 进 no-DB 降级——路径推演成立，未真库复现。
- **lite 侧 taskprofile 等价实现的产品裁决**：代码层确认"显式不存在"而非静默空实现，但 lite-mode 系列设计文档是否已把 taskprofile 列入能力边界清单未逐篇核对（超出窗口时间预算）。
- **docs/storage/README 布局与 §3.3**：窗口零改动 lite 文件布局，未重走全量布局比对。
- **taskprofile CorrectionStore 内部（CSV 导入导出、request 存在性校验依赖 auto_route_selections_all 视图）**：属 D09/D12 域，未展开。
- **CI 是否实际执行 installer 模块测试**：本地双红已实锤；线上 CI 门是否含该包未核实（若含则 #1 升 P0 实锤）。

**主代理复核结论（R43）**：#1 成立实锤（双测试亲跑复现红）→ 已按五点同步补齐（runner.go/main.go embed+map/parity map/revision-sequence），双测试转绿；#2 成立 → embeddata/01-schema.sql 已补 PK 行 + 钉桩测试纳入第三路径；#3/#5 注释已纠偏（脚本噪音登记遗留）；#4/#6 已修；#4 的 ensure COMMENT 已补。
