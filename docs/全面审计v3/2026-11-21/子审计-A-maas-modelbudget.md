# 子审计 A — MaaS / modelbudget 列车（窗口 c4b5d00cb..c6bf29aea，HEAD=c6bf29aea）

> 审计日期：2026-11-21。只读取证，未改任何代码。所有「生产侧」推定均标「待主控活体复核」。
> 不采信提交信息原文；每条断言给 file:line 或命令实测。

## 0. 判定总表

| # | 审计问题 | 判定 | 级别 |
|---|---|---|---|
| 1 | 59 轮 C-1「api/maas 10 端点死面」收口 | **未收口（P2 复发且加重）**：两个并行 HTTP 实现均零接线；窗口还把 gin+sonic 拖进 gateway 依赖图 | P2 |
| 2 | P1#3 modelbudget 请求路径接线 | **部分实现**：只在 serveWithExecutor 一个管面（2 条路由）；messages/responses/gemini 三面无闸（与 59 轮口径一致，未扩面）；闸对真实模型集惰性（硬编码价目） | P2/P3 |
| 3 | P1#4 契约测试 | **部分实现**：13/13 实测绿，但只钉解析+估算，无 degrader/路由集成；Phase 2 e2e 是 `t.Skip` 桩 | P2 |
| 4a | maas_calculate_price 单位归一 | **已实现（双载体同步）**，但该 schema 全家无生产投递通道 ⇒ 只在测试容器可达 | P3（接线前无生产暴露） |
| 4b | UsageCorrect 跨租户校验 | **已实现，SQL 正确**（经 holds 关联租户）；第二处无过滤查询是**有意**的归属披露面，非同族缺陷 | ✅ |
| 4c | 993 迁移登记八面 | **部分实现**：自登记段 2/3 载体同步；installer go:embed/var/map 三面仍缺 ⇒ `go test ./installer/cmd/llm-gw-installer/` 在 HEAD **持续红**（2 门） | P2 |
| 5 | MONEY-TYPE 审计残留 | **大体已修/按计划保留**；cents_per_credit 单位、order_amount 维、毛利下限三特查项均已修（A-14） | ✅ |
| 6 | maas/ 域集成测试 | **本机不可跑（Docker 守护进程无响应）**；根包 maas 81 过/10 skip/0 败；modelbudget 12 绿；maascontracts 绿 | 待主控复核 |
| 7 | 窗口文件清单 + 高风险点 | maas/ 根包**零改动**；高风险 6 项（见 §7） | P2/P3 |

---

## 1. Q1：api/maas 10 端点死面收口状态 —— 未收口（复发且加重）

**59 轮 C-1 原文**（docs/全面审计v3/2026-11-20/子审计-C-maas列车.md:27）：「`api/maas` 10 端点零消费者（死面）」，处方二选一：①wire 进 main mux＋路由注册钉测；②README 头标注「未接线」。**两条处方 HEAD 上都未执行**。

