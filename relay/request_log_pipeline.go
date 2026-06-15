package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/sessions"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

// jsonMarshal is a local alias used by auto_route.go to avoid pulling
// encoding/json into the test hot path. (encoding/json is already imported
// transitively through other helpers.)
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// RequestLogContext caches request facts across handler lifecycle stages
// (auth, body read, routing, upstream, response) so every exit path emits
// a complete request_logs row with user/application correlation.
type RequestLogContext struct {
	handler   *ChatHandler
	RequestID string
	StartTime time.Time
	Request   *http.Request
	Session   *sessions.Session

	KeyInfo       *auth.KeyInfo
	Body          []byte
	ClientModel   string
	OutboundModel string
	EndUser       string
	ProviderID    *int
	CredentialID  *int
	ResponseBody  []byte

	// v2.0 auto-route fields (populated when model="auto" was used)
	IsAutoRequest  bool
	TaskType       string
	WorkType       string // X-Gw-Work-Type header (ACC work type key)
	AutoProfile    string
	AutoDecision   []byte // serialised autoRouteDecision JSON
	AutoConfidence float64

	ErrCode string
	ErrMsg  string

	meta   requestAttemptMeta
	logged bool
}

func (c *RequestLogContext) SetError(code, msg string) {
	if c == nil {
		return
	}
	c.ErrCode = code
	c.ErrMsg = msg
}

// NewRequestLogContext starts a per-request log cache. Call EnsureCaptured early.
func (h *ChatHandler) NewRequestLogContext(r *http.Request, requestID string, start time.Time) *RequestLogContext {
	return &RequestLogContext{
		handler:   h,
		RequestID: requestID,
		StartTime: start,
		Request:   r,
	}
}

func (c *RequestLogContext) SetSession(session *sessions.Session) {
	c.Session = session
}

func (c *RequestLogContext) SetKey(keyInfo *auth.KeyInfo) {
	c.KeyInfo = keyInfo
	c.refreshMeta()
}

func (c *RequestLogContext) SetClientModel(model string) {
	if strings.TrimSpace(model) != "" {
		c.ClientModel = strings.TrimSpace(model)
	}
}

// SetWorkType stores the X-Gw-Work-Type header for request_logs.work_type.
func (c *RequestLogContext) SetWorkType(wt string) {
	c.WorkType = strings.TrimSpace(wt)
}

func (c *RequestLogContext) SetOutboundModel(model string) {
	if strings.TrimSpace(model) != "" {
		c.OutboundModel = strings.TrimSpace(model)
	}
}

func (c *RequestLogContext) SetRoute(providerID, credentialID *int) {
	c.ProviderID = providerID
	c.CredentialID = credentialID
}

func applyWorkTypeField(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil || c.WorkType == "" {
		return
	}
	entry.WorkType = strPtr(c.WorkType)
}

func applyAutoRouteFields(entry *telemetry.RequestLogEntry, c *RequestLogContext) {
	if entry == nil || c == nil {
		return
	}
	applyWorkTypeField(entry, c)
	if !c.IsAutoRequest {
		return
	}
	entry.IsAutoRequest = boolPtr(true)
	if c.TaskType != "" {
		entry.TaskType = strPtr(c.TaskType)
	}
	if c.AutoProfile != "" {
		entry.AutoProfile = strPtr(c.AutoProfile)
	}
	if len(c.AutoDecision) > 0 {
		v := string(c.AutoDecision)
		entry.AutoDecision = &v

		// P7.2: also populate the 4 promoted columns from the
		// JSON payload. Parses the same wire format that
		// relay/auto_route.go emits (autoRouteDecision) so the
		// columns track the JSONB field exactly. Tolerates partial
		// JSON or unexpected shapes — missing fields stay NULL.
		var wire autoRouteDecision
		if err := json.Unmarshal(c.AutoDecision, &wire); err == nil {
			if wire.TaskType != "" {
				entry.TaskTypeChosen = strPtr(wire.TaskType)
			}
			if wire.Confidence > 0 {
				c := wire.Confidence
				entry.ConfidenceNum = &c
			}
			if wire.ChosenModel != "" {
				entry.ModelChosen = strPtr(wire.ChosenModel)
			}
		}
	}
	if c.AutoConfidence > 0 {
		conf := c.AutoConfidence
		entry.AutoConfidence = &conf
	}
}

func (c *RequestLogContext) SetResponseBody(body []byte) {
	if len(body) > 0 {
		c.ResponseBody = body
	}
}

// EnsureCaptured buffers the JSON body (restores r.Body) and fills key/identity meta.
func (c *RequestLogContext) EnsureCaptured() {
	if c == nil || c.Request == nil {
		return
	}
	_ = ensureRequestBodyBuffered(c.Request, &c.Body, &c.ClientModel)
	c.refreshMeta()
}

func (c *RequestLogContext) CapturePartialBody(body []byte) {
	capturePartialBodyOnReadError(body, &c.Body, &c.ClientModel)
}

func (c *RequestLogContext) refreshMeta() {
	if c == nil || c.handler == nil || c.Request == nil {
		return
	}
	c.handler.fillAttemptMeta(c.Request, c.KeyInfo, &c.meta)
}

func (c *RequestLogContext) SessionTask() (sessionID, taskID string) {
	return gwSessionTaskFromRequest(c.Request, c.Session)
}

