package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// accWorkTypesPath is ACC's service-to-service work-type catalog.
//
// It was "/api/llm/work-types", the Node-era path, which acc-go no longer
// serves — acc-go mounts this catalog at /api/v2/llm/work-types, so every
// sync attempt 404'd and this path was never exercised in anger. That 404
// was load-bearing: it was the only thing preventing the sync's
// destructive DELETE from wiping operator-configured model routes. See
// syncWorkTypesFromACC and migration 831 for the other half of that fix.
//
// The /api/v2/internal/llm/work-types path (rather than the user-facing
// /api/v2/llm/work-types) is deliberate. The user-facing route is mounted
// under RequireAuth, which verifies user JWTs (Casdoor / multi-issuer /
// HS256) and rejects the opaque shared token this sync sends. ACC exposes
// the same payload on a separate static-bearer route for exactly this
// caller; one path can carry only one auth mode, so the two live at
// different paths.
const accWorkTypesPath = "/api/v2/internal/llm/work-types"

type accSyncConfig struct {
	BaseURL      string
	ServiceToken string
}

func loadACCSyncConfig() accSyncConfig {
	base := strings.TrimRight(accFirstNonEmpty(
		os.Getenv("LLM_GATEWAY_ACC_BASE_URL"),
		os.Getenv("ACC_BASE_URL"),
		os.Getenv("ACC_URL"),
	), "/")
	token := accFirstNonEmpty(
		os.Getenv("LLM_GATEWAY_ACC_SERVICE_TOKEN"),
		os.Getenv("ACC_SERVICE_TOKEN"),
		os.Getenv("ACC_TOKEN"),
	)
	return accSyncConfig{BaseURL: base, ServiceToken: token}
}

type accWorkTypePayload struct {
	Key            string       `json:"key"`
	Label          string       `json:"label"`
	Category       string       `json:"category"`
	L1TaskType     string       `json:"l1_task_type"`
	DefaultProfile string       `json:"default_profile"`
	Tags           []string     `json:"tags"`
	PromptKeywords []string     `json:"prompt_keywords"`
	ACCTaskType    *string      `json:"acc_task_type"`
	Enabled        *bool        `json:"enabled"`
	SortOrder      int          `json:"sort_order"`
	ModelRoutes    []modelRoute `json:"model_routes"`
	// 2026-08-12: ACC now emits per-route tier so the operator's two-layer
	// ordering round-trips through sync. We mirror modelRoute.Tier into
	// the DB column instead of relying on the schema default 'secondary'.
}

type accWorkTypesResponse struct {
	OK        bool                 `json:"ok"`
	WorkTypes []accWorkTypePayload `json:"work_types"`
}

type workTypeSyncResult struct {
	Synced   bool      `json:"synced"`
	Message  string    `json:"message"`
	Source   string    `json:"source"`
	SyncedAt time.Time `json:"synced_at,omitempty"`
	Upserted int       `json:"upserted"`
	Routes   int       `json:"routes"`
	Disabled int       `json:"disabled"`
	ACCCount int       `json:"acc_count"`
}

// SyncWorkTypesFromACCForBG is the entry point for optional bg worker.
func SyncWorkTypesFromACCForBG(ctx context.Context, db *pgxpool.Pool) error {
	_, err := syncWorkTypesFromACC(ctx, db)
	return err
}