实测证据（HEAD）：
- `api/maas`（echo v5 传输层）唯一构造/挂载入口 = `api/maas/server.go:27`（NewServer）与 `:40`（RegisterRoutes）；全仓消费者**只有** `api/maas/server_test.go`（grep `maasapi\.` 零命中生产代码）。
- gateway 进程未挂载：`cmd/gateway/main.go:45` 虽 import 根 `api` 包，但只用 `api.NewApprovalHandler`（main.go:8871）与 `api.RegisterDingTalkRoutes`（main.go:8913）；`api.NewServer`（MaaS 十端点，api/server.go:27）在 gateway 内**零调用**。
- admin mux 挂的是**另一族**（/api/admin/maas/* 管理面，admin/maas_handlers.go:14-60，superAdmin 门）与 /api/maas/* 用户自助面（admin/maas_handlers.go:51-60），与契约 v0.1 的 inventory/commercial/trading/ledger 十端点**无关**。
- 窗口新增第三份并行实现：根 `api/server.go`（gin，+356 行，f3543a403）＋ `cmd/maas-api/main.go`（独立进程，:39 调 `api.NewServer(pool)`）。`grep -rn "maas-api" scripts/` 零命中 ⇒ 无任何构建/部署脚本引用该二进制（待主控活体复核：是否有人手工跑过）。
- api/maas/README.md 头部（1-25 行）**没有**「未接线」标注；根 api 层 `api/server.go` 无任何 `_test.go`（api/ 只有 approval_handler_test.go、dingtalk_callback_test.go）。

**加重项**：`go list -deps ./cmd/gateway | grep -E "gin-gonic|labstack"` 实测 **gin 与 echo v5 同时在 gateway 依赖图里**（cmd/gateway import 根 api 包 ⇒ 连带拖入 api/server.go 的 gin；bytedance/sonic +21 vendor 行）。为两个零接线端点集付出双框架＋sonic 的二进制/供应链成本。

**鉴权设计核对**（若未来接线，按现状是裸奔）：根 api/server.go:99-107 `extractAuth` 直接信任 `X-Tenant-ID`/`X-Subject` 头，**无令牌校验**；api/maas/server.go:41-42 RegisterRoutes 只挂 metrics+tracing 中间件，**无鉴权中间件**（契约自称 trusted-headers，意味着必须前置认证代理——两份实现都没有这道说明之外的防线）。**若有人照 docs/API.md（Base URL localhost:8080/api/v1）直接部署 cmd/maas-api，即是一台无鉴权计费 API（待主控活体复核部署面）。**

---

## 2. Q2：modelbudget 预算闸覆盖面 —— 部分实现（1 管面 / 2 路由）

接线点（106d4a1f4）：
- 唯一闸体：`domains/streaming/handler.go:3113-3171`（serveWithExecutor 内，Phase 2 P1#3 块）。调用链：`ChatHandler.ServeHTTP`（:2359）→ `serveHTTPInner`（:2377）→ `serveWithExecutor`（:2670 调用，:2697 定义）。
- 路由面：`cmd/gateway/main.go:7679-7680`（/v1/chat/completions、/v1/completions → chatRouteHandler；V2 分派变体 :7923-7925 包装同 handler）⇒ **2 条路由、1 个管面**。
- **无闸面**：MessagesHandler.ServeHTTP（domains/streaming/messages.go:73）、ResponsesHandler.ServeHTTP（responses.go:109）、GeminiHandler.ServeHTTP（handler_gemini.go:453）——grep `MaxCostPerTask|modelbudget|CheckBudget|PreCheckCredits` 在这三个文件**零命中**（messages/responses 里唯一的 "budget" 是 2026-08-24 的 promptBudgetLimit OOM 守卫，messages.go:323-329 / responses.go:342-348，与 modelbudget 无关）。**59 轮「预算闸门只覆盖 2 个面」的现状没有变化**：新闸装进了同一个 serveWithExecutor，没有扩面。

行为级风险（P3）：
- 闸的定价源是**硬编码价目表**（domains/modelbudget/modelbudget.go:44-56：gpt-4/claude-3-*/gemini-1.5-* 等 11 个名字），modelbudget.go:63-70 未知模型 Estimate=0 ⇒「unknown ≠ over budget」恒放行。生产真实模型名（如 glm-4.5-flash/minimax-m3，及 kx- 别名——别名剥离在闸**之后**，handler.go:3176 起）不在表内 ⇒ **闸对生产流量惰性**。
- `requestedModel` 为空时兜底 `"gpt-4"`（handler.go:3126-3128）；`outputTokens` 恒 1000，max_tokens 未消费（handler.go:3118 注释自认 TODO）。
- 降级会在 reqBody.Model 上写出梯内名字（如 gpt-3.5-turbo），该名字若不在真实 models 表 ⇒ 下游路由失败——目前因惰性而不可达，但价目表一旦对齐真实模型即暴露。
- 注：请求路径**已有**真预算闸（key 级 `CheckBudget` handler.go:2866 fail-closed ＋ MaaS 积分 `PreCheckCredits` handler.go:2885-2893，同在 serveWithExecutor，同样不覆盖三面）。modelbudget 是叠加在其上的、客户端自报预算的可选闸。

---

## 3. Q3：契约测试（2052e4d4f）—— 部分实现，13/13 实测绿

