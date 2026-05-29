package admin

import "testing"

func TestParseKeyActionRoute(t *testing.T) {
	tests := []struct {
		name      string
		remaining string
		want      keyActionRoute
	}{
		{
			name:      "root path",
			remaining: "",
			want:      keyActionRoute{kind: "root"},
		},
		{
			name:      "verify action without id",
			remaining: "verify",
			want:      keyActionRoute{kind: "action", subPath: "verify"},
		},
		{
			name:      "budget check action without id",
			remaining: "budget-check",
			want:      keyActionRoute{kind: "action", subPath: "budget-check"},
		},
		{
			name:      "apply action without id",
			remaining: "apply",
			want:      keyActionRoute{kind: "action", subPath: "apply"},
		},
		{
			name:      "detail resource with id",
			remaining: "123/detail/123",
			want:      keyActionRoute{kind: "resource", idPart: "123", subPath: "detail/123"},
		},
		{
			name:      "reveal resource with id",
			remaining: "42/reveal",
			want:      keyActionRoute{kind: "resource", idPart: "42", subPath: "reveal"},
		},
		{
			name:      "plain numeric id",
			remaining: "88",
			want:      keyActionRoute{kind: "resource", idPart: "88", subPath: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseKeyActionRoute(tt.remaining)
			if got != tt.want {
				t.Fatalf("parseKeyActionRoute(%q) = %#v, want %#v", tt.remaining, got, tt.want)
			}
		})
	}
}
