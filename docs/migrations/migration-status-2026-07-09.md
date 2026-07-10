# 数据库迁移状态追踪 - 2026-07-09

## 概览

本文档追踪三个环境的数据库迁移执行状态，确保结构一致性。

**最后更新**: 2026-07-09 16:00  
**更新人**: AI Agent

---

## 迁移清单

### 已完成的迁移 (2026-07-09)

| Migration ID | 文件名 | 说明 | 本地R112 | kaixuan-1 | 252生产 | 备注 |
|--------------|--------|------|----------|-----------|---------|------|
| 332 | 332_credential_view_add_credential_manual_disabled.sql | credential视图修复 | ✅ | ✅ | ✅ | - |
| 359 | 359_session_intent_evolution.sql | session意图演化 | ✅ | ✅ | ✅ | - |
| 360 | 360_intent_classifier_config.sql | 意图分类器配置 | ✅ | ✅ | ✅ | - |
| 361 | 361_intent_analysis_adjustments.sql | 意图分析调整 | ✅ | ✅ | ✅ | - |
| 362 | 362_intent_classification_feedback.sql | 意图分类反馈 | ✅ | ✅ | ✅ | - |
| 363 | 363_security_detector_config.sql | 安全检测器配置 | ✅ | ✅ | ✅ | - |
| 364 | 364_prompt_injection_enhanced.sql | prompt injection增强 | ⚠️ | ⚠️ | ⚠️ | 部分错误已修复 |
| 365 | 365_output_compliance_policy_enhance.sql | output compliance增强 | ⚠️ | ⚠️ | ⚠️ | 缺失表已补齐 |

**说明：**
- ✅ 完全成功
- ⚠️ 部分失败但已手动修复
- ❌ 失败待修复
- ⏸️ 跳过（Citus兼容性）

### 基础迁移依赖

| Migration ID | 文件名 | 说明 | 本地R112 | kaixuan-1 | 252生产 | 备注 |
|--------------|--------|------|----------|-----------|---------|------|
| 315 | 315_prompt_injection_detection.sql | prompt injection基表 | ⚠️ | ✅ | ✅ | 本地简化版 |
| 316 | 316_output_compliance_monitoring.sql | output compliance基表 | ⚠️ | ✅ | ✅ | 本地简化版 |

---

## 环境状态

### 本地 R112

**架构**: 单节点Citus  
**PostgreSQL版本**: Citus 11.3.0  
**表总数**: 170  
**视图总数**: 包含在表总数中

**特殊处理**:
1. 跳过外键约束（tenants.id不存在）
2. v_routable_credential_models使用简化版（无plan_type）
3. 手动创建prompt_injection_policies和output_compliance_policies基表

**执行方式**:
```bash
docker exec r112_postgres psql -U kxuser -d llm_gateway -f /tmp/xxx.sql
```

### kaixuan-1 (K3s测试环境)

**架构**: Citus集群  
**PostgreSQL版本**: PostgreSQL 14 + Citus  
**表总数**: 221  
**视图总数**: 34

**特殊处理**:
1. 手动补齐output_compliance_review_queue和output_compliance_feedback表
2. 添加created_by和updated_by列到output_compliance_policies

**执行方式**:
```bash
export KUBECONFIG=~/.kube/config-kaixuan-1
kubectl exec -n default kaixuan-pg-55fbb459fb-wc75l -- psql -U llm_gateway -d llm_gateway -f /tmp/xxx.sql
```

### 252 (生产环境)

**架构**: Citus集群  
**PostgreSQL版本**: PostgreSQL 17 + Citus  
**表总数**: 218  
**视图总数**: 33

**特殊处理**:
1. 手动补齐output_compliance_review_queue和output_compliance_feedback表
2. 添加created_by和updated_by列到output_compliance_policies

**执行方式**:
```bash
ssh -p 25022 root@115.29.212.252 "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -f /tmp/xxx.sql"
```

