package persist

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakePartition 是分区目录查询的一行。dayEnd 为 nil 表示分区名不符合
// YYYYMMDD 契约（对应 SQL 里 substring(...)::date 为 NULL）。
type fakePartition struct {
	name   string
	dayEnd *time.Time
	bytes  int64
}

// fakeSnapshotRetentionDB captures the cleanup SQL / args and lets tests
// script per-batch rows-deleted + returned cursor (batch loop) and injected errors.
type fakeSnapshotRetentionDB struct {
	mu           sync.Mutex
	begins       int
	execSQL      []string
	execArgs     [][]any
	querySQL     []string
	queryArgs    [][]any
	commit       bool
	batchResults []int64     // consumed per batch; last value repeats once exhausted
	batchFloors  []time.Time // next cursor per batch; zero value = NULL
	execErr      error

	// 分区形态探测
	partitioned bool
	probeErr    error
	probeCalls  int

	// 分区目录
	partitions []fakePartition
	listErr    error
	listCalls  int

	// DROP 执行
	deleteBatches int
	dropped       []string
	dropErr       error
	setLocalCalls int
}

// fakeRetentionRows 是最小 pgx.Rows：按目标指针类型逐列赋值。
type fakeRetentionRows struct {
	cols []fakeRetentionCol
	pos  int
	err  error
}

type fakeRetentionCol struct {
	b bool
	s string
	t *time.Time
	i int64
}

func (r *fakeRetentionRows) Next() bool {
	if r.pos >= len(r.cols) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRetentionRows) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for _, d := range dest {
		col := r.cols[r.pos-1]
		switch p := d.(type) {
		case *bool:
			*p = col.b
		case *string:
			*p = col.s
		case **time.Time:
			*p = col.t
		case *int64:
			*p = col.i
		}
	}
	return nil
}

func (r *fakeRetentionRows) Close()                                       { r.pos = len(r.cols) }
func (r *fakeRetentionRows) Err() error                                   { return r.err }
func (r *fakeRetentionRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRetentionRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRetentionRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeRetentionRows) RawValues() [][]byte                          { return nil }
func (r *fakeRetentionRows) Conn() *pgx.Conn                              { return nil }

// Query 服务于分区形态探测（relkind）与分区目录查询（pg_inherits）。
//
// ★ 与 execSQL 分开记录：那是两族完全不同的语句。既有批 DELETE 断言
// （如 TestSnapshotRetentionAdvancesScanFloorBetweenBatches 逐条检查
// 每批都带 $3 下界）必须只看到 DELETE 族；把探测语句混进去会让
// "每条语句都含下界" 这个断言变成假失败 —— 或者更糟，被人改成
// "跳过前 N 条" 之后就不再有牙。
func (db *fakeSnapshotRetentionDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.querySQL = append(db.querySQL, sql)
	db.queryArgs = append(db.queryArgs, args)
	if strings.Contains(sql, "relkind") {
		db.probeCalls++
		if db.probeErr != nil {
			return &fakeRetentionRows{err: db.probeErr}, db.probeErr
		}
		// partitioned=false 时模拟"表存在但不是分区父表"（生产迁移前形态）。
		return &fakeRetentionRows{cols: []fakeRetentionCol{{b: db.partitioned}}}, nil
	}
	db.listCalls++
	if db.listErr != nil {
		return &fakeRetentionRows{err: db.listErr}, db.listErr
	}
	cols := make([]fakeRetentionCol, 0, len(db.partitions))
	for _, p := range db.partitions {
		cols = append(cols, fakeRetentionCol{s: p.name, t: p.dayEnd, i: p.bytes})
	}
	return &fakeRetentionRows{cols: cols}, nil
}

type fakeSnapshotRetentionTx struct {
	db *fakeSnapshotRetentionDB
}

// fakeRow is a minimal pgx.Row: Scan fills (deleted int64, nextFloor *time.Time).
type fakeRow struct {
	deleted   int64
	nextFloor *time.Time
	err       error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for _, d := range dest {
		switch p := d.(type) {
		case *int64:
			*p = r.deleted
		case **time.Time:
			*p = r.nextFloor
		}
	}
	return nil
}

func (tx *fakeSnapshotRetentionTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	tx.db.execSQL = append(tx.db.execSQL, sql)
	tx.db.execArgs = append(tx.db.execArgs, args)
	if tx.db.execErr != nil {
		return fakeRow{err: tx.db.execErr}
	}
	// ★ 批次序号用**专用的 DELETE 批计数器**，不用 len(execSQL)。
	//   execSQL 是全局语句序列，DROP 型留存会往里塞 SET LOCAL / DROP TABLE，
	//   于是第一个 DELETE 批的 idx 已经不是 0 —— pick() 会静默跳到
	//   batchResults 的最后一项，测试照样"通过"但断的根本不是它想断的东西。
	//   这就是量具自己造的噪声：错的是夹具，不是被测代码。
	if !strings.Contains(sql, "DELETE FROM") {
		return fakeRow{}
	}
	tx.db.deleteBatches++
	idx := tx.db.deleteBatches - 1
	pick := func(n int) int {
		if idx >= n {
			return n - 1
		}
		return idx
	}
	var rows int64
	if len(tx.db.batchResults) > 0 {
		rows = tx.db.batchResults[pick(len(tx.db.batchResults))]
	}
	var floor *time.Time
	if len(tx.db.batchFloors) > 0 {
		f := tx.db.batchFloors[pick(len(tx.db.batchFloors))]
		if !f.IsZero() {
			floor = &f
		}
	}
	return fakeRow{deleted: rows, nextFloor: floor}
}

func (tx *fakeSnapshotRetentionTx) Commit(ctx context.Context) error {
	tx.db.mu.Lock()
	tx.db.commit = true
	tx.db.mu.Unlock()
	return nil
}

// Exec 服务于 DROP 型留存：SET LOCAL lock_timeout 与 DROP TABLE。
func (tx *fakeSnapshotRetentionTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	tx.db.execSQL = append(tx.db.execSQL, sql)
	tx.db.execArgs = append(tx.db.execArgs, args)
	if strings.HasPrefix(sql, "SET LOCAL") {
		tx.db.setLocalCalls++
		return pgconn.CommandTag{}, nil
	}
	if strings.HasPrefix(sql, "DROP TABLE") {
		if tx.db.dropErr != nil {
			return pgconn.CommandTag{}, tx.db.dropErr
		}
		tx.db.dropped = append(tx.db.dropped, sql)
	}
	return pgconn.CommandTag{}, nil
}

// droppedStatements 返回实际下发的 DROP TABLE 语句。
func (db *fakeSnapshotRetentionDB) droppedStatements() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make([]string, 0, len(db.dropped))
	for _, s := range db.dropped {
		out = append(out, s)
	}
	return out
}

func (tx *fakeSnapshotRetentionTx) Rollback(ctx context.Context) error { return nil }

func (db *fakeSnapshotRetentionDB) Begin(ctx context.Context) (SnapshotRetentionTx, error) {
	db.mu.Lock()
	db.begins++
	db.mu.Unlock()
	return &fakeSnapshotRetentionTx{db: db}, nil
}

func (db *fakeSnapshotRetentionDB) statements() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.execSQL...)
}

func (db *fakeSnapshotRetentionDB) began() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.begins
}

func (db *fakeSnapshotRetentionDB) committed() bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.commit
}

func TestSnapshotRetentionBatchLoopStopsOnPartialBatch(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{5000, 1234}}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 6234 {
		t.Fatalf("deleted = %d, want 6234 (full batch + partial batch)", deleted)
	}
	if got := db.began(); got != 2 {
		t.Fatalf("began = %d, want 2 (one transaction per batch)", got)
	}
	stmts := db.statements()
	for i, stmt := range stmts {
		if !strings.Contains(stmt, "DELETE FROM ursm_node_snapshot_min") ||
			!strings.Contains(stmt, "snapshot_ts") {
			t.Fatalf("batch %d SQL must delete expired snapshots by snapshot_ts, got:\n%s", i, stmt)
		}
	}
	// Args are (retention interval, batch size, scan floor).
	if len(db.execArgs[0]) != 3 {
		t.Fatalf("exec args = %v, want (interval, batch size, floor)", db.execArgs[0])
	}
	if interval, ok := db.execArgs[0][0].(string); !ok || !strings.HasPrefix(interval, "720h") {
		t.Fatalf("retention arg = %v, want 30d duration string (720h...)", db.execArgs[0][0])
	}
	if size, ok := db.execArgs[0][1].(int); !ok || size != 5000 {
		t.Fatalf("batch size arg = %v, want 5000", db.execArgs[0][1])
	}
	if !db.committed() {
		t.Fatal("cleanup transactions were not committed")
	}
}

