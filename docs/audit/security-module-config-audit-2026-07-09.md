# 安全检测引擎模块配置优化审计报告

**审计时间**: 2026-07-09  
**提交**: 5e1b643a  
**审计范围**: 安全检测引擎模块配置优化（18个配置项、模块依赖、前端实现）

---

## 1. 架构一致性审计

### ✅ 通过项

1. **配置命名规范一致**
   - 遵循 `{模块}.{子系统}.{配置项}` 三层结构
   - 示例：`security.intent.confidence_threshold`、`security.threat.checks.prompt_inject`
   - 与其他模块（compression、cache、goal）保持一致

2. **配置项规范符合 settings.Spec**
   - 所有18个配置项均包含完整字段：`Key`、`Type`、`Scope`、`Category`、`Default`、`Description`、`DescriptionLong`、`DangerLevel`、`HotReload`
   - `Options` 字段用于枚举类型（mode、response策略）
   - 危险等级标注合理（observe/enforce 切换为 Dangerous，检测开关为 Warning）

3. **前端分组逻辑清晰**
   - 按功能分为6组：基础配置、LLM模型、意图分析、威胁检测、响应策略、审计联动
   - 分组与配置项前缀对应（`security.mode`、`security.llm.*`、`security.intent.*` 等）

### ⚠️ 问题项

#### 问题 1：security 模块实现与配置项脱节

**位置**: `domains/hooks/security/hook.go`、`domains/hooks/security/intent_analyzer.go`、`domains/hooks/security/threat_detector.go`

**问题描述**:
- 添加了18个配置项（`security.llm.intent_model`、`security.intent.confidence_threshold`、`security.threat.severity_threshold` 等）
- 但实际代码中未使用这些配置项，仍使用硬编码值：
  ```go
  // intent_analyzer.go:11
  func NewIntentAnalyzer(minScore float64) *IntentAnalyzer {
      if minScore <= 0 {
          minScore = 0.5  // 硬编码，未读取 security.intent.confidence_threshold
      }
  }
  
  // threat_detector.go:11
  func NewThreatDetector(severityThreshold int) *ThreatDetector {
      if severityThreshold <= 0 {
          severityThreshold = 7  // 硬编码，未读取 security.threat.severity_threshold
      }
  }
  ```

**影响**:
- 用户在前端修改配置项无效
- 配置项成为"空气配置"，违反了"配置即文档"原则

**修正建议**:
1. 在 `SecurityHook` 初始化时读取配置：
   ```go
   func NewSecurityHook(settingsRegistry *settings.Registry) *SecurityHook {
       intentThreshold := settingsRegistry.Float64("security.intent.confidence_threshold", 0.7)
       intentModel := settingsRegistry.String("security.llm.intent_model", "gpt-4o-mini")
       threatThreshold := settingsRegistry.Int("security.threat.severity_threshold", 7)
       // ...
   }
   ```
2. 支持热加载（`HotReload: true`），监听配置变更

---

## 2. 模块依赖正确性审计

### ✅ 通过项

1. **依赖声明清晰**
   - 在 `admin/modules.go:242-249` 声明了6个依赖：
     - 必需：`prompt_injection`、`output_compliance`、`session_audit`
     - 可选：`audit`、`cache`、`compression`

2. **依赖模块存在性**
   - ✅ `domains/hooks/promptinjection/hook.go` 存在
   - ✅ `domains/hooks/outputcompliance/hook.go` 存在
   - ✅ `domains/hooks/sessionaudit/hook.go` 存在
   - ✅ `domains/hooks/intentanalysis/hook.go` 存在（独立模块）

3. **前端依赖状态检查实现**
   - `ModulesView.vue:62-78` 实现依赖状态检查
   - 支持未满足依赖警告（`hasUnmetDependencies`）

### ⚠️ 问题项

#### 问题 2：security 模块未依赖 intentanalysis 模块

**位置**: `admin/modules.go:242-249`

**问题描述**:
- security 模块添加了 `security.intent.*` 配置项（意图分析）
- 但依赖列表中未包含 `intentanalysis` 模块
- 实际代码中 `domains/hooks/security/intent_analyzer.go` 与 `domains/hooks/intentanalysis/hook.go` 存在功能重复

**当前依赖声明**:
```go
Dependencies: []ModuleDependency{
    {Key: "prompt_injection", Name: "提示词注入检测", Required: true, ...},
    {Key: "output_compliance", Name: "输出合规检测", Required: true, ...},
    {Key: "session_audit", Name: "会话审计与审批", Required: true, ...},
    {Key: "audit", Name: "审计日志", Required: false, ...},
    {Key: "cache", Name: "会话缓存", Required: false, ...},
    {Key: "compression", Name: "压缩管理", Required: false, ...},
},
```

