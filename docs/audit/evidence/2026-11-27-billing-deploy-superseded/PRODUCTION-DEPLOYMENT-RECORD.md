# 生产环境部署流程 - 执行记录

**开始时间**: 2026-11-27 02:25  
**目标环境**: 245 → 154 (数据库: 252)

---

## 📋 部署架构

```
架构图:
┌─────────────┐
│   252服务器   │
│ PostgreSQL  │  ← 共享数据库
│ llm_gateway │
└─────────────┘
       ↑
       │ (远程连接)
       │
  ┌────┴─────┐
  │          │
┌─▼────┐  ┌─▼────┐
│ 245  │  │ 154  │
│8781端口│  │8780端口│
└──────┘  └──────┘
```

### 关键信息
- **数据库位置**: 252服务器 (pg-252-pg17容器)
- **数据库名称**: llm_gateway
- **245服务**: llmgo-green.service (端口8781)
- **154服务**: 待确认
- **部署模式**: 蓝绿部署 (slots目录)

---

## 🚀 执行进度

### 阶段1: 本地测试环境 ✅
- [x] 迁移文件验证
- [x] 本地PostgreSQL测试
- [x] 代码编译验证
- **状态**: 完成 (2026-11-26)

### 阶段2: 245环境部署 🔄
- [x] 清理acc_db错误表
- [x] 迁移文件上传到252
- [x] 252数据库备份
- [ ] 应用迁移到252
- [ ] 验证迁移结果
- [ ] 部署245服务
- [ ] 验证245功能
- **状态**: 进行中

### 阶段3: 154环境部署 ⏳
- [ ] 编译服务
- [ ] 上传到154
- [ ] 部署154服务
- [ ] 验证154功能
- **状态**: 等待245完成

---

## 📝 执行日志

### 2026-11-27 02:19 - 首次尝试
- **问题**: 误部署到acc_db数据库
- **原因**: 未识别远程数据库架构
- **处理**: 清理错误表，重新部署

### 2026-11-27 02:25 - 正确部署
- **目标**: 252数据库 (llm_gateway)
- **步骤**:
  1. ✅ 清理acc_db错误表 (billing_precheck_log, billing_retry_queue)
  2. ✅ 识别252 PostgreSQL容器 (pg-252-pg17)
  3. ✅ 上传迁移文件到252
  4. 🔄 备份252数据库
  5. ⏳ 应用3个迁移
  6. ⏳ 部署245服务
  7. ⏳ 验证功能

---

## 🔧 生成的工具

### 部署脚本
1. **tmp/deploy-245-correct.sh** - 245部署脚本 (已执行)
2. **tmp/deploy-154-after-245.sh** - 154部署脚本 (已准备)
3. **tmp/verify-deployment.sh** - 验证脚本

### 文档
1. **tmp/245-DEPLOYMENT-FINAL-REPORT.md** - 245部署报告
2. **tmp/245-deployment-status.md** - 状态分析
3. **tmp/PRODUCTION-DEPLOYMENT-RECORD.md** - 本文档

---

## ✅ 验证清单

### 252数据库验证
```sql
-- 检查新表
SELECT table_name FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit');
-- 预期: 3行

-- 检查新字段
SELECT column_name FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('paid_at', 'payment_method', 'payment_voucher', 'payment_amount_cents');
-- 预期: 4行
```

### 245服务验证
```bash
# 健康检查
curl http://localhost:8781/api/system/version

# 服务状态
systemctl status llmgo-green.service

# 预检日志
tail -f /var/log/llm-gateway-go/app.log | grep "billing precheck"
```

### 154服务验证
```bash
# 健康检查
curl http://localhost:8780/api/system/version

# 服务状态
systemctl status [服务名]

# 预检日志
tail -f /var/log/llm-gateway-go/app.log | grep "billing precheck"
```

---

## 🚨 回滚方案

### 数据库回滚 (252)
```bash
# 连接到252
ssh 252

# 回滚迁移（按相反顺序）
docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway \
  < /tmp/migrations/1002_billing_retry_queue.down.sql

docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway \
  < /tmp/migrations/1001_bill_payment_tracking.down.sql

docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway \
  < /tmp/migrations/1000_billing_status_and_credit_limit.down.sql
```

### 服务回滚 (245)
```bash
ssh 245
cp /opt/llm-gateway-go/slots/8781/gateway.backup.* \
   /opt/llm-gateway-go/slots/8781/gateway
systemctl restart llmgo-green.service
```

### 服务回滚 (154)
```bash
ssh 154
cp [备份路径] [部署路径]
systemctl restart [服务名]
```

---

## 📊 预期结果

### 数据库变化
- **新增3个表**: billing_precheck_log, billing_retry_queue, maas_bill_status_audit
- **修改2个表**: 
  - tenant_credit_wallets (+2字段)
  - maas_monthly_bills (+8字段)
- **新增1个视图**: v_overdue_bills
- **新增2个触发器**: 状态审计、负余额通知

### 功能变化
- **P0-1**: 预检功能生效 (余额不足返回402)
- **P0-2**: 账单API可用 (5个管理端点)
- **日志**: billing_precheck_log 开始记录
- **重试**: billing_retry_queue 开始工作

---

## 🎯 成功标准

### 必须满足
- [ ] 252数据库: 3个表 + 新字段已创建
- [ ] 245服务: 健康检查返回200
- [ ] 154服务: 健康检查返回200
- [ ] 预检日志: 有"billing precheck"记录
- [ ] 无异常错误日志

### 建议观察 (24小时)
- [ ] 402错误率正常 (<1%)
- [ ] 预检查询性能正常
- [ ] 数据库查询无超时
- [ ] 重试队列无积压

---

## 📞 联系人

**技术负责人**: [待填写]  
**DBA**: [待填写]  
**运维**: [待填写]

---

**当前状态**: 🔄 245部署进行中  
**下一步**: 等待245部署完成，验证后部署154  
**预计完成时间**: 2026-11-27 03:00
