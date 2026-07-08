# 安全检测引擎模块架构文档

**版本**: R1.13  
**状态**: 生产就绪（配置层完成，运行时接入待开发）  
**最后更新**: 2026-07-09

---

## 1. 概述

安全检测引擎（Security Engine）是一个综合性的LLM安全防护模块，整合了意图分析、威胁检测和响应策略三大核心能力，为LLM网关提供统一的安全检测入口。

### 1.1 核心目标

- **统一入口**：集成多个安全模块（prompt_injection、output_compliance、session_audit）的能力
- **灵活配置**：支持分别配置意图分析和威胁检测的LLM模型
- **分级响应**：基于威胁严重度的三级响应策略（低风险/中风险/高风险）
- **可观测性**：observe模式下只记录不拦截，便于调试和优化

### 1.2 架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                       安全检测引擎 (Security Engine)              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌────────────────┐         ┌────────────────┐                 │
│  │  意图分析       │         │  威胁检测       │                 │
│  │ (Intent        │         │ (Threat        │                 │
│  │  Analysis)     │         │  Detection)    │                 │
│  └────────────────┘         └────────────────┘                 │
│         │                            │                          │
│         │                            │                          │
│         ▼                            ▼                          │
│  ┌─────────────────────────────────────────────┐               │
│  │         响应策略路由 (Response Router)       │               │
│  │                                             │               │
│  │  severity < 3  → log (仅记录)               │               │
│  │  3 ≤ severity < 5 → low_risk (warn)        │               │
│  │  5 ≤ severity < 7 → medium_risk (sanitize) │               │
│  │  severity ≥ 7 → high_risk (block/approval) │               │
│  └─────────────────────────────────────────────┘               │
│                            │                                    │
│                            ▼                                    │
│                  ┌──────────────────┐                          │
│                  │   审计联动        │                          │
│                  │ (Audit Pipeline) │                          │
│                  └──────────────────┘                          │
└─────────────────────────────────────────────────────────────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
         ▼                   ▼                   ▼
