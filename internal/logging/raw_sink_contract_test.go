package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// R12（2026-09-11 预研 §6）：三实现共跑的 table-driven 行为契约测试集。
// sinkFactory 为每个用例产出写入独立临时目录的启用 sink；所有用例以
// Close 收尾（幂等契约保证重复 Close 安全），确保 Windows 下 TempDir
// 清理不因未关闭句柄失败。

type sinkFactory struct {
	name string
	new  func(t *testing.T) (RawSink, string)
}

func allSinkFactories() []sinkFactory {
	return []sinkFactory{
		{"sync", func(t *testing.T) (RawSink, string) {
			t.Helper()
			dir := t.TempDir()
			s, err := NewRawDataLogger(dir, 1024*1024, true)
			if err != nil {
				t.Fatalf("NewRawDataLogger: %v", err)
			}
			return s, dir
		}},
		{"async", func(t *testing.T) (RawSink, string) {
			t.Helper()
			dir := t.TempDir()
			s, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 4096)
			if err != nil {
				t.Fatalf("NewAsyncRawDataLogger: %v", err)
			}
			return s, dir
		}},
		{"buffered", func(t *testing.T) (RawSink, string) {
			t.Helper()
			dir := t.TempDir()
			s, err := NewBufferedRawSink(dir, 1024*1024, true, BufferedRawSinkConfig{
				BatchSize:  4,
				FlushEvery: 20 * time.Millisecond,
				MaxBytes:   1 << 20,
			})
			if err != nil {
				t.Fatalf("NewBufferedRawSink: %v", err)
			}
			return s, dir
		}},
	}
}

// readSinkEntries 读取目录下全部 raw_data_*.jsonl 并解析为条目。
// skipStubDirections 用于剔除 close_drained / overflow 等控制条目。
func readSinkEntries(t *testing.T, dir string, skipStubDirections bool) []RawDataEntry {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var entries []RawDataEntry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e RawDataEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("unmarshal line in %s: %v (%s)", f, err, line)
			}
			if skipStubDirections && (e.Direction == "close_drained" || e.Direction == "overflow") {
				continue
			}
			entries = append(entries, e)
		}
	}
	return entries
}

// normalizeForGolden 把实现间已知差异字段归零，产出可比对的归一化条目：
// Timestamp（各实现落盘时刻不同）与 SHA256（RawDataLogger 低层执行器不做
// 信封增强，属设计内差异，见预研 §2.3b）。
func normalizeForGolden(e RawDataEntry) RawDataEntry {
	e.Timestamp = time.Time{}
	e.SHA256 = ""
	return e
}

func goldenKey(e RawDataEntry) string {
	return e.RequestID + "|" + e.Direction
}

