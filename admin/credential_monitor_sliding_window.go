package admin

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
)

const (
	slidingWindowBatchMaxItems    = 100
	slidingWindowBatchWorkers     = 8
	slidingWindowBatchLimit       = 50 // entries fetched per pair for stats
	slidingWindowBatchMaxEntryRet = 50 // hard cap when include_entries is true (queue-perspective model header shows 50 icons)
)

// loadSlidingWindowEntries loads call entries for one credential×model pair.
// Primary: Redis recorder; fallback: session_turns. Always returns a non-nil
// entries slice so JSON never serializes to null.
//
// ★ 2026-10-24（v1 → session_turns 替换轮）：回退腿读的是会话族，值却写着
//
//	"_request_logs" ⇒ 运营读到一个不存在的表名 ⇒ 改成 "session_turns"。
//
// 2026-10-16 audit #153: tenantID + isTenantAdmin thread the caller's tenant
// scope down to the SQL filter. Redis entries carry no tenant tag, so for
// tenant_admin callers we skip the Redis fast path and force the DB fallback
// (which carries `AND ($5 = ” OR tenant_id = $5)`).
func (m *CredentialMonitorHandlers) loadSlidingWindowEntries(
	ctx context.Context,
	credentialID int,
	model string,
	minutes, limit int,
	tenantID string,
	isTenantAdmin bool,
) (entries []credentialhealth.CallEntry, source string, err error) {
	if m.recorder == nil && m.redisClient != nil {
		m.recorder = credentialhealth.NewRecorder(m.redisClient, 2*time.Hour, 100)
	}

	source = "redis"
	entries = make([]credentialhealth.CallEntry, 0)
	if !isTenantAdmin && m.recorder != nil && m.recorder.Enabled() {
		since := time.Now().Add(-time.Duration(minutes) * time.Minute)
		entries, _ = m.recorder.GetRecent(ctx, credentialID, model, since)
	}

	if len(entries) == 0 {
		source = "session_turns"
		if m.h == nil || m.h.db == nil {
			return entries, source, fmt.Errorf("database not configured")
		}
		rlEntries, rlErr := m.slidingWindowFromRequestLogs(ctx, credentialID, model, minutes, limit, tenantID)
		if rlErr != nil {
			return nil, source, rlErr
		}
		entries = rlEntries
	}
	if entries == nil {
		entries = make([]credentialhealth.CallEntry, 0)
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, source, nil
}

func slidingWindowStatsMap(stats credentialhealth.Stats) map[string]any {
	return map[string]any{
		"total":        stats.Total,
		"success":      stats.Success,
		"failed":       stats.Failed,
		"failure_rate": stats.FailureRate,
		"error_kinds":  stats.ErrorKinds,
	}
}

// handleSlidingWindowBatch returns per-pair window stats; optionally entries.
// POST /api/credentials/sliding-window/batch
// Body: {"minutes":5,"include_entries":true,"entry_limit":24,"items":[...]}
func (m *CredentialMonitorHandlers) handleSlidingWindowBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 12h-audit 20261017: same role gate as the single-credential endpoint —
	// per-item entries carry cross-tenant per-call detail (Redis fast path has
	// no tenant tag), so role=user must not reach the batch fan-out either.
	if !IsTenantAdmin(r) && !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "administrator role required")
		return
	}

	var req struct {
		Minutes        int  `json:"minutes"`
		IncludeEntries bool `json:"include_entries"`
		EntryLimit     int  `json:"entry_limit"`
		Items          []struct {
			CredentialID int    `json:"credential_id"`
			Model        string `json:"model"`
		} `json:"items"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Items == nil {
		writeError(w, http.StatusBadRequest, "items required")
		return
	}
	if len(req.Items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"window_minutes": 5,
			"count":          0,
			"results":        []any{},
		})
		return
	}
	if len(req.Items) > slidingWindowBatchMaxItems {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("items must be at most %d", slidingWindowBatchMaxItems))
		return
	}

	minutes := req.Minutes
	if minutes <= 0 {
		minutes = 5
	}
	if minutes > 1440 {
		writeError(w, http.StatusBadRequest, "minutes must be 1-1440")
		return
	}

	entryLimit := 0
	if req.IncludeEntries {
		entryLimit = req.EntryLimit
		if entryLimit <= 0 {
			entryLimit = 24
		}
		if entryLimit > slidingWindowBatchMaxEntryRet {
			entryLimit = slidingWindowBatchMaxEntryRet
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type pair struct {
		idx          int
		credentialID int
		model        string
	}
	type itemResult struct {
		CredentialID int                           `json:"credential_id"`
		Model        string                        `json:"model"`
		Source       string                        `json:"source,omitempty"`
		Stats        map[string]any                `json:"stats,omitempty"`
		Entries      *[]credentialhealth.CallEntry `json:"entries,omitempty"`
		Error        string                        `json:"error,omitempty"`
	}

	work := make([]pair, 0, len(req.Items))
	results := make([]itemResult, len(req.Items))
	for i, it := range req.Items {
		results[i] = itemResult{CredentialID: it.CredentialID, Model: it.Model}
		if it.CredentialID == 0 || it.Model == "" {
			results[i].Error = "credential_id and model required"
			continue
		}
		work = append(work, pair{idx: i, credentialID: it.CredentialID, model: it.Model})
	}

	// 2026-10-16 audit #153: compute tenant scope once for the whole batch —
	// caller-side, not per-item (the batch is one caller, many pairs).
	tenantID := ""
	isTenantAdmin := IsTenantAdmin(r)
	if isTenantAdmin {
		tenantID = GetTenantID(r)
	}

	jobs := make(chan pair, len(work))
	for _, job := range work {
		jobs <- job
	}
	close(jobs)

	workers := slidingWindowBatchWorkers
	if workers > len(work) {
		workers = len(work)
	}
	if workers < 1 {
		workers = 0
	}
	var wg sync.WaitGroup
	for wkr := 0; wkr < workers; wkr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				entries, source, err := m.loadSlidingWindowEntries(ctx, job.credentialID, job.model, minutes, slidingWindowBatchLimit, tenantID, isTenantAdmin)
				if err != nil {
					results[job.idx].Error = err.Error()
					continue
				}
				stats := credentialhealth.ComputeStats(entries)
				results[job.idx].Source = source
				results[job.idx].Stats = slidingWindowStatsMap(stats)
				results[job.idx].Entries = slidingWindowJSONEntries(req.IncludeEntries, entries, entryLimit)
			}
		}()
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{
		"window_minutes": minutes,
		"count":          len(results),
		"results":        results,
	})
}

// slidingWindowJSONEntries keeps include_entries=false omitted, while empty
// windows still serialize as "entries":[] so clients can clear stale cells.
func slidingWindowJSONEntries(include bool, entries []credentialhealth.CallEntry, limit int) *[]credentialhealth.CallEntry {
	if !include {
		return nil
	}
	out := entries
	if out == nil {
		out = []credentialhealth.CallEntry{}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return &out
}
