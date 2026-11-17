package memora

// v2 检索面（读）：POST /api/v2/memories/search。
//
// # 为什么读面必须与写面同一身份
//
// memora 的 v2 组是 fail-closed 的，两端的身份语义必须对齐：
//
//   - 写：/api/v2/memories/ingest 用 service JWT 的 tenant_id / actor_id
//     落库（internal/v2/ingest.go：UserID = identity.ActorID）；
//   - 读：/api/v2/memories/search 同样用 identity.ActorID 作为检索的
//     user_id 谓词（internal/v2/retrieval.go）。
//
// ⇒ 读到的就是**同一个 actor 命名空间**里的东西。用另一个身份去读，读回来
// 的是"别人的东西"或"空" —— 那是身份配错，不是没有数据。
//
// # 契约要点（对照 memora internal/v2/retrieval.go）
//
//   - scope 必须含 memory.read / memory.* / admin 之一（defaultScope 已含）；
//   - scope_chain.tenant_id 必须与 JWT 的 tenant_id 一致，否则 422
//     tenant_mismatch；
//   - scope_chain.project_id 必填，且必须与请求头 X-Project-ID 一致，
//     否则 422 project_mismatch；
//   - query 必填且非空（空 ⇒ 422 validation_failed）；
//   - filters 只支持 tags（source_types / classification_max 服务端会拒）；
//   - allowed_scopes、scope_chain.run_id/task_id/agent_session_id 服务端
//     会拒（unsupported_scope）—— 本客户端**不暴露**这些字段，免得发出
//     必然被拒的请求。
//
// # 降级语义
//
// policy.on_degraded=fail_closed ⇒ 服务端在检索后端降级时直接 503，
// 而不是回一份"看起来正常但其实不完整"的结果。于是客户端不必猜：
// 要么拿到一份完整快照，要么拿到明确的失败。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SearchScopeChain 是检索请求的谱系字段。
//
// 只暴露服务端支持的子集：run_id / task_id / agent_session_id 服务端会以
// unsupported_scope 拒绝（当前检索后端不支持这些谓词），暴露它们只会让
// 调用方发出必然失败的请求。
type SearchScopeChain struct {
	TenantID  string `json:"tenant_id"`
	ProjectID string `json:"project_id"`
}

// SearchFilters 是检索过滤器。当前只有 tags 被服务端支持。
type SearchFilters struct {
	Tags []string `json:"tags,omitempty"`
}

// SearchPolicy 是降级策略。
type SearchPolicy struct {
	// OnDegraded 只能是 fail_closed 或 degraded_with_warning。
	OnDegraded string `json:"on_degraded,omitempty"`
}

// SearchRequest 是 /api/v2/memories/search 的请求体。
type SearchRequest struct {
	// Query 必填且非空。
	Query string `json:"query"`
	// ScopeChain 必填：tenant_id 必须与 JWT 一致，project_id 必须与
	// X-Project-ID 一致。
	ScopeChain SearchScopeChain `json:"scope_chain"`
	// TopK 返回条数，服务端上限 100、默认 10。nil ⇒ 用服务端默认。
	TopK *int `json:"top_k,omitempty"`
	// Filters 只支持 tags。
	Filters *SearchFilters `json:"filters,omitempty"`
	// Policy 降级策略。
	Policy *SearchPolicy `json:"policy,omitempty"`
}

// SearchItem 是一条检索命中。
type SearchItem struct {
	Source         string         `json:"source"`
	MemoryID       string         `json:"memory_id"`
	Scope          string         `json:"scope"`
	Score          float64        `json:"score"`
	TokenCount     int            `json:"token_count"`
	Snippet        string         `json:"snippet"`
	PolicyDecision string         `json:"policy_decision"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// SearchResponse 是 /api/v2/memories/search 响应里 **data** 那部分的内容。
//
// 服务的响应形状是 {"data": {...}, "request_id": "..."} ⇒ data 的内容
// 单独成一个类型，外层由调用方解码。
type SearchResponse struct {
	Items           []SearchItem `json:"items"`
	Degraded        bool         `json:"degraded"`
	DegradedReasons []string     `json:"degraded_reasons,omitempty"`
}

// SearchItems 检索记忆并返回**原始命中**（含完整 metadata）。
//
// 与 Ingest 共用同一份 service JWT 身份（同一 tenant_id / actor_id /
// X-Project-ID）⇒ 读回来的就是本客户端写进去的那批东西。
//
// ★ 为什么与既有的 Search（memory.Reader 实现）分开：
//
//   - 既有的 Search 只解码 metadata 里的 source_system / source_type /
//     content_ref / tags 四个字段（searchItem 结构体）⇒ 安全事件需要的
//     event_type / code / severity / tenant_id / instance_id **全被丢掉**；
//   - 既有的 Search 不带 filters.tags ⇒ 分不清安全事件与其它记忆；
//   - 既有的 Search 不发 policy.on_degraded ⇒ 服务端降级时默认
//     degraded_with_warning，客户端拿到的是一份残缺结果却看不出来。
//
// 本方法三条都补上：完整 metadata、tags 过滤、降级即失败。
func (c *Client) SearchItems(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if c == nil {
		return SearchResponse{}, errors.New("memora: client is nil")
	}
	if strings.TrimSpace(req.Query) == "" {
		return SearchResponse{}, errors.New("memora: search query must not be empty")
	}
	// scope_chain 的两个字段由客户端按自己的身份补齐 —— 调用方传了不同的
	// 值就意味着调用方以为自己是另一个身份，那会让"读回来的不是自己写的
	// 东西"变成静默的错误。这里直接以客户端身份为准。
	req.ScopeChain.TenantID = c.tenantID
	req.ScopeChain.ProjectID = c.projectID
	if req.Policy == nil {
		req.Policy = &SearchPolicy{OnDegraded: "fail_closed"}
	}

	var resp struct {
		Data      SearchResponse `json:"data"`
		RequestID string         `json:"request_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/memories/search", req, nil, &resp); err != nil {
		return SearchResponse{}, fmt.Errorf("memora: search failed: %w", err)
	}
	return resp.Data, nil
}

// SearchItemMetadataString 从命中的 metadata 里取一个字符串字段。
//
// metadata 来自 JSONB，反序列化后值是 any（string / float64 / bool）⇒
// 必须做类型收敛。取不到就返回空串与 false —— 调用方据此判断"这条命中
// 不是我要的东西"，而不是把 "<nil>" 当成值。
func SearchItemMetadataString(item SearchItem, key string) (string, bool) {
	if item.Metadata == nil {
		return "", false
	}
	raw, ok := item.Metadata[key]
	if !ok || raw == nil {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "", false
		}
		return v, true
	case float64:
		return fmt.Sprintf("%v", v), true
	case bool:
		return fmt.Sprintf("%v", v), true
	default:
		return "", false
	}
}

// SearchItemMetadataMap 从命中的 metadata 里取一个子对象。
func SearchItemMetadataMap(item SearchItem, key string) (map[string]any, bool) {
	if item.Metadata == nil {
		return nil, false
	}
	raw, ok := item.Metadata[key]
	if !ok || raw == nil {
		return nil, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return m, true
}

// SearchItemMetadataJSON 把命中的某个 metadata 字段原样编码成 JSON。
//
// 用于 metadata 里嵌了结构体的情况（v2 ingest 写进去的是嵌套对象）。
func SearchItemMetadataJSON(item SearchItem, key string) (json.RawMessage, bool) {
	if item.Metadata == nil {
		return nil, false
	}
	raw, ok := item.Metadata[key]
	if !ok || raw == nil {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	return encoded, true
}
