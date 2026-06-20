# 数据生命周期管理方案

> 版本: v1.0  
> 日期: 2026-06-20  
> 范围: request_logs 表的清理、归档、备份策略

## 📊 数据分级策略

### 三温数据模型

| 级别 | 时间范围 | 保留策略 | 存储位置 | 用途 |
|------|----------|----------|----------|------|
| **热数据** | 最近 7 天 | 在线全量保留 | PostgreSQL 主表 | 实时查询、压缩分析、故障排查 |
| **温数据** | 7-30 天 | 在线保留（可选裁剪大字段） | PostgreSQL 主表 | 历史分析、趋势对比 |
| **冷数据** | 30-90 天 | 归档到压缩文件 | 文件系统 (parquet/jsonl.gz) | 合规审计、长期分析 |
| **过期数据** | 90 天以上 | 删除或冷备份 | 可选：S3/OSS 冷存储 | 法务要求保留 |

### 字段裁剪策略（温数据优化）

对于 7-30 天的数据，可选裁剪以下大字段：
- `request_body` → 保留前 1KB + `"...truncated"` 标记
- `response_body` → 保留前 1KB
- `outbound_body` → 保留前 1KB
- 保留所有 metadata 字段（用于统计分析）

## 🗄️ 归档方案

### 方案 A：Parquet 列式存储（推荐）

**优点**：
- 高压缩比（5-10x）
- 列式存储，适合分析查询
- 支持 schema evolution
- 可用 DuckDB/Pandas 直接查询

**实现**：
```bash
# 1. 导出 30-90 天数据为 Parquet
PGPASSWORD=xxx psql -h 172.31.0.4 -U stockuser -d llm_gateway -c \
  "COPY (SELECT * FROM request_logs WHERE ts BETWEEN '2026-03-01' AND '2026-04-01') 
   TO STDOUT WITH (FORMAT binary)" | \
  python3 scripts/pg_to_parquet.py > archive/request_logs_2026-03.parquet

# 2. 验证行数
python3 -c "import pyarrow.parquet as pq; print(pq.read_table('archive/request_logs_2026-03.parquet').num_rows)"

# 3. 删除已归档数据
psql ... -c "DELETE FROM request_logs WHERE ts BETWEEN '2026-03-01' AND '2026-04-01'"
```

### 方案 B：JSONL.GZ（简单通用）

**优点**：
- 无外部依赖
- 行式存储，易于恢复单条记录
- 工具链成熟（jq/gzip）

**实现**：
```bash
# 1. 导出为 JSONL
psql ... -c "COPY (SELECT row_to_json(t) FROM request_logs t WHERE ts BETWEEN ...) TO STDOUT" | \
  gzip -9 > archive/request_logs_2026-03.jsonl.gz

# 2. 查询归档数据
zcat archive/request_logs_2026-03.jsonl.gz | jq 'select(.gw_session_id == "xxx")'
```

### 方案 C：PostgreSQL COPY TO (SQL 转储)

**优点**：
- PostgreSQL 原生，恢复简单
- 保留所有 PG 类型信息

**实现**：
```bash
# 1. 导出
pg_dump -h 172.31.0.4 -U stockuser -d llm_gateway \
  --table=request_logs \
  --data-only \
  --where="ts BETWEEN '2026-03-01' AND '2026-04-01'" \
  | gzip -9 > archive/request_logs_2026-03.sql.gz

# 2. 恢复
zcat archive/request_logs_2026-03.sql.gz | psql ...
```

## 🧹 清理策略

### 自动清理任务（推荐）

**定时任务**：每天凌晨 2:00 执行

```bash
# crontab -e
0 2 * * * /opt/llm-gateway-go/scripts/cleanup-request-logs.sh >> /var/log/llm-gateway-cleanup.log 2>&1
```

**清理逻辑**：
1. **裁剪温数据**（7-30 天）：裁剪大字段为 1KB
2. **归档冷数据**（30-90 天）：导出 Parquet + 删除
3. **删除过期数据**（90 天以上）：直接删除或移到冷备份

### 手动清理命令

