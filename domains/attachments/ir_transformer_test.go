package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// tinyPNG 是一个最小合法 PNG（8 字节签名 + 占位数据），用于构造 base64 图片块。
var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func b64Image(t *testing.T, data []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(data)
}

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return st
}

// base64ImageRequest 构造一个含单条 base64 图片消息的 IR。
func base64ImageRequest(dataB64 string) *ir.InternalRequest {
	return &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "text", Text: "look at this"},
				{Type: "image", Image: &ir.ImageSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      dataB64,
				}},
			}},
		},
	}
}

func TestIRTransformer_StoresBase64AndReplacesWithGatewayURL(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	req := base64ImageRequest(b64Image(t, tinyPNG))
	result := tr.TransformRequest(context.Background(), "req_1", req)

	if result.Failed != 0 || len(result.Attachments) != 1 {
		t.Fatalf("expected 1 stored attachment, got %+v", result)
	}

	// IR 图片块被替换为 URL 引用
	img := req.Messages[0].Content[1].Image
	if img.Type != "url" {
		t.Errorf("image type = %q, want %q", img.Type, "url")
	}
	if img.Data != "" {
		t.Errorf("image data should be cleared, got %d chars", len(img.Data))
	}
	if !strings.HasPrefix(img.URL, "https://cdn.example.com/attachments/") {
		t.Errorf("gateway URL = %q, want prefix https://cdn.example.com/attachments/", img.URL)
	}

	// 元数据与磁盘内容对应
	meta := result.Attachments[0]
	wantHash := sha256.Sum256(tinyPNG)
	if meta.Hash != hex.EncodeToString(wantHash[:]) {
		t.Errorf("hash = %q, want %q", meta.Hash, hex.EncodeToString(wantHash[:]))
	}
	if !strings.HasSuffix(img.URL, meta.Path) {
		t.Errorf("URL %q should end with stored path %q", img.URL, meta.Path)
	}

	// 文件确实落盘且内容一致
	content, err := os.ReadFile(filepath.Join(storage.BaseDir(), filepath.FromSlash(meta.Path)))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(content) != string(tinyPNG) {
		t.Errorf("stored content mismatch: %v", content)
	}
}

func TestIRTransformer_DedupWithinRequest_ReusesManifest(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	dataB64 := b64Image(t, tinyPNG)
	req := &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/png", Data: dataB64}},
				{Type: "text", Text: "again"},
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/png", Data: dataB64}},
			}},
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/png", Data: dataB64}},
			}},
		},
	}

	result := tr.TransformRequest(context.Background(), "req_dedup", req)

	if len(result.Attachments) != 3 {
		t.Fatalf("attachments = %d, want 3 (one per block)", len(result.Attachments))
	}
	if result.Stored != 1 || result.Deduped != 2 {
		t.Errorf("stored=%d deduped=%d, want stored=1 deduped=2", result.Stored, result.Deduped)
	}
	for i, meta := range result.Attachments {
		if meta.Hash != result.Attachments[0].Hash {
			t.Errorf("attachment %d hash mismatch: %q vs %q", i, meta.Hash, result.Attachments[0].Hash)
		}
	}
	// 定位字段记录的是各自块的位置
	if result.Attachments[1].BlockIndex != 2 || result.Attachments[2].MessageIndex != 1 {
		t.Errorf("reused manifest should keep occurrence position: %+v / %+v",
			result.Attachments[1], result.Attachments[2])
	}
	// 三个块都替换为同一 URL
	u := req.Messages[0].Content[0].Image.URL
	if req.Messages[0].Content[2].Image.URL != u || req.Messages[1].Content[0].Image.URL != u {
		t.Error("all occurrences should reference the same gateway URL")
	}
}

