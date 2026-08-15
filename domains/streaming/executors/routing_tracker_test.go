package executors

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
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

// TestClassifyResult_TypedNilUpstreamError_2026_07_20 guards against
// the panic that crashed minimax-m3 / gpt-5.6-luna chat requests on
// 2026-07-19/20. The trigger was a typed-nil *upstream.Error wrapped
// in a non-nil error interface:
//
//	var uErr *upstream.Error = nil
//	var iface error = uErr             // iface != nil (type tag)
//	iface.Error()                      // would dereference nil
//
// Previously ClassifyResult wrapped err.Error() in defer-recover().
// After the upstream-side nil-receiver fix in (*Error).Error() the
// recover is redundant and must stay removed: this test fails (panics)
// if a future refactor reintroduces the band-aid and silently swallows
// the bug. To verify the regression test itself, comment out the
// nil-check in upstream/client.go and re-run.
func TestClassifyResult_TypedNilUpstreamError_2026_07_20(t *testing.T) {
	var nilUErr *upstreampkg.Error
	var iface error = nilUErr
	// Sanity: typed-nil wrapped in error interface is non-nil at the
	// interface level (Go's classic gotcha). assert.NotNil cannot
	// detect this — it uses reflect.ValueOf().IsNil() which sees the
	// underlying nil pointer and reports nil — so we compare with
	// == nil directly.
	if iface == nil {
		t.Fatalf("typed-nil wrapped in interface must be non-nil; test setup is wrong")
	}
	if nilUErr != nil {
		t.Fatalf("sanity: nilUErr must be a nil pointer")
	}

	// This call must NOT panic and must return a deterministic bucket.
	// Pre-fix: panic. Post-fix (with safe (*Error).Error()): "error".
	var got string
	assert.NotPanics(t, func() {
		got = ClassifyResult(iface, 500)
	}, "ClassifyResult must not panic on typed-nil *upstream.Error")
	assert.NotEmpty(t, got, "ClassifyResult must return a bucket string")
}

func TestClassifyResult_PopulatedUpstreamError_2026_07_20(t *testing.T) {
	// Negative case: confirm the nil-fix did not regress the populated path.
	cases := []struct {
		name       string
		err        error
		statusCode int
		want       string
	}{
		{
			name:       "timeout with status 0 → timeout",
			err:        fmt.Errorf("context deadline exceeded"),
			statusCode: 0,
			want:       "timeout",
		},
		{
			name:       "429 with no message → rate_limit (statusCode wins)",
			err:        fmt.Errorf("something else"),
			statusCode: 429,
			want:       "rate_limit",
		},
		{
			name:       "real *upstream.Error with timeout message → timeout",
			err:        &upstreampkg.Error{Kind: upstreampkg.KindTimeout, Message: "upstream timeout", Err: errors.New("i/o timeout")},
			statusCode: 504,
			want:       "timeout",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyResult(tt.err, tt.statusCode)
			assert.Equal(t, tt.want, got)
		})
	}
}
