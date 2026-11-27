# Dev环境部署指南

**目标**: 在dev环境应用数据库迁移并验证部署  
**执行时间**: 2026-11-27  
**预计耗时**: 30-45分钟

---

## 📋 前置准备

### 1. 确认dev环境信息

需要收集以下信息：
- **数据库主机**: dev-db-host (例如: 10.0.1.100)
- **数据库端口**: 5432 (默认)
- **数据库名称**: llm_gateway_dev
- **数据库用户**: admin (或其他有权限的用户)
- **数据库密码**: [需要DBA提供]

### 2. 确认权限

需要以下权限：
- ✅ 数据库读写权限（CREATE TABLE, ALTER TABLE）
- ✅ SSH访问dev服务器（用于部署服务）
- ✅ 管理员token（用于测试API）

### 3. 准备工作

```bash
# 1. 切换到项目目录
cd /Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5

# 2. 确认脚本存在
ls -lh tmp/apply-migrations-dev.sh tmp/verify-migrations-dev.sh

# 3. 拉取最新代码（如果需要）
git pull origin main
```

---

## 🚀 执行步骤

### 步骤1: 配置环境变量

```bash
# 设置dev数据库连接信息
export DB_HOST="dev-db-host"          # 替换为实际主机名
export DB_PORT="5432"
export DB_NAME="llm_gateway_dev"
export DB_USER="admin"                # 替换为实际用户名
export PGPASSWORD="${{ secrets.EVIDENCE_PAT \|\| github.token }"  # 替换为实际密码

# 验证配置
echo "数据库: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
```

**重要**: 也可以使用 `.pgpass` 文件代替 `PGPASSWORD` 环境变量：

```bash
# 创建 .pgpass 文件
cat > ~/.pgpass << EOF
dev-db-host:5432:llm_gateway_dev:admin:your-password-here
EOF

chmod 600 ~/.pgpass
```

---

### 步骤2: 备份数据库（强烈推荐）

```bash
# 创建备份目录
mkdir -p backups

# 备份dev数据库
pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" "$DB_NAME" \
  > backups/dev_backup_$(date +%Y%m%d_%H%M%S).sql

# 验证备份文件
ls -lh backups/dev_backup_*.sql | tail -1
```

**预计耗时**: 1-5分钟（取决于数据库大小）

---

### 步骤3: 应用数据库迁移

```bash
# 运行迁移脚本
bash tmp/apply-migrations-dev.sh
```

**脚本会执行以下操作**:
1. ✅ 检查环境变量配置
2. ✅ 测试数据库连接
3. ⏸️ 要求确认备份（输入 `yes` 继续）
4. ⏸️ 要求确认执行（输入 `yes` 继续）
5. 🔄 应用3个迁移文件
6. ✅ 显示执行结果

**预期输出**:
```
========================================
  Dev环境数据库迁移
========================================

请确认dev环境配置：
数据库主机: dev-db-host
数据库端口: 5432
数据库名称: llm_gateway_dev
数据库用户: admin

警告: 即将修改dev环境数据库
此操作将：
  1. 添加新表: billing_precheck_log, billing_retry_queue
  2. 修改表: tenant_credit_wallets, maas_monthly_bills
  3. 创建视图和触发器

继续执行？(yes/no) yes

测试数据库连接...
✓ 数据库连接成功

建议: 执行迁移前先备份数据库
已经备份了吗？(yes/no) yes

[1/3] 应用 1000_billing_status_and_credit_limit.sql
✓ 迁移1000完成

[2/3] 应用 1001_bill_payment_tracking.sql
✓ 迁移1001完成

[3/3] 应用 1002_billing_retry_queue.sql
✓ 迁移1002完成

========================================
✅ 所有迁移已成功应用到dev环境
========================================
```

**预计耗时**: 2-5分钟

---

### 步骤4: 验证迁移结果

```bash
# 运行验证脚本
bash tmp/verify-migrations-dev.sh
```

**验证项目**:
1. ✅ 新表存在（billing_precheck_log, billing_retry_queue, maas_bill_status_audit）
2. ✅ tenant_credit_wallets 新字段（credit_limit_credits, credit_policy）
3. ✅ maas_monthly_bills 新字段（8个付款相关字段）
4. ✅ 视图创建（v_overdue_bills）
5. ✅ 触发器创建（2个）

**预期输出**:
```
========================================
  Dev环境迁移验证
========================================

[1/5] 检查新表...
       table_name       | size  
------------------------+-------
 billing_precheck_log   | 32 kB
 billing_retry_queue    | 40 kB
 maas_bill_status_audit | 32 kB

[2/5] 检查 tenant_credit_wallets 新字段...
     column_name      | data_type | is_nullable 
----------------------+-----------+-------------
 credit_limit_credits | bigint    | YES
 credit_policy        | text      | YES

[3/5] 检查 maas_monthly_bills 新字段...
     column_name      | data_type | is_nullable 
----------------------+-----------+-------------
 paid_at              | timestamp | YES
 payment_amount_cents | bigint    | YES
 payment_method       | text      | YES
 payment_voucher      | text      | YES
 voided_at            | timestamp | YES

[4/5] 检查新视图...
   table_name    | table_type 
-----------------+------------
 v_overdue_bills | VIEW

[5/5] 检查触发器...
          trigger_name          | event_object_table  
--------------------------------+---------------------
 trg_audit_bill_status_changes  | maas_monthly_bills
 trg_notify_negative_balance    | tenant_credit_wallets

========================================
✅ 验证完成
========================================
```

