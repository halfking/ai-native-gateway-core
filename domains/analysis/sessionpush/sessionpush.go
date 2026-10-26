// Package sessionpush —— 把网关的**结构化会话分析产出**自动推给 memora。
//
// # 缺口本体（2026-10-20 会话族审计 G-2）
//
// 网关侧的结构化分析产出的权威形态是 sessionmeta.Result
// （SchemaVersion="session-analysis/v1"、AnalysisKind="session_metadata"），
// 它被整份落进 public.session_analysis_metadata.payload
// （domains/analysis/sessionmeta/store.go:83）。
//
// 但**它从来不出网关**：唯一的推送路径是人工端点
// POST /api/system/session-context/{taskId}/extract-to-memora
// （admin/session_extract.go，super_admin 手工触发），而且它走的是 memora 的
// v1 通用记忆入口 /api/memory/add —— 那条路**不收**结构化分析产出
// （memora 侧的 /api/v2/session-analysis 才是这条契约的落地域）。
//
// ⇒ 「会话数据 → 记忆」这条链在生产上**从未自动走过**。
//
// # 契约权威（memora main 2a8aff3，契约 v1.1）
//
//	memora/docs/integration/2026-10-20-session-analysis-contract.md
//
// # 这条链做什么
//
// 产出点（两处，都是「结构化分析结果被产生」的时刻）：
//
//  1. provisional：每个请求到达时（admin/auto_title_generator.go 的
//     MaybeGenerateProvisionalMetadata → Extract → UpsertProvisional）；
//  2. final：会话关闭、总结 worker 成功后
//     （domains/analysis/workers/session_metadata_close_hook.go）。
//
// 推送目标：POST {memora}/api/v2/session-analysis，
//
//	头 Idempotency-Key = `<dispatch_id>-<analysis_kind>`、X-Correlation-ID、
//	Authorization（service JWT）；体顶层 project_id + 载荷本体。
//
// ★ **不**发 X-Project-ID 头（契约的必填头清单里没有它；memora 的
//
//	ProjectScope 一旦收到它就会让 ProjectAuthz 生效 ⇒ 无 authorizer 时 503）。
//
// # dispatch_id 怎么拼
//
// 契约 §2：dispatch_id 由**幂等键头**推导（网关的 sessionmeta.Result 没有
// 这个字段），服务端按 `-<analysis_kind>` 后缀切出它 ⇒ 生产方用**它实际有的**
// 标识拼即可。网关实际有的是 (tenant_id, scoped_session_id, status)，
// 而 session_analysis_metadata 的唯一键正是这三元组
// （store.go:47）⇒ 每个 (会话, 状态) 恰好一次产出。
//
// ⇒ dispatch_id = `<tenant_id>:<scoped_session_id>-<status>`。
//
// ★ 为什么带 tenant：scoped_session_id **不含租户**（两个租户可以同名），
//
//	而 memora 的幂等键是**全局**的 ⇒ 不带租户会让 B 租户的投递撞上
//	A 租户已落的键（409）⇒ 分析**永远丢在门外**。
//
// ★ 为什么带 status：provisional 与 final 是**两个不同的产出**；不带状态
//
//	时 final 会被当成「同键重放」⇒ memora 里只剩最早那次粗分析。
//
// # features 与 tags 是**两个字段**（契约 v1.1 第 ② 条）
//
// features = sessionmeta.Result.Features（会话级"特征"）
// tags     = 网关 session_tags 表的行（tag_key / tag_value / source / confidence）
//
// memora 分别把它们落到元数据键 features / tags，**不互相映射**。
// v1.0 把网关的 features 静默落进 tags **是缺陷**（贴错标签）⇒ 本包严格分开。
//
// # 幂等与去重
//
//	· 同一 (dispatch_id, analysis_kind) ⇒ **只推一次**：
//	  进程内去重表（domains/memory/client/session_analysis.go 的 markSeen）
//	  + memora 侧的 409 兜底（跨进程/重启）。
//	· provisional 每个请求都会产出一次分析 ⇒ 去重表把每会话的重复触发
//	  收敛成**一次**推送。
//
// # 失败语义（硬要求）
//
// memora 宕机 / 超时 / 4xx / 5xx ⇒ **记一行日志并吞掉**。
// 这条链跑在网关的正常请求路径上（产出点在请求收尾阶段）⇒
// memora 的故障**绝不能**变成用户的请求错误。
package sessionpush

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
)

