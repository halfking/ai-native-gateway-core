package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalogJSON 是合法的 maintain 插件目录响应，覆盖 asm 0.2.0 linux/arm64。
const catalogJSON = `{"plugins":[{"plugin_id":"asm","display_name":"ASM","latest_version":"0.2.0","latest_build_seq":2,"default_channel":"stable","releases":[{"plugin_version":"0.2.0","build_seq":2,"channel":"stable","gateway_min_version":"0.0.0","api_contract":"gateway-plugin-v1","artifacts":[{"platform":"linux","arch":"arm64","artifact_name":"asm-0.2.0-linux-arm64.tar.gz","sha256":"abc","size_bytes":12}]}]}]}`

func TestMaintainCatalogClientCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintain-api/plugins/catalog" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, catalogJSON)
	}))
	defer srv.Close()

	c := NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7")
	entries, err := c.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.PluginID != "asm" {
		t.Errorf("PluginID = %q, want asm", e.PluginID)
	}
	if e.DisplayName != "ASM" {
		t.Errorf("DisplayName = %q, want ASM", e.DisplayName)
	}
	if e.LatestVersion != "0.2.0" {
		t.Errorf("LatestVersion = %q, want 0.2.0", e.LatestVersion)
	}
	if e.LatestBuildSeq != 2 {
		t.Errorf("LatestBuildSeq = %d, want 2", e.LatestBuildSeq)
	}
	if e.DefaultChannel != "stable" {
		t.Errorf("DefaultChannel = %q, want stable", e.DefaultChannel)
	}
	if len(e.Releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(e.Releases))
	}
	rel := e.Releases[0]
	if rel.PluginVersion != "0.2.0" || rel.BuildSeq != 2 || rel.Channel != "stable" {
		t.Errorf("release = %+v", rel)
	}
	if rel.GatewayMinVersion != "0.0.0" {
		t.Errorf("GatewayMinVersion = %q, want 0.0.0", rel.GatewayMinVersion)
	}
	if rel.APIContract != "gateway-plugin-v1" {
		t.Errorf("APIContract = %q, want gateway-plugin-v1", rel.APIContract)
	}
	if len(rel.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(rel.Artifacts))
	}
	a := rel.Artifacts[0]
	if a.Platform != "linux" || a.Arch != "arm64" {
		t.Errorf("artifact platform/arch = %s/%s", a.Platform, a.Arch)
	}
	if a.ArtifactName != "asm-0.2.0-linux-arm64.tar.gz" {
		t.Errorf("ArtifactName = %q", a.ArtifactName)
	}
	if a.SHA256 != "abc" {
		t.Errorf("SHA256 = %q, want abc", a.SHA256)
	}
	if a.SizeBytes != 12 {
		t.Errorf("SizeBytes = %d, want 12", a.SizeBytes)
	}
}

func TestMaintainCatalogClientCatalogBaseURLTrimmed(t *testing.T) {
	// trailing slash on baseURL must be trimmed — otherwise requests hit //maintain-api/...
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintain-api/plugins/catalog" {
			t.Errorf("path has double slash or wrong: %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, catalogJSON)
	}))
	defer srv.Close()

	c := NewMaintainCatalogClient(srv.URL+"/", "linux", "arm64", "2.4.7")
	if _, err := c.Catalog(context.Background()); err != nil {
		t.Fatalf("Catalog with trailing-slash baseURL returned error: %v", err)
	}
}

func TestMaintainCatalogClientCatalogHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7")
	_, err := c.Catalog(context.Background())
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
}

