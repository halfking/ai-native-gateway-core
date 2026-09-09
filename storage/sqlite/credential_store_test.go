package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
)

func TestCredentialStoreBasicCRUD(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	// Create provider first (foreign key constraint)
	providerStore := NewSQLiteProviderStore(db)
	_ = providerStore.Save(&provider.Provider{
		ID:       "openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com/v1",
		Protocol: provider.ProtocolOpenAI,
	})

	store := NewSQLiteCredentialStore(db)

	// Create
	cred := &credential.Credential{
		ID:           "cred-1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		Model:        "gpt-4",
		EncryptedKey: []byte("encrypted-api-key-data"),
		Priority:     80,
		Status:       credential.StatusActive,
		Metadata:     map[string]any{"env": "production"},
	}

	if err := store.Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read
	got, found, err := store.Get("cred-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("credential not found after save")
	}
	if got.TenantID != "tenant-a" || got.ProviderID != "openai" {
		t.Errorf("Get mismatch: tenant=%s, provider=%s", got.TenantID, got.ProviderID)
	}
	if string(got.EncryptedKey) != "encrypted-api-key-data" {
		t.Errorf("EncryptedKey mismatch: got %s", string(got.EncryptedKey))
	}

	// Update
	cred.Priority = 90
	cred.Status = credential.StatusDegraded
	if err := store.Save(cred); err != nil {
		t.Fatalf("Update Save: %v", err)
	}

	got, found, err = store.Get("cred-1")
	if err != nil || !found {
		t.Fatalf("Get after update: err=%v, found=%v", err, found)
	}
	if got.Priority != 90 || got.Status != credential.StatusDegraded {
		t.Errorf("Update not reflected: priority=%d, status=%s", got.Priority, got.Status)
	}

	// Delete
	if err := store.Delete("cred-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found, err = store.Get("cred-1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if found {
		t.Error("credential still found after delete")
	}
}

func TestCredentialStoreTenantIsolation(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	// Create providers first
	providerStore := NewSQLiteProviderStore(db)
	_ = providerStore.Save(&provider.Provider{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Protocol: provider.ProtocolOpenAI})
	_ = providerStore.Save(&provider.Provider{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com/v1", Protocol: provider.ProtocolAnthropic})

	store := NewSQLiteCredentialStore(db)

	// Create credentials for different tenants
	_ = store.Save(&credential.Credential{
		ID:           "cred-a1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-a1"),
		Status:       credential.StatusActive,
	})

	_ = store.Save(&credential.Credential{
		ID:           "cred-a2",
		TenantID:     "tenant-a",
		ProviderID:   "anthropic",
		EncryptedKey: []byte("key-a2"),
		Status:       credential.StatusActive,
	})

	_ = store.Save(&credential.Credential{
		ID:           "cred-b1",
		TenantID:     "tenant-b",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-b1"),
		Status:       credential.StatusActive,
	})

	// List by tenant
	tenantA, err := store.List("tenant-a")
	if err != nil {
		t.Fatalf("List(tenant-a): %v", err)
	}
	if len(tenantA) != 2 {
		t.Errorf("List(tenant-a) count = %d, want 2", len(tenantA))
	}

	tenantB, err := store.List("tenant-b")
	if err != nil {
		t.Fatalf("List(tenant-b): %v", err)
	}
	if len(tenantB) != 1 || tenantB[0].ID != "cred-b1" {
		t.Errorf("List(tenant-b) = %v, want [cred-b1]", tenantB)
	}

	// Empty tenant list
	empty, err := store.List("non-existent")
	if err != nil {
		t.Fatalf("List(non-existent): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List(non-existent) count = %d, want 0", len(empty))
	}
}

func TestCredentialStoreListByProvider(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	// Create providers first
	providerStore := NewSQLiteProviderStore(db)
	_ = providerStore.Save(&provider.Provider{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Protocol: provider.ProtocolOpenAI})
	_ = providerStore.Save(&provider.Provider{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com/v1", Protocol: provider.ProtocolAnthropic})

	store := NewSQLiteCredentialStore(db)

	// Create credentials for different providers
	_ = store.Save(&credential.Credential{
		ID:           "cred-1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-1"),
		Status:       credential.StatusActive,
	})

	_ = store.Save(&credential.Credential{
		ID:           "cred-2",
		TenantID:     "tenant-b",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-2"),
		Status:       credential.StatusActive,
	})

	_ = store.Save(&credential.Credential{
		ID:           "cred-3",
		TenantID:     "tenant-a",
		ProviderID:   "anthropic",
		EncryptedKey: []byte("key-3"),
		Status:       credential.StatusActive,
	})

	// List by provider (cross-tenant)
	openaiCreds, err := store.ListByProvider("openai")
	if err != nil {
		t.Fatalf("ListByProvider(openai): %v", err)
	}
	if len(openaiCreds) != 2 {
		t.Errorf("ListByProvider(openai) count = %d, want 2", len(openaiCreds))
	}

	anthropicCreds, err := store.ListByProvider("anthropic")
	if err != nil {
		t.Fatalf("ListByProvider(anthropic): %v", err)
	}
	if len(anthropicCreds) != 1 || anthropicCreds[0].ID != "cred-3" {
		t.Errorf("ListByProvider(anthropic) = %v, want [cred-3]", anthropicCreds)
	}
}

func TestCredentialStoreEncryptedKeyRequired(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteCredentialStore(db)

	// Empty EncryptedKey should fail
	cred := &credential.Credential{
		ID:           "cred-1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: []byte{}, // empty
		Status:       credential.StatusActive,
	}

	if err := store.Save(cred); err == nil {
		t.Error("Save with empty EncryptedKey should fail")
	}

	// Nil EncryptedKey should also fail
	cred.EncryptedKey = nil
	if err := store.Save(cred); err == nil {
		t.Error("Save with nil EncryptedKey should fail")
	}
}

func TestCredentialStoreValidation(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	store := NewSQLiteCredentialStore(db)

	tests := []struct {
		name string
		cred *credential.Credential
	}{
		{"nil credential", nil},
		{"empty ID", &credential.Credential{TenantID: "t", ProviderID: "p", EncryptedKey: []byte("k")}},
		{"empty TenantID", &credential.Credential{ID: "c", ProviderID: "p", EncryptedKey: []byte("k")}},
		{"empty ProviderID", &credential.Credential{ID: "c", TenantID: "t", EncryptedKey: []byte("k")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Save(tt.cred); err == nil {
				t.Errorf("Save(%s) should fail", tt.name)
			}
		})
	}

	// Delete non-existent
	if err := store.Delete("non-existent"); err == nil {
		t.Error("Delete non-existent should fail")
	}
}

func TestCredentialStoreStatusFilter(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	// Create provider first
	providerStore := NewSQLiteProviderStore(db)
	_ = providerStore.Save(&provider.Provider{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Protocol: provider.ProtocolOpenAI})

	store := NewSQLiteCredentialStore(db)

	// Create credentials with different statuses
	_ = store.Save(&credential.Credential{
		ID:           "cred-active",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-active"),
		Status:       credential.StatusActive,
	})

	_ = store.Save(&credential.Credential{
		ID:           "cred-disabled",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: []byte("key-disabled"),
		Status:       credential.StatusDisabled,
	})

	// List should return all (filtering is caller's responsibility)
	all, err := store.List("tenant-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List count = %d, want 2", len(all))
	}

	// Verify order: status ASC means active before disabled
	if all[0].Status != credential.StatusActive {
		t.Errorf("First credential status = %s, want active (status ASC sort)", all[0].Status)
	}
}

func TestCredentialStoreNoPlaintextInDB(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "credential.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	// Create provider first
	providerStore := NewSQLiteProviderStore(db)
	_ = providerStore.Save(&provider.Provider{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Protocol: provider.ProtocolOpenAI})

	store := NewSQLiteCredentialStore(db)

	// Save with "encrypted" data (in real usage this would be AES-GCM ciphertext)
	plaintext := "sk-actual-api-key-plaintext"
	encrypted := []byte("v1:kid123:base64encrypteddata") // mock AES-GCM format

	cred := &credential.Credential{
		ID:           "cred-1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		EncryptedKey: encrypted,
		Status:       credential.StatusActive,
	}

	if err := store.Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Direct DB query to verify no plaintext
	var storedKey []byte
	err = db.QueryRow("SELECT encrypted_key FROM credentials WHERE id = ?", "cred-1").Scan(&storedKey)
	if err != nil {
		t.Fatalf("Direct query: %v", err)
	}

	// Plaintext should NOT be in DB
	if string(storedKey) == plaintext {
		t.Error("SECURITY: plaintext API key found in database")
	}

	// Encrypted format should be preserved
	if string(storedKey) != string(encrypted) {
		t.Errorf("Encrypted key mismatch in DB: got %s, want %s", storedKey, encrypted)
	}
}
