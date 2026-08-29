# LLM Gateway 部署与监控环境文档

**版本**: v1.0  
**更新日期**: 2026-08-29  
**维护者**: DevOps Team  

---

## 环境概览

### 测试环境（245）
- **主机**: 8.136.114.245
- **SSH**: `ssh -p 25022 root@8.136.114.245`
- **服务**: llmgo-245.service
- **端口**: 8781
- **用途**: 预生产测试、新功能验证
- **域名**: llmgo.kxpms.cn

### 生产环境（154）
- **主机**: 待补充
- **SSH**: 待补充
- **服务**: llmgo-154.service
- **端口**: 8781
- **用途**: 生产环境
- **域名**: 待补充

---

## 数据库连接

### PostgreSQL 主库（252）
```
Host: 172.16.2.210
Port: 5432
Database: llm_gateway
User: llm_gateway
Password: 4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg

Connection String:
postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable
```

### 关键表
- `request_logs_hot` - 请求日志元数据
- `request_logs_bodies_hot` - 请求/响应完整体
- `tool_call_events` - 工具调用事件
- `tool_usage_stats_hot` - 工具使用统计

---

## 服务管理

### 查看服务状态
```bash
ssh -p 25022 root@8.136.114.245 "systemctl status llmgo-245.service"
```

### 查看实时日志
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service -f"
```

### 查看错误日志
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '1 hour ago' | grep -E '(ERROR|WARN)'"
```

### 重启服务
```bash
ssh -p 25022 root@8.136.114.245 "systemctl restart llmgo-245.service"
```

### 查看服务版本
```bash
curl http://8.136.114.245:8781/api/system/version
```

---

## 数据库查询

### 连接数据库
```bash
ssh -p 25022 root@8.136.114.245 'PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway'
```

### 常用查询

#### 1. 请求统计（最近 N 小时）
```sql
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE success = false) AS failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '24 hours';
```

#### 2. 错误类型分布
```sql
SELECT 
    error_kind,
    COUNT(*) AS count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) AS percentage
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '24 hours'
  AND success = false
GROUP BY error_kind
ORDER BY count DESC;
```

#### 3. 模型请求分布
```sql
SELECT 
    COALESCE(outbound_model, client_model) AS model,
    COUNT(*) AS request_count,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '24 hours'
GROUP BY COALESCE(outbound_model, client_model)
ORDER BY request_count DESC
LIMIT 20;
```

#### 4. MiniMax/GLM 特定查询
```sql
SELECT 
    COALESCE(outbound_model, client_model) AS model,
    COUNT(*) AS request_count,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    COUNT(*) FILTER (WHERE error_kind LIKE '%malformed%' OR error_kind LIKE '%json%') AS malformed_errors
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '24 hours'
  AND (outbound_model LIKE '%minimax%' OR outbound_model LIKE '%glm%' 
       OR client_model LIKE '%minimax%' OR client_model LIKE '%glm%')
GROUP BY COALESCE(outbound_model, client_model)
ORDER BY request_count DESC;
```

#### 5. Tool Use 完整性检查
```sql
WITH tool_use_requests AS (
    SELECT 
        b.request_id,
        b.response_body,
        h.success,
        h.error_kind
    FROM request_logs_bodies_hot b
    JOIN request_logs_hot h ON b.request_id = h.request_id
    WHERE b.ts >= NOW() - INTERVAL '24 hours'
      AND b.response_body IS NOT NULL
      AND b.response_body::text LIKE '%tool_use%'
    LIMIT 1000
)
SELECT 
    COUNT(*) AS total_tool_use_requests,
    COUNT(*) FILTER (WHERE response_body::text LIKE '%tool_result%') AS complete_tool_calls,
    COUNT(*) FILTER (WHERE response_body::text NOT LIKE '%tool_result%') AS incomplete_tool_calls
FROM tool_use_requests;
```

---

## 监控指标

### Prometheus 端点
```
245: http://8.136.114.245:9090
154: 待补充
```

### 关键指标

#### SSE 验证指标
```promql
# 无效帧总数（按提供商和阶段）
llm_gateway_malformed_sse_frame_total

# 5 分钟内的无效帧率
rate(llm_gateway_malformed_sse_frame_total[5m])

# 按提供商分组
sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (provider)

# 按阶段分组
sum(rate(llm_gateway_malformed_sse_frame_total[5m])) by (stage)
```

#### 请求统计指标
```promql
# 请求总数
llm_gateway_requests_total

# 请求成功率
rate(llm_gateway_requests_total{status="success"}[5m]) / 
rate(llm_gateway_requests_total[5m])

# 延迟分布
histogram_quantile(0.95, rate(llm_gateway_request_duration_seconds_bucket[5m]))
```