func TestSnapshotRetentionHonorsCleanupWindow(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{5000}}
	worker := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{
		Retention:        30 * 24 * time.Hour,
		BatchSize:        5000,
		MaxCleanupWindow: time.Nanosecond, // deadline already passed after batch 1
	})
	worker.db = db
	// 时钟缝注入（2026-10-07 96h 审计）：Windows 的 time.Now 粒度 ~0.5ms，
	// fake DB 单批微秒级完成，两批之间的墙钟读数可能落在同一时钟刻度上，
	// 「deadline 已过」判假 ⇒ 循环多跑一批（began=2）。阶梯时钟保证每次
	// 读数推进 1ms：deadline=第一次读数+1ns，第二批检查时必然已越过 ⇒
	// began=1 跨平台确定。
	base := time.Now()
	clockStep := 0
	worker.now = func() time.Time {
		clockStep++
		return base.Add(time.Duration(clockStep) * time.Millisecond)
	}

	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.began(); got != 1 {
		t.Fatalf("began = %d, want 1 (window cap stops the batch loop)", got)
	}
}

func TestSnapshotRetentionBatchErrorStopsLoop(t *testing.T) {
	db := &fakeSnapshotRetentionDB{
		batchResults: []int64{5000},
		execErr:      &pgconn.PgError{Code: "42501", Message: "permission denied for table ursm_node_snapshot_min"},
	}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err == nil {
		t.Fatal("CleanupExpired() error = nil, want exec error")
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (failed batch must not count its rows)", deleted)
	}
	if db.committed() {
		t.Fatal("cleanup committed after batch error")
	}
}

func TestSnapshotRetentionDisabledIsNoOp(t *testing.T) {
	db := &fakeSnapshotRetentionDB{}
	worker := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{Retention: 0})
	if !worker.Disabled() {
		t.Fatal("Disabled() = false for zero retention")
	}
	worker.Start() // must not panic or spawn goroutines
	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	worker.Stop()
	if got := db.began(); got != 0 {
		t.Fatalf("begins = %d, want 0 (disabled worker must not touch the DB)", got)
	}
}

func TestSnapshotRetentionNilDBIsNoOp(t *testing.T) {
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.Start()
	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	worker.Stop()
}

func TestSnapshotRetentionStartStopIsIdempotentAndConcurrentSafe(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{0}}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db
	worker.Start()
	worker.Start()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker.Stop()
		}()
	}
	wg.Wait()
	worker.Stop()
	if got := db.began(); got == 0 {
		t.Fatal("started worker did not perform initial cleanup")
	}
}

func TestSnapshotRetentionStopWithoutStartReturnsImmediately(t *testing.T) {
	db := &fakeSnapshotRetentionDB{}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db
	returned := make(chan struct{})
	go func() {
		worker.Stop()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Stop() hung on a never-started worker")
	}
	if got := db.began(); got != 0 {
		t.Fatalf("begins = %d, want 0 (no cleanup without Start)", got)
	}
}

func TestSnapshotRetentionConfigFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		cfg := SnapshotRetentionConfigFromEnv()
		if cfg.Retention != 30*24*time.Hour {
			t.Fatalf("retention = %s, want default 30d", cfg.Retention)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "7")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 7*24*time.Hour {
			t.Fatalf("retention = %s, want 7d", cfg.Retention)
		}
	})
	t.Run("zero disables", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "0")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 0 {
			t.Fatalf("retention = %s, want 0 (disabled)", cfg.Retention)
		}
	})
	t.Run("invalid ignored", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "not-a-number")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 30*24*time.Hour {
			t.Fatalf("retention = %s, want default 30d on invalid input", cfg.Retention)
		}
	})
}

