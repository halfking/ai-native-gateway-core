package logging

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R12（预研 §2.6 盲点 1-8）补齐测试：写失败注入、rotate 失败、Log* void
// 吞错、Close 排空、frameIndex 跨 rotate 边界、cleanupOldFiles 保留边界。

// forceCloseHandle 在 logger 不知情的情况下关闭底层文件句柄，制造稳定的
// "写入失败"状态（跨平台确定性；磁盘满/权限注入在单测中不可移植）。
func forceCloseHandle(t *testing.T, l *RawDataLogger) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		t.Fatalf("no open file to break")
	}
	if err := l.file.Close(); err != nil {
		t.Fatalf("pre-close: %v", err)
	}
}

func TestRawDataLogger_RotateRetriesExistingCandidate(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, time.September, 11, 12, 34, 56, 123456789, time.UTC)
	oldNow := rawDataLoggerNow
	rawDataLoggerNow = func() time.Time { return fixed }
	t.Cleanup(func() { rawDataLoggerNow = oldNow })

	candidate := filepath.Join(dir, fmt.Sprintf("raw_data_%s_%d.jsonl", fixed.Format("20060102_150405.000000000"), fixed.UnixNano()))
	if err := os.WriteFile(candidate, nil, 0600); err != nil {
		t.Fatal(err)
	}

	l, err := NewRawDataLogger(dir, 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	path, _ := l.CurrentLocation()
	if path == candidate {
		t.Fatalf("rotate reused existing candidate %q", candidate)
	}
	if !strings.HasPrefix(filepath.Base(path), "raw_data_") || !strings.HasSuffix(path, ".jsonl") {
		t.Fatalf("rotated path %q is outside raw_data_*.jsonl contract", path)
	}
	l.LogUpstreamRequest("retry-rotate", "openai-chat", []byte("payload"), "pre_parse")
	if !l.HasFile() {
		t.Fatal("logger lost its file after collision retry")
	}
}

func TestRawDataLogger_RotateFailureClearsClosedHandle(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRawDataLogger(dir, 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}

	l.baseDir = filepath.Join(dir, "missing")
	if err := l.rotate(); err == nil {
		t.Fatal("expected rotate failure")
	}
	if l.file != nil {
		t.Fatalf("file handle retained after rotate failure: %v", l.file)
	}
	if l.HasFile() {
		t.Fatal("HasFile reported true after rotate failure")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close after rotate failure: %v", err)
	}
}

