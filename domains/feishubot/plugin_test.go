package feishubot

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestLoadConfig_UsesSettingsForAllowedUsers(t *testing.T) {
	resetSettings()
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.AllowedUsers) != 0 {
		t.Fatalf("AllowedUsers = %v, want empty default", cfg.AllowedUsers)
	}
}

func resetSettings() {
	settings.Global = settings.NewRegistry()
}

func TestParseUserList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "ou_abc", []string{"ou_abc"}},
		{"multi", "ou_a, ou_b ,ou_c", []string{"ou_a", "ou_b", "ou_c"}},
		{"trailing-comma", "ou_a,ou_b,", []string{"ou_a", "ou_b"}},
		{"whitespace-only", " , , ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseUserList(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("len mismatch: got %d want %d (%v)", len(got), len(c.want), got)
			}
			for i, v := range c.want {
				if got[i] != v {
					t.Errorf("[%d] got %q want %q", i, got[i], v)
				}
			}
		})
	}
}

func TestConfigIsUserAllowed(t *testing.T) {
	t.Run("admin_only=true, no whitelist → all denied", func(t *testing.T) {
		c := Config{AllowedUsers: nil, CommandsAdminOnly: true}
		if c.IsUserAllowed("ou_x") {
			t.Error("expected denial when whitelist empty and admin_only=true")
		}
	})
	t.Run("admin_only=true, whitelist has user → allow", func(t *testing.T) {
		c := Config{AllowedUsers: []string{"ou_a", "ou_b"}, CommandsAdminOnly: true}
		if !c.IsUserAllowed("ou_a") {
			t.Error("expected allow for ou_a")
		}
		if c.IsUserAllowed("ou_c") {
			t.Error("expected denial for ou_c")
		}
	})
	t.Run("admin_only=false, no whitelist → all allow", func(t *testing.T) {
		c := Config{AllowedUsers: nil, CommandsAdminOnly: false}
		if !c.IsUserAllowed("anyone") {
			t.Error("expected allow when whitelist empty and admin_only=false")
		}
	})
}

// ── 2026-07-09: dual-source merge 测试（DB + settings_kv）────────

