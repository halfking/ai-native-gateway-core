package autoupdate

import (
	"context"
	"math"
)

func (s *PgxStore) GetRolloutStats(ctx context.Context, version string) (RolloutStats, error) {
	var stats RolloutStats
	err := s.db.QueryRow(ctx, `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'success')::int,
			COUNT(*) FILTER (WHERE status = 'failed')::int,
			COUNT(*) FILTER (WHERE status = 'rolled_back')::int
		FROM upgrade_logs
		WHERE new_version = $1
	`, version).Scan(&stats.Total, &stats.SuccessCount, &stats.FailedCount, &stats.RolledBackCount)
	if err != nil {
		return RolloutStats{}, err
	}
	terminal := stats.SuccessCount + stats.FailedCount + stats.RolledBackCount
	if terminal > 0 {
		stats.SuccessRatePct = roundPct(float64(stats.SuccessCount) / float64(terminal) * 100)
		stats.RollbackRatePct = roundPct(float64(stats.RolledBackCount) / float64(terminal) * 100)
	}
	return stats, nil
}

func (s *PgxStore) UpdateGrayRuleStatus(ctx context.Context, releaseID int64, status string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE gray_release_rules
		SET status = $2
		WHERE release_id = $1
	`, releaseID, status)
	return err
}

func roundPct(v float64) float64 {
	return math.Round(v*10) / 10
}
