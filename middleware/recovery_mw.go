package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

type RecoveryMiddleware struct {
	BaseMiddleware
}

func NewRecoveryMiddleware() *RecoveryMiddleware {
	return &RecoveryMiddleware{
		BaseMiddleware: BaseMiddleware{name: "recovery"},
	}
}

func (m *RecoveryMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"panic", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"panic"}}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
