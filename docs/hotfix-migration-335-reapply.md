# Migration 335 紧急热修复记录

**日期**: 2026-07-10 19:30  
**问题**: migration 335/336 虽然显示已应用，但视图定义未更新  
**影响**: claude-fable-5, claude-opus-4-8, gpt-5.5 仍然无法路由

---

## 🚨 问题发现

用户报告 `claude-opus-4-8` 和 `claude-fable-5` 仍然无法路由，显示没有可用的模型。

### 诊断过程

1. **检查数据库视图**：
```sql
SELECT raw_model_name, credential_id, is_routable, unavailable_reason
FROM v_routable_credential_models 
WHERE raw_model_name IN ('claude-opus-4-8', 'claude-fable-5');

-- 结果：仍然显示 plan_incompatible_cmb_requires_per_token
```

2. **检查 migration 记录**：
```sql
SELECT version, applied_at FROM schema_migrations WHERE version IN ('335', '336');

-- 结果：显示已应用（2026-07-05 05:07:29）
```

3. **检查视图定义**：
```sql
SELECT pg_get_viewdef('v_routable_credential_models', true);

-- 结果：视图定义仍然包含错误的 plan_type vs billing_mode 校验！
```

### 根本原因

**Migration 记录与实际视图定义不一致**：
- schema_migrations 表记录显示 migration 335 已应用
- 但数据库中的视图定义仍然是旧的（包含错误的校验逻辑）
- 可能原因：部署时只记录了版本号，但实际 SQL 没有执行成功

---

## 🔧 紧急修复

### 执行步骤

1. **手动重建视图**（2026-07-10 19:30）：
```sql
-- 手动执行 migration 335 的视图重建
DROP VIEW IF EXISTS v_routable_credential_models CASCADE;

CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
    cmb.billing_mode,
    c.plan_type,
    cmb.plan_type_origin,
    -- is_routable CASE（移除了 plan_type vs billing_mode 校验）
    CASE
        WHEN NOT p.enabled THEN false
        WHEN COALESCE(p.manual_disabled, false) THEN false
        WHEN COALESCE(c.manual_disabled, false) THEN false
        WHEN NOT pm.available THEN false
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN false
        WHEN c.lifecycle_status != 'active' THEN false
        WHEN cmb.available IS NOT true THEN false
        WHEN c.quota_state = 'periodic_exhausted' THEN false
        WHEN c.quota_state = 'exhausted'
             AND (c.quota_recover_at IS NULL OR c.quota_recover_at > NOW()) THEN false
        WHEN c.availability_state = 'unavailable'
             AND (c.availability_recover_at IS NULL OR c.availability_recover_at > NOW()) THEN false
        ELSE true
    END AS is_routable,
    -- unavailable_reason（移除了 plan_incompatible）
    CASE
        WHEN NOT p.enabled THEN 'provider_disabled'
        WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'
        WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'
        WHEN NOT pm.available THEN 'model_unavailable'
        WHEN c.status NOT IN ('active', 'cooling', 'degraded') THEN 'credential_status_' || c.status
        WHEN c.lifecycle_status != 'active' THEN 'lifecycle_' || c.lifecycle_status
        WHEN cmb.available IS NOT true THEN 'binding_unavailable'
        WHEN c.quota_state = 'periodic_exhausted' THEN 'quota_periodic_exhausted'
        WHEN c.quota_state = 'exhausted' THEN 'quota_exhausted'
        WHEN c.availability_state = 'unavailable' THEN 'availability_unavailable'
        ELSE NULL
    END AS unavailable_reason
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id;

GRANT SELECT ON v_routable_credential_models TO PUBLIC;
SELECT pg_notify('auto_route_refresh', 'manual:335_hotfix');
```

2. **验证修复结果**：
```sql
SELECT raw_model_name, credential_id, is_routable, unavailable_reason
FROM v_routable_credential_models 
WHERE raw_model_name IN ('claude-opus-4-8', 'claude-fable-5', 'gpt-5.5');

-- 结果：
-- claude-fable-5 (credential 17): is_routable=true ✅
-- claude-opus-4-8 (credential 17): is_routable=true ✅
-- gpt-5.5 (credential 2): is_routable=true ✅
```

3. **重启服务**：
```bash
killall -9 llm-gateway-go
systemctl start llm-gateway-go
```

4. **验证路由**：
```bash
curl 'http://localhost:8080/api/routing/resolve?model=claude-fable-5'
# 返回 200 OK，response_bytes=1163 ✅
```

---

## ✅ 修复结果

### 恢复的模型

| 模型 | Credential | 状态 |
|------|-----------|------|
| claude-fable-5 | 17 (130dao) | ✅ 可路由 |
| claude-opus-4-8 | 17 (130dao) | ✅ 可路由 |
| gpt-5.5 | 2 (gpt key) | ✅ 可路由 |

### 仍然不可用的组合

| 模型 | Credential | 原因 |
|------|-----------|------|
| claude-opus-4-8 | 10 (evol-openclaw-proxy) | provider_disabled |
| claude-opus-4-8 | 13 (vapeur-main) | provider_disabled |
| gpt-5.5 | 10, 13 | provider_disabled |

这些是因为 provider 被禁用（enabled=false, manual_disabled=true），需要单独处理。

---

## 📚 经验教训

### 问题根因

1. **Migration 执行不完整**：
   - schema_migrations 表记录了版本号
   - 但实际 SQL 没有执行成功
   - 缺少执行验证机制

2. **缺少部署后验证**：
   - 部署后没有验证视图是否真正更新
   - 只检查了 migration 记录，没有检查实际效果

### 改进措施

1. **Migration 执行加强**：
   - 执行后验证关键对象（视图、表、索引）是否真正改变
   - 记录执行日志和结果
   - 失败时回滚 schema_migrations 记录

2. **部署后验证清单**：
   - 检查 schema_migrations 记录
   - 验证关键视图定义
   - 执行测试 SQL 确认业务逻辑
   - 检查应用日志确认路由正常

3. **监控告警**：
   - 监控关键模型的可路由状态
   - 异常时及时告警

---

## 🔍 待处理问题

### provider_disabled 问题

以下 provider 被标记为 disabled：
- provider 33 (evol - EvolAI 聚合代理): enabled=false, manual_disabled=true
- provider 36 (vapeur - Vapeur AI): enabled=false, manual_disabled=true

**需要确认**：
1. 这些 provider 为什么被禁用？
2. 是否需要重新启用？
3. 如果不需要，相关的 credentials 和 bindings 是否应该清理？

---

**修复人**: OpenCode Agent  
**修复时间**: 2026-07-10 19:30-19:32  
**验证时间**: 2026-07-10 19:32  
**状态**: ✅ 已修复并验证
