package attachments

// mime_types.go —— MIME ↔ 文件扩展名的**唯一事实源**（2026-11-11 附件域审计轮）
//
// ── 为什么要有这个文件 ────────────────────────────────────────────────────
// ★ 同一份「MIME ↔ 扩展名」事实此前散在**三处**：
//   · storage.go 的 mimeTypeToExt（落盘命名）
//   · storage.go 的 extToMimeType（取回 / 对外服务的 Content-Type）
//   · detect_content_type.go 的 detectContentType（S3 PutObject 的 Content-Type）
// ⇒ ⇒ 三处**已经互相漂移**：★ `.zip` / `.mp4` / `.mp3` 只在 S3 那张表里，
//   ★ `.html` 只在另两张表里 ⇒ ⇒ 同一个 MIME 在不同出口得到**不同**的扩展名。
// ⇒ ★★ 这与上一轮修掉的「附件字段表各写一份」是**同款病因**
//   （★ 事实源散在多处 ⇒ 必然漂移 ⇒ 修一处漏一处 ★）。
//
// ── ★★ 而且这张表太窄 ⇒ ★★ 未登记的 MIME 一律落成 ".bin" ⇒ ★★
// ★★ 于是「类型信息」在落盘文件名里**丢失** ⇒ ★★ 三个出口全错：
// ★★   ① 内联回退：LoadAttachment 从扩展名反推 ⇒ application/octet-stream
// ★★      ⇒ 出站 data URI 的 MIME 错 ⇒ 上游按错类型解析附件
// ★★   ② 对外提供 URL：OpenStream 同样从扩展名反推 ⇒ Content-Type 错
// ★★      ⇒ URL 模式上游按 URL 拉取时拿到错类型
// ★★   ③ 落盘文件名：`<hash>.bin` ⇒ 路径里**不含**类型信息 ⇒ 事后无从还原
//
// ⇒ 本文件把三个消费者收敛到**一张规范表** ⇒ 补登记只需改这一处 ⇒ 三边同时生效。
//
// ── 边界（如实登记，不假装覆盖）────────────────────────────────────────────
// ★ ① 扩展名是**有损**的：`.webm` 既可能是 audio/webm 也可能是 video/webm；
//   ★ 本表取 video/webm（附件场景更常见）⇒ ★ 落盘后无法再区分。
//   ★ ② 本表**只决定文件名与 Content-Type**，不改变载荷本身。
//   ★ ③ 历史已落盘的 `.bin` 文件**不会被改名**（改名会破坏内容寻址的去重键）
//   ★ ⇒ 它们的**对外服务** Content-Type 仍是 application/octet-stream；
//   ★ ⇒ **内联出口现在也按落盘扩展名换算**（见 outbound_fetch_fallback.go）
//   ★ ⇒ ⇒ 两个出口对历史文件**给出同一个类型**（octet-stream），不再分叉。
//   ★ ⚠ 这意味着历史文件的内联类型**不是**它当年的原始类型 —— 那份原始 MIME
//   ★   只保留在落盘记录里，不再是对外类型。
//   ★   （2026-11-17 12h 审计 B2 订正：此前内联出口用**记录里的原始 ContentType**
//   ★   ⇒ 别名（image/jpg）与扩展名换算值（image/jpeg）不同 ⇒ 同一文件两个出口
//   ★   两个 MIME ⇒ 与本文件「三个消费者收敛到一张规范表」的前提相悖。）

import (
	"path/filepath"
	"strings"
)

// mimeExtEntry 一条「MIME ↔ 扩展名」规范记录。
//
// ★ 每条记录代表**一个扩展名的唯一规范 MIME** ⇒ ⇒ 反向索引可以由同一份
//
//	数据派生 ⇒ ⇒ MIME→ext 与 ext→MIME **不可能**互相矛盾。
type mimeExtEntry struct {
	Ext  string
	MIME string
}

