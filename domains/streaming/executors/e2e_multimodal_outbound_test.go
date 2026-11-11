package executors

// e2e_multimodal_outbound_test.go —— ★ 端到端：客户端请求 → 网关转换 → 出站 body 断言（含多模态附件）
//
// ── 这道门覆盖的是哪一段链路 ────────────────────────────────────────────
// ★ 断言全部打在 **wire 层**（★ 网关真正发给上游的字节 ★），不看中间态。
//
// ★ 走的是**生产函数**，★ 顺序与 executor_dispatch.go 的 MM-1/MM-2 段一致：
//
//	客户端 body（OpenAI / Anthropic 协议）
//	  ① domains/attachments.Extractor.ExtractFromOpenAIBody / ExtractFromAnthropicBody
//	     ⇒ 上传/缓存侧：base64 落盘 + 内容哈希去重 + 记录坐标
//	  ② domains/attachments.OutboundURLRewriter.RewriteOpenAIBody / RewriteAnthropicBody
//	     ⇒ 转发侧（URL 模式上游）：base64 → 网关 URL
//	     或 domains/attachments.URLFetchFallback.InlineOpenAIBody
//	     ⇒ 转发侧（非 URL 模式上游）：网关 URL → data URI 内联
//	  ③ executor.finalizeOpenAIUpstreamBody
//	     ⇒ 网关 IR 转换：Parse → ValidateAndFix → 解析方言 → Serialize
//	  ④ 断言最终出站 body
//
// ★ 为什么必须测到这一步：★ ①②③ 各有自己的单测，★ 但**三者拼起来**才暴露
//   「附件在出站前被丢了 / 被改了 / 被污染了」这一类缺陷 ⇒ ★ 单测各自都绿，
//   ★ 而线上发出去的 body 是错的 ★。
//
// ★ 覆盖的缺陷（★ 修复前本文件会红 ⇒ 门有牙 ★）：
//   · 转发侧只内联 `image_url.url` ⇒ 文档/视频/音频的网关 URL **原样发给**
//     不支持 URL 的上游 ⇒ 上游取不到 ⇒ **附件丢失**
//   · 转发侧用**全 body 字节级替换** ⇒ 同一个网关 URL 若出现在**文本消息**里，
//     也会被替换成 base64 载荷 ⇒ **内容污染 + 载荷外泄**
//   · URL 模式转发只改写 `image_url` ⇒ 文档/视频/音频**每次请求全量重传**
//     （该功能存在的理由就是「别每次重传大载荷」）

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/provider"
)

const (
	// e2ePublicURL 是附件网关公开前缀（测试里用一个不指向真实主机的域名）。
	e2ePublicURL = "https://files.example.com/attachments"
	// e2eTinyPNG 是最小 PNG 头（8 字节签名 + 占位），够做载荷用。
	e2eTinyPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\x00"
	// e2eTinyPDF 是最小 PDF 头。
	e2eTinyPDF = "%PDF-1.4\n%\xE2\xE3\xCF\xD3\n"
)

func e2eB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// e2eStorage 建一个临时目录上的附件存储。
func e2eStorage(t *testing.T) *attachments.Storage {
	t.Helper()
	st, err := attachments.NewStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return st
}

// e2eWire 把出站 body 解析成通用结构（断言只读 wire，不看中间态）。
func e2eWire(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var w map[string]any
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("出站 body 不是合法 JSON：%v\n%s", err, string(body))
	}
	return w
}

// e2eSeedGatewayURL 把一个 data URI 落盘，返回它的**真实**网关 URL。
//
// ★ 为什么必须用**真实落盘路径**而不是手编路径：★ 回内联要真的能从存储里
//   取回内容（LoadAttachment）⇒ ⇒ 路径必须是内容哈希分片形态 ⇒ ⇒
//   ★ 手编路径 ⇒ 取回失败 ⇒ 回退保留原 URL ⇒ ⇒ **缺陷会被掩盖成「取不到」**
//   ⇒ ⇒ 那样这道门就测不出「只内联 image_url」这回事了 ⇒ ⇒ 必须自造真值 ★。
func e2eSeedGatewayURL(t *testing.T, storage *attachments.Storage, dataURI string, blockIdx int) string {
	t.Helper()
	res, err := storage.SaveBase64Image("req_seed", dataURI, 0, blockIdx)
	if err != nil {
		t.Fatalf("SaveBase64Image 失败：%v", err)
	}
	return e2ePublicURL + "/" + res.Metadata.Path
}

