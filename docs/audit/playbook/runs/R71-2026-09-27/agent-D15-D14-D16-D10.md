# D15+D14+D16+D10 子代理报告（窗口：9785c2398..HEAD，2026-09-26 16:15 → 09-27 16:20，34 提交）

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **detail 端点 500 收口不完整：querySessionDetail 分支仍回显内部错误串**。0aa86d8bd 声明"500 no longer echoes internal error text"，但只修了 resolveSessionID 分支；同一 ServeHTTP 里 `querySessionDetail` 失败臂仍是 `fmt.Sprintf("query failed: %v", err)`，err 为 `query session: %w`/`query turns: %w` 包装的原始 pgx 错误（含 SQL 片段/约束名）。且该分支**无 slog 服务端日志**（对照同函数 241 行 resolve 分支的 slog.Error），违反 D15 域文档 R69 回注“数据完整性类 500 必须落服务端日志作告警锚点” | `admin/session_detail_v2.go:248`（err 来源 `admin/session_detail_v2.go:283/292`） | 触发路径：GET /api/admin/sessions/detail 解析成功后 querySession/queryTurns 遇 DB 错误（10s ctx 超时/连接失败/分区视图缺失）→ 客户端 500 体拿到 "query failed: query session: \<pgx 错误文本\>"。修法同 resolve 分款：slog.Error + 固定文案，契约测试补同款泄漏断言 |
| 2 | P2 | **本轮新挂生产的 list_v2 端点 500 回显 err.Error() 原文**。2a8ad6b74 给该端点补了鉴权/租户防线，但错误体卫生未做：500 体直接 `err.Error()`（sessionforensics.ListRecentSessions 的底层 DB 错误），同样缺服务端日志锚点 | `admin/session_list_v2.go:149`（挂载点 `cmd/gateway/main.go:6904`） | 触发路径：GET /api/admin/sessions/list 期间 DB 错误 → 原始 pgx 错误文本（含 SQL/tenant 参数）回给已认证客户端。与发现 1 同批收口：固定文案 + slog.Error |
| 3 | P2~P3 | **F3 的 primary_request_id ON CONFLICT 臂语义与“首值优先固化”声明相反（实为最后非空 wins）**。表达式 `COALESCE(NULLIF(EXCLUDED.primary_request_id,''), public.sessions.primary_request_id)` 让每轮 upsert 的非空新值**覆盖**旧值；而 outboxUpdate 每轮构造（`domains/session/v2/session_writer_v2.go:668-690`，RequestID=req.RequestID）→ 活跃会话该列收敛为**最近一轮** request id。三方契约均为“第一个请求”：430 迁移列注释、repair 工具写 `v1Turns[0]`、本处代码注释自称“首值优先固化首个请求 id”。shape 测试钉住的正是错误方向的表达式 | `domains/session/v2/session_aggregator.go:284`（注释 280-284、参数 $24 于 297）；契约源 `sql/migrations/startup/430_sessions_v2_schema.sql:65`；对偶写方 `cmd/tools/validate_sessions_v2/repair.go:355`；测试钉 `domains/session/v2/session_snapshot_attribution_test.go:40-43` | 触发路径：会话第 2 轮起每轮聚合 upsert 把 primary_request_id 从首请求改写为当轮请求；repair 过的会话再活一轮即被翻转。反向映射臂（resolveSessionID 步骤 2）功能**不破**（同会话所有 request_logs 行 gw_session_id 相同，任一 request_id 均可命中）——这是测试全绿的原因；但列契约漂移、双写方语义分裂、响应字段 `primary_request_id`（`admin/session_detail_v2.go:98`）对客户端语义失实。修法：改 `COALESCE(public.sessions.primary_request_id, NULLIF(EXCLUDED…,''))`（存量优先=首值固化）并同步 shape 测试；或裁决改契约并更正全部注释。需主代理亲读裁决方向 |
| 4 | P3 登记 | **admin 包 500 回显同类模式存量 85 处/22 文件**（窗口外既有债，横向扫描产物）：credential_monitor.go:727/854/912/969/1434/1616、dashboard_board.go:170、dashboard_session_stats.go:128、degradation_control_handlers.go:77-97、diagnostics_credential.go:84-288、diagnostics_routing.go:77-336、free_discovery.go:118、ip_blocklist.go:35-149、maas_handlers.go:55/74、session_export.go:189/349/385、session_summary_v2.go:112/133 等 | 0aa86d8bd 只收口了 detail 端点 resolve 分支；建议登记 standing checklist 独立轮次批量收口（统一固定文案+slog），避免逐轮点名 | 登记债 |

## 二、核实为健康的面

