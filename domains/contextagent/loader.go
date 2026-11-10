package contextagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// Rows 是本包需要的最小行迭代能力（pgx.Rows 天然满足这四个方法）。
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Queryer 是本包需要的最小 PG 读能力。
//
// ★ 与 domains/session/v2 的 turnReaderDB 同形（Query/QueryRow），但本包
//
//	自带一份窄接口，避免跨包依赖 v2 的未导出类型；并把返回类型收窄成
//	Rows ⇒ 单测可以塞一个假实现，不必连真库。
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// PGQueryer 把 *pgxpool.Pool 适配到本包的窄接口。
type PGQueryer struct {
	Pool *pgxpool.Pool
}

// Query 实现 Queryer。
func (q PGQueryer) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	if q.Pool == nil {
		return nil, fmt.Errorf("contextagent: pg pool is nil")
	}
	rows, err := q.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// StepDetailLoader 取回步骤明细（L1 层）。
type StepDetailLoader interface {
	// LoadSteps 返回指定步骤号的明细，按步骤号升序。
	// 未命中的步骤不返回条目（不报错）——记忆缺失是常态，不是异常。
	LoadSteps(ctx context.Context, id Identity, steps []int) ([]StepDetail, error)
}

// StepDetail 是一步的明细。
type StepDetail struct {
	// StepIndex 是步骤号（= 会话族 turn_no）。
	StepIndex int `json:"step_index"`
	// Messages 是该步的消息（请求增量 + 模型回复），按序。
	Messages []map[string]any `json:"messages"`
	// Tokens 是该步的估算 token 数。
	Tokens int `json:"tokens"`
}

// IndexLoader 取回 L0 索引层（每步一行「目标 → 结果」）。
type IndexLoader interface {
	// LoadIndex 返回该会话的逐步摘要，按 step_index 升序。
	LoadIndex(ctx context.Context, id Identity) ([]IndexEntry, error)
}

// IndexEntry 是一步的索引条目。
type IndexEntry struct {
	StepIndex int    `json:"step_index"`
	Goal      string `json:"goal"`
	Result    string `json:"result"`
}

// LongTermLoader 做 L2 语义召回。
type LongTermLoader interface {
	// Recall 按命名空间 + 查询串召回长期记忆。
	Recall(ctx context.Context, namespace, query string, topK int) ([]memory.Memory, error)
}

// ── PG 实现 ────────────────────────────────────────────────────────────────

// PGStepDetailLoader 从会话族正文表按 turn_no 精确取回步骤明细。
//
// ★ 权威源：public.session_bodies（每轮 request_delta / response_delta）。
//
//	视图 session_bodies_unified（hot ∪ 分区）与 domains/session/v2 的
//	TurnReader.LoadChain 用的是同一张源 ⇒ 与「按需取回最近 N 轮」口径一致。
//
// ★ ★ 诚实边界（重要）：仓内已登记的 FIXME-14372/14373 指出，生产上
//
//	request_delta 常常**不是**「本轮新增」而是「客户端完整历史」
//	（outbound_body 为 jsonb 'null' 时）。因此本 loader 取回的内容
//	**可能是整份历史的重复**，而不是干净的增量。
//	⇒ 本包对同一轮的 request_delta 做了**去重**（按 role+content 指纹），
//	并且把去重前后的字节数都记进 LayerContent.Sources，让上层能看见
//	「这次加载回来的东西有多少是重复的」。
//	⇒ 真正的修法是改 extractRequestDelta 的差集基准（属主决策⑭/⑮），
//	本轮**不改生产写入路径**。
type PGStepDetailLoader struct {
	DB Queryer
}

// NewPGStepDetailLoader 构造 PG 步骤明细加载器。
func NewPGStepDetailLoader(db Queryer) *PGStepDetailLoader {
	return &PGStepDetailLoader{DB: db}
}

const sqlLoadStepDetails = `
	SELECT b.turn_no, b.request_delta, b.response_delta
	FROM public.session_bodies_unified b
	WHERE b.tenant_id = $1 AND b.session_id = $2 AND b.turn_no = ANY($3)
	ORDER BY b.turn_no ASC
`

// LoadSteps 实现 StepDetailLoader。
func (l *PGStepDetailLoader) LoadSteps(ctx context.Context, id Identity, steps []int) ([]StepDetail, error) {
	if l == nil || l.DB == nil || id.Session == "" || len(steps) == 0 {
		return nil, nil
	}
	rows, err := l.DB.Query(ctx, sqlLoadStepDetails, id.TenantID, id.Session, steps)
	if err != nil {
		return nil, fmt.Errorf("contextagent: load step details: %w", err)
	}
	defer rows.Close()

	var out []StepDetail
	for rows.Next() {
		var turnNo int
		var reqDelta, respDelta []byte
		if err := rows.Scan(&turnNo, &reqDelta, &respDelta); err != nil {
			return nil, fmt.Errorf("contextagent: scan step detail: %w", err)
		}
		msgs, dup := mergeDeltas(reqDelta, respDelta)
		out = append(out, StepDetail{
			StepIndex: turnNo,
			Messages:  msgs,
			Tokens:    EstimateTokens(messagesText(msgs)),
		})
		_ = dup
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("contextagent: iterate step details: %w", err)
	}
	return out, nil
}

