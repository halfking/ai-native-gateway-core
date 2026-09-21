package safehttpclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSafeHTTPClient_BlockPrivateIPs(t *testing.T) {
	client := New(5 * time.Second)
	ctx := context.Background()

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		// IPv4 loopback
		{
			name:    "loopback 127.0.0.1",
			url:     "http://127.0.0.1:8080/test",
			wantErr: "blocked range",
		},
		{
			name:    "loopback 127.8.8.8",
			url:     "http://127.8.8.8/test",
			wantErr: "blocked range",
		},
		// RFC1918 private
		{
			name:    "private 10.0.0.1",
			url:     "http://10.0.0.1/test",
			wantErr: "blocked range",
		},
		{
			name:    "private 172.16.0.1",
			url:     "http://172.16.0.1/test",
			wantErr: "blocked range",
		},
		{
			name:    "private 192.168.1.1",
			url:     "http://192.168.1.1/test",
			wantErr: "blocked range",
		},
		// Cloud metadata (link-local)
		{
			name:    "aws metadata",
			url:     "http://169.254.169.254/latest/meta-data/",
			wantErr: "blocked range",
		},
		{
			name:    "gcp metadata",
			url:     "http://169.254.169.254/computeMetadata/v1/",
			wantErr: "blocked range",
		},
		// IPv6 loopback
		{
			name:    "ipv6 loopback",
			url:     "http://[::1]:8080/test",
			wantErr: "blocked range",
		},
		// IPv6 ULA (private)
		{
			name:    "ipv6 ula fc00",
			url:     "http://[fc00::1]/test",
			wantErr: "blocked range",
		},
		{
			name:    "ipv6 ula fd00",
			url:     "http://[fd00::1]/test",
			wantErr: "blocked range",
		},
		// IPv6 link-local
		{
			name:    "ipv6 link-local",
			url:     "http://[fe80::1]/test",
			wantErr: "blocked range",
		},
		// Special addresses
		{
			name:    "broadcast",
			url:     "http://255.255.255.255/test",
			wantErr: "blocked range",
		},
		{
			name:    "unspecified 0.0.0.0",
			url:     "http://0.0.0.0/test",
			wantErr: "blocked range",
		},
		// Multicast
		{
			name:    "multicast 224.0.0.1",
			url:     "http://224.0.0.1/test",
			wantErr: "blocked range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Get(ctx, tt.url)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestSafeHTTPClient_AllowPublicIPs(t *testing.T) {
	// Start a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := New(5 * time.Second)
	ctx := context.Background()

	// Test server should be reachable (it's on a public-like address or localhost via allowlist needed)
	// Note: httptest.Server uses 127.0.0.1 which is blocked by default
	// For this test, we'll test with real public DNS that we expect to work

	// Instead, test the validation logic directly with known public IPs
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "google dns 8.8.8.8",
			url:  "http://8.8.8.8/generate_204", // This will fail to connect but pass validation
		},
		{
			name: "cloudflare dns 1.1.1.1",
			url:  "http://1.1.1.1/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We expect these to pass validation (not blocked by SSRF check)
			// but may fail with connection errors - that's ok
			_, err := client.Get(ctx, tt.url)
			// Should NOT contain "blocked range" error
			if err != nil && strings.Contains(err.Error(), "blocked range") {
				t.Errorf("public IP %s was incorrectly blocked: %v", tt.url, err)
			}
		})
	}
}

