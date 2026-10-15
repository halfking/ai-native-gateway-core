package executors

// executor_probe_one.go — P0-D（2026-10-14 magpie 对标）：全灭时
// probeOne（按 CoolUntil 最早到期半开一个 + Redis SET NX 单飞）+
// quotaHeld（全员额度耗尽则不探测，Retry-After 对齐 earliest resetAt）。
//
// magpie fallback.go probeOne/quotaHeld：候选全 rest 时只问最早到期者；
// 全员 quota/credit 时直接复述拒绝。本仓同步 ProbeSync 原先对全部候选
// 扇出（nodeProbeSyncFanout=8），全灭放大比高。

import (
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// probeOneEnabled 默认开。测试可经 Executor.ProbeOneEnabled 覆盖；
// 未设置（nil）= true。
func (e *Executor) probeOneEnabled() bool {
	if e != nil && e.ProbeOneEnabled != nil {
		return *e.ProbeOneEnabled
	}
	return true
}

// probeOneFlight returns the model-scoped single-flight lock.
// Explicit ProbeOneFlight (生产 Redis) wins; otherwise lazy per-Executor
// memory — NOT a process-global singleton (that poisoned package tests:
// HoldTimeout TTL left "minimax-m3" locked and skipped later ProbeSync).
func (e *Executor) probeOneFlight() ProbeOneFlight {
	if e == nil {
		// 无 Executor 可挂状态 ⇒ 每次返回一把**全新的**空锁，TryAcquire 恒为
		// true，即单飞被静默关闭。此为刻意的 fail-open（与 RedisProbeOneFlight
		// 遇错 fail-open 同一姿态）：宁可多探一次，也不冻结 no-candidate 恢复。
		// 唯一调用点是 Executor.Execute（executor.go 的 hold 分支），e 非 nil；
		// 该臂是防御性的，不可达。
		return NewMemoryProbeOneFlight()
	}
	if e.ProbeOneFlight != nil {
		return e.ProbeOneFlight
	}
	e.probeOneMemOnce.Do(func() {
		e.probeOneMem = NewMemoryProbeOneFlight()
	})
	return e.probeOneMem
}

// selectProbeOne 在启用时把同步探测候选收窄为 1 个：CoolUntil 最早到期者
// （零值排最后）；同刻按 CredentialID / RawModel 稳定破平。空/单元素原样返回。
func selectProbeOne(cands []credentialstate.NoCandidatesCandidate, enabled bool) []credentialstate.NoCandidatesCandidate {
	if !enabled || len(cands) <= 1 {
		return cands
	}
	out := append([]credentialstate.NoCandidatesCandidate(nil), cands...)
	sort.Slice(out, func(i, j int) bool {
		ci, cj := coolSortKey(out[i].CoolUntil), coolSortKey(out[j].CoolUntil)
		if !ci.Equal(cj) {
			return ci.Before(cj)
		}
		if out[i].CredentialID != out[j].CredentialID {
			return out[i].CredentialID < out[j].CredentialID
		}
		return out[i].RawModel < out[j].RawModel
	})
	return out[:1]
}

// coolSortKey maps zero CoolUntil to a far-future sentinel so unknown
// cooling sorts after known soonest half-open targets.
func coolSortKey(t time.Time) time.Time {
	if t.IsZero() {
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return t
}

// candidateCoolUntil extracts the rest-until moment for probeOne / quotaHeld.
// Prefers RoutedNodeState.DisabledUntil (router health snapshot); callers may
// overlay circuit coolingExpires via enrichCandidateCoolUntil.
func candidateCoolUntil(c provider.Candidate) time.Time {
	if c.RoutedNodeState != nil && c.RoutedNodeState.DisabledUntil > 0 {
		return time.Unix(c.RoutedNodeState.DisabledUntil, 0).UTC()
	}
	return time.Time{}
}

// enrichCandidateCoolUntil fills CoolUntil from node state, then circuit.
func (e *Executor) enrichCandidateCoolUntil(c provider.Candidate) time.Time {
	if t := candidateCoolUntil(c); !t.IsZero() {
		return t
	}
	if e != nil && e.Circuit != nil {
		if b := e.Circuit.Get(c.ProviderID, c.CredentialID); b != nil {
			return b.CoolingUntil()
		}
	}
	return time.Time{}
}

// earliestCoolUntil returns the soonest non-zero CoolUntil among candidates.
func earliestCoolUntil(cands []provider.Candidate, coolOf func(provider.Candidate) time.Time) time.Time {
	if coolOf == nil {
		coolOf = candidateCoolUntil
	}
	var earliest time.Time
	for _, c := range cands {
		t := coolOf(c)
		if t.IsZero() {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// retryAfterSecondsFrom maps resetAt → Retry-After seconds.
// Missing/past → 30（与旧固定值兼容）；夹到 [1, 7200]（P0-4 2h 上限同族）。
func retryAfterSecondsFrom(resetAt, now time.Time) int {
	if resetAt.IsZero() || !resetAt.After(now) {
		return 30
	}
	sec := int(resetAt.Sub(now).Seconds())
	if sec < 1 {
		return 1
	}
	if sec > 7200 {
		return 7200
	}
	return sec
}

// quotaHeldAll 报告「剩余候选是否全是额度/余额耗尽」——是则不应 ProbeSync
// （打上游只会再收一次 402/429，浪费放大比）。任一候选带非配额原因 → false。
//
// 生产形态：额度耗尽行几乎总是 Routable=false，UnavailableReason 会带
// `routing_blocked` 前缀（provider.Candidate 累加语义）。routing_blocked 是
// 「不可路由」包装，不是独立故障类——有 quota:/balance: 证据时忽略它，
// 否则 quotaHeld 在生产恒 false（假绿债）。circuit / auth / lifecycle 仍否决。
func quotaHeldAll(cands []provider.Candidate) bool {
	if len(cands) == 0 {
		return false
	}
	for _, c := range cands {
		if !isQuotaHeldReason(c.UnavailableReason()) {
			return false
		}
	}
	return true
}

// isQuotaHeldReason reports whether a candidate's UnavailableReason is pure
// quota/balance exhaustion (optionally wrapped in routing_blocked).
func isQuotaHeldReason(reason string) bool {
	if reason == "" {
		return false
	}
	hasQuota := false
	for _, part := range strings.Split(reason, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "quota:") || strings.HasPrefix(part, "balance:") {
			hasQuota = true
			continue
		}
		if part == "routing_blocked" || strings.HasPrefix(part, "routing_blocked:") {
			continue
		}
		return false
	}
	return hasQuota
}

// toNoCandidatesProbeSlice builds the ProbeSync input with CoolUntil filled.
func (e *Executor) toNoCandidatesProbeSlice(cands []provider.Candidate) []credentialstate.NoCandidatesCandidate {
	raw := make([]credentialstate.NoCandidatesCandidate, 0, len(cands))
	for _, c := range cands {
		if c.CredentialID == 0 {
			continue
		}
		raw = append(raw, credentialstate.NoCandidatesCandidate{
			CredentialID: c.CredentialID,
			ProviderID:   c.ProviderID,
			RawModel:     candidateRawModel(c),
			BillingMode:  c.BillingMode,
			CoolUntil:    e.enrichCandidateCoolUntil(c),
		})
	}
	return selectProbeOne(raw, e.probeOneEnabled())
}