```bash
# 查看各时间段数据量
./scripts/analyze-request-logs-size.sh

# 清理指定时间段（30-90天数据归档）
./scripts/archive-request-logs.sh --from 2026-03-01 --to 2026-04-01

# 删除 90 天以上数据（危险操作，需要确认）
./scripts/delete-old-request-logs.sh --older-than 90 --dry-run
./scripts/delete-old-request-logs.sh --older-than 90 --confirm
```

## 💾 备份方案

### 热备份（增量备份，每天）

```bash
# 使用 pg_dump 增量备份最近 7 天数据
pg_dump -h 172.31.0.4 -U stockuser -d llm_gateway \
  --table=request_logs \
  --data-only \
  --where="ts > NOW() - INTERVAL '7 days'" \
  | gzip -9 > /backup/incremental/request_logs_$(date +%Y%m%d).sql.gz

# 保留最近 14 天的增量备份
find /backup/incremental/ -name "request_logs_*.sql.gz" -mtime +14 -delete
```

### 全量备份（每周日）

```bash
# 全量备份整个 request_logs 表
pg_dump -h 172.31.0.4 -U stockuser -d llm_gateway \
  --table=request_logs \
  --data-only \
  | gzip -9 > /backup/full/request_logs_full_$(date +%Y%m%d).sql.gz

# 保留最近 4 周的全量备份
find /backup/full/ -name "request_logs_full_*.sql.gz" -mtime +28 -delete
```

### 冷备份（归档数据长期保存）

```bash
# 上传归档文件到 OSS/S3（可选）
aws s3 cp archive/request_logs_2026-03.parquet \
  s3://kx-llm-gateway-archive/request_logs/2026/03/ \
  --storage-class GLACIER
```

## 📈 管理界面

### 新增管理页面：`/admin/data-lifecycle`

**功能**：
1. **数据统计面板**
   - 总行数、总大小
   - 按时间段分布（24h/7d/30d/30-90d/>90d）
   - 增长趋势图
   - 预估清理后节省空间

2. **清理操作**
   - 裁剪温数据（7-30天大字段）
   - 归档冷数据（30-90天）
   - 删除过期数据（>90天）
   - 干跑模式（预览影响）

3. **归档记录**
   - 归档历史列表
   - 下载归档文件
   - 恢复归档数据

4. **备份管理**
   - 最近备份列表
   - 验证备份完整性
   - 手动触发备份

### API 端点

```go
// 数据统计
GET /api/admin/data-lifecycle/stats
{
  "total_rows": 15000000,
  "total_size_bytes": 52428800000,  // 50GB
  "hot_data": { "rows": 500000, "size_bytes": 2147483648, "days": 7 },
  "warm_data": { "rows": 1500000, "size_bytes": 6442450944, "days": 23 },
  "cold_data": { "rows": 5000000, "size_bytes": 15032385536, "days": 60 },
  "expired_data": { "rows": 8000000, "size_bytes": 28806324224, "days": 150 }
}

// 清理预览（dry-run）
POST /api/admin/data-lifecycle/cleanup/preview
{
  "action": "archive",      // "trim" | "archive" | "delete"
  "from": "2026-03-01",
  "to": "2026-04-01"
}
→ { "affected_rows": 1234567, "estimated_freed_bytes": 4294967296 }

// 执行清理
POST /api/admin/data-lifecycle/cleanup/execute
{
  "action": "archive",
  "from": "2026-03-01",
  "to": "2026-04-01",
  "confirm_token": "xxx"    // 防止误操作
}

// 归档列表
GET /api/admin/data-lifecycle/archives
→ [
  {
    "filename": "request_logs_2026-03.parquet",
    "date_range": "2026-03-01 to 2026-04-01",
    "rows": 1234567,
    "size_bytes": 524288000,
    "created_at": "2026-04-02T02:00:00Z"
  }
]

// 下载归档文件
GET /api/admin/data-lifecycle/archives/:filename/download

// 恢复归档数据（危险操作）
POST /api/admin/data-lifecycle/archives/:filename/restore
```

## 🔒 安全考虑

### 权限控制

