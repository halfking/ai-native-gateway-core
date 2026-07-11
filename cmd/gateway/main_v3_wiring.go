// Command gateway - main_v3_wiring.go (2026-06-19)
//
// Helpers to wire the v3 session-level intelligent compressor into the chat
// handler at startup. Lives in a separate file from main.go to keep the
// main entry-point readable.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/memory"                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/session"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/provider"
)

// compressorSessionDisabled returns true when the v3 session compressor is
// turned off via env. Default = enabled (when Redis + DB are both available).
func compressorSessionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// ──────────────────────────────────────────────────────────────────────────────
// Redis backend adapter (session.RedisClient → compression.SessionCacheBackend)
// ──────────────────────────────────────────────────────────────────────────────

type redisBackendAdapter struct {
	c *session.RedisClient
}

func redisBackendFromClient(c *session.RedisClient) compression.SessionCacheBackend {
	if c == nil {
		return nil
	}
	return &redisBackendAdapter{c: c}
}

func (a *redisBackendAdapter) HSet(ctx context.Context, key string, values ...any) error {
	fields := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		k, ok := values[i].(string)
		if !ok {
			continue
		}
		fields[k] = values[i+1]
	}
	return a.c.HSet(ctx, key, fields)
}

func (a *redisBackendAdapter) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key)
}

func (a *redisBackendAdapter) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl)
}

func (a *redisBackendAdapter) Del(ctx context.Context, key string) error {
	return a.c.Del(ctx, key)
}

// ──────────────────────────────────────────────────────────────────────────────
// Postgres backend adapter (*db.DB → compression.SessionCacheDB)
// ──────────────────────────────────────────────────────────────────────────────

type pgBackendAdapter struct {
	dbConn *db.DB
}

func dbBackendFromPool(dbConn *db.DB) compression.SessionCacheDB {
	if dbConn == nil || !dbConn.Enabled() {
		return nil
	}
	return &pgBackendAdapter{dbConn: dbConn}
}

// LastOutboundForSession runs the L3 cold-start fallback query.
func (a *pgBackendAdapter) LastOutboundForSession(ctx context.Context, tenantID, gwSessionID string) (*compression.LastOutboundRow, error) {
	if a.dbConn == nil {
		return nil, fmt.Errorf("db pool not configured")
	}
	pool := a.dbConn.Pool()
	if pool == nil {
		return nil, fmt.Errorf("db pool is nil")
	}
	row := pool.QueryRow(ctx, `
		SELECT outbound_body, outbound_msg_count, outbound_token_est,
		       outbound_msg_hashes, compression_meta
		FROM request_logs
		WHERE tenant_id = $1 AND gw_session_id = $2 AND outbound_body IS NOT NULL
		ORDER BY ts DESC
		LIMIT 1
	`, tenantID, gwSessionID)
	return scanLastOutboundRow(row)
}

func scanLastOutboundRow(row interface{ Scan(dest ...any) error }) (*compression.LastOutboundRow, error) {
	var (
		ob     []byte
		mc     *int
		te     *int
		hashes []byte
		meta   []byte
	)
	if err := row.Scan(&ob, &mc, &te, &hashes, &meta); err != nil {
		return nil, err
	}
	out := &compression.LastOutboundRow{}
	if ob != nil {
		out.OutboundBody = json.RawMessage(ob)
	}
	if mc != nil {
		out.OutboundMsgCount = *mc
	}
	if te != nil {
		out.OutboundTokenEst = *te
	}
	if hashes != nil {
		out.OutboundMsgHashes = json.RawMessage(hashes)
	}
	if meta != nil {
		out.CompressionMeta = json.RawMessage(meta)
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Executor → compression.Dependencies
// ──────────────────────────────────────────────────────────────────────────────

// NewDependenciesFromExecutor wires the existing executor's Memora and
// Provider clients into the compression.Dependencies struct. Returns nil
// when either client is missing (LLM summary then degrades to mechanical
// trim — see compression.SessionCompressor).
func NewDependenciesFromExecutor(exec *executors.Executor) *compression.Dependencies {
	if exec == nil {
		return nil
	}
	deps := &compression.Dependencies{}
	if exec.Memora != nil {
		deps.Memora = memoraClientAdapter{c: exec.Memora}
	}
	if exec.Provider != nil {
		deps.Provider = providerClientAdapter{r: exec.Provider}
	}
	return deps
}

// memoraClientAdapter bridges *memclient.Client to the compression.MemoraClient
// interface (Disabled + Search).
type memoraClientAdapter struct {
	c memory.Reader
}

func (m memoraClientAdapter) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m memoraClientAdapter) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func (m memoraClientAdapter) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func convertMemoraItems(items []memory.Memory) []memory.Memory { return items }

type legacyMemoraReader struct {
	c *memclient.Client
}

type legacyMemoraWriter struct {
	s *memclient.Sink
}

func (m legacyMemoraReader) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m legacyMemoraReader) Ping(ctx context.Context) error {
	if m.c == nil {
		return nil
	}
	return m.c.Ping(ctx)
}

