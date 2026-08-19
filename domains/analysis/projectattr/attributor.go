// Package projectattr — 会话→项目归属推断（非 ACC 认领路径）。
//
// 背景：两类流量进入网关。
//
//   - ACC 认领任务的智能体：请求头里带 X-Gw-Project-Id / X-Gw-Task-Id，
//     值是 ACC 数据库的真实主键。这类归属是权威的，直接落
//     session_dim.project_id（migration 407），本包完全不介入。
//
//   - 其它智能体：客户端不可控，头可能为空。这类才需要推断。
//
// 推断结果写 session_project_attribution 表，绝不写
// session_dim.project_id —— 后者供计费口径使用，掺入约 85% 准确率的猜测
// 会让所有下游数字失去可审计性。同样的理由，OpenTelemetry GenAI 规范要求
// gen_ai.conversation.id 在拿不到真实值时留空而不是用兜底值填充。
//
// 三级降级，命中即停，成本从零递增：
//
//  1. rule    — 确定性信号（工种键、仓库路径、项目关键词）。零成本。
//  2. inherit — 同一设备/密钥近期已确认过的项目。零成本，一次查表。
//  3. llm     — 模型兜底。仅处理前两级都没命中的长尾。
//
// 触发点是会话关闭（SessionCloseHook），不是每次请求。这是本设计里最要紧
// 的一个决定：会话级触发天然把 N 次请求压成 1 次调用，把放大倍数从"占比"
// 降到"占比 ÷ 每会话请求数"。
package projectattr

import (
	"context"
	"sort"
	"strings"
)

// Method 是产出归属的方式，与表中的 CHECK 约束一一对应。
type Method string

const (
	MethodRule    Method = "rule"
	MethodInherit Method = "inherit"
	MethodLLM     Method = "llm"
	MethodManual  Method = "manual"
)

// Status 是人工复核状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusRejected  Status = "rejected"
)

// Project 是 project_dim 的一行：从 ACC 同步下来的项目维表。
type Project struct {
	Ref           string
	Name          string
	MatchKeywords []string
	RepoPaths     []string
}

// Signals 是推断可用的输入。全部来自已有的采集字段，本包不新增任何采集。
type Signals struct {
	GwSessionID string
	TenantID    string

	// WorkType 是 X-Gw-Work-Type（ACC 工种键），已落 request_logs.work_type。
	WorkType string
	// SystemPrompt 常含仓库路径 / 目录名，是最强的确定性信号。调用方可从
	// request_body 提取后填入；PGStore 默认不填（避免为归集反序列化整个
	// body），留给需要更高命中率的部署自行注入。
	SystemPrompt string
	// UserText 来自 request_logs.request_preview，是首轮请求的轻量预览。
	UserText string
	// IdentityHash 是 request_logs.identity_hash（客户端指纹），供 inherit
	// 层关联同一客户端的历史归属。
	IdentityHash string
	APIKeyID     int64
}

// Result 是一次推断的产出。ProjectRef 为空表示"无法判定"——这是合法且
// 期望的结果，调用方应当保持字段为空，而不是塞一个占位值。
type Result struct {
	ProjectRef   string
	ProjectLabel string
	TaskRef      string
	Method       Method
	Confidence   float64
	Status       Status
	Evidence     map[string]any
}

// Found 报告是否得到了可用归属。
//
// 要求 ProjectRef 非空：只有一个人类可读的 label 而没有项目引用，无法用来
// 归集，也无法在复核界面上跟 ACC 项目对上号。这类结果视为未命中，字段留空
// ——与 OpenTelemetry 对 gen_ai.conversation.id 的要求一致：宁可留空，不可
// 用兜底值冒充。
func (r Result) Found() bool { return r.ProjectRef != "" }

// InheritLookup 查同一设备/密钥近期已确认的项目。返回空串表示无历史。
type InheritLookup func(ctx context.Context, s Signals) (projectRef string, err error)

// LLMClassifier 是模型兜底层。实现方负责真正的 LLM 调用；返回空串表示
// 模型也无法判定，此时结果留空而不是编造。
type LLMClassifier func(ctx context.Context, s Signals, candidates []Project) (projectRef, label string, err error)

// Attributor 按 rule → inherit → llm 顺序推断。
//
// llm 为 nil 时自动退化为纯零成本模式（只跑前两级），部署上可用来先观察
// 规则层覆盖率，再决定要不要开模型兜底。
type Attributor struct {
	projects []Project
	inherit  InheritLookup
	llm      LLMClassifier

	// ruleConfidence 是规则命中时的置信度。规则是确定性的，但仍低于 1.0：
	// 1.0 保留给 ACC 传入的权威值，推断永远不应自称完全确定。
	ruleConfidence    float64
	inheritConfidence float64
	llmConfidence     float64

	// autoConfirmRules 为 true 时规则层命中直接标 confirmed，不进人工队列。
	autoConfirmRules bool
}

// Option 配置 Attributor。
type Option func(*Attributor)

// WithInherit 注入历史继承查询。
func WithInherit(f InheritLookup) Option { return func(a *Attributor) { a.inherit = f } }

// WithLLM 注入模型兜底。不注入则跳过该层。
func WithLLM(f LLMClassifier) Option { return func(a *Attributor) { a.llm = f } }

