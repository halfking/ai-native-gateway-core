# LLM Gateway 计费模式标准化方案

## 问题背景

当前系统中存在两个计费相关字段：
- `credentials.plan_type` - 凭据级别的套餐类型
- `credential_model_bindings.billing_mode` - 模型绑定级别的计费模式

两者之间存在不一致，导致路由失败和模型无法调用。

## 当前状态分析

### credentials.plan_type 分布
| plan_type | 数量 | 说明 |
|-----------|------|------|
| token     | 17   | 按量付费（统一类型） |

### credential_model_bindings.billing_mode 分布
| billing_mode | 数量 | 说明 |
|--------------|------|------|
| per_token    | 776  | 按 token 计费 |
| token        | 259  | 按量（token 的别名） |
| monthly      | 1    | 月付费 |

### 不一致情况
- ✅ token → per_token: 776 条（正常）
- ✅ token → token: 259 条（正常）
- ❌ token → monthly: 1 条（不一致）

## 标准化方案

### 1. 统一计费模式定义

#### 1.1 成本价（内部）计费模式
**单一真相源（SSOT）：`credentials.plan_type`**

标准枚举值：
```sql
-- 凭据级套餐类型（成本价）
plan_type IN (
    'token',        -- 按量付费（标准）
    'token_plan',   -- 令牌包套餐
    'code_plan',    -- 代码包套餐
    'agent_plan',   -- Agent 包套餐
    'monthly',      -- 月付费
    'free'          -- 免费池
)
```

#### 1.2 对外计价模式
**派生字段：`credential_model_bindings.billing_mode`**

从 `credentials.plan_type` 派生规则：
```
credentials.plan_type → credential_model_bindings.billing_mode
--------------------------------------------------------------
token         → per_token   (标准化名称)
token_plan    → token_plan  (直通)
code_plan     → code_plan   (直通)
agent_plan    → agent_plan  (直通)
monthly       → monthly     (直通)
free          → free        (直通)
```

### 2. 数据修正策略

#### 2.1 立即修正（高优先级）
```sql
-- 修正 token → monthly 的不一致
UPDATE credential_model_bindings cmb
SET billing_mode = 'per_token',
    plan_type_origin = 'standardization',
    updated_at = NOW()
FROM credentials c
WHERE c.id = cmb.credential_id
  AND c.plan_type = 'token'
  AND cmb.billing_mode = 'monthly';
```

#### 2.2 统一别名（中优先级）
```sql
-- 将 'token' 别名统一为标准名称 'per_token'
UPDATE credential_model_bindings
SET billing_mode = 'per_token',
    plan_type_origin = 'standardization',
    updated_at = NOW()
WHERE billing_mode = 'token';
```

### 3. 代码层修正

#### 3.1 模型发现（discovery）
**文件：** `modelcatalog/upsert.go`

当前逻辑已正确：
```go
func DeriveBillingMode(planType string) string {
    if planType == "" || planType == "token" {
        return "per_token"
    }
    return planType
}
```

#### 3.2 路由过滤（routing）
**文件：** `provider/client.go`

确保路由查询使用统一逻辑：
```sql
-- 套餐兼容性检查
WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
     AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') 
     THEN false  -- 不兼容
```

#### 3.3 视图定义
**文件：** `migrations/327_credential_plan_type_full.sql`

已在迁移 327 中正确定义：
```sql
CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT
    cmb.billing_mode,  -- 派生自 credentials.plan_type
    c.plan_type,       -- SSOT
    -- 兼容性检查逻辑
    CASE 
        WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
             AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan')
             THEN false
        ELSE true
    END AS is_routable
    ...
```

### 4. 执行步骤

