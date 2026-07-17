package autoroute

// default_routing_store.go — M2: 显式默认路由（task_default_routing 表）。
//
// 让运营为 (task_type, profile, tenant_id) 显式指定首选/兜底模型。优先级介于
// override pin（最高）与隐式 tag 评分（最低）之间。详见
// docs/拆分/22-Auto智能路由与任务识别.md §22.6。
//
// 与 override_store.go 完全同构：atomic.Pointer[snapshot] 模式，1-min 后台
// Reload（复用 bg/ 的 TuningStoreRefresher 同款 ticker），热路径零分配。
//
// Resolve 优先级（首个命中即返回）：
//  1. (task_type, profile,        tenant_id=租户)
//  2. (task_type, profile='',     tenant_id=租户)
//  3. (task_type, profile,        tenant_id=NULL)
//  4. (task_type, profile='',     tenant_id=NULL)
//
// 未命中返回 (ok=false)，Decider 回退到隐式 tag 评分（RoutingSource=implicit_tag）。

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RoutingTier 表达注入候选池时的 tier，与 Candidate.Tier 一致。
type RoutingTier string

const (
	RoutingPrimary   RoutingTier = "primary"
	RoutingSecondary RoutingTier = "secondary"
	RoutingFallback  RoutingTier = "fallback"
)

// DefaultRouting 是 task_default_routing 的一行。
type DefaultRouting struct {
	ID             int64
	TaskType       string
	Profile        string // "" = 任意 profile（通用）
	Tier           RoutingTier
	CanonicalModel string
	TenantID       *int64 // nil = 平台默认
	Priority       int
	Reason         string
	CreatedBy      string
	ExpiresAt      *time.Time
}

// DefaultRoutingResolution 是 Resolve 的返回。Tier 指示注入候选池的位置。
type DefaultRoutingResolution struct {
	CanonicalModel string
	Tier           RoutingTier
	Priority       int
	// ScopeLevel 标注命中级别，用于审计（"tenant_profile" / "tenant_generic" /
	// "platform_profile" / "platform_generic"）。
	ScopeLevel string
}

// defaultRoutingSnapshot 是某个时刻所有未过期行的不可变视图。
// 按 (task_type) 一级索引，内部按 profile/tenant 组织以便 4 级查找。
type defaultRoutingSnapshot struct {
	// byTask[taskType] = 该 task 下所有行；Resolve 内部做 4 级筛选。
	byTask   map[string][]DefaultRouting
	LoadedAt time.Time
}

// DefaultRoutingStore 加载并暴露 task_default_routing 的活跃行。
type DefaultRoutingStore struct {
	pool     *pgxpool.Pool
	snapshot atomic.Pointer[defaultRoutingSnapshot]
}

// NewDefaultRoutingStore 构造空 store。首次使用前必须 Reload。
func NewDefaultRoutingStore(pool *pgxpool.Pool) *DefaultRoutingStore {
	return &DefaultRoutingStore{pool: pool}
}

// current 返回活跃快照；从未 Reload 成功时返回空快照。绝不返回 nil。
func (s *DefaultRoutingStore) current() *defaultRoutingSnapshot {
	snap := s.snapshot.Load()
	if snap == nil {
		return &defaultRoutingSnapshot{byTask: map[string][]DefaultRouting{}}
	}
	return snap
}

// Reload 从 DB 拉取所有未过期行并原子替换快照。并发安全。
func (s *DefaultRoutingStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil {
		if s != nil {
			s.snapshot.Store(&defaultRoutingSnapshot{byTask: map[string][]DefaultRouting{}})
		}
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, task_type, profile, tier, canonical_model, tenant_id,
		       priority, reason, created_by, expires_at
		FROM task_default_routing
		WHERE expires_at IS NULL OR expires_at > NOW()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &defaultRoutingSnapshot{
		byTask:   make(map[string][]DefaultRouting),
		LoadedAt: time.Now(),
	}
	for rows.Next() {
		var d DefaultRouting
		var tier string
		var tenantID *int64
		var createdBy *string
		var expiresAt *time.Time
		if err := rows.Scan(&d.ID, &d.TaskType, &d.Profile, &tier,
			&d.CanonicalModel, &tenantID, &d.Priority, &d.Reason,
			&createdBy, &expiresAt); err != nil {
			continue
		}
		d.Tier = RoutingTier(tier)
		d.TenantID = tenantID
		if createdBy != nil {
			d.CreatedBy = *createdBy
		}
		d.ExpiresAt = expiresAt
		snap.byTask[d.TaskType] = append(snap.byTask[d.TaskType], d)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.snapshot.Store(snap)
	return nil
}

// Resolve 按 4 级优先级查找 (taskType, profile, tenantID) 的默认模型。
// tenantID <= 0 视为无租户（只查平台级）。返回 ok=false 表示无显式默认。
func (s *DefaultRoutingStore) Resolve(taskType, profile string, tenantID int64) (DefaultRoutingResolution, bool) {
	snap := s.current()
	rows := snap.byTask[taskType]
	if len(rows) == 0 {
		return DefaultRoutingResolution{}, false
	}
	hasTenant := tenantID > 0

	// 4 级优先级。每级内按 priority DESC 取首个。
	type level struct {
		name         string
		matchProfile bool
		matchTenant  bool // true=要求 tenant 命中；false=要求 tenant_id IS NULL
	}
	levels := []level{
		{"tenant_profile", true, true},
		{"tenant_generic", false, true},
		{"platform_profile", true, false},
		{"platform_generic", false, false},
	}

	for _, lv := range levels {
		var best *DefaultRouting
		for i := range rows {
			r := &rows[i]
			// profile 匹配：通用行(profile=='')只匹配 generic 级别；
			// profile 级别要求 row.profile == 请求 profile。
			if lv.matchProfile {
				if r.Profile != profile || r.Profile == "" {
					continue
				}
			} else {
				if r.Profile != "" {
					continue
				}
			}
			// tenant 匹配
			if lv.matchTenant {
				if !hasTenant || r.TenantID == nil || *r.TenantID != tenantID {
					continue
				}
			} else {
				if r.TenantID != nil {
					continue
				}
			}
			if best == nil || r.Priority > best.Priority {
				best = r
			}
		}
		if best != nil {
			return DefaultRoutingResolution{
				CanonicalModel: best.CanonicalModel,
				Tier:           best.Tier,
				Priority:       best.Priority,
				ScopeLevel:     lv.name,
			}, true
		}
	}
	return DefaultRoutingResolution{}, false
}
