# Phase 2.x 实际请求反馈集成 - 部署报告

**日期**: 2026-07-01  
**Git SHA**: 0d5aec70  
**环境**: 184测试环境  
**状态**: ⚠️ 部署成功，但因数据库未初始化导致功能未启用

---

## 执行摘要

Phase 2.x 实际请求反馈集成已完成开发、测试并提交到主分支（commit `0d5aec70`）。尝试部署到184测试环境时发现数据库为空（无任何表），导致 `db.Open()` 失败，StateObserver 未能启用。

**核心功能已实现并通过测试**：
- ✅ 代码集成完成（Executor + main.go）
- ✅ 单元测试通过（4个测试用例）
- ✅ 编译通过（本地 + 184服务器）
- ⚠️ 部署受阻于数据库初始化问题

---

## 技术实现

### 1. 核心变更

#### Executor 结构体（executor.go:428-444）

```go
// StateObserver (2026-07-01 Phase 2.x): credential state manager that
// records real request outcomes (success/failure) and triggers adaptive
// probing. When non-nil, UpdateOnSuccess/UpdateOnFailure are called with
// classified error kinds. KindCanceled (user cancellation) is automatically
// skipped by the manager to avoid false positives.
StateObserver interface {
    UpdateOnSuccess(ctx context.Context, credID int, model string, latencyMs int, requestID string)
    UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string)
}
```

#### 集成点（3处）

**成功路径**（executor.go:927-943）:
```go
// 2026-07-01 Phase 2.x: Record success in credential state manager.
if e.StateObserver != nil {
    requestID := params.R.Header.Get("X-Request-Id")
    if requestID == "" {
        requestID = "async-" + time.Now().Format("20060102T150405.000")
    }
    e.StateObserver.UpdateOnSuccess(
        params.R.Context(),
        cand.CredentialID,
        cand.RawModel,
        result.LatencyMs,
        requestID,
    )
}
```

**失败路径1**（executor.go:1089-1105，流中断）:
```go
// 2026-07-01 Phase 2.x: Record failure in credential state manager.
// KindCanceled is automatically skipped by the manager.
if e.StateObserver != nil {
    requestID := params.R.Header.Get("X-Request-Id")
    if requestID == "" {
        requestID = "async-" + time.Now().Format("20060102T150405.000")
    }
    e.StateObserver.UpdateOnFailure(
        params.R.Context(),
        cand.CredentialID,
        cand.RawModel,
        kind,
        requestID,
    )
}
```

**失败路径2**（executor.go:1249-1265，普通失败）:
同样的模式，在 `e.Recorder.RecordFailure()` 后调用。

#### main.go 连线（main.go:674-681）

```go
// V3.1 (2026-06-26): wire route node recorder + session routing
routingExec.Recorder = executors.NewRouteNodeRecorder(fpSlots)

// 2026-07-01 Phase 2.x: wire credential state observer for real request feedback
if stateManager != nil {
    routingExec.StateObserver = stateManager
    slog.Info("credential state observer enabled (Phase 2.x real request feedback)")
}
```

**依赖条件**:
1. `dbConn != nil && dbConn.Enabled()` → `stateManager != nil`
2. `stateManager != nil` → `routingExec.StateObserver = stateManager`

### 2. 测试覆盖

#### 单元测试（executor_state_observer_test.go）

| 测试用例 | 验证内容 | 状态 |
|---------|---------|------|
| `TestStateObserver_UserCancelSkipped` | KindCanceled 传递到 manager（manager内部跳过） | ✅ PASS |
| `TestStateObserver_Success` | UpdateOnSuccess 正确记录 | ✅ PASS |
| `TestStateObserver_Integration` | 完整请求生命周期 | ✅ PASS |
| `TestStateObserver_ConcurrentCalls` | 并发安全性 | ✅ PASS |

```bash
$ go test -v -run TestStateObserver ./domains/streaming/executors/
=== RUN   TestStateObserver_UserCancelSkipped
--- PASS: TestStateObserver_UserCancelSkipped (0.00s)
=== RUN   TestStateObserver_Success
--- PASS: TestStateObserver_Success (0.00s)
=== RUN   TestStateObserver_Integration
--- PASS: TestStateObserver_Integration (0.00s)
=== RUN   TestStateObserver_ConcurrentCalls
--- PASS: TestStateObserver_ConcurrentCalls (0.00s)
PASS
ok      __REPO_URL_3__/domains/streaming/executors  0.522s
```

#### 编译验证

```bash
✓ go build ./domains/streaming/executors/
✓ go build ./cmd/gateway/
✓ go test ./domains/streaming/executors/ -short
✓ go test ./domains/credentialstate/ -short
```

---

## 部署尝试

### 环境信息

