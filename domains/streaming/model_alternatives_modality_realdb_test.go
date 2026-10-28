package streaming

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFindModelAlternatives_ModalityFilterOnRealData proves the suggestion
// list is actually filtered by modality, against the real catalog.
//
// ★ What breaks without the filter, and why it matters:
//
//	The no_candidate/modality_mismatch response tells the client
//	"this model cannot read your images — use one of these instead". The
//	suggestions were ordered by task type and popularity, NOT by capability,
//	so they routinely contained text-only models. A user following the
//	advice would switch to a text-only model and hit the identical wall —
//	which is exactly the failure this whole change exists to remove.
//
//	On the real catalog the effect is large: of the 8 featured routable
//	models, 4 are text-only, so an unfiltered list sends the user to a dead
//	end half the time.
//
//	Requires TEST_DATABASE_URL; skipped otherwise.
func TestFindModelAlternatives_ModalityFilterOnRealData(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — the modality filter check needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	finder := &ModelAlternativesFinder{db: pool}

	// A vision request must yield ONLY models that can serve vision.
	alts := finder.Find(ctx, "glm-5.3-flash", "default", "", "vision")
	if len(alts.Alternatives) == 0 {
		t.Skip("no routable alternatives in this catalog to check")
	}

	// Independently look up each suggested model's registration.
	for _, alt := range alts.Alternatives {
		var modality string
		err := pool.QueryRow(ctx, `
			SELECT COALESCE(mc.modality, 'text')
			  FROM models_canonical mc
			 WHERE mc.canonical_name = $1`, alt.Model).Scan(&modality)
		if err != nil {
			t.Errorf("%s: look up registration: %v", alt.Model, err)
			continue
		}
		if modality != "vision" && modality != "multimodal" {
			t.Errorf("suggested model %q is registered as %q but was offered for a "+
				"vision request — the client would follow the advice and hit the same wall",
				alt.Model, modality)
		}
	}

	// The filter must not leak into the unfiltered path: a text request
	// keeps the historical behaviour (any routable model may be offered).
	textAlts := finder.Find(ctx, "glm-5.3-flash", "default", "", "")
	if len(textAlts.Alternatives) == 0 {
		t.Error("the unfiltered path returned nothing — the modality filter leaked into it")
	}

	// The result must also SAY which modality it filtered on, so a client
	// can tell "these can read images" from "these are merely up".
	if alts.RequestedModality != "vision" {
		t.Errorf("RequestedModality = %q, want %q", alts.RequestedModality, "vision")
	}
	if textAlts.RequestedModality != "" {
		t.Errorf("unfiltered result carries RequestedModality = %q, want empty",
			textAlts.RequestedModality)
	}
}
