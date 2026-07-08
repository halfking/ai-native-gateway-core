# 修复租户隔离导致的"无可用路由"错误

**日期**: 2026-07-08  
**优先级**: P0  
**问题**: 部分租户的用户无法访问全局供应商，报"无可用路由"错误  
**根本原因**: SQL WHERE 条件过于严格，只查询当前租户的供应商，未支持跨租户共享

---

## 🎯 问题描述

### 症状
- 部分用户（如某业务租户）使用租户 API Key 访问网关时报错
- 错误信息: `"No available provider for model 'xxx'"`
- `error_kind`: `no_candidate`
- 数据库中供应商配置正常，但路由器找不到候选者

### 架构说明

系统使用**白名单策略**（默认全开）：
1. **供应商配置是全局共享的**
   - 所有供应商配置在 `default` 租户下
   - 所有租户的 API Key 默认可以访问所有供应商的模型
   
2. **租户模型策略是"黑名单"机制**
   - 默认：所有租户可以访问所有模型
   - 配置：通过 `/api/admin/tenants/{code}/model-policies` 禁用特定模型
   - 实现在 `domains/streaming/policy.go` 的 `enforceTenantModelPolicy`

3. **API Key 的 tenant_id**
   - API Key 创建时如果不指定 `tenant_id`，默认为 `"default"`
   - 如果指定了 `tenant_id`（如 `"hansi"`），Key 会关联到该租户

### 根本原因

`provider/client.go` 的 `loadCandidatesDB` 方法中，SQL WHERE 条件为：

```sql
WHERE p.tenant_id = $2  -- 严格匹配当前租户
```

这导致：
- `default` 租户的 API Key: 能访问 `default` 租户的供应商 ✅
- `hansi` 租户的 API Key: **只能**访问 `hansi` 租户的供应商，看不到 `default` 租户的全局供应商 ❌

当用户使用 `hansi` 租户的 Key 时：
```
GetCandidates(model="gpt-4", tenantID="hansi")
→ WHERE p.tenant_id = 'hansi'
→ 供应商都在 'default' 租户下
→ 返回空列表
→ "无可用路由" ❌
```

---

## ✅ 修复方案

### 核心修改

修改 `provider/client.go` 的 `loadCandidatesDB` 方法（第 750 行左右）：

**修改前（错误）：**
```sql
WHERE p.tenant_id = $2  -- 严格匹配，不支持跨租户共享
```

**修改后（正确）：**
```sql
WHERE (p.tenant_id = $2 OR p.tenant_id = 'default')  -- 支持跨租户共享
```

### 行为变化

修改后的路由逻辑：

| 场景 | 修改前 | 修改后 |
|------|--------|--------|
| `default` 租户访问 `default` 供应商 | ✅ | ✅ |
| `hansi` 租户访问 `default` 供应商 | ❌ | ✅ **（修复）** |
| `hansi` 租户访问 `hansi` 私有供应商 | ✅ | ✅ |
| `hansi` 租户访问 `other` 私有供应商 | ❌ | ❌ |

### 设计原则

1. **`default` 租户是全局共享池**
   - 所有配置在 `default` 租户下的供应商，对所有租户可见
   - 适合平台级的公共供应商配置

2. **租户可以有私有供应商**
   - 租户可以配置自己的私有供应商（`tenant_id` 设为租户 ID）
   - 私有供应商只对该租户可见，其他租户看不到

3. **租户模型访问控制通过黑名单**
   - 不是在路由层过滤（WHERE 条件）
   - 而是在策略层拦截（`enforceTenantModelPolicy`）
   - 参见 `docs/2026-06-21-tenant-model-policy.md`

---

## 📋 测试验证

### 单元测试

新增测试文件：`provider/client_cross_tenant_test.go`

测试场景：
1. ✅ `default` 租户访问 `default` 供应商
2. ✅ `hansi` 租户访问 `default` 供应商（跨租户共享）
3. ✅ `hansi` 租户访问 `hansi` 私有供应商
4. ✅ `hansi` 租户不能访问 `other` 私有供应商

运行测试（需要数据库连接）：
```bash
export TEST_DATABASE_URL="postgresql://user:pass@host:5432/dbname"
go test -v ./provider -run TestLoadCandidatesDB_CrossTenantProviderSharing
```