// TestSnapshotRetentionAdvancesScanFloorBetweenBatches pins the 2026-10-02
// production defect: without a per-batch scan floor, every batch restarts its
// candidate scan at the head of the pkey index and must walk past all index
// entries deleted by earlier batches before it can fill BatchSize. On 252
// (45.6M rows / 26.1M expired) the measured rate decayed 8,658 → 15,024 →
// 4,208 rows/s, i.e. O(n^2) across a round.
//
// The floor must therefore be carried forward. It is INCLUSIVE (>=) on
// purpose: one flush writes ~1,008 rows sharing a single snapshot_ts, so a
// strict (>) cursor risks silently skipping live rows that share the
// boundary timestamp. Re-scanning one timestamp is cheap; skipping rows is
// not.
func TestSnapshotRetentionAdvancesScanFloorBetweenBatches(t *testing.T) {
	first := time.Date(2026, 9, 6, 23, 56, 50, 0, time.UTC)
	second := time.Date(2026, 9, 6, 23, 58, 10, 0, time.UTC)
	db := &fakeSnapshotRetentionDB{
		batchResults: []int64{5000, 5000, 17},
		batchFloors:  []time.Time{first, second, second},
	}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if want := int64(10017); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}

	args := db.execArgs
	if len(args) != 3 {
		t.Fatalf("batches = %d, want 3", len(args))
	}
	// Batch 1 starts from the zero floor: the "no lower bound" seed.
	if got, ok := args[0][2].(time.Time); !ok || !got.IsZero() {
		t.Fatalf("batch 1 floor = %v, want zero time", args[0][2])
	}
	// Batches 2 and 3 must resume from the previous batch's max snapshot_ts.
	for i, want := range []time.Time{first, second} {
		got, ok := args[i+1][2].(time.Time)
		if !ok || !got.Equal(want) {
			t.Fatalf("batch %d floor = %v, want %v", i+2, args[i+1][2], want)
		}
	}

	// The floor predicate must be inclusive. A strict ">" would let the
	// scanner step past live rows sharing the boundary timestamp.
	for i, stmt := range db.statements() {
		if !strings.Contains(stmt, "snapshot_ts >= $3::timestamptz") {
			t.Fatalf("batch %d must use an INCLUSIVE floor (>=), got:\n%s", i, stmt)
		}
		if strings.Contains(stmt, "snapshot_ts > $3") {
			t.Fatalf("batch %d uses a strict floor (>) which can skip live rows:\n%s", i, stmt)
		}
	}
}

// ---------------------------------------------------------------------------
// 分区型留存（DROP）—— 2026-10-04
//
// 形态切换由 pg_class.relkind 决定，不由配置开关决定。下面每一组都同时
// 钉住两件事：走哪条路 + 为什么不走另一条路。
// ---------------------------------------------------------------------------

// partitionWorker 造一个时钟固定的 worker，让 cutoff 完全确定。
func partitionWorker(t *testing.T, db *fakeSnapshotRetentionDB, now time.Time, retention time.Duration) *SnapshotRetentionWorker {
	t.Helper()
	w := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{
		Retention:        retention,
		BatchSize:        5000,
		MaxCleanupWindow: 10 * time.Minute,
	})
	w.db = db
	w.now = func() time.Time { return now }
	return w
}

