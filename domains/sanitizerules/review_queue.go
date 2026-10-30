package sanitizerules

// 拦截审核队列（2026-10-28 拦截反馈明确化轮；2026-10-29 拦截持久化轮落库）。
//
// ## owner 口径（需求的直接原话）
//
// 「对触发规则的，需要有记录，并可以人工进行通过，让执行继续。并且一旦
// 审批通过，对于同一客户端的一周内的同样的规则将同样放行。」
//
// ## 设计
//
//   - 每次 output/egress 硬阻断发生时，拦截现场把（租户、客户端、规则、
//     规则名、原因、位置、载荷摘要、请求 ID、匹配说明）登记成一条 pending
//     记录；客户端拿到的错误 message 里带审核单号（rv-N）与「等待审核」的
//     提示。
//   - 管理员在后台看到列表（**落库**——迁移 917 的 sanitize_block_reviews），
//     批准 ⇒ 服务端写入一条规则级放行（租户 + 规则，**不带载荷摘要**，
//     expires_at = 批准 + 7 天，sanitize_egress_releases + 进程内 atomic
//     表），**同一租户一周内同规则不再拦**；拒绝 ⇒ 记录置为 rejected，
//     拦截维持。
//   - 「审核完成后返回给前端」的实现语义是**重试即返回**：流式响应在拦截
//     那一刻已经终止，无法（也不应该）在审核通过后把陈旧的模型输出补发。
//
// ## 安全边界（与 egress_release.go 同一条线）
//
//   - 队列的登记与决定都由服务端代码完成；客户端自带的头/参数影响不了它。
//   - 记录里**没有载荷原文**：只有摘要（sha256，不可逆）与说明文字。
//   - 批准的动作只是「该规则放行」——同一次重试仍会过全部其它检查。
//
// ## 持久化
//
// InstallReviewPersistence 安装落库通道后，记录/列表/决定全走 DB；未安装
// （测试、无 DB 场景）保持进程内行为。DB 故障时登记降级进程内（拦截主路径
// 不因记录失败而中断——但会丢这单，WARN 可见）。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// BlockReview 是一次硬阻断的审核记录。
type BlockReview struct {
	// ID 是审核单号（进程内自增或 DB 序列；展示为 rv-<ID>）。
	ID int64 `json:"id"`
	// TenantID 是被拦的租户（放行按租户生效，必须精确）。
	TenantID string `json:"tenant_id"`
	// ClientType 是拦截现场的客户端类型（extractClientType 的输出；
	// 展示与追溯用，放行键按租户——见 block_review_persist.go）。
	ClientType string `json:"client_type,omitempty"`
	// Scope 目前只支持 "egress"（output 侧文本 lane 的规则级拦截没有
	// 载荷摘要，批准时退化为该租户该规则的整条放行）。
	Scope string `json:"scope"`
	// Rule 是命中的规则 ID。
	Rule string `json:"rule"`
	// RuleName 是规则的展示名（builtin 走固定说明表；托管规则由前端按
	// rule_id 关联规则配置）。
	RuleName string `json:"rule_name,omitempty"`
	// Reason 是人类可读的原因短语（与客户端 message 同源）。
	Reason string `json:"reason"`
	// Field 是被拦载荷的位置（tool JSON 路径，如 $.command）。
	Field string `json:"field,omitempty"`
	// Digest 是被拦载荷的摘要；空串表示没有可绑定的载荷。
	Digest string `json:"digest,omitempty"`
	// RequestID 是网关请求 ID（与日志 / X-Request-Id 对账用）。
	RequestID string `json:"request_id,omitempty"`
	// MatchDetail 是给审核员看的匹配说明（「检查是否是规则问题」的入口：
	// 规则语义 + 命中位置 + 判定路径；前端再按 rule_id 关联 pattern 全文）。
	MatchDetail string `json:"match_detail,omitempty"`
	// Status ∈ {pending, approved, rejected}。
	Status string `json:"status"`
	// CreatedAt 是登记时间。
	CreatedAt time.Time `json:"created_at"`
	// DecidedBy / DecidedAt 是审核人与审核时间（未决为空）。
	DecidedBy string    `json:"decided_by,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
	// SessionID 是被拦请求的会话 ID（原始会话内容的入口：审核员可据此
	// 打开该会话的详情页看上下文）。2026-10-29 输出合规误报轮补。
	SessionID string `json:"session_id,omitempty"`
	// Label 是人工标注（闭集见 BlockReviewLabels）：这次拦得对不对。
	// ★ 与 Status 正交：状态回答「现在放不放行」，标注回答「拦得对不对」。
	Label string `json:"label,omitempty"`
	// LabelComment / LabeledBy / LabeledAt 是标注的说明与责任人。
	LabelComment string    `json:"label_comment,omitempty"`
	LabeledBy    string    `json:"labeled_by,omitempty"`
	LabeledAt    time.Time `json:"labeled_at,omitempty"`
	// Evidence 是被拦内容的**有界摘录**（原始会话内容），由证据表按需装配
	// （不在本表落原文——迁移 917 的契约）。EvidenceKind 说明摘录代表什么。
	Evidence          string `json:"evidence,omitempty"`
	EvidenceKind      string `json:"evidence_kind,omitempty"`
	EvidenceTruncated bool   `json:"evidence_truncated,omitempty"`
	// Degraded 标记「这条记录对应的是一次**降级放行**，不是硬阻断」
	// （迁移 926；2026-10-30 输出合规审计轮）。
	//
	// ★ 为什么必须有这一位：守卫缓冲越界在 release 模式下**放行**了响应
	// （客户端已拿到内容），921 却让它与硬阻断共用 pending 状态与同一张表
	// ⇒ 211 生产以 ~230 条/天把审核面灌成降级遥测，且它挂着的批准按钮
	// 写出的放行行没有任何消费方（死操作）。⇒ 降级单保留**可见性**
	// （921 的留痕设计不回退），但语义回到遥测：pending_count 不计它、
	// 读数分母不含它、前端渲染成只读行。
	Degraded bool `json:"degraded,omitempty"`
}

// 审核状态。
const (
	BlockReviewPending  = "pending"
	BlockReviewApproved = "approved"
	BlockReviewRejected = "rejected"
)

// blockReviewCapacity 是**内存回退队列**的环形上限（仅未安装持久化时使用；
// DB-backed 时记录量由 sanitize_block_reviews 本身承担，按查询 limit 截断）。
const blockReviewCapacity = 1000

type blockReviewStore struct {
	mu      sync.Mutex
	next    int64
	entries []BlockReview
}

var blockReviews = &blockReviewStore{}

// BlockReviewInput 是登记一次硬阻断审核单的入参。
//
// ★ 用结构体而不是十个位置参数：审核单现在有 13 个字段（2026-10-29 输出合规
// 误报轮补了 session_id / evidence），位置参数在那种长度下**读错位不会报错**
// ⇒ 会写出「规则名写进了原因字段」这种脏数据（而且审核单是空白的，更难发现）。
type BlockReviewInput struct {
	TenantID   string
	ClientType string
	Scope      string
	Rule       string
	RuleName   string
	Reason     string
	Field      string
	Digest     string
	RequestID  string
	// MatchDetail 是给审核员看的匹配说明。
	MatchDetail string
	// SessionID 是被拦请求的会话 ID（原始会话内容的入口）。
	SessionID string
	// EvidenceKind / Evidence 是被拦内容的**有界摘录**（原始会话内容）。
	// 证据单独落证据表（带 TTL），本表只落摘要与说明。
	EvidenceKind string
	Evidence     string
	// Degraded 是「降级放行」标记（迁移 926）：登记方（streaming finish 的
	// 降级路径）必须置位，硬阻断路径保持 false。
	Degraded bool
}

// AddBlockReview 登记一次硬阻断，返回审核单。
//
// 去重：同一 (tenant, scope, rule, digest, requestID) 已有 **pending** 记录时
// 不重复登记（同一次请求的重试路径 / 多 lane 拦截只占一单）。持久化通道
// 已安装时先查 DB 再插 DB；DB 查询失败降级内存（拦截主路径不因记录失败
// 中断）。
func AddBlockReview(in BlockReviewInput) BlockReview {
	scope := in.Scope
	if scope == "" {
		scope = "egress"
	}
	evidence, evidenceTruncated := boundedEvidence(in.Evidence)
	if p := reviewPersistenceInstalled(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if existing, found, err := p.FindPendingBlockReview(ctx, in.TenantID, scope, in.Rule, in.Digest, in.RequestID); err == nil && found {
			return existing
		} else if err != nil {
			slog.Warn("sanitize block review dedupe query failed; inserting new row",
				"error", err, "rule", in.Rule, "request_id", in.RequestID)
		}
		entry := BlockReview{
			TenantID: in.TenantID, ClientType: in.ClientType, Scope: scope, Rule: in.Rule,
			RuleName: in.RuleName, Reason: in.Reason, Field: in.Field, Digest: in.Digest,
			RequestID: in.RequestID, MatchDetail: in.MatchDetail, SessionID: in.SessionID,
			Status: BlockReviewPending, CreatedAt: time.Now().UTC(),
			Degraded: in.Degraded,
		}
		inserted, err := p.InsertBlockReview(ctx, entry)
		if err != nil {
			slog.Warn("sanitize block review persist failed; review falls back to in-process queue",
				"error", err, "rule", in.Rule, "request_id", in.RequestID)
		} else {
			if evidence != "" {
				if err := p.InsertBlockReviewEvidence(ctx, BlockReviewEvidence{
					ReviewID: inserted.ID, TenantID: in.TenantID, Kind: in.EvidenceKind,
					Content: evidence, Bytes: len(evidence), Truncated: evidenceTruncated,
					CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(BlockReviewEvidenceTTL),
				}); err != nil {
					slog.Warn("sanitize block review evidence persist failed",
						"error", err, "review_id", inserted.ID)
				} else {
					inserted.Evidence = evidence
					inserted.EvidenceKind = in.EvidenceKind
					inserted.EvidenceTruncated = evidenceTruncated
				}
			}
			return inserted
		}
	}
	return addBlockReviewMemory(in, scope, evidence, evidenceTruncated)
}

// addBlockReviewMemory 进程内回退路径（原第 33 增量行为）。
func addBlockReviewMemory(in BlockReviewInput, scope, evidence string, evidenceTruncated bool) BlockReview {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	for _, e := range blockReviews.entries {
		if e.Status == BlockReviewPending &&
			e.TenantID == in.TenantID && e.Scope == scope && e.Rule == in.Rule &&
			e.Digest == in.Digest && e.RequestID == in.RequestID {
			return e
		}
	}
	blockReviews.next++
	entry := BlockReview{
		ID: blockReviews.next, TenantID: in.TenantID, ClientType: in.ClientType,
		Scope: scope, Rule: in.Rule, RuleName: in.RuleName, Reason: in.Reason,
		Field: in.Field, Digest: in.Digest, RequestID: in.RequestID,
		MatchDetail: in.MatchDetail, SessionID: in.SessionID,
		Evidence: evidence, EvidenceKind: in.EvidenceKind, EvidenceTruncated: evidenceTruncated,
		Status: BlockReviewPending, CreatedAt: time.Now().UTC(),
		Degraded: in.Degraded,
	}
	blockReviews.entries = append(blockReviews.entries, entry)
	if len(blockReviews.entries) > blockReviewCapacity {
		blockReviews.entries = blockReviews.entries[len(blockReviews.entries)-blockReviewCapacity:]
	}
	return entry
}

// ListBlockReviews 列出审核记录（新→旧）。status 为空列出全部；
// tenantID 非空时只列该租户。limit ≤ 0 → 100，上限 1000。
// 持久化通道已安装时读 DB（含历史），否则读进程内环形队列。
func ListBlockReviews(status, tenantID string, limit int) []BlockReview {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if p := reviewPersistenceInstalled(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// ★ 有界重试一次：瞬时读库抖动不该让审核面「整页空白」（211 生产实测：
		// 一次瞬时失败 ⇒ 审核单列表恒空 ⇒ 审核员看到的是「没有待复核」）。
		rows, err := p.ListBlockReviewsDB(ctx, status, tenantID, limit)
		if err != nil {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
			}
			rows, err = p.ListBlockReviewsDB(ctx, status, tenantID, limit)
		}
		if err == nil {
			attachBlockReviewEvidence(ctx, p, rows)
			return rows
		}
		slog.Warn("sanitize block review list from db failed; falling back to in-process queue",
			"error", err, "root_cause", storeErrorDetail(err))
	}
	return listBlockReviewsMemory(status, tenantID, limit)
}

// CountBlockReviews 数符合条件的审核单（★ 列表端点的「待处理数」用）。
//
// ★ 2026-10-29：★ 旧实现是「再列一次（limit=1000）然后取 len」⇒ ★★ 那是一次
//
//	**全表读**，而列表端点本来就已经列过一次 ⇒ ★★ 每次打开审核面都读两遍
//	全表 ⇒ ★★ 改成一个 count(*)（★ DB 路径）/ 内存计数（★ 降级路径）★★。
func CountBlockReviews(status, tenantID string) int {
	if p := reviewPersistenceInstalled(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n, err := p.CountBlockReviewsDB(ctx, status, tenantID); err == nil {
			return n
		} else {
			slog.Warn("sanitize block review count from db failed; falling back to in-process count",
				"error", err, "root_cause", storeErrorDetail(err))
		}
	}
	return countBlockReviewsMemory(status, tenantID)
}

// countBlockReviewsMemory 在进程内队列里数（降级路径）。
func countBlockReviewsMemory(status, tenantID string) int {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	count := 0
	for _, e := range blockReviews.entries {
		if status != "" && e.Status != status {
			continue
		}
		if tenantID != "" && e.TenantID != tenantID {
			continue
		}
		count++
	}
	return count
}

// CountPendingBlockDecisions 数「待人工决断」的拦截单：pending 且**非降级**。
//
// ★ 管理面的「待审数」必须用这个口径而不是 CountBlockReviews(pending)：
// 降级放行单（Degraded，迁移 926）虽然 status=pending，但它对应的响应
// **已经成功送达客户端**，没有任何决断可做（批准写出的放行行没有消费方）
// ⇒ 把它计进待审数，徽标数字就不再是「有多少拦截等人看」
// （211 实测：24h 内 226/226 条全是降级单，徽标整天闪着假警报）。
func CountPendingBlockDecisions(tenantID string) int {
	if p := reviewPersistenceInstalled(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n, err := p.CountPendingBlockDecisionsDB(ctx, tenantID); err == nil {
			return n
		} else {
			slog.Warn("sanitize block review pending-decision count failed; falling back to in-process count",
				"error", err, "root_cause", storeErrorDetail(err))
		}
	}
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	count := 0
	for _, e := range blockReviews.entries {
		if e.Status != BlockReviewPending || e.Degraded {
			continue
		}
		if tenantID != "" && e.TenantID != tenantID {
			continue
		}
		count++
	}
	return count
}

// attachBlockReviewEvidence 把证据摘录装进审核单（原始会话内容）。
//
// ★ 证据在**另一张表**（sanitize_block_evidence，带 TTL）⇒ 这里按 ID 批量取，
// 一次往返。取不到证据不是错误（记录可能早于 921 迁移，或证据已过期）。
func attachBlockReviewEvidence(ctx context.Context, p ReviewPersistence, rows []BlockReview) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	evidence, err := p.ListBlockReviewEvidenceDB(ctx, ids)
	if err != nil {
		slog.Warn("sanitize block review evidence read failed; showing records without evidence",
			"error", err, "root_cause", storeErrorDetail(err))
		return
	}
	for i := range rows {
		e, ok := evidence[rows[i].ID]
		if !ok {
			continue
		}
		rows[i].Evidence = e.Content
		rows[i].EvidenceKind = e.Kind
		rows[i].EvidenceTruncated = e.Truncated
	}
}

func listBlockReviewsMemory(status, tenantID string, limit int) []BlockReview {
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
	if p := reviewPersistenceInstalled(); p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if row, err := p.GetBlockReviewDB(ctx, id); err == nil {
			return row, true
		}
	}
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	for _, e := range blockReviews.entries {
		if e.ID == id {
			return e, true
		}
	}
	return BlockReview{}, false
}

// DecideBlockReview 审核一条记录：approve=true 时同步写入**规则级 7 天放行**
// （租户 + 规则，不带载荷摘要——用户口径「同一客户端一周内的同样的规则
// 将同样放行」）。
//
// 只有 pending 记录可决；批准后 DB 行与进程内放行表同时生效（写穿透）。
func DecideBlockReview(id int64, approve bool, decidedBy string) (BlockReview, error) {
	return DecideBlockReviewWithContext(context.Background(), id, approve, decidedBy)
}

// DecideBlockReviewWithContext 同 DecideBlockReview，但接受外部 ctx
// （管理面请求超时可控）。
func DecideBlockReviewWithContext(ctx context.Context, id int64, approve bool, decidedBy string) (BlockReview, error) {
	status := BlockReviewRejected
	if approve {
		status = BlockReviewApproved
	}
	decidedBy = strings.TrimSpace(decidedBy)
	now := time.Now().UTC()

	if p := reviewPersistenceInstalled(); p != nil {
		entry, err := p.DecideBlockReviewDB(ctx, id, status, decidedBy, now)
		if err != nil {
			return BlockReview{}, err
		}
		if approve {
			if err := grantReviewRelease(ctx, p, entry, decidedBy); err != nil {
				return entry, err
			}
		}
		return entry, nil
	}
	// 内存回退路径。
	entry, err := decideBlockReviewMemory(id, status, decidedBy, now)
	if err != nil {
		return BlockReview{}, err
	}
	if approve {
		if err := grantReviewRelease(ctx, reviewPersistenceInstalled(), entry, decidedBy); err != nil {
			return entry, err
		}
	}
	return entry, nil
}

// grantReviewRelease 写入审核批准产生的规则级 7 天放行（DB + 进程内）。
func grantReviewRelease(ctx context.Context, p ReviewPersistence, entry BlockReview, decidedBy string) error {
	if entry.Scope != "egress" || entry.Rule == "" || entry.TenantID == "" {
		// 非放行可表达的 scope：只置状态（「已人工看过」）。
		return nil
	}
	release := EgressRelease{
		TenantID:   entry.TenantID,
		Scope:      "egress",
		Rule:       entry.Rule,
		ApprovedBy: decidedBy,
		ClientType: entry.ClientType,
		Source:     "review",
		ExpiresAt:  time.Now().UTC().Add(blockReviewReleaseTTL),
	}
	if p != nil {
		if err := p.UpsertEgressReleaseDB(ctx, release); err != nil {
			return fmt.Errorf("approve block review %d: %w", entry.ID, err)
		}
	}
	// 进程内表无脑更新（重复 Add 同键覆盖）。
	return AddEgressRelease(release)
}

func decideBlockReviewMemory(id int64, status, decidedBy string, now time.Time) (BlockReview, error) {
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
	entry.Status = status
	entry.DecidedBy = decidedBy
	entry.DecidedAt = now
	blockReviews.entries[idx] = entry
	blockReviews.mu.Unlock()
	return entry, nil
}

// ResetBlockReviews 清空内存队列与持久化通道（测试用）。
func ResetBlockReviews() {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	blockReviews.entries = nil
	blockReviews.next = 0
	ResetReviewPersistence()
}
