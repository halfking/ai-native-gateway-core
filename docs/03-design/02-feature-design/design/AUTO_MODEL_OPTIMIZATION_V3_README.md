# AUTO_MODEL Optimization V3 - Implementation Progress

**Status**: Phase 1 & 2 Complete  
**Date**: 2026-09-02  
**Based On**: [AUTO_MODEL_OPTIMIZATION_V3_PLAN.md](./AUTO_MODEL_OPTIMIZATION_V3_PLAN.md)

---

## Quick Start

### Running Migrations

```bash
# Local development
cd sql
./run_migrations.sh local

# Development database (postgres-252)
./run_migrations.sh postgres-252

# Validate migrations
psql -h localhost -U postgres -d llm_gateway -f migrations/validate_v3_migrations.sql
```

### Rollback

```bash
# If you need to rollback migrations
cd sql
./rollback_migrations.sh local
```

---

## Implementation Status

### ✅ Phase 1: Database Foundation (Complete)

**Status**: Ready for testing

**Deliverables**:
- ✅ Migration scripts created and tested
- ✅ Rollback scripts created
- ✅ Validation queries created
- ✅ Automated migration runner script

**Files Created**:
```
sql/
├── migrations/
│   ├── 202609_01_add_request_type_fields.sql
│   ├── 202609_02_create_task_type_tier_config.sql
│   ├── 202609_03_add_tier_to_provider_models.sql
│   └── validate_v3_migrations.sql
├── rollback/
│   ├── 202609_01_rollback_request_type_fields.sql
│   ├── 202609_02_rollback_task_type_tier_config.sql
│   └── 202609_03_rollback_tier_to_provider_models.sql
├── run_migrations.sh
└── rollback_migrations.sh
```

**Schema Changes**:

1. **request_logs table** - New columns:
   - `request_type` (TEXT): 'client' or 'outbound'
   - `parent_request_id` (TEXT): Links child requests to parent
   - `request_depth` (INTEGER): Agent depth (0=client, 1=sub-agent, 2+=nested)
   - `is_terminal` (BOOLEAN): Final successful/failed attempt
   - `task_tier` (TEXT): 'tier-a', 'tier-b', or 'tier-c'
   - `auto_decision` (JSONB): Full decision metadata
   - `escalation_count` (INTEGER): Number of tier escalations

2. **task_type_tier_config table** - New table:
   - Maps task types to preferred tiers
   - Supports tenant-specific overrides
   - 10 default configurations (3 tier-a, 3 tier-b, 4 tier-c)

3. **provider_models table** - New column:
   - `tier` (TEXT): Model tier classification

**Next Steps**:
- [ ] Apply migrations to postgres-252 dev database
- [ ] Validate schema changes with production-like data
- [ ] Update provider_models tier classification based on actual canonical names

---

### ✅ Phase 2: Enhanced Classification (Complete)

**Status**: Code complete, needs testing

**Deliverables**:
- ✅ 10-category task type system
- ✅ V3 keyword sets with strong/weak signals
- ✅ Enhanced classifier implementation
- ✅ Tier selector with database integration
- ✅ Feature flag support for gradual rollout

**Files Created**:
```
autoroute/
├── task_types_v3.go          # 10-category definitions & keywords
├── classifier_v3.go           # V3 classification algorithm
└── tier_selector.go           # Tier determination logic
```

**New Task Categories**:

| Category | Tier | Description | Key Signals |
|----------|------|-------------|-------------|
| **architecture** | A | System design, API design | "system design", "architecture", "design doc" |
| **audit** | A | Code review, security audit | "review", "audit", "security", "vulnerability" |
| **debugging** | A | Bug investigation, stack traces | "debug", "error", "stack trace", "doesn't work" |
| **coding** | B | Feature implementation | Code blocks + "implement", "create", "build" |
| **refactoring** | B | Code restructuring | "refactor", "optimize", "improve", "clean up" |
| **testing** | B | Test generation | "test", "coverage", "mock", "assert" |
| **devops** | C | CI/CD, deployment | "deploy", "docker", "k8s", "terraform" |
| **documentation** | C | Comments, README | "document", "comment", "explain", "README" |
| **summary** | C | Code summarization | "summarize", "overview", "recap", "TLDR" |
| **dependency** | C | Package management | "dependency", "upgrade", "package", "version" |

**Classification Algorithm**:

