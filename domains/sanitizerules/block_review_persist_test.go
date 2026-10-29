package sanitizerules

// block_review_persist_test.go —— 拦截持久化轮（2026-10-29）的语义判据。
//
// 用 fake persistence（实现 ReviewPersistence）钉「队列/放行/清单三件事的
// 落库语义」；SQL 形状由 postgres_block_review_test.go 的 sqlmock 另行钉住。
//
// 判据纪律：每个用例前后清干净进程内全局（队列/放行表/清单/持久化通道），
// 否则用例间互相污染；每个「降级」用例都有对照（持久化正常时行为不同）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeReviewPersistence 是可注入失败的最小持久化实现。
type fakeReviewPersistence struct {
	mu        sync.Mutex
	rows      map[int64]BlockReview
	nextID    int64
	releases  []EgressRelease
	hardblock []HardBlockAllow
	hbFound   bool

	errDedupe, errInsert, errList, errGet, errDecide, errUpsert, errDelete, errLoadReleases, errLoadHB, errSaveHB error
}

func newFakeReviewPersistence() *fakeReviewPersistence {
	return &fakeReviewPersistence{rows: map[int64]BlockReview{}}
}

func (f *fakeReviewPersistence) FindPendingBlockReview(_ context.Context, tenantID, scope, rule, digest, requestID string) (BlockReview, bool, error) {
	if f.errDedupe != nil {
		return BlockReview{}, false, f.errDedupe
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.Status == BlockReviewPending && r.TenantID == tenantID && r.Scope == scope &&
			r.Rule == rule && r.Digest == digest && r.RequestID == requestID {
			return r, true, nil
		}
	}
	return BlockReview{}, false, nil
}

