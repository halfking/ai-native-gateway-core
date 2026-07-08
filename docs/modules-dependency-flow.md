# 模块管理系统 - 依赖验证与UI交互流程

## 概述

本文档描述模块管理系统的依赖验证机制与前端交互流程，重点关注 `session_analytics` 模块的依赖门禁实现。

## 问题背景

### 原有问题

1. **模块状态文案冲突**
   - 模块已启用时，按钮显示"启用此模块"（语义矛盾）
   - 根因：中文 i18n 文件中 `enabledAction` 和 `disabledAction` 键值对调

2. **依赖验证缺失**
   - `session_analytics` 模块依赖 4 个前置模块（compression、cache、prompt_injection、output_compliance）
   - 后端无强制依赖检查，前端无依赖状态展示
   - 用户可以启用依赖未满足的模块，导致功能异常

3. **UI 交互不完整**
   - 左侧模块卡片只有 toggle 开关，无依赖提示
   - 右侧详情页缺少主操作按钮
   - 无法快速跳转到依赖模块配置页

## 解决方案

### 1. 后端数据模型增强

#### 新增类型定义

```go
// admin/modules.go

// ModuleDependency 描述模块间的依赖关系
type ModuleDependency struct {
    Key         string `json:"key"`          // 依赖模块的 key
    Name        string `json:"name"`         // 依赖模块的名称
    Icon        string `json:"icon"`         // 依赖模块的图标
    Required    bool   `json:"required"`     // true=必需，false=可选
    Description string `json:"description"`  // 依赖说明
    Enabled     bool   `json:"enabled,omitempty"` // 运行时状态
}

// ModuleWithStatus 扩展运行时状态
type ModuleWithStatus struct {
    ModuleDefinition
    Enabled          bool   `json:"enabled"`
    Source           string `json:"source"`
    CanToggleEnabled bool   `json:"can_toggle_enabled"`  // 是否允许启用
    BlockedReason    string `json:"blocked_reason,omitempty"` // 阻塞原因
}
```

#### 依赖声明

```go
{
    Key: "session_analytics",
    Name: "会话全景分析",
    Dependencies: []ModuleDependency{
        {Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, 
         Description: "提供增量摘要、上下文裁剪和压缩节省量分析"},
        {Key: "cache", Name: "会话缓存", Icon: "💾", Required: true,
         Description: "提供会话复用、缓存命中和节省量分析"},
        {Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true,
         Description: "提供风险识别、意图辅助和安全标签"},
        {Key: "output_compliance", Name: "输出合规检测", Icon: "🔒", Required: true,
         Description: "提供合规状态、脱敏结果和风险流向"},
    },
}
```

#### 状态计算逻辑

```go
func moduleStatusMap(defs []ModuleDefinition) map[string]ModuleWithStatus {
    statuses := make(map[string]ModuleWithStatus, len(defs))
    
    // 第一遍：计算基础状态
    for _, m := range defs {
        enabled, src := resolveModuleEnabled(m)
        statuses[m.Key] = ModuleWithStatus{
            ModuleDefinition:  m,
            Enabled:           enabled,
            Source:            src,
            CanToggleEnabled:  true,
        }
    }
    
    // 第二遍：计算依赖阻塞状态
    for key, status := range statuses {
        blocked := requiredDependencyBlockReason(statuses, status.ModuleDefinition)
        status.BlockedReason = blocked
        status.CanToggleEnabled = blocked == ""
        
        // 填充依赖模块的 enabled 状态
        if len(status.Dependencies) > 0 {
            deps := make([]ModuleDependency, 0, len(status.Dependencies))
            for _, dep := range status.Dependencies {
                dep.Enabled = statuses[dep.Key].Enabled
                deps = append(deps, dep)
            }
            status.Dependencies = deps
        }
        statuses[key] = status
    }
    return statuses
}

func requiredDependencyBlockReason(statuses map[string]ModuleWithStatus, mod ModuleDefinition) string {
    missing := make([]string, 0, len(mod.Dependencies))
    for _, dep := range mod.Dependencies {
        if !dep.Required {
            continue
        }
        if depStatus, ok := statuses[dep.Key]; !ok || !depStatus.Enabled {
            missing = append(missing, dep.Name)
        }
    }
    if len(missing) == 0 {
        return ""
    }
    return "需先启用依赖模块: " + strings.Join(missing, "、")
}
```

