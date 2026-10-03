package dispatch

// lease_renewer.go — forwarder-side keepalive for TTL-bounded admission
// leases (R73 §3 #6, wired 2026-10-02 round 31).
//
// redis_enforce admits a request by storing a token in a Redis ZSET with a
// sliding-window expiry (redis_backend.go). Before this wiring a stream that
// ran past the lease TTL simply lost its slot while running on: the cluster
// limit stopped counting it and new admissions could overshoot the cap.
// The forwarder now starts one renewal goroutine per admitted attempt when
// the governor exposes the LeaseRenewer capability, and stops it inside the
// release path BEFORE the slot is freed (so a renewal can never race a
// Release on the same *QueuedRequest).
//
// Failure policy is fail-closed:
//   - renewal reports definitive loss (ErrLeaseLost: expired/evicted on the
//     shared store) → abort the stream immediately;
//   - renewal faults transiently (Redis unreachable) → keep retrying, but
//     once a full lease TTL has elapsed since the lease was last (re)armed
//     (Acquire or the last successful Renew) the lease has necessarily
//     expired server-side → abort. The deadline is measured from the last
//     arm, not from the first failure, so the stream never runs on an
//     expired lease.
//
// Aborting means cancelling the attempt's forward context: the upstream call
// unwinds, ForwardFunc returns, and the normal first-byte-aware completion
// path runs (post-first-byte failure is terminal per ADR-Disp-003 — no
// failover mid-stream, the client sees the drop).

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// startLeaseRenewer launches the renewal loop for one admitted attempt and
// returns the stop func. The stop func closes the loop's stop channel and
// blocks until the goroutine has exited, so releaseResources can order
// "stop renewing" strictly before "free the slot". gov implementing
// LeaseRenewer is the only criterion — governors without TTL leases never
// start a loop.
func (cf *credForwarder) startLeaseRenewer(fwdCtx context.Context, abort context.CancelFunc, gov Governor, qr *QueuedRequest) (stop func()) {
	renewer, ok := gov.(LeaseRenewer)
	if !ok {
		return nil
	}
	interval := renewer.RenewInterval()
	if interval <= 0 {
		interval = defaultRedisEnforceTTL / 3
	}
	stopCh := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		cf.leaseRenewLoop(fwdCtx, stopCh, renewer, qr, interval, abort)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			<-stopped
		})
	}
}

// leaseRenewLoop drives Renew every `interval` until the stop channel fires
// or the forward attempt ends. See the file header for the fail-closed
// policy.
func (cf *credForwarder) leaseRenewLoop(fwdCtx context.Context, stopCh <-chan struct{}, renewer LeaseRenewer, qr *QueuedRequest, interval time.Duration, abort context.CancelFunc) {
	ttl := 3 * interval // RenewInterval contract: TTL/3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// lastArmed 是租约最后一次确定被武装的时刻：进入循环前的 Acquire、
	// 之后每次成功的 Renew。fail-closed 截止时间必须从这里起算，而不是
	// 从首次失败起算：租约恰在 lastArmed+TTL 过期，取「首个 ≥ 该时刻的
	// 节拍」中止可保证流永远不会跑在已过期租约上——按首失败起算会让
	// 中止最晚滑到过期后一个节拍（默认 TTL/3 ≈ 10s），恰好是本接线要
	// 堵的未记账窗口（R31.1 批判复审修正）。
	lastArmed := cf.nowClock()
	for {
		select {
		case <-stopCh:
			return
		case <-fwdCtx.Done():
			return
		case <-ticker.C:
		}

		// Renew bounds itself with its own per-call timeout; no outer
		// deadline needed beyond the attempt context.
		err := renewer.Renew(fwdCtx, qr)
		switch {
		case err == nil:
			lastArmed = cf.nowClock()
		case errors.Is(err, ErrLeaseLost):
			metricGovernorLeaseLost.Inc()
			slog.Error("dispatch: governor lease lost; aborting stream fail-closed",
				"request_id", qr.ID,
				"credential_id", cf.cred.CredentialID,
				"error", err)
			abort()
			return
		default:
			// Transient fault: the lease may still be alive server-side.
			// Abort at the first tick at/after lastArmed+TTL — past that
			// point the lease has necessarily expired and the slot is
			// unaccounted.
			if cf.nowClock().Sub(lastArmed) >= ttl {
				metricGovernorLeaseLost.Inc()
				slog.Error("dispatch: governor lease renewal degraded past lease TTL; aborting stream fail-closed",
					"request_id", qr.ID,
					"credential_id", cf.cred.CredentialID,
					"since_last_armed_ms", time.Since(lastArmed).Milliseconds(),
					"last_error", err)
				abort()
				return
			}
			slog.Warn("dispatch: governor lease renewal failed (transient)",
				"request_id", qr.ID,
				"credential_id", cf.cred.CredentialID,
				"error", err)
		}
	}
}