func TestRawSinkContract_FiveDirectionsGolden(t *testing.T) {
	type capture struct {
		name    string
		entries []RawDataEntry
	}
	var captures []capture

	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			t.Cleanup(func() { _ = sink.Close() })

			sink.LogClientRequest("r1", "openai-chat", []byte(`{"side":"client-request"}`), map[string]string{"h1": "v1"}, "pre_parse")
			sink.LogUpstreamRequest("r2", "openai-chat", []byte(`{"side":"upstream-request"}`), "post_conversion")
			sink.LogUpstreamResponse("r3", "openai-chat", []byte(`{"side":"upstream-response"}`), "post_conversion")
			sink.LogClientResponse("r4", "openai-chat", []byte(`{"side":"client-response"}`), "post_conversion")
			sink.LogConversionError("r5", "openai-chat", "upstream_response", "pre_parse", []byte(`{"side":"conversion-error"}`), errors.New("boom"))

			if err := sink.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			entries := readSinkEntries(t, dir, true)
			if len(entries) != 5 {
				t.Fatalf("expected 5 entries, got %d", len(entries))
			}
			seen := map[string]bool{}
			for _, e := range entries {
				seen[goldenKey(e)] = true
				switch e.RequestID {
				case "r1":
					if e.Direction != "client_request" || e.ConversionStep != "pre_parse" || e.Headers["h1"] != "v1" {
						t.Errorf("r1 fields drifted: %+v", e)
					}
				case "r2":
					if e.Direction != "upstream_request" || e.ConversionStep != "post_conversion" {
						t.Errorf("r2 fields drifted: %+v", e)
					}
				case "r3":
					if e.Direction != "upstream_response" || e.ConversionStep != "post_conversion" {
						t.Errorf("r3 fields drifted: %+v", e)
					}
				case "r4":
					if e.Direction != "client_response" || e.ConversionStep != "post_conversion" {
						t.Errorf("r4 fields drifted: %+v", e)
					}
				case "r5":
					if e.Error != "boom" || e.Direction != "upstream_response" || e.ConversionStep != "pre_parse" {
						t.Errorf("r5 fields drifted: %+v", e)
					}
				}
				if e.RawDataEncoding != "base64" {
					t.Errorf("%s: raw_data_encoding = %q", goldenKey(e), e.RawDataEncoding)
				}
			}
			for _, key := range []string{"r1|client_request", "r2|upstream_request", "r3|upstream_response", "r4|client_response", "r5|upstream_response"} {
				if !seen[key] {
					t.Errorf("missing entry %s", key)
				}
			}
			norm := make([]RawDataEntry, 0, len(entries))
			for _, e := range entries {
				norm = append(norm, normalizeForGolden(e))
			}
			captures = append(captures, capture{name: f.name, entries: norm})
		})
	}

	// 跨实现 golden 等价：归一化后三实现条目集合必须完全一致。
	base := captures[0]
	for _, other := range captures[1:] {
		if len(other.entries) != len(base.entries) {
			t.Fatalf("%s has %d entries, %s has %d", other.name, len(other.entries), base.name, len(base.entries))
		}
		byKey := make(map[string]RawDataEntry, len(base.entries))
		for _, e := range base.entries {
			byKey[goldenKey(e)] = e
		}
		for _, e := range other.entries {
			want, ok := byKey[goldenKey(e)]
			if !ok {
				t.Fatalf("%s: unexpected entry %s", other.name, goldenKey(e))
			}
			if e.RawData != want.RawData || e.Protocol != want.Protocol || e.ConversionStep != want.ConversionStep ||
				e.Error != want.Error || e.DataSize != want.DataSize || e.Headers["h1"] != want.Headers["h1"] {
				t.Errorf("%s: entry %s drifted from %s:\n got %+v\nwant %+v", other.name, goldenKey(e), base.name, e, want)
			}
		}
	}
}

