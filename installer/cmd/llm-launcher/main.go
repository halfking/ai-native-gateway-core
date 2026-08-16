// Command llm-launcher is the resident update supervisor + reverse proxy for
// the KX Gateway. It owns :8781 (client-facing), routes /launcher/* to its
// local REST API + web UI, and forwards everything else to the currently
// active Gateway instance. Blue-green upgrades are orchestrated by this
// daemon (not the Gateway itself, which would die mid-restart).
//
// Human approval gate: checker builds NOTIFIED plans but never auto-applies;
// operators must click Prepare then Apply in the web UI.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/api"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/checker"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/orchestrator"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/proxy"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
	"github.com/kaixuan/llm-gateway-go/installer/internal/upgrader"
)

// gatewayMigrator implements orchestrator.Migrator by shelling out to
// `kx-gateway migrate` (the Task 2 subcommand). Returns nil on exit 0,
// error otherwise.
type gatewayMigrator struct{ binary string }

func (m *gatewayMigrator) Migrate(ctx context.Context) error {
	if m.binary == "" {
		return fmt.Errorf("gateway binary not configured")
	}
	cmd := exec.CommandContext(ctx, m.binary, "migrate")
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gateway migrate: %w; stderr: %s", err, stderr.String())
	}
	// Audit I4 + C11: parse the JSON report. The migrate subcommand
	// returns exit 0 with status=noop even when DATABASE_URL is unset
	// (green would start DB-less, /healthz still 200). Also, a non-JSON
	// stdout (older binary, panic trace, leading log line) must NOT be
	// silently treated as success — Prepare would proceed without any
	// migration having run.
	var report struct {
		Status string `json:"status"`
		HasDB  bool   `json:"has_db"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return fmt.Errorf("gateway migrate: stdout is not valid JSON (expected migration report from a compatible binary): %q", stdout.String())
	}
	if report.Status == "noop" && !report.HasDB {
		slog.Warn("gateway migrate was a no-op with no DB configured — "+
			"check DATABASE_URL/LLM_GATEWAY_DATABASE_URL in the launcher's env "+
			"(green will start without schema and /healthz won't catch it)",
			"binary", m.binary)
	}
	return nil
}

// proxySwitcher adapts *proxy.Proxy to orchestrator.ActiveSwitcher.
type proxySwitcher struct{ p *proxy.Proxy }

func (s *proxySwitcher) SwitchActive(addr string) { s.p.SwitchActive(addr) }
func (s *proxySwitcher) ActiveAddr() string       { return s.p.ActiveAddr() }

// daemon holds the long-lived state wired across components.
type daemon struct {
	store          *store.Store
	proxy          *proxy.Proxy
	orch           *orchestrator.Orchestrator
	checker        *checker.Checker
	currentPlanID  string
	currentPlanMu  sync.Mutex
	currentVersion string

	// opCtx is cancelled on shutdown so any in-flight Prepare/Apply/Rollback
	// (specifically the 35s Drain subprocess) aborts promptly instead of
	// keeping the daemon alive past SIGTERM (audit I9).
	opCtx    context.Context
	opCancel context.CancelFunc
}

func (d *daemon) setCurrentPlan(id string) {
	d.currentPlanMu.Lock()
	d.currentPlanID = id
	d.currentPlanMu.Unlock()
}

func (d *daemon) getCurrentPlan() string {
	d.currentPlanMu.Lock()
	defer d.currentPlanMu.Unlock()
	return d.currentPlanID
}

// setCurrentVersion updates the in-memory current version. Called after a
// successful Apply (target version) or Rollback (current version). Without
// this, the daemon's currentVersion is frozen at startup and the checker
// keeps re-notifying the version we just upgraded to (audit C9).
func (d *daemon) setCurrentVersion(v string) {
	d.currentPlanMu.Lock()
	d.currentVersion = v
	d.currentPlanMu.Unlock()
}

func (d *daemon) getCurrentVersion() string {
	d.currentPlanMu.Lock()
	defer d.currentPlanMu.Unlock()
	return d.currentVersion
}

// currentPlanState returns the state of the current plan, or "" if none.
// (audit M1: StatusProvider never populated PlanState, so the UI's "Plan
// 状态" always showed "无" even when a plan was active.)
func (d *daemon) currentPlanState() string {
	id := d.getCurrentPlan()
	if id == "" {
		return ""
	}
	p, err := d.store.Load(id)
	if err != nil {
		return ""
	}
	return p.State
}

// terminalStates are states where a plan is finished (success or settled
// failure/rollback) and shouldn't block creating a new plan or be resumed.
var terminalStates = map[string]bool{
	store.StateDone: true, store.StateFailed: true, store.StateRolledBack: true,
}

// findPlanForVersion returns the most recent non-terminal plan whose
// Target.Version matches, or nil. Used by checker OnUpdate to dedup (I1):
// don't create a second NOTIFIED plan for a version already pending.
func (d *daemon) findPlanForVersion(version string) *store.Plan {
	plans, err := d.store.LoadAll()
	if err != nil {
		slog.Warn("LoadAll for dedup failed", "err", err)
		return nil
	}
	for _, p := range plans { // LoadAll is newest-first
		if p.Target.Version == version && !terminalStates[p.State] {
			return p
		}
	}
	return nil
}

// reconcileAbandonedPlans (audit I9): on daemon startup, fail any plan
// stuck in PREPARING/ACTIVATING/DRAINING. These states are mid-flight
// from a previous daemon lifetime that was interrupted (SIGTERM/OOM/panic);
// the docker subprocess may still be running, but the Go side is gone.
// Marking FAILED lets the operator Rollback to clean up green. Without
// this, the UI shows a plan that no operation can act on (Apply requires
// PREPARED, Prepare requires NOTIFIED/FAILED, Rollback requires DONE/
// FAILED).
func (d *daemon) reconcileAbandonedPlans() {
	abandoned := map[string]bool{
		store.StatePreparing:  true,
		store.StateActivating: true,
		store.StateDraining:   true,
	}
	plans, err := d.store.LoadAll()
	if err != nil {
		slog.Warn("LoadAll for reconcile failed", "err", err)
		return
	}
	for _, p := range plans {
		if !abandoned[p.State] {
			continue
		}
		p.State = store.StateFailed
		p.History = append(p.History, store.StateEvent{
			State: store.StateFailed,
			At:    time.Now().UTC(),
			Note:  "daemon restarted while plan was in " + p.State + "; auto-failed for cleanup",
		})
		if err := d.store.Save(p); err != nil {
			slog.Warn("save failed-plan", "plan", p.ID, "err", err)
			continue
		}
		slog.Warn("abandoned plan auto-failed on startup", "plan", p.ID, "was", p.State)
	}
}

// restoreInProgressPlan (I3): on daemon startup, find the most recent
// non-terminal plan and adopt it as currentPlanID so the UI shows it.
// Without this, a daemon restart mid-PREPARED (or NOTIFIED awaiting
// operator action) loses the plan from the UI until the next checker tick.
func (d *daemon) restoreInProgressPlan() {
	plans, err := d.store.LoadAll()
	if err != nil {
		slog.Warn("LoadAll for restore failed", "err", err)
		return
	}
	for _, p := range plans {
		if !terminalStates[p.State] {
			d.setCurrentPlan(p.ID)
			slog.Info("restored in-progress plan on startup", "plan", p.ID, "state", p.State)
			return
		}
	}
}

func deviceProofFromEnvironment() upgrader.DeviceProof {
	return upgrader.DeviceProof{
		InstanceID:   strings.TrimSpace(os.Getenv("INSTANCE_ID")),
		LicenseKey:   strings.TrimSpace(os.Getenv("LICENSE_KEY")),
		HardwareHash: strings.TrimSpace(os.Getenv("HARDWARE_HASH")),
	}
}

func main() {
	var (
		listen        = flag.String("listen", ":8781", "listen address (client-facing)")
		dataDir       = flag.String("data-dir", "/var/lib/kx-launcher", "state directory")
		backendName   = flag.String("backend", "compose", "compose|systemd (MVP: compose only)")
		currentVer    = flag.String("current-version", "", "current Gateway version")
		currentAddr   = flag.String("current-addr", "127.0.0.1:8782", "current active Gateway addr (blue)")
		gwBinary      = flag.String("gateway-binary", "/usr/local/bin/kx-gateway", "Gateway binary (for migrate)")
		masterURL     = flag.String("master-url", "https://llm.kxpms.cn", "master URL for update checks")
		channel       = flag.String("channel", "stable", "update channel")
		checkInterval = flag.Duration("check-interval", 1*time.Hour, "update check interval")
		envFile       = flag.String("env-file", "/etc/kx-gateway/env", "env file shared with green container (DATABASE_URL etc.)")
	)
	flag.Parse()

	if *backendName != "compose" {
		slog.Error("MVP supports compose backend only", "got", *backendName)
		os.Exit(1)
	}

	d := &daemon{currentVersion: *currentVer}

	// Restore active pointer from store (daemon restart scenario).
	d.store = store.New(*dataDir)
	active, err := d.store.LoadActive()
	if err != nil {
		slog.Warn("load active failed (falling back to flag)", "err", err)
	}
	_ = err
	activeAddr := *currentAddr
	if active != nil && active.Addr != "" {
		activeAddr = active.Addr
		if active.Version != "" {
			d.currentVersion = active.Version
		}
		slog.Info("restored active from store", "addr", activeAddr, "version", d.currentVersion)
	}

	// Build proxy first — orchestrator needs it via ActiveSwitcher.
	d.proxy = proxy.New(activeAddr)

	// I3: restore any in-progress plan so the UI shows it after a restart.
	// (e.g. daemon crashed mid-PREPARED, or was restarted while a NOTIFIED
	// plan awaited operator action.)
	// Audit I9: fail any plans stuck in mid-flight states from a previous
	// daemon lifetime (interrupted Prepare/Apply). Do this before adopting
	// one as the in-progress plan so the adopted one is the most recent
	// NOTIFIED/PREPARED (operator-actionable), not a stranded DRAINING.
	d.reconcileAbandonedPlans()
	d.restoreInProgressPlan()
	d.store.SweepStaleTmp() // Audit I13

	// Backend (compose only for MVP).
	bk := backend.NewComposeBackend(backend.ComposeConfig{
		ProjectDir:    filepath.Join(*dataDir, "compose"),
		GreenPortBase: 8783,
		// EnvFile lets the green container inherit DATABASE_URL/REDIS/secrets
		// from the same env file blue uses. Without it, green starts with no
		// DB and /healthz still returns 200 (C2).
		EnvFile: *envFile,
	})

	// Orchestrator wires backend + migrator + proxy switcher + store.
	d.orch = orchestrator.New(orchestrator.Config{
		Store:          d.store,
		Backend:        bk,
		Migrator:       &gatewayMigrator{binary: *gwBinary},
		ActiveSwitcher: &proxySwitcher{p: d.proxy},
	})

	// Generate token on first run; reuse on subsequent.
	tokenStr, err := ensureToken(*dataDir)
	if err != nil {
		slog.Error("token setup failed", "err", err)
		os.Exit(1)
	}

	// REST API + UI.
	a := api.New(api.Config{
		TokenProvider: func() string { return tokenStr },
		StatusProvider: func() api.Status {
			return api.Status{
				ActiveAddr:    d.proxy.ActiveAddr(),
				ActiveVersion: d.getCurrentVersion(),
				HasPlan:       d.getCurrentPlan() != "",
				PlanID:        d.getCurrentPlan(),
				PlanState:     d.currentPlanState(),
			}
		},
		PlanProvider: func() *store.Plan {
			id := d.getCurrentPlan()
			if id == "" {
				return nil
			}
			p, _ := d.store.Load(id)
			return p
		},
		// Manual check triggers an immediate poll (audit I1: was a no-op).
		CheckFunc: func() error {
			if !d.checker.CheckNow() {
				return fmt.Errorf("checker not running")
			}
			slog.Info("manual check triggered")
			return nil
		},
		PrepareFunc: func(planID string) (*store.Plan, error) {
			// orchestrator.Prepare loads the NOTIFIED plan by ID and
			// transitions it in place (audit C4: no more orphaned plans).
			updated, err := d.orch.Prepare(d.opCtx, planID)
			if err != nil {
				slog.Warn("prepare failed", "plan", planID, "err", err)
				return updated, err
			}
			d.setCurrentPlan(updated.ID)
			return updated, nil
		},
		ApplyFunc: func(planID string, confirmed bool) error {
			if err := d.orch.Apply(d.opCtx, planID); err != nil {
				return err
			}
			// Audit C9: keep in-memory currentVersion in sync so the
			// checker doesn't re-notify the version we just deployed.
			if p, err := d.store.Load(planID); err == nil {
				d.setCurrentVersion(p.Target.Version)
				slog.Info("current version updated after Apply", "version", p.Target.Version)
			}
			return nil
		},
		RollbackFunc: func(planID string) error {
			if err := d.orch.Rollback(d.opCtx, planID); err != nil {
				return err
			}
			// Audit I14: rollback restores the prior current version.
			if p, err := d.store.Load(planID); err == nil {
				d.setCurrentVersion(p.Current.Version)
				slog.Info("current version reverted after Rollback", "version", p.Current.Version)
			}
			return nil
		},
	})
	d.proxy.SetAPIHandler(a)

	// Checker: poll master, on new version build NOTIFIED plan (never auto-apply).
	// CurrentVersion is a provider so the checker compares against the live
	// post-Apply version, not the startup value (audit C9).
	proof := deviceProofFromEnvironment()
	d.checker = checker.New(checker.Config{
		CurrentVersion: d.getCurrentVersion,
		MasterURL:      *masterURL,
		Channel:        *channel,
		Interval:       *checkInterval,
		Source: &checker.MasterHTTPSource{
			MasterURL:      *masterURL,
			CurrentVersion: d.getCurrentVersion,
			Channel:        *channel,
			Platform:       runtime.GOOS,
			Arch:           runtime.GOARCH,
			Proof:          proof,
		},

		OnUpdate: func(r *checker.FoundRelease) {
			// I1 dedup: if there's already a non-terminal plan targeting the
			// same version, don't create a duplicate. Otherwise over a weekend
			// the operator accumulates dozens of NOTIFIED plans for v1.5.0.
			if existing := d.findPlanForVersion(r.Version); existing != nil {
				slog.Info("new version already has pending plan, skipping", "version", r.Version, "plan", existing.ID, "state", existing.State)
				return
			}
			plan := &store.Plan{
				ID:        fmt.Sprintf("plan-%d", time.Now().UnixNano()),
				CreatedAt: time.Now().UTC(),
				State:     store.StateNotified,
				Current:   store.Release{Version: d.currentVersion},
				Target: store.Release{
					Version:     r.Version,
					Image:       r.Image,
					DownloadURL: r.DownloadURL,
					SHA256:      r.SHA256,
					Changelog:   r.Changelog,
				},
				ActiveAddr: d.proxy.ActiveAddr(),
				History:    []store.StateEvent{{State: store.StateNotified, At: time.Now().UTC()}},
			}
			if err := d.store.Save(plan); err != nil {
				slog.Error("save notified plan failed", "err", err)
				return
			}
			d.setCurrentPlan(plan.ID)
			slog.Info("new version notified", "version", r.Version, "plan", plan.ID, "image", r.Image)
		},
	})

	// HTTP server.
	srv := &http.Server{
		Addr:         *listen,
		Handler:      d.proxy,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	d.opCtx, d.opCancel = context.WithCancel(context.Background())
	defer stop()

	chkCtx, chkCancel := context.WithCancel(context.Background())
	defer chkCancel()
	d.checker.Start(chkCtx)

	go func() {
		slog.Info("launcher listening", "addr", *listen, "backend", *backendName, "active", d.proxy.ActiveAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	chkCancel()
	d.checker.Stop()
	// Audit I9: cancel in-flight Prepare/Apply/Rollback so the 35s Drain
	// subprocess aborts promptly. Without this, the daemon would block
	// the Drain even though no one's listening to the API.
	d.opCancel()
	d.orch.Stop()
}

// ensureToken reads the token file, generating one on first run.
func ensureToken(dataDir string) (string, error) {
	tokenPath := filepath.Join(dataDir, "token")
	data, err := os.ReadFile(tokenPath)
	if err == nil && len(data) > 0 {
		return string(data), nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read token: %w", err)
	}
	// Generate.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir data dir: %w", err)
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	tok := hex.EncodeToString(b)
	if err := os.WriteFile(tokenPath, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	slog.Info("generated new token", "path", tokenPath)
	return tok, nil
}
