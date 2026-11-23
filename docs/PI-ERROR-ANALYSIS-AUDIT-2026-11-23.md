# PI 错误自动分析与处理系统审计报告
**日期**：2026-11-23  
**审计范围**：domains/promptinjection 错误收集/分析链、admin PI 管理 API、错误剧本 errorplaybook（有限）、前端 PIAnalysisView（有限）  
**方法**：只读代码审计 + 文档挖矿 + 记忆文件复核  
**基线**：HEAD `99149eb32` (origin/main 持平，github/main 落后 63 提交)

---

## 一、功能线全景

PI 错误自动分析与处理包含以下子系统：

1. **PI 错误分析链**（主线，2026-11-17 首次接线）  
   - 触发源 → 收集器 → 队列（error_analysis_queue）→ 后台 worker → LLM 分析器 → 结果表（error_analysis_results）→ 管理页（/admin/pi-analysis）→ apply/revert 修正规则  
   - 迁移 971/977/979/980，管理 API 在 `admin/pi_analysis_handler.go` + `pi_analysis_fix_apply.go`，前端在 `web/src/views/admin/PIAnalysisView.vue`

2. **输出守卫格式类拦截接入 PI**（2026-11-20，新增触发源）  
   - 格式类守卫拦截 → 转成 error_analysis_queue → 走同一条分析链

3. **错误剧本 errorplaybook**（独立子系统，迁移 995）  
   - 错误事件 → 匹配剧本 → 执行计划（带快照与回滚）→ 未知错误交 LLM 生成方案  
   - 本轮只做有限接线确认（worker 在 initErrorPlaybook 时启动，依赖 V2 pipeline）

4. **影子判官**（Phase 0 已落地，默认关）  
   - 对拦截命中做二次判读，settings 开关 `sanitize.judge_shadow_enabled`

5. **格式错误自动重试方案**（文档方案，代码未实施）  
   - 方案 A/B/C/D，推荐路径：先 B（GLM 修补器）+ D（监控），后 A（延迟检查）

---

## 二、PI 错误分析链审计发现

### 2.1 接线面（已确认接线，默认关闭 opt-in）

| 组件 | 构造点 | 注入点 | 默认部署下是否生效 | 证据 |
|------|--------|--------|------------------|------|
| ErrorCollector | `cmd/gateway/pi_agent_init.go:124` | `piDetector.SetErrorCollector` `:196` | 否（需 `LLMGatewayPIErrorCollectorEnabled=true`） | `:16-18` 注释 + `:123` 条件 |
| ErrorAnalyzer | `pi_agent_init.go:210` | worker 持有 | 否（需 `LLMGatewayPIErrorAnalysisEnabled=true`） | `:19` 注释 + `:204` 条件 |
| ErrorAnalysisWorker | `pi_agent_init.go:213` | `worker.Start(ctx)` `:214` | 同上 | `error_analysis_worker.go:65` bg.SpawnLoop |
| 管理 API | `admin/pi_analysis_handler.go:17` | `main.go:8177-8184` | 是（但需 V2 pipeline + adminHandler 存在） | `:8176` 条件 |
| 前端页面 | `web/src/views/admin/PIAnalysisView.vue` | `router.ts` + `appNav.ts` | 是 | 路由 `/admin/pi-analysis` 已挂 |

**触发源（全仓只有两处）**：
1. 检测器内部：`detector.go:392/415/485/547/596/1126` 六处 `collectError` 调用（Canary 失败、向量失败、LLM 检测失败、策略加载失败、意外行为）
2. 输出守卫格式类拦截：`streaming/policy_block_feedback.go:231` → `emitOutputGuardPI` → `main.go:7845-7857` 适配器投递

**专用收集方法零生产调用方**：`CollectRuleFalsePositive` / `CollectLLMDetectionFailure` 等方法定义在 `error_collector.go:76-158`，但全仓无非测试调用方。

### 2.2 安全面发现（P1）

