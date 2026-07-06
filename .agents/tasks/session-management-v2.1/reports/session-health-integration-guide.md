# Session Health Score API & Worker Integration Guide

## 概述

本文档说明如何将健康评分 API 和后台 worker 集成到 `cmd/gateway/main.go`。

## 文件清单

### 核心实现
- `admin/session_health.go` - 健康评分计算核心逻辑（Phase 0 已完成）
- `admin/session_health_api.go` - 健康评分 API 端点（本次实现）
- `bg/session_health_worker.go` - 后台健康分计算 worker（本次实现）

### 测试文件
- `admin/session_health_test.go` - 核心逻辑测试
- `admin/session_health_api_test.go` - API 端点测试
- `bg/session_health_worker_test.go` - Worker 测试

### 数据库迁移
- `sql/migrations/startup/356_session_health_columns.sql` - 健康评分表字段（已存在）

## 1. API 路由注册

在 `cmd/gateway/main.go` 的路由注册部分添加以下代码：

```go
// Session Health Endpoints (around line 2047, after session-analytics routes)
mux.HandleFunc("/api/admin/sessions/", func(w http.ResponseWriter, r *http.Request) {
    // Extract session ID from path
    path := r.URL.Path
    
    // GET /api/admin/sessions/<id>/health
    if strings.HasSuffix(path, "/health") && r.Method == http.MethodGet {
        adminHandler.HandleSessionHealth(w, r)
        return
    }
    
    // POST /api/admin/sessions/<id>/recompute-health
    if strings.HasSuffix(path, "/recompute-health") && r.Method == http.MethodPost {
        adminHandler.HandleRecomputeSessionHealth(w, r)
        return
    }
    
    // 其他 /api/admin/sessions/* 路由...
    http.NotFound(w, r)
})
```

**注意**：如果已经有 `/api/admin/sessions/` 路由组，将上述两个 handler 添加到现有的路由逻辑中。

## 2. 后台 Worker 启动

在 `cmd/gateway/main.go` 的后台服务初始化部分添加：

### 2.1 构造 Worker（约在 line 1500-1600，bg services 初始化区域）

```go
// Session Health Worker
sessionHealthWorker := bg.NewSessionHealthWorker(db.Pool())
```

### 2.2 启动 Worker（约在 line 1700-1800，启动所有 bg workers）

```go
// Start session health worker
sessionHealthWorker.Start(context.Background())
bgServices = append(bgServices, sessionHealthWorker)
slog.Info("background service started", "service", "session_health_worker")
```

### 2.3 优雅关闭（在 shutdown 逻辑中）

```go
// 在 shutdown handler 中（通常在 line 2500+ 的 signal handler）
for _, svc := range bgServices {
    if w, ok := svc.(*bg.SessionHealthWorker); ok {
        w.Stop()
        slog.Info("stopped session health worker")
    }
}
```

## 3. Prometheus 指标

以下指标会自动注册到 Prometheus：

### 3.1 API 计算次数
```
llmgw_session_health_computed_total{source="api"}
llmgw_session_health_computed_total{source="snapshot"}
```

### 3.2 Worker 处理统计
```
llmgw_session_health_worker_computed_total{status="success"}
llmgw_session_health_worker_computed_total{status="error"}
```

指标可通过 `/metrics` 端点访问。

## 4. API 使用示例

### 4.1 获取会话健康详情

```bash
curl -X GET "http://localhost:8080/api/admin/sessions/gw_abc123/health" \
  -H "Authorization: Bearer <admin-token>"
```

**响应**：
```json
{
  "gw_session_id": "gw_abc123",
  "health_score": 72,
  "health_grade": "C",
  "outcome": "completed",
  "outcome_reason": "0 errors across 45 requests",
  "error_rate": 0.0,
  "avg_latency_ms": 1800,
  "computed_at": "2026-07-06T10:30:00Z",
  "penalties": [
    {
      "reason": "high_latency",
      "deduction": 15,
      "detail": "avg_latency_ms=6200 > 5000"
    },
    {
      "reason": "frequent_model_switch",
      "deduction": 10,
      "detail": "model_switch_count=5 > 3"
    }
  ]
}
```

### 4.2 强制重新计算健康分

