package admin

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
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
// `_request_logs_bodies.ts` 是**正文写入时间**，不是轮次时间。实测（真库，
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
// 三种形态，同一批 200 个真实 request_id、同一列选三列正文，同一分区
// （2026-10-01 真库实测，见审计报告 §8.3）：
//
//	IN (SELECT unnest($1)) → Index Scan using _2026_09_pkey          1.77~2.20 s
//	= ANY($1::text[])      → ColumnarScan, Rows Removed 2,217,398   15.85~17.43 s
//	unnest + LEFT JOIN      → ColumnarScan, 逐轮扫                    10,365 ms
//
// **注意第二行。** `= ANY(数组)` 长得像半连接，实际上与 `IN (SELECT unnest)`
// 不是一回事：前者让规划器放弃主键探针，即使数组里只有 1 个元素也一样
// （实测 N=1/2/4/8/16/32/64 全部 ColumnarScan），而 `= '字面量'` 或
// `IN (SELECT unnest)` 都会走 Index Scan。两者返回的行完全相同，代价差 8~9 倍。
// 这不是估算误差：`= ANY` 那条在列存分区上要解压整列正文。
//
// LEFT JOIN 贴着函数扫描时规划器没法重排，判定全表列存扫描最便宜，于是真扫一遍。
// 半连接把一小撮 id 的哈希交给它，它就会走主键探针。用数组而不是 `VALUES`
// 列表，是为了让轮数无上界时也不会撞上 65535 参数上限。
//
// **2026-10-01 修正**：这里原先写的是 `= ANY($1::text[])`，实测 15.85~17.43 秒。
// `= ANY(数组)` 与 `IN (SELECT unnest(数组))` 返回完全相同的行，但前者让规划器
// 放弃主键探针 —— 即使数组只有 1 个元素也一样（N=1..64 实测全部 ColumnarScan），
// 而 `= '字面量'` 会走 Index Scan。改成 unnest 半连接后同一批 200 个真实 id
// 从 15.85~17.43 s 降到 1.77~2.20 s，2026_09 分区走
// `Index Scan using request_logs_bodies_2026_09_pkey`。详见审计报告 §8.3.3。
//
// ⚠️ 改这个谓词前先读上面那张对照表：`= ANY` 长得就像半连接，能通过任何
// 「不许有 JOIN」的文本判据。守形门见 session_bodies_pairing_test.go。
//
// # S4 停写后的取数源（2026-10-13 复审计轮）

// 本查询的消费方（session_compare）turns 腿读的就是会话族
// （db.SessionFamilyTurnsForSessionSQL），bodies 腿必须同源：停写后新会话
// 的正文只存在于 session_bodies（实测翻转后 _request_logs_bodies 冻结、
// session_only 行 300+），继续读 v1 视图会让这些轮次的正文静默为空。
// 故**不走** db.SessionBodiesSourceSQL() 的运行时开关（其 v1 臂在停写后
// 对会话族 turns 是错的源），直接固定会话族源。列形状差异
// （request_delta 是消息数组、v1 request_body 是带 messages 键的完整对象）
// 由消费方的解析器兼容（parseMessagesFromBody/parseResponseMessages 的
// 数组臂，2026-10-13 同轮补齐）。
var sessionBodiesByRequestIDSQL = `
	SELECT rb.request_id,
	       rb.request_body,
	       rb.outbound_body,
	       rb.response_body
	FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb
	WHERE rb.request_id IN (SELECT unnest($1::text[]))
`

// v1BodiesByRequestIDSQL 是 session_summary_v2 的 **回退路径**专用的正文腿。
//
// 2026-10-20（迁移 890）：v1 族整体退役 ⇒ 这条语句的取数源从 v1 bodies
// 视图换成**会话族正文源**（与主批量取数器同源）。
//
// 为什么换源而不是删掉整条回退路径：回退路径的 turns 腿读的是 canonical
// 视图（`request_logs_with_current_month`），而该视图在 890 之后是
// session-only 的两臂体 ⇒ 遗留会话（镜像链开启之前、只存在于 v1 的那些）
// 在 turns 腿就查不到行，`queryRequestLogsFallback` 会在
// 「len(keys)==0」处提前返回，正文腿根本不会被调用。
// ⇒ 未镜像遗留会话的摘要语料随 v1 表一起消失（已登记的可接受损失），
// 而 bodies 腿必须**存在**（admin/session_summary_v2.go 仍调用
// queryV1BodiesByRequestID），所以它的取数源一并换成会话族。
//
// ⚠ 标识符里的 "v1" 现在是**历史名**：它记录的是「这条腿曾经服务 v1
// 回退路径」这个事实，不再表示它读 v1。改名要连同 admin/session_summary_v2.go
// 的调用点一起动（不在本次改动的所有权范围内）。
// ⚠ var 而不是 const：语句里拼了 db.SessionFamilyBodiesSourceSQL()（函数调用
// 不是常量表达式，const 会编译不过）。主批量取数器 sessionBodiesByRequestIDSQL
// 同理是 var。
var v1BodiesByRequestIDSQL = `
	SELECT rb.request_id,
	       rb.request_body,
	       rb.outbound_body,
	       rb.response_body
	FROM ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb
	WHERE rb.request_id IN (SELECT unnest($1::text[]))
`