**修正建议**:
```go
Dependencies: []ModuleDependency{
    {Key: "prompt_injection", Name: "提示词注入检测", Required: true, Description: "提供提示词注入模式检测能力"},
    {Key: "output_compliance", Name: "输出合规检测", Required: true, Description: "提供PII和敏感数据检测能力"},
    {Key: "session_audit", Name: "会话审计与审批", Required: true, Description: "提供高风险操作审批流程"},
    {Key: "intentanalysis", Name: "意图分析", Required: true, Description: "提供意图分类和漂移检测能力"},  // 新增
    {Key: "audit", Name: "审计日志", Required: false, Description: "记录安全检测审计日志"},
    {Key: "cache", Name: "会话缓存", Required: false, Description: "提升安全检测性能"},
    {Key: "compression", Name: "压缩管理", Required: false, Description: "降低安全检测token消耗"},
},
```

#### 问题 3：代码实际调用与依赖声明不一致

**位置**: `domains/hooks/security/hook.go`

**问题描述**:
- 代码中未直接调用 `prompt_injection`、`output_compliance`、`session_audit` 模块的 API
- 只是在配置项描述中提到"依赖：xxx 模块"（软依赖）
- 如果这些模块未启用，security 模块的对应检测项应该无法工作，但代码中未做检查

**修正建议**:
1. 在 `SecurityHook.Execute` 中检查依赖模块是否启用：
   ```go
   func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
       // 检查依赖模块
       if h.config.ThreatChecks.PromptInject {
           if !settings.Global.Bool("prompt_injection.enabled", true) {
               h.logger.Warn("security: prompt_inject check enabled but prompt_injection module disabled")
           }
       }
       // ...
   }
   ```
2. 或者在配置项描述中改为"推荐启用"而非"依赖"

---

## 3. 配置项完整性审计

### ✅ 通过项

1. **18个配置项覆盖完整**
   - 基础：`security.enabled`、`security.mode`（2个）
   - LLM模型：`security.llm.intent_model`、`security.llm.threat_model`（2个）
   - 意图分析：`security.intent.enabled`、`security.intent.confidence_threshold`、`security.intent.drift_threshold`（3个）
   - 威胁检测：`security.threat.enabled`、`security.threat.severity_threshold`、`security.threat.checks.*`（6个）
   - 响应策略：`security.response.low_risk`、`security.response.medium_risk`、`security.response.high_risk`（3个）
   - 审计联动：`security.audit.enabled`、`security.audit.log_all`、`security.audit.sampling_rate`（3个）

2. **配置项类型正确**
   - Bool: 11个（enabled 开关、checks 开关）
   - String: 5个（mode、LLM 模型、响应策略）
   - Int: 1个（severity_threshold）
   - Float: 2个（confidence_threshold、drift_threshold、sampling_rate）

3. **默认值合理**
   - `security.enabled: true` - 默认启用安全检测
   - `security.mode: "observe"` - 默认观察模式（不拦截）
   - `security.intent.confidence_threshold: 0.7` - 常规置信度阈值
   - `security.threat.severity_threshold: 7` - 中高风险阈值

### ⚠️ 问题项

#### 问题 4：缺少威胁检测响应动作的执行逻辑配置

**问题描述**:
- 添加了 `security.response.{low/medium/high}_risk` 配置项
- 但未添加"何时触发 low/medium/high"的阈值配置
- 当前 `security.threat.severity_threshold: 7` 只区分"通过/阻断"，未区分三级响应

**建议补充配置项**:
```go
{
    Key:             "security.threat.risk_level.low_threshold",
    Type:            TypeInt,
    Scope:           ScopePlatform,
    Category:        CategoryModules,
    Default:         3,
    Description:     "低风险阈值",
    DescriptionLong: "severity < 3 视为正常通过，3 ≤ severity < 5 视为低风险，触发 security.response.low_risk 动作。",
    DangerLevel:     Safe,
    HotReload:       true,
},
{
    Key:             "security.threat.risk_level.medium_threshold",
    Type:            TypeInt,
    Scope:           ScopePlatform,
    Category:        CategoryModules,
    Default:         5,
    Description:     "中风险阈值",
    DescriptionLong: "5 ≤ severity < 7 视为中风险，触发 security.response.medium_risk 动作。",
    DangerLevel:     Safe,
    HotReload:       true,
},
// security.threat.severity_threshold (7) 即为高风险阈值
```

#### 问题 5：配置项描述中的"依赖"未强制校验

