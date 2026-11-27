# Dev环境部署 - 快速开始

**5步完成dev环境迁移和验证**

---

## 🚀 快速执行（15分钟）

### 第1步: 设置环境变量 (1分钟)

```bash
export DB_HOST="dev-db-host"              # 修改为实际主机
export DB_PORT="5432"
export DB_NAME="llm_gateway_dev"
export DB_USER="admin"                    # 修改为实际用户
export PGPASSWORD="否有意别名"   # 修改为实际密码
```

---

### 第2步: 备份数据库 (2-3分钟)

```bash
mkdir -p backups
pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" "$DB_NAME" \
  > backups/dev_backup_$(date +%Y%m%d_%H%M%S).sql
```

---

### 第3步: 应用迁移 (3-5分钟)

```bash
cd /Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5
bash tmp/apply-migrations-dev.sh
```

**提示**: 脚本会要求两次确认：
1. 确认已备份 → 输入 `yes`
2. 确认执行迁移 → 输入 `yes`

---

### 第4步: 验证结果 (1-2分钟)

```bash
bash tmp/verify-migrations-dev.sh
```

**预期**: 看到 ✅ 验证完成

---

### 第5步: 部署并测试 (5-8分钟)

```bash
# 编译
go build -o llm-gateway ./cmd/gateway

# 部署（修改为实际服务器）
DEV_SERVER="dev-server-host"
scp llm-gateway $DEV_SERVER:/opt/llm-gateway/
ssh $DEV_SERVER 'sudo systemctl restart llm-gateway'

# 测试API
export API_HOST="$DEV_SERVER:8080"
export ADMIN_TOKEN="新增一个**设置器**语义（"  # 修改为实际token
bash tmp/auto-test-api.sh
```

---

## ✅ 成功标志

如果看到以下输出，说明部署成功：

```
✅ 所有迁移已成功应用到dev环境
✅ 验证完成
✓ 5个API端点路由存在
```

---

## ⚠️ 如果出错

**问题**: 连接失败  
**解决**: 检查 DB_HOST, DB_USER, PGPASSWORD 是否正确

**问题**: 迁移失败  
**解决**: 查看详细指南 `tmp/DEV-DEPLOYMENT-GUIDE.md`

**问题**: API测试失败  
**解决**: 确认服务已重启，token有效

---

## 📄 完整文档

需要更多信息？查看：
- **完整指南**: `tmp/DEV-DEPLOYMENT-GUIDE.md` (10KB，包含故障排查)
- **测试报告**: `tmp/FINAL-COMPLETION-REPORT.md` (测试环境验证结果)

---

**预计总耗时**: 15-20分钟  
**难度**: ⭐⭐☆☆☆ (简单)
