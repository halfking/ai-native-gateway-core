package logging

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// R12（预研 §6 Buffered 专项 + 盲点补齐）：定量触发 / ticker 触发 /
// MaxBytes 上限 / 写失败注入 / close_drained stub / LookupFrame / Sync 屏障 /
// Headers 复制防护。

// stubRawWriter 是 rawEntryWriter 的测试替身：可注入写失败（预研 §6
// "写失败注入（可注入 writer stub）"），并记录已写出行供断言。
type stubRawWriter struct {
	mu           sync.Mutex
	lines        []string
	bytes        int64
	failAttempts int  // 前 N 次 writeEntriesFallible 整体失败（0 条写出）
	alwaysFail   bool // 每次 writeEntriesFallible 都失败
	fsyncFail    bool // 写出成功但 Sync 返回错误（不得重试的形态）
	path         string
}

func newStubRawWriter() *stubRawWriter { return &stubRawWriter{path: "stub_raw_data.jsonl"} }

func (s *stubRawWriter) writeEntry(e RawDataEntry) { s.writeEntries([]RawDataEntry{e}) }

func (s *stubRawWriter) writeEntries(entries []RawDataEntry) {
	_, _ = s.writeEntriesFallible(entries)
}

func (s *stubRawWriter) writeEntriesFallible(entries []RawDataEntry) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alwaysFail {
		return 0, errors.New("stub write failure")
	}
	if s.failAttempts > 0 {
		s.failAttempts--
		return 0, errors.New("stub write failure")
	}
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		line := append(b, '\n')
		s.lines = append(s.lines, string(line))
		s.bytes += int64(len(line))
	}
	if s.fsyncFail {
		return len(entries), errors.New("stub fsync failure")
	}
	return len(entries), nil
}

func (s *stubRawWriter) Sync() error { return nil }

func (s *stubRawWriter) Close() error { return nil }

func (s *stubRawWriter) CurrentLocation() (string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.bytes
}

func (s *stubRawWriter) HasFile() bool { return true }

func (s *stubRawWriter) peekPostWriteLocation() (string, int64) { return s.CurrentLocation() }

func (s *stubRawWriter) sinkEnabled() bool { return true }

func (s *stubRawWriter) writtenLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, what)
}

