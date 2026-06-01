package middleware

import (
	"net/http"
	"strings"
)

type CORSMiddleware struct {
	BaseMiddleware
	origins      string
	allowMethods string
	allowHeaders string
	maxAge       string
}

func NewCORSMiddleware(origins string) *CORSMiddleware {
	if origins == "" {
		origins = "*"
	}
	return &CORSMiddleware{
		BaseMiddleware: BaseMiddleware{name: "cors"},
		origins:        origins,
		allowMethods:   "GET, POST, PUT, DELETE, OPTIONS",
		allowHeaders:   "Content-Type, Authorization, X-Request-Id, X-Device-Seed, X-Machine-Id, X-Runtime-Name, X-Runtime-Version, X-OS-Name, X-OS-Arch, X-Client-Profile",
		maxAge:         "86400",
	}
}

func (m *CORSMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if m.origins == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, o := range strings.Split(m.origins, ",") {
				if strings.TrimSpace(o) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					break
				}
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", m.allowMethods)
		w.Header().Set("Access-Control-Allow-Headers", m.allowHeaders)
		w.Header().Set("Access-Control-Max-Age", m.maxAge)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
