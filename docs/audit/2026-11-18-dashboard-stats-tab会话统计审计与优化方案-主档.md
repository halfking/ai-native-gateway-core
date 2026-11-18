# /dashboard?tab=stats 会话统计功能审计与优化方案（2026-11-18）

> 状态：审计完成，优化方案待 owner 拍板
> 审计对象：`https://llm.itestu.cn/dashboard?tab=stats`（线上版本 `2.6.1-cce2c6bb-20261117-211`，build_seq 3097）
> 代码基线：`origin/main` @ `94d736c62`（拉取时本地与远端 0/0，已同步）
> 方法：前端组件链 → 后端 SQL 读面 → session_summaries 写面 → 线上 API 实测交叉验证 → ASM 文档对照

---

## 一、统计链路盘点（页面到底在读什么）

页面路由 `/dashboard` → `HomeView` → `DashboardViewV2.vue`，`tab=stats` 渲染两个组件：

- `web/src/components/SessionStatsPanel.vue`（KPI/趋势/健康/合规/成本/排行）
- `web/src/components/session/SessionDrilldownPanel.vue`（下钻）

`SessionStatsPanel` 经 `useDashboard` 并行拉 7 个 API（`web/src/api/dashboard.ts`）：

| API | 后端实现 | 数据源表 |
|---|---|---|
| session-overview | `admin/dashboardapi/session_overview.go` | session_summaries（+session_dim、session_turns 族） |
| session-trend | `admin/dashboardapi/session_trend.go` | session_summaries |
| session-health | `admin/dashboardapi/session_health.go` | session_summaries |
| session-active / session-recent | session_active.go 等 | session_summaries |
| **errors** | `admin/dashboardapi/errors.go` | **session_module_executions_hot** |
| **performance** | `admin/dashboardapi/performance.go` | **session_module_executions_hot** |
| **module-stats** | `admin/dashboardapi/module_stats.go` | **session_module_executions_hot** |

写面：`session_summaries` 的聚合列由 `internal/sessionv2mirror/session_summary_dim.go` 的 `SessionSummaryDimWriter` 逐终态请求增量 upsert（排除内部回环与探针合成会话）；`health_score/grade` 由 `bg/session_health_worker.go` 每小时批 100 条补算。

---

## 二、统计分析的标准（现行口径总结）

当前实现事实上采用了以下口径（多为隐式约定，未成文）：

1. **会话宇宙**：`session_summaries` 一行 = 一个 gw_session_id；探针合成会话、内部回环（标题/摘要自动请求）被排除。
2. **窗口语义**：所有 days 窗口按 `first_request_at >= NOW() - days` 过滤——**会话入队制**（cohort by first request），不是按请求发生时间。
3. **active**：`last_request_at >= NOW() - 24h`（当前状态快照）。
4. **closed**：`last_request_at < NOW()-24h AND first_request_at < NOW()-24h`（= 窗口内会话按“此刻是否活跃”的二分，与 active 恰好互补）。
5. **成本**：`SUM(total_cost_usd)`；input/output 按 token 数 50/50 比例拆分（写入时 `prompt/(prompt+completion)`）。
6. **健康分**：后台 worker 从 error_count/latency/model_switch/compliance_issues 等字段扣分制计算，A≥90/B≥75/C≥60/D≥40/F<40。
7. **合规**：`compliance_status` 列，默认 `'compliant'`。
8. **模块执行/错误/性能**：`session_module_executions_hot`（带 TTL 的缓存表）。
9. **turns 对拍**：`SUM(request_count)` vs `session_turns 族 COUNT(*)`（ts 窗口、排探针），偏差 >10% 且 >100 判 degraded。

ASM 侧（ai-session-manager）标准对照：ASM 是**投影消费方**——网关是 canonical session/message 事实源，ASM 经事件流入维护 `session_projection` + analytics rollup，其 `/api/v1/analytics/dashboard` 按租户 RLS 从自己的投影库聚合，且明确「projection-first 读路径未过 ownership gate 不得切 reader」（ASM README「当前边界」）。**两套统计各自为政，口径从未对齐**：网关按 first_request_at 队列窗口，ASM analytics 按自己投影的事件时间；这正是“数据不准确”感受的结构性来源之一。

---

## 三、实测证据（llm.itestu.cn，2026-11-18 上午）

### F1（P0·读面 SQL）model_usage 成本重复计入，Σ模型成本 ≠ 总成本

`queryModelUsage` 先 `unnest(models_used)` 再对每行复制全量 `total_cost_usd`——**一个用过 2 个模型的会话，其成本被计入 2 个模型各一份**。

- 7d：Σtop10 模型成本 = **$9,757.18** vs KPI 总成本 **$5,710.14**（膨胀 **+70.9%**）
- 30d：$10,971.08 vs $6,828.72（膨胀 +60.7%）

