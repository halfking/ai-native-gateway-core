# W1 缺口① per-model 熔断器 - 部署验收清单

> **状态**: 准备就绪  
> **完成时间**: 2026-11-19  
> **验收目标**: 验证单模型故障不连坐行为

---

## 📋 验收清单

### 阶段 1：代码验收 ✅

- [x] 单元测试通过（13/13）
- [x] 集成测试通过（6/6）
- [x] 并发测试通过（race detector）
- [x] 完整测试套件通过（29.348s）
- [x] 编译验证通过
- [x] 代码推送到 main（commit: 7e2236089）

### 阶段 2：本地验证 ⏳

#### 2.1 前置条件

- [ ] PostgreSQL 运行中（llm_gateway 数据库）
- [ ] Redis 运行中
- [ ] 已构建最新 binary

#### 2.2 验证脚本

运行验证脚本：
```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
chmod +x scripts/verify-per-model-breaker.sh
./scripts/verify-per-model-breaker.sh
```

#### 2.3 预期结果

1. **场景 1**: gpt-4 熔断，gpt-3.5-turbo 正常
   - gpt-4 连续失败后 `circuit_state = open`
   - gpt-3.5-turbo 请求仍可通过
   - ✅ 验证：单模型故障不连坐

2. **场景 2**: 日志中的 per-model 键
   - 日志显示 `key=providerID/model`（如 `key=1/gpt-4`）
   - 不再是 `key=providerID/credentialID`
   - ✅ 验证：键格式正确

3. **场景 3**: DB 上卷态
   - `credentials.circuit_state` 反映最差状态
   - 多模型时取最差（Quarantined > Open > Closed）
   - ✅ 验证：上卷逻辑正确

### 阶段 3：测试环境部署 ⏳

#### 3.1 选择目标环境

推荐顺序（从低风险到高风险）：

1. **本地测试环境** (推荐首选)
   - 脚本: `scripts/deploy-local.sh`
   - 风险: 最低
   - 适用: 功能验证

2. **245 测试服务器**
   - 脚本: `scripts/deploy-245.sh`
   - 风险: 中等
   - 适用: 集成测试

3. **154 测试服务器**
   - 脚本: `scripts/deploy-154.sh`
   - 风险: 中等
   - 适用: 集成测试

4. **K8s 测试集群**
   - 路径: `deploy/k8s/`
   - 风险: 中等
   - 适用: K8s 环境验证

#### 3.2 部署步骤（以 245 为例）

```bash
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 1. 备份当前版本
./scripts/deploy-245.sh --backup

# 2. 部署新版本
./scripts/deploy-245.sh --deploy

# 3. 验证健康检查
curl http://245.itestu.cn:12112/health
curl http://245.itestu.cn:12112/readyz

# 4. 查看版本
curl http://245.itestu.cn:12112/version

# 5. 验证熔断器
# 见下方"阶段 4：功能验证"
```

#### 3.3 回滚预案

如发现问题，立即回滚：
```bash
./scripts/deploy-245.sh --rollback
```

### 阶段 4：功能验证 ⏳

#### 4.1 验证 per-model 隔离

**测试用例 1：单模型故障**

```bash
# 环境变量
TARGET="http://245.itestu.cn:12112"  # 或其他测试环境
API_KEY="your-test-api-key"

# 1. 记录初始状态
curl -sf "$TARGET/api/admin/credentials" \
  -H "Authorization: Bearer admin-token" \
  | jq '.[] | {id, provider, circuit_state}'

# 2. 模拟 gpt-4 故障（连续失败）
for i in {1..3}; do
  curl -sf -X POST "$TARGET/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "gpt-4",
      "messages": [{"role": "user", "content": "trigger failure"}]
    }' || echo "Expected failure $i"
  sleep 1
done

# 3. 验证 gpt-4 熔断
echo "期望: gpt-4 的 circuit_state = open"

# 4. 测试 gpt-3.5-turbo（应该正常）
curl -sf -X POST "$TARGET/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "test isolation"}]
  }'

echo "期望: gpt-3.5-turbo 请求成功，未被 gpt-4 熔断影响"
```

**测试用例 2：多模型上卷**

