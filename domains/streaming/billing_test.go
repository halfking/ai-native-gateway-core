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
		streamChunkCount      int
		want                  bool
	}{
		{name: "successful upstream request", success: true, prompt: 10, streamChunkCount: 4, want: true},
		{name: "successful request without stage", success: true, prompt: 10, streamChunkCount: 4, want: true},
		{name: "client cancelled after upstream usage", success: false, failureStage: stage("upstream"), prompt: 10, streamChunkCount: 3, want: true},
		{name: "client cancelled without stage but chunks", success: false, errorKind: kind("client_cancel"), prompt: 10, streamChunkCount: 2, want: true},
		{name: "client cancelled with no chunks", success: false, errorKind: kind("client_cancel"), failureStage: stage("upstream"), prompt: 10, streamChunkCount: 0, want: false},
		{name: "client disconnected with chunks", success: false, errorKind: kind("client_disconnected"), failureStage: stage("upstream"), prompt: 10, streamChunkCount: 5, want: true},
		{name: "client disconnected with no chunks", success: false, errorKind: kind("client_disconnected"), prompt: 10, streamChunkCount: 0, want: false},
		{name: "gateway auth failure", failureStage: stage("auth"), prompt: 10, streamChunkCount: 1, want: false},
		{name: "routing failure", failureStage: stage("routing"), completion: 10, streamChunkCount: 1, want: false},
		{name: "unknown failed stage", failureStage: stage("unknown"), completion: 10, streamChunkCount: 1, want: false},
		{name: "empty response", success: false, failureStage: stage("upstream"), errorKind: kind("empty_response"), completion: 10, streamChunkCount: 1, want: false},
		{name: "no usage", success: true, streamChunkCount: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldChargeUsage(tt.success, tt.failureStage, tt.errorKind, tt.prompt, tt.completion, tt.cacheRead, tt.cacheWrite, tt.streamChunkCount)
			if got != tt.want {
				t.Fatalf("shouldChargeUsage() = %v, want %v", got, tt.want)
			}
		})
	}
}
