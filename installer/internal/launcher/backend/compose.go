package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// imageTagRegex enforces an allowlist for image tags interpolated into
// the compose YAML. Defense-in-depth against YAML injection via a
// malformed Release.Image (e.g. containing a newline plus extra keys).
// The name group also accepts ':' so registry "host:port" prefixes
// like "localhost:5000/myapp" are valid; the tag group is kept narrow.
var imageTagRegex = regexp.MustCompile(`^[a-zA-Z0-9._/:-]+:[a-zA-Z0-9._-]+$`)

// envPathRegex guards the env_file path interpolated into YAML.
var envPathRegex = regexp.MustCompile(`^/[a-zA-Z0-9._/-]+$`)

// validateImageTag returns nil iff img matches the allowlist.
func validateImageTag(img string) error {
	if !imageTagRegex.MatchString(img) {
		return fmt.Errorf("invalid image tag %q: must match [a-zA-Z0-9._/:-]+:[a-zA-Z0-9._-]+", img)
	}
	return nil
}

func validateEnvPath(p string) error {
	if p == "" {
		return nil
	}
	if !envPathRegex.MatchString(p) {
		return fmt.Errorf("invalid env path %q: must be absolute, chars [a-zA-Z0-9._/-]", p)
	}
	return nil
}

// ComposeConfig configures the ComposeBackend.
type ComposeConfig struct {
	// ProjectDir is where green compose files live (one per port).
	ProjectDir string

	// GreenPortBase is the first host port to try for a green container.
	// Each Stage picks a free port starting here (8783, 8784, ...) so
	// multiple greens / multi-cycle upgrades don't collide (audit C3).
	// Default: 8783.
	GreenPortBase int

	// EnvFile is an optional path to an env file shared with the green
	// container (DATABASE_URL/REDIS/secrets). Without it, green starts
	// with no DB and /healthz still returns 200 (audit C2).
	EnvFile string
}

// ComposeBackend deploys green instances via `docker compose`.
//
// Instance isolation: each Stage call picks a unique host port and derives
// project name kxgw-<port>, compose file docker-compose.<port>.yml, and
// container name kx-gateway-<port>. Drain/Remove/Health receive an addr
// of the form 127.0.0.1:<port> and reverse-map to the project (audit
// C1/C2: previously these ignored addr and always targeted the fixed
// green project, so Apply's drain of "blue" actually stopped the just-
// activated green).
type ComposeBackend struct {
	cfg ComposeConfig
	mu  sync.Mutex
	// allocated tracks ports currently in use by staged greens so two
	// concurrent Stages don't both grab 8783. Guarded by mu.
	allocated map[int]bool
}

func NewComposeBackend(cfg ComposeConfig) *ComposeBackend {
	if cfg.GreenPortBase == 0 {
		cfg.GreenPortBase = 8783
	}
	return &ComposeBackend{cfg: cfg, allocated: make(map[int]bool)}
}

func (b *ComposeBackend) Name() string { return "compose" }

// portFromAddr extracts the port from "host:port". Returns 0 on malformed.
func portFromAddr(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}

func (b *ComposeBackend) composePath(port int) string {
	return filepath.Join(b.cfg.ProjectDir, fmt.Sprintf("docker-compose.%d.yml", port))
}

func (b *ComposeBackend) projectName(port int) string {
	return fmt.Sprintf("kxgw-%d", port)
}

func (b *ComposeBackend) containerName(port int) string {
	return fmt.Sprintf("kx-gateway-%d", port)
}

// pickFreePort finds the first free host port starting at GreenPortBase
// that is both not allocated in-process and actually bindable. Holds mu.
//
// Audit I12: probe binds 0.0.0.0 (not 127.0.0.1) because compose publishes
// the port on all host interfaces — a port free on loopback may be in use
// on another interface, and compose's "up -d --wait" would then fail with
// a confusing EADDRINUSE.
func (b *ComposeBackend) pickFreePort() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for p := b.cfg.GreenPortBase; p < b.cfg.GreenPortBase+100; p++ {
		if b.allocated[p] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			continue // port in use on at least one interface
		}
		_ = ln.Close()
		b.allocated[p] = true
		return p, nil
	}
	return 0, fmt.Errorf("no free green port in range %d-%d", b.cfg.GreenPortBase, b.cfg.GreenPortBase+100)
}

