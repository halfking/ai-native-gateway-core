package sanitizerules

// 拦截审核工作台（2026-10-29 输出合规误报轮）。
//
// ## 这一层解决什么
//
// 211 生产实测（24h）：6 个请求被「输出合规」拦掉，每个已经流出 3806~10529 个
// chunk，客户端最终只拿到 801 字节；而 21 条审核记录（sanitize_block_reviews）
// 的 rule_id / reason / field_path / payload_digest **全部为空**。
//
// ⇒ 审核员面对的是一张**空白清单**：既说不出「因为什么规则被拦」，也看不到
// 「当时到底拦了什么内容」，更没法据此判断「这是规则太宽还是确实违规」。
// ⇒ 批准按钮也**打不开**问题：放行键是 (租户, 规则)，规则为空 ⇒ 批准等于
// 什么都没做 ⇒ 同一个误报无限复发。
//
// ## 本文件定下的三件事
//
//  1. **证据（原始会话内容）**：拦截那一刻把「被拦的那段内容」以**有界摘录**
//     （默认 4 KiB）写进证据表（sanitize_block_evidence，带 7 天 TTL）。
//     ★ 证据**单独一张表**——迁移 917 的契约是「审核记录表不含载荷原文」，
//     证据表是**另一张**表、另一套生命周期（TTL），不破坏那条契约。
//  2. **标注（label）**：与 approve/reject **正交**的一维。
//     approve/reject 回答「现在放不放行」，label 回答「这次拦得对不对」。
//     ⇒ 「误报」这个知识必须能被记下来，否则规则优化没有输入。
//  3. **自动优化（auto-relax）**：同租户同规则的 false_positive 标注累计到
//     阈值（默认 3）⇒ 自动写入**该租户该规则的 7 天放行**（source=auto_label）
//     ⇒ 这条规则对这个租户不再拦。与人工批准的放行**同键同语义**，可撤销、
//     会过期、可见。
//
// ## 为什么自动优化限定在「租户 + 规则 + 7 天」
//
// 规则是全局的（一份快照服务所有租户），但**误报是租户事实**（某个租户的
// 正常业务形态撞上了这条规则）。⇒ 自动动作必须收窄到「这个租户、这条规则、
// 一周」——与人工批准的放行键**逐字一致** ⇒ 两条路径写同一张表、同一套语义，
// 不会分叉。
//
// ⇒ 自动优化**不**改规则本身（改规则要人审、要发版、要回滚面）。它改的是
// 「这条规则对这个租户暂时不生效」——可逆、可见、有 TTL。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// 人工标注的闭集。
//
// ★ 与审核状态（pending/approved/rejected）**正交**：状态回答「现在放不放行」，
// 标注回答「这次拦得对不对」。一个已批准的审核单也可以标成 false_positive
// （规则太宽 ⇒ 该改规则）；一个被拒的也可以标成 true_positive（拦对了）。
const (
	// BlockReviewLabelFalsePositive：这次拦截是误报（内容本身不该被拦）。
	BlockReviewLabelFalsePositive = "false_positive"
	// BlockReviewLabelTruePositive：拦对了（内容确实违规），维持拦截。
	BlockReviewLabelTruePositive = "true_positive"
	// BlockReviewLabelRuleTooBroad：内容确实敏感，但**这条规则的判据太宽** ⇒
	// 该改规则（不是放行这一条）。
	BlockReviewLabelRuleTooBroad = "rule_too_broad"
	// BlockReviewLabelNeedsRule：拦得对，但**缺一条该有的规则**（漏检）⇒
	// 该加规则。
	BlockReviewLabelNeedsRule = "needs_rule"
)

// BlockReviewLabels 是允许的标注集合（闭集）。
func BlockReviewLabels() []string {
	return []string{
		BlockReviewLabelFalsePositive,
		BlockReviewLabelTruePositive,
		BlockReviewLabelRuleTooBroad,
		BlockReviewLabelNeedsRule,
	}
}

// ValidBlockReviewLabel 报告一个标注是否在闭集内。
func ValidBlockReviewLabel(label string) bool {
	label = strings.TrimSpace(label)
	if label == "" {
		return false
	}
	for _, known := range BlockReviewLabels() {
		if label == known {
			return true
		}
	}
	return false
}

