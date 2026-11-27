# 三套 Capability 词表设计说明

## 文档信息

- **创建日期**: 2026-11-27
- **版本**: v1.0
- **状态**: 正式发布
- **作者**: 插件化框架审计工作组
- **相关门禁**: magpie_p0_plugin_capability_holes_test.go G4

## 1. 概述

llm-gateway 插件化框架使用**三套完全不相交的 capability 词表**来管理不同维度的权限控制。这种设计防止了"声明了能力 A 就能执行操作 B"的越权问题。

### 1.1 设计原则

- **职责分离**：每套词表管理独立的权限维度
- **不相交约束**：三套词表之间没有交集，门禁 G4 保证
- **失败关闭**：未声明的能力永不授予
- **单一事实源**：每套词表在代码中有唯一定义位置

### 1.2 核心风险

**命名相似导致混淆**：
- `session.read`（路由能力）
- `session.observe`（绑定能力）
- `session.audit`（商业能力）

这三个能力名称看起来相似，但管控完全不同的权限。开发者必须理解它们的区别，避免误用。

## 2. 三套词表详解

### 2.1 路由能力（Routing Capabilities）

#### 定义位置
- **文件**: `plugin-runtime/capability.go`
- **常量定义**: 第 56-70 行
- **路由表**: 第 73-87 行

#### 用途
管控**插件调用 Gateway HTTP API 的权限**。插件通过 HTTP 请求访问 Gateway 提供的接口时，中间件会检查该插件是否具有对应的路由能力。

#### 完整词表

| 能力 ID | 对应路由 | 状态 | 说明 |
|---------|---------|------|------|
| `session.read` | `GET /_gateway/plugin/v1/sessions` | ✅ 已实现 | 读取会话列表和详情 |
| `analytics.read` | `GET /_gateway/plugin/v1/analytics` | ✅ 已实现 | 读取分析数据 |
| `body.fetch` | `GET /_gateway/plugin/v1/bodies` | ❌ 未实现 | 获取请求/响应 body（需真沙箱） |
| `policy.serve` | `GET /_gateway/plugin/v1/interception-policies` | ❌ 未实现 | 提供拦截策略（需真沙箱） |
| `events.ingest` | `POST /_gateway/plugin/v1/events` | ❌ 未实现 | 上报事件（需事件投递面） |

#### 检查点
**HTTP 中间件** `RequireCapability(capability string)`
- 位置: `plugin-runtime/capability.go:134-158`
- 时机: 插件发起 HTTP 请求时
- 拒绝行为: 返回 403 Forbidden + `capability_denied` 错误码

#### 注册机制
```go
// plugin-runtime/capability.go:182-207
func (r *CapabilityRegistry) RegisterManifest(m *Manifest) error {
    // 1. 识别未实现的能力
    rejected := RejectUnimplementedCapabilities(m.Capabilities)
    
    // 2. 登记已实现的能力
    set := make(map[string]struct{})
    for _, capability := range m.Capabilities {
        c := strings.TrimSpace(capability)
        if c == "" { continue }
        set[c] = struct{}{}
    }
    
    // 3. 移除被拒的能力（永不授予）
    for _, bad := range rejected {
        delete(set, bad)
    }
    
    r.declared[m.PluginID] = set
    
    // 4. 若有被拒能力，返回错误（但允许的能力已登记）
    if len(rejected) > 0 {
        return fmt.Errorf("declares unimplemented capabilities %v", rejected)
    }
    return nil
}
```

#### 设计决策
**为什么 3 条路由未实现？**

来自 `plugin-runtime/capability.go:32-42` 的注释：
> `body.fetch` 和 `interception-policies` 都是数据面能力，而本仓的沙箱策略明确拒绝一切数据面写能力（sandbox.go:11-17："data-plane write capability (mutate/block/tool.execute) is refused until a real sandbox is available"，且 DefaultSandboxPolicy().Available = false）。在没有真沙箱之前实现它们，等于把"声明了但执行被策略全拒"的老问题换个位置再犯一次。

**处置策略**：
1. 在路由表标注 `Implemented: false`
2. manifest 声明这些能力 ⇒ 立即拒绝（RegisterManifest 返回错误）
3. 该能力永远不被授予 ⇒ 访问路径必然 403 capability_denied
4. 门禁 G1 扫描 cmd/gateway 路由注册逐条对拍

---

### 2.2 绑定能力（Binding Capabilities）

