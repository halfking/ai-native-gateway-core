package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// fakeStore is an in-memory Store for CustomerAPI unit tests.
type fakeStore struct {
	mu       sync.Mutex
	releases map[int64]*Release
}

func newFakeStore() *fakeStore {
	return &fakeStore{releases: map[int64]*Release{}}
}

func (s *fakeStore) CreateRelease(ctx context.Context, rel *Release) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rel.ID == 0 {
		rel.ID = int64(len(s.releases) + 1)
	}
	s.releases[rel.ID] = rel
	return nil
}

func (s *fakeStore) GetRelease(ctx context.Context, version string) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.releases {
		if r.Version == version {
			return r, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) GetLatestRelease(ctx context.Context, channel Channel) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Release
	for _, r := range s.releases {
		if r.Channel != channel {
			continue
		}
		if r.PublishedAt == nil {
			continue
		}
		if best == nil || r.BuildSeq > best.BuildSeq {
			best = r
		}
	}
	return best, nil
}

func (s *fakeStore) GetLatestReleaseAfter(ctx context.Context, channel Channel, currentBuildSeq int) (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Release
	for _, r := range s.releases {
		if r.Channel != channel {
			continue
		}
		if r.PublishedAt == nil {
			continue
		}
		if r.BuildSeq <= currentBuildSeq {
			continue
		}
		if best == nil || r.BuildSeq > best.BuildSeq {
			best = r
		}
	}
	return best, nil
}

func (s *fakeStore) ListReleases(ctx context.Context, channel Channel, offset, limit int) ([]Release, int, error) {
	return nil, 0, nil
}
func (s *fakeStore) UpdateReleaseStatus(ctx context.Context, id int64, published bool) error {
	return nil
}
func (s *fakeStore) CreateGrayRule(ctx context.Context, rule *GrayReleaseRule) error { return nil }
func (s *fakeStore) ListGrayRules(ctx context.Context, limit int) ([]GrayReleaseRuleView, error) {
	return nil, nil
}
func (s *fakeStore) GetGrayRule(ctx context.Context, releaseID int64) (*GrayReleaseRule, error) {
	return nil, nil
}
func (s *fakeStore) UpdateGrayPhase(ctx context.Context, releaseID int64, phase Phase, percent int) error {
	return nil
}
func (s *fakeStore) CreateUpgradeLog(ctx context.Context, instanceID, oldVer, newVer string) (int64, error) {
	return 0, nil
}
func (s *fakeStore) UpdateUpgradeLog(ctx context.Context, id int64, status string, err string, completedAt time.Time) error {
	return nil
}
func (s *fakeStore) GetUpgradeHistory(ctx context.Context, instanceID string, offset, limit int) ([]ReleaseStatus, int, error) {
	return nil, 0, nil
}
func (s *fakeStore) GetInstanceStatus(ctx context.Context, instanceID string) (*ReleaseStatus, error) {
	return nil, nil
}
func (s *fakeStore) UpdateInstanceStatus(ctx context.Context, status *ReleaseStatus) error {
	return nil
}
func (s *fakeStore) RecordUpdateReport(ctx context.Context, report *UpdateReportData) error {
	return nil
}

func newTestCustomerAPI(t *testing.T, version string, buildSeq int) (*CustomerAPI, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	api := NewCustomerAPI(store, func() (string, int) { return version, buildSeq }, ChannelStable)
	return api, store
}

func setupEcho(api *CustomerAPI) *echo.Echo {
	e := echo.New()
	g := e.Group("/api/system/upgrade")
	api.RegisterRoutes(g)
	return e
}

func TestCustomerAPI_Status_NoUpdates(t *testing.T) {
	api, _ := newTestCustomerAPI(t, "2.4.1", 950)
	e := setupEcho(api)

	req := httptest.NewRequest(http.MethodGet, "/api/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp UpgradeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HasUpdate {
		t.Errorf("expected has_update=false, got true")
	}
	if resp.CurrentVersion != "2.4.1" {
		t.Errorf("expected current=2.4.1, got %q", resp.CurrentVersion)
	}
	if resp.Channel != ChannelStable {
		t.Errorf("expected channel=stable, got %q", resp.Channel)
	}
}

