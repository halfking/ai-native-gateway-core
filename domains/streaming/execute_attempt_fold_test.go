package streaming

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 2026-09-05 审计闭环2回归：dispatch_v2 路径从不填充 ExecuteError.Attempts
// （全仓无追加点），foldCandidateOutcomes 必须从 RoutingAttemptsTracker 的
// 真实失败尝试恢复 per-candidate 结果并补齐诊断维度，否则聚合器只看到
// no_candidate_outcomes → fail-closed，且丢失全部候选诊断。

func boolPtrFold(v bool) *bool { return &v }

func TestFoldCandidateOutcomesRecoversFromTracker(t *testing.T) {
	tracker := executors.NewRoutingAttemptsTracker()
	// pending 占位（候选池预填）必须被排除。
	tracker.Add(executors.RoutingAttempt{
		ProviderID: int64(3), CredentialID: int64(30), RawModel: "glm-5.2",
		Result: executors.ResultPending,
	})
	// 两个真实失败候选：一个 HTTP 503 超时、一个 429。
	tracker.Add(executors.RoutingAttempt{
		ProviderID: int64(1), CredentialID: int64(10), ProviderName: "zhipu",
		RawModel: "glm-5.2", Result: "timeout", LatencyMs: 1500, HTTPStatus: 503,
		ErrorKind: string(errorsx.KindTimeout), Stage: "upstream",
		Retryable: boolPtrFold(true),
	})
	tracker.Add(executors.RoutingAttempt{
		ProviderID: int64(2), CredentialID: int64(20), ProviderName: "openai",
		RawModel: "gpt-5", Result: "rate_limit", LatencyMs: 220, HTTPStatus: 429,
		ErrorKind: string(errorsx.KindRateLimit), Stage: "upstream",
		Retryable: boolPtrFold(false),
	})

	execErr := &executors.ExecuteError{
		LastErr:   fmt.Errorf("all candidates failed"),
		Exhausted: true,
		LastKind:  errorsx.KindTimeout,
		// Attempts 刻意为空：这是 dispatch_v2 生产路径的真实形状。
	}

	outcomes := foldCandidateOutcomes(execErr, "req-fold-1", tracker)
	require.Len(t, outcomes, 2, "pending placeholders excluded; two real failures folded")

	first := outcomes[0]
	assert.Equal(t, "req-fold-1", first.RequestID)
	assert.Equal(t, 2, first.AttemptSeq, "attempt_seq mirrors tracker Seq (pending placeholder consumed seq 1)")
	assert.Equal(t, 1, first.ProviderID)
	assert.Equal(t, "10", first.CredentialID)
	assert.Equal(t, "zhipu", first.Supplier)
	assert.Equal(t, "glm-5.2", candidateModel(first.CandidateID))
	assert.Equal(t, errorsx.KindTimeout, first.Kind)
	assert.Equal(t, 503, first.HTTPStatus)
	require.NotNil(t, first.Retryable)
	assert.True(t, *first.Retryable)
	assert.Equal(t, int64(1500), first.LatencyMs)
	assert.Equal(t, "upstream", first.Stage)

	second := outcomes[1]
	assert.Equal(t, 3, second.AttemptSeq)
	assert.Equal(t, 429, second.HTTPStatus)
	require.NotNil(t, second.Retryable)
	assert.False(t, *second.Retryable)

	// 每个候选都必须能驱动聚合决策（不再是 no_candidate_outcomes）。
	decision := AggregateTaskOutcome(&AttemptResult{
		Success:           false,
		CandidateOutcomes: outcomes,
		CommitState:       CommitStateNone,
	})
	assert.Equal(t, TaskActionRetryNow, decision.Action,
		"timeout+rate_limit mix must fold to retry-now via recoverable candidate")
}

func TestFoldCandidateOutcomesTrackerFallbackKind(t *testing.T) {
	// tracker 条目没有显式 ErrorKind（旧数据/未迁移写入端）：按 result
	// 与 http_status 兜底分类，保证聚合器仍然拿到非空 kind。
	tracker := executors.NewRoutingAttemptsTracker()
	tracker.Add(executors.RoutingAttempt{
		ProviderID: 5, CredentialID: 50, RawModel: "m",
		Result: "error", HTTPStatus: 502, LatencyMs: 90,
	})
	outcomes := foldCandidateOutcomes(&executors.ExecuteError{
		LastErr: fmt.Errorf("boom"), Exhausted: true,
	}, "req-fold-2", tracker)
	require.Len(t, outcomes, 1)
	assert.Equal(t, errorsx.KindUpstreamOverloaded, outcomes[0].Kind,
		"5xx without explicit kind classifies as upstream_overloaded")
	require.NotNil(t, outcomes[0].Retryable)
	assert.True(t, *outcomes[0].Retryable)
}

func TestFoldCandidateOutcomesNoTrackerKeepsSynthesis(t *testing.T) {
	// 无 tracker（durable 恢复等路径）时保持原合成行为：
	// no_available_channel 单候选，聚合器看到 wait-recovery。
	outcomes := foldCandidateOutcomes(&executors.ExecuteError{
		LastErr:  fmt.Errorf("no route"),
		Exhausted: true,
	}, "req-fold-3", nil)
	require.Len(t, outcomes, 1)
	assert.Equal(t, errorsx.KindNoAvailableChannel, outcomes[0].Kind)
	assert.Equal(t, "req-fold-3", outcomes[0].RequestID)
}