#### API 端点增强

**GET /api/admin/modules**
- 返回所有模块状态，包含 `can_toggle_enabled` 和 `blocked_reason`
- 每个依赖项包含实时 `enabled` 状态

**PUT /api/admin/modules/{key}/toggle**
- 启用前验证依赖：
```go
if body.Enabled {
    statusMap := moduleStatusMap(defs)
    if blockedReason := statusMap[found.Key].BlockedReason; blockedReason != "" {
        writeError(w, http.StatusConflict, blockedReason)
        return
    }
}
```

### 2. 前端类型与 UI 增强

#### 类型定义

```typescript
// web/src/api/modules.ts

export interface ModuleDependency {
  key: string
  name: string
  icon: string
  required: boolean
  description: string
  enabled?: boolean  // 运行时状态
}

export interface ModuleWithStatus extends ModuleDefinition {
  enabled: boolean
  source: string
  can_toggle_enabled: boolean
  blocked_reason?: string
}
```

#### 左侧模块卡片

```vue
<input
  type="checkbox"
  class="toggle-input"
  :checked="mod.enabled"
  :disabled="toggling === mod.key || (!mod.enabled && mod.can_toggle_enabled === false)"
  @change="doToggle(mod.key)"
/>
```

- `!mod.enabled && mod.can_toggle_enabled === false`：依赖未满足时禁止启用

#### 右侧详情页 - 依赖区块

```vue
<div v-if="dependencyStatus.length > 0" class="info-section dependency-section">
  <h3 class="section-title">{{ t('modulesView.overview.dependenciesTitle') }}</h3>
  <p v-if="selectedModule.blocked_reason" class="warning-banner">
    {{ selectedModule.blocked_reason }}
  </p>
  <div class="dependency-list">
    <div v-for="dep in dependencyStatus" :key="dep.key" class="dependency-item"
         :class="{ 'dep-enabled': dep.enabled, 'dep-disabled': !dep.enabled, 'dep-required': dep.required }">
      <span class="dep-status-icon">{{ dep.enabled ? '✅' : (dep.required ? '❌' : '⚠️') }}</span>
      <div class="dep-info">
        <span class="dep-name">{{ dep.moduleName }}</span>
        <span class="dep-desc">{{ dep.description }}</span>
      </div>
      <div class="dep-actions">
        <span class="dep-badge" :class="dep.required ? 'badge-required' : 'badge-optional'">
          {{ dep.required ? t('modulesView.overview.required') : t('modulesView.overview.optional') }}
        </span>
        <button class="btn-link" @click="goToSettings(dep.key)">
          {{ t('modulesView.overview.openDependency') }}
        </button>
      </div>
    </div>
  </div>
</div>
```

#### 右侧详情页 - 主操作按钮

```vue
<div class="info-section action-section">
  <button
    class="btn-action"
    :class="selectedEnabled ? 'btn-danger' : 'btn-primary'"
    :disabled="toggling === selectedModule.key || (!selectedEnabled && selectedModule.can_toggle_enabled === false)"
    @click="doToggle(selectedModule.key)"
  >
    {{ toggling === selectedModule.key ? t('modulesView.status.processing') : 
       selectedEnabled ? t('modulesView.status.enabledAction') : t('modulesView.status.disabledAction') }}
  </button>
  <button class="btn-ghost" @click="goToSettings(selectedModule.key)">
    {{ t('modulesView.overview.viewAllSettings') }}
  </button>
</div>
```

#### 依赖模块跳转

```typescript
function goToSettings(key: string) {
  if (key === 'compression' || key === 'cache') {
    router.push('/admin/compression')
    return
  }
  if (key === 'prompt_injection') {
    router.push('/admin/prompt-injection')
    return
  }
  if (key === 'session_analytics') {
    router.push('/admin/session-analytics')
    return
  }
  router.push('/admin/settings')
}
```

#### 切换逻辑增强

