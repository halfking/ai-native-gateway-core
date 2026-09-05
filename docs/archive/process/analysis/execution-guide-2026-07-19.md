# 火山引擎 GLM-5.2 配置与验证执行指南

**日期**: 2026-07-19
**目的**: 添加火山引擎 GLM-5.2 配置并验证智谱AI/商汤的错误处理问题

---

## 📋 前置准备

### 1. 设置数据库连接

选择你要连接的环境：

```bash
# 方式1: 从 .env 文件加载
source .env

# 方式2: 直接设置环境变量（请替换为实际值）
export DATABASE_URL='postgresql://user:password@host:port/llm_gateway?sslmode=disable'

# 方式3: 使用 Makefile 中定义的环境（如果有）
# 查看可用的数据库连接
grep -E "DB_|DATABASE" Makefile

# 验证连接
psql $DATABASE_URL -c "SELECT current_database(), current_user;"
```

---

## 🚀 执行步骤

### Step 1: 添加火山引擎 GLM-5.2 配置

**执行脚本**: `sql/migrations/manual/20260719_add_volcano_glm52.sql`

```bash
# 执行配置（会输出详细的执行过程）
psql $DATABASE_URL -f sql/migrations/manual/20260719_add_volcano_glm52.sql

# 或者使用 -q 静默模式
psql $DATABASE_URL -q -f sql/migrations/manual/20260719_add_volcano_glm52.sql
```

**预期输出**:
```
=== 检查火山引擎提供商配置 ===
 id | provider_code | provider_name | ...
----+---------------+---------------+-----
 XX | volcano       | 火山引擎       | ...

=== 添加 glm-5.2 模型（如不存在） ===
INSERT 0 2  ← 成功插入2行（glm-5.2 和 GLM-5.2）

=== 为火山引擎凭证绑定 glm-5.2 模型 ===
INSERT 0 4  ← 成功绑定（假设2个凭证 × 2个版本 = 4行）

=== 验证配置结果 ===
 credential_id | provider_code | raw_model_name | available
---------------+---------------+----------------+-----------
            XX | volcano       | glm-5.2        | t
            XX | volcano       | GLM-5.2        | t
            YY | volcano       | glm-5.2        | t
            YY | volcano       | GLM-5.2        | t

=== 检查路由视图中的可路由状态 ===
 credential_id | raw_model_name | is_routable
---------------+----------------+-------------
            XX | glm-5.2        | t
            XX | GLM-5.2        | t
```

**如果出错**:
- `ERROR: duplicate key value` → 说明已存在，忽略即可（脚本是幂等的）
- `ERROR: relation "providers" does not exist` → 数据库连接错误或schema不对

---

### Step 2: 执行验证脚本

**执行脚本**: `sql/migrations/manual/20260719_verify_config.sql`

```bash
# 执行验证并保存结果
psql $DATABASE_URL -f sql/migrations/manual/20260719_verify_config.sql > /tmp/verify_result.txt 2>&1

# 查看结果
cat /tmp/verify_result.txt

# 或者分段查看
psql $DATABASE_URL -f sql/migrations/manual/20260719_verify_config.sql | less
```

---

## 🔍 结果分析指南

### 验证1: 智谱AI最近的错误响应

**查看输出中的这部分**:
```
========================================
验证1: 智谱AI最近的错误响应
========================================

 request_id | credential_id | error_kind | upstream_status_code | upstream_response_preview
------------+---------------+------------+----------------------+---------------------------
```

**关键问题**:
1. ❓ `error_kind` 列显示的是什么？
   - 如果是 `rate_limit` → ✅ 错误分类正确
   - 如果是 `model_not_found` → ❌ 错误分类有问题

2. ❓ `upstream_status_code` 列显示的是什么？
   - 如果是 `429` → ✅ 标准限流响应
   - 如果是 `400/404` → ⚠️ 可能是模型配置问题

3. ❓ `upstream_response_preview` 包含什么内容？
   - 复制完整的错误信息，我们需要分析

---

### 验证2: 智谱AI的模型配置

**查看输出**:
```
========================================
验证2: 智谱AI的模型配置
========================================

 provider_code | raw_model_name | canonical_name | binding_count | available_count | unavailable_count
---------------+----------------+----------------+---------------+-----------------+-------------------
 zhipuai       | glm-4          | glm-4          |             3 |               3 |                 0
 zhipuai       | glm-5.2        | glm-5.2        |             5 |               2 |                 3
```

**关键问题**:
1. ❓ 是否有 `glm-5.2` 这一行？
   - 如果没有 → ⚠️ 智谱AI没有配置 glm-5.2
   - 如果有 → 继续检查

2. ❓ `available_count` 是多少？
   - 如果是 0 → ❌ 所有凭证都被标记为不可用
   - 如果 > 0 → ✅ 至少有部分凭证可用

3. ❓ `unavailable_count` 是多少？
   - 如果很高 → ⚠️ 大部分凭证不可用，需要查原因

---

### 验证6: 智谱AI错误统计（最重要）