func TestSnapshotRetentionDropsExpiredPartitionsNotRows(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour) // 保留 7 天

	old := cutoff.Add(-24 * time.Hour) // 过期
	edge := cutoff                     // 恰好等于 cutoff
	live := cutoff.Add(24 * time.Hour) // 未过期
	oldT, edgeT, liveT := old, edge, live

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			{name: "ursm_node_snapshot_min_20260926", dayEnd: &oldT, bytes: 1024},
			{name: "ursm_node_snapshot_min_20260927", dayEnd: &edgeT, bytes: 2048},
			{name: "ursm_node_snapshot_min_20260928", dayEnd: &liveT, bytes: 4096},
		},
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.Mode != RetentionModePartitionDrop {
		t.Fatalf("Mode = %q, want %q", res.Mode, RetentionModePartitionDrop)
	}
	// 恰好等于 cutoff 的分区必须删（判据是 !After，不是 After）。
	if res.PartitionsDropped != 2 {
		t.Fatalf("PartitionsDropped = %d, want 2 (edge-at-cutoff included)", res.PartitionsDropped)
	}
	if want := int64(1024 + 2048); res.BytesReclaimed != want {
		t.Fatalf("BytesReclaimed = %d, want %d", res.BytesReclaimed, want)
	}
	// DROP 模式下绝不报行数：reltuples 只是估算，报成"删了 N 行"是撒谎。
	if res.RowsDeleted != 0 {
		t.Fatalf("RowsDeleted = %d, want 0 in drop mode (no fabricated row count)", res.RowsDeleted)
	}
	// 关键：一条 DELETE 都不能发。分区表上走 DELETE 正是要根治的形态。
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "DELETE FROM") {
			t.Fatalf("partitioned table must not fall back to row delete:\n%s", stmt)
		}
	}
	got := db.droppedStatements()
	if len(got) != 2 {
		t.Fatalf("DROP statements = %d, want 2: %v", len(got), got)
	}
	// 分区名必须被转义后引用，不能裸拼。
	if !strings.Contains(got[0], `"public"."ursm_node_snapshot_min_20260926"`) {
		t.Fatalf("DROP must use a qualified quoted identifier, got:\n%s", got[0])
	}
	if db.setLocalCalls != 2 {
		t.Fatalf("SET LOCAL lock_timeout = %d calls, want 2 (one per DROP transaction)", db.setLocalCalls)
	}
}

// dayEnd 恰好比 cutoff 晚 1ns ⇒ 保留。把 1ns 换成 0 就是上一个用例。
func TestSnapshotRetentionKeepsPartitionJustInsideRetention(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	justInside := cutoff.Add(time.Nanosecond)

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			{name: "ursm_node_snapshot_min_20260927", dayEnd: &justInside, bytes: 2048},
		},
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.PartitionsDropped != 0 {
		t.Fatalf("PartitionsDropped = %d, want 0 (1ns inside retention)", res.PartitionsDropped)
	}
	if res.Candidates != 0 {
		t.Fatalf("Candidates = %d, want 0", res.Candidates)
	}
}

// 分区名不符合 YYYYMMDD 契约 ⇒ day_end 为 NULL ⇒ 永不删（但也不报错、
// 不阻断本轮其余分区）。契约违约的后果是空间不回收，不是数据丢失。
func TestSnapshotRetentionNeverDropsPartitionWithoutDateContract(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-365 * 24 * time.Hour)

	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions: []fakePartition{
			{name: "ursm_node_snapshot_min_default", dayEnd: nil, bytes: 999},
			{name: "ursm_node_snapshot_min_20260926", dayEnd: &ancient, bytes: 1024},
		},
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.PartitionsDropped != 1 || res.BytesReclaimed != 1024 {
		t.Fatalf("dropped=%d bytes=%d, want 1/1024 (the nameless one must be skipped)",
			res.PartitionsDropped, res.BytesReclaimed)
	}
	for _, stmt := range db.droppedStatements() {
		if strings.Contains(stmt, "default") {
			t.Fatalf("dropped a partition with no parseable date:\n%s", stmt)
		}
	}
}

