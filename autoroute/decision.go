package autoroute

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Decision is the top-level output of Decider.Decide. Consumed by
// relay/handler.go to:
//   1. Substitute the model field with ChosenModel
//   2. Add X-Gw-Auto-Decision header
//   3. Persist auto_decision JSONB to request_logs
type Decision struct {
	// ChosenModel is the canonical_name of the winning credential's model.
	// The relay substitutes this into the request body's "model" field
	// before forwarding upstream.
	ChosenModel string

	// ChosenCredentialID identifies the credential that will be used.
	ChosenCredentialID int64

	// ChosenRawModel is the upstream-visible name (may differ from
	// ChosenModel when canonical maps to multiple raw_model variants).
	ChosenRawModel string

	// TaskType is the classified task type (primary).
	TaskType TaskType

	// Confidence is the classification confidence (0-1).
	Confidence float64

	// Profile is the profile actually used (header → sticky → default).
	Profile Profile

	// Classifier records which path produced the classification
	// ("heuristic", "llm", "default").
	Classifier string

	// Reason is the human-readable explanation (shown in admin UI).
	Reason string

	// CandidatesTopN is the top-N candidates sorted by descending score.
	// The first element is the winner. Used for audit and admin UI.
	CandidatesTopN []ScoredCandidate

	// DecidedAt is the wall-clock time when the decision was made.
	// Used for observability latency tracking.
	DecidedAt time.Time
}

// IndexAccessor is the minimal interface Decider needs from autoroute.Index.
// Defined as an interface so tests can inject stubs without standing up
// the full PG pool + 5-min refresh machinery.
type IndexAccessor interface {
	Recommend(task TaskType, sigs ClassificationSignals, profile Profile, topN int) []ScoredCandidate
	Snapshot() []Candidate
	LastRefresh() time.Time
}

// Decider orchestrates the auto-route pipeline:
//
//   1. Resolve profile (header > sticky > default)
//   2. Classify (heuristic, with LLM fallback if confidence < threshold)
//   3. Score candidates (via index)
//   4. Return Decision
//
// All inputs are read-only after construction (except the index, which
// is refreshed by bg/auto_index_refresher.go).
type Decider struct {
	classifier Classifier   // heuristic
	fallback   Classifier   // optional LLM
	index      IndexAccessor // candidate pool
	profileStore ProfileStore // per-API-Key sticky profile
	intentCache  *SessionIntentCache // per-session intent cache (v2.0.4)
	tuningStore    *TuningStore    // optional dynamic params (v2.1)
	overrideStore  *OverrideStore  // optional admin ban/pin overrides (P7.6)

	// DefaultProfile is used when no header AND no sticky entry exists.
	// Default: ProfileSmart.
	DefaultProfile Profile

	// LLMConfidenceThreshold: heuristic results below this trigger LLM
	// fallback. Default: 0.7.
	LLMConfidenceThreshold float64

	// StickyTTL controls how long a sticky profile persists for an API
	// key without a header. Default: 30 minutes.
	StickyTTL time.Duration

	// IntentCacheTTL controls how long a session intent is cached.
	// Default: 10 minutes.
	IntentCacheTTL time.Duration

	// TopN is the size of CandidatesTopN stored in auto_decision.
	// Default: 3.
	TopN int
}

// NewDecider wires the pieces. classifier is required; fallback and
// profileStore are optional (pass nil to disable).
//
// Defaults applied:
//   - DefaultProfile            : ProfileSmart
//   - LLMConfidenceThreshold    : 0.7
//   - StickyTTL                 : 30 minutes
//   - IntentCacheTTL            : 10 minutes (auto-creates SessionIntentCache)
//   - TopN                      : 3
func NewDecider(classifier Classifier, fallback Classifier, index IndexAccessor, profileStore ProfileStore) *Decider {
	return &Decider{
		classifier:             classifier,
		fallback:               fallback,
		index:                  index,
		profileStore:           profileStore,
		intentCache:            NewSessionIntentCache(10 * time.Minute),
		DefaultProfile:         ProfileSmart,
		LLMConfidenceThreshold: 0.7,
		StickyTTL:              30 * time.Minute,
		IntentCacheTTL:         10 * time.Minute,
		TopN:                   3,
	}
}

// SetIntentCache overrides the default session intent cache. Pass nil
// to disable session-level caching entirely (every request reclassifies).
func (d *Decider) SetIntentCache(c *SessionIntentCache) {
	d.intentCache = c
}

// SetTuningStore wires a dynamic parameter store. When set, the Decider
// reads the LLM-fallback threshold from the store on every classify call
// (atomic pointer load, zero overhead). Pass nil to use the static
// LLMConfidenceThreshold field.
func (d *Decider) SetTuningStore(ts *TuningStore) {
	d.tuningStore = ts
}

