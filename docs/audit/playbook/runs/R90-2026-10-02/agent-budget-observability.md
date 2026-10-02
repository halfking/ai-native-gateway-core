# R90 预算/可观测性域深挖报告（HEAD=20fbdba74，只读审计）

只读审计，未修改任何文件。主会话复核注：遗留项 1(c)/2/3/4 的修复落点建议已按报告落地（拦截计数、租户围栏、request_id 断言、gate 指标）；新发现 /v1/responses 无预算预检登记不修。

## 一、发现（候选，待主代理复核）

### 遗留项 1：预算执行面 fail-open

**(a) 非 BudgetExceededError 被吞放行——全仓仅 2 个生产调用点，均已核实**

| 调用点 | 代码形态 | 触发路径 |
|---|---|---|
| `domains/streaming/handler.go:2385-2393` | `if budgetErr := h.keyVerifier.CheckBudget(...); budgetErr != nil { if _, ok := budgetErr.(*authentication.BudgetExceededError); ok { ...402... } }` —— else 分支不存在：DB 错误直接静默落出 if，无 slog、无 metric，请求继续放行 | ChatHandler 主路由每请求预算预检 |
| `domains/streaming/embeddings.go:399-404` | 同款形态 | /v1/embeddings 每请求预算预检 |

grep 全仓：`CheckBudget` 生产调用点只有这两个。**附带新发现：`/v1/responses` 路径（`domains/streaming/responses.go:217-226`）只做 `verifyRequestKey`，完全没有调用 CheckBudget**——该端点金额预算预检缺失（超出本轮范围，登记）。

DB 错误来源形态（`domains/authentication/verifier.go`）：预算行查询错误（:771-774）、花费查询错误（:799）、usage_ledger 视图缺失时 spent=0 + 仅 slog.Warn（:791-801，预算执行整体降级放行）。

**(b) "best-effort 是既存设计"自述出处**：`verifier.go:757-761` 快照分支注释，与两个调用点的吞错形态互为因果、闭环自证，无任何裁决记录。

**(c) 现有指标机制与拦截计数落点**：根包 `metrics/`（promauto + 默认 registry），`gateway_` 前缀 + snake_case + `_total`，GW-00 低基数闭集标签。域内先例：`domains/requestjourney/recorder_metrics.go`（dropped/degraded 二分）。metrics 是叶包，authentication 引它无环。建议 `gateway_budget_checks_total{outcome="ok|exceeded|error|skipped_snapshot"}` 集中在 verifier.go choke 点。

**(d) fail-closed 503 影响面**：调用方仅 chat 主路径 + embeddings。鉴权路径对 DB 错误已 fail-closed 503（handler.go:2339-2443 `auth_unavailable`），预算 fail-closed 与既有姿态一致。真正放大源是 `checkBudgetDB` 第二条查询（DISTINCT ON + SUM 重查询）无显式超时（继承请求 ctx，DB 变慢表现为悬挂；admin 侧 budgetCheck 有 5s 超时对照 admin/keys.go:958）。缓解顺序：先超时+计数，再切 fail-closed；可短 TTL 缓存上次 spent 做断路。**P2（静默放行无观测信号是核心缺陷；fail-closed 切换需 Owner 拍板）**。

### 遗留项 2：budgetCheck 租户硬编码 'default'

现状：`admin/keys.go:962-965`。`GetTenantID(r)`/`IsTenantAdmin(r)` 就在同包，verifyKey 已正确使用（:928-931 R46 形态），budgetCheck 未跟随。危险性核实：路由挂 `AdminMiddleware`（admin/handler.go:1249），而 AdminMiddleware（admin/auth.go:76-153）**只验 JWT 有效（UserID>0），不验角色**。后果：(i) 任意租户的 tenant_admin 用数字 ID 枚举可探到 default 租户任意 key 的存在性 + budget + 实时花费（200 vs 404 区分）——与 R46 在 verifyKey 刚封住的跨租户泄露同类；(ii) 非 default 租户自己的 key 一律 404，多租户功能残废。**P2（跨租户花费泄露 + 功能缺陷）**。

