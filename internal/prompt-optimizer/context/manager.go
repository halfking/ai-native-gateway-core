// Package context 负责为提示词优化器准备"上下文"。
//
// 数据来源是仓库既有的记忆后端（memora），走 domains/memory 的 Reader 接口，
// 而不是另造一套 HTTP 客户端 —— 这样生产接线时可以直接把
// domains/memory/client.Client 传进来，无需二次适配。
//
// 三层职责：
//   - Manager：实现 optimizer.ContextManager，向策略提供历史、示例、约束；
//   - Window：按 token 预算裁剪历史，避免把整个会话历史塞进提示词；
//   - Dynamic：按任务类型/领域动态拼装上下文骨架。
package context

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// Config 上下文管理器配置。
type Config struct {
	// Enabled 是否启用记忆检索。false 时所有查询直接返回空，不产生任何远端调用。
	Enabled bool

	// UserID 记忆归属用户。记忆后端按 user 隔离，跨用户检索会拿到无关内容。
	UserID string

	// MaxHistory 参与拼装的最大历史条数。
	MaxHistory int

	// SimilarityThreshold 相似度阈值，低于该值的记忆直接丢弃。
	SimilarityThreshold float64

	// MaxContextTokens 历史上下文的 token 预算上限。
	MaxContextTokens int

	// UseSmartSearch 是否使用后端的智能检索（若后端支持）。
	UseSmartSearch bool
}

// DefaultConfig 默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		MaxHistory:          10,
		SimilarityThreshold: 0.0,
		MaxContextTokens:    2000,
		UseSmartSearch:      false,
	}
}

// Manager 基于 memory.Reader 的上下文管理器。
type Manager struct {
	reader memory.Reader
	cfg    Config
}

// NewManager 构造上下文管理器。
//
// reader 为 nil 时退化为"无历史"模式：所有查询返回空切片而非报错，
// 这样未接入记忆后端的环境仍能使用优化器（只是优化效果退化为纯结构化增强）。
func NewManager(reader memory.Reader, cfg Config) *Manager {
	if cfg.MaxHistory <= 0 {
		cfg.MaxHistory = DefaultConfig().MaxHistory
	}
	if cfg.MaxContextTokens <= 0 {
		cfg.MaxContextTokens = DefaultConfig().MaxContextTokens
	}
	return &Manager{reader: reader, cfg: cfg}
}

// GetRelevantHistory 返回与当前意图相关的历史提示词片段。
//
// 契约：
//   - 后端不可用（reader 为 nil 或 Disabled）⇒ 返回空切片 + nil error（降级，不阻断优化）；
//   - 后端调用失败 ⇒ 返回错误，让上层能区分"没有历史"与"查历史失败"。
func (m *Manager) GetRelevantHistory(ctx context.Context, intent *optimizer.Intent) ([]string, error) {
	if m == nil || !m.cfg.Enabled {
		return nil, nil
	}
	if m.reader == nil || m.reader.Disabled() {
		return nil, nil
	}
	if intent == nil {
		return nil, errors.New("context: intent must not be nil")
	}

	query := buildQuery(intent)

	var (
		hits []memory.Memory
		err  error
	)
	if m.cfg.UseSmartSearch {
		hits, err = m.reader.SmartSearch(ctx, m.cfg.UserID, query, m.cfg.MaxHistory)
	} else {
		hits, err = m.reader.Search(ctx, m.cfg.UserID, query, m.cfg.MaxHistory)
	}
	if err != nil {
		return nil, fmt.Errorf("context: memory search failed: %w", err)
	}

	return m.filterAndBudget(hits), nil
}

// GetExamples 返回可作为 few-shot 参考的历史示例。
func (m *Manager) GetExamples(ctx context.Context, taskType, domain string) ([]optimizer.Example, error) {
	if m == nil || !m.cfg.Enabled {
		return nil, nil
	}
	if m.reader == nil || m.reader.Disabled() {
		return nil, nil
	}

	query := buildQuery(&optimizer.Intent{TaskType: taskType, Domain: domain})
	hits, err := m.reader.Search(ctx, m.cfg.UserID, query, m.cfg.MaxHistory)
	if err != nil {
		return nil, fmt.Errorf("context: memory search failed: %w", err)
	}

	examples := make([]optimizer.Example, 0, len(hits))
	for _, hit := range hits {
		text := strings.TrimSpace(hit.Text)
		if text == "" {
			continue
		}
		examples = append(examples, optimizer.Example{
			Input:       text,
			Output:      hit.Text,
			Quality:     hit.Score,
			Explanation: strings.Join(hit.Tags, ", "),
		})
	}
	return examples, nil
}

// GetConstraints 返回领域相关的约束条件。
//
// 约束来自本包内置的领域规则表 —— 它是"确定性事实"，不应交给记忆后端
// （后端返回的是相似度排序的事实片段，不保证是约束）。
func (m *Manager) GetConstraints(ctx context.Context, domain string) ([]string, error) {
	return DomainConstraints(domain), nil
}

// filterAndBudget 按阈值过滤并按 token 预算裁剪。
func (m *Manager) filterAndBudget(hits []memory.Memory) []string {
	filtered := make([]memory.Memory, 0, len(hits))
	for _, hit := range hits {
		if hit.Score < m.cfg.SimilarityThreshold {
			continue
		}
		if strings.TrimSpace(hit.Text) == "" {
			continue
		}
		filtered = append(filtered, hit)
	}

	// 得分高的优先保留。
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Score > filtered[j].Score
	})

	w := NewWindow(m.cfg.MaxContextTokens)
	budgeted := make([]string, 0, len(filtered))
	for _, hit := range filtered {
		text := strings.TrimSpace(hit.Text)
		if !w.Add(text) {
			// 预算耗尽：后续更长的片段必然也放不下，直接停止。
			break
		}
		budgeted = append(budgeted, text)
	}
	return budgeted
}

// buildQuery 构造检索 query。
//
// 记忆后端按语义相似度检索，query 里带上任务类型与领域能显著提高召回精度。
func buildQuery(intent *optimizer.Intent) string {
	parts := []string{}
	if intent.TaskType != "" {
		parts = append(parts, intent.TaskType)
	}
	if intent.Domain != "" && intent.Domain != "general" {
		parts = append(parts, intent.Domain)
	}
	if len(intent.Constraints) > 0 {
		parts = append(parts, strings.Join(intent.Constraints, " "))
	}
	if len(parts) == 0 {
		return "prompt optimization"
	}
	return strings.Join(parts, " ")
}

// DomainConstraints 返回领域内置约束。
func DomainConstraints(domain string) []string {
	base := []string{
		"输出必须完整覆盖任务要求，不允许省略关键步骤",
		"禁止编造未在输入中给出的事实",
	}
	switch strings.ToLower(domain) {
	case "medical":
		return append(base, "不得给出诊断结论或用药建议", "健康数据必须按最小必要原则处理")
	case "legal":
		return append(base, "不得替代律师出具正式法律意见", "引用法条必须标注来源与生效版本")
	case "financial":
		return append(base, "不得给出确定性收益承诺", "金额与比率必须标注口径与计算方式")
	case "technical":
		return append(base, "代码示例必须可直接运行并标注语言与版本")
	case "security":
		return append(base, "不得输出可直接用于攻击的可执行载荷", "漏洞描述必须限定在授权范围内")
	default:
		return base
	}
}