// TestBufferedRawSink_BatchSizeWakeFlush 覆盖条数阈值触发（ticker 故意设为
// 不会触发的 10s）：第 batchSize 条入缓冲后必须经 wake 立即预刷。
func TestBufferedRawSink_BatchSizeWakeFlush(t *testing.T) {
	s, err := NewBufferedRawSink(t.TempDir(), 1024*1024, true, BufferedRawSinkConfig{
		BatchSize:  4,
		FlushEvery: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for i := 0; i < 4; i++ {
		s.LogUpstreamRequest("batch-r", "openai-chat", []byte("x"), "post_conversion")
	}
	waitForCond(t, 5*time.Second, func() bool {
		return s.Stats().FlushCount > 0
	}, "wake-triggered flush at batch threshold")
}

// TestBufferedRawSink_TickerFlush 覆盖定时触发：条数不足 batchSize 时，
// FlushEvery 到点也要刷盘。
func TestBufferedRawSink_TickerFlush(t *testing.T) {
	s, err := NewBufferedRawSink(t.TempDir(), 1024*1024, true, BufferedRawSinkConfig{
		BatchSize:  100,
		FlushEvery: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.LogUpstreamRequest("tick-r", "openai-chat", []byte("y"), "post_conversion")
	waitForCond(t, 5*time.Second, func() bool {
		return s.Stats().FlushCount > 0
	}, "ticker flush")
}

// TestBufferedRawSink_MaxBytesOverflowStub 覆盖字节预算上限：预算不足时
// 当前条目改写为 overflow stub（信封保留），stub 也放不下才丢弃 + 计数。
func TestBufferedRawSink_MaxBytesOverflowStub(t *testing.T) {
	dir := t.TempDir()
	// FlushEvery 拉长，排除 worker 抢先刷盘对缓冲状态的干扰。
	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{
		BatchSize:  1000,
		FlushEvery: 10 * time.Second,
		MaxBytes:   4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	env := RawCorrelationEnvelope{GWSessionID: "s-overflow"}
	// 单条估算：base64(2048B)≈2724 + 信封余量 ≈ 3.3KB；≤4096 可入。
	body := make([]byte, 2048)
	for i := range body {
		body[i] = 'a'
	}
	for i := 0; i < 4; i++ {
		s.LogUpstreamRequestWithEnvelope("of-r", "openai-chat", body, "post_conversion", env)
	}

	stats := s.Stats()
	// 确定性布局：第 1 条入缓冲；第 2 条超预算 → stub 入缓冲；第 3、4 条
	// 连 stub 都放不下 → 丢弃。
	if stats.Accepted != 2 {
		t.Errorf("Accepted = %d, want 2 (1 real + 1 overflow stub)", stats.Accepted)
	}
	if stats.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2", stats.Dropped)
	}
	if stats.Buffered != 2 {
		t.Errorf("Buffered = %d, want 2", stats.Buffered)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries := readSinkEntries(t, dir, false)
	var sawOverflow bool
	for _, e := range entries {
		if e.Direction == "overflow" && strings.Contains(e.Error, "raw_log_queue_full:upstream_request") {
			sawOverflow = true
			if e.GWSessionID != "s-overflow" {
				t.Errorf("overflow stub lost envelope: %+v", e)
			}
		}
	}
	if !sawOverflow {
		t.Errorf("no overflow stub with queue-full marker in output")
	}
}

// TestBufferedRawSink_CloseWritesDrainedStub 与 Async 的
// TestAsyncRawDataLogger_CloseWritesDrainedStub 对齐（灰度硬前置）。
func TestBufferedRawSink_CloseWritesDrainedStub(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	s.LogUpstreamRequest("close-r", "openai-chat", []byte("z"), "post_conversion")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries := readSinkEntries(t, dir, false)
	var sawDrained bool
	for _, e := range entries {
		if e.Direction == "close_drained" {
			sawDrained = true
			if !strings.Contains(e.Error, "accepted=") {
				t.Errorf("close_drained stub missing stats: %q", e.Error)
			}
		}
	}
	if !sawDrained {
		t.Fatalf("expected close_drained stub entry")
	}
	// R12 审计修正回归：Close 排空批同样写入逐帧索引（与 Async 对称），
	// 优雅关闭后 LookupFrame 仍可命中最后一批条目。
	if _, _, ok := s.LookupFrame("close-r", "upstream_request"); !ok {
		t.Errorf("LookupFrame after Close missed; close-drain batch was not frame-indexed")
	}
}

// TestBufferedRawSink_OverflowStubFieldParity 锁定 overflow stub 与 Async
// 的字段奇偶（R12 审计发现 2）：client_request stub 保留 Headers 与原始
// DataSize；conversion error stub 的 Error 是原始错误文本而非 marker。
// maxBytes 用估算函数精确构造「第 1 条 + stub 恰好放下、第 2 条放不下」
// 的确定性布局。
func TestBufferedRawSink_OverflowStubFieldParity(t *testing.T) {
	dir := t.TempDir()
	env := RawCorrelationEnvelope{GWSessionID: "s-parity"}
	body := make([]byte, 512) // 主体远大于 stub 差值，保证 est(entry) > est(stub)
	headers := map[string]string{"h": "v1"}
	probe := makeRawEntry("client_request", "par-r", "openai-chat", body, headers, "pre_parse", env)
	stub := makeOverflowStub("client_request", "par-r", "openai-chat", "pre_parse", env)
	// 镜像 offerLocked 的奇偶增强，估算才与实现一致。
	stub.Headers = probe.Headers
	stub.DataSize = probe.DataSize
	maxBytes := estimateEntryBytes(probe) + estimateEntryBytes(stub)

	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{
		BatchSize:  1000,
		FlushEvery: 10 * time.Second,
		MaxBytes:   maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.LogClientRequestWithEnvelope("par-r", "openai-chat", body, headers, "pre_parse", env)
	s.LogClientRequestWithEnvelope("par-r", "openai-chat", body, headers, "pre_parse", env) // 超预算 → stub

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var sawReal, sawStub bool
	for _, e := range readSinkEntries(t, dir, false) {
		if e.Direction != "overflow" {
			if e.Headers["h"] == "v1" {
				sawReal = true
			}
			continue
		}
		sawStub = true
		if e.Headers["h"] != "v1" {
			t.Errorf("client_request overflow stub lost Headers: %+v", e.Headers)
		}
		if e.DataSize != len(body) {
			t.Errorf("client_request overflow stub DataSize = %d, want %d", e.DataSize, len(body))
		}
		if !strings.Contains(e.Error, "raw_log_queue_full:client_request") {
			t.Errorf("overflow stub Error = %q, want queue-full marker", e.Error)
		}
		if e.GWSessionID != "s-parity" {
			t.Errorf("overflow stub lost envelope: %+v", e)
		}
	}
	if !sawReal || !sawStub {
		t.Fatalf("expected 1 real entry + 1 overflow stub (real=%v stub=%v)", sawReal, sawStub)
	}
}

// TestBufferedRawSink_ConversionErrorStubCarriesErrText 锁定 conversion
// error 的 stub 奇偶：Async 侧 stub 的 Error 用原始错误文本覆盖 marker，
// Buffered 必须一致。
func TestBufferedRawSink_ConversionErrorStubCarriesErrText(t *testing.T) {
	dir := t.TempDir()
	body := []byte("{\"conv\":1}")
	probe := makeRawEntry("upstream_response", "cerr-r", "openai-chat", body, nil, "pre_parse", RawCorrelationEnvelope{})
	probe.Error = "boom-text" // LogConversionError 会给真实条目带上 Error，估算必须一致
	stub := makeOverflowStub("upstream_response", "cerr-r", "openai-chat", "pre_parse", RawCorrelationEnvelope{})
	stub.Error = "boom-text" // 镜像 offerLocked 的 stubErr 覆盖
	maxBytes := estimateEntryBytes(probe) + estimateEntryBytes(stub)

	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{
		BatchSize:  1000,
		FlushEvery: 10 * time.Second,
		MaxBytes:   maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.LogConversionError("cerr-r", "openai-chat", "upstream_response", "pre_parse", body, errors.New("boom-text"))
	s.LogConversionError("cerr-r", "openai-chat", "upstream_response", "pre_parse", body, errors.New("boom-text")) // → stub

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var sawStub bool
	for _, e := range readSinkEntries(t, dir, false) {
		if e.Direction == "overflow" {
			sawStub = true
			if e.Error != "boom-text" {
				t.Errorf("conversion stub Error = %q, want original error text (Async parity)", e.Error)
			}
		}
	}
	if !sawStub {
		t.Fatalf("expected an overflow stub entry")
	}
}

// TestBufferedRawSink_LookupFrame 覆盖逐帧定位能力（FrameLookup，灰度硬
// 前置）：Sync 后 (requestID, direction) 反查位置必须能读回原条目。
func TestBufferedRawSink_LookupFrame(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bodies := map[string][]byte{
		"client_request":    []byte(`{"f":"c1"}`),
		"upstream_request":  []byte(`{"f":"u1","longer":"body-here"}`),
		"upstream_response": []byte(`{"f":"r1","even":"longer-body-here"}`),
	}
	for direction, body := range bodies {
		s.LogConversionError("lf-r", "openai-chat", direction, "step", body, errors.New("x"))
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	for direction, body := range bodies {
		file, offset, ok := s.LookupFrame("lf-r", direction)
		if !ok {
			t.Fatalf("LookupFrame(lf-r,%s) miss", direction)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if offset < 0 || offset >= int64(len(data)) {
			t.Fatalf("%s: offset %d out of bounds (file %d bytes)", direction, offset, len(data))
		}
		line := data[offset:]
		if nl := strings.IndexByte(string(line), '\n'); nl >= 0 {
			line = line[:nl]
		}
		var e RawDataEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("%s: line at offset %d does not parse: %v", direction, offset, err)
		}
		if e.RequestID != "lf-r" || e.Direction != direction || string(e.RawData) != encodeRawData(body) {
			t.Errorf("%s: frame at offset %d drifted: rid=%q dir=%q", direction, offset, e.RequestID, e.Direction)
		}
	}

	if _, _, ok := s.LookupFrame("lf-r", "client_response"); ok {
		t.Errorf("unexpected hit for unwritten direction")
	}
	if _, _, ok := s.LookupFrame("", "client_request"); ok {
		t.Errorf("empty requestID must miss")
	}
	if _, _, ok := s.LookupFrame("lf-r", ""); ok {
		t.Errorf("empty direction must miss")
	}
}

// TestBufferedRawSink_WriteFailureRetryRecovers 覆盖写失败重试成功路径：
// 前 2 次尝试失败、第 3 次成功（MaxWriteAttempts=3），条目零丢失。
func TestBufferedRawSink_WriteFailureRetryRecovers(t *testing.T) {
	stub := newStubRawWriter()
	stub.failAttempts = 2
	s := newBufferedRawSinkWithBase(stub, BufferedRawSinkConfig{
		BatchSize:        2,
		FlushEvery:       time.Hour,
		MaxWriteAttempts: 3,
	})
	t.Cleanup(func() { _ = s.Close() })

	s.LogUpstreamRequest("retry-r", "openai-chat", []byte("a"), "post_conversion")
	s.LogUpstreamRequest("retry-r", "openai-chat", []byte("b"), "post_conversion")
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	stats := s.Stats()
	if stats.WriteErrors != 2 {
		t.Errorf("WriteErrors = %d, want 2", stats.WriteErrors)
	}
	if stats.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 (recovered within attempts)", stats.Dropped)
	}
	lines := stub.writtenLines()
	if len(lines) != 2 {
		t.Fatalf("stub got %d lines, want 2", len(lines))
	}
}

// TestBufferedRawSink_WriteFailureExceedsRetriesDrops 覆盖降级路径：重试
// 超限后丢弃剩余条目 + dropped 计数，不阻塞、不 panic。
func TestBufferedRawSink_WriteFailureExceedsRetriesDrops(t *testing.T) {
	stub := newStubRawWriter()
	stub.alwaysFail = true
	s := newBufferedRawSinkWithBase(stub, BufferedRawSinkConfig{
		BatchSize:        8,
		FlushEvery:       time.Hour,
		MaxWriteAttempts: 2,
	})
	t.Cleanup(func() { _ = s.Close() })

	for i := 0; i < 3; i++ {
		s.LogUpstreamRequest("drop-r", "openai-chat", []byte("x"), "post_conversion")
	}
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync must not fail on write degradation: %v", err)
	}
	stats := s.Stats()
	if stats.Dropped != 3 {
		t.Errorf("Dropped = %d, want 3", stats.Dropped)
	}
	if stats.WriteErrors != 2 {
		t.Errorf("WriteErrors = %d, want 2 (max attempts)", stats.WriteErrors)
	}
}

// TestBufferedRawSink_FsyncFailureNotRetried 锁定"不得重试已落盘条目"：
// fsync 失败形态返回 (len, err)，重试循环必须原样接受、不二次写出。
func TestBufferedRawSink_FsyncFailureNotRetried(t *testing.T) {
	stub := newStubRawWriter()
	stub.fsyncFail = true
	s := newBufferedRawSinkWithBase(stub, BufferedRawSinkConfig{
		BatchSize:        8,
		FlushEvery:       time.Hour,
		MaxWriteAttempts: 5,
	})
	t.Cleanup(func() { _ = s.Close() })

	s.LogUpstreamRequest("fsync-r", "openai-chat", []byte("x"), "post_conversion")
	_ = s.Sync()

	stats := s.Stats()
	if stats.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 (entries are durable)", stats.Dropped)
	}
	if stats.WriteErrors != 1 {
		t.Errorf("WriteErrors = %d, want 1 (single attempt, no retry)", stats.WriteErrors)
	}
	if len(stub.writtenLines()) != 1 {
		t.Errorf("line must be written exactly once (no duplicate from retry)")
	}
}

// TestBufferedRawSink_HeadersCopiedNotAliased 锁定入缓冲时的 Headers 复制：
// 调用方在异步落盘前复用/改写 map 不得污染审计条目。
func TestBufferedRawSink_HeadersCopiedNotAliased(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{FlushEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	headers := map[string]string{"h": "original"}
	s.LogClientRequest("hdr-r", "openai-chat", []byte("x"), headers, "pre_parse")
	headers["h"] = "mutated"
	headers["extra"] = "late"

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, e := range readSinkEntries(t, dir, true) {
		if e.Headers["h"] != "original" || e.Headers["extra"] != "" {
			t.Errorf("headers aliased into entry: %+v", e.Headers)
		}
	}
}

// TestBufferedRawSink_SetOverflowReporterNilSafe 覆盖盲点 7 的 Buffered 侧。
func TestBufferedRawSink_SetOverflowReporterNilSafe(t *testing.T) {
	s, err := NewBufferedRawSink(t.TempDir(), 1024*1024, true, BufferedRawSinkConfig{MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.SetOverflowReporter(nil)
	var nilSink *BufferedRawSink
	nilSink.SetOverflowReporter(nil) // nil 接收者必须安全（对齐 Async）

	for i := 0; i < 8; i++ {
		s.LogUpstreamRequest("nilrep-r", "openai-chat", []byte("payload-that-overflows-tiny-buffer"), "post_conversion")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestAsyncRawDataLogger_SetOverflowReporterNilSafe 覆盖盲点 7 的 Async 侧。
func TestAsyncRawDataLogger_SetOverflowReporterNilSafe(t *testing.T) {
	logger, err := NewAsyncRawDataLogger(t.TempDir(), 1024*1024, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.SetOverflowReporter(nil)
	var nilLogger *AsyncRawDataLogger
	nilLogger.SetOverflowReporter(nil)

	for i := 0; i < 8; i++ {
		logger.LogUpstreamRequest("nilrep-a", "openai-chat", []byte("payload"), "post_conversion")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestBufferedRawSink_SyncPersistsWithoutClose 锁定 Sync 的"落盘可读且不
// 关停"语义：Sync 后文件即含条目，sink 仍可继续写。
func TestBufferedRawSink_SyncPersistsWithoutClose(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{FlushEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.LogUpstreamRequest("sync-p", "openai-chat", []byte("x"), "post_conversion")
	if len(readSinkEntries(t, dir, true)) != 0 {
		t.Fatalf("entries visible before Sync/Close")
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := len(readSinkEntries(t, dir, true)); got != 1 {
		t.Fatalf("after Sync: %d entries, want 1", got)
	}
	s.LogUpstreamRequest("sync-p", "openai-chat", []byte("y"), "post_conversion")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(readSinkEntries(t, dir, true)); got != 2 {
		t.Fatalf("after Close: %d entries, want 2 (sink usable after Sync)", got)
	}
}