| 项目 | 值 |
|------|---|
| 目标环境 | 184测试环境 |
| Namespace | pms-test |
| Deployment | llm-gateway-go-deployment |
| 镜像 | 127.0.0.1:__PORT_8__/kx-llm-gateway-go:gitsha-0d5aec70 |
| 构建时间 | 2026-07-01 |

### 部署步骤

1. **代码打包上传** ✅
   ```bash
   tar czf /tmp/llm-gateway-go-0d5aec70.tar.gz .
   scp → 184:/tmp/
   ```

2. **镜像构建** ✅
   ```bash
   docker build -t kx-llm-gateway-go:gitsha-0d5aec70 .
   docker tag → 127.0.0.1:__PORT_8__/kx-llm-gateway-go:gitsha-0d5aec70
   docker push → 本地registry
   ```

3. **Deployment 更新** ✅
   ```bash
   kubectl set image deployment/llm-gateway-go-deployment \
       llm-gateway-go=127.0.0.1:__PORT_8__/kx-llm-gateway-go:gitsha-0d5aec70
   
   kubectl patch deployment/llm-gateway-go-deployment \
       --type='json' \
       -p='[{"op": "replace", "path": "/spec/template/spec/containers/0/imagePullPolicy", "value": "IfNotPresent"}]'
   ```

4. **Pod 滚动更新** ✅
   ```bash
   NAME                                         READY   STATUS    RESTARTS   AGE
   llm-gateway-go-deployment-d86985f4f-bftqc   1/1     Running   0          2m
   ```

5. **镜像验证** ✅
   ```bash
   $ kubectl get pod llm-gateway-go-deployment-d86985f4f-bftqc -n pms-test \
       -o jsonpath='{.spec.containers[0].image}'
   127.0.0.1:__PORT_8__/kx-llm-gateway-go:gitsha-0d5aec70
   ```

### 启动日志分析

```
2026/06/30 20:43:04 INFO postgres connected
2026/06/30 20:43:04 WARN postgres disabled error="ERROR: relation \"public.analysis_events\" does not exist (SQLSTATE 42P01)"
2026/06/30 20:43:04 WARN routing executor disabled (no database connection)
2026/06/30 20:43:04 WARN API key authentication disabled (no database connection)
2026/06/30 20:43:04 INFO gateway listening listen=:__PORT_3__
```

**问题诊断**:
1. ✅ PostgreSQL 连接成功
2. ❌ `db.Open()` 在 schema 迁移阶段失败
3. ❌ `dbConn.Enabled()` 返回 false
4. ❌ `stateManager` 未创建
5. ❌ `routingExec.StateObserver` 未设置

---

## 根本原因

### 数据库状态

```bash
$ kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "\dt"
Did not find any relations.
```

**184测试环境的 `llm_gateway` 数据库是空的，没有任何表。**

### db.Open() 失败链

`db.Open()` 执行的 schema 迁移函数（db/db.go:30-160）：

1. ✅ `pool.Ping()` - 连接成功
2. ✅ `slog.Info("postgres connected")` - 日志输出
3. ❌ `ensureRequestLogSchema()` - 需要 `request_logs` 表
4. ❌ `ensureQualityFixModeSchema()` - 需要 `credential_fps` 等表
5. ❌ `ensureApplicationsTable()` - 需要 `applications` 表
6. ❌ ... 17个 ensure* 函数
7. ❌ `ensureSupplementalRLS()` - 访问 `analysis_events` 等表时失败

**失败的 ensure* 函数返回错误 → `db.Open()` 返回 `err` → `dbConn = nil` → routing executor 禁用。**

### 为什么 analysis_events 不存在？

`ensureAnalysisEventsRLS()` (db.go:1422-1460) 应该**优雅降级**（表不存在时 `continue`），但实际上错误来自 `ensureSupplementalRLS()` 中的 `ALTER TABLE` 语句，针对不存在的表执行 RLS 操作。

---

## 解决方案

### 方案 A：运行完整数据库迁移（推荐）

1. **收集所有迁移SQL**

   llm-gateway-go 的数据库 schema 分散在：
   - `migrations/*.sql`（6个文件）
   - `db/db.go` 中的 `ensure*` 函数（~20个）

   需要从生产环境或完整部署的环境导出 schema：
   ```bash
   pg_dump -h <prod-host> -U llm_gateway --schema-only --no-owner --no-privileges llm_gateway > schema.sql
   ```

2. **在184测试环境应用 schema**
   ```bash
   kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway < schema.sql
   ```

3. **验证表创建**
   ```bash
   kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "\dt" | wc -l
   # 应该看到 30+ 个表
   ```

4. **重启 gateway**
   ```bash
   kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test
   ```

5. **验证 StateObserver 启用**
   ```bash
   kubectl logs -n pms-test -l app=llm-gateway-go --tail=100 | grep "credential state observer enabled"
   ```

### 方案 B：修复 db.Open() 容错逻辑

