# 模型别名设计（Model Alias Prefix）

> **状态**：已接受（2026-08-25）
> **范围**：网关接收客户端模型请求时，对 `model` 字段的统一前缀剥离行为。
> **目的**：解决 IDE 客户端（如 Cursor、Claude Code、Kiro、OpenCode 等）的模型名称冲突问题，让客户端可以使用业务命名的别名（如 `kx-gpt-5.6-terra`），网关自动转换为真实模型名（如 `gpt-5.6-terra`）。

---

## 1. 问题

不同 IDE 客户端在选择模型时存在名称冲突：

- **Cursor / Claude Code / Kiro / OpenCode** 等客户端预置的模型命名空间相互冲突；
- 客户端内置的模型下拉菜单无法编辑，但不同 IDE 可能使用相同的模型名称指代不同的真实模型；
- 当同一个客户端使用 `gpt-4o` 时，业务期望它指向 OpenAI，但实际 IDE 把它映射到了某个内部分发链路；
- 业务希望统一使用业务语义化的模型名（如 `kx-gpt-5.6-terra`），但网关无法直接识别这个名称。

---

## 2. 设计目标

- **客户端** 在请求时使用业务命名的模型（如 `kx-gpt-5.6-terra`）；
- **网关** 接收请求后自动剥离前缀（`kx-`），将 `kx-gpt-5.6-terra` 转为 `gpt-5.6-terra`，然后进入正常的路由流程；
- 前缀默认是 `kx-`，但可通过配置文件修改；
- 配置支持 **热更新**（修改 yaml 后重启加载即可，无需重启进程）。

---

## 3. 实现位置

### 3.1 配置层（`config/config.go`）

新增 `ModelAliasPrefix` 字段：

```go
// ModelAliasPrefix is the client-facing model name prefix that gets stripped
// before internal routing. When clients send "kx-gpt-5.6-terra", the gateway
// strips this prefix and routes to "gpt-5.6-terra". Default "kx-" can be
// overridden via LLM_GATEWAY_MODEL_ALIAS_PREFIX or yaml "model_alias_prefix".
// Empty string disables alias stripping.
ModelAliasPrefix string `yaml:"model_alias_prefix" env:"LLM_GATEWAY_MODEL_ALIAS_PREFIX"`
```

默认值与覆盖层级：

| 来源 | 示例 |
|---|---|
| 默认值 | `kx-` |
| 环境变量 | `LLM_GATEWAY_MODEL_ALIAS_PREFIX=alias-` |
| YAML 配置 | `model_alias_prefix: "alias-"` |
| 优先级 | 环境变量 > YAML > 默认值（环境变量或 YAML 显式空字符串可禁用） |

### 3.2 模型名称处理层（`modelname/normalize.go`）

新增 `StripAliasPrefix` 函数：

```go
func StripAliasPrefix(model, prefix string) string {
    if prefix == "" {
        return model
    }
    modelLower := strings.ToLower(model)
    prefixLower := strings.ToLower(prefix)
    if strings.HasPrefix(modelLower, prefixLower) {
        stripped := model[len(prefix):]
        return strings.TrimPrefix(stripped, prefix)
    }
    return model
}
```

- 接受 `model` 与 `prefix` 两个参数（避免在函数内引用全局状态，便于单测）；
- 匹配是大小写不敏感的，因为模型名称在 canonicalize 之前会 lowercase；
- 前缀为空时直接返回原值（即禁用别名）；
- 只剥离前缀一次（不重复剥，例如 `kx-kx-gpt-5.6` → `kx-gpt-5.6`，不会变成 `gpt-5.6`，避免客户端依赖多次剥离）。

### 3.3 运行时访问层（`domains/streaming/stream_runtime.go`）

新增 `ModelAliasPrefix()` 函数，从 `config.Store` 读取最新配置：

```go
func ModelAliasPrefix() string {
    if store := streamConfigStore.Load(); store != nil {
        if cfg := store.Get(); cfg != nil {
            return cfg.ModelAliasPrefix
        }
    }
    return envOrDefault("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "kx-")
}
```

- 优先读 Store 里的实时配置（热更新路径）；
- Store 不可用时 fallback 到环境变量或默认值；环境变量显式为空时表示禁用。
- 与 `StreamTimeout()`、`UpstreamTimeout()` 等其他热更新旋钮保持一致模式。

### 3.4 请求处理层（`domains/streaming/handler.go`）

在 `CanonicalizeClientModel` 调用之前插入别名剥离：

```go
// 2026-08-25: strip client-facing alias prefix (e.g. "kx-").
// Must run before CanonicalizeClientModel so the alias is removed
// before canonicalization and SQL lookup.
modelAfterStrip := reqBody.Model
if prefix := ModelAliasPrefix(); prefix != "" {
    modelAfterStrip = modelname.StripAliasPrefix(reqBody.Model, prefix)
    if modelAfterStrip != reqBody.Model {
        slog.Debug("handler: alias prefix stripped",
            "original", reqBody.Model,
            "prefix", prefix,
            "stripped", modelAfterStrip,
            "request_id", requestID)
    }
}

// 2026-07-14: enforce lowercase at the wire boundary...
clientModel := modelname.CanonicalizeClientModel(modelAfterStrip)
```

**位置选择原因**：

- 必须放在 `CanonicalizeClientModel` **之前**：剥离后的名称才进入 canonicalize → SQL 匹配；
- 必须在 **Format Detection & Auto-Fix 之后**：避免 format fix 干扰；
- 必须放在 `reqBody.Model` 被复制之前：直接操作 `reqBody.Model`，对调用链透明。

---

## 4. 数据流

