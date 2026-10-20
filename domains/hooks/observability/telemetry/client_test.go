package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type boolPointerMatcher struct {
	want bool
}

func (m boolPointerMatcher) Match(value interface{}) bool {
	actual, ok := value.(*bool)
	return ok && actual != nil && *actual == m.want
}

type stringPointerMatcher struct {
	want *string
}

func (m stringPointerMatcher) Match(value interface{}) bool {
	actual, ok := value.(*string)
	if !ok {
		return false
	}
	if m.want == nil {
		return actual == nil
	}
	return actual != nil && *actual == *m.want
}

// requestLogUpdateArgs 构造「请求日志更新」语句的绑定参数。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本 helper 原先构造 v1 宽表
// `_request_logs_hot` 的 UPDATE 绑定参数（100 个占位符，覆盖 quality_* /
// client-perception / t0..t9 / canonical 等 v1-only 列）。v1 族随 890 落库后
// 那条语句**不再存在** ⇒ 现在构造**新写入路径**（usage_ledger_hot）的绑定参数：
//
//   - entry 带真实 token（PromptTokens / CompletionTokens 任一非 nil）⇒ 10 个参数：
//     $1 request_id, $2 prompt, $3 completion, $4 total, $5 cache_read,
//     $6 cache_write, $7 cost, $8 latency, $9 success, $10 error_kind；
//   - 否则 ⇒ 5 个参数：$1 request_id, $2 latency, $3 success, $4 error_kind,
//     $5 cost。
//
// success / error_kind 两列用精确匹配器钉住（不是 AnyArg）⇒ 变体选错时
// 判据会红，而不是「照样绿」。
//
// ⚠ 本轮只改 client_test.go / final_success_claim_test.go；本 helper 的**其它**
// 调用方（body_summary_test.go / body_summary_default_test.go /
// usage_correction_test.go）仍在期望 v1 语句（`UPDATE _request_logs_hot`），
// 需随迁移 890 一并改造——它们不在本轮范围内，此处只保证编译面不破。
func requestLogUpdateArgs(entry RequestLogEntry) []interface{} {
	if entry.PromptTokens != nil || entry.CompletionTokens != nil {
		// 10 参变体：$1 request_id … $9 success, $10 error_kind。
		args := anyArgs(10)
		args[8] = boolPointerMatcher{want: entry.Success}
		args[9] = stringPointerMatcher{want: entry.ErrorKind}
		return args
	}
	// 5 参变体：$1 request_id, $2 latency, $3 success, $4 error_kind, $5 cost。
	args := anyArgs(5)
	args[2] = boolPointerMatcher{want: entry.Success}
	args[3] = stringPointerMatcher{want: entry.ErrorKind}
	return args
}

// ── 2026-10-20（迁移 890，v1 族退役）：SQL 录制器 ──────────────────────────
//
// 用途：把「生产代码实际发出的 SQL 文本」抓下来，让判据直接对着**真实语句**
// 下结论，而不是对着测试文件里自己抄的一份字面量。
//
// 为什么需要它：本文件此前有判据断言的是**测试文件内部**的 SQL 字面量
// （TestRequestLogsUpdateSQL_SetClauseDoesNotReferenceTargetAlias 等）——
// 那份字面量与生产代码之间没有任何耦合：字面量写错、表被删、生产语句改了，
// 它都照样绿 ⇒ 那是「判据钉死了一个不存在的东西」的典型形态
// （见 LESSONS.md 教训 17：判据会把错误行为钉死，而且带着注释显得很有理）。
//
// ⚠ 录制点选在 **requestLogDB 接口边界**（而不是 pgxmock 的 QueryMatcher）：
// 匹配器只在「候选期望」上被调用——若某条语句没有对应的期望（严格模式下会被
// mock 拒绝），匹配器**根本不会被触发** ⇒ 那条语句录不到。
// 而这恰恰是最危险的盲区：lookupTurnNumber 读失败会吞掉错误返回 1，于是
// 「v1 回退腿复活」既不会让 mock 报错、也不会让返回值变化 ⇒ 假绿。
// （2026-10-20 实测：复活回退腿后，用匹配器录制的判据**全绿**——改到接口
// 边界录制后，同一个变异才被抓住。）
type sqlRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *sqlRecorder) record(sql string) {
	r.mu.Lock()
	r.queries = append(r.queries, sql)
	r.mu.Unlock()
}

// recorded 返回已录制到的全部 SQL 原文（按发生顺序）。
func (r *sqlRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.queries))
	copy(out, r.queries)
	return out
}

// recordingDB 在 requestLogDB 接口边界上录制**每一条**生产代码发出的语句
// （包括严格模式下被 mock 拒绝的那些——它们同样被记下来）。
type recordingDB struct {
	inner    requestLogDB
	recorder *sqlRecorder
}

func (d *recordingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.recorder.record(sql)
	return d.inner.Exec(ctx, sql, args...)
}

func (d *recordingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	d.recorder.record(sql)
	return d.inner.QueryRow(ctx, sql, args...)
}

func (d *recordingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &recordingTx{Tx: tx, recorder: d.recorder}, nil
}

// recordingTx 同理：事务内的语句也必须录到（lookupTurnNumber 走的是 tx）。
type recordingTx struct {
	pgx.Tx
	recorder *sqlRecorder
}

func (t *recordingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.recorder.record(sql)
	return t.Tx.Exec(ctx, sql, args...)
}

func (t *recordingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	t.recorder.record(sql)
	return t.Tx.QueryRow(ctx, sql, args...)
}

// newRecordingDB 建一个「会录制全部 SQL」的 requestLogDB 包装 + 底层 mock。
// 返回值：mock（设置期望用）、recordingDB（注入 Client 用）、recorder（检查用）。
func newRecordingDB(t *testing.T) (pgxmock.PgxPoolIface, requestLogDB, *sqlRecorder) {
	t.Helper()
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(func() { mockDB.Close() })
	recorder := &sqlRecorder{}
	return mockDB, &recordingDB{inner: mockDB, recorder: recorder}, recorder
}

