// hostedtask/handler.go — /v1/hosted-tasks 门面（§4.1 五端点）。
//
// 鉴权：KeyVerifier（sk-*），tenant/api_key 一律取自验证后 context（§2.4；
// 必须断言跨包 authentication.InvalidKeyError，勿复刻 session 本地副本陷阱）。
// 委托受理即校验（§9）：goal 必填、workspace_id 白名单映射（禁裸路径，
// §2.2 边界②：CheckPath 未接线 → cwd 由网关映射）、callback SSRF 受理时
// 快检、deadline 收紧到配置上限。
package hostedtask

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/internal/hostedcallback"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// KeyVerifier 与 domains/session 的抽象同形（真实现由 main.go 适配注入）。
type KeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (KeyInfo, error)
}

// KeyInfo 是验证后的 key 信息。
type KeyInfo struct {
	ID       int64
	TenantID string
}

// TaskStore 是 handler 依赖的持久层最小接口（*Store 实现；接口化便于
// 矩阵 B 的 httptest 假件）。
type TaskStore interface {
	CreateTask(ctx context.Context, in CreateInput) (*Task, bool, error)
	GetTask(ctx context.Context, tenantID, id string) (*Task, error)
	ListEvents(ctx context.Context, tenantID, taskID string, limit int) ([]Event, error)
	CancelTask(ctx context.Context, tenantID, id string) (*Task, bool, error)
}

// Config 是托管任务门面的运行配置（main.go 从 env 装配）。
type Config struct {
	// Workspaces 是 workspace_id → 宿主机绝对路径的白名单映射
	// （§4.1：不接受裸路径；§9：cwd 任意路径风险的 P0 对策）。
	Workspaces map[string]string
	// Model 是 dispatch 缺省模型偏好（payload.model；companion 侧 PI_MODEL）。
	Model string
	// TimeoutSeconds 是单次 dispatch 的执行超时（payload.timeout_ms）。
	TimeoutSeconds int
	// DefaultDeadline / MaxDeadline 收紧任务期限。
	DefaultDeadline time.Duration
	MaxDeadline     time.Duration
	// CallbackAllowlist 是回调 SSRF 受理校验的显式内网放行清单。
	CallbackAllowlist []string
}

// Handler 是 /v1/hosted-tasks 的 http.Handler。
type Handler struct {
	store   TaskStore
	auth    KeyVerifier
	cfg     Config
	keyring *secret.Keyring // callback url/secret 的 AES-GCM 加密
	logger  *slog.Logger
	now     func() time.Time
	// accCancel 是取消的收尾钩子（本地终态抢占成功后尽力补发 ACC cancel，
	// requested 语义；由 main.go 注入 reconciler.CancelOnACC）。可 nil。
	accCancel func(ctx context.Context, commandID string)
}

// NewHandler 构造门面。auth 为 nil 或未启用时放行（与 session handler 一致
// 的开发姿态）；keyring 为 nil 时带回调的委托返回 503（fail-closed，不落
// 明文 secret）。
func NewHandler(store TaskStore, auth KeyVerifier, cfg Config, keyring *secret.Keyring, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.DefaultDeadline <= 0 {
		cfg.DefaultDeadline = time.Hour
	}
	if cfg.MaxDeadline <= 0 {
		cfg.MaxDeadline = 24 * time.Hour
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 3600
	}
	return &Handler{store: store, auth: auth, cfg: cfg, keyring: keyring, logger: logger, now: time.Now}
}

// SetTestClock 覆盖时间源（测试用）。
func (h *Handler) SetTestClock(now func() time.Time) {
	if now != nil {
		h.now = now
	}
}

// SetACCCancel 安装取消收尾钩子（§3.2：尽力补发 ACC cancel）。
func (h *Handler) SetACCCancel(fn func(ctx context.Context, commandID string)) {
	h.accCancel = fn
}

