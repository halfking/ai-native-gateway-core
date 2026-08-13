// Package compression - quality_score.go
//
// 压缩质量评分系统（Phase 1 Task 1.3）
//
// 提供多维度压缩质量评估：
//   - Token 节省百分比
//   - 消息保留率
//   - 语义保真度（基于 AlignmentMap）
//   - 信息密度
//   - 综合评分
package compression

import (
	"regexp"
	"strings"
)

// CompressionQualityScore 压缩质量评分
type CompressionQualityScore struct {
	TokenSavingsPercent  float64 `json:"token_savings_pct"`  // Token 节省百分比（0-100）
	MessageRetentionRate float64 `json:"msg_retention_rate"` // 消息保留率（0-1）
	SemanticFidelity     float64 `json:"semantic_fidelity"`  // 语义保真度（0-1）
	InformationDensity   float64 `json:"info_density"`       // 信息密度评分（0-1）
	LossinessClass       string  `json:"lossiness_class"`    // none/tail/whole
	OverallScore         float64 `json:"overall_score"`      // 综合评分（0-100）
}

// ComputeCompressionQuality 计算压缩质量评分
//
// 参数：
//   - originalTokens: 原始消息的 token 估算
//   - compressedTokens: 压缩后的 token 估算
//   - originalMsgCount: 原始消息数
//   - compressedMsgCount: 压缩后消息数
//   - alignmentMap: 对齐映射（用于计算语义保真度）
//   - lossiness: 有损程度分类
//
// 返回：压缩质量评分
func ComputeCompressionQuality(
	originalTokens, compressedTokens int,
	originalMsgCount, compressedMsgCount int,
	alignmentMap []AlignmentInfo,
	lossiness string,
) CompressionQualityScore {
	score := CompressionQualityScore{
		LossinessClass: lossiness,
	}

	// 1. Token 节省百分比
	if originalTokens > 0 {
		saved := originalTokens - compressedTokens
		if saved < 0 {
			saved = 0 // 压缩后反而变大了（罕见，但可能发生）
		}
		score.TokenSavingsPercent = float64(saved) / float64(originalTokens) * 100
	}

	// 2. 消息保留率
	if originalMsgCount > 0 {
		score.MessageRetentionRate = float64(compressedMsgCount) / float64(originalMsgCount)
		if score.MessageRetentionRate > 1.0 {
			score.MessageRetentionRate = 1.0
		}
	} else {
		score.MessageRetentionRate = 1.0
	}

	// 3. 语义保真度（基于 AlignmentMap）
	if len(alignmentMap) > 0 {
		preserved := 0
		for _, a := range alignmentMap {
			if !a.IsCompressed {
				preserved++
			}
		}
		score.SemanticFidelity = float64(preserved) / float64(len(alignmentMap))
	} else {
		// 无 AlignmentMap 时，假设语义保真度 = 消息保留率
		score.SemanticFidelity = score.MessageRetentionRate
	}

	// 4. 信息密度（基础版：基于压缩比，Phase 3 会增强）
	if originalTokens > 0 && compressedTokens > 0 {
		compressionRatio := float64(compressedTokens) / float64(originalTokens)
		// 信息密度 = 1 - 压缩比（压缩得越多，密度越高）
		score.InformationDensity = 1.0 - compressionRatio
		if score.InformationDensity < 0 {
			score.InformationDensity = 0
		}
		if score.InformationDensity > 1.0 {
			score.InformationDensity = 1.0
		}
	} else {
		score.InformationDensity = 0.5
	}

	// 5. Lossiness 加分
	lossinessBonus := getLossinessBonus(lossiness)

	// 6. 综合评分（加权平均）
	score.OverallScore =
		score.TokenSavingsPercent*0.30 + // Token 节省权重 30%
			score.MessageRetentionRate*100*0.15 + // 消息保留率权重 15%
			score.SemanticFidelity*100*0.30 + // 语义保真度权重 30%
			score.InformationDensity*100*0.10 + // 信息密度权重 10%
			lossinessBonus*0.15 // Lossiness 加分权重 15%

	// 限制在 0-100 范围
	if score.OverallScore < 0 {
		score.OverallScore = 0
	}
	if score.OverallScore > 100 {
		score.OverallScore = 100
	}

	return score
}

// getLossinessBonus 根据有损程度返回加分（0-100）
func getLossinessBonus(lossiness string) float64 {
	switch lossiness {
	case LossinessNone:
		return 100 // 无损压缩最佳
	case LossinessTail:
		return 70 // 尾部裁剪可接受
	case LossinessWhole:
		return 40 // 整体摘要有损失
	default:
		return 50 // 未知，给中等分
	}
}

// ComputeInformationDensity 计算消息的信息密度（Phase 3 增强版）
//
// 基于内容特征计算密度：
//   - 代码片段：+0.3
//   - 错误信息：+0.3
//   - URL/链接：+0.2
//   - 数字/日期：+0.1
//   - 重复内容：-0.2
//
// 当前版本（Phase 1）：仅基于压缩比估算，Phase 3 会实现完整版本
func ComputeInformationDensity(content string) float64 {
	if len(content) == 0 {
		return 0
	}

	density := 0.5 // 基础密度

	// 检测代码片段
	if strings.Contains(content, "```") || strings.Contains(content, "function") || strings.Contains(content, "class ") {
		density += 0.3
	}

	// 检测错误信息
	if strings.Contains(content, "error") || strings.Contains(content, "Error") || strings.Contains(content, "exception") {
		density += 0.3
	}

	// 检测 URL
	if strings.Contains(content, "http://") || strings.Contains(content, "https://") {
		density += 0.2
	}

	// 检测数字（简单版）
	digitPattern := regexp.MustCompile(`\d+`)
	if digitPattern.MatchString(content) {
		density += 0.1
	}

	// 限制在 0-1 范围
	if density > 1.0 {
		density = 1.0
	}

	return density
}
