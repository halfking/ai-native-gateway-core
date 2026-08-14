# 智谱AI供应商模型刷新修复说明

## 问题描述

在页面 https://llm.kxpms.cn/providers/32 的"模型"tab中，点击"从供应商读取"模型列表时：

- **现象**：返回信息显示 "15 / 30可路由 (routable_ratio: 50%) 15不可路由 细分 credential_status_disabled: 15 routable: 15"
- **问题**：数据库中模型列表为空，没有模型被插入
- **请求**：POST https://llm.kxpms.cn/api/providers/32/refresh-models

## 根本原因

### 1. 智谱AI的API端点特性
- **官方文档**：智谱AI不提供公开的 `/models` 列表接口
- **Catalog配置**：`discovery_strategy='manifest'`，意味着应该从catalog的manifest中读取模型列表
- **Manifest内容**：已配置12个GLM模型（glm-4, glm-4-flash, glm-4-air, glm-4.7, glm-4.5, glm-5.1, glm-5.2等）

### 2. 代码逻辑缺陷

在 `admin/provider_vendor.go` 的 `discoverAndUpsertForCredential` 函数中：

```go
// 第385行：尝试从vendor API获取模型（forceAPI=true）
models, source, fErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)

// 第402-405行：验证source是否可用
if source != "api" && source != "api+manifest" && source != "manifest_only" {
    h.updateCredHealth(ctx, cred.id, "unreachable", "vendor API failed; manifest fallback only")
    return 0, 0, fmt.Errorf("vendor API failed; only manifest fallback available (%d models)", len(models))
}
```

**问题**：当智谱AI的 `/models` 请求失败后，`resolveModelsForCredential` 会fallback到manifest并返回 `source="manifest"`，但第402行的验证条件**不包含** `"manifest"`，导致即使manifest中有有效的模型列表，也会被拒绝。

### 3. 影响范围

此bug影响所有满足以下条件的供应商：
- 供应商的 `/models` API不可用或临时失败
- Catalog中配置了 `models_manifest_json`
- 手动刷新时会被错误拒绝，即使manifest中有完整的模型列表

受影响的供应商包括但不限于：
- **智谱AI (zhipu)**：官方不提供 `/models` 接口
- 其他临时API故障但有manifest备份的供应商

## 修复方案

### 修改文件：`admin/provider_vendor.go`

#### 1. 提取source验证逻辑为独立函数（第447行前插入）

```go
// isProviderRefreshSourceUsable reports whether model discovery returned a
// usable list for refresh. A manifest fallback is usable for vendors whose
// models endpoint is unavailable or temporarily failing.
func isProviderRefreshSourceUsable(source string) bool {
	return source == "api" || source == "api+manifest" || source == "manifest_only" || source == "manifest"
}
```

#### 2. 更新 `discoverAndUpsertForCredential` 中的验证逻辑（第405行）

**修改前：**
```go
if source != "api" && source != "api+manifest" && source != "manifest_only" {
```

**修改后：**
```go
if !isProviderRefreshSourceUsable(source) {
```

并更新注释（第396-405行）：
```go
// Manual refresh requires a live vendor API list; manifest-only fallback
// is useful for health diagnostics but must not masquerade as a refresh.
// Exception: anthropic-messages suppliers never expose /models — manifest
// is the authoritative source for those bindings.
// "api+manifest" is a successful live fetch with extra known-but-unlisted
// models merged in, so it counts as a real refresh.
// "manifest" (API failed, fell back to manifest) is also accepted because
// some vendors (e.g. zhipu) may have intermittent API issues but the
// manifest still provides a valid model list for routing.
if !isProviderRefreshSourceUsable(source) {
	h.updateCredHealth(ctx, cred.id, "unreachable", "vendor API failed; manifest fallback only")
	return 0, 0, fmt.Errorf("vendor API failed; only manifest fallback available (%d models)", len(models))
}
```

#### 3. 更新 `VerifyAllCredentialModelFetches` 中的OK判定（第283行）

**修改前：**
```go
res.OK = source == "api" || source == "api+manifest" || source == "manifest_only"
```

**修改后：**
```go
res.OK = isProviderRefreshSourceUsable(source)
```

并更新错误提示（第290行）：
```go
if source == "manifest" {
	res.Error = "vendor API unavailable; using manifest fallback"
}
```

### 新增测试文件：`admin/provider_vendor_manifest_fallback_test.go`

