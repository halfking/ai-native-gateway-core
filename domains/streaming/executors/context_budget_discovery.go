package executors

import (
	"sync"

	"github.com/kaixuan/llm-gateway-go/provider"
)

type contextBudgetBucket struct {
	credentialID int
	providerID   int
	model        string
	protocol     string
	endpoint     string
}

func contextBudgetBucketOf(cand provider.Candidate) contextBudgetBucket {
	return contextBudgetBucket{cand.CredentialID, cand.ProviderID, cand.RawModel, cand.Protocol, cand.BaseURL}
}

// This request-owned pointer survives the parameter copies used by dispatch.
// Windows never leak into another request/bucket or grow after discovery.
type contextBudgetWindows struct {
	mu      sync.Mutex
	windows map[contextBudgetBucket]int
}

func initContextBudgetWindows(params *ExecParams) {
	if params != nil && params.contextBudgetWindows == nil {
		params.contextBudgetWindows = &contextBudgetWindows{windows: make(map[contextBudgetBucket]int)}
	}
}

func recordDiscoveredContextLimit(params *ExecParams, cand provider.Candidate, limit int) {
	if params == nil || limit <= 0 {
		return
	}
	initContextBudgetWindows(params)
	s := params.contextBudgetWindows
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextBudgetBucketOf(cand)
	if old := s.windows[key]; old == 0 || limit < old {
		s.windows[key] = limit
	}
}

func discoveredContextLimit(params *ExecParams, cand provider.Candidate) int {
	if params == nil || params.contextBudgetWindows == nil {
		return 0
	}
	s := params.contextBudgetWindows
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.windows[contextBudgetBucketOf(cand)]
}
