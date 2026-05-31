package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type AliasSyncService struct {
	db       interface {
		Query(ctx context.Context, q string, args ...any) (interface{ Next() bool; Scan(...any) error; Close() error }, error)
		QueryRow(ctx context.Context, q string, args ...any) interface{ Scan(...any) error }
		Exec(ctx context.Context, q string, args ...any) error
	}
	interval time.Duration
	stopCh   chan struct{}
	mu       sync.RWMutex
	running  bool
}

type AliasSyncResult struct {
	TotalAliases   int `json:"total_aliases"`
	CleanedAliases int `json:"cleaned_aliases"`
	NewAliases     int `json:"new_aliases"`
	Errors         int `json:"errors"`
}

func NewAliasSyncService(db interface {
	Query(ctx context.Context, q string, args ...any) (interface{ Next() bool; Scan(...any) error; Close() error }, error)
	QueryRow(ctx context.Context, q string, args ...any) interface{ Scan(...any) error }
	Exec(ctx context.Context, q string, args ...any) error
}, interval time.Duration) *AliasSyncService {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &AliasSyncService{
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

func (s *AliasSyncService) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	slog.Info("alias sync service starting", "interval", s.interval)

	go func() {
		s.runSync(ctx)

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.runSync(ctx)
			case <-s.stopCh:
				slog.Info("alias sync service stopping")
				return
			case <-ctx.Done():
				slog.Info("alias sync service stopping (context done)")
				return
			}
		}
	}()
}

func (s *AliasSyncService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		close(s.stopCh)
		s.running = false
	}
}

func (s *AliasSyncService) RunOnce(ctx context.Context) (*AliasSyncResult, error) {
	return s.runSync(ctx)
}

func (s *AliasSyncService) runSync(ctx context.Context) (*AliasSyncResult, error) {
	result := &AliasSyncResult{}

	slog.Info("alias sync: starting cleanup")

	if err := s.cleanOrphanedAliases(ctx, result); err != nil {
		slog.Warn("alias sync: cleanup error", "error", err)
		result.Errors++
	}

	if err := s.syncCanonicalNames(ctx, result); err != nil {
		slog.Warn("alias sync: canonical sync error", "error", err)
		result.Errors++
	}

	if err := s.rebuildAliasIndex(ctx, result); err != nil {
		slog.Warn("alias sync: index rebuild error", "error", err)
		result.Errors++
	}

	slog.Info("alias sync completed",
		"total", result.TotalAliases,
		"cleaned", result.CleanedAliases,
		"new", result.NewAliases,
		"errors", result.Errors,
	)

	return result, nil
}

func (s *AliasSyncService) cleanOrphanedAliases(ctx context.Context, result *AliasSyncResult) error {
	type aliasRow struct {
		ID           int
		RawName      string
		CanonicalID  int
		CanonicalName string
	}

	rows, err := s.db.Query(ctx, `
		SELECT ma.id, ma.raw_name, ma.canonical_id, mc.canonical_name
		FROM model_aliases ma
		LEFT JOIN models_canonical mc ON mc.id = ma.canonical_id
		WHERE ma.status = 'active'
	`)
	if err != nil {
		return err
	}
	defer rows.(interface{ Close() error }).Close()

	var toDisable []int
	for rows.(interface{ Next() bool }).Next() {
		var r aliasRow
		if err := rows.(interface{ Scan(...any) error }).Scan(&r.ID, &r.RawName, &r.CanonicalID, &r.CanonicalName); err != nil {
			continue
		}
		result.TotalAliases++

		if r.CanonicalID == 0 || r.CanonicalName == "" {
			toDisable = append(toDisable, r.ID)
			continue
		}

		if strings.EqualFold(r.RawName, r.CanonicalName) {
			toDisable = append(toDisable, r.ID)
			result.CleanedAliases++
		}
	}

	for _, id := range toDisable {
		s.db.Exec(ctx, `UPDATE model_aliases SET status = 'inactive' WHERE id = $1`, id)
	}

	return nil
}

func (s *AliasSyncService) syncCanonicalNames(ctx context.Context, result *AliasSyncResult) error {
	type aliasPair struct {
		RawName     string
		CanonicalID int
	}

	rows, err := s.db.Query(ctx, `
		SELECT lower(raw_name), canonical_id
		FROM model_aliases
		WHERE status = 'active'
	`)
	if err != nil {
		return err
	}
	defer rows.(interface{ Close() error }).Close()

	for rows.(interface{ Next() bool }).Next() {
		var r aliasPair
		if err := rows.(interface{ Scan(...any) error }).Scan(&r.RawName, &r.CanonicalID); err != nil {
			continue
		}

		var canonicalName string
		err := s.db.QueryRow(ctx, `
			SELECT canonical_name FROM models_canonical WHERE id = $1 AND status = 'active'
		`, r.CanonicalID).Scan(&canonicalName)
		if err != nil {
			s.db.Exec(ctx, `UPDATE model_aliases SET status = 'inactive' WHERE canonical_id = $1`, r.CanonicalID)
			result.CleanedAliases++
		}
	}

	return nil
}

func (s *AliasSyncService) rebuildAliasIndex(ctx context.Context, result *AliasSyncResult) error {
	err := s.db.Exec(ctx, `
		INSERT INTO model_aliases (raw_name, canonical_id, status)
		SELECT DISTINCT lower(mo.raw_model_name), mo.canonical_id, 'active'
		FROM model_offers mo
		WHERE mo.available = TRUE
		ON CONFLICT (raw_name) DO NOTHING
	`)
	if err != nil {
		return err
	}

	return nil
}

func (s *AliasSyncService) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"running":    s.running,
		"interval_s": int(s.interval.Seconds()),
	}
}

func (s *AliasSyncService) GenerateRoutingRulesReport(ctx context.Context) (string, error) {
	var sb strings.Builder

	sb.WriteString("# 模型路由规则报告\n\n")

	sb.WriteString("## 按Provider分组\n\n")
	rows, err := s.db.Query(ctx, `
		SELECT p.display_name, p.code, COUNT(DISTINCT mo.raw_model_name) as model_count
		FROM providers p
		JOIN credentials c ON c.provider_id = p.id AND c.status = 'active'
		JOIN model_offers mo ON mo.credential_id = c.id AND mo.available = TRUE
		WHERE p.enabled = TRUE
		GROUP BY p.display_name, p.code
		ORDER BY p.display_name
	`)
	if err != nil {
		return "", fmt.Errorf("query providers: %w", err)
	}
	defer rows.(interface{ Close() error }).Close()

	for rows.(interface{ Next() bool }).Next() {
		var name, code string
		var count int
		if err := rows.(interface{ Scan(...any) error }).Scan(&name, &code, &count); err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s (%s): %d 模型\n", name, code, count))
	}

	sb.WriteString("\n## 按Family分组\n\n")
	rows2, err := s.db.Query(ctx, `
		SELECT family, COUNT(*) as count
		FROM models_canonical
		WHERE status = 'active' AND family IS NOT NULL AND family != ''
		GROUP BY family
		ORDER BY count DESC
	`)
	if err != nil {
		return "", fmt.Errorf("query families: %w", err)
	}
	defer rows2.(interface{ Close() error }).Close()

	for rows2.(interface{ Next() bool }).Next() {
		var family string
		var count int
		if err := rows2.(interface{ Scan(...any) error }).Scan(&family, &count); err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s: %d 模型\n", family, count))
	}

	return sb.String(), nil
}