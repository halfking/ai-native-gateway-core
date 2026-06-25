// Command gateway - main_v2_pipeline_test.go (2026-06-26)
//
// Feature-flag unit tests for the /v2/* opt-in route group. These tests
// pin down the production safety contract:
//
//   - Default OFF: LLM_GATEWAY_V2_ENABLED unset → no /v2 routes registered.
//   - Explicit OFF: env="false"/"0"/etc → no /v2 routes registered.
//   - Explicit ON: env="true"/"1"/"yes" → /v2/chat/completions + /v2/healthz
//     are reachable. v1 routes are untouched (mux isolation).
//
// All tests use an isolated http.ServeMux (NOT the global parent) so they
// do not depend on the package-level main() startup sequence.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withEnv sets an env var for the duration of a test and restores it on
// cleanup. t.Setenv handles parallel-safety and automatic restoration.
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// unsetEnv clears an env var for the duration of a test.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
}

// TestV2Pipeline_DefaultOff_NoRoutes verifies the production safety
// guarantee: when LLM_GATEWAY_V2_ENABLED is not set, the v2 sub-mux is
// not built at all. This is the default in every environment.
func TestV2Pipeline_DefaultOff_NoRoutes(t *testing.T) {
	unsetEnv(t, "LLM_GATEWAY_V2_ENABLED")

	if v2PipelineEnabled() {
		t.Fatal("v2PipelineEnabled() must return false when LLM_GATEWAY_V2_ENABLED is unset")
	}

	handler, deps, ok := v2PipelineSubMux()
	if ok {
		t.Fatalf("v2PipelineSubMux() must report !ok when flag is unset; got ok=%v deps=%v", ok, deps)
	}
	if handler != nil {
		t.Fatalf("v2PipelineSubMux() must return nil handler when flag is unset; got %T", handler)
	}
}

// TestV2Pipeline_ExplicitFalse_NoRoutes verifies that explicit "false"/
// "0" values do not enable the v2 pipeline. Only true-ish values do.
func TestV2Pipeline_ExplicitFalse_NoRoutes(t *testing.T) {
	cases := []string{"false", "FALSE", "0", "no", "off", " ", "garbage"}
	for _, v := range cases {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			withEnv(t, "LLM_GATEWAY_V2_ENABLED", v)
			if v2PipelineEnabled() {
				t.Fatalf("v2PipelineEnabled() must return false for value %q", v)
			}
			_, _, ok := v2PipelineSubMux()
			if ok {
				t.Fatalf("v2PipelineSubMux() must return !ok for value %q", v)
			}
		})
	}
}

// TestV2Pipeline_ExplicitTrue_RegistersRoutes verifies that all true-ish
// values (1/true/yes in any case) enable the v2 sub-mux and register both
// /v2/chat/completions and /v2/healthz.
func TestV2Pipeline_ExplicitTrue_RegistersRoutes(t *testing.T) {
	cases := []string{"true", "TRUE", "1", "yes", "YES", "  true  "}
	for _, v := range cases {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			withEnv(t, "LLM_GATEWAY_V2_ENABLED", v)
			if !v2PipelineEnabled() {
				t.Fatalf("v2PipelineEnabled() must return true for value %q", v)
			}

			handler, deps, ok := v2PipelineSubMux()
			if !ok {
				t.Fatal("v2PipelineSubMux() must report ok=true when flag is set")
			}
			if handler == nil {
				t.Fatal("v2PipelineSubMux() must return non-nil handler")
			}
			if deps == nil || deps.Pipeline == nil {
				t.Fatal("deps.Pipeline must be non-nil after sub-mux build")
			}
			if deps.Pipeline == nil || len(deps.Pipeline.Stages()) == 0 {
				t.Fatal("Pipeline must have at least one stage")
			}

			// /v2/healthz is the simplest sanity check.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v2/healthz", nil)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("/v2/healthz returned %d, want 200; body=%s", rec.Code, rec.Body.String())
			}

			var body map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode /v2/healthz body: %v", err)
			}
			if body["pipeline"] != "v2" {
				t.Errorf("/v2/healthz must report pipeline=v2; got %v", body["pipeline"])
			}
			if body["status"] != "ok" {
				t.Errorf("/v2/healthz must report status=ok; got %v", body["status"])
			}
		})
	}
}

// TestV2Pipeline_MuxIsolation_V1AndV2Coexist verifies that the v2 sub-mux
// can be mounted on a parent mux that already serves v1 routes, and that
// /v1/* traffic is unaffected.
func TestV2Pipeline_MuxIsolation_V1AndV2Coexist(t *testing.T) {
	withEnv(t, "LLM_GATEWAY_V2_ENABLED", "true")

	parent := http.NewServeMux()

	// Simulate a v1 chat handler that the production main.go registers.
	v1Called := false
	parent.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		v1Called = true
		w.Header().Set("X-Path", "v1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"v1"}`))
	})

	// Now wire in the v2 sub-mux under /v2/.
	registerV2PipelineRoutes(parent)

	// /v1/chat/completions must still work exactly as before.
	rec1 := httptest.NewRecorder()
	parent.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !v1Called {
		t.Fatal("v1 handler was not called")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("v1 handler returned %d, want 200", rec1.Code)
	}
	if got := rec1.Header().Get("X-Path"); got != "v1" {
		t.Errorf("v1 X-Path header = %q, want v1", got)
	}
	if !strings.Contains(rec1.Body.String(), `"path":"v1"`) {
		t.Errorf("v1 body should contain path:v1, got %s", rec1.Body.String())
	}

	// /v2/healthz must return the v2-flavored health response.
	rec2 := httptest.NewRecorder()
	parent.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v2/healthz", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("/v2/healthz returned %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec2.Body).Decode(&body); err != nil {
		t.Fatalf("decode v2 healthz: %v", err)
	}
	if body["pipeline"] != "v2" {
		t.Errorf("/v2/healthz must report pipeline=v2; got %v", body["pipeline"])
	}
}

