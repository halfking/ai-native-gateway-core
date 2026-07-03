# ADR-001: 使用 credentials.plan_type 作为计费模式的单一真相源

**状态：** 已接受  
**日期：** 2026-07-03  
**决策者：** AI 运维团队  
**相关文档：** [计费模式标准化方案](./billing-mode-standardization.md)

---

## 背景

在 LLM Gateway 系统中，存在两个计费相关的字段：
- `credentials.plan_type` - 凭据级别的套餐类型
- `credential_model_bindings.billing_mode` - 模型绑定级别的计费模式

这导致了以下问题：
1. **数据不一致**：两个字段可能存储不同的值（例如 token vs per_token）
2. **维护复杂**：需要同步更新两个地方
3. **路由错误**：不一致导致凭据被错误排除在路由候选之外
4. **故障频发**：2026-07-03 发生了因计费模式不匹配导致的全局故障

## 决策

我们决定：
1. **`credentials.plan_type` 作为 SSOT（Single Source of Truth）**
2. **`credential_model_bindings.billing_mode` 作为派生字段**，从 plan_type 自动派生
3. **统一命名规范**：
   - `token` → `per_token`（标准化别名）
   - 其他值保持一致（token_plan, code_plan, agent_plan, monthly, free）

### 派生规则

```go
func DeriveBillingMode(planType string) string {
    if planType == "" || planType == "token" {
        return "per_token"  // 标准化名称
    }
    return planType  // 直通
}
```

## 理由

### 为什么选择 plan_type 作为 SSOT？

1. **语义清晰**：plan_type 描述的是凭据的本质属性（套餐类型）
2. **粒度合适**：凭据级别的属性，不应该在每个模型绑定上重复存储
3. **易于管理**：只需在凭据创建时设置一次
4. **业务对齐**：与财务和采购部门的套餐分类一致

### 为什么保留 billing_mode？

虽然是派生字段，但保留它有以下好处：
1. **查询性能**：避免每次路由查询都 JOIN credentials 表
2. **向后兼容**：现有视图和查询可以继续使用
3. **审计追踪**：通过 `plan_type_origin` 标记数据来源
4. **渐进迁移**：允许逐步废弃，不需要立即修改所有代码

## 后果

### 正面影响
- ✅ 数据一致性得到保证
- ✅ 减少维护负担
- ✅ 路由逻辑更可靠
- ✅ 故障排查更简单

### 负面影响
- ⚠️ 需要修改现有的模型发现逻辑
- ⚠️ 需要运行数据修正脚本
- ⚠️ 团队需要理解新的数据模型

### 风险缓解
1. **数据修正脚本**：已创建并测试（见 `docs/billing-mode-standardization.md`）
2. **监控告警**：添加数据一致性检查（见 `docs/monitoring-alerts.md`）
3. **文档完善**：提供详细的故障排查手册（见 `docs/troubleshooting-guide.md`）

## 实施

### 已完成
- [x] 添加 `credentials.plan_type` 列（迁移 327）
- [x] 添加 `credential_model_bindings.plan_type_origin` 列
- [x] 修正所有不一致的数据（1036 条记录）
- [x] 更新模型发现逻辑（`modelcatalog/upsert.go`）
- [x] 创建监控告警规则

### 待完成（长期）
- [ ] 废弃 `billing_mode` 列（6 个月后）
- [ ] 迁移所有查询使用 `plan_type`
- [ ] 简化视图定义

## 替代方案

### 方案 A：使用 billing_mode 作为 SSOT（已拒绝）
- **理由**：billing_mode 是技术细节，plan_type 是业务概念
- **缺点**：与财务系统不对齐，语义不清晰

### 方案 B：完全删除 billing_mode（已拒绝）
- **理由**：短期内改动太大，风险高
- **缺点**：需要修改大量查询和视图，影响范围广

### 方案 C：引入新的计费配置表（已拒绝）
- **理由**：过度设计，当前数据量不需要
- **缺点**：增加系统复杂度

## 参考资料

- [计费模式标准化方案](./billing-mode-standardization.md)
- [故障报告 2026-07-03](./incident-report-20260703.md)
- [迁移文件 327](../migrations/327_credential_plan_type_full.sql)
- [模型发现代码](../modelcatalog/upsert.go)