// ★ 2026-10-20（迁移 890，v1 族退役）：TestUpsertRequestLogBodies_UsesBodyOnlyColumns
// **已退役**（本函数整条删除）。
//
// 退役对象：upsertRequestLogBodies —— v1 正文族（_request_logs_bodies_hot /
// _request_logs_bodies / _request_logs_bodies_archive）的写入函数，随 890
// 连同 v1 正文族一起删除。v1 族落库后再写它必然 42P01。
//
// 为什么退役而不是改指：它守的不变式是「正文只写正文列（request_body /
// response_body / outbound_body），不污染主表」——这个不变式在 v1 族存在时
// 有意义（主表与正文表分家），v1 正文族退役后**本包不再有任何 PG 正文写入**
// （正文落点已迁到会话族 session_bodies / session_bodies_hot，由
// internal/sessionv2mirror 承接），主表与正文表在 PG 侧不再分家 ⇒ 原来的
// 「只写正文列」已无对象可指。
//
// 现在由什么承载：本包侧**没有任何判据**守着 PG 正文的落点（正文落点归
// internal/sessionv2mirror 与 domains/session/v2 各自的判据）。本包侧仍然
// 守着的相邻不变式是「正文在事务提交后才释放」——由
// TestPersistRequestLog_ReleasesBodiesAfterPersistedHooks 继续守着。
func TestUpsertRequestLogBodies_RetiredWithV1BodyFamily(t *testing.T) {
	// 占位断言：保留一个「退役登记」用例，使退役这件事在测试清单里可见，
	// 而不是变成一段无人引用的注释。
	t.Log("upsertRequestLogBodies 随迁移 890（v1 正文族退役）整条删除；" +
		"正文落点迁到会话族 session_bodies / session_bodies_hot，" +
		"由 internal/sessionv2mirror 与 domains/session/v2 承接。")
}

func TestInsertSessionOpenedEvent_IsIdempotent(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	tx, err := mockDB.Begin(context.Background())
	require.NoError(t, err)
	opened, err := outbox.BuildSessionOpenedEventV1("tenant-1", "session-1", "42")
	require.NoError(t, err)

	mockDB.ExpectExec(`INSERT INTO outbox_events[\s\S]*ON CONFLICT \(event_id\) DO NOTHING`).
		WithArgs(
			opened.EventID, opened.EventType, opened.SchemaVersion, opened.TenantID,
			opened.AggregateID, opened.AggregateVersion, opened.OccurredAt, pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, insertSessionOpenedEvent(context.Background(), tx, opened, "req-1"))
	mockDB.ExpectRollback()
	require.NoError(t, tx.Rollback(context.Background()))
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// ★ 2026-10-20（迁移 890，v1 族退役）：本用例原先期望 v1 宽表的两条语句
// （`UPDATE _request_logs_hot` 100 参 + `INSERT INTO _request_logs_bodies_hot`），
// 那两条随 890 整段退役 ⇒ 改为期望**新写入路径**（usage_ledger_hot 的 UPDATE），
// 并直接检查**生产代码实际发出的** SQL 文本。
//
// 不变式（仍然成立，仍由本用例守着）：终态成功可以覆盖此前的中间失败——
// 新语句用 `success = COALESCE($3, success)` / `error_kind = COALESCE($4, error_kind)`
// 落终态，不做「只在失败时才替换」的分支。
func TestUpdateRequestLog_AllowsTerminalSuccessToReplaceIntermediateFailure(t *testing.T) {
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-update", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	status := RequestStatusSuccess
	client := &Client{requestLogDB: recording}
	err := client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-update",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())

	// 对**生产代码实际发出的** SQL 下结论（不是对着测试文件里的字面量）。
	// 正向对照：录制器必须抓到这条 UPDATE——「没抓到」与「没有别名/没有 v1 表」
	// 长得不一样，所以长度与内容都要断言。
	statements := recorder.recorded()
	require.Len(t, statements, 1, "updateRequestLog 应当只发一条 UPDATE")
	updateSQL := statements[0]
	require.Contains(t, updateSQL, "UPDATE usage_ledger_hot")
	require.Contains(t, updateSQL, "success = COALESCE($3, success)")
	require.Contains(t, updateSQL, "error_kind = COALESCE($4, error_kind)")
}

func TestMergeRequestLogEntry_PreservesProtocolMetadata(t *testing.T) {
	client := "openai-completions"
	upstream := "anthropic-messages"
	converted := true
	dst := &RequestLogEntry{RequestID: "request-1"}
	src := &RequestLogEntry{
		ClientProtocol:     &client,
		UpstreamProtocol:   &upstream,
		ProtocolConversion: &converted,
	}

	mergeRequestLogEntry(dst, src)

	if dst.ClientProtocol == nil || *dst.ClientProtocol != client {
		t.Fatalf("client protocol = %v, want %q", dst.ClientProtocol, client)
	}
	if dst.UpstreamProtocol == nil || *dst.UpstreamProtocol != upstream {
		t.Fatalf("upstream protocol = %v, want %q", dst.UpstreamProtocol, upstream)
	}
	if dst.ProtocolConversion == nil || !*dst.ProtocolConversion {
		t.Fatalf("protocol conversion = %v, want true", dst.ProtocolConversion)
	}
}

// ★ 2026-10-20（迁移 890，v1 族退役）：TestUpdateRequestLog_TerminalGuardDistinguishesNoOpFromMissing
// **已退役**（表驱动用例整条删除）。
//
// 退役对象：「终态守卫把『无操作』与『行缺失』区分开」这个判别。它的唯一
// 用途是决定「UPDATE 命中 0 行之后要不要回落到 INSERT」（`SELECT EXISTS` +
// 回落 INSERT 分支）——该分支随 v1 族整段删除：新的 UPDATE 只改已有行，
// 行不存在时**不**回落 INSERT（事实落点由会话族镜像承接）。
// ⇒ 「无操作 vs 行缺失」在新写入路径上不再产生不同行为，判据失去了被测对象。
//
// 现在由什么承载：
//
//	① 「行缺失不回落 INSERT」⇒ TestUpdateRequestLog_MissingRowDoesNotFallBackToInsert
//	   （带正向对照：insertRequestLog 确实会 INSERT）。
//	② 「终态与否不再改变语句集合」⇒ TestUpdateRequestLog_UsesTokenEnrichmentVariantOnlyWhenTokensPresent
//	   （新写入路径只有两个变体，选择依据是「entry 是否带真实 token」，
//	   与终态无关）。
func TestUpdateRequestLog_TerminalGuardRetiredWithV1InsertFallback(t *testing.T) {
	// 占位断言：保留一个「退役登记」用例，使退役这件事在测试清单里可见，
	// 而不是变成一段无人引用的注释。
	t.Log("终态守卫的『无操作 vs 行缺失』判别随 v1 的 INSERT 回落分支整段退役；" +
		"新写入路径的 UPDATE 永远只改已有行，语句集合由 token 是否存在决定。")
}

// ★ 2026-10-20（迁移 890，v1 族退役）：新写入路径有**两个** UPDATE 变体——
// 带真实 token 时走 10 参（含 token / 缓存 token / cost / latency / success /
// error_kind），不带 token 时走 5 参（latency / success / error_kind / cost）。
// 变体选择只看「entry 是否带真实 token」，与是否终态无关。
//
// 判据强度：pgxmock 的参数个数是**严格**的——期望 5 参而生产发 10 参（或反过来）
// 会让本用例变红 ⇒ 变体选错不会被放过。
func TestUpdateRequestLog_UsesTokenEnrichmentVariantOnlyWhenTokensPresent(t *testing.T) {
	promptTokens, completionTokens := 11, 22
	cases := []struct {
		name     string
		entry    RequestLogEntry
		wantArgs int
	}{
		{
			name:     "no token fields => slim 5-arg variant",
			entry:    RequestLogEntry{RequestID: "req-slim", Success: true},
			wantArgs: 5,
		},
		{
			name:     "prompt tokens present => 10-arg enrichment variant",
			entry:    RequestLogEntry{RequestID: "req-fat-p", Success: true, PromptTokens: &promptTokens},
			wantArgs: 10,
		},
		{
			name:     "completion tokens present => 10-arg enrichment variant",
			entry:    RequestLogEntry{RequestID: "req-fat-c", Success: true, CompletionTokens: &completionTokens},
			wantArgs: 10,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mockDB.Close()

			mockDB.ExpectBegin()
			args := make([]interface{}, tc.wantArgs)
			for index := range args {
				args[index] = pgxmock.AnyArg()
			}
			mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs(args...).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mockDB.ExpectCommit()

			client := &Client{requestLogDB: mockDB}
			entry := tc.entry
			require.NoError(t, client.updateRequestLog(&entry))
			require.NoError(t, mockDB.ExpectationsWereMet())
		})
	}
}

