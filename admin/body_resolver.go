package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
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
	occurredAt, requestBody, responseBody, err := h.lookupControlledBody(ctx, parsed.RequestID, claims.TenantID)
	if err != nil {
		writeControlledBodyError(w, http.StatusNotFound)
		return
	}

	var content string
	var role string
	if parsed.Slot == "prompt" {
		content, ok = normalizeControlledPrompt(requestBody)
		role = "user"
	} else {
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

func (h *Handler) lookupControlledBody(ctx context.Context, requestID, tenantID string) (time.Time, *string, *string, error) {
	var occurredAt time.Time
	var requestBody, responseBody *string

	// 2026-10-20（迁移 890）：v1 族整体退役 ⇒ 原来的两条 v1 腿（v1 hot 主行
	// JOIN v1 hot 正文表；canonical 视图 JOIN v1 bodies 视图）一起删掉，
	// 轮次与正文都只来自会话族。
	//
	// JOIN 键从 (request_id, ts) 改成 **request_id 单键**：会话族正文源
	// （db.SessionFamilyBodiesSourceSQL）**不投影 ts** —— 那是 db 侧刻意的列
	// 合同（两侧 bodies 的 ts 相等率实测 99.85% 不等；把 ts 加进去只会把一个
	// 解析期错误换成一个静默的行数变化），所以 v1 那套 ts 等值 JOIN 搬不过来。
	err := h.bodyDB.QueryRow(ctx, `
		SELECT rl.ts,
		       COALESCE(rb.request_body::text, ''),
		       COALESCE(rb.response_body::text, '')
		  FROM `+dbpkg.SessionFamilyTurnsSourceSQL()+` rl
		  LEFT JOIN `+dbpkg.SessionFamilyBodiesSourceSQL()+` rb
		    ON rb.request_id = rl.request_id
		 WHERE rl.request_id = $1
		   AND rl.tenant_id = $2
		 LIMIT 1
	`, requestID, tenantID).Scan(&occurredAt, &requestBody, &responseBody)
	if err != nil {
		// 找不到就是找不到：会话族是唯一事实源，没有第二处可查。
		// The caller still returns the same 404 for every lookup failure.
		return time.Time{}, nil, nil, err
	}
	// 正文可能只落在一侧（镜像期双写、停写后单写）：空的一侧按 request_id
	// 单键补查会话族正文源（列合同无 ts，恰是本兜底需要的形态）。
	emptyBody := requestBody == nil || *requestBody == ""
	emptyResp := responseBody == nil || *responseBody == ""
	if emptyBody || emptyResp {
		var srb, sob []byte
		if sessErr := h.bodyDB.QueryRow(ctx, `
			SELECT request_body::text, response_body::text
			  FROM `+dbpkg.SessionFamilyBodiesSourceSQL()+`
			 WHERE request_id = $1
			 LIMIT 1
		`, requestID).Scan(&srb, &sob); sessErr == nil {
			if emptyBody && len(srb) > 0 {
				s := string(srb)
				requestBody = &s
			}
			if emptyResp && len(sob) > 0 {
				s := string(sob)
				responseBody = &s
			}
		}
	}
	return occurredAt, requestBody, responseBody, nil
}

func normalizeControlledPrompt(raw *string) (string, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "", false
	}
	// 2026-10-20（迁移 890）：正文只有会话族一份，而会话族的 request_delta
	// 是**消息数组** `[{"role":"user","content":"…"}]`，不是 v1 家族的
	// 「带 messages 键的完整对象」。数组臂先试：JSON 数组解进切片成功即判定
	// 是数组形状（解进对象会报错 ⇒ 落到下面的对象臂）。
	var array []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(*raw), &array); err == nil {
		for i := len(array) - 1; i >= 0; i-- {
			if array[i].Role != "user" {
				continue
			}
			if content, ok := controlledString(array[i].Content); ok {
				return content, true
			}
		}
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
	// 2026-10-20（迁移 890）：会话族的 response_delta 是**消息数组**
	// `[{"role":"assistant","content":"…"}]`（理由同 normalizeControlledPrompt）。
	var array []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(*raw), &array); err == nil {
		for _, message := range array {
			if message.Role != "" && message.Role != "assistant" {
				continue
			}
			// ★ controlledText, not controlledString: the answer to a
			// tool-calling turn is a content ARRAY, and refusing it made
			// 46.1% of the governance domain's traffic invisible.
			if content, ok := controlledText(message.Content); ok {
				return content, true
			}
		}
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
			if content, ok := controlledText(choice.Message.Content); ok {
				return content, true
			}
		}
		if content, ok := controlledText(choice.Text); ok {
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

// controlledText returns the text of a message body written in either of the
// two shapes the session family stores.
//
// ★ WHY THE SECOND SHAPE EXISTS (measured, not guessed):
//
//	The answer to a turn that calls a tool is written as an OpenAI-style
//	content ARRAY — `[{"role":"assistant","content":[{"type":"text",
//	"text":"…"}],"tool_calls":[…]}]` — not as a plain string.
//	Before this function existed, only a plain string was readable ⇒ every
//	such answer was REFUSED by this endpoint ⇒ the answer was there and
//	still invisible to every consumer of it.
//
//	Measured on 2026-10-25 (154, the AI-session-manager governance domain):
//	the domain's 399 usages split into 184 "the answer is a content array"
//	and 37 "the answer is a plain string" ⇒ 46.1% of the traffic the
//	governance domain sees was invisible for this one reason.
//
// ★ THE RULE: a content array is readable when at least one of its parts
//
//	carries a `text` string. The parts' texts are joined with a newline,
//	because two text parts are two blocks, not two halves of one word.
//
// ★ WHAT IS DELIBERATELY NOT SERVED: the `tool_calls` that usually sit
//
//	next to the content array. This endpoint has one `content` field;
//	serving the tool calls would be a NEW field ⇒ a contract change ⇒
//	that is a different change, not this one.
func controlledText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var content string
	if err := json.Unmarshal(raw, &content); err == nil {
		if content == "" {
			return "", false
		}
		return content, true
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		var piece struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(part, &piece); err != nil || piece.Text == nil {
			continue
		}
		if strings.TrimSpace(*piece.Text) == "" {
			continue
		}
		texts = append(texts, *piece.Text)
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n"), true
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
