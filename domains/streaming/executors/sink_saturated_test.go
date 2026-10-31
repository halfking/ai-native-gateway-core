package executors

// sink_saturated_test.go — B1（满槽候选预跳）的门。
//
// 判据（研究方案 §五 B1 验收量具）：
//	① 满槽候选仍在候选集（不丢失）；
//	② 顺序沉底（同层相对顺序保持，open 段在前、sunk 段在后）；
//	③ 无信号时零变化（无 LiveLoad / 无满槽 ⇒ 逐元素一致）；
//	④ lottoLen 随沉底收缩（首跳抽签不抽满槽者）；全员满槽 ⇒ lottoLen 保持
//	   （总得有人先走）。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// satLiveLoad 按 (credID, model) 返回在途数。
type satLiveLoad struct {
	concurrent map[credModelKey]int64
}

func (s *satLiveLoad) GetLiveConcurrent(credID int64, model string) int64 {
	if s == nil {
		return 0
	}
	return s.concurrent[credModelKey{CredID: credID, Model: model}]
}

func satRouter(ll *satLiveLoad) *Router {
	return &Router{LiveLoad: ll}
}

func satCand(credID int, model string, limit *int) provider.Candidate {
	return provider.Candidate{CredentialID: credID, RawModel: model, ConcurrencyLimit: limit}
}

func satIntPtr(v int) *int { return &v }

// 判据②+④：满槽者沉底、顺序保持、lottoLen 收缩。
func TestSinkSaturatedCandidatesSinksAndShrinksLotto(t *testing.T) {
	limit := 2
	ll := &satLiveLoad{concurrent: map[credModelKey]int64{
		{CredID: 1, Model: "m"}: 2, // 满槽
		{CredID: 3, Model: "m"}: 2, // 满槽
		// cred 2 / 4 未满
	}}
	r := satRouter(ll)
	in := []provider.Candidate{
		satCand(1, "m", &limit),
		satCand(2, "m", &limit),
		satCand(3, "m", &limit),
		satCand(4, "m", &limit),
	}
	out, lotto := sinkSaturatedCandidates(in, 4, r)

	// ① 不丢失：集合相等。
	if len(out) != 4 {
		t.Fatalf("len = %d want 4 (no candidate dropped)", len(out))
	}
	seen := map[int]bool{}
	for _, c := range out {
		seen[c.CredentialID] = true
	}
	for _, id := range []int{1, 2, 3, 4} {
		if !seen[id] {
			t.Fatalf("credential %d dropped from candidate set", id)
		}
	}
	// ② 顺序沉底：open 段 (2,4) 在前、sunk 段 (1,3) 在后，各自相对顺序保持。
	want := []int{2, 4, 1, 3}
	for i, c := range out {
		if c.CredentialID != want[i] {
			t.Fatalf("out[%d] = cred %d want %d (order %v)", i, c.CredentialID, want[i], want)
		}
	}
	// ④ lottoLen 收缩到 open 段长度。
	if lotto != 2 {
		t.Fatalf("lottoLen = %d want 2 (lottery stays in open segment)", lotto)
	}
}

// 判据③a：无满槽信号 ⇒ 逐元素一致（零变化）。
func TestSinkSaturatedCandidatesNoSignalUnchanged(t *testing.T) {
	limit := 2
	ll := &satLiveLoad{concurrent: map[credModelKey]int64{
		{CredID: 1, Model: "m"}: 1, // 未满
	}}
	r := satRouter(ll)
	in := []provider.Candidate{satCand(1, "m", &limit), satCand(2, "m", &limit)}
	out, lotto := sinkSaturatedCandidates(in, 2, r)
	if lotto != 2 {
		t.Fatalf("lottoLen = %d want 2 (unchanged)", lotto)
	}
	for i := range in {
		if out[i].CredentialID != in[i].CredentialID {
			t.Fatalf("out[%d] = cred %d want %d (must be element-identical)", i, out[i].CredentialID, in[i].CredentialID)
		}
	}
}

// 判据③b：容量未知 ⇒ fail-open 不沉底。
func TestSinkSaturatedCandidatesUnknownCapacityFailOpen(t *testing.T) {
	ll := &satLiveLoad{concurrent: map[credModelKey]int64{
		{CredID: 1, Model: "m"}: 99, // 在途很大但无容量上限 ⇒ 无法判定满
	}}
	r := satRouter(ll)
	in := []provider.Candidate{satCand(1, "m", nil)}
	out, lotto := sinkSaturatedCandidates(in, 1, r)
	if lotto != 1 {
		t.Fatalf("lottoLen = %d want 1", lotto)
	}
	if out[0].CredentialID != 1 {
		t.Fatalf("candidate re-ordered despite unknown capacity")
	}
}

// 判据④边界：全员满槽 ⇒ 仍全员在集，lottoLen 保持原值（总得有人先走）。
func TestSinkSaturatedCandidatesAllSaturatedKeepsLotto(t *testing.T) {
	limit := 1
	ll := &satLiveLoad{concurrent: map[credModelKey]int64{
		{CredID: 1, Model: "m"}: 1,
		{CredID: 2, Model: "m"}: 1,
	}}
	r := satRouter(ll)
	in := []provider.Candidate{satCand(1, "m", &limit), satCand(2, "m", &limit)}
	out, lotto := sinkSaturatedCandidates(in, 2, r)
	if len(out) != 2 {
		t.Fatalf("len = %d want 2", len(out))
	}
	if lotto != 2 {
		t.Fatalf("lottoLen = %d want 2 (all saturated ⇒ keep)", lotto)
	}
}

// nil Router / 单候选 ⇒ 原样返回。
func TestSinkSaturatedCandidatesDegenerateInputs(t *testing.T) {
	in := []provider.Candidate{satCand(1, "m", nil)}
	out, lotto := sinkSaturatedCandidates(in, 1, nil)
	if lotto != 1 || out[0].CredentialID != 1 {
		t.Fatal("nil router must pass through")
	}
	single := []provider.Candidate{satCand(5, "m", satIntPtr(1))}
	out2, lotto2 := sinkSaturatedCandidates(single, 1, satRouter(&satLiveLoad{concurrent: map[credModelKey]int64{{CredID: 5, Model: "m"}: 1}}))
	if lotto2 != 1 || out2[0].CredentialID != 5 {
		t.Fatal("single candidate must pass through")
	}
}
