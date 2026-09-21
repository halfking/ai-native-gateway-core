# llm_hourly_stats 时间戳格式修复指南

## 问题概述

**日期**: 2026-09-06
**影响**: 外部 redclaw 服务向 `llm_hourly_stats` 表插入数据时失败

### 错误现象
```
ERROR:  invalid input syntax for type timestamp with time zone: "2026-09-05T23"
STATEMENT:  INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
            VALUES ($1, $2, $3, $4, $5)
            ON CONFLICT (hour) DO UPDATE SET ...
```

### 根本原因
外部服务发送的时间戳格式为截断格式 `"2026-09-05T23"` (只到小时,无分钟/秒/时区)，但 PostgreSQL 的 `TIMESTAMPTZ` 类型要求完整格式如 `"2026-09-05T23:00:00+00:00"`。

---

## 解决方案

数据库已部署兼容层(迁移 665 + 667),外部服务需要选择以下任一方案:

### ✅ 推荐方案 1: 使用 normalize_hour_timestamp() 包装器

**适用于**: 需要保留 `ON CONFLICT` 逻辑的场景

**修改前**:
```sql
INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (hour) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  failure_count = EXCLUDED.failure_count,
  total_count = EXCLUDED.total_count,
  total_cost = EXCLUDED.total_cost,
  updated_at = NOW();
```

**修改后**:
```sql
INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
VALUES (normalize_hour_timestamp($1), $2, $3, $4, $5)
ON CONFLICT (hour) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  failure_count = EXCLUDED.failure_count,
  total_count = EXCLUDED.total_count,
  total_cost = EXCLUDED.total_cost,
  updated_at = NOW();
```

**变更点**: 仅在 `VALUES` 子句的第一个参数外包装 `normalize_hour_timestamp()` 函数。

**支持的输入格式**:
- `"2026-09-05T23"` (截断格式)
- `"2026-09-05 23"` (空格分隔)
- `"2026-09-05T23:00:00+00:00"` (完整 ISO8601)
- `"2026-09-05T23:00:00Z"` (UTC 标记)

---

### ✅ 推荐方案 2: 调用 upsert_llm_hourly_stats() 存储过程

**适用于**: 愿意使用存储过程的场景

**修改后**:
```sql
SELECT upsert_llm_hourly_stats(
  '2026-09-05T23',  -- hour_text (支持灵活格式)
  100,               -- success_count
  5,                 -- failure_count
  105,               -- total_count
  1.23               -- total_cost
);
```

**优点**:
- 无需修改现有逻辑,直接调用一个函数
- 自动处理 `ON CONFLICT` 逻辑
- 接受所有灵活格式

---

### 方案 3: 修复外部服务的时间戳格式化代码

**适用于**: 愿意修改客户端代码的场景

**目标**: 发送完整的 ISO8601 时间戳

**示例 (各语言)**:

**Python**:
```python
from datetime import datetime, timezone

hour = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
hour_str = hour.isoformat()  # "2026-09-05T23:00:00+00:00"
```

**Go**:
```go
import "time"

hour := time.Now().UTC().Truncate(time.Hour)
hourStr := hour.Format(time.RFC3339)  // "2026-09-05T23:00:00Z"
```

**JavaScript/TypeScript**:
```typescript
const hour = new Date();
hour.setMinutes(0, 0, 0);
const hourStr = hour.toISOString();  // "2026-09-05T23:00:00.000Z"
```

**Java**:
```java
import java.time.ZonedDateTime;
import java.time.temporal.ChronoUnit;

ZonedDateTime hour = ZonedDateTime.now().truncatedTo(ChronoUnit.HOURS);
String hourStr = hour.toString();  // "2026-09-05T23:00:00Z"
```

---

## 数据库兼容层详情

### 已部署的函数

#### 1. `normalize_hour_timestamp(TEXT) -> TIMESTAMPTZ`
将灵活格式的小时字符串标准化为 `TIMESTAMPTZ`。

```sql
SELECT normalize_hour_timestamp('2026-09-05T23');
-- 返回: 2026-09-05 23:00:00+00
```

#### 2. `upsert_llm_hourly_stats(TEXT, INT, INT, INT, NUMERIC) -> VOID`
安全的插入/更新函数,接受灵活格式。

```sql
SELECT upsert_llm_hourly_stats('2026-09-05T23', 100, 5, 105, 1.23);
-- 自动处理 INSERT + ON CONFLICT UPDATE
```

#### 3. `upsert_llm_hourly_stats_batch(JSONB) -> INTEGER`
批量插入/更新。

```sql
SELECT upsert_llm_hourly_stats_batch('[
  {"hour":"2026-09-05T23","success_count":100,"failure_count":5,"total_count":105,"total_cost":1.23},
  {"hour":"2026-09-06T00","success_count":200,"failure_count":10,"total_count":210,"total_cost":2.50}
]'::jsonb);
-- 返回: 2 (插入/更新的行数)
```

---

## 使用指南快速查询

在数据库中执行:
```sql
SELECT * FROM llm_hourly_stats_usage_guide;
```

将返回所有可用方案的示例代码。

---

## 验证测试

### 测试 1: 直接插入截断格式
```sql
INSERT INTO llm_hourly_stats (hour, success_count, failure_count, total_count, total_cost)
VALUES (normalize_hour_timestamp('2026-09-06T15'), 100, 5, 105, 1.50)
ON CONFLICT (hour) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  total_count = EXCLUDED.total_count;
```

### 测试 2: 调用存储过程
```sql
SELECT upsert_llm_hourly_stats('2026-09-06T15', 200, 10, 210, 3.00);
```

### 测试 3: 查询结果
```sql
SELECT * FROM llm_hourly_stats WHERE hour = '2026-09-06 15:00:00+00';
```

预期:
- `hour`: `2026-09-06 15:00:00+00`
- `success_count`: `200`
- `total_count`: `210`

---

## FAQ

### Q1: 为什么不能直接修改表结构接受 TEXT?
**A**: PostgreSQL 表的主键必须是明确的类型。`TIMESTAMPTZ` 提供时区处理、索引优化和数据完整性保证。改为 TEXT 会失去这些优势。

### Q2: 为什么不能用 BEFORE 触发器自动转换?
**A**: PostgreSQL 在触发器执行之前先进行类型验证。截断格式无法通过 `TIMESTAMPTZ` 的类型检查,触发器根本不会被调用。

### Q3: 如果忘记使用 normalize_hour_timestamp() 会怎样?
**A**: 插入会失败,返回 `22007: invalid input syntax for type timestamp with time zone` 错误。

### Q4: 老的完整格式时间戳还能用吗?
**A**: 可以。`normalize_hour_timestamp()` 会检测输入格式:
- 如果是完整格式,直接转换为 `TIMESTAMPTZ`
- 如果是截断格式,自动补全为完整格式

### Q5: 性能影响如何?
**A**: `normalize_hour_timestamp()` 标记为 `IMMUTABLE`,PostgreSQL 可以缓存结果。对于常量输入(如参数化查询),性能影响可忽略。

---

## 联系与支持

- **迁移文件**: `sql/migrations/startup/667_llm_hourly_stats_timestamp_fix.sql` + `668_llm_hourly_stats_final_fix.sql`
- **详细分析**: `docs/2026-09-06-pg-missing-tables-fix.md`
- **部署脚本**: `scripts/apply-db-revision-sequence.sh`

**建议**: 优先选择方案 1 (normalize_hour_timestamp 包装器),改动最小且支持所有现有逻辑。
