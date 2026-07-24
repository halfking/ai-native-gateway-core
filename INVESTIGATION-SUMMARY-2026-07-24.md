# 客户端自动取消问题 - 调查总结

## 🎯 核心发现

### 问题描述
VSCode Copilot 连接 llm.kxpms.cn 后，请求立即停止，日志显示 `probe-client_cancel-cred21-xxx`，但**用户没有手动取消操作**。

### 关键线索

#### 1. 服务器配置（已确认）✅
```go
// cmd/gateway/main.go:3945
srv := &http.Server{
    ReadTimeout:  300 * time.Second,  // 5分钟
    WriteTimeout: 0,                  // 无限制！
    IdleTimeout:  300 * time.Second,  // 5分钟
}
```

**结论**：服务器不会主动因超时而关闭连接。`WriteTimeout=0` 意味着可以无限写入。

#### 2. 代码逻辑（已确认）✅
```go
// domains/streaming/stream.go:604
select {
case <-ctx.Done():
    // 只有当上下文被取消时才会进入这里
    outcome.Reason = "client_cancel"
    return outcome
}
```

**结论**：`client_cancel` 只会在 `ctx.Done()` 触发时记录，即连接被外部关闭。

---

## 🔍 最可能的原因（按概率排序）

### 1. Nginx/ALB 反向代理超时 ⭐⭐⭐⭐⭐ 90%

**证据**：
- 服务器 `WriteTimeout=0`，不会主动超时
- `ctx.Done()` 被触发 → 外部关闭了连接
- 架构：VSCode → **Nginx(252)** → Gateway

**Nginx 默认超时**：
```nginx
proxy_read_timeout 60s;   # 默认60秒
proxy_send_timeout 60s;
proxy_connect_timeout 60s;
```

**触发场景**：
- Copilot 发送请求 → Nginx 转发到 Gateway → 等待上游 LLM 响应
- 如果 60 秒内没有数据传输 → **Nginx 主动关闭连接**
- Gateway 检测到 `ctx.Done()` → 记录为 `client_cancel`

**为什么是60秒？**
- Reasoning models (o1, DeepSeek-R1) 思考时间可能超过 60 秒
- 在思考期间，上游没有任何 SSE chunk 返回
- Nginx 认为连接"无响应"，主动切断

**验证方法**：
```bash
# 查看 252 服务器 Nginx 配置
ssh 252 "cat /etc/nginx/sites-enabled/llm.kxpms.cn | grep proxy_.*timeout"

# 期望看到：
# proxy_read_timeout 60s;
# proxy_send_timeout 60s;
```

**解决方案**：
```nginx
# 修改 Nginx 配置，增加超时到 5 分钟
location /v1/ {
    proxy_pass http://gateway_backend;
    proxy_read_timeout 300s;    # 增加到 5 分钟
    proxy_send_timeout 300s;
    proxy_connect_timeout 10s;
    proxy_buffering off;        # 确保流式传输不缓冲
}
```

---

### 2. VSCode/Copilot 客户端超时 ⭐⭐⭐ 5%

**可能性较低**，因为：
- Copilot 通常有较长的超时时间（2-3分钟）
- 如果是客户端超时，会在客户端侧显示错误提示
- 用户说"没有手动取消"，但没说是否看到超时错误

**验证方法**：
- 查看 VSCode 的输出面板（Output → Copilot）
- 检查是否有 timeout 相关的错误消息

---

### 3. 上游模型 (cred21) 响应过慢/无响应 ⭐⭐ 3%

**场景**：
- 请求路由到 cred21
- cred21 对应的上游模型挂了或极慢
- 在等待期间触发了某个超时（很可能是 Nginx）

**验证方法**：
```sql
-- 查看 cred21 最近的表现
SELECT 
    COUNT(*) as total,
    AVG(latency_ms) as avg_latency,
    SUM(CASE WHEN success THEN 1 ELSE 0 END)::float / COUNT(*) as success_rate
FROM request_logs 
WHERE credential_id = 21 
  AND event_at > NOW() - INTERVAL '1 hour';
```