**查看输出**:
```
========================================
验证6: 智谱AI超限相关的错误（如果有）
========================================

 error_kind      | upstream_status_code | error_count | last_seen           | sample_responses
-----------------+----------------------+-------------+---------------------+------------------
 rate_limit      |                  429 |         150 | 2026-07-19 10:30:00 | {"error": "超过限额"}
 model_not_found |                  400 |          50 | 2026-07-19 10:25:00 | {"error": "model not found"}
```

**这是最关键的验证！**

#### 场景A: rate_limit + 429（标准限流）
```
 rate_limit      |                  429 |         150 | ... | {"error": "超过限额"}
```
**结论**: ✅ 错误分类正确，问题在降级模式
**下一步**: 查看验证7，确认降级模式是否被触发

#### 场景B: model_not_found + 429（错误分类）
```
 model_not_found |                  429 |         150 | ... | {"error": "超过限额"}
```
**结论**: ❌ 错误分类有问题（429被误判为model_not_found）
**下一步**: 需要修改错误分类逻辑

#### 场景C: model_not_found + 400/404（模型配置问题）
```
 model_not_found |                  404 |         150 | ... | {"error": "model not found"}
```
**结论**: ⚠️ 可能是模型名称配置错误
**下一步**: 检查智谱AI使用的模型名是否正确

---

### 验证7: 降级模式触发情况（证据链）

**查看输出**:
```
========================================
验证7: 降级模式触发情况
========================================

 credential_id | raw_model_name | unavailable_reason    | requests_after_unavailable | error_kinds
---------------+----------------+-----------------------+----------------------------+-------------
            10 | glm-5.2        | continuous_failure    |                        120 | rate_limit
            15 | glm-4          | availability:cooling  |                         80 | rate_limit
```

**关键问题**:
1. ❓ 这个查询是否有输出结果？
   - **如果有** → 🔴 **降级模式确实在强制使用不可用的凭证！**
   - 如果没有 → ✅ 降级模式没有问题

2. ❓ `requests_after_unavailable` 的数值有多大？
   - 数值越大 → 问题越严重
   - 如果是100+，说明被降级模式使用了很多次

3. ❓ `unavailable_reason` 是什么？
   - `continuous_failure` → 持续失败
   - `availability:cooling` → 冷却期
   - `availability:rate_limited` → 限流状态

---

## 📊 问题诊断矩阵

根据验证6和验证7的结果组合，判断问题根因：

| 验证6: error_kind + status | 验证7: 有数据 | 问题诊断 | 修复方案 |
|---------------------------|-------------|---------|---------|
| `rate_limit` + `429` | ✅ 有 | **降级模式问题** | 修改 `isTransientUnavailableReason` |
| `rate_limit` + `429` | ❌ 无 | **自动恢复问题** | 添加恢复前探测 |
| `model_not_found` + `429` | ✅ 有 | **错误分类问题** | 修改 `ClassifyError` 优先级 |
| `model_not_found` + `400/404` | ❌ 无 | **模型配置问题** | 检查模型名称 |

---

## 📝 结果反馈模板

请将验证结果按以下格式反馈：

```markdown
### 验证1: 智谱AI错误响应
- error_kind: [填写]
- upstream_status_code: [填写]
- 样例响应: [复制一条完整的 upstream_response_preview]

### 验证2: 智谱AI模型配置
- 是否有 glm-5.2: [是/否]
- available_count: [填写]
- unavailable_count: [填写]

### 验证6: 错误统计（关键）
[复制完整的表格输出，前5行即可]

### 验证7: 降级模式触发（关键）
- 是否有数据: [是/否]
- 如果有，requests_after_unavailable 最大值: [填写]
- unavailable_reason: [填写]
```

---

## 🎯 快速诊断命令

如果你只想快速确认问题，可以直接执行这些单独的SQL：

### 快速检查1: 智谱AI最近的错误
```bash
psql $DATABASE_URL -c "
SELECT error_kind, upstream_status_code, COUNT(*) as cnt
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
GROUP BY error_kind, upstream_status_code
ORDER BY cnt DESC
LIMIT 5;
"
```

### 快速检查2: 降级模式证据
```bash
psql $DATABASE_URL -c "
WITH unavailable_creds AS (
    SELECT credential_id, raw_model_name, unavailable_at
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.available = FALSE
      AND cmb.unavailable_at > now() - interval '1 hour'
)
SELECT COUNT(*) as degraded_usage_count
FROM unavailable_creds uc
JOIN request_logs rl ON rl.credential_id = uc.credential_id
WHERE rl.created_at > uc.unavailable_at;
"
```

如果输出的 `degraded_usage_count` > 0，说明降级模式确实在使用不可用的凭证。

---

## ⚠️ 注意事项

1. **数据库权限**: 确保你的数据库用户有 `SELECT` 和 `INSERT` 权限
2. **数据量**: 如果 `request_logs` 表很大，查询可能需要几秒钟
3. **时区**: 所有时间戳都是数据库服务器时区，注意换算
4. **备份**: 虽然脚本是幂等的，但建议先在测试环境执行

---

## 📞 需要帮助？

如果遇到问题，请提供：
1. 执行的命令
2. 完整的错误信息
3. 数据库版本：`psql $DATABASE_URL -c "SELECT version();"`

---

**准备好了吗？执行上面的命令，然后把结果发给我！**
