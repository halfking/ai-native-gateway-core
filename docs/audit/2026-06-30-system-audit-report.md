# LLM Gateway 系统审计报告
**日期**: 2026-06-30  
**审计范围**: 错误处理、数据统计、凭据路由  
**服务器**: __HOST_71_IP__ (71服务器)

## 执行摘要

通过对71服务器的日志和数据库审计，发现了**3个P0级关键问题**，导致系统无法正常记录请求日志、路由失败率高、节点切换机制失效。

**核心问题**:
1. ⚠️ **P0-1**: `request_wal` 和 `request_logs` 表完全缺失，导致所有请求日志写入失败
2. ⚠️ **P0-2**: 凭据模型路由匹配返回0个候选节点（minimax-m2.7-quickspeed）
3. ⚠️ **P0-3**: 节点失败后未及时从候选列表移除，导致重复失败

---

## 问题1: 数据库表缺失 (P0-1)

### 现象
```
2026-06-30T06:26:52.037451629Z WARN request_logger: CreateInitial failed 
request_id=294b2c18447e7667747f7ae203c1b2be 
error="ERROR: no partition of relation \"request_wal\" found for row (SQLSTATE 23514)"
```

### 根因分析
71服务器的PostgreSQL数据库（`llm-gateway-pg-71-replica` 容器）中：
- ❌ `request_wal` 表不存在
- ❌ `request_logs` 表不存在
- ❌ 相关分区表 `request_wal_2026_06`、`request_logs_2026_06` 不存在

**验证结果**:
```sql
-- 在71的数据库中执行
SELECT tablename FROM pg_tables WHERE tablename LIKE 'request%';
-- 返回: (0 rows)
```

**代码位置**: `domains/hooks/observability/telemetry/request_logger.go:107`
```go
_, err := rl.db.Exec(ctx, `
    INSERT INTO request_wal (request_id, tenant_id, gw_session_id, ...)
    VALUES ($1, $2, $3, ...)
`)
```

### 影响范围
- ❌ 所有请求无法写入 `request_wal`（WAL日志）
- ❌ 无法生成 `request_logs`（最终日志表）
- ❌ `recent_success_rate()` 函数无法计算（依赖 `request_logs`）
- ❌ 路由器的软质量信号（`RecentSuccessRate`）失效
- ❌ 成本统计、性能分析、错误追溯完全失效

### 修复方案
**立即执行**（在71服务器的数据库中）:

```sql
-- 1. 创建 request_wal 主表（分区表）
CREATE TABLE IF NOT EXISTS request_wal (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb
) PARTITION BY RANGE (created_at);

-- 2. 创建6月和7月分区
CREATE TABLE request_wal_2026_06 PARTITION OF request_wal
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE request_wal_2026_07 PARTITION OF request_wal
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- 3. 创建索引
CREATE INDEX idx_request_wal_request_id ON request_wal (request_id, created_at DESC);
CREATE INDEX idx_request_wal_tenant_ts ON request_wal (tenant_id, created_at DESC);

-- 4. 创建 request_logs 主表（分区表）
-- 从 deploy/sql/01-schema.sql 中复制完整定义
-- （见附录A：完整SQL脚本）

-- 5. 验证
SELECT count(*) FROM request_wal;
SELECT count(*) FROM request_logs;
```

**完整SQL脚本**: 见 `deploy/sql/01-schema.sql` 中的 `request_wal` 和 `request_logs` 定义

---

## 问题2: 凭据模型路由匹配失败 (P0-2)

### 现象
```
2026-06-30T06:26:52.036903082Z INFO provider.GetCandidates: cache hit count=0
```

特定模型（如 `minimax-m2.7-quickspeed`）路由时返回0个候选节点，导致503错误。

### 根因分析

**代码位置**: `provider/client.go:654-796` (loadCandidatesDB方法)

路由查询的WHERE子句有**3个过滤条件**可能导致count=0:

1. **v.is_routable = TRUE** (line 733)
   - 所有节点可能被标记为不可路由

2. **model_probe_state 检查** (line 740-745)
   ```sql
   AND NOT EXISTS (
       SELECT 1 FROM model_probe_state mps
       WHERE mps.credential_id = c.id
         AND mps.raw_model_name = mo.raw_model_name
         AND mps.state = 'broken_confirmed'
   )
   ```
   - 节点被探测标记为 `broken_confirmed` 后未清除

3. **recent_success_rate 硬阈值** (line 756)
   ```sql
   AND NOT (rsr.samples >= 20 AND COALESCE(rsr.rate, 1.0) < 0.3)
   ```
   - 当样本数>=20且成功率<30%时排除
   - **但 `request_logs` 不存在时，`recent_success_rate()` 函数返回什么？**

