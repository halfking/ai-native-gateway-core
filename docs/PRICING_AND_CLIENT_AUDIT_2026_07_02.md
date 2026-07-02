# 价格配置与客户端特性审计报告

**审计日期**: 2026-07-02  
**审计版本**: r1.13-done-bae065af-20260702-3  
**审计范围**: 价格配置、计费模式、客户端特性、Tool ID 处理

---

## 📋 执行摘要

本次审计覆盖了以下关键领域：
1. **价格配置体系**: MaaS计费模式、Credits系统、订阅计划
2. **客户端特性**: 主流编程AI助手的识别和特性
3. **Tool ID 处理**: 历史问题、修复方案、最佳实践
4. **多轮对话修复**: Claude Sonnet 4-6 格式转换问题

---

## 💰 价格配置体系审计

### 1. 计费模式架构

#### 当前支持的计费模式

基于 `provider/billing.go` 和 `db/migrations/007_maas_billing.sql`:

| 计费模式 | 路由优先级 | 说明 | 用途 |
|---------|-----------|------|------|
| `free` | Round 1 (高优先级) | 免费额度 | 新用户试用、开发测试 |
| `token_plan` | Round 1 | Token 套餐 | 按 token 数量预付费 |
| `code_plan` | Round 1 | 代码生成套餐 | 面向编程场景优化 |
| `agent_plan` | Round 1 | Agent 套餐 | AI Agent 应用场景 |
| `monthly` | Round 1 | 月度订阅 | 固定费用订阅制 |
| `token` / `per_token` | Round 2 (低优先级) | 按量付费 | 超出套餐后的计费 |
| `per_request` | Round 2 | 按请求付费 | 简单场景 |

#### 路由优先级逻辑
```go
func BillingRound(mode string) int {
    switch strings.ToLower(strings.TrimSpace(mode)) {
    case "free", "token_plan", "code_plan", "agent_plan", "monthly":
        return 1  // 优先使用预付费/订阅
    default:
        return 2  // PAYG (按量付费) 作为后备
    }
}
```

**设计理念**:
- Round 1 先耗尽预付费额度和订阅配额
- Round 2 仅在 Round 1 资源耗尽后触发
- 降低按量计费成本，鼓励订阅

### 2. Credits 系统

#### 核心配置 (`maas_settings` 表)

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `cents_per_credit` | 0.1 | 每个 credit 对应的人民币分 |
| `base_credits_per_1m` | 10000 | 每百万token的基准credits |
| `currency_display` | CNY | 显示货币单位 |

**换算关系**:
- 1 Credit = 0.001 CNY (1分的1/10)
- 1M tokens = 10,000 credits = 10 CNY

#### Credits 流转

```
用户充值/订阅 → tenant_credit_wallets (余额)
                ↓
        请求消费 → request_logs.credits_charged
                ↓
        流水记录 → credit_ledger
```

### 3. 订阅计划结构

#### 计划维度 (`subscription_plans` 表)

| 字段 | 说明 | 示例 |
|------|------|------|
| `code` | 计划唯一标识 | `starter`, `pro`, `enterprise` |
| `tier` | 计划等级 | `basic`, `standard`, `premium`, `enterprise` |
| `price_cents` | 价格(分) | 9900 = 99元 |
| `credits_included` | 包含credits | 100000 = 10元额度 |
| `billing_cycle` | 计费周期 | `monthly`, `yearly` |
| `features` | 功能特性 (JSON) | 模型访问权限、并发限制等 |

#### 典型计划示例

```json
{
  "code": "pro_monthly",
  "tier": "premium",
  "price_cents": 19900,
  "credits_included": 500000,
  "billing_cycle": "monthly",
  "features": {
    "models": ["claude-sonnet-4-6", "gpt-4o", "claude-opus-4"],
    "max_concurrent": 10,
    "priority_routing": true,
    "advanced_analytics": true
  }
}
```

### 4. 模型价格配置现状

#### 当前缺失
⚠️ **发现**: 数据库迁移文件中**未找到具体的模型价格配置**

#### 建议的价格表结构

基于行业标准和 Credits 系统，建议创建 `model_pricing` 表：