**如果验证失败**，查看错误信息并参考"故障排查"部分。

---

### 步骤5: 编译并部署服务

```bash
# 1. 编译服务
go build -o llm-gateway ./cmd/gateway

# 2. 部署到dev服务器
DEV_SERVER="dev-server-host"
scp llm-gateway $DEV_SERVER:/opt/llm-gateway/

# 3. 重启服务
ssh $DEV_SERVER 'sudo systemctl restart llm-gateway'

# 4. 检查服务状态
ssh $DEV_SERVER 'sudo systemctl status llm-gateway'

# 5. 检查版本
curl http://$DEV_SERVER:8080/api/system/version
```

**预计耗时**: 3-5分钟

---

### 步骤6: 测试API端点

```bash
# 1. 配置环境变量
export API_HOST="dev-server:8080"
export ADMIN_TOKEN="否本应共享"  # 替换为实际token

# 2. 运行API测试
bash tmp/auto-test-api.sh
```

**测试的5个端点**:
1. `GET /api/admin/bills` - 获取账单列表
2. `GET /api/admin/bills/{id}` - 获取账单详情
3. `POST /api/admin/bills/{id}/mark-paid` - 标记已付款
4. `POST /api/admin/bills/{id}/void` - 作废账单
5. `POST /api/admin/bills/batch-mark-paid` - 批量标记

**预期输出**: 所有端点返回正确的HTTP状态码（200/404/400）

---

## 📊 验证清单

完成后，请确认以下所有项目：

### 数据库层
- [ ] 3个新表已创建
- [ ] tenant_credit_wallets 有2个新字段
- [ ] maas_monthly_bills 有8个新字段
- [ ] 2个视图已创建
- [ ] 2个触发器已创建

### 服务层
- [ ] 服务编译成功
- [ ] 服务部署到dev服务器
- [ ] 服务启动正常
- [ ] 健康检查返回200

### API层
- [ ] 5个账单API端点可访问
- [ ] 权限控制正常（非admin返回403）
- [ ] 错误处理正确

---

## 🔍 监控指标

部署后24小时内，监控以下指标：

### 数据库
```sql
-- 检查预检日志数量
SELECT COUNT(*) FROM billing_precheck_log;

-- 检查重试队列
SELECT COUNT(*), status FROM billing_retry_queue GROUP BY status;

-- 检查欠费租户
SELECT * FROM v_tenants_in_debt;
```

### 应用
- `billing_precheck_rejected_total` - 预检拒绝次数
- `http_requests_total{path="/api/admin/bills"}` - API调用次数
- 402错误率
- P0-1预检逻辑是否生效（观察日志中的 "billing precheck" 消息）

---

## ⚠️ 故障排查

### 问题1: 连接数据库失败

**症状**: `connection refused` 或 `authentication failed`

**排查步骤**:
1. 检查数据库服务是否运行: `ssh dev-db-host 'systemctl status postgresql'`
2. 检查防火墙: `telnet dev-db-host 5432`
3. 检查pg_hba.conf配置
4. 验证用户名和密码

### 问题2: 迁移执行失败

**症状**: `relation already exists` 或 `column already exists`

**原因**: 迁移可能已经部分应用过

**解决**:
```bash
# 检查哪些对象已存在
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "\dt billing*"
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "\d tenant_credit_wallets"

# 如果需要重置，使用down迁移
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1002_billing_retry_queue.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1001_bill_payment_tracking.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1000_billing_status_and_credit_limit.down.sql
```

### 问题3: API测试失败

**症状**: 404或500错误

**排查步骤**:
1. 检查服务是否重启: `systemctl status llm-gateway`
2. 检查路由是否注册: `grep registerBillPaymentRoutes admin/handler.go`
3. 检查日志: `tail -f /var/log/llm-gateway/app.log`
4. 验证token有效性

---

## 🔄 回滚方案

如果需要回滚迁移：

```bash
# 1. 停止服务
ssh dev-server 'sudo systemctl stop llm-gateway'

# 2. 回滚数据库（按相反顺序）
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1002_billing_retry_queue.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1001_bill_payment_tracking.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < sql/migrations/startup/1000_billing_status_and_credit_limit.down.sql

# 3. 恢复旧版本服务
scp old-binary dev-server:/opt/llm-gateway/llm-gateway
ssh dev-server 'sudo systemctl start llm-gateway'

# 4. 验证回滚
bash tmp/verify-migrations-dev.sh  # 应该显示表/字段不存在
```

---

## 📞 联系方式

**技术支持**:
- 迁移问题: 查看 `tmp/FINAL-COMPLETION-REPORT.md`
- DBA支持: 联系数据库团队
- 部署问题: 联系运维团队

**相关文档**:
- 完整报告: `tmp/FINAL-COMPLETION-REPORT.md`
- 测试验证: `tmp/test-deployment-report.md`
- 任务交接: `docs/handoff/2026-11-26-p0-tasks-handoff.md`

---

## ✅ 完成确认

部署完成后，请填写：

```
执行人: __________
执行时间: __________
数据库备份文件: __________
服务版本: __________
验证结果: □ 通过  □ 失败
备注: __________
```

---

**文档版本**: 1.0  
**最后更新**: 2026-11-27  
**状态**: ✅ 准备就绪
