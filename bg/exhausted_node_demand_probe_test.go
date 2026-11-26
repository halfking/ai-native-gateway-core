package bg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// ── 回退曲线（纯函数）─────────────────────────────────────────────────────────

// TestDemandProbeInterval_BackoffCurveCapsAtFiveMinutes 把「探测时长自动回退、
// 最长不超过 5 分钟」这条要求钉成数值：30s → 1m → 2m → 4m → **5m 封顶**。
func TestDemandProbeInterval_BackoffCurveCapsAtFiveMinutes(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: 0}, // 首次：立即（不等）
		{attempts: 1, want: 30 * time.Second},
		{attempts: 2, want: 60 * time.Second},
		{attempts: 3, want: 120 * time.Second},
		{attempts: 4, want: 240 * time.Second},
		{attempts: 5, want: 300 * time.Second}, // 封顶（480s 被夹到 300s）
		{attempts: 6, want: 300 * time.Second},
		{attempts: 50, want: 300 * time.Second},
	}
	for _, c := range cases {
		got := demandProbeInterval(c.attempts)
		if got != c.want {
			t.Errorf("demandProbeInterval(%d) = %v, want %v", c.attempts, got, c.want)
		}
		if got > demandProbeMaxInterval {
			t.Errorf("demandProbeInterval(%d) = %v 超过封顶 %v", c.attempts, got, demandProbeMaxInterval)
		}
	}
}

// ── 需求驱动调度器 ────────────────────────────────────────────────────────────

type demandProbeRecorder struct {
	mu   sync.Mutex
	ids  []int
	busy map[int]bool // 置位 ⇒ 模拟「已有同凭据探针在跑」（submit 返回 false）
}

func (r *demandProbeRecorder) submit(credID int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy[credID] {
		return false
	}
	r.ids = append(r.ids, credID)
	return true
}

func (r *demandProbeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ids)
}

func (r *demandProbeRecorder) snapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.ids))
	copy(out, r.ids)
	return out
}

func newDemandProbeMock(t *testing.T, rows []int) (*ExhaustedNodeDemandProbe, pgxmock.PgxPoolIface, *demandProbeRecorder) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	mockRows := pgxmock.NewRows([]string{"id"}).AddRow(int32(rows[0]))
	for _, id := range rows[1:] {
		mockRows.AddRow(int32(id))
	}
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("claude-sonnet-5-5", "default").
		WillReturnRows(mockRows)
	rec := &demandProbeRecorder{busy: map[int]bool{}}
	p := NewExhaustedNodeDemandProbe(mock, rec.submit)
	return p, mock, rec
}

// expectRecoveryQuery 期望一次「凭据级恢复判定」查询，并返回该凭据的耗尽态。
//
// ★ 这条查询回答的是「这把凭据现在还耗尽吗」（看 credentials.quota_state，
//
//	与模型无关）⇒ 跨模型口径成立 ⇒ 5 分钟封顶不会被别的模型的扫描击穿。
func expectRecoveryQuery(mock pgxmock.PgxPoolIface, credID int, stillExhausted bool) {
	rows := pgxmock.NewRows([]string{"id", "still_exhausted"})
	if stillExhausted {
		rows.AddRow(int32(credID), true)
	} else {
		rows.AddRow(int32(credID), false)
	}
	mock.ExpectQuery("FROM credentials").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
}