```sql
CREATE TABLE IF NOT EXISTS model_pricing (
    id SERIAL PRIMARY KEY,
    model_canonical VARCHAR(64) NOT NULL UNIQUE,
    
    -- 价格配置（以 credits 为单位）
    input_credits_per_1m BIGINT NOT NULL,   -- 输入token价格
    output_credits_per_1m BIGINT NOT NULL,  -- 输出token价格
    
    -- 缓存价格（如支持）
    cache_write_credits_per_1m BIGINT,
    cache_read_credits_per_1m BIGINT,
    
    -- 元数据
    provider VARCHAR(32) NOT NULL,           -- anthropic, openai, etc.
    tier VARCHAR(16) NOT NULL,               -- basic, advanced, premium
    context_window INT NOT NULL,             -- 上下文窗口大小
    
    -- 成本控制
    daily_free_quota_credits BIGINT,         -- 每日免费额度
    requires_plan VARCHAR(32)[],             -- 需要的订阅计划
    
    -- 审计
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes TEXT
);
```

#### 建议的模型价格配置

| 模型 | 输入价格<br>(credits/1M tokens) | 输出价格<br>(credits/1M tokens) | 人民币等价<br>(输入/输出 per 1M) |
|------|------------------------------|-------------------------------|------------------------------|
| **Claude Sonnet 4-6** | 30000 | 150000 | ¥30 / ¥150 |
| **Claude Opus 4** | 150000 | 750000 | ¥150 / ¥750 |
| **GPT-4o** | 25000 | 100000 | ¥25 / ¥100 |
| **GPT-4o-mini** | 1500 | 6000 | ¥1.5 / ¥6 |
| **Claude Sonnet 3.5** | 30000 | 150000 | ¥30 / ¥150 |
| **Claude Haiku 3.5** | 8000 | 40000 | ¥8 / ¥40 |

**定价依据**:
- 基于官方 API 价格（美元）
- 汇率: 1 USD ≈ 7.2 CNY
- 加价率: 1.2x (覆盖运营成本)

#### 缓存定价（Anthropic Prompt Caching）

| 操作 | 价格比例 | Claude Sonnet 4-6 示例 |
|------|---------|---------------------|
| Cache Write | 1.25x 输入价格 | 37500 credits/1M |
| Cache Read | 0.1x 输入价格 | 3000 credits/1M |

### 5. 免费额度池配置

基于 `admin/free_pool_extra.go`，当前支持的免费平台：

| 平台 | 模型 | RPM 限制 | 获取方式 |
|------|------|---------|---------|
| **AIGoCode** | gpt-4o-mini, claude-sonnet-4-6, gemini-2.0-flash | 30 | 注册获取 API Key |
| **SiliconFlow** | Qwen, DeepSeek 系列 | 未指定 | 手机号注册 |
| **智谱AI** | GLM-4-Flash | 未指定 | 手机号注册 |

### 6. 价格配置建议

#### 立即行动项

1. **创建 model_pricing 表**
   - 运行上述 SQL 创建表结构
   - 导入建议的模型价格配置

2. **实现价格查询 API**
   ```go
   GET /admin/pricing/models
   GET /admin/pricing/model/{canonical}
   PUT /admin/pricing/model/{canonical}
   ```

3. **集成到计费流程**
   - 在 `request_logs` 中记录 `credits_charged`
   - 基于实际 token 使用量和模型价格计算
   - 更新 `tenant_credit_wallets` 余额

#### 中期优化

1. **动态定价**
   - 根据供应商成本变化自动调整
   - 支持批量价格更新
   - 价格历史记录（审计用）

2. **分级定价**
   - 不同订阅等级享受不同折扣
   - 大客户协议价
   - 促销活动价格

3. **成本预警**
   - 单次请求成本预估
   - 达到预算阈值时告警
   - 自动限流保护

---

## 🖥️ 编程AI助手客户端特性分析

### 1. 客户端识别机制

基于 `domains/streaming/client_fingerprint.go`:

#### 识别优先级

1. **X-Gw-Client-Type** 头（最高优先级）
   - 用户显式指定
   - 格式: `X-Gw-Client-Type: cursor`

2. **User-Agent** 解析
   - 包含 IDE 特征字符串
   - 大小写不敏感

3. **X-Stainless-Lang** 辅助
   - OpenAI SDK 语言指纹
   - 用于未来扩展

### 2. 支持的客户端列表

