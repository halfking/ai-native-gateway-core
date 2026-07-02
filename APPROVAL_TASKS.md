# llm-gateway-go 架构重构 - 审批流程集成任务分解

## 任务重新划分

基于审批流程需求，将原有的 Phase 2-6 重新划分为更细粒度的并行任务。

---

## 任务分组

### Group A: 审批核心基础设施（可并行）

#### 任务 A1: 扩展会话状态机支持审批流程
**预计时间**: 2-3 小时  
**依赖**: 无  
**可并行**: ✅

**任务内容**:
1. 扩展 `domains/session/state_machine.go`，添加 4 个新状态：
   - `StatePendingApproval` - 等待审批检测
   - `StateApprovalRequested` - 审批请求已创建
   - `StateApprovalApproved` - 审批通过
   - `StateApprovalRejected` - 审批拒绝

2. 添加审批相关错误类型：
   - `ErrApprovalPending` - 审批进行中
   - `ErrApprovalTimeout` - 审批超时
   - `ErrApprovalRejected` - 审批被拒绝

3. 扩展 `SessionContext`，添加审批相关字段：
   - `ApprovalRequestID string`
   - `ApprovalStatus ApprovalStatus`
   - `ApprovalResult *ApprovalResult`

4. 单元测试覆盖新状态转换

---

#### 任务 A2: 实现审批数据模型和存储
**预计时间**: 3-4 小时  
**依赖**: 无  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/types.go`，定义所有数据模型：
   - `ApprovalRequest`
   - `ApprovalConfig`
   - `ApprovalRule`
   - `Approver`
   - `NotificationChannel`
   - 所有枚举类型

2. 创建数据库迁移文件：
   - `XXX_create_approval_tables.up.sql`
   - `XXX_create_approval_tables.down.sql`
   - 创建 4 张表：approval_configs, approval_requests, approval_approvers, approval_rules

3. 实现 `domains/approval/store.go`：
   - PostgreSQL 存储实现
   - Redis 缓存实现
   - CRUD 操作

4. 单元测试（使用 testcontainers）

---

#### 任务 A3: 实现敏感信息检测器
**预计时间**: 2-3 小时  
**依赖**: 无  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/sensitive_detector.go`：
   - 正则匹配：身份证、手机号、邮箱、银行卡
   - 关键词匹配：密码、API Key、Token
   - NER 检测（可选，使用轻量级模型）

2. 实现脱敏函数：
   - `Redact(content string, items []SensitiveItem) string`
   - 保留部分信息用于识别（如手机号只显示后 4 位）

3. 配置化敏感词库：
   - 支持从配置文件加载
   - 支持正则和精确匹配

4. 单元测试（覆盖各种敏感信息类型）

---

#### 任务 A4: 实现会话摘要生成器
**预计时间**: 2-3 小时  
**依赖**: 无  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/summarizer.go`：
   - 使用 LLM 生成会话摘要（调用 OpenAI/Claude）
   - Prompt 工程：提取话题、意图、关键信息
   - 支持多语言

2. 实现 fallback 机制：
   - LLM 调用失败时使用规则提取
   - 提取最近 N 条消息
   - 统计 token 数和消息数

3. 缓存机制：
   - 相同会话只生成一次摘要
   - Redis 缓存

4. 单元测试（mock LLM 响应）

---

### Group B: 通知渠道实现（可并行）

#### 任务 B1: 实现飞书通知渠道
**预计时间**: 3-4 小时  
**依赖**: 任务 A2（数据模型）  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/channels/feishu.go`：
   - 集成飞书 Open API
   - 实现消息卡片构建
   - 支持交互式按钮（批准/拒绝）

2. 实现回调处理：
   - `/api/webhooks/feishu/approval-callback`
   - 验证签名
   - 解析按钮点击事件

3. 配置管理：
   - app_id, app_secret
   - webhook_url

4. 单元测试和集成测试

---

#### 任务 B2: 实现企业微信通知渠道
**预计时间**: 3-4 小时  
**依赖**: 任务 A2  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/channels/wechat.go`：
   - 集成企业微信 API
   - 实现文本卡片消息
   - 支持审批链接跳转

2. 实现回调处理：
   - `/api/webhooks/wechat/approval-callback`

3. 配置管理：
   - corp_id, corp_secret, agent_id

4. 单元测试和集成测试

---

#### 任务 B3: 实现钉钉通知渠道
**预计时间**: 3-4 小时  
**依赖**: 任务 A2  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/channels/dingtalk.go`：
   - 集成钉钉 Open API
   - 实现工作通知
   - 支持审批链接