```
客户端请求
   POST /v1/chat/completions
   {
     "model": "kx-gpt-5.6-terra",
     "messages": [...]
   }
              │
              ▼
   ┌──────────────────────────────┐
   │  Format Detection & Auto-Fix │
   └──────────────────────────────┘
              │
              ▼ (reqBody.Model == "kx-gpt-5.6-terra")
   ┌──────────────────────────────┐
   │  StripAliasPrefix            │  ◄── 配置：ModelAliasPrefix = "kx-"
   │  kx-gpt-5.6-terra → gpt-5.6-terra
   └──────────────────────────────┘
              │
              ▼ (modelAfterStrip == "gpt-5.6-terra")
   ┌──────────────────────────────┐
   │  CanonicalizeClientModel     │  ◄── 小写化、去 vendor 前缀
   │  gpt-5.6-terra → gpt-5.6-terra
   └──────────────────────────────┘
              │
              ▼ (clientModel == "gpt-5.6-terra")
   ┌──────────────────────────────┐
   │  Resolve Candidates          │  ◄── SQL 匹配 models_canonical / model_aliases
   └──────────────────────────────┘
              │
              ▼
   upstream.Send(model="gpt-5.6-terra")
```

---

## 5. 配置热更新

`config.Store` 的 `ReloadFile` 流程已经支持热更新 yaml 配置：

```bash
# 修改 config.yaml 后，无需重启进程
curl -X POST http://localhost:8781/api/admin/config/reload
# 或 POST /admin/config/reload
```

热更新后，`ModelAliasPrefix()` 立即返回新值，无需重启进程。

如果 Store 不可用（如 hotconfig 还未加载），fallback 到环境变量：

```
启动序列：
1. cfg := Load()                    // 从 env / yaml 读取 ModelAliasPrefix
2. cfgStore := config.NewStore(cfg) // 包装为热更新 Store
3. streaming.SetConfigStore(cfgStore) // streaming 包读取 Store
```

---

## 6. 测试覆盖

### 6.1 单元测试

- **`modelname/normalize_test.go::TestStripAliasPrefix`** —— 8 个用例覆盖：
  - 前缀匹配成功；
  - 大小写不敏感；
  - 前缀不存在（返回原值）；
  - 空前缀（禁用别名）；
  - 模型名比前缀短（返回原值）；
  - 自定义前缀；
  - 模型名恰好等于前缀（返回空）；
  - 不同前缀不剥离。

- **`domains/streaming/stream_runtime_test.go::TestModelAliasPrefix*`** —— 4 个用例覆盖：
  - 默认值（store 中未设置）；
  - Store 值生效；
  - Store 设为空（禁用）；
  - Store 不可用时 fallback 到 env。

### 6.2 集成测试

`handler.go` 中的处理逻辑不依赖外部状态，可通过构造 `reqBody` 直接验证。集成测试位于 `domains/streaming/handler_alias_test.go`：

- 完整请求：客户端发送 `kx-gpt-5.6-terra`，期望 SQL 查询使用 `gpt-5.6-terra`；
- 无前缀：`gpt-5.6-terra` 不变；
- 配置禁用：Store 中 `ModelAliasPrefix=""`，所有模型名都不变。

---

## 7. 兼容性

- **默认行为向后兼容**：默认前缀 `kx-` 是新行为，但客户端如果原本就用 `kx-gpt-5.6-terra`，现在能正确路由到真实模型；
- **空前缀禁用**：可通过 `LLM_GATEWAY_MODEL_ALIAS_PREFIX=""` 完全禁用，不影响任何路由；
- **数据库 schema 不变**：本特性是**纯请求处理层**逻辑，不修改 `models_canonical` / `model_aliases` 等表；
- **SQL 查询不变**：下游的 SQL 匹配仍使用真实模型名（如 `gpt-5.6-terra`），与原有逻辑一致。

---

## 8. 反模式（禁止）

| 反模式 | 原因 |
|---|---|
| ❌ 在 SQL 层做别名转换 | 破坏 DB 真相；下游 metrics / analytics / billing 都会看到别名而非真实模型 |
| ❌ 把别名存储到 `model_aliases` 表 | 该表用于 vendor 同模型的不同标识符（如 `glm-5.1` ↔ `GLM-5.1`），不应混入业务别名 |
| ❌ 在 Format Detection 之前剥离 | format fix 依赖原始 `model` 字段做格式推断 |
| ❌ 在 CanonicalizeClientModel 之后剥离 | `model_aliases.raw_name` 已经按真实模型名存储，再剥离会找不到 |
| ❌ 重复剥离（`kx-kx-gpt-5.6` → `gpt-5.6`） | 客户端不应依赖多次剥离；只剥一次更可控 |
| ❌ 把前缀硬编码到代码 | 必须是可配置的，业务命名空间可能演进 |

---

## 9. 监控与日志

调试日志（debug level）：

```
handler: alias prefix stripped
  original=    "kx-gpt-5.6-terra"
  prefix=      "kx-"
  stripped=    "gpt-5.6-terra"
  request_id=  req_xxx
```

可在 `slog` 级别调整为 `debug` 时观察到别名剥离行为。生产环境默认 `info` 级别，不会被记录。

如需统计别名使用量，可在 `request_logs` 表加列 `alias_used BOOL` 或在 `logCtx` 中记录别名剥离事件。

---

## 10. 演进路径

| 阶段 | 描述 |
|---|---|
| **v1（当前）** | 单一全局前缀，所有客户端共享 |
| **v2（未来）** | 按租户配置前缀（每个 IDE 集成商一个前缀）：`tenant_alias_prefixes` map[string]string |
| **v3（未来）** | 按 HTTP header 动态选择前缀：`X-Kx-Alias-Prefix` |

v1 已是 90% 的需求覆盖；v2/v3 仅当业务命名空间需要多租户隔离时才需要。
