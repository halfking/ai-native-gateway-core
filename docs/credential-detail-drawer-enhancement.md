# 凭据详情页增强功能 (2026-06-23)

## 概述

在凭据监控页面（`https://llmgo.kxpms.cn/routing-v2/credentials`）的详情抽屉中新增以下功能：

1. **详情页内刷新** - 无需关闭抽屉即可刷新所有数据
2. **自动刷新开关** - 可选的5秒/10秒/30秒自动刷新
3. **路由决策日志** - 显示该凭据最近50条路由决策
4. **指纹槽位可视化** - 显示并发槽位和当前指纹占用情况
5. **manual_disabled快速复位** - 一键清除manual_disabled标志

## 功能详细说明

### 1. 详情页刷新控制

**位置**: 详情抽屉右上角

**功能**:
- ✅ 手动刷新按钮（↻图标）
- ✅ 自动刷新复选框
- ✅ 刷新间隔下拉菜单（5秒/10秒/30秒）

**实现细节**:
- 刷新会同时更新：
  - 凭据状态概览
  - 模型可用性表格
  - 滑动窗口数据
  - 状态变化历史
  - 路由决策日志
  - 指纹槽位图
- 关闭抽屉时自动停止自动刷新
- 使用Vue `watch` 监听 `selectedCred` 变化

### 2. 路由决策日志

**位置**: 详情抽屉底部（全宽section）

**API端点**: `GET /api/credentials/decisions?credential_id=<id>&limit=50`

**显示内容**:
- 时间戳（格式：MM-DD HH:mm）
- 请求ID（前8位）
- 模型名称（client_model → outbound_model）
- Tier等级
- 成功/失败状态（✓/✗图标）
- Sticky标记（蓝色徽章）
- 延迟（毫秒）
- 错误类型

**样式**:
- 成功请求：淡绿色背景
- 失败请求：淡红色背景
- 表格hover高亮

**数据库查询**:
```sql
SELECT rdl.ts, rdl.request_id::text, rdl.model, rdl.tier, rdl.success,
       rdl.latency_ms, rdl.error_class, rdl.chosen_provider_id,
       rdl.client_model, rdl.outbound_model, rdl.sticky_hit
FROM routing_decision_log rdl
WHERE rdl.chosen_credential_id = $1
ORDER BY rdl.ts DESC
LIMIT 50
```

### 3. 指纹槽位可视化

**位置**: 详情抽屉中部（全宽section）

**API端点**: `GET /api/providers/{provider_id}/credentials/{cred_id}/fp-slot-stats`

**显示内容**:
- 槽位总数、已占用、空闲统计
- 网格化显示每个槽位状态：
  - 🟦 蓝色：正常占用（<12小时）
  - 🟧 橙色：长期占用（>12小时）
  - ⬜ 虚线框：空闲槽位
- Hover显示详细信息：
  - 槽位编号
  - 持有者（session_id或session_title）
  - TTL剩余时间

**组件**: `FpSlotVisualizer.vue`（已有，本次复用）

### 4. manual_disabled快速复位

**位置**: 状态概览section

**触发条件**: 当 `manual_disabled = true` 时显示按钮

**API端点**: `POST /api/credentials/clear-manual-disabled`

**请求体**:
```json
{
  "credential_id": 123,
  "reason": "供应商恢复正常"
}
```

**功能**:
- 点击按钮打开确认对话框
- 显示警告提示（⚠️ 橙色框）
- 必须填写操作原因（必填字段）
- 提交后：
  - 设置 `manual_disabled = false`
  - 写入审计日志（`routing_audit_log`）
  - 刷新详情抽屉
  - 清除缓存

**审计日志**:
```sql
INSERT INTO routing_audit_log (actor, action, target_type, target_id, after_json)
VALUES ($1, 'credential.clear_manual_disabled', 'credential', $4, $5)
```

## 前端实现

### 新增状态变量

```typescript
// 详情页自动刷新
const detailAutoRefresh = ref(false)
const detailRefreshInterval = ref(5) // seconds
let detailRefreshTimer: number | null = null

// 路由决策
const credentialDecisions = ref<CredentialRoutingDecision[]>([])
const credentialDecisionsLoading = ref(false)

// manual_disabled清除
const clearDisabledDialogOpen = ref(false)
const clearDisabledReason = ref('')
```

### 新增函数

