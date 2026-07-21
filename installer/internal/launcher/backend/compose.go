package backend

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ComposeConfig configures the ComposeBackend.
type ComposeConfig struct {
	// ProjectDir is where green compose files live.
	// (Each Stage invocation writes a new <port>-specific compose file here.)
	ProjectDir string

	// GreenPort is the host port the green container listens on
	// (the container itself listens on 8780, mapped to GreenPort on host).
	// Default: 8783.
	GreenPort int
}

// ComposeBackend deploys green instances via `docker compose`.
// Each Stage writes a new green-<port>.yml file with project name
// kxgw-green-<port>, isolating green from blue.
type ComposeBackend struct {
	cfg ComposeConfig
}

func NewComposeBackend(cfg ComposeConfig) *ComposeBackend {
	if cfg.GreenPort == 0 {
		cfg.GreenPort = 8783
	}
	return &ComposeBackend{cfg: cfg}
}

func (b *ComposeBackend) Name() string { return "compose" }

func (b *ComposeBackend) greenComposePath() string {
	return filepath.Join(b.cfg.ProjectDir, fmt.Sprintf("docker-compose.green.%d.yml", b.cfg.GreenPort))
}

func (b *ComposeBackend) greenProjectName() string {
	return fmt.Sprintf("kxgw-green-%d", b.cfg.GreenPort)
}

func (b *ComposeBackend) greenContainerName() string {
	return fmt.Sprintf("kx-gateway-green-%d", b.cfg.GreenPort)
}

// Stage writes a green compose file and brings up the green container.
func (b *ComposeBackend) Stage(ctx context.Context, rel Release) (string, error) {
	if rel.Image == "" {
		return "", fmt.Errorf("compose backend requires Release.Image")
	}
	compose := fmt.Sprintf(`services:
  gateway-green:
    image: %s
    container_name: %s
    ports:
      - "%d:8780"
    environment:
      - LLM_GATEWAY_LISTEN=:8780
    restart: "no"
`, rel.Image, b.greenContainerName(), b.cfg.GreenPort)
	if err := writeFile(b.greenComposePath(), compose); err != nil {
		return "", fmt.Errorf("write compose: %w", err)
	}
	if err := b.composeCmd(ctx, "up", "-d", "--wait"); err != nil {
		return "", fmt.Errorf("compose up: %w", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", b.cfg.GreenPort), nil
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

// Drain sends SIGTERM (via docker compose stop) and waits up to 35s
// for the container to exit gracefully.
func (b *ComposeBackend) Drain(ctx context.Context, _ string) error {
	return b.composeCmd(ctx, "stop", "-t", "35")
}

// Remove stops and removes the container and its volumes.
func (b *ComposeBackend) Remove(ctx context.Context, _ string) error {
	return b.composeCmd(ctx, "down", "-v")
}

// composeCmd runs docker compose -p <project> -f <file> <args...>.
func (b *ComposeBackend) composeCmd(ctx context.Context, args ...string) error {
	full := append([]string{
		"compose",
		"-p", b.greenProjectName(),
		"-f", b.greenComposePath(),
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
