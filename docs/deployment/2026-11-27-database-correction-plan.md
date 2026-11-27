# 数据库部署修正方案

**问题**: 迁移误应用到211，需要应用到252  
**日期**: 2026-11-27  
**状态**: 待修正

---

## 🚨 问题分析

### 当前状态
1. **迁移位置错误**:
   - ❌ 迁移已应用到：211/acc-pg17/llm_gateway
   - ✓ 应该应用到：252/pg-252-pg17/llm_gateway

2. **252数据库状态**:
   - IP: 172.16.2.210 (这是.env配置的地址)
   - 状态: **恢复模式（STANDBY）**
   - 问题: 备库无法写入，拒绝迁移

3. **.env配置**:
   ```
   LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@172.16.2.210:5432/llm_gateway
   ```

### 架构说明
```
当前架构（误部署）:
┌─────────┐
│  211    │
│ acc-pg17│ ← 迁移误应用到这里
└─────────┘

目标架构（正确）:
┌─────────┐
│  252    │
│pg-252   │ ← 应该应用到这里
│(备库)   │   但处于恢复模式
└─────────┘
```

---

## 🔧 修正方案

### 方案1: 等待252退出恢复模式（推荐）

**适用场景**: 252即将变为主库

**步骤**:
1. 联系DBA，询问252何时退出恢复模式
2. 等待252变为主库（PRIMARY）
3. 应用迁移到252
4. 重启245和154服务

**优点**: 
- 符合原计划架构
- 不需要改.env配置

**时间**: 取决于DBA计划

---

### 方案2: 找到252的主库并迁移

**适用场景**: 252长期保持备库状态

**步骤**:
1. 找到252的主库地址
   ```bash
   ssh 252 "docker exec pg-252-pg17 cat /var/lib/postgresql/data/postgresql.auto.conf | grep primary_conninfo"
   ```

2. 连接主库并应用迁移
   ```bash
   # 假设主库在某台服务器上
   ssh [主库服务器] "docker exec [容器名] psql -U llm_gateway -d llm_gateway < /tmp/migrations/1000_*.sql"
   ```

3. 等待252从主库同步数据

**优点**: 
- 数据最终会同步到252
- 不改变现有架构

**时间**: 迁移立即执行，同步延迟几秒

---

### 方案3: 修改.env指向211（临时方案）

**适用场景**: 紧急需要上线，252短期无法使用

**步骤**:
1. 修改245和154的.env
   ```bash
   # 改为指向211
   LLM_GATEWAY_DATABASE_URL=postgres://llm_gateway:***@192.168.31.211:5432/llm_gateway
   ```

2. 重启245和154服务

3. 后续再迁移回252

**优点**: 
- 立即可用
- 迁移已在211完成

**缺点**: 
- 改变了架构
- 需要后续迁移回252

---

## 📋 推荐方案：方案2

### 执行步骤

#### 1. 找到252的主库
```bash
ssh 252 "docker logs pg-252-pg17 2>&1 | grep 'primary' | tail -5"
```

#### 2. 应用迁移到主库
```bash
# 上传迁移文件到主库服务器
scp sql/migrations/startup/*.sql [主库服务器]:/tmp/migrations/

# 应用迁移
ssh [主库服务器] "docker exec [主库容器] psql -U llm_gateway -d llm_gateway < /tmp/migrations/1000_billing_status_and_credit_limit.sql"
ssh [主库服务器] "docker exec [主库容器] psql -U llm_gateway -d llm_gateway < /tmp/migrations/1001_bill_payment_tracking.sql"
ssh [主库服务器] "docker exec [主库容器] psql -U llm_gateway -d llm_gateway < /tmp/migrations/1002_billing_retry_queue.sql"
```

#### 3. 验证252同步
```bash
# 等待几秒后检查252
ssh 252 "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c '
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_name IN (\"billing_precheck_log\", \"billing_retry_queue\", \"maas_bill_status_audit\");
'"
# 应该返回: 3
```

#### 4. 清理211的误迁移（可选）
```bash
ssh 211 "docker exec acc-pg17 psql -U llm_gateway -d llm_gateway < /tmp/migrations/1002_billing_retry_queue.down.sql"
ssh 211 "docker exec acc-pg17 psql -U llm_gateway -d llm_gateway < /tmp/migrations/1001_bill_payment_tracking.down.sql"
ssh 211 "docker exec acc-pg17 psql -U llm_gateway -d llm_gateway < /tmp/migrations/1000_billing_status_and_credit_limit.down.sql"
```

---

## 🔍 验证清单

### 迁移验证
- [ ] 252主库有3个新表
- [ ] 252备库同步完成（3个新表）
- [ ] maas_monthly_bills有8个新字段
- [ ] v_overdue_bills视图已创建

### 服务验证
- [ ] 245服务连接到252
- [ ] 154服务连接到252
- [ ] 健康检查返回200
- [ ] 预检日志开始记录

---

## 📞 需要的信息

请联系DBA/运维提供：
1. **252的主库地址** - 用于方案2
2. **252何时退出恢复模式** - 用于方案1
3. **是否允许临时使用211** - 用于方案3

---

## 🎯 下一步行动

### 立即执行
1. 联系DBA确认252的主库位置
2. 根据DBA反馈选择方案
3. 执行迁移到正确的数据库

### 代码提交
当前状态已提交代码，包括：
- 迁移文件（3个.sql + 3个.down.sql）
- 部署脚本
- 完整文档

### 后续计划
- 等待DBA反馈
- 执行正确的迁移
- 更新部署文档

---

**状态**: ⚠️ 待修正  
**优先级**: P1  
**负责人**: 待分配