// ★ 2026-10-20（迁移 890，v1 族退役）：本用例**方向反转**。
//
// 原用例（TestUpdateRequestLog_MissingRequestFallsBackToInsert）断言的是
// 「UPDATE 命中 0 行 ⇒ `SELECT EXISTS` 查不到 ⇒ 回落到 INSERT」。该回落分支
// 随 v1 族整段删除——新写入路径的 UPDATE **只**改已有行，行不存在时**不**
// 回落 INSERT（事实落点由会话族镜像承接）。
//
// 反转后的不变式：行不存在时**绝不**发出 INSERT。
//
// 正向对照（方法纪律第 2 条：否定判据必须带同测的正向对照）：同一个测试里
// 用 insertRequestLog 跑一遍，证明「INSERT INTO usage_ledger_hot」这条语句
// **确实会被录制到**——否则「检查没跑成」与「检查通过」长得一模一样。
func TestUpdateRequestLog_MissingRowDoesNotFallBackToInsert(t *testing.T) {
	// ── 第一段：UPDATE 命中 0 行（= 目标行不存在）──
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-missing", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: recording}
	err := client.updateRequestLog(&RequestLogEntry{
		RequestID: "req-missing",
		Success:   false,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())

	statements := recorder.recorded()
	require.Len(t, statements, 1, "updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	for _, statement := range statements {
		require.NotContains(t, statement, "INSERT INTO usage_ledger_hot",
			"updateRequestLog 在目标行不存在时不得回落 INSERT")
	}

	// ── 正向对照：insertRequestLog 确实会发出 INSERT ──
	mockDB2, recording2, recorder2 := newRecordingDB(t)
	mockDB2.ExpectBegin()
	insertArgs := make([]interface{}, 19)
	for index := range insertArgs {
		insertArgs[index] = pgxmock.AnyArg()
	}
	mockDB2.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(insertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB2.ExpectCommit()

	client2 := &Client{requestLogDB: recording2}
	require.NoError(t, client2.insertRequestLog(&RequestLogEntry{RequestID: "req-present"}))
	require.NoError(t, mockDB2.ExpectationsWereMet())

	inserted := false
	for _, statement := range recorder2.recorded() {
		if strings.Contains(statement, "INSERT INTO usage_ledger_hot") {
			inserted = true
			break
		}
	}
	require.True(t, inserted,
		"正向对照失效：insertRequestLog 应当发出 INSERT INTO usage_ledger_hot（否则上面的否定判据是假绿）")
}

func TestClient_StopIsIdempotent(t *testing.T) {
	c := newClientWithBufSize(2)

	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			c.Stop()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	c.Stop()
}
func TestClient_Disabled(t *testing.T) {
	c := NewClient()
	if c.Enabled() {
		t.Fatal("no DB should be disabled")
	}
	c.EmitDecisionLog(&DecisionLogEntry{RequestID: "test"})
	c.EmitRequestLog(&RequestLogEntry{RequestID: "test"})
	c.Stop()
}

func TestClient_QueueFull(t *testing.T) {
	c := newClientWithBufSize(2)

	for i := 0; i < 10; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{
			RequestID: "overflow",
			Model:     "test",
			Success:   true,
		})
	}

	c.Stop()
}

func TestClient_QueueFull_SyncFallback(t *testing.T) {
	c := newClientWithBufSize(1)

	// Fill the queue so the next Emit hits the default (sync) path.
	// Worker doesn't drain during test, so buffer fills after 1 item.
	c.EmitDecisionLog(&DecisionLogEntry{RequestID: "fill", Model: "test", Success: true})

	// This emit should hit the default case (sync insert) without blocking.
	c.EmitDecisionLog(&DecisionLogEntry{
		RequestID: "sync",
		Model:     "test",
		Success:   true,
	})

	c.Stop()
}

func TestClient_EmitDoesNotBlock(t *testing.T) {
	c := NewClient()
	start := time.Now()
	for i := 0; i < 100; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{RequestID: "bench", Model: "test", Success: true})
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Emit should not block")
	}
	c.Stop()
}