func TestMergeAllowedUsers(t *testing.T) {
	tests := []struct {
		name     string
		fromDB   []string
		fromSet  []string
		expected []string
	}{
		{
			name:     "empty_db_empty_settings",
			fromDB:   nil,
			fromSet:  nil,
			expected: nil,
		},
		{
			name:     "only_db",
			fromDB:   []string{"ou_1", "ou_2"},
			fromSet:  nil,
			expected: []string{"ou_1", "ou_2"},
		},
		{
			name:     "only_settings",
			fromDB:   nil,
			fromSet:  []string{"ou_1"},
			expected: []string{"ou_1"},
		},
		{
			name:     "db_priority_wins",
			fromDB:   []string{"ou_2", "ou_1"}, // priority ASC
			fromSet:  []string{"ou_3"},
			expected: []string{"ou_2", "ou_1", "ou_3"},
		},
		{
			name:     "dedup_across_sources",
			fromDB:   []string{"ou_1", "ou_2"},
			fromSet:  []string{"ou_2", "ou_3"}, // ou_2 duplicate
			expected: []string{"ou_1", "ou_2", "ou_3"},
		},
		{
			name:     "empty_strings_filtered",
			fromDB:   []string{"", "ou_1", ""},
			fromSet:  nil,
			expected: []string{"ou_1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeAllowedUsers(tt.fromDB, tt.fromSet)
			if !sliceEq(got, tt.expected) {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoadAllowedUsersFromDB_NilPool(t *testing.T) {
	_, err := loadAllowedUsersFromDB(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil pool, got nil")
	}
}

func TestPluginSetDBPool(t *testing.T) {
	p := NewPlugin(nil)
	p.SetDBPool(nil) // 应该不 panic
	if p.db != nil {
		t.Error("expected nil db after SetDBPool(nil)")
	}
}

func TestReloadConfig_UsesDBAllowedUsers(t *testing.T) {
	resetSettings()
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}

	p := NewPlugin(nil)

	// nil db 时，ReloadConfig 应该退回 settings_kv（默认空）
	if err := p.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if len(p.Snapshot().AllowedUsers) != 0 {
		t.Fatalf("AllowedUsers = %v, want empty without db", p.Snapshot().AllowedUsers)
	}
}

func TestConfigIsSeverityPassing(t *testing.T) {
	c := Config{AlertSeverityMin: "high"}
	if !c.IsSeverityPassing("high") {
		t.Error("high should pass when min=high")
	}
	if !c.IsSeverityPassing("critical") {
		t.Error("critical should pass when min=high")
	}
	if c.IsSeverityPassing("medium") {
		t.Error("medium should NOT pass when min=high")
	}
	if c.IsSeverityPassing("low") {
		t.Error("low should NOT pass when min=high")
	}
}

func TestConfigIsInQuietHours(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		c := Config{QuietHoursEnabled: false}
		if c.IsInQuietHours(time.Now()) {
			t.Error("disabled should never be in quiet hours")
		}
	})
	t.Run("same-day window", func(t *testing.T) {
		c := Config{QuietHoursEnabled: true, QuietHoursStart: "12:00", QuietHoursEnd: "14:00"}
		inWindow := time.Date(2026, 7, 9, 13, 0, 0, 0, time.Local)
		outWindow := time.Date(2026, 7, 9, 15, 0, 0, 0, time.Local)
		if !c.IsInQuietHours(inWindow) {
			t.Error("13:00 should be in [12:00, 14:00)")
		}
		if c.IsInQuietHours(outWindow) {
			t.Error("15:00 should NOT be in [12:00, 14:00)")
		}
	})
	t.Run("cross-night window 22:00 → 08:00", func(t *testing.T) {
		c := Config{QuietHoursEnabled: true, QuietHoursStart: "22:00", QuietHoursEnd: "08:00"}
		lateNight := time.Date(2026, 7, 9, 23, 30, 0, 0, time.Local)
		earlyMorning := time.Date(2026, 7, 9, 6, 0, 0, 0, time.Local)
		daytime := time.Date(2026, 7, 9, 12, 0, 0, 0, time.Local)
		if !c.IsInQuietHours(lateNight) {
			t.Error("23:30 should be in quiet (cross-night)")
		}
		if !c.IsInQuietHours(earlyMorning) {
			t.Error("06:00 should be in quiet (cross-night)")
		}
		if c.IsInQuietHours(daytime) {
			t.Error("12:00 should NOT be in quiet")
		}
	})
	t.Run("invalid HH:MM", func(t *testing.T) {
		c := Config{QuietHoursEnabled: true, QuietHoursStart: "bad", QuietHoursEnd: "08:00"}
		if c.IsInQuietHours(time.Now()) {
			t.Error("invalid HH:MM should fail-secure (no quiet)")
		}
	})
}

func TestVerifyLarkSignature(t *testing.T) {
	const (
		ts    = "1700000000"
		nonce = "abc123"
		body  = `{"action":"status"}`
		key   = "test_encrypt_key"
	)
	// 正确签名
	good := computeLarkSig(ts, nonce, body, key)
	if !VerifyLarkSignature(ts, nonce, body, good, key) {
		t.Error("expected signature to verify with correct inputs")
	}
	// 错 timestamp
	if VerifyLarkSignature("wrong", nonce, body, good, key) {
		t.Error("expected signature to fail with wrong timestamp")
	}
	// 空 signature 拒绝（fail-secure）
	if VerifyLarkSignature(ts, nonce, body, "", key) {
		t.Error("empty signature should be rejected")
	}
	// 空 key 拒绝（fail-secure）
	if VerifyLarkSignature(ts, nonce, body, good, "") {
		t.Error("empty encrypt_key should be rejected")
	}
}

func TestVerifyLarkTimestamp(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name     string
		ts       string
		window   int
		expected bool
	}{
		{"now", strconvItoa(now), 300, true},
		{"4min_ago", strconvItoa(now - 240), 300, true},
		{"just_outside_window", strconvItoa(now - 360), 300, false},
		{"future", strconvItoa(now + 60), 300, true},
		{"invalid_string", "not-a-number", 300, false},
		{"empty", "", 300, false},
		{"zero_window_default", strconvItoa(now - 100), 0, true}, // uses default 300
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerifyLarkTimestamp(c.ts, c.window); got != c.expected {
				t.Errorf("got %v want %v", got, c.expected)
			}
		})
	}
}

