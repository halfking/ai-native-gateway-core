package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/labstack/echo/v5"
)

func TestCustomerClientIPUsesOriginProxyPolicy(t *testing.T) {
	e := echo.New()
	e.GET("/probe", func(c *echo.Context) error {
		return c.String(http.StatusOK, clientIPFromEchoRequest(c))
	})
	trustedOrigin := middleware.NewOriginMiddlewareWithTrustedProxies(
		middleware.ParseTrustedProxyCIDRs([]string{"127.0.0.1/32"}))

	for _, tc := range []struct {
		name    string
		peer    string
		handler http.Handler
		want    string
	}{
		{name: "trusted proxy context", peer: "127.0.0.1:12345", handler: trustedOrigin.Wrap(e), want: "198.51.100.17"},
		{name: "direct peer context", peer: "192.0.2.10:12345", handler: trustedOrigin.Wrap(e), want: "192.0.2.10"},
		{name: "standalone echo", peer: "192.0.2.11:12345", handler: e, want: "192.0.2.11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set(echo.HeaderXRealIP, "198.51.100.17")
			req.Header.Set(echo.HeaderXForwardedFor, "198.51.100.18, 127.0.0.1")
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
				t.Fatalf("client IP = %q (HTTP %d), want %q", rec.Body.String(), rec.Code, tc.want)
			}
		})
	}
}

func TestCustomerMiddlewareLogsResponseStatus(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	for _, tc := range []struct {
		name    string
		handler echo.HandlerFunc
		status  int
	}{
		{name: "written response", handler: func(c *echo.Context) error { return c.NoContent(http.StatusAccepted) }, status: http.StatusAccepted},
		{name: "returned HTTP error", handler: func(c *echo.Context) error { return echo.NewHTTPError(http.StatusBadRequest, "invalid request") }, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			e := echo.New()
			e.Use(noAuthCustomerMiddleware())
			e.GET("/probe", tc.handler)
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.RemoteAddr = "192.0.2.12:12345"
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("HTTP status = %d, want %d", rec.Code, tc.status)
			}
			var entry struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("decode customer request log: %v: %s", err, logs.String())
			}
			if entry.Status != tc.status {
				t.Fatalf("logged status = %d, want %d", entry.Status, tc.status)
			}
		})
	}
}
