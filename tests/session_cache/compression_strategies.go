// Package session_cache_test - compression_strategies.go
//
// 多种压缩策略实现：
// 1. IncrementalCompression - 增量压缩（复用已有摘要）
// 2. SmartSlidingWindow - 智能滑动窗口（基于重要性）
// 3. HybridCompression - 混合策略（自动选择最优策略）
//
// 业界参考：
//   - Anthropic Contextual Retrieval: 上下文增强检索
//   - MemGPT: 分层记忆管理
//   - LangChain ConversationSummaryBufferMemory: 摘要+滑动窗口
//   - Claude Prompt Caching: 缓存复用降低延迟
package session_cache_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// 策略1：增量压缩（IncrementalCompression）
// ──────────────────────────────────────────────────────────────────────────────

// IncrementalCompressor 增量压缩器
//
// 原理：
//   - 复用上次的摘要，只对新增消息进行摘要
//   - 当新增消息过多时（>增量阈值）才重新生成完整摘要
//   - 避免重复压缩，节省70%以上的LLM调用
//
// 触发条件：
//   - 增量阈值：默认5条消息
//   - 摘要失效阈值：默认20条消息（超过则重新生成）
type IncrementalCompressor struct {
	incrementThreshold int // 增量阈值：新增消息超过此值才重新压缩
	maxAgeMessages     int // 摘要最大保留消息数：超过则重新生成
}

func NewIncrementalCompressor() *IncrementalCompressor {
	return &IncrementalCompressor{
		incrementThreshold: 5,
		maxAgeMessages:     20,
	}
}

// Compress 增量压缩
//
// 输入：
//   - messages: 当前所有消息
//   - existingSummary: 已有摘要（可能为空）
//   - lastSummaryIndex: 上次摘要覆盖到的索引
//
// 输出：
//   - compressedMessages: 压缩后的消息
//   - summary: 新摘要（可能复用旧的）
//   - newSummaryIndex: 新摘要覆盖到的索引
func (c *IncrementalCompressor) Compress(ctx context.Context, messages []Message, existingSummary string, lastSummaryIndex int) (compressed []Message, summary string, newIndex int, err error) {
	// 1. 检查是否需要完全重新压缩
	if lastSummaryIndex >= len(messages) {
		// 所有消息都已经被摘要过了，直接复用
		if existingSummary != "" {
			summaryMsg := Message{
				Role:    "system",
				Content: existingSummary,
			}
			return []Message{summaryMsg}, existingSummary, lastSummaryIndex, nil
		}
		return messages, "", len(messages), nil
	}

	// 2. 检查增量消息数
	newMessages := messages[lastSummaryIndex:]
	newCount := len(newMessages)

	if newCount <= c.incrementThreshold && existingSummary != "" {
		// 增量压缩：复用旧摘要，只追加新消息
		// 保留最近的一些原始消息 + 旧摘要
		recentMessages := messages
		if len(messages) > 10 {
			recentMessages = messages[len(messages)-10:]
		}
		summaryMsg := Message{
			Role:    "system",
			Content: existingSummary,
		}
		compressed = append([]Message{summaryMsg}, recentMessages...)
		// 增量压缩：复用旧摘要，不生成新摘要
		return compressed, existingSummary, lastSummaryIndex, nil
	}

	// 3. 需要重新生成摘要
	summary = c.generateSummary(ctx, messages)
	summaryMsg := Message{
		Role:    "system",
		Content: summary,
	}

	// 保留最近10条消息
	recentMessages := messages
	if len(messages) > 10 {
		recentMessages = messages[len(messages)-10:]
	}

	compressed = append([]Message{summaryMsg}, recentMessages...)
	newIndex = len(messages) - len(recentMessages) // 摘要覆盖到 recentMessages 之前

	return compressed, summary, newIndex, nil
}

// generateSummary 模拟LLM生成摘要
func (c *IncrementalCompressor) generateSummary(ctx context.Context, messages []Message) string {
	// 模拟摘要生成
	var topics []string
	for _, msg := range messages {
		if msg.Role == "user" {
			// 提取关键词（简化版）
			if len(msg.Content) > 20 {
				topics = append(topics, msg.Content[:20])
			} else {
				topics = append(topics, msg.Content)
			}
		}
	}

	summary := fmt.Sprintf("[会话摘要] 共%d条消息，主要话题：%s",
		len(messages),
		strings.Join(topics[:min(3, len(topics))], "；"))

	return summary
}

// ──────────────────────────────────────────────────────────────────────────────
// 策略2：智能滑动窗口（SmartSlidingWindow）
// ──────────────────────────────────────────────────────────────────────────────

