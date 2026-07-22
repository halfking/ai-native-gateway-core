# 故障记录：claude-opus-4-8 无可用节点

## 故障概述

- **故障时间**: 2026-07-12
- **故障模型**: claude-opus-4-8
- **故障现象**: 请求时返回 "No available provider for model 'claude-opus-4-8'"，但探测可行
- **影响范围**: 所有使用 claude-opus-4-8 模型的请求

## 故障原因

### 根本原因

`models_canonical` 表中缺少 `claude-opus-4-8` 模型记录，导致：

1. `model_aliases` 表中的 `canonical_id=354` 指向不存在的记录
2. 路由时无法找到可用的候选节点
3. 探测可能使用不同的路径，所以可以成功

### 详细分析

1. **model_aliases 表**:
   - 存在 `claude-opus-4.8` 别名记录 (id=246, canonical_id=354)
   - 但 `canonical_id=354` 在 `models_canonical` 表中不存在

2. **models_canonical 表**:
   - 缺少 `claude-opus-4-8` 模型记录
   - 只有 `claude-3-opus` 和 `claude-3-sonnet`

3. **provider_models 表**:
   - 没有 `claude-opus-4-8` 的模型记录
   - 只有 `claude-3-opus` 和 `claude-3-sonnet`

4. **凭证配置**:
   - Anthropic provider (id=2) 存在且启用
   - 但没有对应的凭证记录
   - 也没有 provider_models 记录

## 修复方案

### 1. 添加 models_canonical 记录

```sql
INSERT INTO models_canonical (
    canonical_name, family, source, status, notes, display_name, context_window
)
VALUES (
    'claude-opus-4-8', 'anthropic-claude', 'seed', 'active',
    'Anthropic Claude 4.8 Opus', 'Claude Opus 4.8', 200000
)
ON CONFLICT (canonical_name) DO UPDATE SET
    family = EXCLUDED.family,
    notes = EXCLUDED.notes,
    updated_at = NOW();
```

### 2. 更新 model_aliases 的 canonical_id

```sql
UPDATE model_aliases
SET canonical_id = (SELECT id FROM models_canonical WHERE canonical_name = 'claude-opus-4-8'),
    updated_at = NOW()
WHERE raw_name IN ('claude-opus-4.8', 'claude-opus-4-8');
```

### 3. 添加 provider_models 记录

```sql
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id,
    standardized_name, outbound_model_name, available
)
VALUES (
    2, 'default', 'claude-opus-4-8',
    (SELECT id FROM models_canonical WHERE canonical_name = 'claude-opus-4-8'),
    'claude-opus-4-8', 'claude-opus-4-8', true
)
ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
    canonical_id = EXCLUDED.canonical_id,
    available = EXCLUDED.available,
    updated_at = NOW();
```

### 4. 添加凭证（需要手动操作）

生产环境需要手动添加 Anthropic 的 API Key 凭证。

## 预防措施

### 1. 种子数据完整性检查

定期检查 `deploy/sql/001_vendor_family_mappings.sql` 中的种子数据是否与数据库同步。

### 2. 模型注册流程

新增模型时，确保：
- `models_canonical` 表中有记录
- `model_aliases` 表中有正确的 `canonical_id`
- `provider_models` 表中有模型记录
- `credentials` 表中有凭证
- `credential_model_bindings` 表中有绑定

### 3. 自动化测试

添加自动化测试，验证：
- 新模型可以路由
- 探测和实际请求都成功
- 错误信息清晰

## 相关文件

- 修复脚本: `sql/fix-claude-opus-4-8.sql`
- 种子数据: `deploy/sql/001_vendor_family_mappings.sql`
- 测试脚本: `scripts/test-claude-opus-4-8-routing.sh`

## 验证结果

修复后验证：
- ✅ `models_canonical` 表中有 `claude-opus-4-8` 记录
- ✅ `model_aliases` 表中的 `canonical_id` 已更新
- ✅ `provider_models` 表中有 `claude-opus-4-8` 记录
- ⚠️ 需要添加凭证后才能完全验证路由