// WithAutoConfirmRules 让规则层命中免于人工确认。
func WithAutoConfirmRules(v bool) Option { return func(a *Attributor) { a.autoConfirmRules = v } }

// New 构造 Attributor。projects 通常来自 project_dim 的快照。
func New(projects []Project, opts ...Option) *Attributor {
	a := &Attributor{
		projects:          projects,
		ruleConfidence:    0.95,
		inheritConfidence: 0.75,
		llmConfidence:     0.55,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Attribute 执行三级降级推断。
//
// 任一层出错都不中断整条链路：推断是尽力而为的分析功能，不该因为一次
// Redis/DB/LLM 抖动就丢掉本可由后续层得到的结果。
func (a *Attributor) Attribute(ctx context.Context, s Signals) Result {
	// 第一级：确定性规则。
	if res, ok := a.matchRules(s); ok {
		if a.autoConfirmRules {
			res.Status = StatusConfirmed
		}
		return res
	}

	// 第二级：历史继承。同一设备/密钥近期确认过的项目，大概率仍是它。
	if a.inherit != nil {
		if ref, err := a.inherit(ctx, s); err == nil && ref != "" {
			return Result{
				ProjectRef:   ref,
				ProjectLabel: a.labelFor(ref),
				Method:       MethodInherit,
				Confidence:   a.inheritConfidence,
				Status:       StatusPending,
				Evidence: map[string]any{
					"source":        "inherit",
					"identity_hash": s.IdentityHash,
				},
			}
		}
	}

	// 第三级：模型兜底，只处理长尾。
	//
	// 要求 ref 非空：模型只给出一个自由文本标签而无法对应到具体项目时，
	// 该结果无法用于归集，按未命中处理。
	if a.llm != nil {
		if ref, label, err := a.llm(ctx, s, a.projects); err == nil && ref != "" {
			return Result{
				ProjectRef:   ref,
				ProjectLabel: firstNonEmpty(label, a.labelFor(ref)),
				Method:       MethodLLM,
				Confidence:   a.llmConfidence,
				Status:       StatusPending,
				Evidence: map[string]any{
					"source": "llm",
				},
			}
		}
	}

	// 三级都没命中：留空。这是正确结果，不是失败。
	return Result{Status: StatusPending}
}

// matchRules 在 system prompt / 用户文本 / 工种键里找确定性信号。
//
// 仓库路径优先于关键词：路径是强唯一信号，关键词可能在多个项目间撞车。
// 同类信号内按匹配长度取最长者，避免短词误命中（"api" 撞 "api-gateway"）。
func (a *Attributor) matchRules(s Signals) (Result, bool) {
	haystack := strings.ToLower(
		strings.Join([]string{s.SystemPrompt, s.UserText, s.WorkType}, "\n"),
	)
	if strings.TrimSpace(haystack) == "" {
		return Result{}, false
	}

	type hit struct {
		proj    Project
		matched string
		isPath  bool
	}
	var hits []hit

	for _, p := range a.projects {
		// 没有 Ref 的项目无法用于归集，直接跳过——否则它会一路走到
		// Result 里，产出一个只有名字、无法与 ACC 对账的"幽灵归属"。
		if p.Ref == "" {
			continue
		}
		for _, rp := range p.RepoPaths {
			rp = strings.ToLower(strings.TrimSpace(rp))
			if rp != "" && strings.Contains(haystack, rp) {
				hits = append(hits, hit{proj: p, matched: rp, isPath: true})
			}
		}
		for _, kw := range p.MatchKeywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw != "" && strings.Contains(haystack, kw) {
				hits = append(hits, hit{proj: p, matched: kw})
			}
		}
	}
	if len(hits) == 0 {
		return Result{}, false
	}

	// 路径优先，其次匹配串更长者，最后按 ref 保证结果稳定可复现。
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].isPath != hits[j].isPath {
			return hits[i].isPath
		}
		if len(hits[i].matched) != len(hits[j].matched) {
			return len(hits[i].matched) > len(hits[j].matched)
		}
		return hits[i].proj.Ref < hits[j].proj.Ref
	})

	best := hits[0]

	// 多个不同项目同时命中时降低置信度并强制人工复核：这正是最容易出错、
	// 也最值得人看一眼的情况。
	ambiguous := false
	for _, h := range hits[1:] {
		if h.proj.Ref != best.proj.Ref {
			ambiguous = true
			break
		}
	}
	conf := a.ruleConfidence
	if ambiguous {
		conf = 0.6
	}

	return Result{
		ProjectRef:   best.proj.Ref,
		ProjectLabel: best.proj.Name,
		Method:       MethodRule,
		Confidence:   conf,
		Status:       StatusPending,
		Evidence: map[string]any{
			"source":     "rule",
			"matched":    best.matched,
			"match_kind": matchKind(best.isPath),
			"ambiguous":  ambiguous,
		},
	}, true
}

func (a *Attributor) labelFor(ref string) string {
	for _, p := range a.projects {
		if p.Ref == ref {
			return p.Name
		}
	}
	return ""
}

func matchKind(isPath bool) string {
	if isPath {
		return "repo_path"
	}
	return "keyword"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
