# PostgreSQL Instance Sync Test Cases

## Test Case Catalog

### TC-001: CLI Command Parsing

**Objective:** Verify all CLI commands are recognized and parsed correctly

**Preconditions:**
- `pg-instance-sync.sh` script is executable
- Test environment is clean

**Test Steps:**
1. Execute `--help` flag
2. Test each command: `inventory`, `plan`, `backup`, `apply-schema`, `apply-data`, `verify`, `all`
3. Test invalid commands
4. Test option parsing for each command

**Expected Results:**
- Help displays usage information
- Valid commands are accepted
- Invalid commands show error message
- Required options are enforced

**Validation:**
```bash
pg-instance-sync.sh --help | grep -q "Usage:"
pg-instance-sync.sh invalid-command 2>&1 | grep -q "unsupported command"
```

### TC-002: LLM Gateway Data Protection

**Objective:** Ensure llm_gateway databases are protected from data modifications

**Preconditions:**
- Manifest contains llm_gateway database entries
- Valid manifest hash available

**Test Steps:**
1. Create manifest with llm_gateway in SCHEMA_ONLY mode
2. Attempt `apply-data` operation  
3. Attempt `all` operation with llm_gateway data modes
4. Verify schema operations are allowed

**Expected Results:**
- `apply-data` is blocked for llm_gateway databases
- `all` command is blocked when llm_gateway has data sync modes
- `apply-schema` is allowed for llm_gateway with SCHEMA_ONLY mode

**Validation:**
```bash
# Should fail
pg-instance-sync.sh apply-data --yes --manifest manifest.tsv --manifest-hash $HASH --freshness-check

# Should succeed  
pg-instance-sync.sh apply-schema --yes --manifest manifest.tsv --manifest-hash $HASH --freshness-check
```

### TC-003: Manifest Hash Validation

**Objective:** Verify manifest integrity through hash validation

**Preconditions:**
- Valid manifest file exists
- Correct hash calculated

**Test Steps:**
1. Calculate correct hash for manifest
2. Attempt operation with correct hash
3. Attempt operation with incorrect hash
4. Modify manifest and retry with original hash

**Expected Results:**
- Operations with correct hash succeed validation
- Operations with incorrect hash fail immediately
- Modified manifests are detected

**Validation:**
```bash
CORRECT_HASH=$(shasum -a 256 manifest.tsv | awk '{print $1}')
WRONG_HASH="incorrect_hash_value"

# Should succeed
pg-instance-sync.sh verify --manifest manifest.tsv --manifest-hash $CORRECT_HASH

# Should fail
pg-instance-sync.sh verify --manifest manifest.tsv --manifest-hash $WRONG_HASH
```

### TC-004: Safety Flag Requirements

**Objective:** Ensure all write operations require explicit confirmation

**Preconditions:**
- Valid manifest and hash available

**Test Steps:**
1. Attempt each write command without `--yes` flag
2. Attempt write commands without `--manifest-hash`
3. Attempt write commands without `--freshness-check`
4. Test read-only commands without flags

**Expected Results:**
- Write commands fail without `--yes`
- Write commands fail without `--manifest-hash`
- Write commands fail without `--freshness-check`
- Read-only commands work without write flags

**Validation:**
```bash
# All should fail
pg-instance-sync.sh apply-schema
pg-instance-sync.sh apply-data --yes  
pg-instance-sync.sh backup --yes --manifest-hash $HASH

# Should succeed
pg-instance-sync.sh plan --local-inventory local.tsv --remote-inventory remote.tsv --policy policy.conf --manifest manifest.tsv
```

### TC-005: Database Classification Logic

**Objective:** Verify correct database classification based on policy

**Preconditions:**
- Mock local and remote inventories
- Policy configuration with exclusion rules and aliases

**Test Steps:**
1. Create test inventories with various database types
2. Run plan command with test policy
3. Examine resulting manifest classifications

**Expected Results:**
- Common databases classified as COMMON
- Excluded databases marked as EXCLUDED
- Local-only databases marked as LOCAL_ONLY
- Remote-only databases marked as REMOTE_ONLY
- Aliases correctly mapped

**Test Data:**
```
Local: acc_db, test_db, llm_gateway, smm_data, local_only_db
Remote: acc_db, llm_gateway, smm, remote_only_db
Policy: EXCLUDE_DB_REGEX='.*test.*', DB_ALIASES='smm_data=smm'
```

**Expected Classifications:**
```
COMMON      acc_db          acc_db          SCHEMA_AND_INSERT_ONLY
EXCLUDED    test_db         -               NAME_POLICY  
COMMON      llm_gateway     llm_gateway     SCHEMA_ONLY
ALIAS       smm_data        smm             SCHEMA_AND_INSERT_ONLY
LOCAL_ONLY  local_only_db   -               CREATE_REMOTE_AND_INSERT_ONLY
REMOTE_ONLY -               remote_only_db  BOOTSTRAP_LOCAL_FULL
```