func TestResolveRequestStatus(t *testing.T) {
	t.Parallel()
	errKind := "timeout"
	cases := []struct {
		name      string
		success   bool
		errorKind *string
		initial   bool
		want      string
	}{
		{name: "success", success: true, want: RequestStatusSuccess},
		{name: "failure", success: false, errorKind: &errKind, want: RequestStatusFailure},
		{name: "initial in progress", success: false, initial: true, want: RequestStatusInProgress},
		{name: "update without error still in progress", success: false, initial: false, want: RequestStatusInProgress},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveRequestStatus(tc.success, tc.errorKind, tc.initial); got != tc.want {
				t.Fatalf("ResolveRequestStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ★ 2026-10-20（迁移 890，v1 族退役）：TestRequestLogsUpdateSQL_SetClauseDoesNotReferenceTargetAlias
// **已退役**（原用例整条删除），并由下面的
// TestUpdateRequestLog_ProductionSQLTargetsUsageLedgerHotWithoutAlias 接手。
//
// 退役对象：一份**写在测试文件内部**的 SQL 字面量
// （`UPDATE _request_logs_hot ... FROM latest ...`）。两个问题：
//
//	① 它描述的表已随 890 删除，字面量从此指向一个不存在的东西；
//	② 它与生产代码**没有任何耦合**——字面量写错、表被删、生产语句改了，
//	   它都照样绿。这是「判据钉死了一个不存在的东西」的典型形态
//	   （LESSONS.md 教训 17：判据会把错误行为钉死，还带着注释显得很有理）。
//
// ⇒ 断言对象换成**生产代码实际发出的 SQL**（录制器抓到的原文）。
//
// 现在由什么承载：TestUpdateRequestLog_ProductionSQLTargetsUsageLedgerHotWithoutAlias
// ——同一不变式（UPDATE 必须指向 canonical 目标、SET 子句不得引用目标别名）
// 改为对着生产语句下结论，并带正向对照。
func TestRequestLogsUpdateSQL_SetClauseDoesNotReferenceTargetAlias_Retired(t *testing.T) {
	// 占位断言：保留一个「退役登记」用例，使退役这件事在测试清单里可见，
	// 而不是变成一段无人引用的注释。
	t.Log("断言对象从『测试文件内的 SQL 字面量』换成『生产代码实际发出的 SQL』；" +
		"原字面量描述的是随 890 删除的 _request_logs_hot。")
}

// ★ 2026-10-20（迁移 890，v1 族退役）：UPDATE 语句的**真实**形状判据。
//
// 断言对象是生产代码实际发出的 SQL（录制器抓到的原文），不是测试文件里的
// 抄写。两条不变式：
//
//	① UPDATE 必须指向新 canonical 目标 usage_ledger_hot（v1 宽表已退役，
//	   写它必然 42P01）；
//	② SET 子句不得引用目标别名（`rl.` 之类）——UPDATE 没有 FROM 臂，
//	   带别名会报 "invalid reference to FROM-clause entry"；同时也钉住
//	   「不得引用任何 v1 表」。
//
// 正向对照：录制器必须抓到这条 UPDATE（长度 + 内容都断言）——否则
// 「没抓到语句」与「语句里没有别名」长得一模一样。
func TestUpdateRequestLog_ProductionSQLTargetsUsageLedgerHotWithoutAlias(t *testing.T) {
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-alias", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: recording}
	require.NoError(t, client.updateRequestLog(&RequestLogEntry{RequestID: "req-alias", Success: true}))
	require.NoError(t, mockDB.ExpectationsWereMet())

	statements := recorder.recorded()
	require.Len(t, statements, 1, "updateRequestLog 应当只发一条 UPDATE（正向对照：录制器抓到了语句）")
	updateSQL := statements[0]

	require.Contains(t, updateSQL, "UPDATE usage_ledger_hot",
		"UPDATE 必须指向新 canonical 目标 usage_ledger_hot")
	setIdx := strings.Index(updateSQL, "SET ")
	require.Greater(t, setIdx, 0, "UPDATE 必须有 SET 子句")
	setClause := updateSQL[setIdx:]
	require.NotContains(t, setClause, "rl.",
		"SET 子句不得引用目标别名（UPDATE 无 FROM 臂，带别名会报 invalid reference to FROM-clause entry）")
	require.NotContains(t, updateSQL, "_request_logs",
		"写入路径不得再引用随 890 退役的 v1 族")
}

// ★ 2026-10-20（迁移 890，v1 族退役）：TestRequestLogsHotTarget_QualityColumnsUseHotTable
// **已退役**（原用例整条删除）。
//
// 退役对象：一份**写在测试文件内部**的 SQL 字面量，断言「quality_flags /
// quality_fix_actions 随 INSERT/UPDATE 一起迁到 hot 表」。
//
// 为什么退役而不是改指：
//
//	① 断言对象是测试文件里的抄写，与生产代码没有耦合（字面量与生产语句各说各话）；
//	② quality_flags / quality_fix_actions 是 **v1-only 列**——v1 族随 890 删除后，
//	   新 canonical 目标 usage_ledger_hot **没有这两列**（见
//	   sql/objects/tables/usage_ledger_hot.sql），所以「迁到新表」这个不变式
//	   本身已不存在。
//
// 现在由什么承载：**本包侧没有任何判据**守着 quality_* 的落点——它们随 v1
// 一起消失了（新写入路径不写这两列，会话族也没有对应列）。
// ⇒ 这一点已作为**发现**上报父会话（见交付报告）：RequestLogEntry 仍带
// QualityFlags / QualityFixActions 字段，但生产路径已无落点。
// 相邻的、由本包继续守着的不变式是「写入路径只碰 usage_ledger_hot / api_keys
// / outbox_events 三类表」⇒ TestTelemetryWritePath_IssuesNoV1FamilySQL。
func TestRequestLogsHotTarget_QualityColumnsUseHotTable_Retired(t *testing.T) {
	// 占位断言：保留一个「退役登记」用例，使退役这件事在测试清单里可见，
	// 而不是变成一段无人引用的注释。
	t.Log("quality_flags / quality_fix_actions 是 v1-only 列，随 890 退役；" +
		"usage_ledger_hot 无对应列，本包不再有落点可指。")
}

// ★ 2026-10-20（迁移 890，v1 族退役）：telemetry 的 PG 写入路径**不得**再碰
// v1 族（_request_logs / _request_logs_hot / _request_logs_bodies* /
// request_logs*）——890 落库后写/读它们必然 42P01。
//
// 这是「否定判据」，因此**必须带正向对照**（方法纪律第 2 条）：
//   - 否定面：录制到的全部 SQL 里不得出现任何 v1 表名；
//   - 正向面：同一次录制里必须确实抓到新目标的语句（usage_ledger_hot），
//     否则「检查没跑成」与「检查通过」长得一模一样。
//
// 另有一层对照：另一个用例（TestUpdateRequestLog_MissingRowDoesNotFallBackToInsert）
// 证明 `INSERT INTO usage_ledger_hot` 这条语句本身是可被检出的。
//
// ⚠ 两条腿都要覆盖（UPDATE 与 INSERT）：2026-10-20 的变异验证里，把
// insertRequestLog 的目标改成 `_request_logs_hot` 时，只覆盖 UPDATE 腿的版本
// **全绿**——漏检。⇒ 现在插入腿与更新腿各自跑一遍，缺哪条都红。
func TestTelemetryWritePath_IssuesNoV1FamilySQL(t *testing.T) {
	t.Run("update path", func(t *testing.T) {
		mockDB, recording, recorder := newRecordingDB(t)

		mockDB.ExpectBegin()
		mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
			WithArgs("req-no-v1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mockDB.ExpectCommit()

		client := &Client{requestLogDB: recording}
		require.NoError(t, client.updateRequestLog(&RequestLogEntry{RequestID: "req-no-v1", Success: true}))
		require.NoError(t, mockDB.ExpectationsWereMet())

		statements := recorder.recorded()
		require.NotEmpty(t, statements, "正向对照失效：录制器没有抓到任何语句，否定判据无从谈起")

		sawCanonicalTarget := false
		for _, statement := range statements {
			require.NotContains(t, statement, "_request_logs",
				"telemetry 写入路径不得再引用随 890 退役的 v1 族")
			require.NotContains(t, statement, "request_logs_bodies",
				"telemetry 写入路径不得再引用随 890 退役的 v1 正文族")
			if strings.Contains(statement, "usage_ledger_hot") {
				sawCanonicalTarget = true
			}
		}
		require.True(t, sawCanonicalTarget,
			"正向对照失效：录制到的语句里必须出现新 canonical 目标 usage_ledger_hot")
	})

	t.Run("insert path", func(t *testing.T) {
		mockDB, recording, recorder := newRecordingDB(t)

		mockDB.ExpectBegin()
		insertArgs := make([]interface{}, 19)
		for index := range insertArgs {
			insertArgs[index] = pgxmock.AnyArg()
		}
		mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
			WithArgs(insertArgs...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mockDB.ExpectCommit()

		client := &Client{requestLogDB: recording}
		require.NoError(t, client.insertRequestLog(&RequestLogEntry{RequestID: "req-no-v1-insert"}))
		require.NoError(t, mockDB.ExpectationsWereMet())

		statements := recorder.recorded()
		require.NotEmpty(t, statements, "正向对照失效：录制器没有抓到任何语句，否定判据无从谈起")

		sawCanonicalTarget := false
		for _, statement := range statements {
			require.NotContains(t, statement, "_request_logs",
				"telemetry 写入路径不得再引用随 890 退役的 v1 族")
			require.NotContains(t, statement, "request_logs_bodies",
				"telemetry 写入路径不得再引用随 890 退役的 v1 正文族")
			if strings.Contains(statement, "usage_ledger_hot") {
				sawCanonicalTarget = true
			}
		}
		require.True(t, sawCanonicalTarget,
			"正向对照失效：录制到的语句里必须出现新 canonical 目标 usage_ledger_hot")
	})
}
func TestNormalizeRequestStatus(t *testing.T) {
	entry := &RequestLogEntry{Op: RequestLogInsert, Success: false}
	normalizeRequestStatus(entry)
	if entry.RequestStatus == nil || *entry.RequestStatus != RequestStatusInProgress {
		t.Fatalf("expected in_progress, got %#v", entry.RequestStatus)
	}
}

func TestMergeRequestLogEntry_PreservesDiscardEventsOnEmptyUpdate(t *testing.T) {
	events := json.RawMessage(`[{"reason":"survival_attempt_discarded","attempt_number":1}]`)
	dst := &RequestLogEntry{RequestID: "req-discard-preserve", DiscardEvents: events}
	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID})
	if string(dst.DiscardEvents) != string(events) {
		t.Fatalf("DiscardEvents = %s, want %s", dst.DiscardEvents, events)
	}
}

func TestMergeRequestLogEntry_PreservesBodiesOnEmptyUpdate(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	dst := &RequestLogEntry{
		RequestID:    "req-body-preserve",
		RequestBody:  &requestBody,
		ResponseBody: &responseBody,
	}
	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: true})
	if dst.RequestBody == nil || *dst.RequestBody != requestBody {
		t.Fatalf("RequestBody = %v, want original body", dst.RequestBody)
	}
	if dst.ResponseBody == nil || *dst.ResponseBody != responseBody {
		t.Fatalf("ResponseBody = %v, want original body", dst.ResponseBody)
	}
}

// ★ 2026-10-20（迁移 890，v1 族退役）：语句集合改为新写入路径——
// 事务里现在只有一条 `UPDATE usage_ledger_hot`（v1 宽表 UPDATE 与 v1 正文族
// INSERT 随 890 整段退役）。本用例守的不变式（正文在事务提交后才释放、
// persisted hook 能读到完整正文）与 v1 无关，保留。
func TestPersistRequestLog_ReleasesBodiesAfterPersistedHooks(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	outboundBody := json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)
	status := RequestStatusSuccess

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-release", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	entry := &RequestLogEntry{
		RequestID:     "req-release",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		RequestBody:   &requestBody,
		ResponseBody:  &responseBody,
		OutboundBody:  outboundBody,
	}
	client := &Client{requestLogDB: mockDB}
	client.AddOnRequestLogPersisted(func(got *RequestLogEntry) {
		require.Equal(t, requestBody, *got.RequestBody)
		require.Equal(t, responseBody, *got.ResponseBody)
		require.Equal(t, outboundBody, got.OutboundBody)
	})

	require.NoError(t, client.persistRequestLog(entry))
	require.Nil(t, entry.RequestBody)
	require.Nil(t, entry.ResponseBody)
	require.Nil(t, entry.OutboundBody)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestPersistRequestLog_KeepsBodiesWhenWriteFails(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	outboundBody := json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin().WillReturnError(pgx.ErrTxClosed)
	entry := &RequestLogEntry{
		RequestID:    "req-retain",
		RequestBody:  &requestBody,
		ResponseBody: &responseBody,
		OutboundBody: outboundBody,
	}
	client := &Client{requestLogDB: mockDB}

	require.Error(t, client.persistRequestLog(entry))
	require.Equal(t, requestBody, *entry.RequestBody)
	require.Equal(t, responseBody, *entry.ResponseBody)
	require.Equal(t, outboundBody, entry.OutboundBody)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestMergeRequestLogEntry_PreservesClientPerceptionFields(t *testing.T) {
	agentName := "zcode"
	agentType := "ide-agent"
	protocol := "anthropic-messages"
	virtualID := "client-123"
	dst := &RequestLogEntry{
		RequestID:       "req-client-fields",
		AgentName:       &agentName,
		AgentType:       &agentType,
		ClientProtocol:  &protocol,
		VirtualClientID: &virtualID,
	}

	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: false})

	require.Equal(t, agentName, *dst.AgentName)
	require.Equal(t, agentType, *dst.AgentType)
	require.Equal(t, protocol, *dst.ClientProtocol)
	require.Equal(t, virtualID, *dst.VirtualClientID)
}

