package logging

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const maxRawLogFileSize = 200 * 1024 * 1024

// 特性：
//   - 回转日志文件，最大200MB
//   - 记录完整的请求和响应数据（未经IR转换）
//   - 包含时间戳、请求ID、协议类型等元数据
//   - 线程安全
type RawDataLogger struct {
	mu          sync.Mutex
	file        *os.File
	currentSize int64
	maxSize     int64
	baseDir     string
	enabled     bool

	// 2026-07-28: 记录当前 raw log 文件路径与已落盘偏移，供异常上报时
	// 引用最近一次写盘位置。currentOffset 是"下一次落盘的起点"，
	// 即历史写入字节总数（受 mu 保护）。
	currentPath   string
	currentOffset int64
}

// RawDataEntry 原始数据日志条目
type RawDataEntry struct {
	Timestamp       time.Time         `json:"timestamp"`
	RequestID       string            `json:"request_id"`
	Direction       string            `json:"direction"` // "client_request", "upstream_request", "upstream_response", "client_response"
	Protocol        string            `json:"protocol"`  // "openai-chat", "anthropic-messages", etc.
	DataSize        int               `json:"data_size"`
	RawData         string            `json:"raw_data"` // base64-encoded complete payload
	RawDataEncoding string            `json:"raw_data_encoding,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	ConversionStep  string            `json:"conversion_step,omitempty"` // "pre_parse", "post_parse", "pre_serialize", "post_serialize"
	Error           string            `json:"error,omitempty"`

	// Reason carries the structured outcome of a streaming or
	// interrupted frame (e.g. "client_cancel", "upstream_error",
	// "end_of_stream", "stream_panic"). Distinct from Error which
	// holds the message text. Operators filter the audit log on this
	// field the same way they filter request_logs.error_kind, so the
	// values must line up with the executor's errorsx classification
	// (2026-07-28 §5.6 / §5.8).
	Reason string `json:"reason,omitempty"`

	// 2026-07-27: Correlation envelope. Every raw entry is tagged with the
	// same identifiers that request_logs and trace spans use, so the
	// audit log can be sliced by session, provider, attempt, or trace.
	ClientRequestID  string `json:"client_request_id,omitempty"`
	GWSessionID      string `json:"gw_session_id,omitempty"`
	GWTaskID         string `json:"gw_task_id,omitempty"`
	ParentRequestID  string `json:"parent_request_id,omitempty"`
	TenantID         string `json:"tenant_id,omitempty"`
	ApplicationID    string `json:"application_id,omitempty"`
	APIKeyID         int    `json:"api_key_id,omitempty"`
	ProviderID       int    `json:"provider_id,omitempty"`
	CredentialID     int    `json:"credential_id,omitempty"`
	AttemptNo        int    `json:"attempt_no,omitempty"`
	ChunkIndex       int    `json:"chunk_index,omitempty"`
	UpstreamEndpoint string `json:"upstream_endpoint,omitempty"`
	TraceID          string `json:"trace_id,omitempty"`
	SpanID           string `json:"span_id,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
}

// NewRawDataLogger 创建原始数据日志记录器
//
// baseDir: 日志文件目录路径
// maxSize: 单个日志文件最大字节数（默认200MB）
// enabled: 是否启用日志记录（可通过环境变量控制）
func NewRawDataLogger(baseDir string, maxSize int64, enabled bool) (*RawDataLogger, error) {
	if !enabled {
		return &RawDataLogger{enabled: false}, nil
	}

	if maxSize <= 0 || maxSize > maxRawLogFileSize {
		maxSize = maxRawLogFileSize
	}

	// 确保目录存在
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create raw data log directory: %w", err)
	}

	logger := &RawDataLogger{
		baseDir: baseDir,
		maxSize: maxSize,
		enabled: true,
	}

	if err := logger.rotate(); err != nil {
		return nil, fmt.Errorf("failed to initialize raw data log file: %w", err)
	}

	return logger, nil
}

// LogClientRequest 记录客户端原始请求（转换前）
func (l *RawDataLogger) LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string) {
	if !l.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:       time.Now(),
		RequestID:       requestID,
		Direction:       "client_request",
		Protocol:        protocol,
		DataSize:        len(body),
		RawData:         encodeRawData(body),
		RawDataEncoding: "base64",
		Headers:         headers,
		ConversionStep:  conversionStep,
	}

	l.writeEntry(entry)
}

// LogUpstreamRequest 记录发送到上游的请求（转换后）
func (l *RawDataLogger) LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string) {
	if !l.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:       time.Now(),
		RequestID:       requestID,
		Direction:       "upstream_request",
		Protocol:        protocol,
		DataSize:        len(body),
		RawData:         encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep:  conversionStep,
	}

	l.writeEntry(entry)
}

// LogUpstreamResponse 记录上游响应（转换前）
func (l *RawDataLogger) LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string) {
	if !l.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:       time.Now(),
		RequestID:       requestID,
		Direction:       "upstream_response",
		Protocol:        protocol,
		DataSize:        len(body),
		RawData:         encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep:  conversionStep,
	}

	l.writeEntry(entry)
}