// e2eBlocks 返回 messages[i].content 的块数组（content 是字符串时返回 nil）。
func e2eBlocks(t *testing.T, w map[string]any, msgIdx int) []map[string]any {
	t.Helper()
	msgs, ok := w["messages"].([]any)
	if !ok {
		t.Fatalf("出站 body 没有 messages 数组")
	}
	if msgIdx >= len(msgs) {
		t.Fatalf("出站 body 的 messages 只有 %d 条，取不到第 %d 条", len(msgs), msgIdx)
	}
	m, ok := msgs[msgIdx].(map[string]any)
	if !ok {
		t.Fatalf("messages[%d] 不是对象", msgIdx)
	}
	arr, ok := m["content"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, b := range arr {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, bm)
	}
	return out
}

// e2eTextOf 取第 i 条消息的**全部文本**。
//
// ★ 为什么不能直接 `content.(string)`：★ 客户端连着两条 user 消息时，
//   网关的清洗链会把它们合并 ⇒ 出站 body 的 content 变成**块数组**
//   ⇒ ★ 直接断言字符串会取到空串 ⇒ ★ **把「断言取不到值」误读成「内容被清空」**
//   ⇒ ⇒ 两种形态都读（★ 这就是「量具要能区分「没有」和「不是那个形态」」★★）。
func e2eTextOf(t *testing.T, w map[string]any, msgIdx int) string {
	t.Helper()
	msgs, ok := w["messages"].([]any)
	if !ok {
		t.Fatalf("出站 body 没有 messages 数组")
	}
	if msgIdx >= len(msgs) {
		t.Fatalf("出站 body 的 messages 只有 %d 条，取不到第 %d 条", len(msgs), msgIdx)
	}
	m, ok := msgs[msgIdx].(map[string]any)
	if !ok {
		t.Fatalf("messages[%d] 不是对象", msgIdx)
	}
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, b := range c {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := bm["text"].(string); ok {
				sb.WriteString(s)
			}
		}
		return sb.String()
	default:
		t.Fatalf("messages[%d].content 既不是字符串也不是数组（%T）", msgIdx, m["content"])
		return ""
	}
}

// e2eAllMessagesText 拼起出站 body 里**所有**消息的文本。
//
// ★ 为什么不用固定下标：★ 客户端连着两条 user 消息时，网关的清洗链会**合并**
//   它们 ⇒ ⇒ 出站消息的**条数不固定**（★ 本轮实测：合并成 1 条 ★）⇒ ⇒
//   断言必须对「全部消息的文本」做，而不是对某一条做（★ 否则就是「假锚点」★）。
func e2eAllMessagesText(t *testing.T, w map[string]any) string {
	t.Helper()
	msgs, ok := w["messages"].([]any)
	if !ok {
		t.Fatalf("出站 body 里没有 messages 数组（%T）", w["messages"])
	}
	var sb strings.Builder
	for i := range msgs {
		sb.WriteString(e2eTextOf(t, w, i))
		sb.WriteString("\n")
	}
	return sb.String()
}

// e2eBlockField 取块里某个嵌套对象的字符串字段（如 image_url.url）。
func e2eBlockField(t *testing.T, block map[string]any, obj, field string) string {
	t.Helper()
	o, ok := block[obj].(map[string]any)
	if !ok {
		t.Fatalf("块 %v 里没有 %q 对象", block, obj)
	}
	v, ok := o[field].(string)
	if !ok {
		t.Fatalf("块 %v 的 %q 里没有字符串字段 %q", block, obj, field)
	}
	return v
}

// e2eRunOutbound 跑完整的「客户端 → 网关 → 出站」链路，返回出站 body。
//
// ★ 参数与 executor_dispatch.go 的 MM-1/MM-2 段一一对应：
//   · clientProtocol 决定用哪个提取器（OpenAI / Anthropic 两种 body 形态不同）
//   · catalogCode 决定附件策略（URL 模式 ⇒ 改写；非 URL 模式 ⇒ 内联）
func e2eRunOutbound(t *testing.T, clientProtocol, catalogCode string, clientBody []byte) []byte {
	t.Helper()
	return e2eRunOutboundWithStorage(t, clientProtocol, catalogCode, clientBody, e2eStorage(t))
}

