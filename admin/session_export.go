// Package admin — 会话跨主机迁移：导出/导入/拉取 API
//
// 三个端点（挂载在 /api/admin/session-export 下，由 main.go 路由）：
//   GET  /api/admin/session-export?id=<gw_session_id>&tenant=<tenant_id>
//        导出完整会话迁移包（消息流 + 压缩链 + 附件 + 摘要 + resume brief）
//   POST /api/admin/session-export/import   body = 迁移包 JSON
//        导入迁移包到 staging（session_packs 表），返回 pack_id
//   GET  /api/admin/session-export/pack?id=<pack_id>&tenant=<tenant_id>
//        按 pack_id 拉取已导入的迁移包（供目标主机 manager/plugin 拉取）
//
// 设计要点：
//   - 复用 request_logs + request_logs_bodies + 压缩链列（已天然落库）
//   - 复用 session_summaries（自动聚合摘要）
//   - 复用 request_logs.attachments（JSONB 附件元数据）
//   - RLS 租户隔离（withTenantTx）
//   - 附件走 CloudReve URL（files.itestu.cn）：导出时只放元数据 + path，
//     由 Pocket 端上传 CloudReve 后回填 cloudreve_url（避免 gateway 依赖 CloudReve 凭据）
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 迁移包 wire format（与 Pocket model.SessionResumeBrief + opencode-plugin MigrationPack 对齐）──

type SessionExport struct {
	SessionMeta SessionExportMeta   `json:"session_meta"`
	ResumeBrief SessionResumeBrief  `json:"resume_brief"`
	Messages    []ExportMessage     `json:"messages"`
	Attachments []ExportAttachment  `json:"attachments"`
	Summary     string              `json:"summary,omitempty"`
	ExportedAt  string              `json:"exported_at"`
}

type SessionExportMeta struct {
	ID        string `json:"id"`        // gw_session_id
	Title     string `json:"title,omitempty"`
	Directory string `json:"directory,omitempty"` // 工作目录（跨主机路径重映射用）
	Instance  string `json:"instance,omitempty"`  // 来源实例标识
	TaskID    string `json:"taskId,omitempty"`
}

// SessionResumeBrief 是迁移包的语义层（与 Pocket model.SessionResumeBrief 对齐）。
// 由导出端尽量填充；缺失字段留空，由 Pocket 迁移服务用 kxmemory 摘要补全。
type SessionResumeBrief struct {
	CurrentState  string   `json:"currentState,omitempty"`
	LastObjective string   `json:"lastObjective,omitempty"`
	Decisions     []string `json:"decisions,omitempty"`
	ChangedFiles  []string `json:"changedFiles,omitempty"`
	Blockers      []string `json:"blockers,omitempty"`
	NextAction    string   `json:"nextAction,omitempty"`
}

type ExportMessage struct {
	Turn               int                    `json:"turn"`
	Role               string                 `json:"role"`
	Content            string                 `json:"content"`
	ParentRequestID    string                 `json:"parent_request_id,omitempty"`    // 压缩链还原
	CompressionReason  string                 `json:"compression_reason,omitempty"`   // mode_1_auto_threshold 等
	CompressionStrategy string                `json:"compression_strategy,omitempty"` // mechanical_trim/memora_l1_inject/llm_summary
	CompressionMeta    map[string]any         `json:"compression_meta,omitempty"`     // tokens_before/after 等
	CreatedAt          string                 `json:"created_at,omitempty"`
}

type ExportAttachment struct {
	Type   string `json:"type"`             // file/diff/report
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`   // gateway attachment path（Pocket 据此上传 CloudReve 回填 URL）
	Size   int64  `json:"size,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

// SessionExportAPI 提供会话导出/导入/拉取端点。
type SessionExportAPI struct {
	db *pgxpool.Pool
}

func NewSessionExportAPI(db *pgxpool.Pool) *SessionExportAPI {
	return &SessionExportAPI{db: db}
}

// ServeHTTP 路由分发：/import 子路径走 POST，其余按 id/pack 查询参数。
func (api *SessionExportAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.db == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session export API requires database")
		return
	}
	switch r.URL.Path {
	case "/api/admin/session-export/import":
		if r.Method != http.MethodPost {
			writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		api.handleImport(w, r)
	case "/api/admin/session-export/pack":
		if r.Method != http.MethodGet {
			writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		api.handleFetchPack(w, r)
	default:
		if r.Method != http.MethodGet {
			writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		api.handleExport(w, r)
	}
}

// handleExport 导出完整会话迁移包。
func (api *SessionExportAPI) handleExport(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("id")
	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}
	if sessionID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "missing id (gw_session_id)")
		return
	}

	pack, err := api.buildExport(r.Context(), sessionID, tenantID)
	if err != nil {
		slog.Error("session export failed", "session_id", sessionID, "tenant", tenantID, "err", err)
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("export failed: %v", err))
		return
	}
	if len(pack.Messages) == 0 && pack.Summary == "" {
		writeExportJSONError(w, http.StatusNotFound, "no data for session")
		return
	}

	writeExportJSON(w, http.StatusOK, pack)
}

