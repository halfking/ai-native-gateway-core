package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lib/pq"
)

// FreeResourceEntry 免费资源条目
type FreeResourceEntry struct {
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
	// TrainsOnPrompts marks providers known to train on user prompts (privacy flag).
	// Default false; callers routing privacy-sensitive traffic should filter these out.
	TrainsOnPrompts bool `json:"trains_on_prompts"`
	Enabled         bool `json:"enabled"`
}

// AutoComboTemplate Auto Combo 模板
type AutoComboTemplate struct {
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

// KeylessProvider Keyless 提供商
type KeylessProvider struct {
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

func main() {
	// 命令行参数
	dbURL := flag.String("db-url", "", "数据库连接 URL (必填)")
	catalogFile := flag.String("catalog", "", "免费资源目录 JSON 文件")
	templatesFile := flag.String("templates", "", "Auto Combo 模板 JSON 文件")
	keylessFile := flag.String("keyless", "", "Keyless 提供商 JSON 文件")
	tenantID := flag.String("tenant-id", "default", "租户 ID")
	dryRun := flag.Bool("dry-run", false, "试运行模式（不实际写入数据库）")

	flag.Parse()

	if *dbURL == "" {
		log.Fatal("错误: 必须提供 --db-url 参数")
	}

	// 连接数据库
	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("数据库 Ping 失败: %v", err)
	}

	log.Println("✅ 数据库连接成功")

	// 导入免费资源目录
	if *catalogFile != "" {
		if err := importFreeResources(db, *catalogFile, *tenantID, *dryRun); err != nil {
			log.Fatalf("导入免费资源失败: %v", err)
		}
	}

	// 导入 Auto Combo 模板
	if *templatesFile != "" {
		if err := importAutoComboTemplates(db, *templatesFile, *tenantID, *dryRun); err != nil {
			log.Fatalf("导入 Auto Combo 模板失败: %v", err)
		}
	}

	// 导入 Keyless 提供商
	if *keylessFile != "" {
		if err := importKeylessProviders(db, *keylessFile, *tenantID, *dryRun); err != nil {
			log.Fatalf("导入 Keyless 提供商失败: %v", err)
		}
	}

	log.Println("🎉 所有种子数据导入完成！")
}

func importFreeResources(db *sql.DB, filename string, tenantID string, dryRun bool) error {
	log.Printf("📊 导入免费资源目录: %s", filename)

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}

	var entries []FreeResourceEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}

	log.Printf("  发现 %d 个免费资源", len(entries))

	if dryRun {
		log.Println("  [试运行] 跳过实际插入")
		return nil
	}

	query := `
		INSERT INTO free_resource_catalog (
			provider_code, model_id, display_name, display_name_en,
			free_type, monthly_tokens, daily_tokens, credit_tokens,
			pool_key, tos_verdict, tos_notes, constraints_json,
			discovery_method, verified_at, trains_on_prompts, enabled, tenant_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (provider_code, model_id, tenant_id)
		DO UPDATE SET
			display_name = EXCLUDED.display_name,
			display_name_en = EXCLUDED.display_name_en,
			free_type = EXCLUDED.free_type,
			monthly_tokens = EXCLUDED.monthly_tokens,
			daily_tokens = EXCLUDED.daily_tokens,
			credit_tokens = EXCLUDED.credit_tokens,
			pool_key = EXCLUDED.pool_key,
			tos_verdict = EXCLUDED.tos_verdict,
			tos_notes = EXCLUDED.tos_notes,
			constraints_json = EXCLUDED.constraints_json,
			discovery_method = EXCLUDED.discovery_method,
			verified_at = EXCLUDED.verified_at,
			trains_on_prompts = EXCLUDED.trains_on_prompts,
			enabled = EXCLUDED.enabled,
			updated_at = now()
	`

	inserted := 0
	for _, entry := range entries {
		var verifiedAt *time.Time
		if entry.VerifiedAt != "" {
			t, err := time.Parse(time.RFC3339, entry.VerifiedAt)
			if err == nil {
				verifiedAt = &t
			}
		}

		_, err := db.Exec(query,
			entry.ProviderCode, entry.ModelID, entry.DisplayName, nullString(entry.DisplayNameEN),
			entry.FreeType, entry.MonthlyTokens, entry.DailyTokens, entry.CreditTokens,
			nullString(entry.PoolKey), entry.ToSVerdict, nullString(entry.ToSNotes), entry.ConstraintsJSON,
			entry.DiscoveryMethod, verifiedAt, entry.TrainsOnPrompts, entry.Enabled, tenantID,
		)
		if err != nil {
			return fmt.Errorf("插入 %s/%s 失败: %w", entry.ProviderCode, entry.ModelID, err)
		}
		inserted++
	}

	log.Printf("✅ 已导入 %d 个免费资源", inserted)
	return nil
}

