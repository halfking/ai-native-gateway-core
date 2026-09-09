package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/provider"
)

func TestProviderStoreBasicCRUD(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteProviderStore(db)

	// Create
	p := &provider.Provider{
		ID:       "openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com/v1",
		Protocol: provider.ProtocolOpenAI,
		AuthType: "bearer",
		Models: []provider.ModelSpec{
			{Name: "gpt-4", MaxContextTokens: 8192, SupportsStream: true},
			{Name: "gpt-3.5-turbo", MaxContextTokens: 4096, SupportsStream: true},
		},
		Headers:    map[string]string{"X-Custom": "value"},
		TimeoutSec: 30,
		Metadata:   map[string]any{"tier": "premium"},
	}

	if err := store.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read
	got, found, err := store.Get("openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("provider not found after save")
	}
	if got.Name != "OpenAI" || got.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("Get mismatch: name=%s, base_url=%s", got.Name, got.BaseURL)
	}
	if len(got.Models) != 2 || got.Models[0].Name != "gpt-4" {
		t.Errorf("Models mismatch: got %d models, first=%v", len(got.Models), got.Models)
	}

	// Update
	p.Name = "OpenAI Updated"
	p.TimeoutSec = 60
	if err := store.Save(p); err != nil {
		t.Fatalf("Update Save: %v", err)
	}

	got, found, err = store.Get("openai")
	if err != nil || !found {
		t.Fatalf("Get after update: err=%v, found=%v", err, found)
	}
	if got.Name != "OpenAI Updated" || got.TimeoutSec != 60 {
		t.Errorf("Update not reflected: name=%s, timeout=%d", got.Name, got.TimeoutSec)
	}

	// List
	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List count = %d, want 1", len(all))
	}

	// Delete
	if err := store.Delete("openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found, err = store.Get("openai")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if found {
		t.Error("provider still found after delete")
	}
}

func TestProviderStoreFindByModel(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteProviderStore(db)

	// Create multiple providers with overlapping models
	_ = store.Save(&provider.Provider{
		ID:       "openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com/v1",
		Protocol: provider.ProtocolOpenAI,
		Models:   []provider.ModelSpec{{Name: "gpt-4"}, {Name: "gpt-3.5-turbo"}},
	})

	_ = store.Save(&provider.Provider{
		ID:       "azure",
		Name:     "Azure OpenAI",
		BaseURL:  "https://azure.openai.com",
		Protocol: provider.ProtocolAzure,
		Models:   []provider.ModelSpec{{Name: "gpt-4"}},
	})

	_ = store.Save(&provider.Provider{
		ID:       "anthropic",
		Name:     "Anthropic",
		BaseURL:  "https://api.anthropic.com/v1",
		Protocol: provider.ProtocolAnthropic,
		Models:   []provider.ModelSpec{{Name: "claude-3"}},
	})

	// Find by model
	gpt4, err := store.FindByModel("gpt-4")
	if err != nil {
		t.Fatalf("FindByModel(gpt-4): %v", err)
	}
	if len(gpt4) != 2 {
		t.Errorf("FindByModel(gpt-4) count = %d, want 2", len(gpt4))
	}

	claude, err := store.FindByModel("claude-3")
	if err != nil {
		t.Fatalf("FindByModel(claude-3): %v", err)
	}
	if len(claude) != 1 || claude[0].ID != "anthropic" {
		t.Errorf("FindByModel(claude-3) = %v, want [anthropic]", claude)
	}

	unknown, err := store.FindByModel("unknown")
	if err != nil {
		t.Fatalf("FindByModel(unknown): %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("FindByModel(unknown) count = %d, want 0", len(unknown))
	}
}

func TestProviderStoreDisabledFlag(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteProviderStore(db)

	p := &provider.Provider{
		ID:       "test",
		Name:     "Test Provider",
		BaseURL:  "https://test.com",
		Protocol: provider.ProtocolCustom,
		Disabled: true,
	}

	if err := store.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, found, err := store.Get("test")
	if err != nil || !found {
		t.Fatalf("Get: err=%v, found=%v", err, found)
	}
	if !got.Disabled {
		t.Error("Disabled flag not persisted")
	}

	// FindByModel should skip disabled providers
	p.Models = []provider.ModelSpec{{Name: "test-model"}}
	p.Disabled = true
	_ = store.Save(p)

	matched, _ := store.FindByModel("test-model")
	if len(matched) != 0 {
		t.Errorf("FindByModel returned disabled provider: %v", matched)
	}
}

func TestProviderStoreHealthFields(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteProviderStore(db)

	now := time.Now()
	p := &provider.Provider{
		ID:               "test",
		Name:             "Test",
		BaseURL:          "https://test.com",
		Protocol:         provider.ProtocolCustom,
		LastHealthCheck:  now,
		ConsecutiveFails: 5,
	}

	if err := store.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, found, err := store.Get("test")
	if err != nil || !found {
		t.Fatalf("Get: err=%v, found=%v", err, found)
	}

	if got.ConsecutiveFails != 5 {
		t.Errorf("ConsecutiveFails = %d, want 5", got.ConsecutiveFails)
	}

	// Time may have minor precision loss, check within 1 second
	if diff := got.LastHealthCheck.Sub(now); diff < -time.Second || diff > time.Second {
		t.Errorf("LastHealthCheck diff = %v, want ~0", diff)
	}
}

func TestProviderStoreValidation(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "provider.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteProviderStore(db)

	// Nil provider
	if err := store.Save(nil); err == nil {
		t.Error("Save(nil) should fail")
	}

	// Empty ID
	if err := store.Save(&provider.Provider{Name: "test", BaseURL: "https://test.com", Protocol: provider.ProtocolCustom}); err == nil {
		t.Error("Save with empty ID should fail")
	}

	// Delete non-existent
	if err := store.Delete("non-existent"); err == nil {
		t.Error("Delete non-existent should fail")
	}
}