---

## 更新历史

| 日期 | 版本 | 变更 | 作者 |
|------|------|------|------|
| 2026-07-03 | 1.0 | 初始版本 | AI 运维团队 |

---

# ADR-002: 计费模式命名标准化

**状态：** 已接受  
**日期：** 2026-07-03  
**决策者：** AI 运维团队  
**依赖：** ADR-001

---

## 背景

系统中存在多种计费模式的名称变体：
- `token` vs `per_token`
- `token_plan` vs `tokenplan`
- 不同代码模块使用不同的名称

## 决策

统一使用以下标准名称：

| 标准名称 | 含义 | 别名（废弃） |
|---------|------|-------------|
| `per_token` | 按 token 计费 | token |
| `token_plan` | 令牌包套餐 | tokenplan |
| `code_plan` | 代码包套餐 | codeplan |
| `agent_plan` | Agent 包套餐 | agentplan |
| `monthly` | 月付费 | - |
| `free` | 免费池 | - |

### 命名规则
1. 使用 snake_case（下划线分隔）
2. 避免缩写（除非是行业标准，如 token）
3. 描述性优先（per_token 比 token 更清晰）

## 理由

1. **一致性**：减少混淆，提高代码可读性
2. **可维护性**：统一命名方便搜索和替换
3. **可扩展性**：为未来新增计费模式预留清晰的命名空间

## 实施

- [x] 数据库标准化：所有 `token` 别名已统一为 `per_token`
- [x] 代码标准化：`DeriveBillingMode` 函数已更新
- [ ] 文档标准化：更新所有文档使用标准名称

---

# ADR-003: 凭据类型自动检测与验证

**状态：** 提议中  
**日期：** 2026-07-03  
**决策者：** 待定  

---

## 背景

2026-07-03 故障中发现，credential_id=11 的 plan_type 配置错误：
- 配置为：`token`
- 实际为：`code_plan`（火山方舟代码计划凭据）

上游 API 返回错误："不支持代码计划功能"，但系统无法自动检测和修正。

## 问题

1. 凭据类型依赖人工配置，容易出错
2. 配置错误只在运行时被发现，影响用户
3. 没有自动验证机制

## 提议

实现凭据类型自动检测和验证机制：

### 方案 1：探测端点（Recommended）
```go
// 在凭据创建/更新时自动探测
func DetectPlanType(credential Credential) (string, error) {
    // 1. 调用上游 /v1/models 端点
    models, err := fetchModels(credential)
    if err != nil {
        return "", err
    }
    
    // 2. 发送测试请求
    testReq := createTestRequest()
    resp, err := sendRequest(credential, testReq)
    
    // 3. 根据响应判断类型
    if strings.Contains(resp.Error, "coding plan") {
        return "code_plan", nil
    } else if strings.Contains(resp.Error, "token plan") {
        return "token_plan", nil
    }
    
    return "token", nil  // 默认为按量付费
}
```

### 方案 2：错误学习
```go
// 根据运行时错误自动修正
func LearnFromError(credential Credential, error APIError) {
    if strings.Contains(error.Message, "does not support the coding plan") {
        // 自动标记凭据为 code_plan
        updateCredentialPlanType(credential.ID, "code_plan")
        logAudit("auto_corrected_plan_type", credential.ID, "code_plan")
    }
}
```

## 优缺点

### 方案 1：探测端点
- ✅ 在凭据创建时就能发现问题
- ✅ 避免影响用户请求
- ❌ 增加凭据创建时间
- ❌ 需要消耗 API 配额

### 方案 2：错误学习
- ✅ 零额外成本
- ✅ 实现简单
- ❌ 第一批用户请求会失败
- ❌ 依赖错误信息格式

## 决策

**待定** - 需要进一步讨论权衡

## 后续步骤

1. 团队讨论优先级
2. 设计详细的实现方案
3. 在测试环境验证
4. 编写测试用例

---

**ADR 索引维护者：** AI 运维团队  
**更新频率：** 有重大架构决策时更新
