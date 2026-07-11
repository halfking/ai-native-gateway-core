package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardResponseWriterRecordsFirstStatus(t *testing.T) {
	t.Run("direct write defaults to OK", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := &dashboardResponseWriter{ResponseWriter: recorder, statusCode: http.StatusOK}
		if _, err := writer.Write([]byte("ok")); err != nil {
			t.Fatalf("write response: %v", err)
		}
		if writer.statusCode != http.StatusOK || recorder.Code != http.StatusOK {
			t.Fatalf("expected status 200, got writer=%d recorder=%d", writer.statusCode, recorder.Code)
		}
	})

	t.Run("first header is retained", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := &dashboardResponseWriter{ResponseWriter: recorder, statusCode: http.StatusOK}
		writer.WriteHeader(http.StatusForbidden)
		writer.WriteHeader(http.StatusOK)
		if writer.statusCode != http.StatusForbidden || recorder.Code != http.StatusForbidden {
			t.Fatalf("expected first status 403, got writer=%d recorder=%d", writer.statusCode, recorder.Code)
		}
	})
}