// mimeExtTable 是 MIME ↔ 扩展名的**唯一事实源**（规范记录：★ 一个 MIME 一条 ★）。
//
// ★ 登记规则：★ **一个 MIME 只登记一条**（它的**规范扩展名**）⇒ ⇒
//
//	★ 反向索引**不可能**出现「同一个 MIME 指向两个扩展名」⇒ ⇒
//	★ 落盘文件名**与表里的顺序无关**（★ 本轮由钉测当场抓出过一次
//	★★ 「后写覆盖」⇒ ★★ .jpeg 盖掉 .jpg、.m4v 盖掉 .mp4 ★★ ⇒ ★★ 现改成一对一 ★★）★。
//
// ★ 覆盖：图片 / 文档 / 文本 / 压缩 / 音频 / 视频 ⇒ ★ 覆盖本仓附件域实际承载的形态。
var mimeExtTable = []mimeExtEntry{
	// ── 图片 ──
	{".jpg", "image/jpeg"},
	{".png", "image/png"},
	{".gif", "image/gif"},
	{".webp", "image/webp"},
	{".svg", "image/svg+xml"},
	{".bmp", "image/bmp"},
	{".tif", "image/tiff"},
	{".heic", "image/heic"},
	{".ico", "image/x-icon"},

	// ── 文档 ──
	{".pdf", "application/pdf"},
	{".doc", "application/msword"},
	{".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	{".xls", "application/vnd.ms-excel"},
	{".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	{".ppt", "application/vnd.ms-powerpoint"},
	{".pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	{".rtf", "application/rtf"},

	// ── 文本 / 数据 ──
	{".txt", "text/plain"},
	{".md", "text/markdown"},
	{".csv", "text/csv"},
	{".html", "text/html"},
	{".json", "application/json"},
	{".xml", "application/xml"},

	// ── 压缩 ──
	{".zip", "application/zip"},
	{".gz", "application/gzip"},
	{".tar", "application/x-tar"},
	{".rar", "application/vnd.rar"},

	// ── 音频 ──
	{".mp3", "audio/mpeg"},
	{".m4a", "audio/mp4"},
	{".wav", "audio/wav"},
	{".ogg", "audio/ogg"},
	{".flac", "audio/flac"},
	{".aac", "audio/aac"},
	{".opus", "audio/opus"},

	// ── 视频（★ 本轮重点：此前 video/mp4 未登记 ⇒ 落盘成 .bin ⇒ MIME 丢失 ★）──
	{".mp4", "video/mp4"},
	{".mov", "video/quicktime"},
	{".avi", "video/x-msvideo"},
	{".mkv", "video/x-matroska"},
	{".mpeg", "video/mpeg"},
	{".webm", "video/webm"},
	{".3gp", "video/3gpp"},

	// ── 兜底（★ 显式登记：让「未知类型」与「二进制兜底」可区分 ★）──
	{".bin", "application/octet-stream"},
}

// mimeExtEquivalents 是「同一 MIME 的**其他**扩展名」名单。
//
// ★ 它们**只进反向索引**（ext→MIME）⇒ ⇒ 落盘命名仍用规范扩展名
//
//	（★ .jpeg 的载荷落成 .jpg ★）⇒ ⇒ 但**取回/对外服务**时 .jpeg 也能
//	⇒ ⇒ 解析回 image/jpeg ⇒ ⇒ 历史 .jpeg 文件不受影响。
var mimeExtEquivalents = []mimeExtEntry{
	{".jpeg", "image/jpeg"},
	{".tiff", "image/tiff"},
	{".htm", "text/html"},
	{".m4v", "video/mp4"},
	{".mpg", "video/mpeg"},
}

// mimeAliases 是「非规范 MIME → 规范 MIME」的别名表。
//
// ★ 别名**不**新增扩展名，只把历史/方言写法**归一到规范 MIME** ⇒ ⇒
//
//	归一后再查表 ⇒ ⇒ 同一个 MIME 的别名与本体必然得到**同一个**扩展名。
var mimeAliases = map[string]string{
	"image/jpg":                    "image/jpeg",
	"image/pjpeg":                  "image/jpeg",
	"image/x-png":                  "image/png",
	"image/x-ms-bmp":               "image/bmp",
	"text/xml":                     "application/xml",
	"application/x-zip-compressed": "application/zip",
	"application/x-gzip":           "application/gzip",
	"application/x-tar":            "application/x-tar",
	"application/x-rar-compressed": "application/vnd.rar",
	"audio/x-wav":                  "audio/wav",
	"audio/vnd.wave":               "audio/wav",
	"audio/x-m4a":                  "audio/mp4",
	"video/x-m4v":                  "video/mp4",
}

// extToMIMEIndex 是 ext → 规范 MIME 的反向索引（规范表 + 等价扩展名单派生）。
var extToMIMEIndex = func() map[string]string {
	m := make(map[string]string, len(mimeExtTable)+len(mimeExtEquivalents))
	for _, e := range mimeExtTable {
		m[e.Ext] = e.MIME
	}
	for _, e := range mimeExtEquivalents {
		if _, exists := m[e.Ext]; !exists {
			m[e.Ext] = e.MIME
		}
	}
	return m
}()

// mimeToExtIndex 是 规范 MIME → ext 的索引（由 mimeExtTable 派生）。
//
// ★ 规范表里**一个 MIME 只有一条** ⇒ ⇒ 这个索引**不会有冲突** ⇒ ⇒
//
//	★ 落盘文件名与表里的顺序**无关**（★ 这正是本轮钉测抓出来的性质 ★）。
var mimeToExtIndex = func() map[string]string {
	m := make(map[string]string, len(mimeExtTable))
	for _, e := range mimeExtTable {
		m[e.MIME] = e.Ext
	}
	return m
}()

// canonicalMIME 去掉 MIME 的参数部分（★ 如 "text/plain; charset=utf-8" ★）
// 并把别名归一到规范 MIME。
func canonicalMIME(mimeType string) string {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	if canonical, ok := mimeAliases[mimeType]; ok {
		return canonical
	}
	return mimeType
}

// mimeTypeToExt 将 MIME 类型转换为文件扩展名。
//
// ★ 规范 MIME ⇒ 规范扩展名；★ 别名 ⇒ 先归一再查表 ⇒ ★ 得到与本体**同一个**
//
//	扩展名；★ 未登记 ⇒ ".bin"（★ 兜底，不是「猜一个」★）。
func mimeTypeToExt(mimeType string) string {
	mimeType = canonicalMIME(mimeType)
	if ext, ok := mimeToExtIndex[mimeType]; ok {
		return ext
	}
	return ".bin"
}

// extToMimeType 将文件扩展名转换为 MIME 类型。
//
// ★ 未登记 ⇒ "application/octet-stream"（★ 与扩展名列表保持同一兜底语义 ★）。
func extToMimeType(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if mime, ok := extToMIMEIndex[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

// detectContentType 由**路径**推断 MIME 类型（S3 后端 PutObject 用）。
//
// ★ 与落盘命名、取回 MIME **同源** ⇒ ⇒ 三个出口对同一个文件给出同一个类型。
func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "application/octet-stream"
	}
	return extToMimeType(ext)
}
