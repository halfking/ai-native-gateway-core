package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestParseControlledBodyRef(t *testing.T) {
	parsed, ok := parseControlledBodyRef("internal://body/req_123/prompt")
	require.True(t, ok)
	require.Equal(t, controlledBodyRef{RequestID: "req_123", Slot: "prompt"}, parsed)

	for _, ref := range []string{
		"",
		"internal://body/req_123/unknown",
		"internal://body/req_123/prompt/extra",
		"internal://body/../secret/prompt",
		"internal://body/req_123/prompt/../response",
		"internal://body/req/123/prompt",
	} {
		_, ok := parseControlledBodyRef(ref)
		require.False(t, ok, "ref %q must be rejected", ref)
	}
}

func TestNormalizeControlledPromptUsesLastUserString(t *testing.T) {
	raw := `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"last"}]}`
	content, ok := normalizeControlledPrompt(&raw)
	require.True(t, ok)
	require.Equal(t, "last", content)

	structured := `{"messages":[{"role":"user","content":[{"type":"text","text":"no"}]}]}`
	content, ok = normalizeControlledPrompt(&structured)
	require.False(t, ok)
	require.Empty(t, content)
}

func TestNormalizeControlledResponseTextFallback(t *testing.T) {
	raw := `{"choices":[{"message":{"content":null},"text":"fallback"}]}`
	content, ok := normalizeControlledResponse(&raw)
	require.True(t, ok)
	require.Equal(t, "fallback", content)

	messageRaw := `{"choices":[{"message":{"content":"assistant"},"text":"ignored"}]}`
	content, ok = normalizeControlledResponse(&messageRaw)
	require.True(t, ok)
	require.Equal(t, "assistant", content)
}

func TestValidateControlledBodyTokenRejectsWrongClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	secret := "test-secret"
	base := func() controlledBodyClaims {
		return controlledBodyClaims{
			TenantID: "tenant-a",
			Scope:    "session:read",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				Issuer:    controlledBodyIssuer,
				Audience:  jwt.ClaimStrings{controlledBodyAudience},
				Subject:   controlledBodySubject,
			},
		}
	}
	sign := func(claims controlledBodyClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString([]byte(secret))
		require.NoError(t, err)
		return signed
	}

	valid, err := validateControlledBodyToken(sign(base()), secret, now, controlledBodyIssuer, controlledBodyAudience)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", valid.TenantID)

	cases := []struct {
		name   string
		modify func(*controlledBodyClaims)
	}{
		{"subject", func(c *controlledBodyClaims) { c.Subject = "user:someone" }},
		{"audience", func(c *controlledBodyClaims) { c.Audience = jwt.ClaimStrings{"other-audience"} }},
		{"scope", func(c *controlledBodyClaims) { c.Scope = "session:write" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := base()
			tc.modify(&claims)
			_, err := validateControlledBodyToken(sign(claims), secret, now, controlledBodyIssuer, controlledBodyAudience)
			require.Error(t, err)
		})
	}
}

