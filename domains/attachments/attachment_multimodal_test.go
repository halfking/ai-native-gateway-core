package attachments

// attachment_multimodal_test.go —— 多模态附件的落盘与转发钉测（2026-11-11 附件域审计轮）
//
// ★ 分层：★ 这里钉的是**单元层**（附件域内部的行为）；
//   端到端层（客户端请求 → 网关转换 → 出站 body 断言）在
//   domains/streaming/executors/e2e_multimodal_outbound_test.go ★。
//
// ★ 本轮修掉的四个缺陷（每个都有钉测）：
//   ① IRTransformer 只处理 image ⇒ 文档/视频/音频**不落盘**（载荷每次全量重传）
//   ② URLFetchFallback 只内联 image_url ⇒ 文档/视频的网关 URL **原样发给**
//      不支持 URL 的上游 ⇒ 附件在链路上**丢失**
//   ③ URLFetchFallback 用**全 body 字节级替换** ⇒ 文本消息里的同名 URL
//      也被替换成 base64 载荷 ⇒ **内容污染 + 载荷外泄**
//   ④ OutboundURLRewriter 只改写 image_url（且 Anthropic 侧只处理 image）
//      ⇒ 文档/视频**每次请求全量重传**

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ── 钉测 ①：IRTransformer 覆盖 document / video / audio ─────────────────

// TestIRTransformer_DocumentVideoAudio_AreStoredAndReplaced 钉住
// 「IRTransformer 只处理 image ⇒ 文档/视频/音频的 base64 载荷**完全没被落盘**」。
func TestIRTransformer_DocumentVideoAudio_AreStoredAndReplaced(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	docB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 test"))
	vidB64 := base64.StdEncoding.EncodeToString([]byte("fake-mp4"))
	audB64 := base64.StdEncoding.EncodeToString([]byte("fake-wav"))

	req := &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "document", Document: &ir.DocumentBlock{
					Kind:     "pdf",
					MIMEType: "application/pdf",
					Source:   &ir.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: docB64},
				}},
				{Type: "video", Video: &ir.MediaSource{
					Kind: "video", Type: "base64", MediaType: "video/mp4", Data: vidB64,
				}},
				{Type: "audio", Audio: &ir.MediaSource{
					Kind: "audio", Type: "base64", MediaType: "audio/wav", Data: audB64,
				}},
			}},
		},
	}

	result := tr.TransformRequest(context.Background(), "req_multi", req)
	if result.Failed != 0 {
		t.Fatalf("落盘失败：%+v", result.Failures)
	}
	if len(result.Attachments) != 3 {
		t.Fatalf("落盘记录数 = %d，期望 3（文档/视频/音频各一）", len(result.Attachments))
	}
	if result.Stored != 3 {
		t.Errorf("新写入文件数 = %d，期望 3", result.Stored)
	}

	doc := req.Messages[0].Content[0].Document
	if doc.Source.Type != "url" {
		t.Errorf("文档 source.type = %q，期望 url", doc.Source.Type)
	}
	if !strings.HasPrefix(doc.Source.URL, "https://cdn.example.com/attachments/") {
		t.Errorf("文档 source.url = %q，期望网关 URL", doc.Source.URL)
	}
	if doc.Source.Data != "" {
		t.Errorf("文档 source.data 未清空：%q", doc.Source.Data)
	}

	vid := req.Messages[0].Content[1].Video
	if vid.Type != "url" || !strings.HasPrefix(vid.URL, "https://cdn.example.com/attachments/") {
		t.Errorf("视频块未被换成网关 URL：type=%q url=%q", vid.Type, vid.URL)
	}
	if vid.Data != "" {
		t.Errorf("视频 data 未清空：%q", vid.Data)
	}

	aud := req.Messages[0].Content[2].Audio
	if aud.Type != "url" || !strings.HasPrefix(aud.URL, "https://cdn.example.com/attachments/") {
		t.Errorf("音频块未被换成网关 URL：type=%q url=%q", aud.Type, aud.URL)
	}
	if aud.Data != "" {
		t.Errorf("音频 data 未清空：%q", aud.Data)
	}

	// ★ 落盘记录的**类型**必须与 Extractor 同口径（image/file/video/audio）
	//   ⇒ 否则改写器的类型闸会把它们判成「类型不符」而跳过。
	wantTypes := []string{"file", "video", "audio"}
	for i, att := range result.Attachments {
		if att.Type != wantTypes[i] {
			t.Errorf("第 %d 条落盘记录 Type = %q，期望 %q", i, att.Type, wantTypes[i])
		}
	}
}

