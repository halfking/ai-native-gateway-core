# 245 服务器压缩修复现状分析报告

**生成时间**: 2026-07-20
**服务器**: 8.136.114.245:25022
**数据库**: 172.16.2.210:5432/llm_gateway

---

## 一、现状总结

### ✅ 好消息

1. **数据库已包含压缩字段**
   - `compression_strategy` 字段存在
   - `compression_reason` 字段存在
   - 表结构支持压缩功能

2. **错误率为 0**
   - 最近 24 小时：9 个请求，0 个 context_length_exceeded 错误
   - 错误率：0.00%

3. **Gateway 正在运行**
   - 进程 PID: 3358569
   - 运行时间：从 03:48 开始
   - 路径：`/opt/llm-gateway-go/gateway`

### ⚠️ 发现的问题

1. **所有 gpt-5.6-luna 模型的 context_window = NULL**
   ```
   canonical_name: gpt-5.6-luna
   context_window: (空)
   family: openai-gpt
   ```
   - 这是核心问题！NULL 导致 token 触发器失效

2. **provider_settings 表中没有压缩配置**
   ```sql
   SELECT * FROM provider_settings
   WHERE provider_id IN (587, 2451)
     AND setting_key = 'compression.mode';
   -- 结果：0 行
   ```
   - apigpt (2451) 和 apiclaude (587) 都没有压缩设置
   - **这意味着使用全局默认值**（应该是启用的）

3. **最近几乎没有流量**
   - 最近 1 小时：0 个请求
   - 最近 24 小时：仅 9 个请求（测试数据）
   - 这解释了为什么错误率为 0（没有真实流量）

4. **修复 SQL 未部署到服务器**
   - `sql/hotfix/20260719-fix-compression-relay-nodes.sql` 不存在
   - Git 未安装在服务器上
   - 代码未同步

---

## 二、数据库 Schema 分析

### 表结构差异

#### Provider Settings
- **列名不同**：
  - 我们的 SQL 使用：`key`, `value`
  - 实际表结构：`setting_key`, `setting_value`

- **数据类型不同**：
  - 我们的 SQL：`value TEXT`
  - 实际表结构：`setting_value JSONB`

#### Request Logs
- **时间戳列名**：
  - 我们的 SQL 使用：`created_at`
  - 实际表结构：`ts`

- **Providers 表列名**：
  - 我们的 SQL 使用：`label`
  - 实际表结构：`code`, `display_name`, `catalog_code`（没有 `label`）

### 结论
**我们的修复 SQL 与生产数据库 schema 不兼容！需要重新编写。**

---

## 三、根因确认

### 原因 1: context_window = NULL ✅ 已确认

```sql
SELECT canonical_name, context_window
FROM models_canonical
WHERE canonical_name = 'gpt-5.6-luna';

-- 结果：
-- gpt-5.6-luna | (空)
```

**影响**：
- Token 触发器（85% 阈值）无法工作
- 只能依赖消息数触发器（50 条消息）或空闲触发器（5 分钟）

### 原因 2: 流量极低

- 最近 24 小时仅 9 个请求
- 最近 1 小时 0 个请求
- **这可能是测试环境，而非生产环境**

### 原因 3: 中转节点压缩配置

```sql
SELECT * FROM provider_settings
WHERE provider_id IN (587, 2451)
  AND setting_key = 'compression.mode';
-- 结果：0 行
```

**当前状态**：
- 没有显式禁用压缩
- 使用全局默认值（Smart 模式）
- **理论上应该启用了压缩**

---

## 四、修复方案

### 方案 A: 修复 context_window（立即执行）

```sql
-- 连接数据库
psql 'postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable'

-- 修复 gpt-5.6-luna
BEGIN;

UPDATE models_canonical
SET
    context_window = 128000,
    updated_at = NOW()
WHERE canonical_name = 'gpt-5.6-luna'
  AND context_window IS NULL;

-- 验证
SELECT canonical_name, context_window
FROM models_canonical
WHERE canonical_name = 'gpt-5.6-luna';

COMMIT;
```

### 方案 B: 显式启用中转节点压缩（可选）

