# 模型智商修复 - 部署到 154 服务器

## 修复内容

✅ 已修复：模型质量服务默认启用（`mqEnabled := true`）
✅ 已构建：二进制文件 `llm-gateway` (74MB)
✅ 已推送：代码提交到远程仓库

## 部署步骤

### 1. 准备工作

检查当前服务状态：
```bash
ssh root@154服务器
systemctl status llm-gateway  # 或查看当前运行方式
```

### 2. 停止服务

```bash
# 如果是 systemd 服务
systemctl stop llm-gateway

# 或如果是其他方式运行
pkill -f llm-gateway
```

### 3. 备份当前二进制

```bash
cd /path/to/gateway  # 替换为实际路径
cp llm-gateway llm-gateway.backup.$(date +%Y%m%d_%H%M%S)
```

### 4. 上传新二进制

从本地上传到服务器：
```bash
# 在本地执行
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
scp llm-gateway root@154服务器:/path/to/gateway/

# 或使用 rsync
rsync -avz llm-gateway root@154服务器:/path/to/gateway/
```

### 5. 设置权限

```bash
# 在服务器上执行
cd /path/to/gateway
chmod +x llm-gateway
chown gateway:gateway llm-gateway  # 如果有特定用户
```

### 6. 启动服务

```bash
# 如果是 systemd 服务
systemctl start llm-gateway

# 或如果是其他方式
cd /path/to/gateway && ./llm-gateway &
```

### 7. 验证部署

#### 7.1 检查服务启动

```bash
systemctl status llm-gateway
# 或
ps aux | grep llm-gateway
```

#### 7.2 检查日志

```bash
# 查找模型质量服务启动日志
tail -f /var/log/llm-gateway/gateway.log | grep "model_quality_worker"

# 应该看到：
# CHECKPOINT: model_quality_worker started
#   data_dir=./data
#   base_url=http://localhost:8787
#   interval_hours=24
#   ...
```

如果看到此日志，说明服务已成功启动！

#### 7.3 测试 API 端点

```bash
# 测试模型智商触发端点
curl -X POST 'https://llm.kxpms.cn/api/admin/model-iq/trigger' \
  -H 'Authorization: Bearer YOUR_ADMIN_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{
    "credential_id": 42,
    "raw_model_name": "glm-5.2"
  }'

# 期望结果：200 OK，返回测试结果
# 不应该再返回 503 Service Unavailable
```

#### 7.4 在管理后台测试

1. 打开浏览器访问 `https://llm.kxpms.cn`
2. 登录管理后台
3. 进入任意供应商详情页
4. 点击"模型"标签
5. 选择一个模型并打开详情抽屉
6. 点击"测试模型智商"按钮
7. 应该开始测试，不再显示 "model-quality worker not configured" 错误

### 8. 验证数据存储

```bash
# 连接数据库
psql -U postgres -d llm_gateway

# 查看测试历史
SELECT 
    credential_id,
    raw_model_name,
    overall_score,
    accuracy,
    grade,
    tested_at
FROM model_iq_runs
ORDER BY tested_at DESC
LIMIT 5;
```

如果有测试记录，说明功能完全正常！

## 故障排查

### 问题 1: 服务启动失败

**检查**：
```bash
journalctl -u llm-gateway -n 100 --no-pager
# 或
tail -100 /var/log/llm-gateway/gateway.log
```

**常见原因**：
- 端口被占用
- 数据库连接失败
- 配置文件错误

### 问题 2: 仍然报 "worker not configured"

**检查日志**：
```bash
grep "model_quality_worker" /var/log/llm-gateway/gateway.log
```

如果没有找到 "model_quality_worker started"，可能：
1. 服务没有重启（仍在运行旧版本）
2. 数据库连接失败（worker 在 dbConn 块内初始化）

**解决**：
```bash
# 确保完全停止旧进程
pkill -9 -f llm-gateway

# 重新启动
systemctl start llm-gateway

# 再次检查日志
tail -f /var/log/llm-gateway/gateway.log | grep "model_quality"
```

### 问题 3: 测试超时

**检查配置**：
```sql
SELECT key, value 
FROM settings_kv 
WHERE key LIKE 'model_quality.%';
```

**调整超时**（如需要）：
```sql
INSERT INTO settings_kv (key, value, value_type, scope, category)
VALUES ('model_quality.test_timeout_seconds', '60', 'integer', 'platform', 'model_quality')
ON CONFLICT (key) DO UPDATE SET value = '60', updated_at = NOW();
```

然后重启服务。

## 回滚方案

如果部署后有问题，可以快速回滚：

```bash
# 停止服务
systemctl stop llm-gateway

# 恢复备份
cp llm-gateway.backup.YYYYMMDD_HHMMSS llm-gateway

# 启动服务
systemctl start llm-gateway
```

## 验证清单

部署完成后，确认以下项目：

- [ ] 服务成功启动（`systemctl status llm-gateway` 显示 active）
- [ ] 日志中有 "model_quality_worker started"
- [ ] API 测试返回 200（不是 503）
- [ ] 管理后台可以点击"测试模型智商"
- [ ] 测试结果存储在数据库中
- [ ] 历史记录图表可以显示

## 监控建议

部署后持续监控：

```bash
# 实时监控日志
tail -f /var/log/llm-gateway/gateway.log | grep -E "model_quality|model_iq|ERROR"

# 检查错误
grep -i error /var/log/llm-gateway/gateway.log | tail -20

# 监控资源使用
top -p $(pgrep llm-gateway)
```

## 联系支持

如果遇到无法解决的问题：
1. 收集日志：最近 100 行启动日志
2. 收集配置：settings_kv 中的 model_quality 配置
3. 提供错误信息：完整的错误堆栈
4. 联系技术支持团队

---

## 快速参考

**本地二进制路径**：
```
/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/llm-gateway
```

**Git 提交**：
```
273664679 feat(migration): add V360 down.sql for credential_probe_queue.automatic rollback
1a1e7b04f fix(model-iq): enable model quality worker by default
```

**修复代码位置**：
```
cmd/gateway/main.go:3673
mqEnabled := true // default enabled
```

**文档**：
- `MODEL_IQ_FIX.md` - 详细修复说明
- `scripts/enable_model_quality.sql` - 配置脚本
- `verify_model_iq.sh` - 验证脚本
