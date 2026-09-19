package admin

import "testing"

// R46 F9: 决策回放的 request_id 双形态匹配——同一请求可能以 hex32 形态
// 存于 hot（TEXT 原样），promote 进母表（UUID 列）后归一为 dashed 形态读出。
func TestUUIDVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "hex32 展开 dashed 双形态",
			in:   "841eb980c90c46d86dbcf0556cbb2687",
			want: []string{"841eb980c90c46d86dbcf0556cbb2687", "841eb980-c90c-46d8-6dbc-f0556cbb2687"},
		},
		{
			name: "dashed 补充紧凑形态",
			in:   "841EB980-C90C-46D8-6DBC-F0556CBB2687",
			want: []string{"841EB980-C90C-46D8-6DBC-F0556CBB2687", "841eb980c90c46d86dbcf0556cbb2687"},
		},
		{
			name: "探测字符串 id 原样",
			in:   "probe-direct-c29-mminimax-m3-a2-ok-1789808764875985922",
			want: []string{"probe-direct-c29-mminimax-m3-a2-ok-1789808764875985922"},
		},
		{
			name: "32 位但含非 hex 字符原样",
			in:   "probe-direct-c29-mminimax-m3-a2-0123456789",
			want: []string{"probe-direct-c29-mminimax-m3-a2-0123456789"},
		},
		{
			name: "短串原样",
			in:   "abc",
			want: []string{"abc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uuidVariants(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("uuidVariants(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("uuidVariants(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