- 数据统计：`platform_ops` 或 `super_admin`
- 清理预览：`platform_ops` 或 `super_admin`
- 执行清理：**仅 `super_admin` + 二次确认**
- 恢复数据：**仅 `super_admin` + 审计日志**

### 审计日志

所有清理/归档/恢复操作必须记录到 `data_lifecycle_audit` 表：

```sql
CREATE TABLE data_lifecycle_audit (
    id SERIAL PRIMARY KEY,
    operation VARCHAR(50) NOT NULL,  -- 'trim' | 'archive' | 'delete' | 'restore'
    operator_user VARCHAR(255) NOT NULL,
    date_range_from TIMESTAMPTZ,
    date_range_to TIMESTAMPTZ,
    affected_rows BIGINT,
    freed_bytes BIGINT,
    archive_filename VARCHAR(500),
    dry_run BOOLEAN DEFAULT FALSE,
    executed_at TIMESTAMPTZ DEFAULT NOW(),
    execution_duration_ms INT,
    status VARCHAR(50),  -- 'success' | 'failed' | 'partial'
    error_message TEXT
);
```

## 📋 实施清单

### Phase 1: 数据分析 + 手工脚本（1天）

- [x] `scripts/analyze-request-logs-size.sh` — 数据量统计
- [ ] `scripts/archive-request-logs.sh` — 归档脚本（Parquet）
- [ ] `scripts/delete-old-request-logs.sh` — 删除脚本（带 dry-run）
- [ ] `scripts/backup-request-logs.sh` — 备份脚本
- [ ] 测试：归档 → 验证行数 → 删除 → 恢复验证

### Phase 2: 自动化任务（1天）

- [ ] `scripts/cleanup-request-logs.sh` — 定时清理主脚本
- [ ] crontab 配置 + 日志轮转
- [ ] Prometheus metrics 暴露（清理进度、归档大小）
- [ ] 告警规则（清理失败、磁盘空间不足）

### Phase 3: 管理 API（2天）

- [ ] `admin/data_lifecycle.go` — 统计/预览/执行 API
- [ ] `admin/data_lifecycle_audit.go` — 审计日志
- [ ] SQL migration — `data_lifecycle_audit` 表
- [ ] 权限检查 + 二次确认 token

### Phase 4: 管理界面（2天）

- [ ] `web/src/views/DataLifecycleView.vue` — 管理页面
- [ ] 统计面板 + 图表
- [ ] 清理操作 UI + 进度反馈
- [ ] 归档列表 + 下载链接

### Phase 5: 测试 + 文档（1天）

- [ ] 端到端测试（预演清理流程）
- [ ] 运维手册（runbook）
- [ ] 故障恢复预案

## 🚀 快速开始（最小可行方案）

如果需要立即清理数据，可以先用以下手工命令：

```bash
# 1. 查看数据量
psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "
SELECT 
    COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days') AS hot,
    COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days') AS warm,
    COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days') AS cold,
    COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days') AS expired,
    pg_size_pretty(pg_total_relation_size('request_logs')) AS total_size
FROM request_logs;
"

# 2. 备份 30-90 天数据（归档前先备份）
pg_dump -h 172.31.0.4 -U stockuser -d llm_gateway \
  --table=request_logs \
  --data-only \
  --where="ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days'" \
  | gzip -9 > /backup/archive_$(date +%Y%m%d).sql.gz

# 3. 删除 90 天以上数据（谨慎操作！）
psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "
-- 先统计影响行数
SELECT COUNT(*) AS will_delete FROM request_logs WHERE ts < NOW() - INTERVAL '90 days';

-- 确认后执行删除
-- DELETE FROM request_logs WHERE ts < NOW() - INTERVAL '90 days';
"

# 4. VACUUM 回收空间
psql -h 172.31.0.4 -U stockuser -d llm_gateway -c "VACUUM FULL ANALYZE request_logs;"
```

## 📞 联系与支持

- 运维文档：`docs/operations/data-lifecycle-runbook.md`
- 故障恢复：`docs/operations/data-recovery-playbook.md`
- 监控面板：Grafana → LLM Gateway → Data Lifecycle