func importAutoComboTemplates(db *sql.DB, filename string, tenantID string, dryRun bool) error {
	log.Printf("🔀 导入 Auto Combo 模板: %s", filename)

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}

	var templates []AutoComboTemplate
	if err := json.Unmarshal(data, &templates); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}

	log.Printf("  发现 %d 个模板", len(templates))

	if dryRun {
		log.Println("  [试运行] 跳过实际插入")
		return nil
	}

	query := `
		INSERT INTO auto_combo_templates (
			combo_name, display_name, description, variant,
			tier_filter, free_type_filter, tos_filter,
			provider_allowlist, provider_denylist,
			scoring_weights_json, max_candidates, exploration_rate,
			enabled, priority, tenant_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (combo_name, tenant_id)
		DO UPDATE SET
			display_name = EXCLUDED.display_name,
			description = EXCLUDED.description,
			variant = EXCLUDED.variant,
			tier_filter = EXCLUDED.tier_filter,
			free_type_filter = EXCLUDED.free_type_filter,
			tos_filter = EXCLUDED.tos_filter,
			provider_allowlist = EXCLUDED.provider_allowlist,
			provider_denylist = EXCLUDED.provider_denylist,
			scoring_weights_json = EXCLUDED.scoring_weights_json,
			max_candidates = EXCLUDED.max_candidates,
			exploration_rate = EXCLUDED.exploration_rate,
			enabled = EXCLUDED.enabled,
			priority = EXCLUDED.priority,
			updated_at = now()
	`

	inserted := 0
	for _, tmpl := range templates {
		_, err := db.Exec(query,
			tmpl.ComboName, tmpl.DisplayName, nullString(tmpl.Description), tmpl.Variant,
			pqArray(tmpl.TierFilter), pqArray(tmpl.FreeTypeFilter), pqArray(tmpl.ToSFilter),
			pqArray(tmpl.ProviderAllowlist), pqArray(tmpl.ProviderDenylist),
			tmpl.ScoringWeightsJSON, tmpl.MaxCandidates, tmpl.ExplorationRate,
			tmpl.Enabled, tmpl.Priority, tenantID,
		)
		if err != nil {
			return fmt.Errorf("插入 %s 失败: %w", tmpl.ComboName, err)
		}
		inserted++
	}

	log.Printf("✅ 已导入 %d 个 Auto Combo 模板", inserted)
	return nil
}

func importKeylessProviders(db *sql.DB, filename string, tenantID string, dryRun bool) error {
	log.Printf("🔓 导入 Keyless 提供商: %s", filename)

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}

	var providers []KeylessProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}

	log.Printf("  发现 %d 个 Keyless 提供商", len(providers))

	if dryRun {
		log.Println("  [试运行] 跳过实际插入")
		return nil
	}

	query := `
		INSERT INTO keyless_providers (
			provider_code, display_name, auth_hint, bootstrap_method,
			rpm_limit, rpd_limit, concurrent_limit, reliability_score,
			enabled, allowlist_in_auto_combo, notes, tenant_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (provider_code, tenant_id)
		DO UPDATE SET
			display_name = EXCLUDED.display_name,
			auth_hint = EXCLUDED.auth_hint,
			bootstrap_method = EXCLUDED.bootstrap_method,
			rpm_limit = EXCLUDED.rpm_limit,
			rpd_limit = EXCLUDED.rpd_limit,
			concurrent_limit = EXCLUDED.concurrent_limit,
			reliability_score = EXCLUDED.reliability_score,
			enabled = EXCLUDED.enabled,
			allowlist_in_auto_combo = EXCLUDED.allowlist_in_auto_combo,
			notes = EXCLUDED.notes,
			updated_at = now()
	`

	inserted := 0
	for _, prov := range providers {
		_, err := db.Exec(query,
			prov.ProviderCode, prov.DisplayName, nullString(prov.AuthHint), nullString(prov.BootstrapMethod),
			prov.RPMLimit, prov.RPDLimit, prov.ConcurrentLimit, prov.ReliabilityScore,
			prov.Enabled, prov.AllowlistInAutoCombo, nullString(prov.Notes), tenantID,
		)
		if err != nil {
			return fmt.Errorf("插入 %s 失败: %w", prov.ProviderCode, err)
		}
		inserted++
	}

	log.Printf("✅ 已导入 %d 个 Keyless 提供商", inserted)
	return nil
}

// 辅助函数
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func pqArray(arr []string) interface{} {
	return pq.Array(arr)
}
