package observability

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// recordingSpan 是只记录属性的最小 trace.Span 实现。
//
// 为什么不用 sdk/trace/tracetest 的 InMemoryExporter：那个包没有进 vendor
// （vendor/go.opentelemetry.io/otel/sdk/trace/tracetest/ 不存在），
// 为一道门引入一个新依赖会让 vendor 树与 go.mod 失去同步。
// SetTenantAttrs 只调用 span.SetAttributes，其余接口方法用不到，
// 因此内嵌 nil 接口 + 覆写 SetAttributes 就够了。
type recordingSpan struct {
	trace.Span
	attrs []attribute.KeyValue
}

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.attrs = append(s.attrs, kv...)
}

func (s *recordingSpan) get(key attribute.Key) (attribute.Value, bool) {
	for _, kv := range s.attrs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// TestSetTenantAttrsSetsRequiredTenantID 是 OTel 租户属性的**门**。
//
// 为什么需要这道门：CONTRIBUTING.md 以前写着「OTel tenant.id 属性没有独立门禁」
// （因为 make lint-otel-tenant 这个 Makefile 目标从来不存在）。
// 本门把「没有门」变成「有门」：它钉住 internal/observability/tenant.go 的
// 语义约定 ——
//
//	L1（必须）：tenant.id          —— 空值时**不设置**（租户标识丢失）
//	L1（必须）：kaixuan.tenant.pattern = "A"（llm-gateway-go 是 Pattern A）
//	L2（推荐）：kaixuan.tenant.auth_method / kaixuan.user.id —— 空值时跳过
//
// 它不是「全链路 span 都带 tenant.id」的证明（那需要把每个 span 生产点都
// 接进门），但它至少保证**属性名与语义**不会被悄悄改掉 ——
// 属性名一改，线上按 tenant.id 过滤的 Jaeger 查询就会静默失效，
// 而这正是本门要防的那类回归。
func TestSetTenantAttrsSetsRequiredTenantID(t *testing.T) {
	span := &recordingSpan{}
	SetTenantAttrs(span, "tenant-42", "api_key", "user-7")

	v, ok := span.get(AttrTenantID)
	if !ok {
		t.Fatalf("span 缺 %s 属性（L1 必须项）", AttrTenantID)
	}
	if v.AsString() != "tenant-42" {
		t.Errorf("%s = %q, want %q", AttrTenantID, v.AsString(), "tenant-42")
	}

	v, ok = span.get(AttrKaixuanTenantPattern)
	if !ok {
		t.Fatalf("span 缺 %s 属性（L1 必须项）", AttrKaixuanTenantPattern)
	}
	if v.AsString() != "A" {
		t.Errorf("%s = %q, want %q（llm-gateway-go 是 Pattern A：直连 tenant_id 列）",
			AttrKaixuanTenantPattern, v.AsString(), "A")
	}

	v, ok = span.get(AttrKaixuanAuthMethod)
	if !ok {
		t.Fatalf("span 缺 %s 属性（L2 推荐项，非空时应写入）", AttrKaixuanAuthMethod)
	}
	if v.AsString() != "api_key" {
		t.Errorf("%s = %q, want %q", AttrKaixuanAuthMethod, v.AsString(), "api_key")
	}

	v, ok = span.get(AttrKaixuanUserID)
	if !ok {
		t.Fatalf("span 缺 %s 属性（L2 推荐项，非空时应写入）", AttrKaixuanUserID)
	}
	if v.AsString() != "user-7" {
		t.Errorf("%s = %q, want %q", AttrKaixuanUserID, v.AsString(), "user-7")
	}
}

// TestSetTenantAttrsSkipsEmptyOptionalAttrs 钉住「空值只跳过 L2、不跳过 L1」
// 的语义：tenant_id 为空时**不写** tenant.id（宁可不写，也不写一个空租户标识
// —— 空标识会让 Jaeger 按 tenant.id="" 过滤时把不同租户的请求混成一桶）。
func TestSetTenantAttrsSkipsEmptyOptionalAttrs(t *testing.T) {
	span := &recordingSpan{}
	SetTenantAttrs(span, "", "", "")

	if _, ok := span.get(AttrTenantID); ok {
		t.Errorf("tenant_id 为空时不应写 %s 属性（L1 空值必须跳过）", AttrTenantID)
	}
	if _, ok := span.get(AttrKaixuanAuthMethod); ok {
		t.Errorf("authMethod 为空时不应写 %s 属性（L2 空值跳过）", AttrKaixuanAuthMethod)
	}
	if _, ok := span.get(AttrKaixuanUserID); ok {
		t.Errorf("userID 为空时不应写 %s 属性（L2 空值跳过）", AttrKaixuanUserID)
	}
	// pattern 恒写：它标识「本服务用哪种租户模型」，与租户标识本身无关。
	if _, ok := span.get(AttrKaixuanTenantPattern); !ok {
		t.Errorf("span 缺 %s 属性（恒写项）", AttrKaixuanTenantPattern)
	}
}

// TestSetTenantAttrsPatternBShape 钉住 Pattern B 的跨服务规格一致性：
// llm-gateway-go 不用它，但它属于同一份「跨栈单一真源」规格
// （R34 约定），改坏了会让 crm-go 的对拍规格失真。
func TestSetTenantAttrsPatternBShape(t *testing.T) {
	span := &recordingSpan{}
	SetTenantAttrsPatternB(span, []string{"s1", "s2"}, "jwt", "user-9")

	v, ok := span.get(AttrKaixuanTenantPattern)
	if !ok {
		t.Fatalf("span 缺 %s 属性", AttrKaixuanTenantPattern)
	}
	if v.AsString() != "B" {
		t.Errorf("%s = %q, want %q", AttrKaixuanTenantPattern, v.AsString(), "B")
	}

	v, ok = span.get(AttrTenantShopIDs)
	if !ok {
		t.Fatalf("span 缺 %s 属性", AttrTenantShopIDs)
	}
	if v.AsString() != "s1,s2" {
		t.Errorf("%s = %q, want %q", AttrTenantShopIDs, v.AsString(), "s1,s2")
	}
}
