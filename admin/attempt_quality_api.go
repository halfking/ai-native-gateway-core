package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

const attemptQualityAPIPath = "/api/admin/quality/attempts"

type attemptQualityAnalyzer interface {
	Analyze(context.Context, string, time.Time, time.Time) ([]providerprofile.AttemptQualityAggregate, error)
}

// AttemptQualityAPI exposes tenant-scoped attempt metrics without changing the
// existing final-request quality API contract.
type AttemptQualityAPI struct {
	analyzer attemptQualityAnalyzer
	now      func() time.Time
}

func NewAttemptQualityAPI(analyzer attemptQualityAnalyzer) *AttemptQualityAPI {
	return &AttemptQualityAPI{analyzer: analyzer, now: time.Now}
}

func (api *AttemptQualityAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if api == nil || api.analyzer == nil {
		writeError(w, http.StatusServiceUnavailable, "attempt quality analytics unavailable")
		return
	}
	auth := GetAuthContext(r)
	tenantID := ""
	if auth != nil {
		tenantID = strings.TrimSpace(auth.TenantID)
	}
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant is required")
		return
	}
	hours, err := positiveIntQuery(r, "hours", 24, 168)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerID, err := optionalPositiveInt64Query(r, "provider_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	credentialID, err := optionalPositiveInt64Query(r, "credential_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	end := api.now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	aggregates, err := api.analyzer.Analyze(r.Context(), tenantID, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load attempt quality failed: %v", err))
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	filtered := filterAttemptQuality(aggregates, providerID, credentialID, model)
	writeJSON(w, http.StatusOK, attemptQualityResponse{
		TenantID: tenantID, Start: start, End: end, Hours: hours,
		SampleSize: attemptQualitySampleSize(filtered), Aggregates: filtered,
		ObservationStatus: attemptQualityObservationStatus(filtered),
	})
}

type attemptQualityResponse struct {
	TenantID          string                                    `json:"tenant_id"`
	Start             time.Time                                 `json:"start"`
	End               time.Time                                 `json:"end"`
	Hours             int                                       `json:"hours"`
	SampleSize        int                                       `json:"sample_size"`
	ObservationStatus requestjourney.ObservationStatus          `json:"observation_status"`
	Aggregates        []providerprofile.AttemptQualityAggregate `json:"aggregates"`
}

func positiveIntQuery(r *http.Request, name string, defaultValue, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maximum)
	}
	return value, nil
}

func optionalPositiveInt64Query(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func filterAttemptQuality(aggregates []providerprofile.AttemptQualityAggregate, providerID, credentialID int64, model string) []providerprofile.AttemptQualityAggregate {
	filtered := make([]providerprofile.AttemptQualityAggregate, 0, len(aggregates))
	for _, aggregate := range aggregates {
		if providerID != 0 && aggregate.ProviderID != providerID {
			continue
		}
		if credentialID != 0 && aggregate.CredentialID != credentialID {
			continue
		}
		if model != "" && aggregate.Model != model {
			continue
		}
		filtered = append(filtered, aggregate)
	}
	return filtered
}

func attemptQualitySampleSize(aggregates []providerprofile.AttemptQualityAggregate) int {
	total := 0
	for _, aggregate := range aggregates {
		total += aggregate.AttemptTotal
	}
	return total
}

func attemptQualityObservationStatus(aggregates []providerprofile.AttemptQualityAggregate) requestjourney.ObservationStatus {
	for _, aggregate := range aggregates {
		if aggregate.ObservationDegradedCount > 0 {
			return requestjourney.ObservationDegraded
		}
	}
	return requestjourney.ObservationComplete
}
