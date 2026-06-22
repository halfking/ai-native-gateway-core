# 指纹槽复位功能 (2026-06-23)

## 问题分析

### 症状
在 `https://llmgo.kxpms.cn/providers/587` 页面，发现指纹槽的"已占用"数量只涨不降，即使请求已经完成，槽位也没有被释放。

### 根因分析

通过代码审查发现三个潜在问题：

1. **Slot TTL 设计**：
   - `slotTTLSeconds = 3600`（1小时）
   - 每次 `Acquire` 时用 `SET key holder NX EX 3600` 设置 TTL
   - `Release` 时只删除 slot key，但不删除 pin key
   - Pin 的 TTL 是 `sessionPinTTLSeconds = 1800`（30分钟）

2. **清理依赖 defer**：
   - `routing/executor.go:689` 中通过 defer 调用 `Release`
   - 如果网关重启、程序 panic 或其他异常情况，defer 可能不会执行

3. **Redis key 未过期**：
   - Redis key 可能因各种原因没有正确过期
   - 导致 `AvailableCount()` 通过 `EXISTS` 检查时仍然认为槽位被占用

### 关键代码路径

```
routing/executor.go:675-691
  ├─ defer 包装清理逻辑
  ├─ PeakCollector.Release (line 685)
  ├─ Limiter release() (line 687)
  └─ FpSlots.Release (line 689)  ← 这是释放槽位的唯一途径

credentialfpslot/slot.go:157-175 (Release 方法)
  ├─ releaseSlotScript: DEL slotKey (line 209)
  └─ 保留 pinKey 以便会话重用 (设计意图)
```

## 解决方案

### 新增功能：ResetSlots

在 `credentialfpslot.Manager` 中新增 `ResetSlots` 方法，用于强制清空指定凭据的所有槽位和 pin。

**核心实现**：

```go
// credentialfpslot/slot.go:447-504
func (m *Manager) ResetSlots(ctx context.Context, credentialID int, limit *int) (int, int, error)
```

**特性**：
- 删除所有 slot keys: `llmgw:cred_fp_slot:{credentialID}:{0..limit-1}`
- 扫描并删除所有 pin keys: `llmgw:sess_cred_fp:*:{credentialID}`
- 使用 Lua 脚本保证原子性
- 支持内存模式 fallback
- 返回删除的 slot 和 pin 数量

### API Endpoint

**路由**: `POST /api/providers/{provider_id}/credentials/{cred_id}/reset-fp-slots`

**权限**: `superAdmin`（继承自父路由 `/api/providers/*`）

**实现**: `admin/provider_credential.go:349-400`

**响应示例**：
```json
{
  "message": "reset completed",
  "deleted_slots": 3,
  "deleted_pins": 3
}
```

### 前端集成

**位置**: `web/src/views/provider-detail/CredsTab.vue`

**UI 变更**：
- 在"并发与有效期"区块的"指纹槽"字段旁边添加"复位槽位"按钮
- 按钮仅在 `fp_slot_limit != null` 时显示（即启用了槽位限制的凭据）
- 点击后弹出确认对话框
- 成功后显示删除的槽位和 pin 数量

**API 调用**: `web/src/api/providers.ts:496-500`

```typescript
export function resetCredentialFpSlots(providerId: number, credId: number) {
  return req<{ message: string; deleted_slots: number; deleted_pins: number }>(
    'POST', `/api/providers/${providerId}/credentials/${credId}/reset-fp-slots`
  )
}
```

## 测试覆盖

新增 `credentialfpslot/slot_reset_test.go`，包含 6 个测试用例：

1. ✅ `TestResetSlots_Redis` - Redis 模式正常重置
2. ✅ `TestResetSlots_Memory` - 内存模式正常重置
3. ✅ `TestResetSlots_Disabled` - 功能禁用时不报错
4. ✅ `TestResetSlots_UnlimitedCredential` - 无限制凭据（limit=0/-1）不操作
5. ✅ `TestResetSlots_PartialOccupancy` - 部分占用时正确清理
6. ✅ `TestResetSlots_ExpiredSlotsStillCounted` - 过期槽位仍然可清理

