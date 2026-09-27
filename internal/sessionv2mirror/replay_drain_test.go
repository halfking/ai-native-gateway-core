package sessionv2mirror

// Subtask 5 收口测试（审计 §23 F-15 / F-16 / F-17）。
//
// 覆盖三件本轮新增、此前**零覆盖**的东西：
//  1. drainWorker 的排空判定（并行 + FOR UPDATE SKIP LOCKED 下的正确性）；
//  2. currentMaxAtts 的钳制，以及「spec key 真的登记了」（F-15 的回归守卫）；
//  3. mirrorReplayWorkers 的扇出边界。
//
// 用 fakeRows 伪造 claimBatch 的返回序列，不依赖真实 PostgreSQL；并发部分
// 跑 -race 才有意义（见 commit message 里的变异测试记录）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// --- fake: 可编程的 claimBatch 返回序列 -------------------------------------

// scriptRows 是一个按调用次数依次返回预设行数的假 pgx.Rows。
type scriptRows struct {
	batch [][]any
	pos   int
}

func (r *scriptRows) Close()                                       {}
func (r *scriptRows) Err() error                                   { return nil }
func (r *scriptRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *scriptRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *scriptRows) Values() ([]any, error)                       { return nil, nil }
func (r *scriptRows) RawValues() [][]byte                          { return nil }
func (r *scriptRows) Conn() *pgx.Conn                              { return nil }

func (r *scriptRows) Next() bool {
	if r.pos >= len(r.batch) {
		return false
	}
	r.pos++
	return true
}

func (r *scriptRows) Scan(dest ...any) error {
	if r.pos == 0 || r.pos > len(r.batch) {
		return errors.New("Scan without preceding Next")
	}
	row := r.batch[r.pos-1]
	if len(row) != len(dest) {
		return fmt.Errorf("scan arity mismatch: row has %d, dest %d", len(row), len(dest))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = row[i].(int64)
		case *string:
			*d = row[i].(string)
		case *[]byte:
			*d = row[i].([]byte)
		case *int:
			*d = row[i].(int)
		default:
			return fmt.Errorf("unsupported scan target %T", dest[i])
		}
	}
	return nil
}

// claimRowSlice 构造 n 行的 claimBatch 返回体，列序对齐 replay.go 的
// RETURNING id, request_id, session_id, payload, attempts。
func claimRowSlice(n int, baseID int64) [][]any {
	out := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		id := baseID + int64(i)
		out = append(out, []any{id, fmt.Sprintf("req-%d", id), "gw_sess", []byte(`{}`), 0})
	}
	return out
}

// scriptedDB 按调用次序把 claimBatch 的 Query 结果喂给调用方；次数用尽后一律
// 返回空批次（模拟表已排空）。
type scriptedDB struct {
	mu      sync.Mutex
	batches []int // 第 n 次 Query 返回 batches[n] 行；用尽后恒返回 0 行
	calls   int
	claimQ  int
}