```bash
# 1. 触发多个模型熔断
# gpt-4 熔断 (Open)
# gpt-3.5-turbo 配额耗尽 (Quarantined)
# text-embedding-ada-002 正常 (Closed)

# 2. 查询 DB 上卷态
psql -h 245-db-host -U user -d llm_gateway -c \
  "SELECT id, provider_id, circuit_state, circuit_opened_at 
   FROM credentials WHERE provider_id = 1;"

# 期望: circuit_state = 'open' (Quarantined 映射为 open)
# 说明: 上卷态反映最差状态
```

#### 4.2 验证日志

```bash
# 查看 Gateway 日志
tail -f /var/log/llm-gateway-go/gateway.log | grep "circuit"

# 期望输出示例:
# WARN circuit opened key=1/gpt-4 previous_state=closed new_state=open
# WARN circuit quarantined key=1/gpt-3.5-turbo previous_state=closed new_state=quarantined
# INFO circuit closed key=1/gpt-4 previous_state=open new_state=closed

# 验证点:
# 1. key 格式为 "providerID/model"（不是 "providerID/credentialID"）
# 2. 不同模型独立记录状态变化
```

#### 4.3 验证监控指标

```bash
# 查看 Prometheus 指标
curl -sf http://245.itestu.cn:12112/metrics | grep circuit

# 期望指标:
# llm_gateway_circuit_state{provider="openai",model="gpt-4",credential="100"} 1.0  # Open
# llm_gateway_circuit_state{provider="openai",model="gpt-3.5-turbo",credential="100"} 0.0  # Closed
```

### 阶段 5：性能验证 ⏳

#### 5.1 并发压测

```bash
# 使用 wrk 或 ab 进行压测
wrk -t4 -c100 -d30s \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  --script=post.lua \
  http://245.itestu.cn:12112/v1/chat/completions

# 期望:
# 1. 无明显性能下降
# 2. 熔断器正常工作
# 3. 无 race condition
```

#### 5.2 内存检查

```bash
# 查看 Gateway 内存占用
ps aux | grep gateway

# 监控一段时间后检查:
# 1. 无内存泄漏
# 2. breaker map 大小合理
```

### 阶段 6：回归测试 ⏳

#### 6.1 现有功能验证

- [ ] 旧接口仍可用（向后兼容）
- [ ] DB 镜像正常
- [ ] 路由决策正常
- [ ] 健康检查正常
- [ ] 管理 API 正常

#### 6.2 集成测试

- [ ] 与前端集成正常
- [ ] 与监控集成正常
- [ ] 与告警集成正常

---

## ✅ 验收标准

### 功能验收

- [ ] 单模型故障不连坐其他模型
- [ ] DB 上卷态反映最差状态
- [ ] 日志中显示 per-model 键
- [ ] 旧接口向后兼容

### 性能验收

- [ ] 响应时间无明显增加
- [ ] 并发性能无下降
- [ ] 无内存泄漏
- [ ] 无 race condition

### 稳定性验收

- [ ] 运行 24 小时无异常
- [ ] 熔断恢复正常
- [ ] DB 一致性保持
- [ ] 日志无错误

---

## 📊 验收报告模板

### 验收环境

- **环境**: [本地/245/154/K8s]
- **版本**: 7e2236089
- **时间**: YYYY-MM-DD HH:MM

### 测试结果

| 测试项 | 预期 | 实际 | 状态 |
|--------|------|------|------|
| 单模型隔离 | gpt-4 熔断不影响 gpt-3.5 | | ⏳ |
| 上卷态 | 最差状态 | | ⏳ |
| 日志键格式 | providerID/model | | ⏳ |
| 向后兼容 | 旧接口可用 | | ⏳ |
| 性能 | 无明显下降 | | ⏳ |

### 问题记录

| 问题 | 严重程度 | 状态 | 解决方案 |
|------|----------|------|----------|
| - | - | - | - |

### 结论

- [ ] ✅ 通过验收，可以发布
- [ ] ⚠️  部分问题，需修复
- [ ] ❌ 未通过，需回滚

---

## 🔗 相关文档

- [实施计划](../docs/benchmark-dsh-octop-2026-11-18/B0-W1-per-model-implementation-plan.md)
- [进度跟踪](../docs/benchmark-dsh-octop-2026-11-18/B0-W1-per-model-progress.md)
- [验收报告](../docs/benchmark-dsh-octop-2026-11-18/B0-W1-per-model-acceptance-report.md)

---

**创建时间**: 2026-11-19  
**最后更新**: 2026-11-19