func TestRawSinkContract_DisabledCreatesNoFile(t *testing.T) {
	factories := []struct {
		name string
		new  func(t *testing.T) RawSink
	}{
		{"sync", func(t *testing.T) RawSink {
			s, err := NewRawDataLogger(t.TempDir(), 1024*1024, false)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"async", func(t *testing.T) RawSink {
			s, err := NewAsyncRawDataLogger(t.TempDir(), 1024*1024, false, 16)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"buffered", func(t *testing.T) RawSink {
			s, err := NewBufferedRawSink(t.TempDir(), 1024*1024, false, BufferedRawSinkConfig{})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
	}
	for _, f := range factories {
		t.Run(f.name, func(t *testing.T) {
			sink := f.new(t)
			if sink.HasFile() {
				t.Errorf("disabled sink reports HasFile()=true")
			}
			if p, o := sink.CurrentLocation(); p != "" || o != 0 {
				t.Errorf("disabled sink CurrentLocation = (%q,%d), want empty", p, o)
			}
			sink.LogUpstreamRequest("r", "openai-chat", []byte("x"), "post_conversion")
			if err := sink.Sync(); err != nil {
				t.Errorf("disabled Sync = %v, want nil", err)
			}
			if err := sink.Close(); err != nil {
				t.Errorf("disabled Close = %v, want nil", err)
			}
		})
	}
}

func TestRawSinkContract_LocationAdvances(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			t.Cleanup(func() { _ = sink.Close() })

			if !sink.HasFile() {
				t.Fatalf("expected HasFile()=true after construction")
			}
			path0, offset0 := sink.CurrentLocation()
			if !strings.HasPrefix(path0, dir) {
				t.Errorf("path %q not under %s", path0, dir)
			}
			if offset0 != 0 {
				t.Errorf("offset0 = %d, want 0", offset0)
			}

			sink.LogUpstreamRequest("r1", "openai-chat", []byte(`{"a":1}`), "post_conversion")
			if err := sink.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			_, offset1 := sink.CurrentLocation()
			if offset1 <= offset0 {
				t.Errorf("offset did not advance: %d -> %d", offset0, offset1)
			}

			// 幂等 Close：两次调用返回同一错误。
			if err := sink.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := sink.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			if sink.HasFile() {
				t.Errorf("HasFile after Close = true, want false")
			}
		})
	}
}

func TestRawSinkContract_SyncBarrierWithConcurrentLog(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			t.Cleanup(func() { _ = sink.Close() })

			const n = 100
			produced := make(chan struct{})
			go func() {
				defer close(produced)
				for i := 0; i < n; i++ {
					sink.LogUpstreamRequest("sync-r", "openai-chat", []byte{byte('a' + i%26)}, "post_conversion")
				}
			}()
			<-produced
			if err := sink.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}

			// 屏障语义：Sync 返回时 producer 全部条目必然已落盘可读。
			entries := readSinkEntries(t, dir, true)
			if len(entries) != n {
				t.Fatalf("after Sync: %d entries, want %d", len(entries), n)
			}
		})
	}
}

func TestRawSinkContract_CloseDrainsPending(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			for i := 0; i < 3; i++ {
				sink.LogUpstreamRequest("drain-r", "openai-chat", []byte{byte('a' + i)}, "post_conversion")
			}
			// 不等 ticker，直接 Close 排空。
			if err := sink.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			entries := readSinkEntries(t, dir, true)
			if len(entries) != 3 {
				t.Fatalf("after Close: %d entries, want 3", len(entries))
			}
		})
	}
}

func TestRawSinkContract_CloseThenLogIsNoop(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			sink.LogUpstreamRequest("pre", "openai-chat", []byte("x"), "post_conversion")
			if err := sink.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			before := readSinkEntries(t, dir, false)
			sink.LogUpstreamRequest("post", "openai-chat", []byte("y"), "post_conversion")
			after := readSinkEntries(t, dir, false)
			if len(after) != len(before) {
				t.Fatalf("log after Close changed file: %d -> %d entries", len(before), len(after))
			}
		})
	}
}

func TestRawSinkContract_ConcurrentWrites(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, dir := f.new(t)
			t.Cleanup(func() { _ = sink.Close() })

			const goroutines = 8
			const perG = 50
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := 0; i < perG; i++ {
						sink.LogUpstreamRequest(fmt.Sprintf("g%d-%d", g, i), "openai-chat", []byte("payload"), "post_conversion")
					}
				}(g)
			}
			wg.Wait()
			if err := sink.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}

			entries := readSinkEntries(t, dir, true)
			if len(entries) != goroutines*perG {
				t.Fatalf("got %d entries, want %d (no drops allowed at this load)", len(entries), goroutines*perG)
			}
			seen := make(map[string]bool, len(entries))
			for _, e := range entries {
				key := goldenKey(e)
				if seen[key] {
					t.Errorf("duplicate entry %s", key)
				}
				seen[key] = true
			}
			if len(seen) != goroutines*perG {
				t.Fatalf("got %d unique keys, want %d", len(seen), goroutines*perG)
			}
		})
	}
}

