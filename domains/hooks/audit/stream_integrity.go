package audit

// StreamTextObserver is the optional incremental observer a
// StreamCapture notifies as assistant text accumulates. It exists so
// the integrity detector can spot a looping model mid-stream instead of
// only after the stream completes, without the audit package importing
// domains/streaming/integrity (the dependency graph stays
// integrity → audit, never the reverse).
//
// Implementations MUST be safe for concurrent use and MUST NOT call
// back into the StreamCapture that owns them: ObserveText is invoked
// while sc.mu is held, so a re-entrant call would deadlock.
type StreamTextObserver interface {
	// ObserveText consumes the next slice of assistant text exactly as
	// it was appended to textContent. It returns true when the observed
	// content breaches an integrity rule and the caller should abort the
	// stream. Returning true more than once per stream is allowed; the
	// capture latches the first breach.
	ObserveText(s string) bool
	// BreachReason names the rule that tripped, e.g.
	// "integrity_repeated_content". Used as the capture's interruption
	// reason so request_logs keeps a specific cause.
	BreachReason() string
	// RepeatedContentHash returns the block hash and hit count when
	// repeated content was detected incrementally, so the final
	// detection pass can record the event without rescanning (and
	// without double-recording). ok is false when no repeat was seen.
	RepeatedContentHash() (hash string, hits int, blockSize int, blocksTotal int, ok bool)
	// Reset clears accumulated state so the observer can be reused for a
	// fresh attempt after a mid-request credential failover.
	Reset()
}

// SetTextObserver attaches an incremental text observer. Safe to call
// with nil (clears the observer). Must be called before the stream
// starts producing chunks; attaching mid-stream only sees text appended
// from that point on.
func (sc *StreamCapture) SetTextObserver(o StreamTextObserver) {
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.textObserver = o
}

// IntegrityBreached reports whether the attached observer flagged an
// integrity breach during this stream. Transformers poll it right after
// ObserveChunk / ObservePayload to decide whether to cut the stream.
func (sc *StreamCapture) IntegrityBreached() bool {
	if sc == nil {
		return false
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.integrityBreached
}

// IntegrityBreachReason returns the rule name that tripped, or "" when
// no breach was flagged.
func (sc *StreamCapture) IntegrityBreachReason() string {
	if sc == nil {
		return ""
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.integrityBreachReason
}

// IncrementalRepeatedContent surfaces the observer's repeated-content
// finding to the completion path so the integrity detector can record
// one event per request without rescanning textContent. ok is false when
// nothing was detected incrementally.
func (sc *StreamCapture) IncrementalRepeatedContent() (hash string, hits int, blockSize int, blocksTotal int, ok bool) {
	if sc == nil {
		return "", 0, 0, 0, false
	}
	sc.mu.Lock()
	obs := sc.textObserver
	sc.mu.Unlock()
	if obs == nil {
		return "", 0, 0, 0, false
	}
	// Called outside sc.mu: the observer is independently locked and
	// holding sc.mu here would violate the same no-reentrance rule the
	// observer contract imposes on itself.
	return obs.RepeatedContentHash()
}

// notifyTextObserver forwards freshly appended text to the observer and
// latches a breach. Assumes sc.mu is held by the caller (appendText).
func (sc *StreamCapture) notifyTextObserver(s string) {
	if sc.textObserver == nil || s == "" {
		return
	}
	if !sc.textObserver.ObserveText(s) {
		return
	}
	if sc.integrityBreached {
		return
	}
	sc.integrityBreached = true
	sc.integrityBreachReason = sc.textObserver.BreachReason()
	if sc.integrityBreachReason == "" {
		sc.integrityBreachReason = "integrity_breach"
	}
}
