package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

const (
	controlledBodyIssuer   = "ai-session-manager"
	controlledBodyAudience = "llm-gateway-session-analytics"
	controlledBodySubject  = "service:ai-session-manager"
)

type controlledBodyClaims struct {
	TenantID string `json:"tenant_id"`
	Scope    string `json:"scope"`
	jwt.RegisteredClaims
}

type controlledBodyRef struct {
	RequestID string
	Slot      string
}

type controlledBodyResponse struct {
	BodyRef     string `json:"body_ref"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
	OccurredAt  string `json:"occurred_at"`
}

func parseControlledBodyRef(ref string) (controlledBodyRef, bool) {
	const prefix = "internal://body/"
	if !strings.HasPrefix(ref, prefix) {
		return controlledBodyRef{}, false
	}
	remainder := strings.TrimPrefix(ref, prefix)
	var slot string
	switch {
	case strings.HasSuffix(remainder, "/prompt"):
		slot = "prompt"
	case strings.HasSuffix(remainder, "/response"):
		slot = "response"
	default:
		return controlledBodyRef{}, false
	}
	requestID := strings.TrimSuffix(remainder, "/"+slot)
	if !safeControlledRequestID(requestID) {
		return controlledBodyRef{}, false
	}
	return controlledBodyRef{RequestID: requestID, Slot: slot}, true
}

func safeControlledRequestID(requestID string) bool {
	if requestID == "" || len(requestID) > 256 {
		return false
	}
	for i := 0; i < len(requestID); i++ {
		c := requestID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			continue
		}
		return false
	}
	return true
}

func validateControlledBodyToken(tokenString, secret string, now time.Time, issuer, audience string) (*controlledBodyClaims, error) {
	if tokenString == "" || secret == "" || issuer == "" || audience == "" {
		return nil, errors.New("missing token validation input")
	}
	claims := &controlledBodyClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || token == nil || !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.ExpiresAt == nil || !claims.ExpiresAt.Time.After(now) {
		return nil, errors.New("missing or expired token")
	}
	if claims.Issuer != issuer || !controlledBodyAudienceMatches(claims.Audience, audience) || claims.Subject != controlledBodySubject {
		return nil, errors.New("invalid token claims")
	}
	if strings.TrimSpace(claims.TenantID) == "" || !hasControlledBodyScope(claims.Scope) {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

func controlledBodyAudienceMatches(audiences jwt.ClaimStrings, expected string) bool {
	for _, audience := range audiences {
		if audience == expected {
			return true
		}
	}
	return false
}

func hasControlledBodyScope(scope string) bool {
	for _, value := range strings.Fields(scope) {
		if value == "session:read" {
			return true
		}
	}
	return false
}

func controlledBodyIssuerFromEnv() string {
	if value := os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_ISSUER"); value != "" {
		return value
	}
	return controlledBodyIssuer
}

func controlledBodyAudienceFromEnv() string {
	if value := os.Getenv("LLM_GATEWAY_SESSION_SERVICE_JWT_AUDIENCE"); value != "" {
		return value
	}
	return controlledBodyAudience
}

func (h *Handler) handleControlledBody(w http.ResponseWriter, r *http.Request) {
	claims, ok := controlledBodyBearerClaims(r, h.bodyServiceJWTSecret, time.Now())
	if !ok {
		writeControlledBodyAuthError(w)
		return
	}
	if r.Method != http.MethodGet {
		writeControlledBodyError(w, http.StatusMethodNotAllowed)
		return
	}

	ref := r.PathValue("body_ref")
	parsed, ok := parseControlledBodyRef(ref)
	if !ok || h.bodyDB == nil {
		writeControlledBodyError(w, http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	occurredAt, requestBody, responseBody, outboundBody, err := h.lookupControlledBody(ctx, parsed.RequestID, claims.TenantID)
	if err != nil {
		writeControlledBodyError(w, http.StatusNotFound)
		return
	}

	var content string
	var role string
	switch parsed.Slot {
	case "prompt":
		// 2026-08-24 Phase 1: outbound_body is the post-compression /
		// post-transform body actually sent upstream. For LLM Gateway's
		// canonical prompt slot we prefer outbound_body when present and
		// fall back to the original request_body for upstream messages
		// whose compression rewrite was a no-op.
		if v, found := normalizeControlledPrompt(outboundBody); found {
			content, ok = v, true
		} else {
			content, ok = normalizeControlledPrompt(requestBody)
		}
		role = "user"
	case "outbound":
		// 2026-08-24 Phase 1: explicit "outbound" slot — return the
		// compressed/transformed body as text. The slot has no normalized
		// message role; we surface the body verbatim when extractable,
		// otherwise 404.
		content, ok = normalizeControlledPrompt(outboundBody)
		role = "outbound"
	default:
		content, ok = normalizeControlledResponse(responseBody)
		role = "assistant"
	}
	if !ok {
		writeControlledBodyError(w, http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, controlledBodyResponse{
		BodyRef:     ref,
		Role:        role,
		Content:     content,
		ContentType: "text/plain",
		OccurredAt:  occurredAt.UTC().Format(time.RFC3339Nano),
	})
}

func controlledBodyBearerClaims(r *http.Request, fallbackSecret string, now time.Time) (*controlledBodyClaims, bool) {
	authorization := r.Header.Get("Authorization")
	if len(authorization) < len("Bearer ") || !strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return nil, false
	}
	tokenString := authorization[len("Bearer "):]
	if tokenString == "" || strings.TrimSpace(tokenString) != tokenString {
		return nil, false
	}
	claims, err := validateControlledBodyToken(tokenString, fallbackSecret, now, controlledBodyIssuerFromEnv(), controlledBodyAudienceFromEnv())
	if err != nil {
		return nil, false
	}
	return claims, true
}

func (h *Handler) lookupControlledBody(ctx context.Context, requestID, tenantID string) (time.Time, *string, *string, *string, error) {
	var occurredAt time.Time
	var requestBody, responseBody, outboundBody *string

	// 2026-08-24 Phase 1: outbound_body is also routed to
	// request_logs_bodies_hot alongside request_body / response_body. The main
	// request_logs_hot row keeps all three body columns NULL, so the LEFT JOIN
	// through the dedicated body table is the canonical read path. The COALESCE
	// keeps a (currently theoretical) backward-compatible fallback to any legacy
	// in-main-table value that older deployments may still hold.
	err := h.bodyDB.QueryRow(ctx, `
		SELECT rl.ts,
		       COALESCE(rb.request_body::text, rl.request_body::text),
		       COALESCE(rb.response_body::text, rl.response_body::text),
		       rb.outbound_body::text
		  FROM request_logs_hot rl
		  LEFT JOIN request_logs_bodies_hot rb
		    ON rb.request_id = rl.request_id
		 WHERE rl.request_id = $1
		   AND rl.tenant_id = $2
		 LIMIT 1
	`, requestID, tenantID).Scan(&occurredAt, &requestBody, &responseBody, &outboundBody)
	if err == nil {
		return occurredAt, requestBody, responseBody, outboundBody, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, sql.ErrNoRows) {
		// A stale/missing hot table should not prevent the bounded archive fallback.
		// The caller still returns the same 404 for every lookup failure.
	}

	err = h.bodyDB.QueryRow(ctx, `
		SELECT rl.ts,
		       COALESCE(rb.request_body::text, rl.request_body::text),
		       COALESCE(rb.response_body::text, rl.response_body::text),
		       rb.outbound_body::text
		  FROM request_logs_with_current_month rl
		  LEFT JOIN request_logs_bodies_with_current_month rb
		    ON rb.request_id = rl.request_id
		 WHERE rl.request_id = $1
		   AND rl.tenant_id = $2
		 LIMIT 1
	`, requestID, tenantID).Scan(&occurredAt, &requestBody, &responseBody, &outboundBody)
	if err != nil {
		return time.Time{}, nil, nil, nil, err
	}
	return occurredAt, requestBody, responseBody, outboundBody, nil
}

func normalizeControlledPrompt(raw *string) (string, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "", false
	}
	var document struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(*raw), &document); err != nil {
		return "", false
	}
	var lastUserContent json.RawMessage
	for _, message := range document.Messages {
		if message.Role == "user" {
			lastUserContent = message.Content
		}
	}
	if len(lastUserContent) == 0 {
		return "", false
	}
	var content string
	if err := json.Unmarshal(lastUserContent, &content); err != nil {
		return "", false
	}
	return content, true
}

func normalizeControlledResponse(raw *string) (string, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "", false
	}
	var document struct {
		Choices []struct {
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Text json.RawMessage `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(*raw), &document); err != nil {
		return "", false
	}
	for _, choice := range document.Choices {
		if choice.Message != nil {
			if content, ok := controlledString(choice.Message.Content); ok {
				return content, true
			}
		}
		if content, ok := controlledString(choice.Text); ok {
			return content, true
		}
	}
	return "", false
}

func controlledString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var content string
	if err := json.Unmarshal(raw, &content); err != nil || content == "" {
		return "", false
	}
	return content, true
}

func writeControlledBodyAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="service-body"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"authentication required"}`))
}

func writeControlledBodyError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"not found"}`))
}