func (c *RequestLogContext) LatencyMs() int {
	if c == nil || c.StartTime.IsZero() {
		return 0
	}
	return int(time.Since(c.StartTime).Milliseconds())
}

func (c *RequestLogContext) RequestMode() string {
	if c.meta.RequestMode != "" {
		return c.meta.RequestMode
	}
	if c.Request != nil {
		return requestModeFromPath(c.Request.URL.Path)
	}
	return "chat"
}

func (c *RequestLogContext) MarkLogged() { c.logged = true }
func (c *RequestLogContext) IsLogged() bool {
	return c != nil && c.logged
}

// SetAutoDecision stores the auto-route decision for persistence in
// request_logs.auto_decision JSONB. Called from relay/handler.go when
// model="auto" was resolved.
func (c *RequestLogContext) SetAutoDecision(wire *autoRouteDecision) {
	if c == nil {
		return
	}
	c.IsAutoRequest = true
	if wire == nil {
		return
	}
	c.TaskType = wire.TaskType
	c.AutoProfile = wire.Profile
	c.AutoConfidence = wire.Confidence
	b, err := jsonMarshal(wire)
	if err == nil {
		c.AutoDecision = b
	}
}

// BuildFailureEntry assembles a failure row from cached context + exit metadata.
func (c *RequestLogContext) BuildFailureEntry(errCode, errMessage string, providerID, credentialID *int) *telemetry.RequestLogEntry {
	if c == nil {
		return nil
	}
	if providerID != nil {
		c.ProviderID = providerID
	}
	if credentialID != nil {
		c.CredentialID = credentialID
	}
	c.refreshMeta()

	clientModel := c.ClientModel
	outboundModel := c.OutboundModel
	if clientModel == "" && len(c.Body) > 0 {
		clientModel = extractModelFromBody(c.Body)
	}

	gwSessionID, gwTaskID := c.SessionTask()
	clientProfile := c.meta.ClientProfile
	identityHash := c.meta.IdentityHash
	if c.Request != nil && (clientProfile == "" || identityHash == "") {
		cp, ih := failedRequestIdentity(c.Request, c.KeyInfo)
		if clientProfile == "" {
			clientProfile = cp
		}
		if identityHash == "" {
			identityHash = ih
		}
	}

	var requestBodyText *string
	if len(c.Body) > 0 {
		v := string(c.Body)
		requestBodyText = &v
	}
	var responseBodyText *string
	if len(c.ResponseBody) > 0 {
		v := string(c.ResponseBody)
		responseBodyText = &v
	}

	latency := c.LatencyMs()
	tenantID := "default"
	apiKeyID := apiKeyIDForLog(c.KeyInfo, &c.meta)
	var applicationID *int
	if c.KeyInfo != nil {
		tenantID = c.KeyInfo.TenantID
		applicationID = appID(c.KeyInfo)
	}

	var requestPreviewPtr *string
	if preview := requestPreview(c.Body); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	var responsePreviewPtr *string
	if preview := responsePreview(c.ResponseBody); preview != "" {
		responsePreviewPtr = strPtr(preview)
	}

	detailCode := mapGatewayErrorToDetail(errCode)
	failureStage := classifyFailureStage(errCode)

	reqLog := &telemetry.RequestLogEntry{
		RequestID:         c.RequestID,
		TenantID:          tenantID,
		ApplicationID:     applicationID,
		APIKeyID:          apiKeyID,
		ClientModel:       strPtr(clientModel),
		OutboundModel:     strPtr(outboundModel),
		ProviderID:        c.ProviderID,
		CredentialID:      c.CredentialID,
		ClientProfile:     strPtr(clientProfile),
		IdentityHash:      strPtr(identityHash),
		RequestMode:       strPtr(c.RequestMode()),
		GwSessionID:       strPtr(gwSessionID),
		GwTaskID:          strPtr(gwTaskID),
		LatencyMs:         &latency,
		Success:           false,
		RequestStatus:     strPtr(telemetry.RequestStatusFailure),
		ErrorKind:         strPtr(errCode),
		FailureStage:      strPtr(failureStage),
		FailureDetailCode: strPtr(detailCode),
		RequestBody:       requestBodyText,
		RequestPreview:    requestPreviewPtr,
		ResponseBody:      responseBodyText,
		ResponsePreview:   responsePreviewPtr,
	}
	enrichRequestLogFromMeta(reqLog, c.KeyInfo, &c.meta)
	applyAutoRouteFields(reqLog, c)
	return reqLog
}

// EmitFailure writes/updates request_logs for a non-success exit.
func (c *RequestLogContext) EmitFailure(errCode, errMessage string, providerID, credentialID *int) {
	if c == nil || c.handler == nil {
		return
	}
	reqLog := c.BuildFailureEntry(errCode, errMessage, providerID, credentialID)
	if reqLog == nil {
		return
	}
	if c.handler.requestLogHook != nil {
		c.handler.requestLogHook(reqLog)
	}
	if c.handler.telemetryClient != nil && c.handler.telemetryClient.Enabled() {
		c.handler.telemetryClient.EmitRequestLogUpdate(reqLog)
	}
	c.logged = true
}

// failAndMark emits a failure row immediately (explicit exit paths).
func (c *RequestLogContext) failAndMark(errCode, errMsg string, providerID, credentialID *int) {
	c.EmitFailure(errCode, errMsg, providerID, credentialID)
}