// TestV2Pipeline_MuxIsolation_DefaultOff_NoV2Routes verifies the flip
// side of the coexistence test: when the flag is off, mounting
// registerV2PipelineRoutes does NOT add /v2/* routes to the parent.
func TestV2Pipeline_MuxIsolation_DefaultOff_NoV2Routes(t *testing.T) {
	unsetEnv(t, "LLM_GATEWAY_V2_ENABLED")

	parent := http.NewServeMux()
	v1Called := false
	parent.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		v1Called = true
		w.WriteHeader(http.StatusOK)
	})

	registerV2PipelineRoutes(parent)

	// v1 still works.
	rec1 := httptest.NewRecorder()
	parent.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !v1Called {
		t.Fatal("v1 handler was not called")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("v1 returned %d, want 200", rec1.Code)
	}

	// /v2/healthz must NOT be registered → 404 from the default mux.
	rec2 := httptest.NewRecorder()
	parent.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v2/healthz", nil))
	if rec2.Code == http.StatusOK {
		t.Fatalf("/v2/healthz must not be registered when flag is OFF; got 200 body=%s", rec2.Body.String())
	}
}

// TestV2Pipeline_NilParentSafe verifies that registerV2PipelineRoutes
// tolerates a nil parent (e.g. if main.go's mux wiring is interrupted by
// another regression). It must log and return rather than panic.
func TestV2Pipeline_NilParentSafe(t *testing.T) {
	withEnv(t, "LLM_GATEWAY_V2_ENABLED", "true")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerV2PipelineRoutes panicked on nil parent: %v", r)
		}
	}()
	registerV2PipelineRoutes(nil)
}

// TestLoadV2PipelineConfig_Defaults verifies that when LLM_GATEWAY_V2_*
// knobs are unset, the config matches cmd/gateway-v2/main.go defaults.
func TestLoadV2PipelineConfig_Defaults(t *testing.T) {
	unsetEnv(t, "LLM_GATEWAY_V2_ENABLED")
	unsetEnv(t, "LLM_GATEWAY_V2_CACHE")
	unsetEnv(t, "LLM_GATEWAY_V2_SECURITY")
	unsetEnv(t, "LLM_GATEWAY_V2_AUDIT")
	unsetEnv(t, "LLM_GATEWAY_V2_OBSERV")
	unsetEnv(t, "LLM_GATEWAY_V2_STREAMING")

	cfg := loadV2PipelineConfig()
	if cfg.Enabled {
		t.Error("default Enabled must be false")
	}
	if !cfg.EnableCache || !cfg.EnableSecurity || !cfg.EnableAudit ||
		!cfg.EnableObserv || !cfg.EnableStreaming {
		t.Errorf("default sub-flags must all be true; got %+v", cfg)
	}
}

// TestLoadV2PipelineConfig_DisablesSubFlags verifies that operators can
// turn individual Pipeline stages off without disabling the v2 entry
// point. This is useful for differential灰度 (e.g. enable cache, disable
// audit) during integration testing.
func TestLoadV2PipelineConfig_DisablesSubFlags(t *testing.T) {
	withEnv(t, "LLM_GATEWAY_V2_ENABLED", "true")
	withEnv(t, "LLM_GATEWAY_V2_CACHE", "false")
	withEnv(t, "LLM_GATEWAY_V2_AUDIT", "0")

	cfg := loadV2PipelineConfig()
	if !cfg.Enabled {
		t.Error("Enabled must be true when LLM_GATEWAY_V2_ENABLED=true")
	}
	if cfg.EnableCache {
		t.Error("EnableCache must be false when LLM_GATEWAY_V2_CACHE=false")
	}
	if cfg.EnableAudit {
		t.Error("EnableAudit must be false when LLM_GATEWAY_V2_AUDIT=0")
	}
	if !cfg.EnableSecurity || !cfg.EnableObserv || !cfg.EnableStreaming {
		t.Errorf("untouched sub-flags must default to true; got %+v", cfg)
	}
}

// TestShutdownV2Pipeline_NilSafe verifies the shutdown helper tolerates
// nil deps (e.g. when the flag is off and the deps were never built).
func TestShutdownV2Pipeline_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("shutdownV2Pipeline panicked on nil deps: %v", r)
		}
	}()
	shutdownV2Pipeline(nil)
}