func TestMergeRequestLogEntry_PreservesMirrorDimensions(t *testing.T) {
	projectID := "project-123"
	namespace := "workspace"
	dst := &RequestLogEntry{
		RequestID: "req-mirror-dimensions",
		ProjectID: &projectID,
		Namespace: &namespace,
	}

	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: true})

	require.Equal(t, projectID, *dst.ProjectID)
	require.Equal(t, namespace, *dst.Namespace)
}

func TestMergeRequestLogEntry_ClearsErrorKindOnSuccess(t *testing.T) {
	// 2026-06-20 audit fix: when a failure entry is merged with

	// a success entry, the merged entry's ErrorKind should be
	// nil (matching the SQL CASE that clears it in the DB).
	// This prevents stale error_kind from being logged by any
	// pre-write observability path.
	rateLimit := "rate_limit"
	emptyKind := ""

	// Start with a failure entry (the dst of the first merge).
	failure := &RequestLogEntry{
		RequestID:     "req-1",
		Op:            RequestLogUpdate,
		Success:       false,
		ErrorKind:     &rateLimit,
		RequestStatus: strPtrT("failure"),
	}

	// Then a success update arrives.
	success := &RequestLogEntry{
		RequestID:     "req-1",
		Op:            RequestLogUpdate,
		Success:       true,
		ErrorKind:     &emptyKind, // empty string: "clear it"
		RequestStatus: strPtrT("success"),
	}

	mergeRequestLogEntry(failure, success)

	if !failure.Success {
		t.Error("merged Success should be true")
	}
	if failure.ErrorKind != nil {
		t.Errorf("merged ErrorKind should be nil when Success=true, got %v", *failure.ErrorKind)
	}
	if failure.RequestStatus == nil || *failure.RequestStatus != "success" {
		t.Errorf("RequestStatus should be 'success', got %v", failure.RequestStatus)
	}
}