- **歧义 409 主体在 HEAD 完整**（D14/D16 重点项）——`admin/session_detail_v2.go:512-521` 反向臂 `DISTINCT + LIMIT 2`（509-511 注释钉“计数为下界”的截断语义）；租户谓词双层（外层 `s.tenant_id=$1` + 内层 `request_logs.tenant_id=$1`）；`defer rows.Close()` 在 Query err 检查之后（528），Scan/Err 错误臂均覆盖；`errors.Is(err, errSessionAmbiguous)→409 固定文案`（237-240）；>1 拒绝前 slog.Warn 告警锚点（548）。契约测试钉死：409 状态、响应体五元组泄漏断言（srv_a/srv_b/tenant-a/gw_dupe/原始错误串）、新增 TestSessionDetailV2_ResolveFailureDoesNotLeakInternalError 覆盖 500 分支、mock `WithArgs("tenant-a", …)` 参数序钉租户谓词（删谓词即红）——`tests/session_identity_contract/session_identity_contract_test.go:406-511`
- **list_v2 是真接线非死代码**——`cmd/gateway/main.go:6903-6904` 精确 pattern 挂载 + wrapAdmin；handler 内 GetAuthContext nil→404（`admin/session_list_v2.go:124`）+ tenantFromQueryOrContext 钉扎（128）；`tenantFromQueryOrContext` 兼容 `?tenant_id=` 仅 super 生效（`admin/session_turns_v2.go:856-877`）；钉桩 TestSessionListV2_NoAuthContextRejected（404 且零 DB 访问）与 TestSessionListV2_TenantPinnedToAuthContext（?tenant=other 被钉回 auth 租户，WithArgs 断言）
- **SessionPK/turnPK 序列化出口封死**——SessionV2.ID/SessionTurnV2.ID `json:"-"`（`admin/session_detail_v2.go:79/113`）；两 struct 全仓唯一使用点即本文件，序列化出口唯一（ServeHTTP 261-272 map 组装）；`web/src/api/sessions_v2.ts` TS 类型无数值 id 字段（turn_no 为外部定位键）；日志/审计面无 SessionV2/SessionTurnV2 的 %+v 输出（全仓 grep 无命中）
- **outbox/writer.go 5 行**——`string(payloadBytes)`（`internal/outbox/writer.go:126`）与 `domains/hooks/observability/telemetry/client.go:2722` 既有 string 化范式一致；SimpleProtocol 前提成立（`db/db.go:72` DefaultQueryExecMode=QueryExecModeSimpleProtocol）；outbox.Writer 确无生产构造点（仅 main.go:179 stub），“接线前修复到位”声明属实
- **telemetry client.go 15 行**——两处 insert（insertSessionOpenedEvent/insertRequestCompletedEvent）payloadJSON→string 与 R11 FIX-C 同根一致（1771/1801 段）；claim 热表臂补 `other.gw_session_id IS NOT NULL AND <> ''`（client.go:2632-2635）与 promoted 臂同论证：外层 `COALESCE(gw_session_id,'')<>''` 下 NULL/'' 行本不可匹配，零行为差、解锁 partial 索引蕴含；final_success_claim_test.go 形状正则同步钉住热表臂段
- **rollup UTC 口径注记与两侧接线一致**（D10）——生产侧全 UTC（`bg/report_rollup_worker.go` now().UTC()/02:00 UTC/补跑昨日=UTC 昨日，RollupDay UTC [00:00,+24h)）；消费侧 `admin/report_rollup.go:80-98` reportRange 用 `time.ParseInLocation("2006-01-02",…,time.UTC)`——读面同为 UTC 日，注记“勿解读为上海日”与实际相符；UTC 日窗横跨两个上海日物理分区、pruning 仍扫 2 分区的论证正确。写必有读：report_snapshots 有 admin 读面（summary/export/run）
- **menu-config.json 零语义变化**——仅 exported_at 一行时间戳（46f215a83/59107132e 部署副产物收口），无菜单条目增删；D15 生成物纪律无违例
- **R69 各标注类改动属实**——session_list.go 家族 Deprecated 标注（生产零构造点、parseIntParam 共享活函数先迁移的注记与 grep 相符）；serveSessionTurnsList 遮蔽死路径标注属实（handler.go:1171 精确 pattern 遮蔽子树 case "turns"）；unifiedTurnsTreeFallback 补 IDKind/PrimaryKey 与 unified 主路径、child requests 三处一致
- **c6e36134b routing_health_checks NULL 修复正确**（D14 扫描类）——availability_state/circuit_state 可空列 COALESCE 'unknown'，防 pgx Scan NULL→string 使整批中止；stripMarkdownNoise 3 钉桩在位，TrimLeft 误伤面与十三轮 O-2 已登记观察一致（维持挂账）

## 三、未覆盖项与原因

- **三门实跑（go build/vet/test）**——本子代理规约禁止跑构建/测试；依据为窗口内各提交声明的门禁记录及十四轮文档 §2.4 的独立重跑声明（自 0aa86d8bd 后无新提交触碰上述文件，HEAD=949ec2f70 merge 零额外 diff）
- **500 回显横向扫描范围**——限 admin 包 HTTP 响应面（writeError/writeExportJSONError 两族）；domains/ 内自有 handler（apihub/hub 等）未扫，超出窗口改动面
- **mockprobe/752 迁移八维度**——十四轮 §2.4 已全查，本域仅抽验其与 D15 无交集（无新前端页面/菜单项），未重查
- **真机部署面验证**——409 在 245/252 的实际响应、list_v2 生产可达性、primary_request_id 真库空置率，需真库/凭据，未覆盖（与 R69 §六 下轮入口②重合）
- **409 新语义的消费方适配**——web 端无 sessions/detail 直接消费（grep 无命中）；sessionforensics CLI 对 409 的处理未逐脚本核（CLI 侧把非 200 一律按失败呈现，无功能性破裂证据，但未亲读 CLI 源码）

> 主代理复核结论（R71 收口时回填）：发现#1/#2 → F7 修复（固定文案+slog+契约测试双泄漏断言）；#3 与 D01 代理 #1 合并 → F3 存量优先翻转+形状测试同步；#4 → L4 登记债（合并 D08 代理 26 处/8 文件口径，总数 85 处/22 文件）。健康面维持。