```typescript
async function doToggle(key: string) {
  const mod = modules.value.find(m => m.key === key)
  if (!mod) return
  
  // 依赖未满足时阻止启用
  if (!mod.enabled && mod.can_toggle_enabled === false) {
    error.value = mod.blocked_reason || t('modulesView.error.operationFailed')
    return
  }
  
  toggling.value = key
  error.value = null
  const prevEnabled = mod.enabled
  
  try {
    const r = await toggleModule(key, !prevEnabled)
    mod.enabled = r.enabled
    
    // 刷新状态以更新依赖计算
    await loadModules()
    
    if (selectedKey.value === key) {
      await selectModule(key)
    }
  } catch (e: any) {
    error.value = e.message || t('modulesView.error.operationFailed')
    mod.enabled = prevEnabled  // 回滚
  } finally {
    toggling.value = null
  }
}
```

### 3. 国际化修正

#### 中文（zh-CN/modulesView.ts）

```typescript
status: {
  enabled: '已启用',
  disabled: '已禁用',
  processing: '处理中…',
  enabledAction: '禁用此模块',      // 修正：已启用时显示"禁用"
  disabledAction: '启用此模块',     // 修正：已禁用时显示"启用"
},

overview: {
  dependenciesTitle: '依赖模块',
  required: '必需',
  optional: '可选',
  openDependency: '前往配置',
  // ...
}
```

#### 英文（en-US/modulesView.ts）

```typescript
overview: {
  sectionDependencies: 'Dependencies',
  dependenciesTitle: 'Dependencies',
  required: 'Required',
  optional: 'Optional',
  openDependency: 'Open settings',
  dependencyDisabled: 'This dependency is disabled',
  notEnabled: 'Not enabled',
}
```

## 功能验证

### 后端测试

```bash
# 单元测试
go test ./admin/...  # PASS

# API 测试
curl -sS http://127.0.0.1:8781/api/admin/modules | jq '.items[] | select(.key=="session_analytics") | {key, can_toggle_enabled, blocked_reason, dependencies: .dependencies | map({key, name, enabled, required})}'
```

预期输出（假设依赖未全部启用）：
```json
{
  "key": "session_analytics",
  "can_toggle_enabled": false,
  "blocked_reason": "需先启用依赖模块: 会话缓存、输出合规检测",
  "dependencies": [
    {"key": "compression", "name": "会话压缩", "enabled": true, "required": true},
    {"key": "cache", "name": "会话缓存", "enabled": false, "required": true},
    {"key": "prompt_injection", "name": "提示词注入检测", "enabled": true, "required": true},
    {"key": "output_compliance", "name": "输出合规检测", "enabled": false, "required": true}
  ]
}
```

### 前端构建

```bash
cd web
npm run build  # SUCCESS (安装 sass-embedded 后)
```

### UI 交互验证（预期行为）

1. **模块列表页**
   - 左侧卡片显示所有模块
   - 已启用模块的 toggle 为 ON
   - 依赖未满足的模块 toggle 禁用（灰色）

2. **session_analytics 详情页（依赖未满足）**
   - 右侧顶部显示黄色警告条：`需先启用依赖模块: 会话缓存、输出合规检测`
   - 依赖列表显示：
     - ✅ 会话压缩（必需）
     - ❌ 会话缓存（必需）- 红色 badge
     - ✅ 提示词注入检测（必需）
     - ❌ 输出合规检测（必需）- 红色 badge
   - 每个依赖项有"前往配置"按钮
   - 主操作按钮"启用此模块"禁用（灰色）

3. **启用依赖模块后**
   - 点击"前往配置"跳转到依赖模块页
   - 依次启用 cache 和 output_compliance
   - 返回 session_analytics 页面
   - 警告条消失，依赖列表全部 ✅
   - 主操作按钮变为可用，点击后成功启用

4. **已启用模块**
   - 按钮文案：`禁用此模块`（红色按钮）
   - 点击后模块禁用，按钮变为`启用此模块`（蓝色按钮）

## 代码变更总结

### 后端（Go）

