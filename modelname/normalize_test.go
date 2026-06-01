package modelname

import "testing"

func TestNormalizeRouteKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Date suffix stripping
		{in: "gpt-4o-2024-08-06", want: "gpt-4o"},
		{in: "claude-sonnet-4-5-20250929", want: "claude-sonnet-4-5"},
		{in: "glm-4-7-251222", want: "glm-4.7"},
		{in: "glm-4-7-20251201", want: "glm-4.7"},
		{in: "minimax-m2.7-20251201", want: "minimax-m2.7"},
		{in: "MiniMax-M2.7-251222", want: "minimax-m2.7"},

		// Version dot/dash normalization (converts dash to dot in version)
		{in: "glm-4-7", want: "glm-4.7"},
		{in: "minimax-m4-7", want: "minimax-m4.7"},
		{in: "glm-4.7", want: "glm-4.7"},
		{in: "minimax-m4.7", want: "minimax-m4.7"},

		// Feature suffixes preserved
		{in: "glm-4.7-flash", want: "glm-4.7-flash"},
		{in: "glm-4.7-air", want: "glm-4.7-air"},
		{in: "minimax-m2.7-thinking", want: "minimax-m2.7-thinking"},
		{in: "minimax-m4.7-highspeed", want: "minimax-m4.7-highspeed"},

		// Vendor prefix stripping
		{in: "openai/gpt-4o-mini-2024-07-18", want: "gpt-4o-mini"},
		{in: "scnet/minimax-m2.5", want: "minimax-m2.5"},
		{in: "volcengine/glm-4-9b", want: "glm-4-9b"},

		// [1M] suffix stripping
		{in: "claude-sonnet-4-5 [1M]", want: "claude-sonnet-4-5"},
		{in: "gpt-4o [2M]", want: "gpt-4o"},

		// Complex date + version combinations
		{in: "glm-4-5-air-20250728", want: "glm-4.5-air"},
		{in: "glm-4-5-pro-20251201", want: "glm-4.5-pro"},

		// Edge cases
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "GPT-4O", want: "gpt-4o"},
		{in: "MiniMax-M2.7", want: "minimax-m2.7"},
	}
	for _, tc := range tests {
		if got := NormalizeRouteKey(tc.in); got != tc.want {
			t.Fatalf("NormalizeRouteKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeModelRef(t *testing.T) {
	tests := []struct {
		in              string
		wantProvider    string
		wantBaseModel   string
		wantVersion     string
	}{
		// BaseModel strips all non-alphanumeric for consistent matching
		{in: "glm-4.7", wantProvider: "", wantBaseModel: "glm", wantVersion: "4.7"},
		{in: "minimax-m4.7", wantProvider: "", wantBaseModel: "minimax", wantVersion: "4.7"},
		{in: "openai/gpt-4o", wantProvider: "openai", wantBaseModel: "gpt4o", wantVersion: ""},
		{in: "scnet/minimax-m2.5", wantProvider: "scnet", wantBaseModel: "minimax", wantVersion: "2.5"},
		{in: "glm-4-7-251222", wantProvider: "", wantBaseModel: "glm", wantVersion: "4.7"},
		{in: "no-version-model", wantProvider: "", wantBaseModel: "noversionmodel", wantVersion: ""},
	}
	for _, tc := range tests {
		prov, base, ver := NormalizeModelRef(tc.in)
		if prov != tc.wantProvider || base != tc.wantBaseModel || ver != tc.wantVersion {
			t.Fatalf("NormalizeModelRef(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.in, prov, base, ver, tc.wantProvider, tc.wantBaseModel, tc.wantVersion)
		}
	}
}

func TestExtractFeatures(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "glm-4.7-flash", want: []string{"flash"}},
		{in: "glm-4.7-air", want: []string{"air"}},
		{in: "minimax-m2.7-thinking", want: []string{"thinking"}},
		{in: "gpt-4o-mini", want: []string{"mini"}},
		{in: "claude-sonnet-4-5", want: []string{}},
		{in: "glm-4.7-flash-highspeed", want: []string{"flash", "highspeed"}},
	}
	for _, tc := range tests {
		got := ExtractFeatures(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("ExtractFeatures(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i, f := range got {
			if f != tc.want[i] {
				t.Fatalf("ExtractFeatures(%q)[%d] = %q, want %q", tc.in, i, f, tc.want[i])
			}
		}
	}
}

func TestMatchModelOffer(t *testing.T) {
	tests := []struct {
		name        string
		client      string
		offer       string
		shouldMatch bool
	}{
		// Exact match
		{name: "exact_match", client: "gpt-4o", offer: "gpt-4o", shouldMatch: true},
		{name: "case_insensitive", client: "GPT-4O", offer: "gpt-4o", shouldMatch: true},

		// Version separator normalization (dash vs dot)
		{name: "glm-4-7_eq_glm-4.7", client: "glm-4-7", offer: "glm-4.7", shouldMatch: true},
		{name: "glm-4.7_eq_glm-4-7", client: "glm-4.7", offer: "glm-4-7", shouldMatch: true},
		{name: "minimax-m4-7_eq_minimax-m4.7", client: "minimax-m4-7", offer: "minimax-m4.7", shouldMatch: true},

		// Date suffix stripping
		{name: "glm-4-7-251222_matches_glm-4.7", client: "glm-4-7-251222", offer: "glm-4.7", shouldMatch: true},
		{name: "glm-4-7-251222_matches_glm-4-7", client: "glm-4-7-251222", offer: "glm-4-7", shouldMatch: true},
		{name: "glm-4.7_matches_glm-4-7-251222", client: "glm-4.7", offer: "glm-4-7-251222", shouldMatch: true},

		// Feature matching - when client has no features, it should NOT match offer with features
		// because we don't know if the provider supports the non-featured version
		{name: "glm-4.7-flash_self_match", client: "glm-4.7-flash", offer: "glm-4.7-flash", shouldMatch: true},
		{name: "glm-4.7_no_feature_does_not_match_glm-4.7-flash", client: "glm-4.7", offer: "glm-4.7-flash", shouldMatch: false},
		{name: "glm-4.7-flash_does_not_match_glm-4.7-air", client: "glm-4.7-flash", offer: "glm-4.7-air", shouldMatch: false},
		{name: "glm-4.7-air_does_not_match_glm-4.7", client: "glm-4.7-air", offer: "glm-4.7", shouldMatch: false},

		// Version mismatch
		{name: "glm-4.5_does_not_match_glm-4.7", client: "glm-4.5", offer: "glm-4.7", shouldMatch: false},
		{name: "minimax-m2.5_does_not_match_minimax-m2.7", client: "minimax-m2.5", offer: "minimax-m2.7", shouldMatch: false},
		{name: "glm-4-5-air_matches_glm-4.5-air", client: "glm-4-5-air", offer: "glm-4.5-air", shouldMatch: true},
		{name: "glm-4-5-air-20250728_matches_glm-4.5-air", client: "glm-4-5-air-20250728", offer: "glm-4.5-air", shouldMatch: true},

		// Family mismatch
		{name: "gpt-4o_does_not_match_gpt-4o-mini", client: "gpt-4o", offer: "gpt-4o-mini", shouldMatch: false},
		{name: "gpt-4o-mini_does_not_match_gpt-4o", client: "gpt-4o-mini", offer: "gpt-4o", shouldMatch: false},

		// Vendor prefix stripping for matching
		{name: "scnet_minimax_m2.5_matches_minimax_m2.5", client: "scnet/minimax-m2.5", offer: "minimax-m2.5", shouldMatch: true},

		// Complex real-world scenarios (the SCNET bug scenario)
		{name: "minimax-m4.7_should_not_match_minimax-m2.5", client: "minimax-m4.7", offer: "minimax-m2.5", shouldMatch: false},
		{name: "minimax-m4.7_should_not_match_scnet_minimax_m2.5", client: "minimax-m4.7", offer: "scnet/minimax-m2.5", shouldMatch: false},
		{name: "minimax-m2.7_matches_minimax_m2.7", client: "minimax-m2.7", offer: "minimax-m2.7", shouldMatch: true},
		{name: "minimax-m2.7_matches_MiniMax-M2.7", client: "minimax-m2.7", offer: "MiniMax-M2.7", shouldMatch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchModelOffer(tc.client, tc.offer)
			if got != tc.shouldMatch {
				t.Fatalf("MatchModelOffer(%q, %q) = %v, want %v",
					tc.client, tc.offer, got, tc.shouldMatch)
			}
		})
	}
}

func TestMatchModelOffer_FromTestFile(t *testing.T) {
	scnetOffers := []string{"MiniMax-M2.7", "MiniMax-M2.5", "minimax-m2.7", "scnet/minimax-m2.5"}
	requestModel := "minimax-m2.7"

	for _, offer := range scnetOffers {
		matched := MatchModelOffer(requestModel, offer)
		if requestModel == "minimax-m2.7" && (offer == "MiniMax-M2.7" || offer == "minimax-m2.7") {
			if !matched {
				t.Errorf("expected MatchModelOffer(%q, %q) = true", requestModel, offer)
			}
		}
		if offer == "MiniMax-M2.5" || offer == "minimax-m2.5" || offer == "scnet/minimax-m2.5" {
			if matched {
				t.Errorf("expected MatchModelOffer(%q, %q) = false", requestModel, offer)
			}
		}
	}
}

func TestHasOverlap(t *testing.T) {
	tests := []struct {
		client []string
		offer  []string
		want   bool
	}{
		{client: []string{"flash"}, offer: []string{"flash"}, want: true},
		{client: []string{"flash"}, offer: []string{"air"}, want: false},
		{client: []string{}, offer: []string{"flash"}, want: true},
		{client: []string{"flash", "highspeed"}, offer: []string{"flash"}, want: true},
		{client: []string{"flash", "highspeed"}, offer: []string{"air"}, want: false},
	}
	for i, tc := range tests {
		got := hasOverlap(tc.client, tc.offer)
		if got != tc.want {
			t.Fatalf("test[%d] hasOverlap(%v, %v) = %v, want %v",
				i, tc.client, tc.offer, got, tc.want)
		}
	}
}