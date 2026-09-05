package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

type vocabularyFixture struct {
	SchemaVersion      int      `json:"schema_version"`
	UnknownVersion     string   `json:"unknown_version"`
	EventTypes         []string `json:"event_types"`
	LifecycleStates    []string `json:"lifecycle_states"`
	JourneyStages      []string `json:"journey_stages"`
	Actions            []string `json:"actions"`
	KnownErrorKinds    []string `json:"known_error_kinds"`
	ResourceKindPolicy string   `json:"resource_kind_policy"`
	ResourceKinds      []string `json:"resource_kinds"`
	ErrorKindPolicy    string   `json:"error_kind_policy"`
}

func TestVocabularyV1Fixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "fixtures", "vocabulary_v1_valid.json"))
	if err != nil {
		t.Fatalf("read vocabulary fixture: %v", err)
	}
	var fixture vocabularyFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode vocabulary fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 || fixture.UnknownVersion != "reject" {
		t.Fatalf("invalid vocabulary fixture header: %+v", fixture)
	}
	if fixture.ErrorKindPolicy != "open_with_frozen_dispatch_subset" || fixture.ResourceKindPolicy != "derived_cache_only_no_admission_authority" {
		t.Fatalf("invalid vocabulary policy: %+v", fixture)
	}
	if !reflect.DeepEqual(fixture.EventTypes, eventTypeStrings()) || !reflect.DeepEqual(fixture.LifecycleStates, lifecycleStrings()) || !reflect.DeepEqual(fixture.JourneyStages, stageStrings()) || !reflect.DeepEqual(fixture.Actions, actionStrings()) || !reflect.DeepEqual(fixture.KnownErrorKinds, requestjourney.KnownErrorKinds()) {
		t.Fatalf("fixture vocabulary diverged from exported contract: %+v", fixture)
	}
	if !reflect.DeepEqual(fixture.ResourceKinds, []string{"concurrency", "fp_slot", "rpm"}) {
		t.Fatalf("resource kinds = %v", fixture.ResourceKinds)
	}
}

func eventTypeStrings() []string {
	types := requestjourney.AllEventTypes()
	got := make([]string, len(types))
	for i, eventType := range types {
		got[i] = string(eventType)
	}
	return got
}

func lifecycleStrings() []string {
	states := requestjourney.AllLifecycleStates()
	got := make([]string, len(states))
	for i, state := range states {
		got[i] = string(state)
	}
	return got
}

func stageStrings() []string {
	stages := requestjourney.AllJourneyStages()
	got := make([]string, len(stages))
	for i, stage := range stages {
		got[i] = string(stage)
	}
	return got
}

func actionStrings() []string {
	got := make([]string, len(liveactions.Actions))
	for i, action := range liveactions.Actions {
		got[i] = string(action)
	}
	return got
}
