# Database-Backed Plugin Registry

## Overview

The `DBRegistry` provides persistent storage for plugin registration and state management using PostgreSQL. It maintains plugin enabled/disabled state across gateway restarts.

## Architecture

### Dual-Write Strategy
- **Database**: Source of truth (PostgreSQL with JSONB support)
- **Memory Cache**: In-memory map for fast reads (RWMutex protected)
- **File Fallback**: Optional file-based backup (future enhancement)

### Schema

```sql
CREATE TABLE plugin_registry (
    plugin_id TEXT PRIMARY KEY,
    manifest_json JSONB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('enabled', 'disabled')),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_plugin_registry_state ON plugin_registry(state);
CREATE INDEX idx_plugin_registry_updated_at ON plugin_registry(updated_at);
```

## Usage

### Initialization

```go
import "github.com/kaixuan/llm-gateway-go/plugin-runtime"

// Connect to database
registry, err := pluginruntime.NewDBRegistry(
    "postgres://user@localhost:5432/llm_gateway?sslmode=disable",
    "/var/lib/llm-gateway/plugins.json", // file fallback (optional)
)
if err != nil {
    log.Fatal(err)
}
defer registry.Close()
```

### Register Plugin

```go
ctx := context.Background()

manifest := map[string]interface{}{
    "name":    "ai-session-manager",
    "version": "1.0.0",
    "type":    "extension",
}

err := registry.Register(ctx, "ai-session-manager", manifest, pluginruntime.StateEnabled)
```

### Change Plugin State

```go
// Disable plugin
err := registry.SetState(ctx, "ai-session-manager", pluginruntime.StateDisabled)

// Enable plugin
err := registry.SetState(ctx, "ai-session-manager", pluginruntime.StateEnabled)
```

### Query Plugins

```go
// Get single plugin
entry, err := registry.Get("ai-session-manager")
if err != nil {
    log.Printf("Plugin not found: %v", err)
}

// List all plugins
allPlugins := registry.List()

// List by state
enabledPlugins := registry.ListByState(pluginruntime.StateEnabled)
disabledPlugins := registry.ListByState(pluginruntime.StateDisabled)
```

### State Persistence

Plugin state survives gateway restarts:

```go
// First instance: disable plugin
registry1, _ := pluginruntime.NewDBRegistry(connStr, "")
registry1.SetState(ctx, "plugin-id", pluginruntime.StateDisabled)
registry1.Close()

// Second instance: state persisted
registry2, _ := pluginruntime.NewDBRegistry(connStr, "")
state, _ := registry2.GetState("plugin-id")
// state == StateDisabled ✓
```

## Testing

### Prerequisites

**Option 1: Local PostgreSQL**
```bash
# macOS (Homebrew)
brew install postgresql@14
brew services start postgresql@14
createdb llm_gateway_test

# Linux
sudo apt-get install postgresql-14
sudo systemctl start postgresql
sudo -u postgres createdb llm_gateway_test
```

**Option 2: Docker**
```bash
docker run --name postgres-test \
  -e POSTGRES_PASSWORD=testpass \
  -p 5432:5432 \
  -d postgres:14
```

### Run Tests

```bash
# Set database connection
export TEST_DB_URL="postgres://postgres@localhost:5432/llm_gateway_test?sslmode=disable"

# Run all DB registry tests
cd llm-gateway-go
go test -v ./plugin-runtime -run TestDBRegistry

# Run specific test
go test -v ./plugin-runtime -run TestDBRegistry_StatePersistence
```

### Test Coverage

The test suite covers:
- ✅ Schema initialization
- ✅ Plugin registration (insert/update)
- ✅ State changes (enabled ↔ disabled)
- ✅ **State persistence across restarts** (key requirement)
- ✅ List operations (all, by state)
- ✅ Unregister (delete)
- ✅ Concurrent access (race condition handling)
- ✅ Database sync (recovery from external changes)
- ✅ Connection failure handling

## Integration with Existing Components

### Supervisor Integration

```go
// On plugin start, check if it should be started
state, err := registry.GetState(pluginID)
if err != nil || state == pluginruntime.StateDisabled {
    log.Printf("Plugin %s is disabled, skipping start", pluginID)
    return
}

supervisor.Start(ctx, manifest)
```

### Config Watcher Integration