修改 `db/db.go`，让所有 `ensure*` 函数在表不存在时返回 `nil` 而不是错误：

```go
func (d *DB) ensureSupplementalRLS(ctx context.Context) error {
    if d == nil || d.pool == nil {
        return nil
    }
    
    // 先检查每个表是否存在，不存在则跳过
    tables := []string{"tenant_settings_kv", "settings_audit", "tenant_tool_policies", ...}
    for _, tbl := range tables {
        var exists bool
        err := d.pool.QueryRow(ctx, 
            `SELECT EXISTS (SELECT 1 FROM information_schema.tables 
             WHERE table_schema = 'public' AND table_name = $1)`, tbl).Scan(&exists)
        if err != nil || !exists {
            slog.Warn("table does not exist, skipping RLS", "table", tbl)
            continue
        }
        // 执行 ALTER TABLE...
    }
    return nil
}
```

**权衡**:
- ✅ 允许网关在不完整的数据库上启动
- ❌ 隐藏配置问题（数据库未正确初始化）
- ❌ 功能降级（无 StateObserver、无 API Key 认证等）

### 方案 C：条件编译/环境变量控制

添加环境变量 `LLM_GATEWAY_REQUIRE_DB=false`，跳过数据库依赖：

```go
if os.Getenv("LLM_GATEWAY_REQUIRE_DB") == "false" {
    slog.Warn("database disabled by env var")
    dbConn = nil
} else {
    dbConn, err = db.Open(context.Background(), cfg.DatabaseURL)
    ...
}
```

**权衡**:
- ✅ 快速workaround
- ❌ Phase 2.x 功能完全不可用（依赖数据库）
- ❌ 仅用于演示/开发环境

---

## 推荐行动计划

### 短期（完成 Phase 2.x 验证）

1. **方案 A1: 从生产环境导出 schema**
   ```bash
   # 在生产环境执行
   pg_dump -h <prod-pg> -U llm_gateway --schema-only --no-owner llm_gateway > /tmp/llm_gateway_schema.sql
   
   # 传输到184
   scp /tmp/llm_gateway_schema.sql 184:/tmp/
   
   # 在184应用
   ssh 184 "kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway < /tmp/llm_gateway_schema.sql"
   ```

2. **验证核心表存在**
   ```bash
   ssh 184 "kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c '
   SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = '\''credential_states'\'');
   SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = '\''request_logs'\'');
   SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = '\''credentials'\'');
   '"
   ```

3. **重启并验证**
   ```bash
   kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test
   sleep 30
   kubectl logs -n pms-test -l app=llm-gateway-go --tail=50 | grep -E "credential state observer enabled|postgres connected|routing executor enabled"
   ```

4. **功能验证**
   ```bash
   # 成功请求 → UpdateOnSuccess
   curl -X POST http://184:31781/v1/chat/completions \
       -H "Authorization: Bearer <test-key>" \
       -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
   
   # 检查数据库
   kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "
   SELECT credential_id, model, consecutive_fails, last_success_at 
   FROM credential_states 
   ORDER BY last_updated_at DESC LIMIT 5;"
   ```

### 中期（标准化测试环境）

1. **创建数据库初始化脚本**
   ```bash
   # scripts/init_test_db.sh
   #!/bin/bash
   # 1. 导出生产 schema
   # 2. 清理敏感数据
   # 3. 应用到测试环境
   # 4. 验证表结构
   ```

2. **添加健康检查**
   ```go
   // 在 db.Open() 后增加表存在性检查
   requiredTables := []string{"request_logs", "credentials", "credential_states"}
   for _, tbl := range requiredTables {
       // 检查并警告
   }
   ```

3. **CI/CD 集成**
   ```yaml
   # .github/workflows/deploy-test.yml
   - name: Verify test database
     run: |
       kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "\dt" | grep -q "credential_states"
   ```

### 长期（架构改进）

1. **Schema migration 工具集成**
   - 使用 `golang-migrate` 或 `goose`
   - 迁移文件统一管理（不再分散在 `db.go`）
   - 版本化 schema 变更

2. **容错启动模式**
   ```go
   // 数据库不可用时，网关仍可启动（只读模式）
   if err := db.Open(...); err != nil {
       slog.Warn("database unavailable, starting in read-only mode")
       enableReadOnlyMode()
   }
   ```

3. **环境分层配置**
   ```yaml
   # config/test.yaml
   database:
     required_tables: ["request_logs", "credentials"]
     fail_on_missing: false  # 测试环境允许优雅降级
   
   # config/production.yaml
   database:
     required_tables: [...full list...]
     fail_on_missing: true   # 生产环境严格检查
   ```

---

## 验收标准

### Phase 2.x 功能验收

