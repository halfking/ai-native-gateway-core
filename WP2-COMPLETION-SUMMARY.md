# WP2 完成总结

## 执行情况

✅ **WP2 已完成** - Gateway 与 ai-session-manager 事件契约对账的测试基线已建立

## 交付物清单

### 📋 文档（3 个）

1. **`docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md`** (7.2K)
   - 5 个 v1 事件的字段定义
   - Gateway 当前实现 vs ASM 契约的 diff
   - 10 个缺失字段 + 1 个违规字段标识

2. **`test/events/README.md`** (实施指南)
   - 测试框架使用说明
   - Phase 2 实施路线图
   - 验收标准定义

3. **`docs/修订0811/07-WP2-实施报告.md`** (9.4K)
   - 完整实施过程记录
   - 关键发现和风险分析
   - 下一步行动指南

### 🧪 测试框架（1 个测试文件 + 6 个 fixtures）

**测试代码**：
- `test/events/contract/request_completed_test.go` (7 个测试用例)

**Fixtures**：
- `request_completed_v1_valid.json` - 正例
- `request_completed_v1_duplicate.json` - 负例：重复检测
- `request_completed_v1_stale.json` - 负例：版本顺序
- `request_completed_v1_tamper.json` - 负例：签名篡改
- `request_completed_v1_tenant_mismatch.json` - 负例：tenant 不一致
- `request_completed_v1_forbidden_fields.json` - 负例：禁止字段

## 测试结果

```
$ go test ./test/events/contract/... -v

6 tests SKIPPED (预期) - 等待 outbox 实现
1 test FAILED (预期) - 暴露当前实现的差距

TestCurrentGatewayPublisher:
  ❌ Missing 10 required fields
  ❌ VIOLATION: user_content (prompt text) in payload
  ⚠️  MISMATCH: status_code (int) vs status (enum)
```

## 关键发现

### 当前状态分析

| 维度 | 当前 | 契约要求 | 差距 |
|---|---|---|---|
| Payload 字段数 | 4 | 11 | -7 (63%) |
| 必填字段覆盖 | 1/11 | 11/11 | 10 个缺失 |
| 禁止字段遵守 | ❌ | ✅ | 1 个违规 |
| Outbox 实现 | ❌ | ✅ | 完全缺失 |
| 签名机制 | ❌ | ✅ | 完全缺失 |

### 架构缺失

- ❌ `outbox_events` 表不存在
- ❌ OutboxWriter 未实现（无法与事务同提交）
- ❌ OutboxDispatcher 未实现（无法投递到 ASM）
- ❌ HMAC 签名器未实现
- ❌ DLQ 表不存在

## 遵守的约束

✅ **所有 WP2 约束已遵守**：

- ✅ 只读核验，未修改生产代码
- ✅ 测试先行，建立验收标准
- ✅ 未触碰 routing、compression、SessionCacheV2
- ✅ handshake 保持 `event_mode: none`
- ✅ 未添加未审批的 routing/compression metadata

## 下一步（Phase 2）

### 前置条件

✅ 已满足：
- 字段映射表完成
- 测试框架就绪
- 负例测试可复现

### 实施顺序

1. **创建 Outbox 表** - SQL DDL
2. **实现 OutboxWriter** - 与事务同提交
3. **补全 10 个字段** - 修改 main_pipeline.go:845
4. **删除 1 个违规字段** - 移除 `user_content`
5. **实现 OutboxDispatcher** - 后台投递 + 重试
6. **启用测试** - 取消 SKIP，验证通过

### 验收标准

Phase 2 完成的定义：

- [ ] 所有 7 个测试 ✅ PASS
- [ ] `TestCurrentGatewayPublisher` 报告 0 个缺失字段
- [ ] 负例测试返回正确的错误代码（duplicate/stale/401/403）

## 提交建议

```bash
git add docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md
git add docs/修订0811/07-WP2-实施报告.md
git add test/events/
git commit -m "test(events): add gateway-asm v1 contract fixtures for request.completed

WP2 deliverables:
- Field mapping table identifying 10 missing + 1 violating fields
- 6 test fixtures (1 valid + 5 negative scenarios)
- Contract test suite (7 tests: 6 skipped, 1 intentionally failing)
- Current implementation gap analysis

Test results:
- TestCurrentGatewayPublisher: FAIL (expected) - exposes 10 missing fields
- 6 contract tests: SKIP - awaiting outbox implementation

Next: Phase 2 - implement outbox, complete fields, enable tests

Refs: docs/修订0811/06-下一阶段实施计划.md WP2"
```

## 时间消耗

- 文档阅读与分析：15 分钟
- 字段映射表创建：20 分钟
- Fixtures 设计与编写：15 分钟
- 测试代码编写：25 分钟
- 文档撰写：15 分钟
- **总计：约 90 分钟**

## 影响评估

### 风险

✅ **零风险** - 仅添加测试和文档，未修改生产代码

### 依赖

- **无外部依赖** - WP2 是独立的测试基线
- **被依赖** - Phase 2 的验收标准由 WP2 定义

### 回滚

✅ **完全可回滚** - `git revert` 即可，无运行时影响

## 建议

### 立即可做

1. ✅ 提交 WP2 成果
2. ✅ Review 字段映射表和测试用例
3. ⬜ 设计 Phase 2 的数据库 schema

### Phase 2 前的准备

1. **设计决策**：
   - `correlation_id` 和 `idempotency_key` 生成规则
   - `body_refs` 格式设计
   - `turn_no` 数据源（查表 vs 内存计数）

2. **依赖注入**：
   - OutboxWriter 的事务管理
   - OutboxDispatcher 的后台调度
   - HMAC 签名的 secret 来源

3. **Mock ASM**：
   - 创建 ASM mock endpoint 用于测试
   - 实现签名校验和 tenant 校验
   - 返回正确的错误码

---

**WP2 状态：✅ 完成**

测试框架已就绪，可作为 Phase 2 实现的验收标准。
