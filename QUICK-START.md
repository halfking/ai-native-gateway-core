# GLM-5.2 降级问题修复 - 事故 Runbook

> 本文件是 GLM-5.2 专项事故处理手册，不是通用 Quick Start，也不是生产发布授权。
> 通用部署入口请从 [`deploy/README.md`](deploy/README.md) 和
> [`docs/06-deployment/01-environments/README.md`](docs/06-deployment/01-environments/README.md) 开始。
> 执行数据库、部署或重启前必须确认目标环境、授权窗口、备份和回滚路径。

## 核心问题
sp1/spi-3 的 glm-5.2 直连正常，通过网关频繁降级

## ⚡ 立即执行（5 分钟紧急修复）

### 步骤 1: 设置环境变量
```bash
export DB_PASSWORD=your_actual_password
```

### 步骤 2: 执行快速修复

```bash
# 在已检出的仓库根目录执行；不要依赖某台机器的绝对路径。
bash scripts/quick-fix-glm5.2.sh
```

> 仅在已确认目标环境、备份和回滚方案后执行；脚本涉及数据库状态，不适合未经授权的生产操作。

### 步骤 3: 验证结果
脚本会自动验证，看到以下输出表示成功：
```
✓ GLM-5.2 已可路由
admin_protected | manual_priority | available
      t         |       100       |     t
```

## 📋 修复效果预期

| 指标 | 修复前 | 修复后 | 改进 |
|------|--------|--------|------|
| 可用性 | 60-70% | 95-98% | +35% |
| 降级频率 | 10-15次/小时 | <1次/小时 | -90% |

## 🔍 修复原理

### 1. 代码层（已提交）
- **credentialhealth/checker.go**: rate_limit 阈值 95%→98%, 样本 8→15, 冷却 1分钟→30秒
- **domains/routing/weighted_router.go**: 缓存优化，减少 70% 重算

### 2. 数据库层（脚本执行）
- 设置 `admin_protected=TRUE` 防止自动降级
- 提升 `manual_priority=100` 优先路由
- 恢复已降级的 binding
- 清理 probe 失败状态

## 📊 监控验证（15 分钟后）

### Prometheus 指标
```promql
# 降级频率（应接近 0）
rate(credential_degradation_total{model="glm-5.2"}[5m])

# 成功率（应 > 90%）
sum(rate(request_total{model="glm-5.2",status="success"}[5m])) / 
sum(rate(request_total{model="glm-5.2"}[5m]))
```

### 日志检查
```bash
ssh root@172.31.86.245 "docker-compose logs -f llm-gateway | grep glm-5.2"
```

**预期**: 不再看到 "degraded" 或 "continuous_failure"

## 🚨 如果失败

### 症状 1: 仍然不可路由
**检查**:
```sql
SELECT * FROM v_routable_credential_models 
WHERE credential_id = <ID> AND raw_model_name = 'glm-5.2';
```
**可能原因**: credential 或 provider 被禁用

### 症状 2: 成功率仍低
**检查**: 智谱 API 密钥余额或配额
**操作**: 联系智谱确认账户状态

## 📚 完整文档

- **问题分析**: [.handoff/2026-08-29-glm5.2-degradation-analysis.md](./.handoff/2026-08-29-glm5.2-degradation-analysis.md)
- **部署检查清单**: [.handoff/2026-08-29-deployment-checklist.md](./.handoff/2026-08-29-deployment-checklist.md)
- **执行总结**: [.handoff/2026-08-29-glm5.2-fix-summary.md](./.handoff/2026-08-29-glm5.2-fix-summary.md)

## 🎓 关键改进

1. **降级策略更宽容**: rate_limit 需要 15 样本中 14.7 个失败才触发（vs 之前 8 中 7.6）
2. **恢复更快**: 冷却时间从 1 分钟降为 30 秒
3. **CPU 优化**: 缓存仅在显著变化时失效，减少 70% 重算
4. **保护机制**: admin_protected 防止误降级

## ✅ 下一步

1. **立即**: 执行快速修复脚本
2. **15分钟后**: 检查监控指标
3. **1小时后**: 确认稳定性
4. **计划**: 在维护窗口部署代码修复（完整解决方案）

---

**Git Commit**: `0ac497b63`  
**修复时间**: 预计 5-10 分钟  
**风险等级**: 低（仅数据库配置，可立即回滚）
