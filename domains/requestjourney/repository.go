package requestjourney

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type postgresClient interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PostgresRepository persists and reconstructs the durable RequestJourney
// projection using only the explicit content-free columns from migration 530.
type PostgresRepository struct {
	db postgresClient
}

func NewPostgresRepository(db postgresClient) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const journeyEventColumns = `
	tenant_id, gateway_instance_id, request_id, seq, event_type, stage,
	requested_model, resolved_model, model, provider_id, provider,
	credential_id, from_model, to_model, from_credential_id,
	to_credential_id, attempt_id, attempt_no, outcome, error_kind,
	http_status, retry_reason, switch_reason, node_health_status,
	observation_status, occurred_at`

func (r *PostgresRepository) Apply(ctx context.Context, event JourneyEvent) error {
	if r == nil || r.db == nil {
		return errors.New("request journey PostgreSQL is unavailable")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	var attemptID any
	var attemptNo any
	model := event.Model
	providerID := event.ProviderID
	provider := event.Provider
	credentialID := event.CredentialID
	if event.Attempt != nil {
		attemptID = event.Attempt.AttemptID
		attemptNo = event.Attempt.AttemptNo
		if model == "" {
			model = event.Attempt.Model
		}
		if providerID == 0 {
			providerID = event.Attempt.ProviderID
		}
		if provider == "" {
			provider = event.Attempt.Provider
		}
		if credentialID == 0 {
			credentialID = event.Attempt.CredentialID
		}
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO request_state_transitions (`+journeyEventColumns+`)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,
			$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26
		)
		ON CONFLICT (tenant_id, request_id, seq)
		WHERE event_type IS NOT NULL DO NOTHING`,
		event.TenantID, event.GatewayInstanceID, event.RequestID, event.Seq,
		event.Type, event.Stage, nullableString(event.RequestedModel),
		nullableString(event.ResolvedModel), nullableString(model),
		nullableInt64(providerID), nullableString(provider),
		nullableInt64(credentialID), nullableString(event.FromModel),
		nullableString(event.ToModel), nullableInt64(event.FromCredentialID),
		nullableInt64(event.ToCredentialID), attemptID, attemptNo,
		nullableString(string(event.Outcome)), nullableString(event.ErrorKind),
		nullableInt(event.HTTPStatus), nullableString(event.RetryReason),
		nullableString(event.SwitchReason), nullableString(string(event.NodeHealthStatus)),
		event.ObservationStatus, event.OccurredAt,
	)
	return err
}

func (r *PostgresRepository) Detail(ctx context.Context, tenantID, requestID string) (*RequestJourney, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("request journey PostgreSQL is unavailable")
	}
	rows, err := r.db.Query(ctx, `SELECT `+journeyEventColumns+`
		FROM request_state_transitions
		WHERE tenant_id = $1 AND request_id = $2 AND event_type IS NOT NULL
		ORDER BY seq`, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := scanJourneyEvents(rows)
	if err != nil {
		return nil, err
	}
	return journeyFromEvents(events)
}

func (r *PostgresRepository) RecentTotal(ctx context.Context, tenantID string, limit int) ([]RequestSnapshot, error) {
	return r.recentRequests(ctx, tenantID, limit, "", nil)
}

func (r *PostgresRepository) RecentModel(ctx context.Context, tenantID, model string, limit int) ([]RequestSnapshot, error) {
	return r.recentRequests(ctx, tenantID, limit,
		`AND (resolved_model = $3 OR model = $3 OR to_model = $3)`, []any{model})
}

func (r *PostgresRepository) ModelKeys(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT COALESCE(NULLIF(to_model, ''), NULLIF(model, ''), resolved_model)
		FROM request_state_transitions WHERE tenant_id = $1 AND event_type IS NOT NULL
		AND COALESCE(NULLIF(to_model, ''), NULLIF(model, ''), resolved_model) IS NOT NULL`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		result = append(result, model)
	}
	sort.Strings(result)
	return result, rows.Err()
}