#### 定义位置
- **文件**: `plugin-runtime/types.go`
- **常量定义**: 第 68-84 行

#### 用途
管控**插件挂载到请求生命周期阶段的权限**。插件通过 `PluginBinding` 声明要挂载到哪个 phase，以及在该 phase 能做什么操作。

#### 完整词表

| 能力 ID | 类型 | 说明 | 沙箱要求 |
|---------|------|------|---------|
| `request.observe` | 读 | 观察请求数据（只读） | ✅ 无沙箱可用 |
| `request.mutate` | 写 | 修改请求数据 | ❌ 需要真沙箱 |
| `request.block` | 写 | 阻断请求 | ❌ 需要真沙箱 |
| `session.observe` | 读 | 观察会话数据（只读） | ✅ 无沙箱可用 |
| `session.close` | 写 | 关闭会话 | ✅ 无沙箱可用（审计类） |
| `response.observe` | 读 | 观察响应数据（只读） | ✅ 无沙箱可用 |
| `response.mutate` | 写 | 修改响应数据 | ❌ 需要真沙箱 |
| `response.block` | 写 | 阻断响应 | ❌ 需要真沙箱 |
| `response.stream` | 读 | 观察流式响应 | ✅ 无沙箱可用 |
| `tool.observe` | 读 | 观察工具调用（只读） | ✅ 无沙箱可用 |
| `tool.mutate` | 写 | 修改工具调用 | ❌ 需要真沙箱 |
| `tool.block` | 写 | 阻断工具调用 | ❌ 需要真沙箱 |
| `tool.execute` | 写 | 执行工具（代理执行） | ❌ 需要真沙箱 |
| `audit.emit` | 写 | 发出审计事件 | ✅ 无沙箱可用（审计类） |
| `durable.consume` | 读 | 消费持久化消息 | ✅ 无沙箱可用 |

#### 检查点
**绑定校验** `ValidateBinding(pluginID string, b PluginBinding, opts BindingValidationOptions)`
- 位置: `plugin-runtime/binding.go:61-107`
- 时机: 加载 manifest 时
- 拒绝行为: 返回错误，manifest 加载失败

#### 沙箱策略

```go
// plugin-runtime/binding.go:102
if !opts.SandboxAvailable && isWriteCapability(capability) {
    return fmt.Errorf("binding %q: capability %q requires sandbox", b.BindingID, capability)
}

func isWriteCapability(capability string) bool {
    return strings.HasSuffix(capability, ".mutate") ||
           strings.HasSuffix(capability, ".block") ||
           capability == CapabilityToolExecute
}
```

**默认沙箱策略**（plugin-runtime/sandbox.go）：
```go
func DefaultSandboxPolicy() SandboxPolicy {
    return SandboxPolicy{
        Available: false,  // ★ 默认沙箱不可用
        // ... 其他字段
    }
}
```

**门禁保护**（magpie_p0_plugin_capability_holes_test.go G3）：
- 断言生产代码不得出现 `Available: true` 的沙箱策略
- 断言 `DefaultSandboxPolicy().Available` 必须是 false
- 断言 `ValidateSandboxPolicy` 对"自称可用但缺配置"的策略必须报错

#### 生命周期阶段（Phase）

绑定能力挂载到以下 16 个 phase：

| Phase | 中文 | 失败策略 |
|-------|------|---------|
| `authentication` | 认证 | fail_closed（关键） |
| `pre_routing` | 路由前 | fail_closed（关键） |
| `routing` | 路由中 | fail_closed（关键） |
| `pre_upstream` | 上游前 | fail_open |
| `upstream` | 上游调用 | fail_closed（关键） |
| `post_upstream` | 上游后 | fail_open |
| `streaming` | 流式处理 | fail_open |
| `post_response` | 响应后 | fail_open |
| `governance` | 治理 | fail_open |
| `transform` | 转换 | fail_open |
| `tool` | 工具 | fail_open |
| `analysis` | 分析 | fail_open |
| `session_close` | 会话关闭 | fail_open |
| `request` | 请求处理 | fail_open |
| `response` | 响应处理 | fail_open |
| `stream` | 流处理 | fail_open |

**失败策略说明**：
- `fail_closed`：hook 失败必须终止请求（关键 phase）
- `fail_open`：hook 失败继续处理（非关键 phase）

---

### 2.3 商业能力（Commercial Capabilities）

