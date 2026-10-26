package client

// session_analysis.go —— memora **v2 会话分析投递**端点的客户端
// （POST /api/v2/session-analysis）。
//
// # 契约权威
//
//	memora main 2a8aff3：
//	  · memora/docs/integration/2026-10-20-session-analysis-contract.md（v1.1）
//	  · memora/internal/sessionanalysis/payload.go（载荷形态 + DeriveDispatchID）
//	  · memora/internal/v2/session_analysis.go（handler 的校验与错误码）
//	  · memora/internal/middleware/v2jwt.go（service JWT 的必需 claim）
//
// ★ 本文件按 **v1.1** 写。v1.0 → v1.1 的五处变化，每一处都改变了**生产方**
//   必须做的事（逐条对照见下），不是「服务端兼容性」问题：
//
//	① work_types：从 []string 改成**结构化对象**
//	   {value,source,confidence}（网关的 []Classification 逐字绑定）；
//	   memora 侧容忍裸字符串只是给老生产方留的兼容位。
//	② 新增 **features** 字段；**tags 的语义收紧**为「网关 session_tags 的行」。
//	   ⇒ v1.0 把网关的 features 静默落进 tags **是缺陷**（贴错标签），
//	     本文件把两者**分字段**发送。
//	③ dispatch_id 改为**由幂等键头推导**（网关的 sessionmeta.Result 没有这个
//	   字段，它有 scoped_session_id 与 status）；body 里的 dispatch_id 变成
//	   可选的「必须与头一致」的一致性声明。
//	④ project_id 明确为 **resolve-or-create**（调用方用自己的 id 空间，
//	   原样使用；已被别的租户占用 ⇒ 409 project_scope_conflict）。
//	⑤ 新增 **project.unclassified** 显式标记（「判定为未归类」与
//	   「没报项目信号」可区分）。
//
// # 这条链与既有 /api/memory/add 有什么不同
//
// 1. **端点**：/api/v2/session-analysis，不是 /api/memory/add。
//    memora 的 v1 端点**不收**结构化分析产出。
// 2. **鉴权**：v2 组是 **fail-closed service JWT**（HS256，必需
//    iss/aud/sub/tenant_id/scope/exp），写 scope 必须含 memory.write。
//    ★ 另需 **actor_id**：它是 resolve-or-create 建出来的项目的 owner
//      （memora/internal/v2/session_analysis.go 的 EnsureProjectScope(ctx,
//      tenantID, projectID, identity.ActorID)）⇒ 必须**稳定**，否则同一
//      项目可能被反复判成「别人建的」。
// 3. **幂等**：必需头 `Idempotency-Key`，值必须是
//    `<dispatch_id>-<analysis_kind>`；不一致 ⇒ 422 idempotency_key_mismatch。
//    同一键重放 ⇒ **409 idempotency_key_reuse** 且**不产生任何新行**。
// 4. **项目作用域**：**body 顶层**的 `project_id`（必填）。
//    ★ **不要**发 `X-Project-ID` 头：memora 的 ProjectScope 中间件在收到
//      该头时会**挂上项目作用域**，进而让 ProjectAuthz 生效
//      （memora/internal/middleware/project_authz.go:76-104：缺 authorizer
//      时 **503**）。而 session-analysis 这条路由的 project_id 是
//      **body 字段**（handler 自己校验 + 自己判 ACL），契约文档的必填头
//      清单里也**没有** X-Project-ID ⇒ 发它只会把一条本来能通的路由变成 503。
// 5. **correlation**：必需头 `X-Correlation-ID`。
//
// # 失败语义（硬要求）
//
// memora 宕机 / 超时 / 5xx ⇒ **记日志并吞掉**，绝不冒泡成请求错误。
// 这条链跑在网关的正常请求路径上（会话分析产出点），一次 memora 故障
// 不该让用户的请求失败。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SessionAnalysisPath 是 memora 的会话分析落地域路径。
const SessionAnalysisPath = "/api/v2/session-analysis"