// SetOverrideStore wires admin-authored routing overrides (ban/pin).
func (d *Decider) SetOverrideStore(store *OverrideStore) {
	d.overrideStore = store
}

// effectiveLLMThreshold returns the dynamic threshold from the tuning
// store, or the static field when no store is wired.
func (d *Decider) effectiveLLMThreshold() float64 {
	if d.tuningStore != nil {
		return d.tuningStore.LLMConfidenceThreshold()
	}
	return d.LLMConfidenceThreshold
}

// Decide runs the full pipeline. Returns the Decision (always non-nil on
// success). Errors only when ALL paths fail — in that case the caller
// (relay handler) should fall back to the gateway default model.
//
// Parameters:
//
//   ctx              : request context (used for timeout propagation)
//   sigs             : request fingerprint
//   apiKeyID         : the resolved API key ID (0 for unauthenticated)
//   headerProfile    : X-Gw-Auto-Profile header value (empty = no override)
//   taskHint         : X-Gw-Task-Hint header value (optional client hint)
//   sessionID        : X-Gw-Session-Id (empty = no session, always reclassify)
//
// Side effects:
//
//   - Updates sticky profile for apiKeyID (best-effort)
//   - Caches intent for sessionID (10min TTL, best-effort)
//   - Returns Decision including the chosen model + top-N candidates
func (d *Decider) Decide(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	// Step 0: check session intent cache (skip if no sessionID or cache disabled)
	if sessionID != "" && d.intentCache != nil {
		if cached, ok := d.intentCache.Get(sessionID); ok {
			if !shouldReclassify(cached.TaskType, sigs) {
				return &Decision{
					ChosenModel:        cached.ChosenModel,
					ChosenCredentialID: cached.CredentialID,
					ChosenRawModel:     cached.ChosenModel,
					TaskType:           cached.TaskType,
					Confidence:         cached.Confidence,
					Profile:            cached.Profile,
					Classifier:         "session_cache",
					Reason:             "reused session intent (within " + d.IntentCacheTTL.String() + " TTL)",
					DecidedAt:          time.Now(),
				}, nil
			}
		}
	}

	// Step 1: resolve profile (header > sticky > default)
	profile := d.resolveProfile(ctx, apiKeyID, headerProfile)

	// Step 2: classify
	cls, err := d.classify(ctx, sigs, taskHint)
	if err != nil {
		// Both heuristic AND LLM fallback failed — use default chat
		cls = &Classification{
			Primary:    TaskChat,
			Confidence: 0.3,
			Classifier: "default",
			Reason:     "classification failed: " + err.Error(),
		}
	}

	// Step 3: score candidates
	recommended := d.index.Recommend(cls.Primary, sigs, profile, d.TopN)
	if d.overrideStore != nil {
		task := string(cls.Primary)
		prof := string(profile)
		filtered := d.overrideStore.FilterBanned(recommended, task, prof)
		recommended = d.overrideStore.PromotePins(filtered, task, prof)
	}

	// If no candidates matched, return a "no decision" sentinel.
	if len(recommended) == 0 {
		return nil, errors.New("autoroute: no candidates match task type " + string(cls.Primary))
	}

	winner := recommended[0]

	decision := &Decision{
		ChosenModel:        winner.Candidate.CanonicalName,
		ChosenCredentialID: winner.Candidate.CredentialID,
		ChosenRawModel:     winner.Candidate.RawModel,
		TaskType:           cls.Primary,
		Confidence:         cls.Confidence,
		Profile:            profile,
		Classifier:         cls.Classifier,
		Reason:             cls.Reason,
		CandidatesTopN:     recommended,
		DecidedAt:          time.Now(),
	}

	// Step 4: cache the intent for this session
	if sessionID != "" && d.intentCache != nil {
		d.intentCache.Put(sessionID, CachedIntent{
			TaskType:     decision.TaskType,
			ChosenModel:  decision.ChosenModel,
			CredentialID: decision.ChosenCredentialID,
			Profile:      decision.Profile,
			Confidence:   decision.Confidence,
			Classifier:   decision.Classifier,
		})
	}

	return decision, nil
}

