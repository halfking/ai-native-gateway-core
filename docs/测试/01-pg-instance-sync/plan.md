# PostgreSQL Instance Sync Testing Plan

## Test Scope

Testing the PostgreSQL instance synchronization tool for safe, controlled database sync operations between local and remote instances.

## Test Categories

### 1. Unit Tests (`test-pg-instance-sync-commands.sh`)

**Objective:** Validate CLI command parsing and basic functionality

**Test Cases:**
- Help command display
- Command validation and guardrails
- Backward compatibility with existing `plan` command
- Option parsing and validation

**Coverage:**
- All CLI commands: `inventory`, `plan`, `backup`, `apply-schema`, `apply-data`, `verify`, `all`
- Required flags validation: `--yes`, `--manifest-hash`, `--freshness-check`
- Invalid command handling

### 2. Guardrails Tests (`test-pg-instance-sync-guardrails.sh`)

**Objective:** Verify safety mechanisms and data protection

**Test Cases:**
- LLM Gateway data protection (permanent block on data operations)
- Write command safety requirements
- Manifest hash validation
- Command-specific option validation

**Coverage:**
- `llm_gateway` database protection for `apply-data` and `all` commands
- Hash freshness requirements for all write operations
- Safety flag requirements (`--yes`, `--manifest-hash`, `--freshness-check`)

### 3. Integration Tests (`test-pg-instance-sync-integration.sh`)

**Objective:** End-to-end validation with realistic scenarios

**Test Cases:**
- Valid schema operations on protected databases
- Invalid data operations (should fail)
- Manifest hash mismatch detection
- Complete workflow validation

**Coverage:**
- Guardrails library integration
- Policy file processing
- Manifest validation pipeline

### 4. Inventory Tests (`test-pg-instance-inventory.sh`)

**Objective:** Database discovery and inventory collection

**Test Cases:**
- Safety flag requirements
- Mock inventory generation
- Database list collection (mocked to avoid real DB operations)

**Coverage:**
- Inventory command validation
- Mock database list generation for testing

### 5. Library Function Tests

**Objective:** Individual function validation in isolation

#### `pg-instance-guardrails.sh`
- `validate_manifest_freshness()`: Hash verification and age checks
- `validate_llm_gateway_protection()`: LLM data operation blocking
- `check_destructive_operations()`: Destructive operation detection
- `validate_database_names()`: Name pattern validation

#### `pg-instance-inventory.sh`  
- `get_database_list()`: Database name collection
- `get_database_info()`: Extended database metadata
- Environment configuration sourcing

## Test Data

### Mock Database Lists

**Local Inventory:**
```
acc_db
test_db          # Should be excluded
llm_gateway      # Schema-only protected
smm_data         # Alias to remote 'smm'
local_only_db    # Local-only allowlist
```

**Remote Inventory:**
```
acc_db
llm_gateway      # Schema-only protected  
smm              # Alias from local 'smm_data'
remote_only_db   # Remote-only bootstrap
```

### Policy Configuration

```bash
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES='smm_data=smm'
LOCAL_ONLY_ALLOWLIST='acc_swarm_db ai_alyy identity_shadow maintain_db'
```

### Expected Classifications

| Database | Classification | Mode | Rationale |
|----------|---------------|------|-----------|
| acc_db | COMMON | SCHEMA_AND_INSERT_ONLY | Standard sync |
| test_db | EXCLUDED | NAME_POLICY | Matches exclusion regex |
| llm_gateway | COMMON | SCHEMA_ONLY | Protected production data |
| smm_data→smm | ALIAS | SCHEMA_AND_INSERT_ONLY | Name mapping |
| local_only_db | LOCAL_ONLY | CREATE_REMOTE_AND_INSERT_ONLY | Allowlist item |
| remote_only_db | REMOTE_ONLY | BOOTSTRAP_LOCAL_FULL | Missing locally |

## Risk Mitigation

### High-Risk Areas

1. **LLM Gateway Data Protection**
   - Risk: Accidental production data modification
   - Mitigation: Double validation in both policy and guardrails
   - Test: Explicit blocking of data operations

2. **Manifest Integrity**
   - Risk: Execution with stale or tampered manifests
   - Mitigation: SHA256 hash validation + freshness checks
   - Test: Hash mismatch detection

3. **Destructive Operations**
   - Risk: Unintended DROP operations
   - Mitigation: Separate destructive plan generation + manual confirmation
   - Test: Detection and blocking of destructive schema changes

### Test Isolation

- **No Real Database Operations**: All tests use mocks or read-only operations
- **Temporary Files**: All test artifacts in temporary directories with cleanup
- **Environment Isolation**: Tests do not modify production configurations

## Validation Criteria

### Pass Criteria

1. All CLI commands parse correctly and enforce safety requirements
2. LLM Gateway protection blocks data operations completely  
3. Manifest hash validation prevents execution with modified files
4. Policy-based exclusions work correctly
5. Database classification logic produces expected results
6. Library functions handle errors gracefully

### Fail Criteria

1. Any test allows prohibited operations on `llm_gateway` data
2. Write operations execute without required safety flags
3. Invalid manifests are accepted for execution
4. Excluded databases appear in sync plans
5. Library functions crash or produce incorrect results

## Test Execution

```bash
# Run all tests
bash scripts/test-pg-instance-sync.sh
bash scripts/test-pg-instance-sync-commands.sh  
bash scripts/test-pg-instance-sync-guardrails.sh
bash scripts/test-pg-instance-sync-integration.sh
bash scripts/test-pg-instance-inventory.sh

# Validate with shellcheck
shellcheck scripts/pg-instance-sync.sh
shellcheck scripts/lib/pg-instance-*.sh
shellcheck scripts/test-pg-instance-*.sh
```

## Future Test Enhancements

1. **Schema Analysis Tests**: Validate DDL parsing and destructive operation detection
2. **Data Sync Tests**: Mock data transfer with conflict resolution
3. **Performance Tests**: Large database list handling
4. **Error Recovery Tests**: Network failure, connection timeout scenarios
5. **Concurrency Tests**: Multiple sync operations, lock handling