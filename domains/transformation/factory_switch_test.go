package transformation

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// captureSlog 捕获 slog.Default() 的所有输出，直到 restore() 被调用。
//
// spec §10.4.1 要求 Legacy 切换必须写 conversion_path + reason 日志。
// 这里用一个可重复读、线程安全的内存 handler 验证日志内容。
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   string
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, a := range attrs {
		b.WriteString(a.Key + "=" + a.Value.String() + " ")
	}
	h.attrs += b.String()
	return h
}

func (h *captureHandler) WithGroup(name string) slog.Handler { return h }

// snapshot 返回当前已捕获记录的副本（线程安全）。
func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// recordString 把一条 slog.Record 展开为 "key=value" 拼接串，便于断言。
func recordString(r slog.Record) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(r.Level.String()))
	b.WriteString(" ")
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(a.Value.String())
		return true
	})
	return b.String()
}

// containsAny 在捕获记录中查找同时包含所有 want 片段的记录。
func containsAny(records []slog.Record, want ...string) bool {
	for _, r := range records {
		s := recordString(r)
		ok := true
		for _, w := range want {
			if !strings.Contains(s, w) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// captureSlog 替换全局 logger 为捕获 handler 并返回 restore 函数。
func captureSlog() (*captureHandler, func()) {
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	return h, func() { slog.SetDefault(prev) }
}

// drainDiscard 把全局 logger 设为 discard，避免噪音。返回 restore。
func drainDiscard() func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return func() { slog.SetDefault(prev) }
}

// pickEnv 快速构造带 tenant + model 的测试 RequestEnvelope。
//
// 注意：本包已有 newEnvelope(client, upstream, body, model) 辅助函数
// （见 ir_transport_test.go），为避免命名冲突，这里另起一个名字。
func pickEnv(t *testing.T, tenant, model string) *domain.RequestEnvelope {
	t.Helper()
	return domain.NewEnvelopeBuilder("req-1").
		WithTenant(&domain.TenantContext{ID: tenant}).
		WithTransport(&domain.TransportContext{ClientModel: model}).
		Build()
}

// TestSwitch_EnabledTrue_PicksIR 覆盖场景 1：
// TRANSPORT_LAYER_IR_ENABLED=true → Pick 返回 IRTransport，
// ConversionPath() == "ir"。
func TestSwitch_EnabledTrue_PicksIR(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "100")

	f := NewTransportFactory()
	f.Reload()

	got := f.Pick(context.Background(), pickEnv(t, "tenant-1", "gpt-4o"))
	if got.Implementation() != "ir" {
		t.Fatalf("Pick() implementation = %s, want ir", got.Implementation())
	}
	if path := f.ConversionPath(); path != "ir" {
		t.Fatalf("ConversionPath() = %q, want \"ir\"", path)
	}
}

// TestSwitch_DisabledFalse_PicksLegacyWithLog 覆盖场景 2：
// TRANSPORT_LAYER_IR_ENABLED=false → Pick 返回 LegacyTransport，
// ConversionPath() == "legacy"，slog 记录 conversion_path=legacy + reason。
func TestSwitch_DisabledFalse_PicksLegacyWithLog(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "false")

	f := NewTransportFactory()
	f.Reload()

	h, restore := captureSlog()
	defer restore()

	got := f.Pick(context.Background(), pickEnv(t, "tenant-1", "gpt-4o"))
	if got.Implementation() != "legacy" {
		t.Fatalf("Pick() implementation = %s, want legacy", got.Implementation())
	}
	if path := f.ConversionPath(); path != "legacy" {
		t.Fatalf("ConversionPath() = %q, want \"legacy\"", path)
	}

	if !containsAny(h.snapshot(), "conversion_path=legacy", "reason=") {
		t.Fatalf("expected a log record containing conversion_path=legacy and reason=, got:\n%s",
			dumpRecords(h.snapshot()))
	}
}