**文件：admin/modules.go**
- 新增 `ModuleDependency` 类型（7 字段）
- 扩展 `ModuleWithStatus`（新增 `CanToggleEnabled`、`BlockedReason`）
- 新增 `moduleStatusMap()` 函数（依赖状态计算）
- 新增 `requiredDependencyBlockReason()` 函数（阻塞原因生成）
- **新增 `detectCircularDependencies()` 函数（循环依赖检测）** ✅
- 修改 `handleModulesList()`：返回完整状态
- 修改 `handleModulesGet()`：返回完整状态
- 修改 `handleModulesToggle()`：启用前验证依赖
- `session_analytics` 模块声明添加 `Dependencies` 字段
- **`allModuleDefinitions()` 初始化时自动检测循环** ✅

**文件：admin/modules_circular_test.go** ✅ **新增**
- 9 个循环依赖检测测试用例
- 覆盖无循环、简单循环、三节点循环、自循环、钻石图、子图循环等场景

### 前端（Vue + TypeScript）

**文件：web/src/api/modules.ts**
- 扩展 `ModuleDependency` 接口（新增 `icon`、`enabled`）
- 扩展 `ModuleWithStatus` 接口（新增 `can_toggle_enabled`、`blocked_reason`）

**文件：web/src/views/ModulesView.vue**
- 左侧卡片 toggle：添加 `can_toggle_enabled` 检查
- 右侧详情页：新增依赖区块（35 行）
- 新增主操作按钮（启用/禁用）
- `doToggle()` 函数：添加依赖验证
- `goToSettings()` 函数：添加模块跳转路由

**文件：web/src/locales/zh-CN/modulesView.ts**
- 修正 `enabledAction` / `disabledAction` 文案
- 新增依赖相关文案（`dependenciesTitle`、`required`、`optional`、`openDependency`）

**文件：web/src/locales/en-US/modulesView.ts**
- 新增依赖相关文案（英文版）

**文件：web/package.json**
- 新增 `sass-embedded` 依赖

## 设计决策

1. **依赖模型采用结构化对象而非简单字符串数组**
   - 原因：支持必需/可选、图标、说明文字、运行时状态
   - 扩展性：未来可添加版本约束、循环依赖检测

2. **状态计算在每次 API 调用时动态计算**
   - 原因：避免缓存不一致
   - 性能：模块数量有限（<20），计算开销可忽略

3. **前端依赖状态展示分离左右两侧**
   - 左侧：轻量化，仅 toggle 禁用状态
   - 右侧：完整依赖列表 + blocked_reason + 跳转按钮

4. **blocked_reason 采用中文直接返回**
   - 原因：后端无 i18n 框架，前端国际化成本高
   - 折中：英文环境显示中文提示（可接受）

5. **依赖跳转硬编码路由映射**
   - 原因：模块到路由的映射无统一规范
   - 未来优化：在 `ModuleDefinition` 中新增 `settingsRoute` 字段

6. **循环依赖检测在启动时执行** ✅
   - 原因：依赖图是静态的，启动时一次性检测即可
   - 失败模式：panic 并报告循环路径，防止启动
   - 性能：O(V+E) DFS 遍历，模块数量少（<20），开销可忽略

## 后续优化建议

1. **循环依赖检测** ✅ **已实现**
   - 在 `allModuleDefinitions()` 初始化时自动检测依赖图循环
   - 使用 DFS 算法遍历依赖图，检测 visiting 状态节点
   - 检测到循环时立即 panic，提供清晰的循环路径（如：`a -> b -> c -> a`）
   - 启动失败时报错并拒绝启动，防止运行时依赖死锁
   - 已覆盖 9 个测试场景：无循环、简单循环、三节点循环、自循环、钻石图、子图循环、不存在依赖、空图、单节点

2. **依赖自动启用**
   - 扩展 `ModuleDependency` 支持版本范围
   - 示例：`{Key: "compression", MinVersion: "1.2.0"}`

3. **依赖自动启用**
   - 启用模块时询问用户是否自动启用所有必需依赖
   - 提供一键启用链路

4. **模块启用顺序优化**
   - 拓扑排序计算最优启用顺序
   - 前端显示推荐启用步骤

5. **依赖关系可视化**
   - 添加依赖图可视化（DAG 图）
   - 高亮当前模块的上游/下游依赖

## 参考资料

- 代码提交：`4611468b feat(modules): fix module toggle button labels and add dependency UI`
- 相关 PR：（待补充）
- 设计文档：本文档
- API 文档：`/api/admin/modules` 端点说明（待补充）