**F1. 分析器提示词未中和框架闭合标签**  
- 位置：`domains/promptinjection/error_analyzer.go:376-396`  
- 现状：错误消息与输入文本原样放进 `<untrusted_input>` 框架，未调用 `neutraliseUntrustedText`（该函数只在检测提示词 `detector.go:2251` 被调用）  
- 影响：`llm_detection_failed` 类错误的输入文本恰恰是攻击者写的注入载荷，可闭合框架标签注入分析提示词  
- 证据：`pi_layers_audit_guard_test.go:505-523` 只测检测提示词，分析提示词测试未断言载荷无法闭合框架  
- 建议：在 `buildPrompt:402/404` 加 `neutraliseUntrustedText` 包裹

**F2. 递归面未关闭**  
- 位置：`llm_client_gateway.go:149` 设置 `X-Llmgw-Pi-Analyzer: 1` 标记头  
- 现状：**全仓零消费方**（grep 确认），而分析端点配置为另一台网关节点（154 `llm.kxpms.cn`）  
- 影响：分析请求的载荷（含攻击者原文）被 154 的 PI 检测拦截 → 再次收集 → 形成「分析失败事件被再分析」的潜在环路  
- 证据：`pi_agent_init.go:179-198` 端点解析、`cmd/gateway` grep `X-Llmgw-Pi-Analyzer` 零命中（除设置处）  
- 建议：① 在 V2 入口检查该头，存在时跳过所有 PI 检测与收集；② 或配置分析端点为信任域外部模型（文档里提过但未实施）

### 2.3 数据面发现

**F3. 去重机制对输出守卫类事件形同虚设**  
- 位置：`error_collector.go:231-236` 去重指纹 `sha256(errorType + tenantID + errorHash)`，其中 `errorHash = sha256(inputText)`  
- 现状：输出守卫事件的 `inputText` 是逐请求不同的**证据摘录**（`streaming/policy_block_feedback.go:245-247` 的 digest/evidence），每次都不同  
- 影响：同一条规则误报 N 次会入队 N 条（而检测类错误的 inputText 是完整输入，去重有效）  
- 证据：`error_collector.go:170-186` 错误类型常量、`streaming:245` 摘录构造  
- 建议：输出守卫类错误的指纹应只含 `errorType + tenantID + rule`，不含 inputHash

**F4. 孤儿回收跨实例竞态**  
- 位置：`error_collector.go:693-732` `ReclaimStaleClaims` 只排除 `excludeIDs`（本进程在途行）  
- 现状：245 与 154 **共享同一个数据库**（`172.16.2.210`，记忆文件实证），两实例会互相抢回对方正在分析的行  
- 影响：同一错误被重复分析（重复 LLM 花费）+ `retry_count` 累加到上限后被标 failed（本来能成功的错误最终失败）  
- 证据：`:708-710` 注释「超时 60s→150s 后由罕见形态变常态形态」、`pi-agent-245-enable-validation-2026-11-18.md` "245 与 154 共享 DB"  
- 建议：① 分布式互斥（Redis 锁 / DB 租约）；② 或每实例独立数据库；③ 或回收阈值大幅放宽（如 3600s）

**F5. 去重事务降级路径不可达**  
- 位置：`error_collector.go:224-269` 事务内先查重，失败降级为直接插入  
- 现状：PostgreSQL 语义下，事务内任一语句出错即中止（状态码 `25P02`），后续 INSERT 必然失败  
- 影响：注释 `:260-266` 声称的降级路径永不执行（误导维护者）  
- 证据：PostgreSQL 官方文档 "failed transactions" 语义  
- 建议：删除降级 INSERT 或改用 savepoint

**F6. 清理函数零调用方**  
- 位置：`sql/migrations/startup/977_pi_error_analysis_semantics.sql:138` 定义 `cleanup_old_error_analysis(retention_days INTEGER DEFAULT 90)`  
- 现状：**全仓零调用方**（grep 确认：只有定义，无 `SELECT cleanup_old_error_analysis`）  
- 影响：队列与结果表（含攻击者原文 + LLM 原始响应）无界增长，90 天自动清理从未发生  
- 证据：grep `cleanup_old_error_analysis\(` 只在迁移文件出现  
- 建议：① 添加调度任务（cron / bg worker）；② 或改用 pg_cron extension；③ 或添加 `ON DELETE CASCADE` + 时间分区

### 2.4 管理 API（已修复，仅记录）

