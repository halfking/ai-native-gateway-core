package attachments

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// IRTransformer 将 IR 中的 base64 内嵌附件落盘，并替换为网关 URL 引用。
//
// 定位（MM-1a，仅存储与策略层）：
//   - 产物是 IR 侧的引用结构，供后续 outbound 渲染（MM-1b）按 provider 能力输出
//     data-uri / 网关 URL；本阶段不改真实转发 body（转发仍用原始请求字节）。
//   - best-effort：单个附件保存失败不阻断请求，失败块保持原 base64 形态，
//     后续渲染仍可用原始数据兜底。
//   - 跨请求去重：Storage.SaveBase64Image 按内容 SHA256 命名并去重；
//     同一请求内相同 hash 的重复出现直接复用首个 manifest，不重复落盘。
type IRTransformer struct {
	storage *Storage
	// publicBaseURL 附件公开访问前缀（attachments.Config.PublicURL），
	// 如 https://cdn.example.com/attachments 或 /api/attachments
	publicBaseURL string
}

// NewIRTransformer 创建附件 IR 转换器。
// publicBaseURL 为附件公开访问前缀，尾部的 "/" 会被归一化。
func NewIRTransformer(storage *Storage, publicBaseURL string) *IRTransformer {
	return &IRTransformer{
		storage:       storage,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

// TransformResult 是 TransformRequest 的结果统计。
type TransformResult struct {
	// Attachments 成功落盘的附件元数据（每个出现的块一条，含去重复用）。
	Attachments []AttachmentMetadata
	// Failures 保存失败的记录（Status=store_failed，不含原始数据）。
	Failures []AttachmentMetadata
	// Stored 本次实际写入新文件的数量。
	Stored int
	// Deduped 命中 hash 去重（请求内或跨请求）而复用 manifest 的数量。
	Deduped int
	// Failed 保存失败的数量。
	Failed int
}

// AttachmentURL 返回附件的网关公开 URL。
func (t *IRTransformer) AttachmentURL(relPath string) string {
	return joinAttachmentURL(t.publicBaseURL, relPath)
}

// TransformRequest 将请求 IR 中所有 base64 图片块落盘并替换为 URL 引用。
//
// ctx 为 MM-1b 接线预留（Storage 后端当前不接受 context）。
// 转换直接发生在传入的 IR 上；调用方用于真实转发的原始 body 字节不受影响。
// 任何单个块失败都会记录到 Failures 并继续处理其余块，函数本身不返回 error。
func (t *IRTransformer) TransformRequest(ctx context.Context, requestID string, req *ir.InternalRequest) *TransformResult {
	result := &TransformResult{}
	if req == nil {
		return result
	}

	// 请求内 data → 首次落盘的 manifest；同一 base64 payload 再次出现时直接
	// 复用，避免重复解码/哈希大 payload。按内容 hash 的跨请求去重仍由
	// Storage.SaveBase64Image 承担（注意其路径含 YYYY/MM，跨月会重复落盘）。
	manifestsByData := make(map[string]AttachmentMetadata)

	for msgIdx := range req.Messages {
		content := req.Messages[msgIdx].Content
		for blockIdx := range content {
			block := &content[blockIdx]
			if block.Type != "image" || block.Image == nil {
				continue
			}
			img := block.Image
			if img.Type != "base64" || img.Data == "" {
				continue
			}
			t.processImageBlock(requestID, img, msgIdx, blockIdx, manifestsByData, result)
		}
	}

	return result
}

// processImageBlock 处理单个 base64 图片块：落盘（或复用 manifest）并替换为 URL 引用。
func (t *IRTransformer) processImageBlock(
	requestID string,
	img *ir.ImageSource,
	msgIdx, blockIdx int,
	manifestsByData map[string]AttachmentMetadata,
	result *TransformResult,
) {
	// 请求内去重预检：同 payload 直接复用首个 manifest，跳过解码落盘。
	if prev, ok := manifestsByData[img.Data]; ok {
		reused := prev
		reused.MessageIndex = msgIdx
		reused.BlockIndex = blockIdx
		result.Deduped++
		result.Attachments = append(result.Attachments, reused)
		img.Type = "url"
		img.URL = t.AttachmentURL(reused.Path)
		img.Data = ""
		return
	}

	mediaType := img.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, img.Data)

	res, err := t.storage.SaveBase64Image(requestID, dataURI, msgIdx, blockIdx)
	if err != nil {
		slog.Warn("attachments: ir transform save failed",
			"request_id", requestID,
			"message_index", msgIdx,
			"block_index", blockIdx,
			"error", err)
		result.Failed++
		result.Failures = append(result.Failures, AttachmentMetadata{
			Type:         "image",
			OriginalURL:  truncateOriginalURL(dataURI),
			MessageIndex: msgIdx,
			BlockIndex:   blockIdx,
			CreatedAt:    time.Now(),
			Status:       AttachmentStatusStoreFailed,
			ErrorCode:    storageErrorCode(err),
		})
		// 保持块原 base64 形态，后续渲染可用原始数据兜底。
		return
	}

	meta := res.Metadata
	manifestsByData[img.Data] = meta
	result.Attachments = append(result.Attachments, meta)
	if res.Deduped {
		// 跨请求去重：文件早已存在，本次未写入新文件。
		result.Deduped++
	} else {
		result.Stored++
	}

	// 替换 IR 引用：base64 → 网关 URL
	img.Type = "url"
	img.URL = t.AttachmentURL(meta.Path)
	img.Data = ""
}

// joinAttachmentURL 拼接公开前缀与相对路径，容忍前缀中含路径段与多余斜杠。
func joinAttachmentURL(base, relPath string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return relPath
	}
	if u, err := url.Parse(base); err == nil {
		return u.JoinPath(relPath).String()
	}
	return base + "/" + relPath
}