func TestDeduper(t *testing.T) {
	d := NewDeduper(100 * time.Millisecond)
	fp := "abc"

	if dup, _ := d.Check(fp); dup {
		t.Error("first call should not be duplicate")
	}
	if dup, _ := d.Check(fp); !dup {
		t.Error("second call within window should be duplicate")
	}

	// 等窗口过期
	time.Sleep(150 * time.Millisecond)
	if dup, _ := d.Check(fp); dup {
		t.Error("call after window expiry should not be duplicate")
	}
}

func TestDeduperZeroWindowPassThrough(t *testing.T) {
	d := NewDeduper(0)
	for i := 0; i < 5; i++ {
		if dup, _ := d.Check("x"); dup {
			t.Errorf("call %d should not be duplicate with zero window", i)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	r := NewRateLimiter(3)
	for i := 0; i < 3; i++ {
		if r.Allow() {
			t.Errorf("call %d should be allowed", i)
		}
	}
	// 第 4 次应被限流
	if !r.Allow() {
		t.Error("4th call should be throttled")
	}
	// 重置后恢复
	r.Reset()
	if r.Allow() {
		t.Error("after reset, first call should be allowed")
	}
}

func TestRateLimiterZeroMaxPassThrough(t *testing.T) {
	r := NewRateLimiter(0)
	for i := 0; i < 100; i++ {
		if r.Allow() {
			t.Errorf("call %d should not be throttled with max=0", i)
		}
	}
}

// 2026-07-09: dedup + rate limit 边界用例。
func TestDeduper_ParallelSafety(t *testing.T) {
	// 并发 Check 不应 panic 或 data race。
	d := NewDeduper(50 * time.Millisecond)
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fp := "fp-" + strconv.Itoa(i%5) // 5 个不同的 fingerprint 重复 20 次
			for j := 0; j < 20; j++ {
				_, _ = d.Check(fp)
			}
		}(i)
	}
	wg.Wait()
}

func TestDeduper_DifferentKeysIndependent(t *testing.T) {
	// 不同 fingerprint 应独立去重。
	d := NewDeduper(1 * time.Second)
	if dup, _ := d.Check("a"); dup {
		t.Error("first 'a' should not be dup")
	}
	if dup, _ := d.Check("b"); dup {
		t.Error("first 'b' should not be dup")
	}
	if dup, _ := d.Check("a"); !dup {
		t.Error("second 'a' should be dup")
	}
	if dup, _ := d.Check("b"); !dup {
		t.Error("second 'b' should be dup")
	}
}

func TestRateLimiter_PeriodReset(t *testing.T) {
	// 验证 Reset() 后窗口恢复：限额=2，先打满 3 次（1 次被限流），Reset 后第 1 次应放行。
	r := NewRateLimiter(2)
	if r.Allow() { // 1st
		t.Error("1st should be allowed")
	}
	if r.Allow() { // 2nd
		t.Error("2nd should be allowed")
	}
	if !r.Allow() { // 3rd — throttled
		t.Error("3rd should be throttled")
	}
	r.Reset()
	if r.Allow() { // after reset, 1st should be allowed again
		t.Error("after Reset, 1st should be allowed again")
	}
}

func TestFingerprintUniqueness(t *testing.T) {
	// 相似但不同的输入应产生不同 fingerprint。
	a := Fingerprint("alert", "session-A", "high", "key1")
	b := Fingerprint("alert", "session-B", "high", "key1")     // session 不同
	c := Fingerprint("alert", "session-A", "critical", "key1") // severity 不同
	if a == b {
		t.Error("different session should produce different fingerprint")
	}
	if a == c {
		t.Error("different severity should produce different fingerprint")
	}
}

// strconvItoa 由 helpers_test.go 提供（避免与 time 包的 strconv 重复）

func TestFingerprintStability(t *testing.T) {
	a := Fingerprint("prompt_injection", "x", "high", "k")
	b := Fingerprint("prompt_injection", "x", "high", "k")
	if a != b {
		t.Error("same input should produce same fingerprint")
	}
	c := Fingerprint("prompt_injection", "x", "medium", "k")
	if a == c {
		t.Error("different severity should produce different fingerprint")
	}
}

// ── Alert router 集成测试 ──────────────────────────────────────────

type fakeChannel struct {
	mu    sync.Mutex
	cards []*Card
	texts []*Message
}

