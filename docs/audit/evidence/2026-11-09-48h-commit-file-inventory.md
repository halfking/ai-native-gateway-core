# 最近 48 小时逐提交清单

固定窗口：2026-11-07T04:56:13.697343+00:00 → 2026-11-09T04:56:13.697343+00:00；基线 `1fa2ea5925174051bba6925fbffedb4315fc5bf8`。

本表是变更穷举，不等同于每行所有代码均已证明正确；专项审查、批判结论与覆盖边界见本轮报告。

| commit | 时间 | 条目 | 文件数 |
|---|---|---|---|
| `1fa2ea592517` | 2026-11-09T12:12:14+08:00 | docs: claude-opus-5-5 根因审计档 + AGENTS.md 条目登记 | 2 |
| `1179dbee4315` | 2026-11-09T11:58:01+08:00 | fix(installer): 补齐 954/955 移除的 main.go/runner.go 注册（上笔提交遗漏） | 2 |
| `e3ceee172c28` | 2026-11-09T11:55:53+08:00 | fix(executors): 上游非 2xx 错误消息携带响应体预览（诊断面缺口根修） | 16 |
| `426bb9a94855` | 2026-11-09T11:46:46+08:00 | test(audit): ★ 第五十轮续记之四：★ 白名单豁免面【★ 从 35 个值泛化成结构化判据 ★】 | 3 |
| `94fd255c0e8d` | 2026-11-09T11:20:35+08:00 | docs(audit): ★ 第十六轮订正：★ §8.24 两处就地订正（★ 那台门的归属 ＋ 双远端分叉的笔数 ★） | 1 |
| `ec7654f1908b` | 2026-11-09T11:19:46+08:00 | docs(audit)+test(scan-secrets): ★ 第十六轮：★ 代落地「★ 白名单豁免面 ★」机器门 ＋ §8.24 独立复核记录 ★ | 1 |
| `fa81c1e37719` | 2026-11-09T11:14:23+08:00 | fix: claude-opus-5-5 添加多provider绑定实现冗余路由 | 16 |
| `2aad04f3787f` | 2026-11-09T11:11:22+08:00 | docs(audit): ★ 第五十轮续记之三 · 收尾：★ 「白名单豁免面」落成一道独立门（F55） | 3 |
| `85d8b2f1d51f` | 2026-11-09T10:54:53+08:00 | fix: claude-opus-5-5 无可用节点问题修复 | 1 |
| `7f0460e543d2` | 2026-11-09T10:46:46+08:00 | docs: 添加 claude-opus-5-5 访问错误修复报告 | 1 |
| `3150a0f062ab` | 2026-11-09T10:43:29+08:00 | chore(manifest): 更新 startup manifest 包含 migration 953 | 1 |
| `35f0104793a9` | 2026-11-09T10:41:22+08:00 | fix(installer): 注册 954 迁移到 StartupFiles | 1 |
| `0344565ceebd` | 2026-11-09T10:37:42+08:00 | fix(migration-953): 注册到 runner.go + 添加 lock_timeout 守卫 | 3 |
| `52759157ae44` | 2026-11-09T10:36:56+08:00 | fix(model): 修复 claude-opus-5-5 访问错误 | 5 |
| `ab871558cdf3` | 2026-11-09T10:27:19+08:00 | feat(probe): P0-3 恢复检测及时性优化 —— 添加 models_canonical.featured 列 | 6 |
| `b60305e6fa98` | 2026-11-09T10:21:43+08:00 | docs(audit): ★ 第十五轮：★ §8.23 —— ★ 白名单豁免面的机器证据 ＋ 探针口径完整复现 ＋ 只读盘点工具 ★ | 2 |
| `3708ea58a224` | 2026-11-09T10:16:11+08:00 | docs(agents): 探针限流窗口对齐+上游桶加权均衡轮条目（复查轮索引） | 1 |
| `f0ead2e90753` | 2026-11-09T10:14:42+08:00 | docs(audit): 复查轮 §6 回填 211 部署读数（2971 槽b；窗口冷却 60s 落库实证；聚合效应留观察窗的诚实边界） | 1 |
| `c1d72db90dc5` | 2026-11-09T10:03:02+08:00 | docs(audit): ★ 第十四轮：★ §8.22 —— ★ 跨会话交叉复核 ＋ 三个隔离数独立复现 ＋ 三条自纠 ★ | 1 |
| `fd2611a6956d` | 2026-11-09T09:53:12+08:00 | docs(audit): 第 50 轮续记之三（补）—— ②-0 口径切到「脱敏后」 | 1 |
| `573063510744` | 2026-11-09T09:46:24+08:00 | docs(audit): 悬挂引用订正 —— AGENTS 同步轮条目引 §635 应为 §646（合并让号后未回订）+ §646 通式三补 F-11 作废注记 | 2 |
| `738a4d63c70f` | 2026-11-09T09:44:18+08:00 | feat(probe+routing): 探针对齐限流窗口 + 上游桶首跳加权均衡（压力惩罚 + 容量份额） | 9 |
| `f58cbe36bcbb` | 2026-11-09T09:42:00+08:00 | docs(audit): 第 50 轮续记之三 —— ②-0 逐条甄别落档 ＋ 审计档自身脱敏 | 1 |
| `31807a022428` | 2026-11-09T09:34:58+08:00 | docs(audit): 第 50 轮续记之二 —— 机制 A′/C′/E′ 的独立复核与收敛落档 | 1 |
| `bde2baa598e4` | 2026-11-09T09:34:48+08:00 | feat(mcp): 控制面能力位 —— 业务 key 承载注册/管理/统计/拓扑 + 权限放大收口 | 12 |
| `9801786a6266` | 2026-11-09T07:37:06+08:00 | test(sales): 落地报价守卫（另一会话在途文件）+ 锚点跟到上游当前形态；F-13 登记 | 2 |
| `574957cad44e` | 2026-11-09T07:33:47+08:00 | fix(audit): 台账族重采收口（3 红→绿）+ 部署锁/凭据口径修正 + 12h 审计档（2026-11-09 07:30） | 23 |
| `f367dce5ff90` | 2026-11-09T04:52:59+08:00 | fix(omnifree): .gitignore 显式豁免 internal/omnifree/data/ —— go:embed 种子 SSOT 必须随仓库分发（同 bg/data/ 先例） | 4 |
| `53d4e2abd8ea` | 2026-11-09T04:29:18+08:00 | feat(ops): 运营审计轮 —— 探测恢复感知根修 + 统计对账 F1 + 迁移952 定价版本化 + 营销管理面 + 免费模型引入三段断链 + 行情对比 | 79 |
| `6c4d42780973` | 2026-11-09T04:04:35+08:00 | docs(audit): 12h 审计档补 F-6（扫描面受本机未跟踪目录影响 ⇒ 读数不可比） | 1 |
| `bfc3163174ef` | 2026-11-09T04:01:04+08:00 | docs(audit): 12 小时内修订审计（2026-11-09 03:30）—— 40 提交 / 124 文件的窗口复核 + 待修正清单 | 1 |
| `ef5cc58feb23` | 2026-11-09T03:39:13+08:00 | test(audit): q-usage 两条行为门补「脚本内容进缓存键」⇒ 脚本一改不再读旧缓存（12h 审计 F-2） | 1 |
| `c4ae90b7b73d` | 2026-11-09T03:35:11+08:00 | docs(audit): ★ 第十三轮：★ §8.21 —— ★ 归因重做 ＋ 机制 A/C/E 落地 ＋ 挖出一条【★ BLOCK 级假阴性 ★】★★ | 1 |
| `fae7246b72eb` | 2026-11-09T03:26:10+08:00 | docs(audit): 仓库体积 2.9G 核查与清理选项清单（2026-11-07 只读核查轮） | 1 |
| `cc03409b1f5f` | 2026-11-09T03:26:00+08:00 | fix(audit): q-usage.sh 去掉 GNU 专有的 `zcat -f` + 读取失败改为 fail-closed（12h 审计 F-1） | 2 |
| `ac262e36ca2f` | 2026-11-09T03:08:56+08:00 | test(mcp): S24b 断言旧名计数器**确实自增**（族名可见≠有数据） | 4 |
| `6bc93a71d1a7` | 2026-11-09T03:07:22+08:00 | fix(mcp): 错误码**归位**（既有 -32001/-32004 我方误当新增码）+ 补齐既有 26 个指标族（旧名建族） | 17 |
| `139a552a48a9` | 2026-11-09T02:35:04+08:00 | fix(scan-secrets): 消除平台/locale 分叉与大小写假阴性（第十三轮） | 5 |
| `4cf786109235` | 2026-11-09T02:33:13+08:00 | fix(ursmcheck): MCP 网关轮 + R189 轮带入的台账漂移收口 —— 8 门红→2（余 2 红为 252/211 生产采集面） | 10 |
| `97eadd6b0040` | 2026-11-09T02:24:27+08:00 | test(mcp): 报告产物 summary.md 自带逐条用例读数 ⇒ 复核者只读文件即可取到证据 | 2 |
| `7e283e03c0c8` | 2026-11-09T02:22:11+08:00 | test(mcp): 把被点名的 4 项契约做成**运行时可观测**的 L2 场景（S10b/S16b/S21/S22/S22b） | 6 |
| `23995ef11b3b` | 2026-11-09T02:17:05+08:00 | fix(mcp): 429 错误体补 limit 字段 + 补齐既有「按能力域过滤工具清单」能力（导出≠生效的边界登记） | 6 |
| `a705e0616aa2` | 2026-11-09T02:10:44+08:00 | docs(mcp): 补登既有「执法在路径」资产全清单（台账/基线/4 个测试）⇒ 8 项逐个判定去留 | 3 |
| `7225b3e69702` | 2026-11-09T02:05:56+08:00 | fix(mcp): 与既有 tools/mcp-gateway 的契约逐字对齐（错误码/能力头/请求级能力路由/429），并移植「执法在路径」运行时探针 | 16 |
| `74a47f613d18` | 2026-11-09T02:01:28+08:00 | docs(audit): 第 50 轮续 —— 机制 B 已闭合（另一会话落地、我独立复核）＋ 修法后 8 格重测 | 1 |
| `6b363559abdb` | 2026-11-09T01:47:01+08:00 | docs(audit51): 03 记录补提交与基线推进事实（并行会话零冲突合并 + 合并后复跑读数） | 1 |
| `173f9008bb3f` | 2026-11-09T01:44:07+08:00 | docs(agents): 部署轮索引节号订正 §644→§645（LESSONS 撞号让号后的引用同步） | 1 |
| `329b02ea68f9` | 2026-11-09T01:44:01+08:00 | fix(bg/probe): 第51增量 48h审计 —— balance-floor 恢复通知链补齐（F-1 P1）+ 判据补洞（F-2~F-4） | 11 |
| `44d540dca6a8` | 2026-11-09T01:41:44+08:00 | docs(agents): 211 蓝绿部署轮（2968-6e62d2e0 上线）+ LESSONS §644 —— 部署闸门工具链钉扎/跨机部署件落地纪律/排水突发误读/W1 有机 429 铁证 | 2 |
| `8dafabccb171` | 2026-11-09T01:37:41+08:00 | docs(audit): ★ 订正机制 B 的行号：★ `scan-secrets.sh:93`（★ 原写 :94 ★）⇒ ★ 就地订正、★ 并把行号的【★ 钉法 ★】写进正文 | 1 |
| `26f5a5b2c060` | 2026-11-09T01:35:58+08:00 | docs(mcp): 补记合并 origin/main 后的复跑读数（24/24 仍全绿） | 1 |
| `d3b1286e76bb` | 2026-11-09T01:32:07+08:00 | fix(scan-secrets): ★★ 机制 B：★ 白名单那条未转义花括号 ⇒ ★ glibc 判整条 alternation 编译失败 ⇒ ★ **CI 侧白名单整体失效** ★★ ＋ 跨平台机器门 | 5 |
| `be33b0202b2e` | 2026-11-09T01:30:51+08:00 | feat(mcp): 内置企业级 MCP 服务网关（数据面+控制面+治理链）并合并既有 tools/mcp-gateway 能力 | 45 |
| `b8aa8fe4c0aa` | 2026-11-09T01:30:21+08:00 | fix(ursmcheck): W1 sales realdb 腿的 DSN 台账登记收口 —— 两门 TestRealDBLegsUseDeclaredDSNEnvNames/TestDatasourceEnvTargetGuardNoUnregisteredMismatch 转绿 | 2 |
| `9f9a96312a43` | 2026-11-09T01:28:54+08:00 | chore(audit-ledger): db-object-expect 从 SSOT 重生成 885→886（+951 armor_judgments_created_at 索引，R189 轮 cf1e28be9 落地的漂移） | 1 |
| `31d1c21b7b6a` | 2026-11-09T01:14:21+08:00 | docs(audit): 第 50 轮 —— 给平台分叉（归属洞）补 F54 ＋ 8 格受控矩阵 ＋ ②-0 最终可执行版 | 1 |
| `3d39ef2cb407` | 2026-11-09T01:05:23+08:00 | docs(audit55): 第51增量审计方案订正——提交数/代码类口径与失效引用 | 1 |
| `f9abc58e594f` | 2026-11-09T00:55:57+08:00 | docs: LESSONS §643 撞号改 §644（并行会话探测空转轮先推占 §643；取号纪律：合并时以远端实测为准） | 2 |
| `b79bf2ff533e` | 2026-11-09T00:55:28+08:00 | fix(sales,web): 报价系统 MVP 风暴档审计与终版实施轮 —— P1 建单链 23514 根修（服务端派生）+ 4P2/8P3 收口 + W2-T2 看板/成本漂移 + 客户版页面 | 46 |
| `6e62d2e06697` | 2026-11-08T08:03:32+08:00 | docs(agents): 211 探测空转根修 + W1 节奏校准轮条目 + LESSONS §643（豁免两端成对/审计阈值大于重置周期/success 需配副作用对账） | 2 |
| `0b4ce4715576` | 2026-11-08T08:01:56+08:00 | feat(credential): token 桶 W1 —— 429 冷却税则改「节奏校准」（RA∨窗口优先，升级只罚节奏违规） | 7 |
| `8ad9bd8d865d` | 2026-11-08T08:01:56+08:00 | fix(bg/probe): T-P1 探测空转根修 —— 消费侧模型范围门与提交侧 F2 豁免成对 | 2 |
| `ab01e17bd238` | 2026-11-07T21:59:55+08:00 | docs(audit): Token 桶双层路由优化方案轮（规划·零代码）—— 四策略仿真否决绝对AIMD爬坡、真杠杆=节奏校准冷却、211饥饿实证与W1-W4分轮规划 | 3 |
| `b58254654337` | 2026-11-07T21:48:34+08:00 | chore(version): 版本戳随轮收敛（154 侧效应 2955-2f5677e3 清树，先例 3b2b4c996/0ba6755b6） | 4 |
| `2f5677e3faac` | 2026-11-07T21:35:23+08:00 | fix(migrations): 恢复 950 已应用字节 —— R189 包裹改写触发部署 checksum fail-closed（deploy-245 2953 中止实录） | 4 |

