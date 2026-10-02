# 路由/预算/会话分析组子代理报告（窗口 e3406f9e2..77837b013 + 24h 补漏）

窗口相关代码承载提交：e3406f9e2（budgetCheck 收口）、7f24fb5af（R52 逐层回退）、03ce23e18（R78 幽灵错误码）、3483152cb（脱敏+代际）、ce2f67992（会话分析夹具）、77837b013（reasoning 修复）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 | 处置 |
|---|---|---|---|---|
| F1 | P1候选 | 预算闸门执行面未随 e3406f9e2 收口：CheckBudget 非 BudgetExceededError（含 DB 宕机）被两个调用点吞掉继续放行——R89-133 缺陷 B 在热路径的原样存在；verifier.go:758-760 注释自述 best-effort 属半文档化设计，与"收口"宣称口径分叉 | handler.go:2386-2392 / embeddings.go:399-404 / verifier.go:794-796 | 主代理定级：至少登记"e3406f9e2 只修了观测面"（本轮已登记，执行面语义留 Owner） |
| F2 | P2候选 | R52 兜底层对 tier 硬过滤豁免=5 个重量池模型免疫 tier 计划（刻意+默认关+可 opt-out，有界） | decision.go:626-637 等 | 接受为已声明取舍；可用 auto_decision->>'role_fallback_layer' 建告警 |
| F3 | P2候选 | 预算无原子扣减、无在途切断（结构性设计非本窗口回归） | verifier.go:766-802 | 登记 Owner 拍板 |
| F4 | P3候选 | 执行面裸 SUM vs 观测面 DISTINCT ON 去重口径分叉（promote 双份会双计→提前 402 误伤） | keys.go:978-984 vs verifier.go:786 | 本轮已对齐（DISTINCT ON） |
| F5 | P2候选 | budgetCheck 租户硬编码 tenant_id='default' 未修（e3406f9e2 自述缺陷 D 留 Owner） | keys.go:962-968 | 待 Owner（注意 tenant_admin 鉴权面） |
| F6 | P2候选 | fresh-install session_dim.status/created_at NOT NULL 无默认（350 从未注册，805 建列不带默认；今天不炸：仅有的两条 INSERT 都显式传 'active'） | session_analytics_realdb_test.go:38-66 / runner.go:288-293 | 与 §9-6/§9-7 拍板合并 |
| F7 | P3候选 | 遗留#3 核查：reorder 成功分支确实 fail-closed 且已断言审计落库；"行空"唯一代码机制=h.logAudit 失败仅 slog.Debug；同一张表两种失败契约并存 | routing.go:104-107,1562-1566 | 本轮已修（Debug→Warn+actor）；统一契约留 Owner |
| F8 | P3候选 | NeverWorse 回滚分支 OutboundBody 记录压缩器输出而非实际发出 body（存量行为） | handler.go:3936-3947 | 登记观察 |
| F9 | P3候选 | Responses 未知 item 类型默认直通（allowlist 固有残留） | input_protocols.go default | 与核心组 F2 同项，已部分收口 |
| F10 | P4 | goal_control 两处小疵 + LLM_GATEWAY_SANITIZE_OUTPUT_ACTION 未进 envs.samples | goal_control.go:560-577 | envs.samples 本轮已补；其余登记 |

## 二、核实为健康的面
- R52 逐层回退：resolveRolePrefs V1/V2 单点；layerOf 索引比较；跨层去重；nil/空串/全空白处理；兜底只命中候选池内模型（不绕熔断）；RoleFallbackLayer 随 CachedIntent 跨轮复用且 11 用例钉死；V1 CacheReused 顺带修真实
- 预算观测面：usage_ledger_with_current_month=hot∪全部分区（非仅当月）+DISTINCT ON+fail-closed 500；三条钉测承重；budget NULL=不设闸语义正确
- 多轮会话双向解析：sanitizeGenerationMatches 严格相等防字典重建串值；会话 key=(tenant,session)+tenantHash；NeverWorse 回滚基线修正后保留拼装后完整脱敏历史；HSET 全字段覆写杜绝旧元数据复活
- auto 幽灵错误码 auto_route_unavailable→auto_route_decider_failed 订正真实
- goal_control：Redis-nil 不再使 guard 整体缺席；防双装；链序 gate→restore 与 ParsePlaceholder 分支自洽

## 三、未覆盖项与原因
12 条门未实际执行；streaming handoff/survival 线不在分派焦点；sessionv2mirror R77/R88 仅清单核对；真库视图形态以迁移推断；tierFailover 复合行为未重推演。
