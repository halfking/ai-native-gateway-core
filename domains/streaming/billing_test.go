package streaming

import "testing"

func TestShouldChargeUsage(t *testing.T) {
	stage := func(value string) *string { return &value }
	kind := func(value string) *string { return &value }

	tests := []struct {
		name                  string
		success               bool
		failureStage          *string
		errorKind             *string
		prompt, completion    int
		cacheRead, cacheWrite int
		want                  bool
	}{
		{name: "successful upstream request", success: true, prompt: 10, want: true},
		{name: "successful request without stage", success: true, prompt: 10, want: true},
		{name: "client cancelled after upstream usage", success: false, failureStage: stage("upstream"), prompt: 10, want: true},
		{name: "client cancelled without stage", success: false, errorKind: kind("client_cancel"), prompt: 10, want: true},
		{name: "gateway auth failure", failureStage: stage("auth"), prompt: 10, want: false},
		{name: "routing failure", failureStage: stage("routing"), completion: 10, want: false},
		{name: "unknown failed stage", failureStage: stage("unknown"), completion: 10, want: false},
		{name: "empty response", success: false, failureStage: stage("upstream"), errorKind: kind("empty_response"), completion: 10, want: false},
		{name: "no usage", success: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldChargeUsage(tt.success, tt.failureStage, tt.errorKind, tt.prompt, tt.completion, tt.cacheRead, tt.cacheWrite, 1)
			if got != tt.want {
				t.Fatalf("shouldChargeUsage() = %v, want %v", got, tt.want)
			}
		})
	}
}
