package relay

import (
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/sessions"
)

func TestGwSessionTaskFromRequest(t *testing.T) {
	t.Run("gw headers", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Session-Id", "gw_sess_1")
		req.Header.Set("X-Gw-Task-Id", "task_acc_1")
		sid, tid := gwSessionTaskFromRequest(req, nil)
		if sid != "gw_sess_1" || tid != "task_acc_1" {
			t.Fatalf("got sid=%q tid=%q", sid, tid)
		}
	})

	t.Run("legacy session header", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Session-Id", "legacy_sess")
		sid, tid := gwSessionTaskFromRequest(req, nil)
		if sid != "legacy_sess" || tid != "" {
			t.Fatalf("got sid=%q tid=%q", sid, tid)
		}
	})

	t.Run("session object fallback", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		sess := &sessions.Session{SessionID: "gw_from_redis", TaskID: "task_from_redis"}
		sid, tid := gwSessionTaskFromRequest(req, sess)
		if sid != "gw_from_redis" || tid != "task_from_redis" {
			t.Fatalf("got sid=%q tid=%q", sid, tid)
		}
	})

	t.Run("header overrides session object", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Session-Id", "hdr_sess")
		req.Header.Set("X-Gw-Task-Id", "hdr_task")
		sess := &sessions.Session{SessionID: "redis_sess", TaskID: "redis_task"}
		sid, tid := gwSessionTaskFromRequest(req, sess)
		if sid != "hdr_sess" || tid != "hdr_task" {
			t.Fatalf("got sid=%q tid=%q", sid, tid)
		}
	})
}