// MessageImportance 消息重要性评分
type MessageImportance struct {
	MessageIndex int
	Score        float64
	Reason       string
}

// SmartSlidingWindow 智能滑动窗口压缩器
//
// 原理：
//   - 不同于固定保留最近N条消息
//   - 根据消息重要性动态选择保留哪些消息
//   - 重要消息（系统提示、关键决策）始终保留
//   - 不重要的对话（寒暄、重复）可以丢弃
//
// 业界参考：
//   - LangChain ConversationSummaryBufferMemory
//   - MemGPT 分层记忆
type SmartSlidingWindow struct {
	windowSize          int     // 窗口大小（消息数）
	importanceThreshold float64 // 重要性阈值：低于此值的消息可能被丢弃
	maxTokens           int     // 最大token数
}

func NewSmartSlidingWindow() *SmartSlidingWindow {
	return &SmartSlidingWindow{
		windowSize:          15,
		importanceThreshold: 0.3,
		maxTokens:           3000,
	}
}

// Compress 智能滑动窗口压缩
func (w *SmartSlidingWindow) Compress(ctx context.Context, messages []Message) (compressed []Message, droppedIndices []int, err error) {
	// 1. 计算每条消息的重要性
	importances := w.calculateImportances(messages)

	// 2. 如果消息总数在窗口内，直接返回
	if len(messages) <= w.windowSize {
		// 检查token数
		totalTokens := 0
		for _, msg := range messages {
			totalTokens += estimateTokens(msg.Content)
		}
		if totalTokens <= w.maxTokens {
			return messages, nil, nil
		}
	}

	// 3. 选择要保留的消息
	selectedIndices := w.selectMessages(messages, importances)

	// 4. 计算被丢弃的索引
	selectedSet := make(map[int]bool)
	for _, idx := range selectedIndices {
		selectedSet[idx] = true
	}
	for i := range messages {
		if !selectedSet[i] {
			droppedIndices = append(droppedIndices, i)
		}
	}

	// 5. 构建压缩后的消息（保持顺序）
	for _, idx := range selectedIndices {
		compressed = append(compressed, messages[idx])
	}

	return compressed, droppedIndices, nil
}

// calculateImportances 计算消息重要性
func (w *SmartSlidingWindow) calculateImportances(messages []Message) []MessageImportance {
	importances := make([]MessageImportance, len(messages))

	for i, msg := range messages {
		score := 0.0
		reason := ""

		// 规则1：系统消息始终重要
		if msg.Role == "system" {
			score = 1.0
			reason = "系统消息"
		} else {
			// 规则2：长消息通常更重要（包含更多上下文）
			tokenLen := float64(estimateTokens(msg.Content))
			score += math.Min(tokenLen/100.0, 0.4) // 最多0.4分

			// 规则3：包含关键词的消息更重要
			keywords := []string{"如何", "为什么", "错误", "重要", "关键", "总结", "结论", "how", "why", "error", "important", "summary"}
			keywordCount := 0
			for _, kw := range keywords {
				if contains(msg.Content, kw) {
					keywordCount++
				}
			}
			score += math.Min(float64(keywordCount)*0.2, 0.4)

			// 规则4：包含代码的消息更重要
			if contains(msg.Content, "```") || contains(msg.Content, "function") || contains(msg.Content, "import") {
				score += 0.2
				reason = "包含代码"
			}

			// 规则5：最近的消息稍微重要一些（时间衰减）
			recency := float64(i) / float64(len(messages))
			score += recency * 0.2

			// 规则6：寒暄类消息降低重要性
			greetings := []string{"你好", "hi", "hello", "thanks", "谢谢"}
			isGreeting := false
			for _, g := range greetings {
				if len(msg.Content) < 20 && contains(msg.Content, g) {
					isGreeting = true
					break
				}
			}
			if isGreeting {
				score -= 0.3
				reason = "寒暄消息"
			}
		}

		// 确保分数在[0,1]范围内
		score = math.Max(0, math.Min(1, score))
		importances[i] = MessageImportance{
			MessageIndex: i,
			Score:        score,
			Reason:       reason,
		}
	}

	return importances
}

