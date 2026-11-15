# llm-gateway-go Casdoor审批集成 - 实施检查清单

**版本**: v1.0  
**日期**: 2026-11-15  
**用途**: 实施前检查和部署验证

---

## 前置条件检查

### 1. Casdoor配置

- [ ] **Casdoor应用已创建**
  - 应用名称: `llm-gateway-bff`
  - 应用类型: Web Application
  - 记录clientId: __________________
  - 记录clientSecret: __________________

- [ ] **Webhook配置**
  - 回调URL: `https://llm-gateway.kx.com/api/webhooks/casdoor/approval`
  - 生成callbackSecret: __________________
  - 设置callbackKid: `llm-gateway-prod-1`

- [ ] **审批流程已创建**
  - 流程名称: `admin/sensitive-content-approval`
  - 流程状态: Published
  - 审批步骤: 至少1个步骤
  - 审批人: 已配置

- [ ] **测试用户准备**
  - 测试申请人账号: __________________
  - 测试审批人账号: __________________

### 2. 数据库准备

- [ ] **迁移SQL已准备**
  - 文件位置: `db/migrations/202611150001_approval_casdoor_mapping.sql`
  - SQL已审阅: 是/否

- [ ] **数据库备份**
  - 开发环境备份: __________________
  - 测试环境备份: __________________
  - 生产环境备份: __________________

- [ ] **数据库权限**
  - CREATE TABLE权限: 是/否
  - CREATE INDEX权限: 是/否
  - ALTER TABLE权限: 是/否

### 3. 代码准备

- [ ] **依赖包已安装**
  ```bash
  go mod tidy
  go mod download
  ```

- [ ] **单元测试通过**
  ```bash
  go test ./internal/approval/... -v
  go test ./internal/casdoor/... -v
  ```
  - 通过测试数: ____/____

- [ ] **代码审查完成**
  - 审查人: __________________
  - 审查意见: __________________

---

## 实施步骤

### Step 1: 数据库迁移 (预计10分钟)

#### 1.1 开发环境

```bash
# 连接数据库
psql -h localhost -U postgres -d llm_gateway_dev

# 运行迁移
\i db/migrations/202611150001_approval_casdoor_mapping.sql

# 验证表创建
\d approval_casdoor_mapping

# 验证索引
\di approval_casdoor_mapping*
```

- [ ] 表创建成功
- [ ] 索引创建成功
- [ ] RLS policy创建成功

#### 1.2 测试环境

```bash
psql -h test-db-host -U postgres -d llm_gateway_test \
  < db/migrations/202611150001_approval_casdoor_mapping.sql
```

- [ ] 迁移成功
- [ ] 验证通过

#### 1.3 生产环境 (稍后执行)

- [ ] 迁移计划审批: __________________
- [ ] 维护窗口预约: __________________

---

### Step 2: 配置文件更新 (预计15分钟)

#### 2.1 添加配置结构

**文件**: `internal/config/config.go`

- [ ] 添加 `ApprovalConfig` 结构体
- [ ] 添加到主 `Config` 结构体
- [ ] 参考: `examples/config_approval.go`

#### 2.2 更新配置文件

**文件**: `config/config.yaml`

```yaml
approval:
  mode: hybrid
  native:
    enabled: true
    timeout_minutes: 30
  casdoor:
    enabled: ${CASDOOR_APPROVAL_ENABLED}
    endpoint: ${CASDOOR_ENDPOINT}
    client_id: ${CASDOOR_CLIENT_ID}
    client_secret: ${CASDOOR_CLIENT_SECRET}
    callback_secret: ${CASDOOR_CALLBACK_SECRET}
    callback_kid: ${CASDOOR_CALLBACK_KID}
    callback_url: ${CASDOOR_CALLBACK_URL}
    task_owner: admin
    default_flow: "@admin/sensitive-content-approval"
    ttl_minutes: 30
  router:
    high_sensitivity_threshold: 0.8
    require_casdoor_keywords:
      - "财务数据"
      - "薪资信息"
      - "商业机密"
    cross_department_requires_casdoor: true
```