// releasePort frees a port from the allocated set (best-effort).
func (b *ComposeBackend) releasePort(port int) {
	b.mu.Lock()
	delete(b.allocated, port)
	b.mu.Unlock()
}

// Stage writes a green compose file and brings up the green container.
func (b *ComposeBackend) Stage(ctx context.Context, rel Release) (string, error) {
	if rel.Image == "" {
		return "", fmt.Errorf("compose backend requires Release.Image")
	}
	if err := validateImageTag(rel.Image); err != nil {
		return "", err
	}
	if err := validateEnvPath(b.cfg.EnvFile); err != nil {
		return "", err
	}
	port, err := b.pickFreePort()
	if err != nil {
		return "", err
	}
	// If anything below fails, free the port + best-effort clean the
	// half-created container (audit I5).
	cleanup := func() {
		b.releasePort(port)
		_ = b.composeCmdForPort(ctx, port, "down", "-v")
	}

	envBlock := "      - LLM_GATEWAY_LISTEN=:8780\n"
	if b.cfg.EnvFile != "" {
		envBlock += fmt.Sprintf("    env_file:\n      - %s\n", b.cfg.EnvFile)
	}
	compose := fmt.Sprintf(`services:
  gateway-green:
    image: %s
    container_name: %s
    ports:
      - "%d:8780"
    environment:
%s    restart: "no"
`, rel.Image, b.containerName(port), port, envBlock)
	if err := writeFile(b.composePath(port), compose); err != nil {
		cleanup()
		return "", fmt.Errorf("write compose: %w", err)
	}
	if err := b.composeCmdForPort(ctx, port, "up", "-d", "--wait"); err != nil {
		cleanup()
		return "", fmt.Errorf("compose up: %w", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}

// Health does GET http://<addr>/healthz. Returns nil on 2xx, error otherwise.
func (b *ComposeBackend) Health(ctx context.Context, addr string) error {
	url := fmt.Sprintf("http://%s/healthz", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("healthz status %d", resp.StatusCode)
	}
	return nil
}

// Drain sends SIGTERM (via docker compose stop) to the instance at addr
// and waits up to 35s for graceful exit. The instance is identified by
// the port in addr (audit C1: previously ignored addr and always stopped
// the green project, so Apply's drain-blue call stopped the just-activated
// green).
func (b *ComposeBackend) Drain(ctx context.Context, addr string) error {
	port := portFromAddr(addr)
	if port == 0 {
		return fmt.Errorf("drain: cannot parse port from addr %q", addr)
	}
	// Audit M6: if this port isn't one of our compose projects (e.g. blue
	// is managed by systemd, not docker compose), treat as no-op success —
	// the upstream is "drained" from our perspective (we don't manage it).
	if !b.hasComposeFile(port) {
		return nil
	}
	return b.composeCmdForPort(ctx, port, "stop", "-t", "35")
}

// Remove stops and removes the instance at addr and its volumes.
func (b *ComposeBackend) Remove(ctx context.Context, addr string) error {
	port := portFromAddr(addr)
	if port == 0 {
		return fmt.Errorf("remove: cannot parse port from addr %q", addr)
	}
	// Audit M6: same as Drain — silent no-op for non-compose-managed addr.
	if !b.hasComposeFile(port) {
		b.releasePort(port)
		return nil
	}
	err := b.composeCmdForPort(ctx, port, "down", "-v")
	b.releasePort(port)
	return err
}

// hasComposeFile returns true iff the compose YAML for this port exists
// on disk (i.e. we manage it). Used to no-op Drain/Remove on addrs managed
// by something else (e.g. systemd).
func (b *ComposeBackend) hasComposeFile(port int) bool {
	_, err := os.Stat(b.composePath(port))
	return err == nil
}

// composeCmdForPort runs docker compose -p <project(port)> -f <file(port)> <args...>.
func (b *ComposeBackend) composeCmdForPort(ctx context.Context, port int, args ...string) error {
	full := append([]string{
		"compose",
		"-p", b.projectName(port),
		"-f", b.composePath(port),
	}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v: %w; output: %s", args, err, out)
	}
	return nil
}

// writeFile writes content to path, creating parent dirs as needed.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