// mergeDeltas 合并一轮的请求增量与回复，并按「角色+内容」指纹去重。
//
// ★ 去重理由见 PGStepDetailLoader 的诚实边界：生产上 request_delta 可能是
//
//	整份历史，与 response_delta 里的消息重复。
//	指纹 = role \x00 content（与仓内 messageKey 同思路，但这里只要粗粒度去重）。
func mergeDeltas(reqDelta, respDelta []byte) ([]map[string]any, int) {
	seen := map[string]bool{}
	var out []map[string]any
	dup := 0
	appendRaw := func(raw []byte) {
		if len(raw) == 0 {
			return
		}
		var msgs []map[string]any
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return
		}
		for _, m := range msgs {
			key := messageFingerprint(m)
			if key != "" && seen[key] {
				dup++
				continue
			}
			if key != "" {
				seen[key] = true
			}
			out = append(out, m)
		}
	}
	appendRaw(reqDelta)
	appendRaw(respDelta)
	return out, dup
}

// messageFingerprint 算一条消息的粗粒度指纹。
func messageFingerprint(m map[string]any) string {
	role, _ := m["role"].(string)
	content, _ := m["content"].(string)
	if content == "" {
		// 有 tool_calls 的消息用 tool_call id 兜底。
		if calls, ok := m["tool_calls"].([]any); ok && len(calls) > 0 {
			if c0, ok := calls[0].(map[string]any); ok {
				if id, ok := c0["id"].(string); ok {
					return role + "\x00tc:" + id
				}
			}
		}
		return role + "\x00"
	}
	return role + "\x00" + content
}

// PGIndexLoader 从逐步摘要表取回 L0 索引层。
//
// ★ 权威源：public.session_request_summaries
//
//	（gw_session_id / step_index / request_summary / response_summary）。
//	该表由 domains/analysis/request_summary.go 的 RequestSummarizer 写入，
//	触发点是 domains/hooks/sessionanalysis 的 PostResponse Hook。
type PGIndexLoader struct {
	DB Queryer
}

// NewPGIndexLoader 构造 PG 索引层加载器。
func NewPGIndexLoader(db Queryer) *PGIndexLoader {
	return &PGIndexLoader{DB: db}
}

const sqlLoadIndex = `
	SELECT step_index, COALESCE(request_summary, ''), COALESCE(response_summary, '')
	FROM session_request_summaries
	WHERE gw_session_id = $1 AND tenant_id = $2
	ORDER BY step_index ASC
`

// LoadIndex 实现 IndexLoader。
func (l *PGIndexLoader) LoadIndex(ctx context.Context, id Identity) ([]IndexEntry, error) {
	if l == nil || l.DB == nil || id.Session == "" {
		return nil, nil
	}
	rows, err := l.DB.Query(ctx, sqlLoadIndex, id.Session, id.TenantID)
	if err != nil {
		return nil, fmt.Errorf("contextagent: load index: %w", err)
	}
	defer rows.Close()

	var out []IndexEntry
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.StepIndex, &e.Goal, &e.Result); err != nil {
			return nil, fmt.Errorf("contextagent: scan index: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("contextagent: iterate index: %w", err)
	}
	return out, nil
}

// ── Memora 实现 ───────────────────────────────────────────────────────────

// MemoraLongTermLoader 用 Memora 做 L2 语义召回。
//
// ★ Memora 只能做这一层：它的检索请求体是 {user_id, query, top_k}
//
//	（domains/memory/client/client.go），**没有 id/step 寻址**，
//	所以「加载第 N 步」必须走 PG。
type MemoraLongTermLoader struct {
	Reader memory.Reader
}

// NewMemoraLongTermLoader 构造 Memora 长期记忆加载器。
func NewMemoraLongTermLoader(reader memory.Reader) *MemoraLongTermLoader {
	return &MemoraLongTermLoader{Reader: reader}
}

// Recall 实现 LongTermLoader。
func (l *MemoraLongTermLoader) Recall(ctx context.Context, namespace, query string, topK int) ([]memory.Memory, error) {
	if l == nil || l.Reader == nil || l.Reader.Disabled() || namespace == "" || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 8
	}
	return l.Reader.Search(ctx, namespace, query, topK)
}
