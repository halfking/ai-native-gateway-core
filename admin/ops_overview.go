package admin

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const opsOverviewCacheTTL = 15 * time.Second

type opsOverviewCache struct {
	mu      sync.Mutex
	expires time.Time
	payload map[string]any
}

func (h *Handler) ensureOpsOverviewCache() *opsOverviewCache {
	if h.opsOverviewCache == nil {
		h.opsOverviewCache = &opsOverviewCache{}
	}
	return h.opsOverviewCache
}

func (c *opsOverviewCache) get() (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.payload == nil || time.Now().After(c.expires) {
		return nil, false
	}
	out := make(map[string]any, len(c.payload))
	for k, v := range c.payload {
		out[k] = v
	}
	return out, true
}

func (c *opsOverviewCache) set(payload map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payload = payload
	c.expires = time.Now().Add(opsOverviewCacheTTL)
}

// handleOpsOverview serves GET /api/admin/ops/overview — a single bundle
// endpoint for OpsOverviewView to avoid 9 parallel round-trips.
func (h *Handler) handleOpsOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	cache := h.ensureOpsOverviewCache()
	if cached, ok := cache.get(); ok {
		w.Header().Set("Cache-Control", "private, max-age=15")
		writeJSON(w, http.StatusOK, cached)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	payload, err := h.buildOpsOverviewPayload(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cache.set(payload)
	w.Header().Set("Cache-Control", "private, max-age=15")
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) buildOpsOverviewPayload(ctx context.Context) (map[string]any, error) {
	type result struct {
		key string
		val any
		err error
	}

	queries := []struct {
		key string
		fn  func(context.Context) (any, error)
	}{
		{"center_stats", h.queryOpsCenterStats},
		{"region_stats", h.queryOpsRegionStats},
		{"deployment_nodes", h.queryOpsDeploymentNodes},
		{"data_plane_tables", h.queryOpsDataPlaneTables},
		{"license_total", h.queryOpsLicenseTotal},
		{"offline_requests", h.queryOpsOfflineRequests},
		{"fault_stats", h.queryOpsFaultStats},
		{"recent_faults", h.queryOpsRecentFaults},
		{"recent_upgrades", h.queryOpsRecentUpgrades},
		{"download_stats", h.queryOpsDownloadStats},
	}

	ch := make(chan result, len(queries))
	var wg sync.WaitGroup
	for _, q := range queries {
		wg.Add(1)
		go func(key string, fn func(context.Context) (any, error)) {
			defer wg.Done()
			val, err := fn(ctx)
			ch <- result{key: key, val: val, err: err}
		}(q.key, q.fn)
	}
	wg.Wait()
	close(ch)

	payload := map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339)}
	var firstErr error
	for res := range ch {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		payload[res.key] = res.val
	}
	if firstErr != nil && len(payload) <= 1 {
		return nil, firstErr
	}
	return payload, nil
}

func (h *Handler) queryOpsCenterStats(ctx context.Context) (any, error) {
	var total, online, offline, degraded int
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'online')::int,
			COUNT(*) FILTER (WHERE status = 'offline')::int,
			COUNT(*) FILTER (WHERE status = 'degraded')::int
		FROM gateway_instances
	`).Scan(&total, &online, &offline, &degraded)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"total_instances":    total,
		"online_instances":   online,
		"offline_instances":  offline,
		"degraded_instances": degraded,
	}, nil
}

func (h *Handler) queryOpsLicenseTotal(ctx context.Context) (any, error) {
	var total int
	err := h.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM licenses WHERE revoked_at IS NULL`).Scan(&total)
	return total, err
}

func (h *Handler) queryOpsRegionStats(ctx context.Context) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(NULLIF(TRIM(region), ''), 'unknown') AS region,
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'online')::int,
			COUNT(*) FILTER (WHERE status = 'offline')::int,
			COUNT(*) FILTER (WHERE status = 'degraded')::int,
			MAX(last_heartbeat) AS last_heartbeat
		FROM gateway_instances
		GROUP BY 1
		ORDER BY 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byRegion := make(map[string]map[string]any)
	for rows.Next() {
		var (
			region                                    string
			total, online, offline, degraded          int
			lastHeartbeat                             *time.Time
		)
		if err := rows.Scan(&region, &total, &online, &offline, &degraded, &lastHeartbeat); err != nil {
			return nil, err
		}
		item := map[string]any{
			"region":             region,
			"total_instances":    total,
			"online_instances":   online,
			"offline_instances":  offline,
			"degraded_instances": degraded,
		}
		if lastHeartbeat != nil {
			item["last_heartbeat"] = *lastHeartbeat
		}
		byRegion[region] = item
	}

	expected := []string{"local", "245", "154"}
	items := make([]map[string]any, 0, len(expected))
	for _, region := range expected {
		if item, ok := byRegion[region]; ok {
			items = append(items, item)
			delete(byRegion, region)
			continue
		}
		items = append(items, map[string]any{
			"region":             region,
			"total_instances":    0,
			"online_instances":   0,
			"offline_instances":  0,
			"degraded_instances": 0,
			"missing":            true,
		})
	}
	for region, item := range byRegion {
		items = append(items, item)
		_ = region
	}
	return items, nil
}

