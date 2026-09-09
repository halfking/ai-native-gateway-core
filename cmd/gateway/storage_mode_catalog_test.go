package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
	sqlitestore "github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// TestLiteCatalogStoresAccessible 验证 lite 模式下 catalog stores 可通过 runtime 访问。
func TestLiteCatalogStoresAccessible(t *testing.T) {
	tmpDir := t.TempDir()
	storageCfg := &config.StorageConfig{
		Mode: string(config.StorageModeLite),
		Lite: &config.LiteStorageConfig{
			SQLitePath: filepath.Join(tmpDir, "test.db"),
			BodiesDir:  filepath.Join(tmpDir, "bodies"),
			CacheDir:   filepath.Join(tmpDir, "cache"),
			LogsDir:    filepath.Join(tmpDir, "logs"),
		},
	}

	rt, err := initStorageMode(nil, storageCfg)
	if err != nil {
		t.Fatalf("initStorageMode: %v", err)
	}
	defer rt.Shutdown()

	// Provider store
	providerStoreIface := rt.GetProviderStore()
	if providerStoreIface == nil {
		t.Fatal("GetProviderStore returned nil")
	}
	providerStore, ok := providerStoreIface.(*sqlitestore.SQLiteProviderStore)
	if !ok {
		t.Fatalf("GetProviderStore type = %T, want *SQLiteProviderStore", providerStoreIface)
	}

	// Credential store
	credStoreIface := rt.GetCredentialStore()
	if credStoreIface == nil {
		t.Fatal("GetCredentialStore returned nil")
	}
	credStore, ok := credStoreIface.(*sqlitestore.SQLiteCredentialStore)
	if !ok {
		t.Fatalf("GetCredentialStore type = %T, want *SQLiteCredentialStore", credStoreIface)
	}

	// Model store
	modelStoreIface := rt.GetModelStore()
	if modelStoreIface == nil {
		t.Fatal("GetModelStore returned nil")
	}
	_, ok = modelStoreIface.(*sqlitestore.SQLiteModelStore)
	if !ok {
		t.Fatalf("GetModelStore type = %T, want *SQLiteModelStore", modelStoreIface)
	}

	// Binding store
	bindingStoreIface := rt.GetBindingStore()
	if bindingStoreIface == nil {
		t.Fatal("GetBindingStore returned nil")
	}
	_, ok = bindingStoreIface.(*sqlitestore.SQLiteBindingStore)
	if !ok {
		t.Fatalf("GetBindingStore type = %T, want *SQLiteBindingStore", bindingStoreIface)
	}

	// Basic CRUD smoke test
	p := &provider.Provider{
		ID:       "test-provider",
		Name:     "Test Provider",
		BaseURL:  "https://test.example.com",
		Protocol: provider.ProtocolOpenAI,
	}
	if err := providerStore.Save(p); err != nil {
		t.Fatalf("providerStore.Save: %v", err)
	}

	c := &credential.Credential{
		ID:           "test-cred",
		TenantID:     "tenant-1",
		ProviderID:   "test-provider",
		EncryptedKey: []byte("encrypted-data"),
		Status:       credential.StatusActive,
	}
	if err := credStore.Save(c); err != nil {
		t.Fatalf("credStore.Save: %v", err)
	}

	// Verify read
	gotProvider, found, err := providerStore.Get("test-provider")
	if err != nil || !found {
		t.Fatalf("providerStore.Get: err=%v, found=%v", err, found)
	}
	if gotProvider.Name != "Test Provider" {
		t.Errorf("provider.Name = %s, want Test Provider", gotProvider.Name)
	}

	gotCred, found, err := credStore.Get("test-cred")
	if err != nil || !found {
		t.Fatalf("credStore.Get: err=%v, found=%v", err, found)
	}
	if gotCred.TenantID != "tenant-1" {
		t.Errorf("credential.TenantID = %s, want tenant-1", gotCred.TenantID)
	}
}

