# T1 reportrollup 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。主代理复核结论见轮文档（P1#1/P2#2/P2#3 及 P3 批次均复核属实并当轮修复）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1** | **internal_person scope 跨租户 UNIQUE 冲突 → 静默数据覆盖**。internal_person 聚合 GROUP BY 是 `(tenant_id, person)`，但写库时 `b.ScopeKey = person`（tenant 不进四键），ON CONFLICT 目标 `(scope, scope_key, report_date, raw_model_name)` 不含 tenant_id。同一天两个租户各有同名 end_user_id（或双缺都归 `unknown`）的业务流量时，产生两个冲突键完全相同的 bucket，第二个 DO UPDATE 把第一个租户的计数整体覆盖（非合并、非报错，且谁覆盖谁取决于 SQL 返回顺序，非确定性）。触发路径：worker 每日 RollupDay / admin POST /run 聚合含跨租户同人流量的日期 → report_snapshots 的 person 行永久少计。E2E 种子数据每 person 只挂单租户（`realdb_e2e_test.go:81-86`），恰好测不到。 | domains/reportrollup/rollup.go:396-409（GROUP BY tenant_id,person）、:429（ScopeKey=person）、:571-627（upsert，:599 冲突键）；sql/migrations/startup/745_report_snapshots.sql:63-65（UNIQUE 无 tenant_id）；对照 internal_model 无此问题（:497 scope_key=tenant 进入冲突键） | 建议冲突键纳入租户（如 scope_key=`tenant\x00person` 或 UNIQUE 加 tenant_id 需新迁移），或聚合时在 Go 侧按 person 跨租户折叠；修复时补双租户同人 E2E 用例 |
| 2 | **P2** | **internal 视角 + tenant_id 过滤时「按天」序列恒为空**。`ScopeInternalTenant` case 仅在 `filter.TenantID == ""` 时 addToDay，而总计/租户分组在该过滤下照常填充 → `GET /api/admin/report-rollup/summary?view=internal&tenant_id=X` 返回 Totals 有值但 `days=[]`。provider 视角同形过滤（provider_id）却能从 daily_by_provider 补按天（:321-323），处理不对称，应为无意遗漏。前端当前不发 tenant_id，但 `web/src/api/reportrollup.ts:89` 已暴露该参数，未来加过滤 UI 即触发。 | domains/reportrollup/report.go:324-327（days 被跳过）、:328-329 与 :358-363（totals/tenants 照常填充）；对照 :315-323 | tenant 过滤存在且无 model 过滤时也 addToDay（数据已由 SQL WHERE tenant_id 收窄，无双计） |
| 3 | **P2** | **新页面 30 个 i18n 键 8 语言全缺，i18n 审计门禁由 0-missing 转红**。`node scripts/i18n-audit.mjs --missing-only` 实测 56 条 missing 全部来自 `views/admin/ReconciliationReport.vue`（reports.* ×27 + common.startDate/endDate/exportFailed），8 个 locale 目录均无；`npm run i18n:check`（package.json:12）现在 exit 1（R64 刚留痕 STRICT PASS）。页面因 vue-i18n v9 支持 `t(key, defaultMsg)` 而全语言回退中文——功能不炸，但违反 8 语言同步纪律并破坏 CI 门禁。 | web/src/views/admin/ReconciliationReport.vue（全文 t() 调用）；web/src/locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}；web/package.json:12 | 8 语言补齐上述键，i18n:check 恢复绿 |
| 4 | **P3** | **no-DB/lite 模式下三端点与 worker 全走 panic 而非约定降级**。lite 模式 adminDB 保持 nil，summary/export 会先在 `h.providerNames` 的 `h.db.Query`（nil pool）panic；net/http recover 不崩进程，但违反仓库「DB 相关 handler 请求时 503/500」约定与 AuditTrimmer 的显式 nil-pool 守卫先例（bg/audit_trimmer.go:88-89）。 | cmd/gateway/main.go:490-497、:2924-2929；admin/report_rollup.go:118-133、:150、:222；对照 bg/audit_trimmer.go:88-89 | handler 入口补 nil-pool 短路（返 503）【主代理复核注：worker 面 T5 证实被 main.go:3767 `dbConn != nil` 大块跳过，lite 下不跑，无需修】 |
| 5 | **P3** | **export 端点缺 degraded 映射**：report_snapshots 缺表时 summary 返回 200 degraded JSON（:152-155），export 却直接 500（:178-183），与文件头声明的降级语义不一致。 | admin/report_rollup.go:152-155 vs :178-183 | export 复用同一错误串判断返回 degraded JSON |
| 6 | **P3** | **多币种混算**：cost 直接 `SUM(cost_amount)` 跨币种相加、币种取 `MAX(cost_currency)`（rollup.go:224-225 等 5 处），daily_total 又把各 provider 不同币种行求和。单币种部署无症状；多币种时 estimated_cost_cents 数值失义。 | domains/reportrollup/rollup.go:224-225、:280-281、:336-337、:404-405、:467-468 | 按币种分组或报表标注假设单币种（设计文档补勘误）【登记】 |
| 7 | **P3** | **舍入漂移与 daily_total 双写**：(a) daily_by_provider 的成本 = Σ(每模型各自 ROUND 后的分)，daily_total 是全天一次 ROUND，两者可差几个分位；(b) RollupDay 把 foldBuckets 产出的 daily_total（provider 面折叠）与 queryTotalDay 的 daily_total（全流量）先后写同一冲突键，前者永不存活，RowsWritten 恒多计 1。 | domains/reportrollup/rollup.go:536、:131-142、:158-164、:546-548 | 删冗余折叠写；RowsWritten 扣减【已修：删死写】 |
| 8 | **P3** | **daily_by_model 的 '' 与 NULL 模型名撞键**：SQL GROUP BY 保留 NULL 组，写库前 `COALESCE(raw_model_name,'')`，同 provider 同日 '' 与 NULL 两组写同一四键后者覆盖前者。 | domains/reportrollup/rollup.go:216、:235；sql/migrations/startup/537_usage_facts.sql:19 | SQL 侧统一 `COALESCE(raw_model_name,'')` 进 GROUP BY【已修：CTE 内 COALESCE】 |
| 9 | **P3** | **worker 与 POST /run 并发同日聚合可 40P01**：rollup 各查询无 ORDER BY，两会话 bucket 写入次序可能不同，同键行锁交叉可死锁，一方 abort 报错（次日/手动可重跑，ON CONFLICT 保证最终一致，无数据损坏）。另 /run 无频率限制（superAdmin 门控下风险低）。 | domains/reportrollup/rollup.go:169-174；admin/report_rollup.go:197-232 | 可接受；如需加固用 pg_advisory_xact_lock(date)【登记】 |
| 10 | **P3** | **usage_facts 日扫描无 occurred_at 前导索引且不分区**：五条索引均非 occurred_at 前导，internal 面按 `traffic_class='business'` 过滤更无可用索引 → 每日 rollup 随表增长线性退化为全表扫。属预存在形状，本轮新消费方每天再多扫一遍。 | sql/migrations/startup/537_usage_facts.sql:53-60；bg/partition_manager.go:871-932 | 评估 `(occurred_at)` 或 `(traffic_class, occurred_at)` 索引 / usage_facts 月分区接入 ensureSpecs【登记】 |
| 11 | **P3** | **report_snapshots 无分区/无 archiveSpec（量级判断）**：行增速 ≈ (1 + P + P·M + T + T·persons + T·M)/日。取 P=10、M=50、T=20、persons=1000 日活 → ~2.2 万行/日、~800 万行/年，person 维度占大头。单表现量级读面尚可；persons 过万时应挂 archiveSpec 或收缩 person 快照粒度。 | bg/partition_manager.go:871-932；sql/objects/tables/report_snapshots.sql:60-61；设计文档 §2.2 | 登记观察项【登记】 |

