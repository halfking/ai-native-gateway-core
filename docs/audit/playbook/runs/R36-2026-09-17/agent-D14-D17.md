# D14（安全）+D15（可观测性与 UX）+D16（流程闭环）+D17（代码卫生）子代理报告（窗口 643735a28..876302d5e）

> 只读审计，主代理已亲读复核。复核结论见轮文档 §一。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | /api/system/bootstrap/activate 的 FREE- 指纹校验时序倒置：Activate（占座）先执行、指纹 403 后执行且不回滚 → 异机 hash 占掉唯一席位，真机后续撞 409 need_deactivate（R30 L-1 占座 DoS 经 /activate 部分复活）；指纹不可用路径静默跳过无 Warn | licensing/bootstrap_api.go:205（Activate）→:237-247（校验在后）；:298-301（activate-quick 先校验+Warn） | 校验前移+补 Warn（**已修 R36**） |
| 2 | P3 | CredentialHeatmapView 6 个中文串硬编码未入 i18n（313d1ebc8 引入），CJK 棘轮存量+6；该文件 0 处 t() 使用（存量债） | web/src/views/CredentialHeatmapView.vue:268-275,:706 | 入 locale 或登记 D15 存量债（登记） |
| 3 | P3 | FreeDiscoveryView.autoDisabledHint 英文硬编码，同文件其余走 t()——风格不一致 | web/src/views/FreeDiscoveryView.vue:398-404 | 入 locale key（登记） |
| 4 | P3 | 6 个 UNUSED 文件头标注登记指针写 §五，实际在 §四 | durable_contract.go:1-2 等 6 文件 | 批量修正（**已修 R36**） |
| 5 | P3（备忘） | 6 个 UNUSED 文件窗口内被 45054170c 改动但内容仅为标注本身+gofmt，无行为变更——标注无需重新评估 | git show 45054170c | 无需动作（后被 D04#2/#3 推翻两文件的措辞，R36 已修正） |

## 二、核实为健康的面
- P1-2 附件列表租户谓词覆盖完整（唯一调用方 admin/logs.go:346；tenant_admin 传 GetTenantID、谓词不命中返回同"无附件"空数组）
- P2-3 strict 翻转钉桩改对（attachment_pipeline.go:18 + TestAttachmentStrictMode）
- R34 P1-3/P1-4 闭环在位（activate-quick 409 契约 / activate Success 检查）
- ops 脚本三闸未回退且无密钥落盘（生效配置闸/密钥前置闸/reload 硬失败回滚；pw.txt gitignore+mktemp+trap）
- Web 类型与后端 JSON 对齐（HeatmapNodeStatus 9 字段/state 词汇同源；freediscovery 三字段；routeIncident pending；probe state 枚举扩 suspicious/probing）
- /admin/usage 补 requiresSuper 后路由 meta 与相邻一致
- goal 影子轮 R35 修复仍在位（Path 1.5 让位/UseAudit 告警/孤儿注释清理）
- D17 五条零读者抽检全部成立（injectPlaceholderProtection/CompareWithRawLog/MaxBodySize/IsDoubaoCatalog/dashboard_degrade 两函数）

## 四、F3 取证（附件下载端点归属校验现状与插入点）
- 路由与闸门：/api/attachments/ 注册于 admin mux 经 JWT/admin-key 中间件（admin/handler.go:1244）；attachments.Handler 自身 authenticator 仅 apikey 模式装配（main.go:2985），其余模式 admin JWT 是唯一闸门而 TenantID 从未被读
- 当前 TODO：domains/attachments/handler.go:77
- 下载 URL：relPath=TrimPrefix(Path,"/api/attachments/")；新写入内容寻址 2026/07/a1/b2/<sha256>.png（无 request_id 无租户）；历史 req_<requestID>/ 仅读兼容
- 触发路径：tenant_admin 枚举/泄漏获得他人租户 relPath → GET 下载成功
- 最小插入点（首选）：admin/attachments_routes.go 委托前，GetAuthContext/GetTenantID 可用且 admin 包可查 DB；新布局按 relPath/hash 对 request_attachments→request_logs 租户联查；历史路径走 request_id 快路径（**主代理按首选落地 R36**）
- 次选：domains/attachments/handler.go:87-90 之间，需把租户身份传入

## 五、RFC 意图基线摘要
- FEATURE-REQ-auto-multidim-progress-and-pricing-presets.md（v2）：阶段 0=方案+审计文档已完成；零 migration/零新表/零新 Redis 连接/AUTO_USE_SESSION_HEALTH=false 默认关；阶段 1-6 文件清单 autoroute/session_health.go、outcome_feedback.go、progress_decision.go、decision_v2.go、auto_route.go、admin/pricing_presets.go、web PricingPresetBar
- AUDIT 复用分析：70% 组件可复用（goal loop_detector/cost_presets、outcome_feedback、session_intent_cache、decision_trace），~700→~370 行
- 落地核查：确为纯方案无对应代码（autoroute/session_health.go、progress_decision.go、admin/pricing_presets.go 不存在；grep AUTO_USE_SESSION_HEALTH/pricing/presets/PricingPresetBar/health_dims/switched_from 零命中）——无"方案先行代码未跟上"漂移
- 窗口边界更正：两份 RFC 提交（3a7121e78/fd082e652）在窗口 base 之前紧邻处，非窗口内新增
