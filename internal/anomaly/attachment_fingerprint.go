package anomaly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// AttachmentFingerprintDetector 附件指纹检测器
// 检测场景：客户端在每次请求中重复发送相同的附件集合（如 ZCode 重发历史截图）
type AttachmentFingerprintDetector struct {
	cache     map[string]*SessionWindow // key=session_id
	mu        sync.RWMutex
	threshold int   // 连续重复次数阈值
	sizeLimit int64 // 附件大小阈值（bytes）
	maxSize   int   // 最大缓存条目数
}

// NewAttachmentFingerprintDetector 创建检测器
func NewAttachmentFingerprintDetector(cacheSize int) (*AttachmentFingerprintDetector, error) {
	return &AttachmentFingerprintDetector{
		cache:     make(map[string]*SessionWindow),
		threshold: 5,              // 默认：连续 5 次相同触发
		sizeLimit: 2 * 1024 * 1024, // 默认：>2MB 才检测
		maxSize:   cacheSize,
	}, nil
}

// SetThreshold 设置连续重复阈值
func (d *AttachmentFingerprintDetector) SetThreshold(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threshold = n
}

// SetSizeLimit 设置附件大小阈值
func (d *AttachmentFingerprintDetector) SetSizeLimit(bytes int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sizeLimit = bytes
}

// Check 检测单次请求
func (d *AttachmentFingerprintDetector) Check(ctx context.Context, req *Request) *AnomalyResult {
	if len(req.Attachments) == 0 {
		return nil // 无附件，放行
	}

	// 计算本次附件集合的指纹
	currentHash := d.computeAttachmentSetHash(req.Attachments)
	totalBytes := d.sumAttachmentBytes(req.Attachments)

	// 附件太小不检测（避免误报小图标等）
	if totalBytes < d.sizeLimit {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// 清理过期缓存（简单 LRU：超过 maxSize 时删除最老的）
	if len(d.cache) >= d.maxSize {
		d.evictOldest()
	}

	// 获取或创建会话窗口
	window, ok := d.cache[req.SessionID]
	if !ok {
		// 首次请求，记录并放行
		window = &SessionWindow{
			SessionID:      req.SessionID,
			Fingerprints:   []string{currentHash},
			Timestamps:     []time.Time{time.Now()},
			TotalBytes:     totalBytes,
			RequestCount:   1,
			FirstRequestAt: time.Now(),
		}
		d.cache[req.SessionID] = window
		return nil
	}

	// 检查连续重复
	consecutiveSame := d.countConsecutiveSame(window.Fingerprints, currentHash)

	// 更新窗口（保留最近 10 笔）
	window.Fingerprints = append(window.Fingerprints, currentHash)
	window.Timestamps = append(window.Timestamps, time.Now())
	window.RequestCount++
	window.TotalBytes += totalBytes

	const maxWindowSize = 10
	if len(window.Fingerprints) > maxWindowSize {
		window.Fingerprints = window.Fingerprints[len(window.Fingerprints)-maxWindowSize:]
		window.Timestamps = window.Timestamps[len(window.Timestamps)-maxWindowSize:]
	}

	// 触发条件：连续 N 次相同 && 附件总量 > 阈值
	if consecutiveSame >= d.threshold-1 { // -1 因为还没算上当前这次
		duration := time.Since(window.FirstRequestAt)
		rate := float64(window.RequestCount) / duration.Minutes()

		return &AnomalyResult{
			Code:     "attachment_replay_loop",
			Severity: "high",
			Message: fmt.Sprintf("检测到附件重发循环：连续 %d 笔相同指纹，%d 张图片 / %.2f MB",
				consecutiveSame+1,
				len(req.Attachments),
				float64(totalBytes)/(1024*1024)),
			Metrics: map[string]interface{}{
				"consecutive_replays":  consecutiveSame + 1,
				"attachment_count":     len(req.Attachments),
				"attachment_bytes":     totalBytes,
				"session_request_count": window.RequestCount,
				"session_duration_min": duration.Minutes(),
				"request_rate_per_min": rate,
				"fingerprint_hash":     currentHash[:16], // 前 16 字符用于调试
			},
		}
	}

	return nil
}

// computeAttachmentSetHash 计算附件集合的哈希
// 对所有附件的哈希排序后拼接，确保顺序无关
func (d *AttachmentFingerprintDetector) computeAttachmentSetHash(attachments []Attachment) string {
	if len(attachments) == 0 {
		return ""
	}

	// 计算每个附件的哈希
	hashes := make([]string, 0, len(attachments))
	for _, att := range attachments {
		var data []byte
		if len(att.Data) > 0 {
			data = att.Data
		} else if att.URL != "" {
			// 对于 data URL，提取 base64 部分（简化处理）
			data = []byte(att.URL)
		}

		if len(data) > 0 {
			h := sha256.Sum256(data)
			hashes = append(hashes, hex.EncodeToString(h[:]))
		}
	}

	// 排序确保一致性
	sort.Strings(hashes)

	// 拼接所有哈希后再哈希
	combined := ""
	for _, h := range hashes {
		combined += h
	}

	finalHash := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(finalHash[:])
}

// sumAttachmentBytes 计算附件总字节数
func (d *AttachmentFingerprintDetector) sumAttachmentBytes(attachments []Attachment) int64 {
	total := int64(0)
	for _, att := range attachments {
		if att.Size > 0 {
			total += att.Size
		} else if len(att.Data) > 0 {
			total += int64(len(att.Data))
		} else if att.URL != "" {
			// data URL 估算（base64 编码后的长度）
			total += int64(len(att.URL))
		}
	}
	return total
}

// countConsecutiveSame 从末尾开始统计连续相同的个数
func (d *AttachmentFingerprintDetector) countConsecutiveSame(history []string, current string) int {
	if len(history) == 0 {
		return 0
	}

	count := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i] == current {
			count++
		} else {
			break
		}
	}
	return count
}

// GetSessionStats 获取会话统计（用于调试）
func (d *AttachmentFingerprintDetector) GetSessionStats(sessionID string) *SessionWindow {
	d.mu.RLock()
	defer d.mu.RUnlock()

	window, ok := d.cache[sessionID]
	if !ok {
		return nil
	}

	// 返回副本避免并发问题
	copy := *window
	return &copy
}

// ClearSession 清除会话缓存（用于测试）
func (d *AttachmentFingerprintDetector) ClearSession(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.cache, sessionID)
}

// evictOldest 清理最老的缓存条目（简单 LRU）
func (d *AttachmentFingerprintDetector) evictOldest() {
	if len(d.cache) == 0 {
		return
	}

	var oldestID string
	var oldestTime time.Time

	for id, window := range d.cache {
		if oldestID == "" || window.FirstRequestAt.Before(oldestTime) {
			oldestID = id
			oldestTime = window.FirstRequestAt
		}
	}

	if oldestID != "" {
		delete(d.cache, oldestID)
	}
}
