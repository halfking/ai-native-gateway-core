// bg/probe_http_test.go — v5 tests for the GET-based probe with retry
package bg

import (
	"testing"
)

func TestClassifyHTTPResponse_200OK(t *testing.T) {
	// A successful GET /v1/models response with model list
	body := `{"data":[{"id":"gpt-4o"},{"id":"gpt-4-turbo"},{"id":"deepseek-chat"}]}`
	r := classifyHTTPResponse(200, body, 42)
	if r.status != "ok" || r.category != probeCategoryOK {
		t.Errorf("200 OK: expected status=ok category=ok, got status=%s category=%s", r.status, r.category)
	}
	if len(r.modelIDs) != 3 || r.modelIDs[0] != "gpt-4o" {
		t.Errorf("200 OK: expected 3 model IDs starting with gpt-4o, got %v", r.modelIDs)
	}
}

func TestClassifyHTTPResponse_200OKGeminiShape(t *testing.T) {
	// Gemini uses {"models":[{"name":"models/gemini-pro"}]}
	body := `{"models":[{"name":"models/gemini-pro"},{"name":"models/gemini-flash"}]}`
	r := classifyHTTPResponse(200, body, 50)
	if r.status != "ok" || r.category != probeCategoryOK {
		t.Errorf("Gemini shape: expected status=ok, got status=%s", r.status)
	}
	if len(r.modelIDs) != 2 || r.modelIDs[0] != "gemini-pro" {
		t.Errorf("Gemini shape: expected 2 models starting with gemini-pro, got %v", r.modelIDs)
	}
}

func TestClassifyHTTPResponse_401Auth(t *testing.T) {
	body := `{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`
	r := classifyHTTPResponse(401, body, 15)
	if r.status != "auth" || r.category != probeCategoryProviderError {
		t.Errorf("401: expected status=auth category=provider_error, got status=%s category=%s", r.status, r.category)
	}
}

func TestClassifyHTTPResponse_402Quota(t *testing.T) {
	body := `{"error":{"code":"insufficient_quota","message":"You have exceeded your quota"}}`
	r := classifyHTTPResponse(402, body, 10)
	if r.status != "quota_exhausted" || r.category != probeCategoryProviderError {
		t.Errorf("402: expected status=quota_exhausted category=provider_error, got status=%s category=%s", r.status, r.category)
	}
}

func TestClassifyHTTPResponse_429RateLimit(t *testing.T) {
	body := `{"error":{"code":"requests_per_5h_exceeded","message":"Rate limit: 5h exceeded"}}`
	r := classifyHTTPResponse(429, body, 5)
	if r.status != "rate_limit_5h" || r.category != probeCategoryProviderError {
		t.Errorf("429 5h: expected status=rate_limit_5h category=provider_error, got status=%s category=%s", r.status, r.category)
	}
}

func TestClassifyHTTPResponse_429Monthly(t *testing.T) {
	body := `{"error":{"code":"insufficient_quota","message":"Monthly token limit exceeded"}}`
	r := classifyHTTPResponse(429, body, 5)
	if r.status != "rate_limit_monthly" || r.category != probeCategoryProviderError {
		t.Errorf("429 monthly: expected status=rate_limit_monthly, got status=%s", r.status)
	}
}

func TestClassifyHTTPResponse_429Generic(t *testing.T) {
	body := `{"error":{"code":"rate_limit_exceeded","message":"Too many requests"}}`
	r := classifyHTTPResponse(429, body, 5)
	if r.status != "rate_limit" || r.category != probeCategoryProviderError {
		t.Errorf("429 generic: expected status=rate_limit, got status=%s", r.status)
	}
}

func TestClassifyHTTPResponse_404EndpointID(t *testing.T) {
	// Volcano Ark endpoint_id_required
	body := `InvalidEndpointOrModel.NotFound`
	r := classifyHTTPResponse(404, body, 25)
	if r.status != "skipped" || r.category != probeCategorySkipped {
		t.Errorf("404 Volcano Ark: expected status=skipped, got status=%s", r.status)
	}
}