// e2eRunOutboundWithStorage 是可共用存储的版本（★ E2E-② 需要：附件先落盘、
// 再把网关 URL 放进客户端 body、最后由回退取回 ⇒ 三步必须是**同一个**存储 ★）。
func e2eRunOutboundWithStorage(t *testing.T, clientProtocol, catalogCode string, clientBody []byte, storage *attachments.Storage) []byte {
	t.Helper()
	extractor := attachments.NewExtractor(storage)

	// ① 上传/缓存侧：提取并落盘
	var res *attachments.ExtractResult
	switch clientProtocol {
	case "anthropic-messages":
		res = extractor.ExtractFromAnthropicBody("req_e2e", clientBody)
	default:
		res = extractor.ExtractFromOpenAIBody("req_e2e", clientBody)
	}
	if res.Failed != 0 {
		t.Fatalf("附件落盘失败：failed=%d total=%d", res.Failed, res.TotalFound)
	}

	// ② 转发侧：URL 模式改写 / 非 URL 模式内联（两者按 provider 能力互斥）
	body := clientBody
	if attachments.ShouldURLRewrite(catalogCode) {
		rewriter := attachments.NewOutboundURLRewriter(e2ePublicURL)
		var n int
		if clientProtocol == "anthropic-messages" {
			body, n = rewriter.RewriteAnthropicBody(body, res.Attachments, catalogCode)
		} else {
			body, n = rewriter.RewriteOpenAIBody(body, res.Attachments, catalogCode)
		}
		t.Logf("转发侧改写（URL 模式，%s）：%d 个块", catalogCode, n)
	} else if attachments.ShouldFetchFallback(catalogCode) {
		fallback := attachments.NewURLFetchFallback(e2ePublicURL, storage)
		var n int
		body, n = fallback.InlineOpenAIBody(body, catalogCode)
		t.Logf("转发侧内联（非 URL 模式，%s）：%d 处", catalogCode, n)
	}

	// ③ 网关 IR 转换（生产函数）
	executor := &Executor{IR: &irAdapterForTest{}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	cand := provider.Candidate{
		ProviderID:   1,
		CredentialID: 100,
		Protocol:     "openai-completions",
		RawModel:     "upstream-model",
		CatalogCode:  catalogCode,
	}
	params := &ExecParams{
		R:              req,
		BodyBytes:      body,
		ClientModel:    "client-model",
		OutboundModel:  "upstream-model",
		ClientProtocol: clientProtocol,
	}
	out, err := executor.finalizeOpenAIUpstreamBody(params, cand, body)
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody 失败：%v", err)
	}
	return out
}

// ══════════════════════════════════════════════════════════════════════
// E2E-①：URL 模式上游 ⇒ 图片/文档/视频三类附件都要换成网关 URL
// ══════════════════════════════════════════════════════════════════════

// TestE2EMultimodalOutbound_URLCapableUpstream_RewritesAllAttachmentKinds 钉住
// 「URL 模式转发只改写 image_url ⇒ 文档/视频每次请求全量重传」这一缺陷。
//
// 缺陷（修复前）：OutboundURLRewriter.RewriteOpenAIBody 只读 image_url.url
// ⇒ file.file_data 与 video_url.url 仍是 base64 ⇒ 每次请求都把大载荷重传给上游
// ⇒ 而该功能存在的理由正是「别每次重传」。
func TestE2EMultimodalOutbound_URLCapableUpstream_RewritesAllAttachmentKinds(t *testing.T) {
	img := "data:image/png;base64," + e2eB64(e2eTinyPNG)
	doc := "data:application/pdf;base64," + e2eB64(e2eTinyPDF)
	vid := "data:video/mp4;base64," + e2eB64("fake-mp4-bytes")

	clientBody := []byte(`{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "看这三样"},
				{"type": "image_url", "image_url": {"url": "` + img + `"}},
				{"type": "file", "file": {"filename": "a.pdf", "mime_type": "application/pdf", "file_data": "` + doc + `"}},
				{"type": "video_url", "video_url": {"url": "` + vid + `"}}
			]
		}]
	}`)

	// ★ openai：SupportsHTTPSURL=true 且 PreferredMode=gateway_url ⇒ URL 模式。
	out := e2eRunOutbound(t, "openai-chat", "openai", clientBody)
	w := e2eWire(t, out)
	blocks := e2eBlocks(t, w, 0)
	if len(blocks) < 4 {
		t.Fatalf("出站 body 的块数 = %d，期望 ≥4（text+image+file+video）", len(blocks))
	}

	imageURL := e2eBlockField(t, blocks[1], "image_url", "url")
	if !strings.HasPrefix(imageURL, e2ePublicURL+"/") {
		t.Errorf("image_url.url = %q，期望网关 URL（前缀 %q）", imageURL, e2ePublicURL+"/")
	}
	// ★★★ 本缺陷的正面断言：文档也必须是网关 URL。
	docData := e2eBlockField(t, blocks[2], "file", "file_data")
	if !strings.HasPrefix(docData, e2ePublicURL+"/") {
		t.Errorf("file.file_data = %q，期望网关 URL（前缀 %q）—— "+
			"文档附件没有换成 URL ⇒ 每次请求都全量重传大载荷",
			docData, e2ePublicURL+"/")
	}
	// ★★★ 视频同理。
	vidURL := e2eBlockField(t, blocks[3], "video_url", "url")
	if !strings.HasPrefix(vidURL, e2ePublicURL+"/") {
		t.Errorf("video_url.url = %q，期望网关 URL（前缀 %q）", vidURL, e2ePublicURL+"/")
	}
}

