package sessionsummary

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingMessageSource is a fake MessageSource that records its calls and
// returns canned data, used to prove the summarizer reads through the
// MessageSource interface (A6) rather than being welded to request_logs.
type recordingMessageSource struct {
	sinceCalls []sinceCall
	fullCalls  []fullCall
	fullMsgs   []SessionMessage
	sinceMsgs  []SessionMessage
	fullErr    error
	sinceErr   error
}

type sinceCall struct {
	tenantID, sessionKey string
	since                time.Time
}

type fullCall struct {
	tenantID, sessionKey string
}

func (r *recordingMessageSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	r.fullCalls = append(r.fullCalls, fullCall{tenantID, sessionKey})
	return r.fullMsgs, r.fullErr
}

func (r *recordingMessageSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	r.sinceCalls = append(r.sinceCalls, sinceCall{tenantID, sessionKey, since})
	return r.sinceMsgs, r.sinceErr
}

// TestSetMessageSource_RoutesReadsThroughInterface verifies that after
// SetMessageSource, the summarizer's read helpers delegate to the injected
// source instead of the default request_logs source.
func TestSetMessageSource_RoutesReadsThroughInterface(t *testing.T) {
	s := NewSummarizer(nil, nil, nil)
	wantFull := []SessionMessage{{RequestID: "r1", Role: "user", Content: "hi"}}
	wantSince := []SessionMessage{{RequestID: "r2", Role: "assistant", Content: "hey"}}
	fake := &recordingMessageSource{fullMsgs: wantFull, sinceMsgs: wantSince}
	s.SetMessageSource(fake)

	gotFull, err := s.getSessionMessages(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("getSessionMessages returned error: %v", err)
	}
	if len(fake.fullCalls) != 1 || fake.fullCalls[0].tenantID != "t1" || fake.fullCalls[0].sessionKey != "s1" {
		t.Fatalf("expected one full call (t1,s1), got %+v", fake.fullCalls)
	}
	if len(gotFull) != 1 || gotFull[0].RequestID != "r1" {
		t.Fatalf("expected canned full messages, got %+v", gotFull)
	}

	ts := time.Now()
	gotSince, err := s.getMessagesSince(context.Background(), "t2", "s2", ts)
	if err != nil {
		t.Fatalf("getMessagesSince returned error: %v", err)
	}
	if len(fake.sinceCalls) != 1 || fake.sinceCalls[0].tenantID != "t2" || !fake.sinceCalls[0].since.Equal(ts) {
		t.Fatalf("expected one since call (t2, ts), got %+v", fake.sinceCalls)
	}
	if len(gotSince) != 1 || gotSince[0].RequestID != "r2" {
		t.Fatalf("expected canned since messages, got %+v", gotSince)
	}
}

// TestSetMessageSource_NilIgnored ensures a nil injection cannot blank the
// read surface (which would cause a nil-dereference on the next summary).
func TestSetMessageSource_NilIgnored(t *testing.T) {
	s := NewSummarizer(nil, nil, nil)
	original := s.messageSource
	s.SetMessageSource(nil)
	if s.messageSource != original {
		t.Fatal("SetMessageSource(nil) replaced the source; expected no-op")
	}
}

// TestSetMessageSource_PropagatesErrors confirms errors from the source reach
// the caller (summary generation must fail loudly, not silently return empty).
func TestSetMessageSource_PropagatesErrors(t *testing.T) {
	s := NewSummarizer(nil, nil, nil)
	sentinel := errors.New("source unavailable")
	s.SetMessageSource(&recordingMessageSource{fullErr: sentinel, sinceErr: sentinel})

	if _, err := s.getSessionMessages(context.Background(), "t", "s"); !errors.Is(err, sentinel) {
		t.Fatalf("getSessionMessages err = %v, want %v", err, sentinel)
	}
	if _, err := s.getMessagesSince(context.Background(), "t", "s", time.Time{}); !errors.Is(err, sentinel) {
		t.Fatalf("getMessagesSince err = %v, want %v", err, sentinel)
	}
}

// TestDefaultMessageSource_IsRequestLogs guards the contract that NewSummarizer
// installs the V1 request_logs source by default (so behavior is unchanged
// until a V2 source is explicitly registered). The concrete type assertion is
// intentional — it documents the default.
func TestDefaultMessageSource_IsRequestLogs(t *testing.T) {
	s := NewSummarizer(nil, nil, nil)
	if _, ok := s.messageSource.(*pgRequestLogsSource); !ok {
		t.Fatalf("default MessageSource = %T, want *pgRequestLogsSource", s.messageSource)
	}
}