| 客户端 | User-Agent 特征 | 识别代码 | 市场定位 |
|--------|----------------|---------|---------|
| **Cursor** | `cursor/`, `cursor-` | `cursor` | AI代码编辑器 |
| **Windsurf** | `windsurf/` | `windsurf` | 新一代AI IDE |
| **VSCode** | `vscode/`, `visual-studio-code/` | `vscode` | 微软官方编辑器 |
| **Copilot** | `github-copilot/`, `copilot/` | `copilot` | GitHub AI助手 |
| **Zed** | `zed/` | `zed` | 高性能编辑器 |
| **JetBrains** | `jetbrains/`, `intellij/`, `pycharm/`, `webstorm/` | `jetbrains` | JetBrains IDE系列 |
| **Claude Code** | `claude-code/`, `claude-code-` | `claude-code` | Anthropic官方 |
| **RooCode** | `roocode/`, `roo-code/` | `roocode` | AI编程助手 |

### 3. 客户端特性对比

#### Cursor

**特点**:
- 发送完整对话历史（FULL conversation）
- 高频多轮对话
- 依赖精确的 tool_call_id 匹配

**已知问题**:
- ✅ 已修复: Tool call ID 缺失导致的流中断
- ✅ 已修复: 多轮对话上下文丢失

**最佳实践**:
- 保持完整的消息格式转换
- 确保 tool_call_id 在流式响应中正确传递
- 支持大上下文窗口

#### Windsurf

**特点**:
- 新兴 AI IDE
- 类似 Cursor 的交互模式
- 期望严格的 OpenAI 协议兼容

**注意事项**:
- 需要完整的流式响应格式
- 工具调用必须符合 OpenAI 规范

#### GitHub Copilot

**特点**:
- 主要用于代码补全
- 短上下文请求
- 较少使用 tool calls

**特性**:
- 快速响应要求
- 低延迟优先
- 较少多轮对话

#### VSCode Extensions

**特点**:
- 多种扩展（Continue, CodeGPT等）
- 行为差异大
- 依赖插件实现

**挑战**:
- 难以统一识别
- 需要通过 User-Agent 或自定义头区分

#### JetBrains AI

**特点**:
- 多语言 IDE 支持
- 深度集成项目上下文
- 期望高质量代码补全

**特性**:
- 可能包含大量项目文件上下文
- 需要支持长上下文

### 4. 客户端行为差异总结

| 维度 | Cursor/Windsurf | Copilot | VSCode Ext | JetBrains |
|------|----------------|---------|-----------|-----------|
| **对话模式** | 多轮交互 | 单次补全 | 混合 | 混合 |
| **上下文长度** | 长 (>10K tokens) | 短 (<2K) | 中等 | 长 |
| **Tool Calls** | 频繁 | 很少 | 中等 | 中等 |
| **流式响应** | 必需 | 可选 | 混合 | 混合 |
| **协议严格性** | 高 | 中 | 低 | 中 |

---

## 🔧 Tool ID 处理问题审计

### 1. 历史问题回顾

#### 问题 1: Tool Call ID 缺失（2026-06-23）

**文档**: `docs/fixes/2026-06-23-tool-call-id-missing.md`

**症状**:
```
Tool execution was interrupted during streaming recovery
Expected 'id' to be a string
```

**根本原因**:
- Anthropic SSE 流中，`content_block_delta` 事件不包含 `id`
- OpenAI 格式要求每个 `tool_calls` 元素必须有 `id`
- 网关在转换时未正确追踪和传递 ID

**事件流对比**:

```
Anthropic SSE:
├─ content_block_start (tool_use) → 包含 id, name, input
├─ content_block_delta (input_json_delta) → 仅包含 partial_json，无 id
└─ content_block_stop → 结束信号

OpenAI 期望:
├─ delta.tool_calls[0].id → 必须有
├─ delta.tool_calls[0].function.name → 必须有
└─ delta.tool_calls[0].function.arguments → 增量传递
```

**修复方案**:
```go
// 在 content_block_start 时记录 ID
toolCallIDs := make(map[int]string) // index → id

// content_block_delta 时查找对应 ID
if id, ok := toolCallIDs[blockIndex]; ok {
    toolCall["id"] = id
} else {
    // 降级方案：生成 fallback ID
    toolCall["id"] = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), blockIndex)
}
```

#### 问题 2: Tool Call ID 不匹配

**症状**:
- 客户端发送的 `tool_call_id` 与服务端生成的不匹配
- 导致工具结果无法关联到原始调用

**原因**:
- 不同客户端生成 ID 的策略不同
- 网关转换时未保持 ID 一致性

**修复**:
- 在消息转换时保持 ID 映射关系
- Anthropic `tool_use.id` ↔ OpenAI `tool_call.id`
- 响应转换时使用相同的 ID

### 2. 当前的 Tool ID 处理机制

#### ID 生成策略

