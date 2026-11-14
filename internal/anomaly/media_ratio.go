package anomaly

import (
	"context"
	"fmt"
)

// MediaTextRatioDetector 媒体/文本比例检测器
// 检测场景：请求中附件数据远大于文本内容，导致压缩失效、成本激增
type MediaTextRatioDetector struct {
	ratioThreshold float64 // 附件/文本比例阈值
	minMediaBytes  int64   // 最小附件字节数（避免误报）
}

// NewMediaTextRatioDetector 创建检测器
func NewMediaTextRatioDetector() *MediaTextRatioDetector {
	return &MediaTextRatioDetector{
		ratioThreshold: 10.0,           // 默认：附件 > 文本 10 倍
		minMediaBytes:  1 * 1024 * 1024, // 默认：>1MB 才检测
	}
}

// SetRatioThreshold 设置比例阈值
func (d *MediaTextRatioDetector) SetRatioThreshold(ratio float64) {
	d.ratioThreshold = ratio
}

// SetMinMediaBytes 设置最小附件字节数
func (d *MediaTextRatioDetector) SetMinMediaBytes(bytes int64) {
	d.minMediaBytes = bytes
}

// Check 检测单次请求
func (d *MediaTextRatioDetector) Check(ctx context.Context, req *Request) *AnomalyResult {
	attachBytes := d.sumAttachmentBytes(req.Attachments)
	textBytes := d.estimateTextBytes(req.Messages)

	// 附件太小或无文本，不检测
	if attachBytes < d.minMediaBytes || textBytes == 0 {
		return nil
	}

	ratio := float64(attachBytes) / float64(textBytes)

	if ratio > d.ratioThreshold {
		severity := "medium"
		if ratio > 20.0 {
			severity = "high"
		}

		return &AnomalyResult{
			Code:     "media_text_imbalance",
			Severity: severity,
			Message: fmt.Sprintf("附件/文本比 %.1f:1 过高（%.2f MB 附件 / %.1f KB 文本）",
				ratio,
				float64(attachBytes)/(1024*1024),
				float64(textBytes)/1024),
			Metrics: map[string]interface{}{
				"ratio":            ratio,
				"attachment_bytes": attachBytes,
				"text_bytes":       textBytes,
				"attachment_count": len(req.Attachments),
				"message_count":    len(req.Messages),
			},
		}
	}

	return nil
}

// sumAttachmentBytes 计算附件总字节数
func (d *MediaTextRatioDetector) sumAttachmentBytes(attachments []Attachment) int64 {
	total := int64(0)
	for _, att := range attachments {
		if att.Size > 0 {
			total += att.Size
		} else if len(att.Data) > 0 {
			total += int64(len(att.Data))
		} else if att.URL != "" {
			total += int64(len(att.URL))
		}
	}
	return total
}

// estimateTextBytes 估算文本内容字节数
func (d *MediaTextRatioDetector) estimateTextBytes(messages []Message) int64 {
	total := int64(0)
	for _, msg := range messages {
		// 只计算纯文本内容，忽略附件
		total += int64(len(msg.Content))
		total += int64(len(msg.Role)) // role 字段也算
	}

	// 至少返回 1 避免除零
	if total == 0 {
		return 1
	}
	return total
}