- 文件：`domains/streaming/handler_modelbudget_contract_test.go`（+149 行）。三组：TestChatRequestBody_MaxCostPerTask_Parsing（5 场景）/ TestEstimateInputTokensFromMessages（5）/ _EdgeCases（3）。
- 实跑（go1.27.4，`go test ./domains/streaming/ -run "TestChatRequestBody_MaxCostPerTask_Parsing|TestEstimateInputTokensFromMessages" -count=1 -v`）：**13/13 PASS**（本次审计实测输出，非采信提交信息）。
- 覆盖边界：只钉「payload 解析」与「token 估算启发式」（handler.go:1060-1070，len/4 字节启发式），**未**钉 serveWithExecutor 闸块（402 分支/降级分支/reqBody.Model 改写）的集成行为；提交信息自述的 Phase 2（acc-go+gateway 端到端）未落地——见 §7 的 `tests/e2e/maas_flow_test.go:25` `t.Skip` 桩。
- modelbudget 域单测 12 条全绿（含 TestEstimator_ConcurrentReads、TestDefaultLaddersFullyPriced）。

---

## 4. Q4：合并笔 c6bf29aea 三个「真缺陷」复核

### 4a. maas_calculate_price 单位归一 —— 已实现（双载体同步），但只在测试面可达
- 修后公式（migrations/maas_pricing_schema.sql:175-183）：`unit_price_minor::DECIMAL * p_quantity::DECIMAL / CASE pr.unit WHEN '1m_tokens' THEN 1000000 WHEN '1k_tokens' THEN 1000 ELSE 1 END`。1m_tokens 规则×原始 token 数不再超收 1e6 倍，语义正确（unit_price 以规则 unit 为基准）。
- 双载体同步实测：正本 migrations/maas_pricing_schema.sql:175-183 ＋ 测试夹具 internal/testutil/testdb.go:334-341 **逐字同形**（merge diff 双 + 号双侧均含归一 CASE）。
- 消费侧单源：domains/commercial/service.go:59-61 EstimateQuote 只经 `maas_calculate_price(...)` 取 total_minor，quote_lines 写入同源值（service.go:144 起）⇒ 无旁路乘法点（grep `unit_price_minor` 全仓仅函数内一处乘法）。
- **同族第二处排查**：根 maas 包积分计算除数齐备（maas/credits_sql.go:46 `/ 1000000.0`、maas/credits.go:95 `/ 1_000_000.0`）⇒ 无同型缺口。残余风险 P3：`maas_price_rules.unit` 无 CHECK 约束（schema:42 仅 NOT NULL），CASE 的 ELSE 1 兜底意味着写入 `'per_1M_tokens'`（959 号文件用的正是这个拼法，sql/migrations/startup/959_market_dual_floor_a17.sql:130）这类变体会**静默回到 1e6 倍超收**——建议接线时加约束或白名单。
- **关键限定**：maas_pricing_schema.sql / maas_v01_schema.sql / maas_quota_schema.sql 三个文件**不在任何生产投递通道**——grep 全仓（installer embeddata、dbinit runner、apply-db-revision-sequence.sh、tsv）零命中，唯一消费者是 testutil/testdb.go 的 testcontainers 路径。仓内审计档早已点破（docs/12小时内修订审计-2026-11-19-0400.md:160「两个 .sql 迁成 99X startup…加入 installer 内嵌清单」）且**未执行** ⇒ 两个修复在生产不可达（它们修的是测试容器里的账）。另：种子注释与值不一致（schema:233 注释 "$0.03 per 1M" 而值 '3000' minor=分 ⇒ $30/1M，差 1000×）——夹具瑕疵 P3。

### 4b. UsageCorrect 跨租户校验 —— 已实现，SQL 正确
- 修后 SQL（domains/ledger/service.go:454-456）：`FROM maas_usage_settlements st JOIN maas_usage_holds h ON st.usage_hold_id = h.usage_hold_id WHERE st.entry_id = $1 AND h.tenant_id = $2`。列存在性实测：maas_usage_settlements **确无 tenant_id 列**（migrations/maas_v01_schema.sql:159-170），有 usage_hold_id（:161）；maas_usage_holds.tenant_id 在（:137）⇒ 经 holds 关联是正确修法。守卫测试同步（domains/ledger/d9_tenant_scope_test.go:39 锚定正则改为 JOIN 形态）。
- 「同族第二处」排查：service.go:470 的第二个 `SELECT EXISTS(... WHERE entry_id=$1)`（无租户过滤）是**有意**的归属披露面——租户作用域 EXISTS 失败后区分「不存在」vs「存在但不属于你」，后者回 403 不回显存在性（service.go:464-473 注释自述 D9）。设计正确，非同族缺陷。其余跨租户面（UsageHold service.go:62-63/84、UsageSettle :262 holdTenantID 校验 :281）齐备。