明细可见连锁失真：`claude-opus-5-5` 一个模型 $3,420 就占 KPI 总成本的 60%——排行与“模型成本占比”完全不可信。同病根：`z-ai/glm-5.3-flash` 与 `glm-5.3-flash` 因**带不带 provider 前缀被当成两个模型**分列两行（$533+$758），`OutboundModel` 未归一。

### F2（P0·写面缺失）errors / performance / module-stats 三个面板恒零

三端点全部读 `session_module_executions_hot`，而该表的唯一写方 `moduleexec.Executor` **在 `cmd/gateway/main.go` 从未装配**（全仓 `domains/moduleexec` 的 import 方只有 4 个 hook 文件与 bg worker，装配点 grep 0 命中；inspector/audit/security 三个 hook 的 `SetExecutor` 无人调用 ⇒ `h.executor == nil` ⇒ 直接走 direct 分支不落表）。

实测：`errors?days=7` → `total_errors=0, total_requests=0`；`performance?days=7` → 全零；`module-stats?days=7` → `total_modules=0`。

**自相矛盾**：同一页面 KPI 区 `model_usage.success_rate` 显示 95.7%~99.5%（7d 推算失败请求上千），而“信号”区错误率恒 0%、错误数恒 0。owner 感知的“数据不准确”最直观来源。

### F3（P0·写面缺失）合规面板恒 100% 假绿

`compliance_status` 列 `DEFAULT 'compliant'`，`prompt_injection_detected/pii_detected/toxic_output_detected` 默认 false——全仓**无任何生产写方**（只有测试夹具 INSERT）。实测 7d/30d 均 `compliance_rate=100, warning=0, violation=0, pi=0, pii=0`。合规扫描 verdicts 存在（security hook 有 registry.RunAll），但结果**从未回写** session_summaries。

### F4（P1·读面语义）closed 会话语义错位 + 两条趋势腿口径互相打架

- overview 的 `closed_sessions_24h` 实为“此刻不活跃”：7d 实测 active 3717 + closed 15590 = 19307 = 总数，**恰好二分**，不是“24h 内关闭”的流量概念，标签误导。
- 同一天的 closed_count 两个来源不一致：
  - overview 内嵌腿：`last < now-24h AND first < now-24h`（当前状态快照回填历史日）
  - trend 独立腿：`last_request_at < DATE(first)+1day`（“首日内结束”，今日行恒等于 new ⇒ 1911/1911 全 closed）
  - 实测 11-17：overview=3434 vs trend=5226；11-18：overview=0 vs trend=1911。前端 `trendChartData` 优先用 trend 腿 ⇒ 图上的 closed 线两条定义都不对。
- 今日行 overview 腿 `active_count=1911=closed_count=0 之外还有 new=1911`：active FILTER 无日期限定，把“今日新建”整体标成 active，历史日全为 0/1——**图表上的“活跃”线只在今天有值，其余日期恒 0**（趋势图基本没在画趋势）。

### F5（P1·写面滞后）健康分覆盖不足 + 队列窗口截断

- 7d 窗口 19,307 会话中 **2,278（11.8%）无健康分**；30d 窗口 **87,586（50.8%）无健康分**。worker 每小时只扫 100 条（`LIMIT 100`），按当前日增 ~2,400 会话的速率**永远追不上**（日产能 2,400 vs 日新增 2,400，净清零能力为零）。avg_score 是“已评分子集”的均值，30d 的 70.1 分与 7d 的 79.7 分不可比。
- 窗口按 `first_request_at` 过滤 ⇒ 30 天前开始、本月仍在消费的长会话**整条排除**在 30d 成本/请求 KPI 之外（成本归属滞后于会话首请求日）。7d 请求 73,935 vs 30d 请求 314,084 的悬殊差异部分来自队列效应。

### F6（P1·口径分裂）turns 对拍双向 degraded，KPI 的“总请求数”低估 13%

7d：summaries=73,935 vs business turns=83,617（**turns 多 13.1%**）；30d 差 15.5%。原因正是写面有意排除的内部回环（标题/摘要流量，72h 对账曾占 ~10%）进了 turns 宇宙但没进 summaries。页面已诚实标 degraded，但 KPI 卡“总请求数”仍显示 summaries 口径——**用户看到 73,935，真实业务轮次 83,617**。

### F7（P2·归因塌陷）top_clients 92.8% 落在 'default'

7d：default 17,915 / 19,307 = **92.8%**；30d 还有 `system` 53,687（合成系统会话进了 30d 榜但 client 归因塌陷）。client_id 取 `session_dim.client_id`，缺归属时回落 `client_models[1]` 再回落 'unknown'/'default'——归因维度事实上不可用。top_tasks 全部回落 'unknown'（30d 榜未显示 unknown 行是因为按 session_count 排序后 limit 5 截断）。

### F8（P2·写面精度）input/output 成本 50/50 token 比例拆分

