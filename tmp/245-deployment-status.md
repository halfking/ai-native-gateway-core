# 245服务器部署状态报告

**生成时间**: 2026-11-27 02:20  
**服务器**: 245

---

## 🔍 环境发现

### 数据库配置
- **外部数据库**: 172.16.2.210:5432/llm_gateway
- **用户**: llm_gateway
- **连接方式**: 通过.env配置文件

### 服务配置
- **服务名称**: llmgo-green.service
- **二进制路径**: /opt/llm-gateway-go/slots/8781/gateway
- **配置文件**: /opt/llm-gateway-go/.env
- **当前运行**: ✅ 进程ID 1914035

### 本地数据库
- **容器**: acc-pg-245 (用于ACC项目，不是LLM Gateway)
- **数据库**: acc_db
- **表数量**: 221个表（包括刚创建的 billing_precheck_log, billing_retry_queue）

---

## ⚠️ 部署发现的问题

### 问题1: 迁移应用到错误的数据库
- **现象**: 迁移文件应用到了 acc-pg-245 容器的 acc_db 数据库
- **预期**: 应该应用到远程的 172.16.2.210:5432/llm_gateway 数据库
- **影响**: 
  - acc_db 数据库中创建了2个不需要的表（billing_precheck_log, billing_retry_queue）
  - LLM Gateway 实际使用的数据库未应用迁移

### 问题2: 表结构不匹配
- **现象**: 迁移SQL中引用的表（session_turns, maas_monthly_bills）在 acc_db 中不存在
- **原因**: acc_db 是ACC项目的数据库，表结构完全不同
- **错误日志**:
  ```
  ERROR: relation "session_turns" does not exist
  ERROR: relation "maas_monthly_bills" does not exist
  ```

### 问题3: 服务重启失败
- **现象**: systemctl restart llm-gateway.service 失败
- **原因**: 服务名称不对，实际是 llmgo-green.service
- **结果**: 服务未重启，新代码未生效

---

## ✅ 正确的部署步骤

### 步骤1: 连接到正确的数据库

需要通过245跳板连接到远程数据库 172.16.2.210:5432/llm_gateway

```bash
# 在245服务器上执行
source /opt/llm-gateway-go/.env
export PGPASSWORD=$(echo "$LLM_GATEWAY_DATABASE_URL" | sed -n 's|.*://[^:]*:\([^@]*\)@.*|\1|p')

psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c "
SELECT table_name FROM information_schema.tables 
WHERE table_name IN ('session_turns', 'maas_monthly_bills', 'tenant_credit_wallets');
"
```

### 步骤2: 应用迁移到远程数据库

```bash
# 备份远程数据库
pg_dump -h 172.16.2.210 -p 5432 -U llm_gateway llm_gateway > backup_remote_$(date +%Y%m%d_%H%M%S).sql

# 应用迁移
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway < /tmp/migrations/1000_billing_status_and_credit_limit.sql
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway < /tmp/migrations/1001_bill_payment_tracking.sql
psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway < /tmp/migrations/1002_billing_retry_queue.sql
```

### 步骤3: 部署服务

```bash
# 备份当前二进制
cp /opt/llm-gateway-go/slots/8781/gateway /opt/llm-gateway-go/slots/8781/gateway.backup.$(date +%Y%m%d_%H%M%S)

# 部署新二进制
cp /tmp/llm-gateway-new /opt/llm-gateway-go/slots/8781/gateway
chmod +x /opt/llm-gateway-go/slots/8781/gateway

# 重启服务（使用正确的服务名）
systemctl restart llmgo-green.service

# 检查服务状态
systemctl status llmgo-green.service
```

---

## 🔄 清理错误的迁移

需要从 acc_db 数据库中删除误创建的表：

```bash
# 在245服务器上执行
docker exec acc-pg-245 psql -U acc_app -d acc_db -c "
DROP TABLE IF EXISTS billing_precheck_log CASCADE;
DROP TABLE IF EXISTS billing_retry_queue CASCADE;
"
```

---

## 📋 待执行任务清单

- [ ] **清理acc_db中的错误表** (billing_precheck_log, billing_retry_queue)
- [ ] **连接远程数据库** (172.16.2.210:5432/llm_gateway)
- [ ] **检查远程数据库现有表结构**
- [ ] **备份远程数据库**
- [ ] **应用迁移到远程数据库**
- [ ] **验证迁移结果**
- [ ] **部署新二进制到 /opt/llm-gateway-go/slots/8781/gateway**
- [ ] **重启 llmgo-green.service**
- [ ] **验证服务和功能**

---

## 🚨 重要注意事项

1. **数据库位置**: LLM Gateway使用的是**远程数据库**，不是本地容器
2. **服务名称**: 使用 `llmgo-green.service`，不是 `llm-gateway.service`
3. **二进制路径**: `/opt/llm-gateway-go/slots/8781/gateway`
4. **权限**: 需要能够访问 172.16.2.210:5432 的网络权限
5. **密码**: 从 `/opt/llm-gateway-go/.env` 中的 `LLM_GATEWAY_DATABASE_URL` 获取

---

## 📞 下一步建议

由于245环境的特殊性（远程数据库 + 蓝绿部署），建议：

1. **手动执行部署**：由熟悉245环境的运维人员执行
2. **或者提供专门脚本**：创建一个能正确处理远程数据库的部署脚本
3. **先在本地测试环境验证**：已在本地Docker环境成功验证过迁移

---

**状态**: ⚠️ 部署中断，需要重新执行  
**原因**: 数据库和服务配置与预期不符  
**建议**: 由熟悉245环境的运维团队执行后续部署
