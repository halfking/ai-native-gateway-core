# D15+D14 admin 会话契约面 子代理报告（窗口：092ab1b61..908256008）

> 主代理复核结论（R69 收口时回填）：发现#1/#2 实锤→本轮 F2 修复（list_v2 接线 + GetAuthContext/tenantFromQueryOrContext 钉扎 + 2 契约测试）；发现#3/#4/#5/#6/#7/#8 实锤→本轮 P3 修复（errors.Is 补齐/测试注释更正/fallback 填标注/LIMIT 2/歧义 Warn/turns nil→[]/tenant_id 别名/文件头注释更正）。健康面维持。

改动面：`admin/session_detail_v2.go(+141)`、`admin/session_list_v2.go(+106)`、`admin/session_list.go`、`admin/session_turns.go`、`admin/session_turns_tree.go`、`admin/session_turns_unified.go`、两份测试；提交 9f62818c5 / 9785c2398 / eff61ecdf。窗口未改 `web/` 与 `scripts/test_sessions_v2_api.sh`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **SessionListV2API 全仓无生产构造点（O-C 确认），且其文档化路由 `/api/admin/sessions/list` 实际被 V1 子树路由吞掉**：`NewSessionListV2API` 除定义处外全仓零调用；该路径被 `admin/handler.go:1111` 的 `/api/admin/sessions/` 子树捕获，`handleSessionSubrouter` 把 `"list"` 当 sessionID 走 `serveSessionDetail` → 必 404。触发路径：`sessionforensics list --from <gateway>`（cmd/sessionforensics/main.go:181）与 scripts/test_sessions_v2_api.sh:63 打该路由均拿不到信封化响应；9f62818c5 的 id_kind=gw_session_id 信封标注在生产零消费面 | admin/session_list_v2.go:34、admin/handler.go:1111、admin/session_state_handlers.go:149-154 + 217-219、cmd/sessionforensics/main.go:177-181 | 待复核后二选一：在 main.go:6876 旁挂 `mux.HandleFunc("/api/admin/sessions/list", wrapAdmin(...))`（精确 pattern 优先于子树），或明登记灰藏并在 sessionforensics CLI 改走现存路由 |
| 2 | P2 | **SessionListV2API.ServeHTTP 无鉴权上下文检查、无租户钉扎，与同族 detail 端点防线不对齐**：tenant 直接取 query 缺省 `"default"`，不调 `GetAuthContext`/`tenantFromQueryOrContext`。当前因发现 1 未挂载而不可利用，但注释即文档，后人按注释挂上 mux 后 tenant_admin 可用 `?tenant=other` 跨租户读会话列表。测试里设 super_admin auth 只是仪式性的（handler 根本不读） | admin/session_list_v2.go:117-120 vs admin/session_detail_v2.go:188-196（对照组）；tests/session_identity_contract/session_identity_contract_test.go:163 | 挂载前必须补 GetAuthContext nil→404 + tenantFromQueryOrContext 钉扎；契约测试补 tenant_admin 越权用例 |
| 3 | P3 | **F-3 errors.Is 修复不完整**：`querySession` 仍残留 `err.Error() == "no rows in result set"`（包装或文案变化即把 404 变 500）；且 admin/session_detail_v2_test.go:302-304 的注释把该实现当"契约"锁住并称"避免修复成 errors.Is 改变语义"——理由错误。另 ServeHTTP:227 用 `err == errSessionNotFound` 而非 errors.Is。F-3 提交自称"字符串比较×2"仅覆盖 resolveSessionID 两处 | admin/session_detail_v2.go:336、admin/session_detail_v2_test.go:301-304、admin/session_detail_v2.go:227 | 改 `errors.Is`（行为不变）；顺手 :227 改 errors.Is；修正测试注释措辞 |
| 4 | P3 | **unified turns 的 tree_fallback 分支不填 id_kind/primary_key，且注释与实现相悖**：session_turns.go:44-47 注释声称 fallback 在装配时填入，但 `unifiedTurnsTreeFallback` 构造 TurnListItem 未填两字段。触发路径：老会话无 V2 shadow → unified 端点返回的 turns 无身份标注 | admin/session_turns.go:44-47、admin/session_turns_unified.go:103-108 + 204-214 | fallback 填 `IDKind="session_id"; PrimaryKey=sessionID`；测试补 fallback 分支值断言；或修注释改口 |
| 5 | P3 | **resolveSessionID 反向映射候选集无 LIMIT（量级风险复评）**：DISTINCT 全量收集，而 >1 即拒绝 → LIMIT 2 语义等价。同租户调用方可用同一 gw_session_id 铺 N 请求+N 会话使每次详情请求物化 N 行候选才 500——租户内自伤型放大，无跨租户面 | admin/session_detail_v2.go:491-499 + 507-514 + 518-527；索引：355_session_analytics_indexes.sql:6、430_sessions_v2_schema.sql:80 | 外层加 `LIMIT 2`，行为与测试不变 |
| 6 | P3 | **歧义拒绝新路径零服务端观测**：F-1 的 >1 拒绝只把错误串回给客户端，无 slog/metric。触发路径：某 gw_session_id 映射多会话（数据完整性信号）反复发生时运维不可见 | admin/session_detail_v2.go:523-527 → 231 | 500 前补 slog.Warn |
| 7 | P3 待复核 | **detail 响应无轮次时 `"turns": null` 而非 `[]`**：`var turns []SessionTurnV2` 零值 nil 进响应 map。触发路径：会话存在但无 turn（或 offset 越界）→ 严格解析端易崩 | admin/session_detail_v2.go:406 + 252 | 装配时 nil→空切片；契约测试补 null→[] 断言 |
| 8 | P3 | **注释/控件一致性两则**：(a) session_detail_v2.go:14 文件头"仅 super 用户可用"与实际不符——wrapAdmin 后任何 admin 角色可达；(b) V1 列表用 `?tenant_id=`（session_state_handlers.go:61）而 V2 detail 用 `?tenant=`，同族端点控件命名不一 | admin/session_detail_v2.go:14、admin/session_turns_v2.go:849-858、admin/session_state_handlers.go:59-68 | (a) 注释更正；(b) V2 侧兼容读取 tenant_id 作别名或登记已知差异 |

