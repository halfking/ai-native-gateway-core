package pluginruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func sha256hex(t *testing.T, b []byte) string {
	t.Helper()
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// manifestBytes builds a valid manifest JSON for the test tarball. Must pass
// LoadManifest's validate(): api_contract=gateway-plugin-v1, non-empty
// handshake/health paths.
func manifestBytes(version string) []byte {
	return []byte(`{
		"schema_version":1,"plugin_id":"asm","display_name":"ASM",
		"plugin_version":"` + version + `","build_seq":2,"channel":"stable",
		"gateway_compatibility":{"min_version":"0.0.0","api_contract":"gateway-plugin-v1"},
		"runtime":{"entrypoint":"bin/asm","protocol":"http-unix-socket","health_path":"/plugin/healthz","handshake_path":"/plugin/handshake"},
		"capabilities":["session.read"],"pages":[],"web":{"mount":"iframe","base_path":"/p/","entry":"web/index.html"},
		"activation":{"module_key":"session_manager","license_required":false}
	}`)
}

func buildPluginTarball(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	add := func(name string, content []byte, mode int64) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content))})
		tw.Write(content)
	}
	add("plugin-manifest.json", manifestBytes(version), 0o644)
	add("bin/asm", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	add("web/index.html", []byte("<html/>"), 0o644)
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestInstallerEndToEnd(t *testing.T) {
	tarball := buildPluginTarball(t, "0.2.0")
	sha := sha256hex(t, tarball)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/maintain-api/plugins/catalog":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"plugins":[{"plugin_id":"asm","latest_version":"0.2.0","latest_build_seq":2,"releases":[{"plugin_version":"0.2.0","build_seq":2,"channel":"stable","gateway_min_version":"0.0.0","api_contract":"gateway-plugin-v1","artifacts":[{"platform":"linux","arch":"arm64","artifact_name":"asm-0.2.0-linux-arm64.tar.gz","sha256":"` + sha + `","size_bytes":` + strconv.Itoa(len(tarball)) + `}]}]}]}`))
		case r.URL.Path == "/maintain-api/plugins/asm/ticket":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"request_id":"r1","url":"` + srv.URL + `/dl","token":"tok","file_name":"asm-0.2.0-linux-arm64.tar.gz","sha256":"` + sha + `","expires_at":"2026-07-22T00:00:00Z"}`))
		case r.URL.Path == "/dl":
			w.Write(tarball)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	pluginsDir := t.TempDir()
	sup := NewSupervisor(SupervisorConfig{SocketDir: filepath.Join(pluginsDir, ".sockets")})
	sup.commandFactory = func(socketPath, entrypoint string, env []string) command {
		return &fakeProc{} // don't actually exec
	}
	reg := NewRegistry()
	client := NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7")
	inst := NewInstaller(InstallerConfig{
		PluginsDir:    pluginsDir,
		Client:        client,
		Supervisor:    sup,
		Registry:      reg,
		SigningPubkey: "", // dev mode
	})

	if err := inst.Install(context.Background(), "asm", "0.2.0"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// versioned dir exists with extracted files
	versioned := filepath.Join(pluginsDir, "asm", "0.2.0")
	for _, p := range []string{"plugin-manifest.json", "bin/asm", "web/index.html"} {
		if _, err := os.Stat(filepath.Join(versioned, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	// current symlink points to versioned dir (created atomically via tmp+rename)
	current := filepath.Join(pluginsDir, "asm", "current")
	target, err := os.Readlink(current)
	if err != nil {
		t.Fatalf("current symlink missing: %v", err)
	}
	if target != versioned {
		t.Errorf("current -> %q, want %q", target, versioned)
	}
	// supervisor now runs the new manifest
	if m := sup.ManifestOf("asm"); m == nil || m.PluginVersion != "0.2.0" {
		t.Errorf("supervisor manifest = %+v, want version 0.2.0", m)
	}
}

func TestInstallerRejectsManifestPluginIDMismatch(t *testing.T) {
	// tarball has plugin_id "asm" but caller asks for "other"
	tarball := buildPluginTarball(t, "0.2.0") // plugin_id is "asm" inside
	sha := sha256hex(t, tarball)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/maintain-api/plugins/catalog":
			// catalog returns plugin_id "other" so FindRelease passes, but tarball contains "asm"
			w.Write([]byte(`{"plugins":[{"plugin_id":"other","latest_version":"0.2.0","latest_build_seq":2,"releases":[{"plugin_version":"0.2.0","build_seq":2,"channel":"stable","gateway_min_version":"0.0.0","api_contract":"gateway-plugin-v1","artifacts":[{"platform":"linux","arch":"arm64","artifact_name":"x.tar.gz","sha256":"` + sha + `","size_bytes":1}]}]}]}`))
		case r.URL.Path == "/maintain-api/plugins/other/ticket":
			w.Write([]byte(`{"request_id":"r1","url":"` + srv.URL + `/dl","token":"tok","file_name":"x.tar.gz","sha256":"` + sha + `","expires_at":"2026-07-22T00:00:00Z"}`))
		case r.URL.Path == "/dl":
			w.Write(tarball)
		}
	}))
	defer srv.Close()

	pluginsDir := t.TempDir()
	sup := NewSupervisor(SupervisorConfig{SocketDir: filepath.Join(pluginsDir, ".sockets")})
	sup.commandFactory = func(socketPath, entrypoint string, env []string) command { return &fakeProc{} }
	inst := NewInstaller(InstallerConfig{
		PluginsDir: pluginsDir,
		Client:     NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7"),
		Supervisor: sup,
		Registry:   NewRegistry(),
	})

	err := inst.Install(context.Background(), "other", "0.2.0")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("mismatch")) {
		t.Errorf("error should mention mismatch, got: %v", err)
	}
}
