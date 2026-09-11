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

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// 2026-08-18: 调整为 100MB/文件 × 10 备份 = 1000MB 总量上限
// （rule 11 §3 「允许最高1000M的log总量，但每个文件不超过100M」）
const (
	maxRawLogFileSize  = 100 * 1024 * 1024 // 100MB/文件
	maxRawLogKeepCount = 10                // 保留最近 10 个文件

	// rawDataLoggerRotateAttempts bounds the collision retry loop. A collision
	// is expected only when the filesystem clock is coarser than the filename
	// timestamp, so a short bounded loop is enough without delaying failures
	// caused by permissions or an unavailable directory.
	rawDataLoggerRotateAttempts = 8
)

// rawDataLoggerNow is package-scoped so rotation collision tests can hold the
// clock stable while exercising the sequence-suffix fallback.
var rawDataLoggerNow = time.Now

// 特性：
//   - 回转日志文件，单个文件最大 100MB（rule 11 §3 红线）
//   - 保留最近 10 个文件（约 1000MB 总量上限）
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

	// R12（2026-09-11）幂等 Close 契约：首次 Close 关闭文件并记住错误，
	// 后续调用返回同一错误（预研 §2.3 勘误 6：此前仅靠 file==nil 近似
	// 幂等，无法区分"已关闭"与"rotate 失败等未初始化"状态）。
	closed   bool
	closeErr error
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

// RawDataEncodingJSON returns the JSON-line size of this entry as
// it would be written to the on-disk audit log. Computed once at
// index time so the per-request frame index in AsyncRawDataLogger
// can reconstruct the entry's start byte offset from the
// post-write currentOffset.
//
// 2026-07-28 §5.7: this is the inverse of writeEntries, which
// marshals the entry, appends '\n', and writes the resulting bytes.
// The returned slice is the JSON bytes WITHOUT the trailing newline.
func (e *RawDataEntry) RawDataEncodingJSON() []byte {
	b, err := json.Marshal(e)
	if err != nil {
		// marshalling RawDataEntry never fails (it has no func/chan
		// fields), but be defensive — return an empty line so the
		// caller can still peel at least one byte.
		return nil
	}
	return b
}

// NewRawDataLogger 创建原始数据日志记录器
//
// baseDir: 日志文件目录路径
// maxSize: 单个日志文件最大字节数（默认100MB，符合 rule 11 §3）
// enabled: 是否启用日志记录（可通过环境变量控制）
func NewRawDataLogger(baseDir string, maxSize int64, enabled bool) (*RawDataLogger, error) {
	if !enabled {
		return &RawDataLogger{enabled: false}, nil
	}

	// 2026-08-18: maxSize>0 时严格按调用方传入值生效，但超过
	// maxRawLogFileSize（100MB）时截断到上限，避免单文件过大
	// 导致 total 超出 1000MB 上限。
	if maxSize <= 0 {
		maxSize = maxRawLogFileSize
	}
	if maxSize > maxRawLogFileSize {
		slog.Warn("raw_data_logger: requested maxSize exceeds policy, clamping",
			"requested_bytes", maxSize,
			"policy_max_bytes", maxRawLogFileSize)
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
			// P0-2 (audit §3.6 R-3.4): count this audit pipeline failure
			// so the rawaudit_write_failed_total rule in
			// deploy/monitoring/grafana-alerts/shadow-write-failures.yaml
			// can fire. CRITICAL: raw audit JSONL is the only immutable
			// local audit copy before cross-machine replication lands
			// (P2-2). Single failure already matters — operator can
			// diagnose whether disk full / fs corrupt / perms broken.
			metrics.Global().RecordRawAuditWriteFailure()
			return
		}
		l.currentSize += int64(n)
		l.currentOffset += int64(n)
	}

	if err := l.file.Sync(); err != nil {
		slog.Error("raw_data_logger: failed to sync file", "err", err)
	}
}

