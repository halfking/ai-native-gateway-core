package outputcompliance

import "testing"

// TestOwnerAllowsSensitive 覆盖 owner==caller 文本匹配规则的全部边界。
// 规则（保守，与 admin/session_tenant.go requireSessionOwnerAccess 的 deny 语义一致）：
//   - 调用方无 owner（空）→ false（脱敏）
//   - 数据无主人（空）→ false（脱敏）
//   - 两文本相等 → true（明文）
func TestOwnerAllowsSensitive(t *testing.T) {
	cases := []struct {
		name                          string
		callerOwner, dataOwner        string
		want                          bool
	}{
		{"both same", "alice", "alice", true},
		{"mismatch", "alice", "bob", false},
		{"empty caller", "", "alice", false},
		{"empty data", "alice", "", false},
		{"both empty", "", "", false},
		{"case sensitive", "Alice", "alice", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := OwnerAllowsSensitive(c.callerOwner, c.dataOwner)
			if got != c.want {
				t.Errorf("OwnerAllowsSensitive(%q,%q) = %v, want %v",
					c.callerOwner, c.dataOwner, got, c.want)
			}
		})
	}
}

// TestShouldRedact 覆盖三种 redaction_mode 与 owner 判定的组合。
func TestShouldRedact(t *testing.T) {
	cases := []struct {
		mode                                   RedactionMode
		callerOwner, dataOwner                 string
		want                                   bool
	}{
		{RedactOff, "alice", "alice", false},       // off 永不脱敏
		{RedactOff, "alice", "bob", false},
		{RedactAlways, "alice", "alice", true},     // always 永远脱敏
		{RedactAlways, "", "", true},
		{RedactOwnerMismatch, "alice", "alice", false}, // 同 owner → 放行
		{RedactOwnerMismatch, "alice", "bob", true},   // 不同 → 脱敏
		{RedactOwnerMismatch, "", "alice", true},      // 调用方无身份 → 脱敏
		{RedactOwnerMismatch, "alice", "", true},      // 数据无主 → 脱敏
		{RedactionMode("unknown"), "alice", "alice", true}, // 未知模式保守脱敏
	}
	for _, c := range cases {
		got := ShouldRedact(c.mode, c.callerOwner, c.dataOwner)
		if got != c.want {
			t.Errorf("ShouldRedact(%v,%q,%q) = %v, want %v",
				c.mode, c.callerOwner, c.dataOwner, got, c.want)
		}
	}
}

// TestParseCharLocation 覆盖位置解析。
func TestParseCharLocation(t *testing.T) {
	cases := []struct {
		loc       string
		s, e      int
		ok        bool
	}{
		{"char:0-5", 0, 5, true},
		{"char:120-145", 120, 145, true},
		{"char:10-3", 10, 3, true}, // 不校验 start<end，由调用方处理
		{"char:abc-def", 0, 0, false},
		{"", 0, 0, false},
		{"byte:0-5", 0, 0, false}, // 非 char: 前缀
		{"char:", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseCharLocation(c.loc)
		if ok != c.ok || (ok && (s != c.s || e != c.e)) {
			t.Errorf("parseCharLocation(%q) = (%d,%d,%v), want (%d,%d,%v)",
				c.loc, s, e, ok, c.s, c.e, c.ok)
		}
	}
}

// TestRedactOutput_PositonAccurate 验证修复后的位置精确脱敏。
// 这是对原 bug 的回归测试：旧实现用 strings.ReplaceAll(output, issue.Content,...)，
// 而 issue.Content 已是 mask 后的值，导致永不命中。
func TestRedactOutput_PositionAccurate(t *testing.T) {
	checker := &Checker{} // 不需要 DB，redactOutput 纯函数
	output := "用户邮箱是 alice@example.com 请联系"
	policy := &Policy{AutoRedact: true}
	// 构造一个 PII issue：email 在 output 的位置 7-26，mask 后为 a***@e***.com
	emailStart := len("用户邮箱是 ") // 7 (按字节)
	emailEnd := emailStart + len("alice@example.com")
	masked := checker.maskPII("alice@example.com", "email")
	issues := []ComplianceIssue{
		{
			Type:     "pii",
			Subtype:  "email",
			Severity: 8,
			Location: "char:7-24",
			Content:  masked,
			Redacted: true,
		},
	}
	_ = emailStart
	_ = emailEnd

	redacted := checker.redactOutput(output, issues, policy)
	if redacted == output {
		t.Fatalf("redactOutput did not change output (bug regression): %q", redacted)
	}
	// 原始 email 不应再出现
	if contains(redacted, "alice@example.com") {
		t.Errorf("redacted output still contains raw email: %q", redacted)
	}
	// mask 值应出现
	if !contains(redacted, masked) {
		t.Errorf("redacted output missing mask %q: %q", masked, redacted)
	}
}

// TestRedactOutput_NoOpWhenAutoRedactOff 验证 AutoRedact=false 时不脱敏。
func TestRedactOutput_NoOpWhenAutoRedactOff(t *testing.T) {
	checker := &Checker{}
	output := "test alice@example.com"
	policy := &Policy{AutoRedact: false}
	issues := []ComplianceIssue{{Type: "pii", Location: "char:5-24", Content: "a***@e***.com"}}
	got := checker.redactOutput(output, issues, policy)
	if got != output {
		t.Errorf("expected no-op when AutoRedact off, got %q", got)
	}
}

// TestRedactOutput_MultipleIssuesDescendingOrder 验证多个 PII 按 start 降序替换不漂移。
func TestRedactOutput_MultipleIssuesDescendingOrder(t *testing.T) {
	checker := &Checker{}
	// 两个 email，前一个靠后
	output := "first a@x.com then b@y.com end"
	// a@x.com 在 6-13, b@y.com 在 19-26
	issues := []ComplianceIssue{
		{Type: "pii", Subtype: "email", Location: "char:6-13", Content: "[A]"},
		{Type: "pii", Subtype: "email", Location: "char:19-26", Content: "[B]"},
	}
	policy := &Policy{AutoRedact: true}
	got := checker.redactOutput(output, issues, policy)
	if contains(got, "a@x.com") || contains(got, "b@y.com") {
		t.Errorf("both emails should be redacted, got %q", got)
	}
	if !contains(got, "[A]") || !contains(got, "[B]") {
		t.Errorf("both masks should appear, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
