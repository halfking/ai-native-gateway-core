package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

var errBodyTooLarge = errors.New("request body too large")

// requestAttemptMeta captures request-side facts as early as possible so
// request_logs rows stay useful even when auth or body read fails later.
type requestAttemptMeta struct {
	APIKeyPrefix    string
	APIKeyOwnerUser string
	ApplicationCode string
	ClientProfile   string
	IdentityHash    string
	RequestMode     string
	KeyStatus       string // missing | valid | invalid_<db-status> | invalid_unknown
	LookupKeyID     *int
}

// bufferRequestBody reads the body into memory and replaces r.Body so later
// handlers can re-read without losing bytes.
func bufferRequestBody(r *http.Request, limit int) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, err
}

// ensureRequestBodyBuffered peeks the JSON body once for logging and model
// extraction. Safe to call multiple times.
func ensureRequestBodyBuffered(r *http.Request, bodyOut *[]byte, modelOut *string) error {
	if bodyOut != nil && len(*bodyOut) > 0 {
		return nil
	}
	buf, err := bufferRequestBody(r, maxBodySize)
	if bodyOut != nil && len(buf) > 0 {
		*bodyOut = buf
	}
	if modelOut != nil && *modelOut == "" && len(buf) > 0 {
		*modelOut = extractModelFromBody(buf)
	}
	if len(buf) > maxBodySize {
		return errBodyTooLarge
	}
	return err
}

func maskAPIKeyPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "无key"
	}
	if len(raw) <= 12 {
		return raw + "***"
	}
	return raw[:12] + "***"
}

func formatKeyPrefixDisplay(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	if strings.HasSuffix(prefix, "***") {
		return prefix
	}
	return prefix + "***"
}

func (h *ChatHandler) fillAttemptMeta(r *http.Request, keyInfo *auth.KeyInfo, meta *requestAttemptMeta) {
	if meta == nil || r == nil {
		return
	}
	if meta.RequestMode == "" {
		meta.RequestMode = requestModeFromPath(r.URL.Path)
	}
	h.resolveKeyMeta(r.Context(), extractBearerToken(r), keyInfo, meta)

	cp := clientProfileFromKey(keyInfo)
	if cp == "" {
		cp = strings.TrimSpace(meta.ClientProfile)
	}
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), cp)
	if meta.ClientProfile == "" {
		meta.ClientProfile = cp
	}
	if meta.IdentityHash == "" {
		meta.IdentityHash = clientID.ShortID()
	}
}

func (h *ChatHandler) resolveKeyMeta(ctx context.Context, rawKey string, keyInfo *auth.KeyInfo, meta *requestAttemptMeta) {
	if meta == nil {
		return
	}
	if keyInfo != nil {
		meta.APIKeyPrefix = formatKeyPrefixDisplay(keyInfo.KeyPrefix)
		if meta.APIKeyPrefix == "" {
			meta.APIKeyPrefix = maskAPIKeyPrefix(rawKey)
		}
		if keyInfo.OwnerUser != nil {
			meta.APIKeyOwnerUser = strings.TrimSpace(*keyInfo.OwnerUser)
		}
		meta.ApplicationCode = keyInfo.ApplicationCode
		id := keyInfo.ID
		meta.LookupKeyID = &id
		meta.KeyStatus = "valid"
		return
	}
	if strings.TrimSpace(rawKey) == "" {
		meta.APIKeyPrefix = "无key"
		meta.KeyStatus = "missing"
		return
	}
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		if lookup, err := h.keyVerifier.LookupKeyMeta(ctx, rawKey); err == nil && lookup != nil {
			meta.APIKeyPrefix = formatKeyPrefixDisplay(lookup.KeyPrefix)
			if lookup.OwnerUser != nil {
				meta.APIKeyOwnerUser = strings.TrimSpace(*lookup.OwnerUser)
			}
			meta.ApplicationCode = lookup.ApplicationCode
			id := lookup.ID
			meta.LookupKeyID = &id
			meta.KeyStatus = "invalid_" + strings.TrimSpace(lookup.Status)
			if lookup.DefaultClientProfile != nil && strings.TrimSpace(*lookup.DefaultClientProfile) != "" {
				meta.ClientProfile = strings.TrimSpace(*lookup.DefaultClientProfile)
			}
			return
		}
	}
	meta.APIKeyPrefix = maskAPIKeyPrefix(rawKey)
	meta.KeyStatus = "invalid_unknown"
}

func apiKeyIDForLog(keyInfo *auth.KeyInfo, meta *requestAttemptMeta) *int {
	if keyInfo != nil {
		return apiKeyIDPtr(keyInfo)
	}
	if meta != nil && meta.LookupKeyID != nil {
		return meta.LookupKeyID
	}
	return nil
}

func applicationIDForLog(keyInfo *auth.KeyInfo) *int {
	if keyInfo == nil {
		return nil
	}
	return appID(keyInfo)
}

func enrichRequestLogFromMeta(reqLog *telemetry.RequestLogEntry, keyInfo *auth.KeyInfo, meta *requestAttemptMeta) {
	if reqLog == nil || meta == nil {
		return
	}
	if meta.APIKeyPrefix != "" {
		reqLog.APIKeyPrefix = strPtr(meta.APIKeyPrefix)
	}
	if meta.APIKeyOwnerUser != "" {
		reqLog.APIKeyOwnerUser = strPtr(meta.APIKeyOwnerUser)
	}
	if meta.ApplicationCode != "" {
		reqLog.ApplicationCode = strPtr(meta.ApplicationCode)
	}
	if reqLog.APIKeyID == nil {
		reqLog.APIKeyID = apiKeyIDForLog(keyInfo, meta)
	}
	if reqLog.ApplicationID == nil {
		reqLog.ApplicationID = applicationIDForLog(keyInfo)
	}
	if meta.ClientProfile != "" && (reqLog.ClientProfile == nil || strings.TrimSpace(*reqLog.ClientProfile) == "") {
		reqLog.ClientProfile = strPtr(meta.ClientProfile)
	}
	if meta.IdentityHash != "" && (reqLog.IdentityHash == nil || strings.TrimSpace(*reqLog.IdentityHash) == "") {
		reqLog.IdentityHash = strPtr(meta.IdentityHash)
	}
	if meta.RequestMode != "" && (reqLog.RequestMode == nil || strings.TrimSpace(*reqLog.RequestMode) == "") {
		reqLog.RequestMode = strPtr(meta.RequestMode)
	}
}

func keyMetaFromKeyInfo(keyInfo *auth.KeyInfo) (prefix, owner, appCode string) {
	if keyInfo == nil {
		return "", "", ""
	}
	prefix = formatKeyPrefixDisplay(keyInfo.KeyPrefix)
	if keyInfo.OwnerUser != nil {
		owner = strings.TrimSpace(*keyInfo.OwnerUser)
	}
	appCode = keyInfo.ApplicationCode
	return prefix, owner, appCode
}