// TestLiteCatalogPersistenceRoundTrip 验证 catalog 数据在关闭/重启后能够恢复。
func TestLiteCatalogPersistenceRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	sqlitePath := filepath.Join(tmpDir, "catalog.db")

	storageCfg := &config.StorageConfig{
		Mode: string(config.StorageModeLite),
		Lite: &config.LiteStorageConfig{
			SQLitePath: sqlitePath,
			BodiesDir:  filepath.Join(tmpDir, "bodies"),
			CacheDir:   filepath.Join(tmpDir, "cache"),
			LogsDir:    filepath.Join(tmpDir, "logs"),
		},
	}

	// Phase 1: Create and save data
	rt1, err := initStorageMode(nil, storageCfg)
	if err != nil {
		t.Fatalf("initStorageMode phase 1: %v", err)
	}

	providerStore1 := rt1.GetProviderStore().(*sqlitestore.SQLiteProviderStore)
	credStore1 := rt1.GetCredentialStore().(*sqlitestore.SQLiteCredentialStore)

	// Save provider
	p := &provider.Provider{
		ID:       "openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com/v1",
		Protocol: provider.ProtocolOpenAI,
		Models: []provider.ModelSpec{
			{Name: "gpt-4", MaxContextTokens: 8192, SupportsStream: true},
		},
	}
	if err := providerStore1.Save(p); err != nil {
		t.Fatalf("Save provider: %v", err)
	}

	// Save credential
	c := &credential.Credential{
		ID:           "cred-1",
		TenantID:     "tenant-a",
		ProviderID:   "openai",
		Model:        "gpt-4",
		EncryptedKey: []byte("v1:kid:encrypted-api-key"),
		Priority:     80,
		Status:       credential.StatusActive,
	}
	if err := credStore1.Save(c); err != nil {
		t.Fatalf("Save credential: %v", err)
	}

	// Shutdown phase 1
	rt1.Shutdown()

	// Verify SQLite file exists
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		t.Fatalf("SQLite file not created: %s", sqlitePath)
	}

	// Phase 2: Restart and verify data persisted
	rt2, err := initStorageMode(nil, storageCfg)
	if err != nil {
		t.Fatalf("initStorageMode phase 2 (restart): %v", err)
	}
	defer rt2.Shutdown()

	providerStore2 := rt2.GetProviderStore().(*sqlitestore.SQLiteProviderStore)
	credStore2 := rt2.GetCredentialStore().(*sqlitestore.SQLiteCredentialStore)

	// Verify provider restored
	gotProvider, found, err := providerStore2.Get("openai")
	if err != nil {
		t.Fatalf("Get provider after restart: %v", err)
	}
	if !found {
		t.Fatal("Provider not found after restart")
	}
	if gotProvider.Name != "OpenAI" || gotProvider.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("Provider data mismatch: name=%s, base_url=%s", gotProvider.Name, gotProvider.BaseURL)
	}
	if len(gotProvider.Models) != 1 || gotProvider.Models[0].Name != "gpt-4" {
		t.Errorf("Provider models not restored: %+v", gotProvider.Models)
	}

	// Verify credential restored
	gotCred, found, err := credStore2.Get("cred-1")
	if err != nil {
		t.Fatalf("Get credential after restart: %v", err)
	}
	if !found {
		t.Fatal("Credential not found after restart")
	}
	if gotCred.TenantID != "tenant-a" || gotCred.ProviderID != "openai" {
		t.Errorf("Credential data mismatch: tenant=%s, provider=%s", gotCred.TenantID, gotCred.ProviderID)
	}
	if string(gotCred.EncryptedKey) != "v1:kid:encrypted-api-key" {
		t.Errorf("EncryptedKey mismatch: got %s", string(gotCred.EncryptedKey))
	}
	if gotCred.Priority != 80 || gotCred.Status != credential.StatusActive {
		t.Errorf("Credential state mismatch: priority=%d, status=%s", gotCred.Priority, gotCred.Status)
	}
}

// TestLiteCatalogForeignKeyConstraint 验证外键约束生效（删除 provider 级联删除 credential）。
func TestLiteCatalogForeignKeyConstraint(t *testing.T) {
	tmpDir := t.TempDir()
	storageCfg := &config.StorageConfig{
		Mode: string(config.StorageModeLite),
		Lite: &config.LiteStorageConfig{
			SQLitePath: filepath.Join(tmpDir, "fk.db"),
			BodiesDir:  filepath.Join(tmpDir, "bodies"),
			CacheDir:   filepath.Join(tmpDir, "cache"),
			LogsDir:    filepath.Join(tmpDir, "logs"),
		},
	}

	rt, err := initStorageMode(nil, storageCfg)
	if err != nil {
		t.Fatalf("initStorageMode: %v", err)
	}
	defer rt.Shutdown()

	providerStore := rt.GetProviderStore().(*sqlitestore.SQLiteProviderStore)
	credStore := rt.GetCredentialStore().(*sqlitestore.SQLiteCredentialStore)

	// Create provider + credential
	_ = providerStore.Save(&provider.Provider{
		ID:       "test-provider",
		Name:     "Test",
		BaseURL:  "https://test.com",
		Protocol: provider.ProtocolCustom,
	})

	_ = credStore.Save(&credential.Credential{
		ID:           "test-cred",
		TenantID:     "tenant-1",
		ProviderID:   "test-provider",
		EncryptedKey: []byte("encrypted"),
		Status:       credential.StatusActive,
	})

	// Delete provider should cascade delete credential
	if err := providerStore.Delete("test-provider"); err != nil {
		t.Fatalf("Delete provider: %v", err)
	}

	// Credential should be gone
	_, found, err := credStore.Get("test-cred")
	if err != nil {
		t.Fatalf("Get credential after provider delete: %v", err)
	}
	if found {
		t.Error("Credential still exists after provider cascade delete")
	}
}
