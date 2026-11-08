// Package omnifree — 免费资源目录的启动种子（2026-11-09 运营审计轮项5）。
//
// 背景（取证 2026-11-09）：free_resource_catalog 生产 0 行 —— 种子 JSON
// （configs/seed/*.json，523 条目录 + 6 个 auto/* 组合模板 + 8 个 keyless）
// 只能靠手动跑 cmd/seed-free-resources，没有任何启动路径加载它们 ⇒
// auto/... 免费池路由在生产永远空转（「无法引入免费模型」的第二段断链；
// 第一段 = discovery 扫描被 SSRF 守卫挡掉内网源，见 provider_scanner
// PrivateAllowlistRules；第三段 = 扫描结果 pending 后无人导入，见
// bg/scan_scheduler SetAutoImporter）。
//
// 语义：**ON CONFLICT DO NOTHING**（与 CLI 的覆盖式 upsert 不同）——
// 启动种子只补缺，绝不覆盖管理员在生产上的修改；重复启动零写放大。
//
// 开关：LLM_GATEWAY_OMNIFREE_SEED=off|false|0 关闭（默认开）。
package omnifree

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "embed"

	"github.com/lib/pq"
)

//go:embed data/free_resource_catalog.json
var catalogJSON []byte

//go:embed data/auto_combo_templates.json
var comboTemplatesJSON []byte

//go:embed data/keyless_providers.json
var keylessJSON []byte

// SeedIfMissing loads the embedded seed files into the three omni-free
// tables, inserting only rows that do not exist yet. Safe to call on every
// boot; returns per-file inserted counts.
func SeedIfMissing(ctx context.Context, db *sql.DB, tenantID string) (catalog, combos, keyless int, err error) {
	if disabled() {
		return 0, 0, 0, nil
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if catalog, err = seedCatalog(ctx, db, tenantID); err != nil {
		return catalog, combos, keyless, fmt.Errorf("omnifree seed catalog: %w", err)
	}
	if combos, err = seedComboTemplates(ctx, db, tenantID); err != nil {
		return catalog, combos, keyless, fmt.Errorf("omnifree seed combo templates: %w", err)
	}
	if keyless, err = seedKeyless(ctx, db, tenantID); err != nil {
		return catalog, combos, keyless, fmt.Errorf("omnifree seed keyless: %w", err)
	}
	if catalog+combos+keyless > 0 {
		slog.Info("omnifree startup seed inserted missing rows",
			"catalog", catalog, "combo_templates", combos, "keyless", keyless)
	}
	return catalog, combos, keyless, nil
}

func disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_OMNIFREE_SEED"))) {
	case "off", "false", "0":
		return true
	}
	return false
}

type freeResourceEntry struct {
	ProviderCode    string          `json:"provider_code"`
	ModelID         string          `json:"model_id"`
	DisplayName     string          `json:"display_name"`
	DisplayNameEN   string          `json:"display_name_en"`
	FreeType        string          `json:"free_type"`
	MonthlyTokens   int64           `json:"monthly_tokens"`
	DailyTokens     int64           `json:"daily_tokens"`
	CreditTokens    int64           `json:"credit_tokens"`
	PoolKey         string          `json:"pool_key"`
	ToSVerdict      string          `json:"tos_verdict"`
	ToSNotes        string          `json:"tos_notes"`
	ConstraintsJSON json.RawMessage `json:"constraints_json"`
	DiscoveryMethod string          `json:"discovery_method"`
	VerifiedAt      string          `json:"verified_at"`
	TrainsOnPrompts bool            `json:"trains_on_prompts"`
	Enabled         bool            `json:"enabled"`
}

