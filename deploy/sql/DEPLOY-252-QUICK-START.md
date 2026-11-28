# 252服务器数据库部署 - 快速启动指南

**目标**: 在252服务器上部署和修正供应商画像系统数据库

## 前提条件

- 252服务器可访问（192.168.1.252:25022）
- PostgreSQL已安装并运行
- 有数据库访问权限（postgres用户）

## 一键部署

```bash
# 克隆或进入项目目录
cd llm-gateway-go

# 执行自动化脚本
./deploy/sql/verify-and-fix-252.sh
```

脚本会自动处理所有步骤并显示详细进度。

## 如果自动化脚本失败

### 步骤1: 连接到252服务器

```bash
ssh -p 25022 root@192.168.1.252
```

### 步骤2: 进入项目目录

```bash
cd /data/llm-gateway-go  # 或实际路径
git pull origin main
```

### 步骤3: 执行数据库迁移

```bash
# 执行原始迁移（创建表）
psql -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-system.sql

# 执行修正脚本（添加约束和优化）
psql -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-fixes.sql
```

### 步骤4: 验证部署

```bash
# 检查表数量
psql -U postgres -d llm_gateway -c "
  SELECT COUNT(*) FROM pg_tables 
  WHERE schemaname='public' 
  AND (tablename LIKE 'provider_profile%' 
       OR tablename = 'provider_credibility_tests'
       OR tablename = 'provider_cost_reconciliation');
"
# 应该返回: 6

# 检查索引数量
psql -U postgres -d llm_gateway -c "
  SELECT COUNT(*) FROM pg_indexes 
  WHERE schemaname='public' 
  AND (indexname LIKE '%provider_profile%'
       OR indexname LIKE '%ppm_%'
       OR indexname LIKE '%ppd_%'
       OR indexname LIKE '%pct_%'
       OR indexname LIKE '%ppa_%');
"
# 应该返回: 15+
```

## 验证成功标志

✅ 6个表已创建  
✅ 15+个索引已创建  
✅ 12+个约束已添加  
✅ 1个触发器已创建  
✅ credentials表已扩展4个字段

## 故障排除

### 问题: SSH连接超时

```bash
# 检查网络连接
ping 192.168.1.252

# 检查SSH端口
nc -zv 192.168.1.252 25022
```

### 问题: 数据库连接失败

```bash
# 检查PostgreSQL是否运行
ssh -p 25022 root@192.168.1.252 "systemctl status postgresql"

# 检查数据库是否存在
ssh -p 25022 root@192.168.1.252 "psql -U postgres -l | grep llm_gateway"
```

### 问题: 表已存在错误

这是正常的！脚本使用 `CREATE TABLE IF NOT EXISTS`，会跳过已存在的表。

### 问题: 约束冲突错误

如果修正脚本失败：

```bash
# 检查现有约束
psql -U postgres -d llm_gateway -c "
  SELECT constraint_name, table_name 
  FROM information_schema.table_constraints 
  WHERE table_name LIKE 'provider_%';
"

# 如果需要，删除冲突约束后重试
```

## 下一步

1. 检查应用日志: `tail -f /var/log/llm-gateway/gateway.log | grep "provider profile"`
2. 等待2小时后检查数据: `SELECT COUNT(*) FROM provider_profile_metrics;`
3. 次日检查聚合数据: `SELECT COUNT(*) FROM provider_profile_daily;`

## 获取帮助

详细文档: `deploy/sql/252-DATABASE-ISSUES-AND-FIXES.md`