---

## 关键表结构对比

### prompt_injection_policies

| 列名 | 数据类型 | 本地 | kaixuan-1 | 252 |
|------|---------|------|-----------|-----|
| id | SERIAL | ✅ | ✅ | ✅ |
| tenant_id | VARCHAR(255) | ✅ | ✅ | ✅ |
| enabled | BOOLEAN | ✅ | ✅ | ✅ |
| detection_mode | VARCHAR(20) | ✅ | ✅ | ✅ |
| ... | ... | ... | ... | ... |
| **列总数** | - | **37** | **37** | **37** |

**状态**: ✅ 完全一致

### output_compliance_policies

| 列名 | 数据类型 | 本地 | kaixuan-1 | 252 |
|------|---------|------|-----------|-----|
| id | SERIAL | ✅ | ✅ | ✅ |
| tenant_id | VARCHAR(255) | ✅ | ✅ | ✅ |
| enabled | BOOLEAN | ✅ | ✅ | ✅ |
| llm_engine_id | INT | ✅ | ✅ | ✅ |
| check_secrets | BOOLEAN | ✅ | ✅ | ✅ |
| exception_rules | JSONB | ✅ | ✅ | ✅ |
| created_by | VARCHAR(255) | ✅ | ✅ | ✅ |
| updated_by | VARCHAR(255) | ✅ | ✅ | ✅ |
| ... | ... | ... | ... | ... |
| **列总数** | - | **65** | **65** | **65** |

**状态**: ✅ 完全一致

### v_routable_credential_models

| 列名 | 数据类型 | 本地 | kaixuan-1 | 252 |
|------|---------|------|-----------|-----|
| binding_id | BIGINT | ✅ | ✅ | ✅ |
| credential_id | BIGINT | ✅ | ✅ | ✅ |
| is_routable | BOOLEAN | ✅ | ✅ | ✅ |
| unavailable_reason | TEXT | ✅ | ✅ | ✅ |
| plan_type | VARCHAR | ❌ | ✅ | ✅ |
| plan_type_origin | VARCHAR | ❌ | ✅ | ✅ |

**状态**: ⚠️ 本地简化版（缺少plan_type相关列）

---

## 执行记录

### 2026-07-09 初始迁移

**执行时间**: 14:00 - 16:00  
**执行人**: AI Agent  
**影响范围**: 三个环境

**执行步骤**:

1. **Phase 1: 本地R112** (14:00-14:45)
   - 执行332 ✅
   - 尝试执行315/316 ❌ (外键失败)
   - 手动创建基表 ✅
   - 执行359-363 ✅
   - 执行364 ⚠️ (部分错误)
   - 执行365 ⚠️ (缺失表)
   - 手动补齐缺失表 ✅
   - 创建v_routable_credential_models视图 ✅

2. **Phase 2: kaixuan-1** (14:45-15:15)
   - 上传迁移文件 ✅
   - 执行332 ✅
   - 执行359-363 ✅
   - 执行364 ⚠️ (部分错误)
   - 执行365 ⚠️ (缺失表)
   - 手动补齐缺失表 ✅
   - 重启deployment ✅

3. **Phase 3: 252生产** (15:15-15:45)
   - 上传迁移文件 ✅
   - 执行332 ✅
   - 执行359-363 ✅
   - 执行364 ⚠️ (部分错误)
   - 执行365 ⚠️ (缺失表)
   - 手动补齐缺失表 ✅
   - 验证服务正常 ✅

4. **Phase 4: 最终验证** (15:45-16:00)
   - 验证22张关键表一致性 ✅
   - 验证关键列一致性 ✅
   - 生成审计报告 ✅

**遇到的问题**:
1. 本地R112 tenants表主键不匹配 → 跳过外键约束
2. 364/365部分失败 → 手动补齐缺失表
3. 本地缺少plan_type列 → 创建简化版视图