- [ ] 配置文件已更新
- [ ] 环境变量占位符已设置

#### 2.3 设置环境变量

**文件**: `.env` (不要提交到git)

```bash
CASDOOR_APPROVAL_ENABLED=true
CASDOOR_ENDPOINT=https://casdoor.kx.com
CASDOOR_CLIENT_ID=llm-gateway-bff
CASDOOR_CLIENT_SECRET=your_secret_here
CASDOOR_CALLBACK_SECRET=your_webhook_secret_here
CASDOOR_CALLBACK_KID=llm-gateway-prod-1
CASDOOR_CALLBACK_URL=https://llm-gateway-dev.kx.com/api/webhooks/casdoor/approval
```

- [ ] 所有变量已设置
- [ ] 凭据已验证（非占位符）

---

### Step 3: 主程序集成 (预计30分钟)

#### 3.1 初始化函数

**文件**: `cmd/gateway/main.go`

- [ ] 添加 `initApprovalSystem()` 函数
- [ ] 参考: `examples/approval_integration.go`
- [ ] 在main()中调用

#### 3.2 Webhook处理器

**文件**: `api/webhook_handler.go` (或新建)

- [ ] 创建 `ApprovalWebhookHandler` 结构体
- [ ] 实现 `HandleCasdoorApprovalWebhook()` 方法
- [ ] 参考: `examples/webhook_handler.go`

#### 3.3 路由注册

**文件**: `cmd/gateway/routes.go` (或路由配置文件)

```go
mux.HandleFunc("/api/webhooks/casdoor/approval",
    handlers.ApprovalWebhook.HandleCasdoorApprovalWebhook,
)
```

- [ ] 路由已注册
- [ ] HTTP方法限制为POST

---

### Step 4: 审批创建点修改 (预计20分钟)

**查找现有代码**:
```bash
grep -r "ApprovalManager.*Insert\|CreateApproval" --include="*.go" .
```

**修改示例**:

```go
// Old code:
// approval, err := approvalManager.CreateApproval(...)

// New code:
approval, err := approvalRouter.CreateApproval(ctx, &approval.CreateApprovalRequest{
    RequestID:     requestID,
    SessionID:     sessionID,
    TenantID:      tenantID,
    UserID:        userID,
    CasdoorUserID: getCasdoorUserID(user),
    DetectResult: &approval.DetectResult{
        Triggers:         detectResult.Triggers,
        SensitivityScore: detectResult.Score,
        Summary:          detectResult.Summary,
    },
    CreatedAt: time.Now(),
})
```

- [ ] 审批创建点已找到: __________________
- [ ] 代码已修改
- [ ] CasdoorUserID映射已实现

---

### Step 5: 编译和测试 (预计30分钟)

#### 5.1 编译

```bash
go build -o bin/llm-gateway cmd/gateway/main.go
```

- [ ] 编译成功
- [ ] 无警告

#### 5.2 单元测试

```bash
go test ./... -v -cover
```

- [ ] 所有测试通过
- [ ] 覆盖率: ____%

#### 5.3 本地启动

```bash
./bin/llm-gateway
```

**检查日志**:
- [ ] 看到: "approval system initialized"
- [ ] 看到: mode=hybrid
- [ ] 看到: casdoor_enabled=true

---

## 功能验证

### Scenario 1: 低敏感度请求 → Native审批

**测试步骤**:
1. 发送低敏感度请求
   ```bash
   curl -X POST http://localhost:8080/api/sessions/test/execute \
     -H "Content-Type: application/json" \
     -d '{"prompt":"写一个hello world程序"}'
   ```

2. 检查日志
   ```bash
   grep "approval created" logs/app.log
   ```

**验证点**:
- [ ] 审批已创建
- [ ] provider=native
- [ ] 响应时间 < 5秒

---

### Scenario 2: 高敏感度请求 → Casdoor审批

**测试步骤**:
1. 发送高敏感度请求
   ```bash
   curl -X POST http://localhost:8080/api/sessions/test/execute \
     -H "Content-Type: application/json" \
     -d '{"prompt":"查询所有员工的薪资信息"}'
   ```