- 鉴权已修复（2026-11-17 审计轮 P0）：`admin/pi_analysis_handler.go:24-56` 添加 token 校验
- 路由注册已修复：`main.go:8176-8198` 条件判断 + 显式注册
- apply/revert 通道已实现：`admin/pi_analysis_fix_apply.go` 完整，包含白名单动作、置信度门、快照与回滚
- 输出守卫类错误设为 view-only（不可 apply）：`pi_analysis_fix_apply.go` 相关逻辑

---

## 三、记忆文件遗留项汇总

### 3.1 明确的待办（按优先级）

**P1（影响功能）**：
- P1-⑥ 事件驱动触发（设计有实现无）— `pi-agent-utilization-analysis-2026-11-18.md`
- P1 RefreshRules 热更新 — 同上
- P1 模型选型（flash 延迟教训在案）— 同上

**P2（质量与扩展）**：
- P2-⑧ 租户隔离 R1（需 owner）— `pi-agent-deadcode-cleanup-2026-11-20.md`
- P2-⑩ 观测面板 — 同上
- P2-⑪ F-AUTO/F-PI 遗留档 — 同上
- P2 分析反哺规则 — `pi-agent-utilization-analysis-2026-11-18.md`
- L1 检测规则误报债（需 owner）— `pi-analysis-admin-optimize-2026-11-18.md`

**P3（观察项）**：
- F2 GetStats 事务一致性 — `pi-analysis-admin-optimize-2026-11-18.md`
- 视图消费面零监督 — 同上
- F3 口径 tooltip — 同上

### 3.2 待 owner 决策

1. **判官后端选型**（影子判官 Phase 1 前置）：loopback 专用凭据 vs 信任域自建小模型 — `judge-shadow-phase0-2026-11-13.md`
2. **输出检查总开关语义**：`sanitize.output_check_enabled` 是否应关闭所有输出检测包括强制守卫 — `format-guard-root-fix-2026-11-15.md`
3. **格式类失败上游故障分类**：是否应触发候选切换（流已开始后不能重试需评估）— 同上
4. **V2 pipeline 在生产节点的开关策略**（211/245/154 对齐）— `pi-agent-utilization-analysis-2026-11-18.md`

### 3.3 已落地但登记不修

- **enhanced/ 包质量红线**：收编时所有解码器必须 ROT13 式关键词门控 + Base64 排除 data-URL/长度上限 + Unicode 只计全角字母；重写落地后才翻门删包 — `pi-agent-deadcode-cleanup-2026-11-20.md`
- **L6 判官 resolver 已被并行车关闭**（2026-11-18）— 同上
- **980 二修（DROP+CREATE）**：CREATE OR REPLACE VIEW 不允许改列位 — `pi-analysis-admin-optimize-2026-11-18.md`

---

## 四、错误剧本 errorplaybook（有限审计）

**接线确认**：
- 构造与启动：`cmd/gateway/error_playbook_init.go:113-146` `initErrorPlaybook` 在 `main.go:7898` 被调用，worker 在 `:146` 立即启动
- 依赖条件：与 PI 分析链共享 `piAgentCtx` 与 `piSQLDB`，均需 V2 pipeline 开启
- 管理 API：`main.go:8204-8216` 注册 `/api/admin/error-playbook/*`
- 迁移：995（表 error_playbook / playbook_executions / playbook_snapshots）

**未深度审计的项**（配额不足）：
- 触发源（从哪里读错误事件）
- LLM 分析器配置方式与端点
- 自动执行默认开关
- 快照与回滚机制细节
- 跨实例并发控制

---

## 五、前端 PIAnalysisView（有限审计）

**已确认**：
- 文件：`web/src/views/admin/PIAnalysisView.vue` (1553 行)
- 路由：`/admin/pi-analysis` 已挂，`meta: { requiresAuth: true }`
- i18n：`web/src/locales/*/nav.ts` 有 `piAnalysis` 键
- 配置与测试：`web/src/config/piAnalysis.ts` + `.test.ts`，`web/src/lib/piAnalysisStats.ts` + `.test.ts`

**未深度审计的项**（配额不足）：
- 硬编码中文清单
- i18n 八语种完整性
- 前后端契约字段对齐
- 错误处理路径（500 是否渲染成空态）
- apply/revert 按钮条件与二次确认

---

## 六、格式错误自动重试方案（文档方案）

