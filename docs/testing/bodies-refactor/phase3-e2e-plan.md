# Phase 3: E2E 集成测试计划（Ticket #13）

## 目标

验证 Ticket #9-11 的完整业务流程，确保：
1. 双写逻辑在真实业务场景下正常工作
2. 查询 API 正确返回 bodies 数据
3. 性能符合预期（无显著退化）
4. 磁盘空间管理正确

## 测试用例设计

### Test Suite 1: 端到端业务流程

#### TC-E2E-001: 创建会话并请求 → 查询详情
**前置条件**: 
- 245 环境已部署最新代码 (34c41da19)
- PostgreSQL 252 可访问

**步骤**:
1. 通过 Admin API 创建会话
2. 发送 LLM 请求（带 request_body + response_body）
3. 等待 telemetry 双写完成（< 5s）
4. 调用 `GET /admin/logs/{request_id}` 查询详情
5. 验证响应包含完整 bodies

**预期结果**:
- ✅ request_logs_hot 有记录
- ✅ request_logs_bodies_hot 有对应记录
- ✅ GET API 返回 `request_body` 和 `response_body`
- ✅ bodies 内容与原始请求一致

#### TC-E2E-002: Null Bodies 场景
**步骤**:
1. 发送 LLM 请求但 bodies 为 null（如 streaming 场景）
2. 查询详情

**预期结果**:
- ✅ request_logs_hot 有记录
- ✅ request_logs_bodies_hot 无记录 或 bodies 为 null
- ✅ GET API 返回 `request_body: null, response_body: null`

#### TC-E2E-003: 向后兼容（旧记录）
**前置条件**: 
- Migration 353 之前的记录（request_logs_hot 有 bodies）

**步骤**:
1. 查询旧记录的 request_id
2. 调用 `GET /admin/logs/{old_request_id}`

**预期结果**:
- ✅ 返回 request_logs_hot.request_body（向后兼容）
- ✅ 无报错

### Test Suite 2: 性能验证

#### TC-PERF-001: 查询性能（有 bodies）
**步骤**:
1. EXPLAIN ANALYZE 查询带 bodies 的记录
2. 记录执行时间和扫描行数

**预期结果**:
- ✅ 执行时间 < 50ms
- ✅ 使用 Index Scan（不是 Seq Scan）
- ✅ 扫描行数 ≈ 1-2

#### TC-PERF-002: 查询性能（无 bodies）
**步骤**:
1. EXPLAIN ANALYZE 查询无 bodies 的记录

**预期结果**:
- ✅ 执行时间 < 10ms（比有 bodies 快）
- ✅ 只扫描 request_logs_hot

#### TC-PERF-003: 双写性能
**步骤**:
1. 发送 10 个并发请求
2. 观察 telemetry 延迟

**预期结果**:
- ✅ P50 延迟 < 5ms
- ✅ P99 延迟 < 20ms
- ✅ 无事务死锁

### Test Suite 3: 磁盘空间验证

#### TC-DISK-001: 表大小对比
**步骤**:
1. 查询两表大小：
   ```sql
   SELECT 
       pg_size_pretty(pg_total_relation_size('request_logs_hot')) AS metadata_size,
       pg_size_pretty(pg_total_relation_size('request_logs_bodies_hot')) AS bodies_size;
   ```

**预期结果**:
- ✅ bodies 表 > metadata 表（bodies 数据更大）
- ✅ 总大小 ≈ 原 request_logs_hot 大小（无显著增长）

#### TC-DISK-002: 索引大小
**步骤**:
1. 查询索引大小

**预期结果**:
- ✅ request_logs_bodies_hot 索引 < 原 request_logs_hot 索引
- ✅ 总索引大小无显著增长

### Test Suite 4: 业务查询验证

#### TC-BIZ-001: Session 聚合查询
**步骤**:
1. 查询某个 session 的所有请求
2. 验证 tokens 聚合正确

**预期结果**:
- ✅ 聚合查询无报错
- ✅ SUM(prompt_tokens) 正确
- ✅ 不受 bodies 表影响

#### TC-BIZ-002: Goal/Quality 查询
**步骤**:
1. 按 goal_id 查询请求
2. 验证 JOIN 正确

**预期结果**:
- ✅ JOIN 不受影响
- ✅ 查询速度无退化

## 测试执行策略

### 方案 A: 手动测试（快速验证）
1. SSH 到 245
2. 使用 curl 调用 Admin API
3. 手动执行 SQL 验证
4. 记录结果

**时间**: ~30 分钟
**优势**: 快速、直观
**劣势**: 不可重复、不自动化

### 方案 B: 自动化脚本（推荐）
1. 编写 Bash 测试脚本（`scripts/test-e2e-bodies.sh`）
2. 调用 Admin API + PostgreSQL
3. 断言结果
4. 生成测试报告

**时间**: ~1 小时（编写 + 执行）
**优势**: 可重复、可 CI 集成
**劣势**: 需要编写脚本

### 方案 C: Go 集成测试（严格）
1. 编写 Go 集成测试（`admin/integration_test.go`）
2. 使用 testcontainers 或真实 DB
3. 完整覆盖所有场景

**时间**: ~2 小时
**优势**: 最严格、可 CI
**劣势**: 需要解决 vendor 依赖问题

## 推荐执行顺序

1. **Phase 3A**: 手动测试（快速验证核心场景）
2. **Phase 3B**: 自动化脚本（可重复验证）
3. **Phase 3C**: Go 集成测试（长期维护）

---

**当前建议**: 先执行 Phase 3A（手动测试），快速验证核心业务场景。