func (h *Handler) queryOpsDeploymentNodes(ctx context.Context) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT instance_id, hostname, ip_address,
		       COALESCE(NULLIF(TRIM(region), ''), 'unknown') AS region,
		       version, build_seq, status, started_at, last_heartbeat
		FROM gateway_instances
		ORDER BY last_heartbeat DESC
		LIMIT 30
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []map[string]any
	for rows.Next() {
		var (
			instanceID, hostname, ipAddress, region, version, status string
			buildSeq                                                 int
			startedAt, lastHeartbeat                                 time.Time
		)
		if err := rows.Scan(&instanceID, &hostname, &ipAddress, &region, &version, &buildSeq, &status, &startedAt, &lastHeartbeat); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"instance_id":    instanceID,
			"hostname":       hostname,
			"ip_address":     ipAddress,
			"region":         region,
			"version":        version,
			"build_seq":      buildSeq,
			"status":         status,
			"started_at":     startedAt,
			"last_heartbeat": lastHeartbeat,
		})
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

func (h *Handler) queryOpsDataPlaneTables(ctx context.Context) (any, error) {
	type tableSpec struct {
		key   string
		query string
	}
	specs := []tableSpec{
		{"gateway_instances", `SELECT COUNT(*)::int FROM gateway_instances`},
		{"instance_heartbeats", `SELECT COUNT(*)::int FROM instance_heartbeats`},
		{"download_events", `SELECT COUNT(*)::int FROM download_events`},
		{"offline_activation_requests", `SELECT COUNT(*)::int FROM offline_activation_requests`},
		{"license_devices", `SELECT COUNT(*)::int FROM license_devices`},
		{"licenses", `SELECT COUNT(*)::int FROM licenses WHERE revoked_at IS NULL`},
	}
	out := make(map[string]int, len(specs))
	for _, spec := range specs {
		var count int
		if err := h.db.QueryRow(ctx, spec.query).Scan(&count); err != nil {
			out[spec.key] = -1
			continue
		}
		out[spec.key] = count
	}
	return out, nil
}

func (h *Handler) queryOpsOfflineRequests(ctx context.Context) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT license_key, hardware_hash, instance_id, device_name, request_id,
		       created_at, approved_at, activation_code, COALESCE(status, 'pending')
		FROM offline_activation_requests
		ORDER BY created_at DESC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []map[string]any
	for rows.Next() {
		var (
			licenseKey, hardwareHash, instanceID, deviceName, requestID, status string
			activationCode                                                      *string
			createdAt                                                           time.Time
			approvedAt                                                          *time.Time
		)
		if err := rows.Scan(&licenseKey, &hardwareHash, &instanceID, &deviceName, &requestID,
			&createdAt, &approvedAt, &activationCode, &status); err != nil {
			return nil, err
		}
		item := map[string]any{
			"license_key":   licenseKey,
			"hardware_hash": hardwareHash,
			"instance_id":   instanceID,
			"device_name":   deviceName,
			"request_id":    requestID,
			"timestamp":     createdAt,
			"created_at":    createdAt,
			"status":        status,
		}
		if approvedAt != nil {
			item["approved_at"] = *approvedAt
		}
		if activationCode != nil {
			item["activation_code"] = *activationCode
		}
		items = append(items, item)
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

func (h *Handler) queryOpsFaultStats(ctx context.Context) (any, error) {
	var openEvents int
	err := h.db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM fault_events
		WHERE status IN ('new', 'acknowledged', 'resolving')
	`).Scan(&openEvents)
	return map[string]int{"open_events": openEvents}, err
}

func (h *Handler) queryOpsRecentFaults(ctx context.Context) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, status, title, description,
		       source, detected_at
		FROM fault_events
		WHERE status = 'new'
		ORDER BY detected_at DESC
		LIMIT 5
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var (
			id, ruleID                                                        int64
			ruleName, severity, status, title, description, source            string
			detectedAt                                                        time.Time
		)
		if err := rows.Scan(&id, &ruleID, &ruleName, &severity, &status, &title, &description, &source, &detectedAt); err != nil {
			return nil, err
		}
		events = append(events, map[string]any{
			"id": id, "rule_id": ruleID, "rule_name": ruleName,
			"severity": severity, "status": status, "title": title,
			"description": description, "source": source, "detected_at": detectedAt,
		})
	}
	if events == nil {
		events = []map[string]any{}
	}
	return map[string]any{"events": events, "total": len(events)}, nil
}

func (h *Handler) queryOpsRecentUpgrades(ctx context.Context) (any, error) {
	rows, err := h.db.Query(ctx, `
		SELECT instance_id, status,
		       COALESCE(NULLIF(new_version, ''), NULLIF(old_version, ''), ''), started_at, completed_at
		FROM upgrade_logs
		ORDER BY started_at DESC
		LIMIT 20
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []map[string]any
	for rows.Next() {
		var (
			instanceID, status, version string
			startedAt                    time.Time
			completedAt                  *time.Time
		)
		if err := rows.Scan(&instanceID, &status, &version, &startedAt, &completedAt); err != nil {
			return nil, err
		}
		item := map[string]any{
			"instance_id": instanceID,
			"status":      status,
			"version":     version,
			"started_at":  startedAt,
		}
		if completedAt != nil {
			item["completed_at"] = *completedAt
		}
		items = append(items, item)
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}

func (h *Handler) queryOpsDownloadStats(ctx context.Context) (any, error) {
	var todayDownloads, weekDownloads, totalDownloads, supporterCount int
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE created_at >= date_trunc('day', now()))::int,
			COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days')::int,
			COUNT(*)::int,
			COUNT(DISTINCT holder_id) FILTER (WHERE holder_id IS NOT NULL)::int
		FROM download_events
	`).Scan(&todayDownloads, &weekDownloads, &totalDownloads, &supporterCount)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"today_downloads":  todayDownloads,
		"week_downloads":   weekDownloads,
		"total_downloads":  totalDownloads,
		"supporter_count":  supporterCount,
	}, nil
}
