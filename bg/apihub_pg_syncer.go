// Package bg — apihub PGSyncer implementation.
//
// Reads LLM endpoints (model_offers + api_keys) and MCP servers
// (tool_registry.tools) from PostgreSQL and transforms them into
// apihub.Asset structs for the AssetWatcher to register.
//
// This is the production implementation of AssetSyncSource. Tests can
// inject a fake that returns in-memory data.
package bg

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/apihub"
)

// PGSyncer reads source tables from PostgreSQL.
type PGSyncer struct {
	pool *pgxpool.Pool
}

// NewPGSyncer creates a syncer backed by the given connection pool.
func NewPGSyncer(pool *pgxpool.Pool) *PGSyncer {
	return &PGSyncer{pool: pool}
}

// LLMEndpoints reads model_offers joined with api_keys to produce
// apihub.Asset entries with kind=llm-endpoint.
//
// Schema (verified 2026-06-25 against 184 PG schema):
//   - model_offers: id, credential_id, raw_model_name, available, ...
//   - api_keys:     id, tenant_id, owner_user, enabled, status='active', ...
//
// Each model_offer becomes one asset with:
//   - RefID:    model_offers.id
//   - TenantID: api_keys.tenant_id
//   - Name:     raw_model_name
//   - Owner:    owner_user
//   - Metadata: {credential_id, available, ...}
func (s *PGSyncer) LLMEndpoints(ctx context.Context) ([]apihub.Asset, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("pg syncer: pool is nil")
	}

	query := `
		SELECT
			mo.id AS model_id,
			mo.raw_model_name,
			mo.credential_id,
			mo.available AS model_available,
			ak.tenant_id,
			COALESCE(ak.owner_user, '') AS owner_user
		FROM model_offers mo
		INNER JOIN api_keys ak ON mo.credential_id = ak.id
		WHERE ak.enabled = true
		  AND ak.status = 'active'
		ORDER BY mo.id
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("pg syncer LLMEndpoints query: %w", err)
	}
	defer rows.Close()

	var assets []apihub.Asset
	for rows.Next() {
		var (
			modelID        int64
			rawModelName   string
			credentialID   int64
			modelAvailable bool
			tenantID       string
			ownerUser      string
		)
		if err := rows.Scan(
			&modelID, &rawModelName, &credentialID, &modelAvailable,
			&tenantID, &ownerUser,
		); err != nil {
			slog.Warn("pg syncer: LLMEndpoints scan error", "error", err)
			continue
		}

		asset := apihub.Asset{
			Kind:     apihub.KindLLMEndpoint,
			RefID:    modelID,
			TenantID: tenantID,
			Name:     rawModelName,
			Owner:    ownerUser,
			Metadata: map[string]any{
				"credential_id":   credentialID,
				"model_available": modelAvailable,
			},
		}
		assets = append(assets, asset)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg syncer LLMEndpoints rows: %w", err)
	}

	slog.Debug("pg syncer: LLMEndpoints fetched", "count", len(assets))
	return assets, nil
}

// MCPServers reads tool_registry.tools to produce apihub.Asset entries
// with kind=mcp-server.
//
// Schema assumptions:
//   - tool_registry.tools: server_name, tenant_id, enabled, description, ...
//
// Each tool becomes one asset with:
//   - RefID: hash(server_name + tenant_id) (since tools table may not have numeric id)
//   - TenantID: tenant_id
//   - Name: server_name
//   - Metadata: {enabled, description, ...}
//
// NOTE: If tools table has no tenant_id column, this will fail. In that case,
// either add tenant_id to tools or use a global sentinel like "system".
func (s *PGSyncer) MCPServers(ctx context.Context) ([]apihub.Asset, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("pg syncer: pool is nil")
	}

	// Check if tool_registry schema exists
	var schemaExists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.schemata WHERE schema_name = 'tool_registry'
		)
	`).Scan(&schemaExists)
	if err != nil {
		return nil, fmt.Errorf("pg syncer MCPServers schema check: %w", err)
	}
	if !schemaExists {
		slog.Debug("pg syncer: tool_registry schema does not exist, skipping MCP sync")
		return nil, nil
	}

	// Check if tools table has tenant_id column
	var tenantColExists bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns 
			WHERE table_schema = 'tool_registry' 
			  AND table_name = 'tools' 
			  AND column_name = 'tenant_id'
		)
	`).Scan(&tenantColExists)
	if err != nil {
		return nil, fmt.Errorf("pg syncer MCPServers column check: %w", err)
	}

	var query string
	if tenantColExists {
		query = `
			SELECT 
				server_name,
				tenant_id,
				COALESCE(description, '') AS description,
				COALESCE(enabled, true) AS enabled
			FROM tool_registry.tools
			WHERE enabled = true
			ORDER BY server_name
		`
	} else {
		// Fallback: use 'system' as tenant_id if column doesn't exist
		slog.Warn("pg syncer: tool_registry.tools has no tenant_id column, using 'system' as default")
		query = `
			SELECT 
				server_name,
				'system' AS tenant_id,
				COALESCE(description, '') AS description,
				COALESCE(enabled, true) AS enabled
			FROM tool_registry.tools
			WHERE enabled = true
			ORDER BY server_name
		`
	}

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("pg syncer MCPServers query: %w", err)
	}
	defer rows.Close()

	var assets []apihub.Asset
	for rows.Next() {
		var (
			serverName  string
			tenantID    string
			description string
			enabled     bool
		)
		if err := rows.Scan(&serverName, &tenantID, &description, &enabled); err != nil {
			slog.Warn("pg syncer: MCPServers scan error", "error", err)
			continue
		}

		// Use hash of server_name + tenant_id as RefID (tools table may not have numeric id)
		// In production, if tools has an id column, use that instead
		refID := int64(hash(serverName + ":" + tenantID))

		asset := apihub.Asset{
			Kind:     apihub.KindMCPServer,
			RefID:    refID,
			TenantID: tenantID,
			Name:     serverName,
			Metadata: map[string]any{
				"description": description,
				"enabled":     enabled,
			},
		}
		assets = append(assets, asset)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg syncer MCPServers rows: %w", err)
	}

	slog.Debug("pg syncer: MCPServers fetched", "count", len(assets))
	return assets, nil
}

// hash is a simple string hash function for generating RefID.
// In production, consider using a stable hash like FNV-1a or city hash.
func hash(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