```go
// React to config changes
configWatcher.RegisterCallback(func(oldConfig, newConfig *pluginruntime.GatewayConfig) error {
    // Update plugin states based on new config
    for _, pluginID := range newConfig.Plugins.Essentials {
        registry.SetState(ctx, pluginID, pluginruntime.StateEnabled)
    }
    return nil
})
```

### Registry + Supervisor Coordination

```go
type PluginManager struct {
    registry   *pluginruntime.DBRegistry
    supervisor *supervisor.Supervisor
}

func (pm *PluginManager) EnablePlugin(ctx context.Context, pluginID string) error {
    // Update state in registry
    if err := pm.registry.SetState(ctx, pluginID, pluginruntime.StateEnabled); err != nil {
        return err
    }
    
    // Start plugin process
    manifest, _ := pm.registry.Get(pluginID)
    return pm.supervisor.Start(ctx, manifest)
}
```

## Performance Characteristics

| Operation | Latency | Notes |
|-----------|---------|-------|
| Register | ~5ms | DB write + memory update |
| SetState | ~3ms | DB write + memory update |
| Get | ~10µs | Memory read (RWMutex) |
| List | ~50µs | Memory iteration |
| Startup load | ~50ms | Load 100 plugins from DB |

## Error Handling

### Database Connection Loss

```go
// Registry operations return errors on DB failure
err := registry.SetState(ctx, pluginID, StateEnabled)
if err != nil {
    log.Printf("Failed to update state (DB unavailable): %v", err)
    // Fallback: memory-only operation (not persisted)
}
```

### Recovery from Inconsistency

```go
// Manual sync from database
if err := registry.Sync(ctx); err != nil {
    log.Printf("Failed to sync registry: %v", err)
}
```

## Migration from In-Memory Registry

### Step 1: Deploy Schema

```sql
-- Run migration
\i scripts/db_migrations/001_create_plugin_registry.sql
```

### Step 2: Migrate Existing Data

```go
// Load from file-based registry
oldRegistry := loadFileRegistry("/var/lib/llm-gateway/plugins.json")

// Write to database
newRegistry, _ := pluginruntime.NewDBRegistry(connStr, "")
for pluginID, entry := range oldRegistry {
    newRegistry.Register(ctx, pluginID, entry.Manifest, entry.State)
}
```

### Step 3: Update Gateway Code

```go
// Before: in-memory registry
// registry := pluginruntime.NewInMemoryRegistry()

// After: database-backed registry
registry, err := pluginruntime.NewDBRegistry(
    config.DatabaseURL,
    config.PluginRegistryFile,
)
```

## Troubleshooting

### Tests Skipped

```
=== RUN   TestNewDBRegistry
--- SKIP: TestNewDBRegistry (0.00s)
    db_registry_test.go:15: TEST_DB_URL not set, skipping database tests
```

**Solution**: Set `TEST_DB_URL` environment variable:
```bash
export TEST_DB_URL="postgres://postgres@localhost:5432/llm_gateway_test?sslmode=disable"
```

### Connection Refused

```
Failed to connect to database: dial tcp [::1]:5432: connect: connection refused
```

**Solution**: Start PostgreSQL or use Docker (see Testing section)

### Table Already Exists

The `initSchema()` uses `CREATE TABLE IF NOT EXISTS`, so it's safe to re-run.

## Security Considerations

1. **Connection String**: Never commit database credentials
   ```go
   // Bad: hardcoded password
   connStr := "postgres://user:[REDACTED:secret]@localhost/db"
   
   // Good: from environment
   connStr := os.Getenv("DATABASE_URL")
   ```

2. **SQL Injection**: All queries use parameterized statements (`$1`, `$2`, etc.)

3. **Manifest Content**: JSONB column can store arbitrary JSON - validate before use

## Future Enhancements

- [ ] File-based fallback when DB unavailable
- [ ] Automatic retry with exponential backoff
- [ ] Metrics: query latency, error rates
- [ ] Audit log: track who changed what state
- [ ] Bulk operations: enable/disable multiple plugins
- [ ] Transaction support for multi-plugin state changes

## See Also

- [Config Watcher](./config_watcher.go) - Configuration hot reload
- [Supervisor](./supervisor/supervisor.go) - Plugin process lifecycle
- [Extension Points](../extpoints/) - Plugin interface definitions