// TestRawSinkContract_CloseWhileLogging 把 diagnostic_lifecycle_test.go 的
// 关停合同推广为三实现共用（预研 §6）：并发写入者存活时 Close 必须安全。
func TestRawSinkContract_CloseWhileLogging(t *testing.T) {
	for _, f := range allSinkFactories() {
		t.Run(f.name, func(t *testing.T) {
			sink, _ := f.new(t)
			stop := make(chan struct{})
			var producers sync.WaitGroup
			for producer := 0; producer < 16; producer++ {
				producers.Add(1)
				go func(id int) {
					defer producers.Done()
					for {
						select {
						case <-stop:
							return
						default:
							sink.LogUpstreamResponse("race-r", "openai-completions", []byte("payload"), "pre_conversion")
						}
					}
				}(producer)
			}
			time.Sleep(20 * time.Millisecond)
			if err := sink.Close(); err != nil {
				t.Errorf("Close while logging: %v", err)
			}
			// 幂等：并发 Close 亦安全。
			_ = sink.Close()
			close(stop)
			producers.Wait()
		})
	}
}

func TestNewRawSink_FactoryModes(t *testing.T) {
	// 每个构造用独立目录：rotate 文件名取时钟，Windows 时钟粒度粗，
	// 同目录快速连续构造会 O_EXCL 撞名（预存特性，生产单 logger 低频
	// rotate 不受影响）。
	newDir := func() string {
		t.Helper()
		return filepath.Join(t.TempDir(), "sink")
	}

	// 空值 / legacy（含大小写与空白）→ 现网 AsyncRawDataLogger。
	for _, mode := range []string{"", "legacy", " LEGACY ", "Legacy"} {
		s, err := NewRawSink(newDir(), 1024*1024, true, mode)
		if err != nil {
			t.Fatalf("NewRawSink(%q): %v", mode, err)
		}
		if _, ok := s.(*AsyncRawDataLogger); !ok {
			t.Errorf("NewRawSink(%q) = %T, want *AsyncRawDataLogger", mode, s)
		}
		_ = s.Close()
	}

	// buffered（大小写不敏感）→ BufferedRawSink，默认窗口与现网节奏对齐。
	for _, mode := range []string{"buffered", "Buffered", " BUFFERED "} {
		s, err := NewRawSink(newDir(), 1024*1024, true, mode)
		if err != nil {
			t.Fatalf("NewRawSink(%q): %v", mode, err)
		}
		b, ok := s.(*BufferedRawSink)
		if !ok {
			t.Fatalf("NewRawSink(%q) = %T, want *BufferedRawSink", mode, s)
		}
		cfg := b.Config()
		if cfg.FlushEvery != defaultBufferedFlushEvery || cfg.BatchSize != defaultBufferedBatchSize || cfg.MaxBytes != defaultBufferedMaxBytes {
			t.Errorf("buffered defaults drifted: %+v", cfg)
		}
		_ = s.Close()
	}

	// 非法值 → 错误（调用方必须拒绝启动）。
	for _, mode := range []string{"bogus", "Legacy,", "buffer", "0"} {
		if s, err := NewRawSink(newDir(), 1024*1024, true, mode); err == nil {
			_ = s.Close()
			t.Errorf("NewRawSink(%q) accepted, want error", mode)
		}
	}

	// disabled 透传：buffered + enabled=false 不创建文件。
	sub := newDir()
	s, err := NewRawSink(sub, 1024*1024, false, RawSinkModeBuffered)
	if err != nil {
		t.Fatal(err)
	}
	if s.HasFile() {
		t.Errorf("disabled buffered sink reports HasFile()=true")
	}
	if err := s.Close(); err != nil {
		t.Errorf("disabled buffered Close = %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(sub, "raw_data_*.jsonl")); len(files) != 0 {
		t.Errorf("disabled buffered sink created files: %v", files)
	}

	// RawSinkModeName：启动日志用规范化名称。
	if got := RawSinkModeName(""); got != RawSinkModeLegacy {
		t.Errorf("RawSinkModeName(\"\") = %q", got)
	}
	if got := RawSinkModeName(" buffered "); got != RawSinkModeBuffered {
		t.Errorf("RawSinkModeName(buffered) = %q", got)
	}
}
