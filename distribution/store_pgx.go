package distribution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgxStore struct {
	pool *pgxpool.Pool
}

func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool}
}

func (s *PgxStore) EnsureHolderByEmail(ctx context.Context, email, displayName string) (*LicenseHolder, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil, fmt.Errorf("email required")
	}
	var h LicenseHolder
	err := s.pool.QueryRow(ctx, `
		INSERT INTO license_holders (email, display_name, last_seen_at)
		VALUES ($1, $2, now())
		ON CONFLICT (email) DO UPDATE SET
			display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE license_holders.display_name END,
			last_seen_at = now(),
			updated_at = now()
		RETURNING id, email, display_name, holder_type, COALESCE(consent_version, ''), created_at, updated_at, last_seen_at
	`, email, displayName).Scan(
		&h.ID, &h.Email, &h.DisplayName, &h.HolderType, &h.ConsentVersion,
		&h.CreatedAt, &h.UpdatedAt, &h.LastSeenAt,
	)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *PgxStore) GetHolder(ctx context.Context, id int64) (*LicenseHolder, error) {
	var h LicenseHolder
	err := s.pool.QueryRow(ctx, `
		SELECT h.id, h.email, h.display_name, h.holder_type, COALESCE(h.consent_version, ''),
		       h.created_at, h.updated_at, h.last_seen_at,
		       (SELECT COUNT(*) FROM licenses l WHERE l.holder_id = h.id),
		       (SELECT COUNT(*) FROM license_devices d
		          JOIN licenses l ON l.id = d.license_id
		         WHERE l.holder_id = h.id AND d.status = 'active'),
		       COALESCE((SELECT SUM(amount_cents) FROM donations d
		                  WHERE d.holder_id = h.id AND d.status = 'paid'), 0)
		FROM license_holders h WHERE h.id = $1
	`, id).Scan(
		&h.ID, &h.Email, &h.DisplayName, &h.HolderType, &h.ConsentVersion,
		&h.CreatedAt, &h.UpdatedAt, &h.LastSeenAt,
		&h.LicenseCount, &h.DeviceCount, &h.DonationTotal,
	)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *PgxStore) ListHolders(ctx context.Context, offset, limit int, query string) ([]LicenseHolder, int, error) {
	q := strings.TrimSpace(query)
	var total int
	countSQL := `SELECT COUNT(*) FROM license_holders`
	args := []any{}
	if q != "" {
		countSQL += ` WHERE email ILIKE $1 OR display_name ILIKE $1`
		args = append(args, "%"+q+"%")
	}
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := `
		SELECT h.id, h.email, h.display_name, h.holder_type, COALESCE(h.consent_version, ''),
		       h.created_at, h.updated_at, h.last_seen_at,
		       (SELECT COUNT(*) FROM licenses l WHERE l.holder_id = h.id OR lower(l.customer_email) = lower(h.email)),
		       (SELECT COUNT(*) FROM license_devices d
		          JOIN licenses l ON l.id = d.license_id
		         WHERE (l.holder_id = h.id OR lower(l.customer_email) = lower(h.email)) AND d.status = 'active'),
		       COALESCE((SELECT SUM(amount_cents) FROM donations d
		                  WHERE d.holder_id = h.id AND d.status = 'paid'), 0)
		FROM license_holders h`
	if q != "" {
		listSQL += ` WHERE h.email ILIKE $1 OR h.display_name ILIKE $1`
	}
	listSQL += ` ORDER BY h.updated_at DESC LIMIT $` + fmt.Sprint(len(args)+1) + ` OFFSET $` + fmt.Sprint(len(args)+2)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []LicenseHolder
	for rows.Next() {
		var h LicenseHolder
		if err := rows.Scan(
			&h.ID, &h.Email, &h.DisplayName, &h.HolderType, &h.ConsentVersion,
			&h.CreatedAt, &h.UpdatedAt, &h.LastSeenAt,
			&h.LicenseCount, &h.DeviceCount, &h.DonationTotal,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	return out, total, rows.Err()
}

func (s *PgxStore) RecordDownloadEvent(ctx context.Context, ev *DownloadEvent) error {
	if ev.RequestID == "" {
		ev.RequestID = uuid.New().String()
	}
	if ev.Source == "" {
		ev.Source = "web"
	}
	if ev.Result == "" {
		ev.Result = "started"
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO download_events (
			request_id, release_version, platform, arch, edition, channel,
			holder_id, donation_id, result, source
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at
	`, ev.RequestID, ev.ReleaseVersion, ev.Platform, ev.Arch, ev.Edition, ev.Channel,
		ev.HolderID, ev.DonationID, ev.Result, ev.Source,
	).Scan(&ev.ID, &ev.CreatedAt)
}

func (s *PgxStore) UpdateDownloadResult(ctx context.Context, requestID, result string, durationMS int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE download_events SET result = $2, duration_ms = $3 WHERE request_id = $1
	`, requestID, result, durationMS)
	return err
}

func (s *PgxStore) GetDownloadStats(ctx context.Context) (*DownloadStats, error) {
	var st DownloadStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE created_at >= date_trunc('day', now())),
			COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days'),
			COUNT(*),
			(SELECT COUNT(*) FROM donations WHERE status = 'paid' AND paid_at >= date_trunc('day', now())),
			(SELECT COUNT(*) FROM donations WHERE status = 'paid'),
			COALESCE((SELECT SUM(amount_cents) FROM donations WHERE status = 'paid'), 0),
			(SELECT COUNT(DISTINCT holder_id) FROM donations WHERE status = 'paid' AND holder_id IS NOT NULL)
		FROM download_events
	`).Scan(
		&st.TodayDownloads, &st.WeekDownloads, &st.TotalDownloads,
		&st.TodayDonations, &st.TotalDonations, &st.DonationAmount, &st.SupporterCount,
	)
	if err != nil {
		return nil, err
	}
	if st.TotalDownloads > 0 {
		var activated int
		_ = s.pool.QueryRow(ctx, `
			SELECT COUNT(DISTINCT l.id) FROM licenses l
			WHERE l.created_at >= now() - interval '30 days'
		`).Scan(&activated)
		st.ActivationRate = float64(activated) / float64(st.TotalDownloads) * 100
	}
	return &st, nil
}

func (s *PgxStore) CreateDonation(ctx context.Context, d *Donation) error {
	if d.OrderNo == "" {
		d.OrderNo = "DON-" + strings.ToUpper(uuid.New().String()[:8])
	}
	if d.Currency == "" {
		d.Currency = "CNY"
	}
	if d.Status == "" {
		d.Status = "pending"
	}
	if d.TierLabel == "" {
		d.TierLabel = "supporter"
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO donations (order_no, holder_id, email, amount_cents, currency, channel, status, tier_label)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, created_at
	`, d.OrderNo, d.HolderID, d.Email, d.AmountCents, d.Currency, d.Channel, d.Status, d.TierLabel,
	).Scan(&d.ID, &d.CreatedAt)
}

func (s *PgxStore) GetDonationByOrderNo(ctx context.Context, orderNo string) (*Donation, error) {
	var d Donation
	err := s.pool.QueryRow(ctx, `
		SELECT id, order_no, holder_id, email, amount_cents, currency, channel, status, tier_label, paid_at, created_at
		FROM donations WHERE order_no = $1
	`, orderNo).Scan(
		&d.ID, &d.OrderNo, &d.HolderID, &d.Email, &d.AmountCents, &d.Currency,
		&d.Channel, &d.Status, &d.TierLabel, &d.PaidAt, &d.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *PgxStore) MarkDonationPaid(ctx context.Context, orderNo string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE donations SET status = 'paid', paid_at = now(), updated_at = now()
		WHERE order_no = $1 AND status = 'pending'
	`, orderNo)
	return err
}

func (s *PgxStore) CountSupporters(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT COALESCE(holder_id::text, lower(email)))
		FROM donations WHERE status = 'paid'
	`).Scan(&n)
	return n, err
}

func (s *PgxStore) ListArtifacts(ctx context.Context, version string) ([]ReleaseArtifact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, release_version, platform, arch, edition, artifact_name, sha256, size_bytes, download_path
		FROM release_artifacts WHERE release_version = $1 ORDER BY platform, arch
	`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseArtifact
	for rows.Next() {
		var a ReleaseArtifact
		if err := rows.Scan(&a.ID, &a.ReleaseVersion, &a.Platform, &a.Arch, &a.Edition,
			&a.ArtifactName, &a.SHA256, &a.SizeBytes, &a.DownloadPath); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PgxStore) UpsertArtifact(ctx context.Context, a *ReleaseArtifact) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO release_artifacts (release_version, platform, arch, edition, artifact_name, sha256, size_bytes, download_path)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (release_version, platform, arch, edition, artifact_name) DO UPDATE SET
			sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes, download_path = EXCLUDED.download_path
	`, a.ReleaseVersion, a.Platform, a.Arch, a.Edition, a.ArtifactName, a.SHA256, a.SizeBytes, a.DownloadPath)
	return err
}

// ReleaseCatalogProvider reads latest published release from autoupdate releases table.
type ReleaseCatalogProvider struct {
	pool *pgxpool.Pool
}

func NewReleaseCatalogProvider(pool *pgxpool.Pool) *ReleaseCatalogProvider {
	return &ReleaseCatalogProvider{pool: pool}
}

func (p *ReleaseCatalogProvider) LatestPublishedVersion(ctx context.Context) (string, int, *time.Time, error) {
	var version string
	var buildSeq int
	var publishedAt *time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT version, build_seq, published_at FROM releases
		WHERE published_at IS NOT NULL AND channel = 'stable'
		ORDER BY build_seq DESC LIMIT 1
	`).Scan(&version, &buildSeq, &publishedAt)
	if err != nil {
		return "", 0, nil, err
	}
	return version, buildSeq, publishedAt, nil
}