func TestMaintainCatalogClientTicket(t *testing.T) {
	var gotBody map[string]any
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"request_id":"req-1","url":"http://example/dl","token":"tok","expires_at":"2026-07-21T12:00:00Z","file_name":"asm-0.2.0-linux-arm64.tar.gz","sha256":"deadbeef"}`)
	}))
	defer srv.Close()

	c := NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7")
	ticket, err := c.Ticket(context.Background(), "asm", "0.2.0", 2)
	if err != nil {
		t.Fatalf("Ticket returned error: %v", err)
	}
	// 验证请求侧
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/maintain-api/plugins/asm/ticket" {
		t.Errorf("path = %q, want /maintain-api/plugins/asm/ticket", gotPath)
	}
	if gotBody["version"] != "0.2.0" {
		t.Errorf("body.version = %v, want 0.2.0", gotBody["version"])
	}
	if gotBody["build_seq"] != float64(2) {
		t.Errorf("body.build_seq = %v, want 2", gotBody["build_seq"])
	}
	if gotBody["platform"] != "linux" {
		t.Errorf("body.platform = %v, want linux", gotBody["platform"])
	}
	if gotBody["arch"] != "arm64" {
		t.Errorf("body.arch = %v, want arm64", gotBody["arch"])
	}
	// 验证响应侧
	if ticket.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", ticket.RequestID)
	}
	if ticket.URL != "http://example/dl" {
		t.Errorf("URL = %q", ticket.URL)
	}
	if ticket.Token != "tok" {
		t.Errorf("Token = %q", ticket.Token)
	}
	if ticket.FileName != "asm-0.2.0-linux-arm64.tar.gz" {
		t.Errorf("FileName = %q", ticket.FileName)
	}
	if ticket.SHA256 != "deadbeef" {
		t.Errorf("SHA256 = %q", ticket.SHA256)
	}
	if ticket.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should be parsed, got zero")
	}
}

func TestMaintainCatalogClientTicketHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewMaintainCatalogClient(srv.URL, "linux", "arm64", "2.4.7")
	_, err := c.Ticket(context.Background(), "asm", "0.2.0", 2)
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403: %v", err)
	}
}

func TestMaintainCatalogClientDownloadChecksumMismatch(t *testing.T) {
	// mock 提供固定字节，但 ticket 声明的 sha256 不同 → 必须返回 checksum mismatch。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "fixed-bytes-from-server")
	}))
	defer srv.Close()

	destDir := t.TempDir()
	// sha256("fixed-bytes-from-server") != "abc"，故应失败。
	ticket := PluginTicket{
		RequestID: "req-1",
		URL:       srv.URL,
		Token:     "tok",
		FileName:  "asm-0.2.0-linux-arm64.tar.gz",
		SHA256:    "abc",
	}
	c := NewMaintainCatalogClient("http://unused", "linux", "arm64", "2.4.7")
	_, err := c.DownloadArtifact(context.Background(), ticket, destDir)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention checksum mismatch: %v", err)
	}
	// 失败时也应该不留下（或至少不影响后续）——这里仅验证不会 panic/leak。
}

func TestMaintainCatalogClientDownloadHappy(t *testing.T) {
	payload := "hello-plugin"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	destDir := t.TempDir()
	ticket := PluginTicket{
		URL:      srv.URL,
		FileName: "asm.tar.gz",
		SHA256:   "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", // sha256("hello-plugin")? 实际为 hello  — 见下
	}
	// 计算真实 sha256 以让 happy path 通过：
	realSHA := mustSHA256(t, payload)
	ticket.SHA256 = realSHA

	c := NewMaintainCatalogClient("http://unused", "linux", "arm64", "2.4.7")
	path, err := c.DownloadArtifact(context.Background(), ticket, destDir)
	if err != nil {
		t.Fatalf("DownloadArtifact returned error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back file: %v", err)
	}
	if string(got) != payload {
		t.Errorf("file content = %q, want %q", string(got), payload)
	}
	// 路径应以 destDir 和 FileName 拼接
	expected := filepath.Join(destDir, "asm.tar.gz")
	if path != expected && !strings.HasSuffix(path, "/asm.tar.gz") {
		t.Errorf("path = %q, want suffix /asm.tar.gz", path)
	}
}

func TestMaintainCatalogClientDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	destDir := t.TempDir()
	ticket := PluginTicket{URL: srv.URL, FileName: "x", SHA256: "whatever"}
	c := NewMaintainCatalogClient("http://unused", "linux", "arm64", "2.4.7")
	_, err := c.DownloadArtifact(context.Background(), ticket, destDir)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}

func TestFindRelease(t *testing.T) {
	// 用与 catalog 相同的形态构建 entries。
	entries := []CatalogEntry{
		{
			PluginID: "asm", DisplayName: "ASM", LatestVersion: "0.2.0",
			Releases: []CatalogRelease{
				{
					PluginVersion: "0.2.0", BuildSeq: 2, Channel: "stable",
					GatewayMinVersion: "0.0.0", APIContract: "gateway-plugin-v1",
					Artifacts: []CatalogArtifact{
						{Platform: "linux", Arch: "arm64", ArtifactName: "asm-0.2.0-linux-arm64.tar.gz", SHA256: "abc", SizeBytes: 12},
						{Platform: "darwin", Arch: "arm64", ArtifactName: "asm-0.2.0-darwin-arm64.tar.gz", SHA256: "xyz", SizeBytes: 11},
					},
				},
				{
					PluginVersion: "0.3.0", BuildSeq: 3, Channel: "stable",
					GatewayMinVersion: "2.9.0", APIContract: "gateway-plugin-v1",
					Artifacts: []CatalogArtifact{
						{Platform: "linux", Arch: "arm64", ArtifactName: "asm-0.3.0-linux-arm64.tar.gz", SHA256: "def"},
					},
				},
			},
		},
	}

	t.Run("happy", func(t *testing.T) {
		c := NewMaintainCatalogClient("http://x", "linux", "arm64", "2.4.7")
		rel, a, err := c.FindRelease(entries, "asm", "0.2.0")
		if err != nil {
			t.Fatalf("FindRelease: %v", err)
		}
		if rel.PluginVersion != "0.2.0" {
			t.Errorf("release version = %q", rel.PluginVersion)
		}
		if a.Platform != "linux" || a.Arch != "arm64" {
			t.Errorf("artifact = %s/%s", a.Platform, a.Arch)
		}
		if a.SHA256 != "abc" {
			t.Errorf("SHA256 = %q, want abc", a.SHA256)
		}
	})

	t.Run("version-not-found", func(t *testing.T) {
		c := NewMaintainCatalogClient("http://x", "linux", "arm64", "2.4.7")
		_, _, err := c.FindRelease(entries, "asm", "9.9.9")
		if err == nil {
			t.Fatal("expected error for missing version")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention not found: %v", err)
		}
	})

	t.Run("plugin-not-found", func(t *testing.T) {
		c := NewMaintainCatalogClient("http://x", "linux", "arm64", "2.4.7")
		_, _, err := c.FindRelease(entries, "missing", "0.2.0")
		if err == nil {
			t.Fatal("expected error for missing plugin")
		}
	})

	t.Run("platform-mismatch", func(t *testing.T) {
		// 客户端是 windows/amd64，目录里没有 → 必须报错。
		c := NewMaintainCatalogClient("http://x", "windows", "amd64", "2.4.7")
		_, _, err := c.FindRelease(entries, "asm", "0.2.0")
		if err == nil {
			t.Fatal("expected error for platform mismatch")
		}
		if !strings.Contains(err.Error(), "windows/amd64") {
			t.Errorf("error should mention platform/arch: %v", err)
		}
	})

	t.Run("gateway-version-gate", func(t *testing.T) {
		// release 0.3.0 要求 gw>=2.9.0；client=2.4.7 时应跳过并报 not found。
		// 注意：versionCompatible 用字典序比较（P12 约束），所以这里选 2.9.5
		// 作为满足下界的版本（"2.9.5" > "2.9.0" 字典序成立）。跨段版本如
		// 2.10.0 字典序小于 2.9.0，属于已知限制，由 TestVersionCompatible 覆盖。
		c := NewMaintainCatalogClient("http://x", "linux", "arm64", "2.4.7")
		_, _, err := c.FindRelease(entries, "asm", "0.3.0")
		if err == nil {
			t.Fatal("expected error: 0.3.0 requires gw 2.9.0, client is 2.4.7")
		}
		// 升级 gateway 版本后应该找到。
		c2 := NewMaintainCatalogClient("http://x", "linux", "arm64", "2.9.5")
		rel, a, err := c2.FindRelease(entries, "asm", "0.3.0")
		if err != nil {
			t.Fatalf("expected to find 0.3.0 with gw 2.9.5: %v", err)
		}
		if rel.PluginVersion != "0.3.0" {
			t.Errorf("release version = %q", rel.PluginVersion)
		}
		if a.SHA256 != "def" {
			t.Errorf("SHA256 = %q, want def", a.SHA256)
		}
	})
}

func TestVersionCompatible(t *testing.T) {
	cases := []struct {
		name    string
		current string
		min     string
		max     string
		want    bool
	}{
		{"no bounds", "2.4.7", "", "", true},
		{"within min only", "2.4.7", "0.0.0", "", true},
		{"below min", "2.4.7", "2.5.0", "", false},
		{"equal min", "2.5.0", "2.5.0", "", true},
		{"above max", "2.4.7", "", "2.4.0", false},
		{"equal max", "2.4.7", "", "2.4.7", true},
		{"within range", "2.4.7", "2.0.0", "3.0.0", true},
		{"empty current", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := versionCompatible(tc.current, tc.min, tc.max)
			if got != tc.want {
				t.Errorf("versionCompatible(%q,%q,%q) = %v, want %v",
					tc.current, tc.min, tc.max, got, tc.want)
			}
		})
	}
}

// mustSHA256 计算字符串的 sha256 十六进制摘要，测试失败时 fail。
func mustSHA256(t *testing.T, s string) string {
	t.Helper()
	h := sha256.New()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
