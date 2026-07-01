package attachments

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Extractor 从 LLM 请求体（OpenAI / Anthropic 格式）中扫描并提取 base64 编码的附件。
//
// 它与 Storage 配合：Extractor 负责"找到附件"，Storage 负责"存下来"。
// 两者分离使得 Extractor 可以在纯内存（无 Storage）场景下做 dry-run 统计。
type Extractor struct {
	storage *Storage
	// async 是否异步保存。异步模式下 ExtractAttachments 立即返回空列表，
	// 保存结果在后台 goroutine 中通过 callback 回传。
	// 异步用于对延迟敏感的路径（如转发前的热路径）。
	async     bool
	callback  func(requestID string, attachments []AttachmentMetadata)
	wg        sync.WaitGroup
}

// NewExtractor 构造提取器。storage 为 nil 时 ExtractAttachments 只扫描不保存。
func NewExtractor(storage *Storage) *Extractor {
	return &Extractor{storage: storage}
}

// SetAsync 启用异步模式。callback 在每个后台保存完成时调用（可能多次）。
// 异步模式下调用方应在请求结束前 Wait() 确保所有保存完成。
func (e *Extractor) SetAsync(cb func(requestID string, attachments []AttachmentMetadata)) {
	e.async = true
	e.callback = cb
}

// Wait 等待所有异步保存任务完成（仅异步模式有意义）。
func (e *Extractor) Wait() {
	e.wg.Wait()
}

// ExtractResult 是 ExtractFromOpenAIBody 的返回。
type ExtractResult struct {
	// Attachments 成功保存的附件元数据。同步模式下完整；异步模式下为空。
	Attachments []AttachmentMetadata
	// TotalFound 扫描到的 base64 附件总数
	TotalFound int
	// Saved 成功保存的数量
	Saved int
	// Failed 保存失败的数量（不影响请求转发）
	Failed int
}

// ExtractFromOpenAIBody 从 OpenAI Chat Completions 格式的请求体中提取 base64 附件。
//
// 扫描 messages[].content[]，查找 type=image_url 且 image_url.url 以 "data:" 开头的块。
// 对每个匹配块调用 Storage.SaveBase64Image。
//
// body 为原始 JSON 字节，不会被修改（转发用原始 body，确保上游收到完整图片）。
// 失败的附件只记录 warning，不返回错误 —— 附件保存是 best-effort，不应阻塞转发。
func (e *Extractor) ExtractFromOpenAIBody(requestID string, body []byte) *ExtractResult {
	result := &ExtractResult{}

	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		slog.Debug("attachments: body is not a JSON object, skip",
			"request_id", requestID, "error", err)
		return result
	}

	messages, ok := bodyMap["messages"].([]any)
	if !ok {
		return result
	}

	for msgIdx, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		// content 可以是 string（纯文本）或 array（多模态）
		contentArr, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for blockIdx, block := range contentArr {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] != "image_url" {
				continue
			}
			imgURL, ok := blockMap["image_url"].(map[string]any)
			if !ok {
				continue
			}
			url, ok := imgURL["url"].(string)
			if !ok || !strings.HasPrefix(url, "data:") {
				continue
			}
			result.TotalFound++
			e.processOne(requestID, url, msgIdx, blockIdx, result)
		}
	}

	return result
}

// ExtractFromAnthropicBody 从 Anthropic Messages 格式的请求体中提取 base64 附件。
//
// Anthropic 图片格式：
//
//	{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//
// 我们将其归一化为 data URI 再交给 Storage。
func (e *Extractor) ExtractFromAnthropicBody(requestID string, body []byte) *ExtractResult {
	result := &ExtractResult{}

	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return result
	}

	messages, ok := bodyMap["messages"].([]any)
	if !ok {
		return result
	}

	for msgIdx, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		contentArr, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for blockIdx, block := range contentArr {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] != "image" {
				continue
			}
			source, ok := blockMap["source"].(map[string]any)
			if !ok {
				continue
			}
			// 只处理 type=base64 的 source（type=url 的由上游直接拉取）
			if source["type"] != "base64" {
				continue
			}
			mediaType, _ := source["media_type"].(string)
			data, _ := source["data"].(string)
			if data == "" {
				continue
			}
			dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
			result.TotalFound++
			e.processOne(requestID, dataURI, msgIdx, blockIdx, result)
		}
	}

	return result
}

// processOne 处理单个附件（同步或异步）。
func (e *Extractor) processOne(requestID, dataURI string, msgIdx, blockIdx int, result *ExtractResult) {
	if e.async {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			meta := e.saveOne(requestID, dataURI, msgIdx, blockIdx)
			if meta != nil && e.callback != nil {
				e.callback(requestID, []AttachmentMetadata{*meta})
			}
		}()
		return
	}
	// 同步
	meta := e.saveOne(requestID, dataURI, msgIdx, blockIdx)
	if meta != nil {
		result.Attachments = append(result.Attachments, *meta)
		result.Saved++
	} else {
		result.Failed++
	}
}

// saveOne 调用 Storage 保存单个附件，返回元数据或 nil（失败时记录 warning）。
func (e *Extractor) saveOne(requestID, dataURI string, msgIdx, blockIdx int) *AttachmentMetadata {
	if e.storage == nil {
		return nil
	}
	res, err := e.storage.SaveBase64Image(requestID, dataURI, msgIdx, blockIdx)
	if err != nil {
		// 存储失败不阻塞转发：记录 warning，附件元数据不写入
		slog.Warn("attachments: save failed (request will still be forwarded)",
			"request_id", requestID,
			"message_index", msgIdx,
			"error", err)
		return nil
	}
	return &res.Metadata
}

// CountOnly 仅扫描统计附件数量，不保存。用于不需要保存但想知道有多少附件的场景。
func CountOnly(body []byte, protocol string) int {
	if protocol == "anthropic-messages" {
		return countAnthropic(body)
	}
	return countOpenAI(body)
}

func countOpenAI(body []byte) int {
	var bodyMap struct {
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return 0
	}
	n := 0
	for _, m := range bodyMap.Messages {
		for _, c := range m.Content {
			if c.Type == "image_url" && strings.HasPrefix(c.ImageURL.URL, "data:") {
				n++
			}
		}
	}
	return n
}

func countAnthropic(body []byte) int {
	var bodyMap struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					Type string `json:"type"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return 0
	}
	n := 0
	for _, m := range bodyMap.Messages {
		for _, c := range m.Content {
			if c.Type == "image" && c.Source.Type == "base64" {
				n++
			}
		}
	}
	return n
}
