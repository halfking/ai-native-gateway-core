package upgrader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/secrets"
)

const (
	offlineGatewayRepository = "kx-llm-gateway-go:"
	offlineGatewayService    = "llm-gateway-go"
)

var offlineImageTagLine = regexp.MustCompile(`(?m)^[\t ]*APP_IMAGE_TAG[\t ]*=[^\r\n]*`)

func (a *OfflineApplier) applyInstallerStartupPackage(ctx context.Context, manifest *Manifest, packageDir string, startupFiles []string) (err error) {
	tag, err := manifest.installerImageTag()
	if err != nil {
		return err
	}
	composeDir := filepath.Dir(a.composeFile)
	envPath := filepath.Join(composeDir, ".env")
	envFile, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read Compose environment: %w", err)
	}
	env, err := secrets.NewEnvFile(envPath).Read()
	if err != nil {
		return fmt.Errorf("parse Compose environment: %w", err)
	}
	oldTag := env["APP_IMAGE_TAG"]
	if !offlineImageTag.MatchString(oldTag) || len(offlineImageTagLine.FindAllIndex(envFile, 2)) != 1 {
		return fmt.Errorf("expected one valid APP_IMAGE_TAG in the installed Compose environment")
	}
	versionPath := filepath.Join(composeDir, "app", "VERSION")
	if _, err := os.Stat(versionPath); err != nil {
		return fmt.Errorf("find installed app/VERSION: %w", err)
	}
	oldImageID, err := a.gatewayImageID(ctx)
	if err != nil {
		return err
	}
	if !offlineImageID.MatchString(oldImageID) {
		return fmt.Errorf("could not identify the installed gateway image for rollback")
	}
	backupPath, err := a.backupCurrentState()
	if err != nil {
		return fmt.Errorf("backup current state: %w", err)
	}
	backupVersion := filepath.Join(backupPath, "app-VERSION")
	if err := a.copyFile(versionPath, backupVersion); err != nil {
		return fmt.Errorf("backup app/VERSION: %w", err)
	}

	var imageLoadStarted, filesChanged, composeStarted bool
	defer func() {
		if err == nil {
			return
		}
		if imageLoadStarted {
			// Loading an archive may retag the currently running image, including on a retry.
			_, restoreErr := offlineDocker(ctx, "image", "tag", oldImageID, offlineGatewayRepository+oldTag)
			err = errors.Join(err, restoreErr)
		}
		if filesChanged {
			err = errors.Join(err, a.copyFile(filepath.Join(backupPath, ".env"), envPath), a.copyFile(backupVersion, versionPath))
		}
		if composeStarted {
			err = errors.Join(err, a.recreateOfflineGateway(ctx, oldTag))
		}
	}()

	imageLoadStarted = true
	if _, err := offlineDocker(ctx, "load", "-i", filepath.Join(packageDir, "images.tar")); err != nil {
		return err
	}
	newImageID, err := offlineDocker(ctx, "image", "inspect", "--format", "{{.Id}}", manifest.AppImage)
	if err != nil {
		return err
	}
	if newImageID != manifest.AppImageID {
		return fmt.Errorf("loaded gateway image ID does not match the offline manifest")
	}
	if _, err := offlineDocker(ctx, "image", "tag", newImageID, offlineGatewayRepository+tag); err != nil {
		return err
	}
	if err := a.applyPackageMigrations(ctx, filepath.Join(packageDir, "migrations"), startupFiles); err != nil {
		return fmt.Errorf("execute migrations: %w", err)
	}

	filesChanged = true
	newEnv := offlineImageTagLine.ReplaceAllLiteral(envFile, []byte("APP_IMAGE_TAG="+tag))
	if err := os.WriteFile(envPath, newEnv, 0600); err != nil {
		return fmt.Errorf("write Compose environment: %w", err)
	}
	if err := os.WriteFile(versionPath, []byte(tag+"\n"), 0644); err != nil {
		return fmt.Errorf("write app/VERSION: %w", err)
	}
	composeStarted = true
	if err := a.recreateOfflineGateway(ctx, tag); err != nil {
		return err
	}
	runningID, err := a.gatewayImageID(ctx)
	if err != nil {
		return err
	}
	if runningID != newImageID {
		return fmt.Errorf("running gateway image does not match the offline package image")
	}
	return nil
}

func (a *OfflineApplier) recreateOfflineGateway(ctx context.Context, tag string) error {
	_, err := a.offlineCompose(ctx, tag, "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "120", offlineGatewayService)
	return err
}

func (a *OfflineApplier) gatewayImageID(ctx context.Context) (string, error) {
	container, err := a.ComposeServiceContainer(ctx, offlineGatewayService)
	if err != nil {
		return "", err
	}
	return offlineDocker(ctx, "inspect", "--format", "{{.Image}}", container)
}

// ComposeServiceContainer locates exactly one running service in the upgrade's project.
func (a *OfflineApplier) ComposeServiceContainer(ctx context.Context, service string) (string, error) {
	output, err := a.offlineCompose(ctx, "", "ps", "--status", "running", "-q", service)
	if err != nil {
		return "", err
	}
	containers := strings.Fields(output)
	if len(containers) != 1 {
		return "", fmt.Errorf("expected one running Compose service %q in project %q, got %d", service, a.composeProject, len(containers))
	}
	return containers[0], nil
}

func (a *OfflineApplier) offlineCompose(ctx context.Context, tag string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	options := []string{"compose", "-p", a.composeProject, "--env-file", filepath.Join(filepath.Dir(a.composeFile), ".env"), "-f", a.composeFile}
	cmd := exec.CommandContext(ctx, "docker", append(options, args...)...)
	if tag != "" {
		cmd.Env = append(os.Environ(), "APP_IMAGE_TAG="+tag)
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("docker compose %s: %w: %s", args[0], err, exit.Stderr)
		}
		return "", fmt.Errorf("docker compose %s: %w", args[0], err)
	}
	return string(out), nil
}

func offlineDocker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
