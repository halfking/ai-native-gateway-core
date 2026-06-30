# Phase 2 补丁：用户取消错误不计入凭据统计

**日期**: 2026-07-01  
**问题**: 用户取消请求被计入凭据错误统计  
**影响**: 未来 Phase 2.x（实际请求反馈集成）  
**修复**: commit `<将被填充>`

---

## 问题描述

在 Phase 2 的 `credentialstate.Manager.UpdateOnFailure()` 方法中，所有错误都会增加 `ConsecutiveFails` 计数器，包括用户主动取消的请求（`errorsx.KindCanceled`）。

用户取消（如客户端中断、超时取消等）**不是凭据本身的问题**，不应该：
- 增加连续失败计数
- 触发探测器重新验证
- 标记凭据为不可用

## 修复方案

### 代码变更

**文件**: `domains/credentialstate/manager.go`

在 `UpdateOnFailure()` 方法开头添加检查：

```go
func (m *Manager) UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID string) {
	// 2026-07-01: 用户取消不应计入凭据错误统计
	// 用户主动取消请求（如客户端中断、超时取消等）不是凭据本身的问题，
	// 不应触发探测或标记凭据为不可用。直接跳过。
	if errKind == errorsx.KindCanceled {
		return
	}

	// ... 原有逻辑
}
```

### 测试用例

**文件**: `domains/credentialstate/manager_test.go`

新增测试 `TestManager_UpdateOnFailure_IgnoresCanceled`：

```go
func TestManager_UpdateOnFailure_IgnoresCanceled(t *testing.T) {
	// 验证：
	// 1. KindCanceled 不增加 consecutive_fails
	// 2. KindCanceled 不更新 last_error
	// 3. 真实错误仍然被正常计入
}
```

---

## 现状说明

### Phase 2 当前架构

Phase 2（commit 3342cfca）的核心功能是**热度感知探测**：

1. **ModelPopularityTracker**：
   - 每5分钟查询 `request_logs` 统计模型调用频率
   - 根据热度调整探测间隔（热门10s，温热2m，冷门10m）

2. **Manager 集成**：
   - 提供 `GetRecommendedProbeInterval(model)` API
   - 由探测器（`bg/credential_probe_v2.go`）使用

3. **UpdateOnFailure** 接口：
   - **当前未被调用**（预留接口）
   - 设计用于将来集成实际请求反馈

### 相关保护机制

**RouteNodeRecorder**（`domains/streaming/executors/route_node_recorder.go`）已有类似保护：

```go
func isTransientRouteNodeFailure(kind errorsx.ErrorKind) bool {
	if kind == errorsx.KindCanceled ||
		kind == errorsx.KindNetwork ||
		kind == errorsx.KindTimeout ||
		kind == errorsx.KindUpstreamDown {
		return true  // 跳过这些瞬态错误
	}
	// ...
}
```

这确保了路由层面已正确处理用户取消。

---

## 影响范围

### 当前（Phase 2）

- ✅ **无影响**：`UpdateOnFailure` 尚未集成到请求流程
- ✅ **预防性修复**：避免将来集成时出现Bug

### 将来（Phase 2.x）

当实际请求流程调用 `StateObserver.UpdateOnFailure()` 时：

**修复前**（错误行为）:
```
用户取消 → ConsecutiveFails++ → 达到阈值 → 触发探测 → 标记不可用
```

**修复后**（正确行为）:
```
用户取消 → 直接跳过 → 凭据状态不变
真实错误 → ConsecutiveFails++ → 正常失败处理
```

---

## 验收

### 编译验证

```bash
go build ./domains/credentialstate/
# ✓ 编译通过
```

### 测试验证

```bash
go test -v -run TestManager_UpdateOnFailure_IgnoresCanceled ./domains/credentialstate/
# 当前跳过（需要测试数据库）
# 在集成测试环境中验证
```

### 行为验证（将来）

当 `UpdateOnFailure` 被集成后，验证：

```bash
# 1. 模拟用户取消
curl -X POST ... & sleep 1 && kill $!

# 2. 检查凭据状态（ConsecutiveFails 不应增加）
psql -c "SELECT consecutive_fails FROM credential_states WHERE credential_id=X AND model='Y';"
```

---

## 相关错误类型

### 应跳过的错误（不计入凭据统计）

- ✅ `KindCanceled`: 用户主动取消（**本次修复**）

### 应计入的错误

- ❌ `KindAuth`: 凭据认证失败
- ❌ `KindQuotaPermanent`: 永久配额耗尽
- ❌ `KindModelNotFound`: 模型不存在
- ❌ `KindRateLimit`: 速率限制（临时）
- ❌ `KindNetwork`: 网络错误（临时）

分类逻辑见 `manager.go:145-152`（永久故障 vs 临时故障）。

---

## 下一步

### Phase 2.x: 实际请求反馈集成

1. **在 streaming executor 中调用**:
   ```go
   // domains/streaming/executors/executor.go
   if stateObserver != nil {
       if err != nil {
           kind := errorsx.ClassifyError(err, resp)
           stateObserver.UpdateOnFailure(ctx, credID, model, kind, reqID)
       } else {
           stateObserver.UpdateOnSuccess(ctx, credID, model, latency, reqID)
       }
   }
   ```

2. **验证用户取消保护**:
   - 模拟客户端中断
   - 确认 `consecutive_fails` 不变
   - 确认不触发无效探测

3. **性能监控**:
   - 监控 `UpdateOnFailure` 调用频率
   - 评估对数据库的写入压力
   - 考虑批量写入优化

---

## 参考

- Phase 2 核心: commit `3342cfca` (热度感知探测)
- 用户取消修复: commit `<本次提交>`
- 相关文件:
  - `domains/credentialstate/manager.go`
  - `domains/credentialstate/manager_test.go`
  - `errorsx/classify.go` (错误分类)
  - `domains/streaming/executors/route_node_recorder.go` (路由层保护)

---

**结论**: 用户取消错误已正确排除在凭据统计之外，为将来的实际请求反馈集成提供了正确的基础。
