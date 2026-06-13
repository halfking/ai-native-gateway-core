package relay

import "testing"

func TestExtractModelFromBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "chat model", body: `{"model":"glm-4.5","messages":[]}`, want: "glm-4.5"},
		{name: "whitespace", body: `{"model":"  deepseek-r1  "}`, want: "deepseek-r1"},
		{name: "empty", body: `{"messages":[]}`, want: ""},
		{name: "invalid json", body: `{bad`, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractModelFromBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("extractModelFromBody() = %q, want %q", got, tc.want)
			}
		})
	}
}
