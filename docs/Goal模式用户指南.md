# Goal 模式用户指南

> **版本**: v1.0  
> **最后更新**: 2026-07-19  
> **适用版本**: llm-gateway-go v1.x+

---

## 📖 目录

1. [什么是 Goal 模式](#什么是-goal-模式)
2. [5 分钟快速开始](#5-分钟快速开始)
3. [Cost Mode 选择指南](#cost-mode-选择指南)
4. [配置方式详解](#配置方式详解)
5. [功能详解](#功能详解)
6. [常见问题 FAQ](#常见问题-faq)
7. [故障排查](#故障排查)

---

## 什么是 Goal 模式

Goal 模式是 LLM Gateway 的自动会话管理系统，帮助你的 LLM 应用：

- ✅ **自动重试** - 5xx 错误自动重试，无需手动处理
- ✅ **自动继续** - 任务未完成时自动继续对话
- ✅ **循环检测** - 检测并打破无限循环
- ✅ **模型切换** - 遇到问题自动切换到备用模型
- ✅ **代码审计** - 任务完成后自动审计代码质量（aggressive 模式）
- ✅ **自动修正** - 发现问题后自动修正（aggressive 模式）

**核心价值**：让 LLM 对话更可靠、更智能、更自动化。

---

## 5 分钟快速开始

### 步骤 1：启用 Goal 模式

Goal 模式**默认启用**，无需额外配置。

### 步骤 2：选择 Cost Mode

有三种成本模式可选：

| Cost Mode | 成本增加 | 适用场景 |
|-----------|---------|---------|
| **minimal** | +20% | 预算紧张，只需基本重试 |
| **balanced** | +140% | 日常开发，需要自动继续 |
| **aggressive** | +252% | 关键任务，需要全自动质量保障 |

**推荐**：大多数场景使用 **balanced** 模式。

### 步骤 3：配置 Cost Mode

**方式 1：环境变量（全局）**

```bash
export LLM_GATEWAY_GOAL_COST_MODE=balanced
./llm-gateway
```

**方式 2：Admin API（推荐，支持租户级）**

```bash
curl -X PUT http://your-gateway:8080/admin/settings \
  -H "Authorization: Bearer YOUR_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_id": "your-tenant",
    "key": "goal.cost_mode",
    "value": "balanced"
  }'
```

**方式 3：数据库直接设置**

```sql
INSERT INTO settings (scope, tenant_id, key, value, updated_at)
VALUES ('tenant', 'your-tenant', 'goal.cost_mode', '"balanced"', NOW())
ON CONFLICT (scope, tenant_id, key) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
```

### 步骤 4：验证配置

发送一个测试请求，观察日志：

```bash
# 发送请求
curl -X POST http://your-gateway:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 查看日志
tail -f logs/llm-gateway.log | grep goal
```

你应该看到类似这样的日志：

```json
{
  "level": "debug",
  "msg": "goal_retry_config_loaded",
  "tenant_id": "your-tenant",
  "cost_mode": "balanced",
  "max_retries": 3,
  "retry_timeout_sec": 50
}
```

✅ 完成！你已经成功启用 Goal 模式。

---

## Cost Mode 选择指南

### 🔹 minimal - 预算优先

**适用场景**：
- 预算紧张的小型项目
- 低频使用的 API
- 对可靠性要求不高的场景

**功能**：
- ✅ 错误重试（2 次）
- ❌ 无自动继续
- ❌ 无代码审计

**成本**：+20%（相比无 Goal 模式）

**示例月度成本**：
- 基础成本：$100
- Goal 模式成本：$120
- 增加：$20

---

### 🔹 balanced - 日常开发（推荐）⭐

**适用场景**：
- 日常开发和测试
- 中等规模的生产应用
- 需要自动化但成本敏感

**功能**：
- ✅ 错误重试（3 次）
- ✅ 自动继续（5 次）
- ✅ 循环检测
- ✅ 模型切换（3 次）
- ❌ 无代码审计（需要可手动触发）

**成本**：+140%

**示例月度成本**：
- 基础成本：$100
- Goal 模式成本：$240
- 增加：$140

**为什么推荐**：
- 功能完整，覆盖 80% 的需求
- 成本可控，不会失控
- 自动继续功能显著提升任务完成率

---

### 🔹 aggressive - 全自动质量保障

**适用场景**：
- 关键生产任务
- 代码生成应用
- 对质量要求极高的场景
- 预算充足的项目

**功能**：
- ✅ 错误重试（5 次）
- ✅ 自动继续（7 次）
- ✅ 循环检测
- ✅ 模型切换（5 次）
- ✅ **代码审计**（LLM-based）
- ✅ **自动修正**（中高严重度问题）

**成本**：+252%

**示例月度成本**：
- 基础成本：$100
- Goal 模式成本：$352
- 增加：$252

**何时使用**：
- 生产环境的代码生成
- 高价值的 LLM 任务
- 需要自动质量保障
- 可以接受 2.5x+ 成本

---

### 决策树

```
预算是否紧张？
  ├─ 是 → minimal
  └─ 否 → 是否需要自动继续？
           ├─ 否 → minimal
           └─ 是 → 是否需要代码审计？
                    ├─ 否 → balanced ⭐ 推荐
                    └─ 是 → aggressive
```

---

## 配置方式详解

### 方式 1：环境变量（全局默认）

**优点**：
- 简单，无需 API 调用
- 适合单租户场景
- 启动时生效

**缺点**：
- 需要重启网关才能修改
- 无法区分租户

**使用方法**：

```bash
# 在启动脚本或 systemd unit 中设置
export LLM_GATEWAY_GOAL_COST_MODE=balanced

# 或在 .env 文件中
echo "LLM_GATEWAY_GOAL_COST_MODE=balanced" >> .env
```

**验证**：

```bash
# 检查环境变量
env | grep GOAL_COST_MODE

# 查看网关日志
grep "cost_mode" logs/llm-gateway.log
```

---

### 方式 2：Admin API（推荐）

**优点**：
- 支持热重载（无需重启）
- 支持租户级配置
- 可以通过脚本自动化

**缺点**：
- 需要 admin 权限

**使用方法**：

```bash
# 设置全局默认值
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "scope": "global",
    "key": "goal.cost_mode",
    "value": "balanced"
  }'

# 设置租户级覆盖
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "scope": "tenant",
    "tenant_id": "my-tenant",
    "key": "goal.cost_mode",
    "value": "aggressive"
  }'
```

**查询当前配置**：

```bash
curl -X GET "http://localhost:8080/admin/settings?key=goal.cost_mode" \
  -H "Authorization: Bearer ${ADMIN_KEY}"
```

**删除配置（恢复默认）**：

```bash
curl -X DELETE "http://localhost:8080/admin/settings?key=goal.cost_mode&tenant_id=my-tenant" \
  -H "Authorization: Bearer ${ADMIN_KEY}"
```

---

### 方式 3：数据库直接设置

**优点**：
- 适合批量配置
- 适合迁移和初始化脚本

**缺点**：
- 需要数据库访问权限
- 需要手动触发热重载

**使用方法**：

```sql
-- 设置全局默认值
INSERT INTO settings (scope, tenant_id, key, value, updated_at)
VALUES ('global', '', 'goal.cost_mode', '"balanced"', NOW())
ON CONFLICT (scope, tenant_id, key) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 设置租户级覆盖
INSERT INTO settings (scope, tenant_id, key, value, updated_at)
VALUES ('tenant', 'my-tenant', 'goal.cost_mode', '"aggressive"', NOW())
ON CONFLICT (scope, tenant_id, key) 
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

-- 查询当前配置
SELECT scope, tenant_id, key, value, updated_at 
FROM settings 
WHERE key = 'goal.cost_mode'
ORDER BY scope DESC, tenant_id;

-- 删除配置
DELETE FROM settings 
WHERE scope = 'tenant' AND tenant_id = 'my-tenant' AND key = 'goal.cost_mode';
```

**触发热重载**：

```bash
# 方式 1：通过 Admin API
curl -X POST http://localhost:8080/admin/settings/reload \
  -H "Authorization: Bearer ${ADMIN_KEY}"

# 方式 2：发送 SIGHUP 信号
kill -HUP $(pgrep llm-gateway)
```

---

### 配置优先级

配置按以下优先级生效（从高到低）：

```
1. 租户级配置（scope=tenant, tenant_id=<id>）
   ↓
2. 全局配置（scope=global）
   ↓
3. 环境变量（LLM_GATEWAY_GOAL_COST_MODE）
   ↓
4. 默认值（balanced）
```

**示例**：

```
环境变量: minimal
全局配置: balanced
租户 A 配置: aggressive
租户 B 配置: （无）

结果：
- 租户 A 使用: aggressive（租户级覆盖）
- 租户 B 使用: balanced（全局配置）
- 其他租户: balanced（全局配置）
```

---

## 功能详解

### Phase 1：错误重试

**功能**：5xx 错误自动重试，提升可靠性。

**配置**：

| Cost Mode | 重试次数 | 单次延迟 | 总超时 |
|-----------|---------|---------|--------|
| minimal | 2 | 指数退避 | 40s |
| balanced | 3 | 指数退避 | 50s |
| aggressive | 5 | 指数退避 | 120s |

**工作原理**：

```
请求 → LLM 返回 5xx 错误
  ↓
等待 100ms（第1次重试前）
  ↓
重试 → 仍然 5xx
  ↓
等待 200ms（第2次重试前）
  ↓
重试 → 成功 → 返回结果
```

**可重试的错误类型**：
- 5xx 服务器错误
- timeout 超时
- rate_limit 限流
- no_candidates 无候选（模型不可用）
- overloaded 过载

**日志示例**：

```json
{
  "level": "info",
  "msg": "goal_retry_attempt",
  "request_id": "req_abc123",
  "attempt": 2,
  "max_retries": 3,
  "delay_ms": 200,
  "error": "upstream_5xx"
}
```

---

### Phase 2：自动继续（Auto-Continue）

**功能**：任务未完成时自动继续对话。

**配置**：

| Cost Mode | 自动继续 | 最大次数 | 完成置信度 |
|-----------|---------|---------|-----------|
| minimal | ❌ | 0 | - |
| balanced | ✅ | 5 | 0.75 |
| aggressive | ✅ | 7 | 0.70 |

**工作原理**：

```
用户：实现用户注册功能

LLM响应：已创建 User 模型...（未完成）
  ↓
CompletionDetector：置信度 0.3（未完成）
  ↓
自动继续：请继续完成
  ↓
LLM响应：已创建注册路由...（未完成）
  ↓
CompletionDetector：置信度 0.6（未完成）
  ↓
自动继续：请继续
  ↓
LLM响应：已完成所有测试，任务完成
  ↓
CompletionDetector：置信度 0.95（完成）✓
```

**完成检测策略**（三种）：

1. **Structured Output**: 检测 LLM 返回的 `status: "completed"`
2. **Keywords**: 匹配"任务完成"、"done"、"finished"
3. **LLM Analysis**: 调用 LLM 分析并返回信心分数

**日志示例**：

```json
{
  "level": "info",
  "msg": "goal_auto_continue",
  "session_id": "sess_xyz",
  "continue_count": 2,
  "max_continue": 5,
  "completion_confidence": 0.60
}
```

---

### Phase 2：循环检测与模型切换

**功能**：检测无限循环并自动切换模型。

**配置**：

| Cost Mode | 循环检测 | 循环阈值 | 模型切换次数 |
|-----------|---------|---------|-------------|
| minimal | ❌ | - | 0 |
| balanced | ✅ | 2 | 3 |
| aggressive | ✅ | 3 | 5 |

**工作原理**：

```
LLM响应：让我思考一下...
  ↓
LLM响应：让我思考一下...（相同内容）
  ↓
LLM响应：让我思考一下...（第3次）
  ↓
LoopDetector：检测到循环（hash 碰撞 3 次）
  ↓
ModelSwitcher：切换到备用模型（claude-sonnet → gpt-4）
  ↓
LLM响应：[不同的内容]（成功打破循环）
```

**日志示例**：

```json
{
  "level": "warn",
  "msg": "goal_loop_detected",
  "session_id": "sess_xyz",
  "loop_count": 3,
  "switching_model": true,
  "from_model": "claude-sonnet-3.5",
  "to_model": "gpt-4"
}
```

---

### Phase 2：代码审计与自动修正（aggressive 专属）

**功能**：任务完成后自动审计代码并修正问题。

**配置**：

| Cost Mode | 代码审计 | 自动修正 | 修正严重度 |
|-----------|---------|---------|-----------|
| minimal | ❌ | ❌ | - |
| balanced | ❌ | ❌ | - |
| aggressive | ✅ | ✅ | medium |

**工作原理**：

```
LLM响应：任务完成
  ↓
CompletionDetector：确认完成（置信度 0.95）
  ↓
AuditHook：触发代码审计
  ↓
LLM（审计模型）：发现 3 个问题
  - [HIGH] 缺少错误处理
  - [MEDIUM] 变量命名不规范
  - [LOW] 缺少注释
  ↓
AutoFix：修正 HIGH 和 MEDIUM 问题
  ↓
验证：重新编译 + 运行测试
  ↓
成功 → 返回修正后的代码
```

**审计维度**：
- 错误处理完整性
- 代码规范符合度
- 安全漏洞
- 性能问题
- 可维护性

**修正严重度**：

| 严重度 | aggressive 模式 | 说明 |
|--------|----------------|------|
| HIGH | ✅ 自动修正 | 严重问题（安全、错误处理） |
| MEDIUM | ✅ 自动修正 | 中等问题（规范、性能） |
| LOW | ❌ 仅报告 | 轻微问题（注释、格式） |

**日志示例**：

```json
{
  "level": "info",
  "msg": "goal_audit_completed",
  "session_id": "sess_xyz",
  "issues_found": 3,
  "issues_fixed": 2,
  "issues_unfixed": 1,
  "files_changed": ["main.go", "handler.go"]
}
```

---

## 常见问题 FAQ

### Q1：如何查看当前使用的 Cost Mode？

**A1**：查看日志或调用 Admin API。

```bash
# 方式 1：查看日志
tail -f logs/llm-gateway.log | grep "cost_mode"

# 方式 2：Admin API
curl -X GET "http://localhost:8080/admin/settings?key=goal.cost_mode" \
  -H "Authorization: Bearer ${ADMIN_KEY}"
```

---

### Q2：修改配置后需要重启吗？

**A2**：不需要（如果使用 Admin API 或数据库）。

- ✅ Admin API：立即生效
- ✅ 数据库 + 热重载：手动触发后生效
- ❌ 环境变量：需要重启

---

### Q3：为什么 balanced 模式没有代码审计？

**A3**：成本与功能的平衡。

代码审计（LLM-based）成本较高：
- 每次审计：1000-2000 tokens
- aggressive 模式预算：500K tokens/session
- balanced 模式预算：100K tokens/session

如果 balanced 启用审计，可能快速耗尽预算。

**替代方案**：
- 手动触发审计（通过 API）
- 升级到 aggressive 模式
- 等待未来版本的"轻量级审计"（基于工具，非 LLM）

---

### Q4：如何禁用 Goal 模式？

**A4**：Goal 模式是网关核心功能，不建议禁用。

如果确实需要：
- 使用 minimal 模式（成本仅 +20%）
- 或通过代码层面禁用（需要修改源码）

---

### Q5：aggressive 模式成本太高怎么办？

**A5**：三种优化策略。

1. **按场景切换**：
   - 日常开发：balanced
   - 生产部署前：aggressive

2. **设置预算限制**：
   ```bash
   curl -X PUT http://localhost:8080/admin/settings \
     -H "Authorization: Bearer ${ADMIN_KEY}" \
     -d '{
       "key": "goal.session_token_budget",
       "value": 200000
     }'
   ```

3. **启用自动降级**：
   ```bash
   curl -X PUT http://localhost:8080/admin/settings \
     -H "Authorization: Bearer ${ADMIN_KEY}" \
     -d '{
       "key": "goal.downgrade_on_budget",
       "value": true
     }'
   ```
   当预算接近上限时，自动降级到 balanced。

---

### Q6：如何为不同项目使用不同的 Cost Mode？

**A6**：使用租户级配置。

```bash
# 项目 A（关键任务）：aggressive
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{
    "tenant_id": "project-a",
    "key": "goal.cost_mode",
    "value": "aggressive"
  }'

# 项目 B（日常开发）：balanced
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{
    "tenant_id": "project-b",
    "key": "goal.cost_mode",
    "value": "balanced"
  }'
```

---

### Q7：代码审计支持哪些语言？

**A7**：所有主流语言。

因为使用 LLM-based 审计（而非语言特定工具），支持：
- ✅ Go / Python / TypeScript / JavaScript
- ✅ Java / C# / Rust / C++
- ✅ PHP / Ruby / Swift / Kotlin
- ✅ 任何 LLM 能理解的语言

---

### Q8：自动修正会破坏我的代码吗？

**A8**：有严格的安全保障。

1. **修正前自动备份**：`git stash`
2. **修正后强制验证**：重新编译 + 测试
3. **验证失败立即回滚**：`git stash pop`
4. **只修正"安全"类型**：格式化、未使用变量、标准错误处理
5. **不修正逻辑**：类型错误、业务逻辑由 LLM 报告但不自动改

**日志追踪**：
```json
{
  "msg": "goal_fix_rollback",
  "reason": "verification_failed",
  "files_restored": ["main.go"]
}
```

---

## 故障排查

### 问题 1：配置不生效

**症状**：修改配置后，日志仍显示旧值。

**原因**：
1. 使用了环境变量（需要重启）
2. 配置优先级被覆盖
3. Settings 系统未热重载

**解决**：

```bash
# 1. 检查配置优先级
curl -X GET "http://localhost:8080/admin/settings?key=goal.cost_mode&debug=true" \
  -H "Authorization: Bearer ${ADMIN_KEY}"

# 2. 触发热重载
curl -X POST http://localhost:8080/admin/settings/reload \
  -H "Authorization: Bearer ${ADMIN_KEY}"

# 3. 查看日志确认
tail -f logs/llm-gateway.log | grep "cost_mode"
```

---

### 问题 2：重试次数不符合预期

**症状**：配置了 aggressive（5次重试），但只重试了 2 次。

**原因**：
1. 非 5xx 错误（不可重试）
2. 达到总超时限制
3. LLM 返回成功（不需要重试）

**解决**：

```bash
# 查看详细日志
tail -f logs/llm-gateway.log | grep -E "retry|5xx"

# 确认错误类型
# 可重试：5xx, timeout, rate_limit, no_candidates, overloaded
# 不可重试：4xx, auth_error, invalid_request
```

---

### 问题 3：aggressive 模式无代码审计

**症状**：使用 aggressive 但任务完成后没有审计。

**原因**：
1. 未检测到"任务完成"
2. 审计配置被禁用
3. Session 不是 Goal 模式

**解决**：

```bash
# 1. 检查 goal.audit_enabled
curl -X GET "http://localhost:8080/admin/settings?key=goal.audit_enabled" \
  -H "Authorization: Bearer ${ADMIN_KEY}"

# 2. 查看完成检测日志
tail -f logs/llm-gateway.log | grep "completion_detected"

# 3. 确认 Session 类型
tail -f logs/llm-gateway.log | grep "goal_session_created"
```

---

### 问题 4：成本超出预期

**症状**：使用 balanced 模式，但成本增加超过 140%。

**原因**：
1. 大量重试（上游不稳定）
2. 循环过多（任务难度高）
3. 自动继续次数过多

**解决**：

```bash
# 1. 查看 Metrics
curl http://localhost:9090/metrics | grep goal

# 关注指标：
# - goal_retry_total：总重试次数
# - goal_continue_total：总继续次数
# - goal_loop_detected_total：循环检测次数

# 2. 设置预算限制
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{
    "key": "goal.session_token_budget",
    "value": 50000
  }'

# 3. 启用预算告警
curl -X PUT http://localhost:8080/admin/settings \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -d '{
    "key": "goal.cost_alert_threshold",
    "value": 0.80
  }'
```

---

### 问题 5：如何查看审计历史？

**症状**：想查看之前的审计结果。

**解决**：

```bash
# 方式 1：查看日志
grep "goal_audit" logs/llm-gateway.log | tail -20

# 方式 2：数据库查询（如果启用了 HistoryStore）
psql -h localhost -U postgres -d llm_gateway -c "
  SELECT session_id, created_at, issues_found, issues_fixed 
  FROM goal_audit_history 
  ORDER BY created_at DESC 
  LIMIT 10;
"
```

---

## 获取帮助

### 文档

- 设计文档：`docs/会话优化v2/16-Goal模式会话持续机制设计方案.md`
- 实施报告：`docs/会话优化v2/27-Phase0-2完整实现总结.md`
- 配置说明：`settings/goal_specs.go`

### 日志

```bash
# 实时查看 Goal 相关日志
tail -f logs/llm-gateway.log | grep goal

# 查看错误日志
tail -f logs/llm-gateway.log | grep -E "error|ERROR" | grep goal
```

### Metrics

```bash
# Prometheus Metrics
curl http://localhost:9090/metrics | grep goal

# 关键指标：
# - goal_retry_total
# - goal_continue_total
# - goal_loop_detected_total
# - goal_audit_triggered_total
# - goal_fix_applied_total
```

### 联系支持

- GitHub Issues: [提交问题](https://github.com/your-org/llm-gateway-go/issues)
- 飞书群：联系管理员加入

---

**版本历史**：
- v1.0 (2026-07-19): 初始版本

**维护者**：Infrastructure Team
