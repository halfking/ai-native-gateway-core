package admin

import (
	"testing"
)

func TestParseVendorErrorHours(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		err  bool
	}{
		{name: "default", want: 24},
		{name: "one hour", raw: "1", want: 1},
		{name: "one day", raw: "24", want: 24},
		{name: "seven days", raw: "168", want: 168},
		{name: "invalid text", raw: "abc", err: true},
		{name: "unsupported window", raw: "48", err: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVendorErrorHours(tt.raw)
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVendorErrorHours() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseVendorErrorHours() = %d, want %d", got, tt.want)
			}
		})
	}
}