func TestMergeRequestLogEntry_KeepsErrorKindOnFailure(t *testing.T) {

	// If both entries are failures, error_kind from the latest
	// update wins. This is intentional: each new entry fully
	// overwrites dst via mergeStringPtr's *dst = &v pattern, so
	// the most recent information is preserved. In practice the
	// async-retry goroutine emits at most one failure update per
	// request_id, so this is more of a defensive guarantee than
	// a frequent code path.
	rateLimit := "rate_limit"
	upstreamDown := "upstream_down"

	dst := &RequestLogEntry{
		RequestID: "req-2",
		Op:        RequestLogUpdate,
		Success:   false,
		ErrorKind: &rateLimit,
	}
	src := &RequestLogEntry{
		RequestID: "req-2",
		Op:        RequestLogUpdate,
		Success:   false,
		ErrorKind: &upstreamDown,
	}

	mergeRequestLogEntry(dst, src)

	if dst.Success {
		t.Error("Success should remain false")
	}
	if dst.ErrorKind == nil {
		t.Fatal("ErrorKind should be preserved when both are failures")
	}
	// Last non-empty wins: upstream_down replaces rate_limit.
	if *dst.ErrorKind != "upstream_down" {
		t.Errorf("ErrorKind = %q, want upstream_down (last non-empty wins)", *dst.ErrorKind)
	}
}

func TestMergeRequestLogBatch_MultipleUpdatesCoalesce(t *testing.T) {
	// Two UPDATE entries for the same request_id should coalesce
	// into one entry with the merged fields.
	reqID := "req-batch-1"
	rateLimit := "rate_limit"
	emptyKind := ""

	batch := []any{
		&RequestLogEntry{
			RequestID: reqID,
			Op:        RequestLogUpdate,
			Success:   false,
			ErrorKind: &rateLimit,
		},
		&RequestLogEntry{
			RequestID: reqID,
			Op:        RequestLogUpdate,
			Success:   true,
			ErrorKind: &emptyKind,
		},
		// Different request_id — should NOT be merged.
		&RequestLogEntry{
			RequestID: "req-other",
			Op:        RequestLogUpdate,
			Success:   true,
		},
	}

	merged := mergeRequestLogBatch(batch)

	// Expect 2 entries: the merged one for reqID + the other one.
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	// The merged entry for req-batch-1 should have Success=true
	// and ErrorKind=nil.
	var found *RequestLogEntry
	for _, item := range merged {
		if e, ok := item.(*RequestLogEntry); ok && e.RequestID == reqID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("merged entry for req-batch-1 not found")
	}
	if !found.Success {
		t.Error("merged Success should be true")
	}
	if found.ErrorKind != nil {
		t.Errorf("merged ErrorKind should be nil after success merge, got %v", *found.ErrorKind)
	}
}

// strPtrT is a small helper to avoid pulling strings package
// indirection into this section.
func strPtrT(s string) *string { return &s }

