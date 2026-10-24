// Package requestdetail provides in-flight request content storage
// (process memory meta + per-request_id local files) and a read locator
// that prefers local hot content before DB dual-write sources.
package requestdetail

import "encoding/json"

// Source names the layer that supplied the detail payload.
type Source string

const (
	SourceMemory Source = "memory"
	SourceFile   Source = "file"
	SourceLive   Source = "live_stream"
	// ★ 2026-10-24（v1 → session_turns 替换轮）：值从 "_request_logs" 改成
	//   "request_logs" —— ★★ **订正（2026-10-25 12h 审计轮）**：原文写的理由是
	//   「前端 useRequestDetailLoader.ts 的 sourceRank 与其测试用的都是
	//   'request_logs' ⇒ 旧值与前端对不上」⇒ ★★ **该理由不成立**：
	//   实测该文件的 `sourceRank` 只比较 `'journey'`（3）与 `'waterfall'`（2），
	//   **整段不含 'request_logs'**；含 `'request_logs'` 字面量的是**测试文件**
	//   `useRequestDetailLoader.test.ts`（11 处夹具）⇒ ⇒ 生产前端**从不断言**
	//   该字段的值 ⇒ 「旧值与前端对不上」在生产链路上**无可观测后果**。
	//   ⇒ ★ **真正理由**（与 `admin/credential_monitor.go` 同一款）：旧值
	//   `"_request_logs"` 是一个**已被迁移 890 物理删除的表名** ⇒ 运营在响应里
	//   读到的是一个不存在的表 ⇒ 改成读面名 `"request_logs"`。
	//   ⇒ ★★ **改动本身是对的、行为中性**：① 本字段**不落库**（只出现在 API
	//   响应的 `source` JSON 字段里）⇒ 无需数据迁移；② 无任何消费方按该值
	//   分支 ⇒ 改值不影响路由、不影响客户端可见的分类口径。
	//   ⇒ ★ 订正的是**理由**，不是改动本身。
	//   注意：这两个 Source 描述的是**读面**（请求账本 / 会话轮次），
	//   不是物理表名 —— 物理表早已是会话族。
	SourceRequestLogs  Source = "request_logs"
	SourceSessionTurns Source = "session_turns"
)

// Persistence classifies whether the request has been written to DB yet.
type Persistence string

const (
	PersistenceInFlight  Persistence = "in_flight"
	PersistencePersisted Persistence = "persisted"
)

// Bodies holds decoded request/response/outbound payloads.
type Bodies struct {
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	OutboundBody json.RawMessage `json:"outbound_body,omitempty"`
}

// Meta is the lightweight in-memory request identity (no full bodies).
//
// BodyStatus (Subtask 3, feat/session-detail-body-status) is the wire-level
// hint a UI uses to decide whether request/response/outbound bodies are
// readable from the underlying storage. Two-state contract:
//
//	"available"   — at least one of request_delta / response_delta / outbound_body
//	                carries a real payload: anything other than SQL NULL, the
//	                JSON null literal, or pure whitespace. Empty containers
//	                ([] / {}) DO count as payload.
//	"unavailable" — all three are SQL NULL / JSON null / whitespace.
//
// （R73 订正：初稿把「incl. JSON null」写在 available 侧，与实现
// admin/body_status.go columnHasPayload——JSON null 判无载荷——及迁移后
// 统一的 SQL 探针 `<> 'null'::jsonb` 三面相反；JSON null 恰是写路径对
// 「载荷为空」的常态编码（bodies_writer jsonTextOrNull），把它算 available
// 会让「响应从未到达」的轮次在列表上误报有正文。以实现+双侧测试为准。）
//
// "dropped" is intentionally NOT part of the wire contract yet: the retention
// period (lifecycle.request_body_retention_hours) and the bodies_trimmer job
// (Subtask 6) that would let us distinguish "dropped by retention" from
// "never written" are not landed. See docs/audit/2026-09-25-session-storage-audit-handoff.md §5.
//
// omitempty is intentional on this field so older producers that haven't
// adopted the contract don't suddenly start emitting "" for every row —
// consumers must treat the missing key as "unknown / not computed".
type Meta struct {
	RequestID   string  `json:"request_id"`
	TenantID    string  `json:"tenant_id,omitempty"`
	GwSessionID *string `json:"gw_session_id,omitempty"`
	GwTaskID    *string `json:"gw_task_id,omitempty"`
	ClientModel *string `json:"client_model,omitempty"`
	Status      *string `json:"request_status,omitempty"`
	Success     *bool   `json:"success,omitempty"`
	LatencyMs   *int    `json:"latency_ms,omitempty"`
	TurnNumber  *int    `json:"turn_number,omitempty"`
	BodyStatus  string  `json:"body_status,omitempty"`
}

// Detail is the unified admin detail payload.
type Detail struct {
	Source      Source      `json:"source"`
	Persistence Persistence `json:"persistence"`
	Meta        Meta        `json:"meta"`
	Bodies      *Bodies     `json:"bodies,omitempty"`
	Warning     string      `json:"warning,omitempty"`
}

// filePayload is the on-disk JSON shape (filename = request_id.json).
type filePayload struct {
	Meta   Meta   `json:"meta"`
	Bodies Bodies `json:"bodies"`
}