#### 流式处理指标
```promql
# 合成 [DONE] 标记的流
llm_gateway_stream_synthesized_done_total

# Resume blocked 事件
llm_gateway_survival_resume_blocked_total
```

---

## 日志查询命令

### 1. 查找 malformed SSE frame
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '24 hours ago' --no-pager | grep -i 'malformed'"
```

### 2. 查找 JSON 解析错误
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '24 hours ago' --no-pager | grep -i 'json.*fail'"
```

### 3. 查找 survival resume blocked
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '24 hours ago' --no-pager | grep -i 'resume_blocked'"
```

### 4. 统计错误频率
```bash
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '24 hours ago' --no-pager | grep -E '(ERROR|WARN)' | cut -d' ' -f6- | sort | uniq -c | sort -rn | head -20"
```

---

## 部署流程

### 245 测试环境部署
```bash
# 1. 登录服务器
ssh -p 25022 root@8.136.114.245

# 2. 进入部署目录
cd /opt/llm-gateway-go

# 3. 备份当前版本
cp gateway gateway.backup.$(date +%Y%m%d_%H%M%S)

# 4. 拉取最新代码（或上传编译好的二进制）
# git pull && go build ./cmd/gateway
# 或 scp gateway root@8.136.114.245:/opt/llm-gateway-go/

# 5. 重启服务
systemctl restart llmgo-245.service

# 6. 检查服务状态
systemctl status llmgo-245.service

# 7. 查看启动日志
journalctl -u llmgo-245.service --since '1 minute ago'

# 8. 验证版本
curl http://localhost:8781/api/system/version
```

### 154 生产环境部署
```bash
# TODO: 补充生产部署流程
# 建议：金丝雀部署或灰度发布
```

---

## 回滚流程

### 快速回滚（使用备份）
```bash
# 1. 停止服务
systemctl stop llmgo-245.service

# 2. 恢复备份
cd /opt/llm-gateway-go
mv gateway gateway.failed
mv gateway.backup.YYYYMMDD_HHMMSS gateway

# 3. 启动服务
systemctl start llmgo-245.service

# 4. 验证
systemctl status llmgo-245.service
curl http://localhost:8781/api/system/version
```

---

## 监控检查清单

### 每日检查
- [ ] 服务运行状态
- [ ] 错误日志数量
- [ ] 请求成功率
- [ ] 主要模型可用性

### 每周检查
- [ ] 磁盘空间
- [ ] 内存使用率
- [ ] 数据库连接池
- [ ] 日志轮转

### 部署后检查
- [ ] 服务启动成功
- [ ] 版本号正确
- [ ] 无启动错误
- [ ] 关键指标正常
- [ ] 第一批请求成功

---

## 告警阈值

### 关键告警（P0）
- 服务宕机
- 数据库连接失败
- 成功率 < 50%
- 无效 SSE 帧率 > 10%

### 重要告警（P1）
- 成功率 < 80%
- 响应延迟 P95 > 10s
- 无效 SSE 帧率 > 5%
- 内存使用 > 90%

### 一般告警（P2）
- 成功率 < 95%
- 响应延迟 P95 > 5s
- 无效 SSE 帧率 > 1%

---

## 常见问题排查

### 问题 1: 服务无法启动
```bash
# 检查配置文件
cat /opt/llm-gateway-go/.env

# 检查端口占用
netstat -tlnp | grep 8781

# 检查日志
journalctl -u llmgo-245.service -n 100
```

### 问题 2: 数据库连接失败
```bash
# 测试数据库连接
PGPASSWORD="..." psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c "SELECT 1;"

# 检查网络
ping 172.16.2.210
telnet 172.16.2.210 5432
```

### 问题 3: 高错误率
```bash
# 查看错误分布
# SQL 查询见上文"错误类型分布"

# 查看最近失败的请求
ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '10 minutes ago' | grep -i error | tail -50"
```

---

## 审计脚本

### 位置
```
scripts/audit-incomplete-tool-calls.sh
```

### 使用方法
```bash
# 本地运行（需要数据库连接）
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
bash scripts/audit-incomplete-tool-calls.sh

# 远程运行
ssh -p 25022 root@8.136.114.245 'cd /opt/llm-gateway-go && bash scripts/audit-incomplete-tool-calls.sh'
```

---

## 联系方式

### 技术负责人
- **运维**: DevOps Team
- **开发**: Backend Team
- **紧急联系**: 待补充

### 文档更新
- 发现环境变更请更新本文档
- Git 路径: `docs/environment.md`

---

**最后更新**: 2026-08-29  
**版本**: v1.0