func (f *fakeChannel) Name() string { return "fake" }
func (f *fakeChannel) Send(_ context.Context, m *Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, m)
	return nil
}
func (f *fakeChannel) SendCard(_ context.Context, c *Card) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards = append(f.cards, c)
	return nil
}

func TestAlertRouterFilters(t *testing.T) {
	ch := &fakeChannel{}
	r := NewAlertRouter(ch)
	r.Configure(Config{
		Enabled:          true,
		NotifyOnAlert:    true,
		AlertSeverityMin: "high",
		AlertDedupWindow: 60 * time.Second,
		AllowedUsers:     []string{"ou_admin"},
	})

	t.Run("below severity blocked", func(t *testing.T) {
		_, reason := r.PushAlert(context.Background(), Alert{
			ID: "1", Category: "test", Severity: SeverityLow, Title: "low",
		})
		if reason != "severity_below_threshold" {
			t.Errorf("expected severity_below_threshold, got %q", reason)
		}
	})
	t.Run("above severity passes dedup once", func(t *testing.T) {
		sent, reason := r.PushAlert(context.Background(), Alert{
			ID: "2", Category: "test", Severity: SeverityHigh, Title: "high-1",
		})
		if !sent || reason != "" {
			t.Errorf("expected sent, got sent=%v reason=%q", sent, reason)
		}
	})
	t.Run("same fingerprint within window → dedup", func(t *testing.T) {
		_, reason := r.PushAlert(context.Background(), Alert{
			ID: "3", Category: "test", Severity: SeverityHigh, Title: "high-1",
		})
		if reason != "dedup" {
			t.Errorf("expected dedup, got %q", reason)
		}
	})
	t.Run("module disabled blocks", func(t *testing.T) {
		r.Configure(Config{Enabled: false})
		_, reason := r.PushAlert(context.Background(), Alert{
			ID: "4", Category: "test", Severity: SeverityHigh, Title: "x",
		})
		if reason != "module_disabled" {
			t.Errorf("expected module_disabled, got %q", reason)
		}
	})
	t.Run("notify_on_alert=false blocks", func(t *testing.T) {
		r.Configure(Config{Enabled: true, NotifyOnAlert: false, AlertSeverityMin: "low"})
		_, reason := r.PushAlert(context.Background(), Alert{
			ID: "5", Category: "test", Severity: SeverityCritical, Title: "x",
		})
		if reason != "notify_on_alert_off" {
			t.Errorf("expected notify_on_alert_off, got %q", reason)
		}
	})
}