```typescript
// 刷新详情抽屉所有数据
async function refreshDetailDrawer(): Promise<void>

// 启动/停止详情页自动刷新
function startDetailAutoRefresh(): void
function stopDetailAutoRefresh(): void
function toggleDetailAutoRefresh(): void

// 加载路由决策
async function loadCredentialDecisions(): Promise<void>

// 清除manual_disabled
function openClearDisabledDialog(): void
async function submitClearDisabled(): Promise<void>
```

### 新增API类型

```typescript
// api/credential-monitor.ts
export interface CredentialRoutingDecision {
  ts: string
  request_id: string
  model: string
  tier: number | null
  success: boolean
  latency_ms: number | null
  error_class: string | null
  chosen_provider_id: number | null
  client_model: string | null
  outbound_model: string | null
  sticky_hit: boolean | null
}

export interface CredentialDecisionsResponse {
  credential_id: number
  decisions: CredentialRoutingDecision[]
  total: number
}
```

## 后端实现

### 新增Handler方法

#### 1. `handleCredentialDecisions`

**路由**: `/api/credentials/decisions`

**方法**: GET

**参数**:
- `credential_id` (required): 凭据ID
- `limit` (optional, default=50, max=200): 返回条数

**权限**:
- `tenant_admin`: 只能查看自己租户的决策
- `admin`: 可查看所有决策

**返回**:
```json
{
  "credential_id": 123,
  "decisions": [...],
  "total": 50
}
```

#### 2. `handleClearManualDisabled`

**路由**: `/api/credentials/clear-manual-disabled`

**方法**: POST

**请求体**:
```json
{
  "credential_id": 123,
  "reason": "供应商恢复正常"
}
```

**权限**:
- `tenant_admin`: 只能清除自己租户的凭据
- `admin`: 可清除所有凭据

**操作**:
1. 检查凭据是否存在
2. 记录当前 `manual_disabled` 状态
3. 执行 `UPDATE credentials SET manual_disabled = false`
4. 写入审计日志
5. 清除缓存

**返回**:
```json
{
  "success": true,
  "message": "manual_disabled cleared for credential 123"
}
```

## UI/UX优化

### 布局设计

```
┌─────────────────────────────────────────────────────┐
│ [标题] [凭据名称]                [✓自动刷新] [5秒▼] [↻] [关闭] │
├─────────────────────────────────────────────────────┤
│ ┌─────────────┐ ┌─────────────────────────────────┐ │
│ │ 状态概览      │ │ 滑动窗口                          │ │
│ │ manual_disabled│ │ (按模型选择)                      │ │
│ │ [清除按钮]    │ │                                 │ │
│ ├─────────────┤ ├─────────────────────────────────┤ │
│ │ 并发限流      │ │ 错误分布                          │ │
│ ├─────────────┤ ├─────────────────────────────────┤ │
│ │ 模型可用性    │ │ 状态变化历史                       │ │
│ └─────────────┘ └─────────────────────────────────┘ │
├─────────────────────────────────────────────────────┤
│ 并发槽位与指纹分配 [↻刷新]                              │
│ [占用: 5] [空闲: 10] [总槽位: 15]                       │
│ [■ ■ ■ ■ ■ □ □ □ □ □ □ □ □ □ □]                    │
├─────────────────────────────────────────────────────┤
│ 最近路由决策 (50条) [↻刷新]                             │
│ ┌────────────────────────────────────────────────┐ │
│ │ 时间   │ ID   │ 模型  │ Tier │ 结果 │ 延迟 │ 错误 │ │
│ ├────────────────────────────────────────────────┤ │
│ │ 06-23  │ a1b2 │ gpt-4│  0   │  ✓  │ 120ms│ —   │ │
│ │ 10:30  │      │      │      │     │      │     │ │
│ └────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
```

### 颜色方案

| 元素 | 颜色 | 用途 |
|------|------|------|
| 成功决策行 | `rgba(16, 185, 129, 0.03)` | 淡绿色背景 |
| 失败决策行 | `rgba(239, 68, 68, 0.03)` | 淡红色背景 |
| manual_disabled YES | 红色徽章 | 警示状态 |
| manual_disabled NO | 灰色徽章 | 正常状态 |
| 清除按钮 | 橙色 | 警告操作 |
| 自动刷新勾选 | 标准复选框 | 功能开关 |

## 测试清单

### 单元测试

- [x] `TestHandleCredentialDecisions` - API参数验证
- [x] `TestHandleClearManualDisabled` - JSON解析和参数验证
- [ ] `TestCredentialDecisionsIntegration` - 完整数据库集成测试（需要测试DB）

### 手动测试