### 集成测试

在测试环境验证：

**测试步骤 1**: 创建不同租户的 API Key
```sql
-- 创建 hansi 租户的 API Key
INSERT INTO api_keys (application_id, tenant_id, key_hash, ...)
VALUES (1, 'hansi', ...);
```

**测试步骤 2**: 验证能访问 default 租户的供应商
```bash
curl -X POST https://llm-test.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer sk-hansi-tenant-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'
```

期望结果：
- **修改前**：返回 503 "No available provider for model 'gpt-4'"
- **修改后**：返回 200，正常调用供应商

---

## 🔧 部署步骤

### 1. 编译新版本

```bash
cd /Users/xutaohuang/workspace/llm-gateway-go-cursor
go build -o llm-gateway ./cmd/gateway/
```

### 2. 部署到 154 服务器

```bash
# 备份当前版本
ssh llm-gateway-154
cd /path/to/llm-gateway
cp llm-gateway llm-gateway.backup-$(date +%Y%m%d-%H%M%S)

# 上传新版本
scp llm-gateway llm-gateway-154:/path/to/llm-gateway/llm-gateway.new

# 滚动重启
systemctl stop llm-gateway
mv llm-gateway.new llm-gateway
systemctl start llm-gateway

# 验证启动
systemctl status llm-gateway
tail -f /var/log/llm-gateway/gateway.log
```

### 3. 验证修复

使用之前失败的 API Key 重新测试：

```bash
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer <tenant-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

期望：返回 200，正常调用

### 4. 监控指标

部署后监控以下指标：

```promql
# 无可用路由错误率（应该下降）
rate(llmgw_request_errors_total{error_kind="no_candidate"}[5m])

# 请求成功率（应该上升）
rate(llmgw_requests_total{status="success"}[5m]) / rate(llmgw_requests_total[5m])

# 按租户的请求成功率
sum by (tenant_id) (rate(llmgw_requests_total{status="success"}[5m]))
```

---

## 🚨 影响分析

### 向下兼容性

✅ **完全向下兼容**

- 对于 `default` 租户的 API Key，行为不变（仍然访问 `default` 供应商）
- 对于其他租户的 API Key，新增了访问 `default` 供应商的能力（增强，不破坏）
- 现有的租户模型策略（黑名单）继续正常工作

### 性能影响

✅ **几乎无影响**

- SQL 查询只是 WHERE 条件多了一个 `OR` 分支
- 数据库索引 `idx_providers_tenant_id` 仍然有效
- 对于 `default` 租户，查询计划与原来相同

### 安全影响

✅ **安全增强**

- 修复前：通过设置特定 `tenant_id` 可以绕过供应商访问（因为看不到任何供应商）
- 修复后：所有租户都能访问 `default` 供应商，黑名单策略统一生效
- 租户隔离仍然有效：租户 A 的私有供应商对租户 B 不可见

---

## 📚 相关文档

- [租户模型策略设计](./2026-06-21-tenant-model-policy.md)
- [多租户架构设计](./multi-tenant-architecture.md)
- [路由器设计文档](./routing-architecture.md)

---

## 🔍 后续优化建议

### 1. 数据库索引优化

当前索引：
```sql
CREATE INDEX idx_providers_tenant_id ON providers(tenant_id);
```

建议添加复合索引：
```sql
CREATE INDEX idx_providers_tenant_id_status ON providers(tenant_id, status) 
WHERE status IS NULL OR status != 'disabled';
```

### 2. 管理后台改进

在管理后台清楚标识：
- 哪些供应商是全局共享的（`tenant_id='default'`）
- 哪些供应商是租户私有的（`tenant_id='hansi'`）

### 3. 文档完善

更新用户文档，说明：
- API Key 的 `tenant_id` 字段含义
- 供应商全局共享的机制
- 如何配置租户模型黑名单

---

## 变更历史

| 日期 | 作者 | 变更 |
|------|------|------|
| 2026-07-08 | Kiro | 初始版本，修复租户隔离导致的"无可用路由"错误 |
| 2026-07-08 | Kiro | 实施方案 2：支持跨租户供应商共享（白名单策略） |
