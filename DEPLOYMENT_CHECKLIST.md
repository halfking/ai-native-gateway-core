# 71服务器紧急修复部署清单

## 🚨 紧急情况 - 立即执行

### 前置条件
- [ ] 确认可以SSH到71服务器 (`ssh root@llmgateway.internal.example.com`)
- [ ] 确认可以连接到184数据库
- [ ] 确认当前在 `server-71` 分支

### 步骤1: 诊断问题（5分钟）

在71服务器上执行：

```bash
cd /opt/llm-gateway-go
./scripts/diagnose_routing_issue.sh minimax-m3 > /tmp/diagnosis_$(date +%Y%m%d_%H%M%S).txt
```

**检查输出中的关键指标：**
- [ ] `is_routable` 是否有 `true` 的凭据？
- [ ] `empty_response_count` 是否很高？
- [ ] `no_candidate_count` 是否很高？
- [ ] 质量门限过滤了多少凭据？

### 步骤2: 检查代码版本（2分钟）

```bash
cd /opt/llm-gateway-go
git log --oneline -1
```

**期望输出：** `78de1295 fix(relay): resolve empty_response misclassification`

如果不是这个版本，需要更新代码（见步骤3）。

### 步骤3: 更新代码（5分钟）

**选项A: 在本地机器上推送到服务器**

```bash
# 在你的开发机器上
cd __DEV_HOME__/workspace/llm-gateway-go-2
./scripts/deploy_fix_to_71.sh root@llmgateway.internal.example.com
```

**选项B: 直接在71服务器上更新**

```bash
# 在71服务器上
cd /opt/llm-gateway-go
git fetch origin
git checkout server-71
git pull origin server-71
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway
```

### 步骤4: 修复数据库凭据状态（5分钟）

**⚠️ 警告：此操作会重置所有凭据状态，请谨慎执行**

```bash
# 在71服务器上
cd /opt/llm-gateway-go

# 先诊断
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/fix_credentials_state.sql

# 如果需要修复，执行紧急修复脚本
# psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/emergency_fix_credentials.sql
# 然后手动执行 COMMIT; 确认修复
```

**或者手动修复关键凭据：**

```sql
-- 连接数据库
psql "$LLM_GATEWAY_DATABASE_URL"

-- 重置 minimax 凭据
UPDATE credentials 
SET availability_state = 'ready',
    circuit_state = 'closed',
    lifecycle_status = 'active',
    quota_state = 'ok',
    availability_recover_at = NULL,
    cooling_until = NULL,
    consecutive_failures = 0
WHERE label LIKE '%minimax%'
  AND status = 'active';

-- 清除探测状态
UPDATE model_probe_state 
SET state = 'pending',
    consecutive_failures = 0,
    next_probe_at = NOW()
WHERE lower(raw_model_name) = 'minimax-m3'
  AND state = 'broken_confirmed';

COMMIT;
```

### 步骤5: 重启服务（2分钟）

```bash
systemctl restart llm-gateway
sleep 3
systemctl status llm-gateway
journalctl -u llm-gateway -f
```

**检查日志中是否有错误：**
- [ ] 无 panic 或 fatal 错误
- [ ] 数据库连接成功
- [ ] Redis连接成功
- [ ] 服务监听在 8781 端口

### 步骤6: 验证修复（5分钟）

```bash
# 测试健康检查
curl http://llmgateway.internal.example.com/healthz

# 测试路由解析
curl "http://llmgateway.internal.example.com/api/routing/resolve?model=minimax-m3" | jq '.candidates[] | {credential_id, routable, runtime_routable}'

# 测试实际请求
./scripts/test_routing_fix.sh
```

**期望结果：**
- [ ] 健康检查返回 200
- [ ] 路由解析返回至少1个 `routable: true` 的凭据
- [ ] 实际请求返回正常响应（不是 503 或 empty_response）

### 步骤7: 监控恢复（10分钟）

