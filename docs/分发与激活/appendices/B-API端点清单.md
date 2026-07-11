# 附录 B — API 端点清单

> 所有 REST API 端点，按用途分组。详细签名见各章。

## 一、主控端 API（仅 master 二进制）

### 1.1 License 管理（已实现）

```
POST   /api/admin/licenses                  # 创建 License
GET    /api/admin/licenses                  # 列出所有
GET    /api/admin/licenses/:key             # 查询单个
POST   /api/admin/licenses/:key/revoke      # 撤销
GET    /api/admin/licenses/:key/devices     # 列出设备
POST   /api/admin/licenses/:key/devices/:hash/deactivate   # 停用设备
GET    /api/admin/licenses/offline-requests                # 待审批请求
POST   /api/admin/licenses/offline-requests/:id/approve   # 批准
POST   /api/admin/licenses/offline-requests/:id/reject    # 拒绝
```

### 1.2 Release 管理（已实现）

```
POST   /api/admin/releases                  # 创建 Release
GET    /api/admin/releases                  # 列出
GET    /api/admin/releases/:version         # 查询单个
POST   /api/admin/releases/:version/publish
POST   /api/admin/releases/:version/unpublish
POST   /api/admin/releases/:version/gray    # 创建灰度
PATCH  /api/admin/releases/:version/gray    # 更新灰度阶段
GET    /api/admin/upgrade-logs              # 升级日志
POST   /api/admin/releases/:id/rollback
```

### 1.3 Center Ops（已实现）

```
GET    /api/admin/center/instances          # 实例列表
GET    /api/admin/center/stats              # 统计
GET    /api/admin/center/instances/:id/heartbeat?hours=24
POST   /api/admin/center/instances/:id/command    # 下发命令
```

### 1.4 Fault Management（已实现）

```
GET    /api/admin/faults/events
GET    /api/admin/faults/rules
POST   /api/admin/faults/rules
PUT    /api/admin/faults/rules/:id
DELETE /api/admin/faults/rules/:id
GET    /api/admin/faults/stats
POST   /api/admin/faults/events/:id/fix    # 手动触发修复
```

### 1.5 VibeCoding（已实现）

```
GET    /api/admin/vibecoding/projects
POST   /api/admin/vibecoding/projects
GET    /api/admin/vibecoding/sessions
POST   /api/admin/vibecoding/sessions
GET    /api/admin/vibecoding/reviews
```

### 1.6 License Authority（主控端独立服务，待建）

```
POST   /api/v1/license/activate             # 在线激活
POST   /api/v1/license/trial                # 自动试用
POST   /api/v1/license/refresh              # 24h 心跳刷新
POST   /api/v1/license/validate             # 验证
POST   /api/v1/license/deactivate           # 停用
GET    /api/v1/license/crl?since=...        # 撤销列表
POST   /api/v1/license/offline/request      # 客户端生成离线请求
POST   /api/v1/license/offline/verify       # 客户端导入离线响应
POST   /api/v1/license/offline-approve      # 主控端管理员审批
POST   /api/v1/collect/runtime              # 采集器上报
POST   /api/v1/updates/check                # 升级检查
GET    /api/v1/updates/latest               # 最新版本
GET    /api/v1/updates/manifest             # release manifest
```

### 1.7 Billing（v2.x 预留）

```
POST   /api/admin/billing/subscriptions
GET    /api/admin/billing/subscriptions
POST   /api/admin/billing/subscriptions/:id/cancel
POST   /api/admin/billing/subscriptions/:id/renew
POST   /api/admin/billing/invoices
GET    /api/admin/billing/invoices
GET    /api/admin/billing/invoices/:id/pdf
POST   /api/admin/billing/invoices/:id/void
GET    /api/admin/billing/usage
GET    /api/admin/billing/usage/:license_id
POST   /api/admin/billing/payment_methods
DELETE /api/admin/billing/payment_methods/:id
```

## 二、客户端 API（customer 二进制含）

### 2.1 已实现：Center API（已存在）

```
POST   /api/center/activate                 # 激活（对主控端）
POST   /api/center/deactivate
POST   /api/center/heartbeat                # 心跳
POST   /api/center/validate                 # 验证
POST   /api/center/offline/request
POST   /api/center/offline/verify
```

**注意**：这些 endpoint 在 客户端二进制 中是 **server side endpoint**，供主控端调用。

### 2.2 用户侧 API（新建，待实现）

```
# === License 激活 ===
POST   /api/system/license/trial            # 试用激活
POST   /api/system/license/activate         # License Key 激活
POST   /api/system/license/offline/request  # 生成离线请求
POST   /api/system/license/offline/import   # 导入离线响应
GET    /api/system/license/status           # 查询状态

# === 升级控制 ===
POST   /api/system/upgrade/check            # 检查更新
POST   /api/system/upgrade/start            # 开始升级
GET    /api/system/upgrade/stream?job_id=   # SSE 进度
GET    /api/system/upgrade/status           # 升级状态
POST   /api/system/upgrade/rollback         # 紧急回退

# === 采集控制 ===
GET    /api/system/telemetry/pref           # 获取授权偏好
PUT    /api/system/telemetry/pref           # 更新授权偏好

# === 设置 ===
GET    /api/system/info                     # 系统信息（版本、license 类型）
POST   /api/system/restart                  # 软重启
```

### 2.3 业务 API（双版都有，客户版受 license 限制）

```
# === 网关核心 ===
POST   /v1/chat/completions                 # OpenAI 兼容
POST   /v1/messages                         # Anthropic 兼容
POST   /v1/embeddings
GET    /v1/models
...

# === 管理 ===
GET    /api/admin/licenses                  # ⚠️ master only，customer 二进制无此路由
... 其他 admin API
```

## 三、API 安全性

### 3.1 认证

- **Bearer Token**：JWT (HS256)，8 小时有效
- **API Key**：LLM 提供商凭据（access_key）
- **License Signature**：客户端 ↔ License Authority 之间

### 3.2 授权

| API | 所需权限 |
|-----|---------|
| `/api/admin/*` | super_admin |
| `/api/system/*` | 任意已登录用户 |
| `/api/center/*` | License 验证通过 |

### 3.3 限流

- 全局：1000 RPS / IP
- LLM 调用：按 API Key 维度，套餐限制

## 四、API 版本管理

- URL path 中带 `/v1/`、`/v2/` 显式版本
- 同版本内向后兼容（新增字段可，删除/重命名需在 CHANGELOG 标注）

## 五、健康检查

```
GET /healthz               # 健康（业务）
GET /healthz?full=true     # 含 DB / Redis / License
GET /readyz                # readyz (k8s)
GET /metrics               # prometheus metrics
```