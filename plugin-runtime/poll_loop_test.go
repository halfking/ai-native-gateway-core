package pluginruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeCatalogLister is a test stand-in for *MaintainCatalogClient.
type fakeCatalogLister struct {
	entries []CatalogEntry
	err     error // returned by Catalog
	findErr error // returned by FindRelease (simulates incompatible/not-found)
}

func (f *fakeCatalogLister) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	return f.entries, f.err
}

func (f *fakeCatalogLister) FindRelease(entries []CatalogEntry, pluginID, version string) (CatalogRelease, CatalogArtifact, error) {
	if f.findErr != nil {
		return CatalogRelease{}, CatalogArtifact{}, f.findErr
	}
	return CatalogRelease{PluginVersion: version, GatewayMinVersion: "0.0.0"}, CatalogArtifact{}, nil
}

// fakeUpgrader records Install calls.
type fakeUpgrader struct {
	calls []installCall
	err   error // if non-nil, every Install returns this
}

type installCall struct {
	pluginID string
	version  string
}

func (f *fakeUpgrader) Install(ctx context.Context, pluginID, version string) error {
	f.calls = append(f.calls, installCall{pluginID: pluginID, version: version})
	return f.err
}

func newTestPollLoop(lister *fakeCatalogLister, upgrader *fakeUpgrader, installed map[string]string) *PollLoop {
	return NewPollLoop(PollLoopConfig{
		Interval: time.Minute,
		Client:   lister,
		Installer: upgrader,
		InstalledVersions: func() map[string]string {
			// return a copy so the test's map isn't mutated by the loop
			out := make(map[string]string, len(installed))
			for k, v := range installed {
				out[k] = v
			}
			return out
		},
		GatewayVersion: "2.4.0",
	})
}

func TestPollLoop_UpgradesToNewerVersion(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.2.0"},
		},
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{"asm": "0.1.0"})

	p.tick(context.Background())

	if len(upgrader.calls) != 1 {
		t.Fatalf("expected 1 install call, got %d: %+v", len(upgrader.calls), upgrader.calls)
	}
	if upgrader.calls[0].pluginID != "asm" || upgrader.calls[0].version != "0.2.0" {
		t.Errorf("unexpected call: %+v", upgrader.calls[0])
	}
}

func TestPollLoop_SkipsUpToDate(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.2.0"},
		},
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{"asm": "0.2.0"})

	p.tick(context.Background())

	if len(upgrader.calls) != 0 {
		t.Fatalf("expected 0 install calls, got %d: %+v", len(upgrader.calls), upgrader.calls)
	}
}

func TestPollLoop_SkipsUninstalledPlugin(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.2.0"},
		},
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{})

	p.tick(context.Background())

	if len(upgrader.calls) != 0 {
		t.Fatalf("expected 0 install calls for uninstalled plugin, got %d", len(upgrader.calls))
	}
}

func TestPollLoop_SkipsOnFindReleaseError(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.3.0"},
		},
		findErr: errors.New("incompatible"),
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{"asm": "0.1.0"})

	p.tick(context.Background())

	if len(upgrader.calls) != 0 {
		t.Fatalf("expected 0 install calls on incompatible release, got %d", len(upgrader.calls))
	}
}

func TestPollLoop_CatalogErrorNoPanic(t *testing.T) {
	lister := &fakeCatalogLister{
		err: errors.New("network down"),
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{"asm": "0.1.0"})

	// should not panic and should not call Install
	p.tick(context.Background())

	if len(upgrader.calls) != 0 {
		t.Fatalf("expected 0 install calls on catalog error, got %d", len(upgrader.calls))
	}
}

func TestPollLoop_OnePluginFailureDoesNotBlockOthers(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.2.0"},
			{PluginID: "other", LatestVersion: "2.0.0"},
		},
	}
	upgrader := &fakeUpgrader{err: errors.New("install blew up")}
	p := newTestPollLoop(lister, upgrader, map[string]string{
		"asm":   "0.1.0",
		"other": "1.0.0",
	})

	p.tick(context.Background())

	// both should have been attempted despite the first failure
	if len(upgrader.calls) != 2 {
		t.Fatalf("expected 2 install calls (both attempted), got %d: %+v", len(upgrader.calls), upgrader.calls)
	}
	seen := map[string]bool{}
	for _, c := range upgrader.calls {
		seen[c.pluginID] = true
	}
	if !seen["asm"] || !seen["other"] {
		t.Errorf("expected both asm and other to be attempted, got %+v", upgrader.calls)
	}
}

func TestPollLoop_MultiplePluginsUpgraded(t *testing.T) {
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: "0.2.0"},
			{PluginID: "other", LatestVersion: "2.0.0"},
		},
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{
		"asm":   "0.1.0",
		"other": "1.0.0",
	})

	p.tick(context.Background())

	if len(upgrader.calls) != 2 {
		t.Fatalf("expected 2 install calls, got %d: %+v", len(upgrader.calls), upgrader.calls)
	}
	gotVersions := map[string]string{}
	for _, c := range upgrader.calls {
		gotVersions[c.pluginID] = c.version
	}
	if gotVersions["asm"] != "0.2.0" {
		t.Errorf("asm version: want 0.2.0, got %q", gotVersions["asm"])
	}
	if gotVersions["other"] != "2.0.0" {
		t.Errorf("other version: want 2.0.0, got %q", gotVersions["other"])
	}
}

func TestPollLoop_SkipsEmptyLatestVersion(t *testing.T) {
	// defensive: catalog entry with empty LatestVersion should be skipped,
	// not treated as "upgrade to empty string".
	lister := &fakeCatalogLister{
		entries: []CatalogEntry{
			{PluginID: "asm", LatestVersion: ""},
		},
	}
	upgrader := &fakeUpgrader{}
	p := newTestPollLoop(lister, upgrader, map[string]string{"asm": "0.1.0"})

	p.tick(context.Background())

	if len(upgrader.calls) != 0 {
		t.Fatalf("expected 0 install calls for empty LatestVersion, got %d", len(upgrader.calls))
	}
}
