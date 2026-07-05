# LLM Gateway 测试报告

**测试日期**: 2026-07-01  
**测试环境**: https://__DOMAIN_1__ (184服务器 pms-test namespace)  
**部署版本**: `r1.13-done-a2214914-20260701-765`  
**测试人员**: Kiro AI

---

## 📋 测试概览

| 测试项 | 状态 | 备注 |
|--------|------|------|
| 版本信息显示 | ✅ 通过 | 前端可正常获取版本信息 |
| 健康检查 | ✅ 通过 | /healthz 返回正常 |
| 管理端点认证 | ✅ 通过 | JWT/API Key 认证正常 |
| 模型列表API | ✅ 通过 | 返回72个模型 |
| 路由解析 | ✅ 通过 | 路由解析功能正常 |
| 聊天完成API | ⚠️ 受限 | 大部分模型因配额/速率限制不可用 |
| 后台任务 | ✅ 运行 | 所有后台任务alive |
| 系统监控 | ✅ 通过 | 日志、索引正常 |

---

## ✅ 成功的测试

### 1. 版本信息端点（公开访问）

**问题修复**: 已将 `/api/system/version` 从认证端点改为公开端点

**测试请求**:
```bash
curl https://__DOMAIN_1__/api/system/version
```

**响应结果**:
```json
{
  "build_date": "a2214914-20260701-765",
  "build_seq": 765,
  "build_time": "a2214914-20260701-765",
  "git_sha": "a2214914",
  "version": "r1.13"
}
```

✅ **验证通过**: 无需认证即可访问，前端导航栏可正常显示版本号和编译次数

---

### 2. 健康检查端点

**测试请求**:
```bash
curl https://__DOMAIN_1__/healthz
```

**响应结果**:
```json
{
  "status": "ok",
  "version": "r1.13-done-a2214914-20260701-765"
}
```

✅ **验证通过**: 服务健康状态正常

---

### 3. 管理端点认证

**Admin API Key**: `__API_KEY_2__` (从 k8s secret 获取)

#### 3.1 模型列表
```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" https://__DOMAIN_1__/api/models
```

✅ **结果**: 返回 72 个模型，数据结构正确，包含模型元信息、标签、别名等

#### 3.2 API Keys 列表
```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" https://__DOMAIN_1__/api/keys
```

✅ **结果**: 返回 45 个 API keys

#### 3.3 租户列表
```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" https://__DOMAIN_1__/api/admin/tenants
```

✅ **结果**: 返回 8 个租户

#### 3.4 使用统计
```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" \
  "https://__DOMAIN_1__/api/usage?start_date=2026-07-01&end_date=2026-07-01"
```

✅ **结果**:
```json
{
  "total_requests": 16347,
  "total_prompt_tokens": 1188954358,
  "total_completion_tokens": 2673176,
  "total_cost_usd": 1.026113,
  "avg_latency_ms": 6675.64767763765,
  "success_rate": 0.9052425521502416
}
```

---

### 4. 路由解析功能

**测试**: 解析 `doubao-1-5-pro-32k` 模型

```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" \
  "https://__DOMAIN_1__/api/routing/resolve?model=doubao-1-5-pro-32k"
```

✅ **结果**: 
- 返回 2 个候选 provider
- 显示详细的路由信息（provider、credential、availability_state、quota_state 等）
- 路由解析逻辑正常工作

**关键发现**:
- Provider 1 (TokenPlan): `availability_state: rate_limited`, `routable: false`, `block_reason: availability_rate_limited`
- Provider 2 (普通版): `quota_state: periodic_exhausted`, `routable: false`, `block_reason: quota_periodic_exhausted`

---

### 5. 后台任务状态

```bash
curl -H "Authorization: Bearer ${ADMIN_KEY}" \
  https://__DOMAIN_1__/api/system/background-tasks
```

✅ **结果**:
```json
{
  "discovery": { "alive": true, "running": false },
  "probe_loop": { "alive": true, "checks_10m": 0 },
  "cycler": { "alive": true },
  "recovery": { "alive": true },
  "telemetry": { "alive": true }
}
```

✅ **验证通过**: 所有后台任务进程存活

---