// Tag 是一行网关 session_tags（键值标签）。
//
// 它与 sessionmeta.Feature **同构**但**来源不同**（契约 v1.1 第 ② 条）：
// session_tags 是网关 tagger 落库的行（tag_key / tag_value / tag_source），
// Features 是 extractor 产出的会话级"特征"。两者各走各的字段。
type Tag struct {
	Key        string
	Value      string
	Source     string
	Confidence float64
}

// Input 是一次推送的输入。
type Input struct {
	// TenantID / SessionID / TaskID 是会话身份。TenantID 与 SessionID 必填。
	TenantID  string
	SessionID string
	TaskID    string
	// Status 是分析状态（provisional / final）。空 ⇒ 用 result.Status。
	Status string
	// Result 是网关产出的结构化分析结果。
	Result sessionmeta.Result
	// Tags 是网关 session_tags 的行（可空：没有就只发 features）。
	Tags []Tag
}

// Sink 是推送目标（由 memora 客户端实现）。
type Sink interface {
	// Push 投递一次；返回错误由调用方记日志并吞掉。
	Push(ctx context.Context, payload memclient.SessionAnalysisPayload) error
	// Disabled 报告目标是否禁用（禁用 ⇒ 零 HTTP 调用）。
	Disabled() bool
	// ProjectID 返回项目作用域（body 顶层字段）。
	ProjectID() string
}

var (
	sinkMu sync.RWMutex
	sink   Sink
)

// Set 安装进程级推送目标（nil ⇒ 关闭）。
//
// ★ 为什么是进程级的：产出点分散在两个包里（admin 的到达钩子与
//
//	analysis/workers 的关闭钩子），中间还隔着 admin.Handler 与
//	v2DispatchDeps 两层注入链。这条链是**纯副作用**（推送失败不影响
//	任何业务结果），用进程级单点比穿两层注入链更不容易漏接。
//	⇒ 读的是**调用时**的当前值，不是构造时的快照 ⇒ 安装顺序不影响
//	  触发时机。
func Set(s Sink) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	sink = s
}

// Current 返回当前推送目标（nil ⇒ 没有）。
func Current() Sink {
	sinkMu.RLock()
	defer sinkMu.RUnlock()
	return sink
}

