# 提示词注入检测增强（Prompt Injection Detection Enhancement）

> 状态：**已实现**（2026-07-09 审计修正：消除代码重复，统一为薄适配层架构）
>
> 相关提交：`feat(security): enhanced prompt injection detection system` → `refactor(security): thin-adapter plugin, fix auth middleware`

---

## 一、设计目标

在不重复造轮子的前提下，为现有的"提示词注入检测"模块补齐：

1. **LLM 检测引擎**可配置（从已有供应商/模型中选择，高效低成本）
2. **15 种风险类别**与**11 种处理动作**
3. **严重等级矩阵**（low/medium/high/critical → 对应动作）
4. **Canary Token** 泄漏检测
5. **向量相似度**攻击匹配（pgvector）
6. **审批/终止/替换**等后续处理，复用现有审批中心
7. 配置化 UI、流程联动、可观测

---

## 二、架构总览（薄适配层 + 链式接入）

本模块严格遵循项目的 **Hook/Plugin 链式接入**架构，不直接对接 `main`。

```
                  ┌───────────────────────────────────────────────┐
   HTTP Request → │  Pipeline (domains/pipeline)                  │
                  │  Phase: governance                            │
                  │                                               │
                  │  ┌─────────────────────────────────────────┐  │
                  │  │ SecurityHook (pipeline.Hook, prio 100)  │  │
                  │  │  security.Registry.RunAll()             │  │
                  │  │  ├─ PromptInjectionChecker (legacy)     │  │
                  │  │  └─ PromptInjectionEnhancedPlugin ◀───  │  │ ┐
                  │  └─────────────────────────────────────────┘  │ │ 薄适配层
                  │  ┌─────────────────────────────────────────┐  │ │ 仅做：
                  │  │ InterceptionHook (prio 200)             │  │ │  1. 依赖注入
                  │  │  Engine.Decide() → Decision             │  │ │  2. 结果转换
                  │  └─────────────────────────────────────────┘  │ │
                  └─────────────────────┬─────────────────────────┘ │
                                        │                           │
                            ┌───────────▼───────────┐               │
                            │ DispatchGate          │               │
                            │ continue/block/       │               │
                            │ suspend(→approval) /  │               │
                            │ terminate             │               │
                            └───────────┬───────────┘               │
                                        │                           │
                                        │   被调用 ▼                  │
                  ┌─────────────────────────────────────────────┐   │
                  │  promptinjection.Detector (核心)            │ ◀─┘
                  │  domains/promptinjection/detector.go        │
                  │  ┌───────────────────────────────────────┐  │
                  │  │ 6 层检测（单一实现，不重复）          │  │
                  │  │  1. 基础规则（正则，15 类）           │  │
                  │  │  2. 高级规则（正则，15 类）           │  │
                  │  │  3. 启发式（角色切换/超长输入）       │  │
                  │  │  4. Canary Token 泄漏                 │  │
                  │  │  5. 向量相似度（pgvector）            │  │
                  │  │  6. LLM 智能检测（可配置引擎）        │  │
                  │  │ + 评分 / 风险等级 / 动作决策 / 日志   │  │
                  │  └───────────────────────────────────────┘  │
                  └─────────────────────────────────────────────┘
```

### 链式接入顺序

1. `PromptInjectionEnhancedPlugin.Inspect()` 被 `security.Registry.RunAll()` 调用
2. Plugin 委托核心 `promptinjection.Detector.Detect()` 执行检测
3. Plugin 把 `DetectionResult` 转换为 `governance.Verdict`，写入 `env.Governance`
4. `InterceptionHook` 汇总所有 verdict → `Decision`（continue/block/suspend/terminate）
5. `DispatchGate` 执行 Decision：
   - `suspend` → 复用现有 `approval_queue` + `ApprovalManager`
   - `block` → HTTP 403
   - `terminate` → HTTP 410

### 依赖注入路径（不直接对接 main）

```
main.go (启动)
  └─ v2DispatchMux() → buildV2DispatchPipeline()
       ├─ 创建 EnhancedPIPlugin，注册到 security.Registry
       └─ SetV2DispatchAnalysisResources() (pipeline builder 的 DI 钩子)
            └─ dbConn.Stdlib() 桥接 pgxpool → *sql.DB
            └─ plugin.Init(sqlDB) → 内部创建核心 Detector
```

---

## 三、模块复用清单（避免重复造轮子）

