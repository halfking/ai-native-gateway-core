# Task 3: SessionCompressor V2 集成实施指南

> **状态**: 🔄 进行中  
> **文件**: `domains/hooks/compression/session_compressor.go`  
> **复杂度**: 高（648行核心文件）

---

## ✅ 已完成

1. ✅ 修改 `SessionCompressorDeps` 结构（已添加 CacheV2 和 Builder 字段）

---

## 🔧 待实施的修改

### 修改 1: 在文件末尾添加辅助方法

在 `session_compressor.go` 文件末尾（648行之后）添加以下方法：

```go
// ─────────────────────────────────────────────────────────────
// V2 Integration Helpers (Phase V2-2.4)
// ─────────────────────────────────────────────────────────────

// shouldUseV2 determines whether to use V2 cache architecture.
//
// Returns true when:
//   1. V2 components (CacheV2 + Builder) are available, AND
//   2. Feature Flag "sessions_v2_compression_read" is enabled for tenant
func (sc *SessionCompressor) shouldUseV2(tenantID string) bool {
	if sc == nil || sc.deps == nil {
		return false
	}

	// Check V2 components availability
	if sc.deps.CacheV2 == nil || sc.deps.Builder == nil {
		return false
	}

	// Check Feature Flag
	// TODO: Implement Feature Flag check
	// return settings.GetTenantBool(tenantID, "sessions_v2_compression_read", false)
	
	// For now, disabled by default until Feature Flag is implemented
	return false
}

// tryLoadV2State attempts to load session state from V2 architecture.
//
// Returns:
//   - lastOutboundBody: the message array last sent to LLM (JSON marshaled)
//   - ok: true if V2 load succeeded, false to fallback to V1
//
// On V2 error, automatically logs and returns ok=false for V1 fallback.
func (sc *SessionCompressor) tryLoadV2State(
	ctx context.Context,
	tenantID, sessionID string,
) (lastOutboundBody []byte, ok bool) {
	// Call CacheV2.Get() - returns interface{}
	stateInterface, err := sc.deps.CacheV2.Get(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 cache get failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	if stateInterface == nil {
		// New session, no previous state
		return nil, true
	}

	// Call Builder.BuildFromLatestOutbound() - returns ([]byte, interface{}, error)
	outboundBody, _, err := sc.deps.Builder.BuildFromLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "v2 build from outbound failed, fallback to v1",
			"session", sessionID, "tenant", tenantID, "error", err)
		return nil, false
	}

	return outboundBody, true
}
```

### 修改 2: 在 Prepare 方法中集成 V2 路径

在 `Prepare()` 方法中，找到读取 `SessionCache` 的位置（大约在 170-200 行之间），添加 V2 判断：

**查找位置**: 搜索 `sc.deps.Cache` 或 `GetOrLoad`

**插入位置**: 在调用 `GetOrLoad` 之前

**插入代码**:
```go
// ── V2 Integration: Try V2 cache first if enabled ──────────────
var lastOutboundBody []byte

if sc.shouldUseV2(tenantID) {
	slog.InfoContext(ctx, "using v2 cache for session compression",
		"session", gwSessionID, "tenant", tenantID)
	
	v2Body, ok := sc.tryLoadV2State(ctx, tenantID, gwSessionID)
	if ok {
		lastOutboundBody = v2Body
		slog.InfoContext(ctx, "v2 cache loaded successfully",
			"session", gwSessionID, "body_size", len(v2Body))
	} else {
		slog.WarnContext(ctx, "v2 cache failed, falling back to v1",
			"session", gwSessionID)
		// Continue to V1 path below
	}
}

// ── V1 Path (existing logic or fallback) ──────────────────────
if len(lastOutboundBody) == 0 && sc.deps.Cache != nil {
	// Existing V1 cache logic
	state, _ := sc.deps.Cache.GetOrLoad(ctx, tenantID, gwSessionID)
	if state != nil {
		// Extract lastOutboundBody from V1 state
		// ... existing logic ...
	}
}
```

