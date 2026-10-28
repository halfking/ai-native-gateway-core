package sanitizerules

// 拦截审核队列（2026-10-28 拦截反馈明确化轮）。
//
// ## owner 口径（本需求的原话）
//
// 「或者在后台有个列表，需要审核，审核完成后可以返回给前端。前端的显示是
// 等待审核，有敏感内容。」
//
// ## 设计
//
//   - 每次 output/egress 硬阻断发生时，拦截现场把（租户、规则、原因、位置、
//     载荷摘要、请求 ID）登记成一条 pending 记录；客户端拿到的错误 message
//     里带上审核单号（rv-N）与「等待审核」的提示——这就是用户说的
//     「前端的显示是等待审核，有敏感内容」。
//   - 管理员在后台看到列表，批准 ⇒ 服务端写入一条 egress 放行记录
//     （AddEgressRelease，精确绑定到载荷摘要），**用户重试即拿到内容**；
//     拒绝 ⇒ 记录置为 rejected，拦截维持。
//   - 「审核完成后返回给前端」的实现语义是**重试即返回**，不是服务端缓存
//     被拦响应后推送：流式响应在拦截那一刻已经终止，无法（也不应该）在
//     审核通过后把陈旧的模型输出补发给客户端。
//
// ## 安全边界（与 egress_release.go 同一条线）
//
//   - 队列只在**网关进程内**，登记与决定都由服务端代码完成；客户端自带的
//     头/参数影响不了它。
//   - 记录里**没有载荷原文**：只有摘要（sha256，不可逆）与结构信息。被拦的
//     值可能是凭据，原文落库就是一个新的泄漏面（与 r1022「payload preview
//     不进日志」同一条纪律）。
//   - 批准的动作只是「放行」——它不绕过其它规则：同一次重试仍会过全部
//     检查，只是这条 (租户, 规则, 载荷) 不再命中。
//
// ## 持久化（如实记）
//
// 与放行表同一层：进程内、重启即清空。跨重启/多副本的持久化需要新表 +
// 启动迁移（编号要避开并行会话），不在本轮。管理面 UI 必须写明这一点。

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// BlockReview 是一次硬阻断的审核记录。
type BlockReview struct {
	// ID 是审核单号（进程内自增，形如 1、2、3…；展示为 rv-<ID>）。
	ID int64 `json:"id"`
	// TenantID 是被拦的租户（放行按租户生效，必须精确）。
	TenantID string `json:"tenant_id"`
	// Scope 目前只支持 "egress"（output 侧文本 lane 的规则级拦截没有
	// 载荷摘要，批准时退化为该租户该规则的整条放行）。
	Scope string `json:"scope"`
	// Rule 是命中的规则 ID。
	Rule string `json:"rule"`
	// Reason 是人类可读的原因短语（与客户端 message 同源）。
	Reason string `json:"reason"`
	// Field 是被拦载荷的位置（tool JSON 路径，如 $.command）。
	Field string `json:"field,omitempty"`
	// Digest 是被拦载荷的摘要；空串表示没有可绑定的载荷。
	Digest string `json:"digest,omitempty"`
	// RequestID 是网关请求 ID（与日志 / X-Request-Id 对账用）。
	RequestID string `json:"request_id,omitempty"`
	// Status ∈ {pending, approved, rejected}。
	Status string `json:"status"`
	// CreatedAt 是登记时间。
	CreatedAt time.Time `json:"created_at"`
	// DecidedBy / DecidedAt 是审核人与审核时间（未决为空）。
	DecidedBy string    `json:"decided_by,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
}

// 审核状态。
const (
	BlockReviewPending  = "pending"
	BlockReviewApproved = "approved"
	BlockReviewRejected = "rejected"
)

// blockReviewCapacity 是队列的环形上限。拦截风暴（规则误配置时每条请求
// 都拦）不许把网关内存打爆；最旧的 pending 记录被挤出后，同载荷的下一场
// 拦截会重新登记，语义不变。
const blockReviewCapacity = 1000

type blockReviewStore struct {
	mu      sync.Mutex
	next    int64
	entries []BlockReview
}

var blockReviews = &blockReviewStore{}

// AddBlockReview 登记一次硬阻断，返回审核单。
//
// 去重：同一 (tenant, scope, rule, digest, requestID) 已有 **pending** 记录时
// 不重复登记（同一次请求的重试路径 / 多 lane 拦截只占一单），返回既有单。
// 已决（approved/rejected）的同键记录会重新登记——批准是有时效的运营动作，
// 新的拦截是新的审核请求。
func AddBlockReview(tenantID, scope, rule, reason, field, digest, requestID string) BlockReview {
	if scope == "" {
		scope = "egress"
	}
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	for _, e := range blockReviews.entries {
		if e.Status == BlockReviewPending &&
			e.TenantID == tenantID && e.Scope == scope && e.Rule == rule &&
			e.Digest == digest && e.RequestID == requestID {
			return e
		}
	}
	blockReviews.next++
	entry := BlockReview{
		ID: blockReviews.next, TenantID: tenantID, Scope: scope, Rule: rule,
		Reason: reason, Field: field, Digest: digest, RequestID: requestID,
		Status: BlockReviewPending, CreatedAt: time.Now().UTC(),
	}
	blockReviews.entries = append(blockReviews.entries, entry)
	if len(blockReviews.entries) > blockReviewCapacity {
		blockReviews.entries = blockReviews.entries[len(blockReviews.entries)-blockReviewCapacity:]
	}
	return entry
}

// ListBlockReviews 列出审核记录（新→旧）。status 为空列出全部；
// tenantID 非空时只列该租户。limit ≤ 0 → 100，上限 1000。
func ListBlockReviews(status, tenantID string, limit int) []BlockReview {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	out := make([]BlockReview, 0, limit)
	for i := len(blockReviews.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := blockReviews.entries[i]
		if status != "" && e.Status != status {
			continue
		}
		if tenantID != "" && e.TenantID != tenantID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// GetBlockReview 按 ID 取一条记录。
func GetBlockReview(id int64) (BlockReview, bool) {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	for _, e := range blockReviews.entries {
		if e.ID == id {
			return e, true
		}
	}
	return BlockReview{}, false
}

// DecideBlockReview 审核一条记录：approve=true 时同步写入放行表。
//
// 只有 pending 记录可决；批准且带摘要 ⇒ 放行绑定到这一次载荷，未带摘要 ⇒
// 放行该租户该规则的全部载荷（此时管理面必须明确知道自己在开一个口子）。
func DecideBlockReview(id int64, approve bool, decidedBy string) (BlockReview, error) {
	blockReviews.mu.Lock()
	idx := -1
	for i, e := range blockReviews.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		blockReviews.mu.Unlock()
		return BlockReview{}, fmt.Errorf("%w: block review %d not found", ErrNotFound, id)
	}
	entry := blockReviews.entries[idx]
	if entry.Status != BlockReviewPending {
		blockReviews.mu.Unlock()
		return BlockReview{}, fmt.Errorf("%w: block review %d already %s", ErrConflict, id, entry.Status)
	}
	entry.Status = BlockReviewRejected
	if approve {
		entry.Status = BlockReviewApproved
	}
	entry.DecidedBy = strings.TrimSpace(decidedBy)
	entry.DecidedAt = time.Now().UTC()
	blockReviews.entries[idx] = entry
	blockReviews.mu.Unlock()

	if approve {
		// 放行表只支持 egress scope（output 侧没有按载荷放行的机制）。
		// 其它 scope 的批准只把记录置为 approved，不放行——列表里可见，
		// 运营语义是「已人工看过」，配合硬阻断清单调整。
		if entry.Scope == "egress" && entry.Rule != "" && entry.TenantID != "" {
			release := EgressRelease{
				TenantID:      entry.TenantID,
				Scope:         "egress",
				Rule:          entry.Rule,
				PayloadDigest: entry.Digest,
				ApprovedBy:    entry.DecidedBy,
			}
			if err := AddEgressRelease(release); err != nil {
				return entry, fmt.Errorf("approve block review %d: %w", id, err)
			}
		}
	}
	return entry, nil
}

// ResetBlockReviews 清空队列（测试用）。
func ResetBlockReviews() {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	blockReviews.entries = nil
	blockReviews.next = 0
}