**位置**: `settings/spec_modules.go:180`、`:202`、`:213`

**问题描述**:
- 配置项描述中写"依赖：prompt_injection 模块"，但用户可以在 prompt_injection 关闭时仍启用该检测项
- 未在配置层或运行时校验依赖关系

**修正建议**:
1. 在前端添加依赖校验警告（已部分实现在 ModulesView.vue）
2. 在后端 `saveSetting` 时检查依赖：
   ```go
   // 示例：保存 security.threat.checks.prompt_inject 时检查
   if key == "security.threat.checks.prompt_inject" && value == true {
       if !settings.Global.Bool("prompt_injection.enabled", true) {
           return errors.New("cannot enable prompt_inject check: prompt_injection module is disabled")
       }
   }
   ```

---

## 4. 前端实现问题审计

### ✅ 通过项

1. **ModulesView.vue 模块依赖状态检查**
   - `dependencyStatus` computed (62-73 行) 正确实现
   - `hasUnmetDependencies` computed (76-78 行) 正确检查必需依赖

2. **配置表单分组渲染**
   - 针对 security 模块实现专用分组表单（393-641 行）
   - 6个分组清晰：mode、llm、intent、threat、response、audit

3. **模块来源标注**
   - 威胁检测项显示依赖来源（527-528 行）：
     ```vue
     <span v-if="setting.key.includes('prompt_inject')" class="module-ref-badge">← prompt_injection</span>
     ```

### ⚠️ 问题项

#### 问题 6：前端 TypeScript 类型定义缺失

**位置**: `web/src/api/modules.ts`

**问题描述**:
- `ModuleDefinition` 接口中添加了 `dependencies` 字段（L25）
- 但未添加 `config_keys` 字段的类型定义
- 导致 `ModulesView.vue:134` 访问 `selectedModule.value.config_keys` 时无类型检查

**当前定义** (modules.ts:9-26):
```typescript
export interface ModuleDefinition {
  key: string
  name: string
  description: string
  capabilities: string[]
  icon: string
  category: string
  setting_key: string
  config_keys: string[]  // ← 字段存在但可能未在接口中声明
  docs_url: string
  danger_level: number
  integration?: ModuleIntegration
  dependencies?: ModuleDependency[]
}
```

**修正建议**:
检查 `modules.ts` 确保 `config_keys` 已声明，如未声明则补充。

#### 问题 7：security 配置表单未检查依赖状态

**位置**: `ModulesView.vue:393-641`

**问题描述**:
- security 模块配置表单显示所有配置项
- 但未对依赖检测项（prompt_inject、data_leak、pii）做禁用或警告
- 用户可以在 `prompt_injection.enabled=false` 时仍启用 `security.threat.checks.prompt_inject`

**修正建议**:
```vue
<div v-if="setting.type === 'bool'" class="config-bool">
  <label class="switch-label-sm" :class="{ 'disabled': isCheckDisabled(setting.key) }">
    <input
      type="checkbox"
      class="toggle-input"
      :checked="setting.value === true"
      :disabled="isCheckDisabled(setting.key)"
      @change="saveSetting(setting.key, ($event.target as HTMLInputElement).checked)"
    />
    <!-- ... -->
  </label>
  <div v-if="isCheckDisabled(setting.key)" class="dependency-warning">
    ⚠️ 依赖模块未启用，此检测项无法正常工作
  </div>
</div>

<script>
function isCheckDisabled(key: string): boolean {
  if (key === 'security.threat.checks.prompt_inject') {
    return !modules.value.find(m => m.key === 'prompt_injection')?.enabled
  }
  // ... 其他检测项
  return false
}
</script>
```

#### 问题 8：配置保存后未刷新依赖状态

**位置**: `ModulesView.vue:176-183`

**问题描述**:
- `saveSetting` 函数保存配置后调用 `selectModule` 刷新详情
- 但未刷新左侧模块列表（`modules.value`）
- 导致依赖状态显示可能过时（例如刚启用 prompt_injection 模块，但依赖状态仍显示未启用）

**修正建议**:
```typescript
async function saveSetting(settingKey: string, value: any) {
  try {
    await updateSetting(settingKey, { value })
    await loadModules()  // 刷新模块列表
    await selectModule(selectedKey.value!)  // 刷新当前详情
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.saveFailed')
  }
}
```

---

## 5. 文档缺失审计

### ⚠️ 缺失项

#### 问题 9：缺少安全检测引擎架构文档