```
Phase 1: Hard Overrides (confidence 0.90-0.95)
├─ Vision (HasImages) → TaskVision
├─ Strong Coding Signals (code blocks, IDE, plan mode) → TaskCoding
└─ Long Context (> 50k tokens) → TaskLongContext

Phase 2: Specialized Detection (confidence 0.80-0.90)
├─ Architecture (system design keywords)
├─ Audit (code review keywords)
└─ Debugging (error patterns + keywords)

Phase 3: Tool-Based Dispatch
├─ Agent (>= 3 tools + tool results)
└─ Function Call (1-2 tools)

Phase 4: Keyword Scoring (all remaining categories)
└─ Score: 0.35 per keyword hit (max 1.0)

Phase 5: Winner Selection & Confidence Check
└─ Priority: architecture > audit > debugging > coding > ...
```

**Tier Selection Flow**:

```
Priority 1: Header Override (X-Gw-Model-Tier)
├─ Explicit user/client control
└─ Highest priority

Priority 2: Depth-Based Degradation
├─ Depth 0-1: Normal tier selection
└─ Depth >= 2: Force tier-c (nested sub-agents)

Priority 3: Tenant Override
└─ task_type_tier_config WHERE tenant_id = ?

Priority 4: Global Config
└─ task_type_tier_config WHERE tenant_id IS NULL

Priority 5: In-Memory Default
└─ TaskTypeTierMapping fallback
```

**Next Steps**:
- [ ] Create unit tests for V3 classifier
- [ ] Create unit tests for tier selector
- [ ] Integration tests with sample requests
- [ ] Tune keyword weights based on test results

---

### ⏳ Phase 3: Tier-Based Routing (Pending)

**Status**: Not started

**Tasks**:
- [ ] Update `autoroute/recommend_v2.go`:
  - [ ] Filter candidates by tier
  - [ ] Implement fallback tier logic
  - [ ] Add tier to credential_model_index query
- [ ] Update `autoroute/scoring.go`:
  - [ ] Add tier matching bonus to scoring
  - [ ] Adjust weights for tier preference
- [ ] Update `autoroute/decision.go`:
  - [ ] Integrate V3 classifier
  - [ ] Integrate tier selector
  - [ ] Store tier in auto_decision JSONB
  - [ ] Add X-Gw-Task-Tier response header

**Expected Deliverables**:
- Modified recommendation engine
- Modified scoring engine
- Integration with V3 classifier & tier selector
- Feature flag: `auto_v3_tier_routing`

---

### ⏳ Phase 4: Entry/Exit Request Separation (Pending)

**Status**: Not started

**Tasks**:
- [ ] Update `domains/streaming/handler.go`:
  - [ ] Create client entry log on admission
  - [ ] Extract X-Gw-Agent-Depth header
  - [ ] Store auto_decision metadata
- [ ] Update `domains/dispatch/pipeline.go`:
  - [ ] Create outbound log on dispatch
  - [ ] Link via parent_request_id
  - [ ] Mark is_terminal on completion
- [ ] Update telemetry hooks:
  - [ ] Support dual-write pattern
  - [ ] Handle request_type distinction

**Expected Deliverables**:
- Dual logging implementation
- Parent-child request linking
- Feature flag: `auto_v3_request_separation`

---

### ⏳ Phase 5-8: Queue Architecture, Quality Gates, Monitoring, Validation (Pending)

See [AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md](./AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md) for detailed plans.

---

## Feature Flags

All V3 functionality is controlled by feature flags for safe deployment:

```go
// Feature flags (to be implemented in feature flag system)
const (
    FeatureEnhancedClassification = "auto_v3_enhanced_classification"
    FeatureTierSelection         = "auto_v3_tier_selection"
    FeatureTierBasedRouting      = "auto_v3_tier_routing"
    FeatureRequestTypeSeparation = "auto_v3_request_separation"
    FeatureDepthTracking         = "auto_v3_depth_tracking"
    FeatureClientQueue           = "auto_v3_client_queue"
    FeatureTierQueueSegmentation = "auto_v3_tier_queue_segmentation"
    FeatureQualityGates          = "auto_v3_quality_gates"
    FeatureAutoEscalation        = "auto_v3_auto_escalation"
)
```

