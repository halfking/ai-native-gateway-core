package preprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// SessionPreprocessHook is the production hook contract (spec R11.2,
// signature verbatim).  The module is loaded by the startup assembler and
// injected into the ChatHandler/executor seam; the existing direct path stays
// as the kill-switch fallback.
type SessionPreprocessHook interface {
	// Prepare loads or lazily builds raw -> sanitized -> compressed at the
	// current committed revision without advancing the persistent session
	// revision (R11.6 step 1: provisional revision only).
	Prepare(ctx context.Context, in SessionPrepareInput) (*PreparedSessionArtifacts, error)

	// CommitFirstForward CAS-commits the provisional revision as definitive
	// when this request really goes to the LLM for the first time
	// (beginUpstreamAttempt, AttemptNo == 1).  Retries / node switches reuse
	// the same Prepared bundle; repeated commits are idempotent.
	CommitFirstForward(ctx context.Context, p *PreparedSessionArtifacts, attemptNo int) error

	// AppendTerminal appends the assistant delta after terminal persistence,
	// advances the chain hash and marks Sanitized/Compressed stale.
	AppendTerminal(ctx context.Context, turn TerminalTurn) error
}

// SessionPrepareInput is everything Prepare needs.  Deps carries the explicit
// version/config strings feeding the dependency hashes (R11.5).
type SessionPrepareInput struct {
	TenantID  string
	SessionID string
	RequestID string
	AttemptID string
	Model     string
	Variant   string // compressed variant key; "" -> DefaultVariant

	// Delta is the canonicalized new user/system/tool delta of this request.
	Delta []byte
	// DeltaHash is optional; computed from Delta when zero.
	DeltaHash [32]byte

	Mutation MutationKind // "" -> MutationAppendDelta

	Deps ArtifactDependencies
}

// Validate normalizes and validates the input; returns the effective
// mutation kind and delta hash.
func (in *SessionPrepareInput) Validate() (MutationKind, [32]byte, error) {
	if in.TenantID == "" || in.SessionID == "" || in.RequestID == "" {
		return "", [32]byte{}, fmt.Errorf("preprocess: prepare input requires tenant/session/request")
	}
	if in.Delta == nil && isZeroHash(in.DeltaHash) {
		return "", [32]byte{}, fmt.Errorf("preprocess: prepare input requires a delta")
	}
	if in.Mutation == "" {
		in.Mutation = MutationAppendDelta
	}
	if !in.Mutation.Valid() {
		return "", [32]byte{}, fmt.Errorf("preprocess: unknown mutation kind %q", in.Mutation)
	}
	if isZeroHash(in.DeltaHash) {
		in.DeltaHash = HashBytes(in.Delta)
	}
	return in.Mutation, in.DeltaHash, nil
}

// PreparedSessionArtifacts is the reusable per-request bundle.  Retries and
// node switches MUST reuse the same bundle instead of re-sanitizing or
// re-compressing (R11.2 / UT-SA-08).
type PreparedSessionArtifacts struct {
	TenantID, SessionID string
	RequestID           string
	AttemptID           string

	// BaseRevision is the committed revision Prepare observed.
	BaseRevision SessionRevision
	// ProvisionalRevision is the revision this request proposes; it becomes
	// definitive only via CommitFirstForward.
	ProvisionalRevision SessionRevision
	// CommittedRevision is set together with Committed by CommitFirstForward.
	CommittedRevision SessionRevision
	Committed         bool

	Mutation  MutationKind
	Delta     []byte
	DeltaHash [32]byte

	DepHashes         DependencyHashes
	CompressedVariant string

	Raw        *SessionArtifact
	Sanitized  *SessionArtifact
	Compressed *SessionArtifact

	// Reused records which layers were cache hits (true) vs built (false).
	Reused map[ArtifactKind]bool
	// Receipts aggregates the transform receipts of every built layer.
	Receipts []TransformReceipt

	Manifest *ArtifactManifest
}

// TerminalTurn describes the terminal assistant append for AppendTerminal.
type TerminalTurn struct {
	TenantID, SessionID string
	RequestID           string
	AttemptID           string
	TurnNo              int32
	// AssistantDelta is the final assistant content appended to the source
	// log; the session revision advances by its hash.
	AssistantDelta []byte
	// AssistantDeltaHash is optional; computed from AssistantDelta when zero.
	AssistantDeltaHash [32]byte

	Status           ArtifactStatus // ready/failed/degraded/... terminal semantics
	UpstreamResponse BodyRefHash
	ClientResponse   BodyRefHash
}