// DROP 失败不得让留存停摆：退回批 DELETE 并标记 Degraded。
// 停摆的表现是磁盘单调增长，而且没有任何告警。
func TestSnapshotRetentionFallsBackToRowDeleteWhenDropFails(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-30 * 24 * time.Hour)
	db := &fakeSnapshotRetentionDB{
		partitioned:  true,
		partitions:   []fakePartition{{name: "ursm_node_snapshot_min_20260904", dayEnd: &ancient, bytes: 1024}},
		dropErr:      errors.New("lock timeout"),
		batchResults: []int64{5000, 17},
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v, want nil (degraded is not an error)", err)
	}
	if res.Mode != RetentionModeRowDelete {
		t.Fatalf("Mode = %q, want %q", res.Mode, RetentionModeRowDelete)
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true so operators can see the DROP path is broken")
	}
	if res.RowsDeleted != 5017 {
		t.Fatalf("RowsDeleted = %d, want 5017 (fallback must actually delete)", res.RowsDeleted)
	}
	sawDelete := false
	for _, stmt := range db.statements() {
		if strings.Contains(stmt, "DELETE FROM") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatal("fallback path did not issue a DELETE")
	}
}

// 形态探测失败也退回批 DELETE：一次目录读失败不该让留存停摆。
func TestSnapshotRetentionProbeErrorFallsBackToRowDelete(t *testing.T) {
	db := &fakeSnapshotRetentionDB{
		probeErr:     errors.New("connection reset"),
		batchResults: []int64{42},
	}
	w := partitionWorker(t, db, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.Mode != RetentionModeRowDelete || res.RowsDeleted != 42 {
		t.Fatalf("Mode=%q RowsDeleted=%d, want row-delete/42", res.Mode, res.RowsDeleted)
	}
	if db.listCalls != 0 {
		t.Fatalf("listCalls = %d, want 0 (probe failed; must not list partitions)", db.listCalls)
	}
}

// 表还没建好（探测返回 0 行）⇒ 非分区路径，与迁移前语义完全一致。
func TestSnapshotRetentionMissingTableTakesRowDeletePath(t *testing.T) {
	db := &fakeSnapshotRetentionDB{partitioned: false, batchResults: []int64{0}}
	w := partitionWorker(t, db, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), 7*24*time.Hour)

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.Mode != RetentionModeRowDelete {
		t.Fatalf("Mode = %q, want %q", res.Mode, RetentionModeRowDelete)
	}
}

// 墙钟上限：分区堆积时本轮只删得下若干个，剩下的留给下一个 tick。
func TestSnapshotRetentionPartitionDropRespectsCleanupWindow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var parts []fakePartition
	for i := 0; i < 5; i++ {
		d := now.Add(-time.Duration(10+i) * 24 * time.Hour)
		parts = append(parts, fakePartition{
			name:   "ursm_node_snapshot_min_2026091" + string(rune('0'+i)),
			dayEnd: &d, bytes: 1024,
		})
	}
	db := &fakeSnapshotRetentionDB{partitioned: true, partitions: parts}
	w := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{
		Retention:        7 * 24 * time.Hour,
		BatchSize:        5000,
		MaxCleanupWindow: time.Minute,
	})
	w.db = db
	// 时钟每调用一次前进 30s ⇒ 超过 1 分钟墙钟后本轮必须收手。
	var tick int
	w.now = func() time.Time {
		tick++
		return now.Add(time.Duration(tick) * 30 * time.Second)
	}

	res, err := w.CleanupWithStats(context.Background())
	if err != nil {
		t.Fatalf("CleanupWithStats() error = %v", err)
	}
	if res.Candidates != 5 {
		t.Fatalf("Candidates = %d, want 5", res.Candidates)
	}
	if res.PartitionsDropped >= 5 {
		t.Fatalf("PartitionsDropped = %d, want < 5 (window must cut the round short)", res.PartitionsDropped)
	}
	if res.PartitionsDropped == 0 {
		t.Fatal("PartitionsDropped = 0, want at least one before the window closed")
	}
}

// 旧签名在 DROP 模式下返回 0 而不是编造行数。
func TestCleanupExpiredReturnsZeroInDropMode(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-30 * 24 * time.Hour)
	db := &fakeSnapshotRetentionDB{
		partitioned: true,
		partitions:  []fakePartition{{name: "ursm_node_snapshot_min_20260904", dayEnd: &ancient, bytes: 1024}},
	}
	w := partitionWorker(t, db, now, 7*24*time.Hour)

	deleted, err := w.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if deleted != 0 {
		t.Fatalf("CleanupExpired() = %d, want 0 in drop mode (no fabricated row count)", deleted)
	}
}
