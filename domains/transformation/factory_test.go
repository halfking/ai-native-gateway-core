package transformation

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestFactory_DisabledByDefault(t *testing.T) {
	f := NewTransportFactory()
	f.Reload()

	if f.Enabled() {
		t.Fatal("Enabled() = true, want false (env not set)")
	}

	env := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "tenant-1"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()

	if got := f.Pick(context.Background(), env).Implementation(); got != "legacy" {
		t.Fatalf("Pick() = %s, want legacy", got)
	}
}

func TestFactory_GlobalSwitch(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "100")

	f := NewTransportFactory()
	f.Reload()

	if !f.Enabled() {
		t.Fatal("Enabled() = false, want true")
	}

	env := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "t1"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()

	if got := f.Pick(context.Background(), env).Implementation(); got != "ir" {
		t.Fatalf("Pick() = %s, want ir (100%% rollout)", got)
	}
}

func TestFactory_TenantWhitelist(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_TENANT_WHITELIST", "tenant-a, tenant-b")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "0")

	f := NewTransportFactory()
	f.Reload()

	// 白名单内 → IR
	env1 := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "tenant-a"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()
	if got := f.Pick(context.Background(), env1).Implementation(); got != "ir" {
		t.Fatalf("whitelisted tenant: Pick() = %s, want ir", got)
	}

	// 白名单外 → Legacy
	env2 := domain.NewEnvelopeBuilder("r2").
		WithTenant(&domain.TenantContext{ID: "tenant-c"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()
	if got := f.Pick(context.Background(), env2).Implementation(); got != "legacy" {
		t.Fatalf("non-whitelisted tenant: Pick() = %s, want legacy", got)
	}
}

func TestFactory_ModelWhitelist(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_MODEL_WHITELIST", "claude-opus-4-8")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "0")

	f := NewTransportFactory()
	f.Reload()

	env1 := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "tenant-1"}).
		WithTransport(&domain.TransportContext{ClientModel: "claude-opus-4-8"}).
		Build()
	if got := f.Pick(context.Background(), env1).Implementation(); got != "ir" {
		t.Fatalf("whitelisted model: Pick() = %s, want ir", got)
	}

	env2 := domain.NewEnvelopeBuilder("r2").
		WithTenant(&domain.TenantContext{ID: "tenant-1"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()
	if got := f.Pick(context.Background(), env2).Implementation(); got != "legacy" {
		t.Fatalf("non-whitelisted model: Pick() = %s, want legacy", got)
	}
}

func TestFactory_PercentageRollout_Stable(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "50")

	f := NewTransportFactory()
	f.Reload()

	// 同一 (tenant, model) 应稳定分配到同一实现
	env := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "tenant-1"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()

	first := f.Pick(context.Background(), env).Implementation()
	for i := 0; i < 10; i++ {
		if got := f.Pick(context.Background(), env).Implementation(); got != first {
			t.Fatalf("iteration %d: Pick() = %s, want stable %s", i, got, first)
		}
	}
}

func TestFactory_PercentageRollout_Distribution(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "50")

	f := NewTransportFactory()
	f.Reload()

	ir, legacy := 0, 0
	for i := 0; i < 200; i++ {
		env := domain.NewEnvelopeBuilder("r").
			WithTenant(&domain.TenantContext{ID: string(rune('a'+i%26)) + string(rune('0'+i/26))}).
			WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
			Build()
		switch f.Pick(context.Background(), env).Implementation() {
		case "ir":
			ir++
		case "legacy":
			legacy++
		}
	}

	// 50% 灰度，200 个样本，IR 应在 70-130 之间（容许 ±30% 偏差）
	if ir < 70 || ir > 130 {
		t.Fatalf("distribution off: ir=%d, legacy=%d (expected ~100/100)", ir, legacy)
	}
}

func TestParseList(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a,b", 2},
		{" a , b , ", 2},
		{"a,a,a", 1}, // dedup
	}

	for _, tt := range tests {
		got := parseList(tt.in)
		if len(got) != tt.want {
			t.Errorf("parseList(%q) len = %d, want %d", tt.in, len(got), tt.want)
		}
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		in       string
		def      int
		min, max int
		want     int
	}{
		{"", 5, 0, 100, 5},
		{"abc", 5, 0, 100, 5},
		{"50", 0, 0, 100, 50},
		{"-1", 0, 0, 100, 0},    // clamped to min
		{"150", 0, 0, 100, 100}, // clamped to max
	}

	for _, tt := range tests {
		got := parseInt(tt.in, tt.def, tt.min, tt.max)
		if got != tt.want {
			t.Errorf("parseInt(%q, %d, %d, %d) = %d, want %d", tt.in, tt.def, tt.min, tt.max, got, tt.want)
		}
	}
}
