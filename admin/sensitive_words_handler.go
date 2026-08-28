package admin

import (
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

// SensitiveWordsHandler 敏感词管理 Handler
//
// 提供三类管理端点：
//   - reload: 从配置文件热加载敏感词
//   - status: 查询引擎状态（分类、词数、配置路径）
//   - match:  对给定文本触发一次匹配扫描
type SensitiveWordsHandler struct {
	engine          *sensitive.SensitiveWordEngine
	patternDetector interface{ ReloadFromFile() error }
}

// NewSensitiveWordsHandler 创建 Handler
func NewSensitiveWordsHandler(engine *sensitive.SensitiveWordEngine) *SensitiveWordsHandler {
	return &SensitiveWordsHandler{engine: engine}
}

// SetPatternDetector wires the YAML pattern detector into the same reload
// operation as the sensitive-word engine.
func (h *SensitiveWordsHandler) SetPatternDetector(detector interface{ ReloadFromFile() error }) {
	h.patternDetector = detector
}

// RegisterRoutes 注册 /api/admin/sensitive-words/* 路由。
func (h *SensitiveWordsHandler) RegisterRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/sensitive-words/reload", adminWrap(h.handleReload))
	mux.HandleFunc("/api/admin/sensitive-words/status", adminWrap(h.handleStatus))
	mux.HandleFunc("/api/admin/sensitive-words/match", adminWrap(h.handleMatch))
}

// handleReload POST 从配置文件重新加载敏感词。
func (h *SensitiveWordsHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	if err := h.engine.ReloadFromFile(); err != nil {
		slog.Error("sensitive word reload failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Sensitive word reload failed"})
		return
	}
	if h.patternDetector != nil {
		if err := h.patternDetector.ReloadFromFile(); err != nil {
			slog.Error("sensitive pattern reload failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Sensitive pattern reload failed"})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "Sensitive words reloaded",
		"word_count": h.engine.LoadedWordCount(),
		"categories": len(h.engine.Categories()),
	})
}

// handleStatus GET 返回引擎当前状态。
func (h *SensitiveWordsHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	cats := h.engine.Categories()
	type catInfo struct {
		Name  string `json:"name"`
		Key   string `json:"key"`
		Level string `json:"level"`
	}
	catList := make([]catInfo, 0, len(cats))
	for _, c := range cats {
		catList = append(catList, catInfo{
			Name:  c.Name,
			Key:   c.Key,
			Level: c.Level.String(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"word_count": h.engine.LoadedWordCount(),
		"categories": catList,
	})
}

// MatchRequest 匹配测试请求体
type MatchRequest struct {
	Text string `json:"text"`
}

// handleMatch POST 对给定文本执行敏感词匹配。
func (h *SensitiveWordsHandler) handleMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	var req MatchRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	matches := h.engine.Match(req.Text)
	type matchInfo struct {
		Word     string `json:"word"`
		Begin    int    `json:"begin"`
		End      int    `json:"end"`
		Category string `json:"category"`
		Level    string `json:"level"`
	}
	info := make([]matchInfo, 0, len(matches))
	for _, m := range matches {
		info = append(info, matchInfo{
			Word:     m.Word,
			Begin:    m.Begin,
			End:      m.End,
			Category: m.Category.Name,
			Level:    m.Category.Level.String(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"matched": len(info) > 0,
		"count":   len(info),
		"results": info,
	})
}
