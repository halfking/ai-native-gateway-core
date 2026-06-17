package admin

import "testing"

func TestNormalizeSessionTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`"部署故障排查"`, "部署故障排查"},
		{"标题：修复 Casdoor 登录\n多余行", "修复 Casdoor 登录"},
		{"短标题", "短标题"},
	}
	for _, tc := range cases {
		got := normalizeSessionTitle(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeSessionTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := normalizeSessionTitle("这是一个非常非常非常非常长的会话标题需要截断")
	if len([]rune(long)) > sessionTitleMaxRunes+1 {
		t.Fatalf("expected truncation, got %q (%d runes)", long, len([]rune(long)))
	}
}

func TestIsValidSessionTitle(t *testing.T) {
	if isValidSessionTitle("a") {
		t.Fatal("single char title should be invalid")
	}
	if isValidSessionTitle("无法生成标题") {
		t.Fatal("template rejection should be invalid")
	}
	if !isValidSessionTitle("部署鉴权修复") {
		t.Fatal("expected valid title")
	}
}

func TestSessionTitleMapKey(t *testing.T) {
	k := sessionTitleMapKey("default", "sess-1")
	if k != "default\x00sess-1" {
		t.Fatalf("unexpected key: %q", k)
	}
	if sessionTitleMapKey("default", "") != "default\x00" {
		t.Fatalf("empty scoped session should still key")
	}
}
