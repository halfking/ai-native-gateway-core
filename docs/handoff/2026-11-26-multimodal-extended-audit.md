# Multi-Modal Extended Audit: Attachments, Upstream Integration & Parameter Forwarding

**Date**: 2026-11-26  
**Auditor**: System Audit  
**Scope**: Extended multi-modal support covering attachment storage, upstream provider integration, and client-specific parameter forwarding

**Related Document**: [2026-11-26-multimodal-protocol-audit.md](./2026-11-26-multimodal-protocol-audit.md)

---

## Executive Summary

This extended audit examines three critical aspects of multi-modal request handling beyond basic protocol support:

1. **Attachment Storage & Lifecycle** - How base64-encoded media is extracted, persisted, and served
2. **Upstream Provider Integration** - Protocol conversion and media format bridging to upstream models
3. **Client Parameter Forwarding** - Preservation and routing of client-specific parameters

**Key Findings**:
✅ **Comprehensive attachment management** with multi-backend support (local/OSS/S3/Cloudreve)  
✅ **Content-based deduplication** via SHA256 hashing, reducing storage overhead  
✅ **IR-based protocol conversion** preserves multi-modal content across provider protocols  
✅ **Dialect-aware parameter forwarding** prevents parameter leakage to incompatible upstreams  
⚠️ **Historical coverage gap** - Document/video/audio support added 2026-11-11 (previously image-only)

---

## Part 1: Attachment Management

### 1.1 Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│ Request Flow: Base64 Attachment → Storage → URL Reference  │
└─────────────────────────────────────────────────────────────┘

Client Request (base64)
    ↓
┌───────────────────┐
│   Extractor       │ ← Scan messages[] for base64 media
│ (extractor.go)    │   • OpenAI: image_url, input_audio, video_url, file
└─────────┬─────────┘   • Anthropic: image (source.type=base64), document
          │             • Responses: input_image, input_audio, input_file
          ↓
┌───────────────────┐
│    Storage        │ ← Save to backend (SHA256 deduplication)
│ (storage_*.go)    │   • Local filesystem
└─────────┬─────────┘   • OSS (Aliyun)
          │             • S3-compatible
          │             • Cloudreve
          ↓
┌───────────────────┐
│  IRTransformer    │ ← Replace IR base64 → gateway URL
│(ir_transformer.go)│   • Enables upstream URL references
└─────────┬─────────┘   • Request-scoped deduplication
          │
          ↓
Upstream Forward (URL or original base64)
```

**Key Design Principle**: Attachment extraction is **best-effort** and **non-blocking**. Individual failures don't reject the request; original base64 is preserved as fallback.

---

### 1.2 Extraction Logic

#### 1.2.1 Extractor Interface

**Location**: `domains/attachments/extractor.go`

**Key Methods**:
- `ExtractFromOpenAIBody(requestID, bodyBytes)` - Scans OpenAI Chat Completions format
- `ExtractFromAnthropicBody(requestID, bodyBytes)` - Scans Anthropic Messages format
- `ExtractFromResponsesBody(requestID, bodyBytes)` - Scans OpenAI Responses API format

**Coverage** (as of 2026-11-21, N21-3):

| Media Type | OpenAI Format | Anthropic Format | Responses Format |
|------------|---------------|------------------|------------------|
| **Image** | `image_url` with `data:` URI | `image` + `source.type=base64` | `input_image` + `image_url` |
| **Audio** | `input_audio` + `{data, format}` | ⚠️ Passthrough | `input_audio` + `{data, format}` |
| **Video** | `video_url` with `data:` URI | ⚠️ Passthrough | ⚠️ Not supported |
| **Document** | `file` + `file_data` data URI | `document` + `source.type=base64` | `input_file` + `file_data` |

**Audio Data URI Normalization** (`extractor.go:155-169`):
```go
func audioDataURI(format, base64Data string) string {
    switch strings.ToLower(strings.TrimSpace(format)) {
    case "wav":
        return "data:audio/wav;base64," + base64Data
    case "mp3":
        return "data:audio/mpeg;base64," + base64Data  // Canonical MIME
    case "":
        return "data:application/octet-stream;base64," + base64Data
    default:
        return "data:audio/" + format + ";base64," + base64Data
    }
}
```

**Rationale**: OpenAI `input_audio.format` uses non-standard identifiers (`mp3` vs `audio/mpeg`). Extractor normalizes to canonical MIME types.

---

#### 1.2.2 Extraction Flow

**Entry Points** (all three protocol handlers):
- `messages.go:320-331` - Anthropic Messages handler
- `responses.go` - OpenAI Responses handler  
- `handler.go` - OpenAI Chat handler

**Integration Pattern**:
```go
if h.chatHandler.attachmentExtractor != nil {
    extractResult := h.chatHandler.attachmentExtractor.ExtractFromOpenAIBody(requestID, bodyBytes)
    if applyAttachmentResult(logCtx, extractResult) && attachmentStrictMode() {
        // Strict mode: attachment failure rejects request
        writeError(w, 503, "attachment storage failed")
        return
    }
}
```

**Two Modes**:
1. **Best-effort** (default): Extraction failures logged, request proceeds with original base64
2. **Strict** (`attachmentStrictMode()`): Extraction failure rejects request with 503

**Result Structure** (`extractor.go:45-54`):
```go
type ExtractResult struct {
    Attachments []AttachmentMetadata  // All detected, including failures
    TotalFound  int                   // Count of base64 blocks found
    Saved       int                   // Successfully persisted
    Failed      int                   // Storage failures
}
```

---

### 1.3 Storage Backend

#### 1.3.1 Storage Interface

**Location**: `domains/attachments/storage_manager.go`

**Key Method**:
```go
func (s *Storage) SaveBase64Image(requestID, dataURI string, msgIdx, blockIdx int) (*SaveResult, error)
```

**Input**: `data:image/png;base64,iVBORw0KGgo...`  
**Output**:
- Relative path: `2026/11/req_<reqID>/<hash>.png`
- Full metadata record

**Deduplication Strategy**:
1. Parse data URI → extract MIME type + base64 payload
2. Decode base64 → raw bytes
3. Compute SHA256 hash → content fingerprint
4. Check if file already exists at `<year>/<month>/req_<reqID>/<hash>.<ext>`
5. If exists: return existing metadata (`Deduped: true`)
6. If new: write file + return new metadata (`Deduped: false`)

**File Naming Convention**:
- Pattern: `YYYY/MM/req_<requestID>/<sha256-first-16-chars>.<ext>`
- Example: `2026/11/req_a1b2c3d4/4f8a9e3b2d1c0a5e.png`

**Why SHA256-based**:
- **Cross-request deduplication**: Same image in different requests → single file
- **Immutability**: Content hash as filename ensures content never changes
- **Cache-friendly**: Clients can cache forever (1 day private cache currently)

---

#### 1.3.2 Multi-Backend Support

**Backend Implementations**:

| Backend | File | Use Case | Status |
|---------|------|----------|--------|
| **Local** | `storage_backend_local.go` | Development, single-node | ✅ Production |
| **OSS** | `storage_backend_oss.go` | Aliyun OSS (CN deployments) | ✅ Production |
| **S3** | `storage_backend_s3_stub.go` | AWS S3 / MinIO | ⚠️ Stub only |
| **Cloudreve** | `storage_backend_cloudreve.go` | Third-party storage service | ✅ Available |

**Configuration** (`config.go`):
```go
type Config struct {
    Backend       string  // "local" | "oss" | "s3" | "cloudreve"
    BaseDir       string  // Filesystem base directory
    PublicURL     string  // Public attachment URL prefix
    MaxSizeBytes  int64   // Single file size limit
    
    // Backend-specific
    OSSEndpoint   string
    OSSBucket     string
    OSSAccessKey  string
    // ... (S3, Cloudreve configs)
}
```

**Local Backend Details** (`storage_backend_local.go`):
- Base directory (e.g., `/var/attachments`)
- Atomic write via `os.CreateTemp` + `os.Rename`
- Directory auto-creation with mode `0755`
- File permissions `0644`

**OSS Backend Details** (`storage_backend_oss.go`):
- Aliyun OSS SDK integration
- Presigned URL support (not currently used)
- Error mapping to gateway error codes

---

#### 1.3.3 Security Measures

**Download Handler** (`handler.go:49-131`):

1. **Path Traversal Protection**:
   - Only accepts paths relative to `BaseDir`
   - `..` sequences rejected by filepath normalization

2. **MIME-Based Content Disposition**:
   ```go
   if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
       w.Header().Set("Content-Disposition", 
           fmt.Sprintf("attachment; filename=%q", filename))
   }
   ```
   - **Images**: Inline display allowed
   - **SVG**: Force download (prevents XSS via SVG scripts)
   - **Non-images**: Force download

3. **X-Content-Type-Options: nosniff**:
   - Prevents browser MIME sniffing
   - Uploaded MIME type is client-controlled → must disable sniffing

4. **Tenant Isolation** (R35, 2026-09-17):
   ```go
   func (h *Handler) ListByRequest(w http.ResponseWriter, r *http.Request, 
       requestID string, tenantScope ...string)
   ```
   - Previously: Only filtered by `request_id` → cross-tenant metadata leakage
   - Now: Adds `AND tenant_id = $2` when `tenantScope` provided

5. **Authentication** (`auth.go`):
   - Pluggable authenticator interface
   - Modes: none / apikey / signed / admin
   - Gateway deployments should enable authentication for public attachments

---

### 1.4 IR Transformation (Base64 → URL)

#### 1.4.1 Purpose & Design

**Location**: `domains/attachments/ir_transformer.go`

**Goal**: Convert IR's base64-embedded attachments to gateway URL references **before** upstream serialization.

**Why This Matters**:
1. **Size Reduction**: Large base64 payloads don't repeat on retries
2. **Caching**: Upstream can cache URL-referenced media
3. **Provider Support**: Some providers prefer URLs over base64

**Key Innovation** (MM-1a, 2026-11-11):
> **Previously**: Only `Type == "image"` blocks were processed  
> **Issue**: Document/video/audio base64 payloads were **never stored or cached**  
> **Impact**: Large non-image media retransmitted in full on every request  
> **Fix**: Extend to **all four media types** (image/document/video/audio)

**Quote from `ir_transformer.go:24-34`**:
> ★★ 2026-11-11（附件域审计轮）：覆盖范围从「**只有 image**」扩到 **image / document / video / audio** 四类 ★★  
> 根因：★ 此前 TransformRequest 只处理 `block.Type == "image"` ⇒ ⇒ 文档（PDF）/ 视频 / 音频的 base64 载荷**完全没被落盘、也没被缓存** ⇒ ⇒ 它们每次请求都要**原样重传**（大载荷）

---

#### 1.4.2 Transformation Process

**Function**: `TransformRequest(ctx, requestID, req *ir.InternalRequest)`

**Algorithm**:
```
For each message in IR:
  For each content block in message:
    If block contains base64 media:
      1. Check request-scoped dedup map (payload → manifest)
      2. If found: Reuse existing manifest, update indices
      3. If new:
         a. Call Storage.SaveBase64Image(requestID, dataURI, msgIdx, blockIdx)
         b. If success:
            - Replace block: Type="base64" → Type="url"
            - Set URL to gateway public URL
            - Clear Data field
            - Cache manifest in dedup map
         c. If failure:
            - Log warning
            - Keep original base64 (fallback)
            - Record in result.Failures