// 会话分析推送的环境变量。
const (
	// SessionAnalysisPushEnv 是**总开关**。未显式打开 ⇒ 整个功能关闭
	//（零 HTTP 调用、零日志）。
	SessionAnalysisPushEnv = "LLM_GATEWAY_MEMORA_SESSION_ANALYSIS_PUSH"
	// SessionAnalysisJWTSecretEnv 是与 memora 共享的 service JWT 签名密钥
	//（HS256）。**没配 ⇒ 功能关闭**（fail-closed：没有凭据就不去敲门）。
	SessionAnalysisJWTSecretEnv = "LLM_GATEWAY_MEMORA_V2_JWT_SECRET"
	// SessionAnalysisJWTIssuerEnv / SessionAnalysisJWTAudienceEnv 对齐
	// memora 的 JWT 校验（iss 必须匹配、aud 必须匹配）。
	SessionAnalysisJWTIssuerEnv   = "LLM_GATEWAY_MEMORA_V2_JWT_ISSUER"
	SessionAnalysisJWTAudienceEnv = "LLM_GATEWAY_MEMORA_V2_JWT_AUDIENCE"
	// SessionAnalysisTenantEnv 是 JWT 的 tenant_id claim（memora 只从 claim
	// 派生租户，body/header 里的同名字段不参与认证）。
	SessionAnalysisTenantEnv = "LLM_GATEWAY_MEMORA_TENANT_ID"
	// SessionAnalysisActorEnv 是 JWT 的 actor_id claim（**稳定**的项目 owner）。
	// 空 ⇒ 回落成 issuer（见 serviceToken）。
	SessionAnalysisActorEnv = "LLM_GATEWAY_MEMORA_ACTOR_ID"
)

// 默认值。
const (
	defaultSessionAnalysisIssuer   = "llm-gateway"
	defaultSessionAnalysisAudience = "memora"
	defaultSessionAnalysisTenant   = "default"
	// sessionAnalysisScope 是写 scope。memora 的 writeScopeAllowed 接受
	// memory.write / memory.* / admin ⇒ 这里给**最小**的那一个。
	sessionAnalysisScope = "memory.write"
	// sessionAnalysisSeenTTL 是「已投递键」的保留时长：它只是**进程内**
	// 去重，跨进程/重启的兜底是 memora 侧的 409。
	sessionAnalysisSeenTTL = 6 * time.Hour
	// sessionAnalysisSeenMaxCap 是去重表上限（防无界增长）。
	sessionAnalysisSeenMaxCap = 100000
	// sessionAnalysisMaxProjectIDLen 对齐 memora 的 auth.maxProjectIDLength
	//（memora/internal/auth/project.go:8）—— 超长会被 ValidateProjectID 拒掉
	//（400 invalid project_id）。
	sessionAnalysisMaxProjectIDLen = 100
)

// memora 的错误码（契约文档 §6）。
const (
	// sessionAnalysisCodeIdempotencyReuse 是「同一键已落过域」⇒ **成功**
	//（幂等重放，不产生新行）。
	sessionAnalysisCodeIdempotencyReuse = "idempotency_key_reuse"
	// sessionAnalysisCodeProjectConflict 是「该 project id 已被别的租户
	// 占用」⇒ **错误**（不能静默当成功）。
	sessionAnalysisCodeProjectConflict = "project_scope_conflict"
)