## 逐文件来源

| 文件 | 提交 |
|---|---|
| `.env.example` | 6bc93a71d, be33b0202 |
| `.gitignore` | f367dce5f, be33b0202 |
| `AGENTS.md` | 1fa2ea592, 3708ea58a, 573063510, 574957cad, 53d4e2abd, ac262e36c, 6bc93a71d, 7e283e03c, 23995ef11, a705e0616, 7225b3e69, 173f9008b, 44d540dca, be33b0202, f9abc58e5, b79bf2ff5, 6e62d2e06, ab01e17bd |
| `HANDOFF-migration-953-observation.md` | fa81c1e37 |
| `HANDOFF-recovery-detection.md` | fa81c1e37 |
| `LESSONS.md` | fa81c1e37, 573063510, 574957cad, 44d540dca, f9abc58e5, b79bf2ff5, 6e62d2e06, ab01e17bd |
| `Makefile` | 426bb9a94, 2aad04f37, 574957cad, 139a552a4, d3b1286e7 |
| `README.md` | be33b0202 |
| `VERSION` | b58254654 |
| `admin/maas_handlers.go` | 53d4e2abd |
| `admin/sales_handlers.go` | b79bf2ff5 |
| `admin/sales_public_handler.go` | b79bf2ff5 |
| `admin/sales_public_limiter_test.go` | b79bf2ff5 |
| `bg/balance_floor_guard.go` | 53d4e2abd, 329b02ea6 |
| `bg/balance_floor_guard_plan_capacity_test.go` | 53d4e2abd |
| `bg/balance_floor_guard_recovery_notify_test.go` | 329b02ea6 |
| `bg/credential_probe_v2.go` | 53d4e2abd |
| `bg/credential_probe_v2_model_fallback_test.go` | 53d4e2abd |
| `bg/credential_recovery_test.go` | 329b02ea6 |
| `bg/model_tier.go` | ab871558c |
| `bg/node_probe.go` | 738a4d63c, 329b02ea6 |
| `bg/probe_rate_window_20261109_test.go` | 738a4d63c |
| `bg/probe_recovery_policy.go` | 738a4d63c |
| `bg/probe_recovery_policy_test.go` | 738a4d63c |
| `bg/probe_schedule_clamp_test.go` | 329b02ea6 |
| `bg/probe_service.go` | 8ad9bd8d8 |
| `bg/probe_service_model_scope_bypass_test.go` | 8ad9bd8d8 |
| `bg/probe_sustained_failure_20261107_test.go` | 329b02ea6 |
| `bg/scan_scheduler.go` | 53d4e2abd |
| `bg/stats_minute_rollup.go` | 53d4e2abd |
| `cmd/gateway/main.go` | 53d4e2abd, 329b02ea6, be33b0202 |
| `cmd/gateway/mcp_gateway.go` | be33b0202 |
| `cmd/gateway/mcp_gateway_test.go` | be33b0202 |
| `cmd/mcp-gateway/main.go` | 7e283e03c, be33b0202 |
| `db/db.go` | 53d4e2abd |
| `deploy/sql/schemas/baseline/01-schema.sql` | 53d4e2abd |
| `docs/12小时内修订审计-20261104-0329.md` | 94fd255c0, ec7654f19, b60305e6f, c1d72db90, c4ae90b7b, 8dafabccb, d3b1286e7 |
| `docs/12小时内修订审计-20261109-0330.md` | 6c4d42780, bfc316317 |
| `docs/12小时内修订审计-20261109-0730.md` | 9801786a6, 574957cad |
| `docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md` | 4cf786109 |
| `docs/audit/2026-10-27-R164-底账新鲜度与漂移判据.md` | 574957cad |
| `docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md` | 426bb9a94, 2aad04f37, fd2611a69, f58cbe36b, 31807a022, 74a47f613, 31d1c21b7 |
| `docs/audit/2026-11-04-报价系统MVP-头脑风暴与运营思路.md` | b79bf2ff5 |
| `docs/audit/2026-11-07-token桶双层路由优化方案.md` | ab01e17bd |
| `docs/audit/2026-11-07-仓库体积2.9G核查与清理选项清单.md` | fae7246b7 |
| `docs/audit/2026-11-08-报价系统MVP-风暴档审计与终版实施轮.md` | b79bf2ff5 |
| `docs/audit/2026-11-09-Migration-953-Current-Status.md` | fa81c1e37 |
| `docs/audit/2026-11-09-Migration-953-Deployment-Complete.md` | fa81c1e37 |
| `docs/audit/2026-11-09-claude-opus-5-5访问错误根因分析与诊断面修复.md` | 1fa2ea592 |
| `docs/audit/2026-11-09-上游恢复检测及时性问题分析与优化方案.md` | fa81c1e37 |
| `docs/audit/2026-11-09-上游桶令牌池与冷却管理方案审计.md` | fa81c1e37 |
| `docs/audit/2026-11-09-恢复检测及时性优化-P0实施与部署指南.md` | fa81c1e37 |
| `docs/audit/2026-11-09-恢复检测及时性优化-Phase0-P03完成总结.md` | fa81c1e37 |
| `docs/audit/2026-11-09-探针限流窗口对齐与上游桶加权均衡轮.md` | f0ead2e90, 738a4d63c |
| `docs/audit/2026-11-09-运营审计轮-请求统计与定价与营销与免费模型与探测感知.md` | 53d4e2abd |
| `docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv` | 4cf786109 |
| `docs/audit/market-price-compare-2026-11-09.csv` | 53d4e2abd |
| `docs/db-changelog.md` | 53d4e2abd, 2f5677e3f |
| `docs/fix/2026-11-09-claude-opus-5-5-access-fix.md` | 7f0460e54 |
| `docs/fix/2026-11-09-claude-opus-5-5-no-candidate-fix.md` | 85d8b2f1d |
| `docs/mcp-gateway/01-design.md` | 7225b3e69, be33b0202 |
| `docs/mcp-gateway/02-capability-matrix.md` | 6bc93a71d, 7e283e03c, 7225b3e69, be33b0202 |
| `docs/mcp-gateway/03-legacy-merge-map.md` | bde2baa59, ac262e36c, 6bc93a71d, 23995ef11, a705e0616, 7225b3e69, be33b0202 |
| `docs/mcp-gateway/04-operations.md` | bde2baa59, 6bc93a71d, be33b0202 |
| `docs/mcp-gateway/05-verification-report.md` | bde2baa59, ac262e36c, 6bc93a71d, 97eadd6b0, 7e283e03c, 23995ef11, a705e0616, 7225b3e69, 26f5a5b2c, be33b0202 |
| `docs/mcp-gateway/README.md` | 6bc93a71d, 7e283e03c, 7225b3e69, be33b0202 |
| `docs/subsystems/request-surface.md` | 4cf786109 |
| `docs/全面审计v3/00-审计覆盖台账.md` | 574957cad |
| `docs/全面审计v3/2026-11-05/02-审计发现与缺陷台账.md` | 574957cad |
| `docs/全面审计v3/2026-11-05/03-修复与验证记录.md` | 574957cad |
| `docs/全面审计v3/2026-11-08/01-审计方案.md` | 329b02ea6, 3d39ef2cb |
| `docs/全面审计v3/2026-11-08/02-审计发现与缺陷台账.md` | 329b02ea6 |
| `docs/全面审计v3/2026-11-08/03-修复与验证记录.md` | 6b363559a, 329b02ea6 |
| `docs/全面审计v3/README.md` | 574957cad |
| `domains/credential/ratebackoff.go` | 0b4ce4715 |
| `domains/credential/ratebackoff_pace_test.go` | 0b4ce4715 |
| `domains/credential/ratebackoff_test.go` | 0b4ce4715 |
| `domains/credential/writer.go` | 0b4ce4715 |
| `domains/freediscovery/provider_scanner.go` | 53d4e2abd |
| `domains/freediscovery/provider_scanner_allowlist_test.go` | 53d4e2abd |
| `domains/streaming/executors/candidate_failure_logger_contract_test.go` | e3ceee172 |
| `domains/streaming/executors/executor_anthropic.go` | e3ceee172 |
| `domains/streaming/executors/executor_anthropic_test.go` | e3ceee172 |
| `domains/streaming/executors/executor_chat.go` | e3ceee172 |
| `domains/streaming/executors/executor_gemini.go` | e3ceee172 |
| `domains/streaming/executors/executor_ollama.go` | e3ceee172 |
| `domains/streaming/executors/router_rate_pressure_lottery_test.go` | 738a4d63c |
| `domains/streaming/executors/router_recovery_test.go` | 738a4d63c |
| `domains/streaming/executors/router_scoring.go` | 738a4d63c |
| `domains/streaming/executors/router_two_layer_cost_test.go` | 738a4d63c |
| `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 53d4e2abd |
| `installer/cmd/llm-gw-installer/embeddata/startup/950_probe_queue_source_credential_recovery.sql` | 2f5677e3f |
| `installer/cmd/llm-gw-installer/embeddata/startup/952_model_price_versions.down.sql` | 53d4e2abd |
| `installer/cmd/llm-gw-installer/embeddata/startup/952_model_price_versions.sql` | 53d4e2abd |
| `installer/cmd/llm-gw-installer/embeddata/startup/953_models_canonical_featured.down.sql` | ab871558c |
| `installer/cmd/llm-gw-installer/embeddata/startup/953_models_canonical_featured.sql` | 0344565ce, ab871558c |