#### 定义位置
- **文件**: `internal/capability/capability.go`
- **注册**: 第 103-121 行

#### 用途
管控**客户购买的商业功能单元**。这是面向最终用户的能力，决定了 API Key 能使用哪些高级功能。

#### 完整词表

| 能力 ID | 功能模块 | 说明 | 依赖 |
|---------|---------|------|------|
| `session.audit` | 会话审计 | 查看会话审计日志 | 无 |
| `session.forensics` | 会话取证 | 深度分析会话数据 | `session.audit` |
| `session.summary` | 会话摘要 | 生成会话摘要 | 无 |
| `analytics.timeline` | 时间线分析 | 查看使用时间线 | 无 |
| `analytics.advanced` | 高级分析 | 高级分析功能 | `analytics.timeline` |
| `model.routing` | 模型路由 | 自定义模型路由规则 | 无 |

#### 检查点
**API Key 校验**
- 位置: API 层中间件（具体位置待补充）
- 时机: 用户调用功能 API 时
- 拒绝行为: 返回 403 Forbidden + 功能未购买提示

#### 注册机制

```go
// internal/capability/capability.go:103-121
func (r *Registry) Register(c Capability) error {
    // 1. 能力 ID 必须是 [a-z0-9.]+ 格式
    if !isLowerDotted(c.ID) {
        return fmt.Errorf("invalid capability id %q", c.ID)
    }
    
    // 2. 防止重复注册
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.byID[c.ID]; exists {
        return fmt.Errorf("capability %q already registered", c.ID)
    }
    
    r.byID[c.ID] = c
    return nil
}
```

#### 依赖闭包校验

```go
// internal/capability/capability.go:ValidateSet
func (r *Registry) ValidateSet(ids []string) error {
    seen := map[string]bool{}
    for _, id := range ids {
        c, err := r.Lookup(id)
        if err != nil { return err }
        
        // 检查依赖是否都在集合中
        for _, dep := range c.Dependencies {
            if !seen[dep] {
                return fmt.Errorf("capability %q requires %q", id, dep)
            }
        }
        seen[id] = true
    }
    return nil
}
```

**示例**：
- 如果客户购买了 `session.forensics`，必须同时拥有 `session.audit`
- 如果客户购买了 `analytics.advanced`，必须同时拥有 `analytics.timeline`

---

## 3. 三套词表关系矩阵

### 3.1 不相交约束

| 维度 | 路由能力 | 绑定能力 | 商业能力 |
|------|---------|---------|---------|
| **管控对象** | 插件 HTTP 调用 | 插件生命周期挂载 | 客户功能购买 |
| **检查时机** | HTTP 请求时 | Manifest 加载时 | API 调用时 |
| **词表示例** | session.read | session.observe | session.audit |
| **交集** | ∅ | ∅ | ∅ |

**门禁保护**（magpie_p0_plugin_capability_holes_test.go G4）：
```go
// 断言路由能力与绑定能力不得有交集
routingCaps := {"session.read", "analytics.read", "body.fetch", "policy.serve", "events.ingest"}
bindingCaps := {"request.observe", "request.mutate", "session.observe", "tool.execute", ...}
assert routingCaps ∩ bindingCaps = ∅
```

### 3.2 命名对比

**容易混淆的能力名称**：

| 路由能力 | 绑定能力 | 商业能力 | 区别 |
|---------|---------|---------|------|
| `session.read` | `session.observe` | `session.audit` | read=HTTP读会话；observe=生命周期观察；audit=商业审计 |
| `analytics.read` | - | `analytics.timeline` | read=HTTP读分析数据；timeline=商业时间线功能 |
| - | `request.observe` | - | observe=生命周期观察请求 |
| - | `tool.execute` | - | execute=代理执行工具 |

**记忆口诀**：
- `.read` → 路由能力（HTTP API）
- `.observe` / `.mutate` / `.block` / `.execute` → 绑定能力（生命周期）
- `.audit` / `.forensics` / `.timeline` → 商业能力（功能购买）

### 3.3 权限流转示例

**场景**：一个插件需要在请求路由阶段读取会话数据，并通过 HTTP API 获取历史分析数据。

**所需能力**：
1. **绑定能力** `session.observe`：在 routing phase 观察会话数据
2. **路由能力** `session.read`：通过 HTTP 读取会话详情
3. **路由能力** `analytics.read`：通过 HTTP 读取分析数据

