# 凭据状态管理漏洞修复总结
**日期**: 2026-07-03  
**修复范围**: 4个关键漏洞 (漏洞6/7/8/10)  
**编译状态**: ✅ 通过

---

## 一、已修复的漏洞

### ✅ 漏洞10: KindModelNotFound被错误分类为IsClientBug (HIGH)

**问题**:
- `model_not_found` (404) 是provider问题（上游下架模型），不是client bug
- 被错误归类为`IsClientBug`，导致不写任何状态

**修复**:
```go
// errorsx/classify.go:376
func IsClientBug(kind ErrorKind) bool {
    // 2026-07-03: Removed KindModelNotFound from this list.
    // model_not_found is typically a provider issue (model removed/renamed
    // upstream), not a client bug. It should trigger binding unavailability
    // via the dedicated mnf branch in executor.go, not skip state writes.
    switch kind {
    case KindToolCallIdMismatch, KindUnsupportedFeature, KindCanceled:
        return true
    default:
        return false
    }
}
```

**影响**: 避免404模型永远保持available=TRUE

---

### ✅ 漏洞8: UpdateOnFailure不调用InvalidateCache (MED)

**问题**:
- `credentialstate.Manager.UpdateOnFailure`在永久故障时设置`state.Available=false`
- 但不调用`provider.InvalidateAllCandidateCache()`
- router在接下来30s内仍会看到旧的candidate列表（缓存TTL）

**修复**:

1. **添加字段和setter** (`domains/credentialstate/manager.go`):
```go
type Manager struct {
    // ... 其他字段
    invalidateCandidateCache func()  // 2026-07-03: Bug #8 fix
}

func (m *Manager) SetInvalidateCandidateCache(fn func()) {
    m.invalidateCandidateCache = fn
}
```

2. **在UpdateOnFailure中调用** (`domains/credentialstate/manager.go:170-181`):
```go
if isPermanent && state.ConsecutiveFails >= 2 {
    state.Available = false
    nextRetry := now.Add(15 * time.Minute)
    state.RecoverAt = &nextRetry
    
    // 2026-07-03: Bug #8 fix - invalidate candidate cache when marking unavailable
    if m.invalidateCandidateCache != nil {
        m.invalidateCandidateCache()
    }
    // ... 日志
}
```

3. **在main.go中注入函数** (`cmd/gateway/main.go:1285`):
```go
stateManager.SetInvalidateCandidateCache(provider.InvalidateAllCandidateCache)
```

**影响**: 已标记为unavailable的凭据立即从候选列表移除

---

### ✅ 漏洞7: tenant_id硬编码'default' (HIGH)

**问题**:
- `loadCandidatesDB`硬编码`WHERE p.tenant_id = 'default'`
- 多租户部署时，非default租户的凭据永远不会被路由

**修复**:

1. **修改函数签名和SQL** (`provider/client.go:660-809`):
```go
func (c *Client) loadCandidatesDB(ctx context.Context, clientModel, tenantID string) ([]Candidate, error) {
    // 2026-07-03: Bug #7 fix - support tenantID parameter
    if tenantID == "" {
        tenantID = "default"  // backward compatibility
    }
    
    rows, err := c.dbPool.Query(ctx, `
        SELECT ...
        FROM model_offers mo
        ...
        WHERE p.tenant_id = $2  -- 原来是 'default'
        ...
    `, clientModelLower, tenantID)
}
```

2. **向上传递tenantID参数**:
   - `GetCandidates(ctx, model, profile, tenantID)` ← 添加第4个参数
   - `fetchCandidatesDB(ctx, model, profile, tenantID)`
   - `loadCandidatesDB(ctx, clientModel, tenantID)`

3. **修改所有接口定义**:
   - `provider/client.go:329`
   - `domains/streaming/executors/executor.go:45`
   - `domains/streaming/handler.go:201`
   - `cmd/gateway/main_v3_wiring.go:337`

4. **调用点传递tenantID**:
   - `domains/streaming/handler.go:1327` ← 从`keyInfo.TenantID`获取
   - `domains/streaming/messages.go:391` ← 同上
   - `domains/streaming/responses.go:334` ← 同上
   - `domains/streaming/executors/context_summarize.go:125` ← 传空字符串（compaction内部功能）

**影响**: 支持多租户路由，不会越权

---

### ✅ 漏洞6: writer.RestoreOnSuccess子查询bug (MED)

**问题**:
```sql
UPDATE model_offers mo
FROM provider_models pm
WHERE pm.id = (
    SELECT cmb.provider_model_id
    FROM credential_model_bindings cmb
    WHERE cmb.credential_id = $1
      AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2  -- ❌ 引用外层pm
)
```
子查询内引用外层FROM的`pm`，导致关联子查询语义错误。

