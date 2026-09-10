package logging

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// RawSink 是 raw 审计落盘的抽象（R12，2026-09-11 预研 §3）。实现三方：
//
//   - *RawDataLogger      同步直写执行器（构造期已 rotate 出第一个文件）
//   - *AsyncRawDataLogger 队列批量包装（现网默认，灰度 legacy 档）
//   - *BufferedRawSink    缓冲批量包装（R12 新增，写/fsync 节奏可配）
//
// 契约：
//   - 五个 Log* 保持 void：错误经 slog + RecordRawAuditWriteFailure 计数 +
//     各实现的统计暴露，不改签名（改签名会波及消费侧 executors.RawDataLogger
//     与全部实现，超出 R12 范围）。
//   - Sync()：持久化屏障。返回 nil 时，调用发生前已接受的条目保证已写出并
//     fsync；与并发 Log 的线性化空窗（Sync 进行中新入队的条目）不在此保证内。
//     disabled / 已关闭的实现返回 nil。
//   - Close()：幂等。首次调用排空 + sync + 关闭并记住错误；后续调用返回
//     同一错误。
//   - CurrentLocation()：只反映已 flush 条目的最新位置，不反映仍在缓冲 /
//     队列中的数据。
type RawSink interface {
	LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string)
	LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string)
	LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string)
	LogClientResponse(requestID, protocol string, body []byte, conversionStep string)
	LogConversionError(requestID, protocol, direction, step string, body []byte, err error)
	Sync() error
	Close() error
	CurrentLocation() (file string, offset int64)
	HasFile() bool
}

// EnvelopeRawSink 是可选的相关性信封能力子接口，延续现网的类型断言探测
// 模式（消费方 executors.diagnostic_adapters 的 envelopeAwareRawDataLogger）。
// Async 与 Buffered 实现；RawDataLogger 是低层执行器，不做信封增强。
type EnvelopeRawSink interface {
	LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope)
	LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
	LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
	LogClientResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
}

// FrameLookup 是可选的逐帧定位能力子接口：按 (requestID, direction) 反查
// 最近一条已落盘条目的 (file, offset)。BufferedRawSink 灰度前必须实现等价
// 能力（R12 预研 §10 问题 4 决议：硬前置）。
type FrameLookup interface {
	LookupFrame(requestID, direction string) (file string, offset int64, ok bool)
}

// rawEntryWriter 是包装层（Async/Buffered）对底层落盘执行器的包内窄接口。
// 这是 R10b「RawDataLogger 具体类型下线阻塞点」的实际解除动作：包装层不再
// 依赖具体类型，只依赖此接口（R12 预研 §3.4）；公开构造函数签名不变。
type rawEntryWriter interface {
	writeEntry(RawDataEntry)
	writeEntries([]RawDataEntry)
	writeEntriesFallible(entries []RawDataEntry) (written int, err error)
	Sync() error
	Close() error
	CurrentLocation() (string, int64)
	HasFile() bool
	peekPostWriteLocation() (string, int64)
	sinkEnabled() bool
}

// 编译期断言：接口三方落地 + 信封/逐帧能力归属（R12 预研 §3.3）。
var (
	_ RawSink = (*RawDataLogger)(nil)
	_ RawSink = (*AsyncRawDataLogger)(nil)
	_ RawSink = (*BufferedRawSink)(nil)

	_ rawEntryWriter = (*RawDataLogger)(nil)

	_ EnvelopeRawSink = (*AsyncRawDataLogger)(nil)
	_ EnvelopeRawSink = (*BufferedRawSink)(nil)

	_ FrameLookup = (*AsyncRawDataLogger)(nil)
	_ FrameLookup = (*BufferedRawSink)(nil)
)

// 灰度档位（LLM_GATEWAY_RAW_LOG_SINK 的合法取值）。
const (
	RawSinkModeLegacy   = "legacy"
	RawSinkModeBuffered = "buffered"
)

// RawSinkModeName 返回灰度档位的规范化名称（空值按 legacy），供启动日志
// 打印最终生效的 sink 模式，便于现场核对。
func RawSinkModeName(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case RawSinkModeBuffered:
		return RawSinkModeBuffered
	default:
		return RawSinkModeLegacy
	}
}

// NewRawSink 是 R12 灰度工厂（预研 §5）。mode 为空或 "legacy" 返回现网
// AsyncRawDataLogger（行为零变化）；"buffered" 返回 BufferedRawSink（默认
// 窗口 100ms / 50 条 / 4MiB，与现网 Async 节奏对齐以便灰度对比）；其他值
// 返回错误——调用方（cmd/gateway）必须拒绝启动：拼错静默回退会无声改变
// 审计实现。
//
// 回滚 = 环境变量改回 legacy + 重启进程（sink 在启动期创建，不做运行时
// 热切换）；两实现共用 JSONL 格式与目录约定，带时间戳文件不互相覆盖，
// 无数据迁移。
func NewRawSink(baseDir string, maxSize int64, enabled bool, mode string) (RawSink, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", RawSinkModeLegacy:
		return NewAsyncRawDataLogger(baseDir, maxSize, enabled, 10000)
	case RawSinkModeBuffered:
		return NewBufferedRawSink(baseDir, maxSize, enabled, BufferedRawSinkConfig{})
	default:
		return nil, fmt.Errorf("unsupported raw log sink mode %q (want %q or %q)", mode, RawSinkModeLegacy, RawSinkModeBuffered)
	}
}

