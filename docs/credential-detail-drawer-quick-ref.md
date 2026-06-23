# 凭据详情页优化 - 快速参考卡

## 🎯 核心功能（5个）

| # | 功能 | 入口 | 关键API |
|---|------|------|---------|
| 1 | **详情页刷新** | 右上角↻按钮 | 复用现有API |
| 2 | **自动刷新** | 右上角勾选框 | 5秒/10秒/30秒可选 |
| 3 | **路由决策日志** | 底部表格 | `GET /api/credentials/decisions` |
| 4 | **指纹槽位图** | 中部可视化 | `GET /api/providers/{pid}/credentials/{cid}/fp-slot-stats` |
| 5 | **清除manual_disabled** | 状态概览按钮 | `POST /api/credentials/clear-manual-disabled` |

## 📂 代码改动

```
frontend (Vue):
  ✅ web/src/api/credential-monitor.ts        +54行
  ✅ web/src/views/CredentialMonitorView.vue  +322行

backend (Go):
  ✅ admin/credential_monitor.go              +193行 (2个handler)
  ✅ admin/credential_monitor_decisions_test.go +169行

docs:
  ✅ docs/credential-detail-drawer-enhancement.md          (完整文档)
  ✅ docs/credential-detail-drawer-implementation-summary.md (实施总结)
```

## 🔌 新增API端点

### 1. 获取凭据路由决策
```http
GET /api/credentials/decisions?credential_id=123&limit=50
Authorization: Bearer <token>

Response:
{
  "credential_id": 123,
  "decisions": [
    {
      "ts": "2026-06-23T10:30:00Z",
      "request_id": "uuid",
      "model": "gpt-4",
      "tier": 0,
      "success": true,
      "latency_ms": 120,
      "error_class": null,
      "client_model": "gpt-4",
      "outbound_model": "gpt-4-0613",
      "sticky_hit": false
    }
  ],
  "total": 50
}
```

### 2. 清除manual_disabled
```http
POST /api/credentials/clear-manual-disabled
Content-Type: application/json
Authorization: Bearer <token>

Body:
{
  "credential_id": 123,
  "reason": "供应商恢复正常"
}

Response:
{
  "success": true,
  "message": "manual_disabled cleared for credential 123"
}
```

## 🎨 UI布局

```
┌─────────────────────────────────────────────────────────┐
│ 凭据 #123                   [✓自动刷新][5秒▼][↻][关闭]    │
├───────────────┬─────────────────────────────────────────┤
│ 状态概览        │ 滑动窗口 (最近1小时)                      │
│ manual_disabled│ [模型选择] [刷新]                        │
│ [🔓清除]       │ ████████████░░░░                         │
├───────────────┤ ─────────────────────────────────────── │
│ 并发限流        │ 错误分布 (饼图)                          │
│ [调整][降级]    │                                         │
├───────────────┤ ─────────────────────────────────────── │
│ 模型可用性表格   │ 状态变化历史 (手动+自动)                  │
│ (可点击查看)    │ [刷新]                                  │
├─────────────────────────────────────────────────────────┤
│ 并发槽位与指纹分配 [↻刷新]                                │
│ [占用: 5] [空闲: 10] [总: 15]                             │
│ ■■■■■□□□□□□□□□□ (hover显示详情)                       │
├─────────────────────────────────────────────────────────┤
│ 最近路由决策 (50条) [↻刷新]                              │
│ ┌─────────────────────────────────────────────────────┐ │
│ │时间   │ID   │模型  │Tier│✓/✗│延迟  │错误            │ │
│ ├─────────────────────────────────────────────────────┤ │
│ │06-23  │a1b2c│gpt-4 │ 0  │ ✓ │120ms │—              │ │ (绿)
│ │10:30  │     │      │    │   │      │               │ │
│ │06-23  │d3e4f│gpt-35│ 1  │ ✗ │2500ms│rate_limit     │ │ (红)
│ │10:29  │     │turbo │    │   │      │               │ │
│ └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## ⚡ 快速测试

### 前端测试（浏览器）
```javascript
// 1. 打开 https://llmgo.kxpms.cn/routing-v2/credentials
// 2. 点击任一凭据
// 3. 在控制台执行：

// 测试自动刷新
localStorage.setItem('test_auto_refresh', '1')

// 测试API
fetch('/api/credentials/decisions?credential_id=1&limit=10')
  .then(r => r.json())
  .then(console.log)
```

### 后端测试（curl）
```bash
# 测试决策API
curl -s "https://llmgo.kxpms.cn/api/credentials/decisions?credential_id=1&limit=5" | jq

# 测试清除API
curl -X POST "https://llmgo.kxpms.cn/api/credentials/clear-manual-disabled" \
  -H "Content-Type: application/json" \
  -d '{"credential_id": 1, "reason": "test"}' | jq
```

### 数据库验证
```sql
-- 查看路由决策数量
SELECT chosen_credential_id, COUNT(*) 
FROM routing_decision_log 
GROUP BY chosen_credential_id 
ORDER BY COUNT(*) DESC 
LIMIT 10;