```sql
-- 如果需要确保压缩启用
BEGIN;

INSERT INTO provider_settings (provider_id, setting_key, setting_value, enabled, created_by)
VALUES
    (587, 'compression.mode', '"smart"'::jsonb, true, 'admin'),
    (2451, 'compression.mode', '"smart"'::jsonb, true, 'admin')
ON CONFLICT (provider_id, setting_key) DO UPDATE
SET
    setting_value = '"smart"'::jsonb,
    enabled = true,
    updated_at = NOW();

-- 验证
SELECT provider_id, setting_key, setting_value, enabled
FROM provider_settings
WHERE provider_id IN (587, 2451)
  AND setting_key = 'compression.mode';

COMMIT;
```

### 方案 C: 重启 Gateway（如需要）

```bash
# SSH 到服务器
ssh -p 25022 root@8.136.114.245

# 查看当前进程
ps aux | grep gateway

# 重启（方式取决于部署方式）
kill -HUP 3358569
# 或
systemctl restart llm-gateway
# 或重新启动二进制
```

---

## 五、验证计划

### 修复后验证

#### 1. 验证 context_window

```sql
SELECT canonical_name, context_window, updated_at
FROM models_canonical
WHERE canonical_name = 'gpt-5.6-luna';
```

**预期**: `context_window = 128000`

#### 2. 验证压缩配置

```sql
SELECT provider_id, setting_key, setting_value
FROM provider_settings
WHERE provider_id IN (587, 2451)
  AND setting_key = 'compression.mode';
```

**预期**: `setting_value = "smart"`（如果执行了方案 B）

#### 3. 模拟大会话测试

需要在有真实流量后观察：
```sql
SELECT
    COUNT(*) AS total,
    COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) AS compressed,
    ROUND(100.0 * COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) / NULLIF(COUNT(*), 0), 1) AS rate_pct,
    COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) AS ctx_errors
FROM request_logs
WHERE ts > NOW() - INTERVAL '1 hour'
  AND provider_id IN (587, 2451);
```

**预期**:
- `rate_pct > 70%`（有长会话时）
- `ctx_errors = 0`

---

## 六、重要发现

### 1. 这可能是测试环境

证据：
- 最近 24 小时仅 9 个请求
- 请求 ID 为 "test-new-1" 到 "test-new-5"
- 流量极低

**建议**：确认这是否为生产环境

### 2. Schema 版本不匹配

生产数据库的 schema 与我们的修复 SQL 不兼容：
- 列名不同
- 数据类型不同（JSONB vs TEXT）

**已解决**：上面的修复方案已适配实际 schema

### 3. 没有显式禁用压缩

- `provider_settings` 表中没有 compression.mode = "off"
- 原先认为的"禁用压缩"配置不存在
- **这意味着压缩功能应该已经启用**

**结论**：真正的问题只是 `context_window = NULL`

---

## 七、执行建议

### 立即执行（方案 A）

```bash
ssh -p 25022 root@8.136.114.245 "psql 'postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable' << 'EOF'
BEGIN;
UPDATE models_canonical
SET context_window = 128000, updated_at = NOW()
WHERE canonical_name = 'gpt-5.6-luna' AND context_window IS NULL;
SELECT canonical_name, context_window FROM models_canonical WHERE canonical_name = 'gpt-5.6-luna';
COMMIT;
EOF
"
```

### 观察结果

- 修复后，当有包含大量消息（>50 条）的请求时
- Token 触发器（85% 阈值）将正常工作
- 应该不再出现 context_length_exceeded 错误

---

## 八、风险评估

| 风险 | 可能性 | 影响 | 缓解措施 |
|-----|-------|------|---------|
| 修改错误的数据库 | 低 | 高 | 已确认连接字符串 |
| 影响其他模型 | 极低 | 中 | WHERE 条件精确匹配 |
| 需要重启 Gateway | 中 | 低 | Gateway 应该热加载 canonical 数据 |
| 流量低无法验证 | 高 | 低 | 需要等待真实流量或主动测试 |

---

## 九、后续行动

1. ✅ **立即执行方案 A**（修复 context_window）
2. ⏳ **等待真实流量**，观察压缩效果
3. 📊 **24 小时后查询压缩率**
4. 🔍 **如果仍有问题**，执行方案 B（显式启用压缩）

---

**报告生成**: 2026-07-20
**下一步**: 等待你确认后执行修复
