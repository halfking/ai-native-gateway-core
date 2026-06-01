package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDContextKey struct{}

type RequestIDMiddleware struct {
	BaseMiddleware
}

func NewRequestIDMiddleware() *RequestIDMiddleware {
	return &RequestIDMiddleware{
		BaseMiddleware: BaseMiddleware{name: "request_id"},
	}
}

func (m *RequestIDMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = generateRequestID()
			r.Header.Set("X-Request-Id", id)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