// LogClientResponse 记录返回给客户端的响应（转换后）
func (l *RawDataLogger) LogClientResponse(requestID, protocol string, body []byte, conversionStep string) {
	if !l.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:       time.Now(),
		RequestID:       requestID,
		Direction:       "client_response",
		Protocol:        protocol,
		DataSize:        len(body),
		RawData:         encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep:  conversionStep,
	}

	l.writeEntry(entry)
}

// LogConversionError 记录转换错误及相关数据
func (l *RawDataLogger) LogConversionError(requestID, protocol, direction, step string, body []byte, err error) {
	if !l.enabled {
		return
	}

	entry := RawDataEntry{
		Timestamp:       time.Now(),
		RequestID:       requestID,
		Direction:       direction,
		Protocol:        protocol,
		DataSize:        len(body),
		RawData:         encodeRawData(body),
		RawDataEncoding: "base64",
		ConversionStep:  step,
		Error:           err.Error(),
	}

	l.writeEntry(entry)
}

// writeEntry 写入日志条目（线程安全）
func (l *RawDataLogger) writeEntry(entry RawDataEntry) {
	l.writeEntries([]RawDataEntry{entry})
}

func (l *RawDataLogger) writeEntries(entries []RawDataEntry) {
	if len(entries) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		slog.Warn("raw_data_logger: file not initialized")
		return
	}

	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			slog.Error("raw_data_logger: failed to marshal entry", "err", err)
			continue
		}
		data = append(data, '\n')

		if l.currentSize+int64(len(data)) > l.maxSize {
			if err := l.rotate(); err != nil {
				slog.Error("raw_data_logger: failed to rotate log", "err", err)
				return
			}
		}

		n, err := l.file.Write(data)
		if err != nil {
			slog.Error("raw_data_logger: failed to write entry", "err", err)
			return
		}
		l.currentSize += int64(n)
		l.currentOffset += int64(n)
	}

	if err := l.file.Sync(); err != nil {
		slog.Error("raw_data_logger: failed to sync file", "err", err)
	}
}

// rotate 回转日志文件
func (l *RawDataLogger) rotate() error {
	// 关闭当前文件
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			slog.Warn("raw_data_logger: failed to close old log file", "err", err)
		}
	}

	// 生成唯一文件名，避免同一秒内轮转时重新打开旧文件。
	timestamp := time.Now().UTC().Format("20060102_150405.000000000")
	filename := fmt.Sprintf("raw_data_%s_%d.jsonl", timestamp, time.Now().UnixNano())
	filePath := filepath.Join(l.baseDir, filename)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("failed to create raw data log file: %w", err)
	}

	l.file = file
	l.currentSize = 0
	l.currentPath = filePath
	l.currentOffset = 0

	slog.Info("raw_data_logger: rotated log file", "path", filePath)

	// 清理旧文件（保留最近5个）
	go l.cleanupOldFiles(5)

	return nil
}

// cleanupOldFiles 清理旧的日志文件，保留最近N个
func (l *RawDataLogger) cleanupOldFiles(keepCount int) {
	files, err := filepath.Glob(filepath.Join(l.baseDir, "raw_data_*.jsonl"))
	if err != nil {
		slog.Warn("raw_data_logger: failed to list old files", "err", err)
		return
	}

	if len(files) <= keepCount {
		return
	}

	// 按修改时间排序
	type fileInfo struct {
		path    string
		modTime time.Time
	}

	var infos []fileInfo
	for _, path := range files {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		infos = append(infos, fileInfo{path: path, modTime: stat.ModTime()})
	}

	// 按修改时间排序（使用 sort 包）
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].modTime.After(infos[j].modTime)
	})

	// 删除超出保留数量的文件
	for i := keepCount; i < len(infos); i++ {
		if err := os.Remove(infos[i].path); err != nil {
			slog.Warn("raw_data_logger: failed to remove old file", "path", infos[i].path, "err", err)
		} else {
			slog.Info("raw_data_logger: removed old log file", "path", infos[i].path)
		}
	}
}

// Close 关闭日志记录器
func (l *RawDataLogger) Close() error {
	if !l.enabled {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		return l.file.Close()
	}

	return nil
}

// CurrentLocation returns the path of the raw data log file currently
// being written and the byte offset of the next write. The offset is the
// total number of bytes successfully written to disk (after Sync), so an
// operator seeking to the returned offset will land at the start of the
// next entry. Returns empty values when the logger is disabled or has
// never rotated.
//
// 2026-07-28: used by LockFreeAnomalyReporter to populate
// AnomalyReport.RawLogFile/RawLogOffset so the external endpoint can
// reference the on-disk audit log. Concurrency: safe under the
// gateway-side mu; callers should invoke through the AsyncRawDataLogger
// wrapper to avoid racing against the background flush worker.
func (l *RawDataLogger) CurrentLocation() (file string, offset int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentPath, l.currentOffset
}

// HasFile reports whether the logger has an open raw data log file. It
// returns false when the logger is disabled or has not yet rotated into
// its first file. Callers can use this to distinguish "no audit log
// configured" from "audit log is empty".
func (l *RawDataLogger) HasFile() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}

func encodeRawData(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// hashBytes returns the lowercase hex SHA-256 of the input. Used by
// RawDataEntry to anchor each raw entry against the upstream body so
// request_logs, the anomaly endpoint, and the on-disk audit log can
// be cross-checked without re-reading the payload.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