// resolveProfile applies the profile precedence:
//
//   1. X-Gw-Auto-Profile header (if valid)
//   2. Sticky entry for apiKeyID (if not expired)
//   3. DefaultProfile (ProfileSmart by default)
//
// Side effect: if the header overrides a stale sticky entry, persist the
// new value via ProfileStore.Put (best-effort, error swallowed).
func (d *Decider) resolveProfile(ctx context.Context, apiKeyID int, header string) Profile {
	// (1) Header override
	if h := normaliseProfile(header); h != "" {
		// v2.0.3 audit fix #18: skip sticky write for api_key_id=0
		// (unauthenticated requests, e.g. ad-hoc curl). Avoids polluting
		// the sticky table with junk entries.
		if apiKeyID > 0 && d.profileStore != nil {
			if err := d.profileStore.Put(ctx, apiKeyID, h, d.StickyTTL); err != nil {
				slog.Warn("autoroute: sticky profile write failed",
					"error", err, "api_key_id", apiKeyID, "profile", h)
			}
		} else if h != "" && apiKeyID == 0 {
			slog.Debug("autoroute: header profile set but api_key_id=0, sticky not persisted",
				"profile", h)
		}
		return h
	}
	// (2) Sticky entry
	if apiKeyID > 0 && d.profileStore != nil {
		if p, ok := d.profileStore.Get(ctx, apiKeyID); ok {
			return p
		}
	}
	// (3) Default
	return d.DefaultProfile
}

// normaliseProfile trims and lowercases the header value, validating
// it against AllProfiles. Returns "" if invalid.
func normaliseProfile(raw string) Profile {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	for _, p := range AllProfiles {
		if string(p) == s {
			return p
		}
	}
	return ""
}

// classify runs the heuristic, then escalates to LLM fallback if needed.
//
// Task hint precedence:
//
//   - If taskHint is provided (X-Gw-Task-Hint header) and is a valid
//     TaskType, skip heuristic entirely (high trust in client hint).
//   - Otherwise run heuristic; if confidence < threshold and LLM
//     fallback is configured, escalate.
//
// Errors:
//   - Task hint invalid → ignored, falls through to heuristic
//   - Heuristic error → escalate to LLM
//   - LLM error      → return error (caller falls back to chat default)
func (d *Decider) classify(ctx context.Context, sigs ClassificationSignals, hint TaskType) (*Classification, error) {
	if isValidTaskType(hint) {
		return &Classification{
			Primary:    hint,
			Confidence: 0.9, // trusted client hint
			Classifier: "heuristic",
			Reason:     "client-provided task hint",
		}, nil
	}
	if d.classifier == nil {
		return nil, fmt.Errorf("no classifier configured")
	}
	cls, err := d.classifier.Classify(ctx, sigs)
	if err != nil {
		return nil, fmt.Errorf("heuristic: %w", err)
	}
	if cls.Confidence >= d.effectiveLLMThreshold() {
		return cls, nil
	}
	if d.fallback == nil {
		slog.Debug("autoroute: low confidence, no LLM fallback",
			"confidence", cls.Confidence,
			"task", cls.Primary,
		)
		return cls, nil
	}
	slog.Info("autoroute: escalating to LLM fallback",
		"heuristic_confidence", cls.Confidence,
		"heuristic_task", cls.Primary,
	)
	llmCls, llmErr := d.fallback.Classify(ctx, sigs)
	if llmErr != nil {
		// LLM fallback failed → return heuristic result at low confidence
		slog.Warn("autoroute: LLM fallback failed", "error", llmErr)
		return cls, nil
	}
	return llmCls, nil
}

func isValidTaskType(t TaskType) bool {
	for _, v := range AllTaskTypes {
		if v == t {
			return true
		}
	}
	return false
}

// ProfileStore is the persistence interface used by Decider for sticky
// profile memory. Implemented by a DB-backed store (PostgreSQL +
// in-memory cache) wired in cmd/gateway/main.go.
//
// Get returns (profile, true) when a non-expired entry exists for the
// api key. (profile, false) means no sticky state — caller should fall
// back to DefaultProfile.
//
// Put is best-effort — errors are logged but do not block the request.
type ProfileStore interface {
	Get(ctx context.Context, apiKeyID int) (Profile, bool)
	Put(ctx context.Context, apiKeyID int, p Profile, ttl time.Duration) error
}


// DBProfileStore is the production-grade, multi-instance-safe
// implementation of ProfileStore. Sticky state is persisted to the
// api_key_auto_profile table (added in v2.0.0 SQL migration).
//
// A small in-process cache (1-minute TTL) absorbs the hot path so we
// do not hammer PG on every request. After 1 min, the cache is
// considered stale and we re-read from DB.
//
// v2.0.3 audit fix #14: replaced MemoryProfileStore (single-process)
// for multi-instance deployments. MemoryProfileStore is still
// available for tests.
type DBProfileStore struct {
	pool *pgxpool.Pool

	// Cache with 1-minute TTL (so sticky preference survives at most
	// 1 minute across the cluster). For stronger consistency, set
	// cacheTTL=0 (always read from DB).
	cacheTTL time.Duration

	mu   sync.RWMutex
	rows map[int]cachedProfile
}

type cachedProfile struct {
	profile   Profile
	expiresAt time.Time
}