### 修改 3: 添加 Feature Flag 配置

创建文件 `settings/spec_sessions_v2.go`:

```go
package settings

func init() {
	Register(Spec{
		Key:          "sessions_v2_compression_read",
		Type:         TypeBool,
		Scope:        ScopeTenant,
		DefaultValue: false,
		Description:  "Enable V2 session compression (read from session_turns + session_bodies)",
		Category:     "experimental",
		Version:      "v2.4.0",
	})
}
```

### 修改 4: 在 shouldUseV2 中启用 Feature Flag

修改 `shouldUseV2()` 方法中的 TODO：

```go
// Check Feature Flag
return settings.GetTenantBool(tenantID, "sessions_v2_compression_read", false)
```

---

## ⚠️ 注意事项

### 1. 类型转换

`SessionCompressorDeps` 中的 `CacheV2` 和 `Builder` 使用 `interface{}` 以避免循环依赖。

实际使用时需要类型断言：

```go
// 如果需要访问具体类型
if cacheV2, ok := sc.deps.CacheV2.(*v2.SessionCacheV2); ok {
	// 使用具体类型
}
```

### 2. Fallback 机制

- V2 任何步骤失败 → 自动 fallback 到 V1
- V1 失败 → 视为新会话（现有逻辑）
- 日志记录所有 fallback 事件

### 3. 向后兼容

- Feature Flag 默认 `false`（使用 V1）
- V2 组件为 `nil` 时自动使用 V1
- 不影响现有功能

---

## 🧪 测试策略

### 单元测试

创建 `session_compressor_v2_test.go`:

```go
func TestSessionCompressor_ShouldUseV2(t *testing.T) {
	tests := []struct {
		name     string
		deps     SessionCompressorDeps
		expected bool
	}{
		{
			name: "v2 components nil",
			deps: SessionCompressorDeps{
				Cache:   &SessionCache{},
				CacheV2: nil,
				Builder: nil,
			},
			expected: false,
		},
		{
			name: "v2 components available",
			deps: SessionCompressorDeps{
				CacheV2: &mockCacheV2{},
				Builder: &mockBuilder{},
			},
			expected: true, // 假设 Feature Flag 启用
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &SessionCompressor{deps: &tt.deps}
			result := sc.shouldUseV2("test-tenant")
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
```

### 集成测试

```go
func TestSessionCompressor_V2Integration(t *testing.T) {
	// 初始化 V2 组件
	// 创建测试会话
	// 调用 Prepare()
	// 验证使用了 V2 路径
	// 验证结果正确
}

func TestSessionCompressor_V2Fallback(t *testing.T) {
	// 模拟 V2 失败
	// 验证自动 fallback 到 V1
	// 验证结果仍然正确
}
```

---

## 📋 实施 Checklist

- [ ] 1. 在文件末尾添加 `shouldUseV2()` 方法
- [ ] 2. 在文件末尾添加 `tryLoadV2State()` 方法
- [ ] 3. 在 `Prepare()` 方法中集成 V2 判断
- [ ] 4. 创建 `settings/spec_sessions_v2.go`
- [ ] 5. 启用 `shouldUseV2()` 中的 Feature Flag
- [ ] 6. 创建单元测试
- [ ] 7. 运行测试验证
- [ ] 8. 创建集成测试

---

## 🚦 当前状态

**进度**: 10% (仅完成 Deps 结构修改)

**阻塞点**: 
- 需要仔细定位 Prepare() 方法中的插入点
- 需要理解现有 V1 缓存加载逻辑
- 需要测试 V1/V2 切换

**建议**:
由于文件复杂度高（648行），建议：
1. 先完成辅助方法添加（低风险）
2. 再集成到 Prepare 方法（高风险，需仔细）
3. 充分测试后再提交

---

**文档创建时间**: 2026-08-06  
**状态**: 实施指南已准备