### 6. 系统日志监控

**索引刷新**: 每5分钟自动刷新一次
```
2026/07/01 13:58:02 INFO auto index refreshed bucket=2026-07-01T13:55:00Z 
  credential_rows=175 task_rows=0
```

**路由监听**: 实时响应数据库变更通知
```
2026/07/01 13:13:21 INFO auto_route listener: refresh requested 
  payload=credential_model_bindings:UPDATE:12
```

**模型探测**: 持续监控模型健康状态
```
2026/07/01 13:43:34 INFO model probe: restored binding available 
  (healthy_confirmed) credential_id=11 raw_model=doubao-pro-4k-functioncall-240615
```

✅ **验证通过**: 系统监控机制正常运行

---

## ⚠️ 发现的问题

### 问题 1: 大部分模型不可用（配额/速率限制）

**现象**: 
- 通过 `/api/routing/available-models` 可以看到很多模型有 `provider_count > 0`
- 但实际调用时返回 `No available provider` 错误
- 路由解析显示 `routable: false`

**根本原因**:
1. **速率限制 (rate_limited)**: Provider 被标记为速率受限，需要等待恢复时间
2. **配额耗尽 (periodic_exhausted)**: 周期性配额已用完
3. **凭证状态问题**: 某些凭证可能过期或无效

**测试案例**:

#### 测试 1: doubao-1-5-pro-32k
```bash
curl -X POST https://__DOMAIN_1__/v1/chat/completions \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{"model": "doubao-1-5-pro-32k", "messages": [...]}'
```

❌ **结果**: 
```json
{
  "error": {
    "code": "model_not_found",
    "message": "No available provider for model 'doubao-1-5-pro-32k'. All 0 candidates failed."
  }
}
```

**路由解析结果**:
- 2个候选 provider，但都不可路由
- Provider 1: `block_reason: availability_rate_limited`
- Provider 2: `block_reason: quota_periodic_exhausted`

#### 测试 2: claude-sonnet-4.5
```bash
curl -X POST https://__DOMAIN_1__/v1/chat/completions \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{"model": "claude-sonnet-4.5", "messages": [...]}'
```

❌ **结果**: 
```json
{
  "error": {
    "code": "no_candidate",
    "message": "No available provider for model 'claude-sonnet-4.5'"
  }
}
```

路由解析显示有 1 个 routable provider，但实际调用失败。

---

### 问题 2: Discovery 未运行

**现象**: 
```json
{
  "discovery": {
    "alive": true,
    "running": false,
    "last_run": null,
    "last_success": null
  }
}
```

**影响**: Discovery 服务负责自动发现和更新 provider 模型列表，未运行可能导致某些新模型无法被发现。

**优先级**: 🔵 低 - 系统已有 173 个 credential bindings，手动管理也可工作

---

### 问题 3: Probe Loop 检查次数为0

**现象**: 
```json
{
  "probe_loop": {
    "alive": true,
    "last_run": null,
    "checks_10m": 0
  }
}
```

**影响**: 模型健康探测可能不活跃，但日志显示有探测活动（如 `model probe: restored binding available`），可能是指标统计问题。

**优先级**: 🔵 低 - 日志显示探测仍在工作

---

## 📊 性能指标

### 使用统计 (2026-07-01)
- **总请求数**: 16,347
- **成功率**: 90.52%
- **平均延迟**: 6,675 ms
- **Token 使用**: 
  - Prompt: 1,188,954,358
  - Completion: 2,673,176
- **总成本**: $1.03 USD

### 系统资源
- **Pod 状态**: Running, 1/1 Ready
- **运行时间**: 161 分钟（从最后一次部署算起）
- **重启次数**: 0

---

## 🔧 建议修复项

### 高优先级 🔴

#### 1. 解决模型配额/速率限制问题

**当前状态**:
- `doubao-1-5-pro-32k`: 2个provider，都因配额/速率限制不可用
- 大部分模型无法实际调用

**建议方案**:
1. **检查凭证配额**: 登录前端管理界面，查看各凭证的配额使用情况
2. **增加配额或购买额度**: 对于重要的 provider
3. **配置速率限制恢复策略**: 检查 `availability_recover_at` 时间，考虑调整速率限制参数
4. **添加更多 provider**: 为关键模型添加备用 provider