func (r *PostgresRepository) NodeKeys(ctx context.Context, tenantID string) ([]NodeKey, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT model, provider_id, credential_id
		FROM request_state_transitions WHERE tenant_id = $1 AND event_type IS NOT NULL
		AND attempt_id IS NOT NULL AND model IS NOT NULL AND provider_id IS NOT NULL AND credential_id IS NOT NULL`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NodeKey
	for rows.Next() {
		var key NodeKey
		if err := rows.Scan(&key.Model, &key.ProviderID, &key.CredentialID); err != nil {
			return nil, err
		}
		result = append(result, key)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Model != result[j].Model {
			return result[i].Model < result[j].Model
		}
		if result[i].ProviderID != result[j].ProviderID {
			return result[i].ProviderID < result[j].ProviderID
		}
		return result[i].CredentialID < result[j].CredentialID
	})
	return result, rows.Err()
}

func (r *PostgresRepository) RecentNode(ctx context.Context, tenantID string, key NodeKey, limit int) ([]RequestSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("request journey PostgreSQL is unavailable")
	}
	limit = positiveOrDefault(limit, DefaultPerNodeCapacity)
	rows, err := r.db.Query(ctx, `
		SELECT request_id, attempt_id
		FROM request_state_transitions
		WHERE tenant_id = $1 AND event_type IS NOT NULL
		  AND attempt_id IS NOT NULL AND model = $2
		  AND provider_id = $3 AND credential_id = $4
		GROUP BY request_id, attempt_id
		ORDER BY MIN(occurred_at) DESC, request_id DESC, attempt_id DESC
		LIMIT $5`, tenantID, key.Model, key.ProviderID, key.CredentialID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type attemptEntry struct{ requestID, attemptID string }
	entries := make([]attemptEntry, 0, limit)
	for rows.Next() {
		var entry attemptEntry
		if err := rows.Scan(&entry.requestID, &entry.attemptID); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reverse(entries)
	result := make([]RequestSnapshot, 0, len(entries))
	for _, entry := range entries {
		journey, err := r.Detail(ctx, tenantID, entry.requestID)
		if err != nil {
			return nil, err
		}
		snapshot, found := snapshotForAttempt(journey, entry.attemptID)
		if found {
			result = append(result, snapshot)
		}
	}
	return result, nil
}

func (r *PostgresRepository) recentRequests(
	ctx context.Context,
	tenantID string,
	limit int,
	filter string,
	filterArgs []any,
) ([]RequestSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("request journey PostgreSQL is unavailable")
	}
	limit = positiveOrDefault(limit, DefaultTotalRequestCapacity)
	args := []any{tenantID, limit}
	args = append(args, filterArgs...)
	rows, err := r.db.Query(ctx, `
		SELECT request_id
		FROM request_state_transitions
		WHERE tenant_id = $1 AND event_type IS NOT NULL `+filter+`
		GROUP BY request_id
		ORDER BY MIN(occurred_at) DESC, request_id DESC
		LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requestIDs := make([]string, 0, limit)
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			return nil, err
		}
		requestIDs = append(requestIDs, requestID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reverse(requestIDs)
	result := make([]RequestSnapshot, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		journey, err := r.Detail(ctx, tenantID, requestID)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshotFromJourney(journey))
	}
	return result, nil
}

func scanJourneyEvents(rows pgx.Rows) ([]JourneyEvent, error) {
	events := make([]JourneyEvent, 0)
	for rows.Next() {
		var event JourneyEvent
		var eventType, stage, observationStatus string
		var requestedModel, resolvedModel, model, provider sql.NullString
		var fromModel, toModel, attemptID, outcome, errorKind sql.NullString
		var retryReason, switchReason, nodeHealthStatus sql.NullString
		var providerID, credentialID, fromCredentialID, toCredentialID sql.NullInt64
		var attemptNo, httpStatus sql.NullInt64
		if err := rows.Scan(
			&event.TenantID, &event.GatewayInstanceID, &event.RequestID, &event.Seq,
			&eventType, &stage, &requestedModel, &resolvedModel, &model,
			&providerID, &provider, &credentialID, &fromModel, &toModel,
			&fromCredentialID, &toCredentialID, &attemptID, &attemptNo,
			&outcome, &errorKind, &httpStatus, &retryReason, &switchReason,
			&nodeHealthStatus, &observationStatus, &event.OccurredAt,
		); err != nil {
			return nil, err
		}
		event.Type = EventType(eventType)
		event.Stage = JourneyStage(stage)
		event.RequestedModel = requestedModel.String
		event.ResolvedModel = resolvedModel.String
		event.Model = model.String
		event.ProviderID = providerID.Int64
		event.Provider = provider.String
		event.CredentialID = credentialID.Int64
		event.FromModel = fromModel.String
		event.ToModel = toModel.String
		event.FromCredentialID = fromCredentialID.Int64
		event.ToCredentialID = toCredentialID.Int64
		event.Outcome = Outcome(outcome.String)
		event.ErrorKind = errorKind.String
		event.HTTPStatus = int(httpStatus.Int64)
		event.RetryReason = retryReason.String
		event.SwitchReason = switchReason.String
		event.NodeHealthStatus = NodeHealthStatus(nodeHealthStatus.String)
		event.ObservationStatus = ObservationStatus(observationStatus)
		if attemptID.Valid {
			event.Attempt = &AttemptRef{
				AttemptID: attemptID.String, AttemptNo: int(attemptNo.Int64),
				Model: event.Model, ProviderID: event.ProviderID,
				Provider: event.Provider, CredentialID: event.CredentialID,
			}
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("invalid persisted journey event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return events, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
