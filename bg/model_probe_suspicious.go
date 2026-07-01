// Package bg — model_probe_suspicious.go
//
// SuspiciousProbeRunner implements the suspicious state auto-expiry mechanism.
// Models automatically become suspicious after 2 hours and need re-validation.
//
// State machine (per credential × model, stored in model_probe_state):
//
//	available    (2小时后自动→suspicious)
//	   ↓
//	suspicious   (等待探测或实际调用验证)
//	   ↓ (探测开始)
//	probing      (探测中，防止重复)
//	   ↓ (探测完成)
//	available / unavailable  (2小时后再次→suspicious)
//
// Key features:
//  1. available/unavailable 状态在2小时后自动过期为 suspicious
//  2. suspicious 状态的模型由后台异步探测
//  3. 同一凭据最多2个并发探测线程
//  4. 探测结果记录到 model_probe_runs
//
// Spec: 2026-06-28-suspicious-state-auto-expiry
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

const (
	// SuspiciousProbeInterval is how often the suspicious cycle runs
	SuspiciousProbeInterval = 2 * time.Minute

	// MaxCredentialConcurrency limits concurrent probes per credential
	MaxCredentialConcurrency = 2

	// MaxSuspiciousProbesBatch caps probes per cycle
	MaxSuspiciousProbesBatch = 30
)

