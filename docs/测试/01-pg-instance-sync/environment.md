# PostgreSQL Instance Sync Test Environment

## Environment Overview

The test environment is designed to validate PostgreSQL instance synchronization without performing real database operations that could affect production systems.

## Test Environment Architecture

### Local Test Environment

**Components:**
- Mock database inventories using temporary files
- Policy configuration files for various scenarios
- Temporary manifest files with controlled content
- Library function mocks for database operations

**Safety Features:**
- No actual database connections during testing
- All operations use temporary files that are automatically cleaned up
- Mock functions replace real database calls
- Test isolation prevents interference between test runs

### Mock Database Scenarios

#### Scenario 1: Standard Mixed Environment
```
Local Databases:
- acc_db              (common, should sync)
- test_db             (excluded by policy)  
- llm_gateway         (protected, schema-only)
- smm_data            (aliased to remote 'smm')
- local_business      (local-only, allowlisted)

Remote Databases:  
- acc_db              (common, should sync)
- llm_gateway         (protected, schema-only)
- smm                 (alias target for 'smm_data')
- kxmemory           (remote-only, should bootstrap locally)
```

#### Scenario 2: LLM Gateway Protection Test
```
Local: llm_gateway (various modes for testing)
Remote: llm_gateway (various modes for testing)

Test Cases:
- SCHEMA_ONLY mode (allowed)
- SCHEMA_AND_INSERT_ONLY mode (blocked for data ops)
- Invalid configurations (should fail validation)
```

#### Scenario 3: Policy Edge Cases
```
Databases with edge case names:
- test_edge_case      (should be excluded)
- edge_test          (should be excluded)  
- llm_gateway_sync_meta (should be excluded)
- normal_db          (should be included)
```

## Configuration Files

### Test Policy Configurations

#### `test-policy-standard.conf`
Standard policy for most tests:
```bash
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES='smm_data=smm'
LOCAL_ONLY_ALLOWLIST='acc_swarm_db ai_alyy identity_shadow maintain_db local_business'
```

#### `test-policy-strict.conf`  
Stricter policy for safety testing:
```bash
EXCLUDE_DB_REGEX='(^|_)(test|temp|scratch|dev|stage)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway production_analytics'
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST=''
```

#### `test-policy-permissive.conf`
More permissive for edge case testing:
```bash
EXCLUDE_DB_REGEX='^llm_gateway_sync_'
SCHEMA_ONLY_DBS=''  
DB_ALIASES='old_name=new_name test_local=test_remote'
LOCAL_ONLY_ALLOWLIST='any_local_db'
```

### Environment Configurations

#### Mock Local Environment (`test-env-local.sh`)
```bash
# Mock local environment for testing
TARGET_TYPE="mock"
DOCKER_PG_CONTAINER="mock-local-pg"
PG_HOST="localhost"
PG_PORT="5432"
PG_USER="test_user"
PG_DB="test_db"
```

#### Mock Remote Environment (`test-env-remote.sh`)
```bash  
# Mock remote environment for testing
SSH_HOST="mock-remote"
SSH_PORT="22"
REMOTE_PG_CONTAINER="mock-remote-pg"
PG_HOST="127.0.0.1"
PG_PORT="15432"
PG_USER="test_user"
```

## Test Data Management

### Temporary File Structure

```
tmp/
├── local-inventory-{timestamp}.tsv
├── remote-inventory-{timestamp}.tsv  
├── test-manifest-{scenario}.tsv
├── test-policy-{variant}.conf
└── test-results-{timestamp}/
    ├── validation-output.log
    ├── error-scenarios.log
    └── performance-metrics.log
```

### Cleanup Strategy

**Automatic Cleanup:**
- All test files use `mktemp -d` for automatic cleanup
- Trap handlers ensure cleanup on exit/interrupt
- Test isolation prevents cross-contamination

**Manual Cleanup:**
```bash
# Clean all test artifacts
rm -rf tmp/test-*
rm -rf /tmp/pg-instance-sync-test-*
```

## Mock Function Implementation

### Database Inventory Mocking

```bash
# Mock get_database_list function
mock_get_database_list() {
    local env_type="$1"
    local output_file="$2"
    
    case "$env_type" in
        local)
            cat >"$output_file" <<'EOF'
acc_db
test_db
llm_gateway
smm_data
local_business
EOF
            ;;
        remote)
            cat >"$output_file" <<'EOF'
acc_db
llm_gateway
smm
kxmemory
EOF
            ;;
    esac
}
```

### Connection Mocking

```bash
# Mock database connection functions  
mock_psql_connection() {
    echo "Mock: Connected to $1 database"
    return 0
}

mock_ssh_tunnel() {
    echo "Mock: SSH tunnel established to $1"
    return 0  
}
```

## Test Isolation Techniques

### Process Isolation
- Each test runs in a separate bash process
- No shared state between test executions
- Independent temporary directories per test

### Data Isolation  
- All test data in temporary locations
- No modification of source configuration files
- Mock data prevents real database impact

### Environment Isolation
- Test-specific environment variables
- Mocked external dependencies  
- Controlled input/output paths

## Validation Environment

### Static Analysis
```bash
# ShellCheck validation
shellcheck scripts/pg-instance-sync.sh
shellcheck scripts/lib/pg-instance-*.sh
shellcheck scripts/test-pg-instance-*.sh
```

### Runtime Validation
```bash
# Bash strict mode for all scripts
set -euo pipefail

# Function tracing for debugging
set -x (when needed)

# Error handling validation
trap 'echo "Error on line $LINENO"' ERR
```

### Security Validation
- No real credentials in test files
- No network connections to production systems
- Read-only operations on actual configurations
- Temporary file permissions properly set

## CI/CD Integration

### Test Execution Pipeline

```bash
#!/bin/bash
# CI test pipeline
set -euo pipefail

# 1. Environment setup
source scripts/lib/test-setup.sh

# 2. Static analysis
echo "Running static analysis..."
shellcheck_results=$(shellcheck scripts/pg-instance-sync.sh scripts/lib/*.sh scripts/test-*.sh)

# 3. Unit tests  
echo "Running unit tests..."
for test in scripts/test-pg-instance-*.sh; do
    echo "Executing $test"
    bash "$test"
done

# 4. Integration tests
echo "Running integration tests..."
bash scripts/test-pg-instance-sync-integration.sh

# 5. Performance baseline
echo "Performance validation..."
time bash scripts/test-pg-instance-sync-commands.sh

# 6. Cleanup validation
echo "Cleanup verification..."
# Verify no test artifacts remain
find /tmp -name "*pg-instance-sync*" -mmin -5
```

### Test Reporting

**Success Criteria:**
- All unit tests pass
- All integration tests pass  
- No ShellCheck warnings
- No temporary files leaked
- Performance within acceptable bounds

**Failure Handling:**
- Detailed error logs preserved
- Test artifacts saved for analysis
- Clear failure point identification
- Rollback safety verification

## Performance Considerations

### Resource Usage
- Minimal memory footprint (temporary files only)
- No persistent storage requirements
- CPU usage limited to text processing
- Network usage: none (mocked connections)

### Scalability Testing
- Large inventory file handling (10k+ databases)
- Complex policy rule processing
- Large manifest generation and validation
- Concurrent test execution capability

### Benchmark Baselines
```bash
# Typical performance expectations
plan_generation_time < 5s    (for 1000 databases)
manifest_validation < 1s     (any size manifest)
policy_processing < 2s       (complex rules)
cli_response_time < 0.5s     (any command)
```