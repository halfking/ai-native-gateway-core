package proxy

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMultiFormatParserFetchRedactsHTTPErrorBody(t *testing.T) {
	secret := "subscription-password-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != defaultParserUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, defaultParserUserAgent)
		}
		if got := r.Header.Get("Accept"); got != "*/*" {
			t.Errorf("Accept = %q, want */*", got)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`connect failed password=` + secret + ` token=opaque-token`))
	}))
	defer srv.Close()

	_, err := NewMultiFormatParserWithClient(&http.Client{Timeout: time.Second}).Parse(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("Parse returned nil error for HTTP 502")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "opaque-token") {
		t.Fatalf("HTTP error leaked credentials: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("HTTP status missing from error: %v", err)
	}
}

func TestParseSubscriptionBodyFormatsAndDialability(t *testing.T) {
	plain := "http://user:pass@bridge.local:8080#US-bridge\n" +
		"trojan://trojan-secret@node.example:443?sni=edge.example#HK-node\n" +
		"# comment\n" +
		"not-a-proxy\n"

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "plain", body: []byte(plain)},
		{name: "base64", body: []byte(base64.StdEncoding.EncodeToString([]byte(plain)))},
		{name: "raw-url-base64", body: []byte(base64.RawURLEncoding.EncodeToString([]byte(plain)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := parseSubscriptionBody(tc.body)
			if err != nil {
				t.Fatalf("parseSubscriptionBody: %v", err)
			}
			if len(nodes) != 2 {
				t.Fatalf("got %d nodes, want 2", len(nodes))
			}
			if !nodes[0].Dialable() || nodes[0].Protocol != ProtocolHTTP {
				t.Fatalf("HTTP node = %+v, want dialable HTTP", nodes[0])
			}
			if nodes[0].Password != "pass" {
				t.Fatalf("HTTP password not parsed: %q", nodes[0].Password)
			}
			if nodes[1].Dialable() || nodes[1].Protocol != "trojan" {
				t.Fatalf("trojan node = %+v, want undialable inventory node", nodes[1])
			}
			if nodes[1].Location != "HK" {
				t.Fatalf("trojan location = %q, want HK", nodes[1].Location)
			}
		})
	}
}

func TestSanitizeSecretsAndBodyExcerpt(t *testing.T) {
	secret := "SensitiveValue123"
	for _, key := range []string{
		"password", "passwd", "pwd", "uuid", "psk", "token", "secret",
		"api-key", "api_key", "public-key", "short-id", "auth",
	} {
		t.Run(key, func(t *testing.T) {
			input := key + "=" + secret + " host=bridge.local"
			got := SanitizeSecrets(input)
			if strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("SanitizeSecrets(%q) = %q", input, got)
			}
			if !strings.Contains(got, "bridge.local") {
				t.Fatalf("non-secret context was removed: %q", got)
			}
		})
	}

	uri := `dial trojan://user:password@node.example:443 failed`
	if got := SanitizeSecrets(uri); strings.Contains(got, "user:password") || !strings.Contains(got, "[REDACTED]@") {
		t.Fatalf("userinfo was not redacted: %q", got)
	}

	body := []byte(strings.Repeat("Q", 80))
	got := bodyExcerpt(body, 32)
	if strings.Contains(got, string(body)) || !strings.Contains(got, "opaque base64-like body") {
		t.Fatalf("opaque body excerpt leaked body: %q", got)
	}
}

func TestParserRejectsInvalidAndEmptyInput(t *testing.T) {
	p := NewMultiFormatParser()
	for _, raw := range []string{"", "ftp://example.com/sub", "http://127.0.0.1:1"} {
		_, err := p.Parse(context.Background(), raw)
		if err == nil && raw != "http://127.0.0.1:1" {
			t.Fatalf("Parse(%q) unexpectedly succeeded", raw)
		}
	}
	if _, err := parseSubscriptionBody([]byte("no proxy data")); err == nil {
		t.Fatal("invalid subscription body unexpectedly parsed")
	}
}
