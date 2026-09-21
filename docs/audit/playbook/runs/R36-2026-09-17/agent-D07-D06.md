# D07（hot+columnar 分区不变量）+D06（双重存储架构）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论与修复见轮文档 §一（迁移 717）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| A-1 | P2 | 716 down 不可逆（部分回滚）：up 改 7 对象（6 视图+1 函数），down 只恢复 3 个 → 回滚后 /model/{model}/nodes、/timeline、/summary 42P01 直到重启 ensure 重建 | 716_*.down.sql:4-10 vs 716_*.sql:22-28,:221,:319,:337 | 补 down 旧体或登记（登记 R36） |
| A-2 | P2 | 716 主体（5 视图+函数）双通道无自动等价守卫：db.go:4111-4449 内嵌 SQL 与迁移是两份手工拷贝，现有守卫只覆盖 compat CASE 片段 | db/db.go:4111-4449 vs 716_*.sql:22-385；probe_views_unified_test.go:44-58 | 升级 collapseWS 全文等价守卫（登记 R36） |
| A-3 | P2（线索） | 统一遗漏面：仍直读/直写旧 model_probe_state 的路径未切（credentialstate/cache.go:165、credentialhealth/checker.go:642、provider/client.go:1586,1657,2368、probe_dashboard.go:2249、routing.go:5542,5557、diagnostics_*.go:300/:259） | 各 file:line | 逐点核验门控（部分已修 R36：backfill 源、diagnostics 双清；余登记） |
| A-4 | P3（观察） | 716 视图族时区未钉扎（裸 NOW()/DATE_TRUNC）——非 playbook 强制项，314 时代同形态 | 716_*.sql:72,113,184-190,300-316,323,333 | 可不处置 |
| A-5 | P3（旁证） | promote 函数列清单含 customer_id（hot text→母表 bigint 无 cast）→ 252 hot 确为 text 则每批次 42804 空转 | promote_request_logs_hot_to_partition_interval_integer.sql:88-92,:121,:147 | **迁移 717 对齐后自愈** |
| A-6 | P3 | proxy/store_pg.go 窗口改动实为 bans 回放修复，健康；无 lite 缺口 | store_pg.go:441-449,468-472,530-555 | 健康面记录 |

## 二、核实为健康的面
- 716 四不变量：零 INSERT/UPDATE/DELETE；真实请求 24h 聚合读 request_logs_with_current_month（hot∪母表）
- Go ensure ↔ 716 双通道当前逐段一致；NodeProbeStateCaseSQL 单一事实源三方复用+tripwire 测试
- 老读方契约保持：dashboard 28 列/system_health 20 列列序钉桩；派生状态 6 词夹具断言
- installer 五点同步（716）全绿（字节级 diff 空、parity map、≥704 守卫覆盖）
- banned_regions TEXT[] 写点一致；lite 模式无新分区/redis 误启动

## 四、01-schema 漂移取证（修复 F1 的要素）
1. 载体三份：sql/schema/01-schema.sql（canonical，08-04 从 252 dump）、deploy/sql/schemas/baseline/01-schema.sql、installer embeddata/01-schema.sql；再生成 dump-schema.sh（需真库 DSN）
2. 实测 10 列漂移（R34 的"12 列"多报 tenant_id/response_body 两列——该两列实际一致）：6 硬不兼容 customer_id text→bigint、content_safety_score float8→jsonb、dlp_violations text[]→jsonb、protocol_conversion text→boolean、ir_extensions text→jsonb、sanitizer_mutations text→jsonb；4 同族 varchar↔text（agent_name/agent_type/api_key_fingerprint/task_id）
3. 炸点：request_logs_view_schema.go:109-115 动态 hot∩parent 交集 UNION ALL → 42804 → db.go:136-138 启动链中止；迁移通道 680:84-107 同炸；现网不炸因 baseline 不含 wrapper 视图+459 冻结形态早退
4. 603:28-39 已记录 9 列为 "accepted design divergence"（08-25）——先于 680 动态交集重建，该接受由 717 退休
5. 守卫升级落点：sql/migrations/startup/ 新列型契约测试，复用 baselineEnsureSources/readBaseline
6. 252 生产同漂移置信度：baseline 头注释（08-04 dump 自 252）+ 603 引用 25 处差异实测报告；mtime 09-14 与头注释不一致，ALTER 前须真库复核 information_schema

（主代理按取证落地：迁移 717 + 三 baseline 手工对齐 + TestBaselineRequestLogsHotColumnTypesMatchMother/TestBaselineRequestLogsAlignedColumnsPinned + installer 五点 + sequence 登记；真库复核与 dump-schema 重导列入部署前置）
