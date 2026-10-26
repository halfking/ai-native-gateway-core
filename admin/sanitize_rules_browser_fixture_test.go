package admin

// Opt-in local browser fixture: real rule/settings handlers and JWT middleware,
// two independent managers, disposable PostgreSQL, Redis notification transport.
// It never binds publicly or changes an existing gateway database.
import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

func TestSanitizeRulesBrowserFixture(t *testing.T) {
	addr := os.Getenv("SANITIZE_RULES_BROWSER_ADDR")
	dsn := os.Getenv("SANITIZE_RULES_BROWSER_DSN")
	if addr == "" || dsn == "" {
		t.Skip("browser fixture disabled")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("fixture must bind loopback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, path := range []string{"../sql/migrations/startup/022_settings_kv.sql", "../sql/migrations/startup/023_settings_audit.sql", "../sql/migrations/startup/866_sanitize_rule_management.sql"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	backend := settings.NewStoreDB(pool)
	settings.Init(backend)
	for _, sp := range settings.PlatformSpecs() {
		if settings.Global.Spec(sp.Key) == nil {
			_ = settings.Global.RegisterSpec(sp)
		}
	}
	mini := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer rc.Close()
	detector, err := sanitize.NewPatternDetectorFromFile("../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	managers := []*sanitizerules.Manager{}
	ids := []string{"browser-154", "browser-245"}
	if id := os.Getenv("SANITIZE_RULES_BROWSER_INSTANCE"); id != "" {
		ids = []string{id}
	}
	for _, id := range ids {
		m, err := sanitizerules.NewManager(sanitizerules.NewPostgresStore(db), detector.RuleConfig(), sanitizerules.Options{InstanceID: id, PollInterval: func() time.Duration {
			return time.Duration(settings.SanitizeRulesRefreshIntervalSeconds()) * time.Second
		}, AuditEnabled: settings.SanitizeRulesAuditEnabled, EventLimit: settings.SanitizeRulesEventLimit, Notifications: sanitizerules.NewRedisNotifications(rc, "browser:sanitize-rules")})
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		managers = append(managers, m)
		go m.Run(ctx)
	}
	detector.SetRuleManager(managers[0])
	const secret = "local-browser-fixture-signing-secret"
	h := &Handler{db: pool, secret: secret}
	h.SetSettingsStore(backend)
	h.SetSanitizeRuleManager(managers[0])
	mux := http.NewServeMux()
	h.registerSanitizeRuleRoutes(mux)
	h.registerSettingsRoutes(mux)
	h.registerModuleRoutes(mux)
	// Authentication setup is fixture-only. The resulting signed cookie traverses
	// production AdminMiddleware for every feature operation.
	mux.HandleFunc("/api/auth/token", func(w http.ResponseWriter, r *http.Request) {
		token, expiry, err := SignToken(1, "default", "browser-admin", "super_admin", secret, false)
		if err != nil {
			t.Fatal(err)
		}
		http.SetCookie(w, &http.Cookie{Name: "llmgw_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		writeJSON(w, 200, map[string]any{"access_token": token, "expires_at": expiry, "user": map[string]any{"id": 1, "username": "browser-admin", "role": "super_admin", "tenant_id": "default"}})
	})
	mux.HandleFunc("/api/auth/me", h.admin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"id": 1, "username": "browser-admin", "role": "super_admin", "tenant_id": "default"})
	}))
	mux.HandleFunc("/api/system/bootstrap/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"activated": true}) })
	mux.HandleFunc("/api/fixture/evaluate", h.admin(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		results := []any{}
		for i, m := range managers {
			d := sanitize.NewPatternDetector()
			d.SetRuleManager(m)
			s, _ := sanitize.NewSanitizer(d)
			out, err := s.SanitizeInput(m.Pin(sanitize.WithAuthenticatedTenant(r.Context(), "default")), in.Text)
			safe := ""
			if out != nil {
				safe = out.SanitizedText
			}
			results = append(results, map[string]any{"instance": i, "version": m.Current().Version, "output": safe, "blocked": err != nil})
		}
		writeJSON(w, 200, results)
	}))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	t.Log("browser fixture ready", addr)
	err = server.Serve(listener)
	if err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