**Deployment Strategy**:
1. Phase 1-2: Enable in shadow mode (dual-write, no routing changes)
2. Collect 7 days of shadow data
3. Validate classification accuracy >= 85%
4. Phase 3: Enable tier routing for 10% traffic
5. Monitor for 48 hours, ramp to 50%, then 100%

---

## Testing Strategy

### Unit Tests (To Be Created)

```bash
# Test V3 classifier
go test -v ./autoroute -run TestV3Classifier

# Test tier selector
go test -v ./autoroute -run TestTierSelector

# Test keyword matching
go test -v ./autoroute -run TestV3Keywords
```

### Integration Tests (To Be Created)

```bash
# Test end-to-end classification + tier selection
go test -v ./autoroute -run TestV3Integration

# Test with sample production requests
go test -v ./autoroute -run TestV3Production
```

### Manual Validation

```bash
# Query classification accuracy
psql -h postgres-252 -U postgres -d llm_gateway << EOF
SELECT 
  task_type,
  task_tier,
  COUNT(*) as count,
  AVG(auto_decision->>'confidence') as avg_confidence
FROM request_logs
WHERE request_type = 'client'
  AND ts >= NOW() - INTERVAL '24 hours'
  AND auto_decision IS NOT NULL
GROUP BY task_type, task_tier
ORDER BY count DESC;
EOF
```

---

## Cost Projection

**Current State** (No tier optimization):
```
100% requests × $20/1M avg = $20 per 1M tokens
```

**Optimized State** (V3 tier routing):
```
20% × $30/1M (Tier-A) = $6.00
40% × $10/1M (Tier-B) = $4.00
40% × $2/1M  (Tier-C) = $0.80
─────────────────────────
Total = $10.80 per 1M tokens
```

**Expected Savings**: 46% (conservative: 35-40% after escalations)

---

## Monitoring Queries

### Tier Distribution

```sql
SELECT 
  task_tier,
  COUNT(*) as count,
  ROUND(COUNT(*)::NUMERIC / SUM(COUNT(*)) OVER () * 100, 2) as pct
FROM request_logs
WHERE is_terminal = TRUE 
  AND ts >= NOW() - INTERVAL '24 hours'
  AND task_tier IS NOT NULL
GROUP BY task_tier
ORDER BY task_tier;
```

### Classification Accuracy

```sql
SELECT 
  task_type,
  COUNT(*) as count,
  AVG((auto_decision->>'confidence')::NUMERIC) as avg_confidence,
  MIN((auto_decision->>'confidence')::NUMERIC) as min_confidence
FROM request_logs
WHERE request_type = 'client'
  AND ts >= NOW() - INTERVAL '24 hours'
  AND auto_decision IS NOT NULL
GROUP BY task_type
ORDER BY count DESC;
```

### Cost per Tier

```sql
SELECT 
  task_tier,
  COUNT(*) as requests,
  SUM(total_tokens)::BIGINT as total_tokens,
  ROUND(SUM(total_tokens * unit_price_in / 1000000)::NUMERIC, 2) as cost_usd
FROM request_logs
WHERE is_terminal = TRUE
  AND ts >= NOW() - INTERVAL '24 hours'
  AND task_tier IS NOT NULL
GROUP BY task_tier
ORDER BY task_tier;
```

---

## Known Issues / TODOs

- [ ] Provider model tier classification needs to be updated with actual canonical names
- [ ] Unit tests for V3 classifier and tier selector
- [ ] Integration tests with sample requests
- [ ] Feature flag system integration
- [ ] Prometheus metrics for V3 classification
- [ ] Grafana dashboard for tier analytics
- [ ] Documentation for X-Gw-Agent-Depth header usage
- [ ] Documentation for X-Gw-Model-Tier header usage

---

## References

- [AUTO_MODEL_OPTIMIZATION_V3_PLAN.md](./AUTO_MODEL_OPTIMIZATION_V3_PLAN.md) - Comprehensive design
- [AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md](./AUTO_MODEL_OPTIMIZATION_V3_EXECUTION.md) - Execution tracking
- Original AUTO_MODEL implementation: `autoroute/classifier.go`
- Credential model index: `autoroute/index.go`

---

## Contact

For questions or issues with V3 implementation:
- Design lead: [Owner]
- Implementation: [Team]
- Review: Phase 1-2 complete, ready for testing

**Next Milestone**: Apply migrations to postgres-252 and begin Phase 3 (Tier-Based Routing)
