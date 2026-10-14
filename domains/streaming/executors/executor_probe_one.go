package executors

// executor_probe_one.go — P0-D（2026-10-14 magpie 对标）：全灭时
// probeOne（每请求最多半开探测一个）+ quotaHeld（全员额度耗尽则不探测）。
//
// magpie fallback.go probeOne/quotaHeld：候选全 rest 时只问最早到期者；
// 全员 quota/credit 时直接复述拒绝。本仓同步 ProbeSync 原先对全部候选
// 扇出（nodeProbeSyncFanout=8），全灭放大比高。v1 近似：无冷却到期时刻
// 字段时按 CredentialID 稳定取 1 个；quotaHeld 按 UnavailableReason 前缀判。

import (
	"sort"
	"strings"

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

// selectProbeOne 在启用时把同步探测候选收窄为 1 个（稳定：最小 CredentialID，
// 同 ID 取 RawModel 字典序最小）。空/单元素原样返回。
func selectProbeOne(cands []credentialstate.NoCandidatesCandidate, enabled bool) []credentialstate.NoCandidatesCandidate {
	if !enabled || len(cands) <= 1 {
		return cands
	}
	out := append([]credentialstate.NoCandidatesCandidate(nil), cands...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].CredentialID != out[j].CredentialID {
			return out[i].CredentialID < out[j].CredentialID
		}
		return out[i].RawModel < out[j].RawModel
	})
	return out[:1]
}

// quotaHeldAll 报告「剩余候选是否全是额度/余额耗尽」——是则不应 ProbeSync
// （打上游只会再收一次 402/429，浪费放大比）。任一候选带非配额原因 → false。
func quotaHeldAll(cands []provider.Candidate) bool {
	if len(cands) == 0 {
		return false
	}
	any := false
	for _, c := range cands {
		reason := c.UnavailableReason()
		if reason == "" {
			return false
		}
		any = true
		// UnavailableReason 用 "; " 拼接多原因（provider/client.go），不是逗号。
		for _, part := range strings.Split(reason, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.HasPrefix(part, "quota:") || strings.HasPrefix(part, "balance:") {
				continue
			}
			return false
		}
	}
	return any
}
