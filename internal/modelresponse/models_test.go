package modelresponse

import "testing"

func TestParseModelIDs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr bool
	}{
		{name: "openai objects", body: `{"data":[{"id":"glm-5.2"}]}`, want: []string{"glm-5.2"}},
		{name: "string collection", body: `{"data":["glm-5.2","minimax-m3"]}`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "nested wrappers", body: `{"result":{"data":{"items":[{"model_id":"glm-5.2"},{"model_name":"minimax-m3"}]}}}`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "bare mixed array", body: `[{"model":"glm-5.2"},"minimax-m3"]`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "gemini prefix and dedup", body: `{"models":[{"name":"models/gemini-2.5-pro"},{"id":"GEMINI-2.5-PRO"}]}`, want: []string{"gemini-2.5-pro"}},
		{name: "error envelope", body: `{"error":{"message":"model glm-5.2 unavailable"}}`, wantErr: true},
		{name: "invalid json", body: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseModelIDs([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseModelIDs() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelIDs() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseModelIDs() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("ParseModelIDs() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
