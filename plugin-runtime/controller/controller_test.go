package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/ipc"
)

// TestControllerHelper runs in a real child process with restricted environment.
// The HTTP bridge makes the child itself use its inherited IPC identity; the
// parent never publishes directly to the in-process event bus.
func TestControllerHelper(t *testing.T) {
	socket := os.Getenv("AI_SESSION_MANAGER_PLUGIN_SOCKET")
	if socket == "" {
		return
	}
	m, err := pluginruntime.LoadManifest(os.Getenv("AI_SESSION_MANAGER_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var healthStatus atomic.Int32
	healthStatus.Store(http.StatusOK)
	mux.HandleFunc("/plugin/handshake", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"plugin_id": m.PluginID, "plugin_version": m.PluginVersion, "api_contract": "gateway-plugin-v1", "status": "ready"})
	})
	mux.HandleFunc("/plugin/healthz", func(w http.ResponseWriter, r *http.Request) {
		status := healthStatus.Load()
		if status == 0 {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(int(status))
	})
	mux.HandleFunc("/test/health", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Status int32 `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		healthStatus.Store(request.Status)
		_, _ = w.Write([]byte(`{}`))
	})
	client := ipc.NewClient(os.Getenv("GATEWAY_PLUGIN_EVENT_SOCKET"))
	defer client.Close()
	mux.HandleFunc("/test/event", func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if id, exists := request["plugin_id"]; !exists || id == "" {
			request["plugin_id"] = os.Getenv("GATEWAY_PLUGIN_ID")
		}
		request["token"] = os.Getenv("GATEWAY_PLUGIN_EVENT_TOKEN")
		data, err := json.Marshal(request)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		response, err := client.Send(r.Context(), data)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	})
	if err := http.Serve(listener, mux); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "plugin-controller-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		writePlugin(t, plugins, id, "1.0.0", false)
	}
	path := filepath.Join(dir, "gateway.yaml")
	writeConfig(t, path, plugins, "one, two")
	return plugins, path
}

func writePlugin(t *testing.T, dir, id, version string, broken bool) {
	t.Helper()
	p := filepath.Join(dir, id)
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run='^TestControllerHelper$'\n", strings.ReplaceAll(exe, "'", "'\\''"))
	if broken {
		script = "#!/bin/sh\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(p, "run"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{"plugin_id": id, "plugin_version": version, "gateway_compatibility": map[string]string{"api_contract": "gateway-plugin-v1"}, "runtime": map[string]string{"entrypoint": "run", "protocol": "http-unix-socket", "handshake_path": "/plugin/handshake", "health_path": "/plugin/healthz"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "plugin-manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, path, dir, enabled string) {
	t.Helper()
	data := fmt.Sprintf("server:\n  host: localhost\n  port: 8080\nplugins:\n  directory: %q\n  enabled: [%s]\n", dir, enabled)
	// Atomic replace also tests that the reloader keeps watching the directory.
	if err := os.WriteFile(path+".new", []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func startController(t *testing.T, path string) *Controller {
	t.Helper()
	c, err := Start(context.Background(), Options{ConfigPath: path, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func pluginCall(t *testing.T, c *Controller, id, path string, body any, target any) {
	t.Helper()
	socket := strings.TrimPrefix(c.BaseURL(id), "unix://")
	if socket == "" {
		t.Fatalf("plugin %s has no route", id)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post("http://unix"+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("plugin response %d: %s", response.StatusCode, data)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func childEvent(t *testing.T, c *Controller, id string, request any) EventResponse {
	t.Helper()
	var response EventResponse
	pluginCall(t, c, id, "/test/event", request, &response)
	return response
}

func assertVersion(t *testing.T, c *Controller, id, version string) {
	t.Helper()
	if got := c.Manifest(id); got == nil || got.PluginVersion != version {
		t.Fatalf("actual manifest: %+v; want %s", got, version)
	}
	var hs pluginruntime.HandshakeResponse
	pluginCall(t, c, id, "/plugin/handshake", nil, &hs)
	if hs.PluginVersion != version {
		t.Fatalf("child version %s, want %s", hs.PluginVersion, version)
	}
}

func TestControllerRealProcessesReloadAndUpgrade(t *testing.T) {
	plugins, path := fixture(t)
	c := startController(t, path)
	if c.BaseURL("one") == "" || c.BaseURL("two") == "" {
		t.Fatal("plugins did not become ready")
	}
	assertVersion(t, c, "one", "1.0.0")
	writeConfig(t, path, plugins, "one")
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.BaseURL("two") != "" || c.Manifest("two") != nil {
		t.Fatal("disabled plugin still ready")
	}
	writeConfig(t, path, plugins, "one, two")
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.BaseURL("two") == "" {
		t.Fatal("enabled plugin did not start")
	}
	writePlugin(t, plugins, "one", "2.0.0", false)
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, c, "one", "2.0.0")
	states := c.Supervisor().ProcessSnapshot()
	runtimeDir := c.runtimeDir
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.BaseURL("one") != "" || c.Manifest("one") != nil {
		t.Fatal("closed runtime still routes requests")
	}
	for _, state := range states {
		if state.Pid > 0 && syscall.Kill(state.Pid, 0) == nil {
			t.Errorf("child %s PID %d survived Close", state.PluginID, state.Pid)
		}
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Errorf("runtime files survived close: %v", err)
	}
}

func TestControllerFailedUpgradeRestoresActualBundle(t *testing.T) {
	plugins, path := fixture(t)
	writeConfig(t, path, plugins, "one")
	c := startController(t, path)
	writePlugin(t, plugins, "one", "2.0.0", true)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := c.Reconcile(ctx); err == nil {
		t.Fatal("failed readiness reported upgrade success")
	}
	assertVersion(t, c, "one", "1.0.0")
	c.opMu.Lock()
	version := c.desired["one"].PluginVersion
	c.opMu.Unlock()
	if version != "1.0.0" {
		t.Fatal("failed upgrade committed desired state")
	}
}

func TestControllerFailedConfigRestoresStoppedPlugin(t *testing.T) {
	plugins, path := fixture(t)
	writeConfig(t, path, plugins, "one")
	c := startController(t, path)
	writePlugin(t, plugins, "two", "2.0.0", true)
	// Destroy the source bundle of the old plugin: rollback needs its snapshot.
	writePlugin(t, plugins, "one", "3.0.0", true)
	writeConfig(t, path, plugins, "two")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := c.Reload(ctx); err == nil {
		t.Fatal("failed application committed config")
	}
	assertVersion(t, c, "one", "1.0.0")
	if c.BaseURL("two") != "" {
		t.Fatal("failed plugin kept ready route")
	}
	if got := c.reloader.GetConfig().Plugins.Enabled; len(got) != 1 || got[0] != "one" {
		t.Fatalf("config not rolled back: %v", got)
	}
}

func TestControllerCrossProcessEventsAndIdentity(t *testing.T) {
	plugins, path := fixture(t)
	c := startController(t, path)
	dir, err := os.Stat(c.runtimeDir)
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatalf("runtime directory permissions: %v %v", dir, err)
	}
	socket, err := os.Stat(c.events.socket)
	if err != nil || socket.Mode().Perm() != 0600 {
		t.Fatalf("event socket permissions: %v %v", socket, err)
	}
	subscribed := childEvent(t, c, "two", EventRequest{Type: "event.subscribe", Pattern: "sample.*"})
	if !subscribed.Success || subscribed.SubscriptionID == "" {
		t.Fatalf("subscribe: %+v", subscribed)
	}
	published := childEvent(t, c, "one", map[string]any{"type": "event.publish", "event_type": "sample.changed", "payload": map[string]any{"answer": 42}, "source": "two"})
	if !published.Success {
		t.Fatalf("publish: %+v", published)
	}
	var received EventResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		received = childEvent(t, c, "two", EventRequest{Type: "event.poll", SubscriptionID: subscribed.SubscriptionID})
		if len(received.Events) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(received.Events) != 1 || received.Events[0].Source != "one" || string(received.Events[0].Payload) != `{"answer":42}` {
		t.Fatalf("cross-process event lost or spoofed: %+v", received)
	}
	forged := childEvent(t, c, "one", map[string]any{"type": "event.publish", "plugin_id": "two", "event_type": "sample.changed", "payload": nil})
	if forged.Success || !strings.Contains(forged.Error, "identity") {
		t.Fatalf("forged identity accepted: %+v", forged)
	}
	stolen := childEvent(t, c, "one", EventRequest{Type: "event.poll", SubscriptionID: subscribed.SubscriptionID})
	if stolen.Success {
		t.Fatal("other plugin polled private subscription")
	}
	for i := 0; i < eventQueueSize*3; i++ {
		response := childEvent(t, c, "one", EventRequest{Type: "event.publish", EventType: "sample.changed", Payload: json.RawMessage(`{"bounded":true}`)})
		if !response.Success {
			t.Fatalf("publish bounded event: %+v", response)
		}
	}
	time.Sleep(30 * time.Millisecond)
	bounded := childEvent(t, c, "two", EventRequest{Type: "event.poll", SubscriptionID: subscribed.SubscriptionID})
	if len(bounded.Events) != eventQueueSize || bounded.Dropped == 0 {
		t.Fatalf("queue bound/drop accounting: %+v", bounded)
	}
	for i := 1; i < maxPluginSubscriptions; i++ {
		response := childEvent(t, c, "two", EventRequest{Type: "event.subscribe", Pattern: "sample.*"})
		if !response.Success {
			t.Fatalf("subscribe %d: %+v", i, response)
		}
	}
	limited := childEvent(t, c, "two", EventRequest{Type: "event.subscribe", Pattern: "sample.*"})
	if limited.Success {
		t.Fatal("unbounded subscriptions")
	}
	unsubscribed := childEvent(t, c, "two", EventRequest{Type: "event.unsubscribe", SubscriptionID: subscribed.SubscriptionID})
	if !unsubscribed.Success {
		t.Fatalf("unsubscribe: %+v", unsubscribed)
	}
	if response := childEvent(t, c, "two", EventRequest{Type: "event.poll", SubscriptionID: subscribed.SubscriptionID}); response.Success {
		t.Fatal("unsubscribed queue still readable")
	}
	c.events.mu.Lock()
	oldToken := c.events.identities["two"].token
	c.events.mu.Unlock()
	writeConfig(t, path, plugins, "one")
	if err := c.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.events.mu.Lock()
	active := c.events.identities["two"] != nil
	count := c.events.subscriptionCount
	c.events.mu.Unlock()
	if active || count != 0 {
		t.Fatal("disabled plugin identity/subscriptions survived")
	}
	client := ipc.NewClient(c.events.socket)
	defer client.Close()
	data, _ := json.Marshal(EventRequest{Type: "event.subscribe", PluginID: "two", Token: oldToken, Pattern: "*"})
	response, err := client.Send(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	var rejected EventResponse
	if err := json.Unmarshal(response, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Success {
		t.Fatal("disabled token still accepted")
	}
}

func TestControllerRejectsImmutableConfigChanges(t *testing.T) {
	_, path := fixture(t)
	c := startController(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("port: 8080"), []byte("port: 9090"), 1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Reload(context.Background()); err == nil {
		t.Fatal("live server port silently changed")
	}
	if c.reloader.GetConfig().Server.Port != 8080 || c.BaseURL("one") == "" {
		t.Fatal("rejected config altered live state")
	}
}

func TestControllerConfigWatchAndLifecycleCancellation(t *testing.T) {
	plugins, path := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Start(ctx, Options{ConfigPath: path, Interval: time.Hour})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer c.Close()
	states := c.Supervisor().ProcessSnapshot()
	writeConfig(t, path, plugins, "one")
	deadline := time.Now().Add(3 * time.Second)
	for c.BaseURL("two") != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.BaseURL("two") != "" {
		t.Fatal("config watcher did not apply atomic replacement")
	}
	cancel()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.Pid > 0 && syscall.Kill(state.Pid, 0) == nil {
			t.Errorf("PID %d survived cancellation", state.Pid)
		}
	}
}

func TestControllerUsesCurrentVersionAndRecoversCrash(t *testing.T) {
	plugins, path := fixture(t)
	writeConfig(t, path, plugins, "one")
	versionDir := filepath.Join(plugins, "one", "versions")
	writePlugin(t, versionDir, "v1", "1.0.0", false)
	manifest := filepath.Join(versionDir, "v1", "plugin-manifest.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"plugin_id":"v1"`), []byte(`"plugin_id":"one"`), 1)
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("versions/v1", filepath.Join(plugins, "one", "current")); err != nil {
		t.Fatal(err)
	}
	c := startController(t, path)
	old := c.Supervisor().ProcessSnapshot()[0].Pid
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for c.BaseURL("one") != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, c, "one", "1.0.0")
	if next := c.Supervisor().ProcessSnapshot()[0].Pid; next == old {
		t.Fatal("crashed child did not restart")
	}
}