写入时 `input_cost = total_cost × prompt/(prompt+completion)`。实测 7d output 成本只占 **3.9%**——对输出密集型业务明显失真（真实定价 output 单价通常是 input 的 3-5 倍）。成本拆分列不能用于任何按 input/output 计价的核对。

---

## 四、根因归纳

1. **“设计完成了”≠“接线完成了”**：DASHBOARD_API.md（2026-07-10）设计了 compliance/model stats/模块执行统计，写面接线从未发生（F2/F3）。设计文档与实现差了 4 个月且无人对账。
2. **读面 SQL 的聚合单位错位**：model_usage 用“会话行复制”做展开，成本/请求数被放大（F1）。
3. **一个指标两种定义并存**：closed/active 在 overview 腿与 trend 腿各写各的（F4），没有 single source of truth。
4. **快照指标被当流量指标展示**：active/closed 是“此刻状态二分”，页面文案却是“24h 新增/关闭”（F4）。
5. **后台补算产能远低于数据增速**（F5），且窗口语义（队列制）与用户直觉（发生制）不一致（F5 后半）。
6. **网关与 ASM 两套统计口径从未对齐**（§二末），“哪个数是对的”取决于问哪一侧。

---

## 五、优化方案（按优先级，待 owner 拍板后实施）

### P0（数据正确性，不改 schema）

1. **修 F1 model_usage 展开**：子查询改为按会话先算份额（如按 `session_models_usage` 逐模型请求占比，若无逐模型计数则先降级为“会话数×出现次数”去重计数并**移除成本/请求数列**，避免假数）；同时归一 `OutboundModel`（剥 provider 前缀）或改按 `models_canonical` 关联。
2. **修 F2 三面板数据源**：两选一——(a) 把 errors/performance 切到活数据源（`session_turns 族` 的 status/latency 或 `usage_facts`）；(b) 在 `cmd/gateway` 装配 `moduleexec.Executor` 并给三个 hook 注入。推荐 (a)：turns 族是 canonical 事实源且已在被 turns 对拍腿使用；module_executions_hot 本质是缓存表，拿它做统计面板本就是错位。
3. **修 F3 合规回写**：security hook 的 verdicts 已存在，加一条 best-effort 回写 `session_summaries.compliance_status/compliance_issues_count/prompt_injection_detected/...`（复用 sessionv2mirror 的 DimWriter 模式）；在回写接通前，前端合规卡片显示“未接入”而非 100%。

### P1（口径统一）

4. **统一 trend 与 overview 的 active/closed 定义**：要么都改为真实流量口径（closed = `last_request_at` 落在该日且会话进入终态——需要 outcome 字段配合；active_at_day = 该日有请求），要么明示为状态快照并把文案改为“现存活跃/现存静默”。推荐前者，`closed_sessions_24h` 改名 `inactive_sessions`。
5. **健康分补算提速**：worker 批量 UPDATE（一条 SQL 算一批）替代逐行 computeAndUpdate；或直接在 SQL 内做扣分计算。目标：1 小时清空 backlog。短期兜底：健康分布卡片把“未评分”单独显示为一格（现在被静默归入分母）。
6. **总请求数 KPI 切 turns 口径**或双口径并示（“业务轮次 83,617 · 会话累计 73,935”），turns_alignment 已有数据，前端只是没消费。

### P2（增强）

7. top_clients 归因：从请求头/API key 维度补 client_id（session_dim 已有表结构，问题在上游缺值），短期把 'default' 行标灰显示“未归因 92.8%”。
8. input/output 成本拆分：写入侧改用 provider 价目表拆分；无价目时保留 total 并将两列置 NULL（明示未知）而非按 token 比例伪造）。
9. 与 ASM 对齐：把 §二 的口径表落到 `docs/`（网关侧）并与 ASM `docs/contracts/` 互链，双方窗口语义（队列制 vs 发生制）写明，避免“两套数各说各话”。

### 建议实施顺序

F1/F2/F3 一轮（都是“显示的数是错的”）；F4/F5/F6 二轮（口径与覆盖）；P2 三轮。F1/F2/F3/F4 均可不改 schema 落地（F5 的批量算分也不需要）。

---

## 六、本轮验证记录

- 线上 API 实测：overview/trend × days=7/30、errors/performance/health/module-stats/data-horizon 共 9 个端点（token 认证，2xx；原始 JSON 存 /tmp/overview7.json 等）。
- 交叉计算：Σmodel_usage vs total_cost、graded/total 健康覆盖、active+closed=total 二分验证、两条趋势腿逐日对拍、今日行 new/active/closed 三值关系。
- 代码取证：moduleexec 装配 grep 0 命中、compliance_status 写方仅测试夹具、SetExecutor 调用方仅 autoupdate/chatHandler（非 moduleexec）。
- ASM 文档：README「当前边界」、docs/INDEX.md 三层文档树、session-analytics-ui/01-foundation-snapshot.md（Gateway canonical → 插件代理 → Vue 链路）、session-optimization-architecture.md §9。
