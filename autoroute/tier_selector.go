package autoroute

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// tier_selector.go implements tier determination logic for AUTO_MODEL V3.
//
// Related: docs/03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 2.4
//
// Tier selection flow:
//   1. Query task_type_tier_config for preferred tier
//   2. Apply depth-based degradation (depth >= 2 → force tier-c)
//   3. Check tenant override (tenant_config)
//   4. Check header override (X-Gw-Model-Tier, highest priority)
//
// Tier definitions:
//   - tier-a: High-performance ($15-50/1M tokens) - architecture, audit, debugging
//   - tier-b: Standard ($5-15/1M tokens) - coding, refactoring, testing
//   - tier-c: Economy ($0.5-5/1M tokens) - devops, documentation, summary, dependency

// TierConfig represents the tier configuration for a task type.
type TierConfig struct {
	TaskType       TaskType
	PreferredTier  string   // "tier-a", "tier-b", or "tier-c"
	FallbackTiers  []string // Ordered list of fallback tiers
	MinConfidence  float64  // Minimum confidence to use this tier
	TenantID       string   // Empty = global default
	Enabled        bool
}

// TierSelector determines which tier to use for a given task type and context.
type TierSelector struct {
	db *sql.DB
	
	// configCache caches task type tier configurations from database
	configCache      map[string]*TierConfig // key: "tasktype:tenantid"
	configCacheMu    sync.RWMutex
	configCacheExpiry time.Time
	
	// cacheTTL controls how often to refresh from database (default: 5 minutes)
	cacheTTL time.Duration
	
	// enableV3 controls whether to use V3 tier selection or fall back to legacy
	enableV3 bool
}

// TierSelectionInput contains all information needed for tier selection.
type TierSelectionInput struct {
	TaskType   TaskType
	Confidence float64
	
	// Depth-based degradation
	AgentDepth int // From X-Gw-Agent-Depth header (0=client, 1=first-level sub-agent, 2+=nested)
	
	// Tenant override
	TenantID string
	
	// Header override (highest priority)
	HeaderTier string // From X-Gw-Model-Tier header
}

// TierSelectionResult contains the selected tier and reasoning.
type TierSelectionResult struct {
	Tier           string   // "tier-a", "tier-b", or "tier-c"
	FallbackTiers  []string // Ordered fallback tiers
	Reason         string   // Human-readable explanation
	ConfigSource   string   // "header", "depth", "tenant", "global", "default"
}

// NewTierSelector creates a new tier selector.
// db can be nil for testing (falls back to in-memory defaults).
func NewTierSelector(db *sql.DB, enableV3 bool) *TierSelector {
	return &TierSelector{
		db:                db,
		configCache:       make(map[string]*TierConfig),
		cacheTTL:          5 * time.Minute,
		enableV3:          enableV3,
		configCacheExpiry: time.Now().Add(5 * time.Minute),
	}
}

// SelectTier determines which tier to use for the given input.
//
// Priority order (highest to lowest):
//   1. Header override (X-Gw-Model-Tier)
//   2. Depth-based degradation (depth >= 2 → tier-c)
//   3. Tenant override (tenant_config)
//   4. Global config (task_type_tier_config where tenant_id IS NULL)
//   5. In-memory default (TaskTypeTierMapping)
func (ts *TierSelector) SelectTier(ctx context.Context, input TierSelectionInput) (*TierSelectionResult, error) {
	if !ts.enableV3 {
		// Feature flag disabled, return tier-b as safe default
		return &TierSelectionResult{
			Tier:          "tier-b",
			FallbackTiers: []string{"tier-a", "tier-c"},
			Reason:        "v3 disabled, using default tier-b",
			ConfigSource:  "default",
		}, nil
	}
	
	// Priority 1: Header override (explicit user/client control)
	if input.HeaderTier != "" {
		tier := strings.ToLower(input.HeaderTier)
		if isValidTier(tier) {
			return &TierSelectionResult{
				Tier:          tier,
				FallbackTiers: getFallbacksForTier(tier),
				Reason:        fmt.Sprintf("explicit header override: X-Gw-Model-Tier=%s", tier),
				ConfigSource:  "header",
			}, nil
		}
	}
	
	// Priority 2: Depth-based degradation (cost optimization for nested sub-agents)
	// Depth 0-1: Normal tier selection
	// Depth >= 2: Force tier-c (nested sub-agents get economy models)
	if input.AgentDepth >= 2 {
		return &TierSelectionResult{
			Tier:          "tier-c",
			FallbackTiers: []string{"tier-b"}, // Allow escalation to tier-b if tier-c fails
			Reason:        fmt.Sprintf("depth-based degradation: agent_depth=%d (>= 2) → tier-c", input.AgentDepth),
			ConfigSource:  "depth",
		}, nil
	}
	
	// Priority 3-4: Query database config (tenant override, then global)
	cfg, err := ts.getConfig(ctx, input.TaskType, input.TenantID)
	if err != nil {
		// Database query failed, fall back to in-memory default
		return ts.getDefaultTier(input.TaskType, input.Confidence)
	}
	
	if cfg == nil {
		// No config found in database, use in-memory default
		return ts.getDefaultTier(input.TaskType, input.Confidence)
	}
	
	// Check confidence threshold
	if input.Confidence < cfg.MinConfidence {
		// Low confidence, escalate to tier-a for safety
		return &TierSelectionResult{
			Tier:          "tier-a",
			FallbackTiers: []string{"tier-b"},
			Reason:        fmt.Sprintf("low confidence (%.2f < %.2f), escalating to tier-a", input.Confidence, cfg.MinConfidence),
			ConfigSource:  "confidence_escalation",
		}, nil
	}
	
	// Use configured tier
	source := "global"
	if cfg.TenantID != "" {
		source = "tenant"
	}
	
	return &TierSelectionResult{
		Tier:          cfg.PreferredTier,
		FallbackTiers: cfg.FallbackTiers,
		Reason:        fmt.Sprintf("task_type=%s → preferred_tier=%s (source: %s)", cfg.TaskType, cfg.PreferredTier, source),
		ConfigSource:  source,
	}, nil
}