// TestDemandProbe_FirstRequestProbesImmediately 第一次撞上耗尽节点 ⇒ **立即**
// 发起探测（这是需求驱动存在的全部意义：不等 1 小时退避）。
func TestDemandProbe_FirstRequestProbesImmediately(t *testing.T) {
	p, mock, rec := newDemandProbeMock(t, []int{31, 59, 99036})
	submitted, skipped := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default")
	if submitted != 3 {
		t.Errorf("submitted = %d, want 3", submitted)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	got := rec.snapshot()
	if len(got) != 3 || got[0] != 31 || got[1] != 59 || got[2] != 99036 {
		t.Errorf("probed creds = %v, want [31 59 99036]", got)
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_RepeatedRequestsDoNotStorm 重复请求不得把上游打爆：第二次
// 请求（同一秒内）被回退闸挡下 ⇒ 不再发探测。
func TestDemandProbe_RepeatedRequestsDoNotStorm(t *testing.T) {
	p, mock, rec := newDemandProbeMock(t, []int{31})
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 1 {
		t.Fatalf("first submitted = %d, want 1", submitted)
	}
	submitted, skipped := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default")
	if submitted != 0 {
		t.Errorf("second submitted = %d, want 0（回退闸内）", submitted)
	}
	if skipped != 1 {
		t.Errorf("second skipped = %d, want 1", skipped)
	}
	if rec.count() != 1 {
		t.Errorf("total probes = %d, want 1", rec.count())
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_BackoffUnlocksAfterInterval 时间推进到回退间隔之后 ⇒ 允许
// 再次探测；且间隔随尝试次数自动拉长。
func TestDemandProbe_BackoffUnlocksAfterInterval(t *testing.T) {
	p, mock, rec := newDemandProbeMock(t, []int{31})
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }

	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 1 {
		t.Fatalf("first submitted = %d, want 1", submitted)
	}
	// +29s：仍在 30s 间隔内 ⇒ 不探。
	now = now.Add(29 * time.Second)
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 0 {
		t.Errorf("at +29s submitted = %d, want 0", submitted)
	}
	// +30s：到期 ⇒ 探（第二次）。
	now = now.Add(1 * time.Second)
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 1 {
		t.Errorf("at +30s submitted = %d, want 1", submitted)
	}
	// 第二次之后间隔变成 60s ⇒ +45s 仍不探。
	now = now.Add(45 * time.Second)
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 0 {
		t.Errorf("at +75s submitted = %d, want 0（第二档间隔 60s）", submitted)
	}
	// +60s 到期 ⇒ 探（第三次）。
	now = now.Add(15 * time.Second)
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 1 {
		t.Errorf("at +90s submitted = %d, want 1", submitted)
	}
	if rec.count() != 3 {
		t.Errorf("total probes = %d, want 3", rec.count())
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_DedupedSubmitDoesNotAdvanceBackoff 被同凭据的在跑探针去重掉
// 的提交**不计入回退次数** —— 否则回退虚增、间隔越拖越长（会拖回「1 小时
// 才发现」的老坑）。
func TestDemandProbe_DedupedSubmitDoesNotAdvanceBackoff(t *testing.T) {
	p, mock, rec := newDemandProbeMock(t, []int{31})
	rec.busy[31] = true // 模拟「已有探针在跑」

	submitted, skipped := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default")
	if submitted != 0 || skipped != 1 {
		t.Fatalf("submitted=%d skipped=%d, want 0/1", submitted, skipped)
	}
	p.mu.Lock()
	attempts := 0
	if st := p.state[31]; st != nil {
		attempts = st.attempts
	}
	p.mu.Unlock()
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0（被去重的提交不该计次）", attempts)
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_RecoveredCredentialResetsBackoff 凭据恢复（quota_state 不再
// 是耗尽态）⇒ 回退计数清零 ⇒ 下次耗尽时又是「立即探测」。
func TestDemandProbe_RecoveredCredentialResetsBackoff(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	rec := &demandProbeRecorder{busy: map[int]bool{}}
	p := NewExhaustedNodeDemandProbe(mock, rec.submit)
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }

	// 第一轮：31 耗尽
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	if submitted, _ := p.NotifyRequest(context.Background(), "m", "default"); submitted != 1 {
		t.Fatalf("round1 submitted = %d, want 1", submitted)
	}
	// 扫描 TTL 之后（否则命中缓存，集合没重新扫 ⇒ 恢复检测不会跑）
	now = now.Add(demandProbeScanTTL + time.Second)
	// 第二轮：31 已恢复（耗尽集合为空）⇒ 凭据级查询确认 quota_state 已非耗尽
	// ⇒ 状态清零。
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	expectRecoveryQuery(mock, 31, false)
	if submitted, _ := p.NotifyRequest(context.Background(), "m", "default"); submitted != 0 {
		t.Fatalf("round2 submitted = %d, want 0", submitted)
	}
	p.mu.Lock()
	_, still := p.state[31]
	p.mu.Unlock()
	if still {
		t.Errorf("恢复后仍保留回退状态（未清零）")
	}
	// 第三轮：31 再次耗尽 ⇒ 应当**立即**探（而不是等 30s）
	now = now.Add(demandProbeScanTTL + time.Second)
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	if submitted, _ := p.NotifyRequest(context.Background(), "m", "default"); submitted != 1 {
		t.Errorf("round3 submitted = %d, want 1（恢复后应清零回退）", submitted)
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_BackoffSurvivesCrossModelScan ★★ **跨模型不得误清零**
// （5 分钟封顶的守门测试）。
//
// 事故形状：回退计数是**凭据级**的，但扫描是**按模型**的 ⇒ 若用「本次模型
// 切片里缺席」判「已恢复」，模型 B 的一次扫描就会把「模型 A 上仍耗尽」的
// 凭据 31 的回退状态清零 ⇒ 下次模型 A 的请求立即探测 ⇒ 5 分钟封顶被击穿 ⇒
// 高频请求下退化成「每请求探一次」⇒ 把上游打爆。
//
// ⇒ 恢复判定必须问**凭据自己的 quota_state**（凭据级真值，与模型无关）。
func TestDemandProbe_BackoffSurvivesCrossModelScan(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	rec := &demandProbeRecorder{busy: map[int]bool{}}
	p := NewExhaustedNodeDemandProbe(mock, rec.submit)
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }

	// 模型 A：31 耗尽 ⇒ 首次立即探。
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("model-A", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	if submitted, _ := p.NotifyRequest(context.Background(), "model-A", "default"); submitted != 1 {
		t.Fatalf("model-A round1 submitted = %d, want 1", submitted)
	}
	// 探到封顶档：再探几次，让 attempts 涨到 5（封顶 300s）。
	for i := 0; i < 4; i++ {
		now = now.Add(demandProbeMaxInterval + time.Second)
		mock.ExpectQuery("SELECT DISTINCT c\\.id").
			WithArgs("model-A", "default").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
		expectRecoveryQuery(mock, 31, true) // ★ 31 仍然耗尽（凭据级真值）
		if submitted, _ := p.NotifyRequest(context.Background(), "model-A", "default"); submitted != 1 {
			t.Fatalf("model-A round%d submitted = %d, want 1", i+2, submitted)
		}
	}
	p.mu.Lock()
	attempts := 0
	if st := p.state[31]; st != nil {
		attempts = st.attempts
	}
	p.mu.Unlock()
	if attempts != 5 {
		t.Fatalf("attempts = %d, want 5（应已到封顶档）", attempts)
	}

	// ★ 关键：模型 B 的请求。31 **不在** B 的耗尽切片里（B 没有这个绑定）。
	//   若恢复判定错用「切片缺席」⇒ 31 的回退状态会被清零。
	//   ★ 时间**不推进**（沿用 T）⇒ 后面 model-A 的判定窗口才落在
	//     「30s < Δt < 300s」这个能区分两种口径的区间里：
	//       · 状态被误清零（attempts=0）⇒ 立即探 ⇒ submitted=1（错）
	//       · 状态保留（attempts=5, 封顶 300s）⇒ 不探 ⇒ submitted=0（对）
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("model-B", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	expectRecoveryQuery(mock, 31, true) // ★ 凭据级真值：31 仍耗尽 ⇒ 不得清零
	p.NotifyRequest(context.Background(), "model-B", "default")

	p.mu.Lock()
	_, still := p.state[31]
	p.mu.Unlock()
	if !still {
		t.Fatalf("★ 跨模型扫描把 31 的回退状态误清零了 ⇒ 5 分钟封顶被击穿")
	}

	// 模型 A 再来一次请求：距上次探测 45s —— 落在「30s < Δt < 300s」区间
	//   ⇒ 若 attempts 仍是 5（封顶 300s）则必须**不探**。
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("model-A", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	expectRecoveryQuery(mock, 31, true)
	now = now.Add(45 * time.Second)
	submitted, skipped := p.NotifyRequest(context.Background(), "model-A", "default")
	if submitted != 0 {
		t.Errorf("★ model-A submitted = %d, want 0（跨模型误清零导致封顶失效）", submitted)
	}
	if skipped != 1 {
		t.Errorf("model-A skipped = %d, want 1（被回退闸挡下）", skipped)
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_RecoveryQueryFailureKeepsBackoff 恢复查询失败（DB 抖动）
// ⇒ **保留**回退状态（宁可多探一次，不可漏探打爆上游）。
func TestDemandProbe_RecoveryQueryFailureKeepsBackoff(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	rec := &demandProbeRecorder{busy: map[int]bool{}}
	p := NewExhaustedNodeDemandProbe(mock, rec.submit)
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }

	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	if submitted, _ := p.NotifyRequest(context.Background(), "m", "default"); submitted != 1 {
		t.Fatalf("round1 submitted = %d, want 1", submitted)
	}
	now = now.Add(demandProbeScanTTL + time.Second)
	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	// 恢复查询报错（无 expectation ⇒ pgxmock 返回 error）
	mock.ExpectQuery("FROM credentials").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))
	p.NotifyRequest(context.Background(), "m", "default")

	p.mu.Lock()
	_, still := p.state[31]
	p.mu.Unlock()
	if !still {
		t.Errorf("★ 恢复查询失败却清零了回退状态 ⇒ 会打爆上游")
	}
}

// TestDemandProbe_ScanFailureServesCachedSet 扫描失败（DB 抖动）时**继续用**
// 上一次扫到的耗尽集合 ⇒ 一次 DB 抖动不会把「需求驱动复探」整个关掉
// （否则又回到「等 1 小时退避」的老坑）。
func TestDemandProbe_ScanFailureServesCachedSet(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	rec := &demandProbeRecorder{busy: map[int]bool{}}
	p := NewExhaustedNodeDemandProbe(mock, rec.submit)
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }

	mock.ExpectQuery("SELECT DISTINCT c\\.id").
		WithArgs("m", "default").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int32(31)))
	if submitted, _ := p.NotifyRequest(context.Background(), "m", "default"); submitted != 1 {
		t.Fatalf("round1 submitted = %d, want 1", submitted)
	}
	// TTL 过期后的下一次请求：扫描会失败（无更多期望）⇒ 应回落到缓存集合。
	// ★ 回落生效 ⇒ 凭据仍在名单里；且 61s 已过 30s 回退间隔 ⇒ 允许再探一次。
	//   （修复前：扫描失败 ⇒ 集合为空 ⇒ submitted=0 ⇒ 「一次 DB 抖动把需求
	//   驱动复探整个关掉」。）
	now = now.Add(demandProbeScanTTL + time.Second)
	submitted, skipped := p.NotifyRequest(context.Background(), "m", "default")
	if submitted != 1 {
		t.Errorf("round2 submitted = %d, want 1（扫描失败应回落缓存集合，且 30s 回退已到期）", submitted)
	}
	if skipped != 0 {
		t.Errorf("round2 skipped = %d, want 0", skipped)
	}
}

// TestDemandProbe_ScanResultCachedWithinTTL 同一模型的连续请求不必每次打库
// （请求热路径必须有界）；TTL 过后才重新扫描。
func TestDemandProbe_ScanResultCachedWithinTTL(t *testing.T) {
	p, mock, rec := newDemandProbeMock(t, []int{31})
	now := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return now }
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 1 {
		t.Fatalf("first submitted = %d, want 1", submitted)
	}
	// 第二次请求命中缓存（不再查库）⇒ pgxmock 的期望数仍是 1 次查询。
	now = now.Add(5 * time.Second)
	if submitted, _ := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default"); submitted != 0 {
		t.Errorf("second (cached) submitted = %d, want 0", submitted)
	}
	mock.ExpectationsWereMet()
	if rec.count() != 1 {
		t.Errorf("probes = %d, want 1", rec.count())
	}
}

// TestDemandProbe_MaxCredentialsPerRequest 单次请求最多触发的探测数有上限
// （放大比上限）⇒ 超出的部分被裁掉并计入 skipped。
func TestDemandProbe_MaxCredentialsPerRequest(t *testing.T) {
	ids := make([]int, 0, demandProbeMaxCredentialsPerRequest+5)
	for i := 0; i < demandProbeMaxCredentialsPerRequest+5; i++ {
		ids = append(ids, 100+i)
	}
	p, mock, rec := newDemandProbeMock(t, ids)
	submitted, skipped := p.NotifyRequest(context.Background(), "claude-sonnet-5-5", "default")
	if submitted != demandProbeMaxCredentialsPerRequest {
		t.Errorf("submitted = %d, want %d", submitted, demandProbeMaxCredentialsPerRequest)
	}
	if skipped != 5 {
		t.Errorf("skipped = %d, want 5", skipped)
	}
	if rec.count() != demandProbeMaxCredentialsPerRequest {
		t.Errorf("probes = %d, want %d", rec.count(), demandProbeMaxCredentialsPerRequest)
	}
	mock.ExpectationsWereMet()
}

// TestDemandProbe_NilSubmitIsInert 未接线（submit == nil）⇒ 完全惰性，不查库。
func TestDemandProbe_NilSubmitIsInert(t *testing.T) {
	p := NewExhaustedNodeDemandProbe(nil, nil)
	submitted, skipped := p.NotifyRequest(context.Background(), "m", "default")
	if submitted != 0 || skipped != 0 {
		t.Errorf("submitted=%d skipped=%d, want 0/0", submitted, skipped)
	}
}