```

**Supported IR Block Types** (`ir_transformer.go:91-141`):

| IR Block Type | Source Field | Target Field | MIME Fallback |
|---------------|--------------|--------------|---------------|
| `image` | `Image.Data` (base64) | `Image.URL` | `image/png` |
| `document` | `Document.Source.Data` | `Document.Source.URL` | `application/pdf` |
| `video` | `Video.Data` | `Video.URL` | `video/mp4` |
| `audio` | `Audio.Data` | `Audio.URL` | `audio/wav` |

**Example Transformation**:

**Before**:
```json
{
  "type": "image",
  "image": {
    "type": "base64",
    "media_type": "image/png",
    "data": "iVBORw0KGgo..."  // 500KB
  }
}
```

**After**:
```json
{
  "type": "image",
  "image": {
    "type": "url",
    "url": "https://gateway.example.com/api/attachments/2026/11/req_abc/4f8a9e3b.png",
    "media_type": "image/png",
    "data": ""
  }
}
```

---

#### 1.4.3 Deduplication Layers

**Two-Level Deduplication**:

1. **Request-Scoped** (In-memory map, `ir_transformer.go:85`):
   ```go
   manifestsByData := make(map[string]AttachmentMetadata)
   key := kind + "\x00" + data
   if prev, ok := manifestsByData[key]; ok {
       // Same payload within request → reuse manifest
       result.Deduped++
       return
   }
   ```
   - **Scope**: Single request
   - **Benefit**: Avoids re-decoding/hashing identical payloads in one request
   - **Example**: User sends 10 messages with same profile picture

2. **Storage-Level** (SHA256 filesystem, `storage_manager.go`):
   ```go
   hash := sha256.Sum256(decoded)
   filename := fmt.Sprintf("%s.%s", hex.EncodeToString(hash[:8]), ext)
   ```
   - **Scope**: All requests (cross-request, cross-tenant)
   - **Benefit**: Popular images stored once
   - **Example**: Company logo used by multiple users

**Result Tracking**:
```go
type TransformResult struct {
    Stored  int  // New files written this request
    Deduped int  // Files reused (request-scoped + storage-level)
    Failed  int  // Storage errors
}
```

---

### 1.5 Historical Issues & Fixes

#### Issue 1: Document/Video/Audio Not Stored (2026-11-11)

**Problem**: `IRTransformer.TransformRequest()` only checked `block.Type == "image"`.

**Impact**:
- PDFs, videos, audio → base64 never extracted
- No deduplication benefit for non-image media
- Large payloads retransmitted on every request
- Storage infrastructure unused for 75% of media types

**Fix**: Extended `switch` statement to handle all four types (`ir_transformer.go:91-141`).

**Test Coverage**: Added `TestIRTransformer_DocumentVideoAudio_AreStoredAndReplaced`.

---

#### Issue 2: Type Mislabeling (2026-11-11)

**Problem**: `Storage.SaveBase64Image()` hardcoded `Type: "image"` in success metadata.

**Code Path**:
```go
// storage_manager.go (pseudo)
func SaveBase64Image(...) (*SaveResult, error) {
    // ...
    return &SaveResult{
        Metadata: AttachmentMetadata{
            Type: "image",  // ← Hardcoded!
            Path: relPath,
        },
    }, nil
}
```

**Impact**:
- Video/audio/document attachments labeled as `Type: "image"` in logs
- Attachment queries misclassified media types
- Broken analytics/reporting

**Fix** (`ir_transformer.go:203-209`):
```go
meta := res.Metadata
// ★ 落盘记录的类型按**附件种类**标注（★ 与 Extractor 同口径 ★）。
// ★ SaveBase64Image 成功路径硬编码 Type:"image"（历史图片专用 API）⇒
//   ★ 必须**无条件覆盖** ★
meta.Type = kind  // Overwrite with actual type
```

**Caught By**: `TestIRTransformer_DocumentVideoAudio_AreStoredAndReplaced` - asserted metadata type correctness.

---

### 1.6 Attachment Lifecycle

#### 1.6.1 Creation

**Trigger**: First request containing base64 media arrives  
**Actor**: `Extractor` → `Storage.SaveBase64Image()`  
**Output**: File on disk/OSS + metadata in `request_logs.attachments` JSONB column

**Metadata Schema** (`storage_manager.go`):
```go
type AttachmentMetadata struct {
    Type         string    // "image" | "audio" | "video" | "file"
    Path         string    // Relative path from BaseDir
    OriginalURL  string    // Truncated data URI (for audit)
    Hash         string    // SHA256 hex (first 16 chars)
    SizeBytes    int64     // Raw file size
    MediaType    string    // MIME type
    MessageIndex int       // Position in messages[]
    BlockIndex   int       // Position in content[]
    CreatedAt    time.Time
    Status       string    // "uploaded" | "store_failed"
    ErrorCode    string    // Set when Status=store_failed
}
```

---

#### 1.6.2 Retrieval

**Two Endpoints**:

1. **Download Single Attachment**:
   ```
   GET /api/attachments/2026/11/req_abc/4f8a9e3b.png
   ```
   - Streams file content (via `io.Copy`)
   - Sets appropriate `Content-Type` and `Content-Disposition`
   - 1-day private cache (`Cache-Control: private, max-age=86400`)

2. **List Request Attachments**:
   ```
   GET /api/logs/{request_id}/attachments
   ```
   - Returns JSONB array from `request_logs.attachments`
   - **Tenant-scoped** (R35 fix): `WHERE request_id = $1 AND tenant_id = $2`
   - Used by admin UI for attachment inspection

---

#### 1.6.3 Cleanup

**Current State**: ⚠️ **No automatic cleanup implemented**

**Storage Accumulation**:
- Files persist indefinitely after creation
- SHA256 deduplication prevents exponential growth
- But unique media still accumulates over time

**Recommended Cleanup Strategy** (not implemented):
1. **Age-based**: Delete attachments older than N days
2. **Request-linked**: Delete when parent `request_logs` row is archived/deleted
3. **Reference counting**: Track usage, delete when unreferenced

**Manual Cleanup**:
- Administrators must clean `<BaseDir>` manually
- Safe deletion criteria: Files not referenced in recent request logs

---

## Part 2: Upstream Provider Integration

### 2.1 IR (Internal Request) Architecture

**Purpose**: Unified intermediate representation for cross-protocol request handling.

```
┌──────────────────────────────────────────────────────┐
│          Protocol-Agnostic Processing Flow           │
└──────────────────────────────────────────────────────┘