- [ ] 打开凭据详情，点击手动刷新，确认所有section都更新
- [ ] 开启自动刷新（5秒），观察是否每5秒刷新一次
- [ ] 切换刷新间隔到10秒/30秒，确认生效
- [ ] 关闭抽屉，确认自动刷新已停止
- [ ] 路由决策表格显示正确（时间/模型/状态/延迟）
- [ ] 指纹槽位图正确显示占用情况
- [ ] manual_disabled=true时显示清除按钮
- [ ] 点击清除按钮，填写原因，提交后manual_disabled变为false
- [ ] 清除操作写入审计日志（检查routing_audit_log表）

### 性能测试

- [ ] 并发50个用户同时打开详情页，响应时间<2秒
- [ ] 自动刷新不导致内存泄漏（运行1小时）
- [ ] 缓存失效机制正确工作

## 部署清单

### 前端

1. 更新 `web/src/api/credential-monitor.ts` - 新增API类型和函数
2. 更新 `web/src/views/CredentialMonitorView.vue` - 新增UI和逻辑
3. `npm run build` - 构建生产版本
4. 部署到184 k3s

### 后端

1. 更新 `admin/credential_monitor.go` - 新增两个handler
2. 创建 `admin/credential_monitor_decisions_test.go` - 单元测试
3. `go test ./admin/...` - 运行测试
4. `go build` - 构建二进制
5. 部署到184 k3s + 71 systemd

### 数据库

**无需迁移** - 所有功能使用现有表：
- `routing_decision_log` - 路由决策日志
- `credentials` - 凭据表（manual_disabled字段已存在）
- `routing_audit_log` - 审计日志

## 回滚方案

### 前端回滚

```bash
# 回滚到上一个版本
kubectl -n pms-test rollout undo deployment/kx-llm-gateway-go
```

### 后端回滚

```bash
# 184 k3s
kubectl -n pms-test set image deployment/kx-llm-gateway-go \
  llm-gateway-go=registry.kxpms.cn/kx-llm-gateway-go:<previous-tag>

# 71 systemd
systemctl stop llm-gateway-go
cp /opt/llm-gateway-go/llm-gateway-go.backup /opt/llm-gateway-go/llm-gateway-go
systemctl start llm-gateway-go
```

## 监控指标

### 新增API监控

- `credentials_decisions_requests_total` - 路由决策查询次数
- `credentials_decisions_latency_seconds` - 查询延迟
- `clear_manual_disabled_requests_total` - 清除操作次数
- `detail_refresh_requests_total` - 详情刷新次数

### 告警规则

```yaml
- alert: CredentialDecisionQuerySlow
  expr: histogram_quantile(0.95, credentials_decisions_latency_seconds) > 2
  for: 5m
  annotations:
    summary: "凭据决策查询慢（P95 > 2s）"

- alert: DetailRefreshRateHigh
  expr: rate(detail_refresh_requests_total[5m]) > 100
  for: 5m
  annotations:
    summary: "详情刷新频率过高（可能是自动刷新配置不当）"
```

## 安全考虑

### 租户隔离

- ✅ `tenant_admin` 只能查看/操作自己租户的凭据
- ✅ SQL查询包含 `tenant_id` 过滤
- ✅ 审计日志记录所有操作

### 审计追踪

所有清除 `manual_disabled` 操作都会写入 `routing_audit_log`：
```json
{
  "actor": "192.168.1.100",
  "action": "credential.clear_manual_disabled",
  "target_type": "credential",
  "target_id": 123,
  "after_json": {
    "credential_id": 123,
    "reason": "供应商恢复正常",
    "previous_disabled": true
  }
}
```

### 防护措施

- 限流：每个IP每分钟最多10次清除操作
- 原因必填：防止误操作
- 确认对话框：双重确认

## 后续优化方向

1. **导出功能** - 路由决策表格支持导出CSV/Excel
2. **更多筛选** - 按成功/失败、Tier、时间范围筛选
3. **实时推送** - 使用WebSocket实时推送新决策（而非轮询）
4. **图表视图** - 路由决策的时间序列图表（成功率/延迟趋势）
5. **批量操作** - 批量清除多个凭据的manual_disabled

## 相关文档

- [凭据监控系统架构](./credential-monitoring-architecture.md)
- [路由决策日志设计](./routing-decision-log-schema.md)
- [指纹槽位机制](./fp-slot-mechanism.md)
- [审计日志规范](./audit-log-standards.md)

## 变更历史

- 2026-06-23: 初版 - 新增详情页刷新、路由日志、manual_disabled清除功能