### 4c. 993 迁移登记八面 —— 部分实现（embed face 仍缺，两道门在 HEAD 红）
逐面实测（八面口径）：
1. 正典 sql/migrations/startup/993_report_snapshots_exact_cost_cents.sql（+ .down.sql）：✅，且 11-20 自登记段已在（文件尾 INSERT INTO schema_migrations … ON CONFLICT DO UPDATE）。
2. installer embeddata/startup 副本 up+down：✅ 文件在且与正典逐字节相同（cmp 同）。
3. installer/migrations/993/ 副本（up/down/README）：**⚠ 部分同步**——up.sql 37 行，**缺 12 行自登记段**（cmp EOF 差异；down.sql 同）⇒ 「双载体同步」实际 2/3。
4. installer main.go go:embed×2 / var×2 / map×2：**✗ 全缺**——grep `993` 在 installer/cmd/llm-gw-installer/main.go 零命中（embed 序列 1840 行 992 → 1851 行 994 直接跳过）；embeddata 目录里的文件**没有被嵌入二进制**（无 go:embed 指令）。
5. dbinit runner StartupFiles：✅ installer/internal/dbinit/runner.go:1983。
6. installed_startup_migrations.tsv：✅ sql/schema/installed_startup_migrations.tsv:402（index 384）。
7. docker 通道 apply-db-revision-sequence.sh：✅ scripts/apply-db-revision-sequence.sh:2801。
8. docs/db-changelog.md 台账行：✅ :11117，SHA 936da9c9… 与当前正典文件 sha256 实测**逐字一致**（本次重算）。
合计 ≈ 6.5/8。
**门禁红实测**（installer 独立 go.mod，`cd installer/cmd/llm-gw-installer && go test . -count=1`）：
- `TestStartupFilesAreAllEmbedded`（stats_migrations_test.go:240）：StartupFiles entry "993…" not provided by setupSQLDir — 提示原话「add the file to … go:embed vars, and the embeddedSQLFiles map in main.go」⇒ 面向 4 的缺口被门点名。
- `TestStartupMigrationsAboveCutoffHaveLockGuard`（startup_ddl_lock_guard_gate_test.go:289）：993 含强锁 DDL（ALTER TABLE … TYPE）但**无 lock_timeout 预算**。
- 两门红**先于窗口**（993 由 0f07c848e 引入，先于 c4b5d00cb；窗口合并笔只补了自登记段+台账行，未碰 embed face）⇒ 非本窗引入，但 HEAD 仍红。后果链：走 installer 新装的库拿不到 993 ⇒ estimated_cost_cents 保持 bigint，而 4e6e14667 起写面/读面已按 numeric(24,6) 精确分值工作 ⇒ 新装环境 reportrollup 写非整数分值进 bigint 列（是否实际出错取决于驱动取整行为，待主控活体复核；docker 通道 211/154/245 已应用 993，不受影响——待主控活体复核台账）。

---

## 5. Q5：MONEY-TYPE 审计残留逐条核对

