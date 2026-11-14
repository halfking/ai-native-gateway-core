package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	root "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/config"
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func scan(cfg *config.Config) (map[string]*root.Manifest, error) {
	result := make(map[string]*root.Manifest, len(cfg.Plugins.Enabled))
	for _, id := range cfg.Plugins.Enabled {
		if !pluginIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid enabled plugin id %q", id)
		}
		if result[id] != nil {
			return nil, fmt.Errorf("duplicate enabled plugin id %q", id)
		}
		dir := filepath.Join(cfg.Plugins.Directory, id)
		path := filepath.Join(dir, "current", "plugin-manifest.json")
		if _, err := os.Lstat(filepath.Join(dir, "current")); os.IsNotExist(err) {
			path = filepath.Join(dir, "plugin-manifest.json")
		} else if err != nil {
			return nil, err
		}
		// Resolve the version link now so a later atomic 'current' switch cannot
		// silently change which manifest this reconciliation starts.
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("plugin %s manifest: %w", id, err)
		}
		m, err := root.LoadManifest(resolved)
		if err != nil {
			return nil, fmt.Errorf("plugin %s manifest: %w", id, err)
		}
		if m.PluginID != id {
			return nil, fmt.Errorf("plugin directory %s contains manifest for %s", id, m.PluginID)
		}
		if !root.Compatible(m.EffectiveAPI()) {
			return nil, fmt.Errorf("plugin %s host API %s incompatible", id, m.EffectiveAPI())
		}
		if blocked := root.RejectUnimplementedCapabilities(m.Capabilities); len(blocked) > 0 {
			return nil, fmt.Errorf("plugin %s unimplemented capabilities %v", id, blocked)
		}
		result[id] = m
	}
	return result, nil
}

// snapshot pins a whole bundle, including its signed manifest. This makes
// rollback work for both immutable version/current layouts and legacy mutable
// plugin directories. Runtime copies are private and discarded at shutdown.
func (c *Controller) snapshot(m *root.Manifest) (*root.Manifest, error) {
	if strings.HasPrefix(m.ManifestPath, c.runtimeDir+string(filepath.Separator)) {
		return m, nil
	}
	dir, err := os.MkdirTemp(c.runtimeDir, "bundle-")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(dir)
		}
	}()
	source := filepath.Dir(m.ManifestPath)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(resolved, source+string(filepath.Separator)) || filepath.IsAbs(link) {
				return fmt.Errorf("plugin bundle symlink %s must remain inside bundle", relative)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin bundle contains non-regular file %s", relative)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0700)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		return errorsJoinClose(copyErr, out.Close())
	})
	if err != nil {
		return nil, err
	}
	frozen, err := root.LoadManifest(filepath.Join(dir, filepath.Base(m.ManifestPath)))
	if err != nil {
		return nil, err
	}
	if !sameManifest(m, frozen) {
		return nil, fmt.Errorf("plugin %s changed while snapshotting", m.PluginID)
	}
	frozen.Runtime.Entrypoint = filepath.Join(dir, frozen.Runtime.Entrypoint)
	failed = false
	return frozen, nil
}

// Retain actual and accepted retryable bundles after commit/rollback. Recovery
// and rollback can both fail temporarily, leaving desired as the sole owner.
func (c *Controller) pruneSnapshots() {
	keep := make(map[string]bool)
	for id, m := range c.desired {
		if !c.disabled[id] {
			keep[filepath.Dir(m.ManifestPath)] = true
		}
	}
	c.mu.RLock()
	for _, m := range c.actual {
		keep[filepath.Dir(m.ManifestPath)] = true
	}
	c.mu.RUnlock()
	entries, err := os.ReadDir(c.runtimeDir)
	if err != nil {
		log.Printf("plugin runtime snapshot cleanup: %v", err)
		return
	}
	for _, entry := range entries {
		path := filepath.Join(c.runtimeDir, entry.Name())
		if strings.HasPrefix(entry.Name(), "bundle-") && !keep[path] {
			if err := os.RemoveAll(path); err != nil {
				log.Printf("plugin runtime snapshot cleanup: %v", err)
			}
		}
	}
}

func errorsJoinClose(copyErr, closeErr error) error {
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func sameManifest(a, b *root.Manifest) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func copyManifest(m *root.Manifest) *root.Manifest {
	if m == nil {
		return nil
	}
	data, _ := json.Marshal(m)
	var copy root.Manifest
	_ = json.Unmarshal(data, &copy)
	copy.ManifestPath = m.ManifestPath
	return &copy
}
