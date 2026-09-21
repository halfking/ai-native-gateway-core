package requestjourney

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// AttemptFact is the content-free quality-analysis grain for one upstream
// attempt. It deliberately excludes request and response content.
type AttemptFact struct {
	TenantID          string
	RequestID         string
	AttemptID         string
	AttemptNo         int
	ProviderID        int64
	CredentialID      int64
	Model             string
	StartedAt         time.Time
	FirstByteAt       *time.Time
	EndedAt           *time.Time
	Outcome           Outcome
	ErrorKind         string
	HTTPStatus        int
	RetryScheduled    bool
	NodeSwitched      bool
	ModelSwitched     bool
	ObservationStatus ObservationStatus
}

// AttemptFacts returns attempts whose start time is within [start, end). The
// query keeps tenant filtering in SQL as a second guard beside RLS.
func (r *PostgresRepository) AttemptFacts(ctx context.Context, tenantID string, start, end time.Time) ([]AttemptFact, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("request journey PostgreSQL is unavailable")
	}
	if tenantID == "" || start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("tenant_id and a valid time range are required")
	}
	rows, err := r.db.Query(ctx, `SELECT `+journeyEventColumns+`
		FROM request_state_transitions
		WHERE tenant_id = $1
		  AND request_id IN (
			SELECT request_id
			FROM request_state_transitions
			WHERE tenant_id = $1
			  AND event_type = 'attempt_started'
			  AND occurred_at >= $2 AND occurred_at < $3
		)
		  AND event_type IS NOT NULL
		ORDER BY request_id, seq`, tenantID, start.UTC(), end.UTC())
	if err != nil {
		return nil, fmt.Errorf("query attempt facts failed: %w (context: tenant_id=%s)", err, tenantID)
	}
	defer rows.Close()
	events, err := scanJourneyEvents(rows)
	if err != nil {
		return nil, fmt.Errorf("scan attempt facts failed: %w (context: tenant_id=%s)", err, tenantID)
	}
	return attemptFactsFromEvents(events, start, end), nil
}

func attemptFactsFromEvents(events []JourneyEvent, start, end time.Time) []AttemptFact {
	byRequest := make(map[string][]JourneyEvent)
	for _, event := range events {
		byRequest[event.RequestID] = append(byRequest[event.RequestID], event)
	}
	facts := make([]AttemptFact, 0)
	for _, requestEvents := range byRequest {
		facts = append(facts, attemptFactsFromRequest(requestEvents, start, end)...)
	}
	sort.Slice(facts, func(i, j int) bool {
		if !facts[i].StartedAt.Equal(facts[j].StartedAt) {
			return facts[i].StartedAt.Before(facts[j].StartedAt)
		}
		return facts[i].AttemptNo < facts[j].AttemptNo
	})
	return facts
}

func attemptFactsFromRequest(events []JourneyEvent, start, end time.Time) []AttemptFact {
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	facts := make(map[string]*AttemptFact)
	order := make([]string, 0)
	currentAttemptID := ""
	requestDegraded := false
	for _, event := range events {
		if event.ObservationStatus == ObservationDegraded {
			requestDegraded = true
		}
		if event.Attempt != nil {
			currentAttemptID = event.Attempt.AttemptID
		}
		switch event.Type {
		case EventAttemptStarted:
			if event.Attempt == nil || !event.OccurredAt.Before(end) || event.OccurredAt.Before(start) {
				continue
			}
			fact := &AttemptFact{
				TenantID: event.TenantID, RequestID: event.RequestID,
				AttemptID: event.Attempt.AttemptID, AttemptNo: event.Attempt.AttemptNo,
				ProviderID: event.Attempt.ProviderID, CredentialID: event.Attempt.CredentialID,
				Model: event.Attempt.Model, StartedAt: event.OccurredAt,
				ObservationStatus: event.ObservationStatus,
			}
			facts[fact.AttemptID] = fact
			order = append(order, fact.AttemptID)
		case EventFirstByte:
			if fact := facts[currentAttemptID]; fact != nil {
				occurredAt := event.OccurredAt
				fact.FirstByteAt = &occurredAt
			}
		case EventAttemptSucceeded, EventAttemptFailed:
			if fact := facts[currentAttemptID]; fact != nil {
				occurredAt := event.OccurredAt
				fact.EndedAt = &occurredAt
				fact.Outcome = event.Outcome
				fact.ErrorKind = event.ErrorKind
				fact.HTTPStatus = event.HTTPStatus
			}
		case EventRetryScheduled:
			if fact := facts[currentAttemptID]; fact != nil {
				fact.RetryScheduled = true
			}
		case EventNodeSwitched:
			if fact := facts[currentAttemptID]; fact != nil {
				fact.NodeSwitched = true
			}
		case EventModelSwitched:
			if fact := facts[currentAttemptID]; fact != nil {
				fact.ModelSwitched = true
			}
		}
	}
	result := make([]AttemptFact, 0, len(order))
	for _, attemptID := range order {
		fact := facts[attemptID]
		if requestDegraded {
			fact.ObservationStatus = ObservationDegraded
		}
		result = append(result, *fact)
	}
	return result
}