func seedCatalog(ctx context.Context, db *sql.DB, tenantID string) (int, error) {
	var entries []freeResourceEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		return 0, err
	}
	inserted := 0
	for _, e := range entries {
		if e.ProviderCode == "" || e.ModelID == "" || e.FreeType == "" {
			continue
		}
		var verifiedAt *time.Time
		if e.VerifiedAt != "" {
			if t, perr := time.Parse(time.RFC3339, e.VerifiedAt); perr == nil {
				verifiedAt = &t
			}
		}
		res, err := db.ExecContext(ctx, `
			INSERT INTO free_resource_catalog (
				provider_code, model_id, display_name, display_name_en,
				free_type, monthly_tokens, daily_tokens, credit_tokens,
				pool_key, tos_verdict, tos_notes, constraints_json,
				discovery_method, verified_at, trains_on_prompts, enabled, tenant_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			ON CONFLICT (provider_code, model_id, tenant_id) DO NOTHING
		`,
			e.ProviderCode, e.ModelID, e.DisplayName, nullString(e.DisplayNameEN),
			e.FreeType, e.MonthlyTokens, e.DailyTokens, e.CreditTokens,
			nullString(e.PoolKey), e.ToSVerdict, nullString(e.ToSNotes), e.ConstraintsJSON,
			e.DiscoveryMethod, verifiedAt, e.TrainsOnPrompts, e.Enabled, tenantID)
		if err != nil {
			return inserted, fmt.Errorf("catalog %s/%s: %w", e.ProviderCode, e.ModelID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

type autoComboTemplate struct {
	ComboName          string          `json:"combo_name"`
	DisplayName        string          `json:"display_name"`
	Description        string          `json:"description"`
	Variant            string          `json:"variant"`
	TierFilter         []string        `json:"tier_filter"`
	FreeTypeFilter     []string        `json:"free_type_filter"`
	ToSFilter          []string        `json:"tos_filter"`
	ProviderAllowlist  []string        `json:"provider_allowlist"`
	ProviderDenylist   []string        `json:"provider_denylist"`
	ScoringWeightsJSON json.RawMessage `json:"scoring_weights_json"`
	MaxCandidates      int             `json:"max_candidates"`
	ExplorationRate    float64         `json:"exploration_rate"`
	Enabled            bool            `json:"enabled"`
	Priority           int             `json:"priority"`
}

func seedComboTemplates(ctx context.Context, db *sql.DB, tenantID string) (int, error) {
	var templates []autoComboTemplate
	if err := json.Unmarshal(comboTemplatesJSON, &templates); err != nil {
		return 0, err
	}
	inserted := 0
	for _, t := range templates {
		if t.ComboName == "" {
			continue
		}
		res, err := db.ExecContext(ctx, `
			INSERT INTO auto_combo_templates (
				combo_name, display_name, description, variant,
				tier_filter, free_type_filter, tos_filter,
				provider_allowlist, provider_denylist,
				scoring_weights_json, max_candidates, exploration_rate,
				enabled, priority, tenant_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (combo_name, tenant_id) DO NOTHING
		`,
			t.ComboName, t.DisplayName, nullString(t.Description), t.Variant,
			pq.Array(t.TierFilter), pq.Array(t.FreeTypeFilter), pq.Array(t.ToSFilter),
			pq.Array(t.ProviderAllowlist), pq.Array(t.ProviderDenylist),
			t.ScoringWeightsJSON, t.MaxCandidates, t.ExplorationRate,
			t.Enabled, t.Priority, tenantID)
		if err != nil {
			return inserted, fmt.Errorf("combo %s: %w", t.ComboName, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

type keylessProvider struct {
	ProviderCode         string  `json:"provider_code"`
	DisplayName          string  `json:"display_name"`
	AuthHint             string  `json:"auth_hint"`
	BootstrapMethod      string  `json:"bootstrap_method"`
	RPMLimit             int     `json:"rpm_limit"`
	RPDLimit             int     `json:"rpd_limit"`
	ConcurrentLimit      int     `json:"concurrent_limit"`
	ReliabilityScore     float64 `json:"reliability_score"`
	Enabled              bool    `json:"enabled"`
	AllowlistInAutoCombo bool    `json:"allowlist_in_auto_combo"`
	Notes                string  `json:"notes"`
}

func seedKeyless(ctx context.Context, db *sql.DB, tenantID string) (int, error) {
	var providers []keylessProvider
	if err := json.Unmarshal(keylessJSON, &providers); err != nil {
		return 0, err
	}
	inserted := 0
	for _, p := range providers {
		if p.ProviderCode == "" {
			continue
		}
		res, err := db.ExecContext(ctx, `
			INSERT INTO keyless_providers (
				provider_code, display_name, auth_hint, bootstrap_method,
				rpm_limit, rpd_limit, concurrent_limit, reliability_score,
				enabled, allowlist_in_auto_combo, notes, tenant_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (provider_code, tenant_id) DO NOTHING
		`,
			p.ProviderCode, p.DisplayName, nullString(p.AuthHint), nullString(p.BootstrapMethod),
			p.RPMLimit, p.RPDLimit, p.ConcurrentLimit, p.ReliabilityScore,
			p.Enabled, p.AllowlistInAutoCombo, nullString(p.Notes), tenantID)
		if err != nil {
			return inserted, fmt.Errorf("keyless %s: %w", p.ProviderCode, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