所有测试通过。

## 使用场景

### 何时使用复位按钮

1. **槽位卡死**：前端显示 `fp_slots_used` 长时间不下降
2. **网关重启后**：重启可能导致 defer 未执行
3. **异常流量后**：大量并发请求后槽位未释放
4. **手动恢复**：凭据健康检查失败后需要快速恢复

### 操作步骤

1. 进入 `https://llmgo.kxpms.cn/providers/{provider_id}`
2. 在"凭据列表"中点击目标凭据
3. 在右侧抽屉的"并发与有效期"区块中，点击"复位槽位"按钮
4. 确认对话框中点击"确定"
5. 查看成功提示，显示清空的槽位和 pin 数量
6. 刷新页面，验证 `fp_slots_used` 已归零

### 注意事项

⚠️ **复位是破坏性操作**：
- 会立即清空所有正在使用的槽位
- 正在进行的请求不会被中断（它们持有的 lease 对象仍然有效）
- 但这些请求在 `Release` 时会发现 slot key 已经不存在（幂等操作，不会报错）
- 新请求可以立即获取槽位

⚠️ **不影响并发限制器**：
- 指纹槽只是虚拟身份池，与 `Limiter.AcquireAll` 的并发限制独立
- 复位槽位不会释放并发限制器的 permit

## 代码变更清单

### 后端
- ✅ `credentialfpslot/slot.go` - 新增 `ResetSlots` 方法 + `resetSlotsScript` Lua 脚本
- ✅ `admin/provider_credential.go` - 新增 `resetCredentialFpSlots` handler
- ✅ `admin/providers.go` - 注册路由 `reset-fp-slots`
- ✅ `credentialfpslot/slot_reset_test.go` - 新增 6 个测试用例

### 前端
- ✅ `web/src/api/providers.ts` - 新增 `resetCredentialFpSlots` API 函数
- ✅ `web/src/views/provider-detail/CredsTab.vue` - 添加复位按钮 + `resetFpSlots` 函数
- ✅ 样式：新增 `.btn-warning-outline` class

## 部署检查清单

- [ ] 后端编译通过：`go build ./cmd/gateway`
- [ ] 测试通过：`go test ./credentialfpslot -run TestResetSlots`
- [ ] 前端构建：`cd web && npm run build`
- [ ] 184 部署：`./scripts/deploy-llm-gateway-go-184.sh`
- [ ] 71 部署：`./scripts/deploy-llm-gateway-go-71.sh`
- [ ] 验证 API：`curl -X POST https://llmgo.kxpms.cn/api/providers/587/credentials/{cred_id}/reset-fp-slots -H "Authorization: Bearer <token>"`
- [ ] 前端验证：在 Chrome DevTools 中验证"复位槽位"按钮可见且点击成功

## 未来优化方向

1. **自动垃圾回收**：
   - 添加后台 worker 定期扫描过期的 slot keys
   - 类似 `bg.EnvelopeCleaner` 的设计

2. **监控告警**：
   - 当 `fp_slots_used` 接近 `fp_slot_limit` 且超过阈值时长，发送告警
   - 集成到 `bg.CredentialProbeV2` 健康检查

3. **Release 失败重试**：
   - 在 `executor.go` 的 defer 中捕获 Release 失败
   - 加入异步重试队列

4. **Slot 泄漏检测**：
   - 对比 Redis 中的实际槽位数量与请求日志中的活跃请求数
   - 自动触发清理

## 参考文档

- [credentialfpslot 包设计](../credentialfpslot/slot.go) - 指纹槽核心逻辑
- [routing executor](../routing/executor.go:675-691) - 槽位清理调用点
- [llm-gateway-go 部署红线](../../AGENTS.md#-llm-gateway-go-部署红线2026-06-22-起硬性规范) - 部署规范