// 证据的种类（闭集）——审核员据此判断「这段摘录代表什么」。
const (
	// BlockReviewEvidenceToolArgument：被拦的是工具调用的参数。
	BlockReviewEvidenceToolArgument = "tool_argument"
	// BlockReviewEvidenceAssistantText：被拦的是模型输出的正文。
	BlockReviewEvidenceAssistantText = "assistant_text"
	// BlockReviewEvidenceGuardCapacity：守卫**缓冲越界**（不是内容违规）——
	// 摘录是当时扣下不放的那批帧的头部。
	BlockReviewEvidenceGuardCapacity = "guard_capacity"
)

// BlockReviewEvidenceMaxBytes 是单条证据摘录的字节上限（默认 4 KiB）。
//
// 上限的理由：审核员要看到「拦的是什么」，但不该让管理面变成一个**载荷仓库**。
// 4 KiB 足够看清一段命令 / 一个参数值 / 一段文本的形态；超出的部分标 truncated。
const BlockReviewEvidenceMaxBytes = 4 << 10

// BlockReviewEvidenceTTL 是证据的保留期（7 天）。
//
// 与放行的 7 天 TTL 同量级：审核窗口期内证据都在，过期自动清。
const BlockReviewEvidenceTTL = 7 * 24 * time.Hour

// DefaultAutoRelaxThreshold 是「同租户同规则累计多少条误报标注 ⇒ 自动放行」
// 的出厂阈值。0 = 关闭自动优化。
const DefaultAutoRelaxThreshold = 3