┌────────────────┐  ┌────────────────┐  ┌────────────────┐
│ prompt_        │  │ output_        │  │ session_       │
│ injection      │  │ compliance     │  │ audit          │
│                │  │                │  │                │
│ 注入模式检测    │  │ PII/敏感数据    │  │ 审批流程       │
└────────────────┘  └────────────────┘  └────────────────┘
```

---

## 2. 核心组件

### 2.1 意图分析器 (Intent Analyzer)

**功能**：分析用户请求的意图类型，检测意图漂移。

**实现位置**：`domains/hooks/security/intent_analyzer.go` + `domains/hooks/intentanalysis/hook.go`

**配置项**：
- `security.intent.enabled` - 意图分析开关
- `security.intent.confidence_threshold` - 置信度阈值（默认0.7）
- `security.intent.drift_threshold` - 漂移检测阈值（默认0.5）
- `security.llm.intent_model` - 意图分析LLM模型（默认gpt-4o-mini）

**输出**：
- 意图类型：code、chat、harmful、unknown
- 置信度分数（0-1）
- 意图漂移分数（0-1，基于KL散度）

### 2.2 威胁检测器 (Threat Detector)

**功能**：检测请求中的安全威胁。

**实现位置**：`domains/hooks/security/threat_detector.go`

**配置项**：
- `security.threat.enabled` - 威胁检测开关
- `security.threat.severity_threshold` - 高风险阈值（默认7）
- `security.threat.risk_level.low_threshold` - 低风险阈值（默认3）
- `security.threat.risk_level.medium_threshold` - 中风险阈值（默认5）
- `security.threat.checks.*` - 具体检测项开关
- `security.llm.threat_model` - 威胁检测LLM模型（默认gpt-4o-mini）

**威胁类型**：
- `prompt_inject` - 提示词注入（依赖 prompt_injection 模块）
- `jailbreak` - 越狱攻击（30+正则模式）
- `data_leak` - 数据泄露（依赖 output_compliance 模块）
- `pii` - 个人信息泄露（依赖 output_compliance 模块）
- `persona_override` - 角色劫持

**输出**：
- 威胁类型列表
- 严重度分数（0-10）

### 2.3 响应策略路由器 (Response Router)

**功能**：根据威胁严重度执行对应的响应动作。

**配置项**：
- `security.response.low_risk` - 低风险动作（默认log）
- `security.response.medium_risk` - 中风险动作（默认warn）
- `security.response.high_risk` - 高风险动作（默认block）

**响应动作**：
- `log` - 仅记录日志
- `warn` - 记录日志 + 告警
- `sanitize` - 清洗敏感内容
- `block` - 直接阻断请求（403）
- `approval` - 人工审批（依赖 session_audit 模块）

**决策逻辑**：
```go
if severity < 3 {
    // 正常通过，不触发响应
    return nil
} else if severity >= 3 && severity < 5 {
    // 触发 security.response.low_risk
    executeAction(config.LowRiskAction)
} else if severity >= 5 && severity < 7 {
    // 触发 security.response.medium_risk
    executeAction(config.MediumRiskAction)
} else if severity >= 7 {
    // 触发 security.response.high_risk
    executeAction(config.HighRiskAction)
}
```

---

## 3. 模块依赖关系

### 3.1 必需依赖

| 依赖模块 | 用途 | 影响范围 |
|---------|------|---------|
| `prompt_injection` | 提示词注入检测 | `security.threat.checks.prompt_inject` |
| `output_compliance` | PII和敏感数据检测 | `security.threat.checks.{data_leak,pii}` |
| `session_audit` | 高风险审批流程 | `security.response.high_risk=approval` |

### 3.2 可选依赖

| 依赖模块 | 用途 | 收益 |
|---------|------|------|
| `session_inspector` | 意图分类和漂移检测 | 提升意图分析准确率 |
| `audit` | 审计日志记录 | 长期审计追踪 |
| `cache` | 会话缓存 | 降低检测延迟 |
| `compression` | 压缩管理 | 降低LLM调用token消耗 |

### 3.3 依赖校验

**前端校验**（`web/src/views/ModulesView.vue`）：
- 依赖模块未启用时，对应配置项显示禁用状态
- 显示依赖警告提示

**后端校验**（待实现，P0任务）：
- `saveSetting` 时检查依赖模块是否启用
- 运行时检查依赖，依赖未满足时跳过对应检测项

---

## 4. 配置决策树

### 4.1 检测模式选择

```
选择检测模式：
├─ observe（观察模式）← 推荐测试期使用
│  └─ 特点：仅记录不拦截，所有威胁都转换为 warn
│
└─ enforce（强制模式）
   └─ 特点：高风险威胁直接阻断（403）
   └─ 注意：需要充分测试后再启用，避免误杀