| 能力 | 复用的现有模块 | 不再重复实现 |
|------|----------------|--------------|
| 检测逻辑（规则/启发式/Canary/向量/LLM） | `domains/promptinjection.Detector` | Plugin 不再自带检测函数 |
| 评分 / 风险等级 / 动作决策 | `Detector.calculateScore/RiskLevel/decideAction` | 删除 Plugin 内重复函数 |
| 规则加载（正则编译、缓存） | `Detector.loadRules/RefreshRules` | Plugin 不再 reloadConfig |
| 策略 / 严重等级矩阵读取 | `Detector.getPolicy/getSeverityAction` | Plugin 不再读 DB |
| 检测日志落库 | `Detector.DetectAndLog` | Plugin 不再 logDetection |
| 审批队列 | `approval_queue` + `sessionaudit.ApprovalManager` | 不自建 `prompt_injection_approvals` |
| 审批 UI | `/admin/approvals`、`/admin/approval-config` | 审批标签页改为跳转链接 |
| LLM 调用 | `autoroute.LLMCaller` 模式（核心 Detector 内置 LLM 层） | 不在 Plugin 层重复适配 |
| 认证中间件 | `admin.AdminMiddleware` + `GetAuthContext` | 不自定义 header 认证 |
| JSON 响应 | `admin.writeJSON` / `admin.writeError` | 不自定义 `writeJSONLocal` |
| 租户解析 | `admin.GetTenantID(r)` | 不自定义 `getTenantID` |
| pgxpool → sql.DB 桥接 | `db.DB.Stdlib()` | 不引入新桥接代码 |
| Pipeline 接入 | `security.Registry` → `SecurityHook` | 不直接注册到 mux/main |

---

## 四、数据流流程图

```mermaid
flowchart TD
    A[用户请求 /v1/chat/completions] --> B[Pipeline: PhaseGovernance]
    B --> C[SecurityHook: Registry.RunAll]
    C --> D[PromptInjectionEnhancedPlugin.Inspect]

    D --> E{user_content 非空?}
    E -- 否 --> Z1[返回 nil verdict → 放行]
    E -- 是 --> F[Detector.Detect: 6 层检测]

    F --> F1[Layer1-2: 规则检测 正则]
    F --> F2[Layer3: 启发式检测]
    F --> F3[Layer4: Canary Token]
    F --> F4[Layer5: 向量相似度]
    F --> F5[Layer6: LLM 智能检测]

    F1 & F2 & F3 & F4 & F5 --> G[评分 + 风险等级 + 动作决策]
    G --> H[返回 DetectionResult]

    H --> I{动作类型}
    I -- pass/log --> Z1
    I -- warn --> J1[继续 + 写警告 metadata]
    I -- replace/redact/remove --> J2[替换内容后继续]
    I -- reject/block --> K1[Verdict Allow=false Severity≥2]
    I -- approve --> K2[Verdict FixAction=require_approval]
    I -- terminate --> K3[Verdict FixAction=terminate_session]

    K1 & K2 & K3 --> L[InterceptionHook: Engine.Decide]
    L --> M{Decision}
    M -- continue --> N1[放行]
    M -- block --> N2[HTTP 403]
    M -- suspend --> N3[写入 approval_queue + HTTP 202]
    M -- terminate --> N4[HTTP 410]

    N3 --> O[/admin/approvals 审批]
    O --> P{审批结果}
    P -- 批准 --> Q[ResumeAfterApproval → 上游]
    P -- 拒绝 --> R[返回拒绝信息]
    P -- 超时 --> R
```

---

## 五、风险类别与处理动作

### 15 种风险类别（`injection_category` 枚举）

| 类别 | 说明 |
|------|------|
| `role_hijack` | 角色劫持 |
| `instruction_override` | 指令覆盖 |
| `instruction_leak` | 指令泄漏 |
| `jailbreak` | 越狱攻击（DAN/DevMode） |
| `encoding_bypass` | 编码绕过（Base64/ROT13/Hex） |
| `injection_marker` | 注入标记（特殊 Token） |
| `multi_turn_attack` | 多轮攻击 |
| `resource_exhaustion` | 资源耗尽 |
| `data_exfiltration` | 数据窃取 |
| `social_engineering` | 社会工程 |
| `prompt_leaking` | 提示词泄漏 |
| `payload_smuggling` | Payload 走私 |
| `unicode_obfuscation` | Unicode 混淆 |
| `context_manipulation` | 上下文操纵 |
| `tool_abuse` | 工具滥用 |

### 11 种处理动作（`injection_action` 枚举）

