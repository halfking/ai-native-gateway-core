# 自检系统诊断与修复工具

> **最后更新**: 2026-09-06  
> **相关文档**: `SELFCHECK_CODE_IMPROVEMENTS_20260906.md`

本目录包含自检系统的诊断工具和修复脚本，用于识别和解决供应商节点状态同步问题。

---

## 快速开始

### 运行诊断

```bash
# 设置数据库连接信息
export DB_HOST=localhost
export DB_PORT=5432
export DB_NAME=llm_gateway
export DB_USER=postgres
export DB_PASSWORD=your_password

# 运行诊断脚本
bash scripts/diagnose_selfcheck.sh
```

诊断报告将保存到 `diagnostics_reports/selfcheck_diagnostic_YYYYMMDD_HHMMSS.txt`

### 查看详细 SQL

如果需要手动执行诊断或修复，查看完整的 SQL 脚本：

```bash
cat sql/diagnostics/selfcheck_diagnostics.sql
```

---

## 工具清单

### 1. `scripts/diagnose_selfcheck.sh`

**用途**: 自动化诊断脚本，快速发现常见问题

**功能**:
- ✅ 检查数据库连接
- ✅ 诊断模型绑定歧义
- ✅ 诊断 NULL `unavailable_recover_at`
- ✅ 诊断过期未恢复的凭据
- ✅ 诊断缺失的 `node_probe_state` 记录
- ✅ 诊断长时间未执行的探测任务
- ✅ 生成系统健康状况摘要
- ✅ 保存详细诊断报告

**输出示例**:
```
================================================================
诊断摘要
================================================================

诊断时间: 2026-09-06 20:30:00

⚠ 模型绑定歧义: 3 个凭据
⚠ 缺失 node_probe_state: 5 个绑定
⚠ 延迟探测任务: 12 个

发现 20 个需要关注的问题

建议:
1. 查看完整报告: ./diagnostics_reports/selfcheck_diagnostic_20260906_203000.txt
2. 参考修复 SQL: sql/diagnostics/selfcheck_diagnostics.sql
3. 如需修复，建议先在测试环境验证
```

### 2. `sql/diagnostics/selfcheck_diagnostics.sql`

**用途**: 完整的诊断和修复 SQL 脚本集合

**包含内容**:
- **8 个诊断查询**: 识别各类问题
- **3 个修复脚本**: 修复常见问题（带预览）
- **查询辅助工具**: 查看特定凭据的完整状态
- **详细注释**: 说明每个查询的用途和预期结果

**诊断查询清单**:
1. 模型绑定歧义
2. NULL `unavailable_recover_at`
3. 过期未恢复的凭据
4. 缺失 `node_probe_state`
5. `broken_confirmed` 阻塞
6. `model_offers` 不一致
7. 长时间未执行的探测
8. 系统状态分布统计

**修复脚本清单**:
1. 修复 NULL `unavailable_recover_at`
2. 创建缺失的 `node_probe_state` 记录
3. 重置长时间过期的探测任务

---

## 使用场景

### 场景 1: 日常健康检查

**频率**: 每天或每周

**步骤**:
```bash
# 运行诊断
bash scripts/diagnose_selfcheck.sh

# 查看报告
cat diagnostics_reports/selfcheck_diagnostic_*.txt | tail -50

# 如果发现问题，查看详细 SQL
cat sql/diagnostics/selfcheck_diagnostics.sql
```

### 场景 2: 问题排查

**触发条件**: 用户反馈节点状态未恢复，需要手工干预

**步骤**:
```bash
# 1. 运行诊断识别问题类型
bash scripts/diagnose_selfcheck.sh

# 2. 根据问题类型查看详细信息
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -f sql/diagnostics/selfcheck_diagnostics.sql

# 3. 查看特定凭据的完整状态
psql -h $DB_HOST -U $DB_USER -d $DB_NAME <<EOF
\set CREDENTIAL_ID 42
-- 复制 selfcheck_diagnostics.sql 中的 "查询辅助工具" SQL
EOF

# 4. 根据诊断结果决定是否执行修复
```

### 场景 3: 代码部署后验证

**时机**: P0.3/P2.3 修复部署后