func fetchACCWorkTypes(ctx context.Context, cfg accSyncConfig) ([]accWorkTypePayload, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("ACC 未配置：请设置 ACC_BASE_URL 或 LLM_GATEWAY_ACC_BASE_URL")
	}
	if cfg.ServiceToken == "" {
		return nil, fmt.Errorf("ACC 凭据未配置：请设置 ACC_SERVICE_TOKEN 或 LLM_GATEWAY_ACC_SERVICE_TOKEN")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+accWorkTypesPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ACC 请求失败: %w", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("ACC 尚未提供 %s 端点 (HTTP 404)", accWorkTypesPath)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("ACC 鉴权失败 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, fmt.Errorf("ACC 返回 HTTP %d: %s", resp.StatusCode, snippet)
	}

	var wrapped accWorkTypesResponse
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.WorkTypes) > 0 {
		return wrapped.WorkTypes, nil
	}
	// ok:true with zero work types is not a legitimate "nothing to do" — it
	// means ACC answered with a shape this parser understood but with no
	// entries, and the previous code returned that as a success with a
	// count of 0. The operator saw "已从 ACC 同步 0 个工作类型" and could
	// not tell it apart from "the sync silently did not happen", which is
	// the more likely explanation for a surprise zero. Failing here makes
	// the two distinguishable, and it fails before any DB write, so a
	// misbehaving or misrouted ACC cannot empty the work-type table.
	//
	// Note this is deliberately NOT the same as "entries whose model_routes
	// are empty". Every ACC entry currently ships an empty model_routes
	// and that is a valid, expected state — the catalog is configuration,
	// routes are the optional per-type model hint. Only the entry list
	// being empty is treated as a fault.
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.OK {
		return nil, fmt.Errorf("ACC 返回 ok=true 但 work_types 为空（期望至少 1 条）；" +
			"请核实 ACC 的 /api/v2/llm/work-types 是否真的挂载在该路径上")
	}

	var direct []accWorkTypePayload
	if err := json.Unmarshal(body, &direct); err == nil && len(direct) > 0 {
		return direct, nil
	}
	return nil, fmt.Errorf("ACC 响应格式无法解析（期望 work_types 数组）")
}