// TestRequestLogEntry_ApplyOriginFromContext verifies the helper
// reads context values written by middleware/origin_mw.go and copies
// them onto the entry. The test covers the four new fields, the
// nil-safe path, and the first-write-wins precedence rule.
func TestRequestLogEntry_ApplyOriginFromContext(t *testing.T) {
	t.Run("populates all four fields from ctx", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, "origin.stage", "node_probe")
		ctx = context.WithValue(ctx, "origin.actor", "node-probe-worker")
		ctx = context.WithValue(ctx, "origin.client_ip", "203.0.113.5")
		ctx = context.WithValue(ctx, "origin.xff", "203.0.113.5, 10.0.0.1")
		var e RequestLogEntry
		e.ApplyOriginFromContext(ctx)
		if e.OriginStage == nil || *e.OriginStage != "node_probe" {
			t.Fatalf("stage: %v", e.OriginStage)
		}
		if e.OriginActor == nil || *e.OriginActor != "node-probe-worker" {
			t.Fatalf("actor: %v", e.OriginActor)
		}
		if e.ClientIP == nil || *e.ClientIP != "203.0.113.5" {
			t.Fatalf("client_ip: %v", e.ClientIP)
		}
		if e.ClientForwardedFor == nil || *e.ClientForwardedFor != "203.0.113.5, 10.0.0.1" {
			t.Fatalf("xff: %v", e.ClientForwardedFor)
		}
	})

	t.Run("nil ctx and nil entry are safe", func(t *testing.T) {
		var e *RequestLogEntry
		e.ApplyOriginFromContext(context.Background()) // must not panic
		e = &RequestLogEntry{}
		e.ApplyOriginFromContext(nil) // must not panic
	})

	t.Run("first-write-wins: pre-set fields are not overwritten", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, "origin.stage", "business")
		e := RequestLogEntry{OriginStage: strPtrT("node_probe")}
		e.ApplyOriginFromContext(ctx)
		if e.OriginStage == nil || *e.OriginStage != "node_probe" {
			t.Fatalf("OriginStage should keep pre-set value, got %v", e.OriginStage)
		}
	})
}

// TestLookupProviderName verifies the provider name resolution logic.
func TestLookupProviderName(t *testing.T) {
	t.Run("returns provider code when found", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		providerID := 42
		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
			WithArgs(42).
			WillReturnRows(pgxmock.NewRows([]string{"code"}).AddRow("anthropic"))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupProviderName(ctx, tx, &providerID)
		require.Equal(t, "anthropic", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns unknown when providerID is nil", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		ctx := context.Background()
		// No expectations needed - should not query
		result := lookupProviderName(ctx, nil, nil)
		require.Equal(t, "unknown", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns unknown when query fails", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		providerID := 999
		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
			WithArgs(999).
			WillReturnError(pgx.ErrNoRows)

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupProviderName(ctx, tx, &providerID)
		require.Equal(t, "unknown", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("handles multiple provider types", func(t *testing.T) {
		testCases := []struct {
			providerID int
			code       string
		}{
			{1, "openai"},
			{2, "anthropic"},
			{3, "google"},
			{4, "alibaba-dashscope"},
		}

		for _, tc := range testCases {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)

			mockDB.ExpectBegin()
			mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
				WithArgs(tc.providerID).
				WillReturnRows(pgxmock.NewRows([]string{"code"}).AddRow(tc.code))

			ctx := context.Background()
			tx, err := mockDB.Begin(ctx)
			require.NoError(t, err)

			result := lookupProviderName(ctx, tx, &tc.providerID)
			require.Equal(t, tc.code, result)
			require.NoError(t, mockDB.ExpectationsWereMet())
			mockDB.Close()
		}
	})
}

// TestLookupTurnNumber verifies turn number counting logic.
//
// 2026-10-13 S3 波 2（停写切换）：权威读改为会话族 MAX(turn_no)（两腿），
// 0 时回退 v1 母表 30d 计数（镜像前历史会话）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 回退腿整段删除——v1 族随 890 落库后
// 再读它必然 42P01 ⇒ 会话族两腿恒 0 时 turn_no 取 1（会话首轮）。
// 「回退腿不得复活」由录制器守着：若 v1 回退查询被加回来，录制到的语句会变成
// 2 条 ⇒ 用例变红。（注意：lookupTurnNumber 读失败时**吞掉错误**返回 1，
// 所以只看返回值分辨不出「回退腿复活」——必须看语句条数。）
func TestLookupTurnNumber(t *testing.T) {
	const sessionQ = `SELECT COALESCE\(MAX\(turn_no\), 0\)[\s\S]*`

	t.Run("session family turn_no is authoritative", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(sessionQ).
			WithArgs("session-abc").
			WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(7))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-abc")
		require.Equal(t, 7, result)
		// 会话族已供数：不得再打 v1 回退查询。
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("v1 fallback leg is gone: empty session family yields turn 1", func(t *testing.T) {
		mockDB, recording, recorder := newRecordingDB(t)

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(sessionQ).
			WithArgs("session-legacy").
			WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(0))

		ctx := context.Background()
		// 事务也要走录制包装：lookupTurnNumber 查的是 tx，不是 db。
		tx, err := recording.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-legacy")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())

		// 只有一条查询：v1 回退腿随 890 整段删除，不得复活。
		statements := recorder.recorded()
		require.Len(t, statements, 1,
			"lookupTurnNumber 只能查会话族一条（v1 回退腿随 890 退役）")
		require.Contains(t, statements[0], "session_turns_hot",
			"正向对照：抓到的应是会话族查询")
	})

	t.Run("returns 1 when sessionID is empty", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		ctx := context.Background()
		// No expectations - should not query
		result := lookupTurnNumber(ctx, nil, "")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 1 when queries fail", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(sessionQ).
			WithArgs("session-xyz").
			WillReturnError(pgx.ErrNoRows)

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-xyz")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 1 for brand-new session", func(t *testing.T) {
		mockDB, recording, recorder := newRecordingDB(t)

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(sessionQ).
			WithArgs("session-new").
			WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(0))

		ctx := context.Background()
		// 事务也要走录制包装：lookupTurnNumber 查的是 tx，不是 db。
		tx, err := recording.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-new")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())

		statements := recorder.recorded()
		require.Len(t, statements, 1,
			"lookupTurnNumber 只能查会话族一条（v1 回退腿随 890 退役）")
	})

	t.Run("handles multi-turn conversation", func(t *testing.T) {
		testCases := []struct {
			turnNo       int
			expectedTurn int
		}{
			{1, 1},
			{2, 2},
			{3, 3},
			{5, 5},
			{10, 10},
		}

		for _, tc := range testCases {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)

			mockDB.ExpectBegin()
			mockDB.ExpectQuery(sessionQ).
				WithArgs("session-multi").
				WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(tc.turnNo))

			ctx := context.Background()
			tx, err := mockDB.Begin(ctx)
			require.NoError(t, err)

			result := lookupTurnNumber(ctx, tx, "session-multi")
			require.Equal(t, tc.expectedTurn, result)
			require.NoError(t, mockDB.ExpectationsWereMet())
			mockDB.Close()
		}
	})
}

// ── 2026-08-25 live-stream decoupling: SetOnRequestLogEmitted hook ────────────
//
// EmitRequestLog fires onEmitted synchronously on the caller's goroutine
// BEFORE the entry enters the queue (or the sync-fallback persist path).
// These tests pin that ordering contract plus the disabled/stopped gates.