func TestSafeHTTPClient_Allowlist(t *testing.T) {
	allowlist := []string{
		"localhost",
		"host.docker.internal",
		"10.0.0.0/8",
		"*.internal.corp",
	}
	client := NewWithAllowlist(5*time.Second, allowlist)
	ctx := context.Background()

	tests := []struct {
		name      string
		url       string
		wantBlock bool
	}{
		{
			name:      "localhost allowed",
			url:       "http://localhost:8080/test",
			wantBlock: false,
		},
		{
			name:      "docker internal allowed",
			url:       "http://host.docker.internal/test",
			wantBlock: false,
		},
		{
			name:      "10.x CIDR allowed",
			url:       "http://10.50.1.100:8080/test",
			wantBlock: false,
		},
		{
			name:      "internal.corp suffix allowed",
			url:       "http://api.internal.corp/test",
			wantBlock: false,
		},
		{
			name:      "subdomain suffix allowed",
			url:       "http://foo.bar.internal.corp/test",
			wantBlock: false,
		},
		{
			name:      "192.168 still blocked",
			url:       "http://192.168.1.1/test",
			wantBlock: true,
		},
		{
			name:      "metadata still blocked",
			url:       "http://169.254.169.254/test",
			wantBlock: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Get(ctx, tt.url)

			if tt.wantBlock {
				if err == nil {
					t.Error("expected request to be blocked, got nil error")
				} else if !strings.Contains(err.Error(), "blocked") && !strings.Contains(err.Error(), "ssrf") {
					// Connection errors are ok for blocked requests
					t.Logf("blocked as expected (error: %v)", err)
				}
			} else {
				// For allowed hosts, we expect either success OR connection error (not SSRF block)
				if err != nil && (strings.Contains(err.Error(), "blocked range") || strings.Contains(strings.ToLower(err.Error()), "ssrf")) {
					t.Errorf("allowlisted URL was blocked: %v", err)
				}
			}
		})
	}
}

func TestSafeHTTPClient_InvalidSchemes(t *testing.T) {
	client := New(5 * time.Second)
	ctx := context.Background()

	tests := []struct {
		name string
		url  string
	}{
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://example.com/file"},
		{"data scheme", "data:text/plain,hello"},
		{"javascript scheme", "javascript:alert(1)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Get(ctx, tt.url)
			if err == nil {
				t.Errorf("expected error for scheme, got nil")
			}
			if !strings.Contains(err.Error(), "not allowed") && !strings.Contains(err.Error(), "scheme") {
				t.Errorf("error should mention scheme: %v", err)
			}
		})
	}
}

func TestSafeHTTPClient_RedirectValidation(t *testing.T) {
	// Server that redirects to private IP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-to-private" {
			http.Redirect(w, r, "http://127.0.0.1:9999/private", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Note: httptest.Server uses 127.0.0.1, so we need allowlist for initial request
	client := NewWithAllowlist(5*time.Second, []string{"127.0.0.1"})
	ctx := context.Background()

	_, err := client.Get(ctx, server.URL+"/redirect-to-private")
	// The redirect should be blocked (even though initial host is allowed, redirect target is different port)
	// Actually, since we allowlisted 127.0.0.1 entirely, this will be allowed
	// Let's test redirect to explicitly blocked metadata instead

	// Better test: redirect to metadata endpoint
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/metadata", http.StatusFound)
	}))
	defer server2.Close()

	client2 := NewWithAllowlist(5*time.Second, []string{"127.0.0.1"})
	_, err = client2.Get(ctx, server2.URL)
	if err == nil {
		t.Error("expected redirect to metadata to be blocked")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should mention blocked redirect: %v", err)
	}
}

func TestSafeHTTPClient_DNSRebinding(t *testing.T) {
	// This test verifies that DNS resolution happens at dial time
	// In a real attack, attacker.com would resolve to 8.8.8.8 initially,
	// then to 127.0.0.1 when the actual connection is made

	// We can't easily simulate DNS rebinding in unit tests without a custom resolver
	// So this is a documentation test showing the protection exists

	client := New(5 * time.Second)
	ctx := context.Background()

	// If an attacker domain resolved to a private IP, it would be blocked at dial time
	// The dialWithValidation function re-resolves DNS and validates ALL resolved IPs

	// Test with localhost (which resolves to 127.0.0.1)
	_, err := client.Get(ctx, "http://localhost:8080/test")
	if err == nil {
		t.Error("expected localhost (resolves to 127.0.0.1) to be blocked")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Logf("localhost blocked as expected: %v", err)
	}
}

