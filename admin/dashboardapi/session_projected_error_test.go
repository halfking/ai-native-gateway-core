package dashboardapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProjectedSessionErrorDistinguishesClientCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"client canceled", canceled, 499},
		{"server deadline", expired, http.StatusInternalServerError},
		{"database error", context.Background(), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeProjectedSessionError(tc.ctx, response, http.StatusInternalServerError, ErrCodeDatabaseError,
				"session statistics unavailable", errors.New("database error"))
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
}
