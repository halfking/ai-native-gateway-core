package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/pending"
)

// ──────────────────────────────────────────────────────────────────────────────
// ApprovalPendingWriter adapter
// ──────────────────────────────────────────────────────────────────────────────

// PendingStoreAdapter implements ApprovalPendingWriter by wrapping pending.Store.
//
// Converts PendingResumeEntry → pending.Response and delegates Save.
type PendingStoreAdapter struct {
	store *pending.Store
}

// NewPendingStoreAdapter creates an adapter from pending.Store.
func NewPendingStoreAdapter(store *pending.Store) *PendingStoreAdapter {
	if store == nil {
		return nil
	}
	return &PendingStoreAdapter{store: store}
}

// Save implements ApprovalPendingWriter.
func (a *PendingStoreAdapter) Save(ctx context.Context, entry *PendingResumeEntry) error {
	if a == nil || a.store == nil {
		return fmt.Errorf("pending store adapter: store is nil")
	}
	if entry == nil {
		return fmt.Errorf("pending store adapter: entry is nil")
	}

	status := pending.Status(entry.Status)
	if status != pending.StatusInProgress && status != pending.StatusCompleted && status != pending.StatusFailed {
		status = pending.StatusCompleted // default
	}

	now := time.Now().Unix()
	resp := &pending.Response{
		SessionID:     entry.SessionID,
		TenantID:      entry.TenantID,
		RequestID:     entry.RequestID,
		Status:        status,
		Body:          entry.Body,
		ContentType:   entry.ContentType,
		CreatedAt:     now,
		CompletedAt:   entry.CompletedAt,
		BytesBuffered: len(entry.Body),
		IsStream:      false, // approval resume is not streaming
		ErrorMessage:  entry.ErrorMessage,
	}

	return a.store.Save(ctx, resp)
}

// ──────────────────────────────────────────────────────────────────────────────
// ClientResponder adapter
// ──────────────────────────────────────────────────────────────────────────────

// PendingStoreResponder implements ClientResponder by writing to pending.Store.
//
// Respond writes a generic success response (typically used after approval).
// RespondRejection writes a 403 rejection response.
type PendingStoreResponder struct {
	store *pending.Store
}

// NewPendingStoreResponder creates a ClientResponder from pending.Store.
func NewPendingStoreResponder(store *pending.Store) *PendingStoreResponder {
	if store == nil {
		return nil
	}
	return &PendingStoreResponder{store: store}
}

// Respond implements ClientResponder (generic response).
func (r *PendingStoreResponder) Respond(ctx context.Context, snap *sessionaudit.RequestSnapshot, payload any) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("pending store responder: store is nil")
	}
	if snap == nil {
		return fmt.Errorf("pending store responder: snapshot is nil")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pending store responder: marshal payload: %w", err)
	}

	resp := &pending.Response{
		SessionID:     snap.SessionID,
		TenantID:      snap.TenantID,
		RequestID:     snap.RequestID,
		Status:        pending.StatusCompleted,
		Body:          string(body),
		ContentType:   "application/json",
		CreatedAt:     time.Now().Unix(),
		CompletedAt:   time.Now().Unix(),
		BytesBuffered: len(body),
		IsStream:      false,
	}

	return r.store.Save(ctx, resp)
}

// RespondRejection implements ClientResponder (rejection response).
func (r *PendingStoreResponder) RespondRejection(ctx context.Context, snap *sessionaudit.RequestSnapshot, reason string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("pending store responder: store is nil")
	}
	if snap == nil {
		return fmt.Errorf("pending store responder: snapshot is nil")
	}

	rejection := map[string]any{
		"error": map[string]any{
			"type":    "approval_rejected",
			"message": reason,
		},
		"session_id": snap.SessionID,
		"request_id": snap.RequestID,
	}

	body, _ := json.Marshal(rejection)

	resp := &pending.Response{
		SessionID:     snap.SessionID,
		TenantID:      snap.TenantID,
		RequestID:     snap.RequestID,
		Status:        pending.StatusFailed,
		Body:          string(body),
		ContentType:   "application/json",
		CreatedAt:     time.Now().Unix(),
		CompletedAt:   time.Now().Unix(),
		BytesBuffered: len(body),
		IsStream:      false,
		ErrorMessage:  reason,
	}

	return r.store.Save(ctx, resp)
}

// ──────────────────────────────────────────────────────────────────────────────
// LLMCaller adapter (interface definition - implementation in streaming package)
// ──────────────────────────────────────────────────────────────────────────────

// LLMCallerFunc is a function adapter for LLMCaller.
type LLMCallerFunc func(ctx context.Context, snap *sessionaudit.RequestSnapshot) error

// CallFromSnapshot implements LLMCaller.
func (f LLMCallerFunc) CallFromSnapshot(ctx context.Context, snap *sessionaudit.RequestSnapshot) error {
	return f(ctx, snap)
}
