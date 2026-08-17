---
archived_from: (legacy) docs/archive/2026-08/LOCAL_TEST_PLAN_20260813.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190917
status: archived
note: legacy archive, frontmatter retroactively added
---

# 本地集成测试计划 - 路由 Fail-Safe 机制

> **测试目标**: 验证三层 Fail-Safe 机制在本地环境的完整功能  
> **测试日期**: 2026-08-13  
> **前置条件**: 本地 PostgreSQL 运行中，已有测试数据

---

## 📋 测试环境准备

### 1. 数据库准备

```bash
# 确认 PostgreSQL 运行
psql -h localhost -U postgres -c "SELECT version();"

# 创建测试数据库 (如果不存在)
psql -h localhost -U postgres << 'SQL'
CREATE DATABASE llm_gateway_test;
SQL

# 导入测试 schema
psql -h localhost -U postgres -d llm_gateway_test -f deploy/sql/schema.sql

# 插入测试路由计划
psql -h localhost -U postgres -d llm_gateway_test << 'SQL'
INSERT INTO model_offers (model, provider, endpoint, success_rate) VALUES
  ('gpt-4', 'openai', 'https://api.openai.com', 0.95),
  ('claude-3', 'anthropic', 'https://api.anthropic.com', 0.98);
SQL
```

### 2. 网关配置

```bash
# 创建测试配置
cat > config.test.yaml << 'YAML'
database:
  host: localhost
  port: 5432
  user: postgres
  password: ${LOCAL_PG_PASSWORD}
  database: llm_gateway_test
  pool_size: 10

routing:
  cache_ttl: 30s
  max_retry: 3
YAML
```

### 3. 编译测试二进制

```bash
# 编译
go build -o /tmp/llm-gateway-test .

# 验证编译
/tmp/llm-gateway-test --version
```

---

## 🧪 测试用例

### Test Case 1: 正常场景 - 数据库可用

**目标**: 验证正常情况下路由查询工作正常

**步骤**:
```bash
# 1. 启动网关
/tmp/llm-gateway-test --config config.test.yaml &
GATEWAY_PID=$!

# 2. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "test"}]
  }'

# 3. 检查日志
grep "candidate_diag" /var/log/llm-gateway.log | tail -10
```

**预期结果**:
- ✅ 请求成功返回 200
- ✅ 日志显示数据库查询成功
- ✅ 没有 "stale cache" 警告

---

### Test Case 2: 网络抖动 - 数据库查询第1次失败，重试成功

**目标**: 验证 Layer 1 (数据库重试) 机制

**步骤**:
```bash
# 1. 使用 iptables 模拟网络抖动 (阻塞 100ms)
# 需要 root 权限
sudo iptables -A OUTPUT -p tcp --dport 5432 -j DROP
sleep 0.1
sudo iptables -D OUTPUT -p tcp --dport 5432 -j DROP

# 2. 同时发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "test"}]
  }'

# 3. 检查日志
grep "db query retry" /var/log/llm-gateway.log | tail -5
```

**预期结果**:
- ✅ 请求成功返回 200 (可能延迟 50-150ms)
- ✅ 日志显示 "db query retry" 警告
- ✅ 显示重试次数和退避时间
- ✅ 最终查询成功

---

### Test Case 3: 数据库短暂不可达 - 使用过期缓存

**目标**: 验证 Layer 2 (过期缓存降级) 机制

**步骤**:
```bash
# 1. 确保已有缓存 - 先发送正常请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 2. 等待 5 秒让缓存过期 (TTL=30s，但标记为过期)
sleep 5

# 3. 停止 PostgreSQL
sudo systemctl stop postgresql
# 或 Docker: docker stop postgres-test

# 4. 发送测试请求 (此时数据库不可达)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 5. 检查日志
grep "database unavailable, serving stale cache" /var/log/llm-gateway.log | tail -5

# 6. 恢复数据库
sudo systemctl start postgresql
```

**预期结果**:
- ✅ 请求成功返回 200
- ✅ 日志显示 "database unavailable, serving stale cache"
- ✅ 日志包含 cache_age (如 "5s")
- ✅ 日志包含 db_error 信息
- ✅ 返回的路由计划来自过期缓存

---

### Test Case 4: 数据库持续不可达 + 无缓存 - 返回错误

**目标**: 验证 Layer 3 (真正失败) 场景

**步骤**:
```bash
# 1. 清空缓存并重启网关
killall llm-gateway-test
/tmp/llm-gateway-test --config config.test.yaml &

# 2. 立即停止数据库 (不给缓存建立的时间)
sudo systemctl stop postgresql

# 3. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 4. 检查响应
# 预期: 500 错误

# 5. 恢复数据库
sudo systemctl start postgresql
```