func syncWorkTypesFromACC(ctx context.Context, db *pgxpool.Pool) (workTypeSyncResult, error) {
	cfg := loadACCSyncConfig()
	items, err := fetchACCWorkTypes(ctx, cfg)
	if err != nil {
		return workTypeSyncResult{Synced: false, Message: err.Error(), Source: "acc"}, err
	}

	now := time.Now().UTC()
	syncedKeys := make([]string, 0, len(items))
	upserted := 0
	routes := 0

	tx, err := db.Begin(ctx)
	if err != nil {
		return workTypeSyncResult{}, err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	for _, item := range items {
		key := strings.TrimSpace(item.Key)
		if key == "" || strings.TrimSpace(item.Label) == "" {
			continue
		}
		category := strings.TrimSpace(item.Category)
		if category == "" {
			category = "通用"
		}
		l1 := strings.TrimSpace(item.L1TaskType)
		if l1 == "" {
			l1 = "chat"
		}
		profile := strings.TrimSpace(item.DefaultProfile)
		if profile == "" {
			profile = "smart"
		}
		if profile != "smart" && profile != "speed_first" && profile != "cost_first" {
			profile = "smart"
		}
		enabled := true
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		tags := item.Tags
		if tags == nil {
			tags = []string{}
		}
		kw := item.PromptKeywords
		if kw == nil {
			kw = []string{}
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO work_type_config
			    (key, label, category, l1_task_type, default_profile, tags, prompt_keywords,
			     acc_task_type, enabled, sort_order, synced_from_acc_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW())
			ON CONFLICT (key) DO UPDATE SET
			    label = EXCLUDED.label,
			    category = EXCLUDED.category,
			    l1_task_type = EXCLUDED.l1_task_type,
			    default_profile = EXCLUDED.default_profile,
			    tags = EXCLUDED.tags,
			    prompt_keywords = EXCLUDED.prompt_keywords,
			    acc_task_type = EXCLUDED.acc_task_type,
			    enabled = EXCLUDED.enabled,
			    sort_order = EXCLUDED.sort_order,
			    synced_from_acc_at = EXCLUDED.synced_from_acc_at,
			    updated_at = NOW()
		`, key, item.Label, category, l1, profile, tags, kw,
			item.ACCTaskType, enabled, item.SortOrder, now)
		if err != nil {
			return workTypeSyncResult{}, fmt.Errorf("upsert %s: %w", key, err)
		}
		upserted++
		syncedKeys = append(syncedKeys, key)

		// Delete only the rows a previous ACC sync wrote, never the
		// operator's.
		//
		// This used to be an unqualified DELETE for the whole key, which
		// made the sync a silent data-loss path: the ACC seed carries
		// `model_routes: []` on every one of its 22 entries, so a
		// successful sync removed every route for the key and reinserted
		// nothing, wiping whatever an operator had configured through the
		// admin UI. The result payload reported "0 routes" and nothing
		// warned. Ownership is now tracked per row (migration 831), so a
		// sync manages its own rows and leaves operator rows alone.
		if _, err := tx.Exec(ctx, `DELETE FROM work_type_model_route WHERE work_type_key = $1 AND source = 'acc'`, key); err != nil {
			return workTypeSyncResult{}, err
		}
		for _, rt := range item.ModelRoutes {
			name := strings.TrimSpace(rt.CanonicalName)
			if name == "" {
				continue
			}
			wt := rt.Weight
			if wt <= 0 {
				wt = 1
			}
			// Mirror the putRoutes tier handling: whitelist + default.
			tier := strings.TrimSpace(rt.Tier)
			if tier == "" {
				tier = "secondary"
			}
			if tier != "primary" && tier != "secondary" && tier != "fallback" {
				return workTypeSyncResult{}, fmt.Errorf("sync %s: invalid tier %q for route %s", key, tier, name)
			}
			// ON CONFLICT DO NOTHING, not DO UPDATE: when an operator has
			// already routed this specific model, their row is the
			// authoritative one and the sync must not overwrite it. The
			// old INSERT would have aborted the whole transaction on the
			// UNIQUE (work_type_key, canonical_name) violation, so this is
			// also what keeps a sync from failing outright once operator
			// and ACC route sets overlap.
			if _, err := tx.Exec(ctx, `
				INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier, source)
				VALUES ($1, $2, $3, $4, $5, $6, 'acc')
				ON CONFLICT (work_type_key, canonical_name) DO NOTHING
			`, key, name, wt, rt.MinScore, rt.Enabled, tier); err != nil {
				return workTypeSyncResult{}, err
			}
			routes++
		}
	}

	disabled := 0
	if len(syncedKeys) > 0 {
		tag, err := tx.Exec(ctx, `
			UPDATE work_type_config
			SET enabled = FALSE, updated_at = NOW()
			WHERE synced_from_acc_at IS NOT NULL
			  AND NOT (key = ANY($1))
		`, syncedKeys)
		if err != nil {
			return workTypeSyncResult{}, err
		}
		disabled = int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return workTypeSyncResult{}, err
	}

	msg := fmt.Sprintf("已从 ACC 同步 %d 个工作类型、%d 条模型映射", upserted, routes)
	if disabled > 0 {
		msg += fmt.Sprintf("；禁用 %d 个 ACC 已移除项", disabled)
	}
	// Zero routes is a valid state, not an error: the catalog is
	// configuration and per-type model routes are an optional hint that
	// ACC does not currently send. But "0 条模型映射" sitting in a success
	// message reads like the sync found nothing to do, when what actually
	// happened is that every work type will fall back to its l1_task_type
	// aggregate (autoroute routesForPolicy). Saying so keeps the message
	// honest and saves the next reader from hunting a routing bug that
	// isn't there.
	if routes == 0 && upserted > 0 {
		msg += "（ACC 未提供模型映射，各类型当前回退到 l1_task_type 聚合）"
		slog.Warn("ACC work_type sync completed with zero model routes; "+
			"every work type falls back to its l1_task_type aggregate. "+
			"This is expected while ACC ships an empty model_routes, but it means "+
			"per-work_type tier policy has no effect.", "upserted", upserted)
	}
	return workTypeSyncResult{
		Synced:   true,
		Message:  msg,
		Source:   "acc",
		SyncedAt: now,
		Upserted: upserted,
		Routes:   routes,
		Disabled: disabled,
		ACCCount: len(items),
	}, nil
}

func queryWorkTypeSyncMeta(ctx context.Context, db *pgxpool.Pool) map[string]interface{} {
	meta := map[string]interface{}{
		"source":         "acc",
		"last_synced_at": nil,
		"enabled_count":  0,
		"route_count":    0,
		"acc_configured": loadACCSyncConfig().BaseURL != "",
	}
	var lastSync *time.Time
	_ = db.QueryRow(ctx, `
		SELECT MAX(synced_from_acc_at) FROM work_type_config WHERE synced_from_acc_at IS NOT NULL
	`).Scan(&lastSync)
	if lastSync != nil {
		meta["last_synced_at"] = lastSync.UTC().Format(time.RFC3339)
	}
	var enabled, routeCount int
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM work_type_config WHERE enabled = TRUE`).Scan(&enabled)
	_ = db.QueryRow(ctx, `SELECT COUNT(*) FROM work_type_model_route`).Scan(&routeCount)
	meta["enabled_count"] = enabled
	meta["route_count"] = routeCount
	return meta
}

func accFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
