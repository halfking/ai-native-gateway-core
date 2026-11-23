package pluginruntime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/kaixuan/llm-gateway-go/internal/testguard"
)

// getTestDBConnStr returns a test database connection string.
// Set TEST_DB_URL environment variable to use a real database.
// Falls back to a mock/skip if not available.
func getTestDBConnStr(t *testing.T) (string, bool) {
	connStr := os.Getenv("TEST_DB_URL")
	if connStr == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
		return "", false
	}
	return connStr, true
}

// setupTestDB creates a clean test database.
func setupTestDB(t *testing.T) (*sql.DB, string) {
	connStr, ok := getTestDBConnStr(t)
	if !ok {
		return nil, ""
	}

	// ★★ 目标库守卫（R88 轮形态：判定必须**在第一条破坏性语句之前**）★★
	//
	// ★ 这条腿会 `DROP TABLE IF EXISTS plugin_registry` ⇒ ★ 而 `plugin_registry`
	//   是**插件运行时的产品表**（不只是本腿自建的测试对象）⇒ ★ 若事实源
	//   （scripts/audit/datasource_env.sh）把 TEST_DB_URL 指到产品库 ⇒ ★
	//   **这条腿会清空产品库里的插件注册表** ⇒ ★ 数据事故级。
	// ⇒ ★ 因此在建连/建池**之前**先证明目标是隔离库；证明不了就**跳过**
	//   （跳过不构成证据，但不造成伤害）。
	// ⇒ ★ 范式与仓内另外四条已修的高危腿一致（见
	//   scripts/ursmcheck/destructive_target_ledger.tsv 头部点名的那四条）。
	testguard.RequireIsolatedTarget(t, connStr)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Clean up any existing test data
	ctx := context.Background()
	_, err = db.ExecContext(ctx, "DROP TABLE IF EXISTS plugin_registry")
	if err != nil {
		db.Close()
		t.Fatalf("Failed to drop test table: %v", err)
	}

	return db, connStr
}