// newEmittedHookClient builds an Enabled() Client with a worker-free queue:
// EmitRequestLog runs gate → onEmitted → enqueue, and pre-filling the buffer
// drives the queue-full sync fallback (persistRequestLog → onPersisted).
// No worker goroutine is started, so nothing drains the queue behind the
// test's back and there is nothing to leak.
func newEmittedHookClient(t *testing.T, bufSize int) (*Client, pgxmock.PgxPoolIface) {
	t.Helper()
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(func() { mockDB.Close() })
	return &Client{
		requestLogDB: mockDB,
		queue:        make(chan any, bufSize),
		done:         make(chan struct{}),
	}, mockDB
}

func TestRequestLogEmittedHookFiresBeforeQueueing(t *testing.T) {
	c, _ := newEmittedHookClient(t, 4)
	emitted := make(chan *RequestLogEntry, 1)
	c.SetOnRequestLogEmitted(func(entry *RequestLogEntry) { emitted <- entry })

	c.EmitRequestLog(&RequestLogEntry{RequestID: "req-emit-sync", TenantID: "tenant-a"})

	// Synchronous contract: by the time EmitRequestLog returns, the hook has
	// already fired — no receive-wait, no scheduler dependency.
	select {
	case got := <-emitted:
		require.Equal(t, "req-emit-sync", got.RequestID)
	default:
		t.Fatal("onEmitted must fire before EmitRequestLog returns (synchronous, caller goroutine)")
	}
	// The same call must also have queued the entry (after the hook).
	select {
	case queued := <-c.queue:
		entry, ok := queued.(*RequestLogEntry)
		require.True(t, ok, "queued item type %T", queued)
		require.Equal(t, "req-emit-sync", entry.RequestID)
		require.Equal(t, RequestLogInsert, entry.Op, "EmitRequestLog must default Op before queueing")
	default:
		t.Fatal("entry must be queued by EmitRequestLog")
	}
}

func TestSetOnRequestLogEmitted_ReplacesAndClears(t *testing.T) {
	c, _ := newEmittedHookClient(t, 4)
	first, second := make(chan struct{}, 2), make(chan struct{}, 2)
	c.SetOnRequestLogEmitted(func(*RequestLogEntry) { first <- struct{}{} })
	c.SetOnRequestLogEmitted(func(*RequestLogEntry) { second <- struct{}{} }) // replace semantics
	c.EmitRequestLog(&RequestLogEntry{RequestID: "req-replace"})

	select {
	case <-first:
		t.Fatal("a replaced onEmitted hook must not fire")
	default:
	}
	select {
	case <-second:
	default:
		t.Fatal("the latest onEmitted hook must fire")
	}

	c.SetOnRequestLogEmitted(nil) // clear
	c.EmitRequestLog(&RequestLogEntry{RequestID: "req-clear"})
	select {
	case <-second:
		t.Fatal("cleared onEmitted hook must not fire")
	default:
	}
}

// TestRequestLogEmittedBeforePersisted pins the two-phase broadcast contract:
// for one entry, the onEmitted hook fires before the entry reaches the
// database and the onPersisted hook fires only after the successful INSERT.
// The queue is pre-filled so EmitRequestLog takes the queue-full sync
// fallback (persistRequestLog), letting BOTH hooks fire within a single call
// on the same goroutine — the observed order is therefore deterministic.
func TestRequestLogEmittedBeforePersisted(t *testing.T) {
	c, mockDB := newEmittedHookClient(t, 1)

	// Insert-path mock sequence (mirrors TestUpdateRequestLog_MissingRowDoesNotFallBackToInsert).
	// ★ 2026-10-20（迁移 890，v1 族退役）：插入路径现在**只有**一条
	// `INSERT INTO usage_ledger_hot`（19 个绑定参数）——v1 宽表 INSERT 与
	// v1 正文族 INSERT 随 890 整段退役。
	mockDB.ExpectBegin()
	usageInsertArgs := make([]interface{}, 19)
	for index := range usageInsertArgs {
		usageInsertArgs[index] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(usageInsertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	var mu sync.Mutex
	var order []string
	record := func(stage string) func(*RequestLogEntry) {
		return func(*RequestLogEntry) {
			mu.Lock()
			order = append(order, stage)
			mu.Unlock()
		}
	}
	c.SetOnRequestLogEmitted(record("emitted"))
	c.AddOnRequestLogPersisted(record("persisted"))

	// 1st emit fills the queue; 2nd emit hits the sync fallback: gate →
	// onEmitted → persistRequestLog (INSERT) → onPersisted.
	c.EmitRequestLog(&RequestLogEntry{RequestID: "req-fill", TenantID: "tenant-a"})
	c.EmitRequestLog(&RequestLogEntry{RequestID: "req-emit-before-persist", TenantID: "tenant-a"})

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"emitted", "emitted", "persisted"}, order,
		"per entry the emitted hook must fire before the persisted hook")
	require.NoError(t, mockDB.ExpectationsWereMet())
}

// TestRequestLogEmittedGatedOnDisabledAndStopped: the onEmitted hook is a
// telemetry emit concern, not a shutdown broadcast — it must stay silent when
// the client is disabled (no DB) or already stopped.
func TestRequestLogEmittedGatedOnDisabledAndStopped(t *testing.T) {
	fired := func() chan *RequestLogEntry { return make(chan *RequestLogEntry, 1) }

	disabled := NewClient()
	require.False(t, disabled.Enabled(), "client without a DB must be disabled")
	disabledFired := fired()
	disabled.SetOnRequestLogEmitted(func(entry *RequestLogEntry) { disabledFired <- entry })
	disabled.EmitRequestLog(&RequestLogEntry{RequestID: "req-gate-disabled"})
	select {
	case <-disabledFired:
		t.Fatal("onEmitted must not fire when the client is disabled")
	default:
	}

	stopped, _ := newEmittedHookClient(t, 4)
	stopped.Stop()
	stoppedFired := fired()
	stopped.SetOnRequestLogEmitted(func(entry *RequestLogEntry) { stoppedFired <- entry })
	stopped.EmitRequestLog(&RequestLogEntry{RequestID: "req-gate-stopped"})
	select {
	case <-stoppedFired:
		t.Fatal("onEmitted must not fire after Stop()")
	default:
	}
	// Sanity: the gate fires before queueing, so nothing was enqueued either.
	if got := len(stopped.queue); got != 0 {
		t.Fatalf("stopped client queued %d entries, want 0", got)
	}
}