// ══════════════════════════════════════════════════════════════════════
// E2E-②：非 URL 模式上游 ⇒ 网关 URL 必须被回内联（图片/文档/视频三类）
// ══════════════════════════════════════════════════════════════════════

// TestE2EMultimodalOutbound_NonURLCapableUpstream_InlinesAllAttachmentKinds 钉住
// 「转发侧只内联 image_url ⇒ 文档/视频的网关 URL 原样发给上游 ⇒ 附件丢失」这一缺陷。
//
// 场景：客户端请求里带着**上一轮附件的网关 URL**（会话状态里就是网关 URL）。
// ★ deepseek：SupportsHTTPSURL=false ⇒ 必须回内联为 data URI，否则上游取不到。
func TestE2EMultimodalOutbound_NonURLCapableUpstream_InlinesAllAttachmentKinds(t *testing.T) {
	// ★ 附件先落盘（拿到**真实**的网关 URL）⇒ 客户端请求里带着「上一轮附件的
	//   网关 URL」（★ 会话状态里就是网关 URL ⇒ ★ 这不是编造的场景 ★）。
	storage := e2eStorage(t)
	imgURL := e2eSeedGatewayURL(t, storage,
		"data:image/png;base64,"+e2eB64(e2eTinyPNG), 1)
	docURL := e2eSeedGatewayURL(t, storage,
		"data:application/pdf;base64,"+e2eB64(e2eTinyPDF), 2)
	vidURL := e2eSeedGatewayURL(t, storage,
		"data:video/mp4;base64,"+e2eB64("fake-mp4-bytes"), 3)
	t.Logf("落盘后的网关 URL：img=%s doc=%s vid=%s", imgURL, docURL, vidURL)

	clientBody := []byte(`{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "这三样还在吗"},
				{"type": "image_url", "image_url": {"url": "` + imgURL + `"}},
				{"type": "file", "file": {"filename": "a.pdf", "mime_type": "application/pdf", "file_data": "` + docURL + `"}},
				{"type": "video_url", "video_url": {"url": "` + vidURL + `"}}
			]
		}]
	}`)

	// ★ deepseek：SupportsHTTPSURL=false ⇒ 走 URLFetchFallback 内联。
	out := e2eRunOutboundWithStorage(t, "openai-chat", "deepseek", clientBody, storage)
	w := e2eWire(t, out)
	blocks := e2eBlocks(t, w, 0)
	if len(blocks) < 4 {
		t.Fatalf("出站 body 的块数 = %d，期望 ≥4（text+image+file+video）", len(blocks))
	}

	imageURL := e2eBlockField(t, blocks[1], "image_url", "url")
	if !strings.HasPrefix(imageURL, "data:") {
		t.Errorf("image_url.url = %q，期望 data URI（不支持 URL 的上游必须拿到内联载荷）", imageURL)
	}
	// ★★★ 文档必须也被内联，否则上游拿到的是一个它取不到的 URL ⇒ 附件丢失。
	docData := e2eBlockField(t, blocks[2], "file", "file_data")
	if !strings.HasPrefix(docData, "data:") {
		t.Errorf("file.file_data = %q，期望 data URI —— 文档附件没有内联 ⇒ "+
			"不支持 URL 的上游取不到它 ⇒ 附件在链路上丢失", docData)
	}
	// ★★★ 视频同理。
	vidURL2 := e2eBlockField(t, blocks[3], "video_url", "url")
	if !strings.HasPrefix(vidURL2, "data:") {
		t.Errorf("video_url.url = %q，期望 data URI —— 视频附件没有内联", vidURL2)
	}
}