## 二、核实为健康的面

1. 六 scope 键与四键 UNIQUE 匹配性（唯一破口 internal_person，已修）。
2. internal 面业务流量过滤字段名对 usage_facts schema 验证通过（traffic_class/status/error_kind/end_user_id/person_hash/credits_charged/latency_ms 均真实存在）。
3. credits 冻结价复算三态有单测；E2E 断言 350 credits × 0.2 = 70 分。
4. 注入面干净：动态 WHERE 只拼位置参数；error_kind 透视走 jsonb_object_agg 不拼列名；xlsx 动态错误列 xml.EscapeText + 属性转义补 `&quot;`。
5. xlsx/OOXML：zip 五部件 + Override PartName 齐全；转义回读断言通过；sheet 名固定常量；无公式路径。
6. ROUND 口径半舍入全天一次；percentile_cont WITHIN GROUP + FILTER 语法次序正确，分位不跨桶折叠。
7. 时区：worker/RollupDay/SQL 边界全钉死 UTC，容器 TZ 渗入无。
8. 幂等与防护：ON CONFLICT 全列刷新重跑不翻倍；jsonb `::text::jsonb` 绑定规避 22P02；panic recover + 30min 超时包住整轮；hour 0-23 双重钳制 + HotReload。
9. 装配门控：worker 与 FeedbackAnalyzer 同处 data-plane 跳过块。
10. 迁移 745/746：主仓与 installer 副本逐字节一致；embed map/runner/parity 三点齐注；746 down 有意不做逆转并给理由；fast default 安全。
11. ensure 双路径收敛：新建路径与 SSOT 逐列一致；存量路径 columnsAllPresent 短路；真库双测试钉住。
12. admin 端点：三端点同挂 superAdmin；区间钳 366 天；view/维度交叉校验。
13. 前端契约：ts 字段名与 Go json tag 一一对应；路由 requiresSuper 与后端对齐；导出走 blob + 认证头。

## 三、未覆盖项与原因

- 真库 E2E 与 ensure 真库测试未执行（审计纪律禁连库，TEST_DATABASE_URL 门控）。
- 共享 252 账本 746 未占用、openpyxl 交叉校验：采信设计文档 §10 自述。
- installer 模块全量 go test、web vite build 未跑。
- pgx DATE 编码负 UTC 偏移日界行为未逐行核 pgtype 源码（东八区无实际风险）。