2. 实现回调处理：
   - `/api/webhooks/dingtalk/approval-callback`

3. 配置管理：
   - app_key, app_secret

4. 单元测试和集成测试

---

#### 任务 B4: 实现邮件和 Webhook 通知
**预计时间**: 2-3 小时  
**依赖**: 任务 A2  
**可并行**: ✅

**任务内容**:
1. 创建 `domains/approval/channels/email.go`：
   - SMTP 邮件发送
   - HTML 邮件模板
   - 包含批准/拒绝链接

2. 创建 `domains/approval/channels/webhook.go`：
   - POST JSON 到自定义 URL
   - 支持重试和超时

3. 单元测试

---

### Group C: 审批核心逻辑（依赖 Group A）

#### 任务 C1: 实现审批检测器
**预计时间**: 3-4 小时  
**依赖**: 任务 A2, A3  
**可并行**: ❌（依赖 A2, A3）

**任务内容**:
1. 创建 `domains/approval/detector.go`：
   - 规则引擎：评估审批规则
   - 成本估算：计算 token 和成本
   - 敏感信息检测集成
   - 工具调用检测

2. 实现 `ShouldApprove` 方法：
   - 输入：SessionContext
   - 输出：ApprovalDecision（是否需要审批 + 原因）

3. 支持多种触发类型：
   - 敏感内容
   - 高成本
   - 工具调用
   - 手动模式

4. 单元测试（各种规则组合）

---

#### 任务 C2: 实现审批管理器
**预计时间**: 4-5 小时  
**依赖**: 任务 A2, A4, C1  
**可并行**: ❌（依赖 C1）

**任务内容**:
1. 创建 `domains/approval/manager.go`：
   - `CreateApprovalRequest()` - 创建审批请求
   - `WaitForApproval()` - 异步等待审批结果
   - `Approve()` - 批准审批
   - `Reject()` - 拒绝审批
   - `HandleTimeout()` - 处理超时

2. 集成会话摘要生成器

3. 实现审批结果通知：
   - 批准后继续流程
   - 拒绝后返回错误给客户端

4. 单元测试

---

#### 任务 C3: 实现审批通知器
**预计时间**: 2-3 小时  
**依赖**: 任务 A2, Group B（所有通知渠道）  
**可并行**: ❌（依赖 Group B）

**任务内容**:
1. 创建 `domains/approval/notifier.go`：
   - 渠道管理器
   - 多渠道并发发送
   - 失败重试

2. 支持多审批人并发通知

3. 通知去重（同一审批请求不重复发送）

4. 单元测试

---

### Group D: API 和 Handler（依赖 Group C）

#### 任务 D1: 实现审批配置管理 API
**预计时间**: 3-4 小时  
**依赖**: 任务 A2  
**可并行**: ✅（与 Group C 并行）

**任务内容**:
1. 创建 `admin/approval_config_handler.go`：
   - GET /api/admin/tenants/{tenant_id}/approval-config
   - PUT /api/admin/tenants/{tenant_id}/approval-config
   - GET /api/admin/tenants/{tenant_id}/approvers
   - POST /api/admin/tenants/{tenant_id}/approvers
   - DELETE /api/admin/tenants/{tenant_id}/approvers/{user_id}

2. 权限控制：只有管理员可以修改配置

3. 输入验证和错误处理

4. API 文档（OpenAPI）

---

#### 任务 D2: 实现审批请求查询 API
**预计时间**: 2-3 小时  
**依赖**: 任务 A2  
**可并行**: ✅

**任务内容**:
1. 创建 `api/approval_handler.go`：
   - GET /api/v1/approvals/{request_id}
   - POST /api/v1/approvals/{request_id}/approve
   - POST /api/v1/approvals/{request_id}/reject
   - GET /api/admin/approvals（列表）
   - GET /api/admin/approvals/stats（统计）

2. 权限控制：
   - 审批人可以查看和操作自己的审批请求
   - 管理员可以查看所有

3. API 文档

---

#### 任务 D3: 集成审批流程到 ChatHandler
**预计时间**: 4-5 小时  
**依赖**: 任务 C1, C2, C3  
**可并行**: ❌（依赖 Group C）

**任务内容**:
1. 修改 `ChatHandler.ServeHTTP`：
   - 在 `StatePendingToLLM` 注册审批检测回调
   - 处理 `ErrApprovalPending` 错误
   - 返回 202 Accepted 响应

2. 实现审批轮询接口：
   - GET /v1/chat/approvals/{request_id}/status

3. 实现审批完成后继续流程：
   - 异步处理审批结果
   - 批准后调用 LLM
   - 拒绝后返回错误

4. 集成测试