// selectMessages 选择要保留的消息
func (w *SmartSlidingWindow) selectMessages(messages []Message, importances []MessageImportance) []int {
	// 策略：
	// 1. 始终保留系统消息
	// 2. 保留最近N条消息
	// 3. 在窗口内，优先保留重要性高的消息

	selected := make(map[int]bool)

	// 1. 保留系统消息
	for i, msg := range messages {
		if msg.Role == "system" {
			selected[i] = true
		}
	}

	// 2. 保留最近的消息（强制保留最近5条，保持对话连贯性）
	recentCount := 5
	startIdx := len(messages) - recentCount
	if startIdx < 0 {
		startIdx = 0
	}
	for i := startIdx; i < len(messages); i++ {
		selected[i] = true
	}

	// 3. 在窗口大小内，添加重要性高的消息
	if len(selected) < w.windowSize {
		// 按重要性排序
		sorted := make([]MessageImportance, len(importances))
		copy(sorted, importances)
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].Score > sorted[i].Score {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}

		// 选择重要的消息填充窗口
		for _, imp := range sorted {
			if len(selected) >= w.windowSize {
				break
			}
			if imp.Score >= w.importanceThreshold && !selected[imp.MessageIndex] {
				selected[imp.MessageIndex] = true
			}
		}
	}

	// 4. 转回有序列表
	var result []int
	for i := 0; i < len(messages); i++ {
		if selected[i] {
			result = append(result, i)
		}
	}

	return result
}

// ──────────────────────────────────────────────────────────────────────────────
// 策略3：混合压缩（HybridCompression）
// ──────────────────────────────────────────────────────────────────────────────

// CompressionStrategy 压缩策略类型
type CompressionStrategy string

const (
	StrategyNone        CompressionStrategy = "none"
	StrategyIncremental CompressionStrategy = "incremental"
	StrategySlidingWin  CompressionStrategy = "sliding_window"
	StrategySummary     CompressionStrategy = "summary"
)

// HybridCompressor 混合压缩器
//
// 原理：
//   - 根据消息数量、token数、场景自动选择最优压缩策略
//   - 原则：不进行二次压缩（已经压缩过的内容不再压缩）
//   - 智能选择：
//   - < 10条消息：不压缩
//   - 10-30条：增量压缩
//   - 30-100条：智能滑动窗口
//   - > 100条：摘要压缩（一次性）
type HybridCompressor struct {
	incremental *IncrementalCompressor
	slidingWin  *SmartSlidingWindow
}

func NewHybridCompressor() *HybridCompressor {
	return &HybridCompressor{
		incremental: NewIncrementalCompressor(),
		slidingWin:  NewSmartSlidingWindow(),
	}
}

// SelectStrategy 选择压缩策略
func (h *HybridCompressor) SelectStrategy(messageCount int, totalTokens int) CompressionStrategy {
	// 规则1：消息数少，不压缩
	if messageCount < 10 {
		return StrategyNone
	}

	// 规则2：Token数小，不压缩
	if totalTokens < 2000 {
		return StrategyNone
	}

	// 规则3：根据消息数量选择
	if messageCount <= 30 {
		// 中等长度：增量压缩
		return StrategyIncremental
	} else if messageCount <= 100 {
		// 较长对话：智能滑动窗口
		return StrategySlidingWin
	} else {
		// 超长对话：摘要压缩
		return StrategySummary
	}
}

// Compress 混合压缩（主入口）
func (h *HybridCompressor) Compress(ctx context.Context, messages []Message, existingSummary string, lastSummaryIndex int) (compressed []Message, strategy CompressionStrategy, droppedIndices []int, err error) {
	// 计算总token数
	totalTokens := 0
	for _, msg := range messages {
		totalTokens += estimateTokens(msg.Content)
	}

	// 选择策略
	strategy = h.SelectStrategy(len(messages), totalTokens)

	// 根据策略执行压缩
	switch strategy {
	case StrategyNone:
		// 不压缩
		return messages, strategy, nil, nil

	case StrategyIncremental:
		// 增量压缩
		compressed, _, _, err = h.incremental.Compress(ctx, messages, existingSummary, lastSummaryIndex)
		return compressed, strategy, nil, err

	case StrategySlidingWin:
		// 智能滑动窗口
		compressed, droppedIndices, err = h.slidingWin.Compress(ctx, messages)
		return compressed, strategy, droppedIndices, err

	case StrategySummary:
		// 摘要压缩（与增量压缩类似，但强制重新生成）
		summary := h.incremental.generateSummary(ctx, messages)
		summaryMsg := Message{
			Role:    "system",
			Content: summary,
		}
		recentMessages := messages
		if len(messages) > 20 {
			recentMessages = messages[len(messages)-20:]
		}
		compressed = append([]Message{summaryMsg}, recentMessages...)
		return compressed, strategy, nil, nil

	default:
		return messages, StrategyNone, nil, nil
	}
}

