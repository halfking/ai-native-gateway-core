package providerprofile

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGProfileStore PostgreSQL画像存储实现
type PGProfileStore struct {
	db *pgxpool.Pool
}

// NewPGProfileStore 创建PostgreSQL画像存储
func NewPGProfileStore(db *pgxpool.Pool) *PGProfileStore {
	return &PGProfileStore{db: db}
}

// SaveDailyProfile 保存天级画像
func (s *PGProfileStore) SaveDailyProfile(ctx context.Context, profile *DailyProfile) error {
	// 将 TimeslotScores 序列化为 JSONB
	timeslotScoresJSON, err := marshalJSON(profile.TimeslotScores)
	if err != nil {
		return fmt.Errorf("marshal timeslot scores: %w", err)
	}

	// 将 RawStats 序列化为 JSONB
	rawStatsJSON, err := marshalJSON(profile.RawStats)
	if err != nil {
		return fmt.Errorf("marshal raw stats: %w", err)
	}

	query := `
		INSERT INTO provider_profile_daily (
			credential_id, provider_id, profile_date,
			network_score, credibility_score, availability_score, stability_score,
				scale_score, cost_accuracy_score, price_score, total_score,
				timeslot_scores, score_stddev, best_timeslot, worst_timeslot,
				raw_stats, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::text::jsonb, $13, $14, $15, $16::text::jsonb, $17)
		ON CONFLICT (credential_id, profile_date) DO UPDATE SET
			network_score = EXCLUDED.network_score,
			credibility_score = EXCLUDED.credibility_score,
			availability_score = EXCLUDED.availability_score,
			stability_score = EXCLUDED.stability_score,
			scale_score = EXCLUDED.scale_score,
			cost_accuracy_score = EXCLUDED.cost_accuracy_score,
			price_score = EXCLUDED.price_score,
			total_score = EXCLUDED.total_score,
			timeslot_scores = EXCLUDED.timeslot_scores,
			score_stddev = EXCLUDED.score_stddev,
			best_timeslot = EXCLUDED.best_timeslot,
			worst_timeslot = EXCLUDED.worst_timeslot,
			raw_stats = EXCLUDED.raw_stats
	`

	_, err = s.db.Exec(ctx, query,
		profile.CredentialID,
		profile.ProviderID,
		profile.ProfileDate,
		profile.NetworkScore,
		profile.CredibilityScore,
		profile.AvailabilityScore,
		profile.StabilityScore,
		profile.ScaleScore,
		profile.CostAccuracyScore,
		profile.PriceScore,
		profile.TotalScore,
		string(timeslotScoresJSON),
		profile.ScoreStddev,
		profile.BestTimeslot,
		profile.WorstTimeslot,
		string(rawStatsJSON),
		profile.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("insert daily profile: %w", err)
	}

	return nil
}

// GetDailyProfile 获取指定日期的画像
func (s *PGProfileStore) GetDailyProfile(ctx context.Context, credentialID int64, date time.Time) (*DailyProfile, error) {
	query := `
		SELECT 
			id, credential_id, provider_id, profile_date,
			network_score, credibility_score, availability_score, stability_score,
			scale_score, cost_accuracy_score, price_score, total_score,
			timeslot_scores, score_stddev, best_timeslot, worst_timeslot,
			raw_stats, created_at
		FROM provider_profile_daily
		WHERE credential_id = $1 AND profile_date = $2
	`

	var profile DailyProfile
	var timeslotScoresJSON, rawStatsJSON []byte
	var bestTimeslot, worstTimeslot *string
	// Score columns are nullable numeric(5,2); scan into pointers and deref so a
	// NULL column (e.g. Phase-1 profiles with unimplemented dimensions) doesn't
	// fail the whole scan with "cannot scan NULL into *float64".
	var networkScore, credibilityScore, availabilityScore, stabilityScore *float64
	var scaleScore, costAccuracyScore, priceScore, totalScore, scoreStddev *float64

	err := s.db.QueryRow(ctx, query, credentialID, date).Scan(
		&profile.ID,
		&profile.CredentialID,
		&profile.ProviderID,
		&profile.ProfileDate,
		&networkScore,
		&credibilityScore,
		&availabilityScore,
		&stabilityScore,
		&scaleScore,
		&costAccuracyScore,
		&priceScore,
		&totalScore,
		&timeslotScoresJSON,
		&scoreStddev,
		&bestTimeslot,
		&worstTimeslot,
		&rawStatsJSON,
		&profile.CreatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("query daily profile: %w", err)
	}

	profile.NetworkScore = derefFloat(networkScore)
	profile.CredibilityScore = derefFloat(credibilityScore)
	profile.AvailabilityScore = derefFloat(availabilityScore)
	profile.StabilityScore = derefFloat(stabilityScore)
	profile.ScaleScore = derefFloat(scaleScore)
	profile.CostAccuracyScore = derefFloat(costAccuracyScore)
	profile.PriceScore = derefFloat(priceScore)
	profile.TotalScore = derefFloat(totalScore)
	profile.ScoreStddev = derefFloat(scoreStddev)
	profile.BestTimeslot = TimeSlot(derefStr(bestTimeslot))
	profile.WorstTimeslot = TimeSlot(derefStr(worstTimeslot))

	// 反序列化 JSONB
	if err := unmarshalJSON(timeslotScoresJSON, &profile.TimeslotScores); err != nil {
		return nil, fmt.Errorf("unmarshal timeslot scores: %w", err)
	}
	if err := unmarshalJSON(rawStatsJSON, &profile.RawStats); err != nil {
		return nil, fmt.Errorf("unmarshal raw stats: %w", err)
	}

	return &profile, nil
}