func (f *fakeReviewPersistence) InsertBlockReview(_ context.Context, r BlockReview) (BlockReview, error) {
	if f.errInsert != nil {
		return BlockReview{}, f.errInsert
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	r.ID = f.nextID
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	f.rows[r.ID] = r
	return r, nil
}

func (f *fakeReviewPersistence) ListBlockReviewsDB(_ context.Context, status, tenantID string, limit int) ([]BlockReview, error) {
	if f.errList != nil {
		return nil, f.errList
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []BlockReview{}
	for i := f.nextID; i >= 1 && len(out) < limit; i-- {
		r, ok := f.rows[i]
		if !ok {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		if tenantID != "" && r.TenantID != tenantID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeReviewPersistence) GetBlockReviewDB(_ context.Context, id int64) (BlockReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return BlockReview{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeReviewPersistence) DecideBlockReviewDB(_ context.Context, id int64, status, decidedBy string, decidedAt time.Time) (BlockReview, error) {
	if f.errDecide != nil {
		return BlockReview{}, f.errDecide
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return BlockReview{}, ErrNotFound
	}
	if r.Status != BlockReviewPending {
		return r, ErrConflict
	}
	r.Status = status
	r.DecidedBy = decidedBy
	r.DecidedAt = decidedAt
	f.rows[id] = r
	return r, nil
}

func (f *fakeReviewPersistence) UpsertEgressReleaseDB(_ context.Context, r EgressRelease) error {
	if f.errUpsert != nil {
		return f.errUpsert
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.releases {
		if f.releases[i].TenantID == r.TenantID && f.releases[i].Scope == r.Scope &&
			f.releases[i].Rule == r.Rule && f.releases[i].PayloadDigest == r.PayloadDigest {
			f.releases[i] = r
			return nil
		}
	}
	f.releases = append(f.releases, r)
	return nil
}

func (f *fakeReviewPersistence) DeleteEgressReleaseDB(_ context.Context, tenantID, scope, rule, digest string) (bool, error) {
	if f.errDelete != nil {
		return false, f.errDelete
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	removed := false
	out := f.releases[:0]
	for _, r := range f.releases {
		if r.TenantID == tenantID && r.Scope == scope && r.Rule == rule && (digest == "" || r.PayloadDigest == digest) {
			removed = true
			continue
		}
		out = append(out, r)
	}
	f.releases = out
	return removed, nil
}

func (f *fakeReviewPersistence) LoadEgressReleasesDB(_ context.Context) ([]EgressRelease, error) {
	if f.errLoadReleases != nil {
		return nil, f.errLoadReleases
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EgressRelease(nil), f.releases...), nil
}

func (f *fakeReviewPersistence) LoadHardBlockState(_ context.Context) ([]HardBlockAllow, bool, error) {
	if f.errLoadHB != nil {
		return nil, false, f.errLoadHB
	}
	if !f.hbFound {
		return nil, false, nil
	}
	return append([]HardBlockAllow(nil), f.hardblock...), true, nil
}

func (f *fakeReviewPersistence) SaveHardBlockState(_ context.Context, allows []HardBlockAllow, _ string) error {
	if f.errSaveHB != nil {
		return f.errSaveHB
	}
	f.hardblock = append([]HardBlockAllow(nil), allows...)
	f.hbFound = true
	return nil
}

func resetPersistGlobals(t *testing.T, releases []EgressRelease) {
	t.Helper()
	ResetBlockReviews()
	prevReleases := EgressReleases()
	SetEgressReleases(releases)
	prevAllows := HardBlockAllows()
	SetHardBlockAllows([]HardBlockAllow{{Scope: "input", Rule: "*"}})
	t.Cleanup(func() {
		ResetBlockReviews()
		SetEgressReleases(prevReleases)
		SetHardBlockAllows(prevAllows)
	})
}

// TestPersistedReviewRoundTripAndApproval：登记落库 → 去重复用 → 列表读 DB →
// 批准写 DB + 规则级 7 天放行（DB + 进程内）。
func TestPersistedReviewRoundTripAndApproval(t *testing.T) {
	p := newFakeReviewPersistence()
	resetPersistGlobals(t, nil)
	InstallReviewPersistence(p)
	t.Cleanup(ResetReviewPersistence)

	first := AddBlockReview("acme", "zcode", "egress", "builtin.egress_sink",
		"", "sink destination", "$.command", "digest-x", "req-77", "说明")
	if first.ID == 0 {
		t.Fatal("持久化路径必须给出 DB 序列号")
	}
	again := AddBlockReview("acme", "zcode", "egress", "builtin.egress_sink",
		"", "sink destination", "$.command", "digest-x", "req-77", "说明")
	if again.ID != first.ID {
		t.Fatalf("同键 pending 应复用同一行: %d vs %d", again.ID, first.ID)
	}
	list := ListBlockReviews(BlockReviewPending, "acme", 10)
	if len(list) != 1 || list[0].ClientType != "zcode" || list[0].MatchDetail == "" {
		t.Fatalf("列表读数不对: %+v", list)
	}
	if _, err := DecideBlockReviewWithContext(context.Background(), first.ID, true, "boss"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if !EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "digest-x") {
		t.Fatal("批准后进程内放行必须立即生效")
	}
	if len(p.releases) != 1 || p.releases[0].Source != "review" {
		t.Fatalf("DB 放行来源应为 review: %+v", p.releases)
	}
	if ttl := time.Until(p.releases[0].ExpiresAt); ttl < 6*24*time.Hour || ttl > 8*24*time.Hour {
		t.Fatalf("7 天放行过期时间不对: %v", ttl)
	}
	if p.releases[0].PayloadDigest != "" {
		t.Fatalf("规则级放行不应绑定载荷摘要: %q", p.releases[0].PayloadDigest)
	}
}

// TestPersistedReviewDBFailureFallsBackToMemory：DB 故障时登记降级进程内
// （拦截主路径不因记录失败而中断），列表同样降级——丢单要 WARN 可见，
// 但不能把异常抛进检测链。
func TestPersistedReviewDBFailureFallsBackToMemory(t *testing.T) {
	p := newFakeReviewPersistence()
	p.errInsert = errors.New("db down")
	p.errList = errors.New("db down")
	resetPersistGlobals(t, nil)
	InstallReviewPersistence(p)
	t.Cleanup(ResetReviewPersistence)

	entry := AddBlockReview("acme", "zcode", "egress", "builtin.egress_sink",
		"", "sink", "$.command", "digest-y", "req-88", "")
	if entry.ID == 0 {
		t.Fatal("DB 插入失败应降级内存路径（仍有单号）")
	}
	if len(p.rows) != 0 {
		t.Fatal("DB 故障时不应有行落库")
	}
	list := ListBlockReviews(BlockReviewPending, "acme", 10)
	if len(list) != 1 {
		t.Fatalf("列表应降级内存队列: %d", len(list))
	}
}

// TestInstallReviewPersistenceBootLoad：启动装载——放行进内存表、管理面清单
// 覆盖 env。
func TestInstallReviewPersistenceBootLoad(t *testing.T) {
	p := newFakeReviewPersistence()
	p.releases = []EgressRelease{{TenantID: "acme", Scope: "egress", Rule: "builtin.egress_sink", Source: "review", ExpiresAt: time.Now().Add(time.Hour)}}
	p.hardblock = []HardBlockAllow{{Scope: "input", Rule: "*"}, {Scope: "egress", Rule: "builtin.egress_sink"}}
	p.hbFound = true
	resetPersistGlobals(t, nil)
	InstallReviewPersistence(p)
	t.Cleanup(ResetReviewPersistence)

	if !EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "anything") {
		t.Fatal("启动装载后放行必须在进程内生效")
	}
	if !HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("启动装载后管理面清单必须覆盖默认清单")
	}
}

// TestHardblockStatePersistWriteThrough：管理面保存写穿透（进程内立即生效 +
// DB 持久化）；DB 失败时进程内仍生效。
func TestHardblockStatePersistWriteThrough(t *testing.T) {
	p := newFakeReviewPersistence()
	resetPersistGlobals(t, nil)
	InstallReviewPersistence(p)
	t.Cleanup(ResetReviewPersistence)

	SaveHardBlockStatePersistent(context.Background(),
		[]HardBlockAllow{{Scope: "input", Rule: "*"}, {Scope: "output", Rule: "*"}}, "boss")
	if !HardBlockAllowed("output", "pii.email") {
		t.Fatal("保存后进程内清单必须立即生效")
	}
	if !p.hbFound || len(p.hardblock) != 2 {
		t.Fatalf("清单必须落库: %+v", p.hardblock)
	}

	// DB 失败对照：进程内仍然生效（重启回落 env，WARN 可见）。
	p2 := newFakeReviewPersistence()
	p2.errSaveHB = errors.New("db down")
	SaveHardBlockStatePersistent(context.Background(), nil, "boss")
	if HardBlockAllowed("input", "any") {
		t.Fatal("显式保存空清单后进程内必须关闭全部硬阻断")
	}
}

// TestEgressReleasePersistentWriteThrough：放行授予/撤销写穿透。
func TestEgressReleasePersistentWriteThrough(t *testing.T) {
	p := newFakeReviewPersistence()
	resetPersistGlobals(t, nil)
	InstallReviewPersistence(p)
	t.Cleanup(ResetReviewPersistence)

	if err := AddEgressReleasePersistent(context.Background(), EgressRelease{
		TenantID: "acme", Scope: "egress", Rule: "builtin.egress_sink", Source: "manual",
	}); err != nil {
		t.Fatalf("授予失败: %v", err)
	}
	if !EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") || len(p.releases) != 1 {
		t.Fatal("授予必须同时生效于进程内与 DB")
	}
	if !RemoveEgressReleasePersistent(context.Background(), "acme", "egress", "builtin.egress_sink", "") {
		t.Fatal("撤销应命中（进程内命中 || DB 命中）")
	}
	if EgressReleaseAllowed("acme", "egress", "builtin.egress_sink", "") || len(p.releases) != 0 {
		t.Fatal("撤销后两层都必须干净")
	}
}

// TestBuiltinRuleDescriptions：说明表封闭集——三条内建规则有名字与语义说明；
// 未知 ID 返回 ok=false（前端退化显示规则 ID，不编造说明）。
func TestBuiltinRuleDescriptions(t *testing.T) {
	for _, id := range []string{"builtin.egress_sink", "builtin.network_destination", "builtin.secret_field.egress"} {
		info, ok := BuiltinRuleInfoFor(id)
		if !ok || info.Name == "" || len(info.Description) < 20 {
			t.Errorf("内建规则 %s 缺展示名或语义说明", id)
		}
	}
	if _, ok := BuiltinRuleInfoFor("pii.email.egress"); ok {
		t.Error("托管规则不在内建表里（由前端关联规则配置）")
	}
	// 「全部允许」预设必须过校验器（egress 逐条内建，r1039 约束）。
	for _, a := range BuiltinHardBlockAll() {
		if err := ValidateHardBlockAllow(a.Scope, a.Rule); err != nil {
			t.Errorf("预设清单项 %s:%s 不合法: %v", a.Scope, a.Rule, err)
		}
	}
}

// TestHardblockSingleRuleToggle：单条规则的同步开/关——add 幂等、remove 只删
// 精确同形条目。
func TestHardblockSingleRuleToggle(t *testing.T) {
	resetPersistGlobals(t, nil)
	if err := AddHardBlockAllow("egress", "builtin.egress_sink"); err != nil {
		t.Fatalf("开启单条失败: %v", err)
	}
	if !HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("开启后必须生效")
	}
	// 幂等：重复添加不产生重复条目。
	if err := AddHardBlockAllow("egress", "builtin.egress_sink"); err != nil {
		t.Fatalf("重复添加应 no-op: %v", err)
	}
	if got := len(HardBlockAllows()); got != 2 {
		t.Fatalf("重复添加产生了重复条目: %d", got)
	}
	// r1039 约束继续生效：egress 通配/前缀族被拒。
	if err := AddHardBlockAllow("egress", "*"); err == nil {
		t.Fatal("egress 通配必须被拒")
	}
	if !RemoveHardBlockAllow("egress", "builtin.egress_sink") {
		t.Fatal("关闭单条应命中")
	}
	if HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Fatal("关闭后必须失效")
	}
	if RemoveHardBlockAllow("egress", "builtin.egress_sink") {
		t.Fatal("重复关闭应不命中")
	}
	// input:* 不受误伤（精确同形删除）。
	if !HardBlockAllowed("input", "any") {
		t.Fatal("单条开关不得波及其它条目")
	}
}
