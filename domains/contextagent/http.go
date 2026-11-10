package contextagent

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 返回上下文智能体的 HTTP 处理器。
//
// ★ 路由（相对挂载点）：
//
//	POST /resolve            识别 + 计划 + 分层加载 + 装配（可选继续提交并跑记忆工具回环）
//	POST /tool               执行一次记忆工具调用
//	GET  /record/{request_id} 取回可还原记录（原样还原客户端请求 / 上游响应 / 最终响应）
//
// ★ 认证由挂载方负责（网关的 admin 中间件 / MCP 控制面 scope）；
//
//	本处理器**自己不做鉴权** —— 它是内网能力面，不是公开端点。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", s.handleResolveJSON)
	mux.HandleFunc("/tool", s.handleToolJSON)
	mux.HandleFunc("/record/", s.handleRecordJSON)
	return mux
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// errorPayload 是统一的错误信封。
type errorPayload struct {
	Error string `json:"error"`
}

// handleResolveJSON 处理 /resolve。
func (s *Service) handleResolveJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorPayload{Error: "POST required"})
		return
	}
	resp, status, err := s.HandleResolve(r.Context(), r)
	if err != nil {
		writeJSON(w, status, errorPayload{Error: err.Error()})
		return
	}
	writeJSON(w, status, resp)
}

// handleToolJSON 处理 /tool。
func (s *Service) handleToolJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorPayload{Error: "POST required"})
		return
	}
	resp, status, err := s.HandleTool(r.Context(), r)
	if err != nil {
		writeJSON(w, status, errorPayload{Error: err.Error()})
		return
	}
	writeJSON(w, status, resp)
}

// handleRecordJSON 处理 /record/{request_id}。
func (s *Service) handleRecordJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorPayload{Error: "GET required"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/record/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errorPayload{Error: "request_id is required"})
		return
	}
	resp, status, err := s.HandleRecord(r.Context(), r, id)
	if err != nil {
		writeJSON(w, status, errorPayload{Error: err.Error()})
		return
	}
	writeJSON(w, status, resp)
}
