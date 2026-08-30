// live_detail_adapter.go — adapt LiveStreamRedisStore into the
// requestdetail.Locator's LiveDetailReader interface so the unified
// admin detail handler can resolve a click on a still-running swim lane
// without waiting for the eventual request_logs write.
package admin

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
)

// liveStreamLiveDetailAdapter implements requestdetail.LiveDetailReader
// by translating a LiveStreamRedisStore.LoadRequest result into a
// requestdetail.Meta. Tenant gating is enforced inside the adapter so
// the caller never sees a cross-tenant record; the lookup is fail-closed
// when the loaded row's tenant does not match the caller's scope.
type liveStreamLiveDetailAdapter struct {
	store *LiveStreamRedisStore
}

// loadLiveDetailForStore is package-private so the test below can drive
// the same path with a stub store.
func loadLiveDetailForStore(
	ctx context.Context,
	store *LiveStreamRedisStore,
	scope requestdetail.LookupScope,
	requestID string,
) (requestdetail.Meta, error) {
	if store == nil || requestID == "" {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	req, err := store.LoadRequest(ctx, scope.TenantID, requestID)
	if err != nil {
		// LoadRequest returns "request not found" via fmt.Errorf with a
		// known sentinel message; treat that as ErrNotFound so the
		// locator can fall through to the DB layer cleanly.
		if isLiveStoreMiss(err) {
			return requestdetail.Meta{}, requestdetail.ErrNotFound
		}
		return requestdetail.Meta{}, err
	}
	if req.RequestID == "" || req.RequestID != requestID {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	// Tenant gating: unrestricted (super_admin / legacy admin key) sees
	// all rows; a tenant-scoped caller must match the loaded tenant. Empty
	// loaded tenant fails closed for restricted callers.
	loadedTenant := req.TenantID
	if !scope.Unrestricted && (loadedTenant == "" || loadedTenant != scope.TenantID) {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	meta := requestdetail.Meta{
		RequestID: req.RequestID,
		TenantID:  loadedTenant,
	}
	if req.GwSessionID != "" {
		s := req.GwSessionID
		meta.GwSessionID = &s
	}
	if req.Model != "" {
		s := req.Model
		meta.ClientModel = &s
	}
	if req.Status != "" {
		s := req.Status
		meta.Status = &s
	}
	if req.LatencyMs != nil {
		v := *req.LatencyMs
		meta.LatencyMs = &v
	}
	return meta, nil
}

func (a *liveStreamLiveDetailAdapter) LoadLiveDetail(
	ctx context.Context, scope requestdetail.LookupScope, requestID string,
) (requestdetail.Meta, error) {
	return loadLiveDetailForStore(ctx, a.store, scope, requestID)
}

// isLiveStoreMiss maps the LiveStreamRedisStore "request not found"
// sentinel to requestdetail.ErrNotFound without leaking the store's
// fmt.Errorf string into Locator-level error handling. The store's
// sentinel is wrapped with the request id ("request not found: <id>"),
// so a prefix match is enough.
func isLiveStoreMiss(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, requestdetail.ErrNotFound) {
		return true
	}
	msg := err.Error()
	switch {
	case msg == "request not found",
		msg == "live stream store unavailable",
		len(msg) >= 18 && msg[:18] == "request not found:":
		return true
	}
	slog.Debug("live detail adapter: treating load error as miss", "error", msg)
	return false
}
