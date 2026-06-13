package relay

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/sessions"
)

// gwSessionTaskFromRequest resolves gateway session and task identifiers for
// request_logs correlation. Priority:
//   - session: X-Gw-Session-Id > X-Session-Id > loaded session.SessionID
//   - task:    X-Gw-Task-Id > loaded session.TaskID
func gwSessionTaskFromRequest(r *http.Request, session *sessions.Session) (sessionID, taskID string) {
	if r != nil {
		sessionID = strings.TrimSpace(r.Header.Get("X-Gw-Session-Id"))
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.Header.Get("X-Session-Id"))
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