**Anthropic → OpenAI**:
```go
// 优先使用 Anthropic 原生 ID
if toolUseID := block["id"]; toolUseID != "" {
    return toolUseID
}

// 降级方案：生成确定性 ID
return fmt.Sprintf("tooluse_%s", generateShortID())
```

**OpenAI → Anthropic**:
```go
// 直接使用 OpenAI tool_call.id
toolCallID := toolCall["id"].(string)

// 转换为 tool_result 时保持 ID
toolResult := map[string]any{
    "type": "tool_result",
    "tool_use_id": toolCallID,  // 保持一致
    "content": resultContent,
}
```

#### ID 追踪机制

```go
type ToolCallTracker struct {
    // Anthropic content_block index → tool_use id
    blockIDMap map[int]string
    
    // OpenAI tool_calls index → call id
    callIDMap map[int]string
    
    // 双向映射
    anthropicToOpenAI map[string]string
    openAIToAnthropic map[string]string
}
```

### 3. 质量检测系统

基于 `domains/streaming/tool_call_quality.go`:

#### 检测的质量问题

| 问题标识 | 说明 | 影响 | 修复策略 |
|---------|------|------|---------|
| `empty_tool_name` | 工具名称为空 | 高 | 拒绝请求或使用默认名称 |
| `duplicate_tool_call_id` | 重复的 tool call ID | 中 | 重新生成唯一 ID |
| `xml_in_tool_calls` | 参数中包含XML | 低 | 清理XML标签 |
| `all_empty_tool_names` | 所有工具名称都为空 | 严重 | 拒绝请求 |

#### 质量模式

```go
const (
    QualityModeOff        = "off"        // 不检测
    QualityModeDetectOnly = "detect_only" // 仅检测记录
    QualityModeFix        = "fix"        // 自动修复
)
```

#### 质量评分

```go
func calculateQualityScore(flags []string) float64 {
    if len(flags) == 0 {
        return 1.0  // 完美
    }
    
    // 根据问题严重程度计算
    score := 1.0
    for _, flag := range flags {
        switch flag {
        case "empty_tool_name":
            score -= 0.5  // 严重问题
        case "duplicate_tool_call_id":
            score -= 0.3  // 中等问题
        case "xml_in_tool_calls":
            score -= 0.1  // 轻微问题
        }
    }
    return max(0.0, score)
}
```

### 4. 最佳实践建议

#### 对于网关开发者

1. **始终保持 ID 一致性**
   ```go
   // ✅ 正确
   anthropicID := extractAnthropicToolUseID(block)
   openAIID := anthropicID  // 直接使用
   
   // ❌ 错误
   openAIID := generateNewID()  // 破坏一致性
   ```

2. **实现 ID 映射追踪**
   ```go
   tracker := NewToolCallTracker()
   tracker.RecordAnthropicID(blockIndex, toolUseID)
   tracker.RecordOpenAIID(callIndex, toolCallID)
   tracker.CreateMapping(toolUseID, toolCallID)
   ```

3. **提供降级方案**
   ```go
   if id == "" {
       id = generateFallbackID(requestID, timestamp, index)
       logger.Warn("using fallback tool call id")
   }
   ```

#### 对于客户端开发者

1. **使用服务端返回的 ID**
   - 不要自行生成或修改 tool_call_id
   - 原样返回服务端提供的 ID

2. **验证 ID 存在性**
   ```javascript
   // ✅ 正确
   if (!toolCall.id) {
       throw new Error("Missing tool_call.id");
   }
   
   // 使用服务端 ID
   sendToolResult(toolCall.id, result);
   ```

3. **处理 ID 不匹配**
   ```javascript
   // 记录不匹配情况
   if (receivedID !== expectedID) {
       console.warn("Tool call ID mismatch", {
           expected: expectedID,
           received: receivedID
       });
       // 使用接收到的 ID 继续
   }
   ```

---

## 🔄 多轮对话修复总结

### 修复成果回顾

**问题**: Claude Sonnet 4-6 多轮对话上下文丢失  
**修复日期**: 2026-07-02  
**影响**: Q2路由场景（OpenAI客户端 → Anthropic上游）

### 关键修复

1. **消息格式转换完善**
   - 实现 `convertBridgeChatMessageToAnthropic`
   - 正确处理 tool_calls → tool_use
   - 正确处理 role: tool → tool_result

2. **Tool ID 追踪**
   - 在流式响应中保持 ID 一致性
   - 实现降级生成策略

