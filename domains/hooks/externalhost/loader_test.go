package externalhost

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// fakeHookServer 是一个最小外部钩子实现：unix socket + handshake + health + 钩子点。
type fakeHookServer struct {
	socketPath string
	listener   net.Listener
	server     *http.Server
	// reply 是钩子点返回的回复体；为空则返回 continue。
	reply string
	// seen 记录收到的调用信封。
	seen []contract.Invocation
}

// shortSocketDir 返回一个短路径目录——unix socket 路径长度上限约 104 字节，
// t.TempDir() 在 macOS 上位于 /var/folders/... 且路径过长会导致 bind 失败。
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "xh")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startFakeHook(t *testing.T, reply string) *fakeHookServer {
	t.Helper()
	dir := shortSocketDir(t)
	sock := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	h := &fakeHookServer{socketPath: sock, listener: ln, reply: reply}

	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_contract":   pluginruntime.SupportedAPIContract,
			"plugin_id":      "memoraauto-external",
			"plugin_version": "0.1.0",
			"status":         "ready",
		})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(contract.ContractPath+"/on_request", func(w http.ResponseWriter, r *http.Request) {
		var inv contract.Invocation
		_ = json.NewDecoder(r.Body).Decode(&inv)
		h.seen = append(h.seen, inv)
		body := h.reply
		if body == "" {
			body = `{"contract_version":"gateway-external-hook-v1","decision":"continue"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc(contract.ContractPath+"/on_response", func(w http.ResponseWriter, r *http.Request) {
		var inv contract.Invocation
		_ = json.NewDecoder(r.Body).Decode(&inv)
		h.seen = append(h.seen, inv)
		body := h.reply
		if body == "" {
			body = `{"contract_version":"gateway-external-hook-v1","decision":"continue"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	h.server = &http.Server{Handler: mux}
	go func() { _ = h.server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.server.Shutdown(ctx)
	})
	// 等待 socket 可用。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake hook socket never became ready")
	return nil
}

func hookManifest(socketPath string) *pluginruntime.Manifest {
	return &pluginruntime.Manifest{
		SchemaVersion: 2,
		PluginID:      "memoraauto-external",
		PluginVersion: "0.1.0",
		DisplayName:   "MemoraAuto External Hook",
		GatewayCompatibility: pluginruntime.GatewayCompatibility{
			APIContract: pluginruntime.SupportedAPIContract,
		},
		Runtime: pluginruntime.Runtime{
			Entrypoint:    "/bin/echo --socket=" + socketPath,
			Protocol:      "http-unix-socket",
			HandshakePath: "/handshake",
			HealthPath:    "/health",
		},
	}
}

func TestLoaderRejectsWrongProtocol(t *testing.T) {
	l := NewLoader(LoaderConfig{})
	m := hookManifest("/tmp/x.sock")
	m.Runtime.Protocol = "tcp"
	if _, err := l.Load(context.Background(), m); err == nil {
		t.Fatal("non unix-socket protocol must be rejected")
	} else if !strings.Contains(err.Error(), "unsupported runtime protocol") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderRequiresPluginID(t *testing.T) {
	l := NewLoader(LoaderConfig{})
	if _, err := l.Load(context.Background(), &pluginruntime.Manifest{}); err == nil {
		t.Fatal("missing plugin id must be rejected")
	}
}

func TestLoaderHandshakeThenHealthThenInvoke(t *testing.T) {
	hook := startFakeHook(t, "")
	loader := NewLoader(LoaderConfig{})

	// 无 supervisor 时 socket 路径来自 manifest --socket 参数。
	m := hookManifest(hook.socketPath)
	loaded, err := loader.Load(context.Background(), m)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.SocketPath != hook.socketPath {
		t.Errorf("socket path = %q, want %q", loaded.SocketPath, hook.socketPath)
	}
	if loaded.Handshake == nil || loaded.Handshake.PluginID != "memoraauto-external" {
		t.Errorf("handshake = %+v", loaded.Handshake)
	}

	host := New(Config{SocketPath: loaded.SocketPath, Timeout: 2 * time.Second})
	out := host.OnRequest(context.Background(), &contract.Context{
		Protocol: "anthropic", Model: "claude", RequestID: "r-1", Origin: contract.OriginLocal,
	}, &contract.RequestPayload{Method: "POST", Path: "/v1/messages"})
	if out.Failed() {
		t.Fatalf("unexpected failure: %v", out.Err)
	}
	if out.Decision != contract.DecisionContinue {
		t.Errorf("decision = %q", out.Decision)
	}
	if len(hook.seen) != 1 {
		t.Fatalf("hook call count = %d, want 1", len(hook.seen))
	}
	if hook.seen[0].HookPoint != contract.HookPointOnRequest {
		t.Errorf("hook point = %q", hook.seen[0].HookPoint)
	}
	if hook.seen[0].ContractVersion != contract.ContractVersion {
		t.Errorf("contract version = %q", hook.seen[0].ContractVersion)
	}
}

func TestLoaderHandshakeFailureRollsBack(t *testing.T) {
	// 钩子进程存在但 handshake 失败（plugin_id 不匹配）→ 装载失败且不留残余。
	dir := shortSocketDir(t)
	sock := filepath.Join(dir, "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_contract": pluginruntime.SupportedAPIContract,
			"plugin_id":    "someone-else",
			"status":       "ready",
		})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	loader := NewLoader(LoaderConfig{})
	m := hookManifest(sock)
	if _, err := loader.Load(context.Background(), m); err == nil {
		t.Fatal("handshake mismatch must fail the load")
	} else if !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderHealthFailureRollsBack(t *testing.T) {
	dir := shortSocketDir(t)
	sock := filepath.Join(dir, "u.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_contract": pluginruntime.SupportedAPIContract,
			"plugin_id":    "memoraauto-external",
			"status":       "ready",
		})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	loader := NewLoader(LoaderConfig{HealthTimeout: time.Second})
	m := hookManifest(sock)
	if _, err := loader.Load(context.Background(), m); err == nil {
		t.Fatal("health check failure must fail the load")
	} else if !strings.Contains(err.Error(), "health check") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderAbsenceOfSocketPathRejected(t *testing.T) {
	loader := NewLoader(LoaderConfig{})
	m := hookManifest("")
	m.Runtime.Entrypoint = "/bin/true"
	if _, err := loader.Load(context.Background(), m); err == nil {
		t.Fatal("missing socket path must be rejected")
	}
}

func TestUnixTransportIgnoresEnvironmentProxy(t *testing.T) {
	// 回归钉测：unix 传输不得继承环境 HTTP(S)_PROXY，
	// 否则钩子调用会被代理劫持发往外部代理。
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")

	hook := startFakeHook(t, "")
	client := unixHTTPClient(hook.socketPath, 2*time.Second)
	resp, err := client.Get("http://unix/health")
	if err != nil {
		t.Fatalf("unix transport must bypass proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestUnixHTTPClientTargetsGivenSocket(t *testing.T) {
	hook := startFakeHook(t, "")
	client := unixHTTPClient(hook.socketPath, 2*time.Second)
	resp, err := client.Get("http://unix/health")
	if err != nil {
		t.Fatalf("health over unix socket failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = os.Getpid()
}
