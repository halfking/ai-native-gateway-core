# R71 修复 sanity 复核子代理报告（D15+D17，窗口：949ec2f70..24c5c545a）

> 窗口主体：8b6fbcc10（R71 修复，27 文件）+ merge cb1ac88d3。本轮任务为复核修复本身是否修完整/是否引入新缺陷，非全仓重扫。窗口内其余提交（turn-logs TTL/FIFO 子任务、753 迁移、R71 文档）不在本路分工。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **F6 修完整性缺口：center CreateCommand 的 argsJSON 同根残留未修**。`argsJSON, _ := json.Marshal(cmd.Args)` 以 []byte 裸传 `center_commands.args`（JSONB，377_center_ops.sql:45）。该位点**有生产构造点**：main.go:6590-6593 已接线 PgxStore + AdminAPI 并 RegisterRoutes（`POST /admin/center/instances/:id/command` → center/admin_api.go:33 → center/server.go:46 IssueCommand → CreateCommand）。db/db.go:72 全局 `QueryExecModeSimpleProtocol` 下 []byte 内联 bytea hex，jsonb 解析必炸（R11 FIX-C 机制，bg/feature_stats_worker.go:169 同注）→ 管理台下发命令必败。R71 commit message 只声称修了"center resultJSON"，同文件漏臂 | center/store_pgx.go:211-218；center/admin_api.go:33；cmd/gateway/main.go:6590-6593；db/db.go:72 | 与 F6 同款 string 化（`argsJSON` 经 interface{} 传 string；marshal err 目前被 `_` 吞掉可顺带补） |
| 2 | P3 | **F6 第二处同根残留：RecordHeartbeat metricsJSON []byte → instance_heartbeats.metrics（JSONB）**。调用方 center/client.go:59（Client 心跳循环），但 center.Client 当前无生产构造点（main.go 无 center.NewClient）——按"接线前修复到位"政策本应一并修，R71 修 UpdateCommandStatus 漏此两处 | center/store_pgx.go:139-168（insertQuery 绑 metricsJSON）；center/client.go:59 | 接线前 string 化补齐（nil 保持 NULL 语义同款） |
| 3 | P2 | **admin 500 回显存量债登记数字失真：R71 记载"85 处/22 文件"无法按任何合理口径复现**。实测（可复现命令见 §二末）：主口径（同线 `err.Error()` + `StatusInternalServerError`，全部经 write*/http.Error helper，0 注释污染）= **395 处 / 77 文件**；加 `fmt.Sprintf("…%v/%s", err)` 同线变体 55 处；加 http.Error 非变量/502/503 变体约 +18；多行调用 1 处（admin/modules.go）。合计 **≈469 处 / 约 80 文件**。最接近的窄口径（裸 `err.Error()` 作唯一消息参数）也只有 53 处/14 文件。L4 批量收口工作量约为记载的 5.5 倍，规模判断需按新数字重估（D11 教训同型：摘要数字必须与代码事实对账） | top 文件：admin/prompt_injection_handler.go(28，如 :1063/:1109/:1128)、admin/maas_handlers.go(27，:491/:570)、admin/routing.go(23)、admin/model_policies.go(19)、admin/proxy.go(18)、admin/credential_monitor.go(16，:1677)、admin/output_compliance_handler.go(15)、admin/usage.go(11)、admin/providers.go(10)、admin/provider_credential.go(10)、admin/tenants.go(9)、admin/systemmonitor_handlers.go(9)、admin/dashboardapi/session_overview.go(9)、admin/agents.go(8)、admin/dashboardapi/errors.go:135（writeErrorJSON 第4参 detail=err.Error() 变体） | 主代理亲读抽样复核后按新规模立项；收口模板已有：admin/auto_route.go:1006 writeInternalErr（slog 落服务端+固定文案） |
| 4 | P3（观察） | admin 包 slog 键不统一：R71 新增日志用 `"err"`（与同文件 resolveSessionID 既有键一致），而 auto_route.go writeInternalErr 用 `"error"`。同包两种键名，日志检索需双词 | admin/session_detail_v2.go:251、admin/session_list_v2.go:151 vs admin/auto_route.go:1010 | 不阻断；批量收口 L4 时统一即可 |

## 二、核实为健康的面