// SessionAnalysisPayload 是网关侧的结构化分析产出投影。
//
// 字段与 memora 的 sessionanalysis.AnalysisPayload **逐项对应**
// （memora/internal/sessionanalysis/payload.go）。
//
// ★ 只放网关**真的产出**的东西 —— 不臆造：网关的 sessionmeta.Result 没有
//
//	summary / outcome / key_points，那几个字段就留空（memora 侧 Validate()
//	要求 title/summary/outcome/key_points 至少有一个非空，空载荷客户端直接不发）。
//
// ★ **features 与 tags 是两个字段**（契约 v1.1 第 ② 条）：features 来自
//
//	sessionmeta.Result.Features（会话级"特征"），tags 来自网关 session_tags
//	表的行（tag_key/tag_value）。把它们混为一谈就是 memora 二轮审计点名的
//	「静默贴错标签」缺陷。
type SessionAnalysisPayload struct {
	// DispatchID 是**推导**出来的：`<scoped_session_id>-<status>`。
	// 网关的 sessionmeta.Result 没有这个字段（契约 §2）⇒ 本客户端用
	// 「它实际有的标识」拼出来，并放进幂等键头。
	DispatchID      string                  `json:"dispatch_id,omitempty"`
	AnalysisKind    string                  `json:"analysis_kind"`
	SourceSessionID string                  `json:"source_session_id,omitempty"`
	SchemaVersion   string                  `json:"schema_version,omitempty"`
	Status          string                  `json:"status,omitempty"`
	Title           string                  `json:"title,omitempty"`
	Summary         string                  `json:"summary,omitempty"`
	Intent          string                  `json:"intent,omitempty"`
	Outcome         string                  `json:"outcome,omitempty"`
	KeyPoints       []string                `json:"key_points,omitempty"`
	OpenQuestions   []string                `json:"open_questions,omitempty"`
	Metrics         *SessionAnalysisMetrics `json:"metrics,omitempty"`
	Project         *SessionAnalysisProject `json:"project,omitempty"`
	Task            *SessionAnalysisTask    `json:"task,omitempty"`
	// Grounding 是 memora 侧的历史命名；Evidence 是网关的字段名。两者同构同义，
	// memora 会合并去重（payload.go 的 Normalized）⇒ 本客户端**只发 evidence**。
	Grounding []SessionAnalysisEvidence `json:"grounding,omitempty"`
	Evidence  []SessionAnalysisEvidence `json:"evidence,omitempty"`
	// Tags 是网关 session_tags 表的行（key=tag_key，value=tag_value）。
	Tags []SessionAnalysisTag `json:"tags,omitempty"`
	// Features 是网关 sessionmeta.Result.Features（key/value/source/confidence）。
	Features []SessionAnalysisTag `json:"features,omitempty"`
	// WorkTypes 是网关 sessionmeta.Result.WorkTypes
	//（[]Classification：value/source/confidence）—— **结构化**，不是 []string。
	WorkTypes  []SessionAnalysisWorkType  `json:"work_types,omitempty"`
	Agent      *SessionAnalysisAgent      `json:"agent,omitempty"`
	Client     *SessionAnalysisClient     `json:"client,omitempty"`
	Expert     *SessionAnalysisExpert     `json:"expert,omitempty"`
	Provenance *SessionAnalysisProvenance `json:"provenance,omitempty"`
	InputHash  string                     `json:"input_hash,omitempty"`
}

// SessionAnalysisRequest 是投递体：body **顶层**多一个 project_id。
//
// ⚠ memora 的 handler 读的是 `req.ProjectID`（body 顶层字段），
//
//	不是头 ⇒ 项目作用域必须放在这里。
type SessionAnalysisRequest struct {
	ProjectID string `json:"project_id"`
	SessionAnalysisPayload
}

// SessionAnalysisMetrics 是会话计量（可选；网关的 sessionmeta.Result 没有）。
type SessionAnalysisMetrics struct {
	RequestCount      int     `json:"request_count,omitempty"`
	ErrorCount        int     `json:"error_count,omitempty"`
	PromptTokens      int     `json:"prompt_tokens,omitempty"`
	CompletionTokens  int     `json:"completion_tokens,omitempty"`
	TotalCostUSD      float64 `json:"total_cost_usd,omitempty"`
	AvgLatencyMs      int     `json:"avg_latency_ms,omitempty"`
	QualityScore      *int    `json:"quality_score,omitempty"`
	ComplianceStatus  string  `json:"compliance_status,omitempty"`
	CompletionRatioPc float64 `json:"completion_ratio_pct,omitempty"`
}

// SessionAnalysisProject 是项目引用（对齐网关 sessionmeta.ProjectSignal）。
//
// Unclassified 显式标记「这次分析**判定为没有归属项目**」⇒ 与「project 字段
// 整个缺席」（生产方没报项目信号）**可区分**（契约 §5）。
type SessionAnalysisProject struct {
	Ref          string  `json:"ref,omitempty"`
	Label        string  `json:"label,omitempty"`
	Source       string  `json:"source,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`
	Status       string  `json:"status,omitempty"`
	Unclassified bool    `json:"unclassified,omitempty"`
}

