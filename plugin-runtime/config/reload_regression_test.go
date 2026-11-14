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
