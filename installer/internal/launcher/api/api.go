// Package api exposes the launcher's REST API and embedded web UI.
//
// Endpoints (mounted under /launcher/ by the proxy):
//   GET    /launcher/                     — embedded web UI (static files)
//   GET    /launcher/api/status           — current active instance + plan
//   POST   /launcher/api/check            — manually trigger update check
//   GET    /launcher/api/plan             — current/last plan detail
//   POST   /launcher/api/plan/<id>/prepare — begin Prepare (download+stage+migrate)
//   POST   /launcher/api/plan/<id>/apply   — human-gated activation ({confirm:true} required)
//   POST   /launcher/api/plan/<id>/rollback — revert to previous active
//
// All /launcher/api/* calls require X-Launcher-Token header matching the
// daemon-generated token. The UI path is unauthenticated so operators can
// load the page and enter the token there.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
)

// Status is the summary returned by /launcher/api/status.
type Status struct {
	ActiveAddr    string `json:"active_addr"`
	ActiveVersion string `json:"active_version"`
	HasPlan       bool   `json:"has_plan"`
	PlanState     string `json:"plan_state,omitempty"`
	PlanID        string `json:"plan_id,omitempty"`
}

// Config wires the API to its dependencies. All funcs are optional;
// nil funcs are treated as no-ops or empty results.
type Config struct {
	TokenProvider  func() string                       // returns expected token ("" disables auth)
	StatusProvider func() Status                       // current active + plan summary
	PlanProvider   func() *store.Plan                  // current/last plan (nil if none)
	CheckFunc      func() error                        // manual update check trigger
	PrepareFunc    func(planID string) (*store.Plan, error) // start prepare on existing NOTIFIED plan
	ApplyFunc      func(planID string, confirmed bool) error
	RollbackFunc   func(planID string) error
}

type API struct{ cfg Config }

func New(cfg Config) *API { return &API{cfg: cfg} }

// ServeHTTP routes /launcher/* requests.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// UI: /launcher/ and /launcher/<static-file>
	if r.URL.Path == "/launcher/" || r.URL.Path == "/launcher" {
		a.serveUI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/launcher/api/") {
		if !a.checkToken(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		a.serveAPI(w, r)
		return
	}
	// Other /launcher/* paths are static assets (style.css, app.js)
	a.serveUI(w, r)
}

func (a *API) checkToken(r *http.Request) bool {
	expected := ""
	if a.cfg.TokenProvider != nil {
		expected = a.cfg.TokenProvider()
	}
	if expected == "" {
		return true // auth disabled
	}
	// Constant-time compare to avoid timing side-channels (audit I8).
	got := r.Header.Get("X-Launcher-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func (a *API) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/launcher/api/status" && r.Method == http.MethodGet:
		s := Status{}
		if a.cfg.StatusProvider != nil {
			s = a.cfg.StatusProvider()
		}
		writeJSON(w, s)

	case r.URL.Path == "/launcher/api/check" && r.Method == http.MethodPost:
		if a.cfg.CheckFunc != nil {
			if err := a.cfg.CheckFunc(); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, map[string]string{"status": "checked"})

	case r.URL.Path == "/launcher/api/plan" && r.Method == http.MethodGet:
		if a.cfg.PlanProvider != nil {
			if p := a.cfg.PlanProvider(); p != nil {
				writeJSON(w, p)
				return
			}
		}
		writeJSON(w, nil)

	case strings.HasSuffix(r.URL.Path, "/prepare") && r.Method == http.MethodPost:
		planID := extractPlanID(r.URL.Path, "/prepare")
		if !validPlanID(planID) {
			writeErr(w, http.StatusBadRequest, "invalid or missing plan id")
			return
		}
		if a.cfg.PrepareFunc == nil {
			writeErr(w, http.StatusNotImplemented, "prepare not configured")
			return
		}
		p, err := a.cfg.PrepareFunc(planID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, p)

	case strings.HasSuffix(r.URL.Path, "/apply") && r.Method == http.MethodPost:
		planID := extractPlanID(r.URL.Path, "/apply")
		if !validPlanID(planID) {
			writeErr(w, http.StatusBadRequest, "invalid or missing plan id")
			return
		}
		var body struct {
			Confirm bool `json:"confirm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Confirm {
			// Two-step gate: return plan summary + hint, don't apply.
			resp := map[string]any{
				"status":  "confirm_required",
				"message": "resend with {\"confirm\":true} to activate",
			}
			if a.cfg.PlanProvider != nil {
				if p := a.cfg.PlanProvider(); p != nil {
					resp["plan"] = p
				}
			}
			writeJSON(w, resp)
			return
		}
		if a.cfg.ApplyFunc == nil {
			writeErr(w, http.StatusNotImplemented, "apply not configured")
			return
		}
		if err := a.cfg.ApplyFunc(planID, true); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "applied"})

	case strings.HasSuffix(r.URL.Path, "/rollback") && r.Method == http.MethodPost:
		planID := extractPlanID(r.URL.Path, "/rollback")
		if !validPlanID(planID) {
			writeErr(w, http.StatusBadRequest, "invalid or missing plan id")
			return
		}
		if a.cfg.RollbackFunc == nil {
			writeErr(w, http.StatusNotImplemented, "rollback not configured")
			return
		}
		if err := a.cfg.RollbackFunc(planID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "rolled_back"})

	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// validPlanID rejects malformed/empty plan IDs (audit M5: previously
// /launcher/api/plan/apply parsed planID="apply"). Daemon-generated IDs
// are "plan-<unixnano>", so allow that shape.
func validPlanID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	return planIDRegex.MatchString(id)
}

var planIDRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// extractPlanID parses /launcher/api/plan/<id>/{apply,rollback,prepare} → <id>.
func extractPlanID(path, suffix string) string {
	s := strings.TrimPrefix(path, "/launcher/api/plan/")
	s = strings.TrimSuffix(s, suffix)
	return strings.TrimSuffix(s, "/")
}

func (a *API) serveUI(w http.ResponseWriter, r *http.Request) {
	staticFS := http.FS(WebFiles())
	handler := http.StripPrefix("/launcher/", http.FileServer(staticFS))
	handler.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