### TC-006: Inventory Collection Safety

**Objective:** Ensure inventory collection requires explicit confirmation

**Preconditions:**
- Database instances are available (or mocked)

**Test Steps:**
1. Attempt inventory command without `--yes` flag
2. Execute inventory command with `--yes` flag
3. Verify output files are created correctly

**Expected Results:**
- Inventory fails without `--yes` flag
- With `--yes` flag, inventory collects database lists
- Output files contain expected database names

**Validation:**
```bash
# Should fail
pg-instance-sync.sh inventory 2>&1 | grep -q "requires --yes"

# Should succeed (with mocked databases)
pg-instance-sync.sh inventory --yes
```

### TC-007: Policy File Processing

**Objective:** Verify policy configuration is loaded and applied correctly

**Preconditions:**
- Valid policy configuration file
- Test database inventories

**Test Steps:**
1. Create policy with specific exclusion patterns
2. Create inventories with matching and non-matching databases
3. Run plan command and examine classifications

**Expected Results:**
- Databases matching exclusion regex are excluded
- Schema-only databases are marked correctly
- Aliases are processed properly
- Unknown policy options are handled gracefully

### TC-008: Manifest Freshness Validation

**Objective:** Ensure old manifests trigger warnings or blocks

**Preconditions:**
- Manifest file with known age

**Test Steps:**
1. Create manifest file
2. Modify timestamp to simulate old file
3. Attempt operations with aged manifest

**Expected Results:**
- Manifests older than threshold trigger warnings
- Extremely old manifests may be rejected
- Fresh manifests are accepted

### TC-009: Destructive Operation Detection

**Objective:** Identify and flag potentially destructive schema changes

**Preconditions:**
- Schema comparison capabilities (future enhancement)

**Test Steps:**
1. Simulate schema differences requiring DROP operations
2. Run schema analysis
3. Verify destructive operations are flagged

**Expected Results:**
- DROP TABLE/INDEX/CONSTRAINT operations are detected
- Non-destructive changes are allowed
- Destructive plan is generated separately

**Status:** Future enhancement - detection logic not yet implemented

### TC-010: Error Handling and Recovery

**Objective:** Verify graceful handling of error conditions

**Preconditions:**
- Various error scenarios prepared

**Test Steps:**
1. Test with missing files
2. Test with unreadable files  
3. Test with malformed manifests
4. Test with network connectivity issues

**Expected Results:**
- Clear error messages for each failure type
- No partial state changes on errors
- Proper cleanup of temporary files

## Test Execution Matrix

| Test Case | Command | Safety Check | Expected Result |
|-----------|---------|--------------|-----------------|
| TC-001 | All | CLI Parsing | Pass/Fail per command |
| TC-002 | apply-data | LLM Protection | Fail (blocked) |
| TC-002 | apply-schema | LLM Protection | Pass (allowed) |
| TC-003 | All writes | Hash Validation | Fail on mismatch |
| TC-004 | All writes | Safety Flags | Fail without flags |
| TC-005 | plan | Classification | Correct categories |
| TC-006 | inventory | Safety Flag | Fail without --yes |
| TC-007 | plan | Policy Processing | Correct application |
| TC-008 | All writes | Freshness | Warning on old files |
| TC-009 | apply-schema | Destructive Ops | Detection/flagging |
| TC-010 | All | Error Handling | Graceful failures |

## Automated Test Execution

```bash
#!/bin/bash
# Run all test cases
set -euo pipefail

echo "=== PostgreSQL Instance Sync Test Suite ==="

test_files=(
    "test-pg-instance-sync.sh"
    "test-pg-instance-sync-commands.sh" 
    "test-pg-instance-sync-guardrails.sh"
    "test-pg-instance-sync-integration.sh"
    "test-pg-instance-inventory.sh"
)

failed_tests=()

for test in "${test_files[@]}"; do
    echo "Running $test..."
    if bash "scripts/$test"; then
        echo "✓ $test PASSED"
    else
        echo "✗ $test FAILED"
        failed_tests+=("$test")
    fi
    echo
done

if [[ ${#failed_tests[@]} -eq 0 ]]; then
    echo "🎉 All tests passed!"
    exit 0
else
    echo "❌ Failed tests: ${failed_tests[*]}"
    exit 1
fi
```

## Test Coverage Goals

- **CLI Coverage:** 100% of commands and options
- **Safety Coverage:** 100% of guardrails and protections  
- **Policy Coverage:** All configuration options
- **Error Coverage:** Major failure modes
- **Integration Coverage:** End-to-end workflows