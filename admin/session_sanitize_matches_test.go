package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestMaskSensitiveValue(t *testing.T) {
	cases := []struct {
		typ, raw, want string
	}{
		{"phone", "13800138000", "*******8000"},
		{"email", "ab@example.com", "a***@example.com"},
		{"secret", "supersecret", "[secret len=11]"},
		{"name", "张三丰", "张*丰"},
	}
	for _, c := range cases {
		got := maskSensitiveValue(c.typ, c.raw)
		if got != c.want {
			t.Fatalf("mask(%q,%q)=%q want %q", c.typ, c.raw, got, c.want)
		}
	}
}

func TestServeSessionSanitizeMatches_Redis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	sid := "gw_sess_test"
	legacy := sanitize.SanitizeRedisKey(sid)
	if err := rc.HSet(t.Context(), legacy, "{SENSITIVE:phone:1}", "13800138000").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rc.HSet(t.Context(), legacy, "{SENSITIVE:email:1}", "u@example.com").Err(); err != nil {
		t.Fatal(err)
	}

	h := &Handler{redisClient: rc}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/"+sid+"/sanitize-matches", nil)
	rec := httptest.NewRecorder()
	h.serveSessionSanitizeMatches(rec, req, sid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "{SENSITIVE:phone:1}") {
		t.Fatalf("expected placeholder in body: %s", body)
	}
	if strings.Contains(body, "13800138000") || strings.Contains(body, "u@example.com") {
		t.Fatalf("raw secret must not appear: %s", body)
	}
	if !strings.Contains(body, "*******8000") {
		t.Fatalf("expected masked phone in body: %s", body)
	}
}
