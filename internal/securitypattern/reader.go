package securitypattern

// 读面：从共享知识项目里把别的实例写入的安全事件读回来。
//
// # 身份
//
// 读面必须与写面**同一身份**（同一 service JWT ⇒ 同一 tenant_id /
// actor_id；同一 X-Project-ID）。memora 的 v2 检索以 actor 作为 user_id
// 谓词（internal/v2/retrieval.go）⇒ 身份不同就是另一个命名空间，读回来
// 的是"空"或"别人的东西"。本包因此直接复用写面那个 memv2.Client，
// 不另造一份凭据。
//
// # 检索是语义检索
//
// /api/v2/memories/search 是向量检索：query 必填、top_k ≤ 100，返回的是
// "与 query 最像的 N 条"，**不是**这个项目的全部安全事件。
// ⇒ 本包把它当模式流（尽力而为的 top-K），不是审计级同步。

import (
	"context"
	"fmt"
	"strings"
	"time"

	memv2 "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/securityevent"
)

// DefaultQuery 是默认检索词。
//
// 安全事件的正文以 "[Security]" 开头（FormatEvent），metadata 里带
// severity/action/code ⇒ 这几个词的向量与安全事件正文最接近。
// 可用环境变量覆盖（不同部署的事件正文措辞可能不同）。
const DefaultQuery = "security threat detection event"

// DefaultTopK 是默认取回条数（服务端上限 100）。
const DefaultTopK = 50

// Reader 读回共享域里的安全事件。
type Reader interface {
	// Fetch 读回一批观察。失败返回错误，调用方保留上一份快照。
	Fetch(ctx context.Context) ([]Observation, error)
}

// MemoraReader 是从 memora 共享项目读回安全事件的读面。
type MemoraReader struct {
	client *memv2.Client
	query  string
	topK   int
}

// NewMemoraReader 构造读面。
//
// client 必须与写面（securityevent 的落地面）**是同一个客户端实例** ——
// 它带着写面用的 tenant_id / actor_id / project_id。
func NewMemoraReader(client *memv2.Client, query string, topK int) *MemoraReader {
	if strings.TrimSpace(query) == "" {
		query = DefaultQuery
	}
	if topK <= 0 {
		topK = DefaultTopK
	}
	if topK > 100 {
		// 服务端上限 100：超了会被判 validation_failed。
		topK = 100
	}
	return &MemoraReader{client: client, query: query, topK: topK}
}

// Fetch 读回一批观察。
//
// 只取带 security 标签的条目（写面给每条安全事件都打了这个标签）⇒
// 共享项目里的非安全记忆不会混进模式流。
//
// 降级（data.degraded）**当作失败**：降级意味着这份结果不完整，把它当成
// 快照会把"少了一半"读成"模式变少了"。policy.on_degraded=fail_closed 让
// 服务端在降级时直接 503，而不是回一份残缺的结果。
func (r *MemoraReader) Fetch(ctx context.Context) ([]Observation, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("securitypattern: reader is not configured")
	}
	topK := r.topK
	resp, err := r.client.SearchItems(ctx, memv2.SearchRequest{
		Query:   r.query,
		TopK:    &topK,
		Filters: &memv2.SearchFilters{Tags: []string{securityevent.TagSecurity}},
		Policy:  &memv2.SearchPolicy{OnDegraded: "fail_closed"},
	})
	if err != nil {
		return nil, fmt.Errorf("securitypattern: fetch shared security events: %w", err)
	}
	if resp.Degraded {
		reasons := strings.Join(resp.DegradedReasons, ",")
		if reasons == "" {
			reasons = "unspecified"
		}
		return nil, fmt.Errorf("securitypattern: shared security event search degraded (%s)", reasons)
	}

	observations := make([]Observation, 0, len(resp.Items))
	for _, item := range resp.Items {
		observation, ok := observationFromItem(item)
		if !ok {
			// 这条命中不是安全事件（缺事件类型或检测码）⇒ 跳过。
			continue
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

// observationFromItem 从一条检索命中的 metadata 里还原观察。
//
// 返回 false 表示"这条命中不是我要的安全事件"（缺 event_type 或 code）。
// 缺字段就跳过而不是填默认值：填出来的空签名会污染聚合。
func observationFromItem(item memv2.SearchItem) (Observation, bool) {
	eventType, ok := memv2.SearchItemMetadataString(item, "event_type")
	if !ok {
		return Observation{}, false
	}
	code, ok := memv2.SearchItemMetadataString(item, "code")
	if !ok {
		return Observation{}, false
	}

	observation := Observation{
		EventType: eventType,
		Code:      code,
		MemoryID:  item.MemoryID,
	}
	if severity, ok := memv2.SearchItemMetadataString(item, "severity"); ok {
		// 归一化：共享域里的严重度是写面归一化后的值，这里再过一次
		// NormalizeSeverity 是为了容忍外部写入的原始刻度。
		if normalized, known := securityevent.NormalizeSeverity(severity); known {
			observation.Severity = normalized
		} else {
			// 未知刻度**不降级成 info**（那会被读成"没问题"）：原样保留，
			// 由 severityRank 排在最低但不影响判定。
			observation.Severity = securityevent.Severity(severity)
		}
	}
	if action, ok := memv2.SearchItemMetadataString(item, "action"); ok {
		observation.Action = securityevent.Action(action)
	}
	if tenant, ok := memv2.SearchItemMetadataString(item, "tenant_id"); ok {
		observation.TenantID = tenant
	}
	if instance, ok := memv2.SearchItemMetadataString(item, "instance_id"); ok {
		observation.InstanceID = instance
	}
	if detectedAt, ok := memv2.SearchItemMetadataString(item, "detected_at"); ok {
		if parsed, err := time.Parse(time.RFC3339, detectedAt); err == nil {
			observation.DetectedAt = parsed.UTC()
		}
	}
	return observation, true
}
