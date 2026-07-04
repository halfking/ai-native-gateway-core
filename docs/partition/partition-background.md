# PostgreSQL 分区表 Columnar 存储与写入冲突 - 背景文档

**文档版本**: 1.0  
**创建日期**: 2026-07-04  
**适用环境**: PostgreSQL 15.3 + Citus Columnar Extension  
**项目**: llm-gateway-go

---

## 1. 问题背景

### 1.1 业务场景

llm-gateway-go 是一个高吞吐量的 LLM API 网关，核心遥测表 `request_logs` 和 `usage_ledger` 面临以下挑战：

- **高写入频率**：每秒数百次 INSERT/UPDATE（流式响应期间频繁更新）
- **大数据量**：单月数据 > 1GB，需要高效存储
- **长期归档**：历史数据需要保留但查询频率低

### 1.2 PostgreSQL 分区表架构

为了平衡性能和存储成本，我们采用了**按月范围分区 + Columnar 列存储**的混合架构：

```
request_logs (父表, PARTITION BY RANGE(ts))
├─ request_logs_2026_06 (历史归档, columnar, 高压缩比 70%+)
├─ request_logs_2026_07 (当月数据, heap, 支持 UPSERT)
├─ request_logs_2026_08 (下月预创建, heap)
└─ request_logs_default (兜底分区, heap)
```

**设计初衷**：
- **历史月份** → columnar 存储（压缩比 70%+，节省磁盘）
- **当月数据** → heap 存储（支持频繁 UPSERT）
- **父表查询** → 自动聚合所有分区（统一查询接口）

---

## 2. 核心冲突：Columnar 不支持 UPSERT

### 2.1 技术根源

**PostgreSQL Columnar 存储的限制**：
1. **不支持 `UPDATE`**
2. **不支持 `DELETE`**
3. **不支持 `ON CONFLICT` (speculative insertion)**
4. **不支持 CTID scan**（行级定位）

### 2.2 实际报错

当尝试对 columnar 分区执行 UPDATE 时：

```sql
UPDATE request_logs SET success = true WHERE request_id = 'xxx';
-- ERROR: UPDATE and CTID scans not supported for ColumnarScan
-- (SQLSTATE 0A000)
```

当尝试对 columnar 分区执行 INSERT ... ON CONFLICT 时：

```sql
INSERT INTO request_logs_2026_07 (...) VALUES (...)
ON CONFLICT (request_id, ts) DO UPDATE SET ...;
-- ERROR: ON CONFLICT is not supported for columnar tables
-- (SQLSTATE 0A000)
```

### 2.3 业务冲突

我们的遥测数据写入模式：

```go
// 流式响应期间的典型写入模式
// 1. INSERT 初始记录
INSERT INTO request_logs (...) VALUES (...);

// 2. 流式响应过程中多次 UPDATE
UPDATE request_logs SET prompt_tokens = 100 WHERE request_id = 'xxx';
UPDATE request_logs SET completion_tokens = 50 WHERE request_id = 'xxx';
UPDATE request_logs SET success = true WHERE request_id = 'xxx';

// 3. 偶尔需要 UPSERT（重试场景）
INSERT INTO request_logs (...) VALUES (...)
ON CONFLICT (request_id, ts) DO UPDATE SET ...;
```

**如果当月分区是 columnar**：
- ✅ 第 1 步：INSERT 成功
- ❌ 第 2 步：UPDATE 失败（columnar 不支持）
- ❌ 第 3 步：UPSERT 失败（columnar 不支持 ON CONFLICT）

---

## 3. 分区路由的复杂性

### 3.1 PostgreSQL 自动路由

当代码写入父表时，PostgreSQL 会根据分区键（`ts`）自动路由到对应分区：

```sql
-- 代码写入父表
INSERT INTO request_logs (request_id, ts, ...) 
VALUES ('xxx', '2026-07-04 12:00:00', ...);

-- PostgreSQL 自动路由
→ ts='2026-07-04' 在 [2026-07-01, 2026-08-01) 范围内
→ 路由到 request_logs_2026_07 分区
→ 如果 2026_07 是 columnar → INSERT 成功，但后续 UPDATE 失败
```

**问题**：
- 我们无法控制 PostgreSQL 的自动路由
- 当月分区必须保持 heap（不能转 columnar）
- 这限制了历史归档的优化空间

### 3.2 DEFAULT 分区约束

PostgreSQL 的 DEFAULT 分区有特殊约束：

```sql
-- DEFAULT 分区只接受不在其他分区范围内的数据
-- 如果 2026_07 分区 ATTACHED，范围是 [2026-07-01, 2026-08-01)
-- 那么 DEFAULT 分区会自动排除这个范围

-- 尝试直接写 default
INSERT INTO request_logs_default (request_id, ts, ...)
VALUES ('xxx', '2026-07-04 12:00:00', ...);
-- ERROR: new row for relation "request_logs_default" violates partition constraint
-- (SQLSTATE 23514)
```

