package streaming

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// gwSessionTaskFromRequest resolves gateway session and task identifiers for
// request_logs correlation. Priority:
//   - session: X-Gw-Session-Id > X-Session-Id > loaded session.SessionID
//   - task:    X-Gw-Task-Id > loaded session.TaskID
func gwSessionTaskFromRequest(r *http.Request, session *session.Session) (sessionID, taskID string) {
	if r != nil {
		sessionID = sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id"))
		if sessionID == "" {
			legacy := strings.TrimSpace(r.Header.Get("X-Session-Id"))
			if strings.HasPrefix(legacy, "gw_") {
				sessionID = legacy
			}
		}
		taskID = strings.TrimSpace(r.Header.Get("X-Gw-Task-Id"))
	}
	if session != nil {
		if sessionID == "" {
			sessionID = strings.TrimSpace(session.SessionID)
		}
		if taskID == "" {
			taskID = strings.TrimSpace(session.TaskID)
		}
	}
	return sessionID, taskID
}