Client Protocol                  Internal                  Upstream Protocol
     (入)                      Representation                   (出)
      ↓                             ↓                            ↓
┌─────────────┐              ┌──────────┐              ┌──────────────┐
│OpenAI Chat  │──Parse─────→ │          │              │ OpenAI Chat  │
└─────────────┘              │          │──Serialize──→│ (native)     │
                             │          │              └──────────────┘
┌─────────────┐              │          │              ┌──────────────┐
│Anthropic    │──Parse─────→ │    IR    │──Serialize──→│ Anthropic    │
│Messages     │              │          │              │ (converted)  │
└─────────────┘              │          │              └──────────────┘
                             │          │              ┌──────────────┐
┌─────────────┐              │          │──Serialize──→│ Gemini       │
│OpenAI       │──Parse─────→ │          │              │ (converted)  │
│Responses    │              └──────────┘              └──────────────┘
└─────────────┘
```

**Key Insight**: IR decouples **client protocol** from **upstream protocol**, enabling:
- OpenAI client → Anthropic Claude upstream
- Anthropic client → OpenAI-compatible upstream (DeepSeek, Qwen, etc.)
- Responses API client → Any upstream

---

### 2.2 Multi-Modal Content Representation in IR

**Location**: `internal/ir/types.go` (inferred structure)

**Content Block Types**:

```go
type ContentBlock struct {
    Type     string        // "text" | "image" | "audio" | "video" | "document" | "tool_use" | "tool_result"
    
    // Text content
    Text     string
    
    // Media sources (mutually exclusive)
    Image    *ImageSource
    Audio    *MediaSource
    Video    *MediaSource
    Document *DocumentSource
    
    // Tool invocation
    ToolUse    *ToolUseBlock
    ToolResult *ToolResultBlock
}

type ImageSource struct {
    Type      string  // "url" | "base64"
    URL       string  // HTTP URL or gateway attachment URL
    Data      string  // Base64-encoded data (empty after IR transformation)
    MediaType string  // MIME type
}

type MediaSource struct {
    Type      string  // "url" | "base64"
    URL       string
    Data      string
    MediaType string
    Format    string  // Format hint (e.g., "wav", "mp4")
}

