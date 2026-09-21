package loopback

import "testing"

func TestGatewayBaseFollowsInstanceListen(t *testing.T) {
	cases := []struct {
		name   string
		listen string
		want   string
	}{
		{"unset falls back to historic default", "", "http://127.0.0.1:8781"},
		{"bare port", ":8782", "http://127.0.0.1:8782"},
		{"wildcard host", "0.0.0.0:8781", "http://127.0.0.1:8781"},
		{"loopback host", "127.0.0.1:8782", "http://127.0.0.1:8782"},
		{"ipv6 wildcard", "[::]:8782", "http://127.0.0.1:8782"},
		{"garbage falls back", "garbage", "http://127.0.0.1:8781"},
		{"port without colon falls back", "8782", "http://127.0.0.1:8781"},
		{"zero port falls back", ":0", "http://127.0.0.1:8781"},
		{"out-of-range port falls back", ":99999", "http://127.0.0.1:8781"},
		{"whitespace-only falls back", "  ", "http://127.0.0.1:8781"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_LISTEN", tc.listen)
			if got := GatewayBase(); got != tc.want {
				t.Fatalf("GatewayBase() with LLM_GATEWAY_LISTEN=%q = %q, want %q",
					tc.listen, got, tc.want)
			}
		})
	}
}