// writeEntriesFallible 是 writeEntries 的可错变体，供 BufferedRawSink 的
// 写失败重试降级使用（R12 预研 §4）。返回已完整写出的条目数与首个错误：
//   - rotate / 写失败：返回 (已写出条数, err)；失败批次剩余条目不再写出，
//     由调用方决定重试或丢弃（与 writeEntries 的"静默丢弃剩余"同源行为，
//     此处显式化为返回值）。
//   - marshal 失败：单条跳过，计入已写出（与 writeEntries 的 continue 对齐）。
//   - fsync 失败：条目已全部写出，返回 (len(entries), err)——调用方不得
//     重试，否则会重复写已落盘条目。
//
// metric（RecordRawAuditWriteFailure）与 slog 行为与 writeEntries 一致。
func (l *RawDataLogger) writeEntriesFallible(entries []RawDataEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		slog.Warn("raw_data_logger: file not initialized")
		return 0, fmt.Errorf("raw_data_logger: file not initialized")
	}

	written := 0
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			slog.Error("raw_data_logger: failed to marshal entry", "err", err)
			written++
			continue
		}
		data = append(data, '\n')

		if l.currentSize+int64(len(data)) > l.maxSize {
			if err := l.rotate(); err != nil {
				slog.Error("raw_data_logger: failed to rotate log", "err", err)
				return written, err
			}
		}

		n, err := l.file.Write(data)
		if err != nil {
			slog.Error("raw_data_logger: failed to write entry", "err", err)
			// P0-2 (audit §3.6 R-3.4): 同 writeEntries —— 计数审计管道
			// 失败，供 rawaudit_write_failed_total 告警规则消费。
			metrics.Global().RecordRawAuditWriteFailure()
			return written, err
		}
		l.currentSize += int64(n)
		l.currentOffset += int64(n)
		written++
	}

	if err := l.file.Sync(); err != nil {
		slog.Error("raw_data_logger: failed to sync file", "err", err)
		return len(entries), err
	}
	return len(entries), nil
}

// Sync 将已接受条目落盘并 fsync（RawSink 屏障语义，R12）。同步直写路径
// 每批 writeEntries 末尾已 fsync，这里对最后一次写盘后的文件再补一次
// Sync，作为显式屏障；disabled / 尚无文件时返回 nil。
func (l *RawDataLogger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled || l.file == nil {
		return nil
	}
	return l.file.Sync()
}

// sinkEnabled 报告 sink 是否接受条目（rawEntryWriter 契约）。
func (l *RawDataLogger) sinkEnabled() bool {
	return l.enabled
}

// rotate 回转日志文件
func (l *RawDataLogger) rotate() error {
	// 关闭当前文件，并立即清空引用，避免失败路径留下已关闭句柄。
	if l.file != nil {
		oldFile := l.file
		l.file = nil
		if err := oldFile.Close(); err != nil {
			slog.Warn("raw_data_logger: failed to close old log file", "err", err)
		}
	}

	// 生成唯一文件名，避免同一时钟 tick 内轮转时 O_EXCL 撞名。
	var (
		file     *os.File
		filePath string
		err      error
	)
	for attempt := 0; attempt < rawDataLoggerRotateAttempts; attempt++ {
		now := rawDataLoggerNow()
		timestamp := now.UTC().Format("20060102_150405.000000000")
		filename := fmt.Sprintf("raw_data_%s_%d", timestamp, now.UnixNano())
		if attempt > 0 {
			filename += fmt.Sprintf("_%d", attempt)
		}
		filename += ".jsonl"
		filePath = filepath.Join(l.baseDir, filename)

		file, err = os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("failed to create raw data log file: %w", err)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to create raw data log file after %d attempts: %w", rawDataLoggerRotateAttempts, err)
	}

	l.file = file
	l.currentSize = 0
	l.currentPath = filePath
	l.currentOffset = 0

	slog.Info("raw_data_logger: rotated log file", "path", filePath)

	// 2026-08-18: 清理旧文件（保留最近 10 个，rule 11 §3）。
	// 单文件 100MB × 10 ≈ 1000MB 上限。
	go l.cleanupOldFiles(maxRawLogKeepCount)

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

// Close 关闭日志记录器。幂等（R12 契约）：首次调用关闭文件并记住错误，
// 后续调用返回同一错误。
func (l *RawDataLogger) Close() error {
	if !l.enabled {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return l.closeErr
	}
	l.closed = true
	if l.file != nil {
		l.closeErr = l.file.Close()
		l.file = nil
	}
	return l.closeErr
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
