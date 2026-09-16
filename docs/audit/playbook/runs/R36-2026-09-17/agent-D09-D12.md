# D09（全局节点状态统一与自检）+D12（海外出口代理）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论见轮文档 §一（#1 经复核降级 P2：成功路径有 MarkNodeProbeHealthy 同步，黑洞仅失败路径）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P1→P2 | 313d1ebc8 漏切"结果黑洞"：供应商页"立即测试"走 legacy TriggerManual（真实计费调用）；子代理称结果写旧表无人读——主代理复核：成功路径 TriggerManual/TriggerAllSync 均同步 MarkNodeProbeHealthy（model_probe.go:1352-1360/1464-1480），**失败路径**确为黑洞（recordRun 写 model_probe_runs_hot、node_probe_runs 无行、node_probe_state 不更新，UI 任何处无痕迹） | admin/providers.go:453、admin/probe_history.go:229、bg/model_probe.go:1247,1024 | trigger 端点接 probeSubmitter（**已修 R36**） |
| 2 | P2 | 可用性缓存回填以冻结旧表为源：新模式下旧表停更 → 周期任务持续把冻结状态刷进 llmgw:avail | bg/model_availability_backfill.go:173；admin/probe_dashboard.go:2249 | 切 v_node_probe_state_compat（**已修 R36**） |
| 3 | P2 | 恢复守卫读旧表 + reviver 无门控改旧表：credential_recovery 守卫读冻结 model_probe_state；BrokenProbeReviver main.go 无条件启动把冻结 stuck 翻成 recovering | bg/credential_recovery.go:597；bg/broken_probe_reviver.go:68-73；main.go:3725-3726 | reviver 加门控或改读新表（登记 R36） |
| 4 | P2 | diagnostics 两端点只重置旧系统：v_routable 的 node_probe_failed 门未清，路由仍剔除（force_enable 是双清正确样板） | admin/diagnostics_credential.go:259、diagnostics_routing.go:300；对照 routing.go:5497-5567 | 补 node_probe_state 重置（**已修 R36**） |
| 5 | P2 | HK 默认排除只覆盖新建订阅，存量订阅仍"全地域"；无存量回填 | admin/proxy.go:379-385；proxy/manager.go:373 | 登记（需产品确认） |
| 6 | P3 | 回滚开关单向门：LLM_GATEWAY_USE_NEW_PROBE_MODE=false 后 legacy 只写旧表，已切展示面回冻结，v_routable 失去维护者 | main_helpers.go:255-271；model_probe.go:1024；v_routable_credential_models.sql:19 | 文档明示回滚不可用（登记） |
| 7 | P3 | 兼容投影统计语义漂移：total_attempts 从累计变当前连续段之和；'probing' 含从未探测占位行 | db/probe_views_unified.go:56,:21 | 声明口径或补真实累计列（登记） |
| 8 | P3 | node_probe_runs 派生 status 永不产出 skipped（旧 API 契约含该值→恒空） | admin/probe_history.go:55-62,:71-73 | 确认落点后补映射（登记） |
| 9 | P3 | ensure 兜底失败半径扩大：compat 视图依赖 341 建的 node_probe_state，未跑 341 的存量库整批 DROP+CREATE 失败 | db/db.go:4083-4120 | 加存在性守卫（登记） |
| 10 | P3（运维注意） | parser 默认 client 补 ProxyFromEnvironment：全局设 HTTP(S)_PROXY 后国内机场订阅也绕行 | proxy/parser.go:63-68 | 部署文档登记 |
| 11 | P3 | model-toggle 手动上线不清 last_err_code='manual_offline'（展示面残留 stale 错误码） | admin/credential_monitor.go:1132-1143,:1191-1201 | 上线分支补清空（**已修 R36**） |

## 二、核实为健康的面
- bans 回放不会把"主动清空"回放回旧值（parser 不产出节点 bans、唯一来源 PUT API、空数组不入捕获表、捕获与重插同事务）
- 双代理冲突不存在（唯一 parser 构造点传 nil client）
- 订阅级 HK 默认与既有数据兼容（更新用 *[]string nil=不改）；订阅级+节点级 bans 取并集
- errorsx 与 proxy 交互遗留（R35-gap #7/#1）未被触碰
- 窗口未新增后台 worker；被改 worker 均有守卫；node_probe_state 全列 timestamptz 无时区风险
- 视图列契约与派生规则同源（SELECT * 位置 Scan 安全 + 列序钉桩）；heatmap routable 与 v_routable 等价
- real24 聚合跨月不丢数；node_probe_runs 索引齐；reconcileHealthyConfirmedBindings 有门控
