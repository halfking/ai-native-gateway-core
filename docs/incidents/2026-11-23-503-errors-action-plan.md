# 503 错误修复行动计划
## 2026-11-23 Invalid Provider Keys

## 执行摘要（Executive Summary）

**状态**: 🔴 需要立即处理  
**影响**: 3 个自定义提供商模型不可用  
**根因**: API 密钥失效/过期  
**修复时间**: 10-15 分钟（更新密钥后立即生效）

## 需要修复的提供商（Providers Requiring Action）

### 1. ❌ custom/community/Marcus-Mok-GH/pt-harbor

**错误详情**:
```
API error 401: A valid API key is required
```

**数据库模型名**: `communitymarcus-mok-ghpt-harbor`

**修复步骤**:
1. 登录管理面板: https://llm.itestu.cn/admin/credentials
2. 搜索提供商: "Marcus-Mok-GH" 或 "pt-harbor"
3. 更新 API 密钥或禁用该提供商
4. 保存后系统将自动重新验证

### 2. ❌ custom/community/Catniti/gemini-4-hydrogen

**错误详情**:
```
API error 401: A valid API key is required
```

**数据库模型名**: `communitycatnitigemini-4-hydrogen`

**修复步骤**:
1. 登录管理面板: https://llm.itestu.cn/admin/credentials
2. 搜索提供商: "Catniti" 或 "gemini-4-hydrogen"
3. 更新 API 密钥或禁用该提供商
4. 保存后系统将自动重新验证

### 3. ❌ custom/minimax/minimax-h3

**错误详情**:
```
API error 401: A valid API key is required
```

**数据库模型名**: `minimaxminimax-h3`

**修复步骤**:
1. 登录管理面板: https://llm.itestu.cn/admin/credentials
2. 搜索提供商: "minimax" 和模型 "h3"
3. 更新 API 密钥或禁用该提供商
4. 保存后系统将自动重新验证

## 验证步骤（Verification）

### 1. 检查密钥更新是否生效

```bash
# 监控配置更新事件
ssh 211 "docker logs -f llm-gateway-211-a 2>&1 | grep 'auto route listener: refresh requested'"

# 应该看到类似这样的日志:
# {"msg":"auto route listener: refresh requested","payload":"credentials:UPDATE:99023"}
```

### 2. 检查认证错误是否消失

```bash
# 监控认证失败
ssh 211 "docker logs -f llm-gateway-211-a 2>&1 | grep 'node probe failed.*401'"

# 如果修复成功，不应再看到 401 错误
```

### 3. 验证候选解析成功

```bash
# 监控候选解析
ssh 211 "docker logs -f llm-gateway-211-a 2>&1 | grep 'candidates_resolved' | grep -E 'pt-harbor|gemini-4-hydrogen|minimax-h3'"

# 应该看到 candidates_count > 0
```

### 4. 测试实际请求

```bash
# 使用 API 密钥测试这些模型
curl -X POST https://llm.itestu.cn/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "communitymarcus-mok-ghpt-harbor",
    "messages": [{"role": "user", "content": "test"}]
  }'

# 应该返回 200，而不是 503
```

## 临时绕过方案（Temporary Workaround）

如果无法立即更新密钥，可以考虑：

### 选项 1: 禁用这些提供商

```sql
-- 在 acc-pg17 容器中执行
docker exec -i acc-pg17 psql -U postgres -d llm_gateway <<EOF
-- 禁用失效的凭证（找到对应的 credential_id 后执行）
UPDATE credentials 
SET status = 'disabled', 
    manual_disabled = true,
    manual_disabled_reason = 'API key invalid as of 2026-11-23, pending update'
WHERE id IN (
  SELECT DISTINCT credential_id 
  FROM credential_model_bindings 
  WHERE model_name IN (
    'communitymarcus-mok-ghpt-harbor',
    'communitycatnitigemini-4-hydrogen',
    'minimaxminimax-h3'
  )
);
EOF
```

### 选项 2: 路由到替代提供商

如果这些模型不是关键路径，系统会自动使用其他可用的提供商。检查是否有替代方案：

```bash
# 查看当前可用的替代提供商
ssh 211 "docker logs llm-gateway-211-a 2>&1 | tail -n 500 | grep 'candidates_resolved' | grep -v 'candidates_count\":0'"
```

## 预防措施（Prevention）

### 1. 设置密钥过期监控

建议实施：
- API 密钥定期轮换计划（建议 90 天）
- 密钥失效前 7 天发送提醒
- 为关键模型配置多个备用密钥

### 2. 改进告警

当前系统已经记录了错误，但建议添加：

```yaml
# 添加 Prometheus 告警规则
- alert: ProviderAuthenticationFailure
  expr: rate(llm_gateway_provider_auth_failures_total[5m]) > 0.1
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "Provider {{ $labels.provider }} authentication failing"
    description: "Provider keys may be invalid or expired"

- alert: ZeroCandidatesAvailable
  expr: rate(llm_gateway_zero_candidates_total[5m]) > 0.1
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "No candidates available for model {{ $labels.model }}"
    description: "Check provider keys and configuration"
```

### 3. 定期健康检查

建议每天运行：

```bash
#!/bin/bash
# daily-credential-health-check.sh

echo "=== Credential Health Check $(date) ==="

# 检查最近的认证失败
ssh 211 "docker logs llm-gateway-211-a 2>&1 | tail -n 10000" | \
  grep 'node probe failed.*401' | \
  jq -r '.detail' | \
  grep -oP 'custom/[^"]+' | \
  sort | uniq -c | \
  awk '$1 > 5 {print "⚠️  " $2 " failed " $1 " times in last 10k logs"}'

# 检查零候选情况
ssh 211 "docker logs llm-gateway-211-a 2>&1 | tail -n 5000" | \
  grep 'candidates_resolved.*candidates_count\":0' | \
  wc -l | \
  awk '{if($1>10) print "⚠️  " $1 " zero-candidate resolutions in last 5k logs"}'

echo "=== Check Complete ==="
```

## 时间线（Timeline）

| 时间 | 事件 | 状态 |
|------|------|------|
| 06:26:21 | 首次检测到认证失败 | 🔴 |
| 06:26:21-23 | 3 个请求返回 503 | 🔴 |
| 06:26:23-24 | 检测到配置更新事件 | 🟡 |
| 06:26:24 | 其他模型恢复正常 | 🟢 |
| 当前 | **这 3 个提供商仍未修复** | 🔴 |

## 负责人（Ownership）

- **运维团队**: 更新 API 密钥
- **开发团队**: 实施预防措施（监控告警）
- **产品团队**: 决定哪些提供商是必需的

## 参考资料（References）

- 详细分析报告: `docs/incidents/2026-11-23-503-errors-invalid-provider-keys.md`
- 管理面板: https://llm.itestu.cn/admin/credentials
- 相关日志: `ssh 211 "docker logs llm-gateway-211-a"`

## 更新日志

- 2026-11-23 06:30 - 创建初始行动计划
- 待更新: 密钥修复完成时间
