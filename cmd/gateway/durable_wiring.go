package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// upgradePendingStoreForDurability replaces the Redis-only pending store with
// the explicit PostgreSQL fallback variant once all durable prerequisites are
// ready. Returning the original pointer on every disabled/incomplete path keeps
// the default request and pending-query behavior unchanged.
func upgradePendingStoreForDurability(
	current *pending.Store,
	rdb *redis.Client,
	ttl time.Duration,
	durableEnabled bool,
	db pending.PGQuerier,
	keyring *secret.Keyring,
) (*pending.Store, bool) {
	if !durableEnabled || db == nil || keyring == nil {
		return current, false
	}
	return pending.NewStoreWithFallback(rdb, ttl, pending.NewPGSource(db, keyring)), true
}

type durableCreateClaimer interface {
	CreateAndClaim(context.Context, durable.CreateTaskInput, string, time.Duration) (*durable.Task, error)
}

type durableLifecycleRepository interface {
	Checkpoint(context.Context, string, string, int64, string, json.RawMessage, bool) error
	HoldUntil(context.Context, string, string, int64, time.Time, string, time.Duration) error
	CommitTerminal(context.Context, string, string, int64, durable.TerminalResult) error
}

type durableRequestStoreAdapter struct {
	repository    durableCreateClaimer
	lifecycle     durableLifecycleRepository
	keyring       *secret.Keyring
	ownerPrefix   string
	leaseDuration time.Duration
}

func (a durableRequestStoreAdapter) CreateAndClaim(ctx context.Context, request streaming.DurableCreateRequest) (streaming.DurableLease, error) {
	policy, err := json.Marshal(map[string]any{
		"api_key_id":     request.APIKeyID,
		"application_id": request.ApplicationID,
		"version":        "request-survival:v1",
	})
	if err != nil {
		return streaming.DurableLease{}, err
	}
	owner := a.ownerPrefix + "/" + request.RequestID
	task, err := a.repository.CreateAndClaim(ctx, durable.CreateTaskInput{
		ID:                 request.TaskID,
		TenantID:           request.TenantID,
		RequestID:          request.RequestID,
		SessionID:          request.SessionID,
		Protocol:           request.Protocol,
		Endpoint:           request.Endpoint,
		SnapshotCiphertext: request.SnapshotCiphertext,
		SnapshotVersion:    request.SnapshotVersion,
		EncryptionKeyID:    request.EncryptionKeyID,
		RequestHash:        request.RequestHash,
		Policy:             policy,
		DeadlineAt:         request.DeadlineAt,
	}, owner, a.leaseDuration)
	if err != nil {
		return streaming.DurableLease{}, err
	}
	return streaming.DurableLease{
		TaskID:       task.ID,
		TenantID:     task.TenantID,
		RequestHash:  task.RequestHash,
		LeaseOwner:   task.LeaseOwner,
		FencingToken: task.FencingToken,
	}, nil
}

func (a durableRequestStoreAdapter) Checkpoint(ctx context.Context, lease streaming.DurableLease, state streaming.CommitState) error {
	if a.lifecycle == nil {
		return fmt.Errorf("durable lifecycle repository unavailable")
	}
	return a.lifecycle.Checkpoint(ctx, lease.TaskID, lease.LeaseOwner, lease.FencingToken, state.String(), nil, true)
}

func (a durableRequestStoreAdapter) Reschedule(ctx context.Context, lease streaming.DurableLease, nextRetryAt time.Time, reason string) error {
	if a.lifecycle == nil {
		return fmt.Errorf("durable lifecycle repository unavailable")
	}
	return a.lifecycle.HoldUntil(ctx, lease.TaskID, lease.LeaseOwner, lease.FencingToken, nextRetryAt, reason, a.leaseDuration)
}

func (a durableRequestStoreAdapter) Commit(ctx context.Context, lease streaming.DurableLease, body []byte, contentType string, decision streaming.TaskDecision) error {
	if a.keyring == nil {
		return fmt.Errorf("durable result: keyring unavailable")
	}
	hash := sha256.Sum256(body)
	resultHash := hex.EncodeToString(hash[:])
	ciphertext, _, err := secret.EncryptWithAAD(body, a.keyring, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: lease.TenantID, TaskID: lease.TaskID, RequestHash: lease.RequestHash})
	if err != nil {
		return fmt.Errorf("encrypt durable result: %w", err)
	}
	status := durable.StatusCompleted
	if decision.Action != streaming.TaskActionSucceed {
		status = durable.StatusPermanentFailed
	}
	if a.lifecycle == nil {
		return fmt.Errorf("durable lifecycle repository unavailable")
	}
	return a.lifecycle.CommitTerminal(ctx, lease.TaskID, lease.LeaseOwner, lease.FencingToken, durable.TerminalResult{
		Status: status, ReasonCode: decision.Reason, ResultCiphertext: ciphertext,
		ResultHash: resultHash, ResultVersion: 1, ContentType: contentType,
	})
}

type recoveryWorkerRunner interface {
	Run(context.Context)
}

type recoveryWorkerLifecycle interface {
	Start(context.Context)
	Stop()
}

type recoveryWorkerManager struct {
	runner recoveryWorkerRunner

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newRecoveryWorkerManager(runner recoveryWorkerRunner) *recoveryWorkerManager {
	if runner == nil {
		return nil
	}
	return &recoveryWorkerManager{runner: runner}
}

func (m *recoveryWorkerManager) Start(parent context.Context) {
	if m == nil || m.runner == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	go func() {
		defer close(done)
		m.runner.Run(ctx)
	}()
}

func (m *recoveryWorkerManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// startRecoveryWorker starts the durable worker only after the durable gate and
// all constructor prerequisites have produced a non-nil worker.
func startRecoveryWorker(ctx context.Context, durableEnabled bool, worker recoveryWorkerLifecycle) bool {
	if !durableEnabled || worker == nil {
		return false
	}
	worker.Start(ctx)
	return true
}

func stopRecoveryWorker(started bool, worker recoveryWorkerLifecycle) {
	if started && worker != nil {
		worker.Stop()
	}
}
