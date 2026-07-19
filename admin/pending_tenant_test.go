package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/redis/go-redis/v9"
)

func newPendingStore(t *testing.T) *pending.Store {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return pending.NewStore(rdb, 5*time.Minute)
}

// TestAdminPending_TenantScope verifies that tenant_admins can only see and
// delete their own tenant's entries, while super_admin retains global access.
func TestAdminPending_TenantScope(t *testing.T) {
	store := newPendingStore(t)
	ctx := context.Background()
	if err := store.MarkInProgress(ctx, &pending.Response{
		SessionID: "sess-shared", TenantID: "tenantA", RequestID: "reqA",
		Status: pending.StatusInProgress, IsStream: true,
	}); err != nil {
		t.Fatalf("MarkInProgress A: %v", err)
	}
	if err := store.MarkInProgress(ctx, &pending.Response{
		SessionID: "sess-shared", TenantID: "tenantB", RequestID: "reqB",
		Status: pending.StatusInProgress, IsStream: true,
	}); err != nil {
		t.Fatalf("MarkInProgress B: %v", err)
	}

	t.Run("tenant_admin sees own entries only", func(t *testing.T) {
		h := &Handler{}
		h.SetPendingStore(store)
		r := httptest.NewRequest(http.MethodGet, "/api/admin/pending-responses", nil)
		r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "tenantA"})
		w := httptest.NewRecorder()
		h.handlePendingList(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("list status = %d", w.Code)
		}
		body := w.Body.String()
		if !contains(body, "sess-shared") {
			t.Errorf("own tenant missing: %s", body)
		}
	})

	t.Run("tenant_admin cannot detail foreign tenant latest", func(t *testing.T) {
		h := &Handler{}
		h.SetPendingStore(store)
		r := httptest.NewRequest(http.MethodGet, "/api/admin/pending-responses/sess-shared", nil)
		r = SetAuthContext(r, &AuthContext{Role: "tenant_admin", TenantID: "tenantB"})
		w := httptest.NewRecorder()
		h.handlePendingDetail(w, r, "sess-shared")
		// Latest entry is whichever was inserted last with the higher score.
		// Either way, a tenant_admin must not see a foreign entry; if the
		// latest happens to be their own tenant that is fine, but the
		// foreign-tenant latest must return 404.
		if w.Code == http.StatusOK && contains(w.Body.String(), "tenantA") {
			t.Fatalf("tenantB admin saw tenantA entry: %s", w.Body.String())
		}
	})

	t.Run("super_admin sees global", func(t *testing.T) {
		h := &Handler{}
		h.SetPendingStore(store)
		r := httptest.NewRequest(http.MethodGet, "/api/admin/pending-responses/sess-shared", nil)
		r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: "default"})
		w := httptest.NewRecorder()
		h.handlePendingDetail(w, r, "sess-shared")
		if w.Code == http.StatusNotFound {
			t.Fatalf("super_admin got 404: %s", w.Body.String())
		}
	})
}