// rawFrameLocation 是逐帧索引的值类型：File 为条目落盘时轮转文件的路径，
// Offset 为该条 JSON 行的起始字节。Async 与 Buffered 共用。
type rawFrameLocation struct {
	File   string
	Offset int64
}

// rawFrameIndex 是 (requestID, direction) -> rawFrameLocation 的逐帧索引，
// AsyncRawDataLogger 与 BufferedRawSink 共用同一实现以杜绝漂移（R12 预研
// §8「双实现长期并存漂移」缓解措施）。record 在每次成功写出后调用：
// 从写出后的 (file, offset) 反推批内每条的起始偏移；批内跨 rotate 边界时
// cursor 变负即停止索引（2026-08-06 P1-2 修复语义，勿改）。
type rawFrameIndex struct {
	m sync.Map // key: "requestID|direction" -> rawFrameLocation
}

func (fi *rawFrameIndex) record(entries []RawDataEntry, postWrite func() (string, int64)) {
	if fi == nil || len(entries) == 0 || postWrite == nil {
		return
	}
	file, cursor := postWrite()
	if file == "" {
		return
	}
	// Walk the slice in reverse, peeling off the entry's JSON-line
	// size from the post-write offset one at a time. This keeps each
	// entry's start offset exact without re-marshalling.
	for i := len(entries) - 1; i >= 0; i-- {
		lineSize := int64(len(entries[i].RawDataEncodingJSON())) + 1 // +1 for trailing newline
		if lineSize <= 1 {
			// empty RawData; treat as one byte placeholder.
			lineSize = 1
		}
		nextCursor := cursor - lineSize
		if nextCursor < 0 {
			// Batch crosses file rotation boundary. The remaining entries
			// are in a prior file; stop indexing to avoid incorrect location.
			slog.Debug("rawFrameIndex: batch crosses rotation boundary, stopping early",
				"entries_indexed", len(entries)-i-1,
				"entries_total", len(entries))
			break
		}
		cursor = nextCursor
		key := entries[i].RequestID + "|" + entries[i].Direction
		fi.m.Store(key, rawFrameLocation{File: file, Offset: cursor})
	}
}

func (fi *rawFrameIndex) lookup(requestID, direction string) (file string, offset int64, ok bool) {
	if fi == nil || requestID == "" || direction == "" {
		return "", 0, false
	}
	if v, hit := fi.m.Load(requestID + "|" + direction); hit {
		loc := v.(rawFrameLocation)
		return loc.File, loc.Offset, true
	}
	return "", 0, false
}

// makeRawEntry 按信封构建 RawDataEntry（Async/Buffered 共用，2026-07-28 §5.7
// 字段集合）。Headers 做浅拷贝，防调用方在异步落盘前复用底层 map（R12
// 预研 §4）。body 非空时计算 SHA-256 供审计对账。
func makeRawEntry(direction, requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope) RawDataEntry {
	entry := RawDataEntry{
		Timestamp:        time.Now(),
		RequestID:        requestID,
		Direction:        direction,
		Protocol:         protocol,
		DataSize:         len(body),
		RawData:          encodeRawData(body),
		RawDataEncoding:  "base64",
		ConversionStep:   conversionStep,
		ClientRequestID:  env.ClientRequestID,
		GWSessionID:      env.GWSessionID,
		GWTaskID:         env.GWTaskID,
		ParentRequestID:  env.ParentRequestID,
		TenantID:         env.TenantID,
		ApplicationID:    env.ApplicationID,
		APIKeyID:         env.APIKeyID,
		ProviderID:       env.ProviderID,
		CredentialID:     env.CredentialID,
		AttemptNo:        env.AttemptNo,
		UpstreamEndpoint: env.UpstreamEndpoint,
		TraceID:          env.TraceID,
		SpanID:           env.SpanID,
		ChunkIndex:       env.ChunkIndex,
	}
	if headers != nil {
		entry.Headers = make(map[string]string, len(headers))
		for k, v := range headers {
			entry.Headers[k] = v
		}
	}
	if len(body) > 0 {
		entry.SHA256 = hashBytes(body)
	}
	return entry
}

// makeOverflowStub 构建缓冲/队列满时的 overflow stub 条目（Async/Buffered
// 共用）：direction 固定为 "overflow"，Error 携带 "raw_log_queue_full:<direction>"，
// 原始 payload 丢弃但信封保留，供外部端点与 request_logs 关联去重。
func makeOverflowStub(direction, requestID, protocol, conversionStep string, env RawCorrelationEnvelope) RawDataEntry {
	return RawDataEntry{
		Timestamp:        time.Now(),
		RequestID:        requestID,
		Direction:        "overflow",
		Protocol:         protocol,
		DataSize:         0,
		ConversionStep:   conversionStep,
		ClientRequestID:  env.ClientRequestID,
		GWSessionID:      env.GWSessionID,
		GWTaskID:         env.GWTaskID,
		ParentRequestID:  env.ParentRequestID,
		TenantID:         env.TenantID,
		ApplicationID:    env.ApplicationID,
		APIKeyID:         env.APIKeyID,
		ProviderID:       env.ProviderID,
		CredentialID:     env.CredentialID,
		AttemptNo:        env.AttemptNo,
		UpstreamEndpoint: env.UpstreamEndpoint,
		TraceID:          env.TraceID,
		SpanID:           env.SpanID,
		Error:            "raw_log_queue_full:" + direction,
	}
}
