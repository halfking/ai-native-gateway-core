package bg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// fakeUrsmlState 是 reconciler 对 URSM v2 视图/写入面的替身。
// 视图按 seed 顺序返回预置 NodeView，写侧记录 ApplyProbeForTenant 调用。
type fakeUrsmlState struct {
	mode    ursmv2api.RolloutMode
	ready   bool
	views   []ursmv2api.NodeView
	viewErr error
	seen    []ursmv2.CandidateSeed

	applied  []ursmv2api.ProbeOutcome
	tenants  []string
	applyErr error
}

func (f *fakeUrsmlState) Mode() ursmv2api.RolloutMode { return f.mode }

func (f *fakeUrsmlState) Ready(ctx context.Context) bool { return f.ready }

func (f *fakeUrsmlState) FilterAndScoreReady(ctx context.Context, seeds []ursmv2.CandidateSeed, ready bool) ([]ursmv2api.NodeView, error) {
	f.seen = seeds
	if f.viewErr != nil {
		return nil, f.viewErr
	}
	if f.views != nil {
		return f.views, nil
	}
	// T4 缺键语义：键不存在 ⇒ Available=false。views 未预置时按 seed 逐个
	// 合成不可用视图，与生产读路径对缺失键的判定一致。
	out := make([]ursmv2api.NodeView, len(seeds))
	for i, s := range seeds {
		out[i] = viewFor(s, false)
	}
	return out, nil
}

func (f *fakeUrsmlState) ApplyProbeForTenant(ctx context.Context, tenant string, p ursmv2api.ProbeOutcome) error {
	f.applied = append(f.applied, p)
	f.tenants = append(f.tenants, tenant)
	return f.applyErr
}

// seedHealthyRows 让 pgxmock 返回 (tenant, credential_id, raw_model_name, provider_id) 四列。
func seedHealthyRows(mock pgxmock.PgxPoolIface, rows [][]any) {
	expect := mock.ExpectQuery("SELECT DISTINCT").
		WithArgs(ursmHealthyReconcileLimit)
	out := pgxmock.NewRows([]string{"tenant_id", "credential_id", "raw_model_name", "provider_id"})
	for _, r := range rows {
		out = out.AddRow(r[0], r[1], r[2], r[3])
	}
	expect.WillReturnRows(out)
}

func viewFor(seed ursmv2.CandidateSeed, available bool) ursmv2api.NodeView {
	return ursmv2api.NodeView{
		ProviderID:   seed.ProviderID,
		CredentialID: seed.CredentialID,
		RawModel:     seed.RawModel,
		TenantID:     seed.TenantID,
		Available:    available,
	}
}

// TestUrsmlHealthyReconcileRewritesUnavailableHealthyPairs 是核心判据：
// DB 健康且视图判不可用的对，必须经 ApplyProbeForTenant(success=true) 修复。
// 驱动入口是 reconcileOnce（与生产同层），不是包装函数。
func TestUrsmlHealthyReconcileRewritesUnavailableHealthyPairs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{
		{"default", 62, "glm-5.3", 32},
		{"default", 63, "gpt-6-astra", 36},
	})
	state := &fakeUrsmlState{mode: ursmv2api.ModeAuthoritative, ready: true}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.applied) != 2 {
		t.Fatalf("expected 2 probe-grade writes, got %d", len(state.applied))
	}
	// 写入必须是成功语义（available=1），修复的是 view 判死的对。
	for i, p := range state.applied {
		if !p.Success {
			t.Errorf("write %d: Success=false, want true", i)
		}
	}
	if state.tenants[0] != "default" || state.tenants[1] != "default" {
		t.Errorf("tenants = %v, want all default", state.tenants)
	}
}

// TestUrsmlHealthyReconcileNoOpWhenViewHealthy：视图全绿时必须零写入。
// 变异验证锚点：把 reconcileOnce 的 Available 过滤删掉（每 tick 全量重写）
// 会让本测试变红。
func TestUrsmlHealthyReconcileNoOpWhenViewHealthy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{{"default", 62, "glm-5.3", 32}})
	state := &fakeUrsmlState{
		mode:  ursmv2api.ModeAuthoritative,
		ready: true,
		views: []ursmv2api.NodeView{{Available: true}},
	}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.applied) != 0 {
		t.Fatalf("expected 0 writes when all views healthy, got %d", len(state.applied))
	}
}