**关键发现**：
- DEFAULT 分区约束是**动态的**（随其他分区 ATTACH/DETACH 变化）
- 当月分区 ATTACHED 时，无法直接写 default
- 这限制了"所有新数据写 default"的简单方案

---

## 4. 已有方案的局限性

### 4.1 方案 A：写父表 + 月度分区保持 heap ❌

```go
INSERT INTO request_logs (...) VALUES (...);  // 写父表
```

**优点**：
- ✅ 简单：依赖 PostgreSQL 自动路由
- ✅ 完整查询：SELECT 父表自动聚合所有分区

**缺点**：
- ❌ **放弃 columnar 压缩**：当月及未来月份必须保持 heap
- ❌ **存储成本高**：无法利用 columnar 的 70%+ 压缩比

**结论**：不符合长期归档优化目标

---

### 4.2 方案 B：写 default + DETACH 当月分区 ⚠️

```go
INSERT INTO request_logs_default (...) VALUES (...);  // 硬编码 default
```

**优点**：
- ✅ 简单：代码硬编码表名
- ✅ 历史分区可以转 columnar

**缺点**：
- ❌ **查询需要 UNION**：父表查询不包含 DETACHED 分区
- ❌ **查询复杂度增加**：所有查询代码需要改造

**结论**：查询层改造成本高

---

### 4.3 方案 C：动态路由（完整版）⚠️

```go
// 根据 ts 年龄动态选择表
age := time.Since(ts)
if age < 7*24*time.Hour {
    targetTable = "request_logs_default"  // 热数据
} else {
    targetTable = fmt.Sprintf("request_logs_%s", ts.Format("2006_01"))  // 冷数据
}

sql := fmt.Sprintf("INSERT INTO %s (...) VALUES (...)", targetTable)
```

**优点**：
- ✅ 热数据集中：default 只有 7 天，查询快
- ✅ 历史分区可以转 columnar

**缺点**：
- ❌ **代码复杂度高**：所有写入都需要动态 SQL
- ❌ **开发成本**：10+ 小时开发 + 测试
- ❌ **查询仍需 UNION**：当月分区 DETACHED

**结论**：成本过高，收益有限

---

## 5. 关键洞察

### 5.1 写入模式分析

通过分析实际业务场景，我们发现：

| 写入类型 | 占比 | ts 特征 | 目标表 |
|---------|------|---------|--------|
| 新请求 INSERT | 99.9% | `ts = now()` | 肯定是热数据（< 7天） |
| 流式 UPDATE | 99.9% | 更新几秒前的记录 | 肯定在 default |
| 历史补录 | < 0.1% | `ts` 可能 > 7天 | 需要动态路由 |

**结论**：
- **99.9% 的写入是新数据（热数据）**
- 无需为 < 0.1% 的历史补录增加所有写入的复杂度

### 5.2 方案简化机会

基于上述分析，**方案 C 可以简化为**：

```go
// 99.9% 场景：硬编码 default（简单）
INSERT INTO request_logs_default (...) VALUES (...);

// 0.1% 场景：使用路由器（按需）
if isHistoricalBackfill {
    targetTable := partitionRouter.getRequestLogsTable(ts)
    sql := fmt.Sprintf("INSERT INTO %s (...)", targetTable)
}
```

**效果**：
- ✅ 主路径简单（硬编码）
- ✅ 历史补录有工具支持（路由器）
- ✅ 开发成本降低 95%（45 分钟 vs 10+ 小时）

---

## 6. 最终方案：方案 C 简化版

### 6.1 核心设计

**所有新数据 → `*_default`（硬编码）+ 月度分区 DETACHED + 定期迁移**

```
架构：
┌─────────────────────────────────────────────────────┐
│ 应用层写入                                           │
├─────────────────────────────────────────────────────┤
│ INSERT INTO request_logs_default (...) VALUES (...)  │  ← 硬编码（99.9% 场景）
│ UPDATE request_logs_default SET ... WHERE ...        │
└─────────────────────────────────────────────────────┘
                      ↓
┌─────────────────────────────────────────────────────┐
│ request_logs_default (heap, 所有新数据)              │
│ - 支持 UPSERT                                        │
│ - 热数据集中                                         │
└─────────────────────────────────────────────────────┘
                      ↓ 每日迁移（7天前数据）
┌─────────────────────────────────────────────────────┐
│ request_logs_2026_07 (heap, DETACHED)               │
│ - 当月完整数据                                       │
│ - 查询需要 UNION                                     │
└─────────────────────────────────────────────────────┘
                      ↓ 月底转换
┌─────────────────────────────────────────────────────┐
│ request_logs_2026_07 (columnar, ATTACHED)           │
│ - 历史归档                                           │
│ - 压缩比 70%+                                        │
│ - 只读                                               │
└─────────────────────────────────────────────────────┘
```