- **F3 翻转正确且边界闭合**：新冲突臂 `COALESCE(public.sessions.primary_request_id, NULLIF(EXCLUDED.primary_request_id, ''))`（session_aggregator.go:291）与 430:65 列注释"第一个请求的request_id"一致；INSERT 臂 `NULLIF($24,'')`（:238）已归一空值 → "存量空串永久固化"边界不成立；租户守卫 WHERE 保留；形状测试同步翻转（session_snapshot_attribution_test.go:41-45）；同文件 730 agent_role 注释（"不能用 COALESCE(NULLIF(EXCLUDED...))"）与新注释形态学互证一致；commit 引用的 repair 工具符号 v1Turns 存活（cmd/tools/validate_sessions_v2/main.go:153，R43 符号存活纪律通过）。
- **F7 收口完整**：detail query 臂（session_detail_v2.go:251-252）与 list_v2 端点（session_list_v2.go:151-153）均改固定文案 + slog；resolve/query/list 三臂现已无任何 err.Error() 回显（grep 两文件零命中，list_v2.go:150 仅注释提及）；fmt 仍被 querySessionDetail 使用无 import 悬空；契约测试双泄漏断言齐备（tests/session_identity_contract/session_identity_contract_test.go:517-585，query 臂断言 secret/"turns_missing"/"query failed"/"tenant-a" 四元，list 臂断言三元）。
- **F2 双腿化规范**：反向臂 hot∪母表 UNION ALL + 外层 DISTINCT + LIMIT 2（session_detail_v2.go:521-547，满足 D15 R69 回注的 N+1 候选集纪律）；同 gw_session_id 跨 hot/母表并存时 DISTINCT 去重不影响歧义判定；歧义 slog.Warn 告警锚点保留；sqlreadguard LEGIT 条目按规范登记为"母表腿（hot 腿同查询内联）"（internal/sqlreadguard/guard_test.go:43）。
- **F4/F5 NULL 语义正确**：projectattr evidence 经 `var evidenceParam interface{}` + `len>0` 判空（store.go:195-199），空→nil 保 SQL NULL，非空→string；SaveAttribution 生产接线活跃（domains/analysis/projectattr/hook.go:116）；钉桩断言 string/nil 双案（store_test.go:180-224）。approval 三位点（SaveConfig 1 + SaveRule 2）string 化；该文件其余 json.Marshal 去向均为 Redis cache（cacheRequest/cacheConfig，store.go:633/666），[]byte 合法。
- **F9 守卫无生产误伤**：`mapFinishReasonToGemini("")` 确经 default 臂合成 "STOP"（response.go:1396-1397），翻转后跨协议空 FinishReason 省略字段（response.go:1318-1327）；三路流式 chunk 生产者均设 SourceProtocol（stream.go:222/282/299 OpenAI、:482 Anthropic、parse_gemini_stream.go:53/101 Gemini）→ 同协议直通不受新守卫影响；gemini parse 双写 StopReason+FinishReason（parse_gemini_stream.go:233-235）→ 守卫后终态经映射臂保留、终帧不丢（跨协议 chunk 若 FinishReason 非空走 else-if，不落 ok=false）；streaming 侧守卫与非流式既有守卫（response.go:760）同型一致；三案钉桩（response_stopreason_test.go:153-217）。
- **F8 回退正确**：剥噪空且原文非空才回退 TrimSpace 原文，仍受 200 rune 截断（session_writer_v2.go:1047-1051）；全白空原文行为不变；双案钉桩（session_writer_summary_test.go:44-62）。
- **F10 全部与代码事实互证通过**：helpers.go 新契约（prefs 打头不要求在 tierPlan 内、tierPlan 空→nil、去重/空串跳过）与 withRoleFailoverHead 实现（helpers.go:22-52）一致；注释声称的两个调用站点均在 decision_v2.go:329/348；V1 decision.go:626 走 promoteFirstPresent 属实；.env.example:444 新指向 sql/migrations/startup/752 存在；probe_necessity.go:67 占位符写实。

**存量债盘点可复现命令**（供主代理 L4 重新核数）：
- 主口径：`grep -rn "err\.Error()" admin/ --include=*.go | grep -v _test | grep "StatusInternalServerError"` → 395 行/77 文件
- Sprintf 变体：`grep -rn "StatusInternalServerError" admin/ --include=*.go | grep -v _test | grep -E '%v", err\)|%s", err\)'` → 55
- http.Error 变体：`grep -rn "http\.Error(" admin/ --include=*.go | grep -v _test | grep "err\.Error()"` → 26（其中 20 与主口径重叠）
- 502/503：同线 err.Error() + StatusBadGateway/ServiceUnavailable → 12

## 三、未覆盖项与原因

- **三门验证（go build/vet/test）未在本子代理执行** —— 只读纪律约束（构建会写 GOCACHE），依赖主代理收尾跑；commit message 自述全绿（4 个基线既有失败 stash 对照）。
- **F3 真库回归**（两轮聚合后 primary_request_id==第一轮）—— pgxmock 测不出 SQL 语义，R71 已登记为 L5 遗留，本轮维持。
- **center CreateCommand/RecordHeartbeat 炸点未真库实捕** —— 无真机凭据；SimpleProtocol 行为依据 db/db.go:72 + 仓内 5 处同根注释（R11 FIX-C 先例）推断，登记前建议主代理在真库以 `SELECT '\x7b22…'::jsonb` 复演一次。
- **77 文件 500 回显清单为 grep 计数**，未逐行亲读（conventions §5-3 纪律：登记为正式发现前主代理需抽样亲读；top 15 文件已附典型行号）。
- **窗口内 turn-logs TTL/FIFO 子任务**（8a34eab76/14d34867f/5b558deab、753 迁移及测试）非本路分工，未审。