func TestClassifyHTTPResponse_404ModelNotFound(t *testing.T) {
	// Regular model not found (outbound_model set)
	body := `{"error":{"code":"model_not_found","message":"Model does not exist"}}`
	r := classifyHTTPResponse(404, body, 25)
	if r.status != "model_not_found" || r.category != probeCategoryModelUnavailable {
		t.Errorf("404 model: expected status=model_not_found category=model_unavailable, got status=%s category=%s", r.status, r.category)
	}
}

func TestClassifyHTTPResponse_500(t *testing.T) {
	body := `{"error":{"code":"internal_server_error"}}`
	r := classifyHTTPResponse(503, body, 100)
	if r.status != "http_5xx" || r.category != probeCategoryProviderError {
		t.Errorf("5xx: expected status=http_5xx category=provider_error, got status=%s category=%s", r.status, r.category)
	}
}

func TestParseModelList_OpenAI(t *testing.T) {
	body := `{"data":[{"id":"gpt-4"},{"id":"gpt-4o"},{"id":"deepseek-chat"}]}`
	ids := parseModelList(body)
	if len(ids) != 3 || ids[0] != "gpt-4" {
		t.Errorf("expected 3 models starting with gpt-4, got %v", ids)
	}
}

func TestParseModelList_Anthropic(t *testing.T) {
	body := `{"data":[{"id":"claude-3-5-sonnet-20241022"},{"id":"claude-opus-4-20250514"}]}`
	ids := parseModelList(body)
	if len(ids) != 2 || ids[0] != "claude-3-5-sonnet-20241022" {
		t.Errorf("Anthropic: expected 2 models, got %v", ids)
	}
}

func TestParseModelList_Empty(t *testing.T) {
	ids := parseModelList("")
	if ids != nil {
		t.Errorf("empty body should return nil, got %v", ids)
	}
}

func TestDetectQuotaKind(t *testing.T) {
	if k := detectQuotaKind("insufficient_quota", "insufficient_quota"); k != "quota_exhausted" {
		t.Errorf("expected quota_exhausted, got %s", k)
	}
	if k := detectQuotaKind("balance_not_sufficient", ""); k != "quota_exhausted" {
		t.Errorf("expected quota_exhausted, got %s", k)
	}
}

func TestDetectRateLimitKind(t *testing.T) {
	body := "you have exceeded the 5 hour rate limit"
	if k := detectRateLimitKind(body, ""); k != "rate_limit_5h" {
		t.Errorf("5h: expected rate_limit_5h, got %s", k)
	}
	body = "monthly token budget exhausted"
	if k := detectRateLimitKind(body, ""); k != "rate_limit_monthly" {
		t.Errorf("monthly: expected rate_limit_monthly, got %s", k)
	}
	body = "weekly rate limit exceeded"
	if k := detectRateLimitKind(body, ""); k != "rate_limit_weekly" {
		t.Errorf("weekly: expected rate_limit_weekly, got %s", k)
	}
	body = "rpm limit: requests per minute exceeded"
	if k := detectRateLimitKind(body, ""); k != "rate_limit_short" {
		t.Errorf("minute: expected rate_limit_short, got %s", k)
	}
}

func TestParseErrorCodeAndMessage(t *testing.T) {
	body := `{"error":{"code":"insufficient_quota","message":"You have exceeded quota"}}`
	code, msg := parseErrorCodeAndMessage(body)
	if code != "insufficient_quota" || msg != "You have exceeded quota" {
		t.Errorf("expected code=insufficient_quota msg='You have exceeded quota', got code=%s msg=%s", code, msg)
	}
}

func TestParseErrorCodeAndMessage_OpenAIShape(t *testing.T) {
	body := `{"error":{"type":"invalid_request_error","message":"The model does not exist"}}`
	code, msg := parseErrorCodeAndMessage(body)
	if code != "invalid_request_error" || msg != "The model does not exist" {
		t.Errorf("expected code=invalid_request_error, got code=%s msg=%s", code, msg)
	}
}

func TestParseErrorCodeAndMessage_Empty(t *testing.T) {
	code, msg := parseErrorCodeAndMessage("{}")
	if code != "" || msg != "" {
		t.Errorf("expected empty, got code=%s msg=%s", code, msg)
	}
}