```bash
# 监控request_logs
watch -n 5 'psql "$LLM_GATEWAY_DATABASE_URL" -c "
SELECT 
    COUNT(*) as total_last_5min,
    COUNT(CASE WHEN success THEN 1 END) as success,
    COUNT(CASE WHEN error_kind = '\''empty_response'\'' THEN 1 END) as empty_response,
    COUNT(CASE WHEN error_kind = '\''no_candidate'\'' THEN 1 END) as no_candidate
FROM request_logs 
WHERE ts > NOW() - INTERVAL '\''5 minutes'\''
"'
```

**期望趋势：**
- [ ] `success` 数量上升
- [ ] `empty_response` 数量下降到 < 5%
- [ ] `no_candidate` 数量为 0

## 🔧 如果问题仍未解决

### 场景A: 仍然返回 "no_candidate"

可能原因：
1. 所有凭据都被质量门限过滤
2. 数据库视图未刷新

**解决方案：**

```sql
-- 临时降低质量门限
-- 在代码中修改 provider/client.go:586 行
-- thresholds := []float64{0.3, 0.0}  改为
-- thresholds := []float64{0.0}

-- 或者直接刷新视图
REFRESH MATERIALIZED VIEW CONCURRENTLY v_routable_credential_models;
```

### 场景B: 仍然返回 empty_response

可能原因：
1. 代码未部署最新版本
2. 上游真的返回空响应

**解决方案：**

```bash
# 确认代码版本
git log --oneline -1 | grep 78de1295

# 如果不是，强制更新
git reset --hard origin/server-71
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway
```

### 场景C: 路由到错误的凭据

可能原因：
1. 粘性路由缓存问题
2. 质量门限选择了低质量凭据

**解决方案：**

```bash
# 清除Redis缓存
redis-cli FLUSHDB

# 或者重启服务清除内存缓存
systemctl restart llm-gateway
```

## 📊 后续监控

### 添加告警监控

```bash
# 添加到 crontab
*/5 * * * * /opt/llm-gateway-go/scripts/health_check_alert.sh
```

### 关键指标

1. **凭据可用性**
   ```sql
   SELECT COUNT(*) FROM credentials 
   WHERE status='active' AND availability_state='ready';
   ```
   期望: >= 3

2. **请求成功率**
   ```sql
   SELECT 
       ROUND(COUNT(CASE WHEN success THEN 1 END)::numeric / COUNT(*)::numeric * 100, 2) as success_rate
   FROM request_logs 
   WHERE ts > NOW() - INTERVAL '5 minutes';
   ```
   期望: >= 95%

3. **empty_response 占比**
   ```sql
   SELECT 
       ROUND(COUNT(CASE WHEN error_kind='empty_response' THEN 1 END)::numeric / COUNT(*)::numeric * 100, 2) as pct
   FROM request_logs 
   WHERE ts > NOW() - INTERVAL '5 minutes';
   ```
   期望: < 5%

## 📞 升级路径

如果30分钟内无法修复：

1. **回滚到上一个稳定版本**
   ```bash
   cd /opt/llm-gateway-go
   git checkout <上一个稳定的commit>
   go build -o llm-gateway ./cmd/gateway
   systemctl restart llm-gateway
   ```

2. **切换到备用凭据**
   ```sql
   -- 临时提高备用凭据优先级
   UPDATE model_offers 
   SET manual_priority = 1
   WHERE credential_id IN (/* 备用凭据ID */);
   ```

3. **联系支持团队**
   - 提供诊断脚本输出
   - 提供服务日志
   - 提供数据库快照

## ✅ 完成检查表

- [ ] 代码已更新到最新版本
- [ ] 凭据状态已重置
- [ ] 服务已重启
- [ ] 健康检查通过
- [ ] 路由解析返回可用凭据
- [ ] 实际请求成功
- [ ] empty_response < 5%
- [ ] no_candidate = 0
- [ ] 监控告警已配置

## 📝 事后总结

修复完成后，记录以下信息：

1. 根本原因是什么？
2. 哪些凭据/模型受影响？
3. 修复花费了多少时间？
4. 未来如何预防？

---

**最后更新时间:** 2026-06-26
**文档版本:** 1.0
**紧急联系:** [添加联系方式]
