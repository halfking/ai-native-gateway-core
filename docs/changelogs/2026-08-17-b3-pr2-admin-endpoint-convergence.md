# 2026-08-17 B3 PR2 admin endpoint 收敛 + audit 查询面迁移

## 背景

B3 PR1（commit `0a105fe89e` + 二审 `f46aad03e`）已完成请求生命周期事件从
`state_transition_logger` 全链退役，统一进 `requestjourney` 表（event_type
非空）。legacy 表写入方由 3 个降到 2 个：requestjourney（journey 行）+ admin
节点操作审计（`node_operations_audit.go`，transition_type='state'，伪
request_id）。`/api/admin/requests/{id}/transitions` 仍暴露旧 endpoint，但
其作用面已模糊（journey 行移到 `/api/admin/request-journeys/`，audit 行
无替代查询面）。

PR1 后观察：streamretry 重试请求的 journey 投影正确——`/api/admin/request-journeys/`
Detail 出口按 seq 合并 attempt-1/2 双链路（每个 attempt 各含 terminal +
retry_scheduled 边界 + 单条 ingress 终态 succeeded）。query path 经
`mergeJourneySources` 按 seq 合并 memory/redis/postgres 三源，二审修复的
`ArrivalTime()` carrier 继承在 `ensureRequestJourney`（`domains/streaming/
request_context.go:154`）正确生效，ingress 同 identity HSET 收敛为单条
succeeded。

## 方案

B3 PR2 拆为两 endpoint 收敛：

1. **撤** `/api/admin/requests/{id}/transitions`：
   - `admin/handler.go:921` mux 注册删除
   - `admin/request_transitions.go` 整个文件 `git rm`（106 行）
   - 该 endpoint 旧职责（请求 lifecycle 链查询）由
     `/api/admin/request-journeys/{id}` Detail 接管
2. **建** `/api/admin/audit/node-operations`：
   - 新文件 `admin/audit_operations.go`，查询面只覆盖 audit 行
   - SQL 守卫：`transition_type = 'state' AND event_type IS NULL`，与
     journey 行硬隔离
   - 查询参数：`provider_id` / `operation` / `operator_id` / `since`
     (RFC3339) / `limit` (默认 50，上限 200)
   - 鉴权：admin 中间件；非 super_admin 走 RLS，super_admin 走 bypass

## 实现

新 audit handler 函数顺序：method 守卫 → parse 校验（先于 DB 可用性检查，
避免配置错误干扰客户端输入校验）→ db nil 503 → RLS-aware query。
`requestjourney.PostgresRepository` 的 Detail/RecentTotal 等已用
`event_type IS NOT NULL` 过滤（`domains/requestjourney/repository.go:95/119/
139` 等），audit 行（`event_type IS NULL`）天然不可见，无需双层守卫即可保证
journey/audit 两类数据互不污染。

handler.go 注册点（`admin/handler.go:921` 旧注册位置）替换为：

```go
mux.HandleFunc("/api/admin/audit/node-operations", admin(h.handleAuditNodeOperations))
```

测试 `admin/audit_operations_test.go`：纯单元覆盖 5 个测试点（非 GET → 405、
db nil → 503、parse 校验 7 子 case → 400、limit clamping、JSON round-trip），
真实 DB E2E 走 CI/部署验证阶段。

## 测试验收

- `go build ./...` 全绿
- `go vet ./admin/...` 无输出
- `go test ./admin/` PASS（5.123s，含 6 个新增测试 + 现有测试无回归）
- `go test ./domains/requestjourney/...` PASS（1.860s）
- `go test ./domains/streaming/...` PASS（22.952s + executors 9.295s +
  webcookie 2.108s + integrity 0.519s）
- `go test ./internal/streamretry/...` PASS（1.048s）
- 残留引用扫描：`grep handleRequestTransitions` 无结果

## 边界判定

- `withAllTenantReadOnlyTx` (super_admin) 与 `withTenantTx` (RLS) 分流与
 现有 `request_journey.go` 同源，一致性保留
- audit 行 `tenant_id` 恒为 `'default'`（`node_operations_audit.go:99`
 写死），RLS 路径对非 super_admin 仅在 `app.current_tenant='default'` 时
 返回行——目前节点操作审计统一租户，与产品行为相符
- limit 上限 200 是当前调用方的合理峰值；未来若超 200 需翻页则引入 cursor
- audit endpoint 不返回 journey 行（SQL 过滤）；journey endpoint 不返回
 audit 行（repository 过滤）；两表同 schema 不同 row identity，类型断言
 比 schema 拆分更经济

## 后续

- B2b 监控四项按报告 §E.5 继续滚动观察
- CI 棘轮首跑观察（f46aad03e/adc0851e0/c133906ac 三次 push 触发）
- pre-existing U1000 清理（executors 9 + streaming 14）可选
- version.json 升级流程独立于本会话职责

## 经验

- endpoint 撤销前的查询面归属评估必须落到 SQL 过滤层而不是应用层判断——
  repository 里 `event_type IS NOT NULL` 的过滤让 audit 行天然不可见，
  无需在 journey handler 里加额外分支
- parse 校验放 db nil 检查之前更稳：基础设施错误不应淹没客户端输入反馈
- 命名约定跟随现有 `docs/changelogs/YYYY-MM-DD-{topic}.md`，与原交接
  文档描述的 `session-logs/` 路径偏差（仓库重构后实际落点为 changelogs）