# AUTO路由结构化特征运维手册 (Runbook)

**版本**: v1.0  
**最后更新**: 2026-09-05  
**负责人**: Platform Team  
**相关文档**: 
- [架构设计](./auto-model-optimization/02-architecture-design.md)
- [存储优化](./auto-model-optimization/05-storage-optimization.md)
- [审计报告](./audit/2026-09-05-auto-route-privacy-compliance-audit.md)

---

## 概述

AUTO路由结构化特征系统通过存储**非可逆、低敏感度的特征**（而非原始prompt内容）来支持机器学习训练和人工标注，同时保护用户隐私。

**核心原则**: `auto_route_selections` 表只存储结构化特征（枚举、桶、布尔值、哈希），**不存储** prompt、messages、response、summary、keywords或任何可逆内容。

---

## 系统架构

### 数据流

```
Request
  ↓
extractSignalsForAuto()  → ClassificationSignals (内存，包含原始prompt)
  ↓
ExtractStructuredFeatures()  → StructuredFeatures (非可逆特征)
  ↓
WriteAutoSelection()  → auto_route_selections_hot (数据库，只存特征)
  ↓ (8小时后)
promote_hot_to_partition()  → auto_route_selections_YYYY_MM (月分区)
```

### 存储布局

- **热窗口**: `auto_route_selections_hot` — 最近8小时
- **月分区**: `auto_route_selections_YYYY_MM` — 历史数据，按月分区
- **统一视图**: `auto_route_selections_all` — UNION ALL (hot + 分区)

---

## 日常运维

### 1. 监控健康指标

#### 1.1 写入速率
```sql
-- 每分钟写入行数（最近1小时）
SELECT 
  date_trunc('minute', ts) AS minute,
  COUNT(*) AS rows_per_minute
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '1 hour'
GROUP BY 1
ORDER BY 1 DESC;
```

**正常范围**: 取决于流量，通常 10-1000 行/分钟  
**告警阈值**: 连续5分钟写入为0，或突增10倍

#### 1.2 结算延迟
```sql
-- 未结算行数（应该 < 1000）
SELECT COUNT(*) AS unsettled_count
FROM auto_route_selections_hot
WHERE settled_at IS NULL
  AND ts < NOW() - INTERVAL '5 minutes';
```

**正常值**: < 100（大部分请求5分钟内结算）  
**告警阈值**: > 1000 或持续增长（结算worker可能停滞）

#### 1.3 特征填充率
```sql
-- 检查特征字段填充率（应该 > 95%）
SELECT 
  COUNT(*) AS total_rows,
  COUNT(detected_language) AS has_language,
  COUNT(prompt_length_bucket) AS has_length_bucket,
  COUNT(content_hash) AS has_hash,
  ROUND(100.0 * COUNT(detected_language) / NULLIF(COUNT(*), 0), 2) AS language_fill_rate,
  ROUND(100.0 * COUNT(content_hash) / NULLIF(COUNT(*), 0), 2) AS hash_fill_rate
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '1 hour';
```

**正常值**: fill_rate > 95%  
**告警阈值**: < 80%（特征提取可能失败）

### 2. 分区管理

#### 2.1 检查分区状态
```sql
-- 列出所有分区及其行数
SELECT 
  schemaname,
  tablename,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE tablename LIKE 'auto_route_selections_%'
  AND schemaname = 'public'
ORDER BY tablename;
```

#### 2.2 预创建下月分区
```bash
# 手动创建下月分区（生产环境应自动化）
psql -c "SELECT ensure_auto_route_selections_partition('2026-10-01'::date);"
```

**操作时机**: 每月25日前创建下月分区  
**检查**: 确认DEFAULT分区为空或行数 < 1000

#### 2.3 热窗口促销到分区
```bash
# 手动触发促销（通常由定时任务自动执行）
psql -c "SELECT promote_auto_route_selections_hot_to_partition();"
```

**操作频率**: 每小时自动执行（由bg worker调度）  
**检查**: hot表保持最近8小时数据

### 3. 特征质量审查

#### 3.1 检查特征分布
```sql
-- 语言分布
SELECT detected_language, COUNT(*) AS count
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 2 DESC;

-- 长度桶分布
SELECT prompt_length_bucket, COUNT(*) AS count
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 2 DESC;

-- 复杂度分布
SELECT complexity_bucket, COUNT(*) AS count
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 2 DESC;
```