**修复** (`domains/credential/writer.go:143-159`):
```sql
UPDATE model_offers mo
SET available = TRUE, ...
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE mo.credential_id = cmb.credential_id
  AND mo.raw_model_name = pm.raw_model_name
  AND cmb.credential_id = $1
  AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2
  AND mo.available = FALSE
  ...
```
改用JOIN而不是子查询，避免cross-model pollution。

**影响**: 恢复A模型时不会误恢复B模型

---

## 二、未修复的漏洞（需要后续处理）

### ⚠️ 漏洞1: circuit_state跨进程失同步 (HIGH)

**原因**: 修改较复杂，需要：
1. 将`credential.Manager`的breaker状态移到Redis或定期从DB读取
2. 修改`Allow()`方法读取共享状态
3. 测试多实例部署场景

**临时缓解**: 单实例部署时无影响

**计划**: 阶段2修复

---

## 三、修改文件清单

| 文件 | 修改内容 | 行数变化 |
|------|---------|---------|
| `errorsx/classify.go` | 从IsClientBug移除KindModelNotFound | -1 |
| `domains/credentialstate/manager.go` | 添加invalidateCandidateCache字段和调用 | +11 |
| `cmd/gateway/main.go` | 注入InvalidateAllCandidateCache函数 | +2 |
| `provider/client.go` | 添加tenantID参数，修改SQL | +15 |
| `domains/streaming/executors/executor.go` | 接口添加tenantID参数 | +2 |
| `domains/streaming/handler.go` | 接口+调用添加tenantID | +6 |
| `domains/streaming/messages.go` | 调用添加tenantID | +5 |
| `domains/streaming/responses.go` | 调用添加tenantID | +5 |
| `cmd/gateway/main_v3_wiring.go` | 适配器添加tenantID参数 | +3 |
| `domains/streaming/executors/context_summarize.go` | 调用传空tenantID | +1 |
| `domains/credential/writer.go` | 重写子查询为JOIN | -4 |

**总计**: 11个文件，净增约45行

---

## 四、测试建议

### 4.1 回归测试

1. **漏洞10验证**:
   ```bash
   # 模拟404 model_not_found
   # 预期: cmb.available=FALSE, mnfStreak写入model_probe_runs
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer test-key" \
     -d '{"model":"deleted-model-404","messages":[...]}'
   ```

2. **漏洞8验证**:
   ```sql
   -- 触发永久故障（auth_failed）× 2次
   -- 检查: credentialstate表 available=false
   -- 检查: 下一个请求立即不路由到该凭据（不等30s）
   SELECT * FROM credentials_availability_state 
   WHERE credential_id = X AND available = false;
   ```

3. **漏洞7验证**:
   ```sql
   -- 插入非default租户凭据
   INSERT INTO providers (tenant_id, ...) VALUES ('tenant-A', ...);
   INSERT INTO credentials (provider_id, ...) VALUES (...);
   
   -- 请求时传递tenant-A的API key
   -- 预期: 只路由到tenant-A的凭据
   ```

4. **漏洞6验证**:
   ```sql
   -- 同一raw_model_name有两个pm.id (例如gpt-4o free/paid)
   -- 触发一个cmb的恢复，检查另一个cmb是否误恢复
   SELECT * FROM model_offers 
   WHERE raw_model_name = 'gpt-4o' AND available = TRUE;
   ```

### 4.2 性能测试

- 多租户场景下候选缓存命中率（增加tenantID维度后）
- InvalidateCache调用频率（漏洞8修复后）

---

## 五、部署清单

### 5.1 数据库迁移

**无需迁移** — 所有修改都是代码层面

### 5.2 配置变更

**无需变更** — tenantID从现有`keyInfo.TenantID`获取

### 5.3 回滚方案

```bash
# 如果出现问题，回滚到修复前的commit
git revert <this-commit-hash>
go build ./cmd/gateway
systemctl restart llm-gateway
```

### 5.4 监控指标

部署后监控以下指标：

1. **候选缓存失效频率**: `provider.cache_invalidations` (预期增加)
2. **跨租户路由错误**: 检查日志中是否有`tenant_id`不匹配的错误
3. **model_not_found后的状态写入**: `model_probe_runs`表增长
4. **子查询修复后的恢复正确性**: `model_offers.available`变化

---

## 六、下一步计划

### 阶段2（高优先级）:

1. **漏洞1**: circuit_state移到Redis
2. **漏洞3**: model_probe_state.suspicious不闭环
3. **漏洞12**: syncRetryTimeout失控
4. **漏洞14**: admin UI恢复suspended按钮

### 阶段3（建议修）:

5. 编写状态转换测试套件（覆盖决策树）
6. 本地多实例部署验证
7. 整理剩余10个低优先级漏洞

---

**修复人**: AI Agent  
**审核状态**: 待Code Review  
**编译状态**: ✅ go build 通过  
**预计上线时间**: 2026-07-04
