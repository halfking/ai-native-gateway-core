# 71服务器问题修复 - 快速开始

## 🚨 立即在71服务器上执行

### 1️⃣ 拉取最新代码（2分钟）
```bash
ssh root@llmgateway.internal.example.com
cd /opt/llm-gateway-go
git fetch origin
git checkout server-71
git pull origin server-71
```

### 2️⃣ 运行诊断（3分钟）
```bash
./scripts/diagnose_routing_issue.sh minimax-m3 | tee diagnosis.txt
```

**查看诊断结果，关注：**
- `is_routable` 列 - 应该有 `true` 值
- `empty_response_count` - 应该很低
- `no_candidate_count` - 应该为 0

### 3️⃣ 根据诊断结果选择修复方案

#### 🔧 方案A：代码过期 → 更新代码
```bash
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway
```

#### 🔧 方案B：凭据状态异常 → 重置凭据
```bash
# 先查看问题
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/fix_credentials_state.sql

# 如果需要修复，编辑脚本取消注释修复SQL，然后执行
# vim scripts/fix_credentials_state.sql  # 取消注释第5节
# psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/fix_credentials_state.sql
```

#### 🔧 方案C：紧急情况 → 强制重置所有凭据
```bash
psql "$LLM_GATEWAY_DATABASE_URL" -f scripts/emergency_fix_credentials.sql
# 然后手动执行: COMMIT;
systemctl restart llm-gateway
```

### 4️⃣ 验证修复（2分钟）
```bash
# 测试路由
curl "http://llmgateway.internal.example.com/api/routing/resolve?model=minimax-m3" | jq .

# 测试请求
./scripts/test_routing_fix.sh
```

### 5️⃣ 监控恢复
```bash
# 实时监控日志
journalctl -u llm-gateway -f

# 监控成功率（另开窗口）
watch -n 10 'psql "$LLM_GATEWAY_DATABASE_URL" -c "
SELECT 
    COUNT(*) as total,
    COUNT(CASE WHEN success THEN 1 END) as success,
    ROUND(COUNT(CASE WHEN success THEN 1 END)::numeric/COUNT(*)::numeric*100,1) as rate
FROM request_logs WHERE ts > NOW() - INTERVAL '\''5 minutes'\''"'
```

## 📊 期望结果

- ✅ 路由解析返回至少1个可用凭据
- ✅ 测试请求返回正常响应
- ✅ 成功率 > 95%
- ✅ empty_response < 5%
- ✅ no_candidate = 0

## 🆘 如果问题仍未解决

查看详细文档：
```bash
cat docs/HOTFIX_71_SERVER_ROUTING.md
cat DEPLOYMENT_CHECKLIST.md
```

或联系支持团队提供诊断输出：
```bash
cat diagnosis.txt
journalctl -u llm-gateway --since "1 hour ago" > service.log
```

---
**紧急程度：P0 - 立即处理**
**预计修复时间：15-30分钟**