2. 检查日志
   ```bash
   grep "approval created" logs/app.log | grep casdoor
   ```

3. 登录Casdoor Console验证
   - URL: https://casdoor.kx.com/management/approval-tasks
   - 查找新创建的任务

**验证点**:
- [ ] 审批已创建
- [ ] provider=casdoor
- [ ] Casdoor任务可见
- [ ] 任务标题正确

---

### Scenario 3: Webhook回调

**测试步骤**:
1. 在Casdoor Console中审批任务
2. 检查llm-gateway日志
   ```bash
   grep "received casdoor webhook" logs/app.log
   ```

3. 检查审批状态更新
   ```bash
   psql -d llm_gateway -c "SELECT * FROM approval_casdoor_mapping WHERE status='approved';"
   ```

**验证点**:
- [ ] Webhook收到
- [ ] 签名验证通过
- [ ] 审批状态已更新
- [ ] 会话已恢复（如果实现）

---

### Scenario 4: Casdoor降级

**测试步骤**:
1. 停止Casdoor或设置错误endpoint
   ```bash
   export CASDOOR_ENDPOINT=https://invalid-endpoint
   ```

2. 重启服务
3. 发送高敏感度请求

**验证点**:
- [ ] 看到降级日志
- [ ] 请求仍然成功
- [ ] provider=native (降级)
- [ ] 降级指标递增

---

## 监控验证

### Prometheus指标

```bash
curl http://localhost:9090/metrics | grep llmgw_approval
```

**期望指标**:
- [ ] `llmgw_approval_routing_total{provider="native",sensitivity="low"}`
- [ ] `llmgw_approval_routing_total{provider="casdoor",sensitivity="high"}`
- [ ] `llmgw_approval_creation_total{provider="native",status="success"}`
- [ ] `llmgw_approval_degradation_total`

---

## 生产部署检查清单

### 准备阶段

- [ ] **代码审查完成**
  - 审查人: __________________
  - 审查通过: 是/否

- [ ] **开发环境验证通过**
  - 所有场景测试: 是/否
  - 性能测试: 是/否

- [ ] **测试环境验证通过**
  - E2E测试: 是/否
  - 压力测试: 是/否

- [ ] **文档完成**
  - 集成指南: 是/否
  - 运维手册: 是/否
  - 回滚计划: 是/否

### 部署阶段

- [ ] **维护窗口**
  - 时间: __________________
  - 负责人: __________________

- [ ] **数据库迁移**
  - 备份完成: 是/否
  - 迁移执行: 是/否
  - 验证通过: 是/否

- [ ] **应用部署**
  - 配置更新: 是/否
  - 环境变量: 是/否
  - 服务重启: 是/否

- [ ] **灰度发布**
  - 10%流量: 是/否
  - 50%流量: 是/否
  - 100%流量: 是/否

### 验证阶段

- [ ] **功能验证**
  - Native审批: 是/否
  - Casdoor审批: 是/否
  - Webhook回调: 是/否

- [ ] **监控验证**
  - 指标采集: 是/否
  - 告警测试: 是/否
  - Dashboard: 是/否

- [ ] **性能验证**
  - 响应时间: ___ms (目标<2s)
  - 错误率: ___% (目标<1%)
  - CPU使用: ___% (目标<80%)

---

## 回滚计划

### 快速回滚 (5分钟)

**步骤**:
1. 修改配置禁用Casdoor
   ```yaml
   approval:
     mode: native
   ```

2. 重启服务
   ```bash
   systemctl restart llm-gateway
   ```

- [ ] 回滚步骤已测试

### 完全回滚 (30分钟)

**步骤**:
1. 恢复代码到集成前版本
2. 回滚数据库（可选）
   ```sql
   DROP TABLE IF EXISTS approval_casdoor_mapping;
   ```

- [ ] 回滚步骤已文档化

---

## 签字确认

**开发负责人**: __________________ 日期: __________

**测试负责人**: __________________ 日期: __________

**运维负责人**: __________________ 日期: __________

**项目经理**: __________________ 日期: __________

---

**检查清单版本**: v1.0  
**最后更新**: 2026-11-15