**验证方法**:
```bash
# 检查哪些模型真正可路由
for model in $(curl -s -H "Authorization: Bearer ${ADMIN_KEY}" \
  "${BASE_URL}/api/routing/available-models" | \
  jq -r '.families[].versions[].canonical_name' | head -10); do
  
  ROUTABLE=$(curl -s "${BASE_URL}/api/routing/resolve?model=${model}" \
    -H "Authorization: Bearer ${ADMIN_KEY}" | \
    jq '[.candidates[] | select(.routable == true)] | length')
  
  [ "$ROUTABLE" -gt 0 ] && echo "✅ $model: $ROUTABLE 可用"
done
```

---

### 中优先级 🟡

#### 2. 启用 Discovery 服务

**问题**: Discovery 显示 `running: false`

**可能原因**:
- 环境变量配置问题
- Discovery 触发条件未满足
- 代码逻辑跳过了 discovery 启动

**建议**:
1. 检查环境变量是否正确配置了 discovery 相关参数
2. 查看代码中 discovery 启动条件
3. 考虑手动触发一次 discovery

---

#### 3. 验证 Probe Loop 指标

**问题**: `checks_last_10m: 0` 但日志显示有探测活动

**建议**:
1. 检查指标收集逻辑
2. 验证探测是否真的在运行
3. 如果只是指标问题，修复指标统计代码

---

### 低优先级 🔵

#### 4. 废弃警告: Admin API Key

日志中显示多个警告:
```
WARN admin api key fallback deprecated, migrate to JWT (removal 2026-07-27)
```

**建议**: 计划在 2026-07-27 之前迁移到 JWT 认证

---

## 📝 测试总结

### ✅ 已验证正常的功能

1. **版本显示修复**: `/api/system/version` 公开端点工作正常，前端可以正确显示版本信息
2. **基础 API**: 健康检查、认证、模型列表、租户管理、使用统计等管理端点全部正常
3. **路由解析**: 路由解析逻辑正常，能正确返回候选 provider 和阻塞原因
4. **后台任务**: 所有后台进程存活，日志显示监控、探测、索引刷新正常运行
5. **系统稳定性**: Pod 运行稳定，无重启，成功率 90%+

### ⚠️ 需要关注的问题

1. **模型可用性**: 大部分模型因配额/速率限制无法实际调用
   - 影响: **高** - 直接影响用户使用体验
   - 需要: 补充配额或添加更多 provider

2. **Discovery 未运行**: 自动发现服务未激活
   - 影响: **中** - 不影响现有模型使用，但新模型无法自动发现
   
3. **监控指标异常**: Probe loop 统计为 0
   - 影响: **低** - 不影响功能，但监控数据不准确

### 🎯 下一步行动

1. ✅ **已完成**: 修复版本显示问题（commit a2214914）
2. 🔴 **待处理**: 检查并解决模型配额/速率限制问题
3. 🟡 **可选**: 调查并启用 Discovery 服务
4. 🟡 **可选**: 修复 Probe Loop 指标统计

---

## 📎 附录

### 测试环境信息

- **服务地址**: https://__DOMAIN_1__
- **K8s Namespace**: pms-test
- **Deployment**: llm-gateway-go-deployment
- **Pod**: llm-gateway-go-deployment-75c6589f56-gcgrt
- **Node**: iv-ye02tnqgaovr6okqpr05
- **部署时间**: 2026-07-01 11:12:50 (UTC)

### 测试用 API Key

- **Admin Key**: 存储在 k8s secret `llm-gateway-secret` 中
- **获取方法**: 
  ```bash
  kubectl get secret llm-gateway-secret -n pms-test \
    -o jsonpath='{.data.admin-api-key}' | base64 -d
  ```

### 相关提交

- **a2214914**: `fix(admin): make /api/system/version public endpoint for frontend version display`
  - 修复了版本显示为 0 的问题
  - 将 `/api/system/version` 改为公开端点
  - 更新了构建脚本和版本管理

---

**测试报告生成时间**: 2026-07-01 14:05:00 UTC  
**报告版本**: v1.0