```bash
curl -X POST "http://localhost:8080/api/admin/sessions/gw_abc123/recompute-health" \
  -H "Authorization: Bearer <admin-token>"
```

## 5. Worker 运行逻辑

### 5.1 扫描条件
```sql
SELECT * FROM session_summaries
WHERE last_request_at < NOW() - INTERVAL '1 hour'
  AND health_score IS NULL
ORDER BY last_request_at DESC
LIMIT 100
```

### 5.2 执行频率
- **首次执行**：启动后 5 分钟
- **周期**：每 60 分钟
- **批量大小**：每批最多 100 条

### 5.3 日志示例
```
INFO session health worker started interval=60m
INFO session health worker processing batch count=23
INFO session health worker batch completed total=23 success=23 failed=0
```

## 6. 会话停止时自动计算（可选集成）

若需要在会话停止时自动计算健康分，可在 `admin/session_state_handlers.go` 的停止会话逻辑中调用：

```go
// 在 handleStopSession 函数中（会话停止后）
if h.adminHandler != nil {
    go func(sessionID string) {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        
        if _, err := h.adminHandler.ComputeAndPersistHealth(ctx, sessionID, "snapshot"); err != nil {
            slog.Warn("failed to compute health on session stop",
                "gw_session_id", sessionID,
                "error", err)
        }
    }(gwSessionID)
}
```

## 7. 数据库验证

### 7.1 检查健康分是否写入
```sql
SELECT session_key, health_score, health_grade, outcome, last_health_at
FROM session_summaries
WHERE health_score IS NOT NULL
ORDER BY last_health_at DESC
LIMIT 10;
```

### 7.2 检查待计算会话数量
```sql
SELECT COUNT(*) as pending_count
FROM session_summaries
WHERE last_request_at < NOW() - INTERVAL '1 hour'
  AND health_score IS NULL;
```

## 8. 健康评分配置（可选）

默认配置在 `admin/session_health.go` 的 `DefaultHealthScoreConfig()` 中。

若需支持动态配置，可：
1. 将配置存储到数据库（如 `settings` 表）
2. 在 API/Worker 中从数据库读取配置
3. 提供管理端点更新配置

## 9. 监控建议

### 9.1 关键指标告警
- Worker 失败率 > 10%
- 待计算会话数 > 1000
- API 延迟 > 5s

### 9.2 Grafana 查询示例
```promql
# Worker 成功率
rate(llmgw_session_health_worker_computed_total{status="success"}[5m])
/ rate(llmgw_session_health_worker_computed_total[5m])

# API 计算次数
rate(llmgw_session_health_computed_total{source="api"}[5m])
```

## 10. 故障排查

### 10.1 健康分未计算
- 检查 worker 是否启动：`ps aux | grep gateway`
- 查看日志：`grep "session health worker" /var/log/gateway.log`
- 验证数据库连接：`SELECT 1 FROM session_summaries LIMIT 1`

### 10.2 API 返回 404
- 确认路由已注册：查看启动日志
- 测试 Handler 是否存在：`curl /api/admin/sessions/test/health`

### 10.3 计算结果异常
- 检查 `session_summaries` 数据完整性
- 验证配置参数：`DefaultHealthScoreConfig()`
- 查看扣分明细：penalties 字段

## 11. 性能考虑

### 11.1 批量计算优化
- 每批限制 100 条（避免单次过长）
- 使用事务批量更新（可选优化）

### 11.2 索引建议
```sql
-- 已包含在 356_session_health_columns.sql
CREATE INDEX IF NOT EXISTS idx_session_summaries_health_score
    ON session_summaries (health_score DESC) WHERE health_score IS NOT NULL;
```

### 11.3 查询优化
- GET API：单会话查询，索引已优化
- Worker：批量扫描，使用 `last_request_at` 索引

## 12. 下一步

- [ ] 在 `cmd/gateway/main.go` 中添加路由和 worker 启动代码
- [ ] 部署并观察日志
- [ ] 配置 Prometheus 告警规则
- [ ] 集成到前端会话列表（显示健康等级列）

---

**参考文档**：
- 产品规划：`docs/session-management-analytics-plan.md` 第 4.4 节 + 11.2.9 节
- 数据模型：`docs/session-management-analytics-plan.md` 第 5 章
- API 规格：`docs/session-management-analytics-plan.md` 第 6 章
