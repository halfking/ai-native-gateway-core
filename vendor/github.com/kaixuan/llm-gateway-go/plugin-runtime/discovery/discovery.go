// Package discovery implements plugin discovery and hot-reload mechanisms.
//
// Discovery scans plugin directories for manifest.yaml files, validates them,
// verifies signatures (Ed25519), and emits events when plugins are added,
// modified, or deleted.
package discovery

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Discovery handles plugin discovery and file watching.
type Discovery struct {
	watcher   *fsnotify.Watcher
	validator *Validator
	publicKey ed25519.PublicKey

	mu        sync.RWMutex
	manifests map[string]*Manifest // pluginID -> manifest
}

// NewDiscovery creates a new Discovery instance.
func NewDiscovery(publicKey ed25519.PublicKey) (*Discovery, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	return &Discovery{
		watcher:   watcher,
		validator: NewValidator(),
		publicKey: publicKey,
		manifests: make(map[string]*Manifest),
	}, nil
}

// Scan scans directories for plugin manifests.
func (d *Discovery) Scan(ctx context.Context, dirs []string) ([]*Manifest, error) {
	var manifests []*Manifest

	for _, dir := range dirs {
		found, err := d.scanDirectory(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to scan %s: %w", dir, err)
		}
		manifests = append(manifests, found...)
	}

	// Store discovered manifests
	d.mu.Lock()
	for _, m := range manifests {
		d.manifests[m.Metadata.Name] = m
	}
	d.mu.Unlock()

	return manifests, nil
}

func (d *Discovery) scanDirectory(dir string) ([]*Manifest, error) {
	var manifests []*Manifest

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if !isManifestFile(path) {
			return nil
		}

		manifest, err := d.loadAndValidateManifest(path)
		if err != nil {
			// Log error but continue scanning
			fmt.Printf("WARN: failed to load manifest %s: %v\n", path, err)
			return nil
		}

		manifests = append(manifests, manifest)
		return nil
	})

	return manifests, err
}

// Watch watches directories for manifest changes and emits events.
func (d *Discovery) Watch(ctx context.Context, dirs []string) (<-chan Event, error) {
	events := make(chan Event, 16)

	// Add all directories to watcher
	for _, dir := range dirs {
		if err := d.watcher.Add(dir); err != nil {
			return nil, fmt.Errorf("failed to watch %s: %w", dir, err)
		}

		// Also watch subdirectories
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			d.watcher.Add(path)
			return nil
		})
	}

	// Start event processing goroutine
	go d.processEvents(ctx, events)

	return events, nil
}

func (d *Discovery) processEvents(ctx context.Context, events chan<- Event) {
	defer close(events)

	for {
		select {
		case event, ok := <-d.watcher.Events:
			if !ok {
				return
			}

			// 2026-11-14 audit D6: a directory created AFTER Watch started is
			// not watched yet — fsnotify only delivers events for watched
			// dirs, so the manifest written inside it (the plugin-install
			// shape: mkdir + write manifest.yaml) is never seen and the
			// EventAdded is lost. Recursively watch the new tree AND sweep
			// manifests that landed before the watch was established (the
			// mkdir→write race: the file Create event fires against a
			// not-yet-watched dir and is lost for good).
			if event.Op&fsnotify.Create == fsnotify.Create {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					var swept []*Event
					filepath.Walk(event.Name, func(p string, i os.FileInfo, err error) error {
						if err != nil {
							return nil
						}
						if i.IsDir() {
							d.watcher.Add(p)
						}
						if isManifestFile(p) {
							if e := d.handleFSEvent(fsnotify.Event{Name: p, Op: fsnotify.Create}); e != nil {
								swept = append(swept, e)
							}
						}
						return nil
					})
					for _, e := range swept {
						select {
						case events <- *e:
						case <-ctx.Done():
							return
						}
					}
				}
			}

			if !isManifestFile(event.Name) {
				continue
			}

			e := d.handleFSEvent(event)
			if e != nil {
				select {
				case events <- *e:
				case <-ctx.Done():
					return
				}
			}

		case err, ok := <-d.watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("ERROR: watcher error: %v\n", err)

		case <-ctx.Done():
			return
		}
	}
}

func (d *Discovery) handleFSEvent(event fsnotify.Event) *Event {
	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		manifest, err := d.loadAndValidateManifest(event.Name)
		if err != nil {
			fmt.Printf("ERROR: failed to load new manifest %s: %v\n", event.Name, err)
			return nil
		}

		d.mu.Lock()
		d.manifests[manifest.Metadata.Name] = manifest
		d.mu.Unlock()

		return &Event{
			Type:      EventAdded,
			Manifest:  manifest,
			Timestamp: time.Now(),
		}

	case event.Op&fsnotify.Write == fsnotify.Write:
		manifest, err := d.loadAndValidateManifest(event.Name)
		if err != nil {
			fmt.Printf("ERROR: failed to reload manifest %s: %v\n", event.Name, err)
			return nil
		}

		d.mu.Lock()
		d.manifests[manifest.Metadata.Name] = manifest
		d.mu.Unlock()

		return &Event{
			Type:      EventModified,
			Manifest:  manifest,
			Timestamp: time.Now(),
		}

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		// Extract plugin ID from path
		pluginID := extractPluginIDFromPath(event.Name)

		d.mu.Lock()
		delete(d.manifests, pluginID)
		d.mu.Unlock()

		return &Event{
			Type: EventDeleted,
			Manifest: &Manifest{
				Metadata: Metadata{Name: pluginID},
			},
			Timestamp: time.Now(),
		}
	}

	return nil
}

func (d *Discovery) loadAndValidateManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate manifest
	if err := d.validator.Validate(&manifest); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Verify signature if public key is configured
	if d.publicKey != nil {
		if err := d.VerifySignature(&manifest); err != nil {
			return nil, fmt.Errorf("signature verification failed: %w", err)
		}
	}

	return &manifest, nil
}

// VerifySignature verifies the Ed25519 signature of a manifest.
func (d *Discovery) VerifySignature(manifest *Manifest) error {
	if d.publicKey == nil {
		return nil // Skip verification if no public key
	}

	// Store and clear signature
	signatureValue := manifest.Signature
	manifest.Signature = ""

	// Serialize to canonical form
	canonical, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}

	// Restore signature
	manifest.Signature = signatureValue

	// Decode signature
	signature, err := base64.StdEncoding.DecodeString(signatureValue)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}

	// Verify
	if !ed25519.Verify(d.publicKey, canonical, signature) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// Close closes the discovery watcher.
func (d *Discovery) Close() error {
	return d.watcher.Close()
}

// Helper functions

func isManifestFile(path string) bool {
	base := filepath.Base(path)
	return base == "manifest.yaml" || base == "manifest.yml"
}

func extractPluginIDFromPath(path string) string {
	// Extract plugin ID from path like /plugins/llm-gateway-audit/manifest.yaml
	dir := filepath.Dir(path)
	return filepath.Base(dir)
}
