package executors

import "github.com/kaixuan/llm-gateway-go/provider"

func filterCandidatesByStreamMode(candidates []provider.Candidate, stream bool) (allowed, blocked []provider.Candidate) {
	allowed = make([]provider.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.StreamMode.Allows(stream) {
			allowed = append(allowed, candidate)
			continue
		}
		blocked = append(blocked, candidate)
	}
	return allowed, blocked
}

func streamModeMismatchTrace(blocked []provider.Candidate) *Trace {
	trace := &Trace{
		PlannedCandidates: make([]TraceCandidate, 0, len(blocked)),
		BlockedCandidates: make([]TraceCandidate, 0, len(blocked)),
		FailureReason:     "stream_mode_mismatch",
	}
	for _, candidate := range blocked {
		item := TraceCandidate{
			ProviderID:   candidate.ProviderID,
			CredentialID: candidate.CredentialID,
			RawModel:     candidateRawModel(candidate),
			Tier:         candidate.Tier,
			Reason:       "stream_mode:" + string(candidate.StreamMode.Normalized()),
		}
		trace.PlannedCandidates = append(trace.PlannedCandidates, item)
		trace.BlockedCandidates = append(trace.BlockedCandidates, item)
	}
	return trace
}
