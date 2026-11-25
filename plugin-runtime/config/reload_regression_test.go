package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func auditConfigFile(t *testing.T, path, dir string, port int) {
	t.Helper()
	data := fmt.Sprintf("server:\n  host: localhost\n  port: %d\nplugins:\n  directory: %q\n", port, dir)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadRollsBackAppliedHandlers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	auditConfigFile(t, path, dir, 9090)
	initial := &Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}
	r, err := NewConfigReloader(initial, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	r.(*configReloader).configPath = path
	var order []int
	appliedPort := 8080
	r.OnReload(func(_ context.Context, _, next *Config) error {
		appliedPort = next.Server.Port
		order = append(order, next.Server.Port)
		if r.GetConfig().Server.Port != 8080 {
			t.Error("configuration published before apply completed")
		}
		return nil
	})
	r.OnReload(func(_ context.Context, _, next *Config) error {
		if next.Server.Port == 9090 {
			return errors.New("second apply failed")
		}
		return nil
	})
	if err := r.Reload(context.Background()); err == nil {
		t.Fatal("missing apply error")
	}
	if appliedPort != 8080 || !reflect.DeepEqual(order, []int{9090, 8080}) {
		t.Fatalf("rollback missing: port=%d order=%v", appliedPort, order)
	}
	if r.GetConfig().Server.Port != 8080 {
		t.Fatal("failed configuration was committed")
	}
}

func TestStopCancelsReloadAndAllCallersWait(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	auditConfigFile(t, path, dir, 9090)
	r, err := NewConfigReloader(&Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	r.(*configReloader).configPath = path
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.OnReload(func(ctx context.Context, _, next *Config) error {
		if next.Server.Port == 8080 {
			return nil
		}
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan error, 1)
	go func() { reloaded <- r.Reload(ctx) }()
	<-entered
	stopped := make(chan error, 2)
	go func() { stopped <- r.Stop() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		cancel()
		close(release)
		<-stopped
		<-reloaded
		t.Fatal("Stop did not cancel the active reload")
	}
	go func() { stopped <- r.Stop() }()
	select {
	case <-stopped:
		close(release)
		<-stopped
		<-reloaded
		t.Fatal("Stop returned before handler exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-reloaded; !errors.Is(err, context.Canceled) {
		t.Fatalf("reload error=%v", err)
	}
}

func TestAuditAtomicReplacePreservesWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	auditConfigFile(t, path, dir, 8080)
	initial := &Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}
	r, err := NewConfigReloader(initial, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	if err := r.Watch(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	// Establish a working watcher before replacing its inode.
	auditConfigFile(t, path, dir, 8081)
	until := time.Now().Add(5 * time.Second)
	for r.GetConfig().Server.Port != 8081 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.GetConfig().Server.Port != 8081 {
		t.Fatal("control: ordinary write did not reload")
	}
	replacement := filepath.Join(dir, "gateway.yaml.tmp")
	auditConfigFile(t, replacement, dir, 9090)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(5 * time.Second)
	for r.GetConfig().Server.Port != 9090 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.GetConfig().Server.Port; got != 9090 {
		t.Errorf("atomic save did not reload within 5s: got port %d, want 9090", got)
	}
}

func TestAuditHandlerFailurePreservesOldConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	auditConfigFile(t, path, dir, 9090)
	initial := &Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}
	r, err := NewConfigReloader(initial, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	// Set the path without starting a watch so this isolates manual reload.
	r.(*configReloader).configPath = path
	applyErr := errors.New("runtime rejected new config")
	if err := r.OnReload(func(context.Context, *Config, *Config) error { return applyErr }); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(context.Background()); err == nil {
		t.Error("Reload returned nil after handler failure")
	}
	if got := r.GetConfig().Server.Port; got != 8080 {
		t.Errorf("failed apply committed port %d, want old port 8080", got)
	}
}

// TestWatch_AtomicReplace verifies that watch survives atomic file replacement
// (mv tmp.yaml config.yaml), which changes the inode and can break watchers
// if not rebuilt (D9 regression test).
func TestWatch_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	auditConfigFile(t, path, dir, 8080)
	
	initial := &Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}
	r, err := NewConfigReloader(initial, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	
	// Track reload events
	reloadCh := make(chan int, 3)
	if err := r.OnReload(func(_ context.Context, _, next *Config) error {
		reloadCh <- next.Server.Port
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Start watching
	if err := r.Watch(ctx, path); err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	
	// 1st change: direct write (baseline, should work)
	time.Sleep(200 * time.Millisecond)
	auditConfigFile(t, path, dir, 9001)
	
	select {
	case port := <-reloadCh:
		if port != 9001 {
			t.Errorf("1st reload: got port %d, want 9001", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("1st reload: timeout waiting for direct write event")
	}
	
	// 2nd change: atomic replace (D9 regression trigger)
	time.Sleep(200 * time.Millisecond)
	tmpPath := filepath.Join(dir, "gateway.yaml.tmp")
	auditConfigFile(t, tmpPath, dir, 9002)
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("atomic replace failed: %v", err)
	}
	
	select {
	case port := <-reloadCh:
		if port != 9002 {
			t.Errorf("2nd reload: got port %d, want 9002", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2nd reload: timeout after atomic replace (watch not rebuilt)")
	}
	
	// 3rd change: verify watch still alive after atomic replace
	time.Sleep(200 * time.Millisecond)
	auditConfigFile(t, path, dir, 9003)
	
	select {
	case port := <-reloadCh:
		if port != 9003 {
			t.Errorf("3rd reload: got port %d, want 9003", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("3rd reload: timeout, watch stopped after atomic replace (D9 not fixed)")
	}
	
	t.Logf("SUCCESS: watch survived atomic replace and caught 3 changes (8080→9001→9002→9003)")
}

func TestRejectedConfigDeduplicatesValidationFailureByParsedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	initial := &Config{Server: ServerConfig{Host: "localhost", Port: 8080}, Plugins: PluginsConfig{Directory: dir}}
	r, err := NewConfigReloader(initial, NewValidator())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	r.(*configReloader).configPath = path

	writeInvalid := func(comment string, port int) {
		t.Helper()
		data := fmt.Sprintf("%s\nserver:\n  host: localhost\n  port: %d\nplugins:\n  directory: %q\n", comment, port, dir)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reloadInvalid := func() Rejection {
		t.Helper()
		if err := r.Reload(context.Background()); err == nil {
			t.Fatal("invalid listen port was accepted")
		}
		rejection, ok := RejectionOf(r)
		if !ok || !rejection.Invalid {
			t.Fatalf("validation failure not visible: %+v %t", rejection, ok)
		}
		return rejection
	}

	writeInvalid("", 0)
	first := reloadInvalid()
	writeInvalid("# changed comment", 0)
	if _, ok := RejectionOf(r); ok {
		t.Fatal("replaced file matched an older rejection before reload")
	}
	if second := reloadInvalid(); first != second {
		t.Fatalf("comment-only rewrite changed rejection ID: %x != %x", first.ID, second.ID)
	}
	writeInvalid("", -1)
	if different := reloadInvalid(); first == different {
		t.Fatal("different rejected config reused the earlier hash")
	}
	auditConfigFile(t, path, dir, 8080)
	if err := r.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := RejectionOf(r); ok {
		t.Fatal("accepted config still reports a rejection")
	}
}
