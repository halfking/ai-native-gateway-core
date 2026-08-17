package streaming

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// gwSessionTaskFromRequest resolves gateway session and task identifiers for
// request_logs correlation. Priority:
//   - session: X-Gw-Session-Id > X-Session-Id > loaded session.SessionID
//   - task:    X-Gw-Task-Id > loaded session.TaskID
func gwSessionTaskFromRequest(r *http.Request, session *session.Session) (sessionID, taskID string) {
	if r != nil {
		sessionID = sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id"))
		if sessionID == "" {
			legacy := sanitizeRequestCorrelationID(r.Header.Get("X-Session-Id"))
			if strings.HasPrefix(legacy, "gw_") {
				sessionID = legacy
			}
		}
		taskID = sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id"))
	}
	if session != nil {
		if sessionID == "" {
			sessionID = sanitizeRequestCorrelationID(session.SessionID)
		}
		if taskID == "" {
			taskID = sanitizeRequestCorrelationID(session.TaskID)
		}
	}
	return sessionID, taskID
}

const maxRequestCorrelationIDLen = 128

func sanitizeRequestCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxRequestCorrelationIDLen {
		return ""
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return ""
	}
	return value
}
