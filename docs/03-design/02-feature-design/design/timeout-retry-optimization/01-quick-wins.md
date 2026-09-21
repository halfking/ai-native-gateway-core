# 快速见效方案（Quick Wins）

## 🎯 立即可做的3个优化（今天就能上线）

### Quick Win 1: 调整超时配置（5分钟）⚡

**问题**: 当前30秒超时导致13%请求失败

**解决**: 立即调整154服务器配置

```bash
# SSH到154服务器
ssh root@<env:HOST_154_IP> -p 25022

# 备份现有配置
cp /etc/llm-gateway-go/env /etc/llm-gateway-go/env.bak.$(date +%Y%m%d-%H%M%S)

# 修改超时时间
sed -i 's/LLM_GATEWAY_UPSTREAM_TIMEOUT=30/LLM_GATEWAY_UPSTREAM_TIMEOUT=90/' /etc/llm-gateway-go/env

# 验证
grep UPSTREAM_TIMEOUT /etc/llm-gateway-go/env

# 重启服务
systemctl restart llm-gateway-go

# 验证服务状态
systemctl status llm-gateway-go
journalctl -u llm-gateway-go -n 20 -f
```

**预期效果**:
- ✅ 超时率从 13% → 3%
- ✅ 立即生效
- ✅ 零代码改动

**监控验证**:
```bash
# 30分钟后检查超时情况
ssh root@<env:HOST_154_IP> -p 25022 "journalctl -u llm-gateway-go --since '30 minutes ago' --no-pager | grep -c 'stream_timeout'"
```

---

### Quick Win 2: 启用 Pre-Stream Keepalive（10分钟）⚡

**问题**: 客户端在等待时容易超时断开

**解决**: 启用已有的 keepalive 功能

```bash
# 在 /etc/llm-gateway-go/env 添加
echo "LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=true" >> /etc/llm-gateway-go/env
echo "LLM_GATEWAY_KEEPALIVE_INTERVAL=15" >> /etc/llm-gateway-go/env

# 重启服务
systemctl restart llm-gateway-go
```

**代码已存在**: `config/config.go:75` 已有 `EnablePreStreamKeepalive` 配置

**预期效果**:
- ✅ 客户端不会因为等待过久而超时
- ✅ 每15秒发送一次 keepalive 注释
- ✅ 立即生效

---

### Quick Win 3: 调整重试阈值（5分钟）⚡

**问题**: Stream 失败后立即熔断，没有给足够重试机会

**解决**: 调整重试阈值

```bash
# 当前默认值较小，增加到允许更多重试
echo "LLM_GATEWAY_STREAM_RETRY_THRESHOLD=3" >> /etc/llm-gateway-go/env

# 重启
systemctl restart llm-gateway-go
```

**预期效果**:
- ✅ 允许最多3次重试再熔断
- ✅ 提高成功率

---

## 📊 验证效果的SQL查询

### 查询1: 超时率统计（过去1小时）

```sql
SELECT 
    COUNT(*) FILTER (WHERE success = true) as success_count,
    COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') as timeout_count,
    COUNT(*) as total_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') / COUNT(*), 2) as timeout_rate_percent
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
```

### 查询2: 平均延迟分布（过去1小时）

```sql
SELECT 
    CASE 
        WHEN latency_ms < 10000 THEN '<10s'
        WHEN latency_ms < 30000 THEN '10-30s'
        WHEN latency_ms < 60000 THEN '30-60s'
        WHEN latency_ms < 90000 THEN '60-90s'
        ELSE '>90s'
    END as latency_bucket,
    COUNT(*) as request_count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER(), 2) as percentage
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY latency_bucket
ORDER BY MIN(latency_ms);
```

### 查询3: Minimax模型专项统计

```sql
SELECT 
    client_model,
    provider_id,
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE success = true) as success,
    COUNT(*) FILTER (WHERE success = false) as failed,
    ROUND(AVG(latency_ms)/1000.0, 2) as avg_latency_seconds,
    MAX(latency_ms)/1000 as max_latency_seconds
FROM request_logs
WHERE client_model LIKE '%minimax%'
  AND created_at > NOW() - INTERVAL '1 hour'
GROUP BY client_model, provider_id
ORDER BY total DESC;
```

---

## 🔄 回滚方案（如果出问题）

```bash
# 恢复备份
cd /etc/llm-gateway-go
ls -lt env.bak.* | head -1  # 找到最新备份
cp env.bak.YYYYMMDD-HHMMSS env

# 重启
systemctl restart llm-gateway-go
```

---

## 📈 预期对比

### 调整前（当前）
```
总请求: 514
成功: 447 (87%)
超时: 67 (13%)
平均延迟: 11.6秒
```

### 调整后（预期30分钟后）
```
总请求: ~200
成功: 194 (97%)
超时: 6 (3%)
平均延迟: 12.2秒 (略微增加但成功率提升)
```

---

## ⏭️ 下一步：中期优化（本周内）

完成Quick Wins后，可以开始实施中期方案：

1. **动态超时计算**（2-3天）
   - 根据上下文大小自动调整超时
   - 代码改动：`domains/streaming/executor.go`

2. **继续/重试检测**（3-4天）
   - 检测"请继续"类提示词
   - 复用已有响应缓存

3. **节点切换通知**（2-3天）
   - SSE event: node_switch
   - 客户端实时知道切换状态

详见主设计文档 `00-design-spec.md`

---

**执行时间**: 立即  
**预计耗时**: 20分钟  
**风险等级**: 低（可快速回滚）  
**预期收益**: 超时率降低10%，每天节省约$500