-- 查看清除操作审计
SELECT * FROM routing_audit_log 
WHERE action = 'credential.clear_manual_disabled' 
ORDER BY ts DESC 
LIMIT 5;
```

## 🚀 一键部署

```bash
# 完整部署流程 (从llm-gateway-go子模块根目录)

# 1. 前端构建
cd web && npm run build && cd ..

# 2. 后端测试
go test ./admin/... -v

# 3. 构建镜像
docker build -t registry.kxpms.cn/kx-llm-gateway-go:$(date +%Y%m%d) .

# 4. 推送镜像
docker push registry.kxpms.cn/kx-llm-gateway-go:$(date +%Y%m%d)

# 5. 部署184
kubectl -n pms-test set image deployment/kx-llm-gateway-go \
  llm-gateway-go=registry.kxpms.cn/kx-llm-gateway-go:$(date +%Y%m%d)

# 6. 验证
kubectl -n pms-test rollout status deployment/kx-llm-gateway-go
curl -s https://llmgo.kxpms.cn/healthz | jq
```

## 🔍 故障排查

### 问题：详情页刷新无反应
```bash
# 1. 检查前端控制台
# 预期：无红色error

# 2. 检查后端日志
kubectl -n pms-test logs -l app=llm-gateway-go --tail=50 | grep "decisions"

# 3. 检查API可达性
curl -v https://llmgo.kxpms.cn/api/credentials/decisions?credential_id=1
```

### 问题：路由决策表格空白
```sql
-- 检查数据是否存在
SELECT COUNT(*), chosen_credential_id 
FROM routing_decision_log 
WHERE chosen_credential_id = 123 
GROUP BY chosen_credential_id;

-- 检查最近数据时间
SELECT MAX(ts) FROM routing_decision_log;
```

### 问题：清除manual_disabled失败
```bash
# 1. 检查凭据是否存在
curl "https://llmgo.kxpms.cn/api/credentials/monitor-summary" | jq '.credentials[] | select(.id==123)'

# 2. 检查权限（tenant_admin需要匹配tenant_id）
# 3. 查看后端日志
kubectl -n pms-test logs -l app=llm-gateway-go --tail=100 | grep "clear-manual-disabled"
```

## 📊 性能指标

| 指标 | 目标 | 当前 | 备注 |
|------|------|------|------|
| 详情刷新延迟 | <2s | TBD | 含所有section |
| 决策查询延迟 | <1s | TBD | 50条记录 |
| 自动刷新内存增长 | <10MB/h | TBD | 运行1小时 |
| 并发用户支持 | 50 | TBD | 无卡顿 |

## 🔐 安全检查清单

- [x] tenant_admin租户隔离验证
- [x] SQL注入防护（使用参数化查询）
- [x] 审计日志完整记录
- [x] 必填字段验证（reason）
- [ ] 限流配置（待部署后启用）
- [ ] CSRF保护（复用现有中间件）

## 📚 相关文档

| 文档 | 路径 |
|------|------|
| 完整功能说明 | `docs/credential-detail-drawer-enhancement.md` |
| 实施总结 | `docs/credential-detail-drawer-implementation-summary.md` |
| 本参考卡 | `docs/credential-detail-drawer-quick-ref.md` |
| API规范 | `web/src/api/credential-monitor.ts` |

## 🎓 关键代码片段

### 前端：刷新详情抽屉
```typescript
async function refreshDetailDrawer() {
  if (!selectedCred.value) return
  await load() // 重新加载summary
  const updated = credentials.value.find(c => c.id === selectedCred.value?.id)
  if (updated) selectedCred.value = updated
  
  // 并行刷新所有section
  await Promise.all([
    selectedModel.value ? loadSlidingWindow(selectedCred.value.id, selectedModel.value) : Promise.resolve(),
    selectedModel.value ? loadHistory() : Promise.resolve(),
    loadCredentialDecisions(),
    loadFpSlotStats(),
  ])
}
```

### 后端：查询路由决策
```go
q := fmt.Sprintf(`
  SELECT rdl.ts, rdl.request_id::text, rdl.model, rdl.tier, rdl.success,
         rdl.latency_ms, rdl.error_class, rdl.chosen_provider_id,
         rdl.client_model, rdl.outbound_model, rdl.sticky_hit
  FROM routing_decision_log rdl
  WHERE rdl.chosen_credential_id = $1 %s
  ORDER BY rdl.ts DESC
  LIMIT $%d
`, tenantClause, len(args))
```

### 后端：清除manual_disabled
```go
updateQ := fmt.Sprintf("UPDATE credentials SET manual_disabled = false WHERE id = $1 %s", tenantCheck)
m.h.db.Exec(ctx, updateQ, updateArgs...)

// 写入审计日志
m.h.db.Exec(ctx, `
  INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json)
  VALUES ($1, $2, $3, $4, $5)
`, actor, "credential.clear_manual_disabled", "credential", credentialID, detailsJSON)
```

---

**版本**: 1.0  
**更新**: 2026-06-23  
**维护**: LLM Gateway Team