func TestRawDataLogger_RotateExhaustedCollisionsClearsClosedHandle(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRawDataLogger(dir, 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	fixed := time.Date(2026, time.September, 11, 12, 34, 56, 123456789, time.UTC)
	oldNow := rawDataLoggerNow
	rawDataLoggerNow = func() time.Time { return fixed }
	defer func() { rawDataLoggerNow = oldNow }()
	for attempt := 0; attempt < rawDataLoggerRotateAttempts; attempt++ {
		name := fmt.Sprintf("raw_data_%s_%d", fixed.Format("20060102_150405.000000000"), fixed.UnixNano())
		if attempt > 0 {
			name += fmt.Sprintf("_%d", attempt)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := l.rotate(); err == nil {
		t.Fatal("expected rotate failure after exhausting collisions")
	}
	if l.file != nil {
		t.Fatalf("file handle retained after exhausted collisions: %v", l.file)
	}
}

// TestRawDataLogger_WriteFailureDropsBatchRemainder 覆盖盲点 1：写失败时
// 批次剩余条目静默丢弃（现网既有行为，R12 预研 §10 问题 5 未拍板前不改
// 语义，这里先显式锁定），可错变体显式返回 (written, err)。
func TestRawDataLogger_WriteFailureDropsBatchRemainder(t *testing.T) {
	l, err := NewRawDataLogger(t.TempDir(), 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	forceCloseHandle(t, l)

	batch := []RawDataEntry{
		{RequestID: "wf-1", Direction: "upstream_request", Protocol: "openai-chat", RawData: "aQ==", RawDataEncoding: "base64"},
		{RequestID: "wf-2", Direction: "upstream_request", Protocol: "openai-chat", RawData: "Yg==", RawDataEncoding: "base64"},
		{RequestID: "wf-3", Direction: "upstream_request", Protocol: "openai-chat", RawData: "Yw==", RawDataEncoding: "base64"},
	}
	written, werr := l.writeEntriesFallible(batch)
	if werr == nil {
		t.Fatalf("expected write error on broken handle")
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}

	// 盲点 3：void writeEntries / Log* 吞错不 panic，位置信息保持一致。
	before, _ := l.CurrentLocation()
	l.writeEntries(batch)
	l.LogClientRequest("wf-void", "openai-chat", []byte("x"), nil, "pre_parse")
	if after, _ := l.CurrentLocation(); after != before {
		t.Errorf("failed writes advanced location: %q -> %q", before, after)
	}
}

// TestRawDataLogger_CloseIdempotentRemembersError 锁定 R12 Close 幂等契约：
// 首次 Close 的错误被记住，后续调用返回同一错误（预研 §2.3 勘误 6）。
func TestRawDataLogger_CloseIdempotentRemembersError(t *testing.T) {
	l, err := NewRawDataLogger(t.TempDir(), 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}

	forceCloseHandle(t, l)

	first := l.Close()
	if first == nil || !errors.Is(first, os.ErrClosed) {
		t.Fatalf("first Close err = %v, want os.ErrClosed", first)
	}
	second := l.Close()
	if !errors.Is(second, os.ErrClosed) {
		t.Fatalf("second Close err = %v, want remembered os.ErrClosed", second)
	}
	if first.Error() != second.Error() {
		t.Errorf("Close errors diverge: %v vs %v", first, second)
	}
}

// TestRawDataLogger_RotateFailureSurfaces 覆盖盲点 2：rotate 失败经可错
// 变体显式返回错误（baseDir 指向已消失目录，跨平台确定性）。
func TestRawDataLogger_RotateFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRawDataLogger(dir, 512, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	// 旋转目标目录消失 → OpenFile 失败。maxSize=512 而单条 >512，触发 rotate。
	l.mu.Lock()
	l.baseDir = filepath.Join(dir, "gone")
	l.mu.Unlock()

	entry := RawDataEntry{RequestID: "rot-1", Direction: "upstream_request", Protocol: "openai-chat", RawData: strings.Repeat("QUJD", 200), RawDataEncoding: "base64"}
	written, rerr := l.writeEntriesFallible([]RawDataEntry{entry})
	if rerr == nil {
		t.Fatalf("expected rotate failure to surface")
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
}

// TestAsyncRawDataLogger_FrameIndexRotationBoundary 覆盖盲点 5（P1-2 回归）：
// 批内跨 rotate 边界时 cursor 变负必须停止索引，索引命中项的位置必须能
// 读回正确条目，绝不允许伪位置。
func TestAsyncRawDataLogger_FrameIndexRotationBoundary(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("a"), 400)
	probe := makeRawEntry("upstream_request", "size-probe", "openai-chat", body, nil, "post_conversion", RawCorrelationEnvelope{})
	lineSize := int64(len(probe.RawDataEncodingJSON())) + 1

	// maxSize 约为单行的 2.5 倍：批内 A、B 顺序写入，C 触发唯一一次
	// rotate（rotate 文件名取时钟，Windows 粒度粗，同 tick 撞名会 O_EXCL
	// 失败——先睡一个 tick 与构造期 rotate 拉开，并保证批内只 rotate 一次）。
	// recordFrameLocations 从新文件回推，B 即触发 cursor<0 停止。
	l, err := NewAsyncRawDataLogger(dir, lineSize*5/2, true, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	time.Sleep(10 * time.Millisecond)

	for _, rid := range []string{"fb-A", "fb-B", "fb-C"} {
		l.LogUpstreamRequest(rid, "openai-chat", body, "post_conversion")
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}

	// fb-C（批内最后一条，独占最后一个文件）必须命中且位置精确。
	fileC, offsetC, ok := l.LookupFrame("fb-C", "upstream_request")
	if !ok {
		t.Fatalf("fb-C expected hit")
	}
	data, err := os.ReadFile(fileC)
	if err != nil {
		t.Fatal(err)
	}
	if offsetC < 0 || offsetC >= int64(len(data)) {
		t.Fatalf("fb-C offset %d out of bounds (file %d bytes)", offsetC, len(data))
	}
	line := data[offsetC:]
	if nl := strings.IndexByte(string(line), '\n'); nl >= 0 {
		line = line[:nl]
	}
	if !strings.Contains(string(line), `"request_id":"fb-C"`) {
		t.Errorf("fb-C frame at offset %d resolves to wrong line: %s", offsetC, line)
	}

	// fb-A / fb-B 位于更早的文件：batch 未被 ticker 拆分时 cursor 变负
	// 停止索引（miss）；若重载 CI 下 worker ticker 抢先刷出前缀批，它们
	// 会被正常索引（hit）。两种结果都合法，唯一不允许的是伪位置——命中
	// 时必须能读回正确条目。
	for _, rid := range []string{"fb-A", "fb-B"} {
		f, o, ok := l.LookupFrame(rid, "upstream_request")
		if !ok {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: read indexed file %s: %v", rid, f, err)
		}
		if o < 0 || o >= int64(len(data)) {
			t.Errorf("%s: indexed offset %d out of bounds (file %d bytes)", rid, o, len(data))
			continue
		}
		line := data[o:]
		if nl := strings.IndexByte(string(line), '\n'); nl >= 0 {
			line = line[:nl]
		}
		if !strings.Contains(string(line), `"request_id":"`+rid+`"`) {
			t.Errorf("%s: indexed frame at offset %d resolves to wrong line: %s", rid, o, line)
		}
	}

	// A、B 在第一个文件，C 独占 rotate 出的新文件（rotate 真实发生）。
	files, _ := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
	if len(files) < 2 {
		t.Fatalf("expected >=2 rotated files, got %d", len(files))
	}
}

// TestAsyncRawDataLogger_CloseDrainsQueueUnderDeadline 覆盖盲点 4 的可确定
// 部分：队列未饱和时 Close 在 5*flushDelay deadline 内完整排空，并追加
// close_drained stub。
func TestAsyncRawDataLogger_CloseDrainsQueueUnderDeadline(t *testing.T) {
	dir := t.TempDir()
	l, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 1024)
	if err != nil {
		t.Fatal(err)
	}

	const n = 200
	for i := 0; i < n; i++ {
		l.LogUpstreamRequest("drain-a", "openai-chat", []byte("payload"), "post_conversion")
	}
	start := time.Now()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close took %s, drain deadline is 500ms", elapsed)
	}
	if got := l.queue.Size(); got != 0 {
		t.Errorf("queue not drained: %d remaining", got)
	}
	entries := readSinkEntries(t, dir, false)
	seen := 0
	sawStub := false
	for _, e := range entries {
		if e.Direction == "close_drained" {
			sawStub = true
			continue
		}
		if e.RequestID == "drain-a" {
			seen++
		}
	}
	if seen != n {
		t.Errorf("drained %d entries, want %d", seen, n)
	}
	if !sawStub {
		t.Errorf("close_drained stub missing")
	}
}

// TestRawDataLogger_CleanupOldFilesKeepBoundary 覆盖盲点 6：清理只保留
// 最近 keepCount 个文件，且按修改时间排序。
func TestRawDataLogger_CleanupOldFilesKeepBoundary(t *testing.T) {
	dir := t.TempDir()
	l, err := NewRawDataLogger(dir, 1024*1024, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	const total = 13
	const keep = 10
	base := time.Now().Add(-time.Hour)
	for i := 0; i < total; i++ {
		name := filepath.Join(dir, "raw_data_"+strings.Repeat("x", i+1)+".jsonl")
		if err := os.WriteFile(name, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	l.cleanupOldFiles(keep)

	files, err := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// 14 个文件（13 个历史 + logger 当前文件）保留最近 10 个：当前文件
	//（mtime=now）+ 最新的 9 个历史档（4..12 分钟），最旧 4 档被清理。
	if len(files) != keep {
		t.Fatalf("files after cleanup = %d, want %d", len(files), keep)
	}
	staleCutoff := base.Add(4 * time.Minute)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Before(staleCutoff) {
			t.Errorf("stale file survived cleanup: %s (mtime %s, cutoff %s)", f, info.ModTime(), staleCutoff)
		}
	}
}

// 2026-09-12 审计：rawFrameIndex 此前只增不删，高并发下按请求数无界增长。
// 锁定三件事：超限后条目总数有界；最旧条目被淘汰（lookup miss）；最新
// 条目仍可命中且位置精确。
func TestRawFrameIndex_BoundedEviction(t *testing.T) {
	var fi rawFrameIndex
	entry := makeRawEntry("upstream_request", "rid", "openai-chat", []byte("x"), nil, "post_conversion", RawCorrelationEnvelope{})
	postWrite := func() (string, int64) { return "f.jsonl", int64(len(entry.RawDataEncodingJSON())) + 1 }

	for i := 0; i < rawFrameIndexMax+rawFrameIndexMax/10; i++ {
		entry.RequestID = fmt.Sprintf("r-%d", i)
		// 切片元素是结构体副本，必须在改完 RequestID 后重建，
		// 否则 record 看到的仍是旧 key（这正是本测试最初写错的形态）。
		fi.record([]RawDataEntry{entry}, postWrite)
	}

	// 总量必须收敛到上限附近（淘汰保留最近 1/4 上限，插入仍在增长，
	// 允许一个批次宽量）。
	if got := fi.count.Load(); got > rawFrameIndexMax+10 {
		t.Fatalf("frame index unbounded: %d entries > cap %d", got, rawFrameIndexMax)
	}
	// 最旧的远端 key 已被淘汰。
	if _, _, ok := fi.lookup("r-0", "upstream_request"); ok {
		t.Fatalf("r-0 should have been evicted")
	}
	// 最新的 key 仍命中。
	file, offset, ok := fi.lookup(fmt.Sprintf("r-%d", rawFrameIndexMax+rawFrameIndexMax/10-1), "upstream_request")
	if !ok {
		t.Fatalf("newest entry evicted prematurely")
	}
	if file != "f.jsonl" || offset != 0 {
		t.Fatalf("newest entry location drifted: file=%s offset=%d", file, offset)
	}
}