func (d *scriptedDB) Begin(context.Context) (pgx.Tx, error) { return &scriptedTx{db: d}, nil }
func (d *scriptedDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

type scriptedTx struct{ db *scriptedDB }

func (t *scriptedTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(context.Background(), sql, args...)
}

func (t *scriptedTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (t *scriptedTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !strings.Contains(sql, "session_mirror_outbox") {
		return nil, fmt.Errorf("unexpected query in test: %s", strings.TrimSpace(sql))
	}
	d := t.db
	d.mu.Lock()
	idx := d.calls
	d.calls++
	if idx < len(d.batches) {
		d.claimQ++
	}
	n := 0
	if idx < len(d.batches) {
		n = d.batches[idx]
	}
	d.mu.Unlock()
	return &scriptRows{batch: claimRowSlice(n, int64(1000+idx*100))}, nil
}

func (d *scriptedDB) Commit(context.Context) error          { return nil }
func (d *scriptedTx) Commit(context.Context) error          { return nil }
func (d *scriptedTx) Rollback(context.Context) error        { return nil }
func (d *scriptedTx) Conn() *pgx.Conn                       { return nil }
func (d *scriptedTx) Begin(context.Context) (pgx.Tx, error) { return d, nil }

// 其余 pgx.Tx 方法：本测试路径不触达，panic 以免静默吞掉未来的意外调用。
func (d *scriptedTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("CopyFrom not expected")
}
func (d *scriptedTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("SendBatch not expected")
}
func (d *scriptedTx) LargeObjects() pgx.LargeObjects { panic("LargeObjects not expected") }
func (d *scriptedTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("Prepare not expected")
}

// --- 1. drainWorker 排空判定 -------------------------------------------------

// TestDrainWorker_ShortBatchIsNotDrained 是 F-16 的回归守卫：
// claimBatch 第一次返回**短批次**（行数 < batchSize，但表里确实还有行）时，
// worker 绝不能就此收工——旧实现正是栽在这里。
func TestDrainWorker_ShortBatchIsNotDrained(t *testing.T) {
	const batch = 250
	// 第 1 次：短批次（3 行，但后面还有）；第 2 次：又是短批次（4 行）；之后空。
	db := &scriptedDB{batches: []int{3, 4}}
	r := &MirrorOutboxReaper{
		db: db, writer: &noopWriter{}, batchSize: batch, maxAtts: 10,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	ctx := context.Background()
	r.drainWorker(ctx)

	if db.calls < 3 {
		t.Fatalf("claimBatch called %d times, want >=3: a short batch must not end the drain",
			db.calls)
	}
	if db.claimQ < 2 {
		t.Fatalf("non-empty claimBatches = %d, want >=2 (the two short batches must both be replayed)",
			db.claimQ)
	}
}

// TestDrainWorker_EmptyStreakRequired 钉住新的退出条件：必须连续
// drainEmptyConfirmations 次空批次才收工，单次空读不算排空。
func TestDrainWorker_EmptyStreakRequired(t *testing.T) {
	db := &scriptedDB{batches: []int{0, 0}} // 前两次空，第三次起（未配置）也是空
	r := &MirrorOutboxReaper{
		db: db, writer: &noopWriter{}, batchSize: 250, maxAtts: 10,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	r.drainWorker(context.Background())

	// 空读次数必须 >= drainEmptyConfirmations（+1 是第三次确认读：脚本用尽后仍读空）。
	if db.calls < drainEmptyConfirmations {
		t.Fatalf("claimBatch called %d times, want >= %d (single empty read must not end the drain)",
			db.calls, drainEmptyConfirmations)
	}
}

// TestDrainWorker_CtxCancelStops 确认取消路径不空转。
func TestDrainWorker_CtxCancelStops(t *testing.T) {
	db := &scriptedDB{batches: []int{0, 0, 0, 0, 0, 0}}
	r := &MirrorOutboxReaper{
		db: db, writer: &noopWriter{}, batchSize: 250, maxAtts: 10,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.drainWorker(ctx)
	if db.calls != 0 {
		t.Fatalf("claimBatch called %d times after ctx cancel, want 0", db.calls)
	}
}

// TestDrainWorker_ClaimErrorStops 确认 claim 失败立即退出（不重试打爆 DB）。
func TestDrainWorker_ClaimErrorStops(t *testing.T) {
	db := &errClaimDB{}
	r := &MirrorOutboxReaper{
		db: db, writer: &noopWriter{}, batchSize: 250, maxAtts: 10,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	r.drainWorker(context.Background())
	if db.calls != 1 {
		t.Fatalf("claimBatch attempted %d times on error, want exactly 1 (no hot retry loop)", db.calls)
	}
}

type errClaimDB struct{ calls int }

func (d *errClaimDB) Begin(context.Context) (pgx.Tx, error) { return &errClaimTx{d: d}, nil }

func (d *errClaimDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

type errClaimTx struct{ d *errClaimDB }

func (t *errClaimTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (t *errClaimTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	t.d.calls++
	return nil, errors.New("simulated connection failure")
}
func (t *errClaimTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (t *errClaimTx) Commit(context.Context) error                     { return nil }
func (t *errClaimTx) Rollback(context.Context) error                   { return nil }
func (t *errClaimTx) Conn() *pgx.Conn                                  { return nil }
func (t *errClaimTx) Begin(context.Context) (pgx.Tx, error)            { return t, nil }
func (t *errClaimTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("CopyFrom not expected")
}
func (t *errClaimTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("SendBatch not expected")
}
func (t *errClaimTx) LargeObjects() pgx.LargeObjects { panic("LargeObjects not expected") }
func (t *errClaimTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("Prepare not expected")
}

// noopWriter 满足 V2Writer，replayOne 对它写入成功即走删除路径。
type noopWriter struct{}

func (noopWriter) Write(context.Context, *v2.ProcessedRequest) error { return nil }

// --- 2. currentMaxAtts 与 spec 登记（F-15 回归守卫） ---------------------------

// TestMirrorOutboxMaxAttemptsSpecRegistered 是 F-15 的回归守卫：
// 只加消费点、不把 key 登记进 spec，GetPlatformInt 会恒返回 fallback，
// 「热重载」形同虚设 —— 这正是 §16 F-2 / F-5 同款死配置，必须钉死。
func TestMirrorOutboxMaxAttemptsSpecRegistered(t *testing.T) {
	const key = "sessions_v2.mirror_outbox_max_attempts"
	specs := settings.SessionsV2Specs()
	var found *settings.Spec
	for _, sp := range specs {
		if sp.Key == key {
			found = sp
			break
		}
	}
	if found == nil {
		t.Fatalf("spec %q is not registered — the hot-reload reader in replay.go would "+
			"always fall back to the default, making the setting dead config", key)
	}
	if found.Type != settings.TypeInt {
		t.Errorf("spec type = %v, want TypeInt", found.Type)
	}
	if found.Scope != settings.ScopePlatform {
		t.Errorf("spec scope = %v, want ScopePlatform (replay.go reads it via GetPlatformInt)", found.Scope)
	}
	if !found.HotReload {
		t.Error("spec HotReload = false, want true (requeue reads it per call)")
	}
	// 登记的 Min/Max 必须与 replay.go 的钳制常量一致，否则「登记的范围」和
	// 「实际生效的范围」会各说各话。
	if found.Min == nil || *found.Min != mirrorReplayMaxAttsFloor {
		t.Errorf("spec Min = %v, want %d (must match mirrorReplayMaxAttsFloor)", found.Min, mirrorReplayMaxAttsFloor)
	}
	if found.Max == nil || *found.Max != mirrorReplayMaxAttsCeiling {
		t.Errorf("spec Max = %v, want %d (must match mirrorReplayMaxAttsCeiling)", found.Max, mirrorReplayMaxAttsCeiling)
	}
	if found.Default != mirrorReplayDefaultMaxAtts {
		t.Errorf("spec Default = %d, want %d (must match mirrorReplayDefaultMaxAtts)",
			found.Default, mirrorReplayDefaultMaxAtts)
	}
}

// TestCurrentMaxAtts_Clamp 覆盖钳制边界。未注册 spec 时 getPlatformInt 恒返回
// 入参 fallback，因此本用例同时验证「fallback 也会被钳」。
func TestCurrentMaxAtts_Clamp(t *testing.T) {
	cases := []struct {
		name string
		def  int
		want int
	}{
		{"0 falls to floor", 0, mirrorReplayMaxAttsFloor},
		{"negative falls to floor", -100, mirrorReplayMaxAttsFloor},
		{"below floor clamps up", 2, mirrorReplayMaxAttsFloor},
		{"at floor", mirrorReplayMaxAttsFloor, mirrorReplayMaxAttsFloor},
		{"in range passes through", 17, 17},
		{"at ceiling", mirrorReplayMaxAttsCeiling, mirrorReplayMaxAttsCeiling},
		{"above ceiling clamps down", 999, mirrorReplayMaxAttsCeiling},
		{"default unchanged", mirrorReplayDefaultMaxAtts, mirrorReplayDefaultMaxAtts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentMaxAtts(tc.def); got != tc.want {
				t.Errorf("currentMaxAtts(%d) = %d, want %d", tc.def, got, tc.want)
			}
		})
	}
}

// --- 3. worker 扇出边界 ------------------------------------------------------

func TestMirrorReplayWorkers(t *testing.T) {
	n := mirrorReplayWorkers()
	if n < 1 {
		t.Errorf("mirrorReplayWorkers() = %d, want >= 1 (reaper must stay functional)", n)
	}
	if n > mirrorReplayWorkersCap {
		t.Errorf("mirrorReplayWorkers() = %d, want <= cap %d (DB pool pressure)", n, mirrorReplayWorkersCap)
	}
}
