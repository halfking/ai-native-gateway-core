# GLM-5.2 降级问题修复 - 部署检查清单

**执行人**: ___________  
**执行日期**: ___________  
**开始时间**: ___________  
**完成时间**: ___________

---

## ☑️ 预检查 (Pre-deployment)

- [ ] 确认问题症状: GLM-5.2 频繁被降级（在 245 服务器日志中看到 "continuous_failure"）
- [ ] 备份当前配置: `pg_dump -h 172.31.86.245 -U postgres -t credential_model_bindings > backup.sql`
- [ ] 记录当前状态: 
  - GLM-5.2 当前可用性: _____%
  - 过去 1 小时降级次数: _____
  - 当前 admin_protected: [ ] TRUE  [ ] FALSE

---

## 🚀 方案选择

选择一个方案执行：

### 方案 A: 紧急修复 (仅数据库，5 分钟) ⭐ 推荐先执行

**适用场景**: 生产环境紧急恢复，不能重启服务

**执行步骤**:

```bash
# 1. 设置数据库密码
export DB_PASSWORD=your_password

# 2. 执行快速修复脚本
cd /path/to/llm-gateway-go-2
bash scripts/quick-fix-glm5.2.sh
```

**检查点**:
- [ ] 脚本显示 "✓ 数据库连接成功"
- [ ] 找到 credential_id: _____
- [ ] 显示 "✓ GLM-5.2 已可路由"
- [ ] admin_protected = TRUE, manual_priority = 100

### 方案 B: 完整修复 (代码+数据库，30 分钟)

**适用场景**: 测试环境或可维护窗口，彻底解决问题

**执行步骤**:

```bash
# 1. 设置环境变量
export SERVER_HOST=172.31.86.245
export DB_HOST=172.31.86.245
export DB_PASSWORD=your_password
export PROVIDER_NAME=sp1
export CREDENTIAL_LABEL=spi-3

# 2. 执行完整修复脚本
cd /path/to/llm-gateway-go-2
bash scripts/fix-glm5.2-degradation.sh
```

**检查点**:
- [ ] 代码编译成功
- [ ] 单元测试通过
- [ ] 服务重启成功
- [ ] 数据库修复完成
- [ ] 验证通过

---

## ✅ 部署后验证 (Post-deployment)

### 1. 数据库验证 (5 分钟)

```sql
-- 检查 GLM-5.2 是否可路由
SELECT credential_id, credential_label, is_routable
FROM v_routable_credential_models
WHERE raw_model_name = 'glm-5.2' 
  AND credential_label LIKE '%spi-3%';
```

**预期结果**: is_routable = true

- [ ] ✅ 可路由
- [ ] ❌ 不可路由（记录原因: ___________）

### 2. 日志验证 (10 分钟)

```bash
ssh root@172.31.86.245 "cd /data/llm-gateway && \
  docker-compose logs --tail=100 llm-gateway | \
  grep -i 'glm-5.2' | tail -20"
```

**检查**:
- [ ] 没有 "credential degraded" 日志
- [ ] 没有 "continuous_failure" 日志
- [ ] 看到正常的请求成功日志

### 3. 业务验证 (5 分钟)

发送 10 次测试请求:

```bash
for i in {1..10}; do
  curl -s -X POST http://172.31.86.245:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer your-api-key" \
    -d '{
      "model": "glm-5.2",
      "messages": [{"role": "user", "content": "测试'$i'"}],
      "stream": false
    }' | jq -r '.choices[0].message.content' || echo "请求失败"
  sleep 1
done
```

**统计**:
- 成功次数: _____/10
- 失败次数: _____/10
- 成功率: _____%

- [ ] ✅ 成功率 ≥ 80%
- [ ] ❌ 成功率 < 80%（记录错误: ___________）

### 4. Prometheus 指标验证 (15 分钟)

访问 Prometheus/Grafana，观察以下指标（15 分钟时间窗口）：

```promql
# 1. 降级频率
rate(credential_degradation_total{model="glm-5.2"}[5m])
```
**预期**: 接近 0

- [ ] ✅ 降级频率 < 0.1/分钟
- [ ] ❌ 降级频率 ≥ 0.1/分钟

```promql
# 2. 成功率
sum(rate(request_total{model="glm-5.2", status="success"}[5m]))
/
sum(rate(request_total{model="glm-5.2"}[5m]))
```
**预期**: > 0.90