#### 步骤 1: 数据修正（立即执行）
```sql
-- 1.1 修正 monthly 不一致
UPDATE credential_model_bindings cmb
SET billing_mode = 'per_token',
    plan_type_origin = 'standardization',
    updated_at = NOW()
FROM credentials c
WHERE c.id = cmb.credential_id
  AND c.plan_type = 'token'
  AND cmb.billing_mode = 'monthly';

-- 1.2 统一 'token' 为 'per_token'
UPDATE credential_model_bindings
SET billing_mode = 'per_token',
    plan_type_origin = 'standardization',
    updated_at = NOW()
WHERE billing_mode = 'token';

-- 1.3 验证结果
SELECT 
    c.plan_type,
    cmb.billing_mode,
    COUNT(*) as count
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
GROUP BY c.plan_type, cmb.billing_mode
ORDER BY c.plan_type, cmb.billing_mode;
```

#### 步骤 2: 触发缓存刷新
数据库触发器会自动发送 NOTIFY 刷新路由缓存：
```
trg_notify_auto_route_cmb → NOTIFY → candidate cache invalidated
```

#### 步骤 3: 验证路由
```bash
# 测试请求
curl -X POST http://localhost:__PORT_3__/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v3-241226",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 20
  }'
```

### 5. 预期结果

修正后的状态：
```
plan_type | billing_mode | count | status
----------|--------------|-------|--------
token     | per_token    | 1036  | OK
```

### 6. 监控指标

#### 6.1 成功指标
- ✅ 所有 `plan_type='token'` 的凭据的 `billing_mode='per_token'`
- ✅ 模型发现成功率 = 100%（无 "failed to upsert model" 错误）
- ✅ 请求路由成功率 > 95%（无 "all candidates failed" 错误）

#### 6.2 监控查询
```sql
-- 监控不一致数据
SELECT COUNT(*) as mismatch_count
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token')
   OR (c.plan_type IN ('token_plan', 'code_plan', 'agent_plan') 
       AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan'));

-- 告警阈值：mismatch_count > 0
```

### 7. 回滚方案

如果出现问题，可以回滚：
```sql
-- 恢复到修改前的状态（基于 plan_type_origin 标记）
UPDATE credential_model_bindings
SET billing_mode = (
    SELECT CASE 
        WHEN plan_type_origin = 'standardization' THEN 'token'
        ELSE billing_mode
    END
),
    plan_type_origin = 'auto'
WHERE plan_type_origin = 'standardization';
```

### 8. 长期架构建议

#### 8.1 废弃 billing_mode 列
**目标：** 使 `credentials.plan_type` 成为唯一真相源

实施步骤：
1. 在应用层完全基于 `credentials.plan_type` 进行路由判断
2. 将 `billing_mode` 标记为 deprecated
3. 迁移所有查询使用 `plan_type`
4. 6 个月后删除 `billing_mode` 列

#### 8.2 计费规则配置化
将硬编码的兼容性规则移到配置表：
```sql
CREATE TABLE billing_compatibility_rules (
    credential_plan_type TEXT NOT NULL,
    model_billing_requirement TEXT NOT NULL,
    is_compatible BOOLEAN NOT NULL,
    PRIMARY KEY (credential_plan_type, model_billing_requirement)
);
```

## 附录

### A. 相关文件清单
- `modelcatalog/upsert.go` - 模型发现时的 billing_mode 派生
- `provider/client.go` - 路由候选查询
- `migrations/327_credential_plan_type_full.sql` - plan_type 迁移
- `domains/streaming/executors/router.go` - 路由规划逻辑

### B. 相关数据库表
- `credentials` - 凭据表（包含 plan_type）
- `credential_model_bindings` - 模型绑定表（包含 billing_mode）
- `v_routable_credential_models` - 可路由模型视图

### C. 相关日志关键字
- `failed to upsert model` - 模型发现失败
- `all candidates failed` - 路由失败
- `UnsupportedModel` - 套餐不兼容
- `plan_incompatible` - 计费模式不匹配

---

**文档版本：** 1.0  
**创建日期：** 2026-07-03  
**作者：** LLM Gateway 运维团队  
**状态：** 已批准，待执行
