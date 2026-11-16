package attachments

import (
	"encoding/base64"
	"log/slog"
	"strings"

	sessionv2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// outbound_fetch_fallback.go — MM-2 (doc 19 轨道 MM)
//
// URL 拉取回退：目标 provider 不支持 url source（矩阵判定
// SupportsHTTPSURL=false，或出站改写成网关 URL 后目标供应商实际不支持 URL
// 输入）而出站 body 仍以网关 URL 引用附件时，网关从自身附件存储取回内容、
// 重新内联为 base64 data URI 后出站（provider-url-support-matrix.md §3）。
//
// 取回优先走网关自身存储（AttachmentContentFetcher，*Storage 即实现），
// 不发起外网 HTTP 请求；单个附件取回失败或超出 provider inline 上限时
// best-effort 保留原 URL 引用，不吞掉原请求。

// AttachmentContentFetcher abstracts content retrieval for stored
// attachments. *Storage satisfies it via LoadAttachment.
type AttachmentContentFetcher interface {
	// LoadAttachment returns the stored content and its MIME type for a
	// storage-relative path (e.g. 2026/08/a1/b2/<sha256>.png).
	LoadAttachment(relPath string) ([]byte, string, error)
}

// ShouldFetchFallback reports whether the target provider cannot fetch URL
// sources server-side (doc 19 MM-2 gate; inverse of the URL-support matrix).
func ShouldFetchFallback(provider string) bool {
	capability := sessionv2.GetProviderCapability(provider)
	return !capability.SupportsHTTPSURL
}

// URLFetchFallback re-inlines gateway-hosted attachment URLs as base64 data
// URIs for outbound bodies targeting providers without URL support.
type URLFetchFallback struct {
	fetcher AttachmentContentFetcher
	baseURL string // public base of gateway attachment URLs, no trailing "/"
}

// NewURLFetchFallback creates a fallback serving gateway URLs under baseURL
// (e.g. https://files.example.com/attachments) and fetching content through
// fetcher (pass the *Storage; nil fetcher disables inlining).
func NewURLFetchFallback(baseURL string, fetcher AttachmentContentFetcher) *URLFetchFallback {
	return &URLFetchFallback{fetcher: fetcher, baseURL: strings.TrimRight(baseURL, "/")}
}

// InlineOpenAIBody rewrites one outbound OpenAI-format attempt body. It walks
// messages[].content[] and, for every **attachment-carrying field** whose
// value is a gateway-URL reference (baseURL prefix), fetches the stored
// content and splices in a data URI.
//
// ★ 2026-11-11（附件域审计轮）：本函数的实现从「**全 body 字节级替换**」
//
//	改成了「**字段精确替换**」，同时修掉两处缺陷：
//
//	★ 缺陷①（只内联 image_url ⇒ 文档/视频在链路上丢失）：★★
//	  旧实现只认 `messages[].content[].image_url.url` ⇒ ⇒ 文档
//	  （file.file_data）与视频（video_url.url）**原样**把网关 URL 发给上游
//	  ⇒ ⇒ 而不支持 URL 的上游**取不到**它 ⇒ ⇒ **附件在链路上丢失**
//	  ⇒ ⇒ 现在走 attachment_fields.go 的**共享字段表** ⇒ ⇒ 三类都内联 ★★
//
//	★ 缺陷②（字节级替换 ⇒ 文本污染 + 载荷外泄）：★★
//	  旧实现用 `bytes.ReplaceAll(out, oldURL, dataURI)` 在**整个 body** 上替换
//	  ⇒ ⇒ 同一个网关 URL 若出现在**文本消息**里（例如用户在聊天里贴了上一轮
//	  的附件链接），也会被替换成 base64 载荷 ⇒ ⇒ **内容污染 + 载荷外泄**
//	  ⇒ ⇒ 现在只改「附件承载字段」⇒ ⇒ 文本**逐字不动** ★★
//
//	★ 缺陷③（MIME 取自文件名反推 ⇒ 上游收到错类型）：★★
//	  LoadAttachment 只能按 relPath 取回内容，MIME 是**从扩展名反推**的
//	  ⇒ ⇒ 而未登记的 MIME（video/mp4、audio/wav、text/csv…）在落盘时
//	  ⇒ ⇒ 落成 `.bin` ⇒ ⇒ 反推出来是 application/octet-stream ⇒ ⇒
//	  ⇒ 出站 data URI 的 MIME **是错的** ⇒ ⇒ 上游按错类型解析附件 ★★
//	  ⇒ ⇒ 现在 **MIME 优先取落盘记录里的原始 ContentType**（★ 真源 ★）★★
//
//	★★★ 收益窗口（如实登记，**别说过头**）：★★★
//	  落盘记录（logCtx.Attachments）只覆盖「**本次请求**里以 base64 提取过
//	  的文件」⇒ 而 Extractor **只认 data: 前缀** ⇒ ⇒ 以**网关 URL** 引用的
//	  文件**从不被记录** ⇒ ⇒ 两边**只在**「同一次请求里既发了 base64 又
//	  引用了同一内容的 URL」时才重合（★ 内容寻址 ⇒ 路径相同 ⇒ 查得到 ★）
//	  ⇒ ⇒ 所以这条修法**能**命中，★ 但**不是**「历史文件全都对了」★★
//	  ⇒ ⇒ 历史 .bin 文件在「记录不可得」时**仍是** octet-stream ⇒ ⇒
//	  ⇒ ⇒ 真要修它需要**按路径查元数据**（★ 热路径 DB 查询 ⇒ 需另开一轮 ★）★★
//
// External URLs and existing data URIs are untouched. Fetch failures and
// provider inline-size overruns keep the original URL (best-effort, never
// fails the request). Returns the body (unchanged when nothing applies) and
// the number of attachment fields inlined.
//
// ★ 参数说明：★ attachments 是**落盘记录**（★ 与 RewriteOpenAIBody 对称 ★）
//
//	⇒ ★ 只用它取 MIME，不用于坐标核对（★ 内联按 URL 前缀定位 ★）。
func (f *URLFetchFallback) InlineOpenAIBody(body []byte, attachments []AttachmentMetadata, provider string) ([]byte, int) {
	if f == nil || f.fetcher == nil || len(body) == 0 || !ShouldFetchFallback(provider) {
		return body, 0
	}

	prefix := f.baseURL + "/"
	capability := sessionv2.GetProviderCapability(provider)

	// ★ 落盘记录 ⇒ 原始 ContentType（★ MIME 的真源 ★）⇒ ★ relPath → MIME ★。
	// ★ ★ 为什么不从扩展名反推：★ 未登记的 MIME 落盘成 .bin ⇒ ⇒ 反推值是
	// ★ ★ application/octet-stream ⇒ ⇒ 出站 MIME 错 ⇒ ⇒ 上游按错类型解析 ★。
	mimeByPath := make(map[string]string, len(attachments))
	for _, att := range attachments {
		if att.Path == "" || att.ContentType == "" {
			continue
		}
		mimeByPath[att.Path] = att.ContentType
	}

	// Dedupe fetches per object key; a relPath is fetched at most once even
	// when the same attachment is referenced by several fields.
	dataURIs := make(map[string]string) // relPath → data URI

	out, inlined, err := rewriteChatAttachmentFields(body,
		func(ref attachmentSlotRef, value string) string {
			if !strings.HasPrefix(value, prefix) {
				return ""
			}
			relPath := strings.TrimPrefix(value, prefix)
			if relPath == "" || strings.Contains(relPath, "..") {
				return ""
			}
			if dataURI, ok := dataURIs[relPath]; ok {
				return dataURI
			}
			content, mime, err := f.fetcher.LoadAttachment(relPath)
			if err != nil {
				// best-effort：取回失败保留原 URL 引用。
				slog.Warn("attachments: fetch fallback load failed, keep URL",
					"path", relPath, "error", err)
				return ""
			}
			// ★ MIME 的真源 = **落盘扩展名**（经 mime_types.go 那张唯一表换算）★
			// ★（没有记录时才用 fetcher 从扩展名反推出来的值）★。
			//
			// ★ 2026-11-17（12h 审计 B2）：此前直接用**记录里的原始 ContentType**
			//   （那是客户端声明的原始 MIME，可能是别名，如 image/jpg）⇒
			//   而 URL 出口用的是 extToMimeType(落盘扩展名) = image/jpeg
			//   ⇒ **同一个文件两个出口两个 MIME** —— 正是 25fbd845f 要消灭的漂移类。
			//   现在内联出口也走 canonicalMIME（别名→规范 MIME）⇒
			//   已登记类型：内联 = image/jpeg，URL = image/jpeg ⇒ 按构造一致；
			//   未登记类型：保留归一后的原值（不退化成 octet-stream）。
			if recorded, ok := mimeByPath[relPath]; ok && recorded != "" {
				mime = canonicalMIME(recorded)
			}
			if mime == "" {
				mime = "application/octet-stream"
			}
			if capability.MaxInlineBytes > 0 && int64(len(content)) > capability.MaxInlineBytes {
				slog.Warn("attachments: fetch fallback exceeds provider inline limit, keep URL",
					"path", relPath,
					"content_bytes", len(content),
					"max_inline_bytes", capability.MaxInlineBytes)
				return ""
			}
			dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(content)
			dataURIs[relPath] = dataURI
			return dataURI
		})
	if err != nil {
		return body, 0
	}
	return out, inlined
}
