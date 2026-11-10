package contextagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record 是一次客户端请求的**完整可还原记录**。
//
// ★ 用户要求：「网关接收与处理的内容进行记录，能原样还原客户端的请求与
//
//	  llm 端的返回信息，中间由网关进行优化。」
//	  ⇒ 本结构持有三份**原始字节**：
//
//		OriginalRequestBody  客户端发来的请求体（网关任何改写之前的字节）
//		OriginalResponseBody 第一次上游 LLM 返回的响应体（改写之前的字节）
//		FinalResponseBody    最终返回给客户端的响应体
//
// ★ 缺口说明（诚实登记）：仓内目前**没有**任何字节级落盘点能同时满足
//
//	「原客户端请求」+「原上游响应」+「可按 request_id 读回」。
//	· internal/logging/raw_data_logger.go 是唯一的字节级存储，但它是
//	  **只写、轮转、无读面**的，而且它的 client_request 方向记的是
//	  **改写后**的体（executor.go 的 params.BodyBytes 来自 handler.go 的
//	  upstreamBody := bodyBytes，即压缩/注入/重排之后）。
//	· domains/requestjourney 的契约**明文禁止**携带 body/header
//	  （contract.go 的 JourneyEvent 注释 + DisallowUnknownFields 硬堵）。
//	⇒ 本包自带一个按 request_id 寻址的文件型 Recorder（可读回、可还原），
//	  并把 PG 化列为下一阶段（见设计档 §落地分期）。
type Record struct {
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`

	// Identity 是识别结果（项目 / 任务 / 会话 / 步骤 …）。
	Identity Identity `json:"identity"`

	// Plan 是本次分层加载计划。
	Plan Plan `json:"plan"`

	// Layers 是各层实际加载到的内容与来源。
	Layers []LayerContent `json:"layers"`

	// OriginalRequestBody 是客户端原始请求体（字节级）。
	OriginalRequestBody []byte `json:"original_request_body"`
	// OriginalResponseBody 是第一次上游响应体（字节级）。
	OriginalResponseBody []byte `json:"original_response_body"`
	// FinalResponseBody 是最终返回客户端的响应体（字节级）。
	FinalResponseBody []byte `json:"final_response_body"`

	// Rounds 记录每一轮「请求体 → 响应体」的中间态。
	Rounds []Round `json:"rounds"`

	// AssembledBody 是网关装配后的出站体（**未提交上游**时也有值）。
	//
	// ★ 客户端驱动形态（只装配）下，Rounds 为空但这个字段有值 ⇒
	//	仍能回答「网关把请求改成什么样了」。
	AssembledBody []byte `json:"assembled_body,omitempty"`

	// ServedToolCalls 记录网关自己执行过的记忆工具调用。
	ServedToolCalls []ServedCall `json:"served_tool_calls"`

	// Notes 记录**没有阻止请求但影响读数解释**的事项。
	Notes []string `json:"notes,omitempty"`
}

// Round 是一轮上游交互的记录。
type Round struct {
	Index        int    `json:"index"`
	RequestBody  []byte `json:"request_body"`
	ResponseBody []byte `json:"response_body"`
	// Note 记录这一轮发生了什么（例如「记忆工具结果已并入」）。
	Note string `json:"note,omitempty"`
}

// ServedCall 是一次被网关执行的记忆工具调用。
type ServedCall struct {
	ToolCallID string   `json:"tool_call_id"`
	Name       string   `json:"name"`
	Arguments  []byte   `json:"arguments"`
	Loaded     []string `json:"loaded"`
	Error      string   `json:"error,omitempty"`
}

// RestoreClientRequest 返回**原样**的客户端请求体。
//
// ★ 这是「原样还原客户端请求」的入口：返回的是网关任何改写之前的字节。
func (r *Record) RestoreClientRequest() []byte {
	if r == nil {
		return nil
	}
	return append([]byte(nil), r.OriginalRequestBody...)
}

// RestoreUpstreamResponse 返回**原样**的第一次上游响应体。
func (r *Record) RestoreUpstreamResponse() []byte {
	if r == nil {
		return nil
	}
	return append([]byte(nil), r.OriginalResponseBody...)
}

// RestoreFinalResponse 返回最终返回客户端的响应体。
func (r *Record) RestoreFinalResponse() []byte {
	if r == nil {
		return nil
	}
	return append([]byte(nil), r.FinalResponseBody...)
}

// Recorder 负责把 Record 落盘 / 读回。
type Recorder interface {
	// Save 保存记录（同一 request_id 覆盖）。
	Save(ctx context.Context, rec *Record) error
	// Load 按 request_id 读回记录；不存在时返回 (nil, nil)。
	Load(ctx context.Context, requestID string) (*Record, error)
}

// MemoryRecorder 是内存实现（测试与短生命周期场景用）。
type MemoryRecorder struct {
	mu   sync.RWMutex
	recs map[string]*Record
}

// NewMemoryRecorder 构造内存 Recorder。
func NewMemoryRecorder() *MemoryRecorder {
	return &MemoryRecorder{recs: map[string]*Record{}}
}

// Save 实现 Recorder。
func (m *MemoryRecorder) Save(_ context.Context, rec *Record) error {
	if m == nil || rec == nil || rec.RequestID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recs == nil {
		m.recs = map[string]*Record{}
	}
	m.recs[rec.RequestID] = rec
	return nil
}

// Load 实现 Recorder。
func (m *MemoryRecorder) Load(_ context.Context, requestID string) (*Record, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.recs[requestID], nil
}

// FileRecorder 是按 request_id 寻址的文件型 Recorder。
//
// ★ 落盘形态：一个请求一个 JSON 文件，**三份原始字节用 base64 存**
//
//	（JSON 没有字节类型，base64 是唯一无损的编码）⇒ 读回后逐字节相等。
//
// ★ 为什么先落文件而不是落 PG：新增 PG 表要走本仓的迁移 + manifest +
//
//	dbinit 接线 + RLS 策略一整套流程（§落地分期的下一阶段），
//	而「原样还原」这件事本身只需要一个**可按 request_id 读回**的存储。
//	文件实现让本轮就能交付可验证的还原能力，PG 化是替换而非重写。
type FileRecorder struct {
	dir string
	mu  sync.Mutex
}

// NewFileRecorder 构造文件型 Recorder（目录不存在时创建）。
func NewFileRecorder(dir string) (*FileRecorder, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("contextagent: recorder dir is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("contextagent: create recorder dir: %w", err)
	}
	return &FileRecorder{dir: dir}, nil
}

// filePath 返回某请求的记录文件路径。
func (f *FileRecorder) filePath(requestID string) string {
	return filepath.Join(f.dir, safeFileName(requestID)+".json")
}

// safeFileName 把 request_id 变成安全的文件名。
//
// ★ 只允许字母数字与 -_，其余（含 '.' 与路径分隔符）替换为 _；
//
//	并限制长度，避免路径穿越与超长文件名。
func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "unnamed"
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// Save 实现 Recorder（原子写：先写临时文件再 rename）。
func (f *FileRecorder) Save(_ context.Context, rec *Record) error {
	if f == nil || rec == nil || rec.RequestID == "" {
		return nil
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("contextagent: marshal record: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.filePath(rec.RequestID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return fmt.Errorf("contextagent: write record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("contextagent: commit record: %w", err)
	}
	return nil
}

// Load 实现 Recorder。
func (f *FileRecorder) Load(_ context.Context, requestID string) (*Record, error) {
	if f == nil || requestID == "" {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.filePath(requestID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("contextagent: read record: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("contextagent: unmarshal record: %w", err)
	}
	return &rec, nil
}

// encodeBase64 把字节编码成 base64（JSON 没有字节类型）。
//
// ★ 只在**出参**（HTTP 响应）这一侧用：Record 自己的 JSON 表示由
//
//	encoding/json 直接把 []byte 编成 base64（读回时自动解码），
//	所以落盘那条路径**不需要**手写编解码（曾经多写了一个
//	decodeBase64，是死代码，已删）。
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