// boundedEvidence 把一段内容裁成有界摘录，并报告是否被截断。
//
// ★ 按**字节**裁（不是 rune）：证据是给人看的片段，不是要按字符语义处理的
// 文本；按字节裁在 UTF-8 多字节字符中间切开时会产生半个字符——为避免这一点，
// 裁完再回退到最近的合法 rune 边界。
func boundedEvidence(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if len(text) <= BlockReviewEvidenceMaxBytes {
		return text, false
	}
	cut := BlockReviewEvidenceMaxBytes
	// 回退到 UTF-8 rune 边界（最多回退 3 字节）。
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// utf8RuneStart 报告字节是否是 UTF-8 编码的 rune 起始字节。
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// BlockReviewEvidence 是一条被拦内容的证据摘录。
type BlockReviewEvidence struct {
	ReviewID  int64     `json:"review_id"`
	TenantID  string    `json:"tenant_id"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	Bytes     int       `json:"bytes"`
	Truncated bool      `json:"truncated"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// BlockReviewRuleInsight 是「按规则聚合」的复核读数——规则优化的输入。
type BlockReviewRuleInsight struct {
	Rule           string `json:"rule"`
	RuleName       string `json:"rule_name,omitempty"`
	TenantID       string `json:"tenant_id,omitempty"`
	Total          int    `json:"total"`
	Pending        int    `json:"pending"`
	Approved       int    `json:"approved"`
	Rejected       int    `json:"rejected"`
	FalsePositive  int    `json:"false_positive"`
	TruePositive   int    `json:"true_positive"`
	RuleTooBroad   int    `json:"rule_too_broad"`
	NeedsRule      int    `json:"needs_rule"`
	Labeled        int    `json:"labeled"`
	FalsePositiveR float64 `json:"false_positive_ratio"`
	// AutoRelaxed 报告这条规则是否已被自动放行（该租户 7 天内不再拦）。
	AutoRelaxed bool `json:"auto_relaxed"`
	// AutoRelaxUntil 是自动放行的到期时刻（未自动放行时零值）。
	AutoRelaxUntil time.Time `json:"auto_relax_until,omitempty"`
}

// BlockReviewLabelInput 是「给一条审核单打标注」的入参。
type BlockReviewLabelInput struct {
	Label   string
	Comment string
	LabeledBy string
}

// LabelBlockReview 给一条审核单打标注。
//
// 与 DecideBlockReview 正交：标注不改变放行状态，只记录判断。
// 标注为 false_positive / rule_too_broad 时会触发**自动优化**判定
// （见 maybeAutoRelax）。
func LabelBlockReview(ctx context.Context, id int64, label, comment, labeledBy string) (BlockReview, error) {
	label = strings.TrimSpace(label)
	if !ValidBlockReviewLabel(label) {
		return BlockReview{}, fmt.Errorf("%w: unknown block review label %q", ErrInvalid, label)
	}
	labeledBy = strings.TrimSpace(labeledBy)
	now := time.Now().UTC()
	p := reviewPersistenceInstalled()
	if p != nil {
		entry, err := p.LabelBlockReviewDB(ctx, id, label, strings.TrimSpace(comment), labeledBy, now)
		if err != nil {
			return BlockReview{}, err
		}
		if label == BlockReviewLabelFalsePositive || label == BlockReviewLabelRuleTooBroad {
			if err := maybeAutoRelax(ctx, entry); err != nil {
				// 自动优化失败**不**让标注失败：标注本身已经落库，
				// 自动放行是**附加**效果（可由人工批准补上）。
				slog.Warn("sanitize block review auto-relax failed",
					"error", err, "review_id", id, "rule", entry.Rule, "tenant_id", entry.TenantID)
			}
		}
		return entry, nil
	}
	entry, err := labelBlockReviewMemory(id, label, comment, labeledBy, now)
	if err != nil {
		return BlockReview{}, err
	}
	// ★ 内存路径**同样**要走自动优化判定：两条路径的语义必须一致
	//（否则「没装 DB 的部署不会自动优化」这件事没人说得清）。
	if label == BlockReviewLabelFalsePositive || label == BlockReviewLabelRuleTooBroad {
		if err := maybeAutoRelax(ctx, entry); err != nil {
			slog.Warn("sanitize block review auto-relax failed",
				"error", err, "review_id", id, "rule", entry.Rule, "tenant_id", entry.TenantID)
		}
	}
	return entry, nil
}

func labelBlockReviewMemory(id int64, label, comment, labeledBy string, now time.Time) (BlockReview, error) {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	idx := -1
	for i, e := range blockReviews.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return BlockReview{}, fmt.Errorf("%w: block review %d not found", ErrNotFound, id)
	}
	entry := blockReviews.entries[idx]
	entry.Label = label
	entry.LabelComment = comment
	entry.LabeledBy = labeledBy
	entry.LabeledAt = now
	blockReviews.entries[idx] = entry
	return entry, nil
}

// AutoRelaxThresholdKey 是「同租户同规则累计多少条误报标注 ⇒ 自动放行」的设置键。
//
// 0 = 关闭自动优化。平台级、热加载。
const AutoRelaxThresholdKey = "sanitize.output_guard_autorelax_threshold"

// AutoRelaxThreshold 返回自动优化的阈值（同租户同规则的误报标注条数）。
//
// 0 = 关闭自动优化。设置键 sanitize.output_guard_autorelax_threshold。
//
// ★ 管理面读数要用它 ⇒ 导出（读侧与管理面必须看到**同一个**阈值 ⇒
//   否则运维看得懂「为什么还没自动放行」这件事就成了猜）。
func AutoRelaxThreshold() int {
	return settings.GetPlatformInt(AutoRelaxThresholdKey, DefaultAutoRelaxThreshold)
}

// maybeAutoRelax 在误报标注累计到阈值时，为「该租户该规则」写一条 7 天放行。
//
// ★ 只在**规则级**成立（与人工批准同键）：tenant + scope=egress + rule。
// 载荷摘要不进键 ⇒ 同一租户这条规则一周内一律不再拦（与 owner 口径
// 「同一客户端一周内的同样的规则将同样放行」一致）。
//
// ★ 合成规则（builtin.guard.capacity_exceeded）**不**参与自动放行：它不是
// 内容规则，放行它对业务没有意义（容量问题靠上限/开关解决）。
func maybeAutoRelax(ctx context.Context, entry BlockReview) error {
	if entry.Rule == "" || entry.TenantID == "" {
		return nil
	}
	if strings.HasPrefix(entry.Rule, "builtin.guard.") {
		return nil
	}
	threshold := AutoRelaxThreshold()
	if threshold <= 0 {
		return nil
	}
	count, err := countFalsePositiveLabels(ctx, entry.TenantID, entry.Rule)
	if err != nil {
		return err
	}
	if count < threshold {
		return nil
	}
	release := EgressRelease{
		TenantID:   entry.TenantID,
		Scope:      "egress",
		Rule:       entry.Rule,
		ApprovedBy: "auto-relax",
		ClientType: entry.ClientType,
		Source:     ReleaseSourceAutoLabel,
		ExpiresAt:  time.Now().UTC().Add(blockReviewReleaseTTL),
	}
	if err := AddEgressReleasePersistent(ctx, release); err != nil {
		return err
	}
	slog.Warn("sanitize block review auto-relaxed rule",
		"tenant_id", entry.TenantID,
		"rule", entry.Rule,
		"false_positive_labels", count,
		"threshold", threshold,
		"expires_at", release.ExpiresAt.Format(time.RFC3339),
	)
	return nil
}

// countFalsePositiveLabels 数同租户同规则的误报标注条数。
func countFalsePositiveLabels(ctx context.Context, tenantID, rule string) (int, error) {
	p := reviewPersistenceInstalled()
	if p == nil {
		blockReviews.mu.Lock()
		defer blockReviews.mu.Unlock()
		count := 0
		for _, e := range blockReviews.entries {
			if e.TenantID == tenantID && e.Rule == rule && e.Label == BlockReviewLabelFalsePositive {
				count++
			}
		}
		return count, nil
	}
	return p.CountBlockReviewLabelsDB(ctx, tenantID, rule, BlockReviewLabelFalsePositive)
}

// BlockReviewInsights 按规则聚合复核读数（规则优化的输入面）。
//
// 读数口径：**已标注**的记录按标注分桶 + 未标注的按状态分桶。
// 误报率 = false_positive / labeled（分母是**有判断**的记录数，不是总量）——
// 分母用总量会让「没人看的记录」把误报率稀释成假绿。
func BlockReviewInsights(tenantID string, limit int) ([]BlockReviewRuleInsight, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	p := reviewPersistenceInstalled()
	if p == nil {
		return blockReviewInsightsMemory(tenantID, limit), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	insights, err := p.BlockReviewInsightsDB(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	releases := EgressReleases()
	for i := range insights {
		for _, rel := range releases {
			if rel.Scope == "egress" && rel.Rule == insights[i].Rule &&
				(tenantID == "" || rel.TenantID == tenantID) && rel.Source == ReleaseSourceAutoLabel {
				insights[i].AutoRelaxed = true
				if rel.ExpiresAt.After(insights[i].AutoRelaxUntil) {
					insights[i].AutoRelaxUntil = rel.ExpiresAt
				}
			}
		}
	}
	return insights, nil
}

func blockReviewInsightsMemory(tenantID string, limit int) []BlockReviewRuleInsight {
	blockReviews.mu.Lock()
	defer blockReviews.mu.Unlock()
	byRule := map[string]*BlockReviewRuleInsight{}
	order := []string{}
	for _, e := range blockReviews.entries {
		// ★ 降级放行单不进读数（与 DB 路径 WHERE NOT degraded 同口径）：
		// 它是「守卫让步」的遥测，不是规则反馈。
		if e.Degraded {
			continue
		}
		if tenantID != "" && e.TenantID != tenantID {
			continue
		}
		// ★ 与 DB 路径同口径：空规则归到哨兵桶（否则内存降级路径会把
		// 「没有归因」的记录单列成一条空规则，与 DB 路径的读数对不上）。
		bucket := e.Rule
		if bucket == "" {
			bucket = unattributedRuleBucket
		}
		key := e.TenantID + "|" + bucket
		insight, ok := byRule[key]
		if !ok {
			insight = &BlockReviewRuleInsight{Rule: bucket, RuleName: e.RuleName, TenantID: e.TenantID}
			byRule[key] = insight
			order = append(order, key)
		}
		insight.Total++
		switch e.Status {
		case BlockReviewPending:
			insight.Pending++
		case BlockReviewApproved:
			insight.Approved++
		case BlockReviewRejected:
			insight.Rejected++
		}
		switch e.Label {
		case BlockReviewLabelFalsePositive:
			insight.FalsePositive++
			insight.Labeled++
		case BlockReviewLabelTruePositive:
			insight.TruePositive++
			insight.Labeled++
		case BlockReviewLabelRuleTooBroad:
			insight.RuleTooBroad++
			insight.Labeled++
		case BlockReviewLabelNeedsRule:
			insight.NeedsRule++
			insight.Labeled++
		}
	}
	out := make([]BlockReviewRuleInsight, 0, len(order))
	for _, key := range order {
		insight := byRule[key]
		if insight.Labeled > 0 {
			insight.FalsePositiveR = float64(insight.FalsePositive) / float64(insight.Labeled)
		}
		out = append(out, *insight)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