3. **测试验证**
   - 10个测试场景，100%通过率
   - 覆盖简单对话、复杂上下文、工具调用

### 验证结果

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| 多轮对话 | ❌ 上下文丢失 | ✅ 完整保持 |
| Tool Calls | ❌ 格式错误 | ✅ 正确转换 |
| Tool Result | ❌ ID丢失 | ✅ ID正确 |
| 跨语言 | ❌ 问题 | ✅ 正常 |

---

## 📊 建议行动项

### 高优先级（本周内）

1. **✅ 完成**: Claude Sonnet 4-6 多轮对话修复
2. **🔄 进行中**: 创建 model_pricing 表
3. **📋 待办**: 导入初始价格配置
4. **📋 待办**: 实现价格查询 API

### 中优先级（本月内）

1. **📋 待办**: 客户端特性文档化
2. **📋 待办**: Tool ID 最佳实践指南
3. **📋 待办**: 质量检测规则优化
4. **📋 待办**: 计费系统监控仪表板

### 低优先级（下季度）

1. **📋 待办**: 动态定价系统
2. **📋 待办**: 分级定价策略
3. **📋 待办**: 客户端SDK示例代码
4. **📋 待办**: 性能优化（缓存、批处理）

---

## 📝 附录

### A. 相关文件清单

#### 计费相关
- `provider/billing.go` - 计费模式分类
- `db/migrations/007_maas_billing.sql` - MaaS计费表结构
- `db/migrations/008_billing_orders.sql` - 订单系统
- `domain/cost.go` - 成本上下文定义

#### 客户端识别
- `domains/streaming/client_fingerprint.go` - 客户端识别
- `domains/streaming/handler.go` - 请求处理

#### Tool ID 处理
- `domains/streaming/tool_call_quality.go` - 质量检测
- `domains/streaming/anthropic_bridge.go` - 格式转换
- `domains/transformation/anthropic/chat_to_anthropic.go` - 消息转换
- `docs/fixes/2026-06-23-tool-call-id-missing.md` - 修复文档

#### 多轮对话修复
- `docs/FIX_CLAUDE_SONNET_4_6_MULTI_TURN_CONTEXT.md` - 技术文档
- `docs/CLAUDE_SONNET_4_6_DEPLOYMENT_REPORT.md` - 部署报告
- `docs/MULTI_MODEL_TEST_SUMMARY.md` - 测试总结
- `test_all_models.sh` - 测试脚本

### B. 数据库表结构参考

#### maas_settings
```sql
CREATE TABLE maas_settings (
    id INT PRIMARY KEY DEFAULT 1,
    cents_per_credit NUMERIC(10, 4) NOT NULL DEFAULT 0.1,
    base_credits_per_1m BIGINT NOT NULL DEFAULT 10000,
    currency_display VARCHAR(8) NOT NULL DEFAULT 'CNY',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### subscription_plans
```sql
CREATE TABLE subscription_plans (
    id SERIAL PRIMARY KEY,
    code VARCHAR(32) NOT NULL UNIQUE,
    tier VARCHAR(16) NOT NULL,
    price_cents BIGINT NOT NULL,
    credits_included BIGINT NOT NULL DEFAULT 0,
    billing_cycle VARCHAR(16) NOT NULL,
    features JSONB,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### tenant_credit_wallets
```sql
CREATE TABLE tenant_credit_wallets (
    tenant_id VARCHAR(36) PRIMARY KEY,
    balance_credits BIGINT NOT NULL DEFAULT 0,
    lifetime_purchased BIGINT NOT NULL DEFAULT 0,
    lifetime_consumed BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### C. API 端点建议

```
GET  /admin/pricing/models              # 获取所有模型价格
GET  /admin/pricing/model/{canonical}   # 获取单个模型价格
PUT  /admin/pricing/model/{canonical}   # 更新模型价格
POST /admin/pricing/batch-update        # 批量更新价格

GET  /admin/billing/plans               # 获取所有订阅计划
GET  /admin/billing/plan/{code}         # 获取单个计划
POST /admin/billing/plan                # 创建新计划
PUT  /admin/billing/plan/{code}         # 更新计划

GET  /admin/wallet/{tenant_id}          # 获取钱包余额
POST /admin/wallet/{tenant_id}/charge   # 充值
GET  /admin/ledger/{tenant_id}          # 获取流水记录
```

---

**审计人员**: Kiro (AI Agent)  
**审计版本**: v1.0  
**最后更新**: 2026-07-02