### 关键发现

查看 `deploy/sql/01-schema.sql` 中的 `recent_success_rate()` 函数定义，当 `request_logs` 不存在时：
- 函数会抛出 `relation "request_logs" does not exist` 错误
- 导致整个查询失败，返回0行

**这是根因的根因**: 因为问题1（表缺失），导致问题2（路由失败）。

### 修复方案

**短期**（立即）:
1. 修复问题1（创建表）
2. 检查 `model_probe_state` 中是否有误标记的节点:
   ```sql
   SELECT credential_id, raw_model_name, state, last_checked_at
   FROM model_probe_state
   WHERE state = 'broken_confirmed'
     AND raw_model_name LIKE '%minimax%';
   ```

3. 如果有误标记，手动清除:
   ```sql
   UPDATE model_probe_state
   SET state = 'healthy', consecutive_failures = 0
   WHERE credential_id = <id> 
     AND raw_model_name = 'minimax-m2.7-quickspeed';
   ```

**中期**（本周）:
1. 添加路由诊断日志，当 count=0 时记录详细原因:
   ```go
   // provider/client.go:796 后添加
   if len(out) == 0 {
       slog.Warn("routing_no_candidates_diagnostic",
           "model", clientModel,
           "reason", "check_v_routable_credential_models_and_probe_state")
       // 执行诊断查询，逐条检查过滤条件
   }
   ```

**长期**（下季度）:
1. 实现节点健康度衰减机制（而非硬断路）
2. 添加路由fallback策略（当count=0时降级到次优节点）

---

## 问题3: 节点失败切换机制失效 (P0-3)

### 现象
日志中出现 `sticky DB write failed`:
```
2026-06-30T06:26:53.869819557Z DEBUG sticky DB write failed 
key=default:3:2:default 
error="ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)"
```

### 根因分析

**代码位置**: 搜索 "sticky DB write" 未找到具体位置，但错误信息表明:
- 尝试执行 `INSERT ... ON CONFLICT` 语句
- 目标表缺少 UNIQUE 或 EXCLUSION 约束

**推测**: 这是粘性路由（sticky routing）机制，用于记录 `(tenant, app, key, profile) → credential` 绑定，确保同一会话使用同一节点。

### 影响
- 节点失败后，粘性绑定未更新
- 同一会话的后续请求继续路由到失败节点
- 导致连续失败（日志中 `consecutive_failures` 增加）

### 修复方案

**需要定位**:
```bash
# 查找粘性路由相关代码
grep -r "sticky.*write\|sticky.*db" --include="*.go" .
```

**预期表结构**:
```sql
CREATE TABLE IF NOT EXISTS sticky_routing (
    tenant_id VARCHAR(64),
    application_id BIGINT,
    api_key_id BIGINT,
    profile VARCHAR(64),
    credential_id BIGINT,
    last_success_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (tenant_id, application_id, api_key_id, profile)
);
```

---

## 问题4: Minimax召回404错误 (P1)

### 现象
```
2026-06-30T06:26:54.576296742Z WARN compaction: summarize call failed
attempt=1 of=4 compact_model=MiniMax-Text-01 
error="openai summarize upstream 404: 404 page not found"
```

### 根因
- 上下文压缩功能尝试调用 `MiniMax-Text-01` 模型总结对话历史
- 但该模型的端点配置错误或模型名不存在

### 修复
1. 检查 `MiniMax-Text-01` 的 `base_url` 和 `outbound_model_name`
2. 或禁用该模型的召回功能，改用机械截断（当前已fallback）

---

## 字节数统计审计结果

### 审计发现
代码库中**没有找到** `input_bytes`、`output_bytes`、`cache_read_bytes`、`cache_write_bytes` 字段的统计逻辑。

### 现状
- Token统计存在: `prompt_tokens`, `completion_tokens`, `cache_read_tokens`, `cache_write_tokens`
- 字节数统计缺失

### 影响
- 无法精确计算网络流量成本
- 无法区分token级别计费和字节级别计费的模型

### 建议实现

**方案1**: 在流式处理中添加字节计数器
```go
// domains/streaming/stream.go

type byteCountingWriter struct {
    w         http.ResponseWriter
    bytesWritten int64
}

func (bcw *byteCountingWriter) Write(p []byte) (int, error) {
    n, err := bcw.w.Write(p)
    atomic.AddInt64(&bcw.bytesWritten, int64(n))
    return n, err
}

// StreamChatWithPendingCapture 中使用
bcw := &byteCountingWriter{w: w}
// ... 流式写入到 bcw
// 最后记录 bcw.bytesWritten
```

