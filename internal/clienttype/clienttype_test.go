package clienttype

import "testing"

func TestNormalize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{" Cursor ", "cursor"},
		{"CLAUDE-CODE", "claude-code"},
		{"my-custom-agent", Unknown},
		{"cursor|other", Unknown},
		{"", Unknown},
		{"   ", Unknown},
	} {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
