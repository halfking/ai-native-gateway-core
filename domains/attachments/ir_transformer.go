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
//
// ★★ 2026-11-11（附件域审计轮）：覆盖范围从「**只有 image**」扩到
//   **image / document / video / audio** 四类 ★★
//   根因：★ 此前 TransformRequest 只处理 `block.Type == "image"` ⇒ ⇒
//   文档（PDF）/ 视频 / 音频的 base64 载荷**完全没被落盘、也没被缓存**
//   ⇒ ⇒ 它们每次请求都要**原样重传**（大载荷）⇒ ⇒ 而落盘去重这一整套
//   基础设施对它们**不起作用**。
//   ⇒ 现在四类都走同一条「落盘 → 换成网关 URL」的路径。
//
//   ★ 为什么**四类都能换成 URL**：★ IR 的四种承载（ImageSource / DocumentSource
//   / MediaSource）在 wire 上都有 URL 形态 ⇒ ★ image_url.url / file.file_data /
//   video_url.url / input_audio.url ⇒ ⇒ 换过去**不会丢载荷**。
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

// TransformRequest 将请求 IR 中所有 base64 附件块落盘并替换为 URL 引用。
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
	// Storage.SaveBase64Image 承担（★ 它按内容 SHA256 分片命名 ⇒ 跨月不会
	// ★ 重复落盘 ⇒ 旧注释「路径含 YYYY/MM，跨月会重复落盘」**已过期** ★）。
	manifestsByData := make(map[string]AttachmentMetadata)

	for msgIdx := range req.Messages {
		content := req.Messages[msgIdx].Content
		for blockIdx := range content {
			block := &content[blockIdx]
			switch block.Type {
			case "image":
				if block.Image != nil && block.Image.Type == "base64" && block.Image.Data != "" {
					img := block.Image
					t.processAttachment(requestID, "image", img.MediaType, img.Data,
						msgIdx, blockIdx, manifestsByData, result,
						func(gwURL string) {
							img.Type = "url"
							img.URL = gwURL
							img.Data = ""
						})
				}
			case "document":
				// ★ 文档（PDF / 文本 / CSV）：★ 此前完全没被处理 ⇒ ★
				//   载荷每次请求全量重传 ⇒ ★ 现在与图片同款落盘 + 换成 URL。
				if block.Document != nil && block.Document.Source != nil &&
					block.Document.Source.Type == "base64" && block.Document.Source.Data != "" {
					src := block.Document.Source
					t.processAttachment(requestID, "file", src.MediaType, src.Data,
						msgIdx, blockIdx, manifestsByData, result,
						func(gwURL string) {
							src.Type = "url"
							src.URL = gwURL
							src.Data = ""
						})
				}
			case "video":
				if block.Video != nil && block.Video.Type == "base64" && block.Video.Data != "" {
					vid := block.Video
					t.processAttachment(requestID, "video", vid.MediaType, vid.Data,
						msgIdx, blockIdx, manifestsByData, result,
						func(gwURL string) {
							vid.Type = "url"
							vid.URL = gwURL
							vid.Data = ""
						})
				}
			case "audio":
				// ★ 音频：★ 与视频同款（★ MediaSource 承载）★。
				if block.Audio != nil && block.Audio.Type == "base64" && block.Audio.Data != "" {
					aud := block.Audio
					t.processAttachment(requestID, "audio", aud.MediaType, aud.Data,
						msgIdx, blockIdx, manifestsByData, result,
						func(gwURL string) {
							aud.Type = "url"
							aud.URL = gwURL
							aud.Data = ""
						})
				}
			}
		}
	}

	return result
}

// processAttachment 处理单个 base64 附件块：落盘（或复用 manifest）并替换为 URL 引用。
//
// ★ kind 是 Extractor 口径的附件类型（image / file / video / audio）——
//   ★ 与 domains/attachments/extractor.go 的标注**同口径** ⇒ ⇒
//   ★ 落盘记录与提取记录可以对上（★ 改写器按坐标核对时不会因类型不符而跳过 ★）。
// ★ defaultMediaType 是载荷缺 MIME 时的兜底（★ 宁诚实不猜测：★ 兜底值
//   只影响扩展名与 ContentType，不影响载荷本身 ★）。
func (t *IRTransformer) processAttachment(
	requestID string,
	kind string,
	mediaType string,
	data string,
	msgIdx, blockIdx int,
	manifestsByData map[string]AttachmentMetadata,
	result *TransformResult,
	applyURL func(gwURL string),
) {
	// 请求内去重预检：同 payload 直接复用首个 manifest，跳过解码落盘。
	key := kind + "\x00" + data
	if prev, ok := manifestsByData[key]; ok {
		reused := prev
		reused.MessageIndex = msgIdx
		reused.BlockIndex = blockIdx
		result.Deduped++
		result.Attachments = append(result.Attachments, reused)
		applyURL(t.AttachmentURL(reused.Path))
		return
	}

	if mediaType == "" {
		mediaType = defaultMediaTypeForKind(kind)
	}
	dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, data)

	res, err := t.storage.SaveBase64Image(requestID, dataURI, msgIdx, blockIdx)
	if err != nil {
		slog.Warn("attachments: ir transform save failed",
			"request_id", requestID,
			"kind", kind,
			"message_index", msgIdx,
			"block_index", blockIdx,
			"error", err)
		result.Failed++
		result.Failures = append(result.Failures, AttachmentMetadata{
			Type:         kind,
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
	// ★ 落盘记录的类型按**附件种类**标注（★ 与 Extractor 同口径 ★）。
	// ★ SaveBase64Image 成功路径硬编码 Type:"image"（历史图片专用 API）⇒
	//   ★ 必须**无条件覆盖** ★ ⇒ ★★ 「只在 meta.Type == "" 时覆盖」的写法
	//   ★★ 永远不触发 ⇒ file/video/audio 记录被**静默误标成 image** ★★
	//   ★★（★ 本轮由钉测 TestIRTransformer_DocumentVideoAudio_AreStoredAndReplaced 抓出 ★★）。
	meta.Type = kind
	manifestsByData[key] = meta
	result.Attachments = append(result.Attachments, meta)
	if res.Deduped {
		// 跨请求去重：文件早已存在，本次未写入新文件。
		result.Deduped++
	} else {
		result.Stored++
	}

	// 替换 IR 引用：base64 → 网关 URL
	applyURL(t.AttachmentURL(meta.Path))
}

// defaultMediaTypeForKind 给缺 MIME 的载荷一个兜底标注。
func defaultMediaTypeForKind(kind string) string {
	switch kind {
	case "file":
		return "application/pdf"
	case "video":
		return "video/mp4"
	case "audio":
		return "audio/wav"
	default:
		return "image/png"
	}
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
