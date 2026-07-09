# 252数据库请求日志修复 - 执行指南

## 问题确认

154服务器上 `llm.kxpms.cn` 的所有请求没有保存到252的PostgreSQL数据库中。

**根本原因**: 代码写入 `request_wal_hot` 表，但252数据库缺少该表。

---

## 快速修复（推荐）

### 方法1: 使用自动化脚本

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
./scripts/apply-fix-252.sh
```

该脚本会：
1. ✓ 测试数据库连接
2. ✓ 检查当前状态
3. ✓ 执行修复（幂等）
4. ✓ 验证修复结果
5. ✓ 测试写入功能
6. ✓ 显示统计信息

**优点**: 全自动，包含所有安全检查和验证

---

### 方法2: 手动执行SQL脚本

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor

# 连接并执行修复
psql -h 192.168.0.252 -U postgres -d llm_gateway \
  -f sql/fixes/fix-missing-request-wal-hot.sql
```

**优点**: 简单直接，适合熟悉PostgreSQL的用户

---

### 方法3: 远程执行（如果252上有SSH访问）

```bash
# 1. 将修复脚本复制到252
scp sql/fixes/fix-missing-request-wal-hot.sql root@192.168.0.252:/tmp/

# 2. 在252上执行
ssh root@192.168.0.252
sudo -u postgres psql -d llm_gateway -f /tmp/fix-missing-request-wal-hot.sql
```

---

## 执行步骤详解

### 第1步: 执行修复脚本

选择上面任一方法执行修复。

**预期输出**:
```
NOTICE:  request_wal_hot 表不存在，开始创建...
NOTICE:  ✓ request_wal_hot 表已就绪
NOTICE:  ✓ request_wal_bodies 表已就绪
NOTICE:  ✓ request_wal_with_current_month 视图已就绪
NOTICE:  ========================================
NOTICE:  ✓ 所有验证通过
NOTICE:    - request_wal_hot: 0 行, 17 列
NOTICE:    - request_wal_bodies: 0 行
NOTICE:    - request_wal_with_current_month: 视图已创建
NOTICE:  ========================================
COMMIT

修复完成！
```

---

### 第2步: 重启154服务器上的网关服务

```bash
ssh root@192.168.0.154 'systemctl restart llm-gateway'
```

检查服务状态：
```bash
ssh root@192.168.0.154 'systemctl status llm-gateway'
```

**预期输出**: 服务状态为 `active (running)`

---

### 第3步: 发送测试请求

```bash
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "测试请求日志记录"}]
  }'
```

---

### 第4步: 验证数据是否被记录

等待1-2分钟后执行：

```bash
psql -h 192.168.0.252 -U postgres -d llm_gateway -c \
  "SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '5 minutes';"
```

**预期输出**:
```
 count |         max          
-------+----------------------
     1 | 2026-07-09 23:15:32+08
```

如果 count > 0，说明修复成功！

---

### 第5步: 查看详细记录

```bash
psql -h 192.168.0.252 -U postgres -d llm_gateway -c \
  "SELECT 
     request_id,
     status,
     stage,
     client_model,
     prompt_tokens,
     completion_tokens,
     created_at
   FROM request_wal_hot 
   ORDER BY created_at DESC 
   LIMIT 10;"
```

---

## 故障排查

### 问题1: 连接不上252数据库

**检查**:
```bash
# 测试网络连接
ping 192.168.0.252

# 测试PostgreSQL端口
nc -zv 192.168.0.252 5432

# 检查防火墙
ssh root@192.168.0.252 'firewall-cmd --list-all'
```

**解决**: 确保防火墙开放5432端口，PostgreSQL允许远程连接

---

### 问题2: 修复后仍然没有数据

**检查154服务日志**:
```bash
ssh root@192.168.0.154 'journalctl -u llm-gateway -f'
```

查找错误信息中包含：
- `request_wal_hot`
- `database error`
- `connection failed`

**可能原因**:
1. 154连接的不是252数据库（检查环境变量）
2. 数据库连接池未重置（重启服务解决）
3. 写入权限问题

---

### 问题3: 表已存在但结构不对

**检查表结构**:
```bash
psql -h 192.168.0.252 -U postgres -d llm_gateway -c \
  "\d request_wal_hot"
```

**解决**: 删除表后重新执行修复
```sql
DROP TABLE IF EXISTS request_wal_hot CASCADE;
-- 然后重新执行修复脚本
```

---

## 验证清单

执行完成后，确认以下各项：

- [ ] 252数据库上 `request_wal_hot` 表存在
- [ ] 表包含17个列（request_id, tenant_id, status等）
- [ ] `request_wal_bodies` 表存在
- [ ] `request_wal_with_current_month` 视图存在
- [ ] 测试写入成功
- [ ] 154服务器上网关服务正常运行
- [ ] 发送测试请求后，5分钟内能在表中查到记录
- [ ] 记录包含完整的字段（status, tokens等）

---

## 回滚方案

如果修复导致问题，可以回滚：

```sql
-- 删除创建的对象
DROP VIEW IF EXISTS request_wal_with_current_month;
DROP TABLE IF EXISTS request_wal_hot CASCADE;
DROP TABLE IF EXISTS request_wal_bodies CASCADE;

-- 恢复到修复前状态
-- (如果有备份，从备份恢复)
```

但实际上修复脚本是幂等且安全的，不应该导致问题。

---

## 监控和告警

修复后，建议设置监控：

### 1. 每小时写入量监控
```sql
SELECT 
    date_trunc('hour', created_at) as hour,
    COUNT(*) as request_count
FROM request_wal_hot
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;
```

### 2. 设置告警

当最近15分钟没有新记录时告警：
```sql
SELECT COUNT(*) 
FROM request_wal_hot 
WHERE created_at > NOW() - INTERVAL '15 minutes';
-- 如果结果为0且有流量，则触发告警
```

---

## 相关文档

- 详细技术分析: `docs/issues/REQUEST_LOGGING_FIX_252.md`
- 修复脚本: `sql/fixes/fix-missing-request-wal-hot.sql`
- 自动化脚本: `scripts/apply-fix-252.sh`
- 诊断工具: `scripts/diagnose-request-logging.sh`

---

## 联系支持

如果遇到问题：

1. 查看详细错误日志
2. 参考故障排查部分
3. 联系技术负责人: @xutaohuang

---

## 开始执行

准备好后，执行：

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
./scripts/apply-fix-252.sh
```

脚本会引导你完成整个修复过程。