// TestUrsmlHealthyReconcileNeverWritesUnverifiedPairs：只有 DB 返回的对才有资格
// 被写。视图读到的 seed 集合必须与 DB 健康集合一致 —— 这是 T4 fail-closed
// 不被本 reconciler 打穿的判据。
func TestUrsmlHealthyReconcileNeverWritesUnverifiedPairs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{{"default", 62, "glm-5.3", 32}})
	state := &fakeUrsmlState{mode: ursmv2api.ModeAuthoritative, ready: true}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.seen) != 1 || state.seen[0].CredentialID != 62 || state.seen[0].RawModel != "glm-5.3" {
		t.Fatalf("seeds seen by view = %+v, want exactly the DB-healthy pair", state.seen)
	}
}

// TestUrsmlHealthyReconcileSkipsClosedGate：ready 门关着时（recovery 持有命名空间）
// 既不读视图也不写 —— 写会与 bootstrap/recovery 打架。
// ★ 判据必须真的能失败：这里播种了健康行；若门检查被绕过，流程会走到视图读
//
//	（seen 非空）并产生写入（变异验证以此变红）。
func TestUrsmlHealthyReconcileSkipsClosedGate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{{"default", 62, "glm-5.3", 32}})
	state := &fakeUrsmlState{mode: ursmv2api.ModeAuthoritative, ready: false}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.applied) != 0 {
		t.Fatalf("writes behind a closed gate: %d", len(state.applied))
	}
	if state.seen != nil {
		t.Fatalf("view read behind a closed gate: %d seeds", len(state.seen))
	}
}

// TestUrsmlHealthyReconcileSkipsEmptyTenantInAuthoritative：authoritative 模式
// 校验 seed.TenantID 非空；空租户对必须被跳过而不是让整轮失败。
func TestUrsmlHealthyReconcileSkipsEmptyTenantInAuthoritative(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{
		{"", 71, "claude-opus-5", 59},
		{"default", 62, "glm-5.3", 32},
	})
	state := &fakeUrsmlState{mode: ursmv2api.ModeAuthoritative, ready: true}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.seen) != 1 || state.seen[0].CredentialID != 62 {
		t.Fatalf("seeds = %+v, want only the non-empty-tenant pair", state.seen)
	}
}

// TestUrsmlHealthyReconcileViewErrorIsContainable：视图读失败不允许产生任何写入，
// 也不允许 panic 外溢（下一 tick 重试）。
func TestUrsmlHealthyReconcileViewErrorIsContainable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	seedHealthyRows(mock, [][]any{{"default", 62, "glm-5.3", 32}})
	state := &fakeUrsmlState{
		mode:    ursmv2api.ModeAuthoritative,
		ready:   true,
		viewErr: errors.New("redis timeout"),
	}
	r := NewUrsmlHealthyKeyReconciler(state, mock)
	r.reconcileOnce(context.Background())

	if len(state.applied) != 0 {
		t.Fatalf("writes after a view read error: %d", len(state.applied))
	}
}

// TestUrsmlHealthyReconcileDisabledBySettings：interval <= 0 时 Start 必须
// 空转（运维 kill switch），不能留下任何 goroutine 或写入。
func TestUrsmlHealthyReconcileDisabledBySettings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	state := &fakeUrsmlState{mode: ursmv2api.ModeAuthoritative, ready: true}
	r := newUrsmlHealthyKeyReconcilerWithInterval(state, mock, 0)
	if r.interval != 0 {
		t.Fatalf("interval = %v, want 0 (disabled)", r.interval)
	}
	r.Start(context.Background()) // must not panic / not write
	if len(state.applied) != 0 {
		t.Fatalf("disabled reconciler wrote %d times", len(state.applied))
	}
	// 负值同样折叠为 0（禁用哨兵），而不是按字面值跑。
	r2 := newUrsmlHealthyKeyReconcilerWithInterval(state, mock, -5*time.Second)
	if r2.interval != 0 {
		t.Fatalf("negative interval = %v, want 0", r2.interval)
	}
}