---

### Group E: 前端和文档（可并行）

#### 任务 E1: 实现审批配置管理界面
**预计时间**: 4-5 小时  
**依赖**: 任务 D1  
**可并行**: ✅（与其他任务并行）

**任务内容**:
1. 创建 Vue 页面 `/admin/approval-config`：
   - 启用/禁用开关
   - 审批模式选择
   - 审批人管理（增删改查）
   - 通知渠道配置
   - 审批规则配置

2. 表单验证

3. 实时预览

4. E2E 测试

---

#### 任务 E2: 实现审批请求列表和详情页
**预计时间**: 4-5 小时  
**依赖**: 任务 D2  
**可并行**: ✅

**任务内容**:
1. 创建 `/admin/approvals` 列表页：
   - 待审批列表
   - 已审批历史
   - 过滤和搜索
   - 快速批准/拒绝

2. 创建 `/admin/approvals/{id}` 详情页：
   - 请求详情
   - 会话摘要
   - 敏感信息提示
   - 完整上下文（可展开）
   - 批准/拒绝操作

3. 实时状态更新（WebSocket 或轮询）

4. E2E 测试

---

#### 任务 E3: 更新文档和 API 文档
**预计时间**: 2-3 小时  
**依赖**: 所有功能完成  
**可并行**: ❌（最后执行）

**任务内容**:
1. 更新 `ARCHITECTURE_REFACTOR_GUIDE.md`：
   - 添加审批流程章节
   - 状态机图更新
   - 数据流图更新

2. 创建 `docs/approval-flow.md`：
   - 审批流程详细说明
   - 配置指南
   - 最佳实践

3. 更新 API 文档：
   - 审批相关接口
   - 示例和错误码

4. 创建部署指南：
   - 数据库迁移步骤
   - 环境变量配置
   - 通知渠道配置

---

## 并行执行方案

### 第一轮（完全并行）

可以同时启动 **7 个任务**：

- 任务 A1: 扩展状态机
- 任务 A2: 数据模型和存储
- 任务 A3: 敏感信息检测
- 任务 A4: 会话摘要生成
- 任务 D1: 审批配置 API
- 任务 D2: 审批请求 API
- 任务 E1: 前端配置界面

### 第二轮（依赖第一轮部分完成）

等待 **A2** 完成后，可以启动 **4 个通知渠道任务**：

- 任务 B1: 飞书通知
- 任务 B2: 企业微信通知
- 任务 B3: 钉钉通知
- 任务 B4: 邮件和 Webhook

### 第三轮（依赖前两轮）

等待 **A2, A3** 完成后：

- 任务 C1: 审批检测器

等待 **C1, A4** 完成后：

- 任务 C2: 审批管理器

### 第四轮

等待 **Group B + C2** 完成后：

- 任务 C3: 审批通知器

等待 **Group C** 完成后：

- 任务 D3: 集成到 ChatHandler

### 第五轮（最后）

等待 **D2** 完成后：

- 任务 E2: 前端审批列表

等待所有功能完成后：

- 任务 E3: 文档更新

---

## 任务依赖图

```
A1 (状态机) ──────────────────────────┐
                                      │
A2 (数据模型) ────┬───> B1 (飞书)    │
                  ├───> B2 (微信)    │
                  ├───> B3 (钉钉)    ├──> C3 (通知器) ──┐
                  └───> B4 (邮件)    │                   │
                                      │                   │
A3 (敏感检测) ────┬─> C1 (检测器) ───┤                   │
                  │                   │                   │
A4 (摘要生成) ────┴─> C2 (管理器) ───┴──────────────────┴─> D3 (集成)
                                                              │
D1 (配置API) ─────> E1 (前端配置)                            │
                                                              │
D2 (请求API) ─────> E2 (前端列表) ───────────────────────────┤
                                                              │
                                         所有功能 ───────> E3 (文档)
```

---

## 估算总时间

### 按并行度计算

- **第一轮**（7 个并行）: 3-4 小时
- **第二轮**（4 个并行）: 3-4 小时
- **第三轮**（2 个串行）: 3-4 小时 + 4-5 小时 = 7-9 小时
- **第四轮**（2 个串行）: 2-3 小时 + 4-5 小时 = 6-8 小时
- **第五轮**（2 个串行）: 4-5 小时 + 2-3 小时 = 6-8 小时

**总计**: 25-37 小时（如果完全利用并行）

### 按串行计算

如果按顺序执行所有任务：**56-71 小时**

**并行加速比**: ~2.2x

---

接下来我会为每个任务创建详细的提示词，并使用 Agent 工具同时启动多个并行任务。
