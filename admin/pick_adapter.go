package admin

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
)

// pickProbeModelForCredentialAdapter calls into the bg package's shared picker
// and reshapes the result to the admin-package type.
// This indirection avoids an import cycle (admin cannot import bg directly,
// but bg has no reverse dependency on admin).
func pickProbeModelForCredentialAdapter(ctx context.Context, db *pgxpool.Pool, credID int) (pickProbeResult, error) {
	r, err := bg.PickProbeModelForCredential(ctx, db, credID)
	if err != nil {
		return pickProbeResult{}, err
	}
	return pickProbeResult{Model: r.Model, Source: r.Source}, nil
}