type DocumentSource struct {
    Source *ImageSource  // Reuses ImageSource structure
    Name   string        // Filename
}
```

**Design Rationale**:
- **Unified structure** across protocols
- **Multiple source types** (URL/base64) allows flexible serialization
- **Media-agnostic** `MediaSource` for audio/video (share same wire format)

---

### 2.3 Protocol Conversion: Multi-Modal Content

#### 2.3.1 Parsing (Protocol → IR)

**OpenAI Chat → IR** (`internal/ir/parse_openai.go`):

| OpenAI Block | IR Mapping | Notes |
|--------------|------------|-------|
| `{"type":"text","text":"..."}` | `ContentBlock{Type:"text", Text:"..."}` | Direct |
| `{"type":"image_url","image_url":{"url":"..."}}` | `ContentBlock{Type:"image", Image:{Type:"url", URL:"..."}}` | URL extraction |
| `{"type":"image_url","image_url":{"url":"data:..."}}` | `ContentBlock{Type:"image", Image:{Type:"base64", Data:"...", MediaType:"..."}}` | Data URI parsing |
| `{"type":"input_audio","input_audio":{"data":"...","format":"wav"}}` | `ContentBlock{Type:"audio", Audio:{Type:"base64", Data:"...", Format:"wav"}}` | Audio specific |
| `{"type":"video_url","video_url":{"url":"..."}}` | `ContentBlock{Type:"video", Video:{Type:"url", URL:"..."}}` | URL reference |
| `{"type":"file","file":{"file_data":"data:..."}}` | `ContentBlock{Type:"document", Document:{Source:{Type:"base64", Data:"..."}}}` | File/document |

**Anthropic Messages → IR** (`internal/ir/parse_anthropic.go`):

| Anthropic Block | IR Mapping | Notes |
|-----------------|------------|-------|
| `{"type":"text","text":"..."}` | `ContentBlock{Type:"text", Text:"..."}` | Direct |
| `{"type":"image","source":{"type":"url","url":"..."}}` | `ContentBlock{Type:"image", Image:{Type:"url", URL:"..."}}` | Direct URL |
| `{"type":"image","source":{"type":"base64","data":"...","media_type":"..."}}` | `ContentBlock{Type:"image", Image:{Type:"base64", Data:"...", MediaType:"..."}}` | Base64 inline |
| `{"type":"document","source":{"type":"base64",...}}` | `ContentBlock{Type:"document", Document:{Source:{Type:"base64",...}}}` | PDF/doc support |

---

#### 2.3.2 Serialization (IR → Protocol)

**IR → OpenAI Chat** (`internal/ir/serialize_openai.go`):

```go
func serializeOpenAIMessage(block *ContentBlock) map[string]any {
    switch block.Type {
    case "text":
        return map[string]any{"type": "text", "text": block.Text}
    
    case "image":
        if block.Image.Type == "url" {
            return map[string]any{
                "type": "image_url",
                "image_url": map[string]any{"url": block.Image.URL},
            }
        }
        // base64 → construct data URI
        dataURI := fmt.Sprintf("data:%s;base64,%s", 
            block.Image.MediaType, block.Image.Data)
        return map[string]any{
            "type": "image_url",
            "image_url": map[string]any{"url": dataURI},
        }
    
    case "audio":
        return map[string]any{
            "type": "input_audio",
            "input_audio": map[string]any{
                "data": block.Audio.Data,
                "format": block.Audio.Format,
            },
        }
    
    case "video":
        // OpenAI video blocks use video_url.url
        return map[string]any{
            "type": "video_url",
            "video_url": map[string]any{"url": block.Video.URL},
        }
    
    case "document":
        // OpenAI file blocks use file.file_data
        dataURI := fmt.Sprintf("data:%s;base64,%s",
            block.Document.Source.MediaType, block.Document.Source.Data)
        return map[string]any{
            "type": "file",
            "file": map[string]any{"file_data": dataURI},
        }
    }
}
```

**IR → Anthropic Messages** (`internal/ir/serialize_anthropic.go`):

```go
func serializeAnthropicContentBlock(block *ContentBlock) map[string]any {
    switch block.Type {
    case "text":
        return map[string]any{"type": "text", "text": block.Text}
    
    case "image":
        if block.Image.Type == "url" {
            return map[string]any{
                "type": "image",
                "source": map[string]any{
                    "type": "url",
                    "url": block.Image.URL,
                },
            }
        }
        return map[string]any{
            "type": "image",
            "source": map[string]any{
                "type": "base64",
                "media_type": block.Image.MediaType,
                "data": block.Image.Data,
            },
        }
    
    case "document":
        return map[string]any{
            "type": "document",
            "source": map[string]any{
                "type": "base64",
                "media_type": block.Document.Source.MediaType,
                "data": block.Document.Source.Data,
            },
        }
    
    case "audio":
        // Anthropic has no native audio block → passthrough or convert to text
        // Current implementation: passthrough as unknown block type
        return map[string]any{
            "type": "audio",  // Non-standard, provider may reject
            "data": block.Audio.Data,
        }
    }
}
```

---

#### 2.3.3 Cross-Protocol Multi-Modal Examples

**Example 1: OpenAI Client → Claude Upstream (Image)**

**Client Request** (OpenAI Chat):
```json
{
  "model": "gpt-4o",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "What's in this image?"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0..."}}
    ]
  }]
}
```

**IR Representation**:
```json
{
  "Model": "gpt-4o",
  "Messages": [{
    "Role": "user",
    "Content": [
      {"Type": "text", "Text": "What's in this image?"},
      {
        "Type": "image",
        "Image": {
          "Type": "base64",
          "MediaType": "image/png",
          "Data": "iVBORw0..."
        }
      }
    ]
  }]
}
```

**After IR Transformation** (if attachment storage enabled):
```json
{
  "Type": "image",
  "Image": {
    "Type": "url",
    "URL": "https://gateway/api/attachments/2026/11/req_abc/4f8a9e3b.png",
    "MediaType": "image/png",
    "Data": ""  // Cleared
  }
}
```

**Upstream Request** (Anthropic Messages):
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "What's in this image?"},
      {
        "type": "image",
        "source": {
          "type": "url",
          "url": "https://gateway/api/attachments/2026/11/req_abc/4f8a9e3b.png"
        }
      }
    ]
  }]
}
```

**Key Transformations**:
1. ✅ OpenAI `image_url` → Anthropic `image` + `source`
2. ✅ Base64 inline → Gateway URL reference (via IRTransformer)
3. ✅ MIME type preserved in IR metadata

---

**Example 2: Anthropic Client → OpenAI Upstream (Document)**