func TestIRTransformer_DedupAcrossRequests(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "/api/attachments")

	req1 := base64ImageRequest(b64Image(t, tinyPNG))
	res1 := tr.TransformRequest(context.Background(), "req_a", req1)
	req2 := base64ImageRequest(b64Image(t, tinyPNG))
	res2 := tr.TransformRequest(context.Background(), "req_b", req2)

	if res1.Stored != 1 || res1.Deduped != 0 {
		t.Errorf("first request: stored=%d deduped=%d, want 1/0", res1.Stored, res1.Deduped)
	}
	if res2.Stored != 0 || res2.Deduped != 1 {
		t.Errorf("second request: stored=%d deduped=%d, want 0/1", res2.Stored, res2.Deduped)
	}
	if res2.Attachments[0].Hash != res1.Attachments[0].Hash {
		t.Error("cross-request manifests should share the same hash")
	}
	if req2.Messages[0].Content[1].Image.URL != req1.Messages[0].Content[1].Image.URL {
		t.Error("cross-request references should produce the same gateway URL")
	}
}

func TestIRTransformer_SaveFailureKeepsBase64BlockBestEffort(t *testing.T) {
	storage := newTestStorage(t)
	storage.MaxSize = 4 // tinyPNG 解码后 12 字节，必然超限

	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")
	req := base64ImageRequest(b64Image(t, tinyPNG))
	result := tr.TransformRequest(context.Background(), "req_too_large", req)

	if result.Failed != 1 || len(result.Failures) != 1 {
		t.Fatalf("failed=%d failures=%d, want 1/1", result.Failed, len(result.Failures))
	}
	if result.Failures[0].Status != AttachmentStatusStoreFailed || result.Failures[0].ErrorCode != "file_too_large" {
		t.Errorf("failure record = %+v", result.Failures[0])
	}
	// 按既有约定 OriginalURL 截断至 200 字符，不得携带完整 base64 payload
	if got := result.Failures[0].OriginalURL; len(got) > 200+3 {
		t.Errorf("failure OriginalURL should be truncated, len=%d", len(got))
	}
	// 失败块保持 base64 形态，供后续渲染兜底
	img := req.Messages[0].Content[1].Image
	if img.Type != "base64" || img.Data == "" {
		t.Errorf("failed block should keep base64 form, got type=%q data_len=%d", img.Type, len(img.Data))
	}
}

func TestIRTransformer_NilAndNoImageRequestsAreNoOp(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	if got := tr.TransformRequest(context.Background(), "r", nil); got == nil || got.Stored != 0 || got.Failed != 0 {
		t.Errorf("nil request should return empty result, got %+v", got)
	}

	// 非 base64（url/file_id）图片块与纯文本不受影响
	originalURL := "https://example.com/cat.png"
	req := &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "text", Text: "hi"},
				{Type: "image", Image: &ir.ImageSource{Type: "url", MediaType: "image/png", URL: originalURL}},
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/png", Data: ""}},
			}},
		},
	}
	result := tr.TransformRequest(context.Background(), "r2", req)
	if result.Stored != 0 || result.Deduped != 0 || result.Failed != 0 || len(result.Attachments) != 0 {
		t.Errorf("expected no-op, got %+v", result)
	}
	if req.Messages[0].Content[1].Image.URL != originalURL || req.Messages[0].Content[1].Image.Type != "url" {
		t.Error("pre-existing URL image block must not be modified")
	}
}

func TestIRTransformer_OnlyImageBlocksChangedEverythingElsePreserved(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	req := &ir.InternalRequest{
		Model: "gpt-4o",
		Messages: []ir.Message{
			{Role: "system", Content: []ir.ContentBlock{{Type: "text", Text: "be brief"}}},
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "text", Text: "unchanged"},
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: b64Image(t, tinyPNG), Detail: "high"}},
				{Type: "tool_use", ToolUse: &ir.ToolUse{ID: "t1", Name: "f"}},
			}},
		},
	}
	tr.TransformRequest(context.Background(), "req_keep", req)

	if req.Model != "gpt-4o" {
		t.Errorf("model changed: %q", req.Model)
	}
	if req.Messages[0].Content[0].Text != "be brief" {
		t.Error("system message changed")
	}
	if req.Messages[1].Content[0].Text != "unchanged" {
		t.Error("sibling text block changed")
	}
	img := req.Messages[1].Content[1].Image
	if img.MediaType != "image/jpeg" || img.Detail != "high" {
		t.Errorf("image auxiliary fields should be preserved: %+v", img)
	}
	if req.Messages[1].Content[2].ToolUse == nil || req.Messages[1].Content[2].ToolUse.ID != "t1" {
		t.Error("tool_use block changed")
	}
}
