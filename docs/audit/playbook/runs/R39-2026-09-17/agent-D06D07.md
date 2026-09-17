# D06/D07 子代理报告（窗口：67f78247c..294e0f65d，域：双重存储模式 + hot/columnar 分区，指定面：245 部署阻断修复 70672a052 / installer dbinit 同期变更 / 4a098366f 收口与 716down / 716-719 迁移通道）

> 原文存档（主代理已逐条亲读复核，处置见轮文档）。子代理：Explore，2026-09-17。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | 跳过决策是 ensure 局部的，"未 provisioned"信号没有传导给装配层：在任何 DB 启用但 provider_templates 不存在的部署（245/共享 252 库、以及 **installer 全新安装**——installer embed 只有 00/01/02 + startup/478 起，从不建该表），引擎仍无条件装配、ScanScheduler 仍无条件启动，每 sweep 对不存在的表 SELECT 42P01：启动 3 连败（0/30/60s 重试梯）+ 之后每 6h ticker 一次告警。触发路径：full 模式启动 → dbConn.Enabled → SetFreeDiscovery → FreeDiscoveryEngine()!=nil → Start → cycle。 | cmd/gateway/main.go:2883-2903；admin/free_discovery.go:37-47；bg/scan_scheduler.go:298-303、172-194 | ensure 跳过时记录进程级 "freediscovery 未 provisioned" 状态，main.go 装配点消费之（不启 scheduler）；或运维文档明确未建表部署必须 `LLM_GATEWAY_FD_SCAN_SCHEDULER=off` |
| 2 | P2 | 违反 D06 §3.5"能力边界诚实"：表缺失时 admin 自由发现路由仍注册，GET /api/free-discovery/templates 返回 500 + 原始 42P01 报文（而非 503"not provisioned"）。触发路径：表缺失部署上管理员打开自由发现页 → List 查询 42P01 → 500。 | admin/free_discovery.go:121-125（fdDeps 只挡 nil 依赖，挡不住"已接线但表不存在"） | 同 #1 的 provisioned 门；或 fdStatusFor 识别 42P01 归一为 503+明确文案 |
| 3 | P2 | 系统性复发面未收口：openDBWithBootRetry 把 db.Open 的**一切**错误都按"postgres unreachable"重试/烧预算（70672a052 只拔掉了唯一已知的那颗 42P01 雷）。ensure 链约 40 个函数（db/db.go:129-330）中任何一个再引用某部署形态不存在的特性表，245 式阻断（烧光 boot budget → "postgres disabled" → healthz 窗口超时）原样复发。触发路径：未来任一 ensure 首个语句命中缺失表 → 42P01 → 被当不可达 → 预算耗尽。 | cmd/gateway/main_helpers.go:132-145（无 SQLSTATE 判别）；预算默认 20s（:119） | openDBWithBootRetry/ApplyMigrations 对 42P01/42703/42883 归类"schema mismatch"，fast-fail 并给明确日志，不烧连接重试预算 |
| 4 | P3 | 违反 conventions §5"每修一条配钉桩回归测试"：ensureFreediscoveryTemplateHealth 的 to_regclass 跳过分支无任何测试（全仓 `*_test.go` 对该函数零引用）。触发路径：后续重构 applyMigrationsOnce 时静默丢失存在性检查即复发。 | db/db.go:1591-1599（被测面） | 补集成钉桩（-tags integration）：无表库上 boot 迁移链全绿 + 有表库上三列在位 |
| 5 | P3 | to_regclass 对**任意关系类别**（视图/序列）都返回非空：若未来按 716"视图统一"的活跃模式造出同名视图，存在性检查放行后 `ALTER TABLE` 以 42809 再炸启动。触发路径：表被同名视图替代的假想重构 → 检查误判"已 provisioned" → ALTER 42809 → boot 失败。 | db/db.go:1592-1595 | 改查 pg_class.relkind='r'（一行加固，低优先） |

## 二、核实为健康的面

