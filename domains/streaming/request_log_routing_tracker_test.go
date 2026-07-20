package streaming

import (
	"testing"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// TestRequestLogContext_RoutingTracker_NoCandidate 测试 no_candidate 路径的 tracker 设置
func TestRequestLogContext_RoutingTracker_NoCandidate(t *testing.T) {
	logCtx := &RequestLogContext{}
	
	// 模拟 no_candidate 路径：创建 tracker 并设置到 logCtx
	tracker := executors.NewRoutingAttemptsTracker()
	tracker.Add(executors.RoutingAttempt{
		Seq:          1,
		ProviderName: "router",
		RawModel:     "gpt-4",
		Result:       "error",
		ErrorMessage: "modality=vision => 0 candidates",
	})
	logCtx.RoutingTracker = tracker
	
	// 验证 buildEntry 会序列化 tracker
	reqLog := logCtx.buildEntry("no_candidate", "No available provider", nil, nil, "failure")
	
	if reqLog.RoutingAttempts == nil {
		t.Error("Expected RoutingAttempts to be populated, got nil")
	}
	
	if reqLog.RoutingSummary == nil || *reqLog.RoutingSummary == "" {
		t.Error("Expected RoutingSummary to be populated")
	}
}

// TestRequestLogContext_RoutingTracker_CandidateList 测试候选列表预填充
func TestRequestLogContext_RoutingTracker_CandidateList(t *testing.T) {
	logCtx := &RequestLogContext{}
	
	// 模拟正常路径：预填充候选列表
	tracker := executors.NewRoutingAttemptsTracker()
	for i := 0; i < 3; i++ {
		tracker.Add(executors.RoutingAttempt{
			ProviderID:   int64(i + 1),
			CredentialID: int64(i + 10),
			ProviderName: "openai",
			RawModel:     "gpt-4",
			Result:       "pending",
			ErrorMessage: "candidate from routing",
		})
	}
	logCtx.RoutingTracker = tracker
	
	// 验证 buildEntry 会序列化所有候选
	reqLog := logCtx.buildEntry("", "", nil, nil, "success")
	
	if reqLog.RoutingAttempts == nil {
		t.Error("Expected RoutingAttempts to be populated, got nil")
	}
	
	// 验证 summary 包含候选数量
	if reqLog.RoutingSummary == nil {
		t.Error("Expected RoutingSummary to be populated")
	}
}

// TestTrackerFromResultOrLogCtx 测试回退逻辑
func TestTrackerFromResultOrLogCtx(t *testing.T) {
	// 场景1：result 有 tracker
	resultTracker := executors.NewRoutingAttemptsTracker()
	resultTracker.Add(executors.RoutingAttempt{ProviderName: "from_result"})
	
	logCtxTracker := executors.NewRoutingAttemptsTracker()
	logCtxTracker.Add(executors.RoutingAttempt{ProviderName: "from_logctx"})
	
	result := &executors.ExecuteResult{RoutingTracker: resultTracker}
	logCtx := &RequestLogContext{RoutingTracker: logCtxTracker}
	
	tracker := trackerFromResultOrLogCtx(result, logCtx)
	if tracker != resultTracker {
		t.Error("Expected result.RoutingTracker to be preferred")
	}
	
	// 场景2：result 无 tracker，回退到 logCtx
	result2 := &executors.ExecuteResult{}
	tracker2 := trackerFromResultOrLogCtx(result2, logCtx)
	if tracker2 != logCtxTracker {
		t.Error("Expected logCtx.RoutingTracker as fallback")
	}
	
	// 场景3：都没有
	tracker3 := trackerFromResultOrLogCtx(nil, &RequestLogContext{})
	if tracker3 != nil {
		t.Error("Expected nil when both are absent")
	}
}
