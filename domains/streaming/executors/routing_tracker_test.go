package executors

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingAttemptsTracker_Add(t *testing.T) {
	tracker := NewRoutingAttemptsTracker()
	
	tracker.Add(RoutingAttempt{
		ProviderID:   35,
		ProviderName: "火山方舟",
		CredentialID: 12,
		RawModel:     "glm-5.2",
		UpstreamURL:  "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		Result:       "model_not_found",
		LatencyMs:    1523,
		HTTPStatus:   404,
	})
	
	tracker.Add(RoutingAttempt{
		ProviderID:   18,
		ProviderName: "NVIDIA NIM",
		CredentialID: 23,
		RawModel:     "z-ai/glm-5.2",
		UpstreamURL:  "https://integrate.api.nvidia.com/v1/chat/completions",
		Result:       "canceled",
		LatencyMs:    120000,
	})
	
	assert.Equal(t, 2, tracker.Count())
	assert.Equal(t, 1, tracker.attempts[0].Seq)
	assert.Equal(t, 2, tracker.attempts[1].Seq)
}

func TestRoutingAttemptsTracker_ToJSONBytes_SingleSuccess(t *testing.T) {
	tracker := NewRoutingAttemptsTracker()
	
	tracker.Add(RoutingAttempt{
		ProviderID: 35,
		Result:     "success",
		LatencyMs:  234,
	})
	
	// 单次成功应该返回 nil（优化存储）
	bytes, err := tracker.ToJSONBytes()
	require.NoError(t, err)
	assert.Nil(t, bytes)
}

func TestRoutingAttemptsTracker_ToJSONBytes_MultipleAttempts(t *testing.T) {
	tracker := NewRoutingAttemptsTracker()
	
	tracker.Add(RoutingAttempt{
		ProviderID: 35,
		Result:     "model_not_found",
		LatencyMs:  1523,
	})
	
	tracker.Add(RoutingAttempt{
		ProviderID: 18,
		Result:     "canceled",
		LatencyMs:  120000,
	})
	
	bytes, err := tracker.ToJSONBytes()
	require.NoError(t, err)
	require.NotNil(t, bytes)
	
	var result map[string]interface{}
	err = json.Unmarshal(bytes, &result)
	require.NoError(t, err)
	
	attempts, ok := result["attempts"].([]interface{})
	require.True(t, ok)
	assert.Len(t, attempts, 2)
	
	first := attempts[0].(map[string]interface{})
	assert.Equal(t, float64(1), first["seq"])
	assert.Equal(t, float64(35), first["provider_id"])
	assert.Equal(t, "model_not_found", first["result"])
}

func TestRoutingAttemptsTracker_Summary(t *testing.T) {
	tests := []struct {
		name     string
		attempts []RoutingAttempt
		want     string
	}{
		{
			name:     "empty",
			attempts: []RoutingAttempt{},
			want:     "",
		},
		{
			name: "single success",
			attempts: []RoutingAttempt{
				{ProviderID: 35, Result: "success", LatencyMs: 234},
			},
			want: "", // 单次成功不生成摘要
		},
		{
			name: "multiple attempts",
			attempts: []RoutingAttempt{
				{ProviderID: 35, ProviderName: "火山方舟", Result: "model_not_found", LatencyMs: 1523},
				{ProviderID: 18, ProviderName: "NVIDIA NIM", Result: "canceled", LatencyMs: 120000},
			},
			want: "候选1: 火山方舟(35) 模型未找到 1.5s → 候选2: NVIDIA NIM(18) 取消 120.0s",
		},
		{
			name: "without provider name",
			attempts: []RoutingAttempt{
				{ProviderID: 35, Result: "timeout", LatencyMs: 5000},
			},
			want: "候选1: 35(35) 超时 5.0s",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewRoutingAttemptsTracker()
			for _, a := range tt.attempts {
				tracker.Add(a)
			}
			
			got := tracker.Summary()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClassifyResult(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		want       string
	}{
		{
			name:       "success",
			err:        nil,
			statusCode: 200,
			want:       "success",
		},
		{
			name:       "context canceled",
			err:        errors.New("context canceled"),
			statusCode: 0,
			want:       "canceled",
		},
		{
			name:       "timeout",
			err:        errors.New("deadline exceeded"),
			statusCode: 0,
			want:       "timeout",
		},
		{
			name:       "404",
			err:        errors.New("model not found"),
			statusCode: 404,
			want:       "model_not_found",
		},
		{
			name:       "429",
			err:        errors.New("rate limit"),
			statusCode: 429,
			want:       "rate_limit",
		},
		{
			name:       "401",
			err:        errors.New("unauthorized"),
			statusCode: 401,
			want:       "unauthorized",
		},
		{
			name:       "500",
			err:        errors.New("internal server error"),
			statusCode: 500,
			want:       "error",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyResult(tt.err, tt.statusCode)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRoutingAttemptsTracker_NilSafety(t *testing.T) {
	var tracker *RoutingAttemptsTracker
	
	// 所有方法都应该能处理 nil receiver
	assert.NotPanics(t, func() {
		tracker.Add(RoutingAttempt{})
	})
	
	assert.Equal(t, 0, tracker.Count())
	
	bytes, err := tracker.ToJSONBytes()
	assert.NoError(t, err)
	assert.Nil(t, bytes)
	
	assert.Empty(t, tracker.ToJSONString())
	assert.Empty(t, tracker.Summary())
}
