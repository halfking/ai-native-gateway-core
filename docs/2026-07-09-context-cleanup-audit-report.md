# 2026-07-09 横向修复审计报告

## 审计时间
2026-07-09（在初次横向修复提交 `c5082fb4` 后）

## 审计范围
对 executor / credentialstate / URSM / admin / bg / pool / relay 等模块做全量扫描，检查：
1. 是否还有遗漏的 request-ctx 误用于善后写回/资源释放
2. 是否有模块重复实现资源管理逻辑
3. `runctx` helper 是否被正确复用

## 发现的问题

### 高风险遗漏（已修复）

#### 1. domains/ursm/batch_writer.go:250-263
**问题**: 异步 goroutine 内对 Redis 的写操作直接用 `context.Background()`，没有超时控制。  
**风险**: Redis hang 时会导致 goroutine 永久泄漏。  
**修复**: 改为 `runctx.BackgroundTimeout(3*time.Second)`。  
**状态**: ✅ 已修复

### 误报（不需要修复）

#### 1. executor_anthropic.go:750 / executor_chat.go:348
**判断**: `reqPool.Acquire(upCtx)` 使用的 `upCtx` 在有 session 时已经是独立 background ctx，在无 session 时继承 request ctx 是有意设计（让无会话请求可被客户端断开立即取消）。  
**结论**: 不需要修复

#### 2. admin/credential_monitor.go:825
**判断**: `defer tx.Rollback(context.Background())` 是标准 Go 事务模式（commit 后 rollback 无害）。  
**结论**: 不需要修复

### 中风险点（不在本轮修复范围）

以下场景手写了 `context.WithTimeout(context.Background(), ...)`，但它们本身就是独立生命周期任务，不绑定 request，不需要改成 `runctx`：

1. **测试代码**：
   - `domains/credential/flusher_test.go`
   - `domains/credential/limiter_concurrent_test.go`
   - `pool/pool_concurrent_test.go`

2. **后台任务**（bg/）：
   - `bg/auto_route_realtime_listener.go:147`
   - `bg/candidate_failure_monitor.go:343`
   - `bg/model_probe.go:718`

3. **健康检查**（pool/）：
   - `pool/pool.go:338`

4. **定期刷新**（domains/credential/）：
   - `domains/credential/flusher.go:92`

这些场景本来就应该用 `context.Background()`，不属于"请求结束后的善后写回"。

## runctx 使用覆盖情况

### 已正确使用 runctx 的文件
- `domains/streaming/executors/executor.go`
- `domains/credentialstate/lifecycle.go`
- `domains/credentialstate/manager.go`
- `domains/ursm/api_record.go`
- `domains/ursm/conc_slot_manager.go`
- `domains/ursm/fp_slot_manager.go`
- `domains/ursm/batch_writer.go`
- `domains/ursm/manager.go`
- `admin/models.go`

### 判断标准
**需要用 runctx 的场景**：
- 请求结束后仍必须完成的 DB / Redis / 状态写回
- 异步补偿任务（probe submit / invalidate callback）
- 资源释放（release / cleanup / restore）

**不需要用 runctx 的场景**：
- 后台周期任务（本来就是独立生命周期）
- 测试代码
- 健康检查 / 定期刷新（本来就是独立生命周期）

## 是否有重复造轮子

### 当前状态
- **正向**: URSM 和 credentialstate 的 BatchWriter 虽然有类似逻辑，但业务语义不同，暂不合并。
- **正向**: `runctx` helper 已被核心模块正确复用，避免了各模块手写 `context.Background()+timeout`。
- **正向**: URSM 在路由规划阶段不再真实占用资源，避免了与 executor 的 `FpSlots + Limiter` 双重管理。

## 总结

这轮横向修复的核心目标已达成：
1. executor / credentialstate / URSM / admin 的"请求结束后善后写回"已全部脱离 request ctx
2. 唯一发现的高风险遗漏点（batch_writer 异步 Redis）已修复
3. `runctx` helper 在关键路径上已被正确复用
4. 没有发现新的"重复造轮子"问题

后续建议：
- 为状态写回失败增加 metrics
- 为 URSM `RecordRequest` 的状态机占位实现做完整修正
