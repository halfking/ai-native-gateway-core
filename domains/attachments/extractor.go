package attachments

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	async    bool
	callback func(requestID string, attachments []AttachmentMetadata)
	wg       sync.WaitGroup
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
	// Attachments contains a record for every detected attachment, including failures.
	Attachments []AttachmentMetadata
	// TotalFound 扫描到的 base64 附件总数
	TotalFound int
	// Saved 成功保存的数量
	Saved int
	// Failed 保存失败的数量。
	Failed int
}

// ExtractFromOpenAIBody 从 OpenAI Chat Completions 格式的请求体中提取 base64 附件。
//
// 扫描 messages[].content[]，覆盖四种媒体块形态：
//   - type=image_url 且 image_url.url 以 "data:" 开头（既有）
//   - type=input_audio：{"input_audio":{"data":"<b64>","format":"wav|mp3"}}
//     （N21-3：gemini inbound 音频经 IR 转换后也落为此形态，一个 case
//     同时覆盖 openai 原生音频与 gemini 转换音频两路）
//   - type=video_url 且 video_url.url 为 data URI（Qwen-VL / gemini 视频
//     经 ir.serializeOpenAIVideoBlock 的合成形态）
//   - type=file 且 file.file_data 为 data URI（文档/PDF，
//     ir.serializeOpenAIDocumentBlock 的合成形态）
//
// 对每个匹配块调用 Storage.SaveBase64Image。
//
// body 为原始 JSON 字节，不会被修改（转发用原始 body，确保上游收到完整图片）。
// 保存策略由调用方决定；结果始终包含每个检测到的附件状态。
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
			switch blockMap["type"] {
			case "image_url":
				imgURL, ok := blockMap["image_url"].(map[string]any)
				if !ok {
					continue
				}
				url, ok := imgURL["url"].(string)
				if !ok || !strings.HasPrefix(url, "data:") {
					continue
				}
				result.TotalFound++
				e.processOne(requestID, url, msgIdx, blockIdx, "image", result)
			case "input_audio":
				ia, ok := blockMap["input_audio"].(map[string]any)
				if !ok {
					continue
				}
				data, _ := ia["data"].(string)
				if data == "" {
					continue
				}
				format, _ := ia["format"].(string)
				result.TotalFound++
				e.processOne(requestID, audioDataURI(format, data), msgIdx, blockIdx, "audio", result)
			case "video_url":
				vu, ok := blockMap["video_url"].(map[string]any)
				if !ok {
					continue
				}
				url, _ := vu["url"].(string)
				if !strings.HasPrefix(url, "data:") {
					continue
				}
				result.TotalFound++
				e.processOne(requestID, url, msgIdx, blockIdx, "video", result)
			case "file":
				fi, ok := blockMap["file"].(map[string]any)
				if !ok {
					continue
				}
				fd, _ := fi["file_data"].(string)
				if !strings.HasPrefix(fd, "data:") {
					continue
				}
				result.TotalFound++
				e.processOne(requestID, fd, msgIdx, blockIdx, "file", result)
			}
		}
	}

	return result
}

// audioDataURI 把 OpenAI input_audio 的 (format, data) 归一化为 data URI。
// format 是 OpenAI 的音频格式词；mp3 的规范 MIME 是 audio/mpeg。format
// 缺失时按未知二进制标注——宁诚实不猜测（错误 MIME 会在取证时误导）。
func audioDataURI(format, base64Data string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "data:audio/wav;base64," + base64Data
	case "mp3":
		return "data:audio/mpeg;base64," + base64Data
	case "":
		return "data:application/octet-stream;base64," + base64Data
	default:
		return "data:audio/" + strings.ToLower(strings.TrimSpace(format)) + ";base64," + base64Data
	}
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
			switch blockMap["type"] {
			case "image":
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
				e.processOne(requestID, dataURI, msgIdx, blockIdx, "image", result)
			case "document":
				// N21-3 顺带闭合：Anthropic document 块（PDF 上传）此前
				// 与 openai-chat 侧的 file 块同样不入附件列。
				source, ok := blockMap["source"].(map[string]any)
				if !ok {
					continue
				}
				if source["type"] != "base64" {
					continue
				}
				mediaType, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				if data == "" {
					continue
				}
				if mediaType == "" {
					mediaType = "application/pdf"
				}
				dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
				result.TotalFound++
				e.processOne(requestID, dataURI, msgIdx, blockIdx, "file", result)
			}
		}
	}

	return result
}

// processOne 处理单个附件（同步或异步）。kind 是来源块的媒体种类
// （image/audio/video/file），同时用于成功与失败记录的类型标注。
func (e *Extractor) processOne(requestID, dataURI string, msgIdx, blockIdx int, kind string, result *ExtractResult) {
	if e.async {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			meta, err := e.saveOne(requestID, dataURI, msgIdx, blockIdx, kind)
			if err == nil && e.callback != nil {
				e.callback(requestID, []AttachmentMetadata{meta})
			}
		}()
		return
	}
	// 同步
	meta, err := e.saveOne(requestID, dataURI, msgIdx, blockIdx, kind)
	result.Attachments = append(result.Attachments, meta)
	if err == nil {
		result.Saved++
	} else {
		result.Failed++
	}
}

// saveOne calls Storage and returns a log-safe record for both outcomes.
func (e *Extractor) saveOne(requestID, dataURI string, msgIdx, blockIdx int, kind string) (AttachmentMetadata, error) {
	failed := AttachmentMetadata{
		Type:         kind,
		OriginalURL:  truncateOriginalURL(dataURI),
		MessageIndex: msgIdx,
		BlockIndex:   blockIdx,
		CreatedAt:    time.Now(),
		Status:       AttachmentStatusStoreFailed,
		ErrorCode:    "storage_unavailable",
	}
	if e.storage == nil {
		return failed, errors.New("attachments: storage is nil")
	}
	res, err := e.storage.SaveBase64Image(requestID, dataURI, msgIdx, blockIdx)
	if err != nil {
		slog.Warn("attachments: save failed",
			"request_id", requestID,
			"message_index", msgIdx,
			"error", err)
		failed.ErrorCode = storageErrorCode(err)
		return failed, err
	}
	// SaveBase64Image 成功路径硬编码 Type:"image"（历史图片专用 API）；
	// 按来源块种类诚实覆盖，audio/video/file 记录不再被误标为图片。
	res.Metadata.Type = kind
	return res.Metadata, nil
}

func truncateOriginalURL(value string) string {
	if len(value) > 200 {
		return value[:200] + "..."
	}
	return value
}

func storageErrorCode(err error) string {
	if errors.Is(err, ErrInvalidDataURI) {
		return "invalid_data_uri"
	}
	if strings.Contains(err.Error(), "too large") {
		return "file_too_large"
	}
	return "storage_failed"
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
