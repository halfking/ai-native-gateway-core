package attachments

// mime_types_test.go —— MIME ↔ 扩展名唯一事实源的钉测（2026-11-11 附件域审计轮）
//
// ★ 本轮修掉的缺陷（每个都有钉测 + 变异臂）：
//   ① MIME ↔ 扩展名散在**三处**（mimeTypeToExt / extToMimeType / detectContentType）
//      ⇒ ⇒ 三处**已经互相漂移**（.zip/.mp4/.mp3 只在 S3 那张；.html 只在另两张）
//      ⇒ ⇒ 同一个 MIME 在不同出口得到**不同**的扩展名
//   ② 映射表**太窄** ⇒ 未登记的 MIME（video/mp4、audio/wav、text/csv…）落成 .bin
//      ⇒ ⇒ 类型信息在落盘文件名里**丢失**
//      ⇒ ⇒ 内联回退时 MIME 反推成 application/octet-stream ⇒ ⇒ 上游按错类型解析
//   ③ 内联回退的 MIME 取自**文件名反推**而不是**落盘记录** ⇒ ⇒ 出站 MIME 错
//      （★ 命中窗口见下方用例注释：★ 记录**可得**时才命中 ★）

import (
	"strings"
	"testing"
)

// TestMimeTypeToExt_CoversMultimediaKinds 钉住缺陷②：多模态附件的 MIME 必须
// 有登记 ⇒ ⇒ 落盘文件名带正确扩展名 ⇒ ⇒ 类型信息不丢。
func TestMimeTypeToExt_CoversMultimediaKinds(t *testing.T) {
	cases := []struct {
		mime string
		ext  string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"text/csv", ".csv"},
		{"application/json", ".json"},
		{"application/zip", ".zip"},
		// ★ 此前全部落成 .bin ⇒ 类型信息丢失 ★
		{"video/mp4", ".mp4"},
		{"video/quicktime", ".mov"},
		{"video/webm", ".webm"},
		{"audio/mpeg", ".mp3"},
		{"audio/wav", ".wav"},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	}
	for _, tc := range cases {
		if got := mimeTypeToExt(tc.mime); got != tc.ext {
			t.Errorf("mimeTypeToExt(%q) = %q，期望 %q", tc.mime, got, tc.ext)
		}
	}
}

// TestMimeTypeToExt_UnknownFallsBackToBin 钉住「未登记 ⇒ 显式兜底 .bin」。
//
// ★ 同时钉住兜底的**对称性**：.bin 的规范 MIME 就是 application/octet-stream
//
//	⇒ ⇒ 落盘与反推不会各说各话。
func TestMimeTypeToExt_UnknownFallsBackToBin(t *testing.T) {
	if got := mimeTypeToExt("application/x-something-unheard-of"); got != ".bin" {
		t.Errorf("未登记的 MIME 应兜底成 .bin，实际 = %q", got)
	}
	if got := extToMimeType(".bin"); got != "application/octet-stream" {
		t.Errorf("extToMimeType(\".bin\") = %q，期望 application/octet-stream", got)
	}
	if got := extToMimeType(".not-registered"); got != "application/octet-stream" {
		t.Errorf("未登记的扩展名应兜底成 application/octet-stream，实际 = %q", got)
	}
}

// TestMimeRoundTrip_IsConsistent 钉住「同一张表 ⇒ MIME→ext→MIME 必然回到原值」。
//
// ★ 这条性质**只**在「两张表由同一份数据派生」时成立 ⇒ ⇒ 它就是
//
//	「三张表必须同源」的**可执行**判据。
//
// ★★ 另钉住两条更强的性质：★★
//
//	★★ ① 规范表里**一个 MIME 只有一条** ⇒ ⇒ mimeTypeToExt(MIME) 必然等于
//	★★   规范扩展名 ⇒ ⇒ **落盘文件名与表里的顺序无关** ★★
//	★★   （★ 本轮第一版把 .jpg/.jpeg、.mp4/.m4v 挂成同一 MIME ⇒ ⇒ 索引
//	★★   「后写覆盖」⇒ ⇒ 规范扩展名取决于表顺序 ⇒ ⇒ 由本用例当场抓出 ★★）
//	★★ ② 等价扩展名（.jpeg/.tiff/.htm/.m4v/.mpg）**只进反向索引** ⇒ ⇒
//	★★   它们能被正确反查，但**不会**成为落盘文件名 ★★
func TestMimeRoundTrip_IsConsistent(t *testing.T) {
	seen := make(map[string]bool, len(mimeExtTable))
	for _, e := range mimeExtTable {
		if seen[e.MIME] {
			t.Errorf("规范表里 MIME %q 出现了多次 ⇒ ⇒ 落盘文件名会取决于表顺序", e.MIME)
		}
		seen[e.MIME] = true

		if ext := mimeTypeToExt(e.MIME); ext != e.Ext {
			t.Errorf("规范 MIME %q 反查扩展名 = %q，期望 %q", e.MIME, ext, e.Ext)
			continue
		}
		if back := extToMimeType(e.Ext); back != e.MIME {
			t.Errorf("扩展名 %q 反查 MIME = %q，期望 %q（往返不一致 ⇒ 两张表不是同源）",
				e.Ext, back, e.MIME)
		}
	}
	for _, e := range mimeExtEquivalents {
		if back := extToMimeType(e.Ext); back != e.MIME {
			t.Errorf("等价扩展名 %q 反查 MIME = %q，期望 %q", e.Ext, back, e.MIME)
		}
	}
}

