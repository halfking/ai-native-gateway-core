---
archived_from: (legacy) docs/archive/2026-07/FIX_SUMMARY_FINAL.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# 修复总结：minimax-m3 请求消息为空问题

## 🎯 问题确认

根据您的反馈，我重新理解了架构：
- **request_id 是唯一标识** - 一个 request_id 只对应一条请求记录
- **多个请求通过 gw_session_id 关联** - 会话级别的关联用 session_id
- **不需要 ts 来辅助关联两个表** - ts 只是记录时间戳

## 🔍 根本原因

### 问题：子查询返回 0 行导致数据丢失

**原代码**：
```sql
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
SELECT $1, rl.ts, CAST($2 AS jsonb), CAST($3 AS jsonb)
FROM request_logs_hot rl
WHERE rl.request_id = $1
ORDER BY rl.ts DESC
LIMIT 1
```

**根本问题**：
1. 子查询 `SELECT ... FROM request_logs_hot WHERE request_id = $1` 在高并发时返回 **0 行**
2. 原因：事务隔离级别 + 查询时机问题
3. 结果：`INSERT ... SELECT` 插入 0 行，**bodies 数据彻底丢失**
4. minimax 高频请求时问题更严重

## ✅ 修复方案

### 修复 1：INSERT 使用 NOW() 代替子查询

**修改文件**：`domains/hooks/observability/telemetry/client.go`

```sql
-- 修改后（简单可靠）
INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
VALUES ($1, NOW(), $2::jsonb, $3::jsonb)
ON CONFLICT (request_id, ts) DO UPDATE SET ...
```

**优势**：
- 不依赖子查询，避免查询失败
- NOW() 简单可靠，每次插入都成功
- ts 只用于记录时间，不用于关联

### 修复 2：JOIN 只用 request_id

**修改文件**：`admin/logs.go`

```sql
-- 修改后（request_id 是唯一标识）
LEFT JOIN request_logs_bodies_with_current_month rb 
  ON rb.request_id = rl.request_id
```

**原理**：
- request_id 是唯一标识，不需要 ts 辅助匹配
- 简化查询逻辑
- 避免因 ts 细微差异导致 JOIN 失败

## 📦 部署信息

- **版本**：v2.4.7-a3dfc1be-20260723-1341
- **提交**：a3dfc1be8
- **服务器**：154 (8.136.114.154:25022)
- **时间**：2026-07-23
- **状态**：✅ 已部署成功（总 45s，切换 25s）

## 🔬 架构说明

```
┌─────────────────────────────────────────┐
│ request_logs_hot                        │
│ - request_id (唯一标识)                 │
│ - gw_session_id (会话关联)              │
│ - ts (时间戳)                           │
│ - 其他元数据                            │
└─────────────────────────────────────────┘
         │
         │ request_id (1:1)
         ▼
┌─────────────────────────────────────────┐
│ request_logs_bodies_hot                 │
│ - request_id (唯一标识)                 │
│ - ts (时间戳，仅记录用)                 │
│ - request_body (完整请求)               │
│ - response_body (完整响应)              │
└─────────────────────────────────────────┘

查询逻辑：
- JOIN ON rb.request_id = rl.request_id
- 不需要 ts 匹配（因为 request_id 唯一）

会话关联：
- 同一会话的多个请求通过 gw_session_id 关联
- 每个请求的 request_id 是独立唯一的
```

## ✨ 预期效果

1. ✅ **"request not found" 错误消失** - 数据不再丢失
2. ✅ **请求消息完整显示** - request_body 和 response_body 都正常
3. ✅ **所有模型都正常** - minimax-m3、claude-opus-4-8 等
4. ✅ **高并发场景稳定** - 不再因子查询失败导致数据丢失

## 🧪 验证方式

### 前端验证
1. 打开"请求实时流"
2. 发送几个 minimax-m3 请求
3. 点击请求卡片
4. 验证：
   - ✅ 不再出现 "request not found"
   - ✅ 请求消息完整显示
   - ✅ 响应消息完整显示

### 数据库验证
```sql
-- 检查最近 1 小时的数据完整性
SELECT 
  COUNT(*) as total_requests,
  COUNT(rb.request_id) as with_bodies,
  COUNT(*) - COUNT(rb.request_id) as missing_bodies,
  ROUND(100.0 * COUNT(rb.request_id) / COUNT(*), 2) as coverage_pct
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id
WHERE rl.ts > NOW() - INTERVAL '1 hour';

-- 期望：coverage_pct 接近 100%
```

## 🔄 回滚方案

如需回滚：
```bash
bash scripts/deploy-seamless.sh rollback 154
```

## 📝 相关文件

- 核心修改：
  - `domains/hooks/observability/telemetry/client.go` - INSERT 使用 NOW()
  - `admin/logs.go` - JOIN 只用 request_id
- 文档：
  - `BUGFIX_MINIMAX_REQUEST_MESSAGE_EMPTY.md` - 详细分析
  - `DEPLOYMENT_SUMMARY.md` - 部署总结

## 🙏 感谢反馈

感谢您的纠正！
- ✅ request_id 是唯一标识（不需要 ts）
- ✅ 会话关联用 gw_session_id
- ✅ JOIN 不需要 ts 匹配

这次修复更符合实际架构，逻辑更简单清晰。

---

**修复完成时间**：2026-07-23  
**版本**：v2.4.7-a3dfc1be-20260723-1341  
**状态**：✅ 已部署到 154
