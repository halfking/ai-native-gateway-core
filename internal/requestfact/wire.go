package requestfact

import (
	"encoding/json"
	"time"
)

func (f CanonicalRequestFact) MarshalJSON() ([]byte, error) {
	type factWire struct {
		Identity    Identity            `json:"identity"`
		Lifecycle   Lifecycle           `json:"lifecycle"`
		Routing     Routing             `json:"routing"`
		Request     ContentDocument     `json:"request"`
		Upstream    *ContentDocument    `json:"upstream,omitempty"`
		Response    *ResponseContent    `json:"response,omitempty"`
		Usage       *Usage              `json:"usage,omitempty"`
		Timeline    *Timeline           `json:"timeline,omitempty"`
		Attachments json.RawMessage     `json:"attachments,omitempty"`
		Extensions  json.RawMessage     `json:"extensions,omitempty"`
		Warnings    []ConversionWarning `json:"warnings,omitempty"`
		Integrity   *Integrity          `json:"integrity,omitempty"`
	}

	return json.Marshal(factWire{
		Identity:    f.Identity,
		Lifecycle:   f.Lifecycle,
		Routing:     f.Routing,
		Request:     f.Request,
		Upstream:    nonEmptyContentDocument(f.Upstream),
		Response:    nonEmptyResponseContent(f.Response),
		Usage:       nonEmptyUsage(f.Usage),
		Timeline:    nonEmptyTimeline(f.Timeline),
		Attachments: f.Attachments,
		Extensions:  f.Extensions,
		Warnings:    f.Warnings,
		Integrity:   nonEmptyIntegrity(f.Integrity),
	})
}

func (l Lifecycle) MarshalJSON() ([]byte, error) {
	type lifecycleWire struct {
		Status      string     `json:"status"`
		Success     *bool      `json:"success,omitempty"`
		ErrorKind   string     `json:"error_kind,omitempty"`
		ErrorCode   string     `json:"error_code,omitempty"`
		DeadlineAt  *time.Time `json:"deadline_at,omitempty"`
		CreatedAt   time.Time  `json:"created_at"`
		StartedAt   *time.Time `json:"started_at,omitempty"`
		CompletedAt time.Time  `json:"completed_at"`
	}
	return json.Marshal(lifecycleWire{
		Status:      l.Status,
		Success:     l.Success,
		ErrorKind:   l.ErrorKind,
		ErrorCode:   l.ErrorCode,
		DeadlineAt:  nonZeroTime(l.DeadlineAt),
		CreatedAt:   l.CreatedAt,
		StartedAt:   nonZeroTime(l.StartedAt),
		CompletedAt: l.CompletedAt,
	})
}

func (t Timeline) MarshalJSON() ([]byte, error) {
	type timelineWire struct {
		T0            *time.Time `json:"t0,omitempty"`
		T1            *time.Time `json:"t1,omitempty"`
		T2            *time.Time `json:"t2,omitempty"`
		T3            *time.Time `json:"t3,omitempty"`
		T4            *time.Time `json:"t4,omitempty"`
		T5            *time.Time `json:"t5,omitempty"`
		T6            *time.Time `json:"t6,omitempty"`
		T7            *time.Time `json:"t7,omitempty"`
		T8            *time.Time `json:"t8,omitempty"`
		T9            *time.Time `json:"t9,omitempty"`
		TTFTMillis    int64      `json:"ttft_millis,omitempty"`
		LatencyMillis int64      `json:"latency_millis,omitempty"`
	}
	return json.Marshal(timelineWire{
		T0:            nonZeroTime(t.T0),
		T1:            nonZeroTime(t.T1),
		T2:            nonZeroTime(t.T2),
		T3:            nonZeroTime(t.T3),
		T4:            nonZeroTime(t.T4),
		T5:            nonZeroTime(t.T5),
		T6:            nonZeroTime(t.T6),
		T7:            nonZeroTime(t.T7),
		T8:            nonZeroTime(t.T8),
		T9:            nonZeroTime(t.T9),
		TTFTMillis:    t.TTFTMillis,
		LatencyMillis: t.LatencyMillis,
	})
}

func (a ArchiveMetadata) MarshalJSON() ([]byte, error) {
	type archiveWire struct {
		State       ArchiveState `json:"state"`
		Attempts    int          `json:"attempts,omitempty"`
		LastError   string       `json:"last_error,omitempty"`
		PersistedAt *time.Time   `json:"persisted_at,omitempty"`
	}
	return json.Marshal(archiveWire{
		State:       a.State,
		Attempts:    a.Attempts,
		LastError:   a.LastError,
		PersistedAt: nonZeroTime(a.PersistedAt),
	})
}

func nonEmptyContentDocument(value ContentDocument) *ContentDocument {
	if len(value.RawBody) == 0 && len(value.CanonicalIR) == 0 && value.Protocol == "" && len(value.Extensions) == 0 {
		return nil
	}
	return &value
}

func nonEmptyResponseContent(value ResponseContent) *ResponseContent {
	if len(value.RawBody) == 0 && len(value.CanonicalIR) == 0 && len(value.ClientIR) == 0 &&
		len(value.StreamSummary) == 0 && len(value.StreamChunks) == 0 && len(value.Extensions) == 0 {
		return nil
	}
	return &value
}

func nonEmptyUsage(value Usage) *Usage {
	if value == (Usage{}) {
		return nil
	}
	return &value
}

func nonEmptyTimeline(value Timeline) *Timeline {
	if value == (Timeline{}) {
		return nil
	}
	return &value
}

func nonEmptyIntegrity(value Integrity) *Integrity {
	if value == (Integrity{}) {
		return nil
	}
	return &value
}

func nonZeroTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