// getConfig retrieves tier config from cache or database.
// Returns tenant-specific config if exists, otherwise global config.
func (ts *TierSelector) getConfig(ctx context.Context, taskType TaskType, tenantID string) (*TierConfig, error) {
	if ts.db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	
	// Check cache first
	ts.configCacheMu.RLock()
	expired := time.Now().After(ts.configCacheExpiry)
	if !expired {
		// Try tenant-specific config
		if tenantID != "" {
			key := string(taskType) + ":" + tenantID
			if cfg, ok := ts.configCache[key]; ok {
				ts.configCacheMu.RUnlock()
				return cfg, nil
			}
		}
		
		// Try global config
		key := string(taskType) + ":"
		if cfg, ok := ts.configCache[key]; ok {
			ts.configCacheMu.RUnlock()
			return cfg, nil
		}
	}
	ts.configCacheMu.RUnlock()
	
	// Cache miss or expired, query database
	if expired {
		// Refresh entire cache
		ts.refreshCache(ctx)
	}
	
	// Query specific config
	cfg, err := ts.queryConfig(ctx, taskType, tenantID)
	if err != nil {
		return nil, err
	}
	
	return cfg, nil
}

// refreshCache reloads all configs from database.
func (ts *TierSelector) refreshCache(ctx context.Context) error {
	if ts.db == nil {
		return fmt.Errorf("database not configured")
	}
	
	query := `
		SELECT task_type, preferred_tier, fallback_tiers, min_confidence, 
		       COALESCE(tenant_id, '') as tenant_id, enabled
		FROM task_type_tier_config
		WHERE enabled = TRUE
		ORDER BY tenant_id NULLS FIRST
	`
	
	rows, err := ts.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query task_type_tier_config: %w", err)
	}
	defer rows.Close()
	
	newCache := make(map[string]*TierConfig)
	
	for rows.Next() {
		var cfg TierConfig
		var fallbackTiersStr string
		
		err := rows.Scan(&cfg.TaskType, &cfg.PreferredTier, &fallbackTiersStr, 
			&cfg.MinConfidence, &cfg.TenantID, &cfg.Enabled)
		if err != nil {
			return fmt.Errorf("scan config row: %w", err)
		}
		
		// Parse fallback_tiers array (PostgreSQL text[] format: {tier-a,tier-b})
		cfg.FallbackTiers = parsePostgresArray(fallbackTiersStr)
		
		key := string(cfg.TaskType) + ":" + cfg.TenantID
		newCache[key] = &cfg
	}
	
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate config rows: %w", err)
	}
	
	// Update cache atomically
	ts.configCacheMu.Lock()
	ts.configCache = newCache
	ts.configCacheExpiry = time.Now().Add(ts.cacheTTL)
	ts.configCacheMu.Unlock()
	
	return nil
}

