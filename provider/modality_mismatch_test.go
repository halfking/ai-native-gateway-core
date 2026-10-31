package provider

import (
	"context"
	"strings"
	"testing"
)

// TestModalityAccepts_PinsTheAcceptanceTable pins the acceptance rule the
// router's modality gate and the error diagnosis both rely on.
//
// ★ Why this table must be pinned exactly, and why it lives in the provider
// package (not next to the caller):
//
//	provider/candidate_query_sql_base admits a request when
//	    $3 = ''                       → everything
//	    $3 = 'text'                   → everything
//	    $3 = 'multimodal'             → modality = 'multimodal'
//	    $3 = 'image'                  → modality IN ('image','vision','multimodal')
//	    $3 IN ('vision','audio','video') → modality IN ($3,'multimodal')
//
//	provider/modality_mismatch.go's ModalityAccepts must agree with it,
//	because the diagnosis uses it to decide whether to tell the client
//	"this model cannot read your images". If it disagrees with the router,
//	the error message blames a cause the router never had — strictly worse
//	than the old generic 503, because it sends the user to fix the wrong
//	thing.
//
//	The mutation arm for this table: making ModalityAccepts always return
//	true must fail domains/streaming's cross-check test
//	(TestModalityAccepts_MatchesPublishedTable).
func TestModalityAccepts_PinsTheAcceptanceTable(t *testing.T) {
	cases := []struct {
		registered string
		requested  string
		want       bool
	}{
		// Text requests are served by everything.
		{"text", "text", true},
		{"multimodal", "text", true},
		{"vision", "text", true},
		{"", "text", true},

		// An empty request modality means "no filter" — the router admits
		// everything, so the diagnosis must not call it a mismatch.
		{"text", "", true},
		{"multimodal", "", true},
		{"vision", "", true},

		// Media requests need a matching or multimodal registration.
		{"text", "vision", false},
		{"text", "audio", false},
		{"text", "video", false},
		{"vision", "vision", true},
		{"vision", "audio", false},
		{"vision", "video", false},
		{"audio", "audio", true},
		{"audio", "vision", false},
		{"video", "video", true},
		{"video", "vision", false},
		{"multimodal", "vision", true},
		{"multimodal", "audio", true},
		{"multimodal", "video", true},

		// The image endpoint's modality has its own arm.
		{"image", "image", true},
		{"vision", "image", true},
		{"multimodal", "image", true},
		{"text", "image", false},
		{"audio", "image", false},

		// A multimodal request needs a multimodal registration specifically.
		{"multimodal", "multimodal", true},
		{"vision", "multimodal", false},
		{"text", "multimodal", false},

		// Case and whitespace are normalised before comparison.
		{"TEXT", "VISION", false},
		{" text ", " text ", true},
		{"Multimodal", "VISION", true},
	}

	for _, tc := range cases {
		if got := ModalityAccepts(tc.registered, tc.requested); got != tc.want {
			t.Errorf("ModalityAccepts(%q, %q) = %v, want %v",
				tc.registered, tc.requested, got, tc.want)
		}
	}
}

// TestModalityMismatch_IsZeroSemantics documents the nil/empty contract the
// handler branches on.
//
// A nil mismatch and an empty mismatch both mean "not a modality problem".
// The handler calls IsZero() on a value that may be nil (the resolver
// returns nil when the model simply has no live binding), so IsZero must be
// nil-safe.
func TestModalityMismatch_IsZeroSemantics(t *testing.T) {
	var nilMismatch *ModalityMismatch
	if !nilMismatch.IsZero() {
		t.Error("nil mismatch must report IsZero() = true")
	}
	if !(&ModalityMismatch{}).IsZero() {
		t.Error("a mismatch with no requested modality must report IsZero() = true")
	}
	if (&ModalityMismatch{RequestedModality: "vision"}).IsZero() {
		t.Error("a mismatch with a requested modality must report IsZero() = false")
	}
}

// TestModalityMismatchSQL_Shape guards the diagnosis query's shape.
//
// ★ The query answers two things the error message depends on: how the
// model is registered, and whether it is routable at all when the modality
// filter is dropped. The second is the load-bearing part — without it the
// diagnosis would fire for models that are simply unavailable, and the client
// would be told to change a request that was never the problem.
//
// The assertions are on STRUCTURE (which tables and predicates appear), not
// on literal text: a reformat must not fail the test, but dropping the
// is_routable gate must.
func TestModalityMismatchSQL_Shape(t *testing.T) {
	sql := modalityMismatchSQL

	required := []string{
		"models_canonical",             // where the registered modality lives
		"mc.modality",                  // the registration itself
		"v_routable_credential_models", // the router's own availability gate
		"is_routable",                  // ...and its predicate
	}
	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Errorf("diagnosis SQL is missing %q — the diagnosis and the router "+
				"must read the same facts", needle)
		}
	}

	// The query must NOT re-apply a modality filter: its whole purpose is to
	// ask "would this model be routable if the modality gate were removed?".
	if strings.Contains(sql, "mc.modality IN") {
		t.Error("diagnosis SQL must not filter by modality — that is the gate it is diagnosing")
	}
}

// TestModelModalityMismatch_ShortCircuitsOnText keeps the diagnosis off the
// hot path for the common case.
//
// A text request is never excluded by the modality gate, so the diagnosis
// must not even reach the database for it. The check is behavioural: the
// resolver returns before touching the DB, which is why a nil dbPool does
// not produce an error for a text request.
func TestModelModalityMismatch_ShortCircuitsOnText(t *testing.T) {
	c := &Client{} // dbPool == nil: any query attempt would panic/fail
	ctx := context.Background()

	for _, mod := range []string{"", "text", "TEXT", " text "} {
		got, err := c.ModelModalityMismatch(ctx, "glm-5.3-flash", "", "default", mod)
		if err != nil {
			t.Errorf("modality %q: unexpected error %v", mod, err)
		}
		if !got.IsZero() {
			t.Errorf("modality %q: expected no mismatch for a text request, got %+v", mod, got)
		}
	}
}
