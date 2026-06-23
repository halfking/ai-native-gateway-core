package domain

// CredRouteContext 封装凭据路由领域的上下文。
type CredRouteContext struct {
	KeyID             int
	ConcurrentLimit   int
	Candidates        []Candidate
	SelectedIndex     int
	StickyKeyID       int
	SlotID            string
	PoolState         string
	DisguiseProfileID string
}

// Candidate 候选凭据。
type Candidate struct {
	KeyID             int
	Provider          string
	Endpoint          string
	Weight            int
	ConcurrentUsage   int
	ConcurrentLimit   int
	HealthScore       float64
	CostPer1kTokens   float64
	LastUsedAtUnixMs  int64
	CooldownUntilUnix int64
	Disabled          bool
}
