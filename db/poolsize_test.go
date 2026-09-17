package db

import (
	"testing"
)

// TestPoolMaxConnsFromEnv 覆盖 LLM_GATEWAY_DB_MAX_CONNS 的四种输入形态：
// 未设置（默认值）、合法正整数、非数字、非正数。坏值必须回落默认而非报错。
func TestPoolMaxConnsFromEnv(t *testing.T) {
	const def int32 = 32
	cases := []struct {
		name string
		env  string
		set  bool
		want int32
	}{
		{name: "unset returns default", env: "", set: false, want: def},
		{name: "valid positive overrides", env: "200", set: true, want: 200},
		{name: "spaces trimmed", env: " 64 ", set: true, want: 64},
		{name: "non-numeric falls back to default", env: "abc", set: true, want: def},
		{name: "zero falls back to default", env: "0", set: true, want: def},
		{name: "negative falls back to default", env: "-5", set: true, want: def},
		{name: "overflow falls back to default", env: "99999999999999999999", set: true, want: def},
		{name: "empty value treated as unset", env: "", set: true, want: def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("LLM_GATEWAY_DB_MAX_CONNS", tc.env)
			}
			if got := poolMaxConnsFromEnv(def); got != tc.want {
				t.Fatalf("poolMaxConnsFromEnv(def=%d) with env %q = %d, want %d",
					def, tc.env, got, tc.want)
			}
		})
	}
}
