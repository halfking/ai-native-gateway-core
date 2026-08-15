package attachments

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
// messages[].content[].image_url.url, and for every gateway-URL reference
// (baseURL prefix) fetches the stored content and splices in a data URI.
// External URLs and existing data URIs are untouched. Fetch failures and
// provider inline-size overruns keep the original URL (best-effort, never
// fails the request). Returns the body (unchanged when nothing applies) and
// the number of URL references inlined.
func (f *URLFetchFallback) InlineOpenAIBody(body []byte, provider string) ([]byte, int) {
	if f == nil || f.fetcher == nil || len(body) == 0 || !ShouldFetchFallback(provider) {
		return body, 0
	}

	var parsed struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, 0
	}

	// blocksOf decodes one message's content; string content (plain text
	// turns) is not multimodal and yields no blocks.
	blocksOf := func(raw json.RawMessage) []struct {
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	} {
		var blocks []struct {
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil
		}
		return blocks
	}

	prefix := f.baseURL + "/"
	capability := sessionv2.GetProviderCapability(provider)

	// Dedupe fetches per object key; collect URL → data-URI replacements.
	replacements := make(map[string]string)
	dataURIs := make(map[string]string) // relPath → data URI
	for i := range parsed.Messages {
		blocks := blocksOf(parsed.Messages[i].Content)
		for j := range blocks {
			iu := blocks[j].ImageURL
			if iu == nil || !strings.HasPrefix(iu.URL, prefix) {
				continue
			}
			if _, done := replacements[iu.URL]; done {
				continue
			}
			relPath := strings.TrimPrefix(iu.URL, prefix)
			if relPath == "" || strings.Contains(relPath, "..") {
				continue
			}
			if dataURI, ok := dataURIs[relPath]; ok {
				replacements[iu.URL] = dataURI
				continue
			}
			content, mime, err := f.fetcher.LoadAttachment(relPath)
			if err != nil {
				// best-effort：取回失败保留原 URL 引用。
				slog.Warn("attachments: fetch fallback load failed, keep URL",
					"path", relPath, "error", err)
				continue
			}
			if mime == "" {
				mime = "application/octet-stream"
			}
			if capability.MaxInlineBytes > 0 && int64(len(content)) > capability.MaxInlineBytes {
				slog.Warn("attachments: fetch fallback exceeds provider inline limit, keep URL",
					"path", relPath,
					"content_bytes", len(content),
					"max_inline_bytes", capability.MaxInlineBytes)
				continue
			}
			dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(content)
			dataURIs[relPath] = dataURI
			replacements[iu.URL] = dataURI
		}
	}
	if len(replacements) == 0 {
		return body, 0
	}

	out := body
	inlined := 0
	for oldURL, dataURI := range replacements {
		occurrences := bytes.Count(out, []byte(oldURL))
		if occurrences == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(oldURL), []byte(dataURI))
		inlined += occurrences
	}
	if inlined == 0 {
		return body, 0
	}
	return out, inlined
}