func TestControllerOperationContextAndLifecycleEvents(t *testing.T) {
	plugins, path := fixture(t)
	writeConfig(t, path, plugins, "two")
	c := startController(t, path)
	subscribed := childEvent(t, c, "two", EventRequest{Type: "event.subscribe", Pattern: "plugin.*"})
	if !subscribed.Success {
		t.Fatalf("subscribe: %+v", subscribed)
	}
	writeConfig(t, path, plugins, "one, two")
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Reload(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond)
	assertVersion(t, c, "one", "1.0.0")
	writePlugin(t, plugins, "one", "2.0.0", false)
	ctx, cancel = context.WithCancel(context.Background())
	if err := c.Reconcile(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond)
	assertVersion(t, c, "one", "2.0.0")
	response := childEvent(t, c, "two", EventRequest{Type: "event.poll", SubscriptionID: subscribed.SubscriptionID})
	seen := make(map[string]bool)
	for _, event := range response.Events {
		if event.Source != "plugin-runtime" {
			t.Fatalf("lifecycle event source: %+v", event)
		}
		seen[event.Type] = true
	}
	for _, kind := range []string{"plugin.started", "plugin.stopped", "plugin.upgraded"} {
		if !seen[kind] {
			t.Errorf("missing actual supervisor lifecycle event %s: %+v", kind, response)
		}
	}
}

