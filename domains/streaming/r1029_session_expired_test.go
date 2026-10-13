package streaming

// r1029（2026-10-14）：ErrSessionExpired 不再是 store fault。
//
// 事故链（本地网关日志实测，gateway-8782.log）：auto_summary 内部回环
// 携 gs_ 分支会话调 /v1/chat/completions → 会话 TTL 过期 →
// ErrSessionExpired 落进「store fault → 503」分支，且响应体误用
// SESSION_FORBIDDEN 文案（"session not owned by this API key"）→
// auto_summary 按瞬态重试（日志 2040 次 transient retry / 4051 次
// failed to generate summary），归因还指向了所有权。
//
// 契约：过期是确定性终态（Redis TTL 到期），语义 ≈ NotFound——
// id 保留/重新认领，会话在同一个 id 下重新开始；只有真正的 store
// 故障才 503，且响应体必须是 store 语义（SESSION_STORE_UNAVAILABLE），
// 不得再借用 FORBIDDEN 文案。
import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// expiredGetGetter：Get 恒返 ErrSessionExpired 的 getter（可挂原子认领器）。
type expiredGetGetter struct {
	owner *session.Session
}

func (s *expiredGetGetter) Get(context.Context, string) (*session.Session, error) {
	return nil, session.ErrSessionExpired
}

func (s *expiredGetGetter) EnsureV2WithID(_ context.Context, id string, keyID int, tenant, _, _ string) (*session.Session, bool, error) {
	if s.owner == nil {
		s.owner = &session.Session{SessionID: id, APIKeyID: keyID, TenantID: tenant}
	}
	return s.owner, true, nil
}

func (*expiredGetGetter) Touch(context.Context, string) error { return nil }
func (*expiredGetGetter) CreateV2(_ context.Context, apiKeyID int, tenantID, _, _ string) (*session.Session, error) {
	return &session.Session{SessionID: "gw_created", APIKeyID: apiKeyID, TenantID: tenantID}, nil
}
func (*expiredGetGetter) BindAPIKey(context.Context, string, int, string) error { return nil }

// TestR1029ExpiredSessionIsAbsentNotStoreFault（normalize 层，native 两腿
// 共用路径）：expired 不得映射为 errClientSessionUnavailable。
func TestR1029ExpiredSessionIsAbsentNotStoreFault(t *testing.T) {
	key := &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	t.Run("branch id preserved", func(t *testing.T) {
		id, info, err := normalizeAndRegisterClientSession(req, "gs_gw_expired",
			&expiredGetGetter{}, key)
		if err != nil {
			t.Fatalf("expired branch session must not error (got %v) — 503 retry loop", err)
		}
		if id != "gs_gw_expired" || info != nil {
			t.Fatalf("branch id must be preserved verbatim: id=%q info=%+v", id, info)
		}
	})

	t.Run("gw id re-claimed via atomic ensurer", func(t *testing.T) {
		g := &expiredGetGetter{}
		id, info, err := normalizeAndRegisterClientSession(req, "gw_expired", g, key)
		if err != nil {
			t.Fatalf("expired gw session must be re-claimable (got %v)", err)
		}
		if id != "gw_expired" || info == nil || info.APIKeyID != key.ID {
			t.Fatalf("claim adjudication failed: id=%q info=%+v", id, info)
		}
	})

	t.Run("gw id without ensurer falls back to fresh id", func(t *testing.T) {
		stub := &stubSessionGetter{getErr: session.ErrSessionExpired}
		id, _, err := normalizeAndRegisterClientSession(req, "gw_expired2", stub, key)
		if err != nil {
			t.Fatalf("expired session must not surface store fault (got %v)", err)
		}
		if id == "gw_expired2" || !strings.HasPrefix(id, "gw_") {
			t.Fatalf("unensurable expired id must be replaced by fresh system id: %q", id)
		}
	})

	t.Run("genuine store fault still unavailable", func(t *testing.T) {
		_, _, err := normalizeAndRegisterClientSession(req, "gw_any",
			&stubSessionGetter{getErr: errors.New("redis down")}, key)
		if !errors.Is(err, errClientSessionUnavailable) {
			t.Fatalf("real store fault must stay errClientSessionUnavailable, got %v", err)
		}
	})
}

// TestR1029ExpiredSessionDispatchesOnAllProtocolLegs（全链层）：三层协议
// 入向携过期分支会话时不得被 503 session_unavailable 拦截——请求应进入
// 分发（后续失败与 session 无关）。
func TestR1029ExpiredSessionDispatchesOnAllProtocolLegs(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		serve            func(*ChatHandler) http.Handler
	}{
		{"chat", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return h }},
		{"messages", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"m","input":"hi"}`, func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubSessionGetter{getErr: session.ErrSessionExpired}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			if tc.name == "chat" {
				h.executor = &executors.Executor{}
				h.provider = durableEndpointResolver{}
			}
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
			h.SetSessionGetter(stub)
			var failures []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { failures = append(failures, entry) })
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-Gw-Session-Id", "gs_gw_expired_branch")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)

			body := rec.Body.String()
			for _, f := range failures {
				if f.ErrorKind != nil && *f.ErrorKind == "session_unavailable" {
					t.Fatalf("expired session audited as store fault: status=%d body=%q", rec.Code, body)
				}
			}
			if strings.Contains(body, "session store unavailable") ||
				strings.Contains(body, "SESSION_STORE_UNAVAILABLE") ||
				strings.Contains(body, "not owned by this API key") {
				t.Fatalf("expired session surfaced store/forbidden text: status=%d body=%q", rec.Code, body)
			}
		})
	}
}

// TestR1029StoreFaultKeeps503WithStoreText（真实 store fault 守卫）：故障
// 语义不变（503 + session_unavailable 审计），但响应体必须是 store 文案，
// 不得再借用 SESSION_FORBIDDEN（"not owned"）误导归因。
func TestR1029StoreFaultKeeps503WithStoreText(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = &executors.Executor{}
	h.provider = durableEndpointResolver{}
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1"}})
	h.SetSessionGetter(&stubSessionGetter{getErr: errors.New("redis down")})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Gw-Session-Id", "gw_fault")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store fault status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SESSION_STORE_UNAVAILABLE") {
		t.Fatalf("store fault body must use store text: %s", body)
	}
	if strings.Contains(body, "SESSION_FORBIDDEN") || strings.Contains(body, "not owned by this API key") {
		t.Fatalf("store fault body must not borrow FORBIDDEN text: %s", body)
	}
}