// BuildPayload 把网关的分析结果投影成 memora 的投递载荷。
//
// 返回 ok=false 表示**没有可沉淀的内容** ⇒ 不该发（镜像 memora 侧
// AnalysisPayload.Validate() 的规则：title/summary/outcome/key_points
// 至少一个非空）。⇒ 不去发一个注定被 422 拒掉的请求。
//
// ★ 只映射网关**真的产出**的字段：sessionmeta.Result 没有 summary /
//
//	outcome / key_points ⇒ 那几个字段留空。memora 侧的文档也明写
//	「不臆造网关产不出的东西」。
func BuildPayload(in Input, projectID string) (memclient.SessionAnalysisPayload, bool) {
	result := in.Result
	tenantID := strings.TrimSpace(in.TenantID)
	sessionID := strings.TrimSpace(in.SessionID)
	if tenantID == "" || sessionID == "" {
		return memclient.SessionAnalysisPayload{}, false
	}
	if strings.TrimSpace(projectID) == "" {
		return memclient.SessionAnalysisPayload{}, false
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = strings.TrimSpace(result.Status)
	}
	if status == "" {
		return memclient.SessionAnalysisPayload{}, false
	}
	analysisKind := strings.TrimSpace(result.AnalysisKind)
	if analysisKind == "" {
		analysisKind = sessionmeta.AnalysisKind
	}
	schemaVersion := strings.TrimSpace(result.SchemaVersion)
	if schemaVersion == "" {
		schemaVersion = sessionmeta.SchemaVersion
	}

	payload := memclient.SessionAnalysisPayload{
		// ★ 租户段在前：scoped_session_id 不含租户，而 memora 的幂等键是
		//   全局的 ⇒ 不带租户会跨租户撞键（409 ⇒ 分析永远丢在门外）。
		// ★ 状态段在后：provisional 与 final 是两个不同的产出。
		DispatchID:      tenantID + ":" + sessionID + "-" + status,
		AnalysisKind:    analysisKind,
		SourceSessionID: sessionID,
		SchemaVersion:   schemaVersion,
		Status:          status,
		Title:           strings.TrimSpace(result.Title),
		InputHash:       strings.TrimSpace(result.InputHash),
	}
	if taskID := strings.TrimSpace(in.TaskID); taskID != "" {
		payload.Task = &memclient.SessionAnalysisTask{ID: taskID}
	}
	// 项目信号：网关**总是**给出一个 project 字段（extractor_rules.go 的
	// extractProject 有三个分支：authoritative / rule / unknown）。
	//   · Source="unknown" ⇒ extractor **看过并判定没有归属项目**
	//     ⇒ 映射成 memora 的 unclassified=true（契约 §5：与「project 整个
	//       缺席」可区分）；
	//   · 其余 ⇒ 有项目信号，unclassified 缺省。
	p := result.Project
	payload.Project = &memclient.SessionAnalysisProject{
		Ref:          strings.TrimSpace(p.Ref),
		Label:        strings.TrimSpace(p.Label),
		Source:       strings.TrimSpace(p.Source),
		Confidence:   p.Confidence,
		Status:       strings.TrimSpace(p.Status),
		Unclassified: strings.TrimSpace(p.Source) == "unknown",
	}
	// 引文：发网关的字段名 **evidence**（grounding 是 memora 的历史别名，
	// 两者同构同义、memora 会合并去重 ⇒ 只发一份）。
	for _, e := range result.Evidence {
		if strings.TrimSpace(e.Value) == "" {
			continue
		}
		payload.Evidence = append(payload.Evidence, memclient.SessionAnalysisEvidence{
			Kind:  e.Kind,
			Value: e.Value,
		})
	}
	// ★ features = Result.Features（**不是** tags）。
	for _, f := range result.Features {
		if strings.TrimSpace(f.Key) == "" {
			continue
		}
		payload.Features = append(payload.Features, memclient.SessionAnalysisTag{
			Key:        f.Key,
			Value:      f.Value,
			Source:     f.Source,
			Confidence: f.Confidence,
		})
	}
	// ★ tags = 网关 session_tags 的行（**不是** features）。
	for _, t := range in.Tags {
		if strings.TrimSpace(t.Key) == "" || strings.TrimSpace(t.Value) == "" {
			continue
		}
		payload.Tags = append(payload.Tags, memclient.SessionAnalysisTag{
			Key:        t.Key,
			Value:      t.Value,
			Source:     t.Source,
			Confidence: t.Confidence,
		})
	}
	// work_types：**结构化**（value/source/confidence），不是 []string
	// （契约 v1.1 第 ① 条：网关的 []Classification 逐字绑定）。
	for _, w := range result.WorkTypes {
		if v := strings.TrimSpace(w.Value); v != "" {
			payload.WorkTypes = append(payload.WorkTypes, memclient.SessionAnalysisWorkType{
				Value:      v,
				Source:     w.Source,
				Confidence: w.Confidence,
			})
		}
	}
	if a := result.Agent; a.Name != "" || a.Type != "" || a.Role != "" {
		payload.Agent = &memclient.SessionAnalysisAgent{
			Name:       a.Name,
			Type:       a.Type,
			Role:       a.Role,
			Source:     a.Source,
			Confidence: a.Confidence,
		}
	}
	if c := result.Client; c.Type != "" || c.Protocol != "" {
		payload.Client = &memclient.SessionAnalysisClient{
			Type:       c.Type,
			Protocol:   c.Protocol,
			Source:     c.Source,
			Confidence: c.Confidence,
		}
	}
	if e := result.Expert; e.Type != "" {
		payload.Expert = &memclient.SessionAnalysisExpert{
			Type:       e.Type,
			Source:     e.Source,
			Confidence: e.Confidence,
		}
	}
	if p := result.Provenance; p.Method != "" || p.Extractor != "" {
		payload.Provenance = &memclient.SessionAnalysisProvenance{
			Method:          p.Method,
			Extractor:       p.Extractor,
			PromptVersion:   p.PromptVersion,
			GeneratedAtHint: p.GeneratedAtHint,
		}
	}
	if !payload.HasLandableContent() {
		return memclient.SessionAnalysisPayload{}, false
	}
	return payload, true
}

// Deliver 触发一次推送。
//
// ★ 它**永不**返回错误：memora 的任何故障都只记一行日志。
//
//	（这条链跑在网关的正常请求路径上 —— memora 宕机不能变成用户请求失败。）
func Deliver(ctx context.Context, in Input) {
	target := Current()
	if target == nil || target.Disabled() {
		return
	}
	payload, ok := BuildPayload(in, target.ProjectID())
	if !ok {
		return
	}
	if err := target.Push(ctx, payload); err != nil {
		slog.Warn("session analysis push to memora failed (suppressed)",
			"tenant_id", in.TenantID,
			"session_id", in.SessionID,
			"analysis_kind", payload.AnalysisKind,
			"status", payload.Status,
			"error", err.Error())
	}
}