docs/MONEY-TYPE-AUDIT-2026-11-19.md 清单 vs HEAD：
1. contracts/maascontracts 合规：✅ 仍合规——Money.AmountMinor string（contracts/maascontracts/types.go:11-12）。
2. sales/quote.go float64 + 废弃：**已修（Phase 1 完成）**——`DEPRECATED` 标头在（sales/quote.go:3-4）；且 D19 订正把「不存在的 use_maas_v01 特性开关」话术撤掉（:7-11，全仓 grep `use_maas_v01` 仅此注释引用）。
3. maas/service.go CentsPerCredit float64：**未修（按计划保留）**——`CentsPerCredit float64` 仍在（maas/service.go:67），但文件头已加 DEPRECATED（maas/service.go:4）。属于文档声明的「废弃+重写」路径，不是回归。
4. **三特查项**：cents_per_credit 单位 / order_amount 维 / 毛利下限——**均已修**（2026-11-05 A-14 裁定落地）：÷100 修正在（sales/quote.go:210-212 注释+代码「A-14：cents_per_credit=分/积分 ⇒ ×cents 得分、÷100 折元」）；回归钉 `TestPrecomputeLinesA14Unit` 在（sales/quote_test.go:21）；三维红线可复算（sales/quote.go:928-944 margin_floor/discount_depth/order_amount + settings/spec_sales.go:16-18 阈值键）。units 语义三方锚点（consumption_detail.go:119 AF-1、bills.go:58、价目视图）仍在。
5. 结论：该文档的遗留态与 HEAD 一致，无新增漂移；真正未还的是 Phase 3「双写验证」（无实现、无开关——与 D19 一致，属有意缺省）。

---

## 6. Q6：maas/ 域集成测试实跑读数

- `go build ./api/... ./cmd/maas-api/ ./domains/... ./cmd/gateway/`：**exit 0**（本次实测）。
- `go test ./maas/ ./contracts/maascontracts/ ./domains/modelbudget/ -count=1`：全 ok；分项 -v 实测 **maas 根包 81 PASS / 10 SKIP / 0 FAIL**（SKIP 全部是 TEST_DATABASE_URL/TEST_MAAS_BILLS_DATABASE_URL 未配置的真库腿，skip 机制合规——maas/bills_realdb_test.go:27-30 未配置显式 Skip，配置了库名不合规会 Fatal，不是静默死腿）；modelbudget 12 PASS；maascontracts 全过。
- **testcontainers 族本机不可跑**：domains/{commercial,inventory,ledger,trading} 与 api/maas 的集成测试走 `testutil.SetupTestDB`（internal/testutil/testdb.go:28 testcontainers postgres:15-alpine）。本机 `docker info` 120s 无响应（守护进程宕）⇒ 实跑 600s 超时（goroutine dump 卡在 net/http writeLoop）。⇒ 合并笔声称的「commercial/ledger/inventory/reportrollup/api 全绿」**本次无法独立复现，采信状态＝提交信息自述，待主控在有 Docker 的环境复跑**（`go test ./domains/commercial/ ./domains/inventory/ ./domains/ledger/ ./domains/trading/ ./api/maas/ ./tests/e2e/ -count=1`）。
- `tests/e2e/maas_flow_test.go:25`：`t.Skip("Requires testcontainers-go …")` **硬编码跳过**，且函数体用 `NewService(nil)` 空池——即使解除 Skip 也是零断言价值的桩。所谓「完整 e2e flow」目前是**占位资产**（P2 假绿风险：交付报告 MAAS-COMPLETE-DELIVERY-2026-11-19.md 把它计入完成清单）。

---

## 7. Q7：窗口 maas 面文件全清单与高风险点

窗口（c4b5d00cb..c6bf29aea）实际触及：
- **maas/ 根包：零改动**（git diff --stat 空）；admin/ 的 maas handler 族零改动。
- 新增：api/server.go(+356)、cmd/maas-api/main.go(+82)、tests/e2e/maas_flow_test.go(+228)、domains/{commercial,inventory,ledger,trading}/service_test.go(+176/+284/+406/+256)、migrations/maas_quota_schema.sql(+130)、domains/streaming/handler_modelbudget_contract_test.go(+149)。
- 修改：domains/ledger/service.go(+5/-2 UsageCorrect)、domains/ledger/d9_tenant_scope_test.go(正则同步)、domains/inventory/service.go(+1 注释)、internal/testutil/testdb.go(+9 归一)、migrations/maas_pricing_schema.sql(+9 归一)、sql/migrations/startup/993(+12 自登记段)、go.mod/go.sum/vendor(gin+sonic)。