**Manifest 声明**：
```json
{
  "capabilities": ["session.read", "analytics.read"],
  "bindings": [{
    "binding_id": "routing-session-check",
    "phase": "routing",
    "capabilities": ["session.observe"],
    "execution_mode": "sequential",
    "failure_policy": "fail_closed"
  }]
}
```

**权限校验流程**：
1. **加载时**：`ValidateBinding` 检查 `session.observe` 在绑定能力词表内
2. **加载时**：`RegisterManifest` 检查 `session.read` 和 `analytics.read` 在路由能力词表内且已实现
3. **运行时**：routing phase 执行时，检查插件有 `session.observe` 绑定能力
4. **运行时**：插件发起 `GET /_gateway/plugin/v1/sessions` 时，`RequireCapability("session.read")` 检查通过
5. **运行时**：插件发起 `GET /_gateway/plugin/v1/analytics` 时，`RequireCapability("analytics.read")` 检查通过

---

## 4. 为什么设计成三套独立词表？

### 4.1 设计动机

**问题**：如果用一套统一的 capability 词表，会发生什么？

**风险 1：权限越界**
```go
// 如果只有一套词表
manifest.Capabilities = ["session.read"]

// 开发者误以为声明了 session.read 就能在生命周期中观察会话
binding.Capabilities = ["session.read"]  // ❌ 错误：路由能力被用作绑定能力

// 或者反过来
manifest.Capabilities = ["session.observe"]
http.GET("/_gateway/plugin/v1/sessions")  // ❌ 错误：绑定能力被用作路由能力
```

**风险 2：语义混淆**
- `session.read` 应该表示"通过 HTTP API 读取会话"还是"在生命周期中观察会话"？
- 一旦混用，"声明了 A ⇒ 被允许做 B"这种越权就会静悄悄发生

**风险 3：依赖闭包污染**
- 商业能力有依赖关系（`session.forensics` 依赖 `session.audit`）
- 如果与插件能力混在一起，客户购买行为会影响插件权限判断

### 4.2 三套词表的优势

| 优势 | 说明 |
|------|------|
| **职责清晰** | 每套词表管理独立的权限维度，不会越界 |
| **门禁保护** | G4 断言确保词表不相交，自动化防止混用 |
| **语义明确** | `session.read` vs `session.observe` vs `session.audit` 各司其职 |
| **独立演进** | 路由能力可以扩展新 API，不影响绑定能力和商业能力 |
| **依赖隔离** | 商业能力的依赖关系不会污染插件能力判断 |

### 4.3 代价

| 代价 | 缓解措施 |
|------|---------|
| **命名相似导致混淆** | 本文档 + 命名规范（.read/.observe/.audit） |
| **新人学习曲线** | 本文档 + 端到端插件开发指南 |
| **三处维护** | 单一事实源 + 门禁 G1-G4 防止同源漂移 |

---

## 5. 开发规范

### 5.1 添加新能力的规范

#### 路由能力
1. 在 `plugin-runtime/capability.go` 添加常量定义
2. 在路由表添加 `RouteCapability` 条目，标注 `Implemented` 状态
3. 如果未实现，在 `RejectUnimplementedCapabilities` 添加拒绝逻辑
4. 在 `cmd/gateway` 注册对应的 HTTP 路由处理器
5. 运行门禁 G1 确保"是否实现"与真实路由注册一致

#### 绑定能力
1. 在 `plugin-runtime/types.go` 添加常量定义
2. 如果是写能力，确认沙箱策略是否允许
3. 更新 `isWriteCapability` 函数（如果需要）
4. 在 `domains/hooks` 实现对应的 Hook 执行逻辑
5. 运行门禁 G4 确保不与路由能力重名

#### 商业能力
1. 在 `internal/capability` 调用 `Register()` 注册
2. 定义 `Capability` 结构（ID、DisplayName、Description、Dependencies）
3. 在 API 层添加对应的功能实现
4. 添加 API Key 校验中间件

### 5.2 命名规范

| 词表 | 命名模式 | 示例 |
|------|---------|------|
| **路由能力** | `<资源>.read` / `<资源>.write` | `session.read`, `analytics.read` |
| **绑定能力** | `<阶段>.<操作>` | `request.observe`, `tool.execute` |
| **商业能力** | `<模块>.<功能>` | `session.audit`, `analytics.timeline` |