func TestCustomerAPI_Status_HasUpdate(t *testing.T) {
	api, store := newTestCustomerAPI(t, "2.4.1", 950)
	e := setupEcho(api)

	now := time.Now()
	rel := &Release{
		ID:          1,
		Version:     "2.5.0",
		BuildSeq:    951,
		Channel:     ChannelStable,
		Title:       "Performance & UX",
		Description: "Improved streaming throughput",
		ImageTag:    "kx-gateway:v2.5.0",
		Mandatory:   false,
		CreatedAt:   now,
		PublishedAt: &now,
	}
	store.CreateRelease(context.Background(), rel)

	req := httptest.NewRequest(http.MethodGet, "/api/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp UpgradeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.HasUpdate {
		t.Errorf("expected has_update=true")
	}
	if resp.LatestVersion != "2.5.0" {
		t.Errorf("expected latest=2.5.0, got %q", resp.LatestVersion)
	}
	if resp.LatestBuildSeq != 951 {
		t.Errorf("expected latest_build_seq=951, got %d", resp.LatestBuildSeq)
	}
}

func TestCustomerAPI_Check_TriggeredFresh(t *testing.T) {
	api, store := newTestCustomerAPI(t, "2.4.1", 950)
	e := setupEcho(api)

	now := time.Now()
	store.CreateRelease(context.Background(), &Release{
		ID: 1, Version: "2.5.0", BuildSeq: 951, Channel: ChannelStable,
		ImageTag: "kx-gateway:v2.5.0", PublishedAt: &now, CreatedAt: now,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/system/upgrade/check", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp UpgradeCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CheckedAt == "" {
		t.Errorf("expected checked_at to be set")
	}
	if !resp.HasUpdate {
		t.Errorf("expected has_update=true")
	}
}

func TestCustomerAPI_Status_IncompatibleMinVersion(t *testing.T) {
	api, store := newTestCustomerAPI(t, "2.3.0", 940)
	e := setupEcho(api)

	now := time.Now()
	store.CreateRelease(context.Background(), &Release{
		ID: 1, Version: "2.5.0", BuildSeq: 951, Channel: ChannelStable,
		ImageTag:    "kx-gateway:v2.5.0",
		MinVersion:  "2.4.0",
		PublishedAt: &now, CreatedAt: now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp UpgradeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IsCompatible {
		t.Errorf("expected is_compatible=false (current 2.3.0 < min 2.4.0)")
	}
}

func TestCustomerAPI_Status_UnpublishedIgnored(t *testing.T) {
	api, store := newTestCustomerAPI(t, "2.4.1", 950)
	e := setupEcho(api)

	// Unpublished release should NOT show up as an update.
	store.CreateRelease(context.Background(), &Release{
		ID: 1, Version: "2.5.0", BuildSeq: 951, Channel: ChannelStable,
		ImageTag: "kx-gateway:v2.5.0", CreatedAt: time.Now(),
		// PublishedAt is nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp UpgradeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HasUpdate {
		t.Errorf("expected has_update=false (release unpublished)")
	}
}

func TestCustomerAPI_Status_DifferentChannelIgnored(t *testing.T) {
	api, store := newTestCustomerAPI(t, "2.4.1", 950)
	e := setupEcho(api)

	now := time.Now()
	store.CreateRelease(context.Background(), &Release{
		ID: 1, Version: "2.5.0", BuildSeq: 951, Channel: ChannelBeta,
		ImageTag: "kx-gateway:v2.5.0", PublishedAt: &now, CreatedAt: now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var resp UpgradeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HasUpdate {
		t.Errorf("expected has_update=false (release is on beta channel, customer on stable)")
	}
}

// Compile-time check to silence unused imports if helpers shift.
var _ = fmt.Sprintf