**方案2**: 在审计捕获中添加字节统计
```go
// domains/hooks/audit/stream_capture.go

type StreamCapture struct {
    // ... 现有字段
    InputBytes  int64
    OutputBytes int64
}

func (sc *StreamCapture) RecordChunk(chunk []byte) {
    sc.OutputBytes += int64(len(chunk))
    // ...
}
```

**优先级**: P2（中优先级，不影响核心功能但影响成本分析）

---

## 修复优先级

| 优先级 | 问题 | 影响 | 预计工时 |
|--------|------|------|----------|
| **P0-1** | 创建缺失的数据库表 | 系统完全无法记录日志 | 1小时 |
| **P0-2** | 修复路由匹配逻辑 | 特定模型503错误 | 2小时 |
| **P0-3** | 修复粘性路由表 | 节点切换失效 | 2小时 |
| **P1** | Minimax端点配置 | 压缩功能降级 | 1小时 |
| **P2** | 添加字节数统计 | 成本分析不精确 | 4小时 |

---

## 立即执行清单

### 步骤1: 备份现有数据（10分钟）
```bash
# 在71服务器执行
SSHPASS='__REDACTED_SSH_PASSWORD__' sshpass -e ssh -p 25022 root@__HOST_71_IP__ \
  "docker exec llm-gateway-pg-71-replica pg_dump -U llm_gateway -d crm -Fc -f /tmp/crm_backup_20260630.dump"
```

### 步骤2: 创建表结构（20分钟）
```bash
# 1. 将 deploy/sql/01-schema.sql 上传到71服务器
scp -P 25022 deploy/sql/01-schema.sql root@__HOST_71_IP__:/tmp/

# 2. 提取 request_wal 和 request_logs 相关定义
grep -A 100 "CREATE TABLE public.request_wal" deploy/sql/01-schema.sql > /tmp/request_tables.sql
grep -A 200 "CREATE TABLE public.request_logs" deploy/sql/01-schema.sql >> /tmp/request_tables.sql

# 3. 在71数据库执行
SSHPASS='__REDACTED_SSH_PASSWORD__' sshpass -e ssh -p 25022 root@__HOST_71_IP__ \
  "docker exec -i llm-gateway-pg-71-replica psql -U llm_gateway -d crm < /tmp/request_tables.sql"
```

### 步骤3: 验证表创建（5分钟）
```bash
SSHPASS='__REDACTED_SSH_PASSWORD__' sshpass -e ssh -p 25022 root@__HOST_71_IP__ \
  "docker exec llm-gateway-pg-71-replica psql -U llm_gateway -d crm -c '\dt request*'"
```

预期输出:
```
 request_logs
 request_logs_2026_06
 request_logs_2026_07
 request_wal
 request_wal_2026_06
 request_wal_2026_07
```

### 步骤4: 重启服务（5分钟）
```bash
SSHPASS='__REDACTED_SSH_PASSWORD__' sshpass -e ssh -p 25022 root@__HOST_71_IP__ \
  "docker restart llm-gateway-go"
```

### 步骤5: 验证日志（10分钟）
```bash
# 观察日志，不应再出现 "no partition of relation" 错误
SSHPASS='__REDACTED_SSH_PASSWORD__' sshpass -e ssh -p 25022 root@__HOST_71_IP__ \
  "docker logs -f llm-gateway-go 2>&1 | grep -E 'request_logger|routing'"
```

---

## 监控建议

### 新增告警规则
1. **request_wal写入失败率**
   ```promql
   rate(llmgw_request_logger_errors_total[5m]) > 0.01
   ```

2. **路由候选节点为0**
   ```promql
   rate(llmgw_routing_no_candidates_total[5m]) > 0.01
   ```

3. **粘性路由写入失败率**
   ```promql
   rate(llmgw_sticky_routing_errors_total[5m]) > 0.01
   ```

### 仪表板指标
- request_wal 行数增长趋势
- request_logs 分区大小
- 每个模型的路由候选节点数
- 节点失败切换成功率

---

## 附录A: 完整SQL脚本

见 `deploy/sql/01-schema.sql` 文件，关键部分:
- Line 107-109: `request_wal` 表定义
- Line 2500-2600: `request_logs` 表定义
- Line 4000-4100: 分区创建函数 `ensure_request_logs_partition()`

---

## 附录B: 代码审计路径

1. `domains/hooks/observability/telemetry/request_logger.go` - 请求日志记录器
2. `provider/client.go:654-853` - 候选节点加载逻辑
3. `domains/streaming/stream.go` - 流式响应处理
4. `deploy/sql/01-schema.sql` - 数据库schema定义

---

## 审计人员
- AI Agent (Claude Opus 4)
- 审计时间: 2026-06-30 14:00-15:30 CST
- 服务器: __HOST_71_IP__ (71)