func TestNewDBRegistry(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return // Skipped
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	// Verify table was created
	var exists bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_name = 'plugin_registry'
		)
	`).Scan(&exists)
	
	if err != nil {
		t.Fatalf("Failed to check table existence: %v", err)
	}

	if !exists {
		t.Error("plugin_registry table was not created")
	}
}

func TestDBRegistry_Register(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register a plugin
	manifest := map[string]interface{}{
		"name":    "test-plugin",
		"version": "1.0.0",
		"type":    "extension",
	}

	err = registry.Register(ctx, "test-plugin", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify it's in the database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM plugin_registry WHERE plugin_id = $1", "test-plugin").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 plugin in database, got %d", count)
	}

	// Verify it's in memory cache
	entry, err := registry.Get("test-plugin")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if entry.State != StateEnabled {
		t.Errorf("Expected state 'enabled', got '%s'", entry.State)
	}
}

func TestDBRegistry_SetState(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register a plugin
	manifest := map[string]interface{}{
		"name": "test-plugin",
	}
	err = registry.Register(ctx, "test-plugin", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Change state to disabled
	err = registry.SetState(ctx, "test-plugin", StateDisabled)
	if err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	// Verify state in memory
	state, err := registry.GetState("test-plugin")
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}

	if state != StateDisabled {
		t.Errorf("Expected state 'disabled', got '%s'", state)
	}

	// Verify state in database
	var dbState string
	err = db.QueryRow("SELECT state FROM plugin_registry WHERE plugin_id = $1", "test-plugin").Scan(&dbState)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	if dbState != string(StateDisabled) {
		t.Errorf("Expected database state 'disabled', got '%s'", dbState)
	}
}

func TestDBRegistry_StatePersistence(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	ctx := context.Background()

	// First registry instance: register and disable a plugin
	registry1, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry (1st) failed: %v", err)
	}

	manifest := map[string]interface{}{
		"name":    "persistent-plugin",
		"version": "1.0.0",
	}
	err = registry1.Register(ctx, "persistent-plugin", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err = registry1.SetState(ctx, "persistent-plugin", StateDisabled)
	if err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	registry1.Close()

	// Second registry instance: verify state persisted
	registry2, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry (2nd) failed: %v", err)
	}
	defer registry2.Close()

	state, err := registry2.GetState("persistent-plugin")
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}

	if state != StateDisabled {
		t.Errorf("Expected persisted state 'disabled', got '%s'", state)
	}

	t.Log("State persistence verified: plugin state survived registry restart")
}

func TestDBRegistry_List(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register multiple plugins
	for i := 1; i <= 3; i++ {
		manifest := map[string]interface{}{
			"name": fmt.Sprintf("plugin-%d", i),
		}
		state := StateEnabled
		if i == 2 {
			state = StateDisabled
		}
		err = registry.Register(ctx, fmt.Sprintf("plugin-%d", i), manifest, state)
		if err != nil {
			t.Fatalf("Register plugin-%d failed: %v", i, err)
		}
	}

	// List all plugins
	all := registry.List()
	if len(all) != 3 {
		t.Errorf("Expected 3 plugins, got %d", len(all))
	}

	// List enabled plugins
	enabled := registry.ListByState(StateEnabled)
	if len(enabled) != 2 {
		t.Errorf("Expected 2 enabled plugins, got %d", len(enabled))
	}

	// List disabled plugins
	disabled := registry.ListByState(StateDisabled)
	if len(disabled) != 1 {
		t.Errorf("Expected 1 disabled plugin, got %d", len(disabled))
	}
}

func TestDBRegistry_Unregister(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register a plugin
	manifest := map[string]interface{}{
		"name": "removable-plugin",
	}
	err = registry.Register(ctx, "removable-plugin", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Unregister it
	err = registry.Unregister(ctx, "removable-plugin")
	if err != nil {
		t.Fatalf("Unregister failed: %v", err)
	}

	// Verify it's gone from memory
	_, err = registry.Get("removable-plugin")
	if err == nil {
		t.Error("Expected error when getting unregistered plugin, got nil")
	}

	// Verify it's gone from database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM plugin_registry WHERE plugin_id = $1", "removable-plugin").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 plugins in database, got %d", count)
	}
}

func TestDBRegistry_UpdateExisting(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register initial version
	manifest1 := map[string]interface{}{
		"name":    "updatable-plugin",
		"version": "1.0.0",
	}
	err = registry.Register(ctx, "updatable-plugin", manifest1, StateEnabled)
	if err != nil {
		t.Fatalf("Register (v1) failed: %v", err)
	}

	// Update with new manifest
	manifest2 := map[string]interface{}{
		"name":    "updatable-plugin",
		"version": "2.0.0",
	}
	err = registry.Register(ctx, "updatable-plugin", manifest2, StateEnabled)
	if err != nil {
		t.Fatalf("Register (v2) failed: %v", err)
	}

	// Verify updated version
	entry, err := registry.Get("updatable-plugin")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	version, ok := entry.ManifestJSON["version"].(string)
	if !ok || version != "2.0.0" {
		t.Errorf("Expected version '2.0.0', got '%v'", entry.ManifestJSON["version"])
	}

	// Verify only one entry exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM plugin_registry WHERE plugin_id = $1", "updatable-plugin").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 plugin in database, got %d (should use ON CONFLICT)", count)
	}
}

func TestDBRegistry_Sync(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register a plugin
	manifest := map[string]interface{}{
		"name": "sync-test",
	}
	err = registry.Register(ctx, "sync-test", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Manually modify database (simulate external change)
	_, err = db.ExecContext(ctx, `
		UPDATE plugin_registry 
		SET state = $1
		WHERE plugin_id = $2
	`, StateDisabled, "sync-test")
	if err != nil {
		t.Fatalf("Failed to update database: %v", err)
	}

	// Memory cache still shows old state
	state, _ := registry.GetState("sync-test")
	if state != StateEnabled {
		t.Errorf("Expected memory state 'enabled' before sync, got '%s'", state)
	}

	// Sync from database
	err = registry.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Memory cache should now reflect database state
	state, err = registry.GetState("sync-test")
	if err != nil {
		t.Fatalf("GetState after sync failed: %v", err)
	}

	if state != StateDisabled {
		t.Errorf("Expected synced state 'disabled', got '%s'", state)
	}
}

func TestDBRegistry_ConnectionFailure(t *testing.T) {
	// Test with invalid connection string
	_, err := NewDBRegistry("postgres://invalid:invalid@localhost:5432/nonexistent", "/tmp/plugins.json")
	if err == nil {
		t.Error("Expected error with invalid connection string, got nil")
	}
}

func TestDBRegistry_ConcurrentAccess(t *testing.T) {
	db, connStr := setupTestDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	registry, err := NewDBRegistry(connStr, "/tmp/plugins.json")
	if err != nil {
		t.Fatalf("NewDBRegistry failed: %v", err)
	}
	defer registry.Close()

	ctx := context.Background()

	// Register initial plugin
	manifest := map[string]interface{}{
		"name": "concurrent-plugin",
	}
	err = registry.Register(ctx, "concurrent-plugin", manifest, StateEnabled)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Concurrent state changes
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			
			state := StateEnabled
			if id%2 == 0 {
				state = StateDisabled
			}
			
			time.Sleep(time.Millisecond * time.Duration(id*10))
			registry.SetState(context.Background(), "concurrent-plugin", state)
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify registry is still consistent (no panics, state is valid)
	state, err := registry.GetState("concurrent-plugin")
	if err != nil {
		t.Fatalf("GetState after concurrent access failed: %v", err)
	}

	if state != StateEnabled && state != StateDisabled {
		t.Errorf("Invalid state after concurrent access: '%s'", state)
	}

	t.Logf("Concurrent access test passed, final state: %s", state)
}
