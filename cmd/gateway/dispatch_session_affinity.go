package main

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

type sessionPreferenceAffinitySink struct {
	preference *session.SessionPreference
}

func (s sessionPreferenceAffinitySink) RecordSuccess(ctx context.Context, sessionID string, credential dispatch.CredentialRef, model string) error {
	return s.preference.Set(ctx, sessionID, credential.CredentialID, model)
}

func (s sessionPreferenceAffinitySink) Invalidate(ctx context.Context, sessionID string, credentialID int) error {
	current, found := s.preference.Get(ctx, sessionID)
	if !found || current == nil || current.CredentialID != credentialID {
		return nil
	}
	return s.preference.Delete(ctx, sessionID)
}
