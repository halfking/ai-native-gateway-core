package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func TestAuthorityEchoUsesConfiguredProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cidrs  string
		peer   string
		realIP string
		xff    string
		want   string
	}{
		{name: "default loopback nginx", peer: "127.0.0.1:12345", realIP: "198.51.100.17", want: "198.51.100.17"},
		{name: "default IPv6 proxy", peer: "[::1]:12345", xff: "198.51.100.18", want: "198.51.100.18"},
		{name: "configured proxy", cidrs: "10.23.0.0/16", peer: "10.23.0.4:12345", realIP: "198.51.100.19", want: "198.51.100.19"},
		{name: "direct peer", peer: "192.0.2.10:12345", realIP: "198.51.100.17", xff: "198.51.100.17", want: "192.0.2.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LLM_GATEWAY_TRUSTED_PROXY_CIDRS", tc.cidrs)
			e := newAuthorityEcho()
			e.GET("/probe", func(c *echo.Context) error { return c.String(http.StatusOK, c.RealIP()) })
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set(echo.HeaderXRealIP, tc.realIP)
			req.Header.Set(echo.HeaderXForwardedFor, tc.xff)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, tc.want, rec.Body.String())
		})
	}
}
