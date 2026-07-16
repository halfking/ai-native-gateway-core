# 2026-07-16: 修复 `/request-logs` 显示 833 条但 items=[] 的 bug

## 问题

用户报告：`https://llmgo.kxpms.cn/request-logs` 顶部显示「共 833 条」，但列表为空。

## 根因

`admin/logs.go` 的列表 SQL 用 `COALESCE(jsonb_array_length(rl.attachments), 0)` 计算
`attachment_count`。当某行的 `attachments` 列是 **JSON 字面量 `null`**（不是 SQL NULL）
时，`jsonb_array_length` 会抛错：

```
ERROR: cannot get array length of a scalar (SQLSTATE 22023)
```

245 数据库 24h 内有 36 行 attachments = JSON null（看表 schema 出现这种情况正常，
应该是 attachment writer 把空 attachments 序列化成 `'null'` 而不是 SQL NULL）。

**为什么列表静默返回空 items**：
1. `pgx.Query()` 在游标中途出错时不会立即 fail，要等 `rows.Next()` 走到坏行
2. handler 的 `for rows.Next() { scan... continue }` 把错误吞掉，`items` 保持空
3. 没有检查 `rows.Err()`，错误没有日志
4. 客户端拿到 `{"count":833,"items":[]}`，HTTP 200，误以为查询成功

同样的 bug 影响 `getLog`（详情抽屉）— stderr 中能看到
`admin getLog scan failed: cannot get array length of a scalar` 的告警。

## 修复

### 1. SQL 守门（admin/logs.go:163）

把：
```sql
COALESCE(jsonb_array_length(rl.attachments), 0) AS attachment_count
```

改成：
```sql
CASE WHEN jsonb_typeof(rl.attachments) = 'array'
     THEN jsonb_array_length(rl.attachments)
     ELSE 0
END AS attachment_count
```

`jsonb_typeof` 对 SQL NULL 返回 NULL（但 CASE 走 ELSE 0），对 JSON null/object/scalar
都返回对应类型字符串，统一降级为 0。

### 2. rows.Err() 检查 + scan 错误计数（admin/logs.go:493）

把 silent `continue` 改成累计 `scanErrCount` 并打 WARN 日志。循环结束后
显式检查 `rows.Err()`。这样 mid-stream 错误能立即被发现，不用再等用户报告
"items 是空的"。

## 验证

| 测试 | 修复前 | 修复后 |
|---|---|---|
| `GET /api/logs?page_size=3` | `{"count":833,"items":[]}` | items 返回 3 行 |
| `GET /api/logs?chrono=1&page_size=3` | 3 行（含 trace_seq） | 3 行（含 trace_seq） |
| `GET /api/logs?success=false&page_size=3` | 0 行 | 3 行 |
| `GET /api/logs?gw_task_id=default&page_size=3` | 3 行 | 3 行 |
| 22P02 in last 500 stderr lines | 0 | 0 |
| jsonb_array_length errors in last 500 stderr | N/A (从 list 隐藏) | 0 |

### Browser-use 实测（245 / `https://llmgo.kxpms.cn/request-logs`）

- 登录 `admin / Veritrans&9527`
- 跳转到 `/request-logs`
- 页面显示 `共 833 条` + 50 行表格行 ✓
- 截图：`/tmp/llmgw-requestlogs-fixed.png`

## 部署

- 245：seq=1084（commit 757cdef5），deploy-seamless.sh 一次过，44s 完成
- 154：未重试（handoff 中 SSH 不稳定，等用户决策）