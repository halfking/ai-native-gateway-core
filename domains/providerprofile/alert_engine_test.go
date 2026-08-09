package providerprofile_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubActor is an in-memory CredentialActor for engine unit tests (no DB).
type stubActor struct {
	disabled       map[int64]string
	enabled        map[int64]string
	manualDisabled map[int64]bool
	whitelist      map[int64]bool
	events         int
	eventErr       error
	enableErr      error
	lifecycle      map[int64]string // credential_id -> lifecycle status (default "active")
}

func newStubActor() *stubActor {
	return &stubActor{
		disabled: map[int64]string{}, enabled: map[int64]string{},
		manualDisabled: map[int64]bool{}, whitelist: map[int64]bool{},
		lifecycle: map[int64]string{},
	}
}

func (s *stubActor) Disable(_ context.Context, id int64, reason string) error {
	s.disabled[id] = reason
	s.lifecycle[id] = "disabled"
	return nil
}
func (s *stubActor) Enable(_ context.Context, id int64, reason string) error {
	if s.enableErr != nil {
		return s.enableErr
	}
	if s.manualDisabled[id] {
		return providerprofile.ErrManualDisabled
	}
	s.enabled[id] = reason
	s.lifecycle[id] = "active"
	return nil
}
func (s *stubActor) IsWhitelisted(_ context.Context, pid int64) (bool, error) {
	return s.whitelist[pid], nil
}
func (s *stubActor) CurrentLifecycle(_ context.Context, id int64) (*providerprofile.CredentialLifecycle, error) {
	lc := &providerprofile.CredentialLifecycle{ID: id}
	if l, ok := s.lifecycle[id]; ok {
		lc.Lifecycle = l
	} else {
		lc.Lifecycle = "active"
	}
	lc.ManualDisabled = s.manualDisabled[id]
	return lc, nil
}
func (s *stubActor) RecordEvent(context.Context, int64, string, map[string]interface{}) error {
	s.events++
	return s.eventErr
}

// stubAlertStore records saved alerts in memory.
type stubAlertStore struct {
	saved []*providerprofile.Alert
	err   error
}

func (s *stubAlertStore) SaveIfNew(_ context.Context, a *providerprofile.Alert) error {
	s.saved = append(s.saved, a)
	return s.err
}
func (s *stubAlertStore) HasUnresolved(context.Context, int64, providerprofile.AlertType, time.Time) (bool, error) {
	return false, nil
}

// stubProfileSource returns canned daily profiles per credential.
type stubProfileSource struct {
	profiles map[int64][]providerprofile.DailyProfile
}

func (s *stubProfileSource) Recent(_ context.Context, credID int64, days int) ([]providerprofile.DailyProfile, error) {
	return s.profiles[credID], nil
}

func mkEngineProfile(date time.Time, total float64) providerprofile.DailyProfile {
	return providerprofile.DailyProfile{ProfileDate: date, TotalScore: total, AvailabilityScore: 90}
}

func TestAlertEngine_DisablesLowScore(t *testing.T) {
	actor := newStubActor()
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkEngineProfile(now, 30), mkEngineProfile(now.AddDate(0, 0, -1), 30), mkEngineProfile(now.AddDate(0, 0, -2), 30)},
		},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)

	assert.Equal(t, "disabled", got.Action, "should auto-disable")
	assert.Contains(t, actor.disabled, int64(42), "Disable called")
	assert.NotEmpty(t, store.saved, "alert saved")
}

func TestAlertEngine_WhitelistBlocksDisable(t *testing.T) {
	actor := newStubActor()
	actor.whitelist[4242] = true // provider_id 4242 is whitelisted; the profiles carry ProviderID=4242
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	// Build profiles with ProviderID set so the engine can check whitelist
	mkWithProv := func(date time.Time, total float64) providerprofile.DailyProfile {
		p := mkEngineProfile(date, total)
		p.ProviderID = 4242
		return p
	}
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkWithProv(now, 30), mkWithProv(now.AddDate(0, 0, -1), 30), mkWithProv(now.AddDate(0, 0, -2), 30)},
		},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "none", got.Action, "whitelist suppresses disable")
	assert.NotContains(t, actor.disabled, int64(42), "Disable NOT called for whitelisted provider")
}

func TestAlertEngine_EnablesOnlyIfCurrentlyDisabled(t *testing.T) {
	actor := newStubActor()
	actor.lifecycle[42] = "disabled" // currently disabled → eligible for recovery
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkEngineProfile(now, 75), mkEngineProfile(now.AddDate(0, 0, -1), 75), mkEngineProfile(now.AddDate(0, 0, -2), 75)},
		},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "enabled", got.Action)
	assert.Contains(t, actor.enabled, int64(42))
}

func TestAlertEngine_DoesNotEnableIfAlreadyActive(t *testing.T) {
	actor := newStubActor()
	// lifecycle[42] left unset → defaults to "active" → not eligible for recovery
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkEngineProfile(now, 75), mkEngineProfile(now.AddDate(0, 0, -1), 75), mkEngineProfile(now.AddDate(0, 0, -2), 75)},
		},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "none", got.Action, "must not enable an already-active credential")
	assert.NotContains(t, actor.enabled, int64(42))
}

func TestAlertEngine_SaveFailureIsReturned(t *testing.T) {
	actor := newStubActor()
	store := &stubAlertStore{err: errors.New("alert store unavailable")}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{profiles: map[int64][]providerprofile.DailyProfile{
		42: {mkEngineProfile(now, 30), mkEngineProfile(now.AddDate(0, 0, -1), 30), mkEngineProfile(now.AddDate(0, 0, -2), 30)},
	}}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	_, err := eng.EvaluateCredential(context.Background(), 42)
	require.Error(t, err)
	assert.ErrorContains(t, err, "save auto-disabled alert")
}

func TestAlertEngine_EventFailureIsReturned(t *testing.T) {
	actor := newStubActor()
	actor.eventErr = errors.New("event store unavailable")
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{profiles: map[int64][]providerprofile.DailyProfile{
		42: {mkEngineProfile(now, 30), mkEngineProfile(now.AddDate(0, 0, -1), 30), mkEngineProfile(now.AddDate(0, 0, -2), 30)},
	}}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.Error(t, err)
	assert.ErrorContains(t, err, "record auto-disabled event")
	assert.Equal(t, "none", got.Action)
}

func TestAlertEngine_EnableFailureIsReturned(t *testing.T) {
	actor := newStubActor()
	actor.lifecycle[42] = "disabled"
	actor.enableErr = errors.New("credential update failed")
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{profiles: map[int64][]providerprofile.DailyProfile{
		42: {mkEngineProfile(now, 75), mkEngineProfile(now.AddDate(0, 0, -1), 75), mkEngineProfile(now.AddDate(0, 0, -2), 75)},
	}}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	_, err := eng.EvaluateCredential(context.Background(), 42)
	require.Error(t, err)
	assert.ErrorContains(t, err, "enable credential")
}

func TestAlertEngine_NoDataNoAction(t *testing.T) {
	actor := newStubActor()
	store := &stubAlertStore{}
	src := &stubProfileSource{profiles: map[int64][]providerprofile.DailyProfile{}}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "none", got.Action)
	assert.Empty(t, store.saved)
}