// TestMimeConsumersShareOneSource 钉住缺陷①：三个消费者对同一个文件必须
// 给出**同一个**类型。
//
// ★ 此前：★ detectContentType（S3 PutObject）与 extToMimeType（取回 / 对外
//
//	服务）是**两张独立的表** ⇒ ⇒ `.mp4` 只在 S3 那张 ⇒ ⇒ 落盘命名（第三张）
//	给出 `.bin` ⇒ ⇒ 三个出口三个答案。
func TestMimeConsumersShareOneSource(t *testing.T) {
	for _, e := range mimeExtTable {
		viaPath := detectContentType("2026/11/ab/cd/hash" + e.Ext)
		if viaPath != e.MIME {
			t.Errorf("detectContentType(%q) = %q，期望 %q（★ 三个消费者必须同源 ★）",
				e.Ext, viaPath, e.MIME)
		}
		if viaExt := extToMimeType(e.Ext); viaExt != e.MIME {
			t.Errorf("extToMimeType(%q) = %q，期望 %q", e.Ext, viaExt, e.MIME)
		}
	}
}

// TestMimeTypeToExt_AliasesResolveToSameExt 钉住别名归一：
// 「方言写法」与「规范写法」必须得到**同一个**扩展名。
func TestMimeTypeToExt_AliasesResolveToSameExt(t *testing.T) {
	cases := []struct {
		alias string
		canon string
	}{
		{"image/jpg", "image/jpeg"},
		{"image/pjpeg", "image/jpeg"},
		{"image/x-png", "image/png"},
		{"text/xml", "application/xml"},
		{"application/x-zip-compressed", "application/zip"},
		{"audio/x-wav", "audio/wav"},
		{"video/x-m4v", "video/mp4"},
	}
	for _, tc := range cases {
		gotAlias := mimeTypeToExt(tc.alias)
		gotCanon := mimeTypeToExt(tc.canon)
		if gotAlias != gotCanon {
			t.Errorf("别名 %q ⇒ %q，规范 %q ⇒ %q ⇒ ★ 两者必须相同 ★",
				tc.alias, gotAlias, tc.canon, gotCanon)
		}
	}
	// ★ 参数部分（charset 等）必须被剥掉再查表。
	if got := mimeTypeToExt("text/csv; charset=utf-8"); got != ".csv" {
		t.Errorf("带参数的 MIME 应剥掉参数再查表，实际 = %q", got)
	}
}

// TestURLFetchFallback_UsesRecordedContentType 钉住缺陷③：
// 内联回退的 MIME 必须取**落盘记录里的原始 ContentType**（真源），
// 而不是从文件名反推。
//
// ★ 本用例模拟「文件名是 .bin（反推 ⇒ octet-stream），但**落盘记录**里存着
// ★ 原始 MIME（video/mp4）」的情形 ⇒ ⇒ 出站必须是 video/mp4 ★。
//
// ★ ★ 注意这个前提：★ 记录**可得** ⇒ ★ 真实生产里，★ 内联的对象（URL 引用的
// ★ ★ 文件）与记录（本次请求 base64 提取的文件）**只在同一请求里内容重合**
// ★ ★ 时才对得上 ⇒ ★ ★ 查不到记录时回退到扩展名反推 ⇒ ★ ★ 边界如实登记在
// ★ ★ outbound_fetch_fallback.go 的函数注释与 docs/audit 文档里 ★ ★。
func TestURLFetchFallback_UsesRecordedContentType(t *testing.T) {
	fetcher := &mmFallbackFetcher{
		files: map[string][]byte{"a/legacy.bin": []byte("MP4BYTES")},
		// ★ 模拟「按扩展名反推」的结果：老文件只会得到 octet-stream。
		mime: map[string]string{"a/legacy.bin": "application/octet-stream"},
	}
	f := NewURLFetchFallback("https://gw/att", fetcher)

	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"video_url","video_url":{"url":"https://gw/att/a/legacy.bin"}}
	]}]}`)

	// ★ 落盘记录里存着**原始** MIME ⇒ 它才是真源。
	atts := []AttachmentMetadata{
		{Type: "video", MessageIndex: 0, BlockIndex: 0, Path: "a/legacy.bin", ContentType: "video/mp4"},
	}

	out, n := f.InlineOpenAIBody(body, atts, "deepseek")
	if n != 1 {
		t.Fatalf("内联处数 = %d，期望 1", n)
	}
	s := string(out)
	if !strings.Contains(s, "data:video/mp4;base64,") {
		t.Errorf("内联后的 MIME 不是落盘记录里的原始类型：%s\n★ 期望 data:video/mp4;base64,\n"+
			"★ ⇒ 上游会按 application/octet-stream 解析视频 ⇒ 附件坏掉", s)
	}
	if strings.Contains(s, "application/octet-stream") {
		t.Errorf("出站 body 里出现了 octet-stream：%s ⇒ MIME 仍取自文件名反推", s)
	}
}

// TestURLFetchFallback_FallsBackToFetcherMIME 钉住「没有落盘记录时」
// 仍按 fetcher 给的 MIME 内联（★ 不能因为改了真源就把老路径弄坏 ★）。
func TestURLFetchFallback_FallsBackToFetcherMIME(t *testing.T) {
	fetcher := &mmFallbackFetcher{
		files: map[string][]byte{"a/img.png": []byte("PNGBYTES")},
		mime:  map[string]string{"a/img.png": "image/png"},
	}
	f := NewURLFetchFallback("https://gw/att", fetcher)

	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://gw/att/a/img.png"}}
	]}]}`)

	out, n := f.InlineOpenAIBody(body, nil, "deepseek")
	if n != 1 {
		t.Fatalf("内联处数 = %d，期望 1", n)
	}
	if !strings.Contains(string(out), "data:image/png;base64,") {
		t.Errorf("无落盘记录时应回退到 fetcher 的 MIME：%s", out)
	}
}
