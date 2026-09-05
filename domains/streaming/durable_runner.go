package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// durable_runner.go — SR-12 生产 DurableAttemptRunner（doc 18 §11.2/§11.3）。
//
// RecoveryWorker 每次领取任务后通过 Run 重建并执行一个 detached attempt：
//   - VerifyByID 用快照中的 api_key_id 重验服务端授权（凭据永不落快照，
//     失效 key → 永久终态，DB 瞬态错误 → runner error 由 worker 重排）；
//   - 候选动态重算（禁持久旧候选），credential/circuit/limiter 状态取当下值；
//   - ExecParams 为后台形态：无 writer、SuppressSuccessWrite、合成 request
//     携带 session/correlation header，正文从 ResponseBody 提取。

// DurableKeyVerifier is the narrow re-authorization surface the runner
// needs. *authentication.KeyVerifier satisfies it.
type DurableKeyVerifier interface {
	VerifyByID(ctx context.Context, id int) (*authentication.KeyInfo, error)
}

// DurableAttemptRunnerImpl is the production DurableAttemptRunner.
type DurableAttemptRunnerImpl struct {
	exec     AttemptExecutor
	resolver providerResolver
	verifier DurableKeyVerifier
	// BudgetProvider optionally supplies a task-scoped upstream attempt
	// budget so every detached execution of the same task shares one call
	// ceiling, mirroring the request-wide budget the foreground coordinator
	// owns. nil keeps the executor default (one budget per execution).
	BudgetProvider func(taskID string) *executors.UpstreamAttemptBudget
}

// NewDurableAttemptRunner wires the production runner. exec/resolver are the
// same instances the live ChatHandler uses; verifier is the shared
// KeyVerifier (VerifyByID path only).
func NewDurableAttemptRunner(exec AttemptExecutor, resolver providerResolver, verifier DurableKeyVerifier) *DurableAttemptRunnerImpl {
	return &DurableAttemptRunnerImpl{exec: exec, resolver: resolver, verifier: verifier}
}

// Run implements DurableAttemptRunner. A returned error means "runner-level
// infrastructure failure" — the worker reschedules and the deadline reaper
// owns the terminal (audit-fixed semantics: never a permanent terminal from
// here). Permanent verdicts are folded into AttemptResult.CandidateOutcomes
// for AggregateTaskOutcome instead.
func (r *DurableAttemptRunnerImpl) Run(ctx context.Context, task *durable.Task, snap *durable.Snapshot) (*DurableAttempt, error) {
	if task == nil || snap == nil {
		return nil, errors.New("durable runner: nil task or snapshot")
	}
	snapshot, err := UnmarshalDurableSnapshotV1(snap.Body)
	if err != nil {
		return nil, fmt.Errorf("durable runner: snapshot decode: %w", err)
	}
	if r.verifier == nil {
		// Miswired startup must surface as a bounded runner error (worker
		// reschedules), never as a per-task panic loop.
		return nil, errors.New("durable runner: key verifier not wired")
	}

	ki, err := r.verifier.VerifyByID(ctx, snapshot.APIKeyID)
	if err != nil {
		var invalid *authentication.InvalidKeyError
		if errors.As(err, &invalid) {
			// Key revoked/disabled/expired: permanent terminal for the
			// task (doc 18 §11.2 — never replay credentials to retry).
			return &DurableAttempt{
				Result: &AttemptResult{
					Success:           false,
					FinalError:        err,
					CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindAuthRevoked, Err: err}},
				},
				ErrorKind: string(errorsx.KindAuthRevoked),
				Attempt:   task.AttemptCount,
			}, nil
		}
		return nil, fmt.Errorf("durable runner: re-verify key %d: %w", snapshot.APIKeyID, err)
	}

	// Routing identity inputs are frozen at acceptance: the snapshot's model
	// and client profile define the request that was authorized. Only live
	// node/circuit/credential state is re-derived below. Snapshots written
	// before client_profile was persisted fall back to the key's current
	// default (the historical behavior).
	profile := snapshot.ClientProfile
	if profile == "" && ki.DefaultClientProfile != nil {
		profile = *ki.DefaultClientProfile
	}
	cands, policy, _, err := resolveCandidatesForRequest(ctx, r.resolver, snapshot.ClientModel, profile, ki.TenantID, snapshot.NormalizedBody)
	if err != nil || len(cands) == 0 {
		kind := errorsx.KindNoAvailableChannel
		if err != nil {
			kind = errorsx.ClassifyError(err, nil)
		}
		outcome := CandidateOutcome{Kind: kind, Err: err}
		if err == nil {
			outcome.Err = fmt.Errorf("durable runner: no candidates for model %q", snapshot.ClientModel)
		}
		return &DurableAttempt{
			Result:    &AttemptResult{Success: false, FinalError: outcome.Err, CandidateOutcomes: []CandidateOutcome{outcome}},
			ErrorKind: string(kind),
			Attempt:   task.AttemptCount,
		}, nil
	}

	appID := ki.ApplicationID
	keyID := ki.ID
	clientID := identity.BuildIdentity(ki.TenantID, &appID, &keyID, identity.ClientFingerprint{ClientProfile: profile})

	params := &executors.ExecParams{
		R:                    durableSyntheticRequest(ctx, snapshot),
		BodyBytes:            snapshot.NormalizedBody,
		IsStream:             false,
		SuppressSuccessWrite: true,
		ClientModel:          snapshot.ClientModel,
		ClientID:             clientID,
		Candidates:           cands,
		Policy:               policy,
		ClientProtocol:       snapshot.ClientProtocol,
		SessionID:            snapshot.SessionID,
		TenantID:             ki.TenantID,
		RequestID:            snapshot.RequestID,
		KeyID:                ki.ID,
		AppID:                &appID,
		ApiKeyID:             &keyID,
		ToolsRequested:       snapshot.ToolsRequested,
	}
	if snapshot.Endpoint == "/v1/responses" {
		// Native Responses candidates require the preserved request body;
		// without it every detached pass fails unsupported_feature and burns
		// the task budget. The foreground path sets the same pair.
		params.ResponsesBodyBytes = snapshot.NormalizedBody
	}
	if r.BudgetProvider != nil {
		params.UpstreamAttempts = r.BudgetProvider(task.ID)
	}

	result := ExecuteAttempt(ctx, r.exec, nil, params)
	attempt := &DurableAttempt{
		Result:    result,
		ErrorKind: string(result.LastKind()),
		Attempt:   task.AttemptCount,
	}
	if result.Success && result.ExecResult != nil {
		attempt.Body = result.ExecResult.ResponseBody
		attempt.ContentType = "application/json"
		if result.ExecResult.Response != nil {
			if ct := result.ExecResult.Response.Header.Get("Content-Type"); ct != "" {
				attempt.ContentType = ct
			}
		}
	}
	return attempt, nil
}

// durableSyntheticRequest builds the detached attempt's request: the run
// context (worker-owned, cancel-safe), session/correlation headers for
// downstream routing, and no client connection anywhere.
func durableSyntheticRequest(ctx context.Context, snapshot *DurableRequestSnapshotV1) *http.Request {
	endpoint := snapshot.Endpoint
	if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		req = (&http.Request{Method: http.MethodPost}).WithContext(ctx)
	}
	req.Header.Set("X-Gw-Session-Id", snapshot.SessionID)
	req.Header.Set("X-Request-Id", snapshot.RequestID)
	req.Header.Set("X-Gw-Task-Id", snapshot.TaskCorrelationID)
	return req
}