| 动作 | 说明 | HTTP 行为 |
|------|------|-----------|
| `pass` | 放行 | 继续 |
| `log` | 仅记录 | 继续 |
| `warn` | 警告 | 继续 + metadata |
| `replace` | LLM 重写后继续 | 继续替换内容 |
| `redact` | 脱敏 `[REDACTED]` | 继续 |
| `remove` | 移除恶意片段 | 继续 |
| `reject` | 拒绝请求 | 403 |
| `terminate` | 终止会话 | 410 |
| `approve` | 人工审批 | 202 → approval_queue |
| `quarantine` | 沙箱隔离 | 保留 |
| `block` | 直接阻断 | 403 |

---

## 六、配置项速查

### 策略（`prompt_injection_policies`）

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `detection_mode` | `observe` | `observe`/`enforce` |
| `enable_llm_detection` | `true` | LLM 智能检测 |
| `enable_canary_detection` | `true` | Canary Token 检测 |
| `enable_vector_similarity` | `false` | 向量相似度（需 pgvector） |
| `content_replacement_strategy` | `llm_rewrite` | `llm_rewrite`/`pattern_redact`/`keyword_remove` |
| `score_threshold_block` | `10` | 阻断阈值（0-10） |

### 严重等级矩阵（`severity_action_matrix`）

| 等级 | observe | enforce | 审批 | 会话扣分 |
|------|---------|---------|------|----------|
| low | log | log | 否 | 5 |
| medium | warn | replace | 否 | 15 |
| high | warn | reject | 是 | 30 |
| critical | warn | block | 是 | 50 |

---

## 七、部署指南

### 1. 数据库迁移

```bash
psql -f sql/migrations/startup/364_prompt_injection_enhanced.sql
```

迁移内容：
- 启用 `pgvector` 扩展
- 新增表：`prompt_injection_llm_engines`、`severity_action_matrix`、`canary_tokens`、`injection_attack_vectors`
- 新增枚举：`injection_category`、`injection_action`
- 扩展现有表：`prompt_injection_policies`、`prompt_injection_rules`、`prompt_injection_detections`
- 预置 20+ 检测规则、默认严重等级矩阵

### 2. 启用 V2 Pipeline（必需）

```bash
export LLM_GATEWAY_USE_V2_PIPELINE=true
```

### 3. LLM 检测引擎（可选，默认走核心 Detector 的 LLM 层）

核心 Detector 内置 LLM 层，在 `prompt_injection_llm_engines` 表中配置启用引擎即可。
环境变量（复用 autoroute 模式，未来填充实装时使用）：

```bash
export LLMGatewayAutoLLMEndpoint=https://your-llm-endpoint/v1
export LLMGatewayAutoLLMApiKey=your-key
export LLMGatewayAutoLLMModel=gpt-4o-mini   # 推荐：高效低成本
```

### 4. UI 访问

| 路径 | 说明 |
|------|------|
| `/admin/prompt-injection` | 模块配置（7 标签页） |
| `/admin/approvals` | 审批队列（复用现有） |
| `/admin/approval-config` | 审批配置（复用现有） |

### 5. 验证

```bash
# 后端编译
go build ./cmd/gateway/...

# 前端类型检查（仅本视图）
cd web && npx vue-tsc --noEmit 2>&1 | grep -i promptinjection
```

---

## 八、文件清单

| 文件 | 角色 |
|------|------|
| `domains/promptinjection/detector.go` | 核心 Detector（6 层检测，单一实现） |
| `domains/security/plugins/prompt_injection_enhanced.go` | 薄适配层 Plugin（~180 行，仅做依赖注入+结果转换） |
| `domains/hooks/promptinjection/hook.go` | Pipeline Hook 接入点（可选，当前由 Plugin 替代） |
| `admin/prompt_injection_handler.go` | Admin API（策略/规则/引擎/矩阵/Canary/日志/统计） |
| `cmd/gateway/main_pipeline.go` | Pipeline builder + 依赖注入钩子 |
| `sql/migrations/startup/364_prompt_injection_enhanced.sql` | 数据库迁移 |
| `web/src/views/PromptInjectionSettingsView.vue` | 管理 UI（7 标签页） |

---

## 九、审计修正记录（2026-07-09）

| 问题 | 修正 |
|------|------|
| Plugin 重复实现 ~300 行检测逻辑 | 改为薄适配层，复用核心 Detector |
| Plugin 规则匹配退化为 `strings.Contains`（丢失正则） | 复用 Detector 的正则检测 |
| Admin handler 绕过 `AdminMiddleware`（auth 漏洞） | 路由统一套用 `AdminMiddleware` |
| 重复定义 `getTenantID`/`writeJSONLocal`/`getUserEmail` | 改用 `GetTenantID`/`writeJSON`/`GetAuthContext` |
| 重复创建审批表 | 复用 `approval_queue` + `ApprovalManager` |
| 重复适配 LLM caller | 删除，核心 Detector 内置 LLM 层 |