// ══════════════════════════════════════════════════════════════════════
// E2E-③：文本消息里的同名 URL 不得被替换成载荷（内容污染）
// ══════════════════════════════════════════════════════════════════════

// TestE2EMultimodalOutbound_ReplacementIsFieldPrecise_NoTextContamination 钉住
// 「全 body 字节级替换 ⇒ 文本消息里出现的同名 URL 也被替换成 base64 载荷」。
//
// ★ 后果（两重）：★ ① **内容污染**：用户/模型说的话被改成一大坨 base64；
//   ★ ② **载荷外泄**：附件内容被塞进本不该出现它的文本字段里。
func TestE2EMultimodalOutbound_ReplacementIsFieldPrecise_NoTextContamination(t *testing.T) {
	storage := e2eStorage(t)
	imgURL := e2eSeedGatewayURL(t, storage,
		"data:image/png;base64,"+e2eB64(e2eTinyPNG), 1)

	clientBody := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "上一轮的图在这里：` + imgURL + ` 麻烦看看"},
			{"role": "user", "content": [
				{"type": "text", "text": "这张呢 ` + imgURL + `"},
				{"type": "image_url", "image_url": {"url": "` + imgURL + `"}}
			]}
		]
	}`)

	out := e2eRunOutboundWithStorage(t, "openai-chat", "deepseek", clientBody, storage)
	w := e2eWire(t, out)

	// ★★ 两种文本形态都必须**逐字**保留原 URL ★★：
	//   ① content 是**字符串**（"上一轮的图在这里：…"）
	//   ② content 是**块数组**里的 text 块（"这张呢 …"）
	// ⇒ ★ 断言打在「全部消息的文本」上（★ 消息条数不固定：网关清洗链会合并 ★）。
	all := e2eAllMessagesText(t, w)
	if !strings.Contains(all, "上一轮的图在这里："+imgURL) {
		t.Errorf("文本消息被改写了：%q\n★ 期望原样保留 %q\n★ ⇒ 字节级全 body 替换把文本污染了",
			all, imgURL)
	}
	if !strings.Contains(all, "这张呢 "+imgURL) {
		t.Errorf("文本块被改写了：%q\n★ 期望原样保留 %q\n★ ⇒ 字段表被扩到 text ⇒ 文本污染",
			all, imgURL)
	}
	if strings.Contains(all, "base64,") {
		t.Errorf("文本消息里混进了 base64 载荷：%q ⇒ 内容污染 + 载荷外泄", all)
	}
}

// ══════════════════════════════════════════════════════════════════════
// E2E-④：Anthropic 客户端 → OpenAI 上游（图片 + 文档）
// ══════════════════════════════════════════════════════════════════════

// TestE2EMultimodalOutbound_AnthropicClient_RewritesImageAndDocument 钉住
// 「Anthropic 形态的 URL 改写只处理 image ⇒ document 块每次全量重传」。
//
// ★ Anthropic 客户端的文档块形态：{"type":"document","source":{"type":"base64",...}}
func TestE2EMultimodalOutbound_AnthropicClient_RewritesImageAndDocument(t *testing.T) {
	clientBody := []byte(`{
		"model": "claude-sonnet-5",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "看这两样"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + e2eB64(e2eTinyPNG) + `"}},
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "` + e2eB64(e2eTinyPDF) + `"}}
			]
		}]
	}`)

	// ★ openai 是 URL 模式 ⇒ 两类附件都该换成网关 URL。
	out := e2eRunOutbound(t, "anthropic-messages", "openai", clientBody)
	w := e2eWire(t, out)
	blocks := e2eBlocks(t, w, 0)
	if len(blocks) < 3 {
		t.Fatalf("出站 body 的块数 = %d，期望 ≥3（text+image+document）", len(blocks))
	}

	imageURL := e2eBlockField(t, blocks[1], "image_url", "url")
	if !strings.HasPrefix(imageURL, e2ePublicURL+"/") {
		t.Errorf("image_url.url = %q，期望网关 URL", imageURL)
	}
	// ★★★ 文档（Anthropic 形态）也必须换成网关 URL。
	docData := e2eBlockField(t, blocks[2], "file", "file_data")
	if !strings.HasPrefix(docData, e2ePublicURL+"/") {
		t.Errorf("file.file_data = %q，期望网关 URL —— Anthropic 客户端的文档块没有换成 URL", docData)
	}
}