// TestSwitch_EnabledButRolloutZero 覆盖场景 3：
// 开关=true 但 rollout_percent=0 → Pick 返回 Legacy（百分比未命中），
// conversion_path="legacy" + reason="rollout_percent_0"。
func TestSwitch_EnabledButRolloutZero(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "0")

	f := NewTransportFactory()
	f.Reload()

	h, restore := captureSlog()
	defer restore()

	got := f.Pick(context.Background(), pickEnv(t, "tenant-1", "gpt-4o"))
	if got.Implementation() != "legacy" {
		t.Fatalf("Pick() implementation = %s, want legacy (rollout_percent=0)", got.Implementation())
	}
	if path := f.ConversionPath(); path != "legacy" {
		t.Fatalf("ConversionPath() = %q, want \"legacy\"", path)
	}
	if !containsAny(h.snapshot(), "conversion_path=legacy", "reason=rollout_percent_0") {
		t.Fatalf("expected log record conversion_path=legacy reason=rollout_percent_0, got:\n%s",
			dumpRecords(h.snapshot()))
	}
}

// TestSwitch_TenantWhitelistMatch 覆盖场景 4：
// 开关=true + tenant whitelist 匹配 → Pick 返回 IR，
// conversion_path="ir"。
func TestSwitch_TenantWhitelistMatch(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_TENANT_WHITELIST", "tenant-special")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "0")

	f := NewTransportFactory()
	f.Reload()

	got := f.Pick(context.Background(), pickEnv(t, "tenant-special", "gpt-4o"))
	if got.Implementation() != "ir" {
		t.Fatalf("Pick() implementation = %s, want ir (whitelisted tenant)", got.Implementation())
	}
	if path := f.ConversionPath(); path != "ir" {
		t.Fatalf("ConversionPath() = %q, want \"ir\"", path)
	}
}

// TestSwitch_WhitelistMissRollout100 覆盖场景 5：
// 开关=true + tenant whitelist 不匹配 + rollout=100 → Pick 返回 IR（百分比兜底）。
func TestSwitch_WhitelistMissRollout100(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_TENANT_WHITELIST", "tenant-special")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "100")

	f := NewTransportFactory()
	f.Reload()

	got := f.Pick(context.Background(), pickEnv(t, "tenant-other", "gpt-4o"))
	if got.Implementation() != "ir" {
		t.Fatalf("Pick() implementation = %s, want ir (rollout=100 fallback)", got.Implementation())
	}
	if path := f.ConversionPath(); path != "ir" {
		t.Fatalf("ConversionPath() = %q, want \"ir\"", path)
	}
}

// TestSwitch_StatsIncrement 覆盖场景 6：
// GetConversionPathStats 在多次 Pick 后 irCount/legacyCount 正确递增。
func TestSwitch_StatsIncrement(t *testing.T) {
	// 先把 default logger 拿掉，避免上一个用例 setDefault 后噪音
	restore := drainDiscard()
	defer restore()

	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "100")

	f := NewTransportFactory()
	f.Reload()

	// IR 路径 ×3
	irEnv := pickEnv(t, "tenant-ir", "gpt-4o")
	for i := 0; i < 3; i++ {
		if got := f.Pick(context.Background(), irEnv).Implementation(); got != "ir" {
			t.Fatalf("pick %d: got %s, want ir", i, got)
		}
	}

	// 切到 Legacy：关闭开关
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "false")
	f.Reload()

	legacyEnv := pickEnv(t, "tenant-legacy", "gpt-4o")
	for i := 0; i < 2; i++ {
		if got := f.Pick(context.Background(), legacyEnv).Implementation(); got != "legacy" {
			t.Fatalf("pick %d: got %s, want legacy", i, got)
		}
	}

	irCount, legacyCount := f.GetConversionPathStats()
	if irCount != 3 {
		t.Errorf("irCount = %d, want 3", irCount)
	}
	if legacyCount != 2 {
		t.Errorf("legacyCount = %d, want 2", legacyCount)
	}
}

// dumpRecords 把捕获的记录格式化成多行字符串，用于失败诊断。
func dumpRecords(records []slog.Record) string {
	var b strings.Builder
	for _, r := range records {
		b.WriteString(recordString(r))
		b.WriteString("\n")
	}
	return b.String()
}
