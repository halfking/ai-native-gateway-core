package autocombo

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/quotafetcher"
)

// NewQuotaFetcherFromManager adapts a *quotafetcher.Manager into the factory's
// QuotaFetcher interface. Lives in autocombo (not quotafetcher) so the returned
// type is autocombo.fetchedQuota directly — avoiding a cross-package named-type
// mismatch. quotafetcher does not import autocombo, so there is no cycle.
//
// The projection drops fields preflightQuota doesn't consume (PercentUsed,
// Windows, Extra). LimitReached is the authoritative short-circuit; Total
// becomes corrected_limit so the existing DB Preflight reads the real cap.
// A Total of 0 (unknown, e.g. balance-only fetch) leaves corrected_limit
// untouched (ApplyFetchedQuota writes NULL).
func NewQuotaFetcherFromManager(mgr *quotafetcher.Manager) QuotaFetcher {
	return quotaFetcherAdapter{mgr: mgr}
}

type quotaFetcherAdapter struct {
	mgr *quotafetcher.Manager
}

func (a quotaFetcherAdapter) FetchQuota(
	ctx context.Context,
	providerCode string,
	credentialID int64,
	providerID int,
	baseURL, apiKey, model, tenantID string,
) (*fetchedQuota, error) {
	qi, err := a.mgr.FetchQuota(ctx, quotafetcher.FetchRequest{
		CredentialID: credentialID,
		ProviderID:   int64(providerID),
		ProviderCode: providerCode,
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Model:        model,
		TenantID:     tenantID,
	})
	if err != nil {
		return nil, err
	}
	if qi == nil {
		return nil, nil // fail-open
	}
	out := &fetchedQuota{
		LimitReached: qi.LimitReached,
		ResetAt:      qi.ResetAt,
		PercentUsed:  qi.PercentUsed,
	}
	if qi.Total > 0 {
		out.Total = int64(qi.Total)
	}
	return out, nil
}

// compile-time guard: fetchedQuota.ResetAt is *time.Time; keep the import live
// even if the field is the only user.
var _ = (*time.Time)(nil)