**步骤**:
```bash
# 部署前诊断（作为基线）
bash scripts/diagnose_selfcheck.sh > before_deployment.txt

# 部署代码
# ...

# 部署后诊断
bash scripts/diagnose_selfcheck.sh > after_deployment.txt

# 对比变化
diff before_deployment.txt after_deployment.txt

# 预期变化:
# - "模型绑定歧义" 导致的 RestoreOnSuccess 失败应该消失
# - "NULL unavailable_recover_at" 不应该新增
# - 系统整体健康状况应该改善
```

### 场景 4: 数据修复

**触发条件**: 诊断发现大量历史遗留问题

**步骤**:
```bash
# 1. 在测试环境运行诊断
bash scripts/diagnose_selfcheck.sh

# 2. 备份生产数据库
pg_dump -h $DB_HOST -U $DB_USER $DB_NAME > backup_$(date +%Y%m%d).sql

# 3. 在测试环境执行修复（先预览）
psql -h $DB_HOST -U $DB_USER -d $DB_NAME <<EOF
-- 预览修复 1: NULL unavailable_recover_at
SELECT COUNT(*) FROM credential_model_bindings
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL;
EOF

# 4. 确认影响范围后执行修复
psql -h $DB_HOST -U $DB_USER -d $DB_NAME <<EOF
-- 从 selfcheck_diagnostics.sql 复制修复 SQL（取消注释）
UPDATE credential_model_bindings
SET unavailable_recover_at = unavailable_at + INTERVAL '30 minutes'
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL;
EOF

# 5. 再次运行诊断验证修复效果
bash scripts/diagnose_selfcheck.sh

# 6. 在生产环境重复步骤 2-5
```

---

## 诊断问题解读

### 🟢 正常状态

- **模型绑定歧义**: 0 个
- **NULL unavailable_recover_at**: 0 个
- **过期未恢复凭据**: 0-5 个（少量可接受，可能正在探测中）
- **缺失 node_probe_state**: 0 个
- **延迟探测任务**: 0-10 个（少量可接受，可能是队列正常积压）

### 🟡 需要关注

- **模型绑定歧义**: 1-10 个
  - **影响**: 热路径恢复可能失败（P0.3 修复后会选择第一个候选）
  - **行动**: 查看日志中的 "ambiguous model binding" WARN，准备数据清理

- **过期未恢复凭据**: 5-20 个
  - **影响**: 恢复延迟，可能需要手工干预
  - **行动**: 查看 `state_reason_code`，判断是否是上游实际故障

- **延迟探测任务**: 10-50 个
  - **影响**: 节点状态更新延迟
  - **行动**: 检查探测队列是否积压，考虑执行修复 3

### 🔴 严重问题

- **NULL unavailable_recover_at**: > 0 个
  - **影响**: 永久不可用，恢复机制完全失效
  - **行动**: 立即执行修复 1

- **模型绑定歧义**: > 10 个
  - **影响**: 大量凭据热路径恢复失败
  - **行动**: 优先数据清理，消除歧义根源

- **缺失 node_probe_state**: > 10 个
  - **影响**: 无法调度探测，永久不可用
  - **行动**: 立即执行修复 2

- **过期未恢复凭据**: > 20 个
  - **影响**: 恢复机制大面积失效
  - **行动**: 检查代码部署状态，可能需要回滚

- **延迟探测任务**: > 50 个
  - **影响**: 探测队列严重积压
  - **行动**: 检查 `ProbeQueueWorker` 是否正常运行，考虑执行修复 3

---

## 修复操作指南

### 修复 1: NULL unavailable_recover_at

**何时执行**: 诊断发现 NULL `unavailable_recover_at`

**步骤**:
```sql
-- 1. 预览影响范围
SELECT 
    COUNT(*) AS affected_rows,
    MIN(unavailable_at) AS earliest,
    MAX(unavailable_at) AS latest
FROM credential_model_bindings
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL;

-- 2. 如果影响范围可接受，执行修复
UPDATE credential_model_bindings
SET unavailable_recover_at = unavailable_at + INTERVAL '30 minutes'
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL;

-- 3. 验证
SELECT COUNT(*) FROM credential_model_bindings
WHERE available = FALSE
  AND unavailable_recover_at IS NULL;
-- 预期: 0 或接近 0
```

### 修复 2: 缺失 node_probe_state

**何时执行**: 诊断发现缺失 `node_probe_state`

**步骤**:
```sql
-- 1. 预览影响范围
SELECT COUNT(*) AS affected_rows
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND nps.credential_id IS NULL;

-- 2. 执行修复
INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds)
SELECT 
    cmb.credential_id,
    pm.raw_model_name,
    NOW(),
    5
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND nps.credential_id IS NULL
  AND c.status = 'active'
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

-- 3. 验证
SELECT COUNT(*) 
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND nps.credential_id IS NULL;
-- 预期: 0
```