// queryConfig queries a specific task type config from database.
// Returns tenant-specific config if exists, otherwise global config.
func (ts *TierSelector) queryConfig(ctx context.Context, taskType TaskType, tenantID string) (*TierConfig, error) {
	if ts.db == nil {
		return nil, fmt.Errorf("database not configured")
	}
	
	// Try tenant-specific first
	if tenantID != "" {
		query := `
			SELECT task_type, preferred_tier, fallback_tiers, min_confidence, 
			       tenant_id, enabled
			FROM task_type_tier_config
			WHERE task_type = $1 AND tenant_id = $2 AND enabled = TRUE
			LIMIT 1
		`
		
		cfg, err := ts.scanConfig(ctx, query, string(taskType), tenantID)
		if err == nil {
			// Cache it
			key := string(taskType) + ":" + tenantID
			ts.configCacheMu.Lock()
			ts.configCache[key] = cfg
			ts.configCacheMu.Unlock()
			return cfg, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	
	// Fall back to global config
	query := `
		SELECT task_type, preferred_tier, fallback_tiers, min_confidence, 
		       COALESCE(tenant_id, '') as tenant_id, enabled
		FROM task_type_tier_config
		WHERE task_type = $1 AND tenant_id IS NULL AND enabled = TRUE
		LIMIT 1
	`
	
	cfg, err := ts.scanConfig(ctx, query, string(taskType))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No config found
		}
		return nil, err
	}
	
	// Cache it
	key := string(taskType) + ":"
	ts.configCacheMu.Lock()
	ts.configCache[key] = cfg
	ts.configCacheMu.Unlock()
	
	return cfg, nil
}

// scanConfig executes query and scans a single TierConfig row.
func (ts *TierSelector) scanConfig(ctx context.Context, query string, args ...interface{}) (*TierConfig, error) {
	var cfg TierConfig
	var fallbackTiersStr string
	
	err := ts.db.QueryRowContext(ctx, query, args...).Scan(
		&cfg.TaskType, &cfg.PreferredTier, &fallbackTiersStr,
		&cfg.MinConfidence, &cfg.TenantID, &cfg.Enabled,
	)
	if err != nil {
		return nil, err
	}
	
	cfg.FallbackTiers = parsePostgresArray(fallbackTiersStr)
	
	return &cfg, nil
}

// getDefaultTier returns the in-memory default tier for a task type.
// Used as fallback when database is unavailable.
func (ts *TierSelector) getDefaultTier(taskType TaskType, confidence float64) (*TierSelectionResult, error) {
	tier, ok := TaskTypeTierMapping[taskType]
	if !ok {
		// Unknown task type, default to tier-b (safe middle ground)
		tier = "tier-b"
	}
	
	// Get fallback tiers
	fallbacks := getDefaultFallbacks(tier)
	
	// Check confidence threshold
	minConf := MinConfidenceThresholds[taskType]
	if minConf == 0 {
		minConf = 0.70
	}
	
	reason := fmt.Sprintf("in-memory default: task_type=%s → tier=%s", taskType, tier)
	
	if confidence < minConf {
		// Low confidence, escalate to tier-a
		return &TierSelectionResult{
			Tier:          "tier-a",
			FallbackTiers: []string{"tier-b"},
			Reason:        fmt.Sprintf("low confidence (%.2f < %.2f), escalating to tier-a", confidence, minConf),
			ConfigSource:  "confidence_escalation",
		}, nil
	}
	
	return &TierSelectionResult{
		Tier:          tier,
		FallbackTiers: fallbacks,
		Reason:        reason,
		ConfigSource:  "default",
	}, nil
}

// isValidTier checks if a tier string is valid.
func isValidTier(tier string) bool {
	switch tier {
	case "tier-a", "tier-b", "tier-c":
		return true
	default:
		return false
	}
}

// getFallbacksForTier returns appropriate fallback tiers for a given tier.
func getFallbacksForTier(tier string) []string {
	switch tier {
	case "tier-a":
		return []string{"tier-b"} // Fallback to standard tier
	case "tier-b":
		return []string{"tier-a", "tier-c"} // Can escalate or degrade
	case "tier-c":
		return []string{"tier-b"} // Can escalate if quality issues
	default:
		return []string{"tier-b"}
	}
}

// getDefaultFallbacks returns default fallback tiers based on task type tier.
func getDefaultFallbacks(tier string) []string {
	switch tier {
	case "tier-a":
		return []string{"tier-b"}
	case "tier-b":
		return []string{"tier-a", "tier-c"}
	case "tier-c":
		return []string{"tier-b"}
	default:
		return []string{"tier-b"}
	}
}

// parsePostgresArray parses PostgreSQL text[] format: {tier-a,tier-b,tier-c}
func parsePostgresArray(s string) []string {
	if s == "" || s == "{}" {
		return []string{}
	}
	
	// Remove braces
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	
	if s == "" {
		return []string{}
	}
	
	// Split by comma
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	
	return result
}

// SetV3Enabled dynamically enables or disables V3 tier selection.
func (ts *TierSelector) SetV3Enabled(enabled bool) {
	ts.enableV3 = enabled
}

// IsV3Enabled returns whether V3 tier selection is enabled.
func (ts *TierSelector) IsV3Enabled() bool {
	return ts.enableV3
}
