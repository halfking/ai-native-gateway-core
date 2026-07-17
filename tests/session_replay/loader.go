// Package session_replay - loader.go
//
// 从 /tests/session_replay/sessions/*.json 加载从生产环境（252）导出的会话数据，
// 并按轮次提供给 replayer.go 处理。会话 JSON 的 schema 是 extract.py 写入的格式
// （详见 sessions/manifest.json）。
package session_replay

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// SessionTurn 单轮请求-响应对（含完整 request_body），用于回放到压缩器。
type SessionTurn struct {
	Turn                int             `json:"turn"`
	RequestID           string          `json:"request_id"`
	Ts                  string          `json:"ts"`
	PromptTokens        int             `json:"prompt_tokens"`
	CompletionTokens    int             `json:"completion_tokens"`
	ClientModel         string          `json:"client_model"`
	OutboundModel       string          `json:"outbound_model"`
	CompressionStrategy string          `json:"compression_strategy"`
	CompressionReason   string          `json:"compression_reason"`
	ParentRequestID     string          `json:"parent_request_id"`
	IsAutoRequest       bool            `json:"is_auto_request"`
	TaskID              string          `json:"gw_task_id"`
	Success             bool            `json:"success"`
	ErrorKind           string          `json:"error_kind"`
	BodySizeBytes       int             `json:"body_size_bytes"`
	RespSizeBytes       int             `json:"resp_size_bytes"`
	MsgCount            int             `json:"msg_count"`
	RequestBody         json.RawMessage `json:"request_body"`
	OutboundBody        json.RawMessage `json:"outbound_body"`
	ResponseBody        json.RawMessage `json:"response_body"`
}

// Session 单一会话完整数据 - 由 extract.py 从生产 request_logs_hot 导出
type Session struct {
	Meta  SessionMeta   `json:"session_meta"`
	Turns []SessionTurn `json:"turns"`
}

// SessionMeta 与 extract.py 输出字段对齐
type SessionMeta struct {
	ID                        string `json:"id"`
	Label                     string `json:"label"`
	Instance                  string `json:"instance"`
	SourceDB                  string `json:"source_db"`
	SourceTable               string `json:"source_table"`
	TenantID                  string `json:"tenant_id"`
	ExportedAt                string `json:"exported_at"`
	TurnCount                 int    `json:"turn_count"`
	TotalRequestBytes         int    `json:"total_request_bytes"`
	TotalResponseBytes        int    `json:"total_response_bytes"`
	CompressionTriggeredCount int    `json:"compression_triggered_count"`
	Note                      string `json:"note"`
}

// Manifest sessions/manifest.json 的 schema
type Manifest struct {
	Source       string                 `json:"source"`
	SourceDB     string                 `json:"source_db"`
	Table        string                 `json:"table"`
	ExportedAt   string                 `json:"exported_at"`
	SessionFiles []ManifestSessionEntry `json:"session_files"`
}

// ManifestSessionEntry 索引条目
type ManifestSessionEntry struct {
	SessionID         string  `json:"session_id"`
	Label             string  `json:"label"`
	File              string  `json:"file"`
	Turns             int     `json:"turns"`
	TotalRequestBytes int     `json:"total_request_bytes"`
	SizeKB            float64 `json:"size_kb"`
}

// defaultSessionsDir 默认数据目录（相对于测试文件）
// 通过 ABS_SESSIONS_DIR 覆盖。
const defaultSessionsDir = "tests/session_replay/sessions"

func SessionsDir() string {
	if d := os.Getenv("ABS_SESSIONS_DIR"); d != "" {
		return d
	}
	// 兼容从仓库根目录或子目录运行
	for _, candidate := range []string{
		defaultSessionsDir,
		"../" + defaultSessionsDir,
		"../../" + defaultSessionsDir,
	} {
		if _, err := os.Stat(filepath.Join(candidate, "manifest.json")); err == nil {
			return candidate
		}
	}
	return defaultSessionsDir
}

// LoadSession 从 disk 读一条导出 JSON，解析为 Session 结构。
func LoadSession(file string) (*Session, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	if s.Meta.ID == "" {
		return nil, fmt.Errorf("session_meta.id is empty in %s", file)
	}
	// 按时间排序（即使生产已按 ts ASC 索引，防御性再排序）
	sort.SliceStable(s.Turns, func(i, j int) bool {
		return s.Turns[i].Ts < s.Turns[j].Ts
	})
	return &s, nil
}

// LoadAll 从 manifest.json 索引加载所有导出 session。
func LoadAll() ([]*Session, error) {
	dir := SessionsDir()
	mf, err := LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(mf.SessionFiles))
	for _, e := range mf.SessionFiles {
		file := e.File
		if _, err := os.Stat(file); err != nil && filepath.IsAbs(file) {
			file = filepath.Join(dir, filepath.Base(file))
		}
		s, err := LoadSession(file)
		if err != nil {
			// manifest may reference session files that are intentionally absent
			// (e.g. extract runs without --include-failed); warn instead of
			// failing the whole load.
			if os.IsNotExist(unwrapPathError(err)) {
				log.Printf("session_replay: skipping missing session file %q (manifest entry session_id=%s)", file, e.SessionID)
				continue
			}
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// unwrapPathError walks a wrapped error chain and returns the first
// *os.PathError encountered (or the original error if none exists). It
// exists so callers can use os.IsNotExist on the underlying path-level
// cause rather than a higher-level wrapper such as fmt.Errorf("...: %w", ...).
func unwrapPathError(err error) error {
	for {
		if pathErr, ok := err.(*os.PathError); ok {
			return pathErr
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}

// LoadManifest 读 manifest.json
func LoadManifest(file string) (*Manifest, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// FindSessionByID 按 id 查找已加载的 session
func FindSessionByID(sessions []*Session, id string) *Session {
	for _, s := range sessions {
		if s.Meta.ID == id {
			return s
		}
	}
	return nil
}
