package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/identity"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Application single sign-on.
//
// The gateway is the identity source for the whole product line: a user who
// signed in here must be able to open a sibling service (FreeLLM API, and
// later the other apps in the mesh) without a second login. This endpoint
// mints the short-lived, audience-scoped token that sibling service verifies.
//
// Why a minted token instead of sharing the session cookie:
//   - the audience is the resource-server boundary. A token minted for
//     `freellmapi-api` is refused by every other service, and vice versa;
//   - the sibling service never learns the gateway's session-signing secret,
//     so a leak there cannot be replayed as a gateway session;
//   - the token is short-lived (default 5 minutes) and single-purpose, so it
//     is useless if it leaks into a browser history or a proxy log.
//
// The minting path is the shared identity contract (pkg/identity), the same
// one RedClaw / openpocket / memora / acc already use.

// appAudience is one sibling service the gateway can hand out a ticket for.
//
// The table is static on purpose: the audience a service accepts is part of
// that service's security boundary, so it must not be configurable per
// request or per tenant. Adding a service = adding a row here.
type appAudience struct {
	Code     string // query parameter value: ?app=freellmapi
	Audience string // aud claim the sibling service accepts
	Label    string // human-readable name for the UI
	// URLEnv names the env var holding the service's public base URL. When
	// it is set, the response also carries a ready-to-open landing URL.
	URLEnv string
}

var appAudiences = map[string]appAudience{
	"freellmapi": {
		Code:     "freellmapi",
		Audience: "freellmapi-api",
		Label:    "FreeLLM API",
		URLEnv:   "FREELLMAPI_PUBLIC_URL",
	},
}

// appTokenTTL is how long a minted app ticket stays usable. Five minutes is
// long enough for a browser to complete a redirect chain, short enough that a
// leaked URL is worthless almost immediately.
const appTokenTTL = 5 * time.Minute

// handleAppToken mints a single sign-on ticket for a sibling service.
//
// GET /api/auth/app-token?app=freellmapi
//
// Response:
//
//	{"app":"freellmapi","audience":"freellmapi-api","token":"<jwt>",
//	 "expires_at":"...","url":"https://freellm.itestu.cn/sso?token=<jwt>"}
//
// The token is ALSO returned in the `X-LLMGW-App-Token` response header so an
// nginx auth_request can capture it without parsing a JSON body — that is
// what makes a fully automatic "open the app, land inside it" flow possible
// without a front-end round trip.
func (h *Handler) handleAppToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	appCode := strings.TrimSpace(r.URL.Query().Get("app"))
	if appCode == "" {
		writeError(w, http.StatusBadRequest, "app is required")
		return
	}
	app, ok := appAudiences[appCode]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown app: "+appCode)
		return
	}

	// Fail closed: without the shared secret this gateway is not a member of
	// the identity mesh, so it must not pretend to be able to mint a ticket.
	// There is no fallback to the legacy single-issuer path here — a sibling
	// service verifies the multi-issuer contract, so a legacy token would be
	// rejected by the peer anyway.
	if !identity.HasSharedSecret() {
		writeError(w, http.StatusServiceUnavailable,
			"single sign-on is not configured: "+identity.EnvSharedSecret+" is unset")
		return
	}

	// The email is what the sibling service maps onto a local account, so it
	// is worth one extra read. A missing email is not fatal: the peer falls
	// back to a synthetic address derived from the username.
	email := h.userEmail(r.Context(), auth.UserID)

	extra := map[string]any{
		"username": auth.Username,
		"role":     auth.Role,
	}
	if email != "" {
		extra["email"] = email
	}

	userID := strconv.Itoa(auth.UserID)
	subject := userID
	if auth.CanonicalUserID != "" {
		// A cross-project principal already carries a canonical id; using it
		// as the subject keeps the same person recognisable across services.
		subject = auth.CanonicalUserID
	}

	roles := []string{}
	if auth.Role != "" {
		roles = append(roles, auth.Role)
	}

	token, err := identity.MintCrossIssuerToken(identity.MintSpec{
		Subject:  subject,
		UserID:   userID,
		TenantID: auth.TenantID,
		Roles:    roles,
		Scope:    app.Audience,
		Audience: app.Audience,
		TTL:      appTokenTTL,
		Extra:    extra,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not mint application token: "+err.Error())
		return
	}

	// A minted ticket is a credential the sibling service will exchange for
	// an identity, so every successful issuance leaves a settings_audit row
	// (F-6). The failure paths above are read-only rejections and are not
	// recorded. Best-effort, like every settings.WriteAudit caller: an audit
	// failure must never block the sign-on it is describing.
	appTokenAuditWriter(r.Context(), h.db, appTokenAuditEntry(r, auth, app))

	expiresAt := time.Now().Add(appTokenTTL)
	payload := map[string]any{
		"app":        app.Code,
		"label":      app.Label,
		"audience":   app.Audience,
		"token":      token,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	}
	if base := strings.TrimSpace(os.Getenv(app.URLEnv)); base != "" {
		payload["url"] = strings.TrimRight(base, "/") + "/sso?token=" + token
	}

	// Header copy for the nginx auth_request path.
	w.Header().Set("X-LLMGW-App-Token", token)
	w.Header().Set("X-LLMGW-App-Audience", app.Audience)
	writeJSON(w, http.StatusOK, payload)
}

// appTokenAuditWriter is the test seam for mint audit rows; production uses
// settings.WriteAudit. (Unit tests cannot inject a *pgxpool.Pool into
// Handler.db, same reason authRevocationProbe is a seam.)
var appTokenAuditWriter = settings.WriteAudit

// appTokenAuditEntry builds the settings_audit row for one successful mint:
// the actor lands in the operator columns, the target app in setting_key
// (the audience is the security boundary), tenant in tenant_id, and the
// numeric user_id in new_value JSON. Action "mint" is new vocabulary for the
// free-text action column (settings-kv uses update/rollback/delete).
func appTokenAuditEntry(r *http.Request, auth *AuthContext, app appAudience) settings.AuditEntry {
	user, role, ip := authIdentity(r)
	detail, err := json.Marshal(map[string]any{"user_id": auth.UserID})
	if err != nil {
		detail = nil
	}
	return settings.AuditEntry{
		SettingKey:   "app_token:" + app.Audience,
		TenantID:     auth.TenantID,
		Action:       "mint",
		NewValue:     detail,
		OperatorUser: user,
		OperatorRole: role,
		ClientIP:     ip,
	}
}

// userEmail reads the authenticated user's email. A read failure is not
// fatal — the peer can fall back to a synthetic address — so the error is
// swallowed and the email is simply left empty.
func (h *Handler) userEmail(ctx context.Context, userID int) string {
	if h.db == nil || userID <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var email string
	if err := h.db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		return ""
	}
	return strings.TrimSpace(email)
}