### 修复 3: 重置延迟探测

**何时执行**: 诊断发现大量延迟探测任务（> 50 个）

**步骤**:
```sql
-- 1. 预览影响范围
SELECT 
    COUNT(*) AS affected_rows,
    MIN(next_retry_at) AS earliest,
    MAX(next_retry_at) AS latest
FROM node_probe_state
WHERE next_retry_at < NOW() - INTERVAL '10 minutes'
  AND paused = FALSE
  AND (in_flight_until IS NULL OR in_flight_until < NOW());

-- 2. 执行修复
UPDATE node_probe_state
SET 
    next_retry_at = NOW(),
    next_retry_seconds = 5,
    updated_at = NOW()
WHERE next_retry_at < NOW() - INTERVAL '10 minutes'
  AND paused = FALSE
  AND (in_flight_until IS NULL OR in_flight_until < NOW());

-- 3. 验证
SELECT COUNT(*) 
FROM node_probe_state
WHERE next_retry_at < NOW() - INTERVAL '5 minutes'
  AND paused = FALSE;
-- 预期: 0 或接近 0
```

---

## 监控建议

### 定期诊断

建议设置 cron 任务定期运行诊断：

```bash
# 编辑 crontab
crontab -e

# 每天早上 9 点运行诊断
0 9 * * * cd /path/to/llm-gateway && bash scripts/diagnose_selfcheck.sh > /dev/null 2>&1

# 或者每周一早上运行
0 9 * * 1 cd /path/to/llm-gateway && bash scripts/diagnose_selfcheck.sh
```

### 告警集成

将诊断结果集成到告警系统：

```bash
#!/bin/bash
# 运行诊断并发送告警

bash scripts/diagnose_selfcheck.sh

# 提取问题计数
ISSUES=$(grep -E "⚠|✗" diagnostics_reports/selfcheck_diagnostic_*.txt | wc -l)

# 如果发现问题，发送告警
if [ "$ISSUES" -gt 5 ]; then
    # 发送到 Slack / 钉钉 / 企业微信
    curl -X POST "https://your-webhook-url" \
        -d "发现 $ISSUES 个自检问题，请查看报告"
fi
```

---

## 故障排查

### 问题 1: 诊断脚本运行失败

**现象**: `bash scripts/diagnose_selfcheck.sh` 报错

**可能原因**:
- 数据库连接失败
- 缺少 psql 命令
- 权限不足

**解决方案**:
```bash
# 检查 psql 是否安装
which psql

# 测试数据库连接
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "SELECT 1"

# 检查环境变量
echo $DB_HOST $DB_PORT $DB_NAME $DB_USER

# 添加执行权限
chmod +x scripts/diagnose_selfcheck.sh
```

### 问题 2: 修复后问题仍然存在

**现象**: 执行修复 SQL 后，诊断仍发现相同问题

**可能原因**:
- 有新的代码路径未设置字段
- 修复 SQL 条件不完整
- 后台任务未启动

**解决方案**:
```bash
# 1. 检查后台任务是否运行
ps aux | grep "credential_recovery\|node_probe"

# 2. 查看应用日志
tail -f /var/log/llm-gateway.log | grep -E "recovery|probe"

# 3. 如果是新问题，查看详细错误
psql -h $DB_HOST -U $DB_USER -d $DB_NAME <<EOF
-- 查看最近更新的不可用绑定
SELECT * FROM credential_model_bindings
WHERE available = FALSE
  AND updated_at > NOW() - INTERVAL '1 hour'
ORDER BY updated_at DESC
LIMIT 20;
EOF
```

---

## 相关文档

- **架构审查**: `SELFCHECK_ARCHITECTURE_REVIEW_20260906.md`
- **优化建议**: `SELFCHECK_OPTIMIZATION_RECOMMENDATIONS_20260906.md`
- **代码完善**: `SELFCHECK_CODE_IMPROVEMENTS_20260906.md`
- **设计文档**: `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md`

---

## 维护记录

| 日期 | 变更 | 负责人 |
|------|------|--------|
| 2026-09-06 | 初始版本：创建诊断脚本和 SQL | ZCode |

---

**问题反馈**: 如果诊断工具发现新的问题类型，请更新此文档和 SQL 脚本。