- **跳过条件方向判定正确（fail-closed 于不确定态）**：存在性检查本身失败（超时/断连）→ 包错上抛 → ApplyMigrations 两次尝试（db/db.go:98-117）→ 启动失败，不误跳过；表在但 ALTER/建索引失败 → 报错而非跳过；只有 to_regclass 返回 NULL 才静默跳过并打 Info（db/db.go:1596-1599）。schema 限定 `public.` 前缀，无 search_path 误判；权限不影响 pg_class 解析，无权限性假阴性。
- **跳过后启动深处不崩**：provider_templates 的全部 Go 消费点是 freediscovery 域（template_manager.go:102/145/173/323/349）、bg/scan_scheduler.go（:298 枚举、:446-475 失败计数）与该 ensure 本身；均不在 Open/ping/openDBWithBootRetry 启动关键路径，worker 有 top-level + per-cycle 双 panic guard（scan_scheduler.go safeCycle），失败形态为周期性告警而非崩溃。
- **087 账本诚实**：`INSERT INTO schema_migrations ('087')` 只在表在位时执行（db/db.go:1613-1615），未 provisioned 库不会留下假登记行。
- **lite/sqlite 模式零牵连**：lite 置空 bootDatabaseURL（main.go:470-474）→ dbConn=nil → SetFreeDiscovery no-op（free_discovery.go:38-40）→ scheduler 不启动；ApplyMigrations 不执行；sqlite schema（storage/sqlite/schema.go）无 provider_templates，catalog 四表是未接线的实验占位（storage_mode_init.go:254-257）。两模式能力边界一致。
- **716/718/719 五点同步完整**：正典 sql/migrations/startup 与 installer embeddata 逐字节一致（716 down 两份均 284 行；718/719 down `diff` 全等，本机实测）；runner.go 登记至 719（installer/internal/dbinit/runner.go:158-162）；embed var + embeddedSQLFiles 同步（installer/cmd/llm-gw-installer/main.go:433-439、570-581）；revision-sequence 登记 716/717/718/719（scripts/apply-db-revision-sequence.sh:415-437）；三个漂移方向守卫在位（installer/cmd/llm-gw-installer/stats_migrations_test.go:282-361）；冻结通道 db/migrations/README.md 立规且窗口内未向其新增文件。
- **719 ensure 复活根治模式正确**：9 个约束影蔽索引的 ensure 侧 CREATE 删除与 drop 迁移**同 commit** 落地（db/db.go:3912/4528/4710/4815/4928/4943、db/db_omnifree.go:369-389 对应 sql/migrations/startup/719_*.sql A 组），drop 后无创建者，无 718 式复活循环；request_logs 父索引所有权归一（parent_ts 通道胜出）与 tool_usage_stats_hot ASC/DESC 收敛均只动索引不动"写只在 hot"不变量。
- **idx_provider_templates_health 无跨通道冲突**：718/719 drop 清单不含它（两文件 grep provider_templates 零命中），ensure 与迁移通道对该索引无所有权对抗。
- **4a098366f 探测权威源收口方向正确（D06 邻接）**：brokenProbeReviver（冻结的 legacy model_probe_state 写者）默认被 useNewProbeMode()=true 跳过（cmd/gateway/main.go:3728 区、main_helpers.go:264-270 默认 true），新 NodeProbeWorker 独占 requeue/backoff——双存储权威切换为 fail-closed；716 探测视图双通道守卫在位（db/probe_views_unified_test.go：全文归一比对 + down 覆盖对象钉桩，b756ea861）。

## 三、未覆盖项与原因

- **252/245 真库验证跳过行为与"75s 烧预算"量级**：需要真实凭据与共享库访问；commit message 中的 42703/75s/60s healthz 数字仅转录自 70672a052 提交说明，未独立复核（conventions §3：主代理如采信需亲验）。
- **717/719 "真库实跑 + 复活扫描零复活"声明**：同上，只读子代理无法连真库，采信为线索；建议主代理核对 R38 轮文档所附实跑证据。
- **部署文档面**：deploy/ 排障文档是否已写明"未跑 084 bootstrap 的部署需关 FD scan scheduler"未全文排查（时间预算；且属 docs 域）。
- **-tags integration 集成测试实跑**（含 fresh_installer_integration_test 的表存在性断言）：需真库，未执行。
