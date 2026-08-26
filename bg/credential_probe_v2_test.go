		`AND\s+quota_state\s+NOT\s+IN\s*\(\s*'permanently_exhausted',\s*'balance_exhausted'\s*\)`,
	)
	if oldPattern.MatchString(body) {
		t.Fatalf("writeHealth still has unconditional hard-quota guard without OR-bypass — deadlock regression")
	}
}

func TestWriteHealth_ClosesBindingFailuresWithoutCredentialWideWrite(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"if pr.BindingOnly {",
		"c.writeBindingUnavailable(execCtx, credID, pr)",
		"c.restoreBindingOnProbeSuccess(execCtx, credID, pr.HealthProbeModel)",
		"available := pr.AvailabilityState == \"ready\" && !pr.BindingOnly",
		"state = \"model_binding\"",
		"Available:     pr.AvailabilityState == \"ready\" && !pr.BindingOnly",
		"UPDATE credential_model_bindings cmb",
		"pm.raw_model_name = $2",
		"cmb.unavailable_reason = 'auto_probe_model_binding'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("binding failure closure is missing %q", want)
		}
	}
}

func TestWriteHealth_FansOutBoundRawModels(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"func (c *CredentialProbeV2) loadBoundRawModels(",
		"COALESCE(cmb.available, TRUE) = TRUE",
		"COALESCE(pm.available, TRUE) = TRUE",
		"if err := rows.Err(); err != nil",
		// 2026-08-26 self-check audit: pin the failure-branch call site
		// (the cache mirrors current DB state when the probe is sick).
		// The healthy-ready branch uses loadBoundRawModelsAll instead;
		// covered by TestCredentialProbeV2_CacheFanOutIncludesAllBindings.
		"uniqueStringSet([]string{pr.HealthProbeModel}, c.loadBoundRawModels(execCtx, credID))",
		"for _, model := range writeModels",
		"c.cache.Set(execCtx, credID, model",
		"Model:         model",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("raw-model fan-out is missing %q", want)
		}
	}
	if strings.Contains(body, "cm.archived") || strings.Contains(body, "pm.archived") {
		t.Fatal("raw-model fan-out must not reference nonexistent archived columns")
	}
}

func TestUniqueStringSet(t *testing.T) {
	got := uniqueStringSet([]string{"default", "a", "a"}, []string{"b", "default", ""})
	want := []string{"default", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
