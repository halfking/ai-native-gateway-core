# 统计API全面CST时区标准化 - 变更总结

**日期**: 2026-11-19  
**分支**: feat/stats-timezone-standardization  
**目标**: 全面标准化所有admin/*文件的日期查询，使用东8区（CST，中国标准时间）

---

## 一、变更概览

### 1.1 Migration 992: CST时区函数族
**文件**: `sql/migrations/startup/992_add_cst_timezone_functions.sql`

**新增函数**:
```sql
-- 返回CST日起点（北京时间00:00）
cst_today() → timestamptz

-- 返回N天前的CST日起点
cst_days_ago(n int) → timestamptz

-- 返回给定时间戳的CST日起点
cst_day_start(ts timestamptz) → timestamptz

-- 返回给定时间戳的CST日期（date类型）
cst_date(ts timestamptz) → date
```

**设计决策**:
- ✅ 数据库存储保持UTC（timestamptz标准实践）
- ✅ 创建CST函数族供业务层使用
- ✅ 保留UTC函数（migration 991）供内部系统使用
- ✅ 清晰的命名（`cst_*` vs `utc_*`）避免混淆

**业务语义**:
- "今日"指CST零点（北京时间00:00，等价于UTC前一日16:00）
- "最近7天"指从CST今日零点回溯7个CST日
- 与用户认知的"今天""昨天"对齐

---

## 二、修改的文件清单

### 2.1 SQL Migration (1个文件)
- `sql/migrations/startup/992_add_cst_timezone_functions.sql` (新增)

### 2.2 Go代码 (13个文件，共25处CST函数调用)

| 文件 | 修改类型 | CST函数使用 |
|------|----------|-------------|
| `admin/session_analytics_handler.go` | utc→cst | `cst_today()` × 1 |
| `admin/session_analytics_clients.go` | utc→cst | `cst_days_ago()` × 1 |
| `admin/session_analytics_tasks.go` | utc→cst | `cst_days_ago()` × 1 |
| `admin/user_usage_stats.go` | utc→cst + Asia/Shanghai→cst | `cst_days_ago()` × 2, `cst_today()` × 2, `cst_day_start()` × 1 |
| `admin/user_profile.go` | utc→cst | `cst_days_ago()` × 1 |
| `admin/dashboard_session_stats.go` | utc→cst | `cst_days_ago()` × 2 |
| `admin/session_health_api.go` | CURRENT_DATE→cst | `cst_today()` × 1 |
| `admin/tool_policy_api.go` | CURRENT_DATE→cst | `cst_day_start(cst_days_ago())` × 2 |
| `admin/analytics.go` | CURRENT_DATE→cst | `cst_day_start(cst_days_ago())` × 1 |
| `admin/tenant_stats_rollup.go` | Asia/Shanghai→cst | `cst_today()` × 2, `cst_days_ago()` × 3, `cst_date()` × 3 |
| `admin/tenants.go` | Asia/Shanghai→cst | `cst_today()` × 2, `cst_day_start()` × 1 |
| `admin/prompt_injection_handler.go` | CURRENT_DATE→cst | `cst_today()` × 1 |
| `admin/vendor_credential_error_handlers.go` | CURRENT_DATE→cst | `cst_day_start(cst_days_ago())` × 1 |

**函数使用统计**:
- `cst_today()`: 7处
- `cst_days_ago()`: 14处
- `cst_day_start()`: 5处
- `cst_date()`: 3处
- **总计**: 25处CST函数调用

---

## 三、修改前后对比

### 3.1 查询"今日"数据
```sql
-- 修改前（本地时区，不明确）
WHERE created_at >= CURRENT_DATE

-- 修改后（明确CST）
WHERE created_at >= cst_today()
```

### 3.2 查询"最近N天"数据
```sql
-- 修改前（CURRENT_DATE - N，类型不匹配）
WHERE usage_date >= CURRENT_DATE - $1::int

-- 修改后（明确CST日期）
WHERE usage_date >= cst_day_start(cst_days_ago($1::int))
```

### 3.3 时间范围生成（带默认值）
```sql
-- 修改前（Asia/Shanghai硬编码）
SELECT generate_series(
  date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') - (($2::int - 1) * INTERVAL '1 day'),
  date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai'),
  '1 day'
)

-- 修改后（使用CST函数）
SELECT generate_series(
  cst_today() - (($2::int - 1) * INTERVAL '1 day'),
  cst_today(),
  '1 day'
)
```

### 3.4 日期分组
```sql
-- 修改前（Asia/Shanghai硬编码）
SELECT date_trunc('day', f.occurred_at AT TIME ZONE 'Asia/Shanghai')::date AS d

-- 修改后（使用cst_day_start）
SELECT cst_day_start(f.occurred_at)::date AS d
```

---

## 四、验证结果

### 4.1 编译测试
```bash
✓ go build ./admin/... - 编译成功
```

### 4.2 残留检查（应全为0）
- ❌ CURRENT_DATE残留: **0处**
- ❌ utc_xxx函数残留: **0处**
- ❌ 裸Asia/Shanghai残留: **0处**

### 4.3 函数覆盖率
- 修改的Go文件: **13个**
- 新增Migration: **1个**
- CST函数调用: **25处**

---

## 五、时区语义说明

### 5.1 UTC vs CST 边界对比
**示例**: 2026-11-19 北京时间23:30（即将跨日）

| 函数 | 返回值（CST） | 返回值（UTC） | 业务含义 |
|------|---------------|---------------|----------|
| `cst_today()` | 2026-11-19 00:00:00+08 | 2026-11-18 16:00:00+00 | 今日CST零点 |
| `utc_today()` | 2026-11-19 08:00:00+08 | 2026-11-19 00:00:00+00 | 今日UTC零点 |

**差异**: CST的"今日"比UTC的"今日"**早8小时开始**

### 5.2 函数选择指南
- **CST函数族** (`cst_*`): 面向用户的统计API，与用户认知的"今天""昨天"对齐
- **UTC函数族** (`utc_*`): 内部系统、跨时区协调、国际标准时间对齐

---

## 六、未修改的文件（有意保留）

### 6.1 实时监控（滑动窗口，非日边界）
- `admin/probe_dashboard.go` - 使用 `now() - interval '2 hours'`（实时监控）
- `admin/compression_stats.go` - 使用 `date_trunc('day', rl.ts)`（对已有时间戳分桶）

### 6.2 测试文件（测试数据可保持原样）
- `admin/*_test.go` - 所有测试文件

---

## 七、部署检查清单

### 7.1 Migration 992登记（待完成）
根据AGENTS.md的迁移登记规范，需要在以下**6处**登记：

1. ✅ `sql/migrations/startup/992_add_cst_timezone_functions.sql` - 已创建
2. ⏳ `installer/embeddata/sql/migrations/startup/` - 待复制
3. ⏳ `cmd/gateway/main.go` - 检查embed声明
4. ⏳ `cmd/gateway/dbinit/` - 更新runner
5. ⏳ `docs/migrations/startup.tsv` - 添加条目
6. ⏳ `scripts/apply-db-revision-sequence.sh` - Docker部署通道（211/154）

### 7.2 部署验证计划
1. **迁移前备份**: 备份migration_metadata表
2. **迁移执行**: 应用Migration 992，创建CST函数
3. **功能测试**: 
   - 验证 `cst_today()` 返回当前CST日零点
   - 验证 `cst_days_ago(7)` 返回7天前CST日零点
   - 跨日边界测试（23:50-00:10）
4. **数据一致性**: 运行Maestro测试验证三个页面数据一致
5. **回滚准备**: 保留Migration 991（UTC函数仍可用）

---

## 八、后续工作

### 8.1 立即（本轮）
- [ ] 完成Migration 992的6处登记
- [ ] 提交所有更改到feat分支
- [ ] 更新`docs/STATS_DEPLOYMENT_STATUS.md`

### 8.2 部署阶段
- [ ] 部署到测试环境
- [ ] 运行Maestro测试套件
- [ ] 跨日边界测试（23:50-00:10窗口）
- [ ] 生产环境部署

### 8.3 长期优化
- [ ] API文档更新（注明"北京时间""东8区"）
- [ ] 前端UI更新（时间选择器说明"北京时间"）
- [ ] 监控数据变化（对比迁移前后统计差异）

---

## 九、风险与缓解

### 9.1 已知风险
| 风险 | 影响 | 缓解措施 | 状态 |
|------|------|----------|------|
| 跨日边界数据归属 | 中 | 跨日边界测试（23:50-00:10） | 待测试 |
| 历史数据对比 | 低 | UTC函数仍保留，可对比 | ✅ 已缓解 |
| 部署回滚 | 低 | Migration 992只添加函数不修改数据 | ✅ 已缓解 |

### 9.2 回滚策略
```sql
-- 如需回滚，执行：
DROP FUNCTION IF EXISTS cst_today();
DROP FUNCTION IF EXISTS cst_days_ago(int);
DROP FUNCTION IF EXISTS cst_day_start(timestamptz);
DROP FUNCTION IF EXISTS cst_date(timestamptz);
DELETE FROM migration_metadata WHERE version = 992;

-- 然后将Go代码中的cst_xxx改回utc_xxx或CURRENT_DATE
```

---

## 十、关键决策记录

### 10.1 为什么选择方案B（保持UTC存储 + CST业务函数）
1. ✅ **标准实践**: timestamptz本质是UTC存储 + 时区显示
2. ✅ **向后兼容**: 不影响现有UTC函数和内部系统
3. ✅ **清晰语义**: `cst_*` vs `utc_*` 命名明确，不会混淆
4. ✅ **易于扩展**: 未来支持其他时区只需添加新函数族

### 10.2 为什么不直接修改Migration 991
1. Migration 991已在讨论中被引用和分析
2. 保留UTC函数族供内部系统和跨时区场景使用
3. CST与UTC共存更灵活，避免"二次改回"

---

## 附录A：完整的SQL函数定义

详见 `sql/migrations/startup/992_add_cst_timezone_functions.sql`

## 附录B：受影响的API端点

| 端点 | 文件 | 影响 |
|------|------|------|
| `/api/admin/session-analytics` | session_analytics_handler.go | "今日"定义改为CST |
| `/api/admin/user-usage` | user_usage_stats.go | 时间范围改为CST |
| `/api/admin/session-health` | session_health_api.go | "今日回收数"改为CST |
| `/api/admin/tool-usage` | tool_policy_api.go | 工具使用统计改为CST |
| `/api/admin/tenant-stats` | tenant_stats_rollup.go | 租户统计改为CST |
| `/api/admin/prompt-injection` | prompt_injection_handler.go | 注入检测统计改为CST |

---

**文档版本**: v1.0  
**最后更新**: 2026-11-19 23:30 CST  
**维护者**: AI Assistant (Kiro)