// GetRecentProfiles 获取最近N天的画像
func (s *PGProfileStore) GetRecentProfiles(ctx context.Context, credentialID int64, days int) ([]*DailyProfile, error) {
	query := `
		SELECT 
			id, credential_id, provider_id, profile_date,
			network_score, credibility_score, availability_score, stability_score,
			scale_score, cost_accuracy_score, price_score, total_score,
			timeslot_scores, score_stddev, best_timeslot, worst_timeslot,
			raw_stats, created_at
		FROM provider_profile_daily
		WHERE credential_id = $1
		  AND profile_date >= CURRENT_DATE - $2 * INTERVAL '1 day'
		ORDER BY profile_date DESC
	`

	rows, err := s.db.Query(ctx, query, credentialID, days)
	if err != nil {
		return nil, fmt.Errorf("query recent profiles: %w", err)
	}
	defer rows.Close()

	var profiles []*DailyProfile
	for rows.Next() {
		var profile DailyProfile
		var timeslotScoresJSON, rawStatsJSON []byte
		var bestTimeslot, worstTimeslot *string
		// Score columns are nullable numeric(5,2); scan into pointers and deref so a
		// NULL column (e.g. Phase-1 profiles with unimplemented dimensions) doesn't
		// fail the whole scan with "cannot scan NULL into *float64".
		var networkScore, credibilityScore, availabilityScore, stabilityScore *float64
		var scaleScore, costAccuracyScore, priceScore, totalScore, scoreStddev *float64

		err := rows.Scan(
			&profile.ID,
			&profile.CredentialID,
			&profile.ProviderID,
			&profile.ProfileDate,
			&networkScore,
			&credibilityScore,
			&availabilityScore,
			&stabilityScore,
			&scaleScore,
			&costAccuracyScore,
			&priceScore,
			&totalScore,
			&timeslotScoresJSON,
			&scoreStddev,
			&bestTimeslot,
			&worstTimeslot,
			&rawStatsJSON,
			&profile.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}

		profile.NetworkScore = derefFloat(networkScore)
		profile.CredibilityScore = derefFloat(credibilityScore)
		profile.AvailabilityScore = derefFloat(availabilityScore)
		profile.StabilityScore = derefFloat(stabilityScore)
		profile.ScaleScore = derefFloat(scaleScore)
		profile.CostAccuracyScore = derefFloat(costAccuracyScore)
		profile.PriceScore = derefFloat(priceScore)
		profile.TotalScore = derefFloat(totalScore)
		profile.ScoreStddev = derefFloat(scoreStddev)
		profile.BestTimeslot = TimeSlot(derefStr(bestTimeslot))
		profile.WorstTimeslot = TimeSlot(derefStr(worstTimeslot))

		if err := unmarshalJSON(timeslotScoresJSON, &profile.TimeslotScores); err != nil {
			return nil, fmt.Errorf("unmarshal timeslot scores: %w", err)
		}
		if err := unmarshalJSON(rawStatsJSON, &profile.RawStats); err != nil {
			return nil, fmt.Errorf("unmarshal raw stats: %w", err)
		}

		profiles = append(profiles, &profile)
	}

	return profiles, rows.Err()
}