// TestIRTransformer_Multimodal_DedupWithinRequest 钉住「请求内同载荷只落盘一次」。
func TestIRTransformer_Multimodal_DedupWithinRequest(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	docB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 same"))
	req := &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "document", Document: &ir.DocumentBlock{
					Source: &ir.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: docB64},
				}},
			}},
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "document", Document: &ir.DocumentBlock{
					Source: &ir.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: docB64},
				}},
			}},
		},
	}

	result := tr.TransformRequest(context.Background(), "req_dedup", req)
	if result.Stored != 1 {
		t.Errorf("新写入文件数 = %d，期望 1（同载荷应去重）", result.Stored)
	}
	if result.Deduped != 1 {
		t.Errorf("去重计数 = %d，期望 1", result.Deduped)
	}
	first := req.Messages[0].Content[0].Document.Source.URL
	second := req.Messages[1].Content[0].Document.Source.URL
	if first == "" || first != second {
		t.Errorf("同载荷的两个块没指向同一份落盘文件：%q vs %q", first, second)
	}
}

// TestIRTransformer_Multimodal_DedupKeyIncludesKind 钉住「请求内去重的键
// 必须**含附件种类**」。
//
// ★ 为什么：★ 去重键若只是载荷本身 ⇒ ★ 同载荷的 image 与 document 会**共用**
//   同一个 manifest ⇒ ⇒ document 的落盘记录被标成 image ⇒ ⇒ 改写器的类型闸
//   会把它判成「类型不符」而跳过 ⇒ ⇒ 文档**永远**不会被换成 URL ⇒ ⇒
//   正好退回本轮修掉的那个缺陷。
func TestIRTransformer_Multimodal_DedupKeyIncludesKind(t *testing.T) {
	storage := newTestStorage(t)
	tr := NewIRTransformer(storage, "https://cdn.example.com/attachments")

	same := base64.StdEncoding.EncodeToString([]byte("identical-payload"))
	req := &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "image", Image: &ir.ImageSource{Type: "base64", MediaType: "image/png", Data: same}},
			}},
			{Role: "user", Content: []ir.ContentBlock{
				{Type: "document", Document: &ir.DocumentBlock{
					Source: &ir.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: same},
				}},
			}},
		},
	}

	result := tr.TransformRequest(context.Background(), "req_kind", req)
	if len(result.Attachments) != 2 {
		t.Fatalf("落盘记录数 = %d，期望 2（同载荷但不同种类各一条记录）", len(result.Attachments))
	}
	wantTypes := []string{"image", "file"}
	for i, att := range result.Attachments {
		if att.Type != wantTypes[i] {
			t.Errorf("第 %d 条落盘记录 Type = %q，期望 %q（去重键必须含附件种类）", i, att.Type, wantTypes[i])
		}
	}
}

// ── 钉测 ②③：URLFetchFallback 的字段级内联 ───────────────────────────

// mmFallbackFetcher 是一份最小附件存储（按相对路径返回固定内容）。
type mmFallbackFetcher struct {
	files map[string][]byte
	mime  map[string]string
}

