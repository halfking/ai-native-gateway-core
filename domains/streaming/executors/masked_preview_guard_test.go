package executors

import (
	"strings"
	"testing"
)

// 2026-11-10 审计 A6/B6 守护：错误消息/日志预览必须过 secretmask，
// 而压缩保真路径（truncateForLog 本体）保持原样不脱敏。
func TestTruncateForLogMaskedMasksCredentials(t *testing.T) {
	body := []byte(`{"error":"invalid key sk-FAKE0000000000000000000000000000000000000 for model x"}`)
	got := truncateForLogMasked(body, 200)
	if strings.Contains(got, "sk-FAKE0000000000000000000000000000000000000") {
		t.Fatalf("masked preview leaked credential shape: %s", got)
	}
	if !strings.Contains(got, secretmaskPlaceholderCheck()) {
		t.Fatalf("expected placeholder in %q", got)
	}
	// 纯截断路径不受影响
	plain := truncateForLog(body, 200)
	if !strings.Contains(plain, "sk-FAKE0000000000000000000000000000000000000") {
		t.Fatalf("plain truncateForLog must stay unmasked (compression fidelity path): %s", plain)
	}
	// 截断与脱敏顺序：先截 200B 再脱敏，长度上界不超脱敏后的展开
	long := []byte(`upstream body ` + strings.Repeat("x", 300) + ` sk-FAKE0000000000000000000000000000000000000`)
	out := truncateForLogMasked(long, 200)
	if len(out) > 200+16 { // 截断 200B 内的密钥替换为占位符可能略微展开
		t.Fatalf("masked preview exceeded bound: %d", len(out))
	}
}

func secretmaskPlaceholderCheck() string { return "[REDACTED" }