**预期**: 分布合理（不应集中在单一桶）  
**异常**: 99%+ 数据在同一个桶 → 特征提取逻辑可能有误

#### 3.2 检查内容哈希去重
```sql
-- 检查重复请求（相同content_hash）
SELECT 
  content_hash,
  COUNT(*) AS duplicate_count,
  MIN(ts) AS first_seen,
  MAX(ts) AS last_seen
FROM auto_route_selections_hot
WHERE ts > NOW() - INTERVAL '24 hours'
  AND content_hash IS NOT NULL
GROUP BY 1
HAVING COUNT(*) > 10
ORDER BY 2 DESC
LIMIT 20;
```

**用途**: 识别高频重复请求（可能需要缓存优化）

---

## 故障排查

### 问题1: 特征字段全为NULL

**症状**: 
```sql
SELECT COUNT(*) FROM auto_route_selections_hot 
WHERE detected_language IS NULL AND ts > NOW() - INTERVAL '1 hour';
-- 返回 > 90% 的行
```

**可能原因**:
1. `ExtractStructuredFeatures()` 未被调用
2. 信号提取失败（ClassificationSignals为空）
3. 代码回滚到旧版本（不支持特征）

**排查步骤**:
```bash
# 1. 检查代码版本
git log -1 --oneline

# 2. 检查日志是否有特征提取错误
grep "ExtractStructuredFeatures" /var/log/llm-gateway/*.log | tail -20

# 3. 重启gateway服务
systemctl restart llm-gateway
```

**修复**: 确保运行包含 commit `6686a215c` 或更新版本的代码

### 问题2: 结算worker停滞

**症状**:
```sql
SELECT COUNT(*) FROM auto_route_selections_hot WHERE settled_at IS NULL;
-- 持续增长，超过10000行
```

**可能原因**:
1. AutoRouteSettleWorker未启动
2. request_logs表查询超时
3. 数据库连接池耗尽

**排查步骤**:
```bash
# 1. 检查worker是否运行
ps aux | grep AutoRouteSettleWorker

# 2. 检查数据库连接
psql -c "SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE '%settle%';"

# 3. 检查错误日志
grep "AutoRouteSettleWorker" /var/log/llm-gateway/error.log | tail -50
```

**修复**:
```bash
# 重启gateway（会重新启动所有workers）
systemctl restart llm-gateway

# 或手动触发结算（一次性）
psql -c "UPDATE auto_route_selections_hot SET settled_at = NOW() WHERE settled_at IS NULL AND ts < NOW() - INTERVAL '10 minutes' LIMIT 1000;"
```

### 问题3: 隐私测试失败

**症状**:
```bash
./scripts/verify-privacy-compliance.sh
# 输出: FAIL: Structured features leaked content
```

**可能原因**:
1. 代码修改意外引入内容存储
2. 新增字段未遵循隐私原则
3. 测试用例过时

**排查步骤**:
```bash
# 1. 运行详细测试
./scripts/verify-privacy-compliance.sh --verbose

# 2. 检查具体失败的测试
go test ./autoroute -run TestStructuredFeaturesNoContentLeakage -v

# 3. 审查最近的代码变更
git diff HEAD~5 autoroute/structured_features.go
```

**修复**: 
- 如果是代码bug：修复并添加测试覆盖
- 如果是测试过时：更新测试用例
- **禁止**: 删除或跳过失败的隐私测试

---

## 维护操作

### 月度任务

#### 1. 创建下月分区（每月25日）
```sql
-- 示例：2026年10月
SELECT ensure_auto_route_selections_partition('2026-10-01'::date);
```

#### 2. 检查存储增长
```sql
-- 查看各分区大小
SELECT 
  tablename,
  pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size,
  (SELECT reltuples::bigint FROM pg_class WHERE relname = tablename) AS estimated_rows
FROM pg_tables
WHERE tablename LIKE 'auto_route_selections_%'
ORDER BY pg_total_relation_size('public.'||tablename) DESC;
```

#### 3. 归档旧分区（保留政策：6个月）
```sql
-- 示例：删除6个月前的分区
DROP TABLE IF EXISTS auto_route_selections_2026_03;
```

**注意**: 删除前确保已备份或确认不再需要

### 季度任务