// GetStrategyStats 获取策略统计信息
func (h *HybridCompressor) GetStrategyStats() map[string]interface{} {
	return map[string]interface{}{
		"strategies": []string{
			string(StrategyNone),
			string(StrategyIncremental),
			string(StrategySlidingWin),
			string(StrategySummary),
		},
		"principle": "不进行二次压缩：已经压缩过的内容标记为摘要，不再次压缩",
		"thresholds": map[string]int{
			"none_max":        10,
			"incremental_max": 30,
			"sliding_max":     100,
			"summary_min":     100,
		},
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数（hashMessage, estimateTokens, contains, min 在 helpers.go 中）
// ──────────────────────────────────────────────────────────────────────────────

// CompressedSessionV2 压缩会话 v2（支持多种策略）
type CompressedSessionV2 struct {
	SessionID              string              `json:"session_id"`
	TenantID               string              `json:"tenant_id"`
	CompressedMessages     []Message           `json:"compressed_messages"`
	Strategy               CompressionStrategy `json:"strategy"`
	OriginalMessageCount   int                 `json:"original_message_count"`
	CompressedMessageCount int                 `json:"compressed_message_count"`
	OriginalTokens         int                 `json:"original_tokens"`
	CompressedTokens       int                 `json:"compressed_tokens"`
	CompressionRatio       float64             `json:"compression_ratio"`
	DroppedIndices         []int               `json:"dropped_indices,omitempty"`
	Summary                string              `json:"summary,omitempty"`
	LastSummaryIndex       int                 `json:"last_summary_index"`
	AlignmentMap           []AlignmentInfo     `json:"alignment_map"`
	UpdatedAt              time.Time           `json:"updated_at"`
}

// CompressWithHybrid 使用混合策略压缩并更新缓存状态
func (h *HybridCompressor) CompressWithHybrid(ctx context.Context, raw *RawSession, previousSummary string, previousIndex int) (*CompressedSessionV2, error) {
	compressed, strategy, dropped, err := h.Compress(ctx, raw.Messages, previousSummary, previousIndex)
	if err != nil {
		return nil, err
	}

	// 计算token
	originalTokens := raw.TotalTokens
	compressedTokens := 0
	for _, msg := range compressed {
		compressedTokens += estimateTokens(msg.Content)
	}

	ratio := 1.0
	if originalTokens > 0 {
		ratio = float64(compressedTokens) / float64(originalTokens)
	}

	// 构建对齐映射
	alignmentMap := make([]AlignmentInfo, len(raw.Messages))
	selectedSet := make(map[int]bool)
	for _, idx := range dropped {
		// 不需要标记，被丢弃的不会出现在对齐中
		_ = idx
	}

	if strategy == StrategyNone {
		// 1:1对齐
		for i := range raw.Messages {
			alignmentMap[i] = AlignmentInfo{
				OriginalIndex:   i,
				CompressedIndex: i,
				IsCompressed:    false,
				Hash:            raw.MessageHashes[i],
			}
		}
	} else {
		// 构建映射
		rawIdx := 0
		for compIdx, msg := range compressed {
			if msg.Role == "system" && contains(msg.Content, "[会话摘要]") {
				// 这是摘要消息，标记所有原始消息都被压缩到这里
				for i := 0; i < len(raw.Messages); i++ {
					if !selectedSet[i] {
						alignmentMap[i] = AlignmentInfo{
							OriginalIndex:   i,
							CompressedIndex: compIdx,
							IsCompressed:    true,
							CompressedInto:  compIdx,
							Hash:            raw.MessageHashes[i],
						}
						selectedSet[i] = true
					}
				}
			} else {
				// 原始消息：找到对应的原始索引
				if rawIdx < len(raw.MessageHashes) {
					alignmentMap[rawIdx] = AlignmentInfo{
						OriginalIndex:   rawIdx,
						CompressedIndex: compIdx,
						IsCompressed:    false,
						Hash:            raw.MessageHashes[rawIdx],
					}
					rawIdx++
				}
			}
		}
	}

	result := &CompressedSessionV2{
		SessionID:              raw.SessionID,
		TenantID:               raw.TenantID,
		CompressedMessages:     compressed,
		Strategy:               strategy,
		OriginalMessageCount:   len(raw.Messages),
		CompressedMessageCount: len(compressed),
		OriginalTokens:         originalTokens,
		CompressedTokens:       compressedTokens,
		CompressionRatio:       ratio,
		DroppedIndices:         dropped,
		LastSummaryIndex:       len(raw.Messages),
		AlignmentMap:           alignmentMap,
		UpdatedAt:              time.Now(),
	}

	return result, nil
}

// hashMessageV2 哈希函数 v2
func hashMessageV2(msg Message) string {
	data := fmt.Sprintf("%s:%s", msg.Role, msg.Content)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)
}

// serializeForLog 序列化用于日志
func serializeForLog(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