func TestHandleControlledBodyUsesTokenTenantOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	secret := "test-secret"
	now := time.Now().UTC()
	claims := controlledBodyClaims{
		TenantID: "tenant-a",
		Scope:    "session:read",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    controlledBodyIssuer,
			Audience:  jwt.ClaimStrings{controlledBodyAudience},
			Subject:   controlledBodySubject,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	occurred := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	requestBody := `{"messages":[{"role":"user","content":"first"},{"role":"user","content":"last"}]}`
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-1", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{"ts", "request_body", "response_body", "outbound_body"}).AddRow(occurred, &requestBody, nil, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/bodies/internal://body/req-1/prompt", nil)
	req.SetPathValue("body_ref", "internal://body/req-1/prompt")
	req.Header.Set("Authorization", "bearer "+signed)
	req.Header.Set("X-Tenant-ID", "tenant-b")
	recorder := httptest.NewRecorder()

	(&Handler{bodyDB: mock, bodyServiceJWTSecret: secret}).handleControlledBody(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.JSONEq(t, `{"body_ref":"internal://body/req-1/prompt","role":"user","content":"last","content_type":"text/plain","occurred_at":"2026-08-18T01:02:03Z"}`, recorder.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLookupControlledBodyBindsTenantToHotQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	occurred := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"text":"world"}]}`
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-1", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{"ts", "request_body", "response_body", "outbound_body"}).AddRow(occurred, &requestBody, &responseBody, nil))

	gotAt, gotRequest, gotResponse, _, err := (&Handler{bodyDB: mock}).lookupControlledBody(context.Background(), "req-1", "tenant-a")
	require.NoError(t, err)
	require.Equal(t, occurred, gotAt)
	require.NotNil(t, gotRequest)
	require.Equal(t, requestBody, *gotRequest)
	require.NotNil(t, gotResponse)
	require.Equal(t, responseBody, *gotResponse)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLookupControlledBodyTenantMismatchReturnsNoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-1", "tenant-b").
		WillReturnError(jwt.ErrTokenInvalidClaims)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-1", "tenant-b").
		WillReturnError(jwt.ErrTokenInvalidClaims)

	_, _, _, _, err = (&Handler{bodyDB: mock}).lookupControlledBody(context.Background(), "req-1", "tenant-b")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleControlledBodyRejectsInvalidServiceTokens(t *testing.T) {
	secret := "test-secret"
	now := time.Now().UTC()
	base := controlledBodyClaims{
		TenantID: "tenant-a",
		Scope:    "session:read",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    controlledBodyIssuer,
			Audience:  jwt.ClaimStrings{controlledBodyAudience},
			Subject:   controlledBodySubject,
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	sign := func(secret string, claims controlledBodyClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString([]byte(secret))
		require.NoError(t, err)
		return signed
	}
	cases := []struct {
		name  string
		token string
	}{
		{"wrong secret", sign("other-secret", base)},
		{"expired", sign(secret, func() controlledBodyClaims {
			claims := base
			claims.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			return claims
		}())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/bodies/internal://body/req-1/prompt", nil)
			req.SetPathValue("body_ref", "internal://body/req-1/prompt")
			req.Header.Set("Authorization", "Bearer "+tc.token)
			recorder := httptest.NewRecorder()
			(&Handler{bodyServiceJWTSecret: secret}).handleControlledBody(recorder, req)
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
		})
	}
}

func TestHandleControlledBodyHidesCrossTenantBody(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	secret := "test-secret"
	now := time.Now().UTC()
	claims := controlledBodyClaims{
		TenantID: "tenant-b",
		Scope:    "session:read",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    controlledBodyIssuer,
			Audience:  jwt.ClaimStrings{controlledBodyAudience},
			Subject:   controlledBodySubject,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-cross-tenant", "tenant-b").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*FROM request_logs_with_current_month.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-cross-tenant", "tenant-b").WillReturnError(pgx.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/bodies/internal://body/req-cross-tenant/prompt", nil)
	req.SetPathValue("body_ref", "internal://body/req-cross-tenant/prompt")
	req.Header.Set("Authorization", "Bearer "+signed)
	recorder := httptest.NewRecorder()
	(&Handler{bodyDB: mock, bodyServiceJWTSecret: secret}).handleControlledBody(recorder, req)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestControlledBodyResponseShape(t *testing.T) {
	response := controlledBodyResponse{
		BodyRef:     "internal://body/req_1/response",
		Role:        "assistant",
		Content:     "ok",
		ContentType: "text/plain",
		OccurredAt:  "2026-08-18T00:00:00Z",
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.JSONEq(t, `{"body_ref":"internal://body/req_1/response","role":"assistant","content":"ok","content_type":"text/plain","occurred_at":"2026-08-18T00:00:00Z"}`, string(encoded))
}

func TestHandleControlledBodyFallsBackToArchiveView(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	secret := "test-secret"
	now := time.Now().UTC()
	claims := controlledBodyClaims{
		TenantID: "tenant-a",
		Scope:    "session:read",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    controlledBodyIssuer,
			Audience:  jwt.ClaimStrings{controlledBodyAudience},
			Subject:   controlledBodySubject,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	occurred := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	requestBody := `{"messages":[{"role":"user","content":"archived prompt"}]}`
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*FROM request_logs_hot.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-archive", "tenant-a").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*FROM request_logs_with_current_month.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-archive", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{"ts", "request_body", "response_body", "outbound_body"}).AddRow(occurred, &requestBody, nil, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/bodies/internal://body/req-archive/prompt", nil)
	req.SetPathValue("body_ref", "internal://body/req-archive/prompt")
	req.Header.Set("Authorization", "Bearer "+signed)
	recorder := httptest.NewRecorder()
	(&Handler{bodyDB: mock, bodyServiceJWTSecret: secret}).handleControlledBody(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "archived prompt")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleControlledBodyReturnsNotFoundForTenantWithoutBody(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	secret := "test-secret"
	now := time.Now().UTC()
	claims := controlledBodyClaims{
		TenantID: "tenant-a",
		Scope:    "session:read",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			Issuer:    controlledBodyIssuer,
			Audience:  jwt.ClaimStrings{controlledBodyAudience},
			Subject:   controlledBodySubject,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-cross-tenant", "tenant-a").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT rl\.ts,.*FROM request_logs_with_current_month.*rl\.request_id = \$1.*rl\.tenant_id = \$2`).
		WithArgs("req-cross-tenant", "tenant-a").WillReturnError(pgx.ErrNoRows)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/bodies/internal://body/req-cross-tenant/prompt", nil)
	req.SetPathValue("body_ref", "internal://body/req-cross-tenant/prompt")
	req.Header.Set("Authorization", "Bearer "+signed)
	recorder := httptest.NewRecorder()
	(&Handler{bodyDB: mock, bodyServiceJWTSecret: secret}).handleControlledBody(recorder, req)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