- [ ] ✅ 成功率 > 90%
- [ ] ❌ 成功率 ≤ 90%

```promql
# 3. rate_limit 错误占比
rate(request_errors_total{model="glm-5.2", error_kind="rate_limit"}[1m])
/
rate(request_total{model="glm-5.2"}[1m])
```
**预期**: < 0.05

- [ ] ✅ rate_limit 占比 < 5%
- [ ] ❌ rate_limit 占比 ≥ 5%

---

## 📊 持续观察 (30 分钟 - 2 小时)

### 时间点 1: 15 分钟后

**时间**: ___________

**指标**:
- 降级次数: _____
- 成功率: _____%
- 平均延迟: _____ms

**状态**: [ ] 正常  [ ] 异常（描述: ___________）

### 时间点 2: 30 分钟后

**时间**: ___________

**指标**:
- 降级次数: _____
- 成功率: _____%
- 平均延迟: _____ms

**状态**: [ ] 正常  [ ] 异常（描述: ___________）

### 时间点 3: 1 小时后

**时间**: ___________

**指标**:
- 降级次数: _____
- 成功率: _____%
- 平均延迟: _____ms

**状态**: [ ] 正常  [ ] 异常（描述: ___________）

### 时间点 4: 2 小时后

**时间**: ___________

**指标**:
- 降级次数: _____
- 成功率: _____%
- 平均延迟: _____ms

**状态**: [ ] 正常  [ ] 异常（描述: ___________）

---

## 🚨 回滚计划

**触发条件**（满足任一即回滚）:
- [ ] 成功率持续 < 70%（30 分钟）
- [ ] 降级频率 > 5 次/小时（持续 1 小时）
- [ ] 出现新的异常错误（非 rate_limit）

**回滚步骤**:

### 方案 A 回滚（数据库）:

```sql
-- 取消保护
UPDATE credential_model_bindings cmb
SET 
    admin_protected = FALSE,
    manual_priority = 0,
    updated_at = NOW()
FROM provider_models pm
WHERE cmb.provider_model_id = pm.id
  AND cmb.credential_id = <CREDENTIAL_ID>
  AND pm.raw_model_name = 'glm-5.2';

-- 恢复备份（如有必要）
psql -h 172.31.86.245 -U postgres llm_gateway < backup.sql
```

### 方案 B 回滚（代码）:

```bash
ssh root@172.31.86.245 "cd /data/llm-gateway && \
  mv llm-gateway-go.backup.$(date +%Y%m%d)* llm-gateway-go && \
  docker-compose restart llm-gateway"
```

**回滚执行时间**: ___________  
**回滚完成时间**: ___________  
**回滚结果**: [ ] 成功  [ ] 失败（描述: ___________）

---

## ✍️ 总结

### 执行结果

- [ ] ✅ 完全成功 - GLM-5.2 可用性恢复，降级频率下降
- [ ] ⚠️ 部分成功 - 可用性提升，但仍有少量降级
- [ ] ❌ 失败 - 已回滚（原因: ___________）

### 关键指标对比

| 指标 | 修复前 | 修复后 | 改进 |
|------|--------|--------|------|
| 可用性 | ____% | ____% | ____% |
| 降级频率 | ____次/小时 | ____次/小时 | ____% |
| 成功率 | ____% | ____% | ____% |

### 遗留问题

1. ___________________________________________
2. ___________________________________________
3. ___________________________________________

### 后续行动

- [ ] 更新监控告警阈值
- [ ] 创建 GLM-5.2 专用仪表盘
- [ ] 一周后复查效果
- [ ] 考虑推广到其他智谱模型

### 经验教训

1. ___________________________________________
2. ___________________________________________
3. ___________________________________________

---

**签字确认**:

执行人: ___________  日期: ___________

审核人: ___________  日期: ___________

---

**附件**:
- 详细分析报告: `.handoff/2026-08-29-glm5.2-degradation-analysis.md`
- 执行总结: `.handoff/2026-08-29-glm5.2-fix-summary.md`
- 数据库脚本: `sql/fix-glm5.2-degradation.sql`
- 部署脚本: `scripts/fix-glm5.2-degradation.sh`
- 快速修复: `scripts/quick-fix-glm5.2.sh`