**解决方案**:
- 所有问题已通过手动SQL修复
- 三个环境22张关键表完全一致

---

## 验证清单

### 结构验证

- [x] 本地R112所有关键表存在
- [x] kaixuan-1所有关键表存在
- [x] 252所有关键表存在
- [x] 关键表列数一致
- [x] 关键视图存在
- [x] 视图包含必需逻辑

### 功能验证

- [ ] prompt injection检测正常
- [ ] output compliance检测正常
- [ ] intent分析正常
- [ ] security detector正常
- [ ] credential路由正常

### 性能验证

- [ ] 查询性能测试
- [ ] 索引使用率检查
- [ ] 慢查询分析

---

## 回滚计划

### 回滚触发条件

1. 关键功能异常且无法快速修复
2. 数据完整性问题
3. 性能严重下降

### 回滚步骤

**本地R112**:
```bash
# 1. 停止服务
docker stop r112_gateway

# 2. 执行down脚本（如果有）
docker exec r112_postgres psql -U kxuser -d llm_gateway -f /path/to/xxx.down.sql

# 3. 手动清理表
docker exec r112_postgres psql -U kxuser -d llm_gateway << EOF
DROP TABLE IF EXISTS prompt_injection_llm_engines CASCADE;
DROP TABLE IF EXISTS severity_action_matrix CASCADE;
-- ... 其他表
EOF

# 4. 重启服务
docker start r112_gateway
```

**kaixuan-1/252**:
```bash
# 1. 执行down脚本
kubectl exec ... -- psql -U llm_gateway -d llm_gateway -f /tmp/xxx.down.sql

# 2. 重启deployment
kubectl rollout restart deployment/llm-gateway-go-deployment -n pms-test
```

**注意**: 
- 回滚前备份数据
- 回滚后验证功能
- 记录回滚原因

---

## 后续计划

### 短期（1周内）

- [ ] 补充364/365的down脚本
- [ ] 完善本地R112的plan_type列支持
- [ ] 添加自动化结构对比脚本

### 中期（1月内）

- [ ] 建立三环境定期对比机制
- [ ] 优化migration执行流程
- [ ] 完善回滚测试

### 长期（3月内）

- [ ] 本地R112升级为完整Citus集群
- [ ] 建立migration CI/CD流程
- [ ] 完善数据库监控告警

---

## 相关文档

- [数据库一致性迁移报告](./2026-07-09-database-consistency-migration-report.md)
- [R1.13安全配置迁移指南](./r1.13-security-config.md)
- [数据库错误修复记录](../fix/database-error-fix-record.md)

---

## 附录

### A. 快速验证命令

**检查表是否存在**:
```bash
# 本地
docker exec r112_postgres psql -U kxuser -d llm_gateway -c "\dt prompt_injection_*"

# kaixuan-1
kubectl exec -n default kaixuan-pg-55fbb459fb-wc75l -- psql -U llm_gateway -d llm_gateway -c "\dt prompt_injection_*"

# 252
ssh -p 25022 root@115.29.212.252 "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c '\dt prompt_injection_*'"
```

**检查列是否存在**:
```sql
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'output_compliance_policies' 
  AND column_name IN ('llm_engine_id', 'check_secrets', 'exception_rules')
ORDER BY column_name;
```

**检查视图是否存在**:
```sql
SELECT viewname FROM pg_views WHERE viewname = 'v_routable_credential_models';
```

### B. 紧急联系方式

**数据库问题**:
- DBA: @张三 (Feishu: zhangsan)
- 后端负责人: @李四 (Feishu: lisi)

**部署问题**:
- DevOps: @王五 (Feishu: wangwu)
- SRE: @赵六 (Feishu: zhaoliu)

**紧急热线**: 400-xxx-xxxx

---

**维护者**: Official-Deploy Team  
**文档版本**: v1.0  
**最后审核**: 2026-07-09
