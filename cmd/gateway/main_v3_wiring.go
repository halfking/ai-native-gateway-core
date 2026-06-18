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

	"github.com/kaixuan/llm-gateway-go/compressor"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/routing"
	"github.com/kaixuan/llm-gateway-go/sessions"
)

// compressorSessionDisabled returns true when the v3 session compressor is
// turned off via env. Default = enabled (when Redis + DB are both available).
func compressorSessionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// ──────────────────────────────────────────────────────────────────────────────
// Redis backend adapter (sessions.RedisClient → compressor.SessionCacheBackend)
// ──────────────────────────────────────────────────────────────────────────────

type redisBackendAdapter struct {
	c *sessions.RedisClient
}

func redisBackendFromClient(c *sessions.RedisClient) compressor.SessionCacheBackend {
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
// Postgres backend adapter (*db.DB → compressor.SessionCacheDB)
// ──────────────────────────────────────────────────────────────────────────────

type pgBackendAdapter struct {
	dbConn *db.DB
}

func dbBackendFromPool(dbConn *db.DB) compressor.SessionCacheDB {
	if dbConn == nil || !dbConn.Enabled() {
		return nil
	}
	return &pgBackendAdapter{dbConn: dbConn}
}

// LastOutboundForSession runs the L3 cold-start fallback query.
func (a *pgBackendAdapter) LastOutboundForSession(ctx context.Context, tenantID, gwSessionID string) (*compressor.LastOutboundRow, error) {
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

func scanLastOutboundRow(row interface{ Scan(dest ...any) error }) (*compressor.LastOutboundRow, error) {
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
	out := &compressor.LastOutboundRow{}
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
// Executor → compressor.Dependencies
// ──────────────────────────────────────────────────────────────────────────────

// NewDependenciesFromExecutor wires the existing v7 executor's Memora and
// Provider clients into the compressor.Dependencies struct. Returns nil
// when either client is missing (LLM summary then degrades to mechanical
// trim — see compressor.SessionCompressor).
func NewDependenciesFromExecutor(exec *routing.Executor) *compressor.Dependencies {
	if exec == nil {
		return nil
	}
	deps := &compressor.Dependencies{}
	if exec.Memora != nil {
		deps.Memora = memoraClientAdapter{c: exec.Memora}
	}
	if exec.Provider != nil {
		deps.Provider = providerClientAdapter{r: exec.Provider}
	}
	return deps
}

// memoraClientAdapter bridges *memora.Client to the compressor.MemoraClient
// interface (Disabled + Search).
type memoraClientAdapter struct {
	c *memora.Client
}

func (m memoraClientAdapter) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m memoraClientAdapter) Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error) {
	if m.c == nil {
		return nil, fmt.Errorf("memora client nil")
	}
	return m.c.Search(ctx, userID, query, topK)
}

// providerClientAdapter bridges routing.providerResolver to compressor.ProviderClient.
// The adapter discards the *provider.Policy return value because the compressor
// only needs the candidate list.
type providerClientAdapter struct {
	r routingProviderResolver
}

// routingProviderResolver is the minimal subset of routing.providerResolver
// we depend on. Re-declared here to avoid importing routing into the
// interface signature (which would create a circular type reference).
type routingProviderResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile string) ([]provider.Candidate, *provider.Policy, error)
}

func (p providerClientAdapter) Enabled() bool {
	if p.r == nil {
		return false
	}
	return p.r.Enabled()
}

func (p providerClientAdapter) GetCandidates(ctx context.Context, model, profile string) ([]compressor.ProviderCandidate, error) {
	if p.r == nil {
		return nil, fmt.Errorf("provider resolver nil")
	}
	raw, _, err := p.r.GetCandidates(ctx, model, profile)
	if err != nil {
		return nil, err
	}
	out := make([]compressor.ProviderCandidate, 0, len(raw))
	for i := range raw {
		out = append(out, compressor.ProviderCandidate{
			CredentialID:  raw[i].CredentialID,
			ProviderID:    raw[i].ProviderID,
			RawModel:      raw[i].RawModel,
			BaseURL:       raw[i].BaseURL,
			APIKey:        raw[i].APIKey,
			Protocol:      raw[i].Protocol,
			ContextWindow: nil, // provider.Candidate does not carry context window; compressor falls back to default heuristic
			Available:     raw[i].Routable,
		})
	}
	return out, nil
}