// ServeHTTP 路由：
//
//	POST /v1/hosted-tasks                委托
//	GET  /v1/hosted-tasks/{id}           状态+事件时间线
//	GET  /v1/hosted-tasks/{id}/result    结果（running→202+Retry-After）
//	POST /v1/hosted-tasks/{id}/cancel    取消（P0=请求受理语义）
//	POST /v1/hosted-tasks/{id}/recall    P0 显式 501（§4.1/§6.2）
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	r = r.WithContext(ctx)

	path := strings.Trim(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "v1/hosted-tasks")

	switch {
	case path == "v1/hosted-tasks":
		if r.Method != http.MethodPost {
			h.writeError(w, http.StatusMethodNotAllowed, "", "method not allowed", "hosted_task_error", "METHOD_NOT_ALLOWED")
			return
		}
		h.create(w, r)
	case strings.HasPrefix(rest, "/"):
		id := strings.Trim(rest, "/")
		if id == "" {
			h.writeError(w, http.StatusNotFound, "", "not found", "hosted_task_error", "NOT_FOUND")
			return
		}
		// 拆 {id}[/action]
		parts := strings.SplitN(id, "/", 2)
		taskID := parts[0]
		action := ""
		if len(parts) == 2 {
			action = parts[1]
		}
		if taskID == "" {
			h.writeError(w, http.StatusNotFound, "", "not found", "hosted_task_error", "NOT_FOUND")
			return
		}
		switch action {
		case "":
			if r.Method != http.MethodGet {
				h.writeError(w, http.StatusMethodNotAllowed, "", "method not allowed", "hosted_task_error", "METHOD_NOT_ALLOWED")
				return
			}
			h.getStatus(w, r, taskID)
		case "result":
			if r.Method != http.MethodGet {
				h.writeError(w, http.StatusMethodNotAllowed, "", "method not allowed", "hosted_task_error", "METHOD_NOT_ALLOWED")
				return
			}
			h.getResult(w, r, taskID)
		case "cancel":
			if r.Method != http.MethodPost {
				h.writeError(w, http.StatusMethodNotAllowed, "", "method not allowed", "hosted_task_error", "METHOD_NOT_ALLOWED")
				return
			}
			h.cancel(w, r, taskID)
		case "recall":
			// §4.1：P0 显式不支持（501），P1 实现（§3.3 handoff 包）。
			h.writeError(w, http.StatusNotImplemented, "", "recall is not supported in P0; planned for P1 (handoff packet)", "hosted_task_error", "RECALL_NOT_IMPLEMENTED")
		default:
			h.writeError(w, http.StatusNotFound, "", "not found", "hosted_task_error", "NOT_FOUND")
		}
	default:
		h.writeError(w, http.StatusNotFound, "", "not found", "hosted_task_error", "NOT_FOUND")
	}
}

// ─── 鉴权（§2.4：跨包 InvalidKeyError 断言）───────────────────────────────

func extractBearerToken(r *http.Request) string {
	if authHdr := r.Header.Get("Authorization"); authHdr != "" {
		if strings.HasPrefix(authHdr, "Bearer ") {
			return strings.TrimPrefix(authHdr, "Bearer ")
		}
		if strings.HasPrefix(authHdr, "bearer ") {
			return strings.TrimPrefix(authHdr, "bearer ")
		}
	}
	return r.Header.Get("x-api-key")
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if h.auth == nil || !h.auth.Enabled() {
		return r.Context(), true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		h.writeError(w, http.StatusUnauthorized, "", "Missing API key", "authentication_error", "MISSING_KEY")
		return nil, false
	}
	ki, err := h.auth.Verify(r.Context(), rawKey)
	if err != nil {
		// 必须断言 authentication.InvalidKeyError（跨包类型）；本地副本
		// 断言永不匹配 → 503 陷阱（§0/F2 同源教训，session handler.go:122）。
		if _, ok := err.(*authentication.InvalidKeyError); ok {
			h.writeError(w, http.StatusUnauthorized, "", "Invalid or expired API key", "authentication_error", "INVALID_KEY")
		} else {
			h.writeError(w, http.StatusServiceUnavailable, "", "Authentication service temporarily unavailable", "server_error", "AUTH_UNAVAILABLE")
		}
		return nil, false
	}
	ctx := context.WithValue(r.Context(), ctxKeyAPIKeyID{}, ki.ID)
	ctx = context.WithValue(ctx, ctxKeyTenantID{}, ki.TenantID)
	return ctx, true
}

