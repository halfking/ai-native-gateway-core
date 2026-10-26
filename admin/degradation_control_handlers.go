package admin

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

type degradationControl struct {
	monitor *dbdegradation.Monitor
	ttl     *dbdegradation.TTLManager
	writer  *dbdegradation.FileWriter
	reader  *dbdegradation.FileReader
	generic *dbdegradation.GenericRecovery
	// manual 是唯一的可变状态;用 atomic.Bool 取代 mutex,避免 enter/exit
	// 处理期间长时间持有锁 (旧实现把 mu 跨 enter()/ttl.EnterDegradedMode()
	// 等 Redis SCAN+EXPIRE 持有,慢 Redis 会卡住并发降级请求)。
	manual atomic.Bool
	enter  func(context.Context) error
	exit   func(context.Context) error
}

func (h *Handler) WireDegradationControl(monitor *dbdegradation.Monitor, ttl *dbdegradation.TTLManager, writer *dbdegradation.FileWriter, reader *dbdegradation.FileReader, generic *dbdegradation.GenericRecovery, enter, exit func(context.Context) error) {
	h.degradation = &degradationControl{monitor: monitor, ttl: ttl, writer: writer, reader: reader, generic: generic, enter: enter, exit: exit}
}

// degradationStatusBudget 是 degradation 状态类端点的**有界预算**。
//
// ★ R79 轮（2026-10-24）：旧实现把 `r.Context()`（**无 deadline**）直接交给
//
//	「Redis 全量 SCAN」+「gzip 逐文件解压」⇒ 慢 Redis 或大备份时端点可以
//	跑到 **30s+**（生产实测 30s）⇒ ⇒ 现在给它一个上限。
//	★ 超时后**必须显式标注降级**（沿用 admin/dashboard_board_aux.go 的约定：
//	`degraded` 恒发 + `degraded_reason`），不得让「没查出来」看起来像「是 0」。
const degradationStatusBudget = 5 * time.Second

// truncateReason 截断降级原因（避免把超长错误塞进响应体）。
//
// ★ R80 轮：降级原因会带上底层错误文本（如「100/100 个备份文件读取失败：…」）
//
//	⇒ 不截断的话响应体会变得很长。
func truncateReason(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (h *Handler) handleDegradationStatus(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation control not configured")
		return
	}
	// ★ R79 轮：有界预算（见 degradationStatusBudget 的理由）
	ctx, cancel := context.WithTimeout(r.Context(), degradationStatusBudget)
	defer cancel()

	status := "unknown"
	if h.degradation.monitor != nil {
		status = h.degradation.monitor.GetStatus().String()
	}
	manual := h.degradation.manual.Load()
	response := map[string]any{"status": status, "manual": manual}

	degraded := false
	var reasons []string

	if h.degradation.ttl != nil {
		response["ttl_mode"] = h.degradation.ttl.GetMode()
		stats, err := h.degradation.ttl.GetTTLStats(ctx)
		response["ttl_stats"] = stats
		if err != nil {
			// ★ 扫描被中断 ⇒ 计数是**部分**的 ⇒ 必须记账
			degraded = true
			reasons = append(reasons, "ttl stats unavailable")
		}
	}
	if h.degradation.reader != nil {
		summary, err := h.degradation.reader.GetBackupSummary(ctx)
		// ★ R80 轮：**部分结果也要给**（前端可以显示「部分值」而不是空白），
		//	但**必须**同时标 degraded —— 否则「5.18GB / 记录数 0」会被读成
		//	「备份是空的」（R79 的教训：静默的错误数据比慢更危险）。
		if summary != nil {
			response["backups"] = summary
			// ★ R82b 轮：会话数口径**不可用** ⇒ 必须显式说明。
			//	生产实测（154）：备份记录几乎全是 `request_wal`/`request_log`，
			//	顶层**没有 session_id** ⇒ 「会话数」这个读数本身无意义。
			//	⇒ 不标 degraded ⇒ 「会话数 0」会被读成「备份里没有会话」。
			if !summary.SessionCountAvailable {
				degraded = true
				reasons = append(reasons, "backup session count unavailable: "+
					"备份记录不含 session_id（生产实测多为 request_wal/request_log），会话数不可用")
			}
		}
		if err != nil {
			degraded = true
			// ★ R82b 轮：域层错误已经带了「backup summary incomplete: 」前缀
			//	⇒ 这里**不再**重复加前缀（R80 时代两处都加 ⇒ 端点对外出现
			//	「backup summary incomplete: backup summary incomplete: …」）。
			reasons = append(reasons, truncateReason(err.Error()))
		}
	}

	response["degraded"] = degraded
	if len(reasons) > 0 {
		response["degraded_reason"] = strings.Join(reasons, "；")
	}
	writeJSON(w, http.StatusOK, response)
}
func (h *Handler) handleDegradationControl(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation control not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	// enter/exit/ttl 字段在 WireDegradationControl 后不可变,无需锁保护。
	// 副作用 (enter()/ttl.EnterDegradedMode() 等 Redis 操作) 不在锁内执行,
	// 避免慢 Redis 阻塞并发的降级控制请求。只有 manual 标志需要原子更新。
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	switch req.Action {
	case "enter":
		if h.degradation.enter != nil {
			if err := h.degradation.enter(ctx); err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
		}
		if h.degradation.ttl != nil {
			if err := h.degradation.ttl.EnterDegradedMode(ctx); err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
		}
		h.degradation.manual.Store(true)
	case "exit":
		if h.degradation.exit != nil {
			if err := h.degradation.exit(ctx); err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
		}
		if h.degradation.ttl != nil {
			if err := h.degradation.ttl.ExitDegradedMode(ctx); err != nil {
				writeInternalErr(w, "internal error (see server logs)", err)
				return
			}
		}
		h.degradation.manual.Store(false)
	default:
		writeError(w, http.StatusBadRequest, "action must be enter or exit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "manual": h.degradation.manual.Load()})
}

func (h *Handler) handleDegradationRecovery(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil || h.degradation.reader == nil || h.degradation.generic == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation recovery not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Filename string `json:"filename"`
		Archive  bool   `json:"archive"`
	}
	if err := readJSONRequired(r, &req); err != nil || req.Filename == "" {
		writeError(w, http.StatusBadRequest, "filename required")
		return
	}
	taskID, err := h.degradation.generic.Recover(r.Context(), req.Filename, req.Archive)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "status": "pending"})
}

func (h *Handler) handleDegradationRecoveryTask(w http.ResponseWriter, r *http.Request) {
	if h.degradation == nil || h.degradation.generic == nil {
		writeError(w, http.StatusServiceUnavailable, "degradation recovery not configured")
		return
	}
	task, ok := h.degradation.generic.Status(r.PathValue("task_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}
