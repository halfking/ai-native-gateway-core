# 会话→项目归属：双路径设计

2026-08-20

## 问题

两类流量进入网关，项目/任务信息的可得性完全不同：

1. **ACC 认领路径**：智能体从 ACC 认领任务后执行，请求头里带
   `X-Gw-Project-Id` / `X-Gw-Task-Id`，值是 ACC 数据库的真实主键。
2. **其它智能体**：客户端不可控，无法要求它们传这些头，字段为空。

第二类需要推断，但推断结果与第一类的权威值**绝不能混入同一字段**。

## 核心决策

### 1. 权威值与推断值分表存储

- 权威值：`session_dim.project_id`（migration 407 已存在），供计费与对账。
- 推断值：`session_project_attribution`（migration 544 新增），仅供归集分析。

理由：一旦混存，就再也无法回答"这个项目的 token 数字可信吗"——分不清哪些
是真实的、哪些是猜的。这与 OpenTelemetry GenAI 规范对
`gen_ai.conversation.id` 的要求一致：拿不到真实标识时应当留空，而不是用
UUID、trace id 或内容哈希兜底
（<https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-spans.md>
脚注 5）。

代码层面这条红线由 `CloseHook.OnSessionClosed` 的第一道闸保证：
`HasAuthoritativeProject` 为真时整条推断链路直接返回，绝不覆盖。

### 2. 三级降级，成本递增，命中即停

| 层级 | 方式 | 成本 | 置信度 |
|---|---|---|---|
| rule | 仓库路径 / 关键词 / 工种键匹配 | 0 | 0.95（歧义时 0.6） |
| inherit | 同一 `identity_hash` 近期已确认项目 | 0（一次查表） | 0.75 |
| llm | 模型兜底 | 一次小模型调用 | 0.55 |

`WithLLM` 不注入时自动退化为纯零成本模式。建议先只开前两级，观察规则层
覆盖率，再决定是否值得开模型兜底。

歧义处理：多个不同项目同时命中时降置信度到 0.6 并强制进人工队列——这正是
最易出错、也最值得人看一眼的情况。

### 3. 会话级触发，不是请求级

挂在 `SessionSummaryWorker` 的 `SessionCloseHook`（与
`OptimizationCloseHook` 同一挂载点），订阅 `EventSessionClosed`。

这是成本模型的关键决定。假设 30% 流量是非 ACC 路径、平均每会话 20 次请求：

- 请求级触发：放大 30%
- 会话级触发：放大 30% ÷ 20 ≈ 1.5%
- 再叠加规则层拦掉约 70%：**落到 0.5% 以下**

### 4. 不做成智能体

推断是一次无状态分类调用，不是带记忆/工具/多轮决策的 agent。Anthropic 公开
数据：agent 约 4× token、multi-agent 约 15× token
（<https://www.anthropic.com/engineering/built-multi-agent-research-system>）。
同一篇文章里 Anthropic 自己的子智能体至今仍是同步执行，异步被列为未解难题。

现有 `admin/auto_title_generator.go` 的形态（单次小模型 + Redis 分布式锁 +
首轮 guard + 短消息跳过）已经是最优解，不应改为智能体。

### 5. 人工确认闭环

`status` 三态 `pending|confirmed|rejected`，`method` 四态
`rule|inherit|llm|manual`，与 migration 351 `session_tags.tag_source`
（`auto|llm|manual`）的既有设计保持一致。

- 已 `rejected` 的会话不再重复推断——那是明确的"别再猜了"。
- 只有 `confirmed` 的记录才能被 inherit 层继承，避免错误顺着继承链扩散。
- 人工确认后 `method` 升为 `manual`，`confidence` 由复核者决定。

ChatGPT Projects 与 Claude Projects 均为用户手动组织（Anthropic 官方用词是
"curated"，<https://www.anthropic.com/news/projects>），人工确认是业界主流，
不是妥协。

### 6. 推断结果不用于计费

明确约束：`session_project_attribution` 仅用于归集后的分析，目标是提升 AI
使用效率与质量。约 85% 准确率的归属若用作计费依据，最终会变成人工对账
噩梦。计费口径只认 `session_dim.project_id`。

## 数据来源

推断只读 `request_logs` 已有列，不新增任何采集点：

- `work_type`（`X-Gw-Work-Type`，ACC 工种键）
- `request_preview`（首轮请求预览）
- `identity_hash`（客户端指纹，供 inherit 层关联）

注意 `request_logs` 没有独立的 `system_prompt` 列，请求全文在 `request_body`
(jsonb) 里。`Signals.SystemPrompt` 保留字段但 `PGStore` 默认不填——为归集去
反序列化整个 body 不划算；需要更高命中率的部署可自行注入。

## 待接线

本次提交只落地领域逻辑、存储层与迁移，**未在 `cmd/gateway/main.go` 接线**，
因此对现有运行时零影响。接线前需要：

1. 跑填充率 SQL 确认非 ACC 路径的实际占比，判断是否值得启用。
2. 从 ACC 侧补 `GET /api/llm/projects` 只读接口，参照
   `admin/acc_work_types.go` 的同步模式填充 `project_dim`。
3. 在 `main.go` 用 `summaryWorker.AddCloseHook(projectattr.NewCloseHook(...))`
   挂载，并加 feature flag 控制。

填充率查询：

```sql
SELECT
  count(*) AS total,
  count(*) FILTER (WHERE project_id IS NOT NULL AND project_id <> '') AS has_project,
  count(*) FILTER (WHERE gw_task_id IS NOT NULL AND gw_task_id <> ''
                     AND gw_task_id <> 'default') AS has_task
FROM request_logs
WHERE ts > now() - interval '7 days';
```