// NewDBProfileStore wires the DB-backed store. pool is required.
// cacheTTL is the local cache window; default 1 minute.
func NewDBProfileStore(pool *pgxpool.Pool) *DBProfileStore {
	return &DBProfileStore{
		pool:     pool,
		cacheTTL: 1 * time.Minute,
		rows:     make(map[int]cachedProfile),
	}
}

// Get implements ProfileStore.
//
// Order of operations:
//   1. Check in-process cache (RLock). If fresh, return.
//   2. If miss or stale, query api_key_auto_profile.
//   3. If found, update cache and return.
//   4. If not found, return "" + false (caller falls back to default).
func (s *DBProfileStore) Get(ctx context.Context, apiKeyID int) (Profile, bool) {
	if apiKeyID <= 0 {
		return "", false
	}

	// 1. Cache check
	s.mu.RLock()
	e, ok := s.rows[apiKeyID]
	s.mu.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		return e.profile, true
	}

	// 2. DB read
	var profile string
	err := s.pool.QueryRow(ctx, `
		SELECT profile FROM api_key_auto_profile
		WHERE api_key_id = $1
	`, apiKeyID).Scan(&profile)
	if err != nil {
		// No row or DB error. We log the error but return false so
		// the caller falls back to default. Caching an empty profile
		// would mask DB outages.
		if !errors.Is(err, pgxNoRows()) {
			slog.Warn("DBProfileStore.Get query failed", "error", err, "api_key_id", apiKeyID)
		}
		return "", false
	}

	p := Profile(profile)
	s.setCache(apiKeyID, p)
	return p, true
}

// Put implements ProfileStore. Upserts the row and updates the local
// cache so subsequent reads in this process are immediate.
func (s *DBProfileStore) Put(ctx context.Context, apiKeyID int, p Profile, ttl time.Duration) error {
	if apiKeyID <= 0 {
		return fmt.Errorf("api_key_id must be > 0")
	}
	if p != ProfileSmart && p != ProfileSpeedFirst && p != ProfileCostFirst {
		return fmt.Errorf("invalid profile: %q", p)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO api_key_auto_profile (api_key_id, profile, first_chosen_at, last_used_at, updated_at)
		VALUES ($1, $2, NOW(), NOW(), NOW())
		ON CONFLICT (api_key_id) DO UPDATE SET
		    profile = EXCLUDED.profile,
		    last_used_at = NOW(),
		    updated_at = NOW()
	`, apiKeyID, p)
	if err != nil {
		return fmt.Errorf("upsert sticky profile: %w", err)
	}
	s.setCache(apiKeyID, p)
	return nil
}

func (s *DBProfileStore) setCache(apiKeyID int, p Profile) {
	s.mu.Lock()
	s.rows[apiKeyID] = cachedProfile{
		profile:   p,
		expiresAt: time.Now().Add(s.cacheTTL),
	}
	s.mu.Unlock()
}

// pgxNoRows returns the sentinel error for "no rows" from pgx so we
// can detect the not-found case without an import cycle.
func pgxNoRows() error {
	return errPgxNoRows
}

var errPgxNoRows = errors.New("no rows in result set")

// MemoryProfileStore is a process-local implementation of ProfileStore.
// Suitable for single-instance deployments and tests. For multi-instance
// deployments, swap in a DB-backed implementation that reads from
// api_key_auto_profile (see docs/2026-06-15-auto-route-mode.sql).
//
// Concurrency: protected by sync.RWMutex. Entries are removed lazily on
// read (no background cleanup goroutine — saves 1 goroutine per process).
type MemoryProfileStore struct {
	entries map[int]memoryProfileEntry
	mu      sync.RWMutex
	now     func() time.Time // injectable for tests
}

type memoryProfileEntry struct {
	profile   Profile
	expiresAt time.Time
}

// NewMemoryProfileStore constructs an empty in-memory store.
func NewMemoryProfileStore() *MemoryProfileStore {
	return &MemoryProfileStore{
		entries: make(map[int]memoryProfileEntry),
		now:     time.Now,
	}
}

// Get implements ProfileStore.
func (s *MemoryProfileStore) Get(_ context.Context, apiKeyID int) (Profile, bool) {
	s.mu.RLock()
	e, ok := s.entries[apiKeyID]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if s.now().After(e.expiresAt) {
		// Expired — lazy delete
		s.mu.Lock()
		delete(s.entries, apiKeyID)
		s.mu.Unlock()
		return "", false
	}
	return e.profile, true
}

// Put implements ProfileStore.
func (s *MemoryProfileStore) Put(_ context.Context, apiKeyID int, p Profile, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[apiKeyID] = memoryProfileEntry{
		profile:   p,
		expiresAt: s.now().Add(ttl),
	}
	return nil
}