package file

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// newCodecTestStore 创建指定编码的测试存储实例，并在测试结束后优雅关闭。
func newCodecTestStore(t *testing.T, workers int, codec Codec) *FileBodiesStore {
	t.Helper()
	s := NewFileBodiesStore(t.TempDir(), workers, WithCodec(codec))
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

// TestParseCodec 配置字符串解析：zstd 命中（大小写/空白容错），其余一律
// 回落 gzip（配置错误不应悄悄改变落盘格式，显式报错由 config.Validate 负责）。
func TestParseCodec(t *testing.T) {
	cases := []struct {
		in   string
		want Codec
	}{
		{"", CodecGzip},
		{"gzip", CodecGzip},
		{"GZIP", CodecGzip},
		{"zstd", CodecZstd},
		{" ZSTD ", CodecZstd},
		{"brotli", CodecGzip}, // 未知值回落历史行为
	}
	for _, c := range cases {
		if got := ParseCodec(c.in); got != c.want {
			t.Errorf("ParseCodec(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSuffixPerCodec 后缀即编码标识，ListTurns/DeleteTurnFile 依赖它分发。
func TestSuffixPerCodec(t *testing.T) {
	if got := CodecZstd.Suffix(); got != ".json.zst" {
		t.Errorf("zstd suffix = %q", got)
	}
	if got := CodecGzip.Suffix(); got != ".json.gz" {
		t.Errorf("gzip suffix = %q", got)
	}
}

// TestZstdWriteReadRoundTrip 新写 zstd 编码的完整往返。
func TestZstdWriteReadRoundTrip(t *testing.T) {
	s := newCodecTestStore(t, 2, CodecZstd)
	ctx := context.Background()
	body := makeBody("tenant-z", "sess-zstd", 3)

	if err := s.Write(ctx, body); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// 落盘文件必须是 .json.zst 后缀且内容为 zstd 魔数（0x28 B5 2F FD）
	path := filepath.Join(s.baseDir, "tenant-z", "se", "sess-zstd", "turn_3.json.zst")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read zstd turn file: %v", err)
	}
	if len(raw) < 4 || raw[0] != 0x28 || raw[1] != 0xB5 || raw[2] != 0x2F || raw[3] != 0xFD {
		t.Fatalf("turn file is not zstd framed: first bytes = %x", raw[:min(4, len(raw))])
	}
	if _, err := os.Stat(filepath.Join(s.baseDir, "tenant-z", "se", "sess-zstd", "turn_3.json.gz")); !os.IsNotExist(err) {
		t.Fatalf("gzip twin file must not exist, stat err = %v", err)
	}

	got, err := s.Read(ctx, "tenant-z", "sess-zstd", 3)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	assertBodyEqual(t, body, got)
}

// TestZstdStoreReadsGzipLegacy 存量迁移安全的核心保证：zstd 配置的 store
// 必须能读 gzip 时代写入的历史文件（反之亦然）。
func TestZstdStoreReadsGzipLegacy(t *testing.T) {
	ctx := context.Background()
	body := makeBody("tenant-m", "sess-mixed", 7)

	legacy := newCodecTestStore(t, 2, CodecGzip)
	if err := legacy.Write(ctx, body); err != nil {
		t.Fatalf("legacy Write() error = %v", err)
	}

	// 直接复用同一 baseDir（模拟升级后同一数据目录），仅换编码
	upgraded := NewFileBodiesStore(legacy.baseDir, 2, WithCodec(CodecZstd))
	t.Cleanup(func() { _ = upgraded.Close() })

	got, err := upgraded.Read(ctx, "tenant-m", "sess-mixed", 7)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	assertBodyEqual(t, body, got)

	// 读到旧编码后新写同轮次 → 新文件用新编码（轮次重写场景）
	body.TurnNo = 8
	if err := upgraded.Write(ctx, body); err != nil {
		t.Fatalf("upgraded Write() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(upgraded.baseDir, "tenant-m", "se", "sess-mixed", "turn_8.json.zst")); err != nil {
		t.Fatalf("new write must use configured codec suffix: %v", err)
	}
}

// TestListTurnsMixedCodecs 新旧编码混存的目录必须能完整列出轮次
//（一致性对账 storage.ReconcileTurnArtifacts 依赖此清单）。
func TestListTurnsMixedCodecs(t *testing.T) {
	s := newCodecTestStore(t, 2, CodecGzip)
	ctx := context.Background()

	// turn 1/2 由 gzip 时代写入，turn 3 手工放置 zstd 文件（模拟升级混存）
	if err := s.Write(ctx, makeBody("tenant-l", "sess-list", 1)); err != nil {
		t.Fatalf("Write turn1 error = %v", err)
	}
	if err := s.Write(ctx, makeBody("tenant-l", "sess-list", 2)); err != nil {
		t.Fatalf("Write turn2 error = %v", err)
	}
	zstPath := filepath.Join(s.baseDir, "tenant-l", "se", "sess-list", "turn_3.json.zst")
	if err := os.WriteFile(zstPath, []byte("placeholder-not-a-real-frame"), 0o644); err != nil {
		t.Fatalf("seed zst file: %v", err)
	}

	turns, err := s.ListTurns(ctx, "tenant-l", "sess-list")
	if err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	if len(turns) != 3 || turns[0] != 1 || turns[1] != 2 || turns[2] != 3 {
		t.Fatalf("ListTurns() = %v, want [1 2 3]", turns)
	}
}

// TestDeleteTurnFileBothCodecs 孤儿清理必须把同一轮次的新旧编码文件都删掉。
func TestDeleteTurnFileBothCodecs(t *testing.T) {
	s := newCodecTestStore(t, 2, CodecZstd)
	ctx := context.Background()
	if err := s.Write(ctx, makeBody("tenant-d", "sess-del", 5)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	// 人为放置旧编码孪生文件
	gzPath := filepath.Join(s.baseDir, "tenant-d", "se", "sess-del", "turn_5.json.gz")
	if err := os.WriteFile(gzPath, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("seed gz file: %v", err)
	}

	if err := s.DeleteTurnFile(ctx, "tenant-d", "sess-del", 5); err != nil {
		t.Fatalf("DeleteTurnFile() error = %v", err)
	}
	if _, err := os.Stat(gzPath); !os.IsNotExist(err) {
		t.Fatalf("legacy gz file must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.baseDir, "tenant-d", "se", "sess-del", "turn_5.json.zst")); !os.IsNotExist(err) {
		t.Fatalf("zst file must be removed, stat err = %v", err)
	}
}

// TestTurnFileModTimeFallsBackAcrossCodecs mtime 探测（孤儿删除宽限判定）
// 必须覆盖新旧编码后缀；完全缺失时返回 storage.ErrNotFound 哨兵错误。
func TestTurnFileModTimeFallsBackAcrossCodecs(t *testing.T) {
	s := newCodecTestStore(t, 2, CodecZstd)
	ctx := context.Background()
	if err := s.Write(ctx, makeBody("tenant-t", "sess-mtime", 2)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if _, err := s.TurnFileModTime(ctx, "tenant-t", "sess-mtime", 2); err != nil {
		t.Fatalf("TurnFileModTime() error = %v", err)
	}
	if _, err := s.TurnFileModTime(ctx, "tenant-t", "sess-mtime", 99); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing turn error = %v, want storage.ErrNotFound", err)
	}
}

// BenchmarkFileBodiesWrite / BenchmarkFileBodiesRead：gzip vs zstd 的
// 落盘/读取基准。负载模拟 LLM 会话体（重复结构 + 中文长文本），验证
// zstd-compression-analysis-2026-09-10.md 的结论在本仓真实路径成立。
// 运行：go test -run XXX -bench BenchmarkFileBodies ./storage/file/
func makeBenchmarkBody() *storage.SessionBody {
	question := "请分析以下系统日志并给出根因假设：" + strings.Repeat("2026-09-10T05:00:00Z WARN upstream timeout retry=3 ", 40)
	answer := "根据日志模式，根因大概率是上游连接池耗尽。建议：" + strings.Repeat("1) 检查 max_connections 配置；2) 增大超时预算；", 30)
	return &storage.SessionBody{
		TenantID:  "bench-tenant",
		SessionID: "bench-session",
		TurnNo:    1,
		Request:   []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"` + question + `"}],"stream":true}`),
		Response:  []byte(`{"choices":[{"message":{"role":"assistant","content":"` + answer + `"}}],"usage":{"total_tokens":2048}}`),
		Metadata:  map[string]interface{}{"user_tier": "premium", "retry_count": float64(2)},
	}
}

func BenchmarkFileBodiesWrite(b *testing.B) {
	body := makeBenchmarkBody()
	for _, codec := range []Codec{CodecGzip, CodecZstd} {
		b.Run(string(codec), func(b *testing.B) {
			s := NewFileBodiesStore(b.TempDir(), 1, WithCodec(codec))
			defer func() { _ = s.Close() }()
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				body.TurnNo = i + 1
				if err := s.Write(ctx, body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestCodecCompressedSizes 同一负载下 gzip 与 zstd 的产物大小对照
//（go test -v 可见），作为报告 §3 结论在本仓负载上的抽样验证。
func TestCodecCompressedSizes(t *testing.T) {
	raw, err := json.Marshal(makeBenchmarkBody())
	if err != nil {
		t.Fatal(err)
	}
	for _, codec := range AllCodecs() {
		compressed, err := codec.encode(raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes (ratio %.2fx, raw %d bytes)", codec, len(compressed),
			float64(len(raw))/float64(len(compressed)), len(raw))
	}
}

func BenchmarkFileBodiesRead(b *testing.B) {
	body := makeBenchmarkBody()
	for _, codec := range []Codec{CodecGzip, CodecZstd} {
		b.Run(string(codec), func(b *testing.B) {
			s := NewFileBodiesStore(b.TempDir(), 1, WithCodec(codec))
			defer func() { _ = s.Close() }()
			ctx := context.Background()
			if err := s.Write(ctx, body); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Read(ctx, body.TenantID, body.SessionID, body.TurnNo); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