## 二、核实为健康的面

- **detail 端点路由与鉴权**：`/api/admin/sessions/detail` 注册于 cmd/gateway/main.go:6876，与 summary 同为 wrapAdmin（main.go:6615-6622）；handler 自身另有 GetAuthContext nil→404 双保险（session_detail_v2.go:188-191）。
- **租户隔离**：querySession/queryTurns/resolveSessionID direct 与 reverse 内外两层均带 tenant 谓词（:311/:395/:474-476/:493-498）；非 super 被 tenantFromQueryOrContext 钉 auth tenant；跨租户 404 有契约测试钉住（contract_test:417-446）。RLS 为兜底层。
- **数值 PK 外泄全扫**：admin 包其余数值 `json:"id"` 均为各自域表自身主键（有意暴露，不属 §1.5 五类契约）；`session_turns_v2.go:712` snapshot 与 `turns_sessions.go:151` 的查询 SELECT 不取 `s.id`——SessionV2.ID 与 SessionTurnV2.ID 确为仅有的两处，均已 json:"-"。CONTRACT_FREEZE_2026-08-22.md §1.5 原文核实一致。
- **resolveSessionID 守卫主体**：DISTINCT 收集 + defer rows.Close + rows.Err + 0→404 + 1→通过 + >1→显式拒绝；404 路径不泄露内部细节；歧义 500 有钉桩（contract_test:350-383）。
- **契约测试质量**：list_v2 mock 走真实代码路径（SQL regex 与 export.go:365-382 真实 SQL 匹配，11 列 mock 与 Scan 形状逐列一致）；SessionPK/turn PK 断言方向为"缺失而非泄漏"；F-4 注释与实现三查询一一对应；复用真实构造函数。
- **omitempty 标注一致性**：V1 SessionSummary 的 IDKind/PrimaryKey 两处构造点均填充、全仓无第三构造点→恒显式；turn 级 omitempty 为保 V1 tree 端点 JSON 零变更（session_turns_tree.go:66-68 注释自洽）——除发现 4 的 fallback 缺口外风格成立。
- **web 消费面**：web/src/api/sessions_v2.ts 的 TurnDetail/TurnListItem 无 id 字段——N-1"消费面不读该字段"复核成立；web 全 src 无 /api/admin/sessions/detail 消费者；新增 id_kind/primary_key 为加法字段。
- **SessionAudit/信封形状**：sessionforensics.SessionAudit（types.go:141-157）无数值 PK；list_v2 信封与 detail 信封命名一致。

## 三、未覆盖项与原因

- 活库 RLS 兜底实测——需 TEST_DB_URL 活库。
- 三门实跑——只读约束未运行，主代理收尾应实跑（已实跑，全绿）。
- contract_test 的 makeContractSessionDetailRow 对 created_at/updated_at 传 nil 的 pgxmock 保真度——主代理实跑 admin 包全绿，无 Scan 报错。
- cmd/sessionforensics migrate/replay 子命令全链路——超出窗口。
- scripts/test_sessions_v2_api.sh 存量问题（无 Authorization 头、打不存在路由）——文件未在窗口改动面，接线后可复验。
