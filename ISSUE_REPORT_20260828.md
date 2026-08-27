# LLM Gateway 请求详情页面问题报告

**日期**: 2026-08-28  
**环境**: https://llm.kxpms.cn (154服务器)  
**测试用户**: admin / Veritrans&9527

## 问题概述

在测试请求详情页面（`/request-detail/{request_id}`）时，发现部分成功状态的请求缺少`response_body`数据。

## 测试过程

### 1. 升级页面问题（已解决）

**问题**: 访问网站时显示"系统正在升级"页面
- 升级开始时间: 2026-08-27T21:01:56Z（第一次）和 2026-08-27T22:08:11Z（第二次）
- 原因: 部署脚本在验证失败时未能自动清除升级标志

**解决方案**: 创建了`scripts/clear-upgrade-banner.sh`脚本手动清除升级标志
```bash
bash scripts/clear-upgrade-banner.sh 154
```

### 2. API测试结果

**登录端点**: `/api/auth/token` (正确)  
**请求详情端点**: `/api/admin/request-detail/{request_id}` (正确)

### 3. Response Body 缺失问题

测试了多个请求记录，发现以下模式：

#### 有response_body的请求
- `8b4752bd7f5cf3aa6e53d297dc79f196` - success, glm-5.2 ✓
- `6efd6b87fe167cfae4b050055ff24874` - success, deepseek-v4-pro-260425 ✓
- `0bff014e0b2a0cce77ba445794579332` - success, glm-5.2 ✓
- 等等...

#### 缺少response_body的请求
- `127eaaaf93a9e3ff2873a5e299d90cb6` - **success**, glm-5.2 ✗
- `e2cb5984489f3022da84b67a31b9c0f0` - rate_limited, minimax-m3 ✗ (预期)
- `d2386ffd83faa0d426a07cee366dbc8e` - rate_limited, minimax-m3 ✗ (预期)

## 问题分析

### 1. Rate Limited 请求缺少 response_body（正常行为）

Rate limited的请求在网关层被拦截，没有发送到上游提供商，因此没有response_body是预期行为。

### 2. Success 请求缺少 response_body（异常）

**代码路径分析**:

1. **数据插入**: `domains/hooks/observability/telemetry/client.go:1472`
   ```go
   err = c.upsertRequestLogBodies(ctx, tx, entry.RequestID, 
       stringValue(entry.ApplicationCode),
       strPtrToJSON(entry.RequestBody),
       strPtrToJSON(entry.ResponseBody),  // 这里
       jsonOrNull(entry.OutboundBody),
   )
   ```

2. **strPtrToJSON处理**: 当`entry.ResponseBody`为nil时返回字符串`"null"`
   ```go
   func strPtrToJSON(s *string) string {
       if s == nil {
           return "null"  // 返回字符串"null"
       }
       if *s == "" || !json.Valid([]byte(*s)) {
           return "{}"
       }
       return *s
   }
   ```

3. **数据库插入**: `NULLIF($3, 'null')`将字符串"null"转为SQL NULL
   ```sql
   INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body, outbound_body)
   VALUES ($1, now(), NULLIF($2, 'null')::jsonb, NULLIF($3, 'null')::jsonb, NULLIF($4, 'null')::jsonb)
   ```

**根本原因**: 
- 某些情况下`entry.ResponseBody`在数据写入时就是nil
- 可能的原因：
  1. 响应体捕获失败（网络错误、超时等）
  2. 流式响应处理不完整
  3. 响应体过大被截断
  4. 在响应处理的某个环节丢失了数据

### 3. 数据读取路径

**查询顺序** (`admin/logs.go:1066`):
1. 缓存 (bodyFetchCache) - 最快
2. request_logs_bodies_hot 表 - 3秒超时
3. request_logs_bodies_with_current_month 视图 - 20秒超时

## 建议修复

### 短期修复

1. **增加日志**：在ResponseBody为nil时记录详细信息
   ```go
   if entry.ResponseBody == nil && entry.Success {
       slog.WarnContext(ctx, "success request missing response_body",
           "request_id", entry.RequestID,
           "client_model", entry.ClientModel,
           "latency_ms", entry.LatencyMs,
           "stream", entry.Stream,
       )
   }
   ```

2. **检查数据完整性**：定期扫描success状态但缺少response_body的请求
   ```sql
   SELECT request_id, client_model, request_status, ts
   FROM request_logs_hot rl
   WHERE success = true 
     AND request_status = 'success'
     AND NOT EXISTS (
       SELECT 1 FROM request_logs_bodies_hot rb 
       WHERE rb.request_id = rl.request_id 
         AND rb.response_body IS NOT NULL
     )
   ORDER BY ts DESC
   LIMIT 100;
   ```

### 长期修复

1. **响应捕获加固**：确保所有成功的非流式响应都能完整捕获
2. **流式响应处理**：改进SSE流的完整性验证
3. **监控告警**：添加指标监控response_body缺失率

## 测试工具

创建了以下测试脚本：
- `scripts/clear-upgrade-banner.sh` - 清除升级页面标志

## 相关文件

- `admin/unified_detail.go` - 统一请求详情API
- `admin/logs.go:1066` - fetchRequestBodies函数
- `domains/hooks/observability/telemetry/client.go` - 请求日志存储
- `scripts/deploy-seamless.sh` - 无缝部署脚本（包含升级页面逻辑）
- `scripts/deploy-lib/host.sh` - 升级页面管理函数

## 结论

1. ✅ 升级页面问题已通过手动清除解决
2. ✅ API端点测试通过，路由配置正确
3. ⚠️ 部分success请求的response_body缺失需要进一步调查
4. ✅ Rate limited请求缺少response_body是预期行为

建议优先排查为什么某些成功请求的`entry.ResponseBody`在写入数据库时为nil，这可能涉及响应捕获、流式处理或错误处理的逻辑问题。