**预期结果**:
- ❌ 请求返回 500 错误
- ✅ 错误消息: "No available provider"
- ✅ 日志显示数据库查询失败 (重试 3 次)
- ✅ 日志显示缓存未命中
- ✅ 这是不可避免的失败场景

---

### Test Case 5: 数据库恢复 - 自动切回最新数据

**目标**: 验证数据库恢复后自动使用最新数据

**步骤**:
```bash
# 1. 在 Test Case 3 基础上，数据库已恢复

# 2. 等待缓存完全过期 (30秒)
sleep 30

# 3. 更新数据库中的路由计划
psql -h localhost -U postgres -d llm_gateway_test << 'SQL'
UPDATE model_offers SET success_rate = 0.99 WHERE model = 'gpt-4';
SQL

# 4. 发送测试请求
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 5. 检查日志和响应
grep "candidate_diag" /var/log/llm-gateway.log | tail -5
```

**预期结果**:
- ✅ 请求成功返回 200
- ✅ 日志显示数据库查询成功 (不再使用 stale cache)
- ✅ 返回的路由计划反映最新的 success_rate = 0.99
- ✅ 系统自动恢复到正常状态

---

### Test Case 6: 并发请求 - 验证线程安全

**目标**: 验证 Fail-Safe 机制在高并发下的稳定性

**步骤**:
```bash
# 1. 停止数据库 (模拟故障)
sudo systemctl stop postgresql

# 2. 并发发送 100 个请求
for i in {1..100}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}' &
done

# 3. 等待所有请求完成
wait

# 4. 检查日志中是否有 panic 或 race condition
grep -E "(panic|race|concurrent)" /var/log/llm-gateway.log

# 5. 恢复数据库
sudo systemctl start postgresql
```

**预期结果**:
- ✅ 没有 panic
- ✅ 没有 race condition 错误
- ✅ 缓存访问线程安全 (mu.RLock/RUnlock 生效)
- ✅ 所有请求要么成功 (stale cache) 要么一致失败

---

## 📊 测试结果记录表

| Test Case | 状态 | 响应码 | 延迟 | 日志关键字 | 备注 |
|-----------|------|--------|------|-----------|------|
| TC1: 正常场景 | ⏳ | - | - | - | - |
| TC2: 网络抖动 | ⏳ | - | - | - | - |
| TC3: 过期缓存降级 | ⏳ | - | - | - | - |
| TC4: 真正失败 | ⏳ | - | - | - | - |
| TC5: 自动恢复 | ⏳ | - | - | - | - |
| TC6: 并发测试 | ⏳ | - | - | - | - |

---

## 🔍 关键日志监控

### 正常查询日志
```
[candidate_diag] load from db
  model=gpt-4
  tenant_id=default
  plan_count=1
  candidate_count=1
```

### 重试日志
```
[candidate_diag] db query retry
  model=gpt-4
  attempt=2
  max_attempts=3
  backoff_ms=100
  error=connection refused
```

### 过期缓存日志
```
[candidate_diag] database unavailable, serving stale cache
  model=gpt-4
  cache_age=5s
  plan_count=1
  candidate_count=1
  db_error=dial tcp: connection refused
```

---

## ✅ 通过标准

测试被视为**通过**当且仅当:

1. ✅ TC1-TC6 所有测试用例通过
2. ✅ 没有 panic 或 crash
3. ✅ 日志输出符合预期
4. ✅ Layer 1, 2, 3 分别触发并正常工作
5. ✅ 并发场景无 race condition
6. ✅ 数据库恢复后自动使用最新数据

---

## 🚫 失败处理

如果任何测试用例失败:

1. **记录详细错误信息**
   - 错误日志
   - 堆栈跟踪
   - 请求/响应数据

2. **诊断根因**
   - 检查代码逻辑
   - 验证假设条件
   - 复现步骤

3. **修复并重测**
   - 修改代码
   - 重新编译
   - 重新执行失败的测试用例

4. **记录修复过程**
   - 更新文档
   - 标注变更点

---

## 📝 测试完成检查清单

- [ ] 所有 6 个测试用例执行完毕
- [ ] 测试结果记录表填写完整
- [ ] 关键日志截图保存
- [ ] 性能指标 (延迟) 记录
- [ ] 问题清单整理 (如有)
- [ ] 测试报告生成

---

**测试负责人**: AI Agent  
**预计耗时**: 1-2 小时  
**下一步**: 执行测试 → 生成报告 → 创建 PR
