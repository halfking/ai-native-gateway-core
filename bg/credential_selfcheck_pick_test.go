package bg

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/recentmodels"
	"github.com/pashagolub/pgxmock/v4"
)

func newSelfcheckMock(t *testing.T) (*CredentialSelfcheckWorker, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	return &CredentialSelfcheckWorker{db: mock}, mock
}

var scErrNoRows = pgx.ErrNoRows

func TestSelectSelfcheckPrimaryPrefersHighestRecentUsage(t *testing.T) {
	bindings := []selfcheckBinding{
		{raw: "featured-low", standardized: "featured-low"},
		{raw: "popular-high", standardized: "popular-high"},
	}
	model, strategy := selectSelfcheckPrimary(bindings, []string{"featured-low"}, []recentmodels.Entry{
		{Model: "featured-low", Count: 5},
		{Model: "popular-high", Count: 10},
	})
	if model != "popular-high" || strategy != "recent" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want popular-high/recent", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryUsesFeaturedTieBreak(t *testing.T) {
	bindings := []selfcheckBinding{
		{raw: "featured-model", standardized: "featured-model"},
		{raw: "same-score", standardized: "same-score"},
	}
	model, strategy := selectSelfcheckPrimary(bindings, []string{"featured-model"}, []recentmodels.Entry{
		{Model: "featured-model", Count: 8},
		{Model: "same-score", Count: 8},
	})
	if model != "featured-model" || strategy != "featured" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want featured-model/featured", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryMatchesStandardizedName(t *testing.T) {
	model, strategy := selectSelfcheckPrimary(
		[]selfcheckBinding{{raw: "vendor/gpt-4o", standardized: "gpt-4o"}},
		[]string{"gpt-4o"}, nil,
	)
	if model != "vendor/gpt-4o" || strategy != "featured" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want raw binding matched by standard name", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryNeverChoosesUnrankedModel(t *testing.T) {
	model, strategy := selectSelfcheckPrimary(
		[]selfcheckBinding{{raw: "obscure-model", standardized: "obscure-model"}}, nil, nil,
	)
	if model != "" || strategy != "" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want no random fallback", model, strategy)
	}
}

func TestPickDueCredentialFiltersByCredentialRun(t *testing.T) {
	src, err := os.ReadFile("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "scr.model_name = 'cred-' || c.id::text") {
		t.Fatal("per-credential self-check watermark filter missing")
	}
}

func TestSelfcheckUsesSharedSevenDayModelSource(t *testing.T) {
	src, err := os.ReadFile("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"recentmodels.Read",
		"interval '7 days'",
		"NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)",
		"unavailable_recover_at <= now()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("self-check picker missing %q", want)
		}
	}
	if strings.Contains(body, "credential_most_used_model") || strings.Contains(body, "rng.Shuffle") {
		t.Fatal("self-check must not use the old 24h or random model fallback")
	}
}

func TestCredentialSelfcheckDefaultsToLoopbackGateway(t *testing.T) {
	t.Setenv("LLM_GATEWAY_SELF_CHECK_BASE_URL", "")
	w := NewCredentialSelfcheckWorker(nil, "", "")
	if w.baseURL != "http://127.0.0.1:8781/v1" {
		t.Fatalf("base URL = %q", w.baseURL)
	}
}