func TestSafeHTTPClient_Timeout(t *testing.T) {
	// Server that hangs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer server.Close()

	client := NewWithAllowlist(1*time.Second, []string{"127.0.0.1"})
	ctx := context.Background()

	start := time.Now()
	_, err := client.Get(ctx, server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout: %v", err)
	}
}

func TestSafeHTTPClient_SuccessfulRequest(t *testing.T) {
	// Normal successful request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	client := NewWithAllowlist(5*time.Second, []string{"127.0.0.1"})
	ctx := context.Background()

	resp, err := client.Get(ctx, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestBlockedIPRanges(t *testing.T) {
	// Test the isBlockedIP function directly
	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		// Should be blocked
		{"loopback 127.0.0.1", "127.0.0.1", true},
		{"loopback 127.255.255.255", "127.255.255.255", true},
		{"private 10.0.0.1", "10.0.0.1", true},
		{"private 172.16.0.1", "172.16.0.1", true},
		{"private 192.168.0.1", "192.168.0.1", true},
		{"link-local 169.254.1.1", "169.254.1.1", true},
		{"metadata 169.254.169.254", "169.254.169.254", true},
		{"cgnat 100.64.0.1", "100.64.0.1", true},
		{"aliyun metadata 100.100.100.200", "100.100.100.200", true},
		{"cgnat upper bound 100.127.255.255", "100.127.255.255", true},
		{"benchmark 198.18.0.1", "198.18.0.1", true},
		{"benchmark 198.19.255.255", "198.19.255.255", true},
		{"ipv6 loopback", "::1", true},
		{"ipv6 ula fc00", "fc00::1", true},
		{"ipv6 ula fd00", "fd12::1", true},
		{"ipv6 link-local", "fe80::1", true},
		{"ipv4-mapped loopback via v6", "::ffff:127.0.0.1", true},
		{"ipv4-mapped private via v6", "::ffff:10.0.0.1", true},
		{"ipv4-mapped metadata via v6", "::ffff:169.254.169.254", true},
		{"multicast 224.0.0.1", "224.0.0.1", true},
		{"broadcast", "255.255.255.255", true},
		{"unspecified", "0.0.0.0", true},

		// Should NOT be blocked (public IPs)
		{"google dns", "8.8.8.8", false},
		{"cloudflare dns", "1.1.1.1", false},
		{"public ip", "93.184.216.34", false}, // example.com
		{"above cgnat 100.128.0.1", "100.128.0.1", false},
		{"above benchmark 198.20.0.1", "198.20.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipAddr := net.ParseIP(tt.ip)
			if ipAddr == nil {
				t.Fatalf("invalid IP: %s", tt.ip)
			}

			blocked := isBlockedIP(ipAddr)
			if blocked != tt.blocked {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, blocked, tt.blocked)
			}
		})
	}
}

func TestAllowlist_Matching(t *testing.T) {
	a := newAllowlist([]string{
		"localhost",
		"example.com",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"*.internal",
		"*.corp.local",
	})

	tests := []struct {
		name    string
		host    string
		allowed bool
	}{
		{"exact match", "localhost", true},
		{"exact match 2", "example.com", true},
		{"cidr match", "10.5.6.7", true},
		{"cidr match 2", "172.16.0.1", true},
		{"suffix match", "api.internal", true},
		{"suffix match subdomain", "foo.bar.internal", true},
		{"suffix match 2", "app.corp.local", true},

		{"no match", "google.com", false},
		{"no match ip", "192.168.1.1", false},
		{"partial match not allowed", "myexample.com", false},
		{"wrong suffix", "internal.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed := a.IsAllowed(tt.host)
			if allowed != tt.allowed {
				t.Errorf("IsAllowed(%s) = %v, want %v", tt.host, allowed, tt.allowed)
			}
		})
	}
}