#### 1. 特征schema演进审查
- 检查是否需要添加新特征字段
- 评估现有特征的有效性
- 规划feature_version升级（v1 → v2）

#### 2. 隐私合规审计
```bash
# 运行完整审计
./scripts/verify-privacy-compliance.sh --verbose

# 检查是否有新的敏感字段
psql -c "\d+ auto_route_selections_hot" | grep -E "prompt|message|content|text"
```

**预期**: 无匹配（除了 content_hash）

---

## 性能优化

### 1. 索引维护
```sql
-- 检查索引使用率
SELECT 
  schemaname,
  tablename,
  indexname,
  idx_scan AS index_scans,
  idx_tup_read AS tuples_read,
  idx_tup_fetch AS tuples_fetched
FROM pg_stat_user_indexes
WHERE tablename LIKE 'auto_route_selections%'
ORDER BY idx_scan DESC;
```

**优化**: 删除idx_scan=0的未使用索引

### 2. VACUUM分析
```sql
-- 手动VACUUM（通常自动执行）
VACUUM ANALYZE auto_route_selections_hot;
```

**操作频率**: autovacuum自动处理，手动操作仅在性能下降时

### 3. 查询优化
```sql
-- 慢查询示例（应避免全表扫描）
EXPLAIN ANALYZE
SELECT * FROM auto_route_selections_all
WHERE ts > NOW() - INTERVAL '7 days'
  AND task_type = 'code';
```

**最佳实践**: 
- 总是包含时间范围过滤
- 优先查询hot表（最近8小时）
- 使用`auto_route_selections_all` view而非手动UNION

---

## 应急响应

### Runbook: 数据库空间告急

**触发条件**: 磁盘使用率 > 85%

**立即操作**:
```bash
# 1. 检查最大分区
psql -c "SELECT tablename, pg_size_pretty(pg_total_relation_size('public.'||tablename)) FROM pg_tables WHERE tablename LIKE 'auto_route_selections_%' ORDER BY pg_total_relation_size('public.'||tablename) DESC LIMIT 5;"

# 2. 归档或删除旧分区（示例：删除3个月前）
psql -c "DROP TABLE IF EXISTS auto_route_selections_2026_06;"

# 3. VACUUM释放空间
psql -c "VACUUM FULL auto_route_selections;"
```

**后续**: 调整保留政策或增加存储容量

### Runbook: 隐私泄露疑似事件

**触发条件**: 告警或用户报告"在日志/数据库中看到了prompt内容"

**立即操作**:
```bash
# 1. 停止gateway写入（紧急）
systemctl stop llm-gateway

# 2. 审查最近写入的数据
psql -c "SELECT * FROM auto_route_selections_hot ORDER BY ts DESC LIMIT 10;" > /tmp/audit_sample.txt

# 3. 检查是否有异常字段
cat /tmp/audit_sample.txt | grep -E "credit|password|secret|key|token"
```

**上报**: 立即联系安全团队和平台负责人

**修复**: 根据审计结果决定是否需要回滚代码或清空表

---

## 扩展与演进

### 添加新的结构化特征

**流程**:
1. 在设计文档中提案（说明特征定义、非可逆性证明）
2. 创建新的migration（例如 `659_add_new_feature.sql`）
3. 更新 `StructuredFeatures` 结构体
4. 更新 `ExtractStructuredFeatures()` 函数
5. 添加隐私保护测试
6. 增加 `feature_version`（例如 v1 → v2）

**禁止**:
- 添加任何存储原始内容的字段
- 添加可逆的特征（例如"前10个关键词"）
- 绕过 `ExtractStructuredFeatures()` 直接写入

### Feature Schema版本升级

**场景**: 当特征定义发生变化（新增字段、修改桶划分等）

**步骤**:
1. 更新 `FeatureVersionV1` → `FeatureVersionV2` 常量
2. 保持向后兼容：v1数据仍可查询
3. 训练pipeline需同时支持v1和v2
4. 灰度上线：逐步切换到v2

---

## 联系方式

**问题报告**: 
- Slack: #llm-gateway-alerts
- 值班: oncall@example.com

**代码仓库**: 
- GitHub: [llm-gateway-go](https://github.com/your-org/llm-gateway-go)
- 相关PR: #658 (结构化特征v1)

**文档更新**: 提交PR到 `docs/runbooks/` 目录