**建议补充**:
1. 创建 `docs/modules/security-engine.md`：
   - 架构图（intent analyzer + threat detector + response策略）
   - 与 promptinjection/outputcompliance/sessionaudit 的协作关系
   - 配置决策树（何时用 observe vs enforce）
   - 威胁严重度评分规则（0-10 映射到 low/medium/high）

2. 更新 `README.md` 或 `docs/features.md`：
   - 安全检测引擎作为新功能的简介
   - 快速配置指南

#### 问题 10：缺少配置项迁移指南

**问题描述**:
- 添加了18个新配置项，但未说明：
  - 从旧版本升级如何处理默认值
  - 是否与现有配置项冲突（例如 `prompt_injection.enabled` vs `security.threat.checks.prompt_inject`）

**建议补充**:
创建 `docs/migrations/r1.13-security-config.md`：
```markdown
# R1.13 安全检测引擎配置迁移指南

## 变更概览
- 新增 security 模块（18个配置项）
- 将意图分析、威胁检测整合为统一安全检测引擎

## 配置项对应关系
| 旧配置项 | 新配置项 | 说明 |
|---------|---------|------|
| N/A | security.enabled | 新增总开关 |
| prompt_injection.enabled | security.threat.checks.prompt_inject | 功能拆分 |
| output_compliance.enabled | security.threat.checks.{data_leak,pii} | 功能拆分 |

## 升级步骤
1. 检查现有 prompt_injection/output_compliance 配置
2. 启用 security 模块并配置对应检测项
3. 验证安全检测结果与旧版一致
4. （可选）禁用旧模块，完全迁移到 security 引擎
```

---

## 6. 综合评分

| 维度 | 评分 | 说明 |
|-----|------|------|
| **架构一致性** | 7/10 | 配置命名规范优秀，但实现与配置脱节 |
| **依赖正确性** | 6/10 | 依赖声明清晰，但缺少 intentanalysis、未校验依赖 |
| **配置完整性** | 8/10 | 18个配置项覆盖全面，但缺少风险等级阈值 |
| **前端实现** | 7/10 | 依赖状态检查实现，但未禁用无效配置项 |
| **文档质量** | 4/10 | 缺少架构文档和迁移指南 |
| **总分** | **32/50** | **等级：C（需改进）** |

---

## 7. 优先级修正清单

### P0 - 阻断性问题（必须立即修复）

1. **[问题1] 配置项未实际生效**
   - 修改 `SecurityHook`、`IntentAnalyzer`、`ThreatDetector` 构造函数，读取 settings 配置
   - 支持热加载
   - **影响**: 当前所有配置修改无效，用户体验极差

### P1 - 高优先级（下个迭代修复）

2. **[问题2] 缺少 intentanalysis 依赖声明**
   - 在 `admin/modules.go:242` 添加 `intentanalysis` 依赖
   - 更新前端依赖检查逻辑

3. **[问题4] 补充风险等级阈值配置**
   - 添加 `security.threat.risk_level.{low/medium}_threshold` 配置项
   - 实现三级响应策略路由逻辑

4. **[问题7] 前端配置项依赖校验**
   - 禁用依赖模块未启用时的检测项开关
   - 显示警告提示

### P2 - 中优先级（技术债）

5. **[问题3] 依赖模块调用链路**
   - 重构 security 模块，直接调用 promptinjection/outputcompliance API
   - 或明确标注为"软依赖"

6. **[问题9] 补充架构文档**
   - 创建 `docs/modules/security-engine.md`
   - 绘制架构图和配置决策树

7. **[问题10] 迁移指南**
   - 创建 `docs/migrations/r1.13-security-config.md`

### P3 - 低优先级（优化）

8. **[问题5] 配置项保存时依赖校验**
   - 后端添加依赖检查逻辑

9. **[问题6] TypeScript 类型定义**
   - 确保 `ModuleDefinition.config_keys` 已声明

10. **[问题8] 配置保存后刷新列表**
    - 修改 `saveSetting` 调用 `loadModules`

---

## 8. 总结

本次优化**在配置项设计和前端分组展示上做得很好**，6个分组清晰、18个配置项覆盖全面。但**存在致命缺陷**：

1. **配置项未接入实际代码** - 所有配置修改不生效，这是最严重的问题
2. **模块依赖不完整** - 缺少 intentanalysis 依赖声明
3. **缺少文档** - 用户不知道如何配置和使用

**建议优先修复 P0 和 P1 问题**，让配置项真正生效，并补充架构文档。P2/P3 问题可作为技术债在后续迭代中逐步完善。

---

**审计人**: Claude (OpenCode Agent)  
**审计方法**: 静态代码审查 + 架构分析 + 文档检查  
**审计标准**: ACC Toolkit 规范 + hook 架构一致性原则