type ctxKeyAPIKeyID struct{}
type ctxKeyTenantID struct{}

func apiKeyIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(ctxKeyAPIKeyID{}).(int64)
	return id
}

func tenantIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyTenantID{}).(string)
	return id
}

// ─── POST /v1/hosted-tasks（§3.1 ①）───────────────────────────────────────

type createRequest struct {
	Goal     string `json:"goal"`
	DoneWhen string `json:"done_when"`
	Context  struct {
		Summary   string         `json:"summary"`
		Memora    map[string]any `json:"memora"`
		Artifacts []any          `json:"artifacts"`
	} `json:"context"`
	Environment struct {
		WorkspaceID    string `json:"workspace_id"`
		ModelPref      string `json:"model_pref"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	} `json:"environment"`
	Callback struct {
		URL string `json:"url"`
	} `json:"callback"`
	Limits struct {
		DeadlineSeconds int `json:"deadline_seconds"`
		BudgetCredits   any `json:"budget_credits"` // P0 校验记录，P1 熔断（§6.2）
	} `json:"limits"`
}

var workspaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	idem := r.Header.Get("Idempotency-Key")
	if l := len(idem); l < 16 || l > 256 {
		h.writeError(w, http.StatusBadRequest, "", "Idempotency-Key header required (16-256 chars)", "hosted_task_error", "MISSING_IDEMPOTENCY_KEY")
		return
	}

	var body createRequest
	if err := jsonbody.DecodeRequest(r, &body, jsonbody.MaxRequiredBody, true); err != nil {
		h.writeError(w, http.StatusBadRequest, "", "invalid request body: "+err.Error(), "hosted_task_error", "INVALID_REQUEST")
		return
	}
	if strings.TrimSpace(body.Goal) == "" {
		h.writeError(w, http.StatusBadRequest, "", "goal is required", "hosted_task_error", "MISSING_GOAL")
		return
	}

	// workspace 白名单映射（§4.1：不接受裸路径，P0 强制必填）。
	if body.Environment.WorkspaceID == "" {
		h.writeError(w, http.StatusBadRequest, "", "environment.workspace_id is required (no bare paths; must match gateway allowlist)", "hosted_task_error", "MISSING_WORKSPACE_ID")
		return
	}
	if !workspaceIDPattern.MatchString(body.Environment.WorkspaceID) {
		h.writeError(w, http.StatusBadRequest, "", "environment.workspace_id must match [a-zA-Z0-9_-]{1,64}", "hosted_task_error", "INVALID_WORKSPACE_ID")
		return
	}
	cwd, ok := h.cfg.Workspaces[body.Environment.WorkspaceID]
	if !ok {
		h.writeError(w, http.StatusBadRequest, "", "unknown environment.workspace_id (not in gateway allowlist)", "hosted_task_error", "UNKNOWN_WORKSPACE")
		return
	}
	env := map[string]any{
		"workspace_id": body.Environment.WorkspaceID,
		"cwd":          cwd, // 白名单映射结果；裸路径永不受理
	}
	if body.Environment.ModelPref != "" {
		env["model"] = body.Environment.ModelPref
	} else if h.cfg.Model != "" {
		env["model"] = h.cfg.Model
	}
	timeout := body.Environment.TimeoutSeconds
	if timeout <= 0 {
		timeout = h.cfg.TimeoutSeconds
	}
	env["timeout_seconds"] = timeout

	// callback SSRF 受理校验 + 加密落库准备。
	var cbURLEnc, cbSecEnc, cbHash string
	if raw := strings.TrimSpace(body.Callback.URL); raw != "" {
		if err := hostedcallback.ValidateURL(raw, h.cfg.CallbackAllowlist); err != nil {
			h.writeError(w, http.StatusBadRequest, "", err.Error(), "hosted_task_error", "INVALID_CALLBACK_URL")
			return
		}
		if h.keyring == nil {
			h.writeError(w, http.StatusServiceUnavailable, "", "callback storage unavailable (encryption keyring not configured)", "server_error", "CALLBACK_KEYRING_UNAVAILABLE")
			return
		}
		enc, err := secret.EncryptAESGCM([]byte(raw), h.keyring)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "", "failed to secure callback url", "server_error", "CALLBACK_ENCRYPT_FAILED")
			return
		}
		cbSecret := randomSecret()
		secEnc, err := secret.EncryptAESGCM([]byte(cbSecret), h.keyring)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "", "failed to secure callback secret", "server_error", "CALLBACK_ENCRYPT_FAILED")
			return
		}
		cbURLEnc, cbSecEnc = enc, secEnc
		cbHash = hostedcallback.HostHash(raw)
	}

	// deadline 收紧（§4.1 limits.deadline_seconds）。
	deadline := h.cfg.DefaultDeadline
	if body.Limits.DeadlineSeconds > 0 {
		deadline = time.Duration(body.Limits.DeadlineSeconds) * time.Second
	}
	if deadline > h.cfg.MaxDeadline {
		deadline = h.cfg.MaxDeadline
	}
	if body.Limits.BudgetCredits != nil {
		// P0：仅校验记录，不熔断（§6.2）。
		env["budget_credits"] = body.Limits.BudgetCredits
	}

	contextMap := map[string]any{}
	if body.Context.Summary != "" {
		contextMap["summary"] = body.Context.Summary
	}
	if len(body.Context.Memora) > 0 {
		contextMap["memora"] = body.Context.Memora
	}
	if len(body.Context.Artifacts) > 0 {
		contextMap["artifacts"] = body.Context.Artifacts
	}

	in := CreateInput{
		TenantID:       tenantIDFrom(r.Context()),
		APIKeyID:       apiKeyIDFrom(r.Context()),
		Goal:           strings.TrimSpace(body.Goal),
		DoneWhen:       strings.TrimSpace(body.DoneWhen),
		Context:        contextMap,
		Environment:    env,
		CallbackURLEnc: cbURLEnc,
		CallbackSecEnc: cbSecEnc,
		CallbackHash:   cbHash,
		IdempotencyKey: idem,
		RequestHash: HashRequest(body.Goal, body.DoneWhen, contextMap, env,
			strings.TrimSpace(body.Callback.URL)),
		Deadline: h.now().UTC().Add(deadline),
	}

	task, created, err := h.store.CreateTask(r.Context(), in)
	if err != nil {
		if err == ErrIdempotencyConflict {
			h.writeError(w, http.StatusConflict, "", "idempotency key reused with a different body", "hosted_task_error", "IDEMPOTENCY_CONFLICT")
			return
		}
		h.logger.Error("hostedtask: create failed", "error", err)
		h.writeError(w, http.StatusInternalServerError, "", "failed to create hosted task", "server_error", "HOSTED_TASK_CREATE_FAILED")
		return
	}

	status := http.StatusAccepted
	if !created {
		// 同键同体重放 → 200 返回原任务（§4.1）。
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"hosted_task_id": task.ID,
		"status_url":     "/v1/hosted-tasks/" + task.ID,
		"status":         task.Status.APIStatus(),
		"replayed":       !created,
	})
}

func randomSecret() string {
	var raw [32]byte
	if _, err := crypto_rand.Read(raw[:]); err != nil {
		// 加密随机源失败属进程级异常；回调 secret 退化为拒绝服务而非弱密钥。
		panic("hostedtask: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}

// ─── GET /v1/hosted-tasks/{id}（状态 + 事件时间线）────────────────────────

func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request, taskID string) {
	tenantID := tenantIDFrom(r.Context())
	task, err := h.store.GetTask(r.Context(), tenantID, taskID)
	if err != nil {
		h.readErr(w, err)
		return
	}
	events, err := h.store.ListEvents(r.Context(), tenantID, taskID, 200)
	if err != nil {
		h.logger.Error("hostedtask: list events failed", "task_id", taskID, "error", err)
		h.writeError(w, http.StatusInternalServerError, "", "failed to load events", "server_error", "HOSTED_TASK_EVENTS_FAILED")
		return
	}
	view := task.View()
	evs := make([]map[string]any, 0, len(events))
	for _, e := range events {
		evs = append(evs, map[string]any{
			"seq":        e.Seq,
			"event_type": string(e.Type),
			"payload":    e.Payload,
			"created_at": e.CreatedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"task":   view,
		"events": evs,
	})
}

// ─── GET /v1/hosted-tasks/{id}/result（§4.1：PG 权威）─────────────────────

func (h *Handler) getResult(w http.ResponseWriter, r *http.Request, taskID string) {
	tenantID := tenantIDFrom(r.Context())
	task, err := h.store.GetTask(r.Context(), tenantID, taskID)
	if err != nil {
		h.readErr(w, err)
		return
	}
	if !task.Status.Terminal() {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hosted_task_id": task.ID,
			"status":         task.Status.APIStatus(),
			"retry_after":    5,
		})
		return
	}

	res := task.Result
	if res == nil {
		res = map[string]any{}
	}
	// needs_review → P0 暴露 failed(unknown)（§3.1），事件时间线留证据。
	outcome, _ := res["outcome"].(string)
	if task.Status == StatusNeedsReview && outcome == "" {
		outcome = "unknown"
	}
	body := map[string]any{
		"hosted_task_id": task.ID,
		"status":         task.Status.APIStatus(),
		"outcome":        outcome,
		"gw_session_id":  task.GwSessionID,
		"result_version": task.ResultVersion,
		"completed_at":   task.CompletedAt,
	}
	for _, k := range []string{"summary", "output", "artifact_refs", "memora", "cost", "pi_session_ref", "content_hash", "stop_reason"} {
		if v, ok := res[k]; ok {
			body[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// ─── POST /v1/hosted-tasks/{id}/cancel（§3.2）─────────────────────────────

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request, taskID string) {
	tenantID := tenantIDFrom(r.Context())
	task, won, err := h.store.CancelTask(r.Context(), tenantID, taskID)
	if err != nil {
		h.readErr(w, err)
		return
	}
	if !won {
		// 终态后取消 → 409（§4.1）。
		h.writeError(w, http.StatusConflict, "", "task already in terminal state: "+task.Status.APIStatus(), "hosted_task_error", "ALREADY_TERMINAL")
		return
	}
	// P0 语义（§3.2/§9）：取消=本地终态抢占 + 尽力 ACC cancel(requested)。
	// delivered/effective 依赖 companion CommandCanceler（P1 跨仓库修复）。
	if h.accCancel != nil && task.AccCommandID != "" {
		cmdID := task.AccCommandID
		go h.accCancel(context.WithoutCancel(r.Context()), cmdID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"hosted_task_id": task.ID,
		"cancel_status":  "requested",
	})
}

// ─── 公共 ─────────────────────────────────────────────────────────────────

func (h *Handler) readErr(w http.ResponseWriter, err error) {
	if err == ErrNotFound {
		// 跨租户/不存在统一 404（§4.1）。
		h.writeError(w, http.StatusNotFound, "", "hosted task not found", "hosted_task_error", "NOT_FOUND")
		return
	}
	h.logger.Error("hostedtask: read failed", "error", err)
	h.writeError(w, http.StatusInternalServerError, "", "internal error", "server_error", "HOSTED_TASK_INTERNAL")
}

func (h *Handler) writeError(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    errType,
			"code":    code,
		},
	})
}
