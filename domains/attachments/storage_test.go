package attachments

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 构造一个 1x1 红色 PNG 的 base64 data URI（真实可解码内容）。
func testPNGDataURI() string {
	// 1x1 红色 PNG（67字节）
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x5B, 0x9D, 0x21,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}
	b64 := base64.StdEncoding.EncodeToString(png)
	return "data:image/png;base64," + b64
}

func TestSaveBase64Image_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	uri := testPNGDataURI()
	res, err := s.SaveBase64Image("req-abc-123", uri, 0, 1)
	if err != nil {
		t.Fatalf("SaveBase64Image: %v", err)
	}

	// 验证元数据
	if res.Metadata.Type != "image" {
		t.Errorf("Type = %q, want image", res.Metadata.Type)
	}
	if res.Metadata.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", res.Metadata.ContentType)
	}
	if res.Metadata.Size != 68 {
		t.Errorf("Size = %d, want 68", res.Metadata.Size)
	}
	if res.Metadata.MessageIndex != 0 || res.Metadata.BlockIndex != 1 {
		t.Errorf("index mismatch: %+v", res.Metadata)
	}
	// 验证 hash 正确
	pngBytes, _ := base64.StdEncoding.DecodeString(strings.Split(uri, ",")[1])
	wantHash := sha256.Sum256(pngBytes)
	if res.Metadata.Hash != hex.EncodeToString(wantHash[:]) {
		t.Errorf("Hash mismatch")
	}

	// 验证文件实际存在
	full := filepath.Join(s.BaseDir, res.Path)
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("file not saved: %v", err)
	}
	if info.Size() != 68 {
		t.Errorf("file size = %d, want 68", info.Size())
	}

	// 验证可读回
	data, ct, err := s.LoadAttachment(res.Path)
	if err != nil {
		t.Fatalf("LoadAttachment: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("LoadAttachment contentType = %q", ct)
	}
	if len(data) != 68 {
		t.Errorf("LoadAttachment data len = %d, want 68", len(data))
	}
}

func TestSaveBase64Image_Dedup(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	uri := testPNGDataURI()

	r1, err := s.SaveBase64Image("req-1", uri, 0, 0)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	// 相同内容，不同 request_id —— 因为路径含 request_id 所以文件不重叠
	r2, err := s.SaveBase64Image("req-1", uri, 0, 0)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !r2.Deduped {
		t.Error("second save should be deduped")
	}
	if r1.Path != r2.Path {
		t.Errorf("deduped path should match: %s vs %s", r1.Path, r2.Path)
	}
}

func TestSaveBase64Image_InvalidDataURI(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)

	cases := []string{
		"not-a-data-uri",
		"data:image/png;base64,",      // 空 payload
		"http://example.com/x.png",    // 非 data URI
		"data:image/png,iVBOR",        // 非 base64（无 ;base64 标记）
	}
	for _, c := range cases {
		_, err := s.SaveBase64Image("req", c, 0, 0)
		if err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestSaveBase64Image_MaxSize(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	s.MaxSize = 10 // 极小限制

	// 构造一个 100 字节的 base64 数据
	big := base64.StdEncoding.EncodeToString(make([]byte, 100))
	uri := "data:image/png;base64," + big

	_, err := s.SaveBase64Image("req", uri, 0, 0)
	if err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestSafeJoin_DirectoryTraversal(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)

	cases := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32",
		"/absolute/path",
	}
	for _, c := range cases {
		_, err := s.safeJoin(c)
		// 要么返回 base 目录内的路径（安全归一化），要么报错
		// 关键：不能逃逸出 BaseDir 指向系统文件
		if err == nil {
			full, _ := s.safeJoin(c)
			absBase, _ := filepath.Abs(dir)
			if !strings.HasPrefix(filepath.Clean(full)+string(filepath.Separator),
				absBase+string(filepath.Separator)) && filepath.Clean(full) != absBase {
				t.Errorf("path escaped base dir: %q -> %q (base %q)", c, full, absBase)
			}
		}
	}
	// 正常相对路径应成功
	good, err := s.safeJoin("2026/07/req_x/abc.png")
	if err != nil {
		t.Errorf("valid path rejected: %v", err)
	}
	if !strings.HasSuffix(good, "2026/07/req_x/abc.png") {
		t.Errorf("unexpected path: %q", good)
	}
}

func TestSanitizeRequestID(t *testing.T) {
	cases := map[string]string{
		"abc-123_DEF": "abc-123_DEF",
		"../etc/passwd":  "etcpasswd",
		"":               "unknown",
		"a/b\\c;d":       "abcd",
	}
	for in, want := range cases {
		if got := sanitizeRequestID(in); got != want {
			t.Errorf("sanitizeRequestID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDataURI(t *testing.T) {
	cases := []struct {
		uri    string
		wantCT string
		wantOK bool
	}{
		{"data:image/png;base64,abc", "image/png", true},
		{"data:image/jpeg;base64,xyz", "image/jpeg", true},
		{"data:application/pdf;base64,zzz", "application/pdf", true},
		{"data:;base64,foo", "application/octet-stream", true}, // 无 media type
		{"https://example.com/x.png", "", false},
		{"data:image/png,abc", "", false}, // 非 base64
		{"data:image/png;base64,", "", false}, // 空 payload
	}
	for _, c := range cases {
		ct, data, err := parseDataURI(c.uri)
		ok := err == nil
		if ok != c.wantOK {
			t.Errorf("parseDataURI(%q) ok = %v, want %v (err=%v)", c.uri, ok, c.wantOK, err)
			continue
		}
		if ok && ct != c.wantCT {
			t.Errorf("parseDataURI(%q) ct = %q, want %q", c.uri, ct, c.wantCT)
		}
		if ok && data == "" {
			t.Errorf("parseDataURI(%q) empty data", c.uri)
		}
	}
}
