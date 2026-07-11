# 附录 B — API 端点清单

> 所有 REST API 端点，按用途分组。详细签名见各章。
>
> **状态标记**：`[已实现]` = 代码中存在路由注册，`[待实现]` = 仅文档设计，代码不存在。

## 一、主控端 API（仅 master 二进制）

### 1.1 License 管理 [已实现]

> 路由注册：`licensing/admin_api.go:RegisterRoutes()`，Group 前缀由调用方决定。
> 完整路径示例：`POST /api/admin/licenses`

```
POST   /licenses                              # 创建 License [已实现]
GET    /licenses                              # 列出所有 [已实现]
GET    /licenses/:id                          # 查询单个 [已实现]
POST   /licenses/:id/revoke                   # 撤销 [已实现]
GET    /licenses/:id/devices                  # 列出设备 [已实现]
POST   /licenses/:id/devices/:hash/deactivate # 停用设备 [已实现]
GET    /licenses/offline-requests             # 待审批请求 [已实现]
POST   /licenses/offline-requests/:id/approve # 批准 [已实现]
POST   /licenses/offline-requests/:id/reject  # 拒绝 [已实现]
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

### 1.6 License Authority（主控端独立服务）[全部待实现]

> ⚠️ 整个 `license-authority/` 目录不存在，以下端点均为设计目标。
> 当前客户端通过 `Center API`（2.1 节）与主控端通信。

```
POST   /api/v1/license/activate             # 在线激活 [待实现]
POST   /api/v1/license/trial                # 自动试用 [待实现]
POST   /api/v1/license/refresh              # 24h 心跳刷新 [待实现]
POST   /api/v1/license/validate             # 验证 [待实现]
POST   /api/v1/license/deactivate           # 停用 [待实现]
GET    /api/v1/license/crl?since=...        # 撤销列表 [待实现]
POST   /api/v1/license/offline/request      # 客户端生成离线请求 [待实现]
POST   /api/v1/license/offline/verify       # 客户端导入离线响应 [待实现]
POST   /api/v1/license/offline-approve      # 主控端管理员审批 [待实现]
POST   /api/v1/collect/runtime              # 采集器上报 [待实现]
POST   /api/v1/updates/check                # 升级检查 [待实现]
GET    /api/v1/updates/latest               # 最新版本 [待实现]
GET    /api/v1/updates/manifest             # release manifest [待实现]
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

### 2.1 Center API [已实现]

> 路由注册：`licensing/center_api.go:RegisterRoutes()`，Group 前缀由调用方决定。
> 这些是**客户端暴露的端点**，供主控端远程调用（在线场景）或客户端自身调用（离线场景）。

```
POST   /api/center/activate                 # 激活 [已实现]
POST   /api/center/deactivate               # 停用 [已实现]
POST   /api/center/heartbeat                # 心跳 [已实现]
POST   /api/center/validate                 # 验证 [已实现]
POST   /api/center/offline/request          # 生成离线请求 [已实现]
POST   /api/center/offline/verify           # 导入离线响应 [已实现]
```

**调用方向**：
- **在线场景**：主控端 → 客户端（主控端主动调用客户端的 Center API）
- **离线场景**：客户端自身（用户在客户端 UI 操作，调用本地 Center API）

### 2.2 用户侧 API [全部待实现]

> ⚠️ 以下端点均为设计目标，代码中不存在。`gateway/internal/api/` 目录下无对应 handler。

```
# === License 激活 [待实现] ===
POST   /api/system/license/trial            # 试用激活
POST   /api/system/license/activate         # License Key 激活
POST   /api/system/license/offline/request  # 生成离线请求
POST   /api/system/license/offline/import   # 导入离线响应
GET    /api/system/license/status           # 查询状态

# === 升级控制 [待实现] ===
POST   /api/system/upgrade/check            # 检查更新
POST   /api/system/upgrade/start            # 开始升级
GET    /api/system/upgrade/stream?job_id=   # SSE 进度
GET    /api/system/upgrade/status           # 升级状态
POST   /api/system/upgrade/rollback         # 紧急回退

# === 采集控制 [待实现] ===
GET    /api/system/telemetry/pref           # 获取授权偏好
PUT    /api/system/telemetry/pref           # 更新授权偏好

# === 设置 [待实现] ===
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