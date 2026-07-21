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
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/api"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/backend"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/checker"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/orchestrator"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/proxy"
	"github.com/kaixuan/llm-gateway-go/installer/internal/launcher/store"
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
	cmd.Env = append(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gateway migrate: %w; output: %s", err, out)
	}
	return nil
}

// proxySwitcher adapts *proxy.Proxy to orchestrator.ActiveSwitcher.
type proxySwitcher struct{ p *proxy.Proxy }

func (s *proxySwitcher) SwitchActive(addr string) { s.p.SwitchActive(addr) }

// daemon holds the long-lived state wired across components.
type daemon struct {
	store          *store.Store
	proxy          *proxy.Proxy
	orch           *orchestrator.Orchestrator
	checker        *checker.Checker
	currentPlanID  string
	currentPlanMu  sync.Mutex
	currentVersion string
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
	)
	flag.Parse()

	if *backendName != "compose" {
		slog.Error("MVP supports compose backend only", "got", *backendName)
		os.Exit(1)
	}

	d := &daemon{currentVersion: *currentVer}

	// Restore active pointer from store (daemon restart scenario).
	d.store = store.New(*dataDir)
	active, _ := d.store.LoadActive()
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

	// Backend (compose only for MVP).
	bk := backend.NewComposeBackend(backend.ComposeConfig{
		ProjectDir: filepath.Join(*dataDir, "compose"),
		GreenPort:  8783,
	})

	// Orchestrator wires backend + migrator + proxy switcher + store.
	d.orch = orchestrator.New(orchestrator.Config{
		Store:          d.store,
		Backend:        bk,
		Migrator:       &gatewayMigrator{binary: *gwBinary},
		ActiveSwitcher: &proxySwitcher{p: d.proxy},
		GatewayBinary:  *gwBinary,
		CurrentVersion: d.currentVersion,
		CurrentAddr:    activeAddr,
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
				ActiveVersion: d.currentVersion,
				HasPlan:       d.getCurrentPlan() != "",
				PlanID:        d.getCurrentPlan(),
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
		// Manual check is a no-op for MVP — checker runs in background.
		// Operator can wait for the next poll or restart daemon to force.
		CheckFunc: func() error { slog.Info("manual check requested (background loop will pick up)"); return nil },
		PrepareFunc: func(planID string) (*store.Plan, error) {
			// Load the existing NOTIFIED plan to get current/target.
			plan, err := d.store.Load(planID)
			if err != nil {
				return nil, fmt.Errorf("load plan %s: %w", planID, err)
			}
			updated, err := d.orch.Prepare(context.Background(), plan.Current, plan.Target)
			if err != nil {
				slog.Warn("prepare failed", "plan", planID, "err", err)
				return updated, err
			}
			d.setCurrentPlan(updated.ID)
			return updated, nil
		},
		ApplyFunc: func(planID string, confirmed bool) error {
			return d.orch.Apply(context.Background(), planID)
		},
		RollbackFunc: func(planID string) error {
			return d.orch.Rollback(context.Background(), planID)
		},
	})
	d.proxy.SetAPIHandler(a)

	// Checker: poll master, on new version build NOTIFIED plan (never auto-apply).
	d.checker = checker.New(checker.Config{
		CurrentVersion: d.currentVersion,
		MasterURL:      *masterURL,
		Channel:        *channel,
		Interval:       *checkInterval,
		Source: &checker.MasterHTTPSource{
			MasterURL:      *masterURL,
			CurrentVersion: d.currentVersion,
			Channel:        *channel,
		},
		OnUpdate: func(r *checker.FoundRelease) {
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