func (m legacyMemoraReader) BaseURL() string {
	if m.c == nil {
		return ""
	}
	return m.c.BaseURL()
}

func (m legacyMemoraReader) AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error {
	if m.c == nil {
		return nil
	}
	legacyMessages := make([]memory.Message, 0, len(messages))
	for i := range messages {
		legacyMessages = append(legacyMessages, memory.Message{
			Role:    messages[i].Role,
			Content: messages[i].Content,
		})
	}
	return m.c.AddMessage(ctx, userID, legacyMessages, info)
}

func (m legacyMemoraReader) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (m legacyMemoraReader) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (w legacyMemoraWriter) Enqueue(op memory.WriteOp) {
	if w.s == nil {
		return
	}
	msgs := make([]memory.Message, 0, len(op.Messages))
	for i := range op.Messages {
		msgs = append(msgs, memory.Message{
			Role:    op.Messages[i].Role,
			Content: op.Messages[i].Content,
		})
	}
	w.s.Enqueue(memory.WriteOp{
		UserID:   op.UserID,
		Info:     op.Info,
		Source:   op.Source,
		Messages: msgs,
	})
}

func (w legacyMemoraWriter) Stats() memory.Stats {
	if w.s == nil {
		return memory.Stats{}
	}
	stats := w.s.Stats()
	return memory.Stats{
		Enqueued:          stats.Enqueued,
		Dropped:           stats.Dropped,
		Processed:         stats.Processed,
		Errored:           stats.Errored,
		QueueLen:          stats.QueueLen,
		QueueCap:          stats.QueueCap,
		ConsecutiveErrors: stats.ConsecutiveErrors,
		LastError:         stats.LastError,
		LastErrorAt:       stats.LastErrorAt,
		Paused:            stats.Paused,
	}
}

func (w legacyMemoraWriter) Pause() {
	if w.s != nil {
		w.s.Pause()
	}
}

func (w legacyMemoraWriter) Resume() {
	if w.s != nil {
		w.s.Resume()
	}
}

// providerClientAdapter bridges executors providerResolver to compression.ProviderClient.
// The adapter discards the *provider.Policy return value because the
// compression only needs the candidate list.
type providerClientAdapter struct {
	r routingProviderResolver
}

// routingProviderResolver is the minimal subset of executors providerResolver
// we depend on. Re-declared here to avoid importing executors into the
// interface signature (which would create a circular type reference).
type routingProviderResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
}

func (p providerClientAdapter) Enabled() bool {
	if p.r == nil {
		return false
	}
	return p.r.Enabled()
}

func (p providerClientAdapter) GetCandidates(ctx context.Context, model, profile string) ([]compression.ProviderCandidate, error) {
	if p.r == nil {
		return nil, fmt.Errorf("provider resolver nil")
	}
	// 2026-07-03: Bug #7 fix - pass empty tenantID for backward compatibility
	raw, _, err := p.r.GetCandidates(ctx, model, profile, "")
	if err != nil {
		return nil, err
	}
	out := make([]compression.ProviderCandidate, 0, len(raw))
	for i := range raw {
		out = append(out, compression.ProviderCandidate{
			CredentialID:  raw[i].CredentialID,
			ProviderID:    raw[i].ProviderID,
			RawModel:      raw[i].RawModel,
			BaseURL:       raw[i].BaseURL,
			APIKey:        raw[i].APIKey,
			Protocol:      raw[i].Protocol,
			ContextWindow: nil, // provider.Candidate does not carry context window; compression falls back to default heuristic
			Available:     raw[i].Routable,
		})
	}
	return out, nil
}