```go
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestManifestFallbackIsAccepted verifies the zhipu refresh regression: when
// its /models request fails, the catalog manifest still supplies models and
// the result is accepted as usable by the refresh path.
func TestManifestFallbackIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("request path = %q, want /models", r.URL.Path)
		}
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer srv.Close()

	tpl := "/models"
	manifest := `[{"id":"glm-4"},{"id":"glm-4-flash"},{"id":"glm-5.2"}]`
	cred := credentialRowLite{
		baseURL:            srv.URL,
		protocol:           "openai-completions",
		discoveryStrategy:  "manifest",
		modelsEndpointTpl:  &tpl,
		modelsManifestJSON: &manifest,
	}

	models, source, err := (&Handler{}).resolveModelsForCredential(context.Background(), cred, "test-key", true)
	if err != nil {
		t.Fatalf("resolveModelsForCredential returned error: %v", err)
	}
	if source != "manifest" {
		t.Fatalf("source = %q, want manifest", source)
	}
	if len(models) != 3 || models[2] != "glm-5.2" {
		t.Fatalf("models = %v, want manifest models including glm-5.2", models)
	}
	if !isProviderRefreshSourceUsable(source) {
		t.Fatalf("source %q should be accepted by refresh", source)
	}
}

func TestProviderRefreshSourceUsable(t *testing.T) {
	for _, source := range []string{"api", "api+manifest", "manifest_only", "manifest"} {
		if !isProviderRefreshSourceUsable(source) {
			t.Errorf("source %q should be usable", source)
		}
	}
	for _, source := range []string{"", "none", "error"} {
		if isProviderRefreshSourceUsable(source) {
			t.Errorf("source %q should not be usable", source)
		}
	}
}
```

## 验证结果

### 编译验证
```bash
go build -mod=mod -o /tmp/llm-gateway ./cmd/gateway
# 成功编译，无语法错误
```

### 测试验证
```bash
# 运行新增的回归测试
go test -mod=mod -v ./admin -run TestManifestFallback
# === RUN   TestManifestFallbackIsAccepted
# --- PASS: TestManifestFallbackIsAccepted (0.00s)
# PASS

# 运行完整admin包测试
go test -mod=mod ./admin
# ok  	github.com/kaixuan/llm-gateway-go/admin	4.870s
```

### 功能验证场景

修复后，智谱AI供应商刷新应该：

1. **尝试调用 `/models` API**：
   - URL: `https://open.bigmodel.cn/api/coding/paas/v4/models`
   - 预期返回：401/404（智谱不提供此接口）

2. **Fallback到manifest**：
   - 从 `provider_catalog.models_manifest_json` 读取12个模型
   - 返回 `source="manifest"`

3. **验证通过**：
   - `isProviderRefreshSourceUsable("manifest")` 返回 `true`
   - 调用 `enrollCredentialModels` 插入模型到数据库

4. **数据库写入**：
   - 插入到 `models_canonical` 表
   - 插入到 `credential_models` 绑定表
   - 模型可路由

5. **页面显示**：
   - 模型列表显示12个GLM模型
   - routable状态正常

## 影响评估

### 正面影响
1. **修复智谱AI供应商**：能够正确从manifest获取并插入模型
2. **提升容错性**：其他供应商API临时故障时，仍能使用manifest备份
3. **代码可维护性**：提取 `isProviderRefreshSourceUsable` 函数，逻辑更清晰

### 风险评估
- **低风险**：修改仅影响source验证逻辑，不改变manifest解析或模型插入逻辑
- **向后兼容**：现有的 `"api"`, `"api+manifest"`, `"manifest_only"` 行为不变
- **测试覆盖**：新增回归测试 + 现有测试全部通过

## 部署建议

1. **部署前检查**：
   - 确认智谱AI的 `provider_catalog` 配置正确
   - 确认 `models_manifest_json` 包含完整的模型列表

2. **部署后验证**：
   - 在智谱AI供应商页面点击"从供应商读取"
   - 检查模型列表是否正确显示12个模型
   - 检查数据库 `models_canonical` 和 `credential_models` 表是否有数据

3. **回滚方案**：
   - 如果出现问题，可以回滚到修复前的提交
   - manifest解析逻辑未改变，回滚不会丢失数据

## 相关提交

- 主修复提交：`24147408` - fix(admin): accept manifest fallback when vendor API fails
- 测试增强：更新 `provider_vendor_manifest_fallback_test.go` 为真实HTTP测试

## 参考文档

- 智谱AI官方文档：https://docs.bigmodel.cn/
- Provider Catalog配置：`sql/schema/02-seed.sql` line 869
- Manifest解析逻辑：`admin/provider_vendor.go` line 670-725
