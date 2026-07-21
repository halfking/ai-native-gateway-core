package pluginruntime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// InstallerConfig wires the Installer to its collaborators.
type InstallerConfig struct {
	PluginsDir    string                 // <LLM_GATEWAY_PLUGINS_DIR>
	Client        *MaintainCatalogClient // maintain HTTP client
	Supervisor    *Supervisor            // runs the plugin process
	Registry      *Registry              // nav/state index
	SigningPubkey string                 // ed25519 hex; "" skips verify (dev mode)
}

// Installer orchestrates the plugin install pipeline:
// catalog -> ticket -> download -> extract -> manifest verify -> atomic switch.
type Installer struct {
	cfg InstallerConfig
}

func NewInstaller(cfg InstallerConfig) *Installer {
	return &Installer{cfg: cfg}
}

// Install fetches pluginID@version from maintain and installs it under
// <pluginsDir>/<pluginID>/<version>/, points <pluginsDir>/<pluginID>/current
// at it, and calls Supervisor.Upgrade to start the new version.
func (in *Installer) Install(ctx context.Context, pluginID, version string) error {
	entries, err := in.cfg.Client.Catalog(ctx)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	_, _, err = in.cfg.Client.FindRelease(entries, pluginID, version)
	if err != nil {
		return err
	}

	ticket, err := in.cfg.Client.Ticket(ctx, pluginID, version, 0)
	if err != nil {
		return fmt.Errorf("ticket: %w", err)
	}

	// download into a staging dir, then extract into the versioned target.
	staging := filepath.Join(in.cfg.PluginsDir, ".staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}
	dlPath, err := in.cfg.Client.DownloadArtifact(ctx, ticket, staging)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(dlPath)

	versionedDir := filepath.Join(in.cfg.PluginsDir, pluginID, version)
	if err := os.MkdirAll(versionedDir, 0o755); err != nil {
		return fmt.Errorf("mkdir versioned: %w", err)
	}
	// clean any prior partial extraction in versionedDir
	existing, _ := os.ReadDir(versionedDir)
	for _, e := range existing {
		os.RemoveAll(filepath.Join(versionedDir, e.Name()))
	}

	f, err := os.Open(dlPath)
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()
	if _, err := ExtractTarball(f, versionedDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	manifestPath := filepath.Join(versionedDir, "plugin-manifest.json")
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if m.PluginID != pluginID {
		return fmt.Errorf("manifest plugin_id mismatch: tarball says %q, requested %q", m.PluginID, pluginID)
	}
	if m.PluginVersion != version {
		return fmt.Errorf("manifest version mismatch: tarball says %q, requested %q", m.PluginVersion, version)
	}

	// ed25519 manifest signature is verified by Supervisor.Start (via
	// VerifyManifestSignature with SigningPubkey). Nothing extra here.

	// atomic switch: rewrite `current` symlink via tmp + rename
	currentLink := filepath.Join(in.cfg.PluginsDir, pluginID, "current")
	tmpLink := currentLink + ".tmp." + version
	os.Remove(tmpLink)
	if err := os.Symlink(versionedDir, tmpLink); err != nil {
		return fmt.Errorf("symlink tmp: %w", err)
	}
	if err := os.Rename(tmpLink, currentLink); err != nil {
		os.Remove(tmpLink)
		return fmt.Errorf("rename symlink: %w", err)
	}

	// make entrypoint absolute against the versioned dir (ScanAndStartPlugins convention)
	m2 := *m
	m2.Runtime.Entrypoint = filepath.Join(versionedDir, m.Runtime.Entrypoint)
	if err := in.cfg.Supervisor.Upgrade(ctx, &m2); err != nil {
		slog.Error("plugin upgrade failed after install", "plugin", pluginID, "version", version, "error", err)
		return fmt.Errorf("upgrade: %w", err)
	}

	// refresh registry nav/state from new manifest
	in.cfg.Registry.SetPlugin(&PluginState{
		PluginID: m.PluginID, PluginVersion: m.PluginVersion, Status: "ready",
	})
	in.cfg.Registry.SetNav(m.PluginID, m.PluginVersion, m.Pages)
	return nil
}
