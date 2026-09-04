package file

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// newTestStore 创建一个测试用存储实例，并在测试结束后优雅关闭。
func newTestStore(t *testing.T, workers int) *FileBodiesStore {
	t.Helper()
	s := NewFileBodiesStore(t.TempDir(), workers)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

// makeBody 构造一条带中文与 RawMessage 负载的会话内容，便于验证序列化往返。
func makeBody(tenantID, sessionID string, turnNo int) *storage.SessionBody {
	return &storage.SessionBody{
		TenantID:  tenantID,
		SessionID: sessionID,
		TurnNo:    turnNo,
		Timestamp: time.Date(2026, 9, 5, 12, 0, 0, 123456789, time.UTC),
		Request:   json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"你好，请介绍一下 gzip 压缩的原理"}]}`),
		Response:  json.RawMessage(`{"choices":[{"message":{"role":"assistant","content":"gzip 使用 DEFLATE 算法……"}}]}`),
		// 注意：JSON 反序列化后数字统一为 float64，因此期望值直接写成 float64
		Metadata: map[string]interface{}{"user_tier": "premium", "retry_count": float64(2)},
	}
}

// assertBodyEqual 逐字段比较两条会话内容，失败时给出可定位的错误信息。
func assertBodyEqual(t *testing.T, want, got *storage.SessionBody) {
	t.Helper()
	if want.TenantID != got.TenantID {
		t.Errorf("TenantID = %q, want %q", got.TenantID, want.TenantID)
	}
	if want.SessionID != got.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if want.TurnNo != got.TurnNo {
		t.Errorf("TurnNo = %d, want %d", got.TurnNo, want.TurnNo)
	}
	if !want.Timestamp.Equal(got.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, want.Timestamp)
	}
	if !bytes.Equal(want.Request, got.Request) {
		t.Errorf("Request = %s, want %s", got.Request, want.Request)
	}
	if !bytes.Equal(want.Response, got.Response) {
		t.Errorf("Response = %s, want %s", got.Response, want.Response)
	}
	if !reflect.DeepEqual(want.Metadata, got.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
}

// TestWriteReadRoundTrip 验证写入（含中文与 RawMessage）后读回字段完全一致，
// 且读取不存在的轮次返回 storage.ErrNotFound。
func TestWriteReadRoundTrip(t *testing.T) {
	s := newTestStore(t, 2)
	ctx := context.Background()

	want := makeBody("tenant-a", "sess-abcdef", 1)
	if err := s.Write(ctx, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, err := s.Read(ctx, "tenant-a", "sess-abcdef", 1)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	assertBodyEqual(t, want, got)

	// 读取不存在的轮次：必须是哨兵 ErrNotFound（errors.Is 可判定）
	_, err = s.Read(ctx, "tenant-a", "sess-abcdef", 999)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Read(missing) error = %v, want storage.ErrNotFound", err)
	}

	// 空 tenantID / sessionID 的写入必须报错
	if err := s.Write(ctx, makeBody("", "sess-abcdef", 2)); err == nil {
		t.Error("Write(empty tenantID) error = nil, want error")
	}
	if err := s.Write(ctx, makeBody("tenant-a", "", 2)); err == nil {
		t.Error("Write(empty sessionID) error = nil, want error")
	}
}

// TestDirectoryLayout 验证落盘文件符合分层目录结构：
// {base}/{tenant}/{sessionID前2位}/{sessionID}/turn_N.json.gz，
// 且 sessionID 不足 2 位时使用全量作为目录名。
func TestDirectoryLayout(t *testing.T) {
	s := newTestStore(t, 2)
	ctx := context.Background()

	const tenantID, sessionID = "tenant-a", "sess-abcdef"
	for turn := 1; turn <= 3; turn++ {
		if err := s.Write(ctx, makeBody(tenantID, sessionID, turn)); err != nil {
			t.Fatalf("Write(turn %d) error = %v", turn, err)
		}
	}

	for turn := 1; turn <= 3; turn++ {
		want := filepath.Join(s.baseDir, tenantID, "se", sessionID,
			fmt.Sprintf("turn_%d.json.gz", turn))
		if _, err := os.Stat(want); err != nil {
			t.Errorf("分层文件 %s 不存在: %v", want, err)
		}
	}

	// sessionID 仅 1 位：目录名使用全量
	if err := s.Write(ctx, makeBody(tenantID, "x", 1)); err != nil {
		t.Fatalf("Write(short sessionID) error = %v", err)
	}
	wantShort := filepath.Join(s.baseDir, tenantID, "x", "x", "turn_1.json.gz")
	if _, err := os.Stat(wantShort); err != nil {
		t.Errorf("短 sessionID 文件 %s 不存在: %v", wantShort, err)
	}
}

// TestCompressionRatio 验证 gzip 压缩率：压缩后字节数 < 原始的 30%。
// 负载特意采用典型的重复消息结构（同一系统提示 + 高度相似的对话消息），
// 这类 LLM 会话负载重复度高、可压缩；随机数据熵高无法压缩，故不使用。
func TestCompressionRatio(t *testing.T) {
	// 构造约 10KB 的重复结构 JSON：100 条内容高度相似的消息数组
	repeated := "这是一段用于压缩率测试的重复消息内容，模拟 LLM 会话中相似度很高的对话文本。"
	messages := make([]map[string]string, 0, 100)
	for i := 0; i < 100; i++ {
		messages = append(messages, map[string]string{
			"role":    "user",
			"content": fmt.Sprintf("[消息 %02d] %s", i, repeated),
		})
	}
	raw, err := json.Marshal(map[string]interface{}{"messages": messages})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(raw) < 10*1024 {
		t.Fatalf("构造的原始负载仅 %d 字节，期望至少 10KB", len(raw))
	}

	// 使用与实现相同的压缩路径（bytes.Buffer + gzip.NewWriter）
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}
	compressed := buf.Bytes()

	t.Logf("原始 %d 字节，压缩后 %d 字节，压缩率 %.1f%%",
		len(raw), len(compressed), 100*float64(len(raw)-len(compressed))/float64(len(raw)))

	if len(compressed)*10 >= len(raw)*3 {
		t.Errorf("压缩后 %d 字节未低于原始 %d 字节的 30%%", len(compressed), len(raw))
	}
}

// TestReadRange 验证区间读取：正常区间返回连续且顺序正确的轮次，
// 区间内缺失的轮次被跳过，startTurn > endTurn 返回空切片。
func TestReadRange(t *testing.T) {
	s := newTestStore(t, 2)
	ctx := context.Background()

	for turn := 1; turn <= 5; turn++ {
		if err := s.Write(ctx, makeBody("tenant-a", "sess-range", turn)); err != nil {
			t.Fatalf("Write(turn %d) error = %v", turn, err)
		}
	}

	got, err := s.ReadRange(ctx, "tenant-a", "sess-range", 2, 4)
	if err != nil {
		t.Fatalf("ReadRange(2,4) error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ReadRange(2,4) 返回 %d 条, want 3", len(got))
	}
	for i, body := range got {
		if want := 2 + i; body.TurnNo != want {
			t.Errorf("got[%d].TurnNo = %d, want %d", i, body.TurnNo, want)
		}
	}

	// 删除 turn 3 后，区间 (1,5) 应跳过缺失轮次返回 4 条
	missing := s.buildPath("tenant-a", "sess-range", 3)
	if err := os.Remove(missing); err != nil {
		t.Fatalf("os.Remove(%s) error = %v", missing, err)
	}
	got, err = s.ReadRange(ctx, "tenant-a", "sess-range", 1, 5)
	if err != nil {
		t.Fatalf("ReadRange(1,5) with missing turn error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ReadRange(1,5) 返回 %d 条, want 4（跳过缺失的 turn 3）", len(got))
	}

	// startTurn > endTurn 返回空切片
	got, err = s.ReadRange(ctx, "tenant-a", "sess-range", 5, 1)
	if err != nil {
		t.Fatalf("ReadRange(5,1) error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("ReadRange(5,1) = %v (len %d), want 空非 nil 切片", got, len(got))
	}
}

// TestDelete 验证删除后读取返回 ErrNotFound，且重复删除幂等。
func TestDelete(t *testing.T) {
	s := newTestStore(t, 2)
	ctx := context.Background()

	for turn := 1; turn <= 3; turn++ {
		if err := s.Write(ctx, makeBody("tenant-a", "sess-del", turn)); err != nil {
			t.Fatalf("Write(turn %d) error = %v", turn, err)
		}
	}

	if err := s.Delete(ctx, "tenant-a", "sess-del"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := s.Read(ctx, "tenant-a", "sess-del", 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Read(after delete) error = %v, want storage.ErrNotFound", err)
	}

	// 重复删除：目录已不存在也应返回 nil（幂等）
	if err := s.Delete(ctx, "tenant-a", "sess-del"); err != nil {
		t.Errorf("Delete() again error = %v, want nil", err)
	}
}

// TestConcurrentWriteRead 验证 100 个协程并发写不同轮次并同时并发读取，
// 在 -race 下全部成功：写入互不覆盖，读回内容与写入一致。
func TestConcurrentWriteRead(t *testing.T) {
	s := newTestStore(t, 8)
	ctx := context.Background()

	const concurrency = 100
	var wg sync.WaitGroup
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			turn := i + 1
			body := makeBody("tenant-conc", "sess-conc", turn)
			if err := s.Write(ctx, body); err != nil {
				errs[i] = fmt.Errorf("Write(turn %d): %w", turn, err)
				return
			}
			// Write 阻塞至落盘完成，此刻读取自身轮次必然成功且内容一致
			got, err := s.Read(ctx, "tenant-conc", "sess-conc", turn)
			if err != nil {
				errs[i] = fmt.Errorf("Read(turn %d): %w", turn, err)
				return
			}
			assertBodyEqual(t, body, got)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// 全量区间读取应恰好拿到 100 条连续轮次
	all, err := s.ReadRange(ctx, "tenant-conc", "sess-conc", 1, concurrency)
	if err != nil {
		t.Fatalf("ReadRange(1,%d) error = %v", concurrency, err)
	}
	if len(all) != concurrency {
		t.Fatalf("ReadRange 返回 %d 条, want %d", len(all), concurrency)
	}
}

// TestBodiesWriteAfterClose 验证 Close 之后 Write 立即返回错误（委托 async_writer 的关闭语义）。
func TestBodiesWriteAfterClose(t *testing.T) {
	s := newTestStore(t, 2)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	err := s.Write(context.Background(), makeBody("tenant-a", "sess-closed", 1))
	if err == nil {
		t.Fatal("Write(after Close) error = nil, want error")
	}
	if !errors.Is(err, ErrWriterClosed) {
		t.Errorf("Write(after Close) error = %v, want ErrWriterClosed", err)
	}
}

// TestReadCorruptedData 验证 gzip/JSON 损坏时返回带上下文的错误而非 panic。
func TestReadCorruptedData(t *testing.T) {
	s := newTestStore(t, 2)
	ctx := context.Background()
	path := s.buildPath("tenant-a", "sess-bad", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	// 损坏的 gzip 数据
	if err := os.WriteFile(path, []byte("这不是 gzip 数据"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if _, err := s.Read(ctx, "tenant-a", "sess-bad", 1); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Read(corrupt gzip) error = %v, want 非 ErrNotFound 的错误", err)
	}

	// 合法 gzip 但内容不是合法 JSON
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("同样不是 JSON"))
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if _, err := s.Read(ctx, "tenant-a", "sess-bad", 1); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Read(corrupt json) error = %v, want 非 ErrNotFound 的错误", err)
	}

	// ReadRange 遇到损坏轮次应中止而非跳过
	if _, err := s.ReadRange(ctx, "tenant-a", "sess-bad", 1, 3); err == nil {
		t.Error("ReadRange(corrupt turn) error = nil, want error")
	}
}

// TestPathTraversalRejected 回归：tenantID/sessionID 含 ".." 或路径分隔符时
// 必须拒绝，filepath.Join 的 Clean 否则会解析 "../" 逃逸 baseDir（审计 P1）。
func TestPathTraversalRejected(t *testing.T) {
	store := NewFileBodiesStore(t.TempDir(), 2)
	ctx := context.Background()
	body := &storage.SessionBody{
		TenantID:  "tenant",
		SessionID: "session",
		TurnNo:    1,
		Request:   json.RawMessage(`{"ok":true}`),
	}

	for _, tc := range []struct{ tenantID, sessionID string }{
		{"../../tmp/evil", "session"},
		{"tenant", "../escape"},
		{"tenant", ".."},
		{"../..", "session"},
		{`tenant\..`, `session\..`}, // Windows 分隔符同样拒绝
	} {
		b := *body
		b.TenantID, b.SessionID = tc.tenantID, tc.sessionID
		if err := store.Write(ctx, &b); err == nil {
			t.Errorf("Write(%q, %q) 应拒绝路径遍历 ID", tc.tenantID, tc.sessionID)
		}
		if _, err := store.Read(ctx, tc.tenantID, tc.sessionID, 1); err == nil {
			t.Errorf("Read(%q, %q) 应拒绝路径遍历 ID", tc.tenantID, tc.sessionID)
		}
		if err := store.Delete(ctx, tc.tenantID, tc.sessionID); err == nil {
			t.Errorf("Delete(%q, %q) 应拒绝路径遍历 ID", tc.tenantID, tc.sessionID)
		}
	}
}