// GetProfilesByProvider 获取供应商所有credential的最新画像
func (s *PGProfileStore) GetProfilesByProvider(ctx context.Context, providerID int64) ([]*DailyProfile, error) {
	query := `
		SELECT DISTINCT ON (credential_id)
			id, credential_id, provider_id, profile_date,
			network_score, credibility_score, availability_score, stability_score,
			scale_score, cost_accuracy_score, price_score, total_score,
			timeslot_scores, score_stddev, best_timeslot, worst_timeslot,
			raw_stats, created_at
		FROM provider_profile_daily
		WHERE provider_id = $1
		ORDER BY credential_id, profile_date DESC
	`

	rows, err := s.db.Query(ctx, query, providerID)
	if err != nil {
		return nil, fmt.Errorf("query profiles by provider: %w", err)
	}
	defer rows.Close()

	var profiles []*DailyProfile
	for rows.Next() {
		var profile DailyProfile
		var timeslotScoresJSON, rawStatsJSON []byte
		var bestTimeslot, worstTimeslot *string
		// Score columns are nullable numeric(5,2); scan into pointers and deref so a
		// NULL column (e.g. Phase-1 profiles with unimplemented dimensions) doesn't
		// fail the whole scan with "cannot scan NULL into *float64".
		var networkScore, credibilityScore, availabilityScore, stabilityScore *float64
		var scaleScore, costAccuracyScore, priceScore, totalScore, scoreStddev *float64

		err := rows.Scan(
			&profile.ID,
			&profile.CredentialID,
			&profile.ProviderID,
			&profile.ProfileDate,
			&networkScore,
			&credibilityScore,
			&availabilityScore,
			&stabilityScore,
			&scaleScore,
			&costAccuracyScore,
			&priceScore,
			&totalScore,
			&timeslotScoresJSON,
			&scoreStddev,
			&bestTimeslot,
			&worstTimeslot,
			&rawStatsJSON,
			&profile.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}

		profile.NetworkScore = derefFloat(networkScore)
		profile.CredibilityScore = derefFloat(credibilityScore)
		profile.AvailabilityScore = derefFloat(availabilityScore)
		profile.StabilityScore = derefFloat(stabilityScore)
		profile.ScaleScore = derefFloat(scaleScore)
		profile.CostAccuracyScore = derefFloat(costAccuracyScore)
		profile.PriceScore = derefFloat(priceScore)
		profile.TotalScore = derefFloat(totalScore)
		profile.ScoreStddev = derefFloat(scoreStddev)
		profile.BestTimeslot = TimeSlot(derefStr(bestTimeslot))
		profile.WorstTimeslot = TimeSlot(derefStr(worstTimeslot))

		if err := unmarshalJSON(timeslotScoresJSON, &profile.TimeslotScores); err != nil {
			return nil, fmt.Errorf("unmarshal timeslot scores: %w", err)
		}
		if err := unmarshalJSON(rawStatsJSON, &profile.RawStats); err != nil {
			return nil, fmt.Errorf("unmarshal raw stats: %w", err)
		}

		profiles = append(profiles, &profile)
	}

	return profiles, rows.Err()
}

// PGProfileSource adapts PGProfileStore to the AlertEngine's ProfileSource interface.
// GetRecentProfiles returns []*DailyProfile (pointers); Recent dereferences to
// []DailyProfile (values) to satisfy ProfileSource, which uses value semantics for
// read-only evaluation.
type PGProfileSource struct {
	store *PGProfileStore
}

// NewPGProfileSource creates a ProfileSource backed by PGProfileStore.
func NewPGProfileSource(store *PGProfileStore) *PGProfileSource {
	return &PGProfileSource{store: store}
}

// Recent returns the last N daily profiles ordered by profile_date DESC (most recent first).
func (s *PGProfileSource) Recent(ctx context.Context, credentialID int64, days int) ([]DailyProfile, error) {
	profiles, err := s.store.GetRecentProfiles(ctx, credentialID, days)
	if err != nil {
		return nil, err
	}
	out := make([]DailyProfile, len(profiles))
	for i, p := range profiles {
		out[i] = *p
	}
	return out, nil
}

// derefFloat safely dereferences a nullable float64 pointer, returning 0 for NULL.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefStr safely dereferences a nullable string pointer, returning "" for NULL.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
