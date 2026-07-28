# 架构与 ownership

## 1. 目标架构

```text
┌──────────────────────── Gateway 数据面 ────────────────────────┐
│ request_logs / gateway.session_turns / session_bodies          │
│        │ terminal event / outbox（at-least-once）              │
└────────┼───────────────────────────────────────────────────────┘
         ▼
┌──────────────────── Semantic Worker（TARGET）──────────────────┐
│ normalize → redact → hash → boundary detection                 │
│   ├─ summary/title/intent/entity/event                         │
│   ├─ rules + embedding candidate + constrained LLM label       │
│   ├─ cluster candidate (HDBSCAN/BERTopic)                      │
│   └─ memory candidate / stats facts                            │
└────────┼───────────────────────────────────────────────────────┘
         ├──────────────► semantic_results / candidates / events
         ├──────────────► vector index（可选）
         ├──────────────► graph adapter（可选）
         └──────────────► memory ingest adapter（可选）
                              │
                              ▼
                    人工审校与项目工作台（TARGET）
                    accept / reject / split / merge / undo
                              │
                              ▼
                 current projection + analytics + replay
```

## 2. ownership

| 对象 | Gateway | Semantic worker | session-manager/运营 API | 前端 |
| --- | --- | --- | --- | --- |
| request/turn/body | canonical writer；租户上下文 | 受控只读 | 受控只读，不写事实表 | 只读 |
| terminal/outbox event | 产生、持久化、重试 | 消费、幂等 | 查询/审计 | 不直接消费内部 outbox |
| summary/label/entity | 可产生热路径原始 signal，但 v3 默认旁路 | 生成版本化 candidate/result | 派生投影、查询和审计 | 展示/人工确认 |
| cluster run | 提供输入事实 | 运行算法并写 run/member candidate | 触发、查询、权限控制 | 展示/拆分合并操作 |
| manual override | 保存审计事件的接收方可由 API owner 承担 | 应用到投影 | 认证、授权、审计、写 API | 发起显式操作 |
| memory/graph/vector | 只提供引用和权限边界 | adapter/索引任务 | 查询聚合结果 | 展示证据和 freshness |
| daily stats | 暴露成本/token/质量事实 | 聚合或发布事实 | 查询、导出、对账 | 图表 |

默认 ownership：Gateway 负责事实；Semantic Worker 负责派生计算；session-manager 负责运营读模型和管理动作；任何服务不得绕过公开契约写另一服务的事实表。

## 3. 事实层次

```text
Canonical fact
  request/turn/body、terminal event、人工操作事件
       │
Derived result
  summary、label candidate、entity/relation candidate、cluster run
       │
Current projection
  current label、当前项目成员、统计宽表、搜索索引
```

- canonical fact 不因模型重跑而改变；
- derived result 用 `(module_id, version, session_scope, input_hash)` 幂等；
- projection 可删除重建；
- manual override 是 canonical fact，优先级高于同一作用域的模型 result。

## 4. 分析生命周期

```text
pending → running → completed
              ├── failed → retry → dead_letter
              ├── cancelled
              └── stale（输入或模型版本变更）
```

同一分析请求的幂等键：

```text
analysis:{tenant_id}:{session_id}:{turn_scope}:{module_id}:{input_hash}:{version}
```

worker 必须：

- 先写 `running`，再调用外部服务；
- 外部调用带共享 deadline、最大重试和退避；
- 完成写结果和 event，再更新 projection；
- 重启从 durable queue/outbox 恢复，内存 pending 不能是唯一事实；
- 失败进入 retry/DLQ，并保留可操作错误码；
- 支持按 `event_id` 或 `analysis_id` replay，重复 replay 不产生重复事实。

## 5. 数据流和一致性

1. Gateway 写入 terminal request/turn；
2. 同事务或可靠 outbox 写 `analysis.requested`，正文只用 `body_ref`；
3. worker 按 tenant/session scope 加载规范化输入并计算 `input_hash`；
4. 规则先运行，embedding/LLM/图服务按 feature flag 调用；
5. 结果以版本化记录写入；
6. current projection 使用事件顺序和人工优先级重算；
7. 统计从 canonical/accepted projection 聚合，保存 `as_of` 和 source version；
8. 查询响应返回 `source`、`result_version`、`as_of`、`freshness` 和 evidence refs。

## 6. 安全边界

- tenant 必须来自认证主体或服务端上下文；query 中的 tenant 只能做一致性校验；
- 正文、附件、secret、API key、系统 prompt 片段不得进入普通事件和指标标签；
- 对外 LLM/embedding 调用前执行脱敏和字段 allowlist；
- worker 使用最小 DB scope；RLS 集成测试必须使用 `NOSUPERUSER` + `NOBYPASSRLS`；
- 人工写操作必须有 `actor_id`、`actor_role`、reason、correlation_id、idempotency_key；
- legal hold、retention、删除和导出必须先检查引用与审计要求。

## 7. ownership 迁移顺序

1. 只读消费现有 `request_logs`；
2. 只读消费 V2 turns/bodies，并完成对账；
3. Gateway 发出 terminal/analysis requested event；
4. worker shadow 计算，不改变现有查询；
5. 运营 API 读取 current projection；
6. 通过 RLS、幂等、重放和数据对账门禁后，逐租户开放人工写操作；
7. 最后才考虑记忆注入和任何请求路径影响。