// sessionBodiesByRequestIDAndTSSQL reproduces the tuple pairing the summary
// path has always used. It is kept so that refactor stays
// behaviour-preserving; see the note above on why it mostly returns nothing.
//
// 这条**本来就是** unnest 半连接形态，因此**不在** 15~17 秒那批里：2026-10-01
// 实测同一批 200 个键走 `Index Scan using request_logs_bodies_2026_09_pkey`、
// 101.302 ms。上一轮曾把它一并列入待改清单，实测后撤销 —— 它没有可改之处，
// 改它只会让 diff 看起来比实际变更大。
//
// 2026-10-20（迁移 890）：v1 族整体退役 ⇒ 取数源从 v1 bodies 视图换成
// 会话族正文源（hot ∪ 父表两腿）。形状契约**原样保留**：仍是 unnest 半连接、
// 仍只绑两个数组参数、不引入 JOIN。
//
// ⚠ 这里内联两腿而没有复用 db.SessionFamilyBodiesSourceSQL()，是因为那条
// helper **刻意不投影 ts**（db 侧头注：两侧 bodies 的 ts 相等率实测 99.85%
// 不等，加 ts 会把一个解析期错误换成一个静默的行数变化）。本条要的正是
// (request_id, ts) 元组形态，所以只能自己写两腿。
//
// ⚠ 本条**没有生产调用方**（仅守形测试引用，§5.9 历史形状钉子）；仓库的
// 退役门（admin/request_logs_bodies_retirement_gate_test.go）登记的是
// 「DROP 时直接删除」。本次保留（而不是删掉）是为了不让 admin 包的**测试**
// 编译失败（admin/session_bodies_pairing_test.go 与
// admin/session_summary_v2_fallback_test.go 都直接引用这个标识符）。
// 要彻底删，请连同 querySessionBodiesByRequestIDAndTS 与那两个测试引用一起删。
const sessionBodiesByRequestIDAndTSSQL = `
	SELECT rb.request_id,
	       rb.ts,
	       rb.request_body,
	       rb.outbound_body,
	       rb.response_body
	FROM (
		SELECT sb.request_id, sb.ts, sb.request_delta AS request_body,
		       sb.outbound_body, sb.response_delta AS response_body
		  FROM session_bodies_hot sb
		UNION ALL
		SELECT sb.request_id, sb.ts, sb.request_delta,
		       sb.outbound_body, sb.response_delta
		  FROM session_bodies sb
	) rb
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

// # 并发闸：正文取数是全仓最贵的单条查询，雪崩从这里来
//
// 2026-10-01 实测（真库，生产 SQL 逐字复制 + pgx 绑定参数）：取一个真实会话
// 的 171 个 request_id 要 **17~19 秒**，而 2026_09 是 2,217,555 行的 Citus
// 列存分区，查询落在 ColumnarScan 上（审计 §8.3）。
//
// 连接池上限 **16**。也就是说：16 个并发请求就能把池占满 17 秒，期间进程内
// 所有其他查询（含完全不碰会话数据的端点）都在等连接。单条 19 秒的查询不是
// 「慢」，是**放大器**。
//
// 闸的行为刻意是**快速失败**而不是排队：排队等于把 17 秒的占用原样放大成
// 雪崩，只是晚一点发生。取不到就报 503，让调用方知道「现在忙」，
// 这比让它挂 17 秒后失败诚实得多。
const maxConcurrentBodyFetches = 4

// bodyFetchGate 容量刻意小于连接池（4 < 16）：把大部分连接留给不碰正文的
// 端点，正文查询只在剩下的少数连接上排队。
var bodyFetchGate = make(chan struct{}, maxConcurrentBodyFetches)

// ErrBodyFetchSaturated 表示正文取数并发已满，调用方应快速失败（503）而不是
// 继续占用连接。
var ErrBodyFetchSaturated = errors.New("session body fetch saturated: too many concurrent scans of the columnar bodies partitions")

// acquireBodyFetchSlot 非阻塞地取一个名额。ctx 只用于「取名额前已取消」这一种
// 情况 —— 它**不**用来排队，因为排队就是我们要避免的行为。
func acquireBodyFetchSlot(ctx context.Context) (func(), error) {
	select {
	case bodyFetchGate <- struct{}{}:
		return func() { <-bodyFetchGate }, nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrBodyFetchSaturated
}

// querySessionBodiesByRequestID pairs turns to bodies on request_id alone.
// Unambiguous: no request_id carries more than one body row (measured: 0),
// because every partition of _request_logs_bodies has a UNIQUE (request_id, ts)
// primary key and the hot table is disjoint from the monthly parent.
func querySessionBodiesByRequestID(
	ctx context.Context,
	q sessionBodyQuerier,
	requestIDs []string,
) (map[string]sessionBody, error) {
	return queryBodiesByRequestID(ctx, q, sessionBodiesByRequestIDSQL, requestIDs)
}

// queryV1BodiesByRequestID is the v1-view sibling for the summary fallback
// path（turns 腿为 v1 视图的遗留会话）。见 v1BodiesByRequestIDSQL 注释。
func queryV1BodiesByRequestID(
	ctx context.Context,
	q sessionBodyQuerier,
	requestIDs []string,
) (map[string]sessionBody, error) {
	return queryBodiesByRequestID(ctx, q, v1BodiesByRequestIDSQL, requestIDs)
}

// queryBodiesByRequestID is the shared scanner behind both source variants.
// The concurrency gate applies to both：正文取数是全仓最贵的单条查询，
// 与取数源无关（见下方并发闸注释）。
func queryBodiesByRequestID(
	ctx context.Context,
	q sessionBodyQuerier,
	sqlText string,
	requestIDs []string,
) (map[string]sessionBody, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}
	release, err := acquireBodyFetchSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := q.Query(ctx, sqlText, requestIDs)
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