// ArtifactBuilder produces the semantic layers.  The generation logic itself
// (sanitizer / compressor) is NOT part of this package: integrators inject an
// adapter that reuses security/sanitize and the compression SessionCompressor
// seam.  Thanks to the lease + singleflight in the hook, Build* runs at most
// once per (session, kind, dependency hash).
type ArtifactBuilder interface {
	BuildRaw(ctx context.Context, in *SessionPrepareInput, prev SessionRevision) (*BuiltArtifact, error)
	BuildSanitized(ctx context.Context, in *SessionPrepareInput, raw *SessionArtifact) (*BuiltArtifact, error)
	BuildCompressed(ctx context.Context, in *SessionPrepareInput, sanitized *SessionArtifact, variant string) (*BuiltArtifact, error)
}

// BuiltArtifact is the builder output for one layer.
type BuiltArtifact struct {
	Payload       []byte
	SourceHash    [32]byte // zero -> derived from content hash
	ContentHash   [32]byte // zero -> sha256(Payload)
	SchemaVersion uint16
	TransformVer  uint16
	Tokens        int32
	Degraded      bool
	FailureCode   uint16
	Receipts      []TransformReceipt
}

func (b *BuiltArtifact) toArtifact(tenantID, sessionID string, kind ArtifactKind, variant string, depHash [32]byte, rev SessionRevision, now time.Time) (*SessionArtifact, error) {
	if b == nil || b.Payload == nil {
		return nil, fmt.Errorf("preprocess: builder returned empty %s artifact", kind)
	}
	content := b.ContentHash
	if isZeroHash(content) {
		content = HashBytes(b.Payload)
	}
	source := b.SourceHash
	if isZeroHash(source) {
		source = content
	}
	failure := b.FailureCode
	if b.Degraded && failure == 0 {
		failure = 1
	}
	return &SessionArtifact{
		TenantID:       tenantID,
		SessionID:      sessionID,
		Kind:           kind,
		Variant:        variant,
		Revision:       rev,
		SourceHash:     source,
		DependencyHash: depHash,
		ContentHash:    content,
		SchemaVersion:  b.SchemaVersion,
		TransformVer:   b.TransformVer,
		GeneratedAt:    now.UnixMilli(),
		FailureCode:    failure,
		Tokens:         b.Tokens,
		Payload:        cloneBytes(b.Payload),
	}, nil
}

// ReuseCheck evaluates the four reuse conditions of R11.4.  Flags are only a
// fast path: every condition must hold.
//
//	ReadyBit == 1 && StaleBit == 0 && SourceRevision == current revision &&
//	DependencyHash == current dependency hash && ContentHash verifies
type ReuseCheck struct {
	Manifest   *ArtifactManifest
	Kind       ArtifactKind
	Variant    string
	Meta       ArtifactMeta // manifest meta of the layer (already resolved)
	CurrentRev SessionRevision
	DepHash    [32]byte
	Payload    []byte // optional; when non-nil the content hash is verified
}

// Reuse reasons (diagnosis only; no bodies, hashes truncated via HashPrefix).
const (
	ReuseOK             = ""
	ReuseNotReady       = "not_ready"
	ReuseStale          = "stale"
	ReuseStatusNotReady = "status_not_ready"
	ReuseRevisionMoved  = "revision_moved"
	ReuseDepHash        = "dependency_hash_mismatch"
	ReuseContentHash    = "content_hash_mismatch"
)

// Evaluate returns ("", true) when all four conditions hold.
func (c ReuseCheck) Evaluate() (string, bool) {
	if c.Manifest == nil {
		return ReuseNotReady, false
	}
	if !c.Manifest.Flags.Has(ReadyFlag(c.Kind)) {
		return ReuseNotReady, false
	}
	if c.Manifest.Flags.Has(StaleFlag(c.Kind)) {
		return ReuseStale, false
	}
	if c.Meta.Status != StatusReady {
		return ReuseStatusNotReady, false
	}
	if !c.Manifest.Revision.Equal(c.CurrentRev) {
		return ReuseRevisionMoved, false
	}
	if c.Meta.DependencyHash != c.DepHash {
		return ReuseDepHash, false
	}
	if c.Payload != nil && HashBytes(c.Payload) != c.Meta.ContentHash {
		return ReuseContentHash, false
	}
	return ReuseOK, true
}

