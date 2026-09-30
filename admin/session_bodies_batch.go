package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// 会话域正文批量取数（2026-10-01）。
//
// 背景见 docs/audit/2026-09-30-session-request-data-re-audit.md §5.6：把
// `request_logs_bodies_with_current_month` 用 LEFT JOIN 接到会话级的外侧上，
// 规划器会对每一轮做一次列存分区扫描。迁移 765 把 2026_09 分区（2,216,660 行）
// 转成 Citus columnar 之后，summary 要 40 秒、compare 要 4~6 秒。
//
// 正确形态是**先按谓词取轮次，再按主键批量取正文**。
//
// # 配对键：request_id 与 (request_id, ts) 都能命中，但含义完全不同
//
// `request_logs_bodies.ts` 是**正文写入时间**，不是轮次时间。实测（真库，
// 2026-10-01）：同一个 request_id 在 session_turns 与 bodies 里的 ts
// **99.85% 不相等**（809,892 / 811,128，通常差 8~16 秒）。
//
// 所以：
//   - 配 `(request_id, ts)` —— 几乎永远配不上，返回空。session_summary_v2
//     的原查询就是这么写的，因此它长期拿不到正文。
//   - 配 `request_id` —— 命中。session_compare / session_export /
//     sessionforensics / session_title 全都这么写，这是既有且正确的口径。
//
// 两种键都保留，因为它们对应**两个端点各自的既有行为**。把其中一个悄悄改成
// 另一个就是在重构里夹带行为变更；那是独立的决策，需要单独评估（见审计报告
// §5.9）。此处只负责把「批量取」这件事做对，配对语义原样透传。
//
// # 批量半连接是形态上的关键，不是风格问题
//
//	IN (SELECT unnest) → Index Scan using request_logs_bodies_2026_09_pkey     7 ms
//	unnest + LEFT JOIN → ColumnarScan over all 2,216,660 rows              10,365 ms
//
// LEFT JOIN 贴着函数扫描时规划器没法重排，判定全表列存扫描最便宜，于是真扫一遍。
// 半连接把一小撮 id 的哈希交给它，它就会走主键探针。用数组而不是 `VALUES`
// 列表，是为了让轮数无上界时也不会撞上 65535 参数上限。
const sessionBodiesByRequestIDSQL = `
	SELECT rb.request_id,
	       rb.request_body,
	       rb.outbound_body,
	       rb.response_body
	FROM request_logs_bodies_with_current_month rb
	WHERE rb.request_id = ANY($1::text[])
`

// sessionBodiesByRequestIDAndTSSQL reproduces the tuple pairing the summary
// path has always used. It is kept verbatim so that refactor stays
// behaviour-preserving; see the note above on why it mostly returns nothing.
const sessionBodiesByRequestIDAndTSSQL = `
	SELECT rb.request_id,
	       rb.ts,
	       rb.request_body,
	       rb.outbound_body,
	       rb.response_body
	FROM request_logs_bodies_with_current_month rb
	WHERE (rb.request_id, rb.ts) IN (
		SELECT *
		FROM unnest($1::text[], $2::timestamptz[]) AS k(request_id, ts)
	)
`

// sessionBodyQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, so the read
// path can run inside the caller's tenant transaction (session_compare) or
// straight off the pool (session_summary_v2) without duplicating the query.
type sessionBodyQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// sessionBody is the body triple for one turn. Pointers, not []byte, because the
// callers scan JSONB columns into *string and treat nil as "no body stored" —
// the same thing a NULL from the old LEFT JOIN produced.
type sessionBody struct {
	requestBody  *string
	outboundBody *string
	responseBody *string
}

// querySessionBodiesByRequestID pairs turns to bodies on request_id alone.
// Unambiguous: no request_id carries more than one body row (measured: 0),
// because every partition of request_logs_bodies has a UNIQUE (request_id, ts)
// primary key and the hot table is disjoint from the monthly parent.
func querySessionBodiesByRequestID(
	ctx context.Context,
	q sessionBodyQuerier,
	requestIDs []string,
) (map[string]sessionBody, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, sessionBodiesByRequestIDSQL, requestIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bodies := make(map[string]sessionBody, len(requestIDs))
	for rows.Next() {
		var requestID string
		var reqRaw, outboundRaw, respRaw []byte
		if err := rows.Scan(&requestID, &reqRaw, &outboundRaw, &respRaw); err != nil {
			return nil, err
		}
		bodies[requestID] = sessionBody{
			requestBody:  rawToStringPtr(reqRaw),
			outboundBody: rawToStringPtr(outboundRaw),
			responseBody: rawToStringPtr(respRaw),
		}
	}
	return bodies, rows.Err()
}

// querySessionBodiesByRequestIDAndTS pairs on (request_id, ts) — the summary
// path's long-standing semantics, preserved unchanged. Callers get back only
// the turns that HAVE a body; a missing entry means "no body stored", which is
// what the old LEFT JOIN yielded as NULL.
func querySessionBodiesByRequestIDAndTS(
	ctx context.Context,
	q sessionBodyQuerier,
	requestIDs []string,
	timestamps []time.Time,
) (map[fallbackTurnKey]sessionBody, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, sessionBodiesByRequestIDAndTSSQL, requestIDs, timestamps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bodies := make(map[fallbackTurnKey]sessionBody, len(requestIDs))
	for rows.Next() {
		var requestID string
		var tsValue time.Time
		var reqRaw, outboundRaw, respRaw []byte
		if err := rows.Scan(&requestID, &tsValue, &reqRaw, &outboundRaw, &respRaw); err != nil {
			return nil, err
		}
		// Same normalization as the phase-1 key builder — the two must go
		// through the same function or every hit silently becomes a miss.
		bodies[newFallbackTurnKey(requestID, tsValue)] = sessionBody{
			requestBody:  rawToStringPtr(reqRaw),
			outboundBody: rawToStringPtr(outboundRaw),
			responseBody: rawToStringPtr(respRaw),
		}
	}
	return bodies, rows.Err()
}

// rawToStringPtr mirrors what pgx does when scanning a nullable text column
// into *string: a NULL stays nil, an empty value stays a pointer to "".
func rawToStringPtr(raw []byte) *string {
	if raw == nil {
		return nil
	}
	s := string(raw)
	return &s
}

// strPtrBytes converts a nullable text column to the byte slice
// decodeStoredJSON expects, keeping nil for "no value" so the decoder's
// existing nil handling is reused rather than duplicated.
func strPtrBytes(s *string) []byte {
	if s == nil {
		return nil
	}
	return []byte(*s)
}
