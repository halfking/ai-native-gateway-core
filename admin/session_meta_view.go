// Package admin - session_meta_view.go
//
// Read-side view for `public.session_analysis_metadata` (migration 567).
//
// Background:
//
//	domains/analysis/sessionmeta is the writer/extractor. It owns the `Result`
//	struct that lives inside `session_analysis_metadata.payload` (JSONB), and
//	the status enum ('provisional' | 'final'). The writer never reads it back
//	itself — that is the admin's job. Until now, the admin endpoints that
//	returned `public.sessions` rows (turns_sessions list, session_detail_v2
//	detail, session_turns_v2 snapshot) silently dropped the metadata, so the
//	front-end had to make a second round-trip per session to render
//	agent/work_types/project under any view that didn't end in
//	`/api/admin/session-analytics/...`.
//
// This file plugs that gap. We define a narrow, JSON-friendly view struct
// (`SessionAnalysisView`) shared by every read endpoint, and a tiny helper
// that builds the LATERAL subquery picking the most recent row per session
// (preferring 'final' over 'provisional'). The JSONB `payload` is decoded
// into the writer's `sessionmeta.Result` so consumers see one canonical shape.
//
// Rules followed here:
//
//	* The join is LEFT, so sessions without analysis metadata still render
//	  (front-end distinguishes "no analysis yet" from "analysis failed").
//	* `input_hash` is exposed both as the raw column (so the next migration
//	  can promote it into session_metadata.authoritative_fields) AND inside
//	  `payload.input_hash` (via sessionmeta.Result) — callers that already
//	  render the payload don't need to extract it themselves.
//	* The writer side is `status = 'provisional'` at arrival and `'final'`
//	  after `SessionMetadataCloseHook` runs; the read side prefers 'final' so
//	  late-arriving provisional rows don't backslide a session's display.
//	* No business logic lives here. This is a pure projection.

package admin

import (
	"encoding/json"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
)

// sessionAnalysisFinalLateral is the standard LATERAL subquery used by every
// admin endpoint that returns session rows.
//
// Join pattern:
//
//	LEFT JOIN LATERAL (
//	    SELECT ...
//	      FROM public.session_analysis_metadata sam
//	     WHERE sam.tenant_id = s.tenant_id
//	       AND sam.scoped_session_id = s.session_id
//	     ORDER BY (sam.status = 'final') DESC, sam.updated_at DESC
//	     LIMIT 1
//	) sam ON true
//
// Ordering:
//
//	1. (sam.status = 'final') DESC — boolean ordering in PostgreSQL: 'final'
//	   rows rank before 'provisional'. This guarantees a 'final' row beats a
//	   'provisional' row that arrives (or is refreshed) later.
//	2. sam.updated_at DESC — within the same status, pick the latest row.
//
// Placeholders: this function returns the subquery fragment (without the
// `LEFT JOIN LATERAL (...) alias ON true` wrapper) so callers can append it
// at the right spot in their own FROM clause. See sessionAnalysisJoinSQL.
func sessionAnalysisFinalLateral() string {
	return `(
		SELECT sam.status,
		       sam.schema_version,
		       sam.input_hash,
		       sam.source_task_id,
		       sam.updated_at,
		       sam.payload
		  FROM public.session_analysis_metadata sam
		 WHERE sam.tenant_id = s.tenant_id
		   AND sam.scoped_session_id = s.session_id
		 ORDER BY (sam.status = 'final') DESC, sam.updated_at DESC
		 LIMIT 1
	)`
}

// sessionAnalysisJoinSQL is the full LEFT JOIN LATERAL clause that callers
// paste into their FROM block. Alias: `sam` (matches the column list below).
//
// Use after the existing `LEFT JOIN public.session_title_states tstate` (or
// any equivalent LEFT JOIN that already aliases `s` to public.sessions).
func sessionAnalysisJoinSQL() string {
	return `LEFT JOIN LATERAL ` + sessionAnalysisFinalLateral() + ` sam ON true`
}

// sessionAnalysisSelectCols is the SELECT column list for the join. The order
// MUST match the order in `rows.Scan(...)` for the read-side struct.
//
//	0: status                  text    — 'provisional' | 'final'
//	1: schema_version          text    — e.g. 'session-analysis/v1'
//	2: input_hash              text    — canonical hash of the corpus
//	3: source_task_id          *text   — null when async-produced (not yet wired)
//	4: updated_at              timestamptz
//	5: payload                 jsonb   — sessionmeta.Result (may be null on LEFT JOIN miss)
func sessionAnalysisSelectCols() string {
	return `sam.status, sam.schema_version, sam.input_hash, sam.source_task_id, sam.updated_at, sam.payload`
}

// SessionAnalysisView is the read-side projection of session_analysis_metadata.
//
//	* All scalar columns are nullable so LEFT JOIN misses render as nil fields,
//	  not zero values (a "missing updated_at" is meaningful — it means the
//	  session was never analysed).
//	* `Payload` is the decoded `sessionmeta.Result`. We surface it as the
//	  canonical shape rather than re-marshalling the JSONB; consumers that
//	  already render the payload (admin web, internal tooling) keep working
//	  without a shape change.
//	* `PayloadRaw []byte` keeps the raw bytes so internal callers can pass
//	  them straight through (e.g. snapshot endpoint) without a re-marshal.
//	  Always nil when Payload decodes successfully (decode is lossless).
type SessionAnalysisView struct {
	Status        string             `json:"status"`
	SchemaVersion string             `json:"schema_version,omitempty"`
	InputHash     string             `json:"input_hash,omitempty"`
	SourceTaskID  *string            `json:"source_task_id,omitempty"`
	UpdatedAt     *time.Time         `json:"updated_at,omitempty"`
	Payload       *sessionmeta.Result `json:"payload,omitempty"`
	PayloadRaw    []byte             `json:"-"`
}

// scanSessionAnalysis decodes the raw Scan values for a SessionAnalysisView.
//
// All inputs except `status` may be NULL (LEFT JOIN miss → row not present).
// The caller is expected to pass pointers from `rows.Scan` in the order
// emitted by `sessionAnalysisSelectCols`:
//
//	&status, &schemaVersion, &inputHash, &sourceTaskID,
//	&updatedAt, &payloadRaw
func scanSessionAnalysis(view *SessionAnalysisView, status, schemaVersion, inputHash string, sourceTaskID *string, updatedAt *time.Time, payloadRaw []byte) {
	if status == "" {
		// LEFT JOIN miss — leave view nil-fields zero.
		return
	}
	view.Status = status
	view.SchemaVersion = schemaVersion
	view.InputHash = inputHash
	view.SourceTaskID = sourceTaskID
	view.UpdatedAt = updatedAt
	view.PayloadRaw = payloadRaw
	if len(payloadRaw) > 0 {
		var r sessionmeta.Result
		if err := json.Unmarshal(payloadRaw, &r); err == nil {
			view.Payload = &r
			// Backstop: keep `input_hash` consistent between column and payload
			// even when the writer's payload omitempty dropped it (see the
			// omitempty audit fix in extractor.go). The column is authoritative.
			if r.InputHash == "" {
				r.InputHash = inputHash
				view.Payload = &r
			}
		}
	}
}