// HookConfig configures the default hook.
type HookConfig struct {
	Store   SessionArtifactStore
	Builder ArtifactBuilder
	Events  EventSink     // optional; NoopEventSink when nil
	Blocks  *TurnBlockLog // optional; NewTurnBlockLog(0) when nil
	Now     func() time.Time

	// LeaseWaitTimeout bounds how long a lease waiter polls the manifest for
	// the winner's result (default 3s).
	LeaseWaitTimeout time.Duration
	// LeasePollInterval is the waiter's poll cadence (default 25ms).
	LeasePollInterval time.Duration
	// DefaultVariant is the compressed variant when input.Variant is empty.
	DefaultVariant string
}

const (
	defaultLeaseWaitTimeout = 3 * time.Second
	defaultLeasePoll        = 25 * time.Millisecond
	defaultVariantName      = "default"
	// maxBuildLoop bounds the acquire/CAS-conflict loop so it always
	// terminates even under pathological racing.
	maxBuildLoop = 8
)

type hook struct {
	cfg    HookConfig
	events EventSink
	blocks *TurnBlockLog
	now    func() time.Time
	sf     singleflight.Group
}

// NewHook returns the default SessionPreprocessHook implementation.
func NewHook(cfg HookConfig) (SessionPreprocessHook, error) {
	if cfg.Store == nil || cfg.Builder == nil {
		return nil, fmt.Errorf("preprocess: hook requires a store and a builder")
	}
	if cfg.Events == nil {
		cfg.Events = NoopEventSink{}
	}
	if cfg.Blocks == nil {
		cfg.Blocks = NewTurnBlockLog(0)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.LeaseWaitTimeout <= 0 {
		cfg.LeaseWaitTimeout = defaultLeaseWaitTimeout
	}
	if cfg.LeasePollInterval <= 0 {
		cfg.LeasePollInterval = defaultLeasePoll
	}
	if cfg.DefaultVariant == "" {
		cfg.DefaultVariant = defaultVariantName
	}
	return &hook{cfg: cfg, events: cfg.Events, blocks: cfg.Blocks, now: cfg.Now}, nil
}

var _ SessionPreprocessHook = (*hook)(nil)

func (h *hook) emit(ctx context.Context, kind EventKind, in *SessionPrepareInput, artifact ArtifactKind, variant string, depHash [32]byte, bytes, tokens int64) {
	h.events.EmitPreprocessEvent(ctx, Event{
		Kind:          kind,
		TenantID:      in.TenantID,
		SessionID:     in.SessionID,
		RequestID:     in.RequestID,
		Artifact:      artifact,
		Variant:       variant,
		DepHashPrefix: HashPrefix(depHash),
		Bytes:         bytes,
		Tokens:        tokens,
		AtUnixNano:    h.now().UnixNano(),
	})
}

func hitEvent(kind ArtifactKind) EventKind {
	switch kind {
	case ArtifactRaw:
		return EventRawHit
	case ArtifactSanitized:
		return EventSanitizeHit
	default:
		return EventCompressHit
	}
}

func buildEvent(kind ArtifactKind) EventKind {
	switch kind {
	case ArtifactRaw:
		return EventRawBuild
	case ArtifactSanitized:
		return EventSanitizeBuild
	default:
		return EventCompressBuild
	}
}

// Prepare implements SessionPreprocessHook.
func (h *hook) Prepare(ctx context.Context, in SessionPrepareInput) (*PreparedSessionArtifacts, error) {
	mutation, deltaHash, err := in.Validate()
	if err != nil {
		return nil, err
	}
	inp := in
	inp.DeltaHash = deltaHash
	variant := in.Variant
	if variant == "" {
		variant = h.cfg.DefaultVariant
	}

	manifest, err := h.cfg.Store.GetManifest(ctx, in.TenantID, in.SessionID)
	if err != nil {
		return nil, err
	}
	base := manifest.Revision

	p := &PreparedSessionArtifacts{
		TenantID:     in.TenantID,
		SessionID:    in.SessionID,
		RequestID:    in.RequestID,
		AttemptID:    in.AttemptID,
		BaseRevision: base,
		ProvisionalRevision: SessionRevision{
			TurnNo:        base.TurnNo + 1,
			HeadRequestID: in.RequestID,
			ChainHash:     AdvanceChainHash(base.ChainHash, deltaHash),
		},
		Mutation:          mutation,
		Delta:             cloneBytes(in.Delta),
		DeltaHash:         deltaHash,
		CompressedVariant: variant,
		Reused:            map[ArtifactKind]bool{},
	}

	// Layer 1: raw (R11.5: canonicalizer version + mutation kind + previous
	// chain hash + current delta hash).
	rawDeps := in.Deps.Raw
	if rawDeps.Mutation == "" {
		rawDeps.Mutation = mutation
	}
	rawDeps.PrevChainHash = base.ChainHash
	rawDeps.DeltaHash = deltaHash
	rawDepHash := rawDeps.Hash()
	p.DepHashes.Raw = rawDepHash

	rawArt, rawReceipts, reused, err := h.ensureArtifact(ctx, &inp, ArtifactRaw, "", base, rawDepHash, nil)
	if err != nil {
		return nil, err
	}
	p.Raw = rawArt
	p.Reused[ArtifactRaw] = reused
	p.Receipts = append(p.Receipts, rawReceipts...)

	// Layer 2: sanitized (raw source hash + sanitizer version + policy hash
	// + placeholder schema).
	sanDeps := in.Deps.Sanitized
	sanDeps.RawSourceHash = rawArt.SourceHash
	sanDepHash := sanDeps.Hash()
	p.DepHashes.Sanitized = sanDepHash

	sanArt, sanReceipts, reused, err := h.ensureArtifact(ctx, &inp, ArtifactSanitized, "", base, sanDepHash, rawArt)
	if err != nil {
		return nil, err
	}
	p.Sanitized = sanArt
	p.Reused[ArtifactSanitized] = reused
	p.Receipts = append(p.Receipts, sanReceipts...)

	// Layer 3: compressed variant (12 inputs of R11.5).
	compDeps := in.Deps.Compressed
	compDeps.SanitizedSourceHash = sanArt.SourceHash
	compDepHash := compDeps.Hash()
	p.DepHashes.Compressed = compDepHash

	if _, seen := manifest.CompressedVariants[variant]; !seen {
		h.emit(ctx, EventCompressVariantMiss, &inp, ArtifactCompressed, variant, compDepHash, 0, 0)
	}
	cmpArt, cmpReceipts, reused, err := h.ensureArtifact(ctx, &inp, ArtifactCompressed, variant, base, compDepHash, sanArt)
	if err != nil {
		return nil, err
	}
	p.Compressed = cmpArt
	p.Reused[ArtifactCompressed] = reused
	p.Receipts = append(p.Receipts, cmpReceipts...)

	// Latest manifest snapshot for the bundle (builds may have mutated it).
	finalManifest, err := h.cfg.Store.GetManifest(ctx, in.TenantID, in.SessionID)
	if err != nil {
		return nil, err
	}
	p.Manifest = finalManifest

	h.recordTurnBlock(&inp, p)
	return p, nil
}

// ensureArtifact checks the four reuse conditions; on miss it builds through
// lease + singleflight (in-process callers collapse; cross-process builders
// wait for the lease winner and then reuse its result — UT-SA-02).
func (h *hook) ensureArtifact(ctx context.Context, in *SessionPrepareInput, kind ArtifactKind, variant string,
	base SessionRevision, depHash [32]byte, upstream *SessionArtifact) (*SessionArtifact, []TransformReceipt, bool, error) {

	if artifact, ok, err := h.reusableArtifact(ctx, in, kind, variant, base, depHash); err != nil {
		return nil, nil, false, err
	} else if ok {
		return artifact, nil, true, nil
	}

	flightKey := in.TenantID + "|" + in.SessionID + "|" + string(kind) + "|" + HashPrefix(depHash)
	v, err, _ := h.sf.Do(flightKey, func() (interface{}, error) {
		return h.buildArtifact(ctx, in, kind, variant, base, depHash, upstream)
	})
	if err != nil {
		return nil, nil, false, err
	}
	out := v.(*buildOutcome)
	return out.art, out.receipts, false, nil
}

// buildOutcome carries the built artifact plus its transform receipts.
type buildOutcome struct {
	art      *SessionArtifact
	receipts []TransformReceipt
	degraded bool
}

// reusableArtifact loads the manifest meta and payload and evaluates
// ReuseCheck; a hit also emits the hit + bytes/tokens-saved events.
func (h *hook) reusableArtifact(ctx context.Context, in *SessionPrepareInput, kind ArtifactKind, variant string,
	base SessionRevision, depHash [32]byte) (*SessionArtifact, bool, error) {

	manifest, err := h.cfg.Store.GetManifest(ctx, in.TenantID, in.SessionID)
	if err != nil {
		return nil, false, err
	}
	meta := manifest.MetaFor(kind, variant)
	check := ReuseCheck{Manifest: manifest, Kind: kind, Variant: variant, Meta: meta, CurrentRev: base, DepHash: depHash}
	if _, ok := check.Evaluate(); !ok {
		return nil, false, nil
	}
	art, found, err := h.cfg.Store.Get(ctx, in.TenantID, in.SessionID, kind, variant)
	if err != nil {
		if errors.Is(err, ErrArtifactCorrupted) {
			return nil, false, nil // integrity failure => rebuild, don't fail the request
		}
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	check.Payload = art.Payload
	if _, ok := check.Evaluate(); !ok {
		return nil, false, nil
	}
	h.emit(ctx, hitEvent(kind), in, kind, variant, depHash, int64(len(art.Payload)), int64(art.Tokens))
	h.emit(ctx, EventArtifactSaved, in, kind, variant, depHash, int64(len(art.Payload)), int64(art.Tokens))
	return art, true, nil
}

// buildArtifact implements the lease/singleflight build loop.
func (h *hook) buildArtifact(ctx context.Context, in *SessionPrepareInput, kind ArtifactKind, variant string,
	base SessionRevision, depHash [32]byte, upstream *SessionArtifact) (*buildOutcome, error) {

	deadline := h.now().Add(h.cfg.LeaseWaitTimeout)
	waited := false
	for round := 0; round < maxBuildLoop; round++ {
		// Another builder may have finished while we waited.
		if art, ok, err := h.reusableArtifact(ctx, in, kind, variant, base, depHash); err != nil {
			return nil, err
		} else if ok {
			return &buildOutcome{art: art}, nil
		}

		lease, acquired, err := h.cfg.Store.AcquireBuildLease(ctx, in.TenantID, in.SessionID, kind, depHash)
		if err != nil {
			return nil, err
		}
		if acquired {
			built, err := h.callBuilder(ctx, in, kind, variant, upstream, base)
			if err != nil {
				_ = lease.Release(ctx)
				return nil, err
			}
			art, err := built.b.toArtifact(in.TenantID, in.SessionID, kind, variant, depHash, base, h.now())
			if err != nil {
				_ = lease.Release(ctx)
				return nil, err
			}
			ok, err := h.cfg.Store.PutCAS(ctx, art, base)
			if relErr := lease.Release(ctx); relErr != nil {
				_ = relErr // lease expires server-side anyway (PX)
			}
			if err != nil {
				return nil, err
			}
			if ok {
				// Emit build/degraded events only for the flight leader so N
				// concurrent Preparations produce exactly one build event
				// (IT-SS-04 alignment; singleflight waiters are silent).
				h.emit(ctx, buildEvent(kind), in, kind, variant, depHash, int64(len(art.Payload)), int64(art.Tokens))
				if kind == ArtifactSanitized && built.degraded {
					h.emit(ctx, EventSanitizeDegraded, in, kind, variant, depHash, 0, 0)
				}
				return &buildOutcome{art: art, receipts: built.b.Receipts, degraded: built.degraded}, nil
			}
			// Revision moved: reload/rebase, never overwrite (R11.6-5).
			h.emit(ctx, EventCASConflict, in, kind, variant, depHash, 1, 0)
			manifest, err := h.cfg.Store.GetManifest(ctx, in.TenantID, in.SessionID)
			if err != nil {
				return nil, err
			}
			if !manifest.Revision.Equal(base) && manifest.Revision.HeadRequestID != in.RequestID {
				return nil, fmt.Errorf("%w: cannot put %s artifact at %s", ErrRevisionConflict, kind, base)
			}
			continue
		}

		// Lease held elsewhere: wait for the winner, then reuse (no double
		// consumption — UT-SA-02).
		if !waited {
			h.emit(ctx, EventLeaseWait, in, kind, variant, depHash, 0, 0)
			waited = true
		}
		if !h.now().Before(deadline) {
			return nil, ErrBuildLeaseTimeout
		}
		sleepCtx(ctx, h.cfg.LeasePollInterval)
	}
	return nil, ErrBuildLeaseTimeout
}

type builderResult struct {
	b        *BuiltArtifact
	degraded bool
}

func (h *hook) callBuilder(ctx context.Context, in *SessionPrepareInput, kind ArtifactKind, variant string, upstream *SessionArtifact, base SessionRevision) (*builderResult, error) {
	var built *BuiltArtifact
	var err error
	switch kind {
	case ArtifactRaw:
		built, err = h.cfg.Builder.BuildRaw(ctx, in, base)
	case ArtifactSanitized:
		built, err = h.cfg.Builder.BuildSanitized(ctx, in, upstream)
	case ArtifactCompressed:
		built, err = h.cfg.Builder.BuildCompressed(ctx, in, upstream, variant)
	default:
		return nil, fmt.Errorf("preprocess: unknown kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	return &builderResult{b: built, degraded: built.Degraded}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		d = time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// CommitFirstForward implements SessionPreprocessHook (R11.6 step 3): only
// the first real forward (AttemptNo == 1) commits the provisional revision;
// anything that never reaches the upstream (dedupe hit, pre-reject, early
// client cancel) simply never calls this, leaving the revision untouched.
func (h *hook) CommitFirstForward(ctx context.Context, p *PreparedSessionArtifacts, attemptNo int) error {
	if p == nil {
		return fmt.Errorf("preprocess: nil prepared bundle")
	}
	if attemptNo != 1 {
		return nil // only the first real forward commits
	}
	if p.Committed {
		return nil // idempotent re-commit
	}
	mutation := SessionMutation{
		TenantID:       p.TenantID,
		SessionID:      p.SessionID,
		RequestID:      p.RequestID,
		TurnNo:         p.ProvisionalRevision.TurnNo,
		Mutation:       p.Mutation,
		DeltaHash:      p.DeltaHash,
		Delta:          cloneBytes(p.Delta),
		DependencyHash: p.DepHashes.Raw,
	}
	rev, err := h.cfg.Store.AppendRawTurn(ctx, mutation, p.BaseRevision)
	if err != nil {
		if !errors.Is(err, ErrRevisionConflict) {
			return err
		}
		// CAS conflict: reload; rebase on top of the newer revision, never
		// overwrite it (R11.6 step 5, UT-SA-06).
		manifest, rerr := h.cfg.Store.GetManifest(ctx, p.TenantID, p.SessionID)
		if rerr != nil {
			return rerr
		}
		if manifest.Revision.HeadRequestID == p.RequestID {
			// idempotent replay of our own commit
			p.Committed = true
			p.CommittedRevision = manifest.Revision
			return nil
		}
		mutation.TurnNo = manifest.Revision.TurnNo + 1
		rev, err = h.cfg.Store.AppendRawTurn(ctx, mutation, manifest.Revision)
		if err != nil {
			return err
		}
	}
	p.Committed = true
	p.CommittedRevision = rev
	return nil
}

// terminalMarkerStore is the optional exactly-once capability used by
// AppendTerminal replay deduplication (SET NX PX marker per request).
type terminalMarkerStore interface {
	TryMarkTerminalAppend(ctx context.Context, tenantID, sessionID, requestID string, ttl time.Duration) (bool, error)
}

// AppendTerminal implements SessionPreprocessHook (R11.6 step 4).
func (h *hook) AppendTerminal(ctx context.Context, turn TerminalTurn) error {
	if turn.TenantID == "" || turn.SessionID == "" || turn.RequestID == "" {
		return fmt.Errorf("preprocess: terminal turn requires tenant/session/request")
	}
	if len(turn.AssistantDelta) == 0 && isZeroHash(turn.AssistantDeltaHash) {
		return fmt.Errorf("preprocess: terminal turn requires an assistant delta")
	}
	deltaHash := turn.AssistantDeltaHash
	if isZeroHash(deltaHash) {
		deltaHash = HashBytes(turn.AssistantDelta)
	}

	// Exactly-once per request: a replayed terminal append (client resend /
	// cross-node replay) must not append a second assistant turn.  The head
	// request id alone cannot detect it because commit and terminal share it.
	if ts, ok := h.cfg.Store.(terminalMarkerStore); ok {
		first, err := ts.TryMarkTerminalAppend(ctx, turn.TenantID, turn.SessionID, turn.RequestID, 0)
		if err != nil {
			return err
		}
		if !first {
			return nil // idempotent replay
		}
	}

	manifest, err := h.cfg.Store.GetManifest(ctx, turn.TenantID, turn.SessionID)
	if err != nil {
		return err
	}
	base := manifest.Revision
	mutation := SessionMutation{
		TenantID:  turn.TenantID,
		SessionID: turn.SessionID,
		RequestID: turn.RequestID,
		TurnNo:    base.TurnNo + 1,
		Mutation:  MutationAppendDelta,
		DeltaHash: deltaHash,
		Delta:     cloneBytes(turn.AssistantDelta),
	}
	if _, err := h.cfg.Store.AppendRawTurn(ctx, mutation, base); err != nil {
		if !errors.Is(err, ErrRevisionConflict) {
			return err
		}
		manifest, rerr := h.cfg.Store.GetManifest(ctx, turn.TenantID, turn.SessionID)
		if rerr != nil {
			return rerr
		}
		if manifest.Revision.HeadRequestID != turn.RequestID {
			mutation.TurnNo = manifest.Revision.TurnNo + 1
			if _, err := h.cfg.Store.AppendRawTurn(ctx, mutation, manifest.Revision); err != nil {
				return err
			}
		}
		// else: idempotent replay of our own terminal append
	}

	// Downstream stale (AppendRawTurn already flags sanitized/compressed;
	// the explicit call keeps the dependency-graph contract observable).
	if err := h.cfg.Store.InvalidateFrom(ctx, turn.TenantID, turn.SessionID, ArtifactSanitized); err != nil {
		return err
	}

	status := turn.Status
	if status == StatusMissing || status == StatusBuilding {
		status = StatusReady
	}
	// The block log is a projection: completing a block is best effort (a
	// terminal append without a prior Prepare — e.g. replay on another
	// node — simply has no in-process block to complete).
	_ = h.blocks.CompleteBlock(turn.TenantID, turn.SessionID, turn.RequestID, status,
		turn.UpstreamResponse, turn.ClientResponse, nil)
	return nil
}

// recordTurnBlock registers the request-side block after a successful
// Prepare.  Bodies are referenced (never copied); retries append attempts via
// TurnBlockLog.RecordAttempt only.
func (h *hook) recordTurnBlock(in *SessionPrepareInput, p *PreparedSessionArtifacts) {
	block := TurnArtifactBlock{
		TenantID:       in.TenantID,
		SessionID:      in.SessionID,
		TurnNo:         p.ProvisionalRevision.TurnNo,
		RequestID:      in.RequestID,
		AttemptID:      in.AttemptID,
		SourceRevision: p.BaseRevision,
		Mutation:       p.Mutation,
		Status:         StatusBuilding,
		CreatedAt:      h.now(),
	}
	if p.Raw != nil {
		block.RawRequestRef = BodyRefHash{
			Ref:      ArtifactRef(in.TenantID, in.SessionID, ArtifactRaw, ""),
			Hash:     p.Raw.ContentHash,
			Bytes:    int32(len(p.Raw.Payload)),
			Tokens:   p.Raw.Tokens,
			Complete: true,
		}
	}
	if p.Sanitized != nil {
		block.SanitizedRequestRef = BodyRefHash{
			Ref:      ArtifactRef(in.TenantID, in.SessionID, ArtifactSanitized, ""),
			Hash:     p.Sanitized.ContentHash,
			Bytes:    int32(len(p.Sanitized.Payload)),
			Tokens:   p.Sanitized.Tokens,
			Complete: true,
		}
	}
	if p.Compressed != nil {
		block.CompressedRequestRef = BodyRefHash{
			Ref:      ArtifactRef(in.TenantID, in.SessionID, ArtifactCompressed, p.CompressedVariant),
			Hash:     p.Compressed.ContentHash,
			Bytes:    int32(len(p.Compressed.Payload)),
			Tokens:   p.Compressed.Tokens,
			Complete: true,
		}
	}
	_ = h.blocks.AppendBlock(block) // best effort; body refs stay authoritative in the store
}