**禁止事项**：
- ❌ 不同词表使用相同的能力 ID
- ❌ 路由能力使用 `.observe` 后缀
- ❌ 绑定能力使用 `.read` 后缀
- ❌ 商业能力使用 `.mutate` / `.block` 后缀

### 5.3 门禁检查清单

在提交代码前，确保通过以下门禁：

- [ ] **G1**：路由能力的"是否实现"与真实路由注册一致
- [ ] **G2**：manifest 声明未实现的能力 → 拒绝且永不授予
- [ ] **G3**：沙箱可用性不可由业务代码控制
- [ ] **G4**：路由能力与绑定能力词表不相交

运行命令：
```bash
cd scripts/ursmcheck
go test -run TestMagpieP0PluginCapabilityHoles
go test -run TestMagpieP1FailurePolicy
```

---

## 6. 常见问题

### Q1: 为什么不能在 manifest.capabilities 中声明 `session.observe`？

**答**：`session.observe` 是**绑定能力**，只能在 `bindings[].capabilities` 中声明。`manifest.capabilities` 只接受**路由能力**。

如果需要在生命周期中观察会话，应该这样声明：
```json
{
  "bindings": [{
    "phase": "routing",
    "capabilities": ["session.observe"]
  }]
}
```

### Q2: 为什么声明了 `session.read` 还不能在 routing phase 读取会话数据？

**答**：`session.read` 是**路由能力**，只允许通过 HTTP API 调用 `GET /_gateway/plugin/v1/sessions`。在生命周期中读取会话数据需要**绑定能力** `session.observe`。

正确做法：
```json
{
  "capabilities": ["session.read"],
  "bindings": [{
    "phase": "routing",
    "capabilities": ["session.observe"]
  }]
}
```

### Q3: 为什么声明了 `request.mutate` 会被拒绝？

**答**：`request.mutate` 是**写能力**，需要真沙箱才能使用。当前 `DefaultSandboxPolicy().Available = false`，所以所有写能力都被拒绝。

**被拒的 7 个写能力**：
- `request.mutate`, `request.block`
- `response.mutate`, `response.block`
- `tool.mutate`, `tool.block`, `tool.execute`

**解决方案**：等待真沙箱实现（cgroup v2 + seccomp）。

### Q4: `session.audit` 和 `session.read` 有什么区别？

**答**：
- `session.read`（路由能力）：插件通过 HTTP API 读取会话数据
- `session.audit`（商业能力）：客户购买的会话审计功能

两者管控不同的权限维度，完全不相交。

### Q5: 如何判断一个能力属于哪套词表?

**答**：
1. 看定义位置：
   - `plugin-runtime/capability.go` → 路由能力
   - `plugin-runtime/types.go` → 绑定能力
   - `internal/capability` → 商业能力

2. 看命名后缀：
   - `.read` / `.write` → 路由能力
   - `.observe` / `.mutate` / `.block` / `.execute` → 绑定能力
   - `.audit` / `.forensics` / `.timeline` → 商业能力

3. 看使用位置：
   - `manifest.capabilities` → 路由能力
   - `bindings[].capabilities` → 绑定能力
   - `API Key.capabilities` → 商业能力

---

## 7. 参考资料

### 7.1 源码文件

| 文件 | 说明 |
|------|------|
| `plugin-runtime/capability.go` | 路由能力定义和注册 |
| `plugin-runtime/types.go` | 绑定能力定义和 Manifest 结构 |
| `plugin-runtime/binding.go` | 绑定能力校验 |
| `plugin-runtime/sandbox.go` | 沙箱策略 |
| `internal/capability/capability.go` | 商业能力注册表 |
| `scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go` | 门禁 G1-G4 |

### 7.2 相关文档

- [plugin-runtime v2 ADR](../../../docs/adr/2026-09-04-plugin-runtime-v2-capability-lifecycle.md)
- [插件化框架审计报告](../../../docs/audit/llm-gateway-plugin-framework-audit-2026-11-26.html)
- [端到端插件开发指南](./plugin-development-guide.md)（待补充）

### 7.3 门禁测试

```bash
# 运行能力词表门禁
cd scripts/ursmcheck
go test -v -run TestMagpieP0PluginCapabilityHoles

# 运行失败策略门禁
go test -v -run TestMagpieP1FailurePolicy
```

---

## 8. 更新日志

| 版本 | 日期 | 作者 | 变更 |
|------|------|------|------|
| v1.0 | 2026-11-27 | 插件化框架审计工作组 | 初始版本，基于审计报告整理 |
