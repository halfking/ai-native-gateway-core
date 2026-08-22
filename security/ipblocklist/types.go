package ipblocklist

import (
	"context"
	"net"
	"time"
)

const (
	ScopeGlobal  = "global"
	ScopeCollect = "collect"
	ScopeOps     = "ops"

	SourceManual     = "manual"
	SourceAutoAttack = "auto_attack"

	redisKeyGlobal  = "llmgw:blocklist:global"
	redisKeyCollect = "llmgw:blocklist:collect"
	redisKeyOps     = "llmgw:blocklist:ops"
	redisVersionKey = "llmgw:blocklist:version"
)

// Entry is a blocked IP or CIDR.
type Entry struct {
	ID        int64      `json:"id"`
	IPOrCIDR  string     `json:"ip_or_cidr"`
	Reason    string     `json:"reason"`
	Scope     string     `json:"scope"`
	Source    string     `json:"source"`
	Enabled   bool       `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	HitCount  int64      `json:"hit_count"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// CreateInput for new blocklist rows.
type CreateInput struct {
	IPOrCIDR  string
	Reason    string
	Scope     string
	Source    string
	ExpiresAt *time.Time
	CreatedBy string
}

// UpdateInput for patch operations.
type UpdateInput struct {
	Reason    *string
	Enabled   *bool
	ExpiresAt *time.Time
}

// Store persists and queries blocklist entries.
type Store interface {
	List(ctx context.Context, scope string, limit, offset int) ([]Entry, int, error)
	Get(ctx context.Context, id int64) (*Entry, error)
	Create(ctx context.Context, in CreateInput) (*Entry, error)
	Update(ctx context.Context, id int64, in UpdateInput) (*Entry, error)
	Delete(ctx context.Context, id int64) error
	ListActive(ctx context.Context, scope string) ([]Entry, error)
	IncrementHit(ctx context.Context, id int64) error
	IsBlocked(ctx context.Context, ip net.IP, scope string) (bool, *Entry, error)
}