---

### 4. 网络连接中断 ⭐ 2%

**可能性极低**，因为：
- 如果是网络中断，通常会有 TCP RST/FIN
- 日志会显示 "connection reset by peer" 等错误
- 不会干净地记录为 `client_cancel`

---

## 🎬 下一步行动计划

### 立即执行（今天）

1. **检查 Nginx 配置** ⏰ 10分钟
   ```bash
   ssh 252 "cat /etc/nginx/sites-enabled/llm.kxpms.cn"
   ```
   
2. **查看实际日志数据**（修复已部署后）⏰ 5分钟
   ```sql
   SELECT 
       request_id,
       latency_ms,
       request_preview,
       credential_id
   FROM request_logs 
   WHERE request_id LIKE 'probe-client_cancel%' 
   ORDER BY event_at DESC 
   LIMIT 5;
   ```
   
   **关键判断**：
   - 如果 `latency_ms` 接近 60000ms (60秒) → **确认是 Nginx 超时**
   - 如果 `request_preview` 显示大量 messages → 可能是请求过大
   - 如果多个请求都是 cred21 → 可能是该凭据有问题

3. **临时解决方案** ⏰ 5分钟
   ```bash
   # 如果确认是 Nginx 超时，立即修改配置
   ssh 252
   sudo vim /etc/nginx/sites-enabled/llm.kxpms.cn
   # 添加或修改：
   #   proxy_read_timeout 300s;
   #   proxy_send_timeout 300s;
   sudo nginx -t
   sudo systemctl reload nginx
   ```

### 短期（1-2天）

4. **优化路由策略**
   - 如果 cred21 普遍慢，降低其优先级
   - 将快速响应的凭据提到前面

5. **添加预警机制**
   - 监控 Nginx access log 中的 504/502 错误
   - 监控 `client_cancel` 的频率

### 中期（1周）

6. **实现 SSE Keepalive**
   ```go
   // 在上游长时间无响应时，定期发送注释行保持连接活跃
   // : keepalive\n\n
   ```

7. **添加更细粒度的超时类型**
   ```go
   if latency >= 59.5 && latency <= 60.5 {
       errorKind = "likely_nginx_timeout_60s"
   }
   ```

---

## 📊 预期结果

### 如果是 Nginx 超时（90% 概率）
- ✅ 修改 Nginx 配置后，问题立即解决
- ✅ `latency_ms` 会显示接近 60000ms
- ✅ 修改后，Copilot 可以正常等待长时间响应

### 如果不是 Nginx 超时（10% 概率）
- 🔄 需要查看实际日志中的 `request_preview` 和 `latency_ms`
- 🔄 根据数据进一步分析其他可能原因

---

## 📝 测试验证

### 方法 1：手动模拟 Copilot 请求
```bash
# 使用 curl 测试，观察是否在 60 秒时断开
time curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-reasoner",
    "messages": [
      {"role": "user", "content": "请详细推理：为什么1+1=2？"}
    ],
    "stream": true
  }' \
  -N  # 不缓冲输出

# 如果在 60 秒时断开 → 确认是 Nginx
# 如果能正常完成 → 可能是客户端问题
```

### 方法 2：使用 VSCode Copilot 直接测试
1. 修复部署后，打开 VSCode
2. 使用 Copilot 发送一个需要长时间思考的问题
3. 观察是否在 60 秒时断开
4. 查看数据库中新生成的日志记录

---

## 🎯 结论

**最可能的原因**：Nginx 反向代理的 60 秒超时限制

**建议的快速解决方案**：
1. 检查并修改 Nginx 配置，将 `proxy_read_timeout` 和 `proxy_send_timeout` 增加到 300 秒
2. 部署日志修复（已完成），收集实际数据验证
3. 如果确认是 Nginx 超时，考虑实现 SSE keepalive 机制

**预计修复时间**：如果是 Nginx 超时，修改配置后 5 分钟内生效。

---

**文档更新时间**：2026-07-24  
**状态**：待验证 - 需要检查 Nginx 配置和实际日志数据
