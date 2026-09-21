package errorsx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 回归（round2 轴G）：按字节截断劈开 CJK 字符产生非法 UTF-8，PG UTF8 编码拒收 INSERT。
func TestSanitizeErrorTextUTF8SafeTruncation(t *testing.T) {
	// 100 个汉字 = 300 bytes；截到 299 边界附近必然劈字符。
	in := []byte(strings.Repeat("汉", 100) + " Bearer sk-abcdefghijklmnopqrst")
	out := SanitizeErrorText(in, 299)
	if !utf8.Valid(out) {
		t.Fatalf("sanitized output is not valid UTF-8: %q", out)
	}
	if strings.Contains(string(out), "sk-abcdefghijklmnopqrst") {
		t.Fatalf("key leaked: %q", out)
	}
	// ASCII 语义不变（用带空格的普通文本，避免触发 longBlobPattern）。
	ascii := strings.Repeat("word ", 40)
	out2 := SanitizeErrorText([]byte(ascii), 100)
	if len(out2) > 100 || !utf8.Valid(out2) {
		t.Fatalf("ascii truncation failed: len=%d valid=%v", len(out2), utf8.Valid(out2))
	}
	// 短输入原样。
	out3 := SanitizeErrorText([]byte("短"), 320)
	if string(out3) != "短" {
		t.Fatalf("short input mutated: %q", out3)
	}
}