// SessionAnalysisTask 是任务引用。
type SessionAnalysisTask struct {
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
}

// SessionAnalysisEvidence 是一条引文（对齐网关 sessionmeta.Evidence）。
type SessionAnalysisEvidence struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// SessionAnalysisTag 是一行键值标签。
//
// 两种来源**同构**但**含义不同**（契约 v1.1 第 ② 条）：
//   - Tags：网关 session_tags 表的行（tag_key / tag_value）；
//   - Features：网关 sessionmeta.Result.Features（会话级"特征"）。
//
// 两者各落各的元数据键，**不互相映射**。
type SessionAnalysisTag struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// SessionAnalysisWorkType 是网关 sessionmeta.Classification 的形态
// （value / source / confidence）。
type SessionAnalysisWorkType struct {
	Value      string  `json:"value"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// SessionAnalysisAgent 对齐网关 sessionmeta.AgentIdentity。
type SessionAnalysisAgent struct {
	Name       string  `json:"name,omitempty"`
	Type       string  `json:"type,omitempty"`
	Role       string  `json:"role,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// SessionAnalysisClient 对齐网关 sessionmeta.ClientIdentity。
type SessionAnalysisClient struct {
	Type       string  `json:"type,omitempty"`
	Protocol   string  `json:"protocol,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// SessionAnalysisExpert 对齐网关 sessionmeta.ExpertIdentity。
type SessionAnalysisExpert struct {
	Type       string  `json:"type,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// SessionAnalysisProvenance 对齐网关 sessionmeta.Provenance。
type SessionAnalysisProvenance struct {
	Method          string `json:"method,omitempty"`
	Extractor       string `json:"extractor,omitempty"`
	PromptVersion   string `json:"prompt_version,omitempty"`
	GeneratedAtHint string `json:"generated_at_hint,omitempty"`
}

// IdempotencyKey 返回 memora 契约规定的幂等键：`<dispatch_id>-<analysis_kind>`。
//
// ★ 这不是随便拼的：memora 的 handler 用 DeriveDispatchID 从**头**里按
//
//	`-<analysis_kind>` 后缀切出 dispatch_id（payload.go:358），body 里给的
//	dispatch_id 若与切出来的不一致 ⇒ 422 idempotency_key_mismatch。
func (p SessionAnalysisPayload) IdempotencyKey() string {
	return strings.TrimSpace(p.DispatchID) + "-" + strings.TrimSpace(p.AnalysisKind)
}

// HasLandableContent 报告载荷里有没有「可沉淀的内容」。
//
// 它镜像 memora 侧 AnalysisPayload.Validate() 的同一条规则（title /
// summary / outcome / key_points 至少一个非空）⇒ 客户端**提前**判定，
// 不去发一个注定被 422 拒掉的请求（那会在日志里刷无意义的错误）。
func (p SessionAnalysisPayload) HasLandableContent() bool {
	return strings.TrimSpace(p.Title) != "" ||
		strings.TrimSpace(p.Summary) != "" ||
		strings.TrimSpace(p.Outcome) != "" ||
		len(p.KeyPoints) > 0
}

// SessionAnalysisConfig 配置会话分析推送器。
type SessionAnalysisConfig struct {
	// BaseURL 是 memora 的根地址。空 ⇒ 推送器禁用。
	BaseURL string
	// JWTSecret 是与 memora 共享的 HS256 密钥。空 ⇒ 推送器禁用（fail-closed）。
	JWTSecret string
	// Issuer / Audience 对齐 memora 的 JWT 校验。空 ⇒ 用默认值。
	Issuer   string
	Audience string
	// TenantID 是 JWT 的 tenant_id claim。空 ⇒ 用默认值 "default"。
	TenantID string
	// ActorID 是 JWT 的 actor_id claim（resolve-or-create 建出来的项目的
	// owner）。空 ⇒ 回落成 issuer。
	ActorID string
	// ProjectID 是项目作用域（body 顶层字段）。空 ⇒ 推送器禁用
	//（memora 的 project scope 是 fail-closed 的）。
	ProjectID string
	// Timeout 是单次 HTTP 调用的超时。0 ⇒ 5s。
	Timeout time.Duration
	// HTTPClient 允许注入（测试用）。nil ⇒ 用默认 client。
	HTTPClient *http.Client
}

// SessionAnalysisPusher 把网关的结构化分析产出投递给 memora。
//
// 零值 / 禁用的推送器**不发起任何 HTTP 调用**（Disabled() 为真）。
type SessionAnalysisPusher struct {
	baseURL   string
	secret    []byte
	issuer    string
	audience  string
	tenantID  string
	actorID   string
	projectID string
	http      *http.Client
	timeout   time.Duration

	seenMu sync.Mutex
	seen   map[string]time.Time
}

// NewSessionAnalysisPusher 构造推送器。
//
// ★ 任何一项必要配置缺失都**只**让推送器变成禁用态，**不**返回错误：
//
//	缺 base URL ⇒ 没配 memora；缺 JWT 密钥 ⇒ 没配凭据；缺 project ⇒
//	没有项目作用域。⇒ 三者任一缺失都是「功能没开」，不是「启动失败」。
func NewSessionAnalysisPusher(cfg SessionAnalysisConfig) *SessionAnalysisPusher {
	p := &SessionAnalysisPusher{
		baseURL:   strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		issuer:    strings.TrimSpace(cfg.Issuer),
		audience:  strings.TrimSpace(cfg.Audience),
		tenantID:  strings.TrimSpace(cfg.TenantID),
		actorID:   strings.TrimSpace(cfg.ActorID),
		projectID: strings.TrimSpace(cfg.ProjectID),
		timeout:   cfg.Timeout,
		seen:      map[string]time.Time{},
	}
	if p.issuer == "" {
		p.issuer = defaultSessionAnalysisIssuer
	}
	if p.audience == "" {
		p.audience = defaultSessionAnalysisAudience
	}
	if p.tenantID == "" {
		p.tenantID = defaultSessionAnalysisTenant
	}
	if p.actorID == "" {
		// actor_id 缺省回落成 issuer：必须是**稳定**的值（它是项目的 owner）。
		p.actorID = p.issuer
	}
	if secret := strings.TrimSpace(cfg.JWTSecret); secret != "" {
		p.secret = []byte(secret)
	}
	if p.timeout <= 0 {
		p.timeout = 5 * time.Second
	}
	if cfg.HTTPClient != nil {
		p.http = cfg.HTTPClient
	} else {
		p.http = &http.Client{Timeout: p.timeout}
	}
	return p
}

// SessionAnalysisPusherFromEnv 按环境变量构造推送器。
//
// 开关：LLM_GATEWAY_MEMORA_SESSION_ANALYSIS_PUSH 必须显式打开
// （"1"/"true"/"yes"/"on"）；其余配置缺失 ⇒ 同样得到禁用态。
func SessionAnalysisPusherFromEnv(baseURL string) *SessionAnalysisPusher {
	if !envTruthy(SessionAnalysisPushEnv) {
		return NewSessionAnalysisPusher(SessionAnalysisConfig{})
	}
	return NewSessionAnalysisPusher(SessionAnalysisConfig{
		BaseURL:   baseURL,
		JWTSecret: os.Getenv(SessionAnalysisJWTSecretEnv),
		Issuer:    os.Getenv(SessionAnalysisJWTIssuerEnv),
		Audience:  os.Getenv(SessionAnalysisJWTAudienceEnv),
		TenantID:  os.Getenv(SessionAnalysisTenantEnv),
		ActorID:   os.Getenv(SessionAnalysisActorEnv),
		ProjectID: SessionAnalysisProjectID(),
	})
}

// SessionAnalysisProjectID 解析项目作用域。
//
// 取值顺序：LLM_GATEWAY_MEMORA_PROJECT_ID → MEMORA_PROJECT_ID →
// "kaixuan-1-deploy"（与人工抽取路径 admin/session_extract.go 的兜底一致，
// 保证自动推送与人工推送落在**同一个项目**里）。
//
// ★ memora 的 projects.project_id 是 VARCHAR(100)（auth.maxProjectIDLength）
//
//	⇒ 超长的 id 会被 memora 拒成 400 ⇒ 本客户端在 Disabled() 里挡掉。
func SessionAnalysisProjectID() string {
	for _, key := range []string{"LLM_GATEWAY_MEMORA_PROJECT_ID", "MEMORA_PROJECT_ID"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return "kaixuan-1-deploy"
}

// envTruthy 报告环境变量是否是显式真值。
func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Disabled 报告推送器是否处于禁用态（禁用 ⇒ 零 HTTP 调用）。
func (p *SessionAnalysisPusher) Disabled() bool {
	return p == nil ||
		p.baseURL == "" ||
		len(p.secret) == 0 ||
		p.projectID == "" ||
		len(p.projectID) > sessionAnalysisMaxProjectIDLen
}

// BaseURL 返回配置的 memora 地址（诊断用）。
func (p *SessionAnalysisPusher) BaseURL() string {
	if p == nil {
		return ""
	}
	return p.baseURL
}

// ProjectID 返回项目作用域（诊断用）。
func (p *SessionAnalysisPusher) ProjectID() string {
	if p == nil {
		return ""
	}
	return p.projectID
}

// Push 投递一次会话分析。
//
// ★ 语义：
//   - 禁用 ⇒ 直接返回 nil，**不发任何请求**；
//   - 同一 (dispatch_id, analysis_kind) ⇒ **只发一次**（进程内去重）；
//     失败时**释放占位**，让后续触发可以重试（memora 的 409 是跨进程兜底，
//     但本地失败不该被永久吞掉）；
//   - 载荷里没有可沉淀内容 ⇒ 不发（镜像 memora 的 Validate 规则）；
//   - memora 宕机 / 4xx / 5xx ⇒ 返回错误，**由调用方记日志并吞掉**
//     （本方法不 panic、不阻塞调用方超过 timeout）。
func (p *SessionAnalysisPusher) Push(ctx context.Context, payload SessionAnalysisPayload) error {
	if p.Disabled() {
		return nil
	}
	payload.DispatchID = strings.TrimSpace(payload.DispatchID)
	payload.AnalysisKind = strings.TrimSpace(payload.AnalysisKind)
	if payload.DispatchID == "" || payload.AnalysisKind == "" {
		return fmt.Errorf("sessionanalysis: dispatch_id and analysis_kind are required")
	}
	if !payload.HasLandableContent() {
		return nil
	}

	key := payload.IdempotencyKey()
	// ★ 先占位再发：同一键的**并发**触发也只能产生一次调用。
	if !p.markSeen(key) {
		return nil
	}
	release := func() { p.forgetSeen(key) }

	body := SessionAnalysisRequest{
		ProjectID:              p.projectID,
		SessionAnalysisPayload: payload,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		release()
		return fmt.Errorf("sessionanalysis: marshal body: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, p.baseURL+SessionAnalysisPath, bytes.NewReader(buf))
	if err != nil {
		release()
		return fmt.Errorf("sessionanalysis: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	// X-Correlation-ID 是必填头（契约 §1）。用幂等键当 correlation id：
	// 它就是这次投递的**身份**，排障时能直接从网关日志定位到 memora 那一行。
	req.Header.Set("X-Correlation-ID", key)
	// ★ **不**发 X-Project-ID：契约的必填头清单里没有它，而 memora 的
	//   ProjectScope 一旦收到它就会挂上项目作用域、进而让 ProjectAuthz 生效
	//   （无 authorizer 时 503）。project_id 走 body 顶层字段。
	token, err := p.serviceToken()
	if err != nil {
		release()
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.http.Do(req)
	if err != nil {
		release()
		return fmt.Errorf("sessionanalysis: post: %w", err)
	}
	defer resp.Body.Close()
	// 只读前 512 字节：错误信息够定位就行，不必把整段 body 读进内存。
	var snippet strings.Builder
	buf2 := make([]byte, 512)
	n, _ := resp.Body.Read(buf2)
	snippet.Write(buf2[:n])

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusConflict:
		// 409 有两种，**不能**一律当成功（契约 §6）：
		//   idempotency_key_reuse —— 已落过域 ⇒ 无新行 ⇒ **成功**；
		//   project_scope_conflict —— 该 id 被别的租户占用 ⇒ **错误**
		//     （静默当成功会让分析**永远丢在门外**且没人知道）。
		if code := sessionAnalysisErrorCode(snippet.String()); code == sessionAnalysisCodeProjectConflict {
			release()
			return fmt.Errorf("sessionanalysis: memora status=%d code=%s body=%s",
				resp.StatusCode, code, snippet.String())
		}
		return nil
	default:
		release()
		return fmt.Errorf("sessionanalysis: memora status=%d body=%s", resp.StatusCode, snippet.String())
	}
}

// sessionAnalysisErrorCode 从 memora 的错误体里取 `error.code`。
//
// memora 的错误体形状（internal/v2/candidates.go:43-55）：
//
//	{"error":{"code":"...","message":"...","retryable":bool}, ...}
func sessionAnalysisErrorCode(body string) string {
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Error.Code)
}

// serviceToken 铸造 memora v2 的 service JWT。
//
// 必需 claim（memora/internal/middleware/v2jwt.go 的 Verify）：
// iss / aud / sub / tenant_id / scope / exp。缺任一 ⇒ 401。
//
// ★ 另带 **actor_id**（契约 §4）：它是 resolve-or-create 建出来的项目的
//
//	owner ⇒ 必须**稳定**（否则同一项目可能被反复判成「别人建的」⇒ 409）。
func (p *SessionAnalysisPusher) serviceToken() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       p.issuer,
		"aud":       p.audience,
		"sub":       p.actorID,
		"tenant_id": p.tenantID,
		"actor_id":  p.actorID,
		"scope":     sessionAnalysisScope,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(10 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(p.secret)
	if err != nil {
		return "", fmt.Errorf("sessionanalysis: sign service jwt: %w", err)
	}
	return signed, nil
}

// markSeen 报告该键是否**首次**出现（首次 ⇒ 占位并返回 true）。
//
// 进程内去重表带 TTL 与上限：它只是**减少重复投递**的优化，
// 真正的幂等保证在 memora 侧（409 + 不产生新行）。
func (p *SessionAnalysisPusher) markSeen(key string) bool {
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	now := time.Now()
	if at, ok := p.seen[key]; ok && now.Sub(at) < sessionAnalysisSeenTTL {
		return false
	}
	if len(p.seen) >= sessionAnalysisSeenMaxCap {
		// 顺手清掉过期项；仍超上限就整体清空（去重表只是优化，
		// 丢了它最多多发一次请求，memora 侧仍会 409 兜底）。
		for k, at := range p.seen {
			if now.Sub(at) >= sessionAnalysisSeenTTL {
				delete(p.seen, k)
			}
		}
		if len(p.seen) >= sessionAnalysisSeenMaxCap {
			p.seen = map[string]time.Time{}
		}
	}
	p.seen[key] = now
	return true
}

// forgetSeen 释放一个占位（投递失败时调用），让后续触发可以重试。
func (p *SessionAnalysisPusher) forgetSeen(key string) {
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	delete(p.seen, key)
}

// SeenCount 返回当前去重表大小（诊断/测试用）。
func (p *SessionAnalysisPusher) SeenCount() int {
	if p == nil {
		return 0
	}
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	return len(p.seen)
}

// SessionAnalysisHTTPStatusOf 提取 memora 错误里的状态码（测试辅助）。
func SessionAnalysisHTTPStatusOf(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	idx := strings.Index(msg, "status=")
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len("status="):]
	end := strings.IndexAny(rest, " ")
	if end < 0 {
		end = len(rest)
	}
	n, cerr := strconv.Atoi(rest[:end])
	if cerr != nil {
		return 0
	}
	return n
}