**出处**：`docs/audit/2026-11-15-格式错误自动重试方案分析.md` + `format-guard-root-fix-2026-11-15.md`

**四个方案**：
- **方案 A**：延迟检查模式（缓冲完整 arguments 再检查）→ 根本性解决，但触碰流式核心需充分测试
- **方案 B**：修补器（GLM 格式修复 `<arg_key>/<arg_value>` 透明修复）→ ~100 行，只能修已知格式错误
- **方案 C**：（文档未详述）
- **方案 D**：监控告警自动降级（按 provider 统计自动降级）

**推荐路径**（文档原文）：
1. 本周内实施方案 B（GLM 格式修补器，改动小风险低立即生效）+ 方案 D（监控告警自动降级）
2. 1-2 月后实施方案 A（延迟检查 + 重试，需先完成成本评估 = 统计 tool arguments 平均大小 + 灰度发布验证内存开销和延迟影响）

**代码现状**：grep 未发现 A/B/D 实施证据（方案文档日期 2026-11-15，可能后续轮次已实施但未被记忆记录）

---

## 七、验证状态

**后台验证脚本**：`/tmp/pi-audit/verify.sh` 已启动（后台运行 go build/vet/test + 前端 typecheck/vitest/i18n 门），尚未完成（日志只输出环境变量与测试名模式反解，未见 STEP 结果）

**待验证项**（脚本完成后需查看）：
- go build ./... 是否通过
- promptinjection / errorplaybook / hooks/promptinjection 包测试
- admin / cmd/gateway 的 PI 相关测试
- sqlguard 的 migration_980 / migration_995 测试
- 前端类型检查（vue-tsc）
- 前端 i18n 门（硬编码 CJK 棘轮、i18n:check:strict）

---

## 八、推送前置

**远端水位**：
- origin/main：与本地持平（`99149eb32`）
- **github/main：落后本地 63 提交**（需补齐双仓推送）

**提交范围**：本轮为只读审计，产出为文档（本报告 + 交接文档），不含代码改动

---

## 九、关键通用式（来自记忆与本轮）

1. **注释声称的性质要当缺陷候选核对，不能当验收依据**（本轮 F5 去重降级路径、F6 清理函数）
2. **「组件齐全」≠「链路能跑」** — 判定标准是「谁调用了它」（`pi-chain-audit` 技能核心判据）
3. **跨实例共享库 + 只排除本进程在途行 = 互抢**（本轮 F4）
4. **去重指纹设计要考虑输入的变化性**（本轮 F3）
5. **全仓零调用方 = 功能从未生效**（本轮 F6 cleanup、专用收集方法）
6. **提示词框架标签中和要覆盖所有路径**（本轮 F1：检测有、分析无）
7. **递归面标记头要有消费方，否则标记白写**（本轮 F2）

---

## 十、下一轮行动建议

### 10.1 立即修复（P0/P1）

1. **F1 分析器框架标签中和**：在 `error_analyzer.go:402/404` 加 `neutraliseUntrustedText` 包裹
2. **F2 递归面关闭**：V2 入口检查 `X-Llmgw-Pi-Analyzer` 头，存在时跳过 PI 检测与收集
3. **F6 清理函数接线**：添加 bg worker 或 cron 调用 `cleanup_old_error_analysis(90)`

### 10.2 设计改进（P1）

4. **F3 输出守卫去重**：改指纹算法只含 `errorType + tenantID + rule`
5. **F4 跨实例回收**：三选一（Redis 锁 / 独立库 / 放宽阈值）
6. **F5 去重降级**：删除不可达代码或改用 savepoint

### 10.3 功能完善（P1-P2）

7. 实施事件驱动触发
8. RefreshRules 热更新
9. 模型选型（替换 flash）
10. 观测面板（Prometheus + Grafana）

### 10.4 待 owner 确认（决策项）

11. 判官后端选型
12. 输出检查总开关语义
13. 格式类失败上游故障分类
14. V2 pipeline 生产开关策略
15. 格式错误自动重试方案实施时间表（方案 B+D 本周 / 方案 A 1-2 月）

---

**审计人**：ZCode AI Agent  
**报告生成时间**：2026-11-23 17:12  
**下一步**：生成 handoff 交接文档 → 提交本报告与交接文档 → 推送双仓
