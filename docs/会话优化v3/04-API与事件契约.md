# API 与事件契约

> 所有接口为 `TARGET`，当前不代表路由已注册。兼容期可由现有 `/api/admin/*` 代理，但必须保持租户和审计约束。

## 1. 读 API

```text
GET /api/v1/sessions/{session_id}/semantic
GET /api/v1/sessions/{session_id}/semantic/candidates
GET /api/v1/semantic/clusters?cursor=&limit=&label=&as_of=
GET /api/v1/semantic/clusters/{cluster_id}
GET /api/v1/semantic/projects/{project_id}/timeline
GET /api/v1/semantic/relations?entity_id=&predicate=
GET /api/v1/semantic/stats?from=&to=&project_id=&task_label=
GET /api/v1/semantic/eval-runs/{run_id}
GET /api/v1/jobs/{job_id}
```

会话 semantic response 至少包含：

```json
{
  "session_id": "s-1",
  "summary": {"text": "...", "source": "llm", "result_version": 4},
  "labels": [{"namespace": "task", "key": "debug", "state": "accepted", "confidence": 0.94}],
  "entities": [{"entity_id": "e-1", "name": "llm-gateway", "type": "repo"}],
  "membership": {"project_id": "p-1", "source": "human", "cluster_run_id": "run-3"},
  "as_of": "2026-07-28T10:00:00Z",
  "freshness": "current",
  "evidence_refs": ["request:r-1", "turn:s-1:3"]
}
```

## 2. 人工写 API

```text
POST /api/v1/sessions/{session_id}/semantic/labels/{label_id}/accept
POST /api/v1/sessions/{session_id}/semantic/labels/{label_id}/reject
POST /api/v1/sessions/{session_id}/semantic/labels/custom
POST /api/v1/semantic/clusters/{cluster_id}/split
POST /api/v1/semantic/clusters/{cluster_id}/merge
POST /api/v1/semantic/memberships/override
POST /api/v1/semantic/overrides/{override_id}/revoke
POST /api/v1/sessions/{session_id}/semantic/recompute
POST /api/v1/semantic/replay
```

所有写接口：

- 必须有 `Idempotency-Key`；相同 key + 相同 canonical body 返回原结果；相同 key + 不同 body 返回 `idempotency_conflict`；
- 必须从认证主体取得 tenant，客户端 `tenant_id` 只能做一致性校验；
- 必须写 `actor_id`、`actor_role`、`reason`、`correlation_id`；
- split/merge 必须返回 `override_id`、新 projection version 和影响范围；
- recompute/replay 返回 `202` 和 `job_id`，不能在 HTTP 请求中等待大规模重算；
- 不能在 URL、普通响应或事件中回显 secret、API key、未经允许的全文正文。

## 3. 权限 scope

| 操作 | 最小 scope |
| --- | --- |
| 会话语义读取 | `session:read` |
| 聚类/项目读取 | `session:read` + tenant membership |
| 候选确认 | `session:annotate` |
| split/merge/label 写入 | `session:organize` |
| recompute/replay | `session:analysis:run` |
| 全租户统计/导出 | `session:analytics:all`，仅受控管理员 |

Gateway service token、session-manager service JWT、浏览器 Admin JWT 不能混用 audience 或 scope。内部回调应包含签名、时间戳、nonce/replay window 和 correlation ID。

## 4. 分页、缓存和 freshness

- list 使用 opaque cursor，稳定排序为 `(updated_at DESC, id DESC)`，服务端 `limit <= 100`；
- 详情响应可使用 `ETag`/`If-None-Match`；
- 所有派生响应包含 `as_of`、`source`、`result_version`、`freshness`；
- 投影延迟超阈值返回 `stale_projection` 或明确 degraded 标志，不假装 current；
- 跨租户资源不存在时统一返回 `session_not_found`，避免泄露存在性。

## 5. 错误码

| code | HTTP | 说明 |
| --- | --- | --- |
| `tenant_context_required` | 400 | 缺少服务端租户上下文 |
| `forbidden_scope` | 403 | 权限 scope 不足 |
| `tenant_mismatch` | 403 | 请求 tenant 与认证主体不一致 |
| `session_not_found` | 404 | 不暴露跨租户存在性 |
| `semantic_not_ready` | 409 | 分析尚未完成 |
| `stale_projection` | 409 | 读模型超 freshness 门槛 |
| `idempotency_conflict` | 409 | 相同 key 的参数不一致 |
| `override_conflict` | 409 | 当前版本已有冲突人工事实 |
| `body_unavailable` | 409 | 正文引用不存在或不满足 retention |
| `job_not_found` | 404 | 异步任务不存在 |
| `analysis_unavailable` | 503 | worker/外部分析服务不可用 |
| `deadline_exceeded` | 504 | 共享 deadline 耗尽 |

## 6. 事件 envelope

```json
{
  "event_id": "uuid",
  "event_type": "analysis.completed.v1",
  "schema_version": 1,
  "occurred_at": "2026-07-28T10:00:00Z",
  "producer": "semantic-worker",
  "tenant_id": "t-1",
  "session_id": "s-1",
  "turn_scope": {"from": 1, "to": 4},
  "correlation_id": "c-1",
  "idempotency_key": "analysis:t-1:s-1:summary:hash:v4",
  "source_refs": [{"kind": "request", "id": "r-1", "sha256": "..."}],
  "payload": {"result_id": "...", "result_version": 4}
}
```

最小事件类型：

- `analysis.requested.v1`；
- `analysis.completed.v1` / `analysis.failed.v1`；
- `cluster.rebuilt.v1`；
- `membership.overridden.v1`；
- `project.split.v1` / `project.merged.v1`；
- `relation.corrected.v1`；
- `projection.rebuilt.v1`；
- `eval.completed.v1`。

事件传递是 at-least-once；consumer 必须按 `event_id` 和 idempotency key 幂等，失败进 retry/DLQ，支持 replay。不能宣称 exactly-once delivery。

## 7. 事件 payload 规则

允许：ID、hash、长度、时间、版本、置信度、标签、最小统计、body refs。禁止：API key、密码、token、secret、完整系统 prompt、未脱敏附件和未授权全文。

## 8. 兼容与版本

- 新契约使用 `/api/v1` 和 `X-API-Version`；
- 旧 `/api/admin/session-clusters`、现有 summary/title API 在兼容期保留原响应语义；
- 新增能力只能通过 feature flag 显式启用；
- 删除或变更旧接口前，至少保留 deprecation/sunset、迁移文档和回滚窗口。