func (f *mmFallbackFetcher) LoadAttachment(relPath string) ([]byte, string, error) {
	content, ok := f.files[relPath]
	if !ok {
		return nil, "", errNotFound
	}
	mime := f.mime[relPath]
	if mime == "" {
		mime = "application/octet-stream"
	}
	return content, mime, nil
}

var errNotFound = &mmError{"attachment not found"}

type mmError struct{ msg string }

func (e *mmError) Error() string { return e.msg }

// TestURLFetchFallback_InlinesImageFileAndVideo 钉住缺陷②：
// 「只内联 image_url ⇒ 文档/视频的网关 URL 原样发给不支持 URL 的上游」。
func TestURLFetchFallback_InlinesImageFileAndVideo(t *testing.T) {
	fetcher := &mmFallbackFetcher{
		files: map[string][]byte{
			"a/img.png":  []byte("PNGBYTES"),
			"a/doc.pdf":  []byte("PDFBYTES"),
			"a/vid.mp4":  []byte("MP4BYTES"),
		},
		mime: map[string]string{
			"a/img.png": "image/png",
			"a/doc.pdf": "application/pdf",
			"a/vid.mp4": "video/mp4",
		},
	}
	f := NewURLFetchFallback("https://gw/att", fetcher)

	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://gw/att/a/img.png"}},
		{"type":"file","file":{"filename":"d.pdf","file_data":"https://gw/att/a/doc.pdf"}},
		{"type":"video_url","video_url":{"url":"https://gw/att/a/vid.mp4"}}
	]}]}`)

	out, n := f.InlineOpenAIBody(body, "deepseek")
	if n != 3 {
		t.Fatalf("内联处数 = %d，期望 3（图片+文档+视频）", n)
	}
	s := string(out)
	if strings.Contains(s, "https://gw/att/") {
		t.Errorf("出站 body 里仍留有网关 URL ⇒ 不支持 URL 的上游取不到 ⇒ 附件丢失：%s", s)
	}
	for _, want := range []string{
		"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGBYTES")),
		"data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("PDFBYTES")),
		"data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("MP4BYTES")),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("出站 body 里缺少内联载荷 %q：%s", want, s)
		}
	}
}

// TestURLFetchFallback_DoesNotTouchTextMessages 钉住缺陷③：
// 「全 body 字节级替换 ⇒ 文本消息里的同名 URL 也被替换成 base64 载荷」。
func TestURLFetchFallback_DoesNotTouchTextMessages(t *testing.T) {
	fetcher := &mmFallbackFetcher{
		files: map[string][]byte{"a/img.png": []byte("PNGBYTES")},
		mime:  map[string]string{"a/img.png": "image/png"},
	}
	f := NewURLFetchFallback("https://gw/att", fetcher)

	const url = "https://gw/att/a/img.png"
	// ★★ 两种文本形态都要覆盖 ★★：
	//   ① content 是**字符串**（整条消息就是一个文本）
	//   ② content 是**块数组**，其中有一个 text 块
	// ⇒ ★ 缺任何一种 ⇒ ★★ 遍历器一旦把 text 块也纳入字段表 ⇒ 现有断言抓不到 ★★
	// ⇒ ★★（★ 本轮补这条覆盖，正是为了给变异臂 AT05 一个「必红」的落点 ★★）。
	body := []byte(`{"messages":[
		{"role":"user","content":"上一轮的图在这里：` + url + ` 看看"},
		{"role":"user","content":[{"type":"text","text":"这张呢 ` + url + `"},{"type":"image_url","image_url":{"url":"` + url + `"}}]}
	]}`)

	out, n := f.InlineOpenAIBody(body, "deepseek")
	s := string(out)
	// ★★ 断言顺序：★ 先断「文本没被污染」（★ 语义要点 ★），再断「替换处数」★★
	// ★ 顺序很重要：★ 处数断言是 t.Fatalf ⇒ ★ 一旦它先触发，后面的语义断言
	// ★ 就永远跑不到 ⇒ ★★ 组合变异臂（★ 遍历范围 + 匹配放宽 ★）就会被
	// ★★ 「判成红了但定位不准」★★ ⇒ ★ 这不是洁癖：★ 是「红了要能指到病因」★。
	if !strings.Contains(s, "上一轮的图在这里："+url+" 看看") {
		t.Errorf("文本消息被改写了 ⇒ 内容污染 + 载荷外泄：%s", s)
	}
	if !strings.Contains(s, "这张呢 "+url) {
		t.Errorf("文本块被改写了 ⇒ 内容污染 + 载荷外泄：%s", s)
	}
	if strings.Count(s, "base64,") != 1 {
		t.Errorf("出站 body 里 base64 载荷出现了 %d 次，期望 1 次（文本里不应有）", strings.Count(s, "base64,"))
	}
	if n != 1 {
		t.Fatalf("内联处数 = %d，期望 1（只有附件字段该被替换）", n)
	}
}

// ── 钉测 ④：OutboundURLRewriter 覆盖 file / video / Anthropic document ──

// TestOutboundURLRewriter_RewritesFileAndVideoBlocks 钉住缺陷④的 OpenAI 侧：
// 「只改写 image_url ⇒ 文档/视频每次请求全量重传」。
func TestOutboundURLRewriter_RewritesFileAndVideoBlocks(t *testing.T) {
	r := NewOutboundURLRewriter("https://gw/att")
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"看这三样"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},
		{"type":"file","file":{"filename":"d.pdf","file_data":"data:application/pdf;base64,REVG"}},
		{"type":"video_url","video_url":{"url":"data:video/mp4;base64,R0hJ"}}
	]}]}`)
	atts := []AttachmentMetadata{
		{Type: "image", MessageIndex: 0, BlockIndex: 1, Path: "p/i.png"},
		{Type: "file", MessageIndex: 0, BlockIndex: 2, Path: "p/d.pdf"},
		{Type: "video", MessageIndex: 0, BlockIndex: 3, Path: "p/v.mp4"},
	}

	out, n := r.RewriteOpenAIBody(body, atts, "openai")
	if n != 3 {
		t.Fatalf("改写块数 = %d，期望 3（图片+文档+视频）", n)
	}
	s := string(out)
	if strings.Contains(s, "base64,") {
		t.Errorf("出站 body 里仍有 base64 载荷 ⇒ 文档/视频每次请求全量重传：%s", s)
	}
	for _, want := range []string{"https://gw/att/p/i.png", "https://gw/att/p/d.pdf", "https://gw/att/p/v.mp4"} {
		if !strings.Contains(s, want) {
			t.Errorf("出站 body 里缺少 %q：%s", want, s)
		}
	}
}

// TestOutboundURLRewriter_RewritesAnthropicDocument 钉住缺陷④的 Anthropic 侧：
// 「Anthropic 客户端的 document 块永远不被换成 URL」。
func TestOutboundURLRewriter_RewritesAnthropicDocument(t *testing.T) {
	r := NewOutboundURLRewriter("https://gw/att")
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"看这两样"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"REVG"}}
	]}]}`)
	atts := []AttachmentMetadata{
		{Type: "image", MessageIndex: 0, BlockIndex: 1, Path: "p/i.png"},
		{Type: "file", MessageIndex: 0, BlockIndex: 2, Path: "p/d.pdf"},
	}

	out, n := r.RewriteAnthropicBody(body, atts, "openai")
	if n != 2 {
		t.Fatalf("改写块数 = %d，期望 2（图片+文档）", n)
	}
	s := string(out)
	if strings.Contains(s, "base64") {
		t.Errorf("出站 body 里仍有 base64 载荷 ⇒ 文档每次请求全量重传：%s", s)
	}
	if !strings.Contains(s, "https://gw/att/p/d.pdf") {
		t.Errorf("Anthropic 文档块没被换成网关 URL：%s", s)
	}
}