func TestControllerRecoversLiveUnhealthyProcess(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			plugins, path := fixture(t)
			writeConfig(t, path, plugins, "one")
			entered := make(chan struct{})
			release := make(chan struct{})
			var starts atomic.Int32
			c, err := Start(context.Background(), Options{ConfigPath: path, Interval: time.Hour, Ready: func(ctx context.Context, socket string, m *pluginruntime.Manifest) error {
				if err := waitReady(ctx, socket, m); err != nil {
					return err
				}
				if starts.Add(1) > 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			old := c.Supervisor().ProcessSnapshot()[0].Pid
			var response map[string]any
			pluginCall(t, c, "one", "/test/health", map[string]int{"status": status}, &response)
			if err := syscall.Kill(old, 0); err != nil {
				t.Fatal("helper exited before health check", err)
			}
			if err := c.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c.BaseURL("one") == "" {
				t.Fatal("single transient health failure triggered restart")
			}
			done := make(chan error, 1)
			go func() { done <- c.Reconcile(context.Background()) }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("second health failure did not restart live child: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("health probe/restart did not respect time bound")
			}
			if c.BaseURL("one") != "" {
				t.Fatal("unhealthy process kept a ready proxy route during recovery")
			}
			for _, state := range c.registry.Snapshot() {
				if state.PluginID == "one" && state.Status == "ready" {
					t.Fatal("unhealthy process remained ready in registry")
				}
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			assertVersion(t, c, "one", "1.0.0")
			if next := c.Supervisor().ProcessSnapshot()[0].Pid; next == old {
				t.Fatal("unhealthy PID survived recovery")
			}
		})
	}
}

func TestControllerInvalidDiskManifestDoesNotBlockRecovery(t *testing.T) {
	plugins, path := fixture(t)
	c := startController(t, path)
	pids := make(map[string]int)
	for _, state := range c.Supervisor().ProcessSnapshot() {
		pids[state.PluginID] = state.Pid
	}
	if err := os.WriteFile(filepath.Join(plugins, "two", "plugin-manifest.json"), []byte(`{"plugin_id":`), 0600); err != nil {
		t.Fatal(err)
	}
	// Also remove A's executable: its restart must use the accepted pinned bundle.
	if err := os.Remove(filepath.Join(plugins, "one", "run")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pids["one"], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for c.BaseURL("one") != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "plugin two manifest") {
		t.Fatalf("invalid deployment scan error lost: %v", err)
	}
	assertVersion(t, c, "one", "1.0.0")
	assertVersion(t, c, "two", "1.0.0")
	for _, state := range c.Supervisor().ProcessSnapshot() {
		if state.PluginID == "one" && state.Pid == pids["one"] {
			t.Fatal("A did not restart from its accepted bundle")
		}
		if state.PluginID == "two" && state.Pid != pids["two"] {
			t.Fatal("invalid B manifest disturbed the old ready process")
		}
	}
}

func TestControllerInitialInvalidManifestFailsClosed(t *testing.T) {
	plugins, path := fixture(t)
	if err := os.WriteFile(filepath.Join(plugins, "two", "plugin-manifest.json"), []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(context.Background(), Options{ConfigPath: path})
	if c != nil {
		_ = c.Close()
		t.Fatal("invalid initial deployment returned a controller")
	}
	if err == nil {
		t.Fatal("invalid initial deployment was accepted")
	}
}

func TestControllerRetainsAcceptedBundleAfterFailedRecovery(t *testing.T) {
	plugins, path := fixture(t)
	writeConfig(t, path, plugins, "one")
	var starts atomic.Int32
	c, err := Start(context.Background(), Options{ConfigPath: path, Interval: time.Hour, Ready: func(ctx context.Context, socket string, m *pluginruntime.Manifest) error {
		if err := waitReady(ctx, socket, m); err != nil {
			return err
		}
		attempt := starts.Add(1)
		if attempt == 2 || attempt == 3 {
			return fmt.Errorf("temporary readiness rejection %d", attempt)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pinned := c.Manifest("one")
	pid := c.Supervisor().ProcessSnapshot()[0].Pid
	if err := os.WriteFile(filepath.Join(plugins, "one", "plugin-manifest.json"), []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(plugins, "one", "run")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for c.BaseURL("one") != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "temporary readiness rejection") {
		t.Fatalf("failed recovery/rollback not reported: %v", err)
	}
	if starts.Load() != 3 || c.Manifest("one") != nil {
		t.Fatal("recovery and rollback did not both fail readiness")
	}
	if _, err := os.Stat(pinned.ManifestPath); err != nil {
		t.Fatalf("accepted retryable bundle was pruned: %v", err)
	}
	if err := c.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "plugin one manifest") {
		t.Fatalf("invalid source scan error lost: %v", err)
	}
	assertVersion(t, c, "one", "1.0.0")
	if starts.Load() != 4 {
		t.Fatalf("old accepted bundle was not retried: %d starts", starts.Load())
	}
}