func TestAlertRouterRateLimit(t *testing.T) {
	ch := &fakeChannel{}
	r := NewAlertRouter(ch)
	r.Configure(Config{
		Enabled:              true,
		NotifyOnAlert:        true,
		AlertSeverityMin:     "low",
		AlertRateLimitPerMin: 2,
		AlertDedupWindow:     0, // 关闭去重
	})
	// 不同 fingerprint 才能触发限流而不是去重
	for i := 0; i < 3; i++ {
		_, reason := r.PushAlert(context.Background(), Alert{
			ID: "rl", Category: "test", Severity: SeverityHigh,
			Title: "title-" + intToString(int64(i)),
		})
		_ = reason
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if len(ch.cards) < 3 {
		// 期望：2 张原 alert + 1 张节流摘要
		t.Errorf("expected ≥3 cards (2 alerts + 1 throttled summary), got %d", len(ch.cards))
	}
}

func TestPluginEnabledEffective(t *testing.T) {
	p := NewPlugin(nil)
	p.cfg = Config{Enabled: true}
	t.Run("all deps enabled", func(t *testing.T) {
		ok := p.EnabledEffective(map[string]bool{
			"compression": true, "cache": true,
			"prompt_injection": true, "session_audit": true,
		})
		if !ok {
			t.Error("expected enabled when all deps enabled")
		}
	})
	t.Run("missing dep", func(t *testing.T) {
		ok := p.EnabledEffective(map[string]bool{
			"compression": true, "cache": true,
			"prompt_injection": false, "session_audit": true,
		})
		if ok {
			t.Error("expected disabled when prompt_injection missing")
		}
	})
	t.Run("module itself disabled", func(t *testing.T) {
		p.cfg = Config{Enabled: false}
		ok := p.EnabledEffective(map[string]bool{
			"compression": true, "cache": true,
			"prompt_injection": true, "session_audit": true,
		})
		if ok {
			t.Error("expected disabled when module itself disabled")
		}
	})
}

func TestCommandRouter(t *testing.T) {
	called := 0
	r := NewCommandRouter(&CfgWhitelist{Cfg: &Config{
		AllowedUsers:      []string{"ou_admin"},
		CommandsAdminOnly: true,
	}})
	r.Register("status", func(_ context.Context, _ Command) (*Card, error) {
		called++
		return &Card{Header: CardHeader{Title: "OK"}}, nil
	})

	t.Run("whitelisted user passes", func(t *testing.T) {
		card, allowed, err := r.Handle(context.Background(), Command{Action: "status", UserID: "ou_admin"}, true)
		if err != nil || !allowed || card == nil {
			t.Errorf("expected pass, got allowed=%v err=%v", allowed, err)
		}
		if called != 1 {
			t.Errorf("expected handler called once, got %d", called)
		}
	})
	t.Run("non-whitelisted user blocked", func(t *testing.T) {
		card, allowed, _ := r.Handle(context.Background(), Command{Action: "status", UserID: "ou_stranger"}, true)
		if allowed || card != nil {
			t.Errorf("expected block, got allowed=%v card=%v", allowed, card)
		}
	})
	t.Run("admin_only=false allows anyone", func(t *testing.T) {
		card, allowed, _ := r.Handle(context.Background(), Command{Action: "status", UserID: "ou_stranger"}, false)
		if !allowed || card == nil {
			t.Errorf("expected pass, got allowed=%v", allowed)
		}
	})
	t.Run("unknown action returns error card", func(t *testing.T) {
		card, allowed, _ := r.Handle(context.Background(), Command{Action: "unknown", UserID: "ou_admin"}, true)
		if !allowed || card == nil || card.Header.Title != "未知命令: /unknown" {
			t.Errorf("expected error card, got allowed=%v title=%q", allowed, card.Header.Title)
		}
	})
}

func TestParseCommandAction(t *testing.T) {
	cases := []struct {
		name  string
		value map[string]any
		want  string
	}{
		{"action key", map[string]any{"action": "status"}, "status"},
		{"command key", map[string]any{"command": "help"}, "help"},
		{"slash prefix", map[string]any{"action": "/stats"}, "stats"},
		{"empty", map[string]any{}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseCommandAction(c.value); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestLoadConfigWithRegistry(t *testing.T) {
	resetSettings()
	for _, sp := range settings.ModuleSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}
	// 注入 DB-style 值模拟（这里直接通过 RegisterSpec 的 default 即可）
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	// 检查关键字段有合理默认值
	if cfg.AlertSeverityMin != "high" {
		t.Errorf("AlertSeverityMin default should be 'high', got %q", cfg.AlertSeverityMin)
	}
	if cfg.AlertRateLimitPerMin != 20 {
		t.Errorf("AlertRateLimitPerMin default should be 20, got %d", cfg.AlertRateLimitPerMin)
	}
	if cfg.TimestampWindowSeconds != 300 {
		t.Errorf("TimestampWindowSeconds default should be 300, got %d", cfg.TimestampWindowSeconds)
	}
	if cfg.CardTemplate != "standard" {
		t.Errorf("CardTemplate default should be 'standard', got %q", cfg.CardTemplate)
	}
	if !cfg.SignatureRequired {
		t.Error("SignatureRequired default should be true")
	}
	if !cfg.CommandsAdminOnly {
		t.Error("CommandsAdminOnly default should be true (fail-secure)")
	}
	if !cfg.ApprovalAutoMentionCritical {
		t.Error("ApprovalAutoMentionCritical default should be true")
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// computeLarkSig 计算预期签名供 VerifyLarkSignature 校验。
func computeLarkSig(ts, nonce, body, key string) string {
	return signWithKey(key, ts, nonce, body)
}

func computeHMACBase64(key, data string) string {
	// 兼容性 stub：测试中实际签名走 computeLarkSig → signWithKey。
	// 保留此函数仅供向后兼容。
	return data
}

// strconvItoa 避免直接 import strconv 在 helpers_test 里再写一遍
func strconvItoa(n int64) string {
	return intToString(n)
}

// intToString — simple itoa，避免 import strconv 在 helpers_test 重复
func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// json round-trip 防止 import 警告（如果有未使用的 import）
var _ = json.Marshal