// SuspiciousProbeRunner manages the suspicious state lifecycle
type SuspiciousProbeRunner struct {
	db      *pgxpool.Pool
	encKey  []byte
	keyring *secret.Keyring
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewSuspiciousProbeRunner(db *pgxpool.Pool, encKey []byte) *SuspiciousProbeRunner {
	return &SuspiciousProbeRunner{
		db:     db,
		encKey: encKey,
		done:   make(chan struct{}),
	}
}

func (r *SuspiciousProbeRunner) SetKeyring(kr *secret.Keyring) { r.keyring = kr }

func (r *SuspiciousProbeRunner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	slog.Info("suspicious probe runner started",
		"interval", SuspiciousProbeInterval,
		"max_credential_concurrency", MaxCredentialConcurrency,
		"max_batch", MaxSuspiciousProbesBatch,
	)
}

func (r *SuspiciousProbeRunner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *SuspiciousProbeRunner) run(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(SuspiciousProbeInterval)
	defer ticker.Stop()

	// Run immediately on start
	r.cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// cycle runs one suspicious probe cycle:
//  1. Expire available/unavailable states to suspicious
//  2. Clean up stuck probing states
//  3. Probe suspicious models (with credential concurrency limit)
func (r *SuspiciousProbeRunner) cycle(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Step 1: Expire states to suspicious
	expiredCount := r.expireSuspicious(timeoutCtx)
	if expiredCount > 0 {
		slog.Info("suspicious probe: expired states to suspicious",
			"count", expiredCount)
	}

	// Step 2: Clean up stuck probing states
	cleanedCount := r.cleanupStuckProbing(timeoutCtx)
	if cleanedCount > 0 {
		slog.Warn("suspicious probe: cleaned up stuck probing states",
			"count", cleanedCount)
	}

	// Step 3: Probe suspicious models
	r.probeSuspicious(timeoutCtx)
}

// expireSuspicious calls the SQL function to expire states
func (r *SuspiciousProbeRunner) expireSuspicious(ctx context.Context) int {
	var count int
	err := r.db.QueryRow(ctx, `SELECT model_probe_expire_to_suspicious()`).Scan(&count)
	if err != nil {
		slog.Warn("suspicious probe: expire failed", "error", err)
		return 0
	}
	return count
}

// cleanupStuckProbing calls the SQL function to clean up stuck probing states
func (r *SuspiciousProbeRunner) cleanupStuckProbing(ctx context.Context) int {
	var count int
	err := r.db.QueryRow(ctx, `SELECT model_probe_cleanup_stuck_probing()`).Scan(&count)
	if err != nil {
		slog.Warn("suspicious probe: cleanup failed", "error", err)
		return 0
	}
	return count
}

// suspiciousTarget represents a model to probe
type suspiciousTarget struct {
	CredentialID  int
	RawModel      string
	OutboundModel string
	BaseURL       string
	Protocol      string
	APIKey        string
}

// probeSuspicious probes up to MaxSuspiciousProbesBatch suspicious models,
// respecting the per-credential concurrency limit
func (r *SuspiciousProbeRunner) probeSuspicious(ctx context.Context) {
	// Query suspicious models from the view (already filters by concurrency < 2)
	rows, err := r.db.Query(ctx, `
		SELECT credential_id, raw_model_name, outbound_model_name,
		       base_url, protocol
		FROM v_suspicious_probe_targets
		LIMIT $1
	`, MaxSuspiciousProbesBatch)
	if err != nil {
		slog.Warn("suspicious probe: query failed", "error", err)
		return
	}
	defer rows.Close()

	var targets []suspiciousTarget
	for rows.Next() {
		var t suspiciousTarget
		if err := rows.Scan(&t.CredentialID, &t.RawModel, &t.OutboundModel,
			&t.BaseURL, &t.Protocol); err != nil {
			continue
		}
		targets = append(targets, t)
	}

	if len(targets) == 0 {
		return
	}

	// Probe each target
	probed := 0
	available := 0
	unavailable := 0
	skipped := 0

	for _, t := range targets {
		// Try to acquire probe permission (atomic check + update to 'probing')
		canProbe, err := r.acquireProbePermission(ctx, t.CredentialID, t.RawModel)
		if err != nil {
			slog.Debug("suspicious probe: acquire permission failed",
				"credential_id", t.CredentialID,
				"raw_model", t.RawModel,
				"error", err)
			skipped++
			continue
		}
		if !canProbe {
			// Credential concurrency limit reached or state changed
			skipped++
			continue
		}

		// Decrypt API key
		var ciphertext []byte
		err = r.db.QueryRow(ctx, `SELECT secret_ciphertext FROM credentials WHERE id = $1`,
			t.CredentialID).Scan(&ciphertext)
		if err != nil {
			r.markUnavailable(ctx, t.CredentialID, t.RawModel, "decrypt_error", "failed to load credential")
			unavailable++
			continue
		}

		apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
		if decErr != nil {
			r.markUnavailable(ctx, t.CredentialID, t.RawModel, "decrypt_error", decErr.Error())
			unavailable++
			continue
		}
		t.APIKey = apiKey

		// Perform the probe
		ok, errMsg := r.probeModel(ctx, t)
		probed++

		if ok {
			r.markAvailable(ctx, t.CredentialID, t.RawModel)
			available++
		} else {
			r.markUnavailable(ctx, t.CredentialID, t.RawModel, "probe_failed", errMsg)
			unavailable++
		}

		// Small delay between probes to avoid hammering upstreams
		time.Sleep(500 * time.Millisecond)
	}

	if probed > 0 {
		slog.Info("suspicious probe: cycle complete",
			"probed", probed,
			"available", available,
			"unavailable", unavailable,
			"skipped", skipped,
		)
	}
}

// acquireProbePermission atomically tries to start probing
func (r *SuspiciousProbeRunner) acquireProbePermission(ctx context.Context, credID int, rawModel string) (bool, error) {
	var canProbe bool
	err := r.db.QueryRow(ctx,
		`SELECT model_probe_start_probing($1, $2, $3)`,
		credID, rawModel, MaxCredentialConcurrency,
	).Scan(&canProbe)
	return canProbe, err
}

// probeModel performs a simple probe (models list or minimal chat)
func (r *SuspiciousProbeRunner) probeModel(ctx context.Context, t suspiciousTarget) (bool, string) {
	if t.BaseURL == "" {
		return false, "empty base_url"
	}

	desc := providercap.Resolve(t.Protocol, "")

	// Use the same probing logic as the main model probe runner
	// For simplicity, we'll do a models list probe
	mode := ProbeModeModelsList
	if desc.Protocol == "anthropic-messages" {
		mode = ProbeModeMessages
	}

	target := probeTarget{
		CredentialID:  t.CredentialID,
		RawModel:      t.RawModel,
		OutboundModel: t.OutboundModel,
		BaseURL:       t.BaseURL,
		Protocol:      t.Protocol,
		APIKey:        t.APIKey,
	}

	result := probeWithRetry(ctx, desc, target, mode)
	return result.status == "ok", result.errMsg
}

// markAvailable marks a model as available (expires after 2 hours)
func (r *SuspiciousProbeRunner) markAvailable(ctx context.Context, credID int, rawModel string) {
	_, err := r.db.Exec(ctx, `SELECT model_probe_mark_available($1, $2, 0)`, credID, rawModel)
	if err != nil {
		slog.Warn("suspicious probe: mark available failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", err)
	}
}

// markUnavailable marks a model as unavailable (expires after 2 hours)
func (r *SuspiciousProbeRunner) markUnavailable(ctx context.Context, credID int, rawModel string, errCode, errMsg string) {
	_, err := r.db.Exec(ctx, `SELECT model_probe_mark_unavailable($1, $2, $3, $4)`,
		credID, rawModel, errCode, errMsg)
	if err != nil {
		slog.Warn("suspicious probe: mark unavailable failed",
			"credential_id", credID,
			"raw_model", rawModel,
			"error", err)
	}
}

// OnModelCalled should be called when a suspicious model is actually used in a request.
// It updates the state based on the request outcome.
func (r *SuspiciousProbeRunner) OnModelCalled(ctx context.Context, credID int, rawModel string, success bool, errMsg string) {
	if success {
		r.markAvailable(ctx, credID, rawModel)
		slog.Debug("suspicious probe: model verified available by request",
			"credential_id", credID, "raw_model", rawModel)
	} else {
		r.markUnavailable(ctx, credID, rawModel, "request_failed", errMsg)
		slog.Debug("suspicious probe: model verified unavailable by request",
			"credential_id", credID, "raw_model", rawModel, "error", errMsg)
	}
}
