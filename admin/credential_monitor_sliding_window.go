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
	slidingWindowBatchMaxItems = 100
	slidingWindowBatchWorkers  = 8
	slidingWindowBatchLimit    = 50 // entries fetched per pair; stats only returned
)

// loadSlidingWindowEntries loads call entries for one credential×model pair.
// Primary: Redis recorder; fallback: request_logs. Always returns a non-nil
// entries slice so JSON never serializes to null.
func (m *CredentialMonitorHandlers) loadSlidingWindowEntries(
	ctx context.Context,
	credentialID int,
	model string,
	minutes, limit int,
) (entries []credentialhealth.CallEntry, source string, err error) {
	if m.recorder == nil && m.redisClient != nil {
		m.recorder = credentialhealth.NewRecorder(m.redisClient, 2*time.Hour, 100)
	}

	source = "redis"
	entries = make([]credentialhealth.CallEntry, 0)
	if m.recorder != nil && m.recorder.Enabled() {
		since := time.Now().Add(-time.Duration(minutes) * time.Minute)
		entries, _ = m.recorder.GetRecent(ctx, credentialID, model, since)
	}

	if len(entries) == 0 {
		source = "request_logs"
		if m.h == nil || m.h.db == nil {
			return entries, source, fmt.Errorf("database not configured")
		}
		rlEntries, rlErr := m.slidingWindowFromRequestLogs(ctx, credentialID, model, minutes, limit)
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

// handleSlidingWindowBatch returns per-pair window stats without entries.
// POST /api/credentials/sliding-window/batch
// Body: {"minutes":5,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}
func (m *CredentialMonitorHandlers) handleSlidingWindowBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Minutes int `json:"minutes"`
		Items   []struct {
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type pair struct {
		idx          int
		credentialID int
		model        string
	}
	type itemResult struct {
		CredentialID int            `json:"credential_id"`
		Model        string         `json:"model"`
		Source       string         `json:"source,omitempty"`
		Stats        map[string]any `json:"stats,omitempty"`
		Error        string         `json:"error,omitempty"`
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
				entries, source, err := m.loadSlidingWindowEntries(ctx, job.credentialID, job.model, minutes, slidingWindowBatchLimit)
				if err != nil {
					results[job.idx].Error = err.Error()
					continue
				}
				stats := credentialhealth.ComputeStats(entries)
				results[job.idx].Source = source
				results[job.idx].Stats = slidingWindowStatsMap(stats)
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