// buildExport JOIN request_logs + bodies + summaries + attachments，组装迁移包。
func (api *SessionExportAPI) buildExport(ctx context.Context, sessionID, tenantID string) (*SessionExport, error) {
	pack := &SessionExport{
		SessionMeta: SessionExportMeta{ID: sessionID},
		ExportedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	err := withTenantTx(ctx, api.db, tenantID, func(tx pgx.Tx) error {
		// 1. 消息流（含压缩链）：request_logs JOIN request_logs_bodies
		rows, err := tx.Query(ctx, `
			SELECT
				rl.id, rl.role, rl.parent_request_id,
				rl.compression_reason, rl.compression_strategy, rl.compression_meta,
				rl.attachments, rl.created_at,
				COALESCE(rb.request_body, rl.request_body) AS request_body,
				COALESCE(rb.response_body, rl.response_body) AS response_body
			FROM request_logs rl
			LEFT JOIN request_logs_bodies rb ON rb.request_id = rl.id
			WHERE rl.gw_session_id = $1
			ORDER BY rl.created_at ASC
		`, sessionID)
		if err != nil {
			return fmt.Errorf("query messages: %w", err)
		}
		defer rows.Close()

		turn := 0
		seenAtt := map[string]bool{}
		for rows.Next() {
			var (
				id, role, parentID, reason, strategy string
				compMeta                              []byte
				attachments                           []byte
				createdAt                             time.Time
				reqBody, respBody                     *string
			)
			if err := rows.Scan(&id, &role, &parentID, &reason, &strategy, &compMeta, &attachments, &createdAt, &reqBody, &respBody); err != nil {
				continue
			}
			turn++
			msg := ExportMessage{
				Turn:                turn,
				Role:                role,
				ParentRequestID:     parentID,
				CompressionReason:   reason,
				CompressionStrategy: strategy,
				CreatedAt:           createdAt.UTC().Format(time.RFC3339),
			}
			if len(compMeta) > 0 {
				_ = json.Unmarshal(compMeta, &msg.CompressionMeta)
			}
			// content 优先用 response_body（AI 回复），否则 request_body
			if respBody != nil && *respBody != "" {
				msg.Content = *respBody
			} else if reqBody != nil {
				msg.Content = *reqBody
			}
			pack.Messages = append(pack.Messages, msg)

			// 附件（去重）
			if len(attachments) > 0 {
				var atts []ExportAttachment
				if json.Unmarshal(attachments, &atts) == nil {
					for _, a := range atts {
						key := a.Path + "|" + a.Name
						if seenAtt[key] {
							continue
						}
						seenAtt[key] = true
						pack.Attachments = append(pack.Attachments, a)
					}
				}
			}
		}
		rows.Close()

		// 2. 摘要 + title：session_summaries（session_key = gw_session_id）
		var title, summary *string
		err = tx.QueryRow(ctx, `
			SELECT title, summary
			FROM session_summaries
			WHERE session_key = $1
			LIMIT 1
		`, sessionID).Scan(&title, &summary)
		if err == nil {
			if title != nil {
				pack.SessionMeta.Title = *title
			}
			if summary != nil {
				pack.Summary = *summary
				pack.ResumeBrief.LastObjective = *summary
			}
		}
		// summary 不存在不算错误（会话可能无聚合）
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pack, nil
}

// handleImport 把迁移包写入 staging（session_packs 表），返回 pack_id。
// 目标主机用该 pack_id 经 /pack 端点拉取。
func (api *SessionExportAPI) handleImport(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}
	var pack SessionExport
	if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
		writeExportJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid pack json: %v", err))
		return
	}
	if pack.SessionMeta.ID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "session_meta.id is required")
		return
	}

	packJSON, _ := json.Marshal(pack)
	var packID string
	err := withTenantTx(r.Context(), api.db, tenantID, func(tx pgx.Tx) error {
		// 建表（若不存在，幂等）。正式 schema 应进 migrations；此处 ensure 兼容旧库。
		if _, err := tx.Exec(r.Context(), `
			CREATE TABLE IF NOT EXISTS session_packs (
				pack_id TEXT PRIMARY KEY,
				session_id TEXT NOT NULL,
				tenant_id TEXT NOT NULL,
				pack JSONB NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)
		`); err != nil {
			return fmt.Errorf("ensure session_packs: %w", err)
		}
		packID = fmt.Sprintf("pack-%s-%d", pack.SessionMeta.ID, time.Now().UnixNano())
		_, err := tx.Exec(r.Context(), `
			INSERT INTO session_packs (pack_id, session_id, tenant_id, pack)
			VALUES ($1, $2, $3, $4)
		`, packID, pack.SessionMeta.ID, tenantID, packJSON)
		return err
	})
	if err != nil {
		slog.Error("session import failed", "err", err)
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("import failed: %v", err))
		return
	}

	writeExportJSON(w, http.StatusOK, map[string]string{
		"pack_id":    packID,
		"session_id": pack.SessionMeta.ID,
	})
}

// handleFetchPack 按 pack_id 拉取已导入的迁移包（目标主机调用）。
func (api *SessionExportAPI) handleFetchPack(w http.ResponseWriter, r *http.Request) {
	packID := r.URL.Query().Get("id")
	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}
	if packID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "missing id (pack_id)")
		return
	}

	var packJSON []byte
	err := withTenantTx(r.Context(), api.db, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(r.Context(), `
			SELECT pack FROM session_packs WHERE pack_id = $1
		`, packID)
		return row.Scan(&packJSON)
	})
	if err != nil {
		writeExportJSONError(w, http.StatusNotFound, fmt.Sprintf("pack not found: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(packJSON)
}

// ── helpers（重命名以避免与 admin/handler.go 的 writeJSON 冲突）──

func writeExportJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeExportJSONError(w http.ResponseWriter, status int, msg string) {
	writeExportJSON(w, status, map[string]string{"error": msg})
}
