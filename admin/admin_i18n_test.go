package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/i18n"
)

// TestWriteJSONErrCtx_Localization verifies that admin error responses are
// translated according to the locale in r.Context().
func TestWriteJSONErrCtx_Localization(t *testing.T) {
	tests := []struct {
		name         string
		locale       i18n.Locale
		messageKey   string
		wantContains string // substring expected in the translated message
	}{
		{
			name:         "method_not_allowed_en",
			locale:       i18n.En,
			messageKey:   "admin_method_not_allowed",
			wantContains: "Method not allowed",
		},
		{
			name:         "method_not_allowed_ja",
			locale:       i18n.Ja,
			messageKey:   "admin_method_not_allowed",
			wantContains: "許可されていないメソッド",
		},
		{
			name:         "method_not_allowed_zh_CN",
			locale:       i18n.ZhCN,
			messageKey:   "admin_method_not_allowed",
			wantContains: "不允许的请求方法",
		},
		{
			name:         "days_out_of_range_de",
			locale:       i18n.De,
			messageKey:   "admin_days_out_of_range",
			wantContains: "Tage müssen zwischen 1 und 90",
		},
		{
			name:         "work_type_not_found_fr",
			locale:       i18n.Fr,
			messageKey:   "admin_work_type_not_found",
			wantContains: "Type de travail non trouvé",
		},
		{
			name:         "invalid_json_es",
			locale:       i18n.Es,
			messageKey:   "admin_invalid_json",
			wantContains: "Cuerpo JSON inválido",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			// Inject locale into context
			ctx := i18n.WithLocale(context.Background(), tt.locale)
			req = req.WithContext(ctx)

			// Call the i18n-aware error writer
			writeJSONErrCtx(w, req, http.StatusBadRequest, tt.messageKey)

			// Verify status code
			if w.Code != http.StatusBadRequest {
				t.Errorf("want status %d, got %d", http.StatusBadRequest, w.Code)
			}

			// Verify translated message appears in response body
			body := w.Body.String()
			if !containsString(body, tt.wantContains) {
				t.Errorf("want body to contain %q, got:\n%s", tt.wantContains, body)
			}

			// Verify code field is present (stable machine-readable token)
			if !containsString(body, tt.messageKey) {
				t.Errorf("want body to contain code=%q, got:\n%s", tt.messageKey, body)
			}
		})
	}
}

// containsString is a helper to check substring presence (case-sensitive).
func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || len(needle) == 0 ||
			func() bool {
				for i := 0; i <= len(haystack)-len(needle); i++ {
					if haystack[i:i+len(needle)] == needle {
						return true
					}
				}
				return false
			}())
}