| 验收项 | 验证方法 | 期望结果 | 当前状态 |
|--------|---------|---------|---------|
| 代码集成 | 代码审查 | Executor 调用 StateObserver | ✅ 完成 |
| 单元测试 | `go test` | 4个测试全部通过 | ✅ PASS |
| 编译 | `go build` | 无错误 | ✅ PASS |
| 镜像构建 | `docker build` | 成功 | ✅ PASS |
| 部署 | `kubectl rollout` | Pod Running | ✅ PASS |
| StateObserver 启用 | 日志检查 | "credential state observer enabled" | ❌ 未启用（数据库） |
| 成功请求记录 | 数据库查询 | `credential_states` 有新记录 | ⏸️ 待数据库修复 |
| 失败请求记录 | 数据库查询 | `consecutive_fails` 递增 | ⏸️ 待数据库修复 |
| 用户取消跳过 | 数据库查询 | KindCanceled 不计入统计 | ⏸️ 待数据库修复 |
| 自适应探测 | 监控日志 | 连续失败≥3 触发探测 | ⏸️ 待数据库修复 |

### 数据库修复验收

| 验收项 | 验证命令 | 期望结果 |
|--------|---------|---------|
| Schema 应用 | `psql -c "\dt"` | 30+ 个表 |
| credential_states 表 | `\d credential_states` | 包含 consecutive_fails, last_success_at 等列 |
| request_logs 表 | `\d request_logs` | 包含 model, credential_id 等列 |
| 网关启动 | `kubectl logs` | "routing executor enabled" |
| StateObserver 启用 | `kubectl logs` | "credential state observer enabled" |

---

## 已知问题和限制

### 1. 184测试环境数据库未初始化

**问题**: 空数据库导致 `db.Open()` 失败  
**影响**: Phase 2.x 功能无法验证  
**解决方案**: 运行完整 schema 迁移（方案 A）  
**优先级**: 🔴 P0（阻塞验收）

### 2. imagePullPolicy 反复被重置

**问题**: kubectl set image 后 imagePullPolicy 变回 Never  
**影响**: Pod ImagePullBackOff  
**解决方案**: 使用 `kubectl patch` 或 `kubectl apply -f`  
**优先级**: 🟡 P1（部署体验）

### 3. db.Open() 缺少表存在性检查

**问题**: `ensure*` 函数假设表已存在  
**影响**: 新环境部署失败  
**解决方案**: 添加 `SELECT EXISTS` 检查（方案 B）  
**优先级**: 🟡 P1（架构改进）

### 4. 缺少统一的 migration 工具

**问题**: Schema 分散在代码和 SQL 文件中  
**影响**: 难以追踪 schema 版本  
**解决方案**: 集成 golang-migrate  
**优先级**: 🟢 P2（长期改进）

---

## 相关文档

- [Phase 2 热度感知探测](./phase2_184_deployment_report.md) - commit 3342cfca
- [用户取消跳过修复](./phase2_user_cancel_fix.md) - commit 88772512
- [Phase 2.x 集成](本文档) - commit 0d5aec70
- [部署脚本](../deploy_phase2x_k8s.sh)
- [集成测试](../domains/streaming/executors/executor_state_observer_test.go)

---

## 附录

### A. 完整启动日志（184测试环境）

```
2026/06/30 20:43:04 observability: OTLP/HTTP exporter enabled
2026/06/30 20:43:04 WARN auth: INSECURE fail-open mode
2026/06/30 20:43:04 INFO gateway starting listen=:__PORT_3__ log_level=info
2026/06/30 20:43:04 INFO postgres connected
2026/06/30 20:43:04 WARN postgres disabled error="ERROR: relation \"public.analysis_events\" does not exist (SQLSTATE 42P01)"
2026/06/30 20:43:04 WARN routing executor disabled (no database connection)
2026/06/30 20:43:04 WARN API key authentication disabled (no database connection)
2026/06/30 20:43:04 INFO session endpoints enabled
2026/06/30 20:43:04 INFO gateway listening listen=:__PORT_3__
```

### B. 环境变量清单

| 变量名 | 值 | 说明 |
|--------|---|------|
| `LLM_GATEWAY_DATABASE_URL` | `postgres://...@__PRIV_IP_7__:__PORT_5__/llm_gateway` | 数据库连接 |
| `LLM_GATEWAY_ENABLE_POPULARITY_TRACKING` | (未设置) | Phase 2 热度追踪 |
| `LLM_GATEWAY_ENV` | (未设置) | production 强制认证 |

### C. 数据库诊断

```bash
# 检查数据库连接
kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "SELECT version();"

# 检查表
kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "\dt"

# 检查 credential_states schema
kubectl exec -n pms-test <pg-pod> -- psql -U llm_gateway -c "\d credential_states"
```

---

**结论**: Phase 2.x 代码实现完整且通过测试，部署受阻于184测试环境数据库未初始化。完成数据库 schema 迁移后即可验证全部功能。
