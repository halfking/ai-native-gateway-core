package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromoteHotRequestRetentionHours(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *int
	}{
		{name: "omitted uses default later", body: `{"table_name":"request_logs_hot"}`, want: nil},
		{name: "zero is preserved", body: `{"table_name":"request_logs_hot","retention_hours":0}`, want: intPtr(0)},
		{name: "one day is preserved", body: `{"table_name":"request_logs_hot","retention_hours":24}`, want: intPtr(24)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got promoteHotRequest
			if err := json.NewDecoder(strings.NewReader(tt.body)).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.RetentionHours == nil && tt.want == nil {
				return
			}
			if got.RetentionHours == nil || tt.want == nil || *got.RetentionHours != *tt.want {
				t.Fatalf("retention_hours = %v, want %v", got.RetentionHours, tt.want)
			}
		})
	}
}

func intPtr(value int) *int {
	return &value
}