高风险点（逐条）：
1. **P2｜无鉴权可部署面**：根 api/server.go 信任头鉴权（:99-107）＋cmd/maas-api 无部署脚本；docs/API.md 以「可用 API」口吻书写 ⇒ 接线/部署时若照文档直起进程即开放计费面（§1）。
2. **P2｜e2e 桩**：tests/e2e/maas_flow_test.go:25 硬 Skip＋nil 池（§6）。
3. **P2｜D17 两项未还**（docs/12小时内修订审计-2026-11-19-0400.md:214 登记，标注「接线后」）：①released 截断仍静默（domains/ledger/service.go:308-315 `released.LessThan(Zero) ⇒ 0`，超额不报错不落差额行）；②entry_id 仍 `fmt.Sprintf("entry_%d", UnixNano)`（service.go:346）且 settlements.entry_id 无 UNIQUE（maas_v01_schema.sql:167 仅 TEXT 可空+普通索引）⇒ 并发结算同纳秒撞 id 无约束拦截。这两条在窗口内**未修**，需随接线轮显式销账。
4. **P3｜并发/事务面总体健康**：四个域服务写路径全部 tx＋FOR UPDATE（inventory/service.go:201/220-228 hold 串行化、ledger/service.go:104/262、trading/service.go:99），金额全程 decimal string（decimal.NewFromString 解析，:301-309），无 float/int64 金额混用新增——金额纪律在**新契约栈**内守住了；float64 仍限于 legacy maas/ 与 sales/（废弃面）。
5. **P3｜依赖面加重**：gateway 依赖图新增 gin+echo 双框架+bytedance/sonic（`go list -deps ./cmd/gateway` 实测），为两套零接线端点付出；go.mod 中 gin 标 `// indirect` 但实际被 api/server.go 直接 import（分类漂移，`go mod tidy` 可正）。
6. **P3｜价目/单位防御缺**：unit 无 CHECK 约束（ELSE 1 兜底静默错价）＋种子注释与值差 1000×（§4a）。

---

## 8. 复核要点清单（给主控的活体/门禁动作建议）

1. **有 Docker 环境复跑** testcontainers 族：`go test ./domains/commercial/ ./domains/inventory/ ./domains/ledger/ ./domains/trading/ ./api/maas/ -count=1` ——验证合并笔「全绿」自述（本子审计唯一未能独立复现的绿）。
2. **复跑 installer 门**：`cd installer/cmd/llm-gw-installer && go test . -count=1` ——HEAD 预期仍红 2 门（993 embed 缺＋lock guard 缺）；裁定收口轮次（补 go:embed/var/map＋up.sql 自登记段同步＋993 加 lock_timeout，或按仓内先例给 993 退役/豁免声明——需要裁决，不要静默改判据）。
3. **活体查 252 台账**：`maas_usage_holds`/`maas_usage_settlements`/`maas_price_rules` 三表在生产库是否存在（推断：不存在，因为 v0.1 schema 无投递通道）⇒ 决定「接线轮」前是否先立 schema 投递面（99X startup 化）。
4. **核对 cmd/maas-api 是否在任何主机有进程/端口 8080 暴露**（三台 211/154/245）——若有即是无鉴权计费面，需立即下线。
5. **C-1 处置裁决**：两套 HTTP 实现（echo 版 api/maas、gin 版根 api）二留一，另一套删除或显式标注「未接线」；同时决定 gateway mux 接线轮次与鉴权适配层（59 轮处方①）。
6. **modelbudget 验收口径**：契约测试 13/13 绿但闸体无集成钉测——接线裁决时把「402 分支＋降级分支＋三面覆盖问题（messages/responses/gemini 无闸）」立为 P1#3 的未完项，不要按提交信息「接线完成」闭环。
7. **D17 销账**：接线轮把 released 超额语义＋entry_id 唯一性一并修（当前登记「接线后」，窗口未动）。

## 附：本子审计方法注记
- go 工具链：/Users/niuer/homebrew/bin/go（go1.27.4），主仓与 installer 为两个独立 go.mod（installer 测试须 cd 进其模块跑）。
- 本机 Docker 守护进程无响应（`docker info` 120s 无输出）⇒ 一切 testcontainers 族测试在本环境**挂起而非失败**，判定「不可跑」而非「红」。
- 证据采样命令：git log/show/diff、grep、cmp、shasum、go build/test/list/go vet 未跑（build exit 0 已覆盖编译面）。