**Client Request** (Anthropic Messages):
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "Summarize this PDF"},
      {
        "type": "document",
        "source": {
          "type": "base64",
          "media_type": "application/pdf",
          "data": "JVBERi0xLjQK..."
        }
      }
    ]
  }]
}
```

**IR Representation**:
```json
{
  "Messages": [{
    "Role": "user",
    "Content": [
      {"Type": "text", "Text": "Summarize this PDF"},
      {
        "Type": "document",
        "Document": {
          "Source": {
            "Type": "base64",
            "MediaType": "application/pdf",
            "Data": "JVBERi0xLjQK..."
          }
        }
      }
    ]
  }]
}
```

**Upstream Request** (OpenAI Chat):
```json
{
  "model": "gpt-4-turbo",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "Summarize this PDF"},
      {
        "type": "file",
        "file": {
          "file_data": "data:application/pdf;base64,JVBERi0xLjQK..."
        }
      }
    ]
  }]
}
```

**Key Transformations**:
1. ✅ Anthropic `document` → OpenAI `file` block
2. ✅ Anthropic `source.type=base64` → OpenAI `file.file_data` data URI
3. ✅ MIME type reconstructed as data URI prefix

---

### 2.4 Provider-Specific Handling

#### 2.4.1 Gemini Native Format

**Special Case**: Gemini uses a different structure than OpenAI/Anthropic.

**Gemini Block Structure**:
```json
{
  "contents": [{
    "role": "user",
    "parts": [
      {"text": "Hello"},
      {
        "inlineData": {
          "mimeType": "image/png",
          "data": "iVBORw0..."
        }
      }
    ]
  }]
}
```

**IR → Gemini** (`internal/ir/serialize_gemini.go`):
- `ContentBlock{Type:"image"}` → Gemini `inlineData` or `fileData`
- MIME type from `Image.MediaType` → `mimeType` field
- Base64 from `Image.Data` → `data` field

**Gemini → IR** (`internal/ir/parse_gemini.go`):
- `inlineData` → `ContentBlock{Type:"image", Image:{Type:"base64", ...}}`
- MIME prefix detection: `image/*` → vision, `audio/*` → audio, `video/*` → video

---

#### 2.4.2 Provider Capability Negotiation

**Question**: What if upstream doesn't support a media type?

**Current Behavior**: **Best-effort passthrough**
- Gateway sends the block as-is
- Upstream may:
  - **Accept**: Process the media (ideal)
  - **Ignore**: Skip the block silently
  - **Reject**: Return 400 error

**Modality Mismatch Detection** (covered in main audit):
- `detectRequestModality()` identifies media types in request
- Routing filters by provider modality capability
- Zero candidates → 422 "Model 'X' does not accept [vision/audio/video] input"

**Provider Capability Registry** (`provider/modality_accept.go`):
```go
func ModalityAccepts(providerModality, requestModality string) bool {
    switch requestModality {
    case "text":
        return true  // All providers accept text
    case "vision":
        return providerModality == "vision" || providerModality == "multimodal"
    case "audio":
        return providerModality == "audio" || providerModality == "multimodal"
    case "video":
        return providerModality == "video" || providerModality == "multimodal"
    default:
        return false
    }
}
```

---

## Part 3: Client-Specific Parameter Forwarding

### 3.1 Parameter Registry System

**Location**: `internal/paramreg/` (parameter registry)

**Purpose**: Define which client-specific parameters can be forwarded to which upstream providers.

**Key Concept**: **Dialect-Aware Parameter Forwarding**

Not all parameters are universal:
- `frequency_penalty` - OpenAI-specific, not understood by Anthropic
- `thinking` - Anthropic native, not understood by standard OpenAI
- `top_k` - Anthropic/Gemini, but also supported by many OpenAI-compatible providers

**Problem**: Sending unknown parameters to strict upstreams causes 400 errors.

**Solution**: Parameter registry with dialect annotations.

---

### 3.2 Parameter Classification

**Three Categories** (R52 audit, 2026-09-21):

#### 3.2.1 Portable Parameters

**Definition**: Supported by multiple providers across protocol families.

**Examples**:
- `temperature`, `top_p` - Universal sampling parameters
- `max_tokens` - Universal completion length limit
- `stop` - Stop sequences (universal)
- `repetition_penalty` - Supported by most Chinese LLM providers + vLLM

**Forwarding**: ✅ **Always forward** (all dialects)

---

#### 3.2.2 Dialect-Specific Parameters

**Definition**: Only understood by certain provider families.

**Examples**:

| Parameter | Dialects | Purpose |
|-----------|----------|---------|
| `frequency_penalty` | OpenAI | Penalize frequent tokens |
| `presence_penalty` | OpenAI | Penalize any repeated token |
| `reasoning_effort` | OpenAI | Thinking budget (o1/o3 models) |
| `thinking` | Anthropic | Native thinking config (Claude 3.7+) |
| `top_k` | Anthropic, Gemini, GLM, Qwen, DeepSeek, vLLM | Top-k sampling |
| `mask_sensitive_info` | MiniMax | PII redaction |
| `bot_setting` | MiniMax | Multi-bot configuration |

**Forwarding**: ⚠️ **Conditional** - only to compatible dialects

**Implementation** (`internal/ir/serialize_openai.go:174-199`):
```go
dstDialect := resolveTargetDialect(req, ProtocolOpenAIChat)

if req.TopK != nil && paramreg.IRFieldAllowedForDialect("top_k", dstDialect) {
    out["top_k"] = *req.TopK
}

if req.MaskSensitiveInfo != nil && paramreg.IRFieldAllowedForDialect("mask_sensitive_info", dstDialect) {
    out["mask_sensitive_info"] = *req.MaskSensitiveInfo
}
```

**Dialect Resolution**:
```go
func resolveTargetDialect(req *InternalRequest, protocol string) string {
    if req.TargetProvider != "" {
        // Known providers map to dialects
        switch req.TargetProvider {
        case "minimax":
            return "minimax"
        case "anthropic":
            return "anthropic"
        case "google":
            return "gemini"
        default:
            return "openai"  // Most openai-chat providers
        }
    }
    return "unknown"  // Fail-open for unknown
}
```

---

#### 3.2.3 Extensions (Unknown Parameters)

**Definition**: Parameters not recognized by IR parser.

**Handling**: Stored in `req.Extensions` map, restored during serialization.

**Example Flow**:

**Client Request**:
```json
{
  "model": "gpt-4o",
  "temperature": 0.7,
  "custom_provider_flag": true,  // Unknown parameter
  "messages": [...]
}
```

**IR Parsing**:
```go
// Parse known fields
req.Temperature = 0.7

// Store unknown fields
req.Extensions = map[string]any{
    "custom_provider_flag": true,
}
```

**Serialization**:
```go
out := map[string]any{
    "model": req.Model,
    "temperature": req.Temperature,
}

// Restore extensions
for k, v := range req.Extensions {
    out[k] = v
}
```

**Result**:
```json
{
  "model": "gpt-4o",
  "temperature": 0.7,
  "custom_provider_flag": true  // Forwarded as-is
}
```

**Rationale**: Gateway doesn't understand all provider-specific features. Extensions allow pass-through for custom parameters.

---

### 3.3 Thinking/Reasoning Parameter Handling

**Complex Case**: Multiple ways to express "extended thinking"

**Three Input Forms**:

1. **OpenAI `reasoning_effort`** (o1/o3 models):
   ```json
   {"reasoning_effort": "high"}
   ```

2. **Anthropic `thinking`** (Claude 3.7+):
   ```json
   {"thinking": {"type": "enabled", "budget_tokens": 5000}}
   ```

3. **Gateway-unified `reasoning`** (cross-provider):
   ```json
   {"reasoning": {"effort": "high", "budget_tokens": 5000}}
   ```

**IR Representation**:
```go
type InternalRequest struct {
    // Native Anthropic thinking
    Thinking *ThinkingConfig
    
    // Unified reasoning config
    Reasoning *ReasoningConfig
}

type ThinkingConfig struct {
    Type         string  // "enabled" | "disabled" | "adaptive"
    BudgetTokens *int    // Token budget for thinking
}

type ReasoningConfig struct {
    Effort       string  // "low" | "medium" | "high"
    BudgetTokens *int
}
```

**Serialization Priority** (`serialize_anthropic.go:124-150`):

```
Priority 1: Native `thinking` field (if set)
  → Emit as-is to Anthropic upstream
  → Convert to `reasoning_effort` for OpenAI upstream

Priority 2: Unified `reasoning` field (if `thinking` not set)
  → Convert to Anthropic `thinking` object
  → Convert to OpenAI `reasoning_effort`
```

**Example: Cross-Provider Reasoning**:

**Client Request** (OpenAI format with reasoning):
```json
{
  "model": "gpt-4o",
  "reasoning": {"effort": "high"},
  "messages": [...]
}
```

**Routed to Claude**:
```json
{
  "model": "claude-3-7-sonnet-20250219",
  "thinking": {
    "type": "enabled",
    "budget_tokens": 8000  // Mapped from "high" effort
  },
  "messages": [...]
}
```

**Routed to DeepSeek**:
```json
{
  "model": "deepseek-reasoner",
  "reasoning_effort": "high",  // OpenAI format
  "messages": [...]
}
```

**Budget Token Mapping** (`internal/reasoningdialect/`):
```go
var effortBudgets = map[string]int{
    "low":    2000,
    "medium": 5000,
    "high":   8000,
}
```

**Max Tokens Clamp** (Anthropic-specific, 2026-10-01 audit):
```go
// Anthropic: thinking budget cannot exceed max_tokens
if budgetTokens > finalMaxTokens {
    budgetTokens = finalMaxTokens
}
```

---

### 3.4 Parameter Preservation Across Conversions

**Challenge**: Client uses Protocol A, upstream uses Protocol B. How to preserve client parameters?

**Strategy**: **IR as parameter carrier**

**Example: OpenAI Client Parameters → Anthropic Upstream**

**Client Request** (OpenAI):
```json
{
  "model": "gpt-4o",
  "temperature": 0.7,
  "top_p": 0.9,
  "frequency_penalty": 0.5,  // OpenAI-specific
  "user": "user-123",
  "messages": [...]
}
```

**IR**:
```go
InternalRequest{
    Model:            "gpt-4o",
    Temperature:      ptr(0.7),
    TopP:             ptr(0.9),
    FrequencyPenalty: ptr(0.5),  // Stored but...
    User:             "user-123",
    Messages:         [...]
}
```

**Anthropic Serialization**:
```go
out := map[string]any{
    "model": "claude-3-5-sonnet-20241022",
    "temperature": 0.7,  // ✅ Supported
    "top_p": 0.9,        // ✅ Supported
    // frequency_penalty: SKIPPED (not in Anthropic spec)
    "metadata": map[string]any{
        "user_id": "user-123",  // ✅ Converted to Anthropic format
    },
}
```

**Result**: Universal parameters forwarded, protocol-specific parameters gracefully dropped.

---

### 3.5 Historical Issues & Improvements

#### Issue 1: `top_k` Never Forwarded to OpenAI Upstreams (2026-10-13)

**Problem**: IR parser consumed `top_k` from Anthropic/Gemini requests, but `SerializeOpenAI()` never emitted it.

**Impact**:
- Anthropic → GLM/Qwen/DeepSeek: `top_k` silently dropped
- Gemini → OpenAI-compatible: `top_k` silently dropped
- Cross-format matrix completeness broken

**Root Cause**:
> Code comment: "top_k is Anthropic-only" (outdated assumption)

**Reality**: Many OpenAI-compatible providers support `top_k`:
- GLM (zhipuai)
- Qwen (dashscope)
- DeepSeek
- vLLM
- OpenRouter

**Fix** (`serialize_openai.go:184-186`):
```go
if req.TopK != nil && paramreg.IRFieldAllowedForDialect("top_k", dstDialect) {
    out["top_k"] = *req.TopK
}
```

---

#### Issue 2: MiniMax Parameters Sent to All OpenAI Upstreams (R52, 2026-09-21)

**Problem**: `mask_sensitive_info` and `bot_setting` were IR first-class fields but emitted unconditionally.

**Impact**:
- Sent to strict OpenAI → 400 `additionalProperties: false`
- Parameter leakage to unintended providers

**Fix**: Dialect gating (`serialize_openai.go:187-199`):
```go
dstDialect := resolveTargetDialect(req, ProtocolOpenAIChat)

if req.MaskSensitiveInfo != nil && 
   paramreg.IRFieldAllowedForDialect("mask_sensitive_info", dstDialect) {
    out["mask_sensitive_info"] = *req.MaskSensitiveInfo
}
```

**Parameter Registry** (`internal/paramreg/`):
```go
RegisterParam("mask_sensitive_info", ParamSpec{
    Dialects:  []string{"minimax"},  // Only for MiniMax
    IRHandled: true,                 // Stored in IR (not Extensions)
})
```

---

## Part 4: Integration Testing & Validation

### 4.1 Test Coverage

#### Attachment Tests:
- ✅ Extraction from all three protocols (OpenAI/Anthropic/Responses)
- ✅ Multi-backend storage (local/OSS stub)
- ✅ Deduplication (request-scoped + storage-level)
- ✅ IR transformation (base64 → URL)
- ✅ All four media types (image/document/video/audio)
- ✅ Metadata type correctness (caught type mislabeling bug)

#### Protocol Conversion Tests:
- ✅ OpenAI → Anthropic (images, tools, thinking blocks)
- ✅ Anthropic → OpenAI (images, documents, tool results)
- ✅ Responses → Any (input[] array handling)
- ✅ Gemini native format (inlineData/fileData)
- ✅ Empty/null content handling
- ✅ Orphaned tool_result downgrade (2026-11-13 fix)

#### Parameter Forwarding Tests:
- ✅ Portable parameters (temperature, top_p) always forward
- ✅ Dialect-specific parameters (top_k, mask_sensitive_info) gated
- ✅ Extensions pass-through for unknown parameters
- ✅ Reasoning/thinking cross-provider conversion
- ⚠️ **Gap**: No end-to-end tests for all parameter combinations

---

### 4.2 Real-World Validation

#### Production Evidence (from code comments):

1. **Attachment Storage** (2026-11-11):
   - Document/video/audio previously never stored
   - Now verified storing all four types

2. **Cross-Format Matrix** (2026-10-13):
   - `top_k` now forwards from Anthropic → GLM/Qwen
   - Validated in cross-format tests

3. **Tool Call Integrity** (2026-11-13, request `75777ea4`):
   - 80 orphaned `tool_result` blocks downgraded to text
   - Session brick prevented (vs. outright rejection)

4. **Modality Mismatch** (2026-10-28, production issue):
   - glm-5.3-flash received image block → upstream 400
   - Gateway logged as 503 "All providers unavailable" (misleading)
   - Fix: Proper 422 with "Model does not accept image input"

---

## Part 5: Recommendations

### 5.1 Attachment Management

#### Recommendation 1: Implement Attachment Lifecycle Policy ⚠️

**Current State**: Attachments never deleted, accumulate indefinitely.

**Proposal**:
```yaml
attachment_lifecycle:
  retention_days: 30  # Delete after 30 days
  cleanup_schedule: "0 2 * * *"  # Daily at 2 AM
  reference_check: true  # Verify no active requests reference file
```

**Implementation**:
- Periodic cleanup job scans `<BaseDir>/<YYYY>/<MM>/`
- For each file: Check if referenced in `request_logs.attachments` from last N days
- If unreferenced + age > retention: Delete
- Track cleanup metrics (files deleted, bytes freed)

---

#### Recommendation 2: Add Attachment Metadata Indexing

**Current State**: Attachments stored in JSONB `request_logs.attachments[]` - no direct queries.

**Proposal**: Add indexed attachment metadata table:
```sql
CREATE TABLE attachment_registry (
    hash         TEXT PRIMARY KEY,
    path         TEXT NOT NULL,
    media_type   TEXT,
    size_bytes   BIGINT,
    created_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    ref_count    INTEGER  -- How many requests reference it
);

CREATE INDEX idx_attachment_last_used ON attachment_registry(last_used_at);
```

**Benefits**:
- Fast cleanup: Query `WHERE last_used_at < NOW() - INTERVAL '30 days'`
- Usage analytics: Most popular attachments, storage growth trends
- Orphan detection: `ref_count = 0` indicates unreferenced files

---

#### Recommendation 3: Support CDN Integration

**Current State**: Attachments served directly from gateway (or OSS).

**Proposal**: Add CDN URL rewriting:
```go
type Config struct {
    // ...
    CDNPrefix string  // e.g., "https://cdn.example.com"
}

func (t *IRTransformer) AttachmentURL(relPath string) string {
    if t.cdnPrefix != "" {
        return t.cdnPrefix + "/" + relPath
    }
    return t.publicBaseURL + "/" + relPath
}
```

**Benefits**:
- Reduced gateway load (CDN handles downloads)
- Geographic distribution (lower latency)
- Bandwidth cost optimization

---

### 5.2 Protocol Conversion

#### Recommendation 4: Add Provider Capability Validation

**Current State**: Protocol conversion is best-effort; upstream may reject unsupported blocks.

**Proposal**: Provider capability manifest:
```yaml
providers:
  anthropic/claude-3-5-sonnet:
    supported_modalities: [text, image, document]
    supported_blocks: [text, image, document, tool_use, tool_result, thinking]
    
  openai/gpt-4o:
    supported_modalities: [text, image, audio]
    supported_blocks: [text, image_url, input_audio, tool_calls]
```

**Validation**:
```go
func ValidateIRForProvider(req *InternalRequest, provider string) error {
    caps := GetProviderCapabilities(provider)
    for _, msg := range req.Messages {
        for _, block := range msg.Content {
            if !caps.SupportsBlock(block.Type) {
                return fmt.Errorf("provider %s does not support %s blocks", 
                    provider, block.Type)
            }
        }
    }
    return nil
}
```

**Benefit**: Catch incompatibilities before forwarding, provide clear error messages.

---

#### Recommendation 5: Extend IR to Support More Media Attributes

**Current Gap**: IR lacks metadata fields that some providers support:

| Attribute | Use Case | Provider |
|-----------|----------|----------|
| Image detail level | High-res vs low-res | OpenAI |
| Audio transcription | Pre-transcribed text | OpenAI |
| Video timestamps | Specific frame references | Future providers |

**Proposal**: Extend IR structures:
```go
type ImageSource struct {
    Type      string
    URL       string
    Data      string
    MediaType string
    Detail    string  // "low" | "high" | "auto" (OpenAI)
}

type MediaSource struct {
    Type         string
    URL          string
    Data         string
    MediaType    string
    Format       string
    Transcription string  // Optional text transcription
}
```

---

### 5.3 Parameter Forwarding

#### Recommendation 6: Publish Parameter Compatibility Matrix

**Current State**: Parameter dialect support is code-only (in `paramreg` and serializers).

**Proposal**: Generate public documentation:
```markdown
# Parameter Compatibility Matrix

| Parameter | OpenAI | Anthropic | Gemini | GLM | Qwen | DeepSeek |
|-----------|--------|-----------|--------|-----|------|----------|
| temperature | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| top_p | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| top_k | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ |
| frequency_penalty | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| thinking | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ |
| reasoning_effort | ✅ | ➡️ | ❌ | ➡️ | ❌ | ➡️ |

Legend:
- ✅ Native support
- ➡️ Gateway converts from unified format
- ❌ Not supported (parameter dropped)
```

**Generation**: Extract from `paramreg` registry + serializer code.

---

#### Recommendation 7: Add Parameter Conversion Logging

**Current State**: Parameter drops are silent.

**Proposal**: Log parameter transformations:
```go
type ParameterAudit struct {
    RequestID       string
    ClientProtocol  string
    UpstreamDialect string
    Preserved       []string  // Parameters forwarded as-is
    Converted       []ConversionRecord
    Dropped         []string  // Parameters silently dropped
}

type ConversionRecord struct {
    From      string  // "reasoning.effort=high"
    To        string  // "thinking.budget_tokens=8000"
    Rationale string  // "Cross-provider reasoning mapping"
}
```

**Benefit**: 
- Debugging: "Why didn't my `frequency_penalty` work?"
- Analytics: Which parameters are commonly dropped?
- Documentation: Real-world parameter usage patterns

---

## Part 6: Security Considerations

### 6.1 Attachment Security

#### Threat 1: Path Traversal

**Risk**: Malicious client crafts attachment path to access arbitrary files.

**Mitigation**:
- ✅ All paths validated relative to `BaseDir`
- ✅ `filepath.Clean()` normalizes `..` sequences
- ✅ Download handler rejects absolute paths

**Code**: `handler.go:81-86`

---

#### Threat 2: Stored XSS via SVG

**Risk**: Attacker uploads SVG with embedded JavaScript, gateway serves inline.

**Mitigation**:
- ✅ SVG forced to download (not inline)
- ✅ All non-images forced to download
- ✅ `X-Content-Type-Options: nosniff` prevents MIME sniffing

**Code**: `handler.go:107-118`

---

#### Threat 3: Tenant Isolation Bypass

**Risk**: Tenant A queries attachments from Tenant B's requests.

**Mitigation**:
- ✅ R35 fix (2026-09-17): `ListByRequest()` now filters by `tenant_id`
- ⚠️ **Gap**: Download endpoint doesn't verify tenant ownership (relies on path obscurity)

**Recommendation**: Add tenant verification to download:
```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Extract tenant from auth
    tenantID := extractTenantFromAuth(r)
    
    // Verify path belongs to tenant
    if !h.storage.PathBelongsToTenant(relPath, tenantID) {
        writeJSONError(w, 403, "forbidden", "attachment not accessible")
        return
    }
    // ...
}
```

---

#### Threat 4: Storage Exhaustion (DoS)

**Risk**: Attacker uploads massive attachments repeatedly.

**Mitigation**:
- ✅ File size limit configured (`Config.MaxSizeBytes`)
- ✅ SHA256 deduplication prevents duplicate storage
- ⚠️ **Gap**: No per-tenant storage quota

**Recommendation**: Add tenant-level quotas:
```go
type TenantStorageQuota struct {
    TenantID        string
    MaxTotalBytes   int64   // e.g., 10 GB per tenant
    CurrentBytes    int64
    MaxFilesPerDay  int     // Rate limit on uploads
}
```

---

### 6.2 Parameter Injection Risks

#### Threat 5: Parameter Leakage to Unintended Providers

**Risk**: Client sends provider-specific parameter, gateway forwards to incompatible upstream.

**Example**:
- Client sends `mask_sensitive_info: true` (MiniMax-specific)
- Gateway routes to OpenAI
- OpenAI rejects with 400 (strict schema validation)

**Mitigation**:
- ✅ Dialect gating implemented (R52)
- ✅ Unknown parameters via Extensions (not forced into IR)
- ✅ Provider resolution checks target dialect

**Code**: `serialize_openai.go:174-199`

---

#### Threat 6: Malicious Extension Parameters

**Risk**: Attacker crafts request with malicious unknown parameters hoping upstream interprets them.

**Example**:
```json
{
  "model": "gpt-4o",
  "messages": [...],
  "admin_override": true,  // Fake parameter
  "bypass_safety": true    // Hoping some upstream honors it
}
```

**Current Behavior**: Extensions forwarded as-is to upstream.

**Risk Assessment**: **Medium**
- Gateway doesn't interpret unknown parameters
- Upstream is responsible for validation
- But: Creates attack surface if upstream has undocumented parameters

**Recommendation**: Add extension allowlist mode:
```go
type Config struct {
    ExtensionMode string  // "passthrough" | "allowlist" | "block"
    AllowedExtensions []string
}

// In serialization:
if cfg.ExtensionMode == "allowlist" {
    for k, v := range req.Extensions {
        if !contains(cfg.AllowedExtensions, k) {
            slog.Warn("blocked unknown extension", "key", k)
            continue
        }
        out[k] = v
    }
}
```

---

## Part 7: Conclusion

### Overall Assessment: ✅ Strong with Improvement Opportunities

**Attachment Management**: ✅ Robust
- Comprehensive extraction across all protocols
- Multi-backend storage with deduplication
- Security measures in place (XSS prevention, path validation)
- **Gap**: No automatic cleanup (accumulation over time)

**Upstream Integration**: ✅ Excellent
- IR-based protocol conversion preserves multi-modal content
- Cross-provider routing works reliably
- Thinking/reasoning parameter unification
- **Gap**: Provider capability validation could be more explicit

**Parameter Forwarding**: ✅ Strong
- Dialect-aware gating prevents incompatible parameter leakage
- Extensions mechanism for unknown parameters
- Historical issues (top_k, MiniMax params) have been fixed
- **Gap**: Documentation of parameter compatibility is code-only

---

### Key Strengths

1. **Unified IR Layer** - Single representation for all protocols simplifies cross-provider routing
2. **Content-Based Deduplication** - SHA256 hashing significantly reduces storage overhead
3. **Security-First Attachment Serving** - XSS prevention, MIME sniffing protection, tenant isolation
4. **Best-Effort Philosophy** - Attachment failures don't block requests, original base64 preserved as fallback
5. **Historical Learning** - Multiple rounds of fixes show continuous improvement (top_k, type labeling, tool orphans)

---

### Critical Recommendations (Priority Order)

| Priority | Recommendation | Impact | Effort |
|----------|----------------|--------|--------|
| **P0** | Implement attachment lifecycle policy | Storage growth unchecked | Medium |
| **P0** | Add tenant storage quotas | DoS risk | Medium |
| **P1** | Publish parameter compatibility matrix | Developer experience | Low |
| **P1** | Add download endpoint tenant verification | Security gap | Low |
| **P2** | Provider capability validation | Better error messages | Medium |
| **P2** | Attachment metadata indexing | Cleanup efficiency | High |
| **P3** | CDN integration support | Performance/cost | Medium |
| **P3** | Parameter conversion logging | Debugging/analytics | Low |

---

### Compliance Summary

| Component | Protocols | Status |
|-----------|-----------|--------|
| **Attachment Extraction** | OpenAI Chat, Anthropic Messages, OpenAI Responses | ✅ Full Coverage |
| **Storage Backend** | Local, OSS, S3-stub, Cloudreve | ✅ Multi-Backend |
| **Media Types** | Image, Audio, Video, Document | ✅ All Four Types (since 2026-11-11) |
| **Protocol Conversion** | Any Client → Any Upstream | ✅ IR-Based Conversion |
| **Parameter Forwarding** | Portable, Dialect-Specific, Extensions | ✅ Dialect Gating |
| **Lifecycle Management** | Creation, Retrieval, Cleanup | ⚠️ No Automated Cleanup |

---

**Audit Conclusion**: The multi-modal infrastructure is **production-ready** with mature attachment handling, robust protocol conversion, and intelligent parameter forwarding. Primary improvement opportunity is lifecycle management for long-term storage sustainability.

---

## Appendix: Code Reference Index

### Attachment Management
| Component | File | Key Functions |
|-----------|------|---------------|
| Extractor | `domains/attachments/extractor.go` | `ExtractFrom{OpenAI,Anthropic,Responses}Body()` |
| Storage | `domains/attachments/storage_manager.go` | `SaveBase64Image()` |
| Backends | `domains/attachments/storage_backend_*.go` | Backend implementations |
| Handler | `domains/attachments/handler.go` | `ServeHTTP()`, `ListByRequest()` |
| IR Transform | `domains/attachments/ir_transformer.go` | `TransformRequest()` |

### Protocol Conversion
| Component | File | Key Functions |
|-----------|------|---------------|
| OpenAI Parse | `internal/ir/parse_openai.go` | Message parsing |
| Anthropic Parse | `internal/ir/parse_anthropic.go` | Message parsing |
| Gemini Parse | `internal/ir/parse_gemini.go` | Parts parsing |
| OpenAI Serialize | `internal/ir/serialize_openai.go` | `SerializeOpenAI()` |
| Anthropic Serialize | `internal/ir/serialize_anthropic.go` | `SerializeAnthropic()` |
| Gemini Serialize | `internal/ir/serialize_gemini.go` | `SerializeGemini()` |

### Parameter Forwarding
| Component | File | Key Functions |
|-----------|------|---------------|
| Registry | `internal/paramreg/` | Parameter definitions |
| Reasoning Dialect | `internal/reasoningdialect/` | Thinking/reasoning conversion |
| Dialect Resolution | `internal/ir/serialize_*.go` | `resolveTargetDialect()` |

---

**End of Extended Audit**
