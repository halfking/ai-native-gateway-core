package file

// request_mirror_test.go — 双模式热区层方案 H3：请求侧 body 镜像器单测。
//
// 覆盖：
//   - 三方向（req / resp / out）写文件落盘；
//   - 路径布局 {hotzone}/requests/{tenant}/{date}/{id}.{dir}.json.gz；
//   - 非法 ID（空串/../）按 fail-open 跳过（仅记录监控错误）；
//   - MirrorAsync 投递后立即返回，文件最终落盘。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestRequestMirror(t *testing.T) (*RequestMirror, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewRequestMirror(dir, 2)
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

// gunzip + json.Unmarshal 验证文件可读。
func decodeMirrorJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mirror file %s: %v", path, err)
	}
	raw, err := CodecGzip.decode(data)
	if err != nil {
		t.Fatalf("gunzip mirror file %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal mirror file %s: %v", path, err)
	}
	return out
}

func TestRequestMirrorThreeDirections(t *testing.T) {
	m, dir := newTestRequestMirror(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	tenant, reqID := "tenant-r3", "req-abc"

	m.Mirror(context.Background(), tenant, reqID, DirRequest, map[string]any{"u": "hello"}, now)
	m.Mirror(context.Background(), tenant, reqID, DirResponse, map[string]any{"a": "world"}, now)
	m.Mirror(context.Background(), tenant, reqID, DirOutput, map[string]any{"final": true}, now)

	// 三件套落盘：{hotzoneDir}/requests/{tenant}/{YYYY-MM-DD}/{id}.{dir}.json.gz
	// NewRequestMirror 在传入 hotzoneDir 后会在内部补 /requests/ 子树。
	day := now.Format("2006-01-02")
	for _, direction := range []RequestDirection{DirRequest, DirResponse, DirOutput} {
		path := filepath.Join(dir, "requests", tenant, day, reqID+"."+string(direction)+CodecGzip.Suffix())
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("mirror file missing for %s: %v", direction, err)
		}
	}

	// 解析校验（取 out 的内容做最终断言）
	out := decodeMirrorJSON(t, filepath.Join(dir, "requests", tenant, day, reqID+".out"+CodecGzip.Suffix()))
	if got, _ := out["final"].(bool); !got {
		t.Errorf("decoded payload mismatch: %v", out)
	}
}

func TestRequestMirrorInvalidID(t *testing.T) {
	m, dir := newTestRequestMirror(t)
	now := time.Now()

	// 空 tenant 与 ../ 路径遍历试探都应被拒绝
	cases := []struct {
		tenant, reqID string
	}{
		{"", "req-x"},
		{"tenant-x", ""},
		{".", "req-x"},
		{"tenant-x", "."},
		{"..", "req-x"},
		{"tenant-x", ".."},
		{"tenant/x", "req-x"},
		{"tenant-x", "req/x"},
	}
	for _, c := range cases {
		m.Mirror(context.Background(), c.tenant, c.reqID, DirRequest, map[string]any{"k": "v"}, now)
		m.MirrorAsync(c.tenant, c.reqID, DirRequest, map[string]any{"k": "v"}, now)
	}

	// 显式建 requests 子树以验证「无任何路径绕过 validMirrorID」
	requestsDir := filepath.Join(dir, "requests")
	if err := os.MkdirAll(requestsDir, 0o755); err != nil {
		t.Fatalf("mkdir requests: %v", err)
	}
	entries, err := os.ReadDir(requestsDir)
	if err != nil {
		t.Fatalf("read requests dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("非法 ID 仍写入 %d 子项: %v", len(entries), entries)
	}
}

func TestRequestMirrorAsync(t *testing.T) {
	m, dir := newTestRequestMirror(t)
	now := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)

	// 异步投递后立即返回（不阻塞）
	for i := 0; i < 5; i++ {
		m.MirrorAsync("tenant-async", "req-async", DirRequest, map[string]any{"i": i}, now)
	}

	// Close 排空队列后所有文件应已落盘
	if err := m.Close(); err != nil {
		t.Fatalf("close mirror: %v", err)
	}

	day := now.Format("2006-01-02")
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "requests", "tenant-async", day, "req-async.req.json.gz")
		// 同一文件名被覆盖 5 次：最终内容是 i=4
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("async mirror file missing: %v", err)
		}
	}
}