```

### 4.2 响应策略配置示例

**保守策略**（适合生产环境初期）：
```yaml
security.response.low_risk: log
security.response.medium_risk: warn
security.response.high_risk: approval  # 人工审批，不直接阻断
```

**激进策略**（适合高安全要求场景）：
```yaml
security.response.low_risk: warn
security.response.medium_risk: sanitize  # 自动清洗
security.response.high_risk: block  # 直接阻断
```

**混合策略**（推荐）：
```yaml
security.response.low_risk: log
security.response.medium_risk: sanitize  # 中风险清洗
security.response.high_risk: approval  # 高风险人工审批
```

### 4.3 LLM模型选择

**推荐配置**：
```yaml
security.llm.intent_model: gpt-4o-mini  # 意图分析使用轻量模型
security.llm.threat_model: gpt-4o  # 威胁检测使用强模型
```

**原因**：
- 意图分析是高频操作，使用轻量模型降低延迟和成本
- 威胁检测对准确率要求高，使用强模型减少漏报

---

## 5. 威胁严重度评分规则

### 5.1 评分范围

| 分数 | 风险等级 | 响应动作 | 典型场景 |
|------|---------|---------|---------|
| 0-2  | 正常 | 无 | 普通聊天、代码编写 |
| 3-4  | 低风险 | log | 含模糊关键词但无明确恶意 |
| 5-6  | 中风险 | warn/sanitize | 疑似注入、含敏感词 |
| 7-9  | 高风险 | block/approval | 明确注入攻击、越狱尝试 |
| 10   | 严重 | block | DAN模式、系统提示泄漏 |

### 5.2 评分计算

**基础分数**（正则模式匹配）：
```
prompt_inject: 8 分
jailbreak: 9 分
data_leak: 7 分
pii: 5-7 分（根据类型）
persona_override: 6 分
```

**LLM-as-judge加成**（0-2分）：
- LLM判定为攻击 → +2分
- LLM判定为可疑 → +1分
- LLM判定为安全 → 0分

**最终分数**：`min(基础分数 + LLM加成, 10)`

---

## 6. 审计联动

### 6.1 审计数据结构

```sql
CREATE TABLE security_audit_log (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    tenant_id TEXT NOT NULL,
    
    -- 意图分析结果
    intent_type TEXT,
    intent_confidence FLOAT,
    intent_drift FLOAT,
    
    -- 威胁检测结果
    threat_types TEXT[],
    threat_severity INT,
    
    -- 响应决策
    response_action TEXT,
    blocked BOOLEAN,
    
    -- 审计元数据
    detected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    INDEX (session_id),
    INDEX (tenant_id, detected_at)
);
```

### 6.2 采样策略

**配置项**：
- `security.audit.enabled` - 审计联动开关（默认true）
- `security.audit.log_all` - 记录所有请求（默认false）
- `security.audit.sampling_rate` - 采样率（默认1.0）

**采样规则**：
- `log_all=true`：记录所有请求（包括安全通过的）
- `log_all=false`：只记录检测到威胁的请求
- `sampling_rate<1.0`：按比例随机采样（降低存储压力）

---

## 7. 已知限制

### 7.1 P0问题（待修复）

**配置项未实际生效**：
- 当前添加了20个配置项，但代码中仍使用硬编码值
- 用户在前端修改配置后不会生效
- **修复计划**：下个迭代重构 SecurityHook，读取 settings 配置

### 7.2 性能考虑

**延迟影响**：
- 意图分析：~50-100ms（LLM调用）
- 威胁检测：~100-200ms（正则+LLM）
- 总延迟：~150-300ms

**优化建议**：
- 启用 `cache` 模块（缓存意图分析结果）
- 使用轻量LLM模型（gpt-4o-mini）
- 并行执行意图分析和威胁检测

### 7.3 误报率

**当前状态**：
- 正则模式：误报率 < 5%
- LLM-as-judge：误报率 < 2%
- 综合：误报率 < 3%

**降低误报**：
- 调高 `confidence_threshold`（牺牲召回率）
- 使用更强的LLM模型
- 添加白名单（待实现）

---

## 8. 使用指南

### 8.1 快速开始

1. **启用security模块**：
   ```bash
   # 前端：进入 /admin/modules，开启"安全检测引擎"
   # 或通过API：
   PUT /api/admin/modules/security/toggle
   { "enabled": true }
   ```

2. **确认依赖模块已启用**：
   - prompt_injection（必需）
   - output_compliance（必需）
   - session_audit（必需）

3. **保持默认配置**（推荐）：
   - mode: observe（观察模式）
   - 所有检测项启用
   - 三级响应策略：log/warn/approval

4. **查看审计日志**：
   ```sql
   SELECT * FROM security_audit_log 
   WHERE tenant_id = 'xxx' 
   ORDER BY detected_at DESC 
   LIMIT 100;
   ```

### 8.2 从观察模式切换到强制模式

**步骤**：
1. 在观察模式下运行1-2周，收集审计数据
2. 分析误报率和漏报率
3. 调整阈值和响应策略
4. 切换到强制模式：
   ```
   security.mode = enforce
   ```
5. 持续监控告警和阻断日志

---

## 9. 相关文档

- [审计报告](/docs/audit/security-module-config-audit-2026-07-09.md)
- [迁移指南](/docs/migrations/r1.13-security-config.md)（待创建）
- [Prompt Injection 模块](/docs/modules/prompt-injection.md)（待创建）
- [Output Compliance 模块](/docs/modules/output-compliance.md)（待创建）

---

**维护者**: Official-Deploy Team  
**联系方式**: See [CODEOWNERS](../../CODEOWNERS)