### 6.2 分区状态

```
request_logs (父表)
├─ request_logs_2026_06 [ATTACHED, columnar] ─ 历史归档
├─ request_logs_2026_07 [DETACHED, heap]     ─ 当月数据
├─ request_logs_2026_08 [DETACHED, heap]     ─ 下月预创建
└─ request_logs_default [ATTACHED, heap]     ─ 所有新数据
```

### 6.3 数据流

```
1. 新请求 → request_logs_default (硬编码)
2. 每日迁移 → 7天前数据迁移到 request_logs_2026_07
3. 月底转换 → request_logs_2026_07 转 columnar + ATTACH
```

---

## 7. 技术决策

### 7.1 为什么不需要动态路由？

| 场景 | 写入时间 | 目标表 | 判断依据 |
|------|---------|--------|----------|
| 新请求 INSERT | `ts = now()` | default | 肯定是热数据（< 7天） |
| 流式 UPDATE | 几秒前 | default | 更新刚 INSERT 的记录 |
| 历史补录 | 可能 > 7天 | 使用路由器 | 罕见场景，按需处理 |

**结论**：99.9% 的写入不需要动态路由。

### 7.2 为什么 DETACH 当月分区？

| 方案 | 当月分区状态 | 写入方式 | 查询方式 |
|------|-------------|---------|---------|
| ATTACHED | request_logs_2026_07 | ❌ 无法直接写 default | ✅ 父表自动聚合 |
| DETACHED | request_logs_2026_07 | ✅ 可以直接写 default | ⚠️ 需要 UNION |

**权衡**：
- **写入优先**：硬编码 default（简单）
- **查询成本**：创建 VIEW 封装 UNION（一次性改造）

**结论**：DETACH 是最优解。

### 7.3 为什么预留路由器？

虽然当前不使用动态路由，但我们实现了 `partition_router.go`：

```go
type partitionRouter struct {
    hotDataWindow time.Duration  // 7 days
}

func (r *partitionRouter) getRequestLogsTable(ts time.Time) string {
    age := time.Since(ts)
    if age < r.hotDataWindow {
        return "request_logs_default"  // 热数据
    }
    month := ts.Format("2006_01")
    return fmt.Sprintf("request_logs_%s", month)  // 冷数据
}
```

**用途**：
1. **历史补录工具**：批量补录时使用路由器
2. **未来扩展**：如果业务场景变化，可以快速启用
3. **测试覆盖**：确保路由逻辑正确

---

## 8. 实施效果

### 8.1 成本对比

| 方案 | 开发时间 | 代码复杂度 | 查询改造 | 存储优化 |
|------|---------|-----------|---------|---------|
| 方案 A | 30 分钟 | 低 | 不需要 | ❌ 无法 columnar |
| 方案 B | 1 小时 | 低 | 高（所有查询） | ✅ 支持 columnar |
| 方案 C 完整版 | 10+ 小时 | 高 | 高（所有查询） | ✅ 支持 columnar |
| **方案 C 简化版** | **45 分钟** | **低** | **中（VIEW 封装）** | **✅ 支持 columnar** |

### 8.2 线上验证

**测试结果**（2026-07-04）：
- ✅ 新数据写入：request_logs_default
- ✅ 分区隔离：request_logs_2026_07 无新写入
- ✅ 查询聚合：UNION 包含 6355 行（vs 父表 4 行）

---

## 9. 后续工作

### 9.1 短期（本周）
- [ ] 创建每日迁移脚本（7天前数据 → 月度分区）
- [ ] 创建查询 VIEW（封装 UNION 逻辑）
- [ ] 配置监控告警（default 表大小 > 10GB）

### 9.2 中期（8月1日前）
- [ ] 月底转 columnar 脚本（2026_07 heap → columnar）
- [ ] 更新 admin 查询代码使用 VIEW

### 9.3 长期
- [ ] 同步到 184 环境（71 稳定运行 1 周后）

---

## 10. 参考资料

- [PostgreSQL 分区表文档](https://www.postgresql.org/docs/15/ddl-partitioning.html)
- [Citus Columnar 扩展](https://docs.citusdata.com/en/stable/admin_guide/table_management.html#columnar-storage)
- [项目内部文档] `deploy/sql/migrations/999_columnar_backfill_and_enforce.sql`
- [项目内部文档] `DEPLOYMENT_LESSONS_LEARNED.md`