### 遗留项 3：checkBudgetDB DISTINCT ON 对 request_id='' 空串折叠

折叠方向：空串行折叠为 1 行 → SUM 少算 → **fail-open 方向**。写入侧单一生产写方 `domains/hooks/observability/telemetry/client.go:1284`；RequestID 恒为服务端 UUID（client.go:447-450 + middleware/requestid_mw.go）；Emit 层双守卫已在（client.go:891-901/927-933）；无第二个台账写方。写入侧断言零误伤。**P3（不变量已双守卫，补最后一条纵深）**。

### 遗留项 4：output gate 指标

决策点核实（28 轮给的 `security/sanitize/interceptor.go:244-265` 路径不存在，实际为）：
- gate 决策本体：`security/sanitize/output_sensitive.go:24-26/:62-63/:140`；credential 键名无条件 Blocked。
- 动作落地（block/mask/observe 三态）：`domains/hooks/outputcompliance/interceptor.go:211-248`；流式三处 Blocked→error：`stream_compliance.go:195/:372/:424`。
- chain 层：`domains/hooks/response/chain.go` FailClosed 69-71/138-140。
- 现状：chain 与 outputcompliance 均无 prometheus；`go list -deps` 确认引入 promauto 无环；**chain 不能引 outputcompliance（会成环），公共落点 = 根 metrics 包**。

### 遗留项 5：session_dim.status/created_at NOT NULL 无默认

350 有默认（:32-41）但未注册（<478 下限 + clobber 禁区）；805 无默认（有意按 2026-10-01 生产实测 17 列形态）。**重要前置**：`docs/audit/2026-10-02-round44-ssot-decision.md` §6（227-245）已推翻此项为缺陷——两条 INSERT 写方（sessionv2mirror/session_dim.go:80,113）都显式给 'active'/NOW()，裁决"不为此新增迁移"。若 Owner 推翻：不能改 805 本体（SHA 已钉），取新迁移号（808+）+ 四处登记（runner.go StartupFiles / embeddata 副本 / TSV 重生成 / db-changelog SHA 行）。

### 遗留项 6：夹具 credentials DDL 与生产列默认值漂移

生产基准 `sql/schema/01-schema.sql:6779-6874`。四处夹具差异清单（枚举值漂移 'available'/'live' 回放生产 CHECK 必拒、tenant_id 't0' 语义分叉、幻影列 raw_model/name、revision 无默认、fp_slot_limit 家族缺列）。**P3（testcontainer 自建表漂移不可见于测试；一旦被复制进 bootstrap/迁移 SQL 即变真实缺陷）**。

## 二、各遗留项的修复落点建议（供主代理直接落地）

1. 预算拦截计数：/metrics/budget_metrics.go + verifier.go choke 点（761/773/799/803/805）+ 两调用点 slog.Warn。
2. budgetCheck 租户围栏：keys.go:962 删硬编码，IsTenantAdmin → AND tenant_id=$2，逐字复制 :928-931 R46 形态。
3. request_id 纵深断言：insertRequestLog Exec 前空断言 + incSanitizeEvent 同款标签；不建议 DB CHECK。
4. output gate 指标：根 metrics 包统一落点（防 chain→outputcompliance 环）；decisions{gate,action,path} + fail_closed{gate,source}；预热全部 series（R81 同型教训）。
5. session_dim 默认值：遵守 round-44 §6 裁决默认不做。
6. 夹具 credentials：'available'→'ready'、'live'→'active'、't0'→'default'、删幻影列、revision DEFAULT 0；中期收敛为单一共享 test helper。

## 三、未覆盖项与原因

- fail-closed 503 真实 QPS 放大量化：无生产流量画像，实测需 Owner 提供 252 数据。
- /v1/responses 无金额预算预检：属新发现，是否覆盖需 Owner 判断该端点计费模型。
- 夹具 UNIQUE 约束与生产索引逐一比对未做（漂移清单限定列/默认值/枚举三维度）。
- budget 视图缺失时的告警面已并入 outcome 标签方案，未单独立项。
- 350 拆分注册可行性未评估（805 已按生产形态建表，350 的 DEFAULT 段由新迁移方案覆盖）